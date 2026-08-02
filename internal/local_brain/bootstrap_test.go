package local_brain

import (
	"strings"
	"testing"

	"ghrouter/internal/types"
)

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
	backend := preferredBackendForHost()
	bootstrapper := &Bootstrapper{
		Detector: fakeBackendAvailability{available: map[BackendType]bool{
			backend: true,
		}},
		ModelManager: fakeModelPresence{present: map[string]bool{
			string(backend) + "/claude-sonnet-5": true,
		}},
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
	if report.Backend != backend {
		t.Fatalf("expected backend %q, got %q", backend, report.Backend)
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
			Name:    "claude-code",
			Type:    types.ProviderClaudeCode,
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
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if got := AuthReason(&types.Provider{Type: types.ProviderClaudeCode}); got == "" {
		t.Fatal("expected auth reason for claude-code")
	}
}
