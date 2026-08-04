package server

import (
	"sort"
	"strings"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/catalog"
	"ghrouter/internal/health"
	"ghrouter/internal/types"
)

// RoutingCandidateExplanation is the redacted evidence used to rank one model.
// It intentionally contains no prompt, credential, or provider output.
type RoutingCandidateExplanation struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Eligible      bool     `json:"eligible"`
	Selected      bool     `json:"selected,omitempty"`
	Reason        string   `json:"reason"`
	Score         float64  `json:"score,omitempty"`
	Health        string   `json:"health"`
	CostTier      string   `json:"cost_tier"`
	ContextWindow int      `json:"context_window,omitempty"`
	LatencyP50Ms  int64    `json:"latency_p50_ms,omitempty"`
	LatencyP95Ms  int64    `json:"latency_p95_ms,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Slots         []string `json:"slots,omitempty"`
}

// RoutingExplanation describes the observable decision path without invoking a
// provider. It is suitable for the CLI and future control-plane surfaces.
type RoutingExplanation struct {
	Requested       string                        `json:"requested"`
	Profile         RequestProfile                `json:"profile"`
	Slot            string                        `json:"slot"`
	SelectionSource string                        `json:"selection_source"`
	SelectionReason string                        `json:"selection_reason,omitempty"`
	Selected        *RoutingCandidateExplanation  `json:"selected,omitempty"`
	Candidates      []RoutingCandidateExplanation `json:"candidates"`
}

func (s *Server) ExplainRequest(req *types.OpenAIRequest) RoutingExplanation {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if req == nil {
		req = &types.OpenAIRequest{}
	}
	profile := ProfileRequest(req)
	slot := slotForRequest(req)
	explanation := RoutingExplanation{
		Requested:       req.Model,
		Profile:         profile,
		Slot:            string(slot),
		SelectionSource: "none",
		SelectionReason: "",
		Candidates:      make([]RoutingCandidateExplanation, 0),
	}

	var selected *catalog.ModelEntry
	virtualCandidates := make(map[string]bool)
	selectionCandidates := make([]*catalog.ModelEntry, 0)
	if strings.TrimSpace(req.Model) != "" {
		provider, model := s.routeByModelName(req.Model, req.SessionID, req)
		if provider != "" && model != "" && s.catalog != nil {
			selected = s.catalog.GetModel(canonicalModelID(provider, model))
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Model)), "ghrouter/") {
				for _, candidate := range s.routeCandidatesLocked(req.Model, provider, model, req) {
					virtualCandidates[canonicalModelID(candidate.provider, candidate.model)] = true
				}
			}
		}
		if selected != nil {
			if stage := strings.TrimSpace(req.SelectionStage); stage != "" {
				explanation.SelectionSource = stage
			} else {
				explanation.SelectionSource = "explicit"
			}
		}
	} else {
		candidates := s.policyCandidates(req)
		prioritized := prioritizeModelCandidates(candidates, profile, slot)
		selectionCandidates = prioritized
		if brainSelected := s.selectWithLocalBrain(req, prioritized); brainSelected != nil {
			selected = brainSelected
			explanation.SelectionSource = "local_brain"
		} else {
			if backup := fastBackupCandidatesForServer(s, prioritized); profile.Intent == IntentChat && !profile.NeedsTools && !profile.NeedsVision && !profile.NeedsLongContext && profile.ReasoningEffort == "" && len(backup) > 0 {
				selectionCandidates = backup
			}
			selected = bestScoredCandidate(s, prioritized, req)
			if selected != nil {
				explanation.SelectionSource = "deterministic_score"
			}
		}
	}

	eligible := make(map[string]bool)
	for _, entry := range s.policyCandidates(req) {
		if entry != nil {
			eligible[entry.ID] = true
		}
	}
	prioritized := prioritizeModelCandidates(s.policyCandidates(req), profile, slot)
	priority := make(map[string]bool, len(prioritized))
	for _, entry := range selectionCandidates {
		if entry != nil {
			priority[entry.ID] = true
		}
	}
	if selected != nil && strings.TrimSpace(req.Model) != "" && len(virtualCandidates) == 0 && s.modelExplicitlyRoutable(selected.Provider, selected.Model) {
		eligible[selected.ID] = true
		priority[selected.ID] = true
	}
	if s.catalog == nil {
		explanation.SelectionReason = req.SelectionReason
		return explanation
	}
	explanation.SelectionReason = req.SelectionReason
	for _, entry := range s.catalog.GetAllModels() {
		if entry == nil {
			continue
		}
		if len(virtualCandidates) > 0 && !virtualCandidates[entry.ID] {
			continue
		}
		candidate := RoutingCandidateExplanation{
			ID:            entry.ID,
			Provider:      entry.Provider,
			Model:         entry.Model,
			Eligible:      eligible[entry.ID] && priority[entry.ID],
			Health:        string(entry.HealthStatus),
			CostTier:      string(entry.CostTier),
			ContextWindow: entry.ContextWindow,
			LatencyP50Ms:  entry.LatencyP50.Milliseconds(),
			LatencyP95Ms:  entry.LatencyP95.Milliseconds(),
			Capabilities:  stringifyCaps(entry.Capabilities),
			Slots:         stringifySlots(entry.VirtualSlots),
		}
		switch {
		case !eligible[entry.ID]:
			candidate.Reason = "filtered_by_health_policy_quota_or_required_capability"
		case !priority[entry.ID]:
			candidate.Reason = "compatible_but_deprioritized_by_intent_or_cost"
		default:
			candidate.Reason = "eligible"
			candidate.Score = s.requestModelScore(entry.Provider, entry.Model, req)
		}
		if selected != nil && selected.ID == entry.ID {
			candidate.Selected = true
		}
		explanation.Candidates = append(explanation.Candidates, candidate)
	}
	sort.SliceStable(explanation.Candidates, func(i, j int) bool {
		if explanation.Candidates[i].Selected != explanation.Candidates[j].Selected {
			return explanation.Candidates[i].Selected
		}
		if explanation.Candidates[i].Eligible != explanation.Candidates[j].Eligible {
			return explanation.Candidates[i].Eligible
		}
		return explanation.Candidates[i].ID < explanation.Candidates[j].ID
	})
	if selected != nil {
		for i := range explanation.Candidates {
			if explanation.Candidates[i].ID == selected.ID {
				copy := explanation.Candidates[i]
				explanation.Selected = &copy
				break
			}
		}
	}
	return explanation
}

func (s *Server) policyCandidates(req *types.OpenAIRequest) []*catalog.ModelEntry {
	if s == nil || s.catalog == nil || s.cfg == nil {
		return nil
	}
	candidates := make([]*catalog.ModelEntry, 0)
	profile := ProfileRequest(req)
	for _, entry := range s.catalog.GetAllModels() {
		if entry == nil || !s.modelPolicyAllows(entry.Provider, entry.Model) || s.catalog.IsInCooldown(entry.ID) {
			continue
		}
		if entry.Provider == "local-brain" && !s.brainReadyForSelection() {
			continue
		}
		if !requestModelEligible(entry, profile) || entry.HealthStatus != health.HealthHealthy {
			continue
		}
		quota := account.Load(s.providerConfig(entry.Provider))
		if quota.Source != "unsupported" && quotaScore(quota) <= -100000 {
			continue
		}
		candidates = append(candidates, entry)
	}
	return candidates
}

func bestScoredCandidate(s *Server, candidates []*catalog.ModelEntry, req *types.OpenAIRequest) *catalog.ModelEntry {
	profile := ProfileRequest(req)
	if profile.Intent == IntentChat && !profile.NeedsTools && !profile.NeedsVision && !profile.NeedsLongContext && profile.ReasoningEffort == "" {
		if backup := fastBackupCandidatesForServer(s, candidates); len(backup) > 0 {
			candidates = backup
		}
	}
	var best *catalog.ModelEntry
	bestScore := -1e18
	for _, entry := range candidates {
		if entry == nil {
			continue
		}
		score := s.requestModelScore(entry.Provider, entry.Model, req)
		if best == nil || score > bestScore {
			best, bestScore = entry, score
		}
	}
	return best
}

func fastBackupCandidates(candidates []*catalog.ModelEntry) []*catalog.ModelEntry {
	backup := make([]*catalog.ModelEntry, 0, len(candidates))
	for _, entry := range candidates {
		if entry == nil {
			continue
		}
		if hasModelCapability(entry, catalog.CapabilityFast) || (entry.LatencyP50 > 0 && entry.LatencyP50 < 2*time.Second) {
			backup = append(backup, entry)
		}
	}
	return backup
}

func fastBackupCandidatesForServer(s *Server, candidates []*catalog.ModelEntry) []*catalog.ModelEntry {
	if s == nil {
		return fastBackupCandidates(candidates)
	}
	backup := fastBackupCandidates(candidates)
	seen := make(map[string]struct{}, len(backup))
	for _, candidate := range backup {
		if candidate != nil {
			seen[candidate.ID] = struct{}{}
		}
	}
	for _, candidate := range candidates {
		if candidate == nil || s.modelPolicyScore(candidate.Provider, candidate.Model) <= 0 {
			continue
		}
		if _, ok := seen[candidate.ID]; ok {
			continue
		}
		backup = append(backup, candidate)
		seen[candidate.ID] = struct{}{}
	}
	return backup
}
