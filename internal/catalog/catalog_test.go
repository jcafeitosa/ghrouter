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
