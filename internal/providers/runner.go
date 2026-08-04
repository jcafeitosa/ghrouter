package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/observability"
	"ghrouter/internal/types"
)

// ProviderRunner executes a single request against a CLI provider
type ProviderRunner struct {
	prov          *types.Provider
	health        *ProviderHealth
	circuit       *circuitState
	acpPool       *genericACPWarmPool
	localHTTPGate chan struct{}
}

var localHTTPGates sync.Map

func localHTTPGateFor(baseURL string) chan struct{} {
	key := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	gate, _ := localHTTPGates.LoadOrStore(key, make(chan struct{}, 1))
	return gate.(chan struct{})
}

func AcquireLocalHTTP(ctx context.Context, baseURL string) (func(), error) {
	if strings.TrimSpace(baseURL) == "" {
		return func() {}, nil
	}
	gate := localHTTPGateFor(baseURL)
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

type CircuitOpenError struct {
	Provider string
	RetryAt  time.Time
}

func (e *CircuitOpenError) Error() string {
	if e == nil || e.Provider == "" {
		return "provider circuit is open"
	}
	return fmt.Sprintf("provider %s circuit is open", e.Provider)
}

type CircuitPolicy struct {
	Enabled          bool
	FailureThreshold int
	OpenDuration     time.Duration
}

type circuitState struct {
	mu          sync.Mutex
	policy      CircuitPolicy
	failures    int
	openedAt    time.Time
	probeActive bool
}

func NewProviderRunner(p *types.Provider) *ProviderRunner {
	runner := &ProviderRunner{
		prov:    p,
		circuit: newCircuitState(),
		acpPool: &genericACPWarmPool{},
		health: &ProviderHealth{
			Status:    "unknown",
			Available: true,
		},
	}
	if p != nil && p.Type == types.ProviderLocal && strings.TrimSpace(p.BaseURL) != "" {
		runner.localHTTPGate = localHTTPGateFor(p.BaseURL)
	}
	return runner
}

func (r *ProviderRunner) acquireLocalHTTP(ctx context.Context) (func(), error) {
	if r == nil || r.localHTTPGate == nil {
		return func() {}, nil
	}
	select {
	case r.localHTTPGate <- struct{}{}:
		return func() { <-r.localHTTPGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *ProviderRunner) Close() {
	if r != nil && r.acpPool != nil {
		r.acpPool.Close()
	}
}

func newCircuitState() *circuitState {
	return &circuitState{policy: CircuitPolicy{Enabled: true, FailureThreshold: 3, OpenDuration: 30 * time.Second}}
}

func (r *ProviderRunner) SetCircuitPolicy(policy CircuitPolicy) {
	if r == nil || r.circuit == nil {
		return
	}
	if policy.FailureThreshold <= 0 {
		policy.FailureThreshold = 3
	}
	if policy.OpenDuration <= 0 {
		policy.OpenDuration = 30 * time.Second
	}
	r.circuit.mu.Lock()
	r.circuit.policy = policy
	if !policy.Enabled {
		r.circuit.failures = 0
		r.circuit.openedAt = time.Time{}
		r.circuit.probeActive = false
	}
	r.circuit.mu.Unlock()
}

func (r *ProviderRunner) allowCircuitRequest() (*CircuitOpenError, bool) {
	if r == nil || r.circuit == nil {
		return nil, true
	}
	now := time.Now()
	r.circuit.mu.Lock()
	defer r.circuit.mu.Unlock()
	if !r.circuit.policy.Enabled || r.circuit.openedAt.IsZero() {
		return nil, true
	}
	if now.Sub(r.circuit.openedAt) < r.circuit.policy.OpenDuration || r.circuit.probeActive {
		return &CircuitOpenError{Provider: r.prov.Name, RetryAt: r.circuit.openedAt.Add(r.circuit.policy.OpenDuration)}, false
	}
	r.circuit.probeActive = true
	return nil, true
}

func (r *ProviderRunner) recordCircuitSuccess() {
	if r == nil || r.circuit == nil {
		return
	}
	r.circuit.mu.Lock()
	r.circuit.failures = 0
	r.circuit.openedAt = time.Time{}
	r.circuit.probeActive = false
	r.circuit.mu.Unlock()
}

func (r *ProviderRunner) recordCircuitFailure(err error) bool {
	if r == nil || r.circuit == nil || err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	r.circuit.mu.Lock()
	defer r.circuit.mu.Unlock()
	if !r.circuit.policy.Enabled {
		return false
	}
	r.circuit.probeActive = false
	r.circuit.failures++
	if r.circuit.failures >= r.circuit.policy.FailureThreshold {
		r.circuit.openedAt = time.Now()
		return true
	}
	return false
}

func (r *ProviderRunner) circuitCanProbe() bool {
	if r == nil || r.circuit == nil {
		return true
	}
	r.circuit.mu.Lock()
	defer r.circuit.mu.Unlock()
	return !r.circuit.policy.Enabled || r.circuit.openedAt.IsZero() || (!r.circuit.probeActive && time.Since(r.circuit.openedAt) >= r.circuit.policy.OpenDuration)
}

// Invoke runs a single request and streams responses
func (r *ProviderRunner) Invoke(ctx context.Context, req *types.OpenAIRequest) (<-chan *StreamEvent, <-chan error) {
	eventCh := make(chan *StreamEvent, 10)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)
		if circuitErr, allowed := r.allowCircuitRequest(); !allowed {
			r.health.mu.Lock()
			r.health.Status = "circuit_open"
			r.health.Available = false
			r.health.Error = circuitErr
			r.health.LastCheck = time.Now()
			r.health.mu.Unlock()
			errCh <- circuitErr
			return
		}

		start := time.Now()
		log := observability.Logger("provider").With("provider", r.prov.Name, "model", req.Model, "request_id", req.RequestID)
		maxTokens := 0
		if req.MaxTokens != nil {
			maxTokens = *req.MaxTokens
		}
		stream := req.Stream != nil && *req.Stream
		log.Debug("provider_request_started", "stream", stream, "tools", len(req.Tools), "max_tokens", maxTokens)
		err := r.executeCLI(ctx, req, eventCh)
		latency := time.Since(start)

		r.health.mu.Lock()
		r.health.LastCheck = time.Now()
		r.health.Latency = latency
		if err != nil {
			circuitOpened := r.recordCircuitFailure(err)
			log.Error("provider_request_failed", "error", observability.PublicError(err), "error_type", observability.ErrorType(err), observability.Since(start))
			r.health.Status = "error"
			r.health.Error = err
			if circuitOpened {
				r.health.Available = false
				r.health.Status = "circuit_open"
			}
			errCh <- err
		} else {
			r.recordCircuitSuccess()
			log.Info("provider_request_completed", observability.Since(start))
			r.health.Status = "healthy"
			r.health.Error = nil
			r.health.Available = true
			_ = emitStreamEvent(ctx, eventCh, &StreamEvent{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Model: req.Model, Done: true})
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
	if r.prov.Type == types.ProviderNVIDIA && strings.TrimSpace(r.prov.BaseURL) != "" {
		return r.executeHTTP(commandCtx, req, eventCh, emitted)
	}
	if strings.TrimSpace(r.prov.BaseURL) != "" {
		return r.executeHTTP(commandCtx, req, eventCh, emitted)
	}
	if r.prov.Type == types.ProviderCursor {
		return r.executeCursorACP(commandCtx, req, eventCh, emitted)
	}
	if r.prov.Protocol == "acp" && r.prov.Type == types.ProviderOpenCode {
		if r.prov.Harness.Observed() && r.prov.Harness.SupportsServer {
			return r.acpPool.do(commandCtx, r, req, eventCh, emitted)
		}
		return r.executeGenericACP(commandCtx, req, eventCh, emitted)
	}
	// Build prompt from messages
	prompt := r.buildPrompt(req)

	adapter := adapterFor(r.prov.Type)
	args := adapter.BuildArgs(r.prov, req.Model, req.ReasoningEffort)
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
			_ = stdout.Close()
			_ = stderr.Close()
		case <-processDone:
		}
	}()

	// Write prompt to stdin
	if adapter.PromptOnArgs() {
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
		meaningful, err := r.parseLineAndMaybeEmitContext(commandCtx, scanner.Text(), eventCh, req.Model, false)
		if meaningful {
			*emitted = true
		}
		if err != nil {
			parseErr = err
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		if commandErr := commandCtx.Err(); commandErr != nil {
			return commandErr
		}
		return err
	}

	if err := cmd.Wait(); err != nil {
		if commandErr := commandCtx.Err(); commandErr != nil {
			return commandErr
		}
		if parseErr != nil {
			return parseErr
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
	return adapterFor(provider.Type).BuildArgs(provider, requestedModel, "")
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
						if text, ok := m["text"].(string); ok {
							content += text
						}
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
	values := make(map[string]string, len(r.prov.Env)+len(os.Environ()))
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && providerInheritedEnvAllowed(r.prov.Type, key) {
			values[key] = value
		}
	}
	for k, v := range r.prov.Env {
		if isRouterClientEnv(k) {
			continue
		}
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return env
}

func isRouterClientEnv(key string) bool {
	if strings.HasPrefix(key, "GHR_") || strings.HasPrefix(key, "COPILOT_PROVIDER_") {
		return true
	}
	switch key {
	case "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "OPENAI_BASE_URL", "OPENAI_API_BASE", "CURSOR_API_ENDPOINT", "CURSOR_API_KEY":
		return true
	default:
		return false
	}
}

func providerInheritedEnvAllowed(providerType types.ProviderType, key string) bool {
	if isRouterClientEnv(key) {
		return false
	}
	if isCommonRuntimeEnv(key) {
		return true
	}
	for _, allowed := range providerCredentialEnv(providerType) {
		if key == allowed {
			return true
		}
	}
	return false
}

func isCommonRuntimeEnv(key string) bool {
	if strings.HasPrefix(key, "LC_") || strings.HasPrefix(key, "XDG_") {
		return true
	}
	switch key {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD", "OLDPWD", "TMPDIR", "TMP", "TEMP", "LANG", "TERM", "COLORTERM", "NO_COLOR", "CI":
		return true
	default:
		return false
	}
}

func providerCredentialEnv(providerType types.ProviderType) []string {
	switch providerType {
	case types.ProviderClaudeCode:
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	case types.ProviderCodex:
		return []string{"OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "CODEX_HOME"}
	case types.ProviderOpenCode:
		return []string{"OPENAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_HOME"}
	case types.ProviderMimo:
		return []string{"OPENAI_API_KEY", "MIMO_API_KEY", "MIMO_HOME"}
	case types.ProviderPi:
		return []string{"PI_HOME", "PI_CODING_AGENT_DIR", "OPENAI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY"}
	case types.ProviderCursor:
		return []string{"CURSOR_API_KEY"}
	case types.ProviderNVIDIA:
		return []string{"NVIDIA_API_KEY"}
	default:
		return nil
	}
}

func (r *ProviderRunner) parseLineAndEmit(line string, ch chan<- *StreamEvent, model string) {
	_, _ = r.parseLineAndMaybeEmitContext(context.Background(), line, ch, model, true)
}

func (r *ProviderRunner) parseLineAndMaybeEmitContext(ctx context.Context, line string, ch chan<- *StreamEvent, model string, emitErrors bool) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, nil
	}
	event := StreamEvent{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Model: model}
	var jsonMsg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &jsonMsg); err == nil {
		if message := structuredError(jsonMsg); message != "" {
			event.Error = structuredProviderError(jsonMsg)
			if emitErrors {
				if !emitStreamEvent(ctx, ch, &event) {
					return false, ctx.Err()
				}
			}
			return false, event.Error
		}
		if text := responseText(jsonMsg); text != "" {
			if providerErr := providerOutputError(text); providerErr != nil {
				if emitErrors {
					event.Error = providerErr
					if !emitStreamEvent(ctx, ch, &event) {
						return false, ctx.Err()
					}
				}
				return false, providerErr
			}
			event.Delta = text
			if !emitStreamEvent(ctx, ch, &event) {
				return false, ctx.Err()
			}
			return true, nil
		}
		event.ToolCalls = parseToolCalls(jsonMsg)
		if content, ok := jsonMsg["content"].(string); ok {
			if providerErr := providerOutputError(content); providerErr != nil {
				if emitErrors {
					event.Error = providerErr
					if !emitStreamEvent(ctx, ch, &event) {
						return false, ctx.Err()
					}
				}
				return false, providerErr
			}
			event.Delta = content
			if !emitStreamEvent(ctx, ch, &event) {
				return false, ctx.Err()
			}
			return content != "", nil
		}
		if text, ok := jsonMsg["text"].(string); ok {
			if providerErr := providerOutputError(text); providerErr != nil {
				if emitErrors {
					event.Error = providerErr
					if !emitStreamEvent(ctx, ch, &event) {
						return false, ctx.Err()
					}
				}
				return false, providerErr
			}
			event.Delta = text
			if !emitStreamEvent(ctx, ch, &event) {
				return false, ctx.Err()
			}
			return text != "", nil
		}
		if len(event.ToolCalls) > 0 {
			if !emitStreamEvent(ctx, ch, &event) {
				return false, ctx.Err()
			}
			return true, nil
		}
		if done, ok := jsonMsg["done"].(bool); ok && done {
			event.Done = true
			if !emitStreamEvent(ctx, ch, &event) {
				return false, ctx.Err()
			}
			return false, nil
		}
	}
	if !strings.HasPrefix(line, "{") {
		if providerErr := providerOutputError(line); providerErr != nil {
			if emitErrors {
				event.Error = providerErr
				if !emitStreamEvent(ctx, ch, &event) {
					return false, ctx.Err()
				}
			}
			return false, providerErr
		}
		event.Delta = line + "\n"
		if !emitStreamEvent(ctx, ch, &event) {
			return false, ctx.Err()
		}
		return true, nil
	}
	return false, nil
}

func providerOutputError(text string) error {
	normalized := strings.ToLower(strings.TrimSpace(text))
	for _, marker := range []string{
		"upgrade your plan to continue",
		"extra usage",
		"third-party apps",
		"quota exceeded",
		"usage limit",
		"weekly limit",
		"seven_day",
		"rate limit",
		"insufficient credits",
		"credits exhausted",
		"authentication required",
		"unauthorized",
		"login required",
	} {
		if strings.Contains(normalized, marker) {
			return fmt.Errorf("provider capacity error: %s", marker)
		}
	}
	return nil
}

func emitStreamEvent(ctx context.Context, ch chan<- *StreamEvent, event *StreamEvent) bool {
	select {
	case ch <- event:
		return true
	case <-ctx.Done():
		return false
	}
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
	return structuredErrorDepth(message, 0)
}

func structuredProviderError(message map[string]interface{}) error {
	if rateLimit, ok := message["rate_limit_info"].(map[string]interface{}); ok {
		if status, _ := rateLimit["status"].(string); strings.EqualFold(strings.TrimSpace(status), "rejected") {
			return &CapacityError{StatusCode: http.StatusTooManyRequests, RetryAfter: structuredRetryAfter(rateLimit, time.Now())}
		}
	}
	return fmt.Errorf("provider reported error: %s", structuredError(message))
}

func structuredRetryAfter(info map[string]interface{}, now time.Time) time.Duration {
	for _, key := range []string{"retry_after", "retryAfter", "reset_at", "resetAt"} {
		value, ok := info[key]
		if !ok {
			continue
		}
		switch value := value.(type) {
		case string:
			if delay := retryAfter(value, now); delay > 0 {
				return delay
			}
		case float64:
			if value <= 0 {
				continue
			}
			if strings.Contains(strings.ToLower(key), "reset") && value > float64(now.Unix()) {
				return boundedRetryAfter(time.Until(time.Unix(int64(value), 0)))
			}
			return boundedRetryAfter(time.Duration(value * float64(time.Second)))
		}
	}
	return 0
}

func structuredErrorDepth(message map[string]interface{}, depth int) string {
	if depth > 4 {
		return ""
	}
	if rateLimit, ok := message["rate_limit_info"].(map[string]interface{}); ok {
		if status, _ := rateLimit["status"].(string); strings.EqualFold(strings.TrimSpace(status), "rejected") {
			return "provider rate limit rejected"
		}
	}
	for _, key := range []string{"error", "errorMessage", "error_message", "message", "detail", "details"} {
		switch value := message[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return value
			}
		case map[string]interface{}:
			if nested := structuredErrorDepth(value, depth+1); nested != "" {
				return nested
			}
		}
	}
	if reason, _ := message["stopReason"].(string); reason == "error" {
		return "assistant message stopped with error"
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
	health := &ProviderHealth{
		Status:    r.health.Status,
		LastCheck: r.health.LastCheck,
		Latency:   r.health.Latency,
		Error:     r.health.Error,
		Available: r.health.Available,
	}
	r.health.mu.RUnlock()
	if !health.Available && r.circuitCanProbe() {
		health.Available = true
		health.Status = "half_open"
	}
	return health
}

func (r *ProviderRunner) MarkHealthy(latency time.Duration) {
	if r == nil || r.health == nil {
		return
	}
	r.health.mu.Lock()
	r.health.Status = "healthy"
	r.health.LastCheck = time.Now()
	r.health.Latency = latency
	r.health.Error = nil
	r.health.Available = true
	r.health.mu.Unlock()
	r.recordCircuitSuccess()
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
	if r.prov.Type == types.ProviderNVIDIA && strings.TrimSpace(r.prov.BaseURL) != "" {
		return nil
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
