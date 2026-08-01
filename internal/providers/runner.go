package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"ghrouter/internal/types"
)

// ProviderRunner executes a single request against a CLI provider
type ProviderRunner struct {
	prov   *types.Provider
	health *ProviderHealth
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
		output, err := r.executeCLI(ctx, req)
		latency := time.Since(start)

		r.health.mu.Lock()
		r.health.LastCheck = time.Now()
		r.health.Latency = latency
		if err != nil {
			r.health.Status = "error"
			r.health.Error = err
			r.health.Available = false
			errCh <- err
		} else {
			r.health.Status = "healthy"
			r.health.Error = nil
			r.health.Available = true
			// Parse and emit events
			r.parseAndEmit(output, eventCh, req.Model)
		}
		r.health.mu.Unlock()
	}()

	return eventCh, errCh
}

// StreamEvent represents a streaming response chunk
type StreamEvent struct {
	ID      string
	Model   string
	Delta   string
	Done    bool
	Error   error
}

func (r *ProviderRunner) executeCLI(ctx context.Context, req *types.OpenAIRequest) (string, error) {
	// Build prompt from messages
	prompt := r.buildPrompt(req)

	// Build command
	args := append([]string{}, r.prov.Args...)

	// Add model flag if specified
	if req.Model != "" {
		model := strings.TrimPrefix(req.Model, "cc/")
		model = strings.TrimPrefix(model, "cx/")
		model = strings.TrimPrefix(model, "oc/")
		model = strings.TrimPrefix(model, "mi/")
		model = strings.TrimPrefix(model, "pi/")
		args = append(args, "-m", model)
	}

	cmd := exec.CommandContext(ctx, r.prov.CLIPath, args...)
	cmd.Dir = r.prov.WorkDir
	cmd.Env = r.buildEnv()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	// Write prompt to stdin
	go func() {
		defer stdin.Close()
		stdin.Write([]byte(prompt))
	}()

	// Read stdout
	output, err := io.ReadAll(stdout)
	if err != nil {
		cmd.Wait()
		return "", err
	}

	// Read stderr (non-blocking)
	stderrOut, _ := io.ReadAll(stderr)

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("CLI exited with error: %w, stderr: %s", err, string(stderrOut))
	}

	return string(output), nil
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
	env := []string{}
	for k, v := range r.prov.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	// Add current environment
	for _, e := range os.Environ() {
		env = append(env, e)
	}
	return env
}

func (r *ProviderRunner) parseAndEmit(output string, ch chan<- *StreamEvent, model string) {
	// Try parsing as JSONL (codex/opencode/mimo)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var event StreamEvent
		event.ID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		event.Model = model

		var jsonMsg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &jsonMsg); err == nil {
			// Check for content
			if content, ok := jsonMsg["content"].(string); ok {
				event.Delta = content
				event.Done = false
				ch <- &event
				continue
			}
			if text, ok := jsonMsg["text"].(string); ok {
				event.Delta = text
				event.Done = false
				ch <- &event
				continue
			}
			// Check for done flag
			if done, ok := jsonMsg["done"].(bool); ok && done {
				event.Done = true
				ch <- &event
				continue
			}
		}

		// Fallback: treat line as text
		if !strings.HasPrefix(line, "{") {
			event.Delta = line + "\n"
			event.Done = false
			ch <- &event
		}
	}

	// Final done event
	finalEvent := &StreamEvent{
		ID:    fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Model: model,
		Done:  true,
	}
	ch <- finalEvent
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

func (r *ProviderRunner) HealthCheck(ctx context.Context) error {
	// Quick test request
	testReq := &types.OpenAIRequest{
		Model: r.prov.Models[0],
		Messages: []types.OpenAIMessage{
			{Role: "user", Content: "ping"},
		},
	}

	eventCh, errCh := r.Invoke(ctx, testReq)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case <-eventCh:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("health check timeout")
	}
}