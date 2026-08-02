package detectors

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"ghrouter/internal/types"
)

func TestDetectAllUsesNativeHelpAndDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test shims are not wired for windows in this repo")
	}
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("MIMO_API_KEY", "mimo-key")
	t.Setenv("PI_API_KEY", "pi-key")
	t.Setenv("PI_HOME", filepath.Join(tmpDir, "pi-home"))
	t.Setenv("CURSOR_API_KEY", "cursor-key")

	writeShim(t, filepath.Join(tmpDir, "claude"), `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/claude.calls"
if [ "$1" = "--help" ]; then
  printf 'claude help without acp\n'
  exit 0
fi
exit 0
`)
	writeShim(t, filepath.Join(tmpDir, "codex"), `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/codex.calls"
if [ "$1" = "--help" ]; then
  printf 'codex help without acp\n'
  exit 0
fi
exit 0
`)
	writeShim(t, filepath.Join(tmpDir, "opencode"), `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/opencode.calls"
if [ "$1" = "--help" ]; then
  printf 'OpenCode native acp help\n'
  exit 0
fi
if [ "$1" = "models" ]; then
  printf 'opencode/nemotron-3-ultra-free\n'
  printf '{\n'
  printf '  "limit": {"context": 1000000, "output": 128000},\n'
  printf '  "capabilities": {\n'
  printf '    "reasoning": true,\n'
  printf '    "toolcall": true,\n'
  printf '    "input": {"image": false}\n'
  printf '  },\n'
  printf '  "variants": {\n'
  printf '    "high": {"reasoningEffort": "high"},\n'
  printf '    "max": {"reasoningEffort": "max"}\n'
  printf '  }\n'
  printf '}\n\n'
  exit 0
fi
if [ "$1" = "acp" ]; then
  payload="$(cat)"
  case "$payload" in
    *'"method":"initialize"'*'"protocolVersion":1'*)
      printf 'error: Method not found: initialize\n' >&2
      exit 1
      ;;
  esac
fi
exit 1
`)
	writeShim(t, filepath.Join(tmpDir, "mimo"), `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/mimo.calls"
if [ "$1" = "--help" ]; then
  printf 'Mimo native acp help\n'
  exit 0
fi
if [ "$1" = "models" ]; then
  printf 'mimo/test-model\n'
  exit 0
fi
if [ "$1" = "acp" ]; then
  payload="$(cat)"
  case "$payload" in
    *'"method":"initialize"'*'"protocolVersion":1'*)
      printf 'error: Method not found: initialize\n' >&2
      exit 1
      ;;
  esac
fi
exit 1
`)
	writeShim(t, filepath.Join(tmpDir, "pi"), `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/pi.calls"
if [ "$1" = "--help" ]; then
  printf 'pi native rpc help\n'
  exit 0
fi
if [ "$1" = "--list-models" ]; then
  printf 'provider        model                    context max-out thinking images\n'
  printf 'anthropic       claude-sonnet-5          1M      64K     yes      yes\n'
  exit 0
fi
exit 1
`)
	writeShim(t, filepath.Join(tmpDir, "cursor"), `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/cursor.calls"
if [ "$1" = "--help" ]; then
  printf 'cursor native cli help\n'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "--list-models" ]; then
  printf 'Available models\n'
  printf 'composer-2.5 - Composer\n'
  exit 0
fi
exit 1
`)

	providers, err := NewDetector().DetectAll()
	if err != nil {
		t.Fatalf("detect all: %v", err)
	}
	got := make(map[types.ProviderType]*types.Provider, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		got[provider.Type] = provider
	}
	cases := []struct {
		provider        types.ProviderType
		wantProtocol    string
		wantOrigin      string
		wantCapability  string
		wantDiscovery   types.DiscoveryStatus
		wantModels      []string
		wantEnvKeys     []string
		wantFailKeyword string
	}{
		{
			provider:        types.ProviderClaudeCode,
			wantProtocol:    "native_cli",
			wantOrigin:      "native_cli",
			wantCapability:  "unsupported",
			wantDiscovery:   types.DiscoveryUnsupported,
			wantModels:      nil,
			wantEnvKeys:     []string{"ANTHROPIC_API_KEY", "PATH"},
			wantFailKeyword: "native cli contract",
		},
		{
			provider:        types.ProviderCodex,
			wantProtocol:    "native_cli",
			wantOrigin:      "native_cli",
			wantCapability:  "unsupported",
			wantDiscovery:   types.DiscoveryUnsupported,
			wantModels:      nil,
			wantEnvKeys:     []string{"OPENAI_API_KEY", "PATH"},
			wantFailKeyword: "native cli contract",
		},
		{
			provider:        types.ProviderOpenCode,
			wantProtocol:    "native_cli",
			wantOrigin:      "native_cli",
			wantCapability:  "unsupported",
			wantDiscovery:   types.DiscoverySuccess,
			wantModels:      []string{"oc/nemotron-3-ultra-free"},
			wantEnvKeys:     []string{"OPENAI_API_KEY", "PATH"},
			wantFailKeyword: "ACP initialize handshake not confirmed",
		},
		{
			provider:        types.ProviderMimo,
			wantProtocol:    "native_cli",
			wantOrigin:      "native_cli",
			wantCapability:  "unsupported",
			wantDiscovery:   types.DiscoverySuccess,
			wantModels:      []string{"mi/test-model"},
			wantEnvKeys:     []string{"MIMO_API_KEY", "OPENAI_API_KEY", "PATH"},
			wantFailKeyword: "ACP initialize handshake not confirmed",
		},
		{
			provider:        types.ProviderPi,
			wantProtocol:    "native_rpc",
			wantOrigin:      "native_rpc",
			wantCapability:  "unsupported",
			wantDiscovery:   types.DiscoverySuccess,
			wantModels:      []string{"pi/anthropic/claude-sonnet-5"},
			wantEnvKeys:     []string{"OPENAI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY", "PI_HOME", "PATH"},
			wantFailKeyword: "native rpc contract",
		},
		{
			provider:        types.ProviderCursor,
			wantProtocol:    "native_cli",
			wantOrigin:      "native_cli",
			wantCapability:  "unsupported",
			wantDiscovery:   types.DiscoverySuccess,
			wantModels:      []string{"cu/composer-2.5"},
			wantEnvKeys:     []string{"CURSOR_API_KEY", "PATH"},
			wantFailKeyword: "native cli contract",
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider), func(t *testing.T) {
			provider := got[tc.provider]
			if provider == nil {
				t.Fatalf("expected provider %s to be detected", tc.provider)
			}
			if provider.Protocol != tc.wantProtocol {
				t.Fatalf("protocol = %q, want %q", provider.Protocol, tc.wantProtocol)
			}
			if provider.Origin != tc.wantOrigin {
				t.Fatalf("origin = %q, want %q", provider.Origin, tc.wantOrigin)
			}
			if provider.CapabilityStatus != tc.wantCapability {
				t.Fatalf("capability = %q, want %q", provider.CapabilityStatus, tc.wantCapability)
			}
			if tc.wantFailKeyword != "" && !strings.Contains(provider.FailureReason, tc.wantFailKeyword) {
				t.Fatalf("failure reason %q missing %q", provider.FailureReason, tc.wantFailKeyword)
			}
			if provider.Discovery.Status != tc.wantDiscovery {
				t.Fatalf("discovery status = %q, want %q (error=%q models=%#v)", provider.Discovery.Status, tc.wantDiscovery, provider.Discovery.Error, provider.Models)
			}
			if !reflect.DeepEqual(provider.Models, tc.wantModels) {
				t.Fatalf("models = %#v, want %#v", provider.Models, tc.wantModels)
			}
			assertAllowedEnvKeys(t, provider.Env, tc.wantEnvKeys)
		})
	}

	for _, name := range []string{"claude", "codex", "opencode", "mimo", "pi", "cursor"} {
		calls, err := os.ReadFile(filepath.Join(tmpDir, name+".calls"))
		if err != nil {
			t.Fatalf("read %s calls: %v", name, err)
		}
		if !strings.Contains(string(calls), "--help") {
			t.Fatalf("expected %s to be help-probed, got %q", name, string(calls))
		}
	}
	assertContains(t, filepath.Join(tmpDir, "opencode.calls"), "models --verbose --pure")
	assertContains(t, filepath.Join(tmpDir, "mimo.calls"), "models")
	assertContains(t, filepath.Join(tmpDir, "pi.calls"), "--list-models")
	assertContains(t, filepath.Join(tmpDir, "cursor.calls"), "agent --list-models")
}

