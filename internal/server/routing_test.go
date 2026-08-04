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
	"ghrouter/internal/providers"
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
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "tool", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"tool-model"}, ModelInfo: map[string]types.ModelInfo{"tool-model": {ToolUse: true}}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"tool/tool-model","messages":[{"role":"user","content":"inspect"}],"tools":[{"type":"function","function":{"name":"list_files","description":"list","parameters":{}}}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected tool response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Ghrouter-Provider") != "tool" || rec.Header().Get("X-Ghrouter-Model") != "tool-model" || rec.Header().Get("X-Ghrouter-Candidate-Count") != "1" {
		t.Fatalf("expected route headers, got %v", rec.Header())
	}
	var response types.OpenAIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Name != "list_files" {
		t.Fatalf("expected tool call to survive response, got %+v", response)
	}
}

func TestChatCompletionsPreservesToolsThroughHTTPProvider(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload types.OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider payload: %v", err)
		}
		if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "list_files" {
			t.Fatalf("expected tool schema at HTTP provider, got %+v", payload.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_http","type":"function","function":{"name":"list_files","arguments":"{}"}}]}}]}`))
	}))
	defer providerServer.Close()

	srv := New(&types.Config{Providers: []*types.Provider{{
		Name:    "local",
		Type:    types.ProviderLocal,
		BaseURL: providerServer.URL,
		Models:  []string{"local/model"},
		ModelInfo: map[string]types.ModelInfo{
			"local/model": {VerifiedAt: time.Now().UTC(), HealthStatus: "healthy", ToolUse: true},
		},
		Enabled: true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"local/model","messages":[{"role":"user","content":"inspect"}],"tools":[{"type":"function","function":{"name":"list_files","parameters":{}}}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP provider tool response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response types.OpenAIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Name != "list_files" || response.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected HTTP tool call to survive router, got %+v", response.Choices)
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
	if rec.Header().Get("X-Ghrouter-Provider") != "local" || rec.Header().Get("X-Ghrouter-Model") != "model" {
		t.Fatalf("expected Responses route headers, got %v", rec.Header())
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
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "response.output_text.done") || !strings.Contains(body, "response.content_part.added") || !strings.Contains(body, "response.output_item.done") || !strings.Contains(body, `"response":{"id":"resp_`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected Responses SSE events, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatStreamingProviderFailureEmitsProtocolError(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "chat-stream-failure-cli")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"text\":\"partial\"}'\nsleep 0.05\nexit 1\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "local", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected committed stream status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, `"error":{`) || !strings.Contains(body, `"type":"provider_error"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected protocol error and stream terminator, body=%s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("failed stream must not advertise successful stop, body=%s", body)
	}
}

func TestResponsesStreamingProviderFailureEmitsFailedTerminal(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "responses-stream-failure-cli")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"text\":\"partial\"}'\nsleep 0.05\nexit 1\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "local", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, WorkDir: tmpDir, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"model","input":"hello","stream":true}`))
	rec := httptest.NewRecorder()
	srv.handleResponses(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected committed stream status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "event: response.failed\n") || !strings.Contains(body, "event: error\n") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected failed response terminal and stream terminator, body=%s", body)
	}
	if strings.Contains(body, "event: response.completed\n") {
		t.Fatalf("failed stream must not advertise response.completed, body=%s", body)
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

func TestGraphRouteRunsSpecialistsAndJudge(t *testing.T) {
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
			{Name: "analyst", Type: types.ProviderCustom, CLIPath: write("analyst", "analysis"), Models: []string{"analyst-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "critic", Type: types.ProviderCustom, CLIPath: write("critic", "critique"), Models: []string{"critic-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "judge", Type: types.ProviderCustom, CLIPath: write("judge", "synthesis"), Models: []string{"judge-model"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "graph/*", Provider: "graph", Mode: "graph", Fallback: []string{"analyst", "critic"}, Judge: "judge"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"graph/task","messages":[{"role":"user","content":"analyze the tradeoffs and reason carefully"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "synthesis") {
		t.Fatalf("expected graph judge response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(srv.LiveSnapshot().Telemetry.Recent[0].Attempts) != 3 {
		t.Fatalf("expected two specialists and one judge, got %+v", srv.LiveSnapshot().Telemetry.Recent[0].Attempts)
	}
}

func TestGraphRouteExecutesSequentialStagesWithContext(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "graph-stage-cli")
	script := `#!/bin/sh
prompt=$(cat)
case "$prompt" in
  *"Verify the proposed solution"*) printf '%s\n' '{"text":"VERIFY"}' ;;
  *"Produce the implementation"*) printf '%s\n' '{"text":"IMPLEMENT"}' ;;
  *"Create a concrete, ordered plan"*) printf '%s\n' '{"text":"PLAN"}' ;;
  *) printf '%s\n' '{"text":"UNKNOWN"}' ;;
esac
`
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "alpha", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"alpha-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "beta", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"beta-model"}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "graph/*", Provider: "graph", Mode: "graph", Fallback: []string{"alpha", "beta"}, MaxCandidates: 2}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"graph/task","messages":[{"role":"user","content":"implement this change"}],"tools":[{"type":"function"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "VERIFY") {
		t.Fatalf("expected final verify stage response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	recent := srv.LiveSnapshot().Telemetry.Recent
	if len(recent) != 1 || len(recent[0].Attempts) != 3 {
		t.Fatalf("expected three ordered graph stage attempts, got %+v", recent)
	}
	if recent[0].Attempts[0].Model != "alpha-model" || recent[0].Attempts[1].Model != "beta-model" || recent[0].Attempts[2].Model != "alpha-model" {
		t.Fatalf("expected graph stage candidate rotation, got %+v", recent[0].Attempts)
	}
}

func TestGraphRouteSupportsResponsesAndAnthropic(t *testing.T) {
	tmpDir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\""+text+"\"}'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	providers := []*types.Provider{
		{Name: "analyst", Type: types.ProviderCustom, CLIPath: write("responses-analyst", "analysis"), Models: []string{"analyst-model"}, WorkDir: tmpDir, Enabled: true},
		{Name: "critic", Type: types.ProviderCustom, CLIPath: write("responses-critic", "critique"), Models: []string{"critic-model"}, WorkDir: tmpDir, Enabled: true},
		{Name: "judge", Type: types.ProviderCustom, CLIPath: write("responses-judge", "synthesis"), Models: []string{"judge-model"}, WorkDir: tmpDir, Enabled: true},
	}
	srv := New(&types.Config{Providers: providers, Routes: []*types.Route{{Pattern: "graph/*", Provider: "graph", Mode: "graph", Fallback: []string{"analyst", "critic"}, Judge: "judge"}}})
	responsesReq := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"graph/task","input":"analyze the tradeoffs"}`))
	responsesRec := httptest.NewRecorder()
	srv.handleResponses(responsesRec, responsesReq)
	if responsesRec.Code != http.StatusOK || !strings.Contains(responsesRec.Body.String(), "synthesis") {
		t.Fatalf("expected graph Responses output, got status=%d body=%s", responsesRec.Code, responsesRec.Body.String())
	}
	anthropicReq := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"graph/task","max_tokens":32,"messages":[{"role":"user","content":"analyze the tradeoffs"}]}`))
	anthropicRec := httptest.NewRecorder()
	srv.handleAnthropicMessages(anthropicRec, anthropicReq)
	if anthropicRec.Code != http.StatusOK || !strings.Contains(anthropicRec.Body.String(), "synthesis") {
		t.Fatalf("expected graph Anthropic output, got status=%d body=%s", anthropicRec.Code, anthropicRec.Body.String())
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

func TestFusionCostBudgetExcludesUnknownCostCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	unknown := filepath.Join(tmpDir, "unknown")
	cheap := filepath.Join(tmpDir, "cheap")
	if err := os.WriteFile(unknown, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"should not run\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cheap, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"known budget winner\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "unknown", Type: types.ProviderCustom, CLIPath: unknown, Models: []string{"unknown-model"}, WorkDir: tmpDir, Enabled: true},
			{Name: "cheap", Type: types.ProviderCustom, CLIPath: cheap, Models: []string{"cheap-model"}, ModelInfo: map[string]types.ModelInfo{"cheap-model": {TokenCost: 1}}, WorkDir: tmpDir, Enabled: true},
		},
		Routes: []*types.Route{{Pattern: "unknown-cost/*", Provider: "fusion", FirstComplete: true, MaxCostMicros: 2, Fallback: []string{"unknown", "cheap"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"unknown-cost/task","messages":[{"role":"user","content":"choose"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "known budget winner") {
		t.Fatalf("expected known-cost response, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recent := srv.LiveSnapshot().Telemetry.Recent; len(recent) != 1 || len(recent[0].Attempts) != 1 || recent[0].Attempts[0].Provider != "cheap" {
		t.Fatalf("expected only known-cost candidate to run, got %+v", recent)
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

func TestExplicitProviderPrefixDoesNotFallThroughWhenModelUnavailable(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	srv := NewWithConfigPath(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "cursor",
				Type:    types.ProviderCursor,
				CLIPath: "/bin/true",
				Models:  []string{"cu/auto"},
				ModelInfo: map[string]types.ModelInfo{
					"cu/auto": {VerifiedAt: time.Now().UTC(), HealthStatus: "failed"},
				},
				Enabled: true,
			},
			{Name: "mimo", Type: types.ProviderMimo, CLIPath: "/bin/true", Models: []string{"mi/backup"}, Enabled: true},
		},
	}, configPath)

	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "cu/auto"})
	if provider != "cursor" || model != "cu/auto" {
		t.Fatalf("expected explicit Cursor route to remain pinned, got %q/%q", provider, model)
	}
	if candidates := srv.routeCandidates("cu/auto", provider, model); len(candidates) != 0 {
		t.Fatalf("expected unavailable explicit model to have no fallback candidates, got %+v", candidates)
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
	backupScript := "#!/bin/sh\ncase \"$*\" in *model-b*) printf '{\\\"text\\\":\\\"fallback ok\\\"}\\n' ;; *) exit 2 ;; esac\n"
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
	}, ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"primary/model-a", "backup/model-b"}}}, ModelPolicy: types.ModelPolicy{Preferred: []string{"primary/model-a"}}})

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

func TestVirtualRouteVerifiesUnverifiedCandidateOnDemand(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "probe-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"GHROUTER_MODEL_PROBE_OK\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("providers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewWithConfigPath(&types.Config{
		Providers: []*types.Provider{{
			Name:    "custom",
			Type:    types.ProviderCustom,
			CLIPath: cliPath,
			Models:  []string{"model-a"},
			WorkDir: tmpDir,
			Enabled: true,
		}},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Strategy: "score"}},
	}, configPath)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"ghrouter/auto","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "GHROUTER_MODEL_PROBE_OK") {
		t.Fatalf("expected on-demand verification and routed response, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "ghrouter/auto"}); provider != "custom" || model != "model-a" {
		t.Fatalf("expected verified candidate in automatic route, got %s/%s", provider, model)
	}
}

func TestVirtualRouteExpandsFallbackAfterVerifiedCandidateFails(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-fails")
	backup := filepath.Join(tmpDir, "backup-works")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"GHROUTER_MODEL_PROBE_OK\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("providers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewWithConfigPath(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "primary",
				Type:    types.ProviderCustom,
				CLIPath: primary,
				Models:  []string{"model-a"},
				ModelInfo: map[string]types.ModelInfo{
					"model-a": {Source: "native", VerifiedAt: time.Now().UTC(), HealthStatus: "healthy"},
				},
				WorkDir: tmpDir,
				Enabled: true,
			},
			{
				Name:    "backup",
				Type:    types.ProviderCustom,
				CLIPath: backup,
				Models:  []string{"model-b"},
				WorkDir: tmpDir,
				Enabled: true,
			},
		},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"primary/model-a"}}},
	}, configPath)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"ghrouter/auto","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "GHROUTER_MODEL_PROBE_OK") {
		t.Fatalf("expected post-failure verification fallback, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if provider := rec.Header().Get("X-Ghrouter-Provider"); provider != "backup" {
		t.Fatalf("expected backup provider header, got %q", provider)
	}
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
		if len(list.Members) != 1 || list.Members[0] != "backup/model-b" {
			t.Fatalf("expected only functional list member, got %+v", list.Members)
		}
		return
	}
	t.Fatal("expected automatic model list")
}

func TestExplicitCapacityUnavailableProviderFallsBackWithoutResetDate(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "primary", Type: types.ProviderCustom, AuthConfig: map[string]string{"account_json": `{"source":"quota","available":false,"healthy":false}`}, Models: []string{"model-a"}, Enabled: true},
			{Name: "backup", Type: types.ProviderCustom, Models: []string{"model-b"}, Enabled: true},
		},
	})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "primary/model-a"})
	if provider != "backup" || model != "model-b" {
		t.Fatalf("expected known unavailable capacity to fall back, got %q/%q", provider, model)
	}
}

func TestCapacityFailureUsesExplicitRetryAfterForProviderCooldown(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "primary", Type: types.ProviderCustom, Models: []string{"model-a", "model-b"}, Enabled: true},
		{Name: "backup", Type: types.ProviderCustom, Models: []string{"model-c"}, Enabled: true},
	}})
	srv.recordModelFailure("primary", "model-a", &providers.CapacityError{StatusCode: http.StatusTooManyRequests, RetryAfter: 2 * time.Minute})
	for _, modelID := range []string{"primary/model-a", "primary/model-b"} {
		entry := srv.catalog.GetModel(modelID)
		if entry == nil || entry.HealthStatus != health.HealthCooldown || time.Until(entry.CooldownUntil) < time.Minute {
			t.Fatalf("expected provider cooldown from explicit retry-after for %s, got %+v", modelID, entry)
		}
	}
	if entry := srv.catalog.GetModel("backup/model-c"); entry == nil || entry.HealthStatus == health.HealthCooldown {
		t.Fatalf("expected unrelated provider to remain available, got %+v", entry)
	}
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

func TestBareVirtualModelAliasRoutesToGhrouterList(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "alpha", Type: types.ProviderCustom, Models: []string{"model-a"}, Enabled: true}}})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "auto"})
	if provider != "alpha" || model != "model-a" {
		t.Fatalf("expected bare auto alias to resolve to virtual list member, got %s/%s", provider, model)
	}
	candidates := srv.routeCandidates("auto", provider, model)
	if len(candidates) != 1 || candidates[0].provider != "alpha" || candidates[0].model != "model-a" {
		t.Fatalf("expected bare auto alias candidates, got %+v", candidates)
	}
}

func TestStreamingChatFallsBackBeforeHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-stream")
	backup := filepath.Join(tmpDir, "backup-stream")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write primary cli: %v", err)
	}
	backupScript := "#!/bin/sh\ncase \"$*\" in *model-b*) printf '{\\\"text\\\":\\\"stream fallback ok\\\"}\\n' ;; *) exit 2 ;; esac\n"
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
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"error\":{\"code\":429,\"message\":\"rate limit reached\"}}'\n"), 0o755); err != nil {
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
	if strings.Contains(rec.Body.String(), "rate limit reached") {
		t.Fatalf("primary error leaked into recovered stream: %s", rec.Body.String())
	}
	if entry := srv.catalog.GetModel("primary/model-a"); entry == nil || entry.HealthStatus != health.HealthCooldown {
		t.Fatalf("expected nested quota error to cooldown primary model, got %+v", entry)
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
	cheap := &types.Provider{Name: "cheap", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"cheap-model"}, Enabled: true, ModelInfo: map[string]types.ModelInfo{"cheap-model": {CostTier: "free"}}}
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

func TestModelPolicyPrefersOpenCodeAndLimitsPaidHarnesses(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	srv := New(&types.Config{
		ModelPolicy: types.ModelPolicy{
			Allowed:   []string{"oc/*", "nv/*", "cx/*sol", "cx/*terra", "cx/*luna", "cx/gpt-5.4-mini", "cc/*opus-5", "cc/*sonnet-5", "cc/*haiku-4-5"},
			Preferred: []string{"oc/*", "nv/*"},
		},
		Routes: []*types.Route{{Pattern: "auto/*", Provider: "auto", Mode: "auto"}},
		Providers: []*types.Provider{
			{Name: "codex", Type: types.ProviderCodex, CLIPath: "/bin/true", Models: []string{"cx/gpt-5.4", "cx/gpt-5.4-mini", "cx/gpt-5.6-sol"}, Enabled: true},
			{Name: "opencode", Type: types.ProviderOpenCode, CLIPath: "/bin/true", Models: []string{"oc/free-model"}, Enabled: true},
		},
	})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}})
	if provider != "opencode" || model != "oc/free-model" {
		t.Fatalf("expected preferred OpenCode model, got %s/%s", provider, model)
	}
	if provider, model := srv.RouteModel("cx/gpt-5.4"); provider != "" || model != "" {
		t.Fatalf("disallowed Codex model routed as %s/%s", provider, model)
	}
	if provider, model := srv.RouteModel("cx/gpt-5.6-sol"); provider != "codex" || model != "cx/gpt-5.6-sol" {
		t.Fatalf("allowed Codex model did not route: %s/%s", provider, model)
	}
	if provider, model := srv.RouteModel("auto/request"); provider != "opencode" || model != "oc/free-model" {
		t.Fatalf("automatic route bypassed cost policy: %s/%s", provider, model)
	}
	if !matchesModelPolicy("cu/cursor-grok-4.5-high", []string{"cu/cursor-grok-*", "cu/composer*"}) || !matchesModelPolicy("cu/composer-2.5", []string{"cu/cursor-grok-*", "cu/composer*"}) {
		t.Fatal("approved Cursor Grok/Composer patterns did not match")
	}
	if matchesModelPolicy("cu/claude-opus-5-high", []string{"cu/cursor-grok-*", "cu/composer*"}) {
		t.Fatal("unapproved Cursor model matched policy")
	}
}

func TestVirtualListRanksFunctionalModelByRequestAndEffort(t *testing.T) {
	verifiedAt := time.Now().UTC()
	first := &types.Provider{
		Name: "first", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"model-a"}, ModelInfo: map[string]types.ModelInfo{
			"model-a": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy", Effort: []string{"low"}},
		},
	}
	second := &types.Provider{
		Name: "second", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"model-b"}, ModelInfo: map[string]types.ModelInfo{
			"model-b": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy", Thinking: true, Effort: []string{"high"}},
		},
	}
	srv := New(&types.Config{
		Providers:  []*types.Provider{first, second},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Strategy: "score", Models: []string{"first/model-a", "second/model-b"}}},
	})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{
		Model: "ghrouter/auto", ReasoningEffort: "high",
		Messages: []types.OpenAIMessage{{Role: "user", Content: "analyze the architecture and reason carefully"}},
	})
	if provider != "second" || model != "model-b" {
		t.Fatalf("expected reasoning-capable model-b, got %s/%s", provider, model)
	}
}

func TestSelectedVirtualModelRemainsFirstBeforeFallbackRanking(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:1", Models: []string{"selector"}, Enabled: true},
			{Name: "codex", Type: types.ProviderCodex, CLIPath: "/bin/true", Models: []string{"cx/selected"}, Enabled: true},
		},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"local-brain/selector", "cx/selected"}}},
	})
	req := &types.OpenAIRequest{Model: "ghrouter/auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}
	candidates := srv.routeCandidates(req.Model, "codex", "cx/selected", req)
	if len(candidates) < 2 || candidates[0].provider != "codex" || candidates[0].model != "cx/selected" {
		t.Fatalf("expected selected model first, got %+v", candidates)
	}
}

func TestVirtualListRequiresVisionCapability(t *testing.T) {
	verifiedAt := time.Now().UTC()
	preferred := &types.Provider{
		Name: "preferred", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"model-a"}, ModelInfo: map[string]types.ModelInfo{
			"model-a": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy"},
		},
	}
	vision := &types.Provider{
		Name: "vision", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"model-b"}, ModelInfo: map[string]types.ModelInfo{
			"model-b": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy", Vision: true},
		},
	}
	srv := New(&types.Config{
		ModelPolicy: types.ModelPolicy{Allowed: []string{"preferred/*", "vision/*"}, Preferred: []string{"preferred/*"}},
		Providers:   []*types.Provider{preferred, vision},
		ModelLists:  []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Strategy: "score", Models: []string{"preferred/model-a", "vision/model-b"}}},
	})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{
		Model:    "ghrouter/auto",
		Messages: []types.OpenAIMessage{{Role: "user", Content: "analyze this screenshot"}},
	})
	if provider != "vision" || model != "model-b" {
		t.Fatalf("expected vision-capable model-b despite preferred non-vision model, got %s/%s", provider, model)
	}
}

func TestAutomaticRouteDoesNotUseIncompatibleSoleToolCandidate(t *testing.T) {
	verifiedAt := time.Now().UTC()
	srv := New(&types.Config{
		Providers: []*types.Provider{{
			Name: "local-brain", Type: types.ProviderLocal, Enabled: true,
			Models: []string{"local-model"},
			ModelInfo: map[string]types.ModelInfo{
				"local-model": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy"},
			},
		}},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"local-brain/local-model"}}},
	})

	req := &types.OpenAIRequest{
		Model: "ghrouter/auto",
		Tools: []types.OpenAITool{{Type: "function", Function: types.OpenAIToolFunc{Name: "list_files"}}},
	}
	if candidates := srv.routeCandidates(req.Model, "", "", req); len(candidates) != 0 {
		t.Fatalf("automatic route selected a model without tool capability: %+v", candidates)
	}
}

func TestAutomaticRouteUsesConfiguredToolCapabilityForLocalBrain(t *testing.T) {
	verifiedAt := time.Now().UTC()
	srv := New(&types.Config{
		Providers: []*types.Provider{{
			Name: "local-brain", Type: types.ProviderLocal, CLIPath: "/bin/true", Enabled: true,
			Models: []string{"local-model"},
			ModelInfo: map[string]types.ModelInfo{
				"local-model": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy", ToolUse: true},
			},
		}},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"local-brain/local-model"}}},
	})

	req := &types.OpenAIRequest{
		Model: "ghrouter/auto",
		Tools: []types.OpenAITool{{Type: "function", Function: types.OpenAIToolFunc{Name: "list_files"}}},
	}
	if candidates := srv.routeCandidates(req.Model, "", "", req); len(candidates) != 1 || candidates[0].provider != "local-brain" {
		t.Fatalf("local Brain should be eligible for tools when capability is configured: %+v", candidates)
	}
}

func TestAutomaticRouteDoesNotAdvertiseUnverifiedLocalEndpointForTools(t *testing.T) {
	verifiedAt := time.Now().UTC()
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name: "local-brain", Type: types.ProviderLocal, CLIPath: "/bin/true", BaseURL: "http://127.0.0.1:1", Enabled: true,
				Models: []string{"local-model"},
				ModelInfo: map[string]types.ModelInfo{
					"local-model": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy"},
				},
			},
			{
				Name: "codex", Type: types.ProviderCodex, CLIPath: "/bin/true", Enabled: true,
				Models: []string{"cx/gpt-5.6-sol"},
				ModelInfo: map[string]types.ModelInfo{
					"cx/gpt-5.6-sol": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy"},
				},
			},
		},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"local-brain/local-model", "cx/gpt-5.6-sol"}}},
	})

	req := &types.OpenAIRequest{
		Model: "ghrouter/auto",
		Tools: []types.OpenAITool{{Type: "function", Function: types.OpenAIToolFunc{Name: "list_files"}}},
	}
	candidates := srv.routeCandidates(req.Model, "", "", req)
	if len(candidates) != 1 || candidates[0].provider != "codex" || candidates[0].model != "cx/gpt-5.6-sol" {
		t.Fatalf("unverified local endpoint must be excluded while native Codex remains eligible, got %+v", candidates)
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
