package catalog

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/health"
	"ghrouter/internal/types"
)

// CapabilityTag represents a model capability
type CapabilityTag string

const (
	CapabilityFast        CapabilityTag = "fast"
	CapabilityCheap       CapabilityTag = "cheap"
	CapabilityCode        CapabilityTag = "code"
	CapabilityLongContext CapabilityTag = "long-context"
	CapabilityToolUse     CapabilityTag = "tool-use"
	CapabilityVision      CapabilityTag = "vision"
	CapabilityAutonomous  CapabilityTag = "autonomous"
	CapabilityReasoning   CapabilityTag = "reasoning"
)

// CostTier represents the cost tier of a model
type CostTier string

const (
	CostFree     CostTier = "free"
	CostCheap    CostTier = "cheap"
	CostStandard CostTier = "standard"
	CostPremium  CostTier = "premium"
)

// VirtualSlot represents a virtual model slot for gh copilot
type VirtualSlot string

const (
	SlotFastCode     VirtualSlot = "fast-code"
	SlotCheapChat    VirtualSlot = "cheap-chat"
	SlotStrongReason VirtualSlot = "strong-reasoning"
	SlotLongContext  VirtualSlot = "long-context"
	SlotVision       VirtualSlot = "vision"
	SlotToolUse      VirtualSlot = "tool-use"
	SlotAuto         VirtualSlot = "auto"
)

// ModelEntry represents a model in the catalog
type ModelEntry struct {
	ID              string
	Provider        string
	Model           string
	Info            types.ModelInfo
	Capabilities    []CapabilityTag
	CostTier        CostTier
	VirtualSlots    []VirtualSlot
	HealthStatus    health.HealthStatus
	LatencyP50      time.Duration
	LatencyP95      time.Duration
	ErrorRate       float64
	LastHealthCheck time.Time
	CooldownUntil   time.Time
	TokenCost       int // cost per 1k tokens in micro-units
	MaxTokens       int
	ContextWindow   int
	MaxOutput       int
	Thinking        bool
	Vision          bool
	ToolUse         bool
	Effort          []string
	CatalogSource   string
	ProviderWeight  float64
	FailureCount    int
}

// Catalog maintains a live catalog of available models
type Catalog struct {
	mu               sync.RWMutex
	models           map[string]*ModelEntry // model ID -> entry
	byProvider       map[string][]string    // provider -> model IDs
	bySlot           map[VirtualSlot]string // slot -> best model ID
	healthLoop       *health.Loop
	reclassifyTicker *time.Ticker
	stopCh           chan struct{}
	stopOnce         sync.Once
	startOnce        sync.Once
	wg               sync.WaitGroup
	ttl              time.Duration
	cooldownEnabled  bool
	cooldownDefault  time.Duration
	cooldownMax      time.Duration
	healthSink       func(provider string, old, new health.HealthStatus)
}

var providerPrefixes = map[string]string{
	"claude-code": "cc",
	"codex":       "cx",
	"opencode":    "oc",
	"mimo":        "mi",
	"pi":          "pi",
	"cursor":      "cu",
}

var canonicalPrefixes = []string{"cc/", "cx/", "oc/", "mi/", "pi/", "cu/"}

func NewCatalog(healthLoop *health.Loop, ttl time.Duration) *Catalog {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	c := &Catalog{
		models:          make(map[string]*ModelEntry),
		byProvider:      make(map[string][]string),
		bySlot:          make(map[VirtualSlot]string),
		healthLoop:      healthLoop,
		ttl:             ttl,
		cooldownEnabled: true,
		cooldownDefault: 30 * time.Second,
		cooldownMax:     10 * time.Minute,
		stopCh:          make(chan struct{}),
	}
	// Set up health change callback
	if healthLoop != nil {
		healthLoop.SetOnChange(c.onHealthChange)
	}
	return c
}

