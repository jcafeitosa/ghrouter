package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ghrouter/internal/types"
)

const (
	defaultLocalHTTPProviderTimeout  = 5 * time.Second
	defaultRemoteHTTPProviderTimeout = 30 * time.Second
)

type httpCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content  string                 `json:"content"`
			ToolCall []types.OpenAIToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type httpCompletionChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content  string                 `json:"content"`
			ToolCall []types.OpenAIToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

type CapacityError struct {
	Provider   string
	StatusCode int
	RetryAfter time.Duration
}

func (e *CapacityError) Error() string {
	if e == nil {
		return "provider capacity limit reached"
	}
	if e.StatusCode == http.StatusPaymentRequired {
		return fmt.Sprintf("provider capacity response HTTP %d: insufficient credits", e.StatusCode)
	}
	return fmt.Sprintf("provider rate limit response HTTP %d", e.StatusCode)
}

func (r *ProviderRunner) executeHTTP(ctx context.Context, req *types.OpenAIRequest, eventCh chan<- *StreamEvent, emitted *bool) error {
	release, err := r.acquireLocalHTTP(ctx)
	if err != nil {
		return err
	}
	defer release()

	requestPayload := *req
	if r.prov.Type == types.ProviderLocal {
		requestPayload.ChatTemplateKwargs = cloneChatTemplateKwargs(requestPayload.ChatTemplateKwargs)
		if _, ok := requestPayload.ChatTemplateKwargs["enable_thinking"]; !ok {
			requestPayload.ChatTemplateKwargs["enable_thinking"] = false
		}
	}
	if r.prov.Type == types.ProviderNVIDIA {
		requestPayload.Model = stripProviderPrefix(requestPayload.Model)
	}
	payload, err := json.Marshal(&requestPayload)
	if err != nil {
		return fmt.Errorf("encode local request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.prov.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create local request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	token := strings.TrimSpace(r.prov.AuthConfig["api_key"])
	if r.prov.Type == types.ProviderNVIDIA {
		token = nvidiaAPIKey(r.prov)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: httpProviderTimeout(r.prov)}).Do(request)
	if err != nil {
		return fmt.Errorf("local request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusPaymentRequired {
			return &CapacityError{Provider: r.prov.Name, StatusCode: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
		}
		return fmt.Errorf("local provider returned HTTP %d", response.StatusCode)
	}
	if req.Stream != nil && *req.Stream && strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return readHTTPStream(ctx, response.Body, eventCh, req.Model, emitted)
	}
	var completion httpCompletionResponse
	if err := json.NewDecoder(response.Body).Decode(&completion); err != nil {
		return fmt.Errorf("decode local response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return &EmptyResponseError{Provider: r.prov.Name}
	}
	message := completion.Choices[0].Message
	if strings.TrimSpace(message.Content) == "" && len(message.ToolCall) == 0 {
		return &EmptyResponseError{Provider: r.prov.Name}
	}
	if !emitStreamEvent(ctx, eventCh, &StreamEvent{Model: req.Model, Delta: message.Content, ToolCalls: message.ToolCall}) {
		return ctx.Err()
	}
	*emitted = true
	return nil
}

func httpProviderTimeout(provider *types.Provider) time.Duration {
	defaultTimeout := defaultRemoteHTTPProviderTimeout
	if provider != nil && provider.Type == types.ProviderLocal {
		defaultTimeout = defaultLocalHTTPProviderTimeout
	}
	if provider != nil && provider.Type == types.ProviderLocal && provider.Timeout > 0 {
		return provider.Timeout
	}
	if provider != nil && provider.Timeout > 0 && provider.Timeout < defaultTimeout {
		return provider.Timeout
	}
	return defaultTimeout
}

func retryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return boundedRetryAfter(time.Duration(seconds) * time.Second)
	}
	if resetAt, err := http.ParseTime(raw); err == nil && resetAt.After(now) {
		return boundedRetryAfter(resetAt.Sub(now))
	}
	if resetAt, err := time.Parse(time.RFC3339, raw); err == nil && resetAt.After(now) {
		return boundedRetryAfter(resetAt.Sub(now))
	}
	return 0
}

func boundedRetryAfter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	if delay > 24*time.Hour {
		return 24 * time.Hour
	}
	return delay
}

func cloneChatTemplateKwargs(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values)+1)
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func readHTTPStream(ctx context.Context, body io.Reader, eventCh chan<- *StreamEvent, model string, emitted *bool) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk httpCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode local stream: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content == "" && len(delta.ToolCall) == 0 {
			continue
		}
		if !emitStreamEvent(ctx, eventCh, &StreamEvent{ID: chunk.ID, Model: model, Delta: delta.Content, ToolCalls: delta.ToolCall}) {
			return ctx.Err()
		}
		*emitted = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !*emitted {
		return &EmptyResponseError{Provider: "local"}
	}
	return nil
}

func healthCheckHTTP(ctx context.Context, baseURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("local health returned HTTP %d", response.StatusCode)
	}
	return nil
}
