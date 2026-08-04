package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ghrouter/internal/config"
	"ghrouter/internal/local_brain"
	"ghrouter/internal/server"
	"ghrouter/internal/types"
)

func TestRunUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	r := &Runner{Stdout: &bytes.Buffer{}, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"unknown"})
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("expected error output")
	}
}

func TestRunHelpCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	if code := r.Run(context.Background(), []string{"--help"}); code != 0 {
		t.Fatalf("expected help success, got %d", code)
	}
	for _, expected := range []string{"Usage: ghrouter", "ghrouter probe <model>", "ghrouter serve", "--config PATH"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("help missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRunTestCommandPerformsRealLocalBrainInference(t *testing.T) {
	model := "mlx-community/gemma-4-e2b-it-4bit"
	completionCalls := 0
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mlx-community/gemma-4-e2b-it-4bit","owned_by":"mlx"}]}`))
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/chat/completions":
			completionCalls++
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"GHROUTER_TEST_OK"},"finish_reason":"stop"}],"usage":{"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(configPath, &types.Config{ListenPort: 9090, LocalBrain: types.LocalBrainConfig{
		Enabled: true, ManagedExternally: true, Model: model, Host: "127.0.0.1", Port: port, StartupTimeout: time.Second,
	}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stderr, Config: configPath}
	if code := runner.Run(context.Background(), []string{"test", model}); code != 0 {
		t.Fatalf("test command failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if completionCalls == 0 || !strings.Contains(stdout.String(), "healthy") {
		t.Fatalf("test command did not perform semantic inference: calls=%d stdout=%s", completionCalls, stdout.String())
	}
}

func TestRunRootHelpFlagAndCommand(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			r := &Runner{Stdout: &stdout, Stderr: &stderr}
			if code := r.Run(context.Background(), args); code != 0 {
				t.Fatalf("expected help success, got %d", code)
			}
			for _, expected := range []string{"ghrouter - local AI routing engine for CLI workflows", "ghrouter providers", "ghrouter models"} {
				if !strings.Contains(stdout.String(), expected) {
					t.Fatalf("help missing %q: %s", expected, stdout.String())
				}
			}
		})
	}
}

func TestConnectPrintsNativeCopilotEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "copilot"}); code != 0 {
		t.Fatalf("expected connect to succeed, got %d", code)
	}
	for _, needle := range []string{
		"COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:9090/v1",
		"COPILOT_PROVIDER_TYPE=openai",
		"COPILOT_PROVIDER_WIRE_API=responses",
		"COPILOT_PROVIDER_MODEL_ID=gpt-5.4",
		"COPILOT_PROVIDER_WIRE_MODEL=ghrouter/tool-use",
		"COPILOT_PROVIDER_API_KEY=ghr_gh_",
		"COPILOT_MODEL=ghrouter/tool-use",
	} {
		if !strings.Contains(stdout.String(), needle) {
			t.Fatalf("expected connect output to contain %q, got %s", needle, stdout.String())
		}
	}
}

func TestConnectUsesActiveRouterSessionPort(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)
	t.Setenv("GHR_RUNTIME_DIR", tmpDir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve active router port: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	session := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"port":` + strconv.Itoa(port) + `,"executable":"ghrouter","config_path":"` + configPath + `"}`
	if err := os.WriteFile(filepath.Join(tmpDir, "session.json"), []byte(session), 0o600); err != nil {
		t.Fatalf("seed active session: %v", err)
	}

	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: configPath}
	if code := r.Run(context.Background(), []string{"connect", "copilot"}); code != 0 {
		t.Fatalf("expected connect to succeed, got %d", code)
	}
	if !strings.Contains(stdout.String(), "COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:"+strconv.Itoa(port)+"/v1") {
		t.Fatalf("expected active router port in profile, got %s", stdout.String())
	}
}

func TestConnectPrintsNativeCodexEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "codex"}); code != 0 {
		t.Fatalf("expected codex connect to succeed, got %d", code)
	}
	for _, needle := range []string{
		"OPENAI_API_KEY=sk-ghrouter-",
		"CODEX_HOME=",
		"CODEX_MODEL=auto",
		"ghrouter connect codex --install",
		"codex exec --model auto",
		"codex login --with-api-key",
	} {
		if !strings.Contains(stdout.String(), needle) {
			t.Fatalf("expected codex connect output to contain %q, got %s", needle, stdout.String())
		}
	}
}

func TestConnectInstallsCodexProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "codex", "--install"}); code != 0 {
		t.Fatalf("expected codex profile installation to succeed, got %d: %s", code, stdout.String())
	}
	path := filepath.Join(home, ".config", "ghrouter", "codex", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed codex profile: %v", err)
	}
	for _, needle := range []string{"model_provider = \"ghrouter\"", "base_url = \"http://127.0.0.1:9090/v1\"", "wire_api = \"responses\"", "env_key = \"OPENAI_API_KEY\""} {
		if !strings.Contains(string(data), needle) {
			t.Fatalf("installed Codex profile missing %q: %s", needle, data)
		}
	}
}

func TestConnectPrintsNativeOpenCodeEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "opencode"}); code != 0 {
		t.Fatalf("expected opencode connect to succeed, got %d", code)
	}
	for _, needle := range []string{
		"OPENAI_API_KEY=sk-ghrouter-",
		"OPENCODE_CONFIG_CONTENT=",
		"http://127.0.0.1:9090/v1",
		"opencode run --model ghrouter/auto --format json --pure",
	} {
		if !strings.Contains(stdout.String(), needle) {
			t.Fatalf("expected opencode connect output to contain %q, got %s", needle, stdout.String())
		}
	}
}

func TestConnectPrintsNativeMimoEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "mimo"}); code != 0 {
		t.Fatalf("expected mimo connect to succeed, got %d", code)
	}
	for _, needle := range []string{
		"OPENAI_API_KEY=sk-ghrouter-",
		"MIMOCODE_CONFIG_CONTENT=",
		"http://127.0.0.1:9090/v1",
		"mimo run --model ghrouter/auto --format json --pure",
	} {
		if !strings.Contains(stdout.String(), needle) {
			t.Fatalf("expected mimo connect output to contain %q, got %s", needle, stdout.String())
		}
	}
}

func TestConnectPrintsNativePiEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "pi"}); code != 0 {
		t.Fatalf("expected pi connect to succeed, got %d", code)
	}
	for _, needle := range []string{
		"OPENAI_API_KEY=sk-ghrouter-",
		"PI_CODING_AGENT_DIR=",
		"OPENAI_BASE_URL=http://127.0.0.1:9090/v1",
		"Pi uses native RPC",
		"ghrouter connect pi --install",
	} {
		if !strings.Contains(stdout.String(), needle) {
			t.Fatalf("expected pi connect output to contain %q, got %s", needle, stdout.String())
		}
	}
}

func TestConnectInstallsPiProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "pi", "--install"}); code != 0 {
		t.Fatalf("expected pi profile installation to succeed, got %d: %s", code, stdout.String())
	}
	path := filepath.Join(home, ".config", "ghrouter", "pi", "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed pi profile: %v", err)
	}
	for _, needle := range []string{"\"ghrouter\"", "127.0.0.1:9090/v1", "\"auto\"", "openai-completions"} {
		if !strings.Contains(string(data), needle) {
			t.Fatalf("installed Pi profile missing %q: %s", needle, data)
		}
	}
}

func TestInstallCopilotLauncherPreservesExplicitProviderEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedFakeCopilot(t)
	router := filepath.Join(home, "ghrouter")
	if err := os.WriteFile(router, []byte("router-build-fixture"), 0o700); err != nil {
		t.Fatalf("write router fixture: %v", err)
	}
	t.Setenv("GHR_ROUTER_BIN", router)
	if err := installCopilotLauncher(filepath.Join(home, "config.yaml"), "http://127.0.0.1:9090"); err != nil {
		t.Fatalf("install copilot launcher: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "bin", "copilot"))
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `if [ -z "${COPILOT_PROVIDER_BASE_URL:-}" ]; then`) {
		t.Fatalf("launcher does not preserve explicit provider endpoint: %s", content)
	}
}

func TestInstallCopilotLauncherWaitsForRouterReadiness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedFakeCopilot(t)
	router := filepath.Join(home, "ghrouter")
	if err := os.WriteFile(router, []byte("router-build-fixture"), 0o700); err != nil {
		t.Fatalf("write router fixture: %v", err)
	}
	t.Setenv("GHR_ROUTER_BIN", router)
	if err := installCopilotLauncher(filepath.Join(home, "config.yaml"), "http://127.0.0.1:9090"); err != nil {
		t.Fatalf("install copilot launcher: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "bin", "copilot"))
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"$GHROUTER_BASE/readyz"`) {
		t.Fatalf("launcher does not probe router readiness: %s", content)
	}
	if !strings.Contains(content, `router did not become ready`) {
		t.Fatalf("launcher does not report readiness failure: %s", content)
	}
}

