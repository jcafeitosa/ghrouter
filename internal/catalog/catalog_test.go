package catalog

import (
	"testing"
	"time"

	"ghrouter/internal/health"
	"ghrouter/internal/types"
)

func TestRegisterProviderUsesCanonicalModelIDs(t *testing.T) {
	catalog := NewCatalog(nil, time.Minute)
	done := make(chan struct{})
	go func() {
		catalog.RegisterProvider("claude-code", []*ModelEntry{{
			ID:           "cc/claude-sonnet-5",
			Model:        "cc/claude-sonnet-5",
			Provider:     "claude-code",
			HealthStatus: health.HealthHealthy,
		}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("registering a healthy provider did not complete")
	}

	model := catalog.GetModelBySlot(SlotAuto)
	if model == nil || model.ID != "cc/claude-sonnet-5" {
		t.Fatalf("expected canonical model in auto slot, got %#v", model)
	}
	if got := catalog.GetModel("cc/claude-sonnet-5"); got == nil || got.ID != "cc/claude-sonnet-5" {
		t.Fatalf("expected canonical model lookup to succeed, got %#v", got)
	}
}

func TestProviderWeightInfluencesBestHealthyModel(t *testing.T) {
	catalog := NewCatalog(nil, time.Minute)
	catalog.RegisterProvider("cheap", []*ModelEntry{{
		ID:             "cc/model-a",
		Model:          "cc/model-a",
		Provider:       "cheap",
		HealthStatus:   health.HealthHealthy,
		ProviderWeight: 1.3,
	}})
	catalog.RegisterProvider("expensive", []*ModelEntry{{
		ID:             "cc/model-b",
		Model:          "cc/model-b",
		Provider:       "expensive",
		HealthStatus:   health.HealthHealthy,
		ProviderWeight: 0.5,
	}})

	model := catalog.BestHealthyModel()
	if model == nil {
		t.Fatal("expected best healthy model")
	}
	if model.Provider != "cheap" {
		t.Fatalf("expected weighted provider to win, got %#v", model)
	}
}

func TestBestHealthyModelSkipsCooldownByCanonicalID(t *testing.T) {
	catalog := NewCatalog(nil, time.Minute)
	catalog.RegisterProvider("preferred", []*ModelEntry{{
		ID:             "cc/model-a",
		Model:          "cc/model-a",
		Provider:       "preferred",
		HealthStatus:   health.HealthHealthy,
		ProviderWeight: 2,
	}})
	catalog.RegisterProvider("backup", []*ModelEntry{{
		ID:             "cc/model-b",
		Model:          "cc/model-b",
		Provider:       "backup",
		HealthStatus:   health.HealthHealthy,
		ProviderWeight: 0.5,
	}})
	catalog.SetCooldown("cc/model-a", time.Now().Add(time.Hour))

	model := catalog.BestHealthyModel()
	if model == nil {
		t.Fatal("expected best healthy model")
	}
	if model.Provider != "backup" || model.ID != "cc/model-b" {
		t.Fatalf("expected cooldown model to be skipped, got %#v", model)
	}
}

func TestRecordLatencyMaintainsModelPercentiles(t *testing.T) {
	catalog := NewCatalog(nil, time.Minute)
	catalog.RegisterProvider("opencode", []*ModelEntry{{
		ID: "oc/free", Model: "oc/free", Provider: "opencode", HealthStatus: health.HealthHealthy,
	}})
	for _, sample := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond} {
		catalog.RecordLatency("oc/free", sample)
	}

	model := catalog.GetModel("oc/free")
	if model == nil {
		t.Fatal("expected recorded model")
	}
	if model.LatencyP50 != 300*time.Millisecond || model.LatencyP95 != 500*time.Millisecond {
		t.Fatalf("expected p50=300ms p95=500ms, got p50=%s p95=%s", model.LatencyP50, model.LatencyP95)
	}
}

func TestRestoreModelMetadataDoesNotEraseUnknownDiscoveryTimestamp(t *testing.T) {
	discoveredAt := time.Now().UTC().Add(-time.Hour)
	catalog := NewCatalog(nil, time.Minute)
	catalog.RegisterProvider("opencode", []*ModelEntry{{
		ID: "oc/model", Model: "oc/model", Provider: "opencode", HealthStatus: health.HealthHealthy,
		Info: types.ModelInfo{DiscoveredAt: discoveredAt},
	}})

	catalog.RestoreModelMetadata("oc/model", "native", "unknown", nil, nil, 0, 0, 0, false, false, false, time.Time{})
	model := catalog.GetModel("oc/model")
	if model == nil || !model.Info.DiscoveredAt.Equal(discoveredAt) {
		t.Fatalf("expected missing persisted timestamp to preserve current evidence, got %#v", model)
	}
}

func TestExpiredCooldownClearsOperationalVerificationEvidence(t *testing.T) {
	verifiedAt := time.Now().UTC().Add(-time.Minute)
	catalog := NewCatalog(nil, time.Minute)
	catalog.RegisterProvider("opencode", []*ModelEntry{{
		ID: "oc/model", Model: "oc/model", Provider: "opencode", HealthStatus: health.HealthHealthy,
		Info: types.ModelInfo{VerifiedAt: verifiedAt},
	}})

	catalog.SetCooldown("oc/model", time.Now().Add(time.Hour))
	catalog.mu.Lock()
	catalog.refreshExpiredCooldownsLocked(time.Now().Add(2 * time.Hour))
	catalog.mu.Unlock()

	model := catalog.GetModel("oc/model")
	if model == nil || model.HealthStatus != health.HealthUnknown || !model.Info.VerifiedAt.IsZero() {
		t.Fatalf("expected expired cooldown to require fresh verification, got %#v", model)
	}
	if !catalog.NeedsVerification("oc/model", time.Now(), time.Hour) {
		t.Fatal("expected expired cooldown model to require a fresh verification")
	}
}