func (c *Catalog) SetCooldownPolicy(enabled bool, defaultDuration, maxDuration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cooldownEnabled = enabled
	if defaultDuration > 0 {
		c.cooldownDefault = defaultDuration
	}
	if maxDuration > 0 {
		c.cooldownMax = maxDuration
	}
	if c.cooldownMax < c.cooldownDefault {
		c.cooldownMax = c.cooldownDefault
	}
	if !enabled {
		for _, model := range c.models {
			if model.HealthStatus == health.HealthCooldown {
				model.HealthStatus = health.HealthDegraded
			}
			model.CooldownUntil = time.Time{}
		}
		c.rebuildSlots()
	}
}

func (c *Catalog) SetHealthSampleSink(sink func(provider string, old, new health.HealthStatus)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.healthSink = sink
}

func (c *Catalog) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		c.wg.Add(1)
		go c.runReclassify(ctx)
	})
}

func (c *Catalog) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.wg.Wait()
}

func (c *Catalog) runReclassify(ctx context.Context) {
	defer c.wg.Done()
	c.reclassifyTicker = time.NewTicker(5 * time.Minute)
	defer c.reclassifyTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-c.reclassifyTicker.C:
			c.reclassify()
		}
	}
}

func (c *Catalog) RegisterProvider(provider string, models []*ModelEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	previous := make(map[string]*ModelEntry, len(c.byProvider[provider]))
	for _, id := range c.byProvider[provider] {
		if model := c.models[id]; model != nil {
			copy := *model
			previous[id] = &copy
		}
		delete(c.models, id)
	}
	delete(c.byProvider, provider)
	for _, m := range models {
		if m == nil {
			continue
		}
		id := canonicalModelID(provider, m.ID)
		if id == "" {
			id = canonicalModelID(provider, m.Model)
		}
		if id == "" {
			continue
		}
		m.ID = id
		if strings.TrimSpace(m.Model) == "" {
			m.Model = id
		}
		if old := previous[id]; old != nil {
			if m.HealthStatus == health.HealthHealthy && old.HealthStatus != health.HealthHealthy {
				m.HealthStatus = old.HealthStatus
			}
			if m.CooldownUntil.IsZero() {
				m.CooldownUntil = old.CooldownUntil
			}
			if m.LastHealthCheck.IsZero() {
				m.LastHealthCheck = old.LastHealthCheck
			}
			m.FailureCount = old.FailureCount
			m.ErrorRate = old.ErrorRate
		}
		c.models[id] = m
		c.byProvider[provider] = append(c.byProvider[provider], id)
	}
	c.rebuildSlots()
}

func (c *Catalog) UnregisterProvider(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ids, ok := c.byProvider[provider]; ok {
		for _, id := range ids {
			delete(c.models, id)
		}
		delete(c.byProvider, provider)
	}
	c.rebuildSlots()
}

func (c *Catalog) modelKey(provider, model string) string {
	return canonicalModelID(provider, model)
}

func (c *Catalog) GetModel(id string) *ModelEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(time.Now())
	if m, ok := c.models[canonicalModelID("", id)]; ok {
		copy := *m
		return &copy
	}
	return nil
}

func (c *Catalog) GetModelBySlot(slot VirtualSlot) *ModelEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(time.Now())
	if id, ok := c.bySlot[slot]; ok {
		if m, ok := c.models[id]; ok {
			copy := *m
			return &copy
		}
	}
	return nil
}

func (c *Catalog) BestHealthyModel() *ModelEntry {
	return c.BestHealthyModelForSlot(SlotAuto)
}

func (c *Catalog) BestHealthyModelForSlot(slot VirtualSlot) *ModelEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(time.Now())
	return c.bestHealthyModelForSlotLocked(slot)
}

func (c *Catalog) bestHealthyModelForSlotLocked(slot VirtualSlot) *ModelEntry {
	var best *ModelEntry
	bestScore := math.Inf(-1)
	for _, m := range c.models {
		if m.HealthStatus != health.HealthHealthy {
			continue
		}
		if c.isInCooldownLocked(c.modelKey(m.Provider, m.ID)) {
			continue
		}
		score := c.scoreForSlot(m, slot)
		if score > bestScore {
			bestScore = score
			best = m
		}
	}
	if best == nil {
		return nil
	}
	copy := *best
	return &copy
}