func TestInstallCopilotLauncherEmbedsRouterBuildIdentity(t *testing.T) {
	home := t.TempDir()
	seedFakeCopilot(t)
	router := filepath.Join(home, "bin", "ghrouter")
	if err := os.MkdirAll(filepath.Dir(router), 0o700); err != nil {
		t.Fatalf("create router fixture directory: %v", err)
	}
	if err := os.WriteFile(router, []byte("router-build-fixture"), 0o700); err != nil {
		t.Fatalf("write router fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GHR_ROUTER_BIN", router)
	if err := installCopilotLauncher(filepath.Join(home, "config.yaml"), "http://127.0.0.1:9090"); err != nil {
		t.Fatalf("install copilot launcher: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "bin", "copilot"))
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	content := string(data)
	if !regexp.MustCompile(`GHROUTER_BINARY_SHA256='[0-9a-f]{64}'`).MatchString(content) {
		t.Fatalf("launcher does not embed a binary identity: %s", content)
	}
	if !strings.Contains(content, `binary_sha256`) {
		t.Fatalf("launcher does not validate health identity: %s", content)
	}
}

func seedFakeCopilot(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "copilot")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write copilot fixture: %v", err)
	}
	t.Setenv("GHR_COPILOT_BIN", path)
}

func writeFixtureConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "listen_port: 0\nproviders:\n  - name: fixture\n    type: custom\n    cli_path: /bin/true\n    models: [fixture/model]\n    enabled: true\nroutes: []\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return path
}

func TestRouterInvocationPrefersCurrentExecutableOverPath(t *testing.T) {
	current := "/tmp/ghrouter-current"
	pathLookup := func(string) (string, error) {
		return "/tmp/ghrouter-old", nil
	}
	invocation, err := routerInvocationFor(current, pathLookup)
	if err != nil {
		t.Fatalf("resolve router invocation: %v", err)
	}
	if invocation != shellQuote(current) {
		t.Fatalf("expected current executable %q, got %q", current, invocation)
	}
}

func TestMergeDetectedProvidersPreservesManualOverrides(t *testing.T) {
	previous := []*types.Provider{{
		Name:       "codex",
		Type:       types.ProviderCodex,
		Args:       []string{"custom", "flag"},
		Models:     []string{"cx/custom-model"},
		AuthConfig: map[string]string{"plan": "team"},
		BaseURL:    "http://127.0.0.1:8080",
		Enabled:    false,
	}}
	detected := []*types.Provider{{
		Name:    "codex",
		Type:    types.ProviderCodex,
		Args:    []string{"exec", "--json"},
		Models:  []string{"cx/gpt-5"},
		Enabled: true,
	}}

	merged := mergeDetectedProviders(previous, detected)
	if len(merged) != 1 || merged[0].Enabled {
		t.Fatalf("expected existing enabled state to survive sync, got %+v", merged)
	}
	if strings.Join(merged[0].Args, " ") != "custom flag" {
		t.Fatalf("expected manual provider args to survive sync, got %+v", merged[0])
	}
	if len(merged[0].Models) != 1 || merged[0].Models[0] != "cx/gpt-5" {
		t.Fatalf("expected detected catalog to refresh models, got %+v", merged[0].Models)
	}
	if merged[0].AuthConfig["plan"] != "team" {
		t.Fatalf("expected auth config to survive sync, got %+v", merged[0].AuthConfig)
	}
	if merged[0].BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("expected manual base URL to survive sync, got %q", merged[0].BaseURL)
	}
}

func TestMergeDetectedProvidersRefreshesNativeCatalog(t *testing.T) {
	verifiedAt := time.Now().Add(-time.Hour).UTC()
	previous := []*types.Provider{{
		Name: "opencode", Type: types.ProviderOpenCode,
		Models: []string{"oc/old-model", "oc/new-model"},
		ModelInfo: map[string]types.ModelInfo{
			"oc/old-model": {Source: "native"},
			"oc/new-model": {Source: "native", HealthStatus: "healthy", VerifiedAt: verifiedAt},
		},
		Enabled: true,
	}}
	detected := []*types.Provider{{
		Name: "opencode", Type: types.ProviderOpenCode,
		Models:    []string{"oc/new-model"},
		ModelInfo: map[string]types.ModelInfo{"oc/new-model": {Source: "native"}},
		Enabled:   true,
	}}
	merged := mergeDetectedProviders(previous, detected)
	if len(merged) != 1 || len(merged[0].Models) != 1 || merged[0].Models[0] != "oc/new-model" {
		t.Fatalf("expected native catalog refresh, got %+v", merged)
	}
	if merged[0].ModelInfo["oc/new-model"].Source != "native" {
		t.Fatalf("expected detected metadata to survive refresh, got %+v", merged[0].ModelInfo)
	}
	if got := merged[0].ModelInfo["oc/new-model"]; got.HealthStatus != "healthy" || !got.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("expected verification state to survive refresh, got %+v", got)
	}
	if _, ok := merged[0].ModelInfo["oc/old-model"]; ok {
		t.Fatalf("stale metadata should not survive for removed model: %+v", merged[0].ModelInfo)
	}
}

func TestMergeDetectedProvidersRetainsVerifiedModelMissingFromRefresh(t *testing.T) {
	verifiedAt := time.Now().Add(-time.Hour).UTC()
	previous := []*types.Provider{{
		Name: "nvidia", Type: types.ProviderNVIDIA,
		Models: []string{"nv/configured"},
		ModelInfo: map[string]types.ModelInfo{
			"nv/configured": {Source: "env"},
			"nv/verified":   {Source: "nvidia_api", HealthStatus: "healthy", VerifiedAt: verifiedAt},
			"nv/unverified": {Source: "nvidia_api"},
			"nv/failed":     {Source: "nvidia_api", HealthStatus: "failed", VerificationError: "probe failed"},
		},
		Enabled: true,
	}}
	detected := []*types.Provider{{
		Name: "nvidia", Type: types.ProviderNVIDIA,
		Models: []string{"nv/configured"}, Enabled: true,
	}}

	merged := mergeDetectedProviders(previous, detected)
	models := merged[0].Models
	if len(models) != 2 || models[0] != "nv/configured" || models[1] != "nv/verified" {
		t.Fatalf("expected only verified model to survive refresh, got %v", models)
	}
}

func TestFilterExcludedProviderModels(t *testing.T) {
	cfg := &types.Config{
		ModelPolicy: types.ModelPolicy{Excluded: []string{"cx/gpt-5.3-codex-spark"}},
		Providers: []*types.Provider{{
			Name:   "codex",
			Type:   types.ProviderCodex,
			Models: []string{"cx/gpt-5.3-codex-spark", "cx/gpt-5.4", "cx/gpt-5.4-mini", "cx/gpt-5.6-sol"},
			ModelInfo: map[string]types.ModelInfo{
				"cx/gpt-5.3-codex-spark": {Model: "cx/gpt-5.3-codex-spark"},
				"cx/gpt-5.4":             {Model: "cx/gpt-5.4"},
				"cx/gpt-5.4-mini":        {Model: "cx/gpt-5.4-mini"},
				"cx/gpt-5.6-sol":         {Model: "cx/gpt-5.6-sol"},
			},
		}},
	}

	filterExcludedProviderModels(cfg)
	provider := cfg.Providers[0]
	if len(provider.Models) != 2 || provider.Models[0] != "cx/gpt-5.4-mini" || provider.Models[1] != "cx/gpt-5.6-sol" {
		t.Fatalf("expected excluded model removed from catalog, got %v", provider.Models)
	}
	if _, ok := provider.ModelInfo["cx/gpt-5.3-codex-spark"]; ok {
		t.Fatal("expected excluded model metadata removed from catalog")
	}
}

func TestApplyModelVerificationPersistsFunctionalState(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC()
	cfg := &types.Config{Providers: []*types.Provider{{Name: "alpha", Models: []string{"model-a", "model-b"}, ModelInfo: map[string]types.ModelInfo{"model-a": {Source: "native"}, "model-b": {Source: "native"}}}}}
	results := []server.ModelTestResult{
		{Provider: "alpha", Model: "model-a", Status: "healthy", OK: true},
		{Provider: "alpha", Model: "model-b", Status: "failed", Error: "provider unavailable", CooldownUntil: resetAt},
	}
	applyModelVerification(cfg, results, time.Now().UTC())

	healthy := cfg.Providers[0].ModelInfo["model-a"]
	failed := cfg.Providers[0].ModelInfo["model-b"]
	if healthy.HealthStatus != "healthy" || healthy.VerificationError != "" || healthy.VerifiedAt.IsZero() {
		t.Fatalf("expected healthy verification state, got %+v", healthy)
	}
	if failed.HealthStatus != "failed" || !failed.CooldownUntil.Equal(resetAt) || failed.VerificationError == "" || !failed.VerifiedAt.IsZero() {
		t.Fatalf("expected failed verification state, got %+v", failed)
	}
}

func TestApplyModelVerificationAddsNewHealthyModelToProvider(t *testing.T) {
	cfg := &types.Config{Providers: []*types.Provider{{
		Name:      "nvidia",
		Models:    []string{"nv/configured"},
		ModelInfo: map[string]types.ModelInfo{"nv/discovered": {Source: "nvidia_api"}},
	}}}

	applyModelVerification(cfg, []server.ModelTestResult{{
		Provider: "nvidia", Model: "nv/discovered", Status: "healthy", OK: true,
	}}, time.Now().UTC())

	found := false
	for _, model := range cfg.Providers[0].Models {
		if model == "nv/discovered" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected verified discovered model in provider models, got %v", cfg.Providers[0].Models)
	}
}

