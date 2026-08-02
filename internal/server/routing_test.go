package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghrouter/internal/health"
	"ghrouter/internal/types"
)

func TestChatCompletionsPreservesToolCalls(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "tool-cli")
	output := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_files","arguments":"{}"}}]}` + "\n"
	script := "#!/bin/sh\nprintf '%s' '" + output[:len(output)-1] + "'\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "tool", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"tool-model"}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"tool/tool-model","messages":[{"role":"user","content":"inspect"}],"tools":[{"type":"function","function":{"name":"list_files","description":"list","parameters":{}}}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected tool response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response types.OpenAIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Name != "list_files" {
		t.Fatalf("expected tool call to survive response, got %+v", response)
	}
}

func TestResponsesEndpointReturnsOpenAIResponsesShape(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "responses-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"responses ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "local", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"model","input":"hello"}`))
	rec := httptest.NewRecorder()
	srv.handleResponses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected responses status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Object != "response" || response.Status != "completed" || len(response.Output) != 1 || response.Output[0].Content[0].Text != "responses ok" {
		t.Fatalf("unexpected responses payload: %+v", response)
	}
}

func TestResponsesEndpointNormalizesMessageContentBlocks(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "responses-block-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"blocks ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "local", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	rec := httptest.NewRecorder()
	srv.handleResponses(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "blocks ok") {
		t.Fatalf("expected message block response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestResponsesEndpointStreamsResponseEvents(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "responses-stream-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"streamed\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "local", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"model","input":"hello","stream":true}`))
	rec := httptest.NewRecorder()
	srv.handleResponses(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response.output_text.delta") || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("expected Responses SSE events, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFusionRouteFansOutAndUsesConfiguredJudge(t *testing.T) {
	tmpDir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 0.1\nprintf '%s\\n' '{\"text\":\""+text+"\"}'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "alpha", Type: types.ProviderCustom, CLIPath: write("alpha", "alpha answer"), Models: []string{"alpha-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "beta", Type: types.ProviderCustom, CLIPath: write("beta", "beta answer"), Models: []string{"beta-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "judge", Type: types.ProviderCustom, CLIPath: write("judge", "judged answer"), Models: []string{"judge-model"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "fusion/*", Provider: "fusion", Fallback: []string{"alpha", "beta"}, Judge: "judge"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"fusion/task","messages":[{"role":"user","content":"compare"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "judged answer") {
		t.Fatalf("expected judged fusion response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(srv.LiveSnapshot().Telemetry.Recent[0].Attempts) < 2 {
		t.Fatalf("expected fan-out attempts, got %+v", srv.LiveSnapshot().Telemetry.Recent)
	}
}

func TestResponsesFusionFansOutCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\""+text+"\"}'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "alpha", Type: types.ProviderCustom, CLIPath: write("responses-alpha", "alpha response"), Models: []string{"alpha-model"}, Enabled: true},
			{Name: "beta", Type: types.ProviderCustom, CLIPath: write("responses-beta", "beta response"), Models: []string{"beta-model"}, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "responses-fusion/*", Provider: "fusion", Fallback: []string{"alpha", "beta"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"responses-fusion/task","input":"compare"}`))
	rec := httptest.NewRecorder()
	srv.handleResponses(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response") {
		t.Fatalf("expected fused Responses response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || len(recent[0].Attempts) != 2 {
		t.Fatalf("expected two Responses fusion attempts, got %+v", recent)
	}
}

func TestAnthropicFusionFansOutCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\""+text+"\"}'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "alpha", Type: types.ProviderCustom, CLIPath: write("anthropic-alpha", "alpha response"), Models: []string{"alpha-model"}, Enabled: true},
			{Name: "beta", Type: types.ProviderCustom, CLIPath: write("anthropic-beta", "beta response"), Models: []string{"beta-model"}, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "anthropic-fusion/*", Provider: "fusion", Fallback: []string{"alpha", "beta"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"anthropic-fusion/task","max_tokens":32,"messages":[{"role":"user","content":"compare"}]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response") {
		t.Fatalf("expected fused Anthropic response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || len(recent[0].Attempts) != 2 {
		t.Fatalf("expected two Anthropic fusion attempts, got %+v", recent)
	}
}

func TestFusionRouteHonorsCandidateBudget(t *testing.T) {
	tmpDir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"candidate\"}'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "alpha", Type: types.ProviderCustom, CLIPath: write("budget-alpha"), Models: []string{"alpha-model"}, Enabled: true},
			{Name: "beta", Type: types.ProviderCustom, CLIPath: write("budget-beta"), Models: []string{"beta-model"}, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "budget/*", Provider: "fusion", MaxCandidates: 1, Fallback: []string{"alpha", "beta"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"budget/task","messages":[{"role":"user","content":"test"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected budgeted fusion response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || len(recent[0].Attempts) != 1 {
		t.Fatalf("expected one bounded fusion attempt, got %+v", recent)
	}
}

func TestFusionFirstCompleteCancelsSlowerCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	fast := filepath.Join(tmpDir, "fast")
	slow := filepath.Join(tmpDir, "slow")
	if err := os.WriteFile(fast, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"fast winner\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 10\nprintf '%s\\n' '{\"text\":\"slow loser\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "fast", Type: types.ProviderCustom, CLIPath: fast, Models: []string{"fast-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "slow", Type: types.ProviderCustom, CLIPath: slow, Models: []string{"slow-model"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "first/*", Provider: "fusion", FirstComplete: true, Fallback: []string{"fast", "slow"}}},
	})
	started := time.Now()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"first/task","messages":[{"role":"user","content":"choose"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fast winner") {
		t.Fatalf("expected fast first-complete response, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("first-complete waited for cancelled sibling: %s", elapsed)
	}
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || len(recent[0].Attempts) != 2 {
		t.Fatalf("expected both attempts to be accounted for, got %+v", recent)
	}
	if slowModel := srv.catalog.GetModel("slow/slow-model"); slowModel == nil || slowModel.FailureCount != 0 {
		t.Fatalf("expected sibling cancellation not to quarantine slow model, got %+v", slowModel)
	}
}

func TestFusionCostBudgetExcludesKnownExpensiveCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	cheap := filepath.Join(tmpDir, "cheap")
	expensive := filepath.Join(tmpDir, "expensive")
	if err := os.WriteFile(cheap, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"budget winner\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expensive, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"should not run\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "cheap", Type: types.ProviderCustom, CLIPath: cheap, Models: []string{"cheap-model"}, ModelInfo: map[string]types.ModelInfo{"cheap-model": {TokenCost: 1}}, WorkDir: tmpDir, Enabled: true},
			{Name: "expensive", Type: types.ProviderCustom, CLIPath: expensive, Models: []string{"expensive-model"}, ModelInfo: map[string]types.ModelInfo{"expensive-model": {TokenCost: 100}}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "cost/*", Provider: "fusion", FirstComplete: true, MaxCostMicros: 2, Fallback: []string{"cheap", "expensive"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"cost/task","messages":[{"role":"user","content":"choose"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "budget winner") {
		t.Fatalf("expected budget response, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || len(recent[0].Attempts) != 1 || recent[0].Attempts[0].Provider != "cheap" {
		t.Fatalf("expected only cheap candidate to run, got %+v", recent)
	}
}

func TestCursorPrefixRoutesToCursorProvider(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "test-cursor-key")
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "cursor", Type: types.ProviderCursor, CLIPath: "/bin/true", Models: []string{"cu/composer-2"}, Enabled: true}}})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "cu/composer-2"})
	if provider != "cursor" || model != "cu/composer-2" {
		t.Fatalf("expected cursor route, got %q/%q", provider, model)
	}
}

func TestPoolControlPlaneRoutesAndAppearsInLiveSnapshot(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{{Name: "alpha", Type: types.ProviderCustom, Models: []string{"model-a"}, Enabled: true}},
		Pools:     []types.Pool{{Name: "ghrouter/fast", Members: []string{"alpha/model-a"}, Strategy: "round-robin", Enabled: true}},
	})
	provider, model := srv.resolveModelList("ghrouter/fast")
	if provider != "alpha" || model != "model-a" {
		t.Fatalf("expected pool to route to alpha/model-a, got %s/%s", provider, model)
	}
	snapshot := srv.LiveSnapshot()
	if len(snapshot.Pools) != 1 || snapshot.Pools[0].Name != "ghrouter/fast" {
		t.Fatalf("expected pool in live snapshot, got %+v", snapshot.Pools)
	}
}

func TestPoolRoutesThroughConfiguredConnection(t *testing.T) {
	srv := New(&types.Config{
		Providers:   []*types.Provider{{Name: "alpha", Type: types.ProviderCustom, Models: []string{"model-a"}, Enabled: true}},
		Connections: []types.Connection{{Name: "primary", Provider: "alpha", Model: "model-a", Enabled: true}},
		Pools:       []types.Pool{{Name: "ghrouter/fast", Members: []string{"primary"}, Strategy: "round-robin", Enabled: true}},
	})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "ghrouter/fast"})
	if provider != "alpha" || model != "model-a" {
		t.Fatalf("expected connection-backed pool to route to alpha/model-a, got %s/%s", provider, model)
	}
}

func TestChatCompletionsFallsBackAfterProviderExecutionFailure(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary")
	backup := filepath.Join(tmpDir, "backup")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write primary cli: %v", err)
	}
	backupScript := "#!/bin/sh\ncase \"$*\" in *'-m model-b'*) printf '{\\\"text\\\":\\\"fallback ok\\\"}\\n' ;; *) exit 2 ;; esac\n"
	if err := os.WriteFile(backup, []byte(backupScript), 0o755); err != nil {
		t.Fatalf("write backup cli: %v", err)
	}

	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "primary", Type: types.ProviderCustom, CLIPath: primary, Models: []string{"model-a"}, WorkDir: tmpDir, Enabled: true},
			{Name: "backup", Type: types.ProviderCustom, CLIPath: backup, Models: []string{"model-b"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "auto/*", Provider: "primary", Fallback: []string{"backup"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"auto/task","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected fallback response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fallback ok") {
		t.Fatalf("expected backup response, got %s", rec.Body.String())
	}
	if srv.LiveSnapshot().Telemetry.Fallbacks != 1 {
		t.Fatalf("expected one fallback metric, got %+v", srv.LiveSnapshot().Telemetry)
	}
	recent := srv.LiveSnapshot().Telemetry.Recent
	if len(recent) != 1 || len(recent[0].Attempts) != 2 || recent[0].Attempts[0].Status != "error" || recent[0].Attempts[1].Status != "ok" {
		t.Fatalf("expected ordered attempt telemetry, got %+v", recent)
	}
	if recent[0].PromptTokens == 0 || recent[0].CompletionTokens == 0 {
		t.Fatalf("expected token estimates in telemetry, got %+v", recent[0])
	}
}

func TestFailedModelIsRemovedFromAutomaticList(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-list")
	backup := filepath.Join(tmpDir, "backup-list")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"backup\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "primary", Type: types.ProviderCustom, CLIPath: primary, Models: []string{"model-a"}, WorkDir: tmpDir, Enabled: true},
		{Name: "backup", Type: types.ProviderCustom, CLIPath: backup, Models: []string{"model-b"}, WorkDir: tmpDir, Enabled: true},
	}, ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"primary/model-a", "backup/model-b"}}}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"ghrouter/auto","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "backup") {
		t.Fatalf("expected automatic fallback, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, list := range srv.modelListSummaries() {
		if list.Name == "ghrouter/auto" {
			for _, member := range list.Members {
				if member == "primary/model-a" {
					t.Fatalf("failed model remained in automatic list: %+v", list.Members)
				}
			}
			return
		}
	}
	t.Fatal("automatic list was not returned")
}

func TestCooldownModelCannotBeSelectedByExplicitOrVirtualRoute(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "primary", Type: types.ProviderCustom, Models: []string{"model-a"}, Enabled: true},
		{Name: "backup", Type: types.ProviderCustom, Models: []string{"model-b"}, Enabled: true},
	}, ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"primary/model-a", "backup/model-b"}}}})
	srv.catalog.SetCooldown("primary/model-a", time.Now().Add(time.Minute))

	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "primary/model-a"})
	if provider != "backup" || model != "model-b" {
		t.Fatalf("expected explicit cooldown model to fall through to backup, got %s/%s", provider, model)
	}
	provider, model = srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "ghrouter/auto"})
	if provider != "backup" || model != "model-b" {
		t.Fatalf("expected virtual route to skip cooldown model, got %s/%s", provider, model)
	}
}

func TestAccountResetCooldownExcludesProviderFromRoutesAndLists(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC()
	primaryJSON, err := json.Marshal(map[string]any{
		"source":    "quota",
		"available": true,
		"healthy":   true,
		"balance":   0,
		"reset_at":  resetAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "primary", Type: types.ProviderCustom, AuthConfig: map[string]string{"account_json": string(primaryJSON)}, Models: []string{"model-a"}, Enabled: true},
			{Name: "backup", Type: types.ProviderCustom, Models: []string{"model-b"}, Enabled: true},
		},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"primary/model-a", "backup/model-b"}}},
	})

	primary := srv.catalog.GetModel("primary/model-a")
	if primary == nil || primary.HealthStatus != health.HealthCooldown || !primary.CooldownUntil.Equal(resetAt) {
		t.Fatalf("expected primary models to be in account reset cooldown, got %+v", primary)
	}
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "primary/model-a"})
	if provider != "backup" || model != "model-b" {
		t.Fatalf("expected explicit route to skip account-blocked provider, got %s/%s", provider, model)
	}
	for _, list := range srv.modelListSummaries() {
		if list.Name != "ghrouter/auto" {
			continue
		}
		if len(list.Members) != 1 || list.Members[0] != "model-b" {
			t.Fatalf("expected only functional list member, got %+v", list.Members)
		}
		return
	}
	t.Fatal("expected automatic model list")
}

func TestExplicitUnhealthyModelUsesConfiguredFallback(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-explicit")
	backup := filepath.Join(tmpDir, "backup-explicit")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"explicit fallback ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "primary", Type: types.ProviderCustom, CLIPath: primary, Models: []string{"model-a"}, WorkDir: tmpDir, Enabled: true},
			{Name: "backup", Type: types.ProviderCustom, CLIPath: backup, Models: []string{"model-b"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "primary/*", Provider: "primary", Fallback: []string{"backup"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"primary/model-a","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "explicit fallback ok") {
		t.Fatalf("expected explicit model fallback, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStickyRouteUsesSessionHeaderIdentity(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "alpha", Type: types.ProviderCustom, Models: []string{"a"}, Enabled: true},
		{Name: "beta", Type: types.ProviderCustom, Models: []string{"b"}, Enabled: true},
	}, Routes: []*types.Route{{Pattern: "sticky/*", Provider: "sticky", Fallback: []string{"alpha", "beta"}}}})
	first := &types.OpenAIRequest{Model: "sticky/task", SessionID: "session-1"}
	provider, model := srv.RouteOpenAIRequest(first)
	if provider == "" || model == "" {
		t.Fatal("expected sticky route to select a provider")
	}
	providerAgain, modelAgain := srv.RouteOpenAIRequest(first)
	if providerAgain != provider || modelAgain != model {
		t.Fatalf("expected sticky session to preserve route, got %s/%s then %s/%s", provider, model, providerAgain, modelAgain)
	}
}

func TestStreamingChatFallsBackBeforeHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-stream")
	backup := filepath.Join(tmpDir, "backup-stream")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write primary cli: %v", err)
	}
	backupScript := "#!/bin/sh\ncase \"$*\" in *'-m model-b'*) printf '{\\\"text\\\":\\\"stream fallback ok\\\"}\\n' ;; *) exit 2 ;; esac\n"
	if err := os.WriteFile(backup, []byte(backupScript), 0o755); err != nil {
		t.Fatalf("write backup cli: %v", err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "primary", Type: types.ProviderCustom, CLIPath: primary, Models: []string{"model-a"}, WorkDir: tmpDir, Enabled: true},
			{Name: "backup", Type: types.ProviderCustom, CLIPath: backup, Models: []string{"model-b"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "auto/*", Provider: "primary", Fallback: []string{"backup"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"auto/task","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected streaming fallback response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stream fallback ok") {
		t.Fatalf("expected streaming backup response, got %s", rec.Body.String())
	}
}

func TestStreamingChatFallsBackWhenProviderReportsFirstEventError(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-stream-error")
	backup := filepath.Join(tmpDir, "backup-stream-error")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"error\":\"quota exhausted\"}'\n"), 0o755); err != nil {
		t.Fatalf("write primary cli: %v", err)
	}
	if err := os.WriteFile(backup, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"stream recovered\"}'\n"), 0o755); err != nil {
		t.Fatalf("write backup cli: %v", err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "primary", Type: types.ProviderCustom, CLIPath: primary, Models: []string{"model-a"}, WorkDir: tmpDir, Enabled: true},
			{Name: "backup", Type: types.ProviderCustom, CLIPath: backup, Models: []string{"model-b"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "auto/*", Provider: "primary", Fallback: []string{"backup"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"auto/task","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected recovered stream response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stream recovered") {
		t.Fatalf("expected backup response, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "quota exhausted") {
		t.Fatalf("primary error leaked into recovered stream: %s", rec.Body.String())
	}
}

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

func TestRouteOpenAIRequestUsesCostAndLatencyIntentSlots(t *testing.T) {
	cheap := &types.Provider{Name: "cheap", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"cheap-model"}, Enabled: true, ModelInfo: map[string]types.ModelInfo{"cheap-model": {TokenCost: 0}}}
	fast := &types.Provider{Name: "fast", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"fast-model"}, Enabled: true, ModelInfo: map[string]types.ModelInfo{"fast-model": {ContextWindow: 128000}}}
	srv := New(&types.Config{Providers: []*types.Provider{cheap, fast}})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "give me the cheapest free option"}}})
	if provider != "cheap" || model != "cheap-model" {
		t.Fatalf("expected cheap intent slot, got %s/%s", provider, model)
	}
	provider, model = srv.RouteOpenAIRequest(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "answer quickly with low latency"}}})
	if provider != "fast" || model != "fast-model" {
		t.Fatalf("expected fast intent slot, got %s/%s", provider, model)
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
				Provider: "primary",
				Mode:     "round-robin",
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