func (c *Catalog) GetModelsByProvider(provider string) []*ModelEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(time.Now())
	var out []*ModelEntry
	for _, id := range c.byProvider[provider] {
		if m, ok := c.models[id]; ok {
			copy := *m
			out = append(out, &copy)
		}
	}
	return out
}

func (c *Catalog) GetAllModels() []*ModelEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(time.Now())
	out := make([]*ModelEntry, 0, len(c.models))
	for _, m := range c.models {
		copy := *m
		out = append(out, &copy)
	}
	return out
}

func (c *Catalog) GetProvider(modelID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.models[canonicalModelID("", modelID)]; ok {
		return m.Provider
	}
	return ""
}

func (c *Catalog) GetHealth(modelID string) health.HealthStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.models[canonicalModelID("", modelID)]; ok {
		return m.HealthStatus
	}
	return health.HealthUnknown
}

func (c *Catalog) IsInCooldown(modelID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(time.Now())
	return c.isInCooldownLocked(canonicalModelID("", modelID))
}

func (c *Catalog) NeedsVerification(modelID string, now time.Time, interval time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshExpiredCooldownsLocked(now)
	modelID = canonicalModelID("", modelID)
	m, ok := c.models[modelID]
	if !ok || c.isInCooldownLocked(modelID) {
		return false
	}
	if m.HealthStatus == health.HealthUnknown {
		return true
	}
	if interval <= 0 || m.LastHealthCheck.IsZero() {
		return true
	}
	return now.Sub(m.LastHealthCheck) >= interval
}

func (c *Catalog) refreshExpiredCooldownsLocked(now time.Time) {
	changed := false
	for _, m := range c.models {
		if m.HealthStatus == health.HealthCooldown && !now.Before(m.CooldownUntil) {
			m.HealthStatus = health.HealthUnknown
			m.CooldownUntil = time.Time{}
			m.LastHealthCheck = time.Time{}
			changed = true
		}
	}
	if changed {
		c.rebuildSlots()
	}
}

func (c *Catalog) isInCooldownLocked(modelID string) bool {
	return c.isInCooldownAtLocked(modelID, time.Now())
}

func (c *Catalog) isInCooldownAtLocked(modelID string, now time.Time) bool {
	if m, ok := c.models[modelID]; ok {
		return !m.CooldownUntil.IsZero() && now.Before(m.CooldownUntil)
	}
	return false
}

func (c *Catalog) SetCooldown(modelID string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	modelID = canonicalModelID("", modelID)
	if m, ok := c.models[modelID]; ok {
		m.CooldownUntil = until
		if until.After(time.Now()) {
			m.HealthStatus = health.HealthCooldown
		} else {
			m.CooldownUntil = time.Time{}
			m.HealthStatus = health.HealthUnknown
			m.LastHealthCheck = time.Time{}
		}
		c.rebuildSlots()
	}
}

func (c *Catalog) RestoreModelState(modelID string, status health.HealthStatus, cooldownUntil, lastHealthCheck time.Time, failureCount int, errorRate float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	modelID = canonicalModelID("", modelID)
	m, ok := c.models[modelID]
	if !ok {
		return
	}
	m.FailureCount = failureCount
	m.ErrorRate = errorRate
	m.LastHealthCheck = lastHealthCheck
	if !cooldownUntil.IsZero() && time.Now().Before(cooldownUntil) {
		m.CooldownUntil = cooldownUntil
		m.HealthStatus = health.HealthCooldown
	} else if status == health.HealthCooldown {
		m.CooldownUntil = time.Time{}
		m.HealthStatus = health.HealthUnknown
		m.LastHealthCheck = time.Time{}
	} else if status != health.HealthUnknown {
		m.HealthStatus = status
	}
	c.rebuildSlots()
}