func TestApplyModelVerificationReusesCanonicalModelMetadata(t *testing.T) {
	verifiedAt := time.Now().UTC()
	cfg := &types.Config{Providers: []*types.Provider{{
		Name:   "codex",
		Type:   types.ProviderCodex,
		Models: []string{"gpt-5.4-mini"},
		ModelInfo: map[string]types.ModelInfo{
			"cx/gpt-5.4-mini": {Model: "cx/gpt-5.4-mini", Source: "native"},
		},
	}}}

	applyModelVerification(cfg, []server.ModelTestResult{{
		Provider: "codex", Model: "gpt-5.4-mini", Status: "healthy", OK: true,
	}}, verifiedAt)

	info := cfg.Providers[0].ModelInfo["cx/gpt-5.4-mini"]
	if !info.VerifiedAt.Equal(verifiedAt) || info.HealthStatus != "healthy" {
		t.Fatalf("expected canonical metadata to be updated, got %+v", info)
	}
	if _, ok := cfg.Providers[0].ModelInfo["gpt-5.4-mini"]; ok {
		t.Fatal("verification should not create duplicate unprefixed metadata")
	}
}

func TestMarkLocalBrainToolModelsOnlyMarksVerifiedModel(t *testing.T) {
	provider := &types.Provider{
		Name:   "local-brain",
		Models: []string{"mlx-community/Qwen3.5-0.8B-OptiQ-4bit", "mlx-community/Gemma-4-1B-OptiQ-4bit"},
		ModelInfo: map[string]types.ModelInfo{
			"mlx-community/Qwen3.5-0.8B-OptiQ-4bit": {Source: "native"},
			"mlx-community/Gemma-4-1B-OptiQ-4bit":   {Source: "native"},
		},
	}

	markLocalBrainToolModels(provider, "mlx-community/Qwen3.5-0.8B-OptiQ-4bit")
	verified := provider.ModelInfo["mlx-community/Qwen3.5-0.8B-OptiQ-4bit"]
	unverified := provider.ModelInfo["mlx-community/Gemma-4-1B-OptiQ-4bit"]
	if !verified.ToolUse || verified.HealthStatus != "healthy" || verified.VerifiedAt.IsZero() {
		t.Fatalf("expected probed local model to be verified, got %+v", verified)
	}
	if unverified.ToolUse || unverified.HealthStatus != "" || !unverified.VerifiedAt.IsZero() {
		t.Fatalf("expected unprobed local model to remain unverified, got %+v", unverified)
	}
}

func TestLoadConfigRepairsStaleProviderPathFromPATH(t *testing.T) {
	temp := t.TempDir()
	configPath := filepath.Join(temp, "config.yaml")
	configBody := "listen_port: 9090\nproviders:\n  - name: codex\n    type: codex\n    cli_path: /missing/codex\n    models: [cx/gpt-5.4]\n    enabled: true\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	binDir := filepath.Join(temp, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].CLIPath != codexPath {
		t.Fatalf("expected stale CLI path to be repaired, got %q", cfg.Providers[0].CLIPath)
	}
}

func TestLoadCurrentConfigRefreshesDetectedNativeCatalog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shims are not wired for windows in this repo")
	}
	temp := t.TempDir()
	configPath := filepath.Join(temp, "config.yaml")
	configBody := "listen_port: 9090\nproviders:\n  - name: opencode\n    type: opencode\n    cli_path: /missing/opencode\n    models: [oc/old-model]\n    model_info:\n      oc/old-model:\n        source: configured\n    enabled: true\n  - name: codex\n    type: codex\n    cli_path: /missing/codex\n    models: [cx/gpt-5.6-sol]\n    model_info:\n      cx/gpt-5.6-sol:\n        source: configured\n    enabled: true\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(temp, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(`#!/bin/sh
if [ "$1" = "--help" ]; then
  printf 'OpenCode native acp help\n'
  exit 0
fi
if [ "$1" = "models" ]; then
  printf 'oc/new-model\n'
  exit 0
fi
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(`#!/bin/sh
if [ "$1" = "--help" ]; then
  printf 'codex help without acp\n'
  exit 0
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCurrentConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var opencode, codex *types.Provider
	for _, provider := range cfg.Providers {
		if provider == nil {
			continue
		}
		switch provider.Type {
		case types.ProviderOpenCode:
			opencode = provider
		case types.ProviderCodex:
			codex = provider
		}
	}
	if opencode == nil || codex == nil {
		t.Fatalf("expected both providers, got %+v", cfg.Providers)
	}
	if len(opencode.Models) != 1 || opencode.Models[0] != "oc/new-model" {
		t.Fatalf("expected opencode catalog refresh, got %+v", opencode.Models)
	}
	if len(codex.Models) != 1 || codex.Models[0] != "cx/gpt-5.6-sol" {
		t.Fatalf("expected codex configured catalog to survive unsupported discovery, got %+v", codex.Models)
	}
}

func TestRunLiveCommandProducesSnapshot(t *testing.T) {
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	code := r.Run(ctx, []string{"live"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), `"listen_port"`) {
		t.Fatalf("expected live snapshot output, got %s", stdout.String())
	}
}

func TestAttachLocalBrainRejectsRouterPortConflict(t *testing.T) {
	_, err := attachLocalBrain(context.Background(), &types.Config{
		ListenPort: 19090,
		LocalBrain: types.LocalBrainConfig{
			Enabled: true,
			Model:   "mlx-community/Qwen3.5-0.8B-OptiQ-4bit",
			Port:    19090,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with router listen port") {
		t.Fatalf("expected explicit local Brain port conflict, got %v", err)
	}
}

func TestAttachLocalBrainSkipsDisabledConfig(t *testing.T) {
	supervisor, err := attachLocalBrain(context.Background(), &types.Config{
		ListenPort: 9090,
		LocalBrain: types.LocalBrainConfig{Enabled: false, Host: "127.0.0.1", Port: 19090},
	})
	if err != nil {
		t.Fatalf("disabled local brain should not start or fail: %v", err)
	}
	if supervisor != nil {
		t.Fatal("disabled local brain unexpectedly started")
	}
}

func TestAttachLocalBrainUsesExternalOfficialMLXServerWithoutTools(t *testing.T) {
	model := "mlx-community/gemma-4-e2b-it-4bit"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mlx-community/gemma-4-e2b-it-4bit","owned_by":"mlx"}]}`))
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"GHROUTER_MLX_READY"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &types.Config{ListenPort: 1, LocalBrain: types.LocalBrainConfig{
		Enabled:           true,
		ManagedExternally: true,
		Model:             model,
		Host:              "127.0.0.1",
		Port:              port,
		StartupTimeout:    time.Second,
	}}

	supervisor, err := attachLocalBrain(context.Background(), cfg)
	if err != nil {
		t.Fatalf("attach official MLX server: %v", err)
	}
	if supervisor != nil {
		t.Fatal("external MLX server must not be owned by Ghrouter")
	}
	var attached *types.Provider
	for _, provider := range cfg.Providers {
		if provider != nil && provider.Name == "local-brain" {
			attached = provider
			break
		}
	}
	if attached == nil || attached.BaseURL != server.URL || len(attached.Models) != 1 {
		t.Fatalf("unexpected external MLX provider: %+v", attached)
	}
	info := attached.ModelInfo[model]
	if info.ToolUse {
		t.Fatal("official mlx_lm.server must not be advertised as tool-capable")
	}
}

func TestSelectLocalBrainPortSkipsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	preferred := listener.Addr().(*net.TCPAddr).Port
	selected, err := selectLocalBrainPort("127.0.0.1", preferred, 9090)
	if err != nil {
		t.Fatal(err)
	}
	if selected == preferred || selected == 9090 {
		t.Fatalf("selected occupied or router port: preferred=%d selected=%d", preferred, selected)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(selected))
	if err != nil {
		t.Fatalf("selected port was not available: %v", err)
	}
	probe.Close()
}

func TestIsLocalBrainModelAcceptsVirtualOrConfiguredID(t *testing.T) {
	if !isLocalBrainModel("ghrouter/local-brain", "mlx-community/Qwen3.5-0.8B-OptiQ-4bit") {
		t.Fatal("expected virtual local brain id to be accepted")
	}
	if !isLocalBrainModel("mlx-community/Qwen3.5-0.8B-OptiQ-4bit", "mlx-community/Qwen3.5-0.8B-OptiQ-4bit") {
		t.Fatal("expected configured local brain model id to be accepted")
	}
	if !isLocalBrainModel("/cache/mlx-community-Qwen3.5-0.8B-OptiQ-4bit", "mlx-community/Qwen3.5-0.8B-OptiQ-4bit") {
		t.Fatal("expected physical model path to match configured model id")
	}
	if isLocalBrainModel("mlx-community/other-model", "mlx-community/Qwen3.5-0.8B-OptiQ-4bit") {
		t.Fatal("unexpected unrelated local model match")
	}
}

func TestIsLocalBrainEndpointModelRejectsAnotherGhrouter(t *testing.T) {
	if isLocalBrainEndpointModel("ghrouter/local-brain", "ghrouter", local_brain.DefaultModel) {
		t.Fatal("another ghrouter instance must not be accepted as the local brain")
	}
	if !isLocalBrainEndpointModel(local_brain.DefaultModel, "mlx", local_brain.DefaultModel) {
		t.Fatal("expected a physical local model endpoint to be accepted")
	}
}

func TestLocalBrainModelsDoNotAdvertiseColdCompanionByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	modelRoot := filepath.Join(home, ".localmodel")
	t.Setenv("GHR_LOCAL_MODEL_ROOT", modelRoot)
	companion := strings.ReplaceAll(local_brain.DefaultCompanionModel, "/", "-")
	if err := os.MkdirAll(filepath.Join(modelRoot, "mlx", companion), 0o755); err != nil {
		t.Fatal(err)
	}
	models := localBrainModels(local_brain.DefaultModel, false)
	if len(models) != 1 || models[0] != local_brain.DefaultModel {
		t.Fatalf("expected only the configured warm model by default, got %v", models)
	}
	models = localBrainModels(local_brain.DefaultModel, true)
	if len(models) != 2 || models[1] != local_brain.DefaultCompanionModel {
		t.Fatalf("expected opt-in companion model, got %v", models)
	}
}

