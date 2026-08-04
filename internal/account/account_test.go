package account

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ghrouter/internal/types"
)

func TestLoadReadsEnvBackedAccountStatus(t *testing.T) {
	t.Setenv("GHR_PROVIDER_CLAUDE_CODE_PLAN", "pro")
	t.Setenv("GHR_PROVIDER_CLAUDE_CODE_BALANCE", "0.75")
	t.Setenv("GHR_PROVIDER_CLAUDE_CODE_BALANCE_CURRENCY", "USD")
	t.Setenv("GHR_PROVIDER_CLAUDE_CODE_RESET_AT", time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339))

	status := Load(&types.Provider{Name: "claude-code"})
	if !status.Available || !status.Healthy {
		t.Fatalf("expected available/healthy status, got %+v", status)
	}
	if status.Plan != "pro" {
		t.Fatalf("expected plan pro, got %+v", status)
	}
	if status.Balance == nil || *status.Balance != 0.75 {
		t.Fatalf("expected balance 0.75, got %+v", status)
	}
	if status.Currency != "USD" {
		t.Fatalf("expected currency USD, got %+v", status)
	}
	if status.ResetAt.IsZero() {
		t.Fatalf("expected reset time, got %+v", status)
	}
	if Weight(status) <= 1.0 {
		t.Fatalf("expected weight boost for funded account, got %+v", status)
	}
}

func TestLoadMarksUnavailableWhenNoSignals(t *testing.T) {
	status := Load(&types.Provider{Name: "unknown"})
	if status.Available {
		t.Fatalf("expected unavailable status, got %+v", status)
	}
	if status.Healthy {
		t.Fatalf("expected unhealthy status, got %+v", status)
	}
	if Blocked(status) {
		t.Fatal("expected unknown account metadata to remain non-blocking")
	}
	if Weight(status) >= 1.0 {
		t.Fatalf("expected low weight for unavailable account, got %+v", status)
	}
}

func TestLoadMarksUnsupportedProviderState(t *testing.T) {
	status := Load(&types.Provider{Name: "custom", Type: types.ProviderCustom})
	if status.Source != "unsupported" {
		t.Fatalf("expected unsupported source, got %+v", status)
	}
	if status.Available || status.Healthy {
		t.Fatalf("expected unsupported provider to be unavailable, got %+v", status)
	}
}

func TestLoadMarksMissingAuthState(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	status := Load(&types.Provider{Name: "claude-code", Type: types.ProviderClaudeCode})
	if status.Source != "auth" {
		t.Fatalf("expected auth source, got %+v", status)
	}
	if status.Available || status.Healthy {
		t.Fatalf("expected missing-auth provider to be unavailable, got %+v", status)
	}
}

func TestLoadMarksUnknownStateWhenAuthIsPresent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")

	status := Load(&types.Provider{Name: "claude-code", Type: types.ProviderClaudeCode})
	if status.Source != "unknown" {
		t.Fatalf("expected unknown source, got %+v", status)
	}
	if !status.Available || !status.Healthy {
		t.Fatalf("expected authenticated provider to remain available, got %+v", status)
	}
}

func TestLoadRecognizesConfiguredNVIDIAKeyEnvironment(t *testing.T) {
	t.Setenv("TEAM_NVIDIA_KEY", "test")
	status := Load(&types.Provider{
		Name:       "nvidia",
		Type:       types.ProviderNVIDIA,
		AuthConfig: map[string]string{"api_key_env": "TEAM_NVIDIA_KEY"},
	})
	if !status.Available || !status.Healthy || status.Source != "unknown" {
		t.Fatalf("expected configured NVIDIA auth to be available, got %+v", status)
	}
}

func TestLoadRecognizesNativeCredentialFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MIMO_API_KEY", "")

	cases := []struct {
		name      string
		typeValue types.ProviderType
		path      string
	}{
		{name: "opencode", typeValue: types.ProviderOpenCode, path: ".local/share/opencode/auth.json"},
		{name: "mimo", typeValue: types.ProviderMimo, path: ".local/share/mimocode/auth.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(home, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"provider":"native"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			status := Load(&types.Provider{Name: tc.name, Type: tc.typeValue})
			if !status.Available || !status.Healthy || status.Source != "native" {
				t.Fatalf("expected native credential status, got %+v", status)
			}
		})
	}
}

func TestLoadRecognizesCursorNativeStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_API_KEY", "")
	cliPath := filepath.Join(home, "cursor")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nif [ \"$1\" = agent ] && [ \"$2\" = status ]; then printf '%s\\n' 'Logged in as test@example.com'; exit 0; fi\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	status := Load(&types.Provider{Name: "cursor", Type: types.ProviderCursor, CLIPath: cliPath})
	if !status.Available || !status.Healthy || status.Source != "native" {
		t.Fatalf("expected native Cursor status, got %+v", status)
	}
}

func TestNativeAuthStatusBoundsDescendantPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim is not wired for windows in this repo")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_API_KEY", "")
	cliPath := filepath.Join(home, "cursor")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nif [ \"$1\" = agent ] && [ \"$2\" = status ]; then\n  /bin/sleep 6 &\n  /bin/sleep 6\nfi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	status := Load(&types.Provider{Name: "cursor", Type: types.ProviderCursor, CLIPath: cliPath})
	if status.Source == "native" {
		t.Fatalf("expected timed-out native status to remain unverified, got %+v", status)
	}
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("native status wait exceeded cleanup bound: %s", elapsed)
	}
}

func TestLoadReadsJSONAccountStatus(t *testing.T) {
	payload, err := json.Marshal(Status{
		Plan:      "enterprise",
		Balance:   floatPtr(4.25),
		Currency:  "USD",
		Source:    "payload",
		Available: true,
		Healthy:   true,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	t.Setenv("GHR_PROVIDER_ANTHROPIC_ACCOUNT_JSON", string(payload))

	status := Load(&types.Provider{Name: "anthropic"})
	if status.Plan != "enterprise" {
		t.Fatalf("expected plan enterprise, got %+v", status)
	}
	if status.Balance == nil || *status.Balance != 4.25 {
		t.Fatalf("expected balance 4.25, got %+v", status)
	}
	if status.Currency != "USD" {
		t.Fatalf("expected currency USD, got %+v", status)
	}
	if status.Source != "payload" {
		t.Fatalf("expected source payload, got %+v", status)
	}
	if !status.Available || !status.Healthy {
		t.Fatalf("expected available and healthy status, got %+v", status)
	}
}

func TestLoadPreservesExplicitUnavailableJSONStatus(t *testing.T) {
	t.Setenv("GHR_PROVIDER_CURSOR_ACCOUNT_JSON", `{"available":false,"healthy":false,"source":"quota"}`)

	status := Load(&types.Provider{Name: "cursor"})
	if status.Available || status.Healthy {
		t.Fatalf("expected explicit unavailable status to remain false, got %+v", status)
	}
	if status.Source != "quota" {
		t.Fatalf("expected quota source, got %+v", status)
	}
	if !Blocked(status) {
		t.Fatal("expected explicit unavailable account to block routing")
	}
}

func TestLoadMarksInvalidJSONUnavailable(t *testing.T) {
	t.Setenv("GHR_PROVIDER_OPENAI_ACCOUNT_JSON", "{invalid")

	status := Load(&types.Provider{Name: "openai"})
	if status.Available {
		t.Fatalf("expected unavailable status, got %+v", status)
	}
	if status.Source != "invalid-json" {
		t.Fatalf("expected invalid-json source, got %+v", status)
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
