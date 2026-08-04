package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/catalog"
	"ghrouter/internal/observability"
	"ghrouter/internal/providers"
	"ghrouter/internal/resourcegov"
	"ghrouter/internal/types"
)

const (
	brainSelectionCooldown      = 15 * time.Second
	maxBrainSelectionCandidates = 32
)

type localBrainSelection struct {
	Model  string `json:"model"`
	Reason string `json:"reason,omitempty"`
}

func (s *Server) selectWithLocalBrain(req *types.OpenAIRequest, candidates []*catalog.ModelEntry) *catalog.ModelEntry {
	if s == nil || req == nil || strings.TrimSpace(s.brainURL) == "" || len(candidates) < 2 {
		return nil
	}
	if !s.brainSelectionAvailable() {
		return nil
	}
	if !s.brainReadyForSelection() {
		return nil
	}
	if len(candidates) > maxBrainSelectionCandidates {
		candidates = candidates[:maxBrainSelectionCandidates]
	}
	allowed := make(map[string]*catalog.ModelEntry, len(candidates))
	choices := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		id := canonicalModelID(candidate.Provider, candidate.Model)
		allowed[id] = candidate
		choice := map[string]any{
			"id":             id,
			"preferred":      s.modelPolicyScore(candidate.Provider, candidate.Model) > 0,
			"provider":       candidate.Provider,
			"capabilities":   candidate.Capabilities,
			"cost_tier":      candidate.CostTier,
			"health":         candidate.HealthStatus,
			"latency_p50_ms": candidate.LatencyP50.Milliseconds(),
			"latency_p95_ms": candidate.LatencyP95.Milliseconds(),
			"error_rate":     candidate.ErrorRate,
			"context_window": candidate.ContextWindow,
			"max_output":     candidate.MaxOutput,
			"tool_use":       candidate.ToolUse,
			"vision":         candidate.Vision,
			"thinking":       candidate.Thinking,
			"effort":         candidate.Effort,
			"quota_score":    quotaScore(account.Load(s.providerConfig(candidate.Provider))),
		}
		if !candidate.CooldownUntil.IsZero() {
			choice["cooldown_until"] = candidate.CooldownUntil
		}
		choices = append(choices, choice)
	}
	if len(allowed) < 2 {
		return nil
	}
	profile := ProfileRequest(req)
	hasPreferredCandidate := false
	for _, candidate := range allowed {
		if s.modelPolicyScore(candidate.Provider, candidate.Model) > 0 {
			hasPreferredCandidate = true
			break
		}
	}
	input := map[string]any{
		"intent": profile.Intent, "cost_class": profile.CostClass,
		"complexity": profile.Complexity, "modality": profile.Modality,
		"needs_tools": profile.NeedsTools, "needs_vision": profile.NeedsVision,
		"needs_long_context": profile.NeedsLongContext, "reasoning_effort": profile.ReasoningEffort,
		"requested_output_tokens": profile.RequestedOutput,
		"candidates":              choices,
	}
	payload, err := json.Marshal(map[string]any{
		"model":      s.brainModel,
		"stream":     false,
		"max_tokens": 32,
		"chat_template_kwargs": map[string]any{
			"enable_thinking": false,
		},
		"messages": []types.OpenAIMessage{
			{Role: "system", Content: `You are ghrouter's local model selector. Return exactly one JSON object and nothing else: {"model":"<one allowed id>"}. Never invent an id. Choose only from candidates. Respect candidates marked preferred when they remain capable and healthy; never violate the configured allow/exclude policy. For high or critical complexity prioritize capability and context fit over price; otherwise prefer free and low-latency candidates.`},
			{Role: "user", Content: mustJSON(input)},
		},
	})
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.brainAdmission != nil {
		decision, release, err := s.brainAdmission.AdmitWait(ctx, resourcegov.AdmissionRequest{
			Class:          resourcegov.RequestClassBrain,
			BrainAuxiliary: true,
		})
		if err != nil || !decision.Allowed {
			log := observability.Logger("brain").With("endpoint", s.brainURL, "model", s.brainModel)
			log.Debug("local_brain_selection_deferred", "stage", "admission", "reason", decision.Reason)
			return nil
		}
		defer release()
	}
	selectionOK := false
	defer func() {
		if selectionOK {
			s.markBrainSelectionSuccess()
		} else {
			s.markBrainSelectionFailure()
			s.quarantineBrainModels()
		}
	}()
	log := observability.Logger("brain").With("endpoint", s.brainURL, "model", s.brainModel, "candidates", len(allowed))
	release, err := providers.AcquireLocalHTTP(ctx, s.brainURL)
	if err != nil {
		log.Debug("local_brain_selection_failed", "stage", "acquire", "error_type", observability.ErrorType(err))
		return nil
	}
	defer release()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.brainURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		log.Debug("local_brain_selection_failed", "stage", "request", "error_type", observability.ErrorType(err))
		return nil
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5500 * time.Millisecond}).Do(request)
	if err != nil {
		log.Debug("local_brain_selection_failed", "stage", "http", "error_type", observability.ErrorType(err))
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.Debug("local_brain_selection_failed", "stage", "status", "status", response.StatusCode)
		return nil
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || len(result.Choices) == 0 {
		log.Debug("local_brain_selection_failed", "stage", "decode", "error_type", observability.ErrorType(err))
		return nil
	}
	selection := localBrainSelection{}
	content := strings.TrimSpace(result.Choices[0].Message.Content)
	if json.Unmarshal([]byte(content), &selection) != nil {
		start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
		if start < 0 || end <= start || json.Unmarshal([]byte(content[start:end+1]), &selection) != nil {
			selection.Model = extractSelectionModel(content)
		}
	}
	selected := allowed[canonicalModelID("", strings.TrimSpace(selection.Model))]
	if selected == nil {
		log.Debug("local_brain_selection_failed", "stage", "allowlist", "returned_model", strings.TrimSpace(selection.Model))
	}
	if selected != nil {
		selectionOK = true
		if hasPreferredCandidate && profile.Complexity != ComplexityHigh && profile.Complexity != ComplexityCritical && s.modelPolicyScore(selected.Provider, selected.Model) <= 0 {
			log.Debug("local_brain_selection_overridden", "stage", "preferred_policy", "returned_model", selected.ID)
			return nil
		}
	}
	if selected != nil && req != nil {
		req.SelectionReason = strings.TrimSpace(selection.Reason)
	}
	return selected
}

