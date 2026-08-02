package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/catalog"
	"ghrouter/internal/config"
	"ghrouter/internal/detectors"
	"ghrouter/internal/health"
	"ghrouter/internal/local_brain"
	"ghrouter/internal/observability"
	"ghrouter/internal/providers"
	"ghrouter/internal/security"
	"ghrouter/internal/storage"
	"ghrouter/internal/types"
)

type Server struct {
	cfg        *types.Config
	providers  map[string]*providers.ProviderRunner
	catalog    *catalog.Catalog
	health     *health.Loop
	mu         sync.RWMutex
	monitorMu  sync.Mutex
	monitoring bool
	verifyMu   sync.Mutex
	verifyNext map[string]int
	routeMu    sync.Mutex
	rrCursor   map[string]int
	sticky     map[string]stickyRoute
	httpSrv    *http.Server
	started    time.Time
	telemetry  *telemetryState
	clientKeys security.ClientKeys
	store      *storage.Store
	storageErr string
	configPath string
	storageMu  sync.RWMutex
	authMu     sync.Mutex
	authCache  map[string]authCacheEntry
	rateMu     sync.Mutex
	rateWindow map[string]rateWindow
}

type authCacheEntry struct {
	checkedAt time.Time
	ready     bool
}

type rateWindow struct {
	started time.Time
	count   int
}

type stickyRoute struct {
	provider string
	model    string
	expires  time.Time
}

type ModelSummary struct {
	ID            string    `json:"id"`
	OwnedBy       string    `json:"owned_by"`
	Provenance    string    `json:"provenance,omitempty"`
	CostTier      string    `json:"cost_tier,omitempty"`
	Capabilities  []string  `json:"capabilities,omitempty"`
	Slots         []string  `json:"slots,omitempty"`
	Health        string    `json:"health"`
	LatencyMs     int64     `json:"latency_ms"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	TokenCost     int       `json:"token_cost,omitempty"`
	MaxTokens     int       `json:"max_tokens,omitempty"`
	ContextWindow int       `json:"context_window,omitempty"`
	MaxOutput     int       `json:"max_output,omitempty"`
	Thinking      bool      `json:"thinking,omitempty"`
	Vision        bool      `json:"vision,omitempty"`
	ToolUse       bool      `json:"tool_use,omitempty"`
	Effort        []string  `json:"effort,omitempty"`
	CatalogSource string    `json:"catalog_source,omitempty"`
	List          bool      `json:"list,omitempty"`
	Members       []string  `json:"members,omitempty"`
}

type ModelTestResult struct {
	Requested     string    `json:"requested"`
	Provider      string    `json:"provider,omitempty"`
	Model         string    `json:"model,omitempty"`
	OK            bool      `json:"ok"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	LatencyMS     int64     `json:"latency_ms"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

type ModelListSummary struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind,omitempty"`
	Strategy string   `json:"strategy,omitempty"`
	Members  []string `json:"members"`
}

type ConnectionSummary struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Enabled  bool              `json:"enabled"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type PoolSummary struct {
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Strategy string   `json:"strategy,omitempty"`
	Enabled  bool     `json:"enabled"`
}

type ComboSummary struct {
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Strategy string   `json:"strategy,omitempty"`
	Judge    string   `json:"judge,omitempty"`
	Enabled  bool     `json:"enabled"`
}

type ProviderSnapshot struct {
	Name      string               `json:"name"`
	Type      string               `json:"type"`
	CLIPath   string               `json:"cli_path"`
	Models    []string             `json:"models"`
	Available bool                 `json:"available"`
	Health    string               `json:"health"`
	Auth      string               `json:"auth"`
	Account   account.Status       `json:"account"`
	Discovery types.DiscoveryState `json:"discovery,omitempty"`
}

type LiveSnapshot struct {
	ListenPort  int                     `json:"listen_port"`
	StartedAt   time.Time               `json:"started_at"`
	ClientKeys  map[string]string       `json:"client_keys,omitempty"`
	Providers   []ProviderSnapshot      `json:"providers"`
	Models      []ModelSummary          `json:"models"`
	ModelLists  []ModelListSummary      `json:"model_lists,omitempty"`
	Connections []ConnectionSummary     `json:"connections,omitempty"`
	Pools       []PoolSummary           `json:"pools,omitempty"`
	Combos      []ComboSummary          `json:"combos,omitempty"`
	Slots       map[string]ModelSummary `json:"slots"`
	Health      HealthSnapshot          `json:"health"`
	Telemetry   TelemetrySnapshot       `json:"telemetry"`
	Persistence string                  `json:"persistence,omitempty"`
}

type LiveResponse struct {
	Snapshot  LiveSnapshot                 `json:"snapshot"`
	Bootstrap local_brain.BootstrapSummary `json:"bootstrap"`
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
	RequestID        string         `json:"request_id"`
	Client           string         `json:"client,omitempty"`
	ConnectionID     string         `json:"connection_id,omitempty"`
	At               time.Time      `json:"at"`
	Endpoint         string         `json:"endpoint"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	Status           string         `json:"status"`
	Fallback         bool           `json:"fallback"`
	Latency          time.Duration  `json:"latency"`
	PromptTokens     int            `json:"prompt_tokens,omitempty"`
	CompletionTokens int            `json:"completion_tokens,omitempty"`
	CostMicros       int64          `json:"cost_micros,omitempty"`
	Attempts         []AttemptEvent `json:"attempts,omitempty"`
}

type AttemptEvent struct {
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	ConnectionID string        `json:"connection_id,omitempty"`
	Status       string        `json:"status"`
	Error        string        `json:"error,omitempty"`
	Latency      time.Duration `json:"latency"`
	StartedAt    time.Time     `json:"started_at"`
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
	attempts      map[string][]AttemptEvent
	usage         map[string][2]int
	costFn        func(provider, model string, tokens int) int64
	connectionFn  func(provider, model string) string
	sink          func(RequestEvent)
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

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value
	}
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

