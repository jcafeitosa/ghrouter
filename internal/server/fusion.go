package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/types"
)

type fusionResult struct {
	candidate routeCandidate
	text      string
	tools     []types.OpenAIToolCall
	err       error
	started   time.Time
}

func (s *Server) collectFusionResults(ctx context.Context, req *types.OpenAIRequest, requestID string, candidates []routeCandidate, maxCandidates int, firstComplete bool, maxCostMicros int64) []fusionResult {
	if maxCandidates > 0 && len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	if maxCostMicros > 0 {
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if candidateWithinCostBudget(req, candidate, maxCostMicros) {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}
	if len(candidates) == 0 {
		return nil
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan fusionResult, len(candidates))
	var group sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		group.Add(1)
		go func() {
			defer group.Done()
			attemptStarted := time.Now()
			runner := s.getProvider(candidate.provider)
			result := fusionResult{candidate: candidate, started: attemptStarted}
			if runner == nil {
				result.err = fmt.Errorf("provider %s not started", candidate.provider)
			} else {
				candidateRequest := *req
				candidateRequest.Model = candidate.model
				result.text, result.tools, result.err = collectProviderResponse(workCtx, runner, &candidateRequest)
			}
			status := "ok"
			if result.err != nil {
				status = "error"
				if !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
					s.recordModelFailure(candidate.provider, candidate.model, result.err)
				}
			} else {
				s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
			}
			s.telemetry.recordAttempt(requestID, candidate.provider, candidate.model, status, publicProviderError(result.err), attemptStarted)
			results <- result
		}()
	}
	if firstComplete {
		var winner *fusionResult
		remaining := len(candidates)
		for remaining > 0 {
			result := <-results
			remaining--
			if winner == nil && result.err == nil && (result.text != "" || len(result.tools) > 0) {
				copy := result
				winner = &copy
				cancel()
			}
		}
		group.Wait()
		if winner == nil {
			return nil
		}
		return []fusionResult{*winner}
	}
	group.Wait()
	close(results)
	working := make([]fusionResult, 0, len(candidates))
	for result := range results {
		if result.err == nil && (result.text != "" || len(result.tools) > 0) {
			working = append(working, result)
		}
	}
	return working
}

func candidateWithinCostBudget(req *types.OpenAIRequest, candidate routeCandidate, maxCostMicros int64) bool {
	if maxCostMicros <= 0 || candidate.tokenCost <= 0 {
		return maxCostMicros <= 0
	}
	return fusionCandidateCost(req, candidate) <= maxCostMicros
}

func fusionCandidateCost(req *types.OpenAIRequest, candidate routeCandidate) int64 {
	if candidate.tokenCost <= 0 {
		return 0
	}
	tokens := estimatePromptTokens(req) + 1024
	if req != nil && req.MaxTokens != nil && *req.MaxTokens > 0 {
		tokens = estimatePromptTokens(req) + *req.MaxTokens
	}
	units := int64((tokens + 999) / 1000)
	return units * int64(candidate.tokenCost)
}

func (s *Server) fusionRoute(requested string) *types.Route {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	for _, route := range s.cfg.Routes {
		if route != nil && routeStrategy(route) == "fusion" && matchPattern(requested, route.Pattern) {
			copy := *route
			return &copy
		}
	}
	return nil
}

func routeStrategy(route *types.Route) string {
	if route == nil {
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(route.Mode))
	if mode != "" {
		return mode
	}
	return strings.ToLower(strings.TrimSpace(route.Provider))
}

func (s *Server) handleFusionChat(ctx context.Context, w http.ResponseWriter, req *types.OpenAIRequest, requestID string, end func(string, bool, string, string, string, time.Duration), started time.Time, candidates []routeCandidate, route *types.Route) {
	working := s.collectFusionResults(ctx, req, requestID, candidates, route.MaxCandidates, route.FirstComplete, route.MaxCostMicros)
	if len(working) == 0 {
		end("error", false, "", req.Model, "/v1/chat/completions", time.Since(started))
		writeError(w, http.StatusBadGateway, "fusion_failed", "all fusion candidates failed")
		return
	}
	selected := working[0]
	if route.Judge != "" {
		if judged, ok := s.runFusionJudge(ctx, req, requestID, route.Judge, route.JudgeTimeout, working); ok {
			selected.text = judged
			selected.tools = nil
		}
	}
	promptTokens := estimatePromptTokens(req)
	completionTokens := estimateTokens(selected.text)
	s.telemetry.recordUsage(requestID, promptTokens, completionTokens)
	if req.Stream != nil && *req.Stream {
		if err := writeSyntheticChatStream(w, selected.candidate.model, selected.text, selected.tools); err != nil {
			end("error", false, selected.candidate.provider, selected.candidate.model, "/v1/chat/completions", time.Since(started))
			return
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.OpenAIResponse{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Object: "chat.completion", Created: time.Now().Unix(), Model: selected.candidate.model, Choices: []types.OpenAIChoice{{Index: 0, Message: types.OpenAIMessage{Role: "assistant", Content: selected.text, ToolCalls: selected.tools}, FinishReason: finishReason(selected.tools)}}, Usage: types.OpenAIUsage{PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens}})
	}
	end("ok", false, selected.candidate.provider, selected.candidate.model, "/v1/chat/completions", time.Since(started))
}

func (s *Server) runFusionJudge(ctx context.Context, req *types.OpenAIRequest, requestID, judge string, timeout time.Duration, candidates []fusionResult) (string, bool) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	provider, model := s.resolveProviderChoice(judge, req.Model)
	if provider == "" || model == "" {
		provider, model = s.resolveModelReference(judge)
	}
	runner := s.getProvider(provider)
	if runner == nil {
		return "", false
	}
	var prompt strings.Builder
	prompt.WriteString("Synthesize the candidate answers into one accurate answer. Return only the answer.\n")
	for i, candidate := range candidates {
		fmt.Fprintf(&prompt, "Candidate %d (%s/%s):\n%s\n", i+1, candidate.candidate.provider, candidate.candidate.model, candidate.text)
	}
	judgeRequest := &types.OpenAIRequest{Model: model, Messages: []types.OpenAIMessage{{Role: "system", Content: "You are the configured Ghrouter fusion judge."}, {Role: "user", Content: prompt.String()}}}
	started := time.Now()
	text, _, err := collectProviderResponse(ctx, runner, judgeRequest)
	status := "ok"
	if err != nil {
		status = "error"
		s.recordModelFailure(provider, model, err)
	}
	s.telemetry.recordAttempt(requestID, provider, model, status, publicProviderError(err), started)
	if err != nil || text == "" {
		return "", false
	}
	s.catalog.RecordSuccess(provider+"/"+model, time.Now())
	return text, true
}

func writeSyntheticChatStream(w http.ResponseWriter, model, text string, tools []types.OpenAIToolCall) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	write := func(chunk types.StreamChunk) error {
		payload, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := write(types.StreamChunk{ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model, Choices: []types.StreamChoice{{Index: 0, Delta: types.StreamDelta{Role: "assistant"}}}}); err != nil {
		return err
	}
	if err := write(types.StreamChunk{ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model, Choices: []types.StreamChoice{{Index: 0, Delta: types.StreamDelta{Content: text, ToolCalls: tools}}}}); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
