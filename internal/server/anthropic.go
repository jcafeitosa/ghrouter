package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

// AnthropicRequest represents the request body for Anthropic's /v1/messages endpoint
type AnthropicRequest struct {
	Model         string             `json:"model"`
	RequestID     string             `json:"-"`
	MaxTokens     int                `json:"max_tokens"`
	Messages      []AnthropicMessage `json:"messages"`
	System        any                `json:"system,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	TopK          *int               `json:"top_k,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Tools         []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice    any                `json:"tool_choice,omitempty"`
}

// AnthropicMessage represents a message in the Anthropic format
type AnthropicMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content any    `json:"content"`
}

type AnthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
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
	rid := requestID(r)
	end := s.telemetry.beginWithMeta(rid, requestClient(r))
	start := time.Now()

	var req AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		end("error", false, "", req.Model, "/v1/messages", time.Since(start))
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages must contain at least one item")
		end("error", false, "", req.Model, "/v1/messages", time.Since(start))
		return
	}
	req.RequestID = rid

	// Convert AnthropicRequest to our internal OpenAIRequest for routing
	internalReq := s.convertToInternalRequest(&req)
	internalReq.RequestID = rid
	internalReq.SessionID = strings.TrimSpace(r.Header.Get("X-Claude-Code-Session-Id"))
	s.telemetry.recordDecision(rid, ProfileRequest(internalReq))

	// Determine provider and model
	provider, model := s.RouteOpenAIRequest(internalReq)
	s.telemetry.recordSelection(rid, provider, model, internalReq.SelectionStage, internalReq.SelectionReason)
	candidates := s.routeCandidates(req.Model, provider, model, internalReq)
	if len(candidates) == 0 && isVirtualModelRequest(req.Model) {
		if s.verifyVirtualRouteOnDemand(r.Context(), req.Model, internalReq) {
			provider, model = s.RouteOpenAIRequest(internalReq)
			s.telemetry.recordSelection(rid, provider, model, internalReq.SelectionStage, internalReq.SelectionReason)
			candidates = s.routeCandidates(req.Model, provider, model, internalReq)
		}
	}
	if len(candidates) == 0 {
		status := http.StatusNotFound
		code := "model_not_found"
		message := fmt.Sprintf("no provider for model %q", req.Model)
		if isVirtualModelRequest(req.Model) {
			status = http.StatusServiceUnavailable
			code = "model_unavailable"
			message = fmt.Sprintf("no verified provider is available for model %q; on-demand verification found no eligible provider", req.Model)
		}
		writeError(w, status, code, message)
		end("error", false, "", req.Model, "/v1/messages", time.Since(start))
		return
	}
	setRoutingHeaders(w, rid, req.Model, provider, model, internalReq.SelectionStage, len(candidates), internalReq.SelectionReason)
	if route := s.fusionRoute(req.Model); route != nil {
		s.handleFusionAnthropic(r.Context(), w, req, internalReq, rid, end, start, candidates, route)
		return
	}
	if route := s.graphRoute(req.Model); route != nil {
		s.handleGraphAnthropic(r.Context(), w, req, internalReq, rid, end, start, candidates, route)
		return
	}
	// Handle streaming
	if req.Stream {
		for index, candidate := range candidates {
			runner := s.getProvider(candidate.provider)
			if runner == nil {
				continue
			}
			setRoutingHeaders(w, rid, req.Model, candidate.provider, candidate.model, internalReq.SelectionStage, len(candidates), internalReq.SelectionReason)
			attemptStarted := time.Now()
			startedOutput, promptTokens, completionTokens, err := s.streamAnthropic(r.Context(), w, runner, &req, candidate.model)
			if err != nil {
				s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, "error", publicProviderError(err), attemptStarted)
				s.recordModelFailure(candidate.provider, candidate.model, err)
				if !startedOutput && r.Context().Err() == nil && index < len(candidates)-1 {
					continue
				}
				end("error", index > 0, candidate.provider, candidate.model, "/v1/messages", time.Since(start))
				if !startedOutput {
					writeError(w, http.StatusBadGateway, "provider_error", publicProviderError(err))
				}
				return
			}
			s.telemetry.recordUsage(rid, promptTokens, completionTokens)
			s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, "ok", "", attemptStarted)
			s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
			end("ok", index > 0, candidate.provider, candidate.model, "/v1/messages", time.Since(start))
			return
		}
		end("error", false, provider, model, "/v1/messages", time.Since(start))
		writeError(w, http.StatusInternalServerError, "provider_unavailable", "no routed provider is available")
		return
	}

	// Non-streaming
	for index, candidate := range candidates {
		runner := s.getProvider(candidate.provider)
		if runner == nil {
			continue
		}
		setRoutingHeaders(w, rid, req.Model, candidate.provider, candidate.model, internalReq.SelectionStage, len(candidates), internalReq.SelectionReason)
		attemptStarted := time.Now()
		promptTokens, completionTokens, err := s.nonStreamAnthropic(r.Context(), w, runner, &req, candidate.model)
		if err != nil {
			s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, "error", publicProviderError(err), attemptStarted)
			s.recordModelFailure(candidate.provider, candidate.model, err)
			if index < len(candidates)-1 {
				continue
			}
			end("error", index > 0, candidate.provider, candidate.model, "/v1/messages", time.Since(start))
			writeError(w, http.StatusBadGateway, "provider_error", publicProviderError(err))
			return
		}
		s.telemetry.recordUsage(rid, promptTokens, completionTokens)
		s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, "ok", "", attemptStarted)
		s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
		end("ok", index > 0, candidate.provider, candidate.model, "/v1/messages", time.Since(start))
		return
	}
	end("error", false, provider, model, "/v1/messages", time.Since(start))
	writeError(w, http.StatusInternalServerError, "provider_unavailable", "no routed provider is available")
}