func requestClient(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Ghrouter-Client"))
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func (s *Server) RouteOpenAIRequest(req *types.OpenAIRequest) (provider string, model string) {
	if req == nil {
		return "", ""
	}

	if req.Model != "" {
		if provider, model = s.routeByModelName(req.Model, req.SessionID); provider != "" {
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
	return newServer(cfg, "")
}

func NewWithConfigPath(cfg *types.Config, configPath string) *Server {
	return newServer(cfg, configPath)
}

func newServer(cfg *types.Config, configPath string) *Server {
	normalizeConfiguredProviders(cfg)
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	s := &Server{cfg: cfg, configPath: configPath, providers: make(map[string]*providers.ProviderRunner), rrCursor: make(map[string]int), sticky: make(map[string]stickyRoute), started: time.Now(), telemetry: newTelemetryState(), authCache: make(map[string]authCacheEntry), rateWindow: make(map[string]rateWindow), verifyNext: make(map[string]int)}
	healthInterval := cfg.Health.Interval
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}
	healthTimeout := cfg.Health.Timeout
	if healthTimeout <= 0 {
		healthTimeout = 10 * time.Second
	}
	s.health = health.NewLoop(healthInterval, healthTimeout)
	s.health.SetOnSample(s.persistHealthSample)
	s.catalog = catalog.NewCatalog(s.health, 5*time.Minute)
	cooldownDefault := cfg.Cooldown.DefaultDuration
	if cooldownDefault <= 0 {
		cooldownDefault = 30 * time.Second
	}
	cooldownMax := cfg.Cooldown.MaxDuration
	if cooldownMax <= 0 {
		cooldownMax = 10 * time.Minute
	}
	s.catalog.SetCooldownPolicy(cfg.Cooldown.IsEnabled(), cooldownDefault, cooldownMax)
	s.telemetry.costFn = func(provider, model string, tokens int) int64 {
		if tokens <= 0 {
			return 0
		}
		entry := s.catalog.GetModel(canonicalModelID(provider, model))
		if entry == nil || entry.TokenCost <= 0 {
			return 0
		}
		return int64((tokens+999)/1000) * int64(entry.TokenCost)
	}
	s.telemetry.connectionFn = s.connectionFor
	for _, p := range cfg.Providers {
		if p.Enabled && (strings.TrimSpace(p.CLIPath) != "" || strings.TrimSpace(p.BaseURL) != "" || len(p.Models) > 0 || p.Type == "" || p.Type == types.ProviderCustom) {
			runner := providers.NewProviderRunner(p)
			s.providers[p.Name] = runner
			s.health.Register(runner)
			s.catalog.RegisterProvider(p.Name, buildCatalogModels(p))
			status := account.Load(p)
			if status.ResetAt.After(time.Now()) && (account.Blocked(status) || (status.Balance != nil && *status.Balance <= 0)) {
				s.catalog.RecordProviderFailure(p.Name, time.Now(), status.ResetAt)
			}
		}
	}
	return s
}

func newTelemetryState() *telemetryState {
	return &telemetryState{
		providerUsage: make(map[string]int),
		latencyMs:     make(map[string]int64),
		attempts:      make(map[string][]AttemptEvent),
		usage:         make(map[string][2]int),
	}
}

func (t *telemetryState) setSink(sink func(RequestEvent)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sink = sink
}

func (t *telemetryState) begin() func(status string, fallback bool, provider, model, endpoint string, latency time.Duration) {
	return t.beginWithMeta("", "")
}

func (t *telemetryState) beginWithMeta(id, client string) func(status string, fallback bool, provider, model, endpoint string, latency time.Duration) {
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	t.mu.Lock()
	t.active++
	t.mu.Unlock()
	return func(status string, fallback bool, provider, model, endpoint string, latency time.Duration) {
		var event RequestEvent
		t.mu.Lock()
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
		event = RequestEvent{
			RequestID: id, Client: client, At: time.Now(),
			Endpoint: endpoint,
			Provider: provider,
			Model:    model,
			Status:   status,
			Fallback: fallback,
			Latency:  latency,
		}
		if t.connectionFn != nil {
			event.ConnectionID = t.connectionFn(provider, model)
		}
		event.Attempts = append([]AttemptEvent(nil), t.attempts[id]...)
		delete(t.attempts, id)
		if usage, ok := t.usage[id]; ok {
			event.PromptTokens = usage[0]
			event.CompletionTokens = usage[1]
			if t.costFn != nil {
				event.CostMicros = t.costFn(provider, model, usage[0]+usage[1])
			}
			delete(t.usage, id)
		}
		t.recent = append([]RequestEvent{event}, t.recent...)
		if len(t.recent) > 8 {
			t.recent = t.recent[:8]
		}
		sink := t.sink
		t.mu.Unlock()
		if sink != nil {
			sink(event)
		}
	}
}

func (t *telemetryState) recordAttempt(id, provider, model, status, publicError string, started time.Time) {
	if strings.TrimSpace(id) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	connectionID := ""
	if t.connectionFn != nil {
		connectionID = t.connectionFn(provider, model)
	}
	t.attempts[id] = append(t.attempts[id], AttemptEvent{Provider: provider, Model: model, ConnectionID: connectionID, Status: status, Error: publicError, Latency: time.Since(started), StartedAt: started})
}

func (t *telemetryState) recordUsage(id string, promptTokens, completionTokens int) {
	if strings.TrimSpace(id) == "" {
		return
	}
	t.mu.Lock()
	t.usage[id] = [2]int{promptTokens, completionTokens}
	t.mu.Unlock()
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
	s.monitorMu.Lock()
	if s.monitoring {
		s.monitorMu.Unlock()
		return
	}
	s.monitoring = true
	s.monitorMu.Unlock()
	if s.health != nil && s.cfg.Health.IsEnabled() {
		s.health.Start(ctx)
	}
	if s.catalog != nil {
		s.catalog.Start(ctx)
	}
	if s.cfg.Verification.IsEnabled() {
		go s.runVerificationLoop(ctx)
	}
}

func (s *Server) autoBootstrapEnabled() bool {
	if s == nil || s.configPath == "" || s.cfg == nil || s.cfg.Verification.Enabled != nil {
		return false
	}
	for _, list := range s.cfg.ModelLists {
		if strings.EqualFold(list.Name, "ghrouter/auto") {
			return true
		}
	}
	return false
}

func (s *Server) bootstrapAutomaticModels(ctx context.Context) {
	if !s.autoBootstrapEnabled() {
		return
	}
	providers := 0
	for _, provider := range s.cfg.Providers {
		if provider != nil && provider.Enabled {
			providers++
		}
	}
	if providers == 0 {
		return
	}
	bootstrapTimeout := s.cfg.Verification.Timeout
	if bootstrapTimeout <= 0 || bootstrapTimeout > 20*time.Second {
		bootstrapTimeout = 20 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	results := s.verifyConfiguredModels(probeCtx, providers*3, 3)
	observability.Logger("verification").Info("automatic_model_bootstrap_completed", "providers", providers, "results", len(results))
}

func (s *Server) VerifyConfiguredModels(ctx context.Context) []ModelTestResult {
	return s.verifyConfiguredModels(ctx, 0, 0)
}

func (s *Server) verifyConfiguredModels(ctx context.Context, batchSize, maxPerProvider int) []ModelTestResult {
	if s == nil || s.catalog == nil {
		return nil
	}
	interval := s.cfg.Verification.Interval
	now := time.Now()
	targets := make([]string, 0)
	seen := make(map[string]bool)
	blockedProviders := make(map[string]string)
	for _, entry := range s.catalog.GetAllModels() {
		if entry == nil || entry.ID == "" || seen[entry.ID] {
			continue
		}
		if !s.providerIsActive(entry.Provider) || !s.catalog.NeedsVerification(entry.ID, now, interval) {
			continue
		}
		if !s.providerHealthy(entry.Provider) {
			blockedProviders[entry.Provider] = "provider is not ready; model probes are deferred"
		}
		seen[entry.ID] = true
		targets = append(targets, entry.ID)
	}
	sort.Strings(targets)
	if batchSize > 0 && len(targets) > batchSize {
		targets = s.verificationBatch(targets, batchSize, maxPerProvider)
	}
	if len(targets) == 0 {
		return nil
	}
	workers := s.cfg.Verification.Workers
	if workers <= 0 {
		workers = 4
	}
	if workers > 8 {
		workers = 8
	}
	if workers > len(targets) {
		workers = len(targets)
	}
	timeout := s.cfg.Verification.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	type indexedResult struct {
		index  int
		result ModelTestResult
	}
	jobs := make(chan int)
	results := make(chan indexedResult, len(targets))
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					entry := s.catalog.GetModel(targets[index])
					if entry != nil {
						if reason, blocked := blockedProviders[entry.Provider]; blocked {
							results <- indexedResult{index: index, result: ModelTestResult{Requested: targets[index], Provider: entry.Provider, Model: entry.Model, Status: "unavailable", Error: reason}}
							continue
						}
					}
					probeCtx, cancel := context.WithTimeout(ctx, timeout)
					result := s.TestModel(probeCtx, targets[index])
					cancel()
					results <- indexedResult{index: index, result: result}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range targets {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		group.Wait()
		close(results)
	}()
	ordered := make([]ModelTestResult, len(targets))
	completed := 0
	for result := range results {
		ordered[result.index] = result.result
		completed++
	}
	if completed == 0 {
		return nil
	}
	completedResults := make([]ModelTestResult, 0, completed)
	for _, result := range ordered {
		if result.Status != "" {
			completedResults = append(completedResults, result)
		}
	}
	s.applyModelVerification(completedResults, now.UTC())
	if s.cfg.Storage.Enabled {
		if err := s.PersistCurrentState(); err != nil {
			observability.Logger("verification").Error("verification_state_persist_failed", "error", observability.PublicError(err))
		}
	}
	return completedResults
}

func (s *Server) applyModelVerification(results []ModelTestResult, verifiedAt time.Time) {
	if s == nil || len(results) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, result := range results {
		if result.Provider == "" || result.Model == "" || result.Status == "cooldown" {
			continue
		}
		for _, provider := range s.cfg.Providers {
			if provider == nil || provider.Name != result.Provider {
				continue
			}
			if provider.ModelInfo == nil {
				provider.ModelInfo = make(map[string]types.ModelInfo)
			}
			info := provider.ModelInfo[result.Model]
			info.Model = result.Model
			info.HealthStatus = result.Status
			info.CooldownUntil = result.CooldownUntil
			info.VerifiedAt = verifiedAt
			info.VerificationError = result.Error
			provider.ModelInfo[result.Model] = info
		}
	}
	normalizeConfiguredProviders(s.cfg)
	s.cfg.ModelLists = detectors.BuildAutomaticModelLists(s.cfg.Providers, s.cfg.ModelLists)
	if s.configPath != "" {
		if err := config.Save(s.configPath, s.cfg); err != nil {
			observability.Logger("verification").Error("verification_config_persist_failed", "error", observability.PublicError(err))
		}
	}
}

func (s *Server) runVerificationLoop(ctx context.Context) {
	interval := s.cfg.Verification.Interval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	batchSize := s.cfg.Verification.BatchSize
	if batchSize <= 0 {
		batchSize = 32
	}
	maxPerProvider := s.cfg.Verification.MaxPerProvider
	if maxPerProvider <= 0 {
		maxPerProvider = 8
	}
	if s.cfg.Verification.Startup {
		s.verifyConfiguredModels(ctx, batchSize, maxPerProvider)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.verifyConfiguredModels(ctx, batchSize, maxPerProvider)
		}
	}
}

func (s *Server) verificationBatch(targets []string, batchSize, maxPerProvider int) []string {
	if batchSize <= 0 || len(targets) <= batchSize {
		return targets
	}
	if maxPerProvider <= 0 || maxPerProvider > batchSize {
		maxPerProvider = batchSize
	}
	byProvider := make(map[string][]string)
	for _, target := range targets {
		entry := s.catalog.GetModel(target)
		if entry == nil {
			continue
		}
		byProvider[entry.Provider] = append(byProvider[entry.Provider], target)
	}
	providers := make([]string, 0, len(byProvider))
	for provider := range byProvider {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	selected := make([]string, 0, batchSize)
	counts := make(map[string]int, len(providers))
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	for len(selected) < batchSize {
		progress := false
		for _, provider := range providers {
			if len(selected) >= batchSize || counts[provider] >= maxPerProvider {
				continue
			}
			models := byProvider[provider]
			if len(models) == 0 {
				continue
			}
			cursor := s.verifyNext[provider] % len(models)
			selected = append(selected, models[cursor])
			s.verifyNext[provider] = (cursor + 1) % len(models)
			counts[provider]++
			progress = true
		}
		if !progress {
			break
		}
	}
	sort.Strings(selected)
	return selected
}

func (s *Server) ReloadConfig(next *types.Config) error {
	if s == nil || next == nil {
		return fmt.Errorf("reload requires a configuration")
	}
	if next.ListenPort != 0 && s.cfg.ListenPort != 0 && next.ListenPort != s.cfg.ListenPort {
		return fmt.Errorf("listen port change requires restart")
	}
	if next.Storage.Enabled != s.cfg.Storage.Enabled || next.Storage.Path != s.cfg.Storage.Path {
		return fmt.Errorf("storage change requires restart")
	}
	if next.Server != s.cfg.Server {
		return fmt.Errorf("server settings change requires restart")
	}
	if next.Health.IsEnabled() != s.cfg.Health.IsEnabled() {
		return fmt.Errorf("health enabled change requires restart")
	}
	if len(next.Providers) != len(s.cfg.Providers) {
		return fmt.Errorf("provider set change requires restart")
	}
	for _, provider := range next.Providers {
		if provider == nil {
			return fmt.Errorf("nil provider is not reloadable")
		}
		current := s.providers[provider.Name]
		if current == nil || provider.CLIPath != s.providerPath(provider.Name) || !sameStrings(provider.Models, current.GetModels()) {
			return fmt.Errorf("provider %s change requires restart", provider.Name)
		}
	}
	next.ModelLists = detectors.BuildAutomaticModelLists(next.Providers, next.ModelLists)
	healthInterval := next.Health.Interval
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}
	healthTimeout := next.Health.Timeout
	if healthTimeout <= 0 {
		healthTimeout = 10 * time.Second
	}
	if s.health != nil {
		s.health.SetSettings(healthInterval, healthTimeout)
	}
	cooldownDefault := next.Cooldown.DefaultDuration
	if cooldownDefault <= 0 {
		cooldownDefault = 30 * time.Second
	}
	cooldownMax := next.Cooldown.MaxDuration
	if cooldownMax <= 0 {
		cooldownMax = 10 * time.Minute
	}
	if s.catalog != nil {
		s.catalog.SetCooldownPolicy(next.Cooldown.IsEnabled(), cooldownDefault, cooldownMax)
	}
	s.mu.Lock()
	s.cfg.Routes = next.Routes
	s.cfg.ModelLists = next.ModelLists
	s.cfg.Connections = next.Connections
	s.cfg.Pools = next.Pools
	s.cfg.Combos = next.Combos
	s.cfg.ACL = next.ACL
	s.cfg.RateLimit = next.RateLimit
	s.cfg.Health = next.Health
	s.cfg.Cooldown = next.Cooldown
	for _, provider := range next.Providers {
		for _, current := range s.cfg.Providers {
			if current != nil && current.Name == provider.Name {
				current.AuthConfig = provider.AuthConfig
				current.Account = provider.Account
				current.Enabled = provider.Enabled
				current.Models = append([]string(nil), provider.Models...)
				current.ModelInfo = cloneModelInfoMap(provider.ModelInfo)
			}
		}
	}
	s.mu.Unlock()
	if store := s.getStore(); store != nil {
		if err := s.persistStorageState(); err != nil {
			return fmt.Errorf("persist reloaded configuration: %w", err)
		}
	}
	return nil
}

func normalizeConfiguredProviders(cfg *types.Config) {
	if cfg == nil {
		return
	}
	for _, provider := range cfg.Providers {
		detectors.EnrichProviderModels(provider)
	}
}

func cloneModelInfoMap(values map[string]types.ModelInfo) map[string]types.ModelInfo {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]types.ModelInfo, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func (s *Server) providerPath(name string) string {
	if provider := s.providers[name]; provider != nil {
		for _, configured := range s.cfg.Providers {
			if configured != nil && configured.Name == name {
				return configured.CLIPath
			}
		}
	}
	return ""
}

func (s *Server) connectionFor(provider, model string) string {
	if s == nil || provider == "" || model == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	providerType := types.ProviderType("")
	for _, configured := range s.cfg.Providers {
		if configured != nil && configured.Name == provider {
			providerType = configured.Type
			break
		}
	}
	prefix := prefixFor(providerType)
	normalizedModel := strings.TrimPrefix(model, provider+"/")
	normalizedModel = strings.TrimPrefix(normalizedModel, prefix)
	for _, connection := range s.cfg.Connections {
		if !connection.Enabled || connection.Provider != provider || connection.Name == "" {
			continue
		}
		configuredModel := strings.TrimSpace(connection.Model)
		configuredModel = strings.TrimPrefix(configuredModel, provider+"/")
		configuredModel = strings.TrimPrefix(configuredModel, prefix)
		if configuredModel == normalizedModel || configuredModel == model {
			return connection.Name
		}
	}
	return ""
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	log := observability.Logger("server")
	s.StartMonitoring(ctx)
	if s.cfg.Storage.Enabled {
		store, err := storage.Open(storage.Config{Enabled: true, Path: s.cfg.Storage.Path, RetentionDays: s.cfg.Storage.RetentionDays})
		if err != nil {
			s.setStorageError(err)
			log.Error("storage_unavailable", "error", observability.PublicError(err))
		} else {
			s.setStore(store)
			if err := s.restoreCatalogState(store); err != nil {
				s.setStorageError(err)
				log.Error("storage_catalog_restore_failed", "error", observability.PublicError(err))
			}
			s.telemetry.setSink(func(event RequestEvent) {
				err := store.EnqueueRequest(storage.RequestEvent{
					RequestID: event.RequestID, Client: event.Client,
					Endpoint: event.Endpoint, ConnectionID: event.ConnectionID, Provider: event.Provider, Model: event.Model,
					Status: event.Status, Fallback: event.Fallback,
					LatencyMS: event.Latency.Milliseconds(), At: event.At,
					PromptTokens: event.PromptTokens, CompletionTokens: event.CompletionTokens,
					CostMicros: event.CostMicros,
					Attempts: func() []storage.AttemptEvent {
						attempts := make([]storage.AttemptEvent, 0, len(event.Attempts))
						for _, attempt := range event.Attempts {
							attempts = append(attempts, storage.AttemptEvent{ProviderID: attempt.Provider, ModelID: attempt.Model, ConnectionID: attempt.ConnectionID, Status: attempt.Status, Error: attempt.Error, LatencyMS: attempt.Latency.Milliseconds(), StartedAt: attempt.StartedAt})
						}
						return attempts
					}(),
				})
				if err != nil {
					s.setStorageError(err)
					log.Error("request_persistence_failed", "request_id", event.RequestID, "error", observability.PublicError(err))
				}
			})
			if err := s.persistStorageState(); err != nil {
				s.setStorageError(err)
				log.Error("storage_snapshot_failed", "error", observability.PublicError(err))
			}
			s.persistCurrentHealth()
			if err := store.RecordAudit("server_started", "local", map[string]any{"port": s.cfg.ListenPort}); err != nil {
				s.setStorageError(err)
				log.Error("storage_audit_failed", "error", observability.PublicError(err))
			}
			defer func() {
				_ = store.Close()
				s.setStore(nil)
			}()
		}
	}
	keys, keyErr := security.LoadOrCreate(s.cfg.ACL.KeysFile)
	if keyErr != nil {
		if s.cfg.ACL.Enabled {
			return fmt.Errorf("load client ACL keys: %w", keyErr)
		}
	} else {
		s.clientKeys = keys
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("/v1/responses", s.handleResponses)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/control-plane", s.handleControlPlane)
	mux.HandleFunc("/v1/control-plane/", s.handleControlPlaneResource)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/livez", s.handleLiveness)
	mux.HandleFunc("/readyz", s.handleReadiness)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/audit", s.handleAudit)
	mux.HandleFunc("/v1/live", s.handleLive)
	mux.HandleFunc("/live", s.handleLive)
	mux.HandleFunc("/", s.handleRoot)

	port := s.cfg.ListenPort
	if port == 0 {
		port = 9090
	}
	host := s.cfg.Server.Host
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	if err := validateBindHost(host, s.cfg.ACL.Enabled); err != nil {
		return err
	}
	listener, actualPort, cleanup, err := openListenerOnHost(host, port)
	if err != nil {
		log.Error("listener_failed", "error", observability.PublicError(err), "port", port)
		return err
	}
	defer cleanup()
	s.cfg.ListenPort = actualPort
	log.Info("server_started", "address", listener.Addr().String(), "port", actualPort)
	var handler http.Handler = mux
	if s.cfg.ACL.Enabled || s.cfg.RateLimit.Enabled {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" && r.URL.Path != "/livez" && r.URL.Path != "/readyz" && !s.authorized(r) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "valid ghrouter access token required")
				return
			}
			if r.URL.Path != "/health" && r.URL.Path != "/livez" && r.URL.Path != "/readyz" && !s.allowRequest(r) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
				return
			}
			mux.ServeHTTP(w, r)
		})
	}
	readTimeout := s.cfg.Server.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 30 * time.Second
	}
	writeTimeout := s.cfg.Server.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 180 * time.Second
	}
	idleTimeout := s.cfg.Server.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Second
	}
	s.httpSrv = &http.Server{Addr: listener.Addr().String(), Handler: handler, ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout}
	if s.autoBootstrapEnabled() {
		go s.bootstrapAutomaticModels(ctx)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutdownCtx)
	}()

	err = s.httpSrv.Serve(listener)
	if err != nil && !strings.Contains(err.Error(), "Server closed") {
		log.Error("server_stopped_with_error", "error", observability.PublicError(err))
	}
	return err
}

