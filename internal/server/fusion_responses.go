package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ghrouter/internal/types"
)

func (s *Server) handleFusionResponses(ctx context.Context, w http.ResponseWriter, request ResponsesRequest, internal *types.OpenAIRequest, requestID string, end func(string, bool, string, string, string, time.Duration), started time.Time, candidates []routeCandidate, route *types.Route) {
	working := s.collectFusionResults(ctx, internal, requestID, candidates, route.MaxCandidates, route.FirstComplete, route.MaxCostMicros)
	if len(working) == 0 {
		end("error", false, "", request.Model, "/v1/responses", time.Since(started))
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
	promptTokens := estimatePromptTokens(internal)
	completionTokens := estimateTokens(selected.text)
	s.telemetry.recordUsage(requestID, promptTokens, completionTokens)
	if request.Stream {
		if err := writeSyntheticResponsesStream(w, selected.candidate.model, selected.text, selected.tools, promptTokens, completionTokens); err != nil {
			end("error", false, selected.candidate.provider, selected.candidate.model, "/v1/responses", time.Since(started))
			return
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponsesResponse(selected.candidate.model, selected.text, selected.tools, promptTokens, completionTokens))
	}
	end("ok", false, selected.candidate.provider, selected.candidate.model, "/v1/responses", time.Since(started))
}

func writeSyntheticResponsesStream(w http.ResponseWriter, model, text string, tools []types.OpenAIToolCall, promptTokens, completionTokens int) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	state := newResponsesStreamState()
	if err := state.writeStart(w, flusher, model, promptTokens); err != nil {
		return err
	}
	if err := state.writeDelta(w, flusher, text); err != nil {
		return err
	}
	return state.writeFinish(w, flusher, model, text, tools, promptTokens, completionTokens)
}