// convertToInternalRequest converts AnthropicRequest to our internal OpenAIRequest
func (s *Server) convertToInternalRequest(ar *AnthropicRequest) *types.OpenAIRequest {
	messages := []types.OpenAIMessage{}
	if system := anthropicText(ar.System); system != "" {
		messages = append(messages, types.OpenAIMessage{Role: "system", Content: system})
	}
	for _, msg := range ar.Messages {
		messages = append(messages, types.OpenAIMessage{
			Role:    msg.Role,
			Content: anthropicText(msg.Content),
		})
	}
	tools := make([]types.OpenAITool, 0, len(ar.Tools))
	for _, tool := range ar.Tools {
		tools = append(tools, types.OpenAITool{
			Type: "function",
			Function: types.OpenAIToolFunc{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	return &types.OpenAIRequest{
		Model:       ar.Model,
		RequestID:   ar.RequestID,
		MaxTokens:   &ar.MaxTokens,
		Temperature: ar.Temperature,
		TopP:        ar.TopP,
		Messages:    messages,
		Stream:      func(b bool) *bool { return &b }(ar.Stream),
		Tools:       tools,
		ToolChoice:  ar.ToolChoice,
	}
}

func anthropicText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if block, ok := item.(map[string]interface{}); ok {
				if text, ok := block["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		if text, ok := value["text"].(string); ok {
			return text
		}
	}
	return ""
}

// nonStreamAnthropic handles non-streaming Anthropic requests
func (s *Server) nonStreamAnthropic(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *AnthropicRequest, model string) (int, int, error) {
	internalReq := s.convertToInternalRequest(req)
	internalReq.Model = model
	stream := false
	internalReq.Stream = &stream
	text, _, runErr := collectProviderResponse(ctx, runner, internalReq)
	if runErr != nil {
		return estimateAnthropicPromptTokens(internalReq), 0, runErr
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
			InputTokens:  estimateAnthropicPromptTokens(internalReq),
			OutputTokens: estimateTokens(text),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	promptTokens := estimateAnthropicPromptTokens(internalReq)
	completionTokens := estimateTokens(text)
	return promptTokens, completionTokens, json.NewEncoder(w).Encode(resp)
}

// streamAnthropic handles streaming Anthropic requests
func (s *Server) streamAnthropic(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *AnthropicRequest, model string) (bool, int, int, error) {
	internalReq := s.convertToInternalRequest(req)
	internalReq.Model = model
	promptTokens := estimateAnthropicPromptTokens(internalReq)
	completionTokens := 0
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
		return false, promptTokens, 0, fmt.Errorf("streaming unsupported")
	}

	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	streamModel := model
	if streamModel == "" {
		streamModel = req.Model
	}
	streamStarted := false
	startStream := func() error {
		if streamStarted {
			return nil
		}
		if err := writeAnthropicStreamEvent(w, flusher, "message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            messageID,
				"type":          "message",
				"role":          "assistant",
				"content":       []AnthropicContentBlock{},
				"model":         streamModel,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         AnthropicUsage{InputTokens: promptTokens, OutputTokens: 0},
			},
		}); err != nil {
			return err
		}
		if err := writeAnthropicStreamEvent(w, flusher, "content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]string{
				"type": "text",
				"text": "",
			},
		}); err != nil {
			return err
		}
		if err := writeAnthropicStreamEvent(w, flusher, "ping", map[string]string{"type": "ping"}); err != nil {
			return err
		}
		streamStarted = true
		return nil
	}

	completed := false
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	fail := func(runErr error) (bool, int, int, error) {
		if runErr == nil {
			runErr = fmt.Errorf("provider stream ended before completion")
		}
		if streamStarted && ctx.Err() == nil {
			if writeErr := writeAnthropicStreamError(w, flusher, runErr); writeErr != nil {
				return streamStarted, promptTokens, completionTokens, writeErr
			}
		}
		return streamStarted, promptTokens, completionTokens, runErr
	}
	for events != nil || errs != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				if !completed {
					return fail(fmt.Errorf("provider stream ended before completion"))
				}
				continue
			}
			if ev == nil {
				return fail(fmt.Errorf("provider stream returned an empty event"))
			}
			if ev.Error != nil {
				return fail(ev.Error)
			}
			if ev.Done {
				if err := startStream(); err != nil {
					return streamStarted, promptTokens, completionTokens, err
				}
				if err := writeAnthropicStreamEvent(w, flusher, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": 0,
				}); err != nil {
					return streamStarted, promptTokens, completionTokens, err
				}
				if err := writeAnthropicStreamEvent(w, flusher, "message_delta", map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason":   "end_turn",
						"stop_sequence": nil,
					},
					"usage": AnthropicUsage{OutputTokens: completionTokens},
				}); err != nil {
					return streamStarted, promptTokens, completionTokens, err
				}
				if err := writeAnthropicStreamEvent(w, flusher, "message_stop", map[string]string{"type": "message_stop"}); err != nil {
					return streamStarted, promptTokens, completionTokens, err
				}
				completed = true
				events = nil
				continue
			}
			if ev.Delta != "" {
				if err := startStream(); err != nil {
					return streamStarted, promptTokens, completionTokens, err
				}
				completionTokens += estimateTokens(ev.Delta)
				if err := writeAnthropicStreamEvent(w, flusher, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]string{
						"type": "text_delta",
						"text": ev.Delta,
					},
				}); err != nil {
					return streamStarted, promptTokens, completionTokens, err
				}
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return fail(err)
			}
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-timer.C:
			return fail(fmt.Errorf("request timed out"))
		}
	}

	if !completed {
		return fail(fmt.Errorf("provider stream ended before completion"))
	}
	flusher.Flush()
	return streamStarted, promptTokens, completionTokens, nil
}

func writeAnthropicStreamError(w http.ResponseWriter, flusher http.Flusher, runErr error) error {
	return writeAnthropicStreamEvent(w, flusher, "error", map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "provider_error",
			"message": publicProviderError(runErr),
		},
	})
}

// writeAnthropicStreamEvent writes one spec-shaped Anthropic SSE event.
func writeAnthropicStreamEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func estimateAnthropicPromptTokens(req *types.OpenAIRequest) int {
	return estimatePromptTokens(req)
}
