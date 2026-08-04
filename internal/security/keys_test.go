package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateClientKeysUsesClientPrefixesAndRestrictiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	keys, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("create keys: %v", err)
	}
	if !strings.HasPrefix(keys.GitHub, "ghr_gh_") || !strings.HasPrefix(keys.OpenAI, "sk-ghrouter-") || !strings.HasPrefix(keys.Anthropic, "sk-ant-ghrouter-") {
		t.Fatalf("unexpected client key prefixes: %+v", keys)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat keys: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 key file, got %o", info.Mode().Perm())
	}
	loaded, err := LoadOrCreate(path)
	if err != nil || loaded != keys {
		t.Fatalf("expected stable key reload, got %+v/%v", loaded, err)
	}
}

func TestLoadOrCreateClientKeysHardensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(`{"github":"gh","openai":"oa","anthropic":"an"}`), 0o644); err != nil {
		t.Fatalf("seed keys: %v", err)
	}

	if _, err := LoadOrCreate(path); err != nil {
		t.Fatalf("load existing keys: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat keys: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected existing key file to be hardened to 0600, got %o", info.Mode().Perm())
	}
}

func TestLoadOrCreateClientKeysRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	path := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(target, []byte(`{"github":"gh","openai":"oa","anthropic":"an"}`), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("expected symlink key file to be rejected")
	}
}

func TestLoadOrCreateClientKeysHardensExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("relax key directory: %v", err)
	}
	path := filepath.Join(dir, "keys.json")
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatalf("create keys: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat key directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("expected key directory to be hardened to 0700, got %o", info.Mode().Perm())
	}
}

func TestMaskKeyDoesNotExposeSecret(t *testing.T) {
	key := "sk-ant-ghrouter-abcdefghijklmnopqrstuvwxyz"
	masked := MaskKey(key)
	if masked == key || !strings.Contains(masked, "…") || !strings.HasSuffix(masked, "wxyz") {
		t.Fatalf("expected masked key, got %q", masked)
	}
}
