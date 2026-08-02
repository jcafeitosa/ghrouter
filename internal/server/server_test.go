package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

func TestHandleModelsUsesCatalog(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "alpha",
				Enabled: true,
				Models:  []string{"model-a"},
			},
			{
				Name:    "beta",
				Enabled: true,
				Models:  []string{"model-b"},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	srv.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"owned_by":"alpha"`) || !strings.Contains(body, `"owned_by":"beta"`) {
		t.Fatalf("catalog models missing from response: %s", body)
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
