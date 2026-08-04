package resourcegov

import (
	"context"
	"sync"
	"time"
)

type State string

const (
	StateNormal    State = "normal"
	StateDegraded  State = "degraded"
	StateCritical  State = "critical"
	StateEmergency State = "emergency"
)

type MetricStatus string

const (
	MetricValid       MetricStatus = "valid"
	MetricUnavailable MetricStatus = "unavailable"
	MetricStale       MetricStatus = "stale"
	MetricError       MetricStatus = "error"
)

type Reading struct {
	Status MetricStatus `json:"status"`
	Value  float64      `json:"value"`
}

type Sample struct {
	CapturedAt       time.Time
	HostCPU          Reading
	HostMemory       Reading
	ProcessRSS       Reading
	GPUVRAM          Reading
	UnifiedMemory    Reading
	DiskPressure     Reading
	ProcessPressure  Reading
	SharedUnifiedMem bool
}

type RequestClass string

const (
	RequestClassUser    RequestClass = "user_generation"
	RequestClassBrain   RequestClass = "brain_generation"
	RequestClassControl RequestClass = "internal_control"
	RequestClassHealth  RequestClass = "health"
	RequestClassTUI     RequestClass = "tui"
)

type Reason string

const (
	ReasonNone               Reason = ""
	ReasonSamplerUnavailable Reason = "sampler_unavailable"
	ReasonSamplerError       Reason = "sampler_error"
	ReasonSampleStale        Reason = "sample_stale"
	ReasonPressure           Reason = "pressure"
	ReasonUserShed           Reason = "user_shed"
	ReasonBrainExclusive     Reason = "brain_exclusive"
	ReasonEmergency          Reason = "emergency"
)

type AdmissionRequest struct {
	Class           RequestClass
	InternalControl bool
	BrainExplicit   bool
	BrainAuxiliary  bool
	Stream          bool
}

type OverloadError struct {
	Code       string
	Reason     Reason
	RetryAfter time.Duration
}

func (e OverloadError) Error() string {
	if e.Reason == "" {
		return "resource overloaded"
	}
	return string(e.Reason)
}

type AdmissionDecision struct {
	Allowed   bool
	State     State
	Reason    Reason
	Reduced   bool
	BrainOnly bool
	Error     OverloadError
}

type Config struct {
	SampleFreshness  time.Duration
	EnterHold        time.Duration
	RecoverHold      time.Duration
	DegradedEnter    float64
	CriticalEnter    float64
	EmergencyEnter   float64
	DegradedRecover  float64
	CriticalRecover  float64
	EmergencyRecover float64
	UserMaxInFlight  int
	BrainMaxInFlight int
}

type Clock interface {
	Now() time.Time
}

type Sampler interface {
	Sample(context.Context) (Sample, error)
}

type Governor struct {
	cfg           Config
	sampler       Sampler
	clock         Clock
	mu            sync.Mutex
	state         State
	pending       State
	pendingSince  time.Time
	userInFlight  int
	brainInFlight int
	notify        chan struct{}
	lastSample    Sample
	lastErr       error
}