func TestHasACPInitializeSuccessRecognizesInitializeResponse(t *testing.T) {
	if !hasACPInitializeSuccess(
		[]byte("{\"protocolVersion\":1,\"agentCapabilities\":{\"catalog\":true},\"authMethods\":[\"env\"],\"agentInfo\":{\"name\":\"opencode\"}}\n"),
		nil,
	) {
		t.Fatal("expected initialize response to be recognized")
	}
	if hasACPInitializeSuccess(nil, []byte("error: Method not found: initialize\n")) {
		t.Fatal("expected initialize error to be rejected")
	}
}

func TestEnrichProviderModelsPromotesConfiguredModelInfoIntoInventory(t *testing.T) {
	provider := &types.Provider{
		Name:   "opencode",
		Type:   types.ProviderOpenCode,
		Models: []string{"oc/from-cli", "opencode/from-name"},
		ModelInfo: map[string]types.ModelInfo{
			"oc/from-cli": {
				Source:        "native",
				VerifiedAt:    time.Unix(100, 0).UTC(),
				Thinking:      true,
				ContextWindow: 1_000_000,
			},
			"configured-only": {
				Source:            "configured",
				VerifiedAt:        time.Unix(200, 0).UTC(),
				HealthStatus:      "healthy",
				CooldownUntil:     time.Time{},
				VerificationError: "",
			},
		},
	}

	EnrichProviderModels(provider)

	if !reflect.DeepEqual(provider.Models, []string{"oc/configured-only", "oc/from-cli", "oc/from-name"}) {
		t.Fatalf("unexpected canonical models: %#v", provider.Models)
	}
	if got := provider.ModelInfo["oc/from-cli"]; got.Source != "native" || got.Model != "oc/from-cli" || got.Provider != "opencode" {
		t.Fatalf("unexpected model info for oc/from-cli: %#v", got)
	}
	if got := provider.ModelInfo["oc/configured-only"]; got.Source != "configured" || got.Model != "oc/configured-only" || got.Provider != "opencode" {
		t.Fatalf("unexpected model info for oc/configured-only: %#v", got)
	}
}

