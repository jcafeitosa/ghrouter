package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type resetTarget struct {
	Provider string `json:"provider"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Removed  bool   `json:"removed"`
}

func discoverResetTargets() []resetTarget {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	return dedupeTargets([]resetTarget{
		{Provider: "claude-code", Kind: "config", Path: filepath.Join(home, ".claude")},
		{Provider: "claude-code", Kind: "config", Path: filepath.Join(home, ".claude.json")},
		{Provider: "claude-code", Kind: "config", Path: xdgConfig(home, "claude")},
		{Provider: "codex", Kind: "config", Path: filepath.Join(home, ".codex")},
		{Provider: "codex", Kind: "config", Path: filepath.Join(home, ".config", "codex")},
		{Provider: "codex", Kind: "config", Path: xdgConfig(home, "codex")},
		{Provider: "opencode", Kind: "config", Path: filepath.Join(home, ".opencode")},
		{Provider: "opencode", Kind: "config", Path: xdgConfig(home, "opencode")},
		{Provider: "cursor", Kind: "config", Path: filepath.Join(home, ".cursor")},
		{Provider: "cursor", Kind: "config", Path: filepath.Join(home, ".cursor.json")},
		{Provider: "cursor", Kind: "config", Path: xdgConfig(home, "cursor")},
		{Provider: "cursor", Kind: "config", Path: xdgConfig(home, "cursor-agent")},
		{Provider: "mimo", Kind: "config", Path: filepath.Join(home, ".mimo")},
		{Provider: "mimo", Kind: "config", Path: xdgConfig(home, "mimo")},
		{Provider: "pi", Kind: "config", Path: filepath.Join(home, ".pi")},
		{Provider: "pi", Kind: "config", Path: xdgConfig(home, "pi")},
		{Provider: "claude-code", Kind: "app-support", Path: appSupport(home, "Claude")},
		{Provider: "codex", Kind: "app-support", Path: appSupport(home, "Codex")},
		{Provider: "opencode", Kind: "app-support", Path: appSupport(home, "OpenCode")},
		{Provider: "cursor", Kind: "app-support", Path: appSupport(home, "Cursor")},
		{Provider: "mimo", Kind: "app-support", Path: appSupport(home, "Mimo")},
		{Provider: "pi", Kind: "app-support", Path: appSupport(home, "Pi")},
	})
}

func xdgConfig(home, name string) string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return filepath.Join(value, name)
	}
	return filepath.Join(home, ".config", name)
}

func appSupport(home, name string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", name)
	case "windows":
		if value := os.Getenv("APPDATA"); value != "" {
			return filepath.Join(value, name)
		}
	}
	return ""
}

func dedupeTargets(targets []resetTarget) []resetTarget {
	seen := make(map[string]struct{}, len(targets))
	out := make([]resetTarget, 0, len(targets))
	for _, target := range targets {
		if target.Path == "" {
			continue
		}
		if _, ok := seen[target.Path]; ok {
			continue
		}
		seen[target.Path] = struct{}{}
		out = append(out, target)
	}
	return out
}

func removeResetTarget(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func summarizeResetTargets(targets []resetTarget) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, fmt.Sprintf("%s\t%s\t%s", target.Provider, target.Kind, target.Path))
	}
	return strings.Join(parts, "\n")
}
