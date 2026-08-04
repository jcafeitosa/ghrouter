package health

import (
	"context"
	"errors"
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
	stopOnce       sync.Once
	startOnce      sync.Once
	wakeCh         chan struct{}
	wg             sync.WaitGroup
	onChange       func(provider string, old, new HealthStatus)
	onSample       func(HealthCheckResult)
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
		wakeCh:         make(chan struct{}, 1),
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

func (l *Loop) SetOnSample(fn func(HealthCheckResult)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onSample = fn
}

func (l *Loop) RecordSuccess(provider string, latency time.Duration) {
	if l == nil || provider == "" {
		return
	}
	now := time.Now()
	l.mu.Lock()
	previous := l.results[provider]
	if previous == nil {
		previous = &HealthCheckResult{Provider: provider}
		l.results[provider] = previous
	}
	oldStatus := previous.Status
	previous.Status = HealthHealthy
	previous.Latency = latency
	previous.Error = nil
	previous.Timestamp = now
	previous.ConsecutiveErrors = 0
	previous.ConsecutiveTimeouts = 0
	onChange := l.onChange
	onSample := l.onSample
	sample := *previous
	l.mu.Unlock()
	if onChange != nil && oldStatus != HealthHealthy {
		onChange(provider, oldStatus, HealthHealthy)
	}
	if onSample != nil {
		onSample(sample)
	}
}

func (l *Loop) Settings() (time.Duration, time.Duration) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.interval, l.timeout
}

func (l *Loop) SetSettings(interval, timeout time.Duration) {
	if interval <= 0 || timeout <= 0 {
		return
	}
	l.mu.Lock()
	l.interval = interval
	l.timeout = timeout
	l.mu.Unlock()
	select {
	case l.wakeCh <- struct{}{}:
	default:
	}
}

func (l *Loop) Start(ctx context.Context) {
	l.startOnce.Do(func() {
		l.wg.Add(1)
		go l.run(ctx)
	})
}

func (l *Loop) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
	l.wg.Wait()
}

func (l *Loop) run(ctx context.Context) {
	defer l.wg.Done()

	l.checkAll(ctx)
	timer := time.NewTimer(l.currentInterval())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-l.wakeCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(l.currentInterval())
		case <-timer.C:
			l.checkAll(ctx)
			timer.Reset(l.currentInterval())
		}
	}
}

func (l *Loop) currentInterval() time.Duration {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.interval <= 0 {
		return time.Second
	}
	return l.interval
}

func (l *Loop) checkAll(ctx context.Context) {
	l.mu.RLock()
	checkers := make([]HealthChecker, 0, len(l.checkers))
	for _, checker := range l.checkers {
		checkers = append(checkers, checker)
	}
	l.mu.RUnlock()
	if len(checkers) == 0 {
		return
	}
	workers := len(checkers)
	if workers > 8 {
		workers = 8
	}
	jobs := make(chan HealthChecker)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case checker, ok := <-jobs:
					if !ok {
						return
					}
					l.checkOne(ctx, checker)
				}
			}
		}()
	}
	for _, checker := range checkers {
		select {
		case <-ctx.Done():
			close(jobs)
			group.Wait()
			return
		case jobs <- checker:
		}
	}
	close(jobs)
	group.Wait()
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
		if !prev.Timestamp.IsZero() && l.debounceWindow > 0 && time.Since(prev.Timestamp) > l.debounceWindow {
			prev.ConsecutiveErrors = 0
			prev.ConsecutiveTimeouts = 0
		}
		prev.ConsecutiveErrors++
		if errors.Is(err, context.DeadlineExceeded) {
			prev.ConsecutiveTimeouts++
		} else {
			prev.ConsecutiveTimeouts = 0
		}
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
	onSample := l.onSample
	newStatus := prev.Status
	sample := *prev
	l.mu.Unlock()

	if onChange != nil && newStatus != oldStatus {
		onChange(key, oldStatus, newStatus)
	}
	if onSample != nil {
		onSample(sample)
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
