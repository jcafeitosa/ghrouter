package types

import (
	"strings"
	"time"
)

type ModelList struct {
	Name     string   `yaml:"name" json:"name"`
	Kind     string   `yaml:"kind,omitempty" json:"kind,omitempty"`
	Models   []string `yaml:"models" json:"models"`
	Strategy string   `yaml:"strategy,omitempty" json:"strategy,omitempty"`
}

type ModelInfo struct {
	Provider          string    `json:"provider,omitempty" yaml:"provider,omitempty"`
	Model             string    `json:"model,omitempty" yaml:"model,omitempty"`
	TokenCost         int       `json:"token_cost,omitempty" yaml:"token_cost,omitempty"`
	ContextWindow     int       `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	MaxOutput         int       `json:"max_output,omitempty" yaml:"max_output,omitempty"`
	Thinking          bool      `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Vision            bool      `json:"vision,omitempty" yaml:"vision,omitempty"`
	ToolUse           bool      `json:"tool_use,omitempty" yaml:"tool_use,omitempty"`
	Effort            []string  `json:"effort,omitempty" yaml:"effort,omitempty"`
	Source            string    `json:"source,omitempty" yaml:"source,omitempty"`
	DiscoveredAt      time.Time `json:"discovered_at,omitempty" yaml:"discovered_at,omitempty"`
	HealthStatus      string    `json:"health_status,omitempty" yaml:"health_status,omitempty"`
	CooldownUntil     time.Time `json:"cooldown_until,omitempty" yaml:"cooldown_until,omitempty"`
	VerifiedAt        time.Time `json:"verified_at,omitempty" yaml:"verified_at,omitempty"`
	VerificationError string    `json:"verification_error,omitempty" yaml:"verification_error,omitempty"`
}

type ModelProvenance string

const (
	ModelProvenanceUnknown    ModelProvenance = "unknown"
	ModelProvenanceConfigured ModelProvenance = "configured"
	ModelProvenanceObserved   ModelProvenance = "observed"
	ModelProvenanceVerified   ModelProvenance = "verified"
)

func (m ModelInfo) Provenance() ModelProvenance {
	if !m.VerifiedAt.IsZero() {
		return ModelProvenanceVerified
	}
	switch strings.ToLower(strings.TrimSpace(m.Source)) {
	case "configured":
		return ModelProvenanceConfigured
	case "native", "observed":
		return ModelProvenanceObserved
	default:
		return ModelProvenanceUnknown
	}
}

type DiscoveryStatus string

const (
	DiscoveryUnknown     DiscoveryStatus = "unknown"
	DiscoverySuccess     DiscoveryStatus = "success"
	DiscoveryEmpty       DiscoveryStatus = "empty"
	DiscoveryTimeout     DiscoveryStatus = "timeout"
	DiscoveryAuth        DiscoveryStatus = "auth"
	DiscoveryError       DiscoveryStatus = "error"
	DiscoveryUnsupported DiscoveryStatus = "unsupported"
)

type DiscoveryState struct {
	Status       DiscoveryStatus `yaml:"status" json:"status"`
	Error        string          `yaml:"error,omitempty" json:"error,omitempty"`
	DiscoveredAt time.Time       `yaml:"discovered_at,omitempty" json:"discovered_at,omitempty"`
}

type ModelPolicy struct {
	MaxCostMicros   int           `yaml:"max_cost_micros,omitempty" json:"max_cost_micros,omitempty"`
	MaxDiscoveryAge time.Duration `yaml:"max_discovery_age,omitempty" json:"max_discovery_age,omitempty"`
	Preferred       []string      `yaml:"preferred,omitempty" json:"preferred,omitempty"`
	Excluded        []string      `yaml:"excluded,omitempty" json:"excluded,omitempty"`
}

func CanonicalModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
