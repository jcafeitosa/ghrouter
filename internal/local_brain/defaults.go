package local_brain

import (
	"os"
	"strings"
	"time"

	"ghrouter/internal/types"
)

const (
	DefaultModel          = "mlx-community/gemma-4-e2b-it-4bit"
	DefaultSource         = "hf://mlx-community/gemma-4-e2b-it-4bit"
	DefaultCompanionModel = "mlx-community/Qwen3-0.6B-4bit"
)

func EnsureMandatoryConfig(cfg *types.LocalBrainConfig) bool {
	if cfg == nil {
		return false
	}
	if !cfg.Enabled {
		return false
	}
	changed := false
	if strings.TrimSpace(cfg.Model) == "" && strings.TrimSpace(cfg.Source) == "" {
		model := strings.TrimSpace(os.Getenv("GHR_LOCAL_BRAIN_MODEL"))
		source := strings.TrimSpace(os.Getenv("GHR_LOCAL_BRAIN_SOURCE"))
		if model == "" {
			model = DefaultModel
		}
		if source == "" {
			source = DefaultSource
		}
		cfg.Model = model
		cfg.Source = source
		cfg.AutoProvision = true
		changed = true
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
		changed = true
	}
	if cfg.Port <= 0 {
		cfg.Port = 19090
		changed = true
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = time.Minute
		changed = true
	}
	return changed
}
