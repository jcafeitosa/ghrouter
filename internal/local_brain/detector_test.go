package local_brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureModelAvailableMissingModelReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GHR_LOCAL_MODEL_ROOT", t.TempDir())

	manager, err := NewModelManager()
	if err != nil {
		t.Fatalf("create model manager: %v", err)
	}

	path, err := manager.EnsureModelAvailable(BackendMLX, "org/missing")
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if path != "" {
		t.Fatalf("expected empty path for missing model, got %q", path)
	}
	if !strings.Contains(err.Error(), "not found in cache") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestModelManagerPrepareCreatesCacheStructure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GHR_LOCAL_MODEL_ROOT", t.TempDir())

	manager, err := NewModelManager()
	if err != nil {
		t.Fatalf("create model manager: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(t.TempDir(), "unused")); err != nil {
		t.Fatalf("cleanup unrelated temp dir: %v", err)
	}
	if err := manager.Prepare(); err != nil {
		t.Fatalf("prepare cache structure: %v", err)
	}
	for _, dir := range []string{
		manager.cacheDir,
		filepath.Join(manager.cacheDir, "mlx"),
		filepath.Join(manager.cacheDir, "llama.cpp"),
	} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected cache dir %s to exist: %v", dir, err)
		}
	}
}

func TestLlamaCppModelDirectoryIsNotAcceptedAsGGUF(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GHR_LOCAL_MODEL_ROOT", t.TempDir())
	manager, err := NewModelManager()
	if err != nil {
		t.Fatalf("create model manager: %v", err)
	}
	legacyPath := filepath.Join(manager.CacheDir(), "broken.gguf")
	if err := os.MkdirAll(legacyPath, 0o755); err != nil {
		t.Fatalf("create invalid model directory: %v", err)
	}
	if manager.HasModel(BackendLLAMACPP, "broken") {
		t.Fatal("expected a directory named .gguf to be rejected")
	}
}

func TestLlamaCppProvisionUsesModelDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GHR_LOCAL_MODEL_ROOT", t.TempDir())
	command := modelDownloadCommand(BackendLLAMACPP, "hf://Qwen/Qwen3-8B-GGUF")
	if len(command) == 0 {
		t.Fatal("expected a Hugging Face download command")
	}
	joined := strings.Join(command, " ")
	if strings.Contains(joined, "Qwen3-8B-GGUF.gguf") || !strings.Contains(joined, filepath.Join("llama.cpp", "Qwen-Qwen3-8B-GGUF")) {
		t.Fatalf("expected a llama.cpp model directory, got %v", command)
	}
}

func TestModelManagerUsesConfiguredLocalModelRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GHR_LOCAL_MODEL_ROOT", root)
	manager, err := NewModelManager()
	if err != nil {
		t.Fatalf("create model manager: %v", err)
	}
	if manager.CacheDir() != root {
		t.Fatalf("expected configured local model root %q, got %q", root, manager.CacheDir())
	}
}

func TestEnsureModelAvailableResolvesOwnerModelLayout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GHR_LOCAL_MODEL_ROOT", root)
	model := filepath.Join(root, "mlx-community", "gemma-4-e2b-it-4bit")
	if err := os.MkdirAll(model, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(model, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewModelManager()
	if err != nil {
		t.Fatalf("create model manager: %v", err)
	}
	path, err := manager.EnsureModelAvailable(BackendMLX, "mlx-community/gemma-4-e2b-it-4bit")
	if err != nil {
		t.Fatalf("resolve local model: %v", err)
	}
	if path != model {
		t.Fatalf("expected owner/model path %q, got %q", model, path)
	}
}
