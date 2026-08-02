package health

import (
	"context"
	"sync"
	"time"
)

// HealthChecker defines the interface for provider health checks
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
	GetName() string
	GetModels() []string
}

// Loop runs periodic health checks for all registered providers
type Loop struct {
	mu             sync.RWMutex
	checkers       map[string]HealthChecker
	results        map[string]*HealthCheckResult
	interval       time.Duration
	timeout        time.Duration
	debounceCount  int
	debounceWindow time.Duration
	stopCh         chan struct{}
	wg             sync.WaitGroup
	onChange       func(provider string, old, new HealthStatus)
}

type Snapshot struct {
	Providers map[string]HealthCheckResult
}

// NewLoop returns a new health check loop with the given interval and timeout.
func NewLoop(interval, timeout time.Duration) *Loop {
	return &Loop{
		checkers:       make(map[string]HealthChecker),
		results:        make(map[string]*HealthCheckResult),
		interval:       interval,
		timeout:        timeout,
		debounceCount:  2, // require 2 consecutive failures to mark unhealthy
		debounceWindow: 30 * time.Second,
		stopCh:         make(chan struct{}),
	}
}

func (l *Loop) Register(checker HealthChecker) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checkers[checker.GetName()] = checker
}

func (l *Loop) Unregister(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.checkers, name)
	delete(l.results, name)
}

func (l *Loop) SetOnChange(fn func(provider string, old, new HealthStatus)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onChange = fn
}

func (l *Loop) Start(ctx context.Context) {
	l.wg.Add(1)
	go l.run(ctx)
}

func (l *Loop) Stop() {
	close(l.stopCh)
	l.wg.Wait()
}

func (l *Loop) run(ctx context.Context) {
	defer l.wg.Done()

	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	l.checkAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.checkAll(ctx)
		}
	}
}

func (l *Loop) checkAll(ctx context.Context) {
	l.mu.RLock()
	checkers := make([]HealthChecker, 0, len(l.checkers))
	for _, checker := range l.checkers {
		checkers = append(checkers, checker)
	}
	l.mu.RUnlock()

	for _, checker := range checkers {
		l.checkOne(ctx, checker)
	}
}

func (l *Loop) checkOne(ctx context.Context, checker HealthChecker) {
	checkCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	start := time.Now()
	err := checker.HealthCheck(checkCtx)
	latency := time.Since(start)

	l.mu.Lock()
	key := checker.GetName()
	prev := l.results[key]
	if prev == nil {
		prev = &HealthCheckResult{Provider: key}
		l.results[key] = prev
	}
	oldStatus := prev.Status
	if err != nil {
		prev.ConsecutiveErrors++
		prev.ConsecutiveTimeouts = 0
		if prev.ConsecutiveErrors >= l.debounceCount {
			prev.Status = HealthUnhealthy
		} else {
			prev.Status = HealthDegraded
		}
		prev.Error = err
	} else {
		prev.ConsecutiveErrors = 0
		prev.ConsecutiveTimeouts = 0
		prev.Status = HealthHealthy
		prev.Error = nil
	}
	prev.Latency = latency
	prev.Timestamp = time.Now()
	onChange := l.onChange
	newStatus := prev.Status
	l.mu.Unlock()

	if onChange != nil && newStatus != oldStatus {
		onChange(key, oldStatus, newStatus)
	}
}

func (l *Loop) GetHealth(provider string) *HealthCheckResult {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result, ok := l.results[provider]
	if !ok {
		return nil
	}
	copy := *result
	return &copy
}

func (l *Loop) Snapshot() Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]HealthCheckResult, len(l.results))
	for name, result := range l.results {
		if result == nil {
			continue
		}
		out[name] = *result
	}
	return Snapshot{Providers: out}
}
