package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ghrouter/internal/types"
)

func TestConvertToInternalRequestPreservesMessages(t *testing.T) {
	server := &Server{}
	request := &AnthropicRequest{
		Model:     "cc/model",
		MaxTokens: 128,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "second"},
		},
	}

	converted := server.convertToInternalRequest(request)
	if len(converted.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(converted.Messages))
	}
	if converted.Messages[0].Content != "first" || converted.Messages[1].Content != "second" {
		t.Fatalf("messages were not preserved: %#v", converted.Messages)
	}
}

func TestAnthropicMessagesRejectsEmptyMessages(t *testing.T) {
	srv := New(&types.Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"model","max_tokens":8,"messages":[]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty messages, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestConvertToInternalRequestExtractsAnthropicContentBlocks(t *testing.T) {
	server := &Server{}
	request := &AnthropicRequest{
		Model: "claude-sonnet-4.6",
		Messages: []AnthropicMessage{{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "first block"},
				map[string]interface{}{"type": "text", "text": "second block"},
			},
		}},
	}

	converted := server.convertToInternalRequest(request)
	if got := converted.Messages[0].Content; got != "first block\nsecond block" {
		t.Fatalf("expected text blocks to be joined, got %#v", got)
	}
}

func TestAnthropicProviderFailureIsReportedAsFailedRequest(t *testing.T) {
	cliPath := filepath.Join(t.TempDir(), "failing-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "anthropic", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, Enabled: true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "provider_error") {
		t.Fatalf("expected provider error response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	telemetry := srv.LiveSnapshot().Telemetry
	if telemetry.Failed != 1 || telemetry.Successful != 0 {
		t.Fatalf("expected failed telemetry, got %+v", telemetry)
	}
}

func TestAnthropicMessagesFallsBackAfterProviderExecutionFailure(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary")
	backup := filepath.Join(tmpDir, "backup")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write primary cli: %v", err)
	}
	backupScript := "#!/bin/sh\ncase \"$*\" in *'-m model-b'*) printf '%s\\n' '{\"text\":\"anthropic fallback\"}' ;; *) exit 2 ;; esac\n"
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
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"auto/task","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "anthropic fallback") {
		t.Fatalf("expected Anthropic fallback response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	telemetry := srv.LiveSnapshot().Telemetry
	if telemetry.Fallbacks != 1 || len(telemetry.Recent) != 1 || len(telemetry.Recent[0].Attempts) != 2 {
		t.Fatalf("expected primary and fallback attempts, got %+v", telemetry)
	}
}

func TestAnthropicStreamingUsesStructuredSSE(t *testing.T) {
	cliPath := filepath.Join(t.TempDir(), "streaming-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '{\"text\":\"streamed\"}\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "custom", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, Enabled: true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	wantEvents := []string{
		"message_start",
		"content_block_start",
		"ping",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	last := -1
	for _, event := range wantEvents {
		position := strings.Index(body, "event: "+event+"\n")
		if position < 0 {
			t.Fatalf("missing %s event in body=%s", event, body)
		}
		if position <= last {
			t.Fatalf("event %s is out of order in body=%s", event, body)
		}
		last = position
	}
	if !strings.Contains(body, `"type":"text_delta"`) || !strings.Contains(body, `"text":"streamed"`) {
		t.Fatalf("expected text_delta payload, body=%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected end_turn stop reason, body=%s", body)
	}
	if strings.Contains(body, `"delta":{"type":"text"`) {
		t.Fatalf("found legacy malformed message delta payload, body=%s", body)
	}
}

func TestAnthropicStreamingProviderFailureEmitsErrorEvent(t *testing.T) {
	cliPath := filepath.Join(t.TempDir(), "anthropic-stream-failure-cli")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"text\":\"partial\"}'\nsleep 0.05\nexit 1\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "custom", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, Enabled: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected committed stream status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "event: error\n") || !strings.Contains(body, `"type":"provider_error"`) {
		t.Fatalf("expected structured Anthropic error event, body=%s", body)
	}
	if strings.Contains(body, "event: message_stop\n") {
		t.Fatalf("failed stream must not advertise message_stop, body=%s", body)
	}
}

func TestAnthropicStreamingFallsBackBeforeHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "primary-stream")
	backup := filepath.Join(tmpDir, "backup-stream")
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write primary cli: %v", err)
	}
	backupScript := "#!/bin/sh\ncase \"$*\" in *'-m model-b'*) printf '%s\\n' '{\"text\":\"anthropic stream fallback\"}' ;; *) exit 2 ;; esac\n"
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
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"auto/task","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	srv.handleAnthropicMessages(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "anthropic stream fallback") {
		t.Fatalf("expected Anthropic streaming fallback response, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"type":"provider_error"`) {
		t.Fatalf("initial provider failure leaked into recovered stream: %s", rec.Body.String())
	}
}

func TestSyntheticAnthropicStreamingUsesStructuredSSE(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := writeSyntheticAnthropicStream(rec, "fusion-model", "merged", 4); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, event := range []string{"message_start", "content_block_start", "ping", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		if !strings.Contains(body, "event: "+event+"\n") {
			t.Fatalf("missing %s event in body=%s", event, body)
		}
	}
	if !strings.Contains(body, `"text":"merged"`) || !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("missing fusion text or stop reason in body=%s", body)
	}
}