func (s *Server) brainSelectionAvailable() bool {
	s.brainMu.Lock()
	defer s.brainMu.Unlock()
	return s.brainUntil.IsZero() || !time.Now().Before(s.brainUntil)
}

func (s *Server) brainReadyForSelection() bool {
	s.brainMu.Lock()
	ready := s.brainReady
	s.brainMu.Unlock()
	if ready {
		return true
	}
	s.mu.RLock()
	_, configured := s.providers["local-brain"]
	s.mu.RUnlock()
	return !configured
}

func (s *Server) setBrainReady() {
	s.brainMu.Lock()
	s.brainReady = true
	s.brainMu.Unlock()
}

func (s *Server) markBrainSelectionFailure() {
	s.brainMu.Lock()
	s.brainUntil = time.Now().Add(brainSelectionCooldown)
	s.brainMu.Unlock()
}

func (s *Server) markBrainSelectionSuccess() {
	s.brainMu.Lock()
	s.brainUntil = time.Time{}
	s.brainMu.Unlock()
}

func (s *Server) quarantineBrainModels() {
	if s == nil || s.catalog == nil {
		return
	}
	now := time.Now()
	for _, entry := range s.catalog.GetModelsByProvider("local-brain") {
		if entry != nil {
			s.catalog.RecordFailure(entry.ID, now)
		}
	}
}

func extractSelectionModel(content string) string {
	marker := `"model"`
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	value := strings.TrimSpace(content[start+len(marker):])
	if !strings.HasPrefix(value, ":") {
		return ""
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, ":"))
	if !strings.HasPrefix(value, `"`) {
		return ""
	}
	end := strings.IndexByte(value[1:], '"')
	if end < 0 {
		return ""
	}
	var model string
	if err := json.Unmarshal([]byte(value[:end+2]), &model); err != nil {
		return ""
	}
	return model
}

func mustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(payload)
}
