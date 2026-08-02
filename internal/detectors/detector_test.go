package detectors

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ghrouter/internal/types"
)

func TestDetectAllIncludesCursorAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable test shim not wired for windows in this repo")
	}
	tmpDir := t.TempDir()
	for _, name := range []string{"agent", "cursor"} {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("seed %s binary: %v", name, err)
		}
	}
	t.Setenv("PATH", tmpDir)
	t.Setenv("CURSOR_API_KEY", "test")

	providers, err := NewDetector().DetectAll()
	if err != nil {
		t.Fatalf("detect all: %v", err)
	}
	var found bool
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if provider.Type == types.ProviderCursor {
			found = true
			if provider.CLIPath != filepath.Join(tmpDir, "agent") && provider.CLIPath != filepath.Join(tmpDir, "cursor") {
				t.Fatalf("expected cursor cli path from temp dir, got %s", provider.CLIPath)
			}
			if len(provider.Models) == 0 {
				t.Fatalf("expected cursor models, got %+v", provider)
			}
		}
	}
	if !found {
		t.Fatal("expected cursor provider to be detected")
	}
}
