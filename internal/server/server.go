package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/catalog"
	"ghrouter/internal/health"
	"ghrouter/internal/local_brain"
	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

type Server struct {
	cfg       *types.Config
	providers map[string]*providers.ProviderRunner
	catalog   *catalog.Catalog
	health    *health.Loop
	mu        sync.RWMutex
	routeMu   sync.Mutex
	rrCursor  map[string]int
	httpSrv   *http.Server
	started   time.Time
	telemetry *telemetryState
}

type ModelSummary struct {
	ID            string
	OwnedBy       string
	CostTier      string
	Capabilities  []string
	Slots         []string
	Health        string
	LatencyMs     int64
	CooldownUntil time.Time
	MaxTokens     int
}

type ProviderSnapshot struct {
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	CLIPath   string         `json:"cli_path"`
	Models    []string       `json:"models"`
	Available bool           `json:"available"`
	Health    string         `json:"health"`
	Auth      string         `json:"auth"`
	Account   account.Status `json:"account"`
}

type LiveSnapshot struct {
	ListenPort int                     `json:"listen_port"`
	StartedAt  time.Time               `json:"started_at"`
	Providers  []ProviderSnapshot      `json:"providers"`
	Models     []ModelSummary          `json:"models"`
	Slots      map[string]ModelSummary `json:"slots"`
	Health     HealthSnapshot          `json:"health"`
	Telemetry  TelemetrySnapshot       `json:"telemetry"`
}

type TelemetrySnapshot struct {
	Requests      int              `json:"requests"`
	Successful    int              `json:"successful"`
	Failed        int              `json:"failed"`
	Fallbacks     int              `json:"fallbacks"`
	Active        int              `json:"active"`
	Recent        []RequestEvent   `json:"recent"`
	ProviderUsage map[string]int   `json:"provider_usage"`
	LatencyMs     map[string]int64 `json:"latency_ms"`
}

type RequestEvent struct {
	At       time.Time     `json:"at"`
	Endpoint string        `json:"endpoint"`
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Status   string        `json:"status"`
	Fallback bool          `json:"fallback"`
	Latency  time.Duration `json:"latency"`
}

type telemetryState struct {
	mu            sync.Mutex
	requests      int
	successful    int
	failed        int
	fallbacks     int
	active        int
	providerUsage map[string]int
	latencyMs     map[string]int64
	recent        []RequestEvent
}

type HealthSnapshot struct {
	Healthy   int                    `json:"healthy"`
	Degraded  int                    `json:"degraded"`
	Unhealthy int                    `json:"unhealthy"`
	Cooldown  int                    `json:"cooldown"`
	Unknown   int                    `json:"unknown"`
	Providers map[string]HealthState `json:"providers"`
}

