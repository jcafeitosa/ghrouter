package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadReadsHealthAndCooldownSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`listen_port: 9091
storage:
  retention_days: 14
cooldown:
  enabled: false
  default_duration: "2s"
  max_duration: "9s"
health:
  enabled: false
  interval: "3s"
  timeout: "400ms"
  test_prompt: "health-check"
logging:
  level: "debug"
  format: "text"
  output: "stderr"
server:
  host: "127.0.0.1"
  read_timeout: "1s"
  write_timeout: "2s"
  idle_timeout: "3s"
verification:
  enabled: true
  startup: true
  interval: "4s"
  timeout: "500ms"
  workers: 2
  batch_size: 12
  max_per_provider: 3
local_brain:
  enabled: true
  auto_provision: true
  backend: "mlx"
  model: "qwen3"
  source: "hf://Qwen/Qwen3-8B"
  host: "127.0.0.1"
  port: 19090
  startup_timeout: "7s"
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cooldown.IsEnabled() || cfg.Cooldown.DefaultDuration != 2*time.Second || cfg.Cooldown.MaxDuration != 9*time.Second {
		t.Fatalf("unexpected cooldown settings: %+v", cfg.Cooldown)
	}
	if cfg.Storage.RetentionDays != 14 {
		t.Fatalf("unexpected storage retention: %+v", cfg.Storage)
	}
	if cfg.Health.IsEnabled() || cfg.Health.Interval != 3*time.Second || cfg.Health.Timeout != 400*time.Millisecond || cfg.Health.TestPrompt != "health-check" {
		t.Fatalf("unexpected health settings: %+v", cfg.Health)
	}
	if cfg.Logging.Level != "debug" || cfg.Logging.Format != "text" || cfg.Logging.Output != "stderr" {
		t.Fatalf("unexpected logging settings: %+v", cfg.Logging)
	}
	if cfg.Server.Host != "127.0.0.1" || cfg.Server.ReadTimeout != time.Second || cfg.Server.WriteTimeout != 2*time.Second || cfg.Server.IdleTimeout != 3*time.Second {
		t.Fatalf("unexpected server settings: %+v", cfg.Server)
	}
	if !cfg.Verification.IsEnabled() || !cfg.Verification.Startup || cfg.Verification.Interval != 4*time.Second || cfg.Verification.Timeout != 500*time.Millisecond || cfg.Verification.Workers != 2 || cfg.Verification.BatchSize != 12 || cfg.Verification.MaxPerProvider != 3 {
		t.Fatalf("unexpected verification settings: %+v", cfg.Verification)
	}
	if !cfg.LocalBrain.Enabled || !cfg.LocalBrain.AutoProvision || cfg.LocalBrain.Backend != "mlx" || cfg.LocalBrain.Model != "qwen3" || cfg.LocalBrain.Source != "hf://Qwen/Qwen3-8B" || cfg.LocalBrain.Port != 19090 || cfg.LocalBrain.StartupTimeout != 7*time.Second {
		t.Fatalf("unexpected local brain settings: %+v", cfg.LocalBrain)
	}
}
