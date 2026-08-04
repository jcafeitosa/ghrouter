package types

import (
	"encoding/json"
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
	CostTier          string    `json:"cost_tier,omitempty" yaml:"cost_tier,omitempty"`
	ContextWindow     int       `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	MaxOutput         int       `json:"max_output,omitempty" yaml:"max_output,omitempty"`
	Thinking          bool      `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Vision            bool      `json:"vision,omitempty" yaml:"vision,omitempty"`
	ToolUse           bool      `json:"tool_use,omitempty" yaml:"tool_use,omitempty"`
	Effort            []string  `json:"effort,omitempty" yaml:"effort,omitempty"`
	Kind              string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Modalities        []string  `json:"modalities,omitempty" yaml:"modalities,omitempty"`
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
	case "native", "observed", "nvidia_api":
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

func (s DiscoveryState) MarshalJSON() ([]byte, error) {
	type discoveryAlias DiscoveryState
	var discoveredAt *time.Time
	if !s.DiscoveredAt.IsZero() {
		value := s.DiscoveredAt
		discoveredAt = &value
	}
	return json.Marshal(struct {
		discoveryAlias
		DiscoveredAt *time.Time `json:"discovered_at,omitempty"`
	}{discoveryAlias: discoveryAlias(s), DiscoveredAt: discoveredAt})
}

type HarnessCapabilities struct {
	Version               string    `yaml:"version,omitempty" json:"version,omitempty"`
	Commands              []string  `yaml:"commands,omitempty" json:"commands,omitempty"`
	Flags                 []string  `yaml:"flags,omitempty" json:"flags,omitempty"`
	Formats               []string  `yaml:"formats,omitempty" json:"formats,omitempty"`
	SlashCommands         []string  `yaml:"slash_commands,omitempty" json:"slash_commands,omitempty"`
	SlashCommandsSource   string    `yaml:"slash_commands_source,omitempty" json:"slash_commands_source,omitempty"`
	AdvertisesACP         bool      `yaml:"advertises_acp,omitempty" json:"advertises_acp,omitempty"`
	ACPHandshakeConfirmed bool      `yaml:"acp_handshake_confirmed,omitempty" json:"acp_handshake_confirmed,omitempty"`
	SupportsNativeJSON    bool      `yaml:"supports_native_json,omitempty" json:"supports_native_json,omitempty"`
	SupportsStreaming     bool      `yaml:"supports_streaming,omitempty" json:"supports_streaming,omitempty"`
	SupportsRPC           bool      `yaml:"supports_rpc,omitempty" json:"supports_rpc,omitempty"`
	SupportsServer        bool      `yaml:"supports_server,omitempty" json:"supports_server,omitempty"`
	SupportsModelSelect   bool      `yaml:"supports_model_select,omitempty" json:"supports_model_select,omitempty"`
	SupportsEffort        bool      `yaml:"supports_effort,omitempty" json:"supports_effort,omitempty"`
	SupportsThinking      bool      `yaml:"supports_thinking,omitempty" json:"supports_thinking,omitempty"`
	SupportsTools         bool      `yaml:"supports_tools,omitempty" json:"supports_tools,omitempty"`
	SupportsImages        bool      `yaml:"supports_images,omitempty" json:"supports_images,omitempty"`
	SupportsSessions      bool      `yaml:"supports_sessions,omitempty" json:"supports_sessions,omitempty"`
	SupportsMCP           bool      `yaml:"supports_mcp,omitempty" json:"supports_mcp,omitempty"`
	SupportsHeadless      bool      `yaml:"supports_headless,omitempty" json:"supports_headless,omitempty"`
	ObservedAt            time.Time `yaml:"observed_at,omitempty" json:"observed_at,omitempty"`
}

func (c HarnessCapabilities) MarshalJSON() ([]byte, error) {
	type harnessAlias HarnessCapabilities
	var observedAt *time.Time
	if !c.ObservedAt.IsZero() {
		value := c.ObservedAt
		observedAt = &value
	}
	return json.Marshal(struct {
		harnessAlias
		ObservedAt *time.Time `json:"observed_at,omitempty"`
	}{harnessAlias: harnessAlias(c), ObservedAt: observedAt})
}

func (c HarnessCapabilities) Observed() bool {
	return !c.ObservedAt.IsZero()
}

type ModelPolicy struct {
	MaxCostMicros   int           `yaml:"max_cost_micros,omitempty" json:"max_cost_micros,omitempty"`
	MaxDiscoveryAge time.Duration `yaml:"max_discovery_age,omitempty" json:"max_discovery_age,omitempty"`
	Allowed         []string      `yaml:"allowed,omitempty" json:"allowed,omitempty"`
	Preferred       []string      `yaml:"preferred,omitempty" json:"preferred,omitempty"`
	Excluded        []string      `yaml:"excluded,omitempty" json:"excluded,omitempty"`
}

func CanonicalModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
