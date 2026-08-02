package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/observability"
	"ghrouter/internal/types"
)

// ProviderRunner executes a single request against a CLI provider
type ProviderRunner struct {
	prov   *types.Provider
	health *ProviderHealth
}

type EmptyResponseError struct {
	Provider string
}

func (e *EmptyResponseError) Error() string {
	if e.Provider == "" {
		return "provider returned an empty response"
	}
	return fmt.Sprintf("provider %s returned an empty response", e.Provider)
}

type ProviderHealth struct {
	mu        sync.RWMutex
	Status    string
	LastCheck time.Time
	Latency   time.Duration
	Error     error
	Available bool
}

func NewProviderRunner(p *types.Provider) *ProviderRunner {
	return &ProviderRunner{
		prov: p,
		health: &ProviderHealth{
			Status:    "unknown",
			Available: true,
		},
	}
}

// Invoke runs a single request and streams responses
func (r *ProviderRunner) Invoke(ctx context.Context, req *types.OpenAIRequest) (<-chan *StreamEvent, <-chan error) {
	eventCh := make(chan *StreamEvent, 10)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)

		start := time.Now()
		log := observability.Logger("provider").With("provider", r.prov.Name, "model", req.Model, "request_id", req.RequestID)
		log.Debug("provider_request_started")
		err := r.executeCLI(ctx, req, eventCh)
		latency := time.Since(start)

		r.health.mu.Lock()
		r.health.LastCheck = time.Now()
		r.health.Latency = latency
		if err != nil {
			log.Error("provider_request_failed", "error", observability.PublicError(err), "error_type", observability.ErrorType(err), observability.Since(start))
			r.health.Status = "error"
			r.health.Error = err
			errCh <- err
		} else {
			log.Info("provider_request_completed", observability.Since(start))
			r.health.Status = "healthy"
			r.health.Error = nil
			r.health.Available = true
			eventCh <- &StreamEvent{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Model: req.Model, Done: true}
		}
		r.health.mu.Unlock()
	}()

	return eventCh, errCh
}

// StreamEvent represents a streaming response chunk
type StreamEvent struct {
	ID        string
	Model     string
	Delta     string
	ToolCalls []types.OpenAIToolCall
	Done      bool
	Error     error
}