func (s *Server) allowRequest(r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.RateLimit.Enabled || s.cfg.RateLimit.RequestsPerMinute <= 0 {
		return true
	}
	key := ""
	if s.cfg.ACL.Enabled {
		token := requestToken(r)
		if token != "" {
			digest := sha256.Sum256([]byte(token))
			key = "token:" + hex.EncodeToString(digest[:8])
		}
	}
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-Ghrouter-Client"))
	}
	if key == "" {
		key = r.RemoteAddr
	}
	limit := s.cfg.RateLimit.Burst
	if limit <= 0 || limit > s.cfg.RateLimit.RequestsPerMinute {
		limit = s.cfg.RateLimit.RequestsPerMinute
	}
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	window := s.rateWindow[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= limit {
		s.rateWindow[key] = window
		return false
	}
	window.count++
	s.rateWindow[key] = window
	return true
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	telemetry := s.telemetry.snapshot()
	fmt.Fprintf(w, "# HELP ghrouter_requests_total Total requests handled by Ghrouter.\n# TYPE ghrouter_requests_total counter\nghrouter_requests_total %d\n", telemetry.Requests)
	fmt.Fprintf(w, "# HELP ghrouter_requests_success_total Successful requests handled by Ghrouter.\n# TYPE ghrouter_requests_success_total counter\nghrouter_requests_success_total %d\n", telemetry.Successful)
	fmt.Fprintf(w, "# HELP ghrouter_requests_failed_total Failed requests handled by Ghrouter.\n# TYPE ghrouter_requests_failed_total counter\nghrouter_requests_failed_total %d\n", telemetry.Failed)
	fmt.Fprintf(w, "# HELP ghrouter_request_fallbacks_total Requests that used a fallback candidate.\n# TYPE ghrouter_request_fallbacks_total counter\nghrouter_request_fallbacks_total %d\n", telemetry.Fallbacks)
	fmt.Fprintf(w, "# HELP ghrouter_requests_active Current in-flight requests.\n# TYPE ghrouter_requests_active gauge\nghrouter_requests_active %d\n", telemetry.Active)

	providers := make([]string, 0, len(telemetry.ProviderUsage))
	for provider := range telemetry.ProviderUsage {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	fmt.Fprintln(w, "# HELP ghrouter_provider_requests_total Requests routed to a provider.")
	fmt.Fprintln(w, "# TYPE ghrouter_provider_requests_total counter")
	for _, provider := range providers {
		fmt.Fprintf(w, "ghrouter_provider_requests_total{provider=\"%s\"} %d\n", escapePromLabel(provider), telemetry.ProviderUsage[provider])
	}
	fmt.Fprintln(w, "# HELP ghrouter_provider_latency_ms Last observed provider latency in milliseconds.")
	fmt.Fprintln(w, "# TYPE ghrouter_provider_latency_ms gauge")
	latencyProviders := make([]string, 0, len(telemetry.LatencyMs))
	for provider := range telemetry.LatencyMs {
		latencyProviders = append(latencyProviders, provider)
	}
	sort.Strings(latencyProviders)
	for _, provider := range latencyProviders {
		fmt.Fprintf(w, "ghrouter_provider_latency_ms{provider=\"%s\"} %d\n", escapePromLabel(provider), telemetry.LatencyMs[provider])
	}

	models := s.catalog.GetAllModels()
	sort.Slice(models, func(i, j int) bool {
		left := models[i].Provider + "/" + models[i].Model
		right := models[j].Provider + "/" + models[j].Model
		return left < right
	})
	fmt.Fprintln(w, "# HELP ghrouter_model_health Current model health state, one for the active state.")
	fmt.Fprintln(w, "# TYPE ghrouter_model_health gauge")
	fmt.Fprintln(w, "# HELP ghrouter_model_cooldown_until_seconds Unix timestamp when a model cooldown expires.")
	fmt.Fprintln(w, "# TYPE ghrouter_model_cooldown_until_seconds gauge")
	for _, model := range models {
		provider := escapePromLabel(model.Provider)
		modelID := escapePromLabel(model.Model)
		status := escapePromLabel(string(model.HealthStatus))
		fmt.Fprintf(w, "ghrouter_model_health{provider=\"%s\",model=\"%s\",status=\"%s\"} 1\n", provider, modelID, status)
		cooldown := float64(0)
		if !model.CooldownUntil.IsZero() {
			cooldown = float64(model.CooldownUntil.Unix())
		}
		fmt.Fprintf(w, "ghrouter_model_cooldown_until_seconds{provider=\"%s\",model=\"%s\"} %.0f\n", provider, modelID, cooldown)
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "audit supports GET")
		return
	}
	store := s.getStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_disabled", "audit history is unavailable while SQLite persistence is disabled")
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 1000")
			return
		}
		limit = parsed
	}
	events, err := store.ListAudit(limit)
	if err != nil {
		s.setStorageError(err)
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit history is temporarily unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
}

func escapePromLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rid := requestID(r)
	end := s.telemetry.beginWithMeta(rid, requestClient(r))
	start := time.Now()
	var req types.OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		end("error", false, "", req.Model, "/v1/chat/completions", time.Since(start))
		return
	}
	req.RequestID = rid
	req.SessionID = strings.TrimSpace(r.Header.Get("X-Ghrouter-Session"))

	provider, model := s.RouteOpenAIRequest(&req)
	candidates := s.routeCandidates(req.Model, provider, model)
	observability.Logger("routing").Info("request_routed", "request_id", requestID(r), "requested_model", req.Model, "provider", provider, "model", model, "candidates", len(candidates))
	if len(candidates) == 0 {
		status := http.StatusNotFound
		code := "model_not_found"
		message := fmt.Sprintf("no provider for model %q", req.Model)
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Model)), "ghrouter/") {
			status = http.StatusServiceUnavailable
			code = "model_unavailable"
			message = fmt.Sprintf("no verified provider is available for model %q; run ghrouter verify-models", req.Model)
		}
		writeError(w, status, code, message)
		end("error", false, "", req.Model, "/v1/chat/completions", time.Since(start))
		return
	}
	if fusion := s.fusionRoute(req.Model); fusion != nil {
		s.handleFusionChat(r.Context(), w, &req, rid, end, start, candidates, fusion)
		return
	}

	stream := req.Stream != nil && *req.Stream
	if stream {
		candidate := candidates[0]
		runner := s.getProvider(candidate.provider)
		if runner == nil {
			writeError(w, 500, "provider_unavailable", fmt.Sprintf("provider %s not started", candidate.provider))
			end("error", false, candidate.provider, candidate.model, "/v1/chat/completions", time.Since(start))
			return
		}
		candidateRequest := req
		candidateRequest.Model = candidate.model
		attemptStarted := time.Now()
		started, streamErr := s.streamChat(r.Context(), w, runner, &candidateRequest, candidate.model)
		attemptStatus := "ok"
		if streamErr != nil {
			attemptStatus = "error"
		}
		s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, attemptStatus, publicProviderError(streamErr), attemptStarted)
		if streamErr == nil {
			s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
		} else if !started {
			s.recordModelFailure(candidate.provider, candidate.model, streamErr)
		}
		if streamErr != nil && !started && len(candidates) > 1 {
			for i, next := range candidates[1:] {
				nextRunner := s.getProvider(next.provider)
				if nextRunner == nil {
					continue
				}
				nextRequest := req
				nextRequest.Model = next.model
				attemptStarted = time.Now()
				started, streamErr = s.streamChat(r.Context(), w, nextRunner, &nextRequest, next.model)
				attemptStatus = "ok"
				if streamErr != nil {
					attemptStatus = "error"
				}
				s.telemetry.recordAttempt(rid, next.provider, next.model, attemptStatus, publicProviderError(streamErr), attemptStarted)
				if streamErr == nil {
					s.catalog.RecordSuccess(next.provider+"/"+next.model, time.Now())
				} else if !started {
					s.recordModelFailure(next.provider, next.model, streamErr)
				}
				if streamErr == nil || started || i == len(candidates[1:])-1 {
					candidate = next
					break
				}
			}
		}
		endStatus := "ok"
		if streamErr != nil {
			endStatus = "error"
		}
		end(endStatus, candidate.provider != provider || candidate.model != model, candidate.provider, candidate.model, "/v1/chat/completions", time.Since(start))
		if streamErr != nil && !started {
			writeError(w, 502, "provider_error", publicProviderError(streamErr))
		}
		return
	}
	for i, candidate := range candidates {
		runner := s.getProvider(candidate.provider)
		if runner == nil {
			continue
		}
		candidateRequest := req
		candidateRequest.Model = candidate.model
		attemptStarted := time.Now()
		promptTokens, completionTokens, err := s.nonStreamChat(r.Context(), w, runner, &candidateRequest, candidate.model)
		attemptStatus := "ok"
		if err != nil {
			attemptStatus = "error"
		}
		s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, attemptStatus, publicProviderError(err), attemptStarted)
		if err == nil {
			s.telemetry.recordUsage(rid, promptTokens, completionTokens)
			s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
			end("ok", i > 0, candidate.provider, candidate.model, "/v1/chat/completions", time.Since(start))
			return
		} else {
			s.recordModelFailure(candidate.provider, candidate.model, err)
			if i == len(candidates)-1 {
				end("error", i > 0, candidate.provider, candidate.model, "/v1/chat/completions", time.Since(start))
				writeError(w, 502, "provider_error", publicProviderError(err))
				return
			}
		}
	}
	end("error", false, provider, model, "/v1/chat/completions", time.Since(start))
	writeError(w, 500, "provider_unavailable", "no routed provider is available")
}

type routeCandidate struct {
	provider  string
	model     string
	tokenCost int
}