func TestLiveSettingsPanelShowsActionStatusAndCommands(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.panel = panelIndex("settings")
	model.lastAction = "sync: ok"
	model.settings = settingsModePort

	view := settingsPanel(model)
	if !strings.Contains(view, "last action: sync: ok") {
		t.Fatalf("expected last action in settings view, got %s", view)
	}
	for _, needle := range []string{"mode: port", "p edit port", "enter save port", "esc cancel edit", "g doctor", "s sync", "x reset preview", "X reset apply", "u update check", "U update apply"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected action hint %q in settings view, got %s", needle, view)
		}
	}
}

func TestLiveSettingsSavesMaskedNVIDIAAccount(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newLiveTUIModel(&types.Config{}, cfgPath)
	msg := model.saveNVIDIAAccountCmd(types.ProviderCredential{Name: "primary", APIKey: "secret-value", Enabled: true})()
	result, ok := msg.(liveActionMsg)
	if !ok || result.err != nil {
		t.Fatalf("expected account save success, got %#v", msg)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || len(cfg.Providers[0].Accounts) != 1 || cfg.Providers[0].Accounts[0].Name != "primary" {
		t.Fatalf("expected persisted NVIDIA account, got %+v", cfg.Providers)
	}
	view := modernSettingsPage(model, 100)
	if strings.Contains(view, "secret-value") {
		t.Fatal("settings view must not render the NVIDIA API key")
	}
}

func TestLiveControlPlanePanelSelectsAndEditsResource(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.panel = panelIndex("control-plane")
	model.snapshot.Pools = []server.PoolSummary{{Name: "ghrouter/fast", Members: []string{"codex/cx/gpt-5"}, Strategy: "round-robin", Enabled: true}}
	view := controlPlanePanel(model)
	for _, needle := range []string{"control plane", "pool/ghrouter/fast", "e edit JSON", "codex/cx/gpt-5"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected control-plane element %q, got %s", needle, view)
		}
	}
	next, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	updated := next.(liveTUIModel)
	if !updated.controlPlaneEdit || updated.controlPlaneKind != "pool" || updated.controlPlaneName != "ghrouter/fast" {
		t.Fatalf("expected selected pool editor, got edit=%t kind=%q name=%q", updated.controlPlaneEdit, updated.controlPlaneKind, updated.controlPlaneName)
	}
	if !strings.Contains(updated.input.Value(), "round-robin") {
		t.Fatalf("expected resource JSON in editor, got %q", updated.input.Value())
	}
}

func TestLiveDashboardUsesResponsiveControlRail(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 160
	model.height = 40
	model.hasSnapshot = true
	model.snapshot = server.LiveSnapshot{ListenPort: 9090}
	model.report = local_brain.BootstrapReport{Backend: local_brain.BackendMLX, Checks: []local_brain.StartupCheck{{Provider: "codex", Ready: true}}}

	view := dashboardPanel(model)
	for _, needle := range []string{"OVERVIEW", "CONTROL RAIL", "runtime checklist", "checks: 1/1 ready", "endpoint: 127.0.0.1:9090"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected dashboard element %q, got %s", needle, view)
		}
	}
}

