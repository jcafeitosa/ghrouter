package account

import (
	"testing"

	"ghrouter/internal/types"
)

func TestLoadMarksUnsupportedProviderStateCanonical(t *testing.T) {
	status := Load(&types.Provider{Name: "custom", Type: types.ProviderCustom})
	if status.Source != "unsupported" {
		t.Fatalf("expected unsupported source, got %+v", status)
	}
	if status.Available || status.Healthy {
		t.Fatalf("expected unsupported provider to be unavailable, got %+v", status)
	}
}

func TestLoadMarksMissingAuthStateCanonical(t *testing.T) {
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

func TestLoadMarksUnknownStateWhenAuthIsPresentCanonical(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")

	status := Load(&types.Provider{Name: "claude-code", Type: types.ProviderClaudeCode})
	if status.Source != "unknown" {
		t.Fatalf("expected unknown source, got %+v", status)
	}
	if !status.Available || !status.Healthy {
		t.Fatalf("expected authenticated provider to remain available, got %+v", status)
	}
}
