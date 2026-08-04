package observability

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestConfigureFromEnvWritesStructuredColoredLevels(t *testing.T) {
	var output bytes.Buffer
	t.Setenv("GHR_LOG_LEVEL", "debug")
	t.Setenv("GHR_LOG_FORMAT", "text")
	t.Setenv("GHR_LOG_COLOR", "always")
	closeLogs, err := ConfigureFromEnv(&output)
	if err != nil {
		t.Fatalf("configure logs: %v", err)
	}
	defer closeLogs()
	Logger("test").Debug("debug event", "request_id", "req_test")
	Logger("test").Error("error event", "code", string(CodeProvider))
	text := output.String()
	for _, value := range []string{"service=ghrouter", "category=test", "request_id=req_test", "code=provider_error", "\x1b[90m", "\x1b[31m"} {
		if !strings.Contains(text, value) {
			t.Fatalf("expected log output to contain %q, got %q", value, text)
		}
	}
}

func TestConfigureFromEnvWritesJSONWithoutColor(t *testing.T) {
	var output bytes.Buffer
	t.Setenv("GHR_LOG_LEVEL", "info")
	t.Setenv("GHR_LOG_FORMAT", "json")
	t.Setenv("GHR_LOG_COLOR", "always")
	closeLogs, err := ConfigureFromEnv(&output)
	if err != nil {
		t.Fatalf("configure logs: %v", err)
	}
	defer closeLogs()
	Logger("test").Info("startup", "component", "cli")
	if strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), `"category":"test"`) {
		t.Fatalf("expected uncolored JSON logs, got %q", output.String())
	}
}

func TestConfigureUsesExplicitSettings(t *testing.T) {
	var output bytes.Buffer
	closeLogs, err := Configure(&output, Settings{Level: "debug", Format: "text", Output: "stderr", Color: "never"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogs()
	Logger("test").Debug("debug-visible")
	if !strings.Contains(output.String(), "debug-visible") {
		t.Fatalf("expected configured debug log, got %q", output.String())
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("expected color disabled, got %q", output.String())
	}
}

func TestConfigureRejectsFileOutputWithoutPath(t *testing.T) {
	if _, err := Configure(io.Discard, Settings{Output: "file"}); err == nil {
		t.Fatal("expected file output without path to fail")
	}
}

func TestPublicErrorDoesNotExposeCause(t *testing.T) {
	err := NewError(CodeProvider, "providers", "provider request failed", os.ErrPermission)
	if got := PublicError(err); got != "provider request failed" {
		t.Fatalf("expected safe public error, got %q", got)
	}
	if !IsCode(err, CodeProvider) {
		t.Fatal("expected typed provider error")
	}
}
