package config

import (
	"os"
	"path/filepath"

	"ghrouter/internal/types"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*types.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg types.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 9090
	}
	return &cfg, nil
}

func ResolveConfigPath(raw string) string {
	if raw == "" {
		if env := os.Getenv("GHR_CONFIG"); env != "" {
			return env
		}
		if wd, _ := os.Getwd(); wd != "" {
			return filepath.Join(wd, "config.yaml")
		}
	}
	return raw
}

func Save(path string, cfg *types.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
