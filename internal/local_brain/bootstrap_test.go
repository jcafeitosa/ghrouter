package local_brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ghrouter/internal/types"
)

type recordingProvisionRunner struct {
	commands [][]string
}

func (r *recordingProvisionRunner) Run(_ context.Context, name string, args ...string) error {
	r.commands = append(r.commands, append([]string{name}, args...))
	return nil
}

func TestExecuteProvisionPlanRunsOnlyApprovedCommands(t *testing.T) {
	runner := &recordingProvisionRunner{}
	actions := []ProvisionAction{
		{Action: "backend_setup", ApplyOK: true, Command: []string{"python3", "-m", "pip", "install", "mlx-lm"}},
		{Action: "auth_setup", ApplyOK: false, Command: []string{"echo", "must-not-run"}},
	}

	if err := ExecuteProvisionPlan(context.Background(), actions, runner); err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(runner.commands) != 1 || runner.commands[0][0] != "python3" {
		t.Fatalf("expected only approved command, got %+v", runner.commands)
	}
}

func TestExecuteProvisionPlanRejectsShellCommands(t *testing.T) {
	runner := &recordingProvisionRunner{}
	actions := []ProvisionAction{{Action: "backend_setup", ApplyOK: true, Command: []string{"sh", "-c", "touch /tmp/unsafe"}}}
	if err := ExecuteProvisionPlan(context.Background(), actions, runner); err == nil {
		t.Fatal("expected shell command rejection")
	}
}

type fakeBackendAvailability struct {
	available map[BackendType]bool
}

func (f fakeBackendAvailability) IsBackendAvailable(backend BackendType) bool {
	return f.available[backend]
}

type fakeModelPresence struct {
	present map[string]bool
}

func (f fakeModelPresence) HasModel(backend BackendType, modelID string) bool {
	return f.present[string(backend)+"/"+modelID]
}

func TestBootstrapperCheckReady(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	bootstrapper := &Bootstrapper{
		Detector:     fakeBackendAvailability{available: map[BackendType]bool{}},
		ModelManager: fakeModelPresence{present: map[string]bool{}},
	}

	report, err := bootstrapper.Check([]*types.Provider{
		{
			Name:    "claude-code",
			Type:    types.ProviderClaudeCode,
			Models:  []string{"claude-sonnet-5"},
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("check returned error: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("expected ready report, got issues: %+v", report.Issues)
	}
	if report.Backend != BackendExternalCLI {
		t.Fatalf("expected external CLI backend, got %q", report.Backend)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one startup check, got %d", len(report.Checks))
	}
	if len(report.Summary().Provision) != 0 {
		t.Fatalf("expected no provision plan for ready startup check, got %+v", report.Summary().Provision)
	}
	if !report.Checks[0].Ready || !report.Checks[0].AuthOK || !report.Checks[0].ModelOK {
		t.Fatalf("expected ready startup check, got %+v", report.Checks[0])
	}
	if report.Checks[0].NextStep != "" {
		t.Fatalf("expected no next step for ready startup check, got %+v", report.Checks[0])
	}
}

func TestBootstrapperCheckReportsMissingBackendAndModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	backend := preferredBackendForHost()
	bootstrapper := &Bootstrapper{
		Detector: fakeBackendAvailability{available: map[BackendType]bool{}},
		ModelManager: fakeModelPresence{present: map[string]bool{
			string(backend) + "/claude-sonnet-5": false,
		}},
	}

	report, err := bootstrapper.Check([]*types.Provider{
		{
			Name:    "local",
			Type:    types.ProviderCustom,
			Models:  []string{"claude-sonnet-5"},
			Enabled: true,
		},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if report.Ready() {
		t.Fatalf("expected report not ready")
	}
	if len(report.Issues) == 0 {
		t.Fatalf("expected issues")
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one startup check, got %d", len(report.Checks))
	}
	if len(report.Summary().Provision) != 1 {
		t.Fatalf("expected one provision action, got %+v", report.Summary().Provision)
	}
	if report.Checks[0].Ready {
		t.Fatalf("expected non-ready startup check, got %+v", report.Checks[0])
	}
	if report.Summary().Provision[0].Action == "" {
		t.Fatalf("expected provision action, got %+v", report.Summary().Provision[0])
	}
	if report.Checks[0].NextStep == "" {
		t.Fatalf("expected next step for non-ready startup check, got %+v", report.Checks[0])
	}
	if !strings.Contains(err.Error(), "startup prerequisites missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapReportSummaryLines(t *testing.T) {
	report := BootstrapReport{
		Backend: BackendLLAMACPP,
		Issues: []BootstrapIssue{{
			Provider: "claude-code",
			Backend:  BackendLLAMACPP,
			Model:    "claude-sonnet-5",
			Reason:   "model not present in local cache",
		}},
	}

	lines := report.SummaryLines()
	if len(lines) != 1 {
		t.Fatalf("expected one summary line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "claude-code") || !strings.Contains(lines[0], "model not present in local cache") {
		t.Fatalf("unexpected summary line: %s", lines[0])
	}
}

func TestAuthReasonDetectsMissingAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if got := AuthReason(&types.Provider{Type: types.ProviderClaudeCode}); got == "" {
		t.Fatal("expected auth reason for claude-code")
	}
}

func TestAuthReasonAcceptsPiOpenAIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("PI_API_KEY", "")
	if got := AuthReason(&types.Provider{Type: types.ProviderPi}); got != "" {
		t.Fatalf("expected OpenAI key to satisfy pi auth, got %q", got)
	}
}

func TestAuthReasonAcceptsClaudeGatewayToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "gateway-token")
	if got := AuthReason(&types.Provider{Type: types.ProviderClaudeCode}); got != "" {
		t.Fatalf("expected Claude gateway token to satisfy auth, got %q", got)
	}
}

func TestAuthReasonAcceptsPersistedCLIAuthWithoutReadingSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"opaque":"credential"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := AuthReason(&types.Provider{Type: types.ProviderClaudeCode}); got != "" {
		t.Fatalf("expected persisted auth to satisfy check, got %q", got)
	}
}

func TestAuthReasonAcceptsClaudeNativeJSONStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	cliPath := filepath.Join(home, "claude")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := &types.Provider{Name: "claude-code", Type: types.ProviderClaudeCode, CLIPath: cliPath}
	if got := AuthReason(provider); got != "" {
		t.Fatalf("expected native Claude JSON status to satisfy auth, got %q", got)
	}
}

func TestAuthReasonAcceptsCursorNativeStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_API_KEY", "")
	cliPath := filepath.Join(home, "cursor")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nif [ \"$1\" = agent ] && [ \"$2\" = status ]; then echo 'Logged in as test@example.com'; exit 0; fi\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := AuthReason(&types.Provider{Type: types.ProviderCursor, CLIPath: cliPath}); got != "" {
		t.Fatalf("expected native Cursor status to satisfy auth, got %q", got)
	}
}

func TestAuthReasonAcceptsMimoNativeCredentialFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MIMO_API_KEY", "")
	path := filepath.Join(home, ".local", "share", "mimocode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"github-copilot":{"type":"oauth"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := AuthReason(&types.Provider{Type: types.ProviderMimo}); got != "" {
		t.Fatalf("expected native Mimo credentials to satisfy auth, got %q", got)
	}
}
