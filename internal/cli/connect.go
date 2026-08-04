package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ghrouter/internal/config"
	"ghrouter/internal/security"
	"ghrouter/internal/server"
)

func (r *Runner) connect(args []string) int {
	client := "copilot"
	install := false
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		client = strings.ToLower(strings.TrimSpace(args[0]))
	}
	for _, arg := range args[1:] {
		if arg == "--install" {
			install = true
		}
	}
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	port := cfg.ListenPort
	if port == 0 {
		port = 9090
	}
	if activePort, ok := server.ActiveSessionPort(port, config.ResolveConfigPath(r.Config)); ok {
		port = activePort
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	keys, err := security.LoadOrCreate(cfg.ACL.KeysFile)
	if err != nil {
		fmt.Fprintf(r.Stderr, "client key load failed: %v\n", err)
		return 1
	}
	if install {
		switch client {
		case "copilot", "gh-copilot", "github-copilot":
			if err := installCopilotLauncher(cfgPath(r.Config), base); err != nil {
				fmt.Fprintf(r.Stderr, "copilot launcher installation failed: %v\n", err)
				return 1
			}
			fmt.Fprintln(r.Stdout, "installed automatic Copilot launcher in ~/.local/bin/copilot")
			return 0
		case "pi":
			dir, err := installPiProfile(base)
			if err != nil {
				fmt.Fprintf(r.Stderr, "pi profile installation failed: %v\n", err)
				return 1
			}
			fmt.Fprintf(r.Stdout, "installed Pi Ghrouter profile in %s\n", dir)
			return 0
		case "codex":
			dir, err := installCodexProfile(base)
			if err != nil {
				fmt.Fprintf(r.Stderr, "codex profile installation failed: %v\n", err)
				return 1
			}
			fmt.Fprintf(r.Stdout, "installed Codex Ghrouter profile in %s\n", dir)
			return 0
		default:
			fmt.Fprintf(r.Stderr, "automatic installation is supported for copilot, codex, and pi\n")
			return 2
		}
	}

	fmt.Fprintf(r.Stdout, "# Ghrouter client profile: %s\n", client)
	fmt.Fprintln(r.Stdout, "# Start `ghrouter serve` first. The router token is never copied by this command.")
	switch client {
	case "copilot", "gh-copilot", "github-copilot":
		fmt.Fprintf(r.Stdout, "export COPILOT_PROVIDER_BASE_URL=%s/v1\n", base)
		fmt.Fprintln(r.Stdout, "export COPILOT_PROVIDER_TYPE=openai")
		fmt.Fprintln(r.Stdout, "export COPILOT_PROVIDER_WIRE_API=responses")
		fmt.Fprintln(r.Stdout, "export COPILOT_PROVIDER_MODEL_ID=gpt-5.4")
		fmt.Fprintln(r.Stdout, "export COPILOT_PROVIDER_WIRE_MODEL=ghrouter/tool-use")
		fmt.Fprintf(r.Stdout, "export COPILOT_PROVIDER_API_KEY=%s\n", keys.GitHub)
		fmt.Fprintln(r.Stdout, "export COPILOT_MODEL=ghrouter/tool-use")
	case "codex":
		fmt.Fprintf(r.Stdout, "export OPENAI_API_KEY=%s\n", keys.OpenAI)
		fmt.Fprintf(r.Stdout, "export CODEX_HOME=%s\n", codexProfileDir())
		fmt.Fprintln(r.Stdout, "export CODEX_MODEL=auto")
		fmt.Fprintln(r.Stdout, "# Run `ghrouter connect codex --install` once, then use `codex exec --model auto`.")
		fmt.Fprintln(r.Stdout, "# Bootstrap once if needed: printenv OPENAI_API_KEY | codex login --with-api-key")
	case "claude", "claude-code":
		fmt.Fprintf(r.Stdout, "export ANTHROPIC_BASE_URL=%s\n", base)
		fmt.Fprintf(r.Stdout, "export ANTHROPIC_AUTH_TOKEN=%s\n", keys.Anthropic)
		fmt.Fprintln(r.Stdout, "export ANTHROPIC_MODEL=ghrouter/auto")
	case "opencode":
		fmt.Fprintf(r.Stdout, "export OPENAI_API_KEY=%s\n", keys.OpenAI)
		fmt.Fprintf(r.Stdout, "export OPENCODE_CONFIG_CONTENT=%s\n", shellQuote(openCodeRouterConfig(base)))
		fmt.Fprintln(r.Stdout, "# Run natively: opencode run --model ghrouter/auto --format json --pure")
	case "mimo":
		fmt.Fprintf(r.Stdout, "export OPENAI_API_KEY=%s\n", keys.OpenAI)
		fmt.Fprintf(r.Stdout, "export MIMOCODE_CONFIG_CONTENT=%s\n", shellQuote(openCodeRouterConfig(base)))
		fmt.Fprintln(r.Stdout, "# Run natively: mimo run --model ghrouter/auto --format json --pure")
	case "pi":
		fmt.Fprintf(r.Stdout, "export OPENAI_API_KEY=%s\n", keys.OpenAI)
		fmt.Fprintf(r.Stdout, "export PI_CODING_AGENT_DIR=%s\n", piProfileDir())
		fmt.Fprintf(r.Stdout, "export OPENAI_BASE_URL=%s/v1\n", base)
		fmt.Fprintln(r.Stdout, "# Pi uses native RPC; run `ghrouter connect pi --install` to provision its Ghrouter models.json profile.")
	case "cursor", "cursor-agent":
		fmt.Fprintf(r.Stdout, "export CURSOR_API_ENDPOINT=%s/v1\n", base)
		fmt.Fprintf(r.Stdout, "export CURSOR_API_KEY=%s\n", keys.OpenAI)
	case "nvidia", "nvidia-nim":
		fmt.Fprintln(r.Stdout, "# NVIDIA NIM is a direct upstream connector; configure a real model ID explicitly.")
		fmt.Fprintln(r.Stdout, "export NVIDIA_API_KEY=<set-your-nvidia-api-key>")
		fmt.Fprintln(r.Stdout, "export GHR_NVIDIA_MODELS=<provider/model[,provider/model...]>")
		fmt.Fprintln(r.Stdout, "# YAML: auth_method: env; auth_config: {api_key_env: NVIDIA_API_KEY}")
		fmt.Fprintln(r.Stdout, "# Add a provider with type: nvidia, then run `ghrouter sync` to refresh the local catalog.")
	default:
		fmt.Fprintf(r.Stderr, "unsupported client %q (use copilot, codex, claude, opencode, mimo, pi, cursor, or nvidia)\n", client)
		return 2
	}
	return 0
}