type HealthState struct {
	Status    string        `json:"status"`
	Latency   time.Duration `json:"latency"`
	Error     string        `json:"error,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

type HealthResponse struct {
	Status        string         `json:"status"`
	Uptime        time.Duration  `json:"uptime"`
	Health        HealthSnapshot `json:"health"`
	ProviderCount int            `json:"provider_count"`
	ModelCount    int            `json:"model_count"`
}

func (s *Server) RouteOpenAIRequest(req *types.OpenAIRequest) (provider string, model string) {
	if req == nil {
		return "", ""
	}

	if req.Model != "" {
		if provider, model = s.routeByModelName(req.Model); provider != "" {
			return provider, model
		}
	}

	if model := s.catalog.BestHealthyModelForSlot(slotForRequest(req)); model != nil {
		return model.Provider, model.Model
	}

	if model := s.catalog.BestHealthyModel(); model != nil {
		return model.Provider, model.Model
	}

	return "", ""
}

func New(cfg *types.Config) *Server {
	s := &Server{cfg: cfg, providers: make(map[string]*providers.ProviderRunner), rrCursor: make(map[string]int), started: time.Now(), telemetry: newTelemetryState()}
	s.health = health.NewLoop(30*time.Second, 10*time.Second)
	s.catalog = catalog.NewCatalog(s.health, 5*time.Minute)
	for _, p := range cfg.Providers {
		if p.Enabled {
			runner := providers.NewProviderRunner(p)
			s.providers[p.Name] = runner
			s.health.Register(runner)
			s.catalog.RegisterProvider(p.Name, buildCatalogModels(p))
		}
	}
	return s
}

func newTelemetryState() *telemetryState {
	return &telemetryState{
		providerUsage: make(map[string]int),
		latencyMs:     make(map[string]int64),
	}
}

func (t *telemetryState) begin() func(status string, fallback bool, provider, model, endpoint string, latency time.Duration) {
	t.mu.Lock()
	t.active++
	t.mu.Unlock()
	return func(status string, fallback bool, provider, model, endpoint string, latency time.Duration) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.active > 0 {
			t.active--
		}
		t.requests++
		if status == "ok" {
			t.successful++
		} else {
			t.failed++
		}
		if fallback {
			t.fallbacks++
		}
		if provider != "" {
			t.providerUsage[provider]++
		}
		if provider != "" {
			t.latencyMs[provider] = int64(latency / time.Millisecond)
		}
		t.recent = append([]RequestEvent{{
			At:       time.Now(),
			Endpoint: endpoint,
			Provider: provider,
			Model:    model,
			Status:   status,
			Fallback: fallback,
			Latency:  latency,
		}}, t.recent...)
		if len(t.recent) > 8 {
			t.recent = t.recent[:8]
		}
	}
}

func (t *telemetryState) snapshot() TelemetrySnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	recent := make([]RequestEvent, len(t.recent))
	copy(recent, t.recent)
	usage := make(map[string]int, len(t.providerUsage))
	for k, v := range t.providerUsage {
		usage[k] = v
	}
	latency := make(map[string]int64, len(t.latencyMs))
	for k, v := range t.latencyMs {
		latency[k] = v
	}
	return TelemetrySnapshot{
		Requests:      t.requests,
		Successful:    t.successful,
		Failed:        t.failed,
		Fallbacks:     t.fallbacks,
		Active:        t.active,
		Recent:        recent,
		ProviderUsage: usage,
		LatencyMs:     latency,
	}
}

func (s *Server) StartMonitoring(ctx context.Context) {
	if s.health != nil {
		s.health.Start(ctx)
	}
	if s.catalog != nil {
		s.catalog.Start(ctx)
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	s.StartMonitoring(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleRoot)

	port := s.cfg.ListenPort
	if port == 0 {
		port = 9090
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	s.httpSrv = &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutdownCtx)
	}()

	return s.httpSrv.ListenAndServe()
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	end := s.telemetry.begin()
	start := time.Now()
	var req types.OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		end("error", false, "", req.Model, "/v1/chat/completions", time.Since(start))
		return
	}

	provider, model := s.RouteOpenAIRequest(&req)
	if provider == "" {
		writeError(w, 404, "model_not_found", fmt.Sprintf("no provider for model %q", req.Model))
		end("error", false, "", req.Model, "/v1/chat/completions", time.Since(start))
		return
	}
	fallback := req.Model != "" && !strings.EqualFold(req.Model, model) && !strings.EqualFold(req.Model, provider)

	runner := s.getProvider(provider)
	if runner == nil {
		writeError(w, 500, "provider_unavailable", fmt.Sprintf("provider %s not started", provider))
		end("error", fallback, provider, model, "/v1/chat/completions", time.Since(start))
		return
	}

	stream := req.Stream != nil && *req.Stream
	if stream {
		s.streamChat(r.Context(), w, runner, &req, model)
	} else {
		s.nonStreamChat(r.Context(), w, runner, &req, model)
	}
	end("ok", fallback, provider, model, "/v1/chat/completions", time.Since(start))
}

// route maps a requested model (or empty) to provider + concrete model
func (s *Server) RouteModel(requested string) (provider string, model string) {
	return s.routeByModelName(requested)
}

func (s *Server) routeByModelName(requested string) (provider string, model string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// explicit provider prefix wins
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		pref := prefixFor(p.Type)
		if pref != "" && strings.HasPrefix(requested, pref) {
			rest := strings.TrimPrefix(requested, pref)
			if model = s.resolveModel(p, rest); model != "" && s.providerHealthy(p.Name) {
				return p.Name, model
			}
		}
	}

	// empty model -> first healthy provider
	if requested == "" {
		if model := s.catalog.GetModelBySlot(catalog.SlotAuto); model != nil {
			return model.Provider, model.Model
		}
		return "", ""
	}

	// exact model match across providers
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		if model = s.resolveModel(p, requested); model != "" && s.providerHealthy(p.Name) {
			return p.Name, model
		}
	}

	if model := s.catalog.GetModel(requested); model != nil {
		return model.Provider, model.Model
	}

	// route table fallback
	for _, route := range s.cfg.Routes {
		if matchPattern(requested, route.Pattern) {
			if provider, model := s.resolveConfiguredRoute(route, requested); provider != "" {
				return provider, model
			}
		}
	}

	return "", ""
}

func slotForRequest(req *types.OpenAIRequest) catalog.VirtualSlot {
	if req == nil {
		return catalog.SlotAuto
	}
	if len(req.Tools) > 0 || req.ToolChoice != nil {
		return catalog.SlotToolUse
	}
	text := requestText(req)
	switch {
	case containsAny(text, "vision", "image", "screenshot", "photo"):
		return catalog.SlotVision
	case len(text) > 6000 || (req.MaxTokens != nil && *req.MaxTokens >= 100000):
		return catalog.SlotLongContext
	case containsAny(text, "code", "bug", "compile", "test", "refactor", "golang", "go "):
		return catalog.SlotFastCode
	case containsAny(text, "reason", "plan", "analyze", "design", "architecture"):
		return catalog.SlotStrongReason
	default:
		return catalog.SlotAuto
	}
}

func requestText(req *types.OpenAIRequest) string {
	var parts []string
	for _, msg := range req.Messages {
		switch v := msg.Content.(type) {
		case string:
			parts = append(parts, v)
		case []interface{}:
			for _, part := range v {
				if m, ok := part.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func containsAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func (s *Server) resolveModel(p *types.Provider, name string) string {
	if name == "" {
		return ""
	}
	for _, m := range p.Models {
		if strings.HasSuffix(m, name) || strings.EqualFold(m, name) {
			return m
		}
	}
	return ""
}

func (s *Server) resolveConfiguredRoute(route *types.Route, requested string) (provider string, model string) {
	if route == nil {
		return "", ""
	}

	switch route.Provider {
	case "auto":
		if model := s.catalog.BestHealthyModel(); model != nil {
			return model.Provider, model.Model
		}
		return "", ""
	case "round-robin":
		return s.resolveRoundRobinRoute(route, requested)
	case "fusion":
		return s.resolveFusionRoute(route, requested)
	case "sticky":
		return s.resolveStickyRoute(route, requested)
	}

	if s.providerIsActive(route.Provider) && s.providerHealthy(route.Provider) {
		return route.Provider, requested
	}

	for _, fallback := range route.Fallback {
		if s.providerIsActive(fallback) && s.providerHealthy(fallback) {
			return fallback, requested
		}
	}

	if model := s.catalog.BestHealthyModel(); model != nil {
		return model.Provider, model.Model
	}

	return "", ""
}

func (s *Server) resolveRoundRobinRoute(route *types.Route, requested string) (provider string, model string) {
	candidates := s.healthyFallbacks(route)
	if len(candidates) == 0 {
		return "", ""
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	cursor := s.rrCursor[route.Pattern]
	for i := 0; i < len(candidates); i++ {
		idx := (cursor + i) % len(candidates)
		if provider, model = s.resolveProviderChoice(candidates[idx], requested); provider != "" {
			s.rrCursor[route.Pattern] = idx + 1
			return provider, model
		}
	}
	return "", ""
}

func (s *Server) resolveFusionRoute(route *types.Route, requested string) (provider string, model string) {
	candidates := s.healthyFallbacks(route)
	if len(candidates) == 0 {
		return "", ""
	}
	bestProvider := ""
	bestModel := ""
	bestScore := -1.0
	for _, candidate := range candidates {
		provider, model := s.resolveProviderChoice(candidate, requested)
		if provider == "" {
			continue
		}
		score := 0.0
		if runner := s.getProvider(provider); runner != nil {
			if health := runner.GetHealth(); health.Available {
				score += 10
				if health.Latency > 0 {
					score += float64(1000-health.Latency.Milliseconds()) * 0.01
				}
			}
		}
		if entry := s.catalog.GetModel(provider + "/" + model); entry != nil {
			switch entry.CostTier {
			case catalog.CostFree:
				score += 10
			case catalog.CostCheap:
				score += 5
			case catalog.CostPremium:
				score -= 5
			}
		}
		if score > bestScore {
			bestScore = score
			bestProvider = provider
			bestModel = model
		}
	}
	return bestProvider, bestModel
}

func (s *Server) resolveStickyRoute(route *types.Route, requested string) (provider string, model string) {
	candidates := s.healthyFallbacks(route)
	if len(candidates) == 0 {
		return "", ""
	}
	idx := stableIndex(requested, len(candidates))
	for i := 0; i < len(candidates); i++ {
		candidate := candidates[(idx+i)%len(candidates)]
		if provider, model = s.resolveProviderChoice(candidate, requested); provider != "" {
			return provider, model
		}
	}
	return "", ""
}

func (s *Server) healthyFallbacks(route *types.Route) []string {
	out := make([]string, 0, len(route.Fallback)+1)
	if route.Provider != "" && route.Provider != "round-robin" && route.Provider != "fusion" && route.Provider != "sticky" && route.Provider != "auto" {
		out = append(out, route.Provider)
	}
	out = append(out, route.Fallback...)
	filtered := out[:0]
	for _, candidate := range out {
		if s.providerIsActive(candidate) && s.providerHealthy(candidate) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (s *Server) resolveProviderChoice(providerName, requested string) (provider string, model string) {
	if !s.providerIsActive(providerName) || !s.providerHealthy(providerName) {
		return "", ""
	}
	runner := s.getProvider(providerName)
	if runner == nil {
		return "", ""
	}
	if requested == "" {
		if models := runner.GetModels(); len(models) > 0 {
			return providerName, models[0]
		}
		return providerName, ""
	}
	if model := s.resolveModelByProvider(providerName, requested); model != "" {
		return providerName, model
	}
	if models := runner.GetModels(); len(models) > 0 {
		return providerName, models[0]
	}
	return providerName, requested
}

func (s *Server) resolveModelByProvider(providerName, requested string) string {
	p := s.providers[providerName]
	if p == nil {
		return ""
	}
	for _, model := range p.GetModels() {
		if strings.HasSuffix(model, requested) || strings.EqualFold(model, requested) {
			return model
		}
	}
	return ""
}

func stableIndex(input string, size int) int {
	if size <= 0 {
		return 0
	}
	sum := 0
	for _, r := range input {
		sum = (sum*33 + int(r)) % size
	}
	return sum % size
}

func (s *Server) providerIsActive(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.providers[name]
	return ok
}

func (s *Server) providerHealthy(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runner := s.providers[name]
	if runner == nil {
		return false
	}
	health := runner.GetHealth()
	return health.Available
}

func matchPattern(name, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return strings.EqualFold(name, pattern)
}

func (s *Server) getProvider(name string) *providers.ProviderRunner {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providers[name]
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	var data []modelEntry
	for _, m := range s.catalog.GetAllModels() {
		data = append(data, modelEntry{ID: m.Model, Object: "model", Created: s.started.Unix(), OwnedBy: m.Provider})
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) ModelSummaries() []ModelSummary {
	out := make([]ModelSummary, 0)
	for _, m := range s.catalog.GetAllModels() {
		out = append(out, ModelSummary{
			ID:            m.Model,
			OwnedBy:       m.Provider,
			CostTier:      string(m.CostTier),
			Capabilities:  stringifyCaps(m.Capabilities),
			Slots:         stringifySlots(m.VirtualSlots),
			Health:        string(m.HealthStatus),
			LatencyMs:     m.LatencyP50.Milliseconds(),
			CooldownUntil: m.CooldownUntil,
			MaxTokens:     m.MaxTokens,
		})
	}
	return out
}

func (s *Server) LiveSnapshot() LiveSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := LiveSnapshot{
		ListenPort: s.cfg.ListenPort,
		StartedAt:  s.started,
		Models:     s.ModelSummaries(),
		Slots:      s.slotSummaries(),
		Health:     s.healthSnapshotLocked(),
		Telemetry:  s.telemetry.snapshot(),
	}
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		healthState := "unknown"
		available := false
		authState := "ok"
		if reason := local_brain.AuthReason(p); reason != "" {
			authState = reason
		}
		if runner := s.providers[p.Name]; runner != nil {
			state := runner.GetHealth()
			healthState = state.Status
			available = state.Available
		}
		accountState := account.Load(p)
		summary.Providers = append(summary.Providers, ProviderSnapshot{
			Name:      p.Name,
			Type:      string(p.Type),
			CLIPath:   p.CLIPath,
			Models:    append([]string(nil), p.Models...),
			Available: available,
			Health:    healthState,
			Auth:      authState,
			Account:   accountState,
		})
	}
	return summary
}

func (s *Server) slotSummaries() map[string]ModelSummary {
	out := make(map[string]ModelSummary)
	for _, slot := range []catalog.VirtualSlot{
		catalog.SlotFastCode, catalog.SlotCheapChat, catalog.SlotStrongReason, catalog.SlotLongContext, catalog.SlotVision, catalog.SlotToolUse, catalog.SlotAuto,
	} {
		if m := s.catalog.GetModelBySlot(slot); m != nil {
			out[string(slot)] = ModelSummary{
				ID:            m.Model,
				OwnedBy:       m.Provider,
				CostTier:      string(m.CostTier),
				Capabilities:  stringifyCaps(m.Capabilities),
				Slots:         stringifySlots(m.VirtualSlots),
				Health:        string(m.HealthStatus),
				LatencyMs:     m.LatencyP50.Milliseconds(),
				CooldownUntil: m.CooldownUntil,
				MaxTokens:     m.MaxTokens,
			}
		}
	}
	return out
}

func stringifyCaps(caps []catalog.CapabilityTag) []string {
	out := make([]string, 0, len(caps))
	for _, cap := range caps {
		out = append(out, string(cap))
	}
	return out
}

func stringifySlots(slots []catalog.VirtualSlot) []string {
	out := make([]string, 0, len(slots))
	for _, slot := range slots {
		out = append(out, string(slot))
	}
	return out
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	summary := s.healthSnapshot()
	resp := HealthResponse{
		Status:        "ok",
		Uptime:        time.Since(s.started),
		Health:        summary,
		ProviderCount: len(s.cfg.Providers),
		ModelCount:    len(s.ModelSummaries()),
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) healthSnapshot() HealthSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.healthSnapshotLocked()
}

func (s *Server) healthSnapshotLocked() HealthSnapshot {
	summary := HealthSnapshot{Providers: make(map[string]HealthState)}
	if s.health == nil {
		return summary
	}
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		result := s.health.GetHealth(p.Name)
		if result == nil {
			summary.Unknown++
			summary.Providers[p.Name] = HealthState{Status: string(health.HealthUnknown)}
			continue
		}
		state := HealthState{
			Status:    string(result.Status),
			Latency:   result.Latency,
			Timestamp: result.Timestamp,
		}
		if result.Error != nil {
			state.Error = result.Error.Error()
		}
		switch result.Status {
		case health.HealthHealthy:
			summary.Healthy++
		case health.HealthDegraded:
			summary.Degraded++
		case health.HealthUnhealthy:
			summary.Unhealthy++
		case health.HealthCooldown:
			summary.Cooldown++
		default:
			summary.Unknown++
		}
		summary.Providers[p.Name] = state
	}
	return summary
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "ghrouter: OpenAI-compatible router for gh copilot.\nGET /v1/models, POST /v1/chat/completions, GET /health\n")
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": code, "message": msg}})
}

func prefixFor(provider types.ProviderType) string {
	m := map[types.ProviderType]string{
		types.ProviderClaudeCode: "cc/", types.ProviderCodex: "cx/",
		types.ProviderOpenCode: "oc/", types.ProviderMimo: "mi/", types.ProviderPi: "pi/",
	}
	return m[provider]
}

func buildCatalogModels(p *types.Provider) []*catalog.ModelEntry {
	models := make([]*catalog.ModelEntry, 0, len(p.Models))
	accountState := account.Load(p)
	weight := account.Weight(accountState)
	for _, model := range p.Models {
		models = append(models, &catalog.ModelEntry{
			ID:             p.Name + "/" + model,
			Provider:       p.Name,
			Model:          model,
			HealthStatus:   health.HealthHealthy,
			MaxTokens:      p.MaxTokens,
			ProviderWeight: weight,
		})
	}
	return models
}
