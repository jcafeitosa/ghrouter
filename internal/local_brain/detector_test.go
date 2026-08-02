package local_brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureModelAvailableMissingModelReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
