package local_brain

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BackendType represents the local brain backend type
type BackendType string

const (
	BackendMLX         BackendType = "mlx"
	BackendLLAMACPP    BackendType = "llama.cpp"
	BackendExternalCLI BackendType = "external-cli"
	BackendNone        BackendType = "none"
)

// Detector checks for MLX or llama.cpp availability
type Detector struct{}

type HostCapabilities struct {
	OS                string      `json:"os"`
	Architecture      string      `json:"architecture"`
	PreferredBackend  BackendType `json:"preferred_backend"`
	DetectedBackend   BackendType `json:"detected_backend"`
	MLXAvailable      bool        `json:"mlx_available"`
	LlamaCppAvailable bool        `json:"llama_cpp_available"`
}

func (d *Detector) HostCapabilities() HostCapabilities {
	host := HostCapabilities{OS: runtime.GOOS, Architecture: runtime.GOARCH, PreferredBackend: preferredBackendForHost()}
	host.MLXAvailable = d.isMLXAvailable()
	host.LlamaCppAvailable = d.isLlamaCppAvailable()
	if host.MLXAvailable {
		host.DetectedBackend = BackendMLX
	} else if host.LlamaCppAvailable {
		host.DetectedBackend = BackendLLAMACPP
	} else {
		host.DetectedBackend = BackendNone
	}
	return host
}

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
	cacheDir := strings.TrimSpace(os.Getenv("GHR_LOCAL_MODEL_ROOT"))
	if cacheDir == "" {
		cacheDir = ".localmodel"
	}
	if !filepath.IsAbs(cacheDir) {
		workingDir, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve local model root: %w", err)
		}
		cacheDir = filepath.Join(workingDir, cacheDir)
	}
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
	if m == nil {
		return "", fmt.Errorf("model manager not configured")
	}
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
	if filepath.IsAbs(strings.TrimSpace(modelID)) {
		if info, err := os.Stat(modelID); err == nil && info.IsDir() {
			return modelID, nil
		}
	}
	if relative := localModelRelativePath(modelID); relative != "" {
		modelDir := filepath.Join(m.cacheDir, relative)
		if info, err := os.Stat(modelDir); err == nil && info.IsDir() {
			return modelDir, nil
		}
	}
	slug := sanitizeModelSlug(modelID)
	if slug == "" {
		return "", fmt.Errorf("mlx model %q has an invalid cache name", modelID)
	}
	modelDir := filepath.Join(m.cacheDir, "mlx", slug)
	if info, err := os.Stat(modelDir); err == nil && info.IsDir() {
		return modelDir, nil
	}
	return "", fmt.Errorf("mlx model %q not found in cache", modelID)
}

func localModelRelativePath(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "hf://")
	model = strings.TrimPrefix(model, "huggingface://")
	model = strings.TrimPrefix(model, "hf/")
	if model == "" || filepath.IsAbs(model) {
		return ""
	}
	clean := filepath.Clean(filepath.FromSlash(model))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ""
	}
	return clean
}

func (m *ModelManager) ensureLlamaCppModel(modelID string) (string, error) {
	slug := sanitizeModelSlug(modelID)
	if slug == "" {
		return "", fmt.Errorf("llama.cpp model %q has an invalid cache name", modelID)
	}
	modelDir := filepath.Join(m.cacheDir, "llama.cpp", slug)
	if info, err := os.Stat(modelDir); err == nil {
		if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(modelDir), ".gguf") {
			return modelDir, nil
		}
		if info.IsDir() {
			entries, readErr := os.ReadDir(modelDir)
			if readErr == nil {
				for _, entry := range entries {
					if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".gguf") {
						continue
					}
					return filepath.Join(modelDir, entry.Name()), nil
				}
			}
		}
	}
	legacyPath := filepath.Join(m.cacheDir, slug+".gguf")
	if info, err := os.Stat(legacyPath); err == nil && info.Mode().IsRegular() {
		return legacyPath, nil
	}
	return "", fmt.Errorf("llama.cpp model %q not found in cache", modelID)
}

func (m *ModelManager) HasModel(backend BackendType, modelID string) bool {
	_, err := m.EnsureModelAvailable(backend, modelID)
	return err == nil
}