func openCodeRouterConfig(base string) string {
	config := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"ghrouter": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "Ghrouter",
				"options": map[string]any{
					"baseURL": base + "/v1",
					"apiKey":  "{env:OPENAI_API_KEY}",
				},
				"models": map[string]any{
					"auto":     map[string]string{"name": "Ghrouter Auto"},
					"codex":    map[string]string{"name": "Ghrouter Codex"},
					"claude":   map[string]string{"name": "Ghrouter Claude"},
					"opencode": map[string]string{"name": "Ghrouter OpenCode"},
					"mimo":     map[string]string{"name": "Ghrouter Mimo"},
					"pi":       map[string]string{"name": "Ghrouter Pi"},
				},
			},
		},
		"model": "ghrouter/auto",
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func piRouterConfig(base string) string {
	config := map[string]any{
		"providers": map[string]any{
			"ghrouter": map[string]any{
				"baseUrl": base + "/v1",
				"api":     "openai-completions",
				"apiKey":  "OPENAI_API_KEY",
				"models": []map[string]any{
					{"id": "auto", "name": "Ghrouter Auto", "reasoning": true, "input": []string{"text", "image"}, "contextWindow": 128000, "maxTokens": 128000},
					{"id": "codex", "name": "Ghrouter Codex", "reasoning": true, "input": []string{"text", "image"}, "contextWindow": 128000, "maxTokens": 128000},
					{"id": "claude", "name": "Ghrouter Claude", "reasoning": true, "input": []string{"text", "image"}, "contextWindow": 128000, "maxTokens": 128000},
				},
			},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(data) + "\n"
}

func installPiProfile(base string) (string, error) {
	dir := piProfileDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create profile directory: %w", err)
	}
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(piRouterConfig(base)), 0o600); err != nil {
		return "", fmt.Errorf("write models.json: %w", err)
	}
	return dir, nil
}

func piProfileDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "ghrouter", "pi")
}

func codexProfileDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "ghrouter", "codex")
}

func installCodexProfile(base string) (string, error) {
	dir := codexProfileDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create profile directory: %w", err)
	}
	config := fmt.Sprintf(`model = "auto"
model_provider = "ghrouter"

[model_providers.ghrouter]
name = "Ghrouter"
base_url = %s
wire_api = "responses"
env_key = "OPENAI_API_KEY"
`, strconv.Quote(base+"/v1"))
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		return "", fmt.Errorf("write config.toml: %w", err)
	}
	return dir, nil
}

func cfgPath(path string) string {
	if path == "" {
		path = "config.yaml"
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		return abs
	}
	return path
}

