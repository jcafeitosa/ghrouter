//go:build darwin

package resourcegov

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseDarwinVMStat(t *testing.T) {
	pageSize, pages, err := parseDarwinVMStat("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 100.\nPages speculative: 10.\nPages purgeable: 5.\n")
	if err != nil {
		t.Fatalf("parse vm_stat: %v", err)
	}
	if pageSize != 16384 || pages["pages free"] != 100 || pages["pages speculative"] != 10 || pages["pages purgeable"] != 5 {
		t.Fatalf("unexpected vm_stat parse: page_size=%d pages=%v", pageSize, pages)
	}
	pressure, err := parseDarwinMemoryPressure("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 100.\nPages speculative: 10.\nPages purgeable: 5.\n", 16384*1000)
	if err != nil || pressure <= 0.7 || pressure >= 0.9 {
		t.Fatalf("unexpected memory pressure: pressure=%v err=%v", pressure, err)
	}
}

func TestSystemSamplerUsesRealUnifiedMemoryEvidence(t *testing.T) {
	sampler := &SystemSampler{
		now: func() time.Time { return time.Unix(100, 0) },
		run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "sysctl" {
				return []byte("16384000\n"), nil
			}
			return []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 100.\nPages speculative: 10.\nPages purgeable: 5.\n"), nil
		},
	}
	sample, err := sampler.Sample(context.Background())
	if err != nil || sample.UnifiedMemory.Status != MetricValid || !sample.SharedUnifiedMem {
		t.Fatalf("expected valid unified memory sample, sample=%+v err=%v", sample, err)
	}
	if sample.CapturedAt != time.Unix(100, 0) || sample.UnifiedMemory.Value <= 0 {
		t.Fatalf("unexpected sample provenance/value: %+v", sample)
	}
}

func TestSystemSamplerPreservesUnavailableStateOnCommandError(t *testing.T) {
	sampler := &SystemSampler{
		now: func() time.Time { return time.Unix(100, 0) },
		run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("command unavailable")
		},
	}
	sample, err := sampler.Sample(context.Background())
	if err == nil || sample.UnifiedMemory.Status != MetricUnavailable {
		t.Fatalf("expected unavailable sample with error, sample=%+v err=%v", sample, err)
	}
}

type failingSampler struct{}

func (failingSampler) Sample(context.Context) (Sample, error) {
	return Sample{}, errors.New("sampler unavailable")
}

func TestAdmitWaitShedsAuxiliaryBrainAfterSamplerEmergency(t *testing.T) {
	governor := New(Config{BrainMaxInFlight: 1}, failingSampler{}, nil)
	if _, err := governor.Observe(context.Background()); err == nil {
		t.Fatal("expected sampler error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	decision, release, err := governor.AdmitWait(ctx, AdmissionRequest{Class: RequestClassBrain, BrainAuxiliary: true})
	if err != nil || release != nil || decision.Allowed || decision.State != StateEmergency {
		t.Fatalf("expected immediate auxiliary Brain shed, decision=%+v release=%v err=%v", decision, release != nil, err)
	}
}

func TestSnapshotPreservesObservedMetricProvenance(t *testing.T) {
	observedAt := time.Unix(200, 0)
	governor := New(Config{}, fixedSampler{sample: Sample{
		CapturedAt:       observedAt,
		UnifiedMemory:    Reading{Status: MetricValid, Value: 0.5},
		SharedUnifiedMem: true,
	}}, nil)
	if _, err := governor.Observe(context.Background()); err != nil {
		t.Fatalf("observe fixed sample: %v", err)
	}
	snapshot := governor.Snapshot()
	if snapshot.ObservedAt == nil || !snapshot.ObservedAt.Equal(observedAt) || snapshot.UnifiedMemory.Status != MetricValid || snapshot.UnifiedMemory.Value != 0.5 {
		t.Fatalf("unexpected resource snapshot: %+v", snapshot)
	}
}

func TestGovernorAppliesZeroHoldPressureImmediately(t *testing.T) {
	governor := New(Config{DegradedEnter: 0.7}, fixedSampler{sample: Sample{
		CapturedAt:       time.Unix(300, 0),
		UnifiedMemory:    Reading{Status: MetricValid, Value: 0.9},
		SharedUnifiedMem: true,
	}}, &fixedClock{now: time.Unix(300, 0)})
	state, err := governor.Observe(context.Background())
	if err != nil || state != StateDegraded {
		t.Fatalf("expected immediate degraded state, state=%s err=%v", state, err)
	}
}

type fixedSampler struct{ sample Sample }

func (f fixedSampler) Sample(context.Context) (Sample, error) { return f.sample, nil }

type fixedClock struct{ now time.Time }

func (f *fixedClock) Now() time.Time { return f.now }
