package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghrouter/internal/detectors"
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

func TestLiveSnapshotIncludesConfiguredModelInfoOnlyAndExcludesCooldown(t *testing.T) {
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "opencode",
				Type:    types.ProviderOpenCode,
				Enabled: true,
				ModelInfo: map[string]types.ModelInfo{
					"oc/good": {
						Source:        "configured",
						VerifiedAt:    time.Unix(100, 0).UTC(),
						HealthStatus:  "healthy",
						ContextWindow: 1_000_000,
						Thinking:      true,
					},
					"oc/bad": {
						Source:        "configured",
						HealthStatus:  "failed",
						CooldownUntil: time.Now().Add(time.Hour),
					},
				},
			},
		},
	}
	detectors.EnrichProviderModels(cfg.Providers[0])

	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))

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
	var directModels []struct {
		ID         string
		OwnedBy    string
		Provenance string
	}
	for _, entry := range payload.Data {
		if entry.List {
			if entry.ID == "ghrouter/auto" && (len(entry.Members) != 1 || entry.Members[0] != "oc/good") {
				t.Fatalf("expected auto list to keep verified canonical model only, got %+v", entry)
			}
			continue
		}
		directModels = append(directModels, struct {
			ID         string
			OwnedBy    string
			Provenance string
		}{ID: entry.ID, OwnedBy: entry.OwnedBy, Provenance: entry.Provenance})
		if entry.ID == "oc/bad" {
			t.Fatalf("expected failed/cooldown model to be excluded, got %+v", entry)
		}
	}
	if len(directModels) != 1 {
		t.Fatalf("expected one direct live model, got %+v", directModels)
	}
	entry := directModels[0]
	if entry.ID != "oc/good" || entry.OwnedBy != "opencode" {
		t.Fatalf("unexpected live model entry: %+v", entry)
	}
	if entry.Provenance != "verified" {
		t.Fatalf("expected verified provenance, got %+v", entry)
	}
	snapshot := srv.LiveSnapshot()
	var snapshotDirect []string
	var snapshotLists map[string][]string
	for _, model := range snapshot.Models {
		if model.List {
			if snapshotLists == nil {
				snapshotLists = make(map[string][]string)
			}
			snapshotLists[model.ID] = append([]string(nil), model.Members...)
			continue
		}
		snapshotDirect = append(snapshotDirect, model.ID)
		if model.ID == "oc/bad" {
			t.Fatalf("expected failed/cooldown model to stay excluded from snapshot, got %+v", model)
		}
		if model.ID == "oc/good" && model.Provenance != "verified" {
			t.Fatalf("expected verified provenance, got %+v", model)
		}
	}
	if len(snapshotDirect) != 1 || snapshotDirect[0] != "oc/good" {
		t.Fatalf("expected one direct verified model in snapshot, got %+v", snapshotDirect)
	}
	if members := snapshotLists["ghrouter/auto"]; len(members) != 1 || members[0] != "oc/good" {
		t.Fatalf("expected auto list to keep verified canonical model only, got %+v", members)
	}
	if providers := snapshot.Providers; len(providers) != 1 || len(providers[0].Models) != 1 || providers[0].Models[0] != "oc/good" {
		t.Fatalf("expected only the verified canonical provider model in live providers, got %+v", providers)
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