func TestDiscoveryStatusesAreTypedForUnsupportedTimeoutAndAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test shims are not wired for windows in this repo")
	}
	tmpDir := t.TempDir()

	unsupported := discoverModelsWithTimeout(filepath.Join(tmpDir, "missing"), types.ProviderClaudeCode, time.Second)
	if unsupported.status != string(types.DiscoveryUnsupported) {
		t.Fatalf("unsupported status = %q, want %q", unsupported.status, types.DiscoveryUnsupported)
	}

	authShim := filepath.Join(tmpDir, "mimo")
	writeShim(t, authShim, `#!/bin/sh
if [ "$1" = "models" ]; then
  printf 'auth failed\n' >&2
  exit 1
fi
exit 1
`)
	authResult := discoverModelsWithTimeout(authShim, types.ProviderMimo, time.Second)
	if authResult.status != string(types.DiscoveryAuth) {
		t.Fatalf("auth status = %q, want %q", authResult.status, types.DiscoveryAuth)
	}

	timeoutShim := filepath.Join(tmpDir, "pi")
	writeShim(t, timeoutShim, `#!/bin/sh
if [ "$1" = "--list-models" ]; then
  sleep 1
  exit 0
fi
exit 1
`)
	timeoutResult := discoverModelsWithTimeout(timeoutShim, types.ProviderPi, 20*time.Millisecond)
	if timeoutResult.status != string(types.DiscoveryTimeout) {
		t.Fatalf("timeout status = %q, want %q", timeoutResult.status, types.DiscoveryTimeout)
	}
}