func TestLiveCommandCenterUsesProductTabsAndAnimatedStatus(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 160
	model.height = 40
	model.panel = panelIndex("settings")
	model.hasSnapshot = true
	model.report = local_brain.BootstrapReport{Checks: []local_brain.StartupCheck{{Provider: "router", Ready: true}}}
	model.graphFrame = 2

	ansi := regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]`)
	navigation := ansi.ReplaceAllString(navView(model), "")
	for _, label := range []string{"DASHBOARD", "PROVIDERS", "MODELS", "ROUTES", "CONTROL PLANE", "ACTIVITY", "SETTINGS"} {
		if !strings.Contains(navigation, label) {
			t.Fatalf("expected product tab %q, got %s", label, navigation)
		}
	}
	if !strings.Contains(navigation, "SETTINGS") {
		t.Fatalf("expected active settings tab, got %s", navigation)
	}
	header := ansi.ReplaceAllString(commandCenterHeader(model), "")
	if !strings.Contains(header, "CONTROL CENTER") || !strings.Contains(header, "ghrouter") {
		t.Fatalf("expected command center branding, got %s", header)
	}
	footer := footerView(model)
	if !strings.Contains(footer, "SETTINGS") || !strings.Contains(footer, "TAB") {
		t.Fatalf("expected contextual footer, got %s", footer)
	}
}

func TestModernLiveTUIUsesNewInformationArchitecture(t *testing.T) {
	model := newLiveTUIModel(&types.Config{ListenPort: 9090}, "config.yaml")
	model.width = 160
	model.height = 44
	model.hasSnapshot = true
	model.report = local_brain.BootstrapReport{Checks: []local_brain.StartupCheck{{Provider: "router", Ready: true}}}
	model.snapshot = server.LiveSnapshot{
		ListenPort: 9090,
		Providers:  []server.ProviderSnapshot{{Name: "opencode", Type: "opencode", Available: true, Health: "healthy", Models: []string{"oc/free"}}},
		Models:     []server.ModelSummary{{ID: "oc/free", OwnedBy: "opencode", Health: "healthy", ContextWindow: 128000}},
		Telemetry:  server.TelemetrySnapshot{Requests: 12, Successful: 11, Failed: 1, Active: 1},
		Graph: server.RoutingGraphSnapshot{Nodes: []server.RoutingGraphNode{
			{ID: "brain", Kind: "brain", Label: "BRAIN", Status: "virtual"},
			{ID: "model/oc/free", Kind: "model", Label: "oc/free", Status: "available", Provider: "opencode"},
		}},
	}

	view := modernLiveView(model)
	for _, needle := range []string{"LIVE", "BRAIN LOG", "CATALOG", "TOPOLOGY", "ROUTES", "ACTIVITY", "SETTINGS", "MODEL GRAPH", "BRAIN DECISION", "CATALOG READINESS"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected v2 surface element %q, got %s", needle, view)
		}
	}

	model.panel = panelIndex("providers")
	if view := modernLiveView(model); !strings.Contains(view, "PROVIDER FLEET") || !strings.Contains(view, "opencode") {
		t.Fatalf("expected provider tab, got %s", view)
	}
	model.panel = panelIndex("models")
	if view := modernLiveView(model); !strings.Contains(view, "MODEL CATALOG") || !strings.Contains(view, "oc/free") {
		t.Fatalf("expected catalog tab, got %s", view)
	}
}

func TestModernBrainLogShowsObservedDecisionAndAttempts(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 140
	model.height = 40
	model.panel = panelIndex("brain-log")
	model.snapshot.Telemetry.Recent = []server.RequestEvent{{
		At:           time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC),
		Provider:     "opencode",
		Model:        "oc/free",
		Status:       "ok",
		Fallback:     true,
		DecisionJSON: `{"selected":"oc/free","reason":"healthy"}`,
		Attempts:     []server.AttemptEvent{{Provider: "codex", Model: "cx/old", Status: "cooldown"}},
	}}

	view := modernLiveView(model)
	for _, needle := range []string{"BRAIN LOG", "oc/free", "selected", "attempt codex/cx/old", "cooldown"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected observed Brain log field %q, got %s", needle, view)
		}
	}
}

func TestLiveFilterEscapesWithoutQuitting(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.panel = panelIndex("providers")
	updatedModel, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	updated := updatedModel.(liveTUIModel)
	if !updated.filterActive {
		t.Fatal("expected filter mode to be active")
	}
	updatedModel, cmd := updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd != nil {
		t.Fatal("expected escape from filter to avoid quitting")
	}
	if updatedModel.(liveTUIModel).filterActive {
		t.Fatal("expected filter mode to close on escape")
	}
}

func TestLiveFilterAppliesToModelsAndRoutes(t *testing.T) {
	model := newLiveTUIModel(&types.Config{Routes: []*types.Route{{Pattern: "cc/*", Provider: "claude"}, {Pattern: "cx/*", Provider: "codex"}}}, "config.yaml")
	model.width = 120
	model.snapshot.Models = []server.ModelSummary{{ID: "opus", OwnedBy: "claude"}, {ID: "gpt-5", OwnedBy: "codex"}}

	model.panel = panelIndex("models")
	model.input.SetValue("opus")
	model.filterActive = true
	modelView := modelsPanelLines(model)
	if strings.Join(modelView, "\n") == "" || !strings.Contains(strings.Join(modelView, "\n"), "claude/opus") || strings.Contains(strings.Join(modelView, "\n"), "codex/gpt-5") {
		t.Fatalf("expected model filter to keep only opus, got %v", modelView)
	}

	model.panel = panelIndex("routes")
	model.input.SetValue("cx")
	routeView := routesPanelLines(model)
	if !strings.Contains(strings.Join(routeView, "\n"), "cx/*") || strings.Contains(strings.Join(routeView, "\n"), "cc/*") {
		t.Fatalf("expected route filter to keep only cx route, got %v", routeView)
	}
}

func TestLiveProviderSelectionUsesVisibleSortedProviders(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 120
	model.panel = panelIndex("providers")
	model.snapshot.Providers = []server.ProviderSnapshot{{Name: "zeta"}, {Name: "alpha"}, {Name: "beta"}}
	model.input.SetValue("alpha")
	model.filterActive = true

	view := providersPanel(model)
	if !strings.Contains(view, "name: alpha") || strings.Contains(view, "name: zeta") {
		t.Fatalf("expected selected detail to follow visible filter, got %s", view)
	}
}

func TestLiveSmallTerminalShowsResizeFallback(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 20
	model.height = 8

	view := renderLiveTUIView(model).Content
	if !strings.Contains(view, "resize terminal") {
		t.Fatalf("expected resize fallback for narrow terminal, got %s", view)
	}
}

func TestLiveDashboardUsesHealthSnapshotAndCatalogReadyCount(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 180
	model.snapshot = server.LiveSnapshot{
		Providers: []server.ProviderSnapshot{{Name: "codex", Health: "unknown", Available: false}},
		Models: []server.ModelSummary{
			{ID: "cx/gpt-5.4", OwnedBy: "codex", Health: "healthy"},
			{ID: "cx/gpt-5.3", OwnedBy: "codex", Health: "degraded"},
			{ID: "ghrouter/auto", OwnedBy: "ghrouter", Health: "virtual", List: true},
		},
		Health: server.HealthSnapshot{
			Providers: map[string]server.HealthState{"codex": {Status: "degraded"}},
			Models:    server.ModelReadiness{Catalog: 3, VerifiedHealthy: 1},
		},
	}
	view := strings.Join(commandCenterProviderBody(model, model.snapshot.Providers, 0), "\n")
	if !strings.Contains(view, "◐ degraded") {
		t.Fatalf("expected provider card to use health snapshot, got %s", view)
	}
	telemetry := commandCenterTelemetry(model, model.width)
	if !strings.Contains(telemetry, "1 ready / 3 catalog") {
		t.Fatalf("expected ready/catalog distinction, got %s", telemetry)
	}
}

func TestLiveDashboardDoesNotRenderClientKeyFragments(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.snapshot.ClientKeys = map[string]string{
		"github":    "ghr_gh_secret-fragment",
		"openai":    "sk-ghrouter-secret-fragment",
		"anthropic": "sk-ant-ghrouter-secret-fragment",
	}
	body := strings.Join(commandCenterAPIBody(model), "\n")
	for _, secret := range model.snapshot.ClientKeys {
		if strings.Contains(body, secret) {
			t.Fatalf("dashboard rendered a client key fragment: %s", body)
		}
	}
	if !strings.Contains(body, "auth: router keys active") {
		t.Fatalf("expected generated router key status, got %s", body)
	}
}

func TestLiveTopologyLabelsHiddenProviders(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 180
	model.snapshot.Providers = []server.ProviderSnapshot{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}, {Name: "f"},
	}
	view := commandCenterStage(model, model.width)
	if !strings.Contains(view, "showing 3/6 providers") {
		t.Fatalf("expected topology to disclose hidden providers, got %s", view)
	}
}

type trackingLiveSource struct {
	started chan context.Context
}

func (s trackingLiveSource) Start(ctx context.Context) { s.started <- ctx }

func (s trackingLiveSource) Snapshot() (server.LiveSnapshot, local_brain.BootstrapReport, error) {
	return server.LiveSnapshot{}, local_brain.BootstrapReport{}, nil
}

func TestStartLiveSourcePassesCancellableContext(t *testing.T) {
	started := make(chan context.Context, 1)
	source := trackingLiveSource{started: started}
	ctx, cancel := context.WithCancel(context.Background())
	startLiveSource(source, ctx)

	select {
	case got := <-started:
		cancel()
		select {
		case <-got.Done():
		case <-time.After(time.Second):
			t.Fatal("expected source context to be canceled")
		}
	default:
		t.Fatal("expected source.Start to be called")
	}
}

func TestLiveActionCommandDoesNotStartAfterCancellation(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	cancel := model.actionCancel
	cancel()

	raw := model.runActionCmd(liveActionDoctor)()
	msg, ok := raw.(liveActionMsg)
	if !ok {
		t.Fatalf("expected liveActionMsg, got %T", raw)
	}
	if msg.err == nil || !strings.Contains(msg.err.Error(), "context canceled") {
		t.Fatalf("expected canceled action, got %+v", msg)
	}
}

func TestLiveSnapshotRecoveryClearsRuntimeError(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.runtimeErr = fmt.Errorf("bind failed")
	updatedModel, _ := model.Update(liveSnapshotMsg{seq: model.issuedSeq, snapshot: server.LiveSnapshot{ListenPort: 9090}})
	if updatedModel.(liveTUIModel).runtimeErr != nil {
		t.Fatal("expected a valid snapshot to clear the runtime error")
	}
}

func TestLiveRuntimeFailureIsRenderedOffline(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.hasSnapshot = true
	model.runtimeFailed = true
	model.snapshot = server.LiveSnapshot{ListenPort: 9090}
	view := metricsRow(model)
	if !strings.Contains(view, "OFFLINE") || !strings.Contains(view, "offline") {
		t.Fatalf("expected offline server card, got %s", view)
	}
}

func TestLiveScrollablePanelMovesViewport(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.panel = panelIndex("activity")
	model.width = 100
	model.height = 20
	for i := 0; i < 20; i++ {
		model.snapshot.Telemetry.Recent = append(model.snapshot.Telemetry.Recent, server.RequestEvent{Provider: "codex", Model: fmt.Sprintf("m-%d", i)})
	}
	model.activityTable.SetRows(activityTableRows(model.snapshot))
	for i := 0; i < 10; i++ {
		updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
		model = updated.(liveTUIModel)
	}
	if row := model.activityTable.SelectedRow(); len(row) == 0 || row[3] != "m-10" {
		t.Fatalf("expected activity table selection to move, got %v", row)
	}
}

func TestLiveActivityPanelUsesStructuredTable(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 120
	model.height = 40
	model.snapshot.Telemetry.Recent = []server.RequestEvent{{
		At:       time.Date(2026, time.August, 1, 12, 34, 56, 0, time.UTC),
		Endpoint: "/v1/chat/completions",
		Provider: "codex",
		Model:    "cx/gpt-5",
		Status:   "ok",
	}}
	model.activityTable.SetRows(activityTableRows(model.snapshot))
	view := activityPanel(model)
	for _, needle := range []string{"TIME", "ENDPOINT", "PROVIDER", "cx/gpt-5", "direct"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected activity table element %q, got %s", needle, view)
		}
	}
}

func TestLiveProvidersPanelShowsSelectedProviderDetail(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.panel = panelIndex("providers")
	model.snapshot = server.LiveSnapshot{
		Providers: []server.ProviderSnapshot{
			{
				Name:      "claude-code",
				Type:      "claude-code",
				CLIPath:   "/usr/local/bin/claude",
				Models:    []string{"cc/opus"},
				Available: true,
				Health:    "healthy",
				Auth:      "ok",
			},
			{
				Name:      "codex",
				Type:      "codex",
				CLIPath:   "/usr/local/bin/codex",
				Models:    []string{"cx/gpt-5"},
				Available: true,
				Health:    "degraded",
				Auth:      "missing",
			},
		},
		Health: server.HealthSnapshot{
			Providers: map[string]server.HealthState{
				"codex": {Status: "degraded"},
			},
		},
		Telemetry: server.TelemetrySnapshot{
			ProviderUsage: map[string]int{"codex": 7},
			LatencyMs:     map[string]int64{"codex": 123},
		},
	}
	model.selected = 1

	view := strings.Join(providerDetailCard(model, model.snapshot.Providers), "\n")
	for _, needle := range []string{"selected provider detail", "name: codex", "cli: /usr/local/bin/codex", "usage: 7", "latency: 123ms", "keys: ↑ ↓ select provider"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected %q in providers view, got %s", needle, view)
		}
	}
}

func TestLiveProviderSelectionMovesWithKeys(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.panel = panelIndex("providers")
	model.snapshot = server.LiveSnapshot{
		Providers: []server.ProviderSnapshot{{Name: "a"}, {Name: "b"}, {Name: "c"}},
	}

	next, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
	updated := next.(liveTUIModel)
	if updated.selected != 1 {
		t.Fatalf("expected selected provider 1 after j, got %d", updated.selected)
	}
	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'}))
	updated = next.(liveTUIModel)
	if updated.selected != 0 {
		t.Fatalf("expected selected provider 0 after k, got %d", updated.selected)
	}
}

func TestLiveUsagePageRendersObservedModelKPIs(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 120
	model.height = 30
	model.snapshot.Telemetry = server.TelemetrySnapshot{
		Requests:     5,
		Successful:   4,
		Failed:       1,
		Fallbacks:    2,
		ModelUsage:   map[string]int{"local-brain/model": 3, "nv/model": 2},
		ModelLatency: map[string]server.ModelLatencySnapshot{"local-brain/model": {Samples: 3, P50Ms: 42, P95Ms: 71}},
	}

	view := modernUsagePage(model, 110)
	for _, needle := range []string{"MODEL USAGE", "REQUESTS", "SUCCESS RATE", "local-brain/model", "42/71ms"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected %q in usage view, got %s", needle, view)
		}
	}
}

func TestLiveSnapshotFailureKeepsLastSnapshotAndMarksStale(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.snapshot = server.LiveSnapshot{Telemetry: server.TelemetrySnapshot{Requests: 9}}
	model.lastFetch = time.Now().Add(-time.Second)
	model.hasSnapshot = true

	next, _ := model.Update(liveSnapshotMsg{seq: 1, err: fmt.Errorf("upstream unavailable")})
	updated := next.(liveTUIModel)
	if !updated.stale || updated.snapshot.Telemetry.Requests != 9 {
		t.Fatalf("expected stale state with last snapshot preserved, got stale=%t requests=%d", updated.stale, updated.snapshot.Telemetry.Requests)
	}
	if !strings.Contains(bannerView(updated), "snapshot stale") {
		t.Fatalf("expected stale banner, got %s", bannerView(updated))
	}
}

func TestLiveSlashOpensCommandPalette(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	next, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "/", Code: '/'}))
	updated := next.(liveTUIModel)
	if updated.overlay != overlayPalette {
		t.Fatalf("expected slash to open command palette, got %q", updated.overlay)
	}
}

func TestLivePaletteSelectionNavigatesAndConfirms(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.overlay = overlayPalette
	model.palette = "apply reset"
	model.input.SetValue("apply reset")

	next, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "down", Code: tea.KeyDown}))
	updated := next.(liveTUIModel)
	if updated.paletteSel != 0 {
		t.Fatalf("expected single filtered command to remain selected, got %d", updated.paletteSel)
	}
	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Text: "enter", Code: tea.KeyEnter}))
	updated = next.(liveTUIModel)
	if updated.overlay != overlayConfirm || updated.confirmKind != liveActionResetApply {
		t.Fatalf("expected reset confirmation, got overlay=%q action=%q", updated.overlay, updated.confirmKind)
	}
}

func TestLiveCompactLayoutUsesLoadingState(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 80
	model.height = 24
	view := renderLiveTUIView(model).Content
	if !strings.Contains(view, "SERVER connecting") {
		t.Fatalf("expected compact loading metrics, got %s", view)
	}
	if strings.Contains(view, "port: 0") {
		t.Fatalf("compact loading view must not expose zero port, got %s", view)
	}
}

func TestLiveMetricsGridStaysInsideTerminalWidth(t *testing.T) {
	for _, width := range []int{80, 120, 185} {
		model := newLiveTUIModel(&types.Config{}, "config.yaml")
		model.width = width
		view := metricsRow(model)
		for _, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > panelContentWidth(width, 120) {
				t.Fatalf("width=%d rendered metric line at %d columns, want <= %d: %q", width, got, panelContentWidth(width, 120), line)
			}
		}
	}
}

func TestLiveRoutingGraphShowsProvidersRouterAndClients(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 185
	model.snapshot = server.LiveSnapshot{
		ListenPort: 9090,
		Providers: []server.ProviderSnapshot{
			{Name: "claude-code", Type: "claude-code", Models: []string{"cc/opus"}, Available: true, Health: "healthy"},
			{Name: "codex", Type: "codex", Models: []string{"cx/gpt-5"}, Available: true, Health: "degraded"},
		},
	}
	model.report.Backend = local_brain.BackendMLX
	model.graphFrame = 2
	view := routingGraph(model)
	for _, needle := range []string{"CLAUDE-CODE", "CODEX", "GHROUTER", "GH COPILOT", "CLAUDE CODE", "CURSOR", "●"} {
		if !strings.Contains(view, needle) {
			t.Fatalf("expected routing graph element %q, got %s", needle, view)
		}
	}
}

func TestLiveRoutingGraphMovesTrafficAcrossBothHops(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 185
	model.snapshot = server.LiveSnapshot{
		Providers: []server.ProviderSnapshot{{Name: "codex", Type: "codex", Available: true, Health: "healthy"}},
		Telemetry: server.TelemetrySnapshot{
			Active: 1,
			Recent: []server.RequestEvent{{Provider: "codex", At: time.Now()}},
		},
	}
	model.graphFrame = 1
	first := routingGraph(model)
	model.graphFrame = 10
	second := routingGraph(model)
	if !strings.Contains(first, "●") || !strings.Contains(second, "●") {
		t.Fatalf("expected active traffic marker in both frames")
	}
	if first == second {
		t.Fatal("expected traffic marker to move between animation frames")
	}
}

func TestLiveRoutingGraphShowsModelLegendAndAvailability(t *testing.T) {
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	model.width = 185
	model.snapshot = server.LiveSnapshot{
		Providers: []server.ProviderSnapshot{{Name: "opencode", Type: "opencode", Available: true, Health: "healthy"}},
		Models: []server.ModelSummary{
			{ID: "oc/free", OwnedBy: "opencode", Health: "healthy"},
			{ID: "oc/down", OwnedBy: "opencode", Health: "unhealthy"},
			{ID: "oc/wait", OwnedBy: "opencode", Health: "cooldown", CooldownUntil: time.Now().Add(time.Hour)},
			{ID: "oc/unknown", OwnedBy: "opencode", Health: "unknown"},
		},
	}
	view := routingGraph(model)
	for _, needle := range []string{"MODEL AVAILABILITY", "oc/free", "oc/down", "oc/wait", "oc/unknown", "available", "unavailable", "cooldown", "unknown"} {
		if !strings.Contains(strings.ToLower(view), strings.ToLower(needle)) {
			t.Fatalf("expected model graph element %q, got %s", needle, view)
		}
	}
}

func TestAttachedSourceReadsLiveSnapshotAndBootstrap(t *testing.T) {
	serverResponse := `{"snapshot":{"listen_port":9090,"providers":[{"name":"codex","health":"degraded"}],"models":[],"slots":{},"health":{"healthy":0,"degraded":1,"unhealthy":0,"cooldown":0,"unknown":0,"providers":{}},"telemetry":{"requests":4,"successful":3,"failed":1,"fallbacks":1,"active":0,"recent":[],"provider_usage":{},"latency_ms":{}}},"bootstrap":{"ready":false,"backend":"mlx","issues":[{"Provider":"codex","Backend":"mlx","Model":"cx/gpt-5","Reason":"missing auth"}],"checks":[],"provision":[],"suggestions":[]}}`
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live" {
			t.Fatalf("expected /live, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(serverResponse))
	}))
	defer httpServer.Close()

	source := attachedSource{baseURL: httpServer.URL, client: httpServer.Client()}
	snapshot, report, err := source.Snapshot()
	if err != nil {
		t.Fatalf("expected attached snapshot, got %v", err)
	}
	if snapshot.Telemetry.Requests != 4 || snapshot.Providers[0].Name != "codex" {
		t.Fatalf("unexpected attached snapshot: %+v", snapshot)
	}
	if report.Ready() || report.Backend != local_brain.BackendMLX {
		t.Fatalf("expected degraded bootstrap report, got %+v", report)
	}
}

func TestLiveActionResetPreviewReturnsResult(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	model := newLiveTUIModel(&types.Config{}, "config.yaml")
	cmd := model.runActionCmd(liveActionResetPreview)
	if cmd == nil {
		t.Fatal("expected command for reset preview")
	}
	raw := cmd()
	msg, ok := raw.(liveActionMsg)
	if !ok {
		t.Fatalf("expected liveActionMsg, got %T", raw)
	}
	if msg.name != string(liveActionResetPreview) {
		t.Fatalf("expected action name %q, got %q", liveActionResetPreview, msg.name)
	}
	if msg.err != nil {
		t.Fatalf("expected reset preview to succeed, got %v (%s)", msg.err, msg.output)
	}
	if strings.TrimSpace(msg.output) == "" || !strings.Contains(msg.output, "\tconfig\t") {
		t.Fatalf("expected detected reset targets in output, got %s", msg.output)
	}
}

func TestLiveSavePortCmdWritesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	model := newLiveTUIModel(&types.Config{ListenPort: 9090}, cfgPath)
	model.input.SetValue("8123")
	cmd := model.savePortCmd()
	if cmd == nil {
		t.Fatal("expected save port command")
	}

	raw := cmd()
	msg, ok := raw.(liveActionMsg)
	if !ok {
		t.Fatalf("expected liveActionMsg, got %T", raw)
	}
	if msg.err != nil {
		t.Fatalf("expected save port to succeed, got %v (%s)", msg.err, msg.output)
	}
	if !strings.Contains(msg.output, "listen_port=8123 saved") || !strings.Contains(msg.output, "restart required") {
		t.Fatalf("expected save output, got %s", msg.output)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "listen_port: 8123") {
		t.Fatalf("expected config file to be updated, got %s", string(data))
	}
}

func TestRunPingCommandProducesStatus(t *testing.T) {
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}}

	code := r.Run(context.Background(), []string{"ping"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "ok\tport=") {
		t.Fatalf("expected ping output, got %s", stdout.String())
	}
}

func TestRunConfigCommandProducesJSON(t *testing.T) {
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}}

	code := r.Run(context.Background(), []string{"config"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), `"ListenPort"`) {
		t.Fatalf("expected config JSON, got %s", stdout.String())
	}
}

func TestRunTestCommandRequiresModel(t *testing.T) {
	var stderr bytes.Buffer
	r := &Runner{Stdout: &bytes.Buffer{}, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"test"})
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: ghrouter test <model>") {
		t.Fatalf("expected usage output, got %s", stderr.String())
	}
}

func TestRunVersionCommand(t *testing.T) {
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}}

	code := r.Run(context.Background(), []string{"version"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), Version) {
		t.Fatalf("expected version output, got %s", stdout.String())
	}
}

func TestRunVersionJSONCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "version"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"version"`) {
		t.Fatalf("expected JSON version output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"binary_sha256"`) {
		t.Fatalf("expected binary identity in JSON output, got %s", stdout.String())
	}
}

