package local_brain

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BackendType represents the local brain backend type
type BackendType string

const (
	BackendMLX      BackendType = "mlx"
	BackendLLAMACPP BackendType = "llama.cpp"
	BackendNone     BackendType = "none"
)

// Detector checks for MLX or llama.cpp availability
type Detector struct{}

func (d *Detector) Detect() (BackendType, error) {
	if d.isMLXAvailable() {
		return BackendMLX, nil
	}
	if d.isLlamaCppAvailable() {
		return BackendLLAMACPP, nil
	}
	return BackendNone, nil
}

func (d *Detector) IsBackendAvailable(backend BackendType) bool {
	switch backend {
	case BackendMLX:
		return d.isMLXAvailable()
	case BackendLLAMACPP:
		return d.isLlamaCppAvailable()
	default:
		return false
	}
}

func (d *Detector) isMLXAvailable() bool {
	cmd := exec.Command("python3", "-c", "import mlx; print('ok')")
	if out, err := cmd.CombinedOutput(); err == nil && strings.Contains(string(out), "ok") {
		return true
	}
	cmd = exec.Command("python", "-c", "import mlx; print('ok')")
	if out, err := cmd.CombinedOutput(); err == nil && strings.Contains(string(out), "ok") {
		return true
	}
	return false
}

func (d *Detector) isLlamaCppAvailable() bool {
	if _, err := exec.LookPath("llama-server"); err == nil {
		return true
	}
	paths := []string{
		"/usr/local/bin/llama-server",
		"/opt/homebrew/bin/llama-server",
		"/usr/bin/llama-server",
		filepath.Join(os.Getenv("HOME"), ".local", "bin", "llama-server"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

type ModelManager struct {
	cacheDir string
}

func NewModelManager() (*ModelManager, error) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	cacheDir := filepath.Join(home, ".cache", "ghrouter", "models")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	return &ModelManager{cacheDir: cacheDir}, nil
}

func (m *ModelManager) Prepare() error {
	if m == nil {
		return fmt.Errorf("model manager not configured")
	}
	for _, dir := range []string{
		m.cacheDir,
		filepath.Join(m.cacheDir, "mlx"),
		filepath.Join(m.cacheDir, "llama.cpp"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (m *ModelManager) CacheDir() string {
	if m == nil {
		return ""
	}
	return m.cacheDir
}

func (m *ModelManager) EnsureModelAvailable(backend BackendType, modelID string) (string, error) {
	switch backend {
	case BackendMLX:
		return m.ensureMLXModel(modelID)
	case BackendLLAMACPP:
		return m.ensureLlamaCppModel(modelID)
	default:
		return "", fmt.Errorf("unsupported backend: %s", backend)
	}
}

func (m *ModelManager) ensureMLXModel(modelID string) (string, error) {
	modelDir := filepath.Join(m.cacheDir, "mlx", modelID)
	if _, err := os.Stat(modelDir); err == nil {
		return modelDir, nil
	}
	return "", fmt.Errorf("mlx model %q not found in cache", modelID)
}

func (m *ModelManager) ensureLlamaCppModel(modelID string) (string, error) {
	modelPath := filepath.Join(m.cacheDir, modelID+".gguf")
	if _, err := os.Stat(modelPath); err == nil {
		return modelPath, nil
	}
	return "", fmt.Errorf("llama.cpp model %q not found in cache", modelID)
}

func (m *ModelManager) HasModel(backend BackendType, modelID string) bool {
	switch backend {
	case BackendMLX:
		_, err := os.Stat(filepath.Join(m.cacheDir, "mlx", modelID))
		return err == nil
	case BackendLLAMACPP:
		_, err := os.Stat(filepath.Join(m.cacheDir, modelID+".gguf"))
		return err == nil
	default:
		return false
	}
}
