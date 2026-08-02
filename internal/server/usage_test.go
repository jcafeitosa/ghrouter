package server

import (
	"net/http/httptest"
	"strings"
	"testing"

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
}