func TestRunUpdateJSONCommand(t *testing.T) {
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/jcafeitosa/ghrouter/releases/latest":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[{"name":"ghrouter_`+runtime.GOOS+`_`+runtime.GOARCH+`","browser_download_url":"`+baseURL+`/asset"}]}`)
		case "/asset":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "new-binary")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	baseURL = srv.URL

	t.Setenv("GHR_UPDATE_API_BASE", srv.URL)
	t.Setenv("GHR_UPDATE_REPO", "jcafeitosa/ghrouter")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"--json", "update"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"update_available"`) {
		t.Fatalf("expected update JSON output, got %s", stdout.String())
	}
}

func TestRunUpdateApplyWritesTarget(t *testing.T) {
	var baseURL string
	digest := sha256.Sum256([]byte("new-binary"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/jcafeitosa/ghrouter/releases/latest":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[{"name":"ghrouter_`+runtime.GOOS+`_`+runtime.GOARCH+`","browser_download_url":"`+baseURL+`/asset","digest":"sha256:`+hex.EncodeToString(digest[:])+`"}]}`)
		case "/asset":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "new-binary")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	baseURL = srv.URL

	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "ghrouter")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	t.Setenv("GHR_UPDATE_API_BASE", srv.URL)
	t.Setenv("GHR_UPDATE_REPO", "jcafeitosa/ghrouter")
	t.Setenv("GHR_UPDATE_TARGET", target)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"update", "--apply"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("expected updated binary content, got %q", string(data))
	}
}

func TestRunConfigFlagUsesCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "custom.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 8123\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := r.Run(context.Background(), []string{"--config", cfgPath, "config"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "8123") {
		t.Fatalf("expected custom config path to be used, got %s", stdout.String())
	}
}

func TestRunDoctorJSONCommand(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("GOOGLE_API_KEY", "test")

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"--config", cfgPath, "--json", "doctor"})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"ready"`) {
		t.Fatalf("expected JSON output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"suggestions"`) {
		t.Fatalf("expected suggestions in JSON output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"router_ready": false`) || !strings.Contains(stdout.String(), `"router_reason"`) {
		t.Fatalf("expected router readiness diagnosis, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"build"`) || !strings.Contains(stdout.String(), `"binary_sha256"`) {
		t.Fatalf("expected build identity in doctor output, got %s", stdout.String())
	}
}

func TestPrintStartupStatusReportsMissingAuthWithoutFailing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	r.printStartupStatus(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "claude-code",
				Type:    types.ProviderClaudeCode,
				Models:  []string{"claude-sonnet-5"},
				Enabled: true,
			},
		},
	})

	if !strings.Contains(stdout.String(), "startup: backend=") {
		t.Fatalf("expected startup status output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "missing Anthropic auth") {
		t.Fatalf("expected missing auth notice, got %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected partial startup status to stay out of stderr, got %s", stderr.String())
	}
}