func installCopilotLauncher(absoluteConfig, baseURL string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".local", "bin")
	dest := filepath.Join(dir, "copilot")
	if realPath := realCopilotPath(dest); realPath == "" {
		return fmt.Errorf("real copilot executable not found")
	} else if realPath == dest {
		return fmt.Errorf("refusing to replace existing launcher")
	} else {
		return writeCopilotLauncher(dest, realPath, absoluteConfig, baseURL)
	}
}

func realCopilotPath(launcher string) string {
	if path := strings.TrimSpace(os.Getenv("GHR_COPILOT_BIN")); path != "" && filepath.Clean(path) != filepath.Clean(launcher) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	candidates := []string{"/opt/homebrew/bin/copilot", "/usr/local/bin/copilot"}
	for _, candidate := range candidates {
		if candidate != launcher {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	if path, err := exec.LookPath("copilot"); err == nil && filepath.Clean(path) != filepath.Clean(launcher) {
		return path
	}
	return ""
}

func writeCopilotLauncher(dest, realPath, absoluteConfig, baseURL string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create launcher directory: %w", err)
	}
	if data, err := os.ReadFile(dest); err == nil && !strings.Contains(string(data), "GHROUTER_AUTOMATIC_COPILOT") {
		return fmt.Errorf("%s already exists and is not managed by Ghrouter", dest)
	}
	router, err := routerInvocation()
	if err != nil {
		return err
	}
	routerPath, err := routerExecutable()
	if err != nil {
		return err
	}
	routerSHA256, err := routerBinarySHA256(routerPath)
	if err != nil {
		return fmt.Errorf("read router build identity: %w", err)
	}
	logPath := filepath.Join(filepath.Dir(absoluteConfig), ".ghrouter", "ghrouter.log")
	content := fmt.Sprintf(`#!/bin/sh
# GHROUTER_AUTOMATIC_COPILOT
set -eu
GHROUTER_CONFIG=%s
GHROUTER_BASE=%s
GHROUTER_LOG=%s
GHROUTER_BINARY_SHA256=%s
ghrouter_cmd() {
  env GHR_CONFIG="$GHROUTER_CONFIG" %s "$@"
}
if ! curl -fsS --max-time 1 "$GHROUTER_BASE/health" >/dev/null 2>&1; then
  mkdir -p "$(dirname "$GHROUTER_LOG")"
	  nohup env GHR_DETACH=1 GHR_LOG_LEVEL=warn GHR_LOG_FORMAT=text GHR_CONFIG="$GHROUTER_CONFIG" %s serve >>"$GHROUTER_LOG" 2>&1 </dev/null &
fi
ready=0
for _ in $(seq 1 300); do
  if curl -fsS --max-time 1 "$GHROUTER_BASE/readyz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
if [ "$ready" -ne 1 ]; then
  echo "ghrouter: router did not become ready; see $GHROUTER_LOG" >&2
  exit 1
fi
router_health=$(curl -fsS --max-time 1 "$GHROUTER_BASE/health" 2>/dev/null || true)
if ! printf '%%s' "$router_health" | grep -F '"binary_sha256":"'$GHROUTER_BINARY_SHA256'"' >/dev/null 2>&1; then
  echo "ghrouter: running router build does not match launcher; reinstall the launcher" >&2
  exit 1
fi
if [ -z "${COPILOT_PROVIDER_BASE_URL:-}" ]; then
  eval "$(ghrouter_cmd connect copilot 2>/dev/null)"
fi
exec %s "$@"
`, shellQuote(absoluteConfig), shellQuote(baseURL), shellQuote(logPath), shellQuote(routerSHA256), router, router, shellQuote(realPath))
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".copilot-ghrouter-*")
	if err != nil {
		return fmt.Errorf("create launcher temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect launcher: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write launcher: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close launcher: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("publish launcher: %w", err)
	}
	return nil
}

func routerInvocation() (string, error) {
	path, err := routerExecutable()
	if err != nil {
		return "", err
	}
	return shellQuote(path), nil
}

func routerExecutable() (string, error) {
	if value := strings.TrimSpace(os.Getenv("GHR_ROUTER_BIN")); value != "" {
		return value, nil
	}
	current := ""
	if path, err := os.Executable(); err == nil && !strings.Contains(path, string(filepath.Separator)+"go-build") {
		current = path
	}
	return routerExecutableFor(current, exec.LookPath)
}

func routerExecutableFor(current string, lookup func(string) (string, error)) (string, error) {
	if current != "" {
		return current, nil
	}
	if path, err := lookup("ghrouter"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("ghrouter executable not found; install it or set GHR_ROUTER_BIN")
}

func routerInvocationFor(current string, lookup func(string) (string, error)) (string, error) {
	path, err := routerExecutableFor(current, lookup)
	if err != nil {
		return "", err
	}
	return shellQuote(path), nil
}

func routerBinarySHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