func (s *Server) routeCandidates(requested, selectedProvider, selectedModel string) []routeCandidate {
	out := make([]routeCandidate, 0, len(s.cfg.Providers))
	appendCandidate := func(provider, model string) {
		if provider == "" || model == "" || !s.providerIsActive(provider) || !s.providerHealthy(provider) {
			return
		}
		if s.configPath != "" && !s.modelRoutable(provider, model) {
			return
		}
		if s.catalog != nil && s.catalog.IsInCooldown(canonicalModelID(provider, model)) {
			return
		}
		for _, candidate := range out {
			if candidate.provider == provider && candidate.model == model {
				return
			}
		}
		candidate := routeCandidate{provider: provider, model: model}
		if entry := s.catalog.GetModel(canonicalModelID(provider, model)); entry != nil {
			candidate.tokenCost = entry.TokenCost
		}
		out = append(out, candidate)
	}
	appendCandidate(selectedProvider, selectedModel)
	for _, list := range s.cfg.ModelLists {
		if !strings.EqualFold(list.Name, requested) {
			continue
		}
		for _, reference := range list.Models {
			provider, model := s.resolveModelReference(reference)
			if provider == "" {
				for _, candidateProvider := range s.cfg.Providers {
					if candidateProvider == nil || !candidateProvider.Enabled {
						continue
					}
					if resolved := s.resolveModel(candidateProvider, reference); resolved != "" {
						provider, model = candidateProvider.Name, resolved
						break
					}
				}
			}
			appendCandidate(provider, model)
		}
		break
	}
	for _, list := range s.controlPlaneLists() {
		if !strings.EqualFold(list.Name, requested) {
			continue
		}
		for _, reference := range list.Models {
			provider, model := s.resolveModelReference(reference)
			if provider == "" {
				for _, candidateProvider := range s.cfg.Providers {
					if candidateProvider == nil || !candidateProvider.Enabled {
						continue
					}
					if resolved := s.resolveModel(candidateProvider, reference); resolved != "" {
						provider, model = candidateProvider.Name, resolved
						break
					}
				}
			}
			appendCandidate(provider, model)
		}
		break
	}
	for _, route := range s.cfg.Routes {
		if !matchPattern(requested, route.Pattern) {
			continue
		}
		for _, fallback := range route.Fallback {
			provider, model := s.resolveProviderChoice(fallback, requested)
			if provider == "" {
				provider, model = s.resolveModelReference(fallback)
			}
			appendCandidate(provider, model)
		}
		break
	}
	return out
}

func (s *Server) resolveModelReference(reference string) (string, string) {
	for _, connection := range s.cfg.Connections {
		if !connection.Enabled || connection.Name != reference {
			continue
		}
		for _, p := range s.cfg.Providers {
			if p == nil || !p.Enabled || p.Name != connection.Provider {
				continue
			}
			modelReference := strings.TrimPrefix(connection.Model, p.Name+"/")
			modelReference = strings.TrimPrefix(modelReference, prefixFor(p.Type))
			if model := s.resolveModel(p, modelReference); model != "" {
				return p.Name, model
			}
		}
	}
	for _, p := range s.cfg.Providers {
		if p == nil || !p.Enabled {
			continue
		}
		if strings.HasPrefix(reference, p.Name+"/") {
			if model := s.resolveModel(p, strings.TrimPrefix(reference, p.Name+"/")); model != "" {
				return p.Name, model
			}
		}
		prefix := prefixFor(p.Type)
		if prefix != "" && strings.HasPrefix(reference, prefix) {
			if model := s.resolveModel(p, strings.TrimPrefix(reference, prefix)); model != "" {
				return p.Name, model
			}
		}
	}
	return "", ""
}

// route maps a requested model (or empty) to provider + concrete model
func (s *Server) RouteModel(requested string) (provider string, model string) {
	provider, model = s.routeByModelName(requested, "")
	if provider == "" || model == "" {
		return provider, model
	}
	if s.catalog != nil {
		if entry := s.catalog.GetModel(canonicalModelID(provider, model)); entry != nil {
			return provider, entry.ID
		}
	}
	return provider, model
}

func (s *Server) TestModel(ctx context.Context, requested string) ModelTestResult {
	result := ModelTestResult{Requested: requested, Status: "unrouted"}
	provider, model := s.resolveProbeTarget(requested)
	result.Provider, result.Model = provider, model
	if provider == "" || model == "" {
		result.Error = "no functional provider/model is available"
		return result
	}
	if entry := s.catalog.GetModel(canonicalModelID(provider, model)); entry != nil && s.catalog.IsInCooldown(canonicalModelID(provider, model)) {
		result.Status = "cooldown"
		result.Error = "model is in cooldown; verification is deferred until the reset window"
		result.CooldownUntil = entry.CooldownUntil
		return result
	}
	runner := s.getProvider(provider)
	if runner == nil {
		result.Status = "unavailable"
		result.Error = "provider runner is not started"
		return result
	}
	started := time.Now()
	var output strings.Builder
	events, errorsCh := runner.Invoke(ctx, &types.OpenAIRequest{
		Model:    model,
		Messages: []types.OpenAIMessage{{Role: "user", Content: "say hello"}},
	})
	var invokeErr error
	for events != nil || errorsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil && event.Error != nil && invokeErr == nil {
				invokeErr = event.Error
			}
			if event != nil && event.Delta != "" {
				output.WriteString(event.Delta)
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil && invokeErr == nil {
				invokeErr = err
			}
		}
	}
	probeOutput := strings.ToLower(strings.TrimSpace(output.String()))
	if invokeErr == nil && probeOutput != "ghrouter_model_probe_ok" && !strings.Contains(probeOutput, "ghrouter_model_probe_ok") && !strings.Contains(probeOutput, "hello") {
		invokeErr = fmt.Errorf("provider returned an invalid model probe response")
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	if invokeErr != nil {
		result.Status = "failed"
		result.Error = publicProviderError(invokeErr)
		s.recordModelFailure(provider, model, invokeErr)
		if entry := s.catalog.GetModel(canonicalModelID(provider, model)); entry != nil {
			result.CooldownUntil = entry.CooldownUntil
		}
		return result
	}
	result.OK = true
	result.Status = "healthy"
	s.catalog.RecordSuccess(provider+"/"+model, time.Now())
	return result
}

func (s *Server) resolveProbeTarget(requested string) (provider, model string) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", ""
	}
	for _, p := range s.cfg.Providers {
		if p == nil || !p.Enabled {
			continue
		}
		if strings.HasPrefix(requested, p.Name+"/") {
			if resolved := s.resolveModel(p, strings.TrimPrefix(requested, p.Name+"/")); resolved != "" {
				return p.Name, resolved
			}
		}
		if prefix := prefixFor(p.Type); prefix != "" && strings.HasPrefix(requested, prefix) {
			if resolved := s.resolveModel(p, strings.TrimPrefix(requested, prefix)); resolved != "" {
				return p.Name, resolved
			}
		}
	}
	if provider, model := s.catalog.GetProvider(requested), s.catalog.GetModel(requested); provider != "" && model != nil {
		return provider, model.Model
	}
	return s.RouteOpenAIRequest(&types.OpenAIRequest{Model: requested})
}

func (s *Server) recordModelFailure(provider, model string, err error) {
	if s.catalog == nil {
		return
	}
	if !isQuotaError(err) {
		s.catalog.RecordFailure(provider+"/"+model, time.Now())
		return
	}
	now := time.Now()
	var restoreAt time.Time
	for _, configured := range s.cfg.Providers {
		if configured != nil && configured.Name == provider {
			status := account.Load(configured)
			if status.ResetAt.After(now) {
				restoreAt = status.ResetAt
			}
			break
		}
	}
	s.catalog.RecordProviderFailure(provider, now, restoreAt)
}