type Snapshot struct {
	State            State      `json:"state"`
	ObservedAt       *time.Time `json:"observed_at,omitempty"`
	HostCPU          Reading    `json:"host_cpu"`
	HostMemory       Reading    `json:"host_memory"`
	ProcessRSS       Reading    `json:"process_rss"`
	GPUVRAM          Reading    `json:"gpu_vram"`
	UnifiedMemory    Reading    `json:"unified_memory"`
	DiskPressure     Reading    `json:"disk_pressure"`
	ProcessPressure  Reading    `json:"process_pressure"`
	SharedUnifiedMem bool       `json:"shared_unified_memory"`
	Error            string     `json:"error,omitempty"`
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func New(cfg Config, sampler Sampler, clock Clock) *Governor {
	if cfg.SampleFreshness <= 0 {
		cfg.SampleFreshness = 5 * time.Second
	}
	if cfg.EnterHold < 0 {
		cfg.EnterHold = 0
	}
	if cfg.RecoverHold < 0 {
		cfg.RecoverHold = 0
	}
	if cfg.DegradedEnter == 0 {
		cfg.DegradedEnter = 0.70
	}
	if cfg.CriticalEnter == 0 {
		cfg.CriticalEnter = 0.85
	}
	if cfg.EmergencyEnter == 0 {
		cfg.EmergencyEnter = 0.99
	}
	if cfg.DegradedRecover == 0 {
		cfg.DegradedRecover = 0.60
	}
	if cfg.CriticalRecover == 0 {
		cfg.CriticalRecover = 0.78
	}
	if cfg.EmergencyRecover == 0 {
		cfg.EmergencyRecover = 0.90
	}
	if cfg.UserMaxInFlight <= 0 {
		cfg.UserMaxInFlight = 4
	}
	if cfg.BrainMaxInFlight <= 0 {
		cfg.BrainMaxInFlight = 1
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Governor{cfg: cfg, sampler: sampler, clock: clock, state: StateNormal, notify: make(chan struct{})}
}

func (g *Governor) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

func (g *Governor) Snapshot() Snapshot {
	if g == nil {
		return Snapshot{State: StateEmergency}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	snapshot := Snapshot{
		State:            g.state,
		HostCPU:          g.lastSample.HostCPU,
		HostMemory:       g.lastSample.HostMemory,
		ProcessRSS:       g.lastSample.ProcessRSS,
		GPUVRAM:          g.lastSample.GPUVRAM,
		UnifiedMemory:    g.lastSample.UnifiedMemory,
		DiskPressure:     g.lastSample.DiskPressure,
		ProcessPressure:  g.lastSample.ProcessPressure,
		SharedUnifiedMem: g.lastSample.SharedUnifiedMem,
	}
	if !g.lastSample.CapturedAt.IsZero() {
		observedAt := g.lastSample.CapturedAt
		snapshot.ObservedAt = &observedAt
	}
	if g.lastErr != nil {
		snapshot.Error = g.lastErr.Error()
	}
	return snapshot
}

func (g *Governor) Observe(ctx context.Context) (State, error) {
	if g == nil || g.sampler == nil {
		return StateEmergency, OverloadError{Code: "resource_overloaded", Reason: ReasonSamplerUnavailable, RetryAfter: time.Second}
	}
	sample, err := g.sampler.Sample(ctx)
	now := g.clock.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastSample = sample
	g.lastErr = err
	if err != nil {
		g.transitionLocked(StateEmergency, ReasonSamplerError, now)
		return g.state, err
	}
	pressure, reason, ok := g.pressureLocked(sample, now)
	if !ok {
		g.transitionLocked(StateEmergency, reason, now)
		return g.state, nil
	}
	desired := g.desiredStateLocked(pressure)
	g.advanceLocked(desired, now)
	return g.state, nil
}

func (g *Governor) Admit(ctx context.Context, req AdmissionRequest) (AdmissionDecision, func(), error) {
	_ = ctx
	g.mu.Lock()
	defer g.mu.Unlock()

	decision := AdmissionDecision{State: g.state}
	switch req.Class {
	case RequestClassHealth, RequestClassTUI, RequestClassControl:
		decision.Allowed = true
		return decision, nil, nil
	case RequestClassBrain:
		return g.admitBrainLocked(req, decision)
	default:
		return g.admitUserLocked(req, decision)
	}
}

func (g *Governor) AdmitWait(ctx context.Context, req AdmissionRequest) (AdmissionDecision, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		decision, release, err := g.Admit(ctx, req)
		if err != nil || decision.Allowed {
			return decision, release, err
		}
		if req.BrainAuxiliary && (decision.State == StateCritical || decision.State == StateEmergency) {
			return decision, nil, nil
		}

		g.mu.Lock()
		wait := g.notify
		if wait == nil {
			wait = make(chan struct{})
			g.notify = wait
		}
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return decision, nil, ctx.Err()
		case <-wait:
		}
	}
}

func (g *Governor) signalLocked() {
	if g.notify == nil {
		g.notify = make(chan struct{})
		return
	}
	close(g.notify)
	g.notify = make(chan struct{})
}

func (g *Governor) admitUserLocked(req AdmissionRequest, decision AdmissionDecision) (AdmissionDecision, func(), error) {
	retry := g.retryAfterLocked()
	switch g.state {
	case StateEmergency:
		decision.Reason = ReasonEmergency
	case StateCritical:
		decision.Reason = ReasonUserShed
	case StateDegraded:
		decision.Reduced = true
		if g.userInFlight >= g.cfg.UserMaxInFlight {
			decision.Reason = ReasonUserShed
		}
	default:
		if g.userInFlight >= g.cfg.UserMaxInFlight {
			decision.Reason = ReasonUserShed
		}
	}
	if decision.Reason != ReasonNone {
		decision.Error = OverloadError{Code: "resource_overloaded", Reason: decision.Reason, RetryAfter: retry}
		return decision, nil, nil
	}
	g.userInFlight++
	decision.Allowed = true
	release := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.userInFlight > 0 {
			g.userInFlight--
			g.signalLocked()
		}
	}
	return decision, release, nil
}

