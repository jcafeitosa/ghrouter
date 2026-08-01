package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

// AnthropicRequest represents the request body for Anthropic's /v1/messages endpoint
type AnthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	Messages      []AnthropicMessage `json:"messages"`
	System        string             `json:"system,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	TopK          *int               `json:"top_k,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

// AnthropicMessage represents a message in the Anthropic format
type AnthropicMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// AnthropicResponse represents the response body for Anthropic's /v1/messages endpoint
type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"` // always "message"
	Role         string                  `json:"role"` // always "assistant"
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence string                  `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicContentBlock represents a content block in the response
type AnthropicContentBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// AnthropicUsage represents token usage
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// handleAnthropicMessages handles POST /v1/messages (Anthropic-compatible)
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}

	// Convert AnthropicRequest to our internal OpenAIRequest for routing
	internalReq := s.convertToInternalRequest(&req)

	// Determine provider and model
	provider, model := s.route(internalReq.Model)
	if provider == "" {
		writeError(w, 404, "model_not_found", fmt.Sprintf("no provider for model %q", internalReq.Model))
		return
	}

	runner := s.getProvider(provider)
	if runner == nil {
		writeError(w, 500, "provider_unavailable", fmt.Sprintf("provider %s not started", provider))
		return
	}

	// Handle streaming
	if req.Stream {
		s.streamAnthropic(r.Context(), w, runner, &req, model)
		return
	}

	// Non-streaming
	s.nonStreamAnthropic(r.Context(), w, runner, &req, model)
}

// convertToInternalRequest converts AnthropicRequest to our internal OpenAIRequest
func (s *Server) convertToInternalRequest(ar *AnthropicRequest) *types.OpenAIRequest {
	messages := []types.OpenAIMessage{}
	for _, msg := range ar.Messages {
		messages = append(messages, types.OpenAIMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return &types.OpenAIRequest{
		Model:       ar.Model,
		MaxTokens:   &ar.MaxTokens,
		Temperature: ar.Temperature,
		TopP:        ar.TopP,
		Messages:    messages,
		Stream:      func(b bool) *bool { return &b }(ar.Stream),
	}
}

// nonStreamAnthropic handles non-streaming Anthropic requests
func (s *Server) nonStreamAnthropic(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *AnthropicRequest, model string) {
	internalReq := s.convertToInternalRequest(req)
	stream := false
	internalReq.Stream = &stream
	events, errs := runner.Invoke(ctx, internalReq)

	text := ""
	done := false
	var runErr error
	for !done {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			if ev.Done {
				done = true
				break
			}
			text += ev.Delta
		case err, ok := <-errs:
			if ok {
				runErr = err
			}
			done = true
		case <-time.After(3 * time.Minute):
			runErr = fmt.Errorf("request timed out")
			done = true
		}
	}

	if runErr != nil {
		writeError(w, 502, "provider_error", runErr.Error())
		return
	}

	// Build Anthropic response
	resp := AnthropicResponse{
		ID:         fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Type:       "message",
		Role:       "assistant",
		Content:    []AnthropicContentBlock{{Type: "text", Text: text}},
		Model:      req.Model,
		StopReason: "end_turn",
		Usage: AnthropicUsage{
			InputTokens:  0, // TODO: implement token counting
			OutputTokens: estimateTokens(text),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// streamAnthropic handles streaming Anthropic requests
func (s *Server) streamAnthropic(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *AnthropicRequest, model string) {
	internalReq := s.convertToInternalRequest(req)
	stream := true
	internalReq.Stream = &stream
	events, errs := runner.Invoke(ctx, internalReq)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Send initial message with role
	s.writeAnthropicEvent(w, flusher, AnthropicContentBlock{Type: "text", Text: ""}, false, "")

	done := false
	for !done {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			if ev.Done {
				// Send final delta with stop reason
				s.writeAnthropicEvent(w, flusher, AnthropicContentBlock{Type: "text", Text: ""}, true, "end_turn")
				done = true
				break
			}
			if ev.Delta != "" {
				s.writeAnthropicEvent(w, flusher, AnthropicContentBlock{Type: "text", Text: ev.Delta}, false, "")
			}
		case _, ok := <-errs:
			if ok {
				// For simplicity, we break and let the client see the stream end.
				// In a real implementation, we might send an error event.
				done = true
			}
			done = true
		case <-ctx.Done():
			done = true
		case <-time.After(3 * time.Minute):
			done = true
		}
	}

	flusher.Flush()
}

// writeAnthropicEvent writes a single event in the Anthropic streaming format
func (s *Server) writeAnthropicEvent(w http.ResponseWriter, flusher http.Flusher, delta AnthropicContentBlock, done bool, stopReason string) {
	event := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"type": "text",
			"text": delta.Text,
		},
	}
	if done {
		event["delta"] = map[string]interface{}{
			"type": "text",
			"text": "",
		}
		event["stop_reason"] = stopReason
		event["stop_sequence"] = nil
	}

	data, _ := json.Marshal(event)
	fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", data)
	flusher.Flush()
}