func TestBuildAutomaticModelListsUsesEligibilityAndCanonicalIDs(t *testing.T) {
	now := time.Now()
	providers := []*types.Provider{{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		Enabled: true,
		Models: []string{
			"oc/healthy",
			"oc/failed",
			"oc/cooldown",
			"oc/expired",
			"oc/native-ready",
			"oc/native-unknown",
			"oc/configured",
			"oc/vision",
		},
		ModelInfo: map[string]types.ModelInfo{
			"oc/healthy":        {ContextWindow: 1_000_000, Thinking: true, ToolUse: true, Effort: []string{"max", "high"}},
			"oc/failed":         {HealthStatus: "failed"},
			"oc/cooldown":       {HealthStatus: "cooldown", CooldownUntil: now.Add(time.Hour)},
			"oc/expired":        {HealthStatus: "cooldown", CooldownUntil: now.Add(-time.Hour)},
			"oc/native-ready":   {Source: "native", HealthStatus: "healthy", VerifiedAt: now},
			"oc/native-unknown": {Source: "native"},
			"oc/configured":     {Source: "configured"},
			"oc/vision":         {Vision: true},
		},
	}}
	lists := BuildAutomaticModelLists(providers, []types.ModelList{
		{Name: "ghrouter/opencode", Models: []string{"oc/stale"}},
		{Name: "ghrouter/auto", Models: []string{"oc/stale"}},
	})
	got := make(map[string][]string, len(lists))
	for _, list := range lists {
		got[list.Name] = list.Models
	}
	if !reflect.DeepEqual(got["ghrouter/opencode"], []string{"oc/healthy", "oc/expired", "oc/native-ready", "oc/native-unknown", "oc/configured", "oc/vision"}) {
		t.Fatalf("unexpected provider list: %#v", got["ghrouter/opencode"])
	}
	if !reflect.DeepEqual(got["ghrouter/auto"], []string{"oc/healthy", "oc/expired", "oc/native-ready", "oc/native-unknown", "oc/configured", "oc/vision"}) {
		t.Fatalf("unexpected automatic list: %#v", got["ghrouter/auto"])
	}
	if !reflect.DeepEqual(got["ghrouter/context-1m"], []string{"oc/healthy"}) {
		t.Fatalf("unexpected context list: %#v", got["ghrouter/context-1m"])
	}
	if !reflect.DeepEqual(got["ghrouter/reasoning"], []string{"oc/healthy"}) {
		t.Fatalf("unexpected reasoning list: %#v", got["ghrouter/reasoning"])
	}
	if !reflect.DeepEqual(got["ghrouter/tool-use"], []string{"oc/healthy"}) {
		t.Fatalf("unexpected tool-use list: %#v", got["ghrouter/tool-use"])
	}
	if !reflect.DeepEqual(got["ghrouter/vision"], []string{"oc/vision"}) {
		t.Fatalf("unexpected vision list: %#v", got["ghrouter/vision"])
	}
}

func assertAllowedEnvKeys(t *testing.T, env map[string]string, wantKeys []string) {
	t.Helper()
	allowed := make(map[string]struct{}, len(wantKeys))
	for _, key := range wantKeys {
		allowed[key] = struct{}{}
	}
	for key := range env {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected env key %q in provider env %#v", key, env)
		}
	}
	for _, key := range wantKeys {
		if _, ok := env[key]; !ok {
			t.Fatalf("expected env key %q in provider env %#v", key, env)
		}
	}
}

func writeShim(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write shim %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, path, want string) {
	t.Helper()
	if !strings.Contains(readFile(t, path), want) {
		t.Fatalf("expected %s to contain %q", path, want)
	}
}
