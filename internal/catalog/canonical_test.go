package catalog

import (
	"testing"
	"time"

	"ghrouter/internal/health"
)

func TestRegisterProviderUsesCanonicalModelIDsCanonical(t *testing.T) {
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

func TestBestHealthyModelSkipsCooldownByCanonicalIDCanonical(t *testing.T) {
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