func (g *Governor) admitBrainLocked(req AdmissionRequest, decision AdmissionDecision) (AdmissionDecision, func(), error) {
	retry := g.retryAfterLocked()
	explicitBrain := req.BrainExplicit && !req.BrainAuxiliary
	switch g.state {
	case StateEmergency:
		decision.Reason = ReasonEmergency
	case StateCritical:
		if !(req.InternalControl && explicitBrain) {
			decision.Reason = ReasonBrainExclusive
			break
		}
		if g.brainInFlight >= g.cfg.BrainMaxInFlight {
			decision.Reason = ReasonUserShed
			break
		}
		decision.BrainOnly = true
	case StateDegraded, StateNormal:
		if g.brainInFlight >= g.cfg.BrainMaxInFlight {
			decision.Reason = ReasonUserShed
			break
		}
	default:
		decision.Reason = ReasonEmergency
	}
	if decision.Reason != ReasonNone {
		decision.Error = OverloadError{Code: "resource_overloaded", Reason: decision.Reason, RetryAfter: retry}
		return decision, nil, nil
	}
	g.brainInFlight++
	decision.Allowed = true
	release := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.brainInFlight > 0 {
			g.brainInFlight--
			g.signalLocked()
		}
	}
	return decision, release, nil
}

func (g *Governor) pressureLocked(sample Sample, now time.Time) (float64, Reason, bool) {
	if !sample.CapturedAt.IsZero() && now.Sub(sample.CapturedAt) > g.cfg.SampleFreshness {
		return 1, ReasonSampleStale, false
	}
	readings := []Reading{sample.HostCPU, sample.HostMemory, sample.DiskPressure, sample.ProcessPressure}
	if sample.UnifiedMemory.Status == MetricValid {
		readings = append(readings, sample.UnifiedMemory)
	} else if !sample.SharedUnifiedMem {
		readings = append(readings, sample.ProcessRSS, sample.GPUVRAM)
	}
	pressure := 0.0
	seenValid := false
	seenUnavailable := false
	seenError := false
	for _, reading := range readings {
		switch reading.Status {
		case MetricValid:
			seenValid = true
			if reading.Value > pressure {
				pressure = reading.Value
			}
		case MetricUnavailable:
			seenUnavailable = true
		case MetricError:
			seenError = true
		case MetricStale:
			return 1, ReasonSampleStale, false
		}
	}
	if !seenValid {
		switch {
		case seenError:
			return 1, ReasonSamplerError, false
		case seenUnavailable:
			return 1, ReasonSamplerUnavailable, false
		default:
			return 1, ReasonSamplerUnavailable, false
		}
	}
	return pressure, ReasonNone, true
}

func (g *Governor) desiredStateLocked(pressure float64) State {
	switch g.state {
	case StateNormal:
		if pressure >= g.cfg.DegradedEnter {
			return StateDegraded
		}
	case StateDegraded:
		if pressure >= g.cfg.CriticalEnter {
			return StateCritical
		}
		if pressure < g.cfg.DegradedRecover {
			return StateNormal
		}
	case StateCritical:
		if pressure >= g.cfg.EmergencyEnter {
			return StateEmergency
		}
		if pressure < g.cfg.CriticalRecover {
			return StateDegraded
		}
	case StateEmergency:
		if pressure < g.cfg.EmergencyRecover {
			return StateCritical
		}
	}
	return g.state
}

func (g *Governor) advanceLocked(desired State, now time.Time) {
	if desired == g.state {
		g.pending = ""
		g.pendingSince = time.Time{}
		return
	}
	hold := g.cfg.EnterHold
	if severity(desired) < severity(g.state) {
		hold = g.cfg.RecoverHold
	}
	if hold == 0 {
		g.transitionLocked(desired, ReasonPressure, now)
		return
	}
	if g.pending != desired {
		g.pending = desired
		g.pendingSince = now
		return
	}
	if now.Sub(g.pendingSince) < hold {
		return
	}
	g.transitionLocked(desired, ReasonPressure, now)
}

func (g *Governor) transitionLocked(next State, _ Reason, now time.Time) {
	g.state = next
	g.pending = ""
	g.pendingSince = now
}

func (g *Governor) retryAfterLocked() time.Duration {
	switch g.state {
	case StateEmergency:
		return 5 * time.Second
	case StateCritical:
		return 2 * time.Second
	case StateDegraded:
		return 750 * time.Millisecond
	default:
		return 500 * time.Millisecond
	}
}

func severity(state State) int {
	switch state {
	case StateNormal:
		return 0
	case StateDegraded:
		return 1
	case StateCritical:
		return 2
	case StateEmergency:
		return 3
	default:
		return 0
	}
}
