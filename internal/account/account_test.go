package account

import (
	"encoding/json"
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
	if Weight(status) >= 1.0 {
		t.Fatalf("expected low weight for unavailable account, got %+v", status)
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