func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"quota", "rate limit", "rate_limit", "too many requests", "insufficient credits", "usage limit", "credits exhausted", "monthly limit"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Server) routeByModelName(requested, sessionID string) (provider string, model string) {
	if provider, model = s.resolveModelList(requested); provider != "" {
		return provider, model
	}

	// explicit provider prefix wins
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		pref := prefixFor(p.Type)
		if pref != "" && strings.HasPrefix(requested, pref) {
			rest := strings.TrimPrefix(requested, pref)
			if model = s.resolveModel(p, rest); model != "" && s.modelRoutable(p.Name, model) {
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
		if model = s.resolveModel(p, requested); model != "" && s.modelRoutable(p.Name, model) {
			return p.Name, model
		}
	}

	if model := s.catalog.GetModel(requested); model != nil && s.modelRoutable(model.Provider, model.Model) {
		return model.Provider, model.Model
	}

	// route table fallback
	for _, route := range s.cfg.Routes {
		if matchPattern(requested, route.Pattern) {
			if provider, model := s.resolveConfiguredRoute(route, requested, sessionID); provider != "" {
				if s.modelRoutable(provider, model) {
					return provider, model
				}
			}
		}
	}

	return "", ""
}

func (s *Server) resolveModelList(requested string) (string, string) {
	var selected *types.ModelList
	for i := range s.cfg.ModelLists {
		if strings.EqualFold(s.cfg.ModelLists[i].Name, requested) {
			selected = &s.cfg.ModelLists[i]
			break
		}
	}
	if selected == nil {
		lists := s.controlPlaneLists()
		for i := range lists {
			if strings.EqualFold(lists[i].Name, requested) {
				selected = &lists[i]
				break
			}
		}
	}
	if selected == nil {
		return "", ""
	}
	type candidate struct {
		provider string
		model    string
		score    float64
	}
	candidates := make([]candidate, 0, len(selected.Models))
	for _, reference := range selected.Models {
		for _, leaf := range s.expandModelReferences(reference, make(map[string]bool)) {
			provider, model := s.resolveModelReference(leaf)
			if provider == "" {
				for _, p := range s.cfg.Providers {
					if p == nil || !p.Enabled {
						continue
					}
					if resolved := s.resolveModel(p, leaf); resolved != "" {
						provider, model = p.Name, resolved
						break
					}
				}
			}
			if provider == "" || model == "" || !s.providerHealthy(provider) || !s.modelRoutable(provider, model) {
				continue
			}
			score := 1.0
			if entry := s.catalog.GetModel(canonicalModelID(provider, model)); entry != nil {
				score += float64(entry.ProviderWeight)
				if entry.LatencyP50 > 0 {
					score -= entry.LatencyP50.Seconds()
				}
			}
			candidates = append(candidates, candidate{provider: provider, model: model, score: score})
		}
	}
	if len(candidates) == 0 {
		return "", ""
	}
	if strings.EqualFold(selected.Strategy, "round-robin") {
		s.routeMu.Lock()
		index := s.rrCursor["list:"+selected.Name] % len(candidates)
		s.rrCursor["list:"+selected.Name] = index + 1
		s.routeMu.Unlock()
		return candidates[index].provider, candidates[index].model
	}
	best := candidates[0]
	for _, item := range candidates[1:] {
		if item.score > best.score {
			best = item
		}
	}
	return best.provider, best.model
}

func (s *Server) expandModelReferences(reference string, visiting map[string]bool) []string {
	reference = strings.TrimSpace(reference)
	if reference == "" || visiting[reference] {
		return nil
	}
	for _, list := range s.allModelLists() {
		if !strings.EqualFold(list.Name, reference) {
			continue
		}
		visiting[reference] = true
		defer delete(visiting, reference)
		var leaves []string
		for _, member := range list.Models {
			leaves = append(leaves, s.expandModelReferences(member, visiting)...)
		}
		return leaves
	}
	return []string{reference}
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
	case containsAny(text, "cheap", "cheapest", "free", "budget", "low cost"):
		return catalog.SlotCheapChat
	case len(text) > 6000 || (req.MaxTokens != nil && *req.MaxTokens >= 100000):
		return catalog.SlotLongContext
	case containsAny(text, "fast", "quick", "low latency", "instant"):
		return catalog.SlotFastCode
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

func (s *Server) resolveConfiguredRoute(route *types.Route, requested, sessionID string) (provider string, model string) {
	if route == nil {
		return "", ""
	}

	switch routeStrategy(route) {
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
		return s.resolveStickyRoute(route, requested, sessionID)
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
		if entry := s.catalog.GetModel(canonicalModelID(provider, model)); entry != nil {
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

func (s *Server) resolveStickyRoute(route *types.Route, requested, sessionID string) (provider string, model string) {
	candidates := s.healthyFallbacks(route)
	if len(candidates) == 0 {
		return "", ""
	}
	key := strings.TrimSpace(sessionID)
	if key == "" {
		key = requested
	}
	stickyKey := route.Pattern + "\x00" + key
	s.routeMu.Lock()
	if sticky, ok := s.sticky[stickyKey]; ok && time.Now().Before(sticky.expires) {
		for _, candidate := range candidates {
			if candidate == sticky.provider {
				s.routeMu.Unlock()
				return sticky.provider, sticky.model
			}
		}
	}
	s.routeMu.Unlock()
	idx := stableIndex(key, len(candidates))
	for i := 0; i < len(candidates); i++ {
		candidate := candidates[(idx+i)%len(candidates)]
		if provider, model = s.resolveProviderChoice(candidate, requested); provider != "" {
			s.routeMu.Lock()
			s.sticky[stickyKey] = stickyRoute{provider: provider, model: model, expires: time.Now().Add(30 * time.Minute)}
			s.routeMu.Unlock()
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
	for _, provider := range s.cfg.Providers {
		if provider != nil && provider.Name == name && !s.providerAuthReady(provider) {
			if provider.Type == types.ProviderLocal {
				return false
			}
		}
		if provider != nil && provider.Name == name && provider.Type == types.ProviderLocal {
			status := account.Load(provider)
			if account.Blocked(status) {
				return false
			}
			if status.Balance != nil && *status.Balance <= 0 && !status.ResetAt.IsZero() && time.Now().Before(status.ResetAt) {
				return false
			}
		}
	}
	runnerHealth := runner.GetHealth()
	if !runnerHealth.Available {
		return false
	}
	if s.health != nil {
		if result := s.health.GetHealth(name); result != nil && result.Status == health.HealthUnhealthy {
			return false
		}
	}
	return true
}

func (s *Server) modelRoutable(provider, model string) bool {
	if provider == "" || model == "" {
		return false
	}
	if !s.modelVerified(provider, model) {
		return false
	}
	for _, configured := range s.cfg.Providers {
		if configured != nil && configured.Name == provider && !s.providerHealthy(provider) {
			return false
		}
	}
	if s.catalog == nil {
		return true
	}
	entry := s.catalog.GetModel(canonicalModelID(provider, model))
	if entry == nil {
		return true
	}
	return entry.HealthStatus == health.HealthHealthy && !s.catalog.IsInCooldown(canonicalModelID(provider, model))
}

func (s *Server) modelVerified(providerName, modelName string) bool {
	if s == nil || s.configPath == "" {
		return true
	}
	for _, provider := range s.cfg.Providers {
		if provider == nil || provider.Name != providerName {
			continue
		}
		metadata, ok := provider.ModelInfo[modelName]
		if !ok {
			return provider.Type == types.ProviderCustom || provider.Type == types.ProviderLocal
		}
		return !metadata.VerifiedAt.IsZero()
	}
	return false
}

func (s *Server) visibleProviderModels(providerName string) []string {
	for _, provider := range s.cfg.Providers {
		if provider == nil || provider.Name != providerName {
			continue
		}
		models := make([]string, 0, len(provider.Models))
		for _, model := range provider.Models {
			if s.modelRoutable(providerName, model) {
				models = append(models, canonicalModelID(providerName, model))
			}
		}
		return models
	}
	return nil
}

func (s *Server) providerAuthReady(provider *types.Provider) bool {
	if provider == nil {
		return false
	}
	now := time.Now()
	s.authMu.Lock()
	if s.authCache == nil {
		s.authCache = make(map[string]authCacheEntry)
	}
	if cached, ok := s.authCache[provider.Name]; ok && now.Sub(cached.checkedAt) < 15*time.Second {
		s.authMu.Unlock()
		return cached.ready
	}
	s.authMu.Unlock()
	ready := local_brain.AuthReason(provider) == ""
	s.authMu.Lock()
	s.authCache[provider.Name] = authCacheEntry{checkedAt: now, ready: ready}
	s.authMu.Unlock()
	return ready
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
		ID            string    `json:"id"`
		Object        string    `json:"object"`
		Created       int64     `json:"created"`
		OwnedBy       string    `json:"owned_by"`
		Provenance    string    `json:"provenance,omitempty"`
		Health        string    `json:"health"`
		CooldownUntil time.Time `json:"cooldown_until,omitempty"`
		ContextWindow int       `json:"context_window,omitempty"`
		MaxOutput     int       `json:"max_output,omitempty"`
		TokenCost     int       `json:"token_cost,omitempty"`
		Thinking      bool      `json:"thinking,omitempty"`
		Vision        bool      `json:"vision,omitempty"`
		ToolUse       bool      `json:"tool_use,omitempty"`
		Effort        []string  `json:"effort,omitempty"`
		CatalogSource string    `json:"catalog_source,omitempty"`
		List          bool      `json:"list,omitempty"`
		Members       []string  `json:"members,omitempty"`
	}
	var data []modelEntry
	for _, m := range s.catalog.GetAllModels() {
		if !s.modelRoutable(m.Provider, m.Model) {
			continue
		}
		data = append(data, modelEntry{ID: canonicalModelID(m.Provider, m.Model), Object: "model", Created: s.started.Unix(), OwnedBy: m.Provider, Provenance: string(m.Info.Provenance()), Health: string(m.HealthStatus), CooldownUntil: m.CooldownUntil, ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput, TokenCost: m.TokenCost, Thinking: m.Thinking, Vision: m.Vision, ToolUse: m.ToolUse, Effort: append([]string(nil), m.Effort...), CatalogSource: m.CatalogSource})
	}
	for _, list := range s.allModelLists() {
		members := s.functionalModelListMembers(list)
		if len(members) > 0 {
			data = append(data, modelEntry{ID: list.Name, Object: "model", Created: s.started.Unix(), OwnedBy: "ghrouter", Provenance: string(types.ModelProvenanceConfigured), CatalogSource: "generated", List: true, Members: members})
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) ModelSummaries() []ModelSummary {
	out := make([]ModelSummary, 0)
	for _, m := range s.catalog.GetAllModels() {
		out = append(out, ModelSummary{
			ID:            canonicalModelID(m.Provider, m.Model),
			OwnedBy:       m.Provider,
			Provenance:    string(m.Info.Provenance()),
			CostTier:      string(m.CostTier),
			Capabilities:  stringifyCaps(m.Capabilities),
			Slots:         stringifySlots(m.VirtualSlots),
			Health:        string(m.HealthStatus),
			LatencyMs:     m.LatencyP50.Milliseconds(),
			CooldownUntil: m.CooldownUntil,
			TokenCost:     m.TokenCost,
			MaxTokens:     m.MaxTokens,
			ContextWindow: m.ContextWindow,
			MaxOutput:     m.MaxOutput,
			Thinking:      m.Thinking,
			Vision:        m.Vision,
			ToolUse:       m.ToolUse,
			Effort:        append([]string(nil), m.Effort...),
			CatalogSource: m.CatalogSource,
		})
	}
	for _, list := range s.allModelLists() {
		members := s.functionalModelListMembers(list)
		if len(members) > 0 {
			out = append(out, ModelSummary{ID: list.Name, OwnedBy: "ghrouter", Provenance: string(types.ModelProvenanceConfigured), Health: "virtual", CatalogSource: "generated", List: true, Members: members})
		}
	}
	return out
}

func (s *Server) providerAuthenticated(providerName string) bool {
	for _, provider := range s.cfg.Providers {
		if provider != nil && provider.Name == providerName {
			return local_brain.AuthReason(provider) == ""
		}
	}
	return false
}

func (s *Server) FunctionalModelSummaries() []ModelSummary {
	all := s.ModelSummaries()
	out := make([]ModelSummary, 0, len(all))
	for _, model := range all {
		if model.List || s.modelRoutable(model.OwnedBy, strings.TrimPrefix(model.ID, model.OwnedBy+"/")) {
			out = append(out, model)
		}
	}
	return out
}

func (s *Server) functionalModelListMembers(list types.ModelList) []string {
	providerReady := make(map[string]bool, len(s.cfg.Providers))
	for _, provider := range s.cfg.Providers {
		if provider != nil {
			providerReady[provider.Name] = s.providerHealthy(provider.Name)
		}
	}
	members := make([]string, 0, len(list.Models))
	for _, reference := range list.Models {
		for _, leaf := range s.expandModelReferences(reference, make(map[string]bool)) {
			provider, model := s.resolveModelReference(leaf)
			if provider == "" {
				for _, candidate := range s.cfg.Providers {
					if candidate == nil || !candidate.Enabled {
						continue
					}
					if resolved := s.resolveModel(candidate, leaf); resolved != "" {
						provider, model = candidate.Name, resolved
						break
					}
				}
			}
			if provider == "" || model == "" || !providerReady[provider] {
				continue
			}
			if s.catalog != nil {
				entry := s.catalog.GetModel(canonicalModelID(provider, model))
				if entry == nil || entry.HealthStatus != health.HealthHealthy || s.catalog.IsInCooldown(canonicalModelID(provider, model)) {
					continue
				}
			}
			if !s.modelVerified(provider, model) {
				continue
			}
			if !containsString(members, leaf) {
				members = append(members, leaf)
			}
		}
	}
	return members
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *Server) modelListSummaries() []ModelListSummary {
	out := make([]ModelListSummary, 0, len(s.cfg.ModelLists))
	for _, list := range s.allModelLists() {
		members := s.functionalModelListMembers(list)
		if len(members) > 0 {
			out = append(out, ModelListSummary{Name: list.Name, Kind: list.Kind, Strategy: list.Strategy, Members: members})
		}
	}
	return out
}

func (s *Server) allModelLists() []types.ModelList {
	lists := append([]types.ModelList(nil), s.cfg.ModelLists...)
	lists = append(lists, s.controlPlaneLists()...)
	return lists
}

func (s *Server) controlPlaneLists() []types.ModelList {
	lists := make([]types.ModelList, 0, len(s.cfg.Pools)+len(s.cfg.Combos))
	for _, pool := range s.cfg.Pools {
		if pool.Enabled && pool.Name != "" {
			strategy := pool.Strategy
			if strategy == "" {
				strategy = "round-robin"
			}
			lists = append(lists, types.ModelList{Name: pool.Name, Kind: "pool", Models: append([]string(nil), pool.Members...), Strategy: strategy})
		}
	}
	for _, combo := range s.cfg.Combos {
		if combo.Enabled && combo.Name != "" {
			strategy := combo.Strategy
			if strategy == "" {
				strategy = "score"
			}
			lists = append(lists, types.ModelList{Name: combo.Name, Kind: "combo", Models: append([]string(nil), combo.Members...), Strategy: strategy})
		}
	}
	return lists
}

func (s *Server) controlPlaneSummaries() ([]ConnectionSummary, []PoolSummary, []ComboSummary) {
	connections := make([]ConnectionSummary, 0, len(s.cfg.Connections))
	for _, connection := range s.cfg.Connections {
		connections = append(connections, ConnectionSummary{Name: connection.Name, Provider: connection.Provider, Model: connection.Model, Enabled: connection.Enabled, Metadata: cloneStringMap(connection.Metadata)})
	}
	pools := make([]PoolSummary, 0, len(s.cfg.Pools))
	for _, pool := range s.cfg.Pools {
		pools = append(pools, PoolSummary{Name: pool.Name, Members: append([]string(nil), pool.Members...), Strategy: pool.Strategy, Enabled: pool.Enabled})
	}
	combos := make([]ComboSummary, 0, len(s.cfg.Combos))
	for _, combo := range s.cfg.Combos {
		combos = append(combos, ComboSummary{Name: combo.Name, Members: append([]string(nil), combo.Members...), Strategy: combo.Strategy, Judge: combo.Judge, Enabled: combo.Enabled})
	}
	return connections, pools, combos
}

func (s *Server) LiveSnapshot() LiveSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := LiveSnapshot{
		ListenPort:  s.cfg.ListenPort,
		StartedAt:   s.started,
		ClientKeys:  s.clientKeys.Masked(),
		Models:      s.FunctionalModelSummaries(),
		ModelLists:  s.modelListSummaries(),
		Slots:       s.slotSummaries(),
		Health:      s.healthSnapshotLocked(),
		Telemetry:   s.telemetry.snapshot(),
		Persistence: s.persistenceStatus(),
	}
	summary.Connections, summary.Pools, summary.Combos = s.controlPlaneSummaries()
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
		if strings.TrimSpace(p.CLIPath) == "" {
			healthState = "unavailable"
			authState = "missing CLI on PATH"
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
			Models:    s.visibleProviderModels(p.Name),
			Available: available,
			Health:    healthState,
			Auth:      authState,
			Account:   accountState,
			Discovery: p.Discovery,
		})
	}
	return summary
}

func (s *Server) persistenceStatus() string {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	if s.storageErr != "" {
		return "degraded: " + s.storageErr
	}
	if s.store != nil {
		return "sqlite"
	}
	return "disabled"
}

func (s *Server) setStore(store *storage.Store) {
	s.storageMu.Lock()
	s.store = store
	s.storageMu.Unlock()
}

func (s *Server) getStore() *storage.Store {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	return s.store
}

func (s *Server) setStorageError(err error) {
	if err == nil {
		return
	}
	s.storageMu.Lock()
	s.storageErr = err.Error()
	s.storageMu.Unlock()
}

func (s *Server) persistStorageState() error {
	store := s.getStore()
	if store == nil {
		return storage.ErrStoreClosed
	}
	providers := make([]storage.ProviderRecord, 0, len(s.cfg.Providers))
	for _, provider := range s.cfg.Providers {
		if provider == nil || !provider.Enabled {
			continue
		}
		authState := "ready"
		if reason := local_brain.AuthReason(provider); reason != "" {
			authState = reason
		}
		providers = append(providers, storage.ProviderRecord{
			ProviderID: provider.Name, CLIType: string(provider.Type), Executable: provider.CLIPath, AuthState: authState,
		})
	}
	models := make([]storage.ModelRecord, 0)
	for _, model := range s.catalog.GetAllModels() {
		models = append(models, storage.ModelRecord{
			ModelID: model.Provider + "/" + model.Model, ProviderID: model.Provider,
			Capabilities: stringifyCaps(model.Capabilities), Slots: stringifySlots(model.VirtualSlots),
			CatalogSource: model.CatalogSource, Effort: append([]string(nil), model.Effort...),
			HealthState: string(model.HealthStatus), MaxTokens: model.MaxTokens,
			TokenCost: model.TokenCost, ContextWindow: model.ContextWindow, MaxOutput: model.MaxOutput,
			Thinking: model.Thinking, Vision: model.Vision, ToolUse: model.ToolUse,
			CooldownUntil: model.CooldownUntil, FailureCount: model.FailureCount,
			ErrorRate: model.ErrorRate, LastHealthCheck: model.LastHealthCheck,
		})
	}
	if err := store.ReplaceCatalog(providers, models); err != nil {
		return err
	}
	connections := make([]storage.ConnectionRecord, 0, len(s.cfg.Connections))
	for _, connection := range s.cfg.Connections {
		connections = append(connections, storage.ConnectionRecord{Name: connection.Name, Provider: connection.Provider, Model: connection.Model, Enabled: connection.Enabled, Metadata: connection.Metadata})
	}
	pools := make([]storage.PoolRecord, 0, len(s.cfg.Pools))
	for _, pool := range s.cfg.Pools {
		pools = append(pools, storage.PoolRecord{Name: pool.Name, Members: append([]string(nil), pool.Members...), Strategy: pool.Strategy, Enabled: pool.Enabled})
	}
	combos := make([]storage.ComboRecord, 0, len(s.cfg.Combos))
	for _, combo := range s.cfg.Combos {
		combos = append(combos, storage.ComboRecord{Name: combo.Name, Members: append([]string(nil), combo.Members...), Strategy: combo.Strategy, Judge: combo.Judge, Enabled: combo.Enabled})
	}
	if err := store.ReplaceControlPlane(connections, pools, combos); err != nil {
		return err
	}
	payload, err := json.Marshal(s.cfg)
	if err != nil {
		return fmt.Errorf("marshal config snapshot: %w", err)
	}
	digest := sha256.Sum256(payload)
	path := os.Getenv("GHR_CONFIG")
	if path == "" {
		path = "runtime"
	}
	if err := store.RecordConfigSnapshot(storage.ConfigSnapshot{Checksum: "sha256:" + hex.EncodeToString(digest[:]), Path: path}); err != nil {
		return err
	}
	return nil
}

func (s *Server) PersistCurrentState() error {
	if s == nil || !s.cfg.Storage.Enabled {
		return nil
	}
	if store := s.getStore(); store != nil {
		return s.persistStorageState()
	}
	store, err := storage.Open(storage.Config{Enabled: true, Path: s.cfg.Storage.Path, RetentionDays: s.cfg.Storage.RetentionDays})
	if err != nil {
		return err
	}
	s.setStore(store)
	persistErr := s.persistStorageState()
	closeErr := store.Close()
	s.setStore(nil)
	if persistErr != nil {
		return persistErr
	}
	return closeErr
}

func (s *Server) recordAudit(action, actor string, details map[string]any) {
	if s == nil {
		return
	}
	store := s.getStore()
	if store == nil {
		return
	}
	if err := store.RecordAudit(action, actor, details); err != nil {
		s.setStorageError(err)
		observability.Logger("storage").Error("audit_write_failed", "action", action, "error", observability.PublicError(err))
	}
}

func (s *Server) restoreCatalogState(store *storage.Store) error {
	records, err := store.LoadModelCatalog()
	if err != nil {
		return err
	}
	for _, record := range records {
		s.catalog.RestoreModelMetadata(record.ModelID, record.CatalogSource, record.Capabilities, record.Effort, record.TokenCost, record.ContextWindow, record.MaxOutput, record.Thinking, record.Vision, record.ToolUse)
		s.catalog.RestoreModelState(record.ModelID, health.HealthStatus(record.HealthState), record.CooldownUntil, record.LastHealthCheck, record.FailureCount, record.ErrorRate)
	}
	return nil
}

func (s *Server) persistHealthSample(result health.HealthCheckResult) {
	store := s.getStore()
	if store == nil {
		return
	}
	sample := storage.HealthSample{ProviderID: result.Provider, Status: string(result.Status), LatencyMS: result.Latency.Milliseconds(), Error: observability.PublicError(result.Error), ObservedAt: result.Timestamp}
	if err := store.RecordHealthSample(sample); err != nil {
		s.setStorageError(err)
		observability.Logger("storage").Error("health_sample_write_failed", "provider", result.Provider, "error", observability.PublicError(err))
	}
}

func (s *Server) persistCurrentHealth() {
	if s.health == nil {
		return
	}
	for provider, result := range s.health.Snapshot().Providers {
		s.persistHealthSample(health.HealthCheckResult{
			Provider: provider, Status: result.Status, Latency: result.Latency,
			Error: result.Error, Timestamp: result.Timestamp,
		})
	}
}

func (s *Server) slotSummaries() map[string]ModelSummary {
	out := make(map[string]ModelSummary)
	for _, slot := range []catalog.VirtualSlot{
		catalog.SlotFastCode, catalog.SlotCheapChat, catalog.SlotStrongReason, catalog.SlotLongContext, catalog.SlotVision, catalog.SlotToolUse, catalog.SlotAuto,
	} {
		if m := s.catalog.GetModelBySlot(slot); m != nil {
			out[string(slot)] = ModelSummary{
				ID:            canonicalModelID(m.Provider, m.Model),
				OwnedBy:       m.Provider,
				Provenance:    string(m.Info.Provenance()),
				CostTier:      string(m.CostTier),
				Capabilities:  stringifyCaps(m.Capabilities),
				Slots:         stringifySlots(m.VirtualSlots),
				Health:        string(m.HealthStatus),
				LatencyMs:     m.LatencyP50.Milliseconds(),
				CooldownUntil: m.CooldownUntil,
				TokenCost:     m.TokenCost,
				MaxTokens:     m.MaxTokens,
				ContextWindow: m.ContextWindow,
				MaxOutput:     m.MaxOutput,
				Thinking:      m.Thinking,
				Vision:        m.Vision,
				ToolUse:       m.ToolUse,
				Effort:        append([]string(nil), m.Effort...),
				CatalogSource: m.CatalogSource,
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
		ModelCount:    len(s.FunctionalModelSummaries()),
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "alive"})
}

func (s *Server) handleReadiness(w http.ResponseWriter, _ *http.Request) {
	summary := s.healthSnapshot()
	ready := false
	for _, provider := range s.cfg.Providers {
		if provider == nil || !s.providerHealthy(provider.Name) {
			continue
		}
		state := s.health.GetHealth(provider.Name)
		if state != nil && (state.Status == health.HealthHealthy || state.Status == health.HealthDegraded) {
			ready = true
			break
		}
	}
	if ready && len(s.FunctionalModelSummaries()) == 0 {
		ready = false
	}
	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": state, "health": summary, "persistence": s.persistenceStatus()})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bootstrapper, err := local_brain.NewBootstrapper()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bootstrap_failed", err.Error())
		return
	}
	report, _ := bootstrapper.Check(s.cfg.Providers)
	response := LiveResponse{Snapshot: s.LiveSnapshot(), Bootstrap: report.Summary()}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
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
			state.Error = publicProviderError(result.Error)
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
	fmt.Fprintf(w, "ghrouter: OpenAI-compatible router for gh copilot.\nGET /v1/models, POST /v1/chat/completions, POST /v1/responses, POST /v1/messages, GET/PUT /v1/control-plane, GET/PUT/DELETE /v1/control-plane/{kind}/{name}, GET /health\n")
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
		types.ProviderCursor: "cu/",
	}
	return m[provider]
}

func canonicalModelID(provider, model string) string {
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)
	if model == "" {
		return ""
	}
	for _, prefix := range []string{"cc/", "cx/", "oc/", "mi/", "pi/", "cu/"} {
		if strings.HasPrefix(model, prefix) {
			return model
		}
	}
	if provider == "" {
		return model
	}
	if prefix := canonicalPrefixForProviderName(provider); prefix != "" {
		if strings.HasPrefix(model, provider+"/") {
			model = strings.TrimPrefix(model, provider+"/")
		}
		if head, tail, ok := strings.Cut(model, "/"); ok && strings.EqualFold(head, provider) {
			model = tail
		}
		return prefix + model
	}
	if strings.HasPrefix(model, provider+"/") {
		return model
	}
	if strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

func canonicalPrefixForProviderName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "claude-code":
		return "cc/"
	case "codex":
		return "cx/"
	case "opencode":
		return "oc/"
	case "mimo":
		return "mi/"
	case "pi":
		return "pi/"
	case "cursor":
		return "cu/"
	default:
		return ""
	}
}

func buildCatalogModels(p *types.Provider) []*catalog.ModelEntry {
	models := make([]*catalog.ModelEntry, 0, len(p.Models))
	accountState := account.Load(p)
	weight := account.Weight(accountState)
	for _, model := range p.Models {
		metadata := p.ModelInfo[model]
		metadata.Provider = p.Name
		metadata.Model = model
		if strings.TrimSpace(metadata.Source) == "" {
			metadata.Source = "configured"
		}
		status := health.HealthHealthy
		verifiedAt := metadata.VerifiedAt
		if metadata.HealthStatus != "" {
			status = health.HealthStatus(metadata.HealthStatus)
			if metadata.HealthStatus == "failed" && !metadata.CooldownUntil.IsZero() {
				status = health.HealthCooldown
			} else if metadata.HealthStatus == "failed" {
				status = health.HealthUnhealthy
			}
		}
		if !metadata.CooldownUntil.IsZero() && time.Now().Before(metadata.CooldownUntil) {
			status = health.HealthCooldown
		} else if status == health.HealthCooldown {
			status = health.HealthUnknown
			verifiedAt = time.Time{}
		} else if status == health.HealthUnhealthy && !metadata.CooldownUntil.IsZero() {
			status = health.HealthUnknown
			verifiedAt = time.Time{}
		}
		capabilities := modelCapabilities(model, metadata)
		models = append(models, &catalog.ModelEntry{
			ID:              canonicalModelID(p.Name, model),
			Provider:        p.Name,
			Model:           model,
			HealthStatus:    status,
			Capabilities:    capabilities,
			CostTier:        modelCostTier(metadata),
			MaxTokens:       p.MaxTokens,
			TokenCost:       metadata.TokenCost,
			ContextWindow:   metadata.ContextWindow,
			MaxOutput:       metadata.MaxOutput,
			Thinking:        metadata.Thinking,
			Vision:          metadata.Vision,
			ToolUse:         metadata.ToolUse,
			Effort:          append([]string(nil), metadata.Effort...),
			CatalogSource:   metadata.Source,
			Info:            metadata,
			CooldownUntil:   metadata.CooldownUntil,
			LastHealthCheck: verifiedAt,
			ProviderWeight:  weight,
		})
	}
	return models
}

func modelCapabilities(model string, metadata types.ModelInfo) []catalog.CapabilityTag {
	capabilities := make([]catalog.CapabilityTag, 0, 6)
	lowerModel := strings.ToLower(model)
	if strings.Contains(lowerModel, "fast") || strings.Contains(lowerModel, "instant") || strings.Contains(lowerModel, "latency") {
		capabilities = append(capabilities, catalog.CapabilityFast)
	}
	add := func(capability catalog.CapabilityTag) {
		for _, existing := range capabilities {
			if existing == capability {
				return
			}
		}
		capabilities = append(capabilities, capability)
	}
	if metadata.ContextWindow >= 128_000 {
		add(catalog.CapabilityLongContext)
	}
	if metadata.Thinking {
		add(catalog.CapabilityAutonomous)
		add(catalog.CapabilityReasoning)
		add(catalog.CapabilityCode)
	}
	if metadata.Vision {
		add(catalog.CapabilityVision)
	}
	if metadata.ToolUse {
		add(catalog.CapabilityToolUse)
	}
	if strings.Contains(strings.ToLower(model), "code") || strings.Contains(strings.ToLower(model), "coder") {
		add(catalog.CapabilityCode)
	}
	return capabilities
}

func modelCostTier(metadata types.ModelInfo) catalog.CostTier {
	if metadata.TokenCost == 0 && metadata.ContextWindow == 0 {
		return catalog.CostFree
	}
	if metadata.TokenCost <= 0 {
		return catalog.CostStandard
	}
	if metadata.TokenCost < 500 {
		return catalog.CostCheap
	}
	if metadata.TokenCost >= 5000 {
		return catalog.CostPremium
	}
	return catalog.CostStandard
}
