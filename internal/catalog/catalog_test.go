package catalog

import (
	"testing"
	"time"

	"ghrouter/internal/health"
)

func TestRegisterProviderResolvesHealthySlot(t *testing.T) {
	catalog := NewCatalog(nil, time.Minute)
	done := make(chan struct{})
	go func() {
		catalog.RegisterProvider("provider", []*ModelEntry{{
			Model:        "model",
			Provider:     "provider",
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
	if model == nil || model.Model != "model" {
		t.Fatalf("expected model in auto slot, got %#v", model)
	}
}

func TestProviderWeightInfluencesBestHealthyModel(t *testing.T) {
	catalog := NewCatalog(nil, time.Minute)
	catalog.RegisterProvider("cheap", []*ModelEntry{{
		Model:          "model-a",
		Provider:       "cheap",
		HealthStatus:   health.HealthHealthy,
		ProviderWeight: 1.3,
	}})
	catalog.RegisterProvider("expensive", []*ModelEntry{{
		Model:          "model-b",
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
