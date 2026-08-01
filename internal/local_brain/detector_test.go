package local_brain

import "testing"

func TestEnsureModelAvailableMissingModelReturnsEmptyPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	manager, err := NewModelManager()
	if err != nil {
		t.Fatalf("create model manager: %v", err)
	}

	path, err := manager.EnsureModelAvailable(BackendMLX, "org/missing")
	if err != nil {
		t.Fatalf("ensure missing model: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path for missing model, got %q", path)
	}
}
