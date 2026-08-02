package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

func TestHandleModelsUsesCanonicalIDsAndSkipsCooldown(t *testing.T) {
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
			ID         string   `json:"id"`
			Object     string   `json:"object"`
			Created    int64    `json:"created"`
			OwnedBy    string   `json:"owned_by"`
			Provenance string   `json:"provenance"`
			List       bool     `json:"list"`
			Members    []string `json:"members"`
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
		if entry.ID == "cx/gpt-5" && entry.Provenance != "configured" {
			t.Fatalf("expected configured provenance for catalog model, got %+v", entry)
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

func TestRouteModelReturnsCanonicalID(t *testing.T) {
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

func TestHealthCheckRejectsEmptyModelList(t *testing.T) {
	runner := providers.NewProviderRunner(&types.Provider{
		Name:    "alpha",
		Enabled: true,
		Models:  nil,
	})

	err := runner.HealthCheck(httptest.NewRequest(http.MethodGet, "/", nil).Context())
	if err == nil {
		t.Fatal("expected error for provider with no models")
	}
	if !strings.Contains(err.Error(), "no configured models") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLiveSnapshotIncludesAccountStatus(t *testing.T) {
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "anthropic",
				Enabled: true,
				Models:  []string{"model-a"},
				AuthConfig: map[string]string{
					"account_json": `{"plan":"pro","balance":3.5,"currency":"USD","source":"fixture","available":true,"healthy":true}`,
				},
			},
		},
	}

	srv := New(cfg)
	snapshot := srv.LiveSnapshot()
	if len(snapshot.Providers) != 1 {
		t.Fatalf("expected one provider, got %+v", snapshot.Providers)
	}
	account := snapshot.Providers[0].Account
	if account.Plan != "pro" {
		t.Fatalf("expected account plan pro, got %+v", snapshot.Providers[0])
	}
	if account.Balance == nil || *account.Balance != 3.5 {
		t.Fatalf("expected balance 3.5, got %+v", snapshot.Providers[0])
	}
	if account.Currency != "USD" {
		t.Fatalf("expected currency USD, got %+v", snapshot.Providers[0])
	}
	if account.Source != "fixture" {
		t.Fatalf("expected source fixture, got %+v", snapshot.Providers[0])
	}
}

func TestLiveSnapshotJSONIncludesAccountStatus(t *testing.T) {
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "openai",
				Enabled: true,
				Models:  []string{"model-b"},
				AuthConfig: map[string]string{
					"account_json": `{"plan":"team","balance":1.25,"currency":"USD","source":"fixture","available":true,"healthy":true}`,
				},
			},
		},
	}

	srv := New(cfg)
	payload, err := json.Marshal(srv.LiveSnapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if !strings.Contains(string(payload), `"account"`) {
		t.Fatalf("expected account field in live snapshot JSON, got %s", string(payload))
	}
}

func TestHandleLiveRespondsOnV1Path(t *testing.T) {
	srv := New(&types.Config{})
	req := httptest.NewRequest(http.MethodGet, "/v1/live", nil)
	rec := httptest.NewRecorder()

	srv.handleLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"snapshot"`) {
		t.Fatalf("expected live response snapshot, got %s", rec.Body.String())
	}
}