func TestRunLiveJSONCommandProducesSnapshot(t *testing.T) {
	cfgPath := writeFixtureConfig(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}

	code := r.Run(context.Background(), []string{"--json", "live"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"listen_port"`) {
		t.Fatalf("expected live JSON output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"auth"`) {
		t.Fatalf("expected auth field in live JSON output, got %s", stdout.String())
	}
}

func TestRunProvidersJSONCommand(t *testing.T) {
	cfgPath := writeFixtureConfig(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}

	code := r.Run(context.Background(), []string{"--json", "providers"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name"`) {
		t.Fatalf("expected JSON providers output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"auth"`) {
		t.Fatalf("expected auth field in providers JSON output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"account"`) {
		t.Fatalf("expected account field in providers JSON output, got %s", stdout.String())
	}
}

func TestRunModelsJSONCommand(t *testing.T) {
	root := t.TempDir()
	cliPath := filepath.Join(root, "models-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	cfgBody := "listen_port: 0\nproviders:\n  - name: local\n    type: custom\n    cli_path: " + cliPath + "\n    models: [local/model]\n    enabled: true\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}

	code := r.Run(context.Background(), []string{"--json", "models"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"local/model"`) {
		t.Fatalf("expected JSON models output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"health"`) || !strings.Contains(stdout.String(), `"catalog_source"`) {
		t.Fatalf("expected model health and provenance in JSON output, got %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "0001-01-01T00:00:00Z") {
		t.Fatalf("unobserved cooldown timestamp leaked into JSON output: %s", stdout.String())
	}
}

func TestParseModelListFilter(t *testing.T) {
	filter, err := parseModelListFilter([]string{"--functional-only", "--provider", "nvidia", "--health=healthy", "--capability", "tool-use", "--cost", "free"})
	if err != nil {
		t.Fatalf("parse model filter: %v", err)
	}
	if !filter.functionalOnly || filter.provider != "nvidia" || filter.health != "healthy" || filter.capability != "tool-use" || filter.cost != "free" {
		t.Fatalf("unexpected model filter: %+v", filter)
	}
	if _, err := parseModelListFilter([]string{"--unknown"}); err == nil {
		t.Fatal("expected unknown model option to fail")
	}
}

func TestRunModelsFilterJSONCommand(t *testing.T) {
	root := t.TempDir()
	cliPath := filepath.Join(root, "models-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	configBody := fmt.Sprintf("providers:\n  - name: nvidia\n    type: nvidia\n    cli_path: %s\n    models: [nv/code, nv/chat]\n    model_info:\n      nv/code:\n        source: nvidia_api\n        health_status: healthy\n        verified_at: 2026-08-04T00:00:00Z\n        cost_tier: free\n        tool_use: true\n        capabilities: [code, tool-use]\n      nv/chat:\n        source: nvidia_api\n        health_status: unknown\n    enabled: true\n", cliPath)
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}
	if code := r.Run(context.Background(), []string{"--json", "models", "--functional-only", "--provider", "nvidia", "--capability", "tool-use"}); code != 0 {
		t.Fatalf("expected filtered models success, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id": "nv/code"`) || strings.Contains(stdout.String(), "nv/chat") {
		t.Fatalf("unexpected filtered models output: %s", stdout.String())
	}
}

func TestRunModelsJSONCommandReportsUnverifiedNativeModelsAsUnknown(t *testing.T) {
	root := t.TempDir()
	cliPath := filepath.Join(root, "models-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	cfgBody := "providers:\n  - name: opencode\n    type: opencode\n    cli_path: " + cliPath + "\n    models: [oc/discovered]\n    model_info:\n      oc/discovered:\n        source: native\n    enabled: true\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}

	if code := r.Run(context.Background(), []string{"--json", "models"}); code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"health": "unknown"`) {
		t.Fatalf("expected unverified native model to remain unknown, got %s", stdout.String())
	}
}

func TestRunTestJSONCommand(t *testing.T) {
	cliPath := filepath.Join(t.TempDir(), "test-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"GHROUTER_TEST_OK\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	configBody := "providers:\n  - name: fixture\n    type: custom\n    cli_path: " + cliPath + "\n    models: [fixture/model]\n    model_info:\n      fixture/model:\n        health_status: healthy\n        verified_at: 2026-08-02T00:00:00Z\n    enabled: true\n"
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}

	code := r.Run(context.Background(), []string{"--json", "test", "fixture/model"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"requested"`) {
		t.Fatalf("expected JSON test output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"ok": true`) || !strings.Contains(stdout.String(), `"status": "healthy"`) {
		t.Fatalf("expected healthy real test output, got %s", stdout.String())
	}
}

func TestRunProbeJSONCommandExecutesRealProviderAndReportsHealth(t *testing.T) {
	root := t.TempDir()
	cliPath := filepath.Join(root, "probe-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"GHROUTER_MODEL_PROBE_OK\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	configBody := fmt.Sprintf("listen_port: 0\nproviders:\n  - name: local\n    type: custom\n    cli_path: %s\n    models: [local/model]\n    enabled: true\n", cliPath)
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}
	code := r.Run(context.Background(), []string{"--json", "probe", "local/model"})
	if code != 0 {
		t.Fatalf("expected probe success, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"ok": true`) || !strings.Contains(stdout.String(), `"status": "healthy"`) {
		t.Fatalf("expected healthy real probe result, got %s", stdout.String())
	}
	updated, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "name: ghrouter/auto") || !strings.Contains(string(updated), "local/model") {
		t.Fatalf("expected successful probe to rebuild automatic lists, got %s", updated)
	}
}

func TestRunProbeRefreshesNativeCatalogBeforeRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shims are not wired for windows in this repo")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(binDir, "opencode")
	script := `#!/bin/sh
case "$1" in
  --help)
    printf 'opencode native help\n'
    ;;
  models)
    printf 'oc/new-model\n'
    ;;
  acp)
    IFS= read -r _
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}'
    ;;
  run)
    printf '%s\n' '{"text":"GHROUTER_MODEL_PROBE_OK"}'
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	configBody := fmt.Sprintf("providers:\n  - name: opencode\n    type: opencode\n    cli_path: %s\n    models: [oc/old-model]\n    enabled: true\n", cliPath)
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}
	if code := r.Run(context.Background(), []string{"--json", "probe", "new-model"}); code != 0 {
		t.Fatalf("expected refreshed native model probe to succeed, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"model": "oc/new-model"`) {
		t.Fatalf("expected native catalog model in probe result, got %s", stdout.String())
	}
}

func TestRunVerifyModelsJSONCommandChecksDiscoveredModels(t *testing.T) {
	root := t.TempDir()
	cliPath := filepath.Join(root, "verify-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"text\":\"GHROUTER_MODEL_PROBE_OK\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	configBody := fmt.Sprintf("providers:\n  - name: local\n    type: custom\n    cli_path: %s\n    models: [local/model]\n    enabled: true\n", cliPath)
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}
	if code := r.Run(context.Background(), []string{"--json", "verify-models"}); code != 0 {
		t.Fatalf("expected verification success, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "healthy"`) {
		t.Fatalf("expected per-model verification result, got %s", stdout.String())
	}
	updated, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "health_status: healthy") || !strings.Contains(string(updated), "verified_at:") {
		t.Fatalf("expected verification state persisted to config, got %s", updated)
	}
	if !strings.Contains(string(updated), "name: ghrouter/auto") || !strings.Contains(string(updated), "local/model") {
		t.Fatalf("expected verification to rebuild automatic lists, got %s", updated)
	}
}

func TestRunExplainJSONCommand(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "explain.yaml")
	configBody := "listen_port: 9090\nproviders:\n  - name: claude-code\n    type: claude-code\n    models: [cc/claude-sonnet-5]\n    model_info:\n      cc/claude-sonnet-5:\n        health_status: healthy\n        verified_at: 2026-08-02T00:00:00Z\n    enabled: true\nroutes:\n  - pattern: 'cc/*'\n    provider: claude-code\n"
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--config", cfgPath, "--json", "explain", "cc/claude-sonnet-5"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"requested"`) {
		t.Fatalf("expected JSON explain output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"selection_source"`) || !strings.Contains(stdout.String(), `"candidates"`) {
		t.Fatalf("expected decision evidence in explain output, got %s", stdout.String())
	}
}

func TestRunRoutesJSONCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "routes.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes:\n  - pattern: \"cc/*\"\n    provider: \"claude-code\"\n    fallback:\n      - \"codex\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"--config", cfgPath, "--json", "routes"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"pattern"`) {
		t.Fatalf("expected JSON routes output, got %s", stdout.String())
	}
}

func TestRunPingJSONCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "ping"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"ok"`) {
		t.Fatalf("expected JSON ping output, got %s", stdout.String())
	}
}

func TestCheckStartupUsesBootstrapper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	old := newBootstrapper
	t.Cleanup(func() { newBootstrapper = old })
	newBootstrapper = func() (*local_brain.Bootstrapper, error) {
		return &local_brain.Bootstrapper{
			Detector: fakeStartupAvailability{available: true},
			ModelManager: fakeStartupModels{
				present: false,
			},
		}, nil
	}

	err := (&Runner{}).checkStartup(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "claude-code",
				Type:    types.ProviderClaudeCode,
				Models:  []string{"claude-sonnet-5"},
				Enabled: true,
			},
		},
	})
	if err == nil {
		t.Fatal("expected startup error")
	}
}

func TestRunInitCommandWritesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("GHR_CONFIG", cfgPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader("\n"),
	}

	old := newBootstrapper
	t.Cleanup(func() { newBootstrapper = old })
	newBootstrapper = old

	code := r.Run(context.Background(), []string{"init"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected config file: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "listen_port: 9090") {
		t.Fatalf("expected listen port in config, got %s", string(data))
	}
}

func TestRunInitJSONCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json.yaml")
	t.Setenv("GHR_CONFIG", cfgPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader("\n"),
	}

	code := r.Run(context.Background(), []string{"--json", "init"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"wrote"`) {
		t.Fatalf("expected JSON init output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"detected"`) {
		t.Fatalf("expected detected providers in init JSON output, got %s", stdout.String())
	}
}

func TestRunSyncCommandUpdatesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	initial := []byte("listen_port: 7777\nproviders: []\nroutes: []\n")
	if err := os.WriteFile(cfgPath, initial, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("GHR_CONFIG", cfgPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"sync"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "listen_port: 7777") {
		t.Fatalf("expected listen port preserved, got %s", string(data))
	}
}

func TestRunSyncJSONCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	initial := []byte("listen_port: 7777\nproviders: []\nroutes: []\n")
	if err := os.WriteFile(cfgPath, initial, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("GHR_CONFIG", cfgPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "sync"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"providers"`) {
		t.Fatalf("expected JSON sync output, got %s", stdout.String())
	}
}

func TestRunBootstrapJSONCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bootstrap.yaml")
	t.Setenv("GHR_CONFIG", cfgPath)

	old := newBootstrapper
	t.Cleanup(func() { newBootstrapper = old })
	newBootstrapper = func() (*local_brain.Bootstrapper, error) {
		return &local_brain.Bootstrapper{
			Detector: testBackendAvailability{available: map[local_brain.BackendType]bool{
				local_brain.BackendLLAMACPP: true,
				local_brain.BackendMLX:      true,
			}},
			ModelManager: testModelPresence{present: map[string]bool{
				string(local_brain.BackendLLAMACPP) + "/claude-opus-5":        true,
				string(local_brain.BackendMLX) + "/anthropic/claude-sonnet-5": true,
			}},
		}, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "bootstrap"})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"synced"`) {
		t.Fatalf("expected JSON bootstrap output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"suggestions"`) {
		t.Fatalf("expected suggestions in bootstrap output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"checks"`) {
		t.Fatalf("expected checks in bootstrap output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"provision"`) {
		t.Fatalf("expected provision plan in bootstrap output, got %s", stdout.String())
	}
}

func TestRunProvisionJSONCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "provision.yaml")
	t.Setenv("GHR_CONFIG", cfgPath)
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"--json", "provision"})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"provision"`) {
		t.Fatalf("expected provision output, got %s", stdout.String())
	}
}

func TestRunProvisionApplyWritesPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	modelRoot := filepath.Join(home, ".localmodel")
	t.Setenv("GHR_LOCAL_MODEL_ROOT", modelRoot)

	cfgPath := filepath.Join(home, "provision-apply.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("GHR_CONFIG", cfgPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"provision", "--apply"})
	if code != 1 {
		t.Fatalf("expected post-check failure for unavailable detected providers, got %d (%s)", code, stderr.String())
	}
	planPath := filepath.Join(modelRoot, "provision-plan.json")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read provision plan: %v", err)
	}
	if !strings.Contains(string(data), `"action"`) {
		t.Fatalf("expected provision plan contents, got %s", string(data))
	}
	if !strings.Contains(stdout.String(), "apply\tpending") {
		t.Fatalf("expected post-check pending status, got %s", stdout.String())
	}
}

func TestRunExportAndImportBundle(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	bundlePath := filepath.Join(tmpDir, "bundle.json")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("GHR_CONFIG", cfgPath)

	var exportOut bytes.Buffer
	r := &Runner{Stdout: &exportOut, Stderr: &bytes.Buffer{}}
	code := r.Run(context.Background(), []string{"export"})
	if code != 0 {
		t.Fatalf("expected export exit code 0, got %d", code)
	}
	if err := os.WriteFile(bundlePath, exportOut.Bytes(), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	importCfgPath := filepath.Join(tmpDir, "imported.yaml")
	t.Setenv("GHR_CONFIG", importCfgPath)
	var importOut bytes.Buffer
	r = &Runner{Stdout: &importOut, Stderr: &bytes.Buffer{}}
	code = r.Run(context.Background(), []string{"import", bundlePath})
	if code != 0 {
		t.Fatalf("expected import exit code 0, got %d", code)
	}
	data, err := os.ReadFile(importCfgPath)
	if err != nil {
		t.Fatalf("read imported config: %v", err)
	}
	if !strings.Contains(string(data), "listen_port: 9090") {
		t.Fatalf("expected imported config content, got %s", string(data))
	}
}

func TestRunResetListsDetectedProviderConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("seed claude config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatalf("seed config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "codex"), []byte("settings"), 0o600); err != nil {
		t.Fatalf("seed codex config file: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"reset"})
	if code != 0 {
		t.Fatalf("expected reset exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude-code") {
		t.Fatalf("expected claude config in reset output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "codex") {
		t.Fatalf("expected codex config in reset output, got %s", stdout.String())
	}
}

func TestRunResetApplyRemovesProviderConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("seed claude config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatalf("seed config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "codex"), []byte("settings"), 0o600); err != nil {
		t.Fatalf("seed codex config file: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"reset", "--apply"})
	if code != 0 {
		t.Fatalf("expected reset apply exit code 0, got %d (%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("expected claude config dir removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "codex")); !os.IsNotExist(err) {
		t.Fatalf("expected codex config file removed, got %v", err)
	}
	backupMatches, err := filepath.Glob(filepath.Join(home, ".ghrouter", "backups", "*", "*-codex"))
	if err != nil || len(backupMatches) == 0 {
		t.Fatalf("expected codex backup, matches=%v err=%v", backupMatches, err)
	}
	if !strings.Contains(stdout.String(), "reset\tok") {
		t.Fatalf("expected reset confirmation, got %s", stdout.String())
	}
}

type testBackendAvailability struct {
	available map[local_brain.BackendType]bool
}

func (f testBackendAvailability) IsBackendAvailable(backend local_brain.BackendType) bool {
	return f.available[backend]
}

type testModelPresence struct {
	present map[string]bool
}

func (f testModelPresence) HasModel(backend local_brain.BackendType, modelID string) bool {
	return f.present[string(backend)+"/"+modelID]
}

type fakeStartupAvailability struct {
	available bool
}

func (f fakeStartupAvailability) IsBackendAvailable(backend local_brain.BackendType) bool {
	return f.available
}

type fakeStartupModels struct {
	present bool
}

func (f fakeStartupModels) HasModel(backend local_brain.BackendType, modelID string) bool {
	return f.present
}