func (c *Catalog) RestoreModelMetadata(modelID, source string, capabilities []string, effort []string, tokenCost, contextWindow, maxOutput int, thinking, vision, toolUse bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	modelID = canonicalModelID("", modelID)
	m, ok := c.models[modelID]
	if !ok {
		return
	}
	m.CatalogSource = source
	m.Info.Source = source
	m.Capabilities = make([]CapabilityTag, 0, len(capabilities))
	for _, capability := range capabilities {
		m.Capabilities = append(m.Capabilities, CapabilityTag(capability))
	}
	m.Effort = append([]string(nil), effort...)
	m.TokenCost = tokenCost
	m.ContextWindow = contextWindow
	m.MaxOutput = maxOutput
	m.Thinking = thinking
	m.Vision = vision
	m.ToolUse = toolUse
	c.rebuildSlots()
}

func (c *Catalog) RecordFailure(modelID string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	modelID = canonicalModelID("", modelID)
	m, ok := c.models[modelID]
	if !ok {
		return
	}
	m.FailureCount++
	m.ErrorRate = m.ErrorRate*0.7 + 0.3
	if m.ErrorRate > 1 {
		m.ErrorRate = 1
	}
	if !c.cooldownEnabled {
		m.HealthStatus = health.HealthDegraded
		c.rebuildSlots()
		return
	}
	delay := c.cooldownDefault
	for i := 1; i < m.FailureCount && delay < c.cooldownMax; i++ {
		delay *= 2
	}
	if delay > c.cooldownMax {
		delay = c.cooldownMax
	}
	m.CooldownUntil = now.Add(delay)
	m.HealthStatus = health.HealthCooldown
	c.rebuildSlots()
}

func (c *Catalog) RecordProviderFailure(provider string, now, restoreAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.byProvider[provider] {
		m, ok := c.models[id]
		if !ok {
			continue
		}
		m.FailureCount++
		m.ErrorRate = m.ErrorRate*0.7 + 0.3
		if !c.cooldownEnabled {
			m.HealthStatus = health.HealthDegraded
			continue
		}
		delay := c.cooldownDefault
		for i := 1; i < m.FailureCount && delay < c.cooldownMax; i++ {
			delay *= 2
		}
		if delay > c.cooldownMax {
			delay = c.cooldownMax
		}
		until := now.Add(delay)
		if restoreAt.After(until) {
			until = restoreAt
		}
		m.CooldownUntil = until
		m.HealthStatus = health.HealthCooldown
	}
	c.rebuildSlots()
}

func (c *Catalog) RecordSuccess(modelID string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	modelID = canonicalModelID("", modelID)
	m, ok := c.models[modelID]
	if !ok {
		return
	}
	m.FailureCount = 0
	m.ErrorRate *= 0.5
	m.CooldownUntil = time.Time{}
	m.HealthStatus = health.HealthHealthy
	m.LastHealthCheck = now
	c.rebuildSlots()
}

func (c *Catalog) onHealthChange(provider string, old, new health.HealthStatus) {
	c.mu.Lock()

	for _, id := range c.byProvider[provider] {
		if m, ok := c.models[id]; ok {
			if m.HealthStatus == health.HealthCooldown || m.HealthStatus == health.HealthUnknown {
				continue
			}
			if new == health.HealthHealthy {
				continue
			}
			m.HealthStatus = new
		}
	}
	c.rebuildSlots()
	sink := c.healthSink
	c.mu.Unlock()
	if sink != nil && old != new {
		sink(provider, old, new)
	}
}

func (c *Catalog) rebuildSlots() {
	// Clear slots
	for slot := range c.bySlot {
		delete(c.bySlot, slot)
	}
	for _, model := range c.models {
		model.VirtualSlots = nil
	}

	// Assign best model for each slot
	for slot := range map[VirtualSlot]bool{
		SlotFastCode: true, SlotCheapChat: true, SlotStrongReason: true,
		SlotLongContext: true, SlotVision: true, SlotToolUse: true, SlotAuto: true,
	} {
		c.assignSlot(slot)
	}
}

