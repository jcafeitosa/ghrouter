package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ghrouter/internal/types"
)

func (s *Server) handleFusionAnthropic(ctx context.Context, w http.ResponseWriter, request AnthropicRequest, internal *types.OpenAIRequest, requestID string, end func(string, bool, string, string, string, time.Duration), started time.Time, candidates []routeCandidate, route *types.Route) {
	working := s.collectFusionResults(ctx, internal, requestID, candidates, route.MaxCandidates, route.FirstComplete, route.MaxCostMicros)
	if len(working) == 0 {
		end("error", false, "", request.Model, "/v1/messages", time.Since(started))
		writeError(w, http.StatusBadGateway, "fusion_failed", "all fusion candidates failed")
		return
	}
	selected := working[0]
	if route.Judge != "" {
		if judged, ok := s.runFusionJudge(ctx, internal, requestID, route.Judge, route.JudgeTimeout, working); ok {
			selected.text = judged
			selected.tools = nil
		}
	}
	promptTokens := estimateAnthropicPromptTokens(internal)
	completionTokens := estimateTokens(selected.text)
	s.telemetry.recordUsage(requestID, promptTokens, completionTokens)
	if request.Stream {
		if err := writeSyntheticAnthropicStream(w, request.Model, selected.text, promptTokens); err != nil {
			end("error", false, selected.candidate.provider, selected.candidate.model, "/v1/messages", time.Since(started))
			return
		}
	} else {
		response := AnthropicResponse{ID: fmt.Sprintf("msg_%d", time.Now().UnixNano()), Type: "message", Role: "assistant", Content: []AnthropicContentBlock{{Type: "text", Text: selected.text}}, Model: request.Model, StopReason: "end_turn", Usage: AnthropicUsage{InputTokens: promptTokens, OutputTokens: completionTokens}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
	end("ok", false, selected.candidate.provider, selected.candidate.model, "/v1/messages", time.Since(started))
}

func writeSyntheticAnthropicStream(w http.ResponseWriter, model, text string, promptTokens int) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	if err := writeAnthropicStreamEvent(w, flusher, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []AnthropicContentBlock{},
			"model":         model,
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
	if text != "" {
		if err := writeAnthropicStreamEvent(w, flusher, "content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]string{
				"type": "text_delta",
				"text": text,
			},
		}); err != nil {
			return err
		}
	}
	if err := writeAnthropicStreamEvent(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0}); err != nil {
		return err
	}
	if err := writeAnthropicStreamEvent(w, flusher, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": AnthropicUsage{OutputTokens: estimateTokens(text)},
	}); err != nil {
		return err
	}
	if err := writeAnthropicStreamEvent(w, flusher, "message_stop", map[string]string{"type": "message_stop"}); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