func (r *ProviderRunner) executeCLI(ctx context.Context, req *types.OpenAIRequest, eventCh chan<- *StreamEvent) error {
	tries := r.prov.Retries + 1
	if tries < 1 {
		tries = 1
	}
	backoff := r.prov.RetryBackoff
	if backoff <= 0 {
		backoff = 150 * time.Millisecond
	}
	for attempt := 0; attempt < tries; attempt++ {
		emitted := false
		err := r.executeCLIOnce(ctx, req, eventCh, &emitted)
		if err == nil || emitted || attempt == tries-1 {
			return err
		}
		timer := time.NewTimer(backoff * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (r *ProviderRunner) executeCLIOnce(ctx context.Context, req *types.OpenAIRequest, eventCh chan<- *StreamEvent, emitted *bool) error {
	commandCtx := ctx
	if r.prov.Timeout > 0 {
		var cancel context.CancelFunc
		commandCtx, cancel = context.WithTimeout(ctx, r.prov.Timeout)
		defer cancel()
	}
	if strings.TrimSpace(r.prov.BaseURL) != "" {
		return r.executeHTTP(commandCtx, req, eventCh, emitted)
	}
	// Build prompt from messages
	prompt := r.buildPrompt(req)

	adapter := adapterFor(r.prov.Type)
	args := adapter.BuildArgs(r.prov, req.Model)
	if adapter.PromptOnArgs() {
		args = append(args, prompt)
	}

	cmd := exec.CommandContext(commandCtx, r.prov.CLIPath, args...)
	prepareProviderCommand(cmd)
	cmd.Dir = r.prov.WorkDir
	cmd.Env = r.buildEnv()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	processDone := make(chan struct{})
	defer close(processDone)
	go func() {
		select {
		case <-commandCtx.Done():
			killProviderProcess(cmd)
		case <-processDone:
		}
	}()

	// Write prompt to stdin
	if r.prov.Type == types.ProviderCursor {
		_ = stdin.Close()
	} else {
		go func() {
			defer stdin.Close()
			_, _ = stdin.Write([]byte(prompt))
		}()
	}

	stderrDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- data
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var parseErr error
	for scanner.Scan() {
		meaningful, err := r.parseLineAndMaybeEmit(scanner.Text(), eventCh, req.Model, false)
		if meaningful {
			*emitted = true
		}
		if err != nil {
			parseErr = err
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return err
	}

	if err := cmd.Wait(); err != nil {
		if commandErr := commandCtx.Err(); commandErr != nil {
			return commandErr
		}
		return fmt.Errorf("CLI exited with error: %w, stderr: %s", err, string(<-stderrDone))
	}
	<-stderrDone
	if commandErr := commandCtx.Err(); commandErr != nil {
		return commandErr
	}
	if parseErr != nil {
		return parseErr
	}
	if !*emitted {
		return &EmptyResponseError{Provider: r.prov.Name}
	}
	return nil
}

func buildCLIArgs(provider *types.Provider, requestedModel string) []string {
	if provider == nil {
		return nil
	}
	return adapterFor(provider.Type).BuildArgs(provider, requestedModel)
}

func hasModelFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-m" || arg == "--model" || strings.HasPrefix(arg, "--model=") {
			return true
		}
	}
	return false
}

func (r *ProviderRunner) buildPrompt(req *types.OpenAIRequest) string {
	var parts []string
	for _, msg := range req.Messages {
		role := msg.Role
		content := ""
		switch v := msg.Content.(type) {
		case string:
			content = v
		case []interface{}:
			for _, part := range v {
				if m, ok := part.(map[string]interface{}); ok {
					if m["type"] == "text" {
						content += m["text"].(string)
					}
				}
			}
		}
		if content != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", role, content))
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func (r *ProviderRunner) buildEnv() []string {
	env := make([]string, 0, len(r.prov.Env)+len(os.Environ()))
	for k, v := range r.prov.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && isRouterClientEnv(key) {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func isRouterClientEnv(key string) bool {
	switch key {
	case "GHR_ACCESS_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_PROVIDER_HEADERS", "COPILOT_PROVIDER_WIRE_API", "COPILOT_PROVIDER_WIRE_MODEL", "CURSOR_API_ENDPOINT", "CURSOR_API_KEY":
		return true
	default:
		return false
	}
}

func (r *ProviderRunner) parseLineAndEmit(line string, ch chan<- *StreamEvent, model string) {
	_, _ = r.parseLineAndMaybeEmit(line, ch, model, true)
}

func (r *ProviderRunner) parseLineAndMaybeEmit(line string, ch chan<- *StreamEvent, model string, emitErrors bool) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, nil
	}
	event := StreamEvent{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Model: model}
	var jsonMsg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &jsonMsg); err == nil {
		if message := structuredError(jsonMsg); message != "" {
			event.Error = fmt.Errorf("provider reported error: %s", message)
			if emitErrors {
				ch <- &event
			}
			return false, event.Error
		}
		if text := responseText(jsonMsg); text != "" {
			event.Delta = text
			ch <- &event
			return true, nil
		}
		event.ToolCalls = parseToolCalls(jsonMsg)
		if content, ok := jsonMsg["content"].(string); ok {
			event.Delta = content
			ch <- &event
			return content != "", nil
		}
		if text, ok := jsonMsg["text"].(string); ok {
			event.Delta = text
			ch <- &event
			return text != "", nil
		}
		if len(event.ToolCalls) > 0 {
			ch <- &event
			return true, nil
		}
		if done, ok := jsonMsg["done"].(bool); ok && done {
			event.Done = true
			ch <- &event
			return false, nil
		}
	}
	if !strings.HasPrefix(line, "{") {
		event.Delta = line + "\n"
		ch <- &event
		return true, nil
	}
	return false, nil
}

func responseText(message map[string]interface{}) string {
	for _, key := range []string{"delta", "text", "content"} {
		if text := contentText(message[key]); text != "" {
			return text
		}
	}
	if nested, ok := message["assistantMessageEvent"].(map[string]interface{}); ok {
		if text := responseText(nested); text != "" {
			return text
		}
	}
	if nested, ok := message["message"].(map[string]interface{}); ok {
		if role, _ := nested["role"].(string); role == "assistant" {
			return contentText(nested["content"])
		}
	}
	if nested, ok := message["item"].(map[string]interface{}); ok {
		if itemType, _ := nested["type"].(string); itemType == "agent_message" {
			return contentText(nested["text"])
		}
	}
	if nested, ok := message["part"].(map[string]interface{}); ok {
		if partType, _ := nested["type"].(string); partType == "text" {
			return contentText(nested["text"])
		}
	}
	return ""
}

func contentText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []interface{}:
		var parts []string
		for _, item := range value {
			if block, ok := item.(map[string]interface{}); ok {
				if text := contentText(block["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	case map[string]interface{}:
		return contentText(value["text"])
	default:
		return ""
	}
}

func structuredError(message map[string]interface{}) string {
	for _, key := range []string{"error", "errorMessage", "error_message"} {
		if value, ok := message[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	if nested, ok := message["message"].(map[string]interface{}); ok {
		if value := structuredError(nested); value != "" {
			return value
		}
		if reason, _ := nested["stopReason"].(string); reason == "error" {
			return "assistant message stopped with error"
		}
	}
	return ""
}

func parseToolCalls(message map[string]interface{}) []types.OpenAIToolCall {
	for _, key := range []string{"tool_calls", "toolCalls"} {
		if raw, ok := message[key]; ok {
			data, err := json.Marshal(raw)
			if err != nil {
				continue
			}
			var calls []types.OpenAIToolCall
			if json.Unmarshal(data, &calls) == nil && len(calls) > 0 {
				return calls
			}
		}
	}
	if raw, ok := message["choices"].([]interface{}); ok && len(raw) > 0 {
		if choice, ok := raw[0].(map[string]interface{}); ok {
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				return parseToolCalls(delta)
			}
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				return parseToolCalls(msg)
			}
		}
	}
	return nil
}

func (r *ProviderRunner) GetHealth() *ProviderHealth {
	r.health.mu.RLock()
	defer r.health.mu.RUnlock()
	return &ProviderHealth{
		Status:    r.health.Status,
		LastCheck: r.health.LastCheck,
		Latency:   r.health.Latency,
		Error:     r.health.Error,
		Available: r.health.Available,
	}
}

func (r *ProviderRunner) GetName() string {
	return r.prov.Name
}

func (r *ProviderRunner) GetModels() []string {
	return append([]string(nil), r.prov.Models...)
}

func (r *ProviderRunner) HealthCheck(ctx context.Context) error {
	if len(r.prov.Models) == 0 {
		return fmt.Errorf("provider %s has no configured models", r.prov.Name)
	}
	if strings.TrimSpace(r.prov.BaseURL) != "" {
		return healthCheckHTTP(ctx, r.prov.BaseURL)
	}
	if strings.TrimSpace(r.prov.CLIPath) == "" {
		return fmt.Errorf("provider %s has no CLI path", r.prov.Name)
	}
	if _, err := os.Stat(r.prov.CLIPath); err != nil {
		return fmt.Errorf("provider %s CLI unavailable", r.prov.Name)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
