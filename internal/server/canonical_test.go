package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ghrouter/internal/types"
)

func TestHandleModelsUsesCanonicalIDsAndSkipsCooldownCanonical(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "claude-code",
				Type:    types.ProviderClaudeCode,
				Enabled: true,
				Models:  []string{"claude-sonnet-5"},
			},
			{
				Name:    "codex",
				Type:    types.ProviderCodex,
				Enabled: true,
				Models:  []string{"gpt-5"},
			},
		},
	})

	srv.catalog.SetCooldown("cc/claude-sonnet-5", time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string   `json:"id"`
			Object  string   `json:"object"`
			OwnedBy string   `json:"owned_by"`
			List    bool     `json:"list"`
			Members []string `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal model list: %v", err)
	}
	if payload.Object != "list" {
		t.Fatalf("expected list object, got %+v", payload)
	}
	var directModels []string
	lists := map[string][]string{}
	for _, entry := range payload.Data {
		if entry.Object != "model" {
			t.Fatalf("expected model object, got %+v", entry)
		}
		if entry.List {
			lists[entry.ID] = append([]string(nil), entry.Members...)
			continue
		}
		directModels = append(directModels, entry.ID)
		if entry.ID == "cc/claude-sonnet-5" {
			t.Fatalf("expected cooldowned model to be omitted, got %+v", entry)
		}
	}
	if len(directModels) != 1 || directModels[0] != "cx/gpt-5" {
		t.Fatalf("expected only eligible canonical direct model, got %+v", directModels)
	}
	if members := lists["ghrouter/auto"]; len(members) != 1 || members[0] != "cx/gpt-5" {
		t.Fatalf("expected automatic list to keep only eligible canonical model, got %+v", members)
	}
	if members := lists["ghrouter/codex"]; len(members) != 1 || members[0] != "cx/gpt-5" {
		t.Fatalf("expected provider list to keep only eligible canonical model, got %+v", members)
	}
}

func TestRouteModelReturnsCanonicalIDCanonical(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "claude-code",
				Type:    types.ProviderClaudeCode,
				Enabled: true,
				Models:  []string{"claude-sonnet-5"},
			},
		},
	})

	provider, model := srv.RouteModel("cc/claude-sonnet-5")
	if provider != "claude-code" {
		t.Fatalf("expected claude-code provider, got %q", provider)
	}
	if model != "cc/claude-sonnet-5" {
		t.Fatalf("expected canonical model ID, got %q", model)
	}
}
