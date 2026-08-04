package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/types"
)

func (s *Server) graphRoute(requested string) *types.Route {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	for _, route := range s.cfg.Routes {
		if route != nil && routeStrategy(route) == "graph" && matchPattern(requested, route.Pattern) {
			copy := *route
			return &copy
		}
	}
	return nil
}

func (s *Server) collectGraphResults(ctx context.Context, req *types.OpenAIRequest, requestID string, candidates []routeCandidate, route *types.Route) []fusionResult {
	limit := route.MaxCandidates
	if limit <= 0 || limit > 2 {
		limit = 2
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if route.MaxCostMicros > 0 {
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if candidateWithinCostBudget(req, candidate, route.MaxCostMicros) {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}
	if len(candidates) == 0 {
		return nil
	}
	profile := ProfileRequest(req)
	if !profile.Graph.Parallel && len(profile.Graph.Stages) > 1 {
		return s.collectSequentialGraphResults(ctx, req, requestID, candidates, profile)
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan fusionResult, len(candidates))
	var group sync.WaitGroup
	for index, candidate := range candidates {
		candidate, stage := candidate, graphStageForIndex(index, profile)
		group.Add(1)
		go func() {
			defer group.Done()
			started := time.Now()
			result := fusionResult{candidate: candidate, started: started}
			runner := s.getProvider(candidate.provider)
			if runner == nil {
				result.err = fmt.Errorf("provider %s not started", candidate.provider)
			} else {
				stageRequest := graphStageRequest(req, stage)
				stageRequest.Model = candidate.model
				result.text, result.tools, result.err = collectProviderResponse(workCtx, runner, stageRequest)
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
			s.telemetry.recordAttempt(requestID, candidate.provider, candidate.model, status, publicProviderError(result.err), started)
			results <- result
		}()
	}
	working := make([]fusionResult, 0, len(candidates))
	if route.FirstComplete {
		var winner *fusionResult
		for remaining := len(candidates); remaining > 0; remaining-- {
			result := <-results
			if winner == nil && result.err == nil && (result.text != "" || len(result.tools) > 0) {
				copy := result
				winner = &copy
				cancel()
			}
		}
		group.Wait()
		if winner != nil {
			working = append(working, *winner)
		}
		return working
	}
	group.Wait()
	close(results)
	for result := range results {
		if result.err == nil && (result.text != "" || len(result.tools) > 0) {
			working = append(working, result)
		}
	}
	sort.SliceStable(working, func(i, j int) bool {
		left := s.requestModelScore(working[i].candidate.provider, working[i].candidate.model, req)
		right := s.requestModelScore(working[j].candidate.provider, working[j].candidate.model, req)
		if left != right {
			return left > right
		}
		return canonicalModelID(working[i].candidate.provider, working[i].candidate.model) < canonicalModelID(working[j].candidate.provider, working[j].candidate.model)
	})
	return working
}

func (s *Server) collectSequentialGraphResults(ctx context.Context, req *types.OpenAIRequest, requestID string, candidates []routeCandidate, profile RequestProfile) []fusionResult {
	var previous string
	var final fusionResult
	for index, stage := range profile.Graph.Stages {
		candidate := candidates[index%len(candidates)]
		started := time.Now()
		result := fusionResult{candidate: candidate, started: started}
		runner := s.getProvider(candidate.provider)
		if runner == nil {
			result.err = fmt.Errorf("provider %s not started", candidate.provider)
		} else {
			stageRequest := graphStageRequestWithContext(req, stage, previous)
			stageRequest.Model = candidate.model
			result.text, result.tools, result.err = collectProviderResponse(ctx, runner, stageRequest)
		}
		status := "ok"
		if result.err != nil {
			status = "error"
			if !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
				s.recordModelFailure(candidate.provider, candidate.model, result.err)
			}
		} else {
			s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
			previous = result.text
			final = result
		}
		s.telemetry.recordAttempt(requestID, candidate.provider, candidate.model, status, publicProviderError(result.err), started)
		if result.err != nil {
			return nil
		}
	}
	if final.text == "" && len(final.tools) == 0 {
		return nil
	}
	return []fusionResult{final}
}

func graphStageForIndex(index int, profile RequestProfile) GraphStage {
	if len(profile.Graph.Stages) > 0 && profile.Intent != IntentChat {
		return profile.Graph.Stages[index%len(profile.Graph.Stages)]
	}
	if index%2 == 0 {
		return GraphAnalyze
	}
	return GraphCritique
}

func graphStageRequest(req *types.OpenAIRequest, stage GraphStage) *types.OpenAIRequest {
	return graphStageRequestWithContext(req, stage, "")
}

func graphStageRequestWithContext(req *types.OpenAIRequest, stage GraphStage, previous string) *types.OpenAIRequest {
	copy := *req
	instruction := "Analyze the request and produce an independent technical assessment."
	switch stage {
	case GraphAnswer:
		instruction = "Answer the request directly and accurately."
	case GraphExtract:
		instruction = "Extract the relevant facts, constraints, and inputs needed to answer the request."
	case GraphPlan:
		instruction = "Create a concrete, ordered plan for solving the request. Identify risks and acceptance checks."
	case GraphImplement:
		instruction = "Produce the implementation or actionable solution for the request, respecting its constraints."
	case GraphVerify:
		instruction = "Verify the proposed solution, identify defects, and state the precise corrections required."
	case GraphSynthesize:
		instruction = "Synthesize a precise final answer from the request and the available specialist context."
	case GraphCritique:
		instruction = "Critically inspect the request, identify risks and propose corrections."
	}
	messages := []types.OpenAIMessage{{Role: "system", Content: instruction}}
	if previous = strings.TrimSpace(previous); previous != "" {
		if len(previous) > 12000 {
			previous = previous[len(previous)-12000:]
		}
		messages = append(messages, types.OpenAIMessage{Role: "system", Content: "Previous graph stage output:\n" + previous})
	}
	copy.Messages = append(messages, req.Messages...)
	return &copy
}

func (s *Server) handleGraphChat(ctx context.Context, w http.ResponseWriter, req *types.OpenAIRequest, requestID string, end func(string, bool, string, string, string, time.Duration), started time.Time, candidates []routeCandidate, route *types.Route) {
	working := s.collectGraphResults(ctx, req, requestID, candidates, route)
	if len(working) == 0 {
		end("error", false, "", req.Model, "/v1/chat/completions", time.Since(started))
		writeError(w, http.StatusBadGateway, "graph_failed", "all graph specialists failed")
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

func (s *Server) handleGraphResponses(ctx context.Context, w http.ResponseWriter, request ResponsesRequest, internal *types.OpenAIRequest, requestID string, end func(string, bool, string, string, string, time.Duration), started time.Time, candidates []routeCandidate, route *types.Route) {
	working := s.collectGraphResults(ctx, internal, requestID, candidates, route)
	if len(working) == 0 {
		end("error", false, "", request.Model, "/v1/responses", time.Since(started))
		writeError(w, http.StatusBadGateway, "graph_failed", "all graph specialists failed")
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

func (s *Server) handleGraphAnthropic(ctx context.Context, w http.ResponseWriter, request AnthropicRequest, internal *types.OpenAIRequest, requestID string, end func(string, bool, string, string, string, time.Duration), started time.Time, candidates []routeCandidate, route *types.Route) {
	working := s.collectGraphResults(ctx, internal, requestID, candidates, route)
	if len(working) == 0 {
		end("error", false, "", request.Model, "/v1/messages", time.Since(started))
		writeError(w, http.StatusBadGateway, "graph_failed", "all graph specialists failed")
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
