package health

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type testChecker struct {
	name   string
	models []string
}

func (c testChecker) HealthCheck(context.Context) error { return nil }
func (c testChecker) GetName() string                   { return c.name }
func (c testChecker) GetModels() []string               { return c.models }

func TestLoopCheckOneMarksHealthyChecker(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	loop.checkOne(context.Background(), testChecker{name: "test", models: []string{"model"}})

	result := loop.GetHealth("test")
	if result == nil {
		t.Fatal("expected a health result")
	}
	if result.Status != HealthHealthy {
		t.Fatalf("expected healthy status, got %q", result.Status)
	}
}

func TestLoopRecordSuccessSeedsVerifiedProviderHealth(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	loop.RecordSuccess("local-brain", 250*time.Millisecond)
	result := loop.GetHealth("local-brain")
	if result == nil || result.Status != HealthHealthy || result.Latency != 250*time.Millisecond || result.Timestamp.IsZero() {
		t.Fatalf("expected recorded provider success, got %+v", result)
	}
}

func TestLoopSampleCallbackReceivesEveryCheck(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	samples := make(chan HealthCheckResult, 1)
	loop.SetOnSample(func(result HealthCheckResult) { samples <- result })
	loop.checkOne(context.Background(), testChecker{name: "test", models: []string{"model"}})
	select {
	case result := <-samples:
		if result.Provider != "test" || result.Status != HealthHealthy {
			t.Fatalf("unexpected health sample: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health sample")
	}
}

type parallelChecker struct {
	name   string
	active *atomic.Int32
	max    *atomic.Int32
	calls  *atomic.Int32
}

func (c parallelChecker) HealthCheck(context.Context) error {
	if c.calls != nil {
		c.calls.Add(1)
	}
	current := c.active.Add(1)
	for {
		previous := c.max.Load()
		if current <= previous || c.max.CompareAndSwap(previous, current) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	c.active.Add(-1)
	return nil
}

func (c parallelChecker) GetName() string     { return c.name }
func (c parallelChecker) GetModels() []string { return nil }

func TestLoopChecksProvidersConcurrently(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	var active, max atomic.Int32
	for i := 0; i < 4; i++ {
		loop.Register(parallelChecker{name: string(rune('a' + i)), active: &active, max: &max})
	}
	loop.checkAll(context.Background())
	if max.Load() < 2 {
		t.Fatalf("expected health checks to overlap, maximum concurrency was %d", max.Load())
	}
}

func TestLoopStopIsIdempotent(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop.Start(ctx)
	loop.Stop()
	loop.Stop()
}

func TestLoopStartIsIdempotent(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	var calls atomic.Int32
	checker := &parallelChecker{name: "once", active: &atomic.Int32{}, max: &atomic.Int32{}, calls: &calls}
	loop.Register(checker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop.Start(ctx)
	loop.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	loop.Stop()
	if calls.Load() != 1 {
		t.Fatalf("expected one initial health check, got %d", calls.Load())
	}
}

type errorChecker struct {
	name string
	err  error
}

func (c errorChecker) HealthCheck(context.Context) error { return c.err }
func (c errorChecker) GetName() string                   { return c.name }
func (c errorChecker) GetModels() []string               { return nil }

func TestLoopTracksTimeoutsAndResetsOldFailures(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	checker := errorChecker{name: "timeout", err: context.DeadlineExceeded}
	loop.checkOne(context.Background(), checker)
	result := loop.GetHealth("timeout")
	if result == nil || result.ConsecutiveTimeouts != 1 || result.Status != HealthDegraded {
		t.Fatalf("expected first timeout to be degraded and counted, got %+v", result)
	}
	loop.mu.Lock()
	loop.results["timeout"].Timestamp = time.Now().Add(-time.Minute)
	loop.mu.Unlock()
	loop.checkOne(context.Background(), errorChecker{name: "timeout", err: errors.New("connection reset")})
	result = loop.GetHealth("timeout")
	if result == nil || result.ConsecutiveErrors != 1 || result.Status != HealthDegraded {
		t.Fatalf("expected an old failure window to reset debounce, got %+v", result)
	}
}
