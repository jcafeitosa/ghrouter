package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ghrouter/internal/types"
)

func TestEstimatePromptTokensCountsMessageContent(t *testing.T) {
	req := &types.OpenAIRequest{
		Model: "cx/gpt-5",
		Messages: []types.OpenAIMessage{
			{Role: "user", Content: "hello world"},
			{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "more words"},
			}},
		},
	}

	if got := estimatePromptTokens(req); got == 0 {
		t.Fatal("expected prompt token estimate to be greater than zero")
	}
}

func TestTelemetryRecordsKnownModelCost(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "alpha", Type: types.ProviderCustom, Models: []string{"model"}, ModelInfo: map[string]types.ModelInfo{"model": {TokenCost: 7}}, Enabled: true}}})
	end := srv.telemetry.beginWithMeta("req-cost", "test")
	srv.telemetry.recordUsage("req-cost", 3, 5)
	end("ok", false, "alpha", "model", "/v1/chat/completions", 10)
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || recent[0].CostMicros != 7 {
		t.Fatalf("expected known token cost in telemetry, got %+v", recent)
	}
}

func TestTelemetryRecordsConfiguredConnectionForRequestAndAttempt(t *testing.T) {
	srv := New(&types.Config{
		Providers:   []*types.Provider{{Name: "alpha", Type: types.ProviderCustom, Models: []string{"model"}, Enabled: true}},
		Connections: []types.Connection{{Name: "primary", Provider: "alpha", Model: "model", Enabled: true}},
	})
	end := srv.telemetry.beginWithMeta("req-connection", "test-client")
	srv.telemetry.recordAttempt("req-connection", "alpha", "model", "ok", "", time.Now())
	end("ok", false, "alpha", "model", "/v1/chat/completions", time.Millisecond)

	recent := srv.LiveSnapshot().Telemetry.Recent
	if len(recent) != 1 || recent[0].ConnectionID != "primary" {
		t.Fatalf("expected request connection identity, got %+v", recent)
	}
	if len(recent[0].Attempts) != 1 || recent[0].Attempts[0].ConnectionID != "primary" {
		t.Fatalf("expected attempt connection identity, got %+v", recent[0].Attempts)
	}
}

func TestTelemetryAggregatesLatencyPerModel(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "alpha", Type: types.ProviderCustom, Models: []string{"model"}, Enabled: true}}})
	for _, latency := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond} {
		end := srv.telemetry.beginWithMeta("req-latency", "test")
		end("ok", false, "alpha", "model", "/v1/chat/completions", latency)
	}

	latency, ok := srv.LiveSnapshot().Telemetry.ModelLatency["alpha/model"]
	if !ok {
		t.Fatal("expected model latency aggregate")
	}
	if latency.Samples != 3 || latency.LastMs != 300 || latency.P50Ms != 200 || latency.P95Ms != 300 {
		t.Fatalf("unexpected model latency aggregate: %+v", latency)
	}
	model := srv.ModelSummaries()[0]
	if model.LatencyMs != 200 {
		t.Fatalf("expected catalog model p50 latency, got %dms", model.LatencyMs)
	}
}

func TestConvertAnthropicUsageIncludesPromptEstimate(t *testing.T) {
	srv := &Server{}
	req := &AnthropicRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "hello world"},
		},
	}

	internal := srv.convertToInternalRequest(req)
	if got := estimateAnthropicPromptTokens(internal); got == 0 {
		t.Fatal("expected anthropic prompt token estimate to be greater than zero")
	}
}

func TestHealthEndpointIncludesAggregatedCounts(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{{
			Name:    "alpha",
			Enabled: true,
			Models:  []string{"model-a"},
		}},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/health", nil)
	srv.handleHealth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"provider_count":1`) {
		t.Fatalf("expected provider count in health response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model_readiness"`) {
		t.Fatalf("expected model readiness in health response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"binary_sha256"`) {
		t.Fatalf("expected running build identity in health response, got %s", rec.Body.String())
	}
}

func TestHealthSnapshotSeparatesModelCatalogFromVerifiedReadiness(t *testing.T) {
	verifiedAt := time.Now().UTC()
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name:    "alpha",
		Type:    types.ProviderCustom,
		Enabled: true,
		Models:  []string{"verified", "unknown"},
		ModelInfo: map[string]types.ModelInfo{
			"verified": {Source: "native", HealthStatus: "healthy", VerifiedAt: verifiedAt},
			"unknown":  {Source: "native", HealthStatus: "healthy"},
		},
	}}}, "/tmp/ghrouter-health-readiness.yaml")

	models := srv.healthSnapshot().Models
	if models.Catalog != 2 || models.Verified != 1 || models.VerifiedHealthy != 1 || models.Healthy != 1 || models.Unknown != 1 {
		t.Fatalf("expected catalog and evidence-backed counts to differ, got %+v", models)
	}
}

func TestLivenessAndReadinessAreSeparate(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "missing", Type: types.ProviderCustom, Models: []string{"model"}, Enabled: true}}})
	live := httptest.NewRecorder()
	srv.handleLiveness(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), "alive") {
		t.Fatalf("expected liveness 200, got %d: %s", live.Code, live.Body.String())
	}
	ready := httptest.NewRecorder()
	srv.handleReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), "not_ready") {
		t.Fatalf("expected readiness 503 without a usable provider, got %d: %s", ready.Code, ready.Body.String())
	}
}
