package server

import (
	"testing"

	"ghrouter/internal/types"
)

func TestRouteOpenAIRequestUsesToolSlot(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "tool-provider",
				Enabled: true,
				Models:  []string{"tool-model"},
			},
		},
	})

	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{
		Tools: []types.OpenAITool{{Type: "function", Function: types.OpenAIToolFunc{Name: "lookup"}}},
	})

	if provider != "tool-provider" || model != "tool-model" {
		t.Fatalf("expected tool-provider/tool-model, got %q/%q", provider, model)
	}
}

func TestRouteOpenAIRequestFallsBackToHealthyModel(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "alpha",
				Enabled: true,
				Models:  []string{"model-a"},
			},
		},
	})

	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{
		Model: "unknown-model",
	})

	if provider != "alpha" || model != "model-a" {
		t.Fatalf("expected fallback alpha/model-a, got %q/%q", provider, model)
	}
}

func TestRouteOpenAIRequestUsesConfiguredFallback(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "primary",
				Enabled: true,
				Models:  []string{"model-a"},
			},
			{
				Name:    "backup",
				Enabled: true,
				Models:  []string{"model-b"},
			},
		},
		Routes: []*types.Route{
			{
				Pattern:  "custom/*",
				Provider: "missing",
				Fallback: []string{"backup"},
			},
		},
	})

	provider, model := srv.RouteModel("custom/task")
	if provider != "backup" || model != "custom/task" {
		t.Fatalf("expected configured fallback backup/custom/task, got %q/%q", provider, model)
	}
}

func TestRouteModelRoundRobinFallbackCycles(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "primary",
				Enabled: true,
				Models:  []string{"model-a"},
			},
			{
				Name:    "backup-a",
				Enabled: true,
				Models:  []string{"model-b"},
			},
			{
				Name:    "backup-b",
				Enabled: true,
				Models:  []string{"model-c"},
			},
		},
		Routes: []*types.Route{
			{
				Pattern:  "pool/*",
				Provider: "round-robin",
				Fallback: []string{"backup-a", "backup-b"},
			},
		},
	})

	firstProvider, _ := srv.RouteModel("pool/request")
	secondProvider, _ := srv.RouteModel("pool/request")
	if firstProvider == "" || secondProvider == "" {
		t.Fatalf("expected providers from round robin, got %q and %q", firstProvider, secondProvider)
	}
	if firstProvider == secondProvider {
		t.Fatalf("expected round robin to alternate, got %q twice", firstProvider)
	}
}

func TestRouteModelStickyIsDeterministic(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "sticky-a",
				Enabled: true,
				Models:  []string{"model-a"},
			},
			{
				Name:    "sticky-b",
				Enabled: true,
				Models:  []string{"model-b"},
			},
		},
		Routes: []*types.Route{
			{
				Pattern:  "sticky/*",
				Provider: "sticky",
				Fallback: []string{"sticky-a", "sticky-b"},
			},
		},
	})

	firstProvider, _ := srv.RouteModel("sticky/task")
	secondProvider, _ := srv.RouteModel("sticky/task")
	if firstProvider == "" {
		t.Fatal("expected sticky provider")
	}
	if firstProvider != secondProvider {
		t.Fatalf("expected sticky selection to be stable, got %q then %q", firstProvider, secondProvider)
	}
}
