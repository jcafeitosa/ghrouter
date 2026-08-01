package catalog

import (
	"context"
	"sync"
	"time"

	"ghrouter/internal/health"
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
	wg               sync.WaitGroup
	ttl              time.Duration
}

func NewCatalog(healthLoop *health.Loop, ttl time.Duration) *Catalog {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	c := &Catalog{
		models:     make(map[string]*ModelEntry),
		byProvider: make(map[string][]string),
		bySlot:     make(map[VirtualSlot]string),
		healthLoop: healthLoop,
		ttl:        ttl,
		stopCh:     make(chan struct{}),
	}
	// Set up health change callback
	if healthLoop != nil {
		healthLoop.SetOnChange(c.onHealthChange)
	}
	return c
}

func (c *Catalog) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.runReclassify(ctx)
}

func (c *Catalog) Stop() {
	close(c.stopCh)
	if c.reclassifyTicker != nil {
		c.reclassifyTicker.Stop()
	}
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

	var ids []string
	for _, m := range models {
		id := c.modelKey(provider, m.Model)
		c.models[id] = m
		ids = append(ids, id)
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
	return provider + "/" + model
}

func (c *Catalog) GetModel(id string) *ModelEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.models[id]; ok {
		// Return copy
		copy := *m
		return &copy
	}
	return nil
}

func (c *Catalog) GetModelBySlot(slot VirtualSlot) *ModelEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id, ok := c.bySlot[slot]; ok {
		if m, ok := c.models[id]; ok {
			copy := *m
			return &copy
		}
	}
	return nil
}

func (c *Catalog) GetModelsByProvider(provider string) []*ModelEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
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
	c.mu.RLock()
	defer c.mu.RUnlock()
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
	if m, ok := c.models[modelID]; ok {
		return m.Provider
	}
	return ""
}

func (c *Catalog) GetHealth(modelID string) health.HealthStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.models[modelID]; ok {
		return m.HealthStatus
	}
	return health.HealthUnknown
}

func (c *Catalog) IsInCooldown(modelID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isInCooldownLocked(modelID)
}

func (c *Catalog) isInCooldownLocked(modelID string) bool {
	if m, ok := c.models[modelID]; ok {
		return time.Now().Before(m.CooldownUntil)
	}
	return false
}

func (c *Catalog) SetCooldown(modelID string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.models[modelID]; ok {
		m.CooldownUntil = until
	}
}

func (c *Catalog) onHealthChange(provider string, old, new health.HealthStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, id := range c.byProvider[provider] {
		if m, ok := c.models[id]; ok {
			m.HealthStatus = new
		}
	}
	c.rebuildSlots()
}

func (c *Catalog) rebuildSlots() {
	// Clear slots
	for slot := range c.bySlot {
		delete(c.bySlot, slot)
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
	bestScore := -1.0

	for _, m := range c.models {
		// Skip unhealthy/cooldown models
		if m.HealthStatus != health.HealthHealthy {
			continue
		}
		if c.isInCooldownLocked(c.modelKey(m.Provider, m.Model)) {
			continue
		}

		score := c.scoreForSlot(m, slot)
		if score > bestScore {
			bestScore = score
			best = m
		}
	}

	if best != nil {
		c.bySlot[slot] = c.modelKey(best.Provider, best.Model)
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