func (c *Catalog) assignSlot(slot VirtualSlot) {
	var best *ModelEntry
	bestScore := math.Inf(-1)

	for _, m := range c.models {
		// Skip unhealthy/cooldown models
		if m.HealthStatus != health.HealthHealthy {
			continue
		}
		if c.isInCooldownLocked(c.modelKey(m.Provider, m.ID)) {
			continue
		}

		score := c.scoreForSlot(m, slot)
		if score > bestScore {
			bestScore = score
			best = m
		}
	}

	if best != nil {
		c.bySlot[slot] = c.modelKey(best.Provider, best.ID)
		best.VirtualSlots = append(best.VirtualSlots, slot)
	}
}

func (c *Catalog) scoreForSlot(m *ModelEntry, slot VirtualSlot) float64 {
	score := 0.0

	// Capability matching
	switch slot {
	case SlotFastCode:
		if hasCap(m, CapabilityFast) {
			score += 50
		}
		if hasCap(m, CapabilityCode) {
			score += 30
		}
	case SlotCheapChat:
		if hasCap(m, CapabilityCheap) {
			score += 50
		}
		if m.CostTier == CostFree {
			score += 50
		} else if m.CostTier == CostCheap {
			score += 30
		}
	case SlotStrongReason:
		if hasCap(m, CapabilityAutonomous) {
			score += 40
		}
		if hasCap(m, CapabilityCode) {
			score += 20
		}
	case SlotLongContext:
		if hasCap(m, CapabilityLongContext) {
			score += 50
		}
	case SlotVision:
		if hasCap(m, CapabilityVision) {
			score += 50
		}
	case SlotToolUse:
		if hasCap(m, CapabilityToolUse) {
			score += 50
		}
	case SlotAuto:
		score += 10 // baseline
	}

	// Latency bonus (lower is better)
	if m.LatencyP50 > 0 {
		score += float64(1000-m.LatencyP50.Milliseconds()) * 0.01
		if score < 0 {
			score = 0
		}
	}

	// Cost penalty (lower tier = better)
	switch m.CostTier {
	case CostFree:
		score += 20
	case CostCheap:
		score += 10
	case CostStandard:
		score += 0
	case CostPremium:
		score -= 10
	}

	// Error rate penalty
	score -= m.ErrorRate * 100

	if m.ProviderWeight > 0 {
		score *= m.ProviderWeight
	}

	return score
}

func hasCap(m *ModelEntry, cap CapabilityTag) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

func (c *Catalog) reclassify() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, m := range c.models {
		// Update capability tags based on observed performance
		if m.LatencyP50 < 2*time.Second {
			addCap(m, CapabilityFast)
		} else {
			removeCap(m, CapabilityFast)
		}

		if m.TokenCost < 500 { // < $0.50/1k tokens
			addCap(m, CapabilityCheap)
		}

		if m.MaxTokens >= 100000 {
			addCap(m, CapabilityLongContext)
		}

		// Auto re-evaluate slot assignments
	}
	c.rebuildSlots()
}

func addCap(m *ModelEntry, cap CapabilityTag) {
	for _, c := range m.Capabilities {
		if c == cap {
			return
		}
	}
	m.Capabilities = append(m.Capabilities, cap)
}

func removeCap(m *ModelEntry, cap CapabilityTag) {
	for i, c := range m.Capabilities {
		if c == cap {
			m.Capabilities = append(m.Capabilities[:i], m.Capabilities[i+1:]...)
			return
		}
	}
}

func canonicalModelID(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	for _, prefix := range canonicalPrefixes {
		if strings.HasPrefix(model, prefix) {
			return model
		}
	}
	if provider == "" {
		if head, tail, ok := strings.Cut(model, "/"); ok {
			if prefix, ok := providerPrefixes[strings.ToLower(head)]; ok && prefix != "" {
				return prefix + "/" + tail
			}
		}
		return model
	}
	prefix, ok := providerPrefixes[strings.ToLower(provider)]
	if !ok || prefix == "" {
		if strings.Contains(model, "/") {
			return model
		}
		return provider + "/" + model
	}
	if strings.HasPrefix(model, provider+"/") {
		model = strings.TrimPrefix(model, provider+"/")
	}
	if head, tail, ok := strings.Cut(model, "/"); ok && head == strings.ToLower(provider) {
		model = tail
	}
	return prefix + "/" + model
}
