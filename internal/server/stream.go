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

// nonStreamChat collects the CLI output and returns a complete OpenAI response
func (s *Server) nonStreamChat(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *types.OpenAIRequest, model string) (int, int, error) {
	text, toolCalls, err := collectProviderResponse(ctx, runner, req)
	if err != nil {
		return 0, 0, err
	}
	promptTokens := estimatePromptTokens(req)
	completionTokens := estimateTokens(text)

	w.Header().Set("Content-Type", "application/json")
	resp := types.OpenAIResponse{
		ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Object: "chat.completion",
		Created: time.Now().Unix(), Model: model,
		Choices: []types.OpenAIChoice{{Index: 0, Message: types.OpenAIMessage{Role: "assistant", Content: text, ToolCalls: toolCalls}, FinishReason: finishReason(toolCalls)}},
		Usage:   types.OpenAIUsage{PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens},
	}
	return promptTokens, completionTokens, json.NewEncoder(w).Encode(resp)
}

func collectProviderResponse(ctx context.Context, runner *providers.ProviderRunner, req *types.OpenAIRequest) (string, []types.OpenAIToolCall, error) {
	events, errs := runner.Invoke(ctx, req)
	text := ""
	var toolCalls []types.OpenAIToolCall
	var runErr error
	eventsOpen, errsOpen := true, true
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	for eventsOpen || errsOpen {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				eventsOpen = false
				continue
			}
			if ev == nil {
				runErr = fmt.Errorf("provider returned an empty event")
				continue
			}
			if ev.Done {
				continue
			}
			if ev.Error != nil {
				runErr = ev.Error
				continue
			}
			text += ev.Delta
			if len(ev.ToolCalls) > 0 {
				toolCalls = append(toolCalls, ev.ToolCalls...)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				errsOpen = false
				continue
			}
			if err != nil {
				runErr = err
				return "", nil, runErr
			}
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-timer.C:
			runErr = fmt.Errorf("request timed out")
			eventsOpen, errsOpen = false, false
		}
	}
	if runErr != nil {
		return "", nil, runErr
	}
	return text, toolCalls, nil
}

func finishReason(toolCalls []types.OpenAIToolCall) string {
	if len(toolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

// streamChat emits SSE chunks (OpenAI-compatible) as the CLI streams
func (s *Server) streamChat(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *types.OpenAIRequest, model string) (bool, error) {
	events, errs := runner.Invoke(ctx, req)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return false, fmt.Errorf("streaming unsupported")
	}
	first, firstErr := firstProviderEvent(ctx, events, errs)
	if firstErr != nil {
		return false, firstErr
	}
	if first.Error != nil {
		return false, first.Error
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())

	if err := s.writeChunk(w, flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{Role: "assistant"}}); err != nil {
		return true, err
	}

	done := false
	var streamErr error
	if first.Done {
		if err := s.writeChunk(w, flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{}, FinishReason: "stop"}); err != nil {
			return true, err
		}
		done = true
	} else if first.Delta != "" || len(first.ToolCalls) > 0 {
		if err := s.writeChunk(w, flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{Content: first.Delta, ToolCalls: first.ToolCalls}}); err != nil {
			return true, err
		}
	}
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	for !done {
		select {
		case ev, ok := <-events:
			if !ok {
				streamErr = fmt.Errorf("provider stream ended before completion")
				done = true
				break
			}
			if ev == nil {
				streamErr = fmt.Errorf("provider stream returned an empty event")
				done = true
				break
			}
			if ev.Done {
				if err := s.writeChunk(w, flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{}, FinishReason: "stop"}); err != nil {
					return true, err
				}
				done = true
				break
			}
			if ev.Error != nil {
				streamErr = ev.Error
				done = true
				break
			}
			if ev.Delta != "" || len(ev.ToolCalls) > 0 {
				if err := s.writeChunk(w, flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{Content: ev.Delta, ToolCalls: ev.ToolCalls}}); err != nil {
					return true, err
				}
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				streamErr = err
			}
			done = true
		case <-ctx.Done():
			streamErr = ctx.Err()
			done = true
		case <-timer.C:
			streamErr = fmt.Errorf("request timed out")
			done = true
		}
	}

	if streamErr != nil {
		if ctx.Err() == nil {
			if err := s.writeChatStreamError(w, flusher, streamErr); err != nil {
				return true, err
			}
		}
	} else if !done {
		streamErr = fmt.Errorf("provider stream ended before completion")
		if ctx.Err() == nil {
			if err := s.writeChatStreamError(w, flusher, streamErr); err != nil {
				return true, err
			}
		}
	}
	if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
		return true, err
	}
	flusher.Flush()
	return true, streamErr
}

func firstProviderEvent(ctx context.Context, events <-chan *providers.StreamEvent, errs <-chan error) (*providers.StreamEvent, error) {
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event == nil {
				return nil, fmt.Errorf("provider stream returned an empty event")
			}
			if event.Error != nil {
				return nil, event.Error
			}
			return event, nil
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("request timed out")
		}
	}
	return nil, fmt.Errorf("provider stream ended before response")
}

func (s *Server) writeChatStreamError(w http.ResponseWriter, flusher http.Flusher, err error) error {
	payload := map[string]any{
		"error": map[string]string{
			"message": publicProviderError(err),
			"type":    "provider_error",
		},
	}
	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return marshalErr
	}
	if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", data); writeErr != nil {
		return writeErr
	}
	flusher.Flush()
	return nil
}

func (s *Server) writeChunk(w http.ResponseWriter, flusher http.Flusher, chatID, model string, choice types.StreamChoice) error {
	chunk := types.StreamChunk{
		ID: chatID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
		Choices: []types.StreamChoice{choice},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func estimateTokens(text string) int { return (len(text) + 3) / 4 }

func estimatePromptTokens(req *types.OpenAIRequest) int {
	if req == nil {
		return 0
	}
	total := 0
	for _, msg := range req.Messages {
		switch content := msg.Content.(type) {
		case string:
			total += estimateTokens(content)
		case []interface{}:
			for _, part := range content {
				if m, ok := part.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						total += estimateTokens(text)
					}
				}
			}
		}
	}
	if req.Model != "" {
		total += estimateTokens(req.Model)
	}
	return total
}
