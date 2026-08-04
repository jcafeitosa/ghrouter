package local_brain

import (
	"testing"

	"ghrouter/internal/types"
)

func TestEnsureMandatoryConfigAddsLocalBrainDefaults(t *testing.T) {
	cfg := types.LocalBrainConfig{Enabled: true}
	if !EnsureMandatoryConfig(&cfg) {
		t.Fatal("expected defaults to change the configuration")
	}
	if !cfg.Enabled || !cfg.AutoProvision {
		t.Fatalf("expected enabled auto-provisioned Brain, got %+v", cfg)
	}
	if cfg.Model != DefaultModel || cfg.Source != DefaultSource {
		t.Fatalf("unexpected default model/source: %q %q", cfg.Model, cfg.Source)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 19090 {
		t.Fatalf("unexpected runtime defaults: %+v", cfg)
	}
}

func TestEnsureMandatoryConfigPreservesExplicitModel(t *testing.T) {
	cfg := types.LocalBrainConfig{Enabled: true, Model: "custom/model", Source: "hf://custom/model", AutoProvision: false}
	EnsureMandatoryConfig(&cfg)
	if !cfg.Enabled || cfg.Model != "custom/model" || cfg.Source != "hf://custom/model" || cfg.AutoProvision {
		t.Fatalf("explicit Brain configuration was changed: %+v", cfg)
	}
}
