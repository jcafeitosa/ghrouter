package account

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ghrouter/internal/types"
)

type Status struct {
	Plan      string    `json:"plan,omitempty" yaml:"plan,omitempty"`
	Balance   *float64  `json:"balance,omitempty" yaml:"balance,omitempty"`
	Currency  string    `json:"currency,omitempty" yaml:"currency,omitempty"`
	ResetAt   time.Time `json:"reset_at,omitempty" yaml:"reset_at,omitempty"`
	Source    string    `json:"source,omitempty" yaml:"source,omitempty"`
	Healthy   bool      `json:"healthy"`
	Available bool      `json:"available"`
}

func Load(p *types.Provider) Status {
	if p == nil {
		return Status{Available: false, Healthy: false, Source: "missing-provider"}
	}
	if status, ok := loadJSONStatus(p); ok {
		return status
	}
	s := Status{Available: true, Healthy: true, Source: "env"}
	keyBase := providerKey(p.Name)
	s.Plan = firstNonEmpty(
		p.AuthConfig["plan"],
		lookupEnv(keyBase, "PLAN"),
		lookupEnv("GHR_PROVIDER", "PLAN"),
	)
	s.Currency = firstNonEmpty(
		p.AuthConfig["balance_currency"],
		lookupEnv(keyBase, "BALANCE_CURRENCY"),
		lookupEnv("GHR_PROVIDER", "BALANCE_CURRENCY"),
	)
	s.Balance = parseOptionalFloat(firstNonEmpty(
		p.AuthConfig["balance"],
		lookupEnv(keyBase, "BALANCE"),
		lookupEnv("GHR_PROVIDER", "BALANCE"),
	))
	if raw := firstNonEmpty(
		p.AuthConfig["reset_at"],
		lookupEnv(keyBase, "RESET_AT"),
		lookupEnv("GHR_PROVIDER", "RESET_AT"),
	); raw != "" {
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			s.ResetAt = ts
		}
	}
	if s.Plan == "" && s.Balance == nil && s.Currency == "" && s.ResetAt.IsZero() {
		s.Source = "unavailable"
		s.Available = false
		s.Healthy = false
	}
	return s
}

func loadJSONStatus(p *types.Provider) (Status, bool) {
	raw := firstNonEmpty(
		p.AuthConfig["account_json"],
		lookupEnv(providerKey(p.Name), "ACCOUNT_JSON"),
		lookupEnv("GHR_PROVIDER", "ACCOUNT_JSON"),
	)
	if raw == "" {
		return Status{}, false
	}
	var status Status
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return Status{Available: false, Healthy: false, Source: "invalid-json"}, true
	}
	if status.Source == "" {
		status.Source = "json"
	}
	if !status.Available {
		status.Available = true
	}
	if !status.Healthy {
		status.Healthy = true
	}
	return status, true
}

func Weight(status Status) float64 {
	if !status.Available {
		return 0.15
	}
	score := 1.0
	if status.Plan != "" {
		score += 0.05
	}
	if status.Balance != nil {
		switch {
		case *status.Balance <= 0:
			score -= 0.6
		case *status.Balance < 0.2:
			score -= 0.25
		case *status.Balance < 0.5:
			score += 0.05
		case *status.Balance < 0.8:
			score += 0.12
		default:
			score += 0.2
		}
	}
	if !status.ResetAt.IsZero() && time.Until(status.ResetAt) < 24*time.Hour {
		score -= 0.05
	}
	if score < 0.1 {
		return 0.1
	}
	return score
}

func providerKey(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	name = strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(name)
	return "GHR_PROVIDER_" + name
}

func lookupEnv(prefix, key string) string {
	if v := os.Getenv(prefix + "_" + key); v != "" {
		return v
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseOptionalFloat(raw string) *float64 {
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

func (s Status) String() string {
	balance := ""
	if s.Balance != nil {
		balance = fmt.Sprintf("%.2f", *s.Balance)
	}
	return fmt.Sprintf("%s %s %s %s", s.Plan, balance, s.Currency, s.Source)
}
