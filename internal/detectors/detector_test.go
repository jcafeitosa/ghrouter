package detectors

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
if [ "$1" = "app-server" ]; then
  IFS= read -r initialize
  IFS= read -r initialized
  IFS= read -r model_list
  printf '%s\n' '{"id":1,"result":{"userAgent":"codex","codexHome":"/tmp/codex"}}'
  printf '%s\n' '{"id":2,"result":{"data":[{"id":"gpt-5.4","model":"gpt-5.4","hidden":false,"inputModalities":["text","image"],"supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]},{"id":"hidden","model":"hidden","hidden":true,"inputModalities":["text"],"supportedReasoningEfforts":[]}],"nextCursor":null}}'
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
if [ "$1" = "agent" ] && [ "$2" = "--trust" ] && [ "$3" = "acp" ]; then
  IFS= read -r initialize
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}}'
  IFS= read -r initialized
  case "$initialized" in
    *'"method":"initialized"'*) ;;
    *) exit 1 ;;
  esac
  sleep 0.1
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"test-session","models":{"currentModelId":"composer-2.5[]","availableModels":[{"modelId":"composer-2.5[]","name":"Composer"}]}}}'
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
			provider:       types.ProviderCodex,
			wantProtocol:   "native_app_server",
			wantOrigin:     "native_app_server",
			wantCapability: "supported",
			wantDiscovery:  types.DiscoverySuccess,
			wantModels:     []string{"cx/gpt-5.4"},
			wantEnvKeys:    []string{"OPENAI_API_KEY", "PATH"},
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
			provider:       types.ProviderCursor,
			wantProtocol:   "acp",
			wantOrigin:     "native_cli",
			wantCapability: "supported",
			wantDiscovery:  types.DiscoverySuccess,
			wantModels:     []string{"cu/composer-2.5"},
			wantEnvKeys:    []string{"CURSOR_API_KEY", "PATH"},
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

	for _, name := range []string{"claude", "codex", "opencode", "mimo", "pi"} {
		calls, err := os.ReadFile(filepath.Join(tmpDir, name+".calls"))
		if err != nil {
			t.Fatalf("read %s calls: %v", name, err)
		}
		if !strings.Contains(string(calls), "--help") {
			t.Fatalf("expected %s to be help-probed, got %q", name, string(calls))
		}
	}
	assertContains(t, filepath.Join(tmpDir, "opencode.calls"), "models --verbose --pure")
	assertContains(t, filepath.Join(tmpDir, "codex.calls"), "app-server --stdio")
	if strings.Contains(strings.Join(got[types.ProviderOpenCode].Args, " "), "--no-remote") {
		t.Fatalf("OpenCode args include unsupported --no-remote flag: %#v", got[types.ProviderOpenCode].Args)
	}
	if !strings.Contains(strings.Join(got[types.ProviderClaudeCode].Args, " "), "--verbose") {
		t.Fatalf("Claude stream-json args omit required --verbose flag: %#v", got[types.ProviderClaudeCode].Args)
	}
	if !strings.Contains(strings.Join(got[types.ProviderOpenCode].Args, " "), "run --format json --pure") {
		t.Fatalf("OpenCode args do not match the installed CLI contract: %#v", got[types.ProviderOpenCode].Args)
	}
	assertContains(t, filepath.Join(tmpDir, "mimo.calls"), "models")
	assertContains(t, filepath.Join(tmpDir, "pi.calls"), "--list-models")
	if !got[types.ProviderOpenCode].Harness.Observed() || !got[types.ProviderOpenCode].Harness.AdvertisesACP {
		t.Fatalf("expected observed OpenCode harness capabilities: %+v", got[types.ProviderOpenCode].Harness)
	}
	if !containsString(got[types.ProviderPi].Harness.SlashCommands, "/model") {
		t.Fatalf("expected Pi slash command inventory: %+v", got[types.ProviderPi].Harness)
	}
	cursorCalls, err := os.ReadFile(filepath.Join(tmpDir, "cursor.calls"))
	if err != nil {
		t.Fatalf("read cursor calls: %v", err)
	}
	if !strings.Contains(string(cursorCalls), "agent --trust acp") {
		t.Fatalf("expected cursor ACP probe, got %q", cursorCalls)
	}
	if strings.Contains(string(cursorCalls), "--list-models") {
		t.Fatalf("expected cursor native model listing to stay disabled")
	}
	if !strings.Contains(string(cursorCalls), "agent --trust acp") {
		t.Fatalf("expected cursor ACP catalog discovery, got %q", cursorCalls)
	}
}

func TestDetectConfiguredNVIDIAUsesOnlyOperatorModelIDs(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "nvidia-test-key")
	t.Setenv("GHR_NVIDIA_MODELS", "meta/llama-3.1-8b-instruct,nv/mistralai/mixtral-8x7b-instruct,meta/llama-3.1-8b-instruct")
	provider := detectConfiguredNVIDIA()
	if provider == nil {
		t.Fatal("expected NVIDIA provider from explicit credentials and model list")
	}
	if len(provider.Models) != 2 || provider.Models[0] != "nv/meta/llama-3.1-8b-instruct" || provider.Models[1] != "nv/mistralai/mixtral-8x7b-instruct" {
		t.Fatalf("unexpected NVIDIA models: %#v", provider.Models)
	}
	t.Setenv("GHR_NVIDIA_MODELS", "")
	if detectConfiguredNVIDIA() != nil {
		t.Fatal("NVIDIA provider must not be fabricated without explicit model IDs")
	}
}

func TestClassifyNVIDIAModelPreservesUnverifiedKindAndModalities(t *testing.T) {
	info := classifyNVIDIAModel("deepseek-ai/deepseek-coder-6.7b-instruct")
	if info.Source != "nvidia_api" || info.Kind != "coding" || len(info.Modalities) != 1 || info.Modalities[0] != "text" {
		t.Fatalf("unexpected NVIDIA classification: %+v", info)
	}
	if !info.ToolUse || !info.VerifiedAt.IsZero() {
		t.Fatalf("inferred classification must not imply verification: %+v", info)
	}
}

func TestDiscoveryEnvForPiPreservesNativeConfigDirectory(t *testing.T) {
	piDir := filepath.Join(t.TempDir(), "pi-native")
	t.Setenv("PI_CODING_AGENT_DIR", piDir)

	env := discoveryEnvForProvider(types.ProviderPi)
	for _, entry := range env {
		if entry == "PI_CODING_AGENT_DIR="+piDir {
			return
		}
	}
	t.Fatalf("PI_CODING_AGENT_DIR was not preserved in discovery environment: %v", env)
}

func TestRunACPInitializeStopsDescendantsThatKeepPipesOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are not wired for windows in this repo")
	}
	path := filepath.Join(t.TempDir(), "acp")
	writeShim(t, path, `#!/bin/sh
/usr/bin/perl -e 'my $pid = fork(); if (!$pid) { sleep 2; exit 0; } exit 0;'
printf 'error: Method not found: initialize\n' >&2
exit 0
`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd := exec.Command(path, "acp")
	prepareDiscoveryCommand(cmd)
	started := time.Now()
	_, _, _ = runACPInitialize(ctx, cmd)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("ACP probe waited for a descendant pipe holder: %s", elapsed)
	}
}

func TestWaitDiscoveryProcessIsBounded(t *testing.T) {
	waitCh := make(chan error)
	started := time.Now()
	err, completed := waitDiscoveryProcess(waitCh)
	if completed {
		t.Fatal("expected an incomplete process wait")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("bounded process wait took too long: %s", elapsed)
	}
}

func TestRunACPInitializeWithCapabilitiesKeepsCursorStdinOpen(t *testing.T) {
	if os.Getenv("GO_WANT_ACP_HELPER_PROCESS") == "1" {
		reader := bufio.NewReader(os.Stdin)
		if _, err := reader.ReadString('\n'); err != nil {
			os.Exit(0)
		}
		eof := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, reader)
			close(eof)
		}()
		select {
		case <-eof:
			os.Exit(0)
		case <-time.After(100 * time.Millisecond):
			_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`)
			os.Exit(0)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestRunACPInitializeWithCapabilitiesKeepsCursorStdinOpen")
	cmd.Env = append(os.Environ(), "GO_WANT_ACP_HELPER_PROCESS=1")
	prepareDiscoveryCommand(cmd)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stdout, stderr, err := runACPInitializeWithCapabilities(ctx, cmd, true)
	if err != nil {
		t.Fatalf("run Cursor ACP initialize: %v (stdout=%q stderr=%q)", err, stderr, stdout)
	}
	if !hasACPInitializeSuccess(stdout, stderr) {
		t.Fatalf("expected initialize response after keeping stdin open, got stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestHasACPInitializeSuccessRecognizesInitializeResponse(t *testing.T) {
	if hasACPInitializeSuccess([]byte("{\"protocolVersion\":1}\n"), nil) {
		t.Fatal("top-level protocolVersion must not confirm ACP")
	}
	if hasACPInitializeSuccess(nil, []byte("error: Method not found: initialize\n")) {
		t.Fatal("expected initialize error to be rejected")
	}
	if !hasACPInitializeSuccess(
		[]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{},\"authMethods\":[],\"agentInfo\":{\"name\":\"opencode\"}}}\n"),
		nil,
	) {
		t.Fatal("expected JSON-RPC initialize response to be recognized")
	}
	if hasACPInitializeSuccess([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32601,\"message\":\"Method not found: initialize\"}}\n"), nil) {
		t.Fatal("ACP error response must not confirm handshake")
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

func TestBuildAutomaticModelListsExcludesUnverifiedModels(t *testing.T) {
	provider := &types.Provider{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		Models:  []string{"oc/unverified", "oc/verified"},
		Enabled: true,
		ModelInfo: map[string]types.ModelInfo{
			"oc/unverified": {Source: "native"},
			"oc/verified": {
				Source:       "native",
				HealthStatus: "healthy",
				VerifiedAt:   time.Unix(300, 0).UTC(),
			},
		},
	}

	lists := BuildAutomaticModelLists([]*types.Provider{provider}, nil)
	for _, list := range lists {
		if list.Name != "ghrouter/auto" {
			continue
		}
		if !reflect.DeepEqual(list.Models, []string{"oc/verified"}) {
			t.Fatalf("expected only verified model in automatic list, got %#v", list.Models)
		}
		return
	}
	t.Fatal("expected ghrouter/auto list")
}

func TestBuildAutomaticModelListsExcludesUnverifiedNativeModelsWithoutMetadata(t *testing.T) {
	provider := &types.Provider{
		Name:    "mimo",
		Type:    types.ProviderMimo,
		Models:  []string{"mi/configured-only"},
		Enabled: true,
	}

	lists := BuildAutomaticModelLists([]*types.Provider{provider}, []types.ModelList{
		{Name: "ghrouter/mimo", Kind: "provider", Models: []string{"mi/stale"}},
		{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"mi/stale"}},
	})
	for _, list := range lists {
		if list.Name != "ghrouter/mimo" && list.Name != "ghrouter/auto" {
			continue
		}
		if len(list.Models) != 0 {
			t.Fatalf("unverified native model leaked into %s: %#v", list.Name, list.Models)
		}
	}
}

func TestBuildAutomaticModelListsAllowsUnannotatedCustomModels(t *testing.T) {
	provider := &types.Provider{
		Name:    "custom",
		Type:    types.ProviderCustom,
		Models:  []string{"model"},
		Enabled: true,
	}

	lists := BuildAutomaticModelLists([]*types.Provider{provider}, nil)
	for _, list := range lists {
		if list.Name == "ghrouter/auto" && reflect.DeepEqual(list.Models, []string{"model"}) {
			return
		}
	}
	t.Fatalf("expected unannotated custom model in automatic list, got %#v", lists)
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

func TestDetectAllKeepsNativeListingThatFinishesAfterTwoSeconds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test shims are not wired for windows in this repo")
	}
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)
	writeShim(t, filepath.Join(tmpDir, "claude"), `#!/bin/sh
if [ "$1" = "--help" ]; then printf 'claude help\n'; fi
`)
	writeShim(t, filepath.Join(tmpDir, "codex"), `#!/bin/sh
if [ "$1" = "--help" ]; then printf 'codex help\n'; exit 0; fi
if [ "$1" = "app-server" ]; then
  IFS= read -r initialize
  IFS= read -r initialized
  IFS= read -r model_list
  printf '%s\n' '{"id":1,"result":{"data":[{"model":"gpt-5.4"}],"nextCursor":null}}'
fi
`)
	writeShim(t, filepath.Join(tmpDir, "opencode"), `#!/bin/sh
if [ "$1" = "--help" ]; then printf 'opencode acp help\n'; exit 0; fi
if [ "$1" = "acp" ]; then
  cat >/dev/null
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}'
  exit 0
fi
if [ "$1" = "models" ]; then
  /bin/sleep 3
  printf 'opencode/slow-but-real\n'
fi
`)
	writeShim(t, filepath.Join(tmpDir, "mimo"), `#!/bin/sh
if [ "$1" = "--help" ]; then printf 'mimo help\n'; exit 0; fi
if [ "$1" = "models" ]; then printf 'mimo/fast\n'; exit 0; fi
if [ "$1" = "acp" ]; then cat >/dev/null; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}'; fi
`)
	writeShim(t, filepath.Join(tmpDir, "pi"), `#!/bin/sh
if [ "$1" = "--help" ]; then printf 'pi help\n'; exit 0; fi
if [ "$1" = "--list-models" ]; then printf 'provider model context max-out thinking images\nanthropic claude-sonnet-5 1M 64K yes yes\n'; fi
`)

	providers, err := NewDetector().DetectAll()
	if err != nil {
		t.Fatalf("detect all: %v", err)
	}
	for _, provider := range providers {
		if provider.Type != types.ProviderOpenCode {
			continue
		}
		if provider.Discovery.Status != types.DiscoverySuccess {
			t.Fatalf("opencode discovery status = %q, want %q (error=%q)", provider.Discovery.Status, types.DiscoverySuccess, provider.Discovery.Error)
		}
		if !reflect.DeepEqual(provider.Models, []string{"oc/slow-but-real"}) {
			t.Fatalf("opencode models = %#v, want slow native model", provider.Models)
		}
		return
	}
	t.Fatal("opencode provider was not detected")
}

func TestDetectAllCachesRecentDiscoveryAndFreshBypassesCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test shims are not wired for windows in this repo")
	}
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)
	writeShim(t, filepath.Join(tmpDir, "claude"), "#!/bin/sh\ndir=${0%/*}\nprintf x >> \"$dir/claude.calls\"\nexit 0\n")

	if _, err := NewDetector().DetectAll(); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(tmpDir, "claude.calls"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDetector().DetectAll(); err != nil {
		t.Fatalf("cached discovery: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(tmpDir, "claude.calls"))
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Fatalf("cached discovery executed CLI again: before=%q after=%q", first, second)
	}
	if _, err := NewDetector().DetectAllFresh(); err != nil {
		t.Fatalf("fresh discovery: %v", err)
	}
	fresh, err := os.ReadFile(filepath.Join(tmpDir, "claude.calls"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) <= len(second) {
		t.Fatalf("fresh discovery did not bypass cache: before=%d after=%d", len(second), len(fresh))
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
	if !reflect.DeepEqual(got["ghrouter/opencode"], []string{"oc/native-ready"}) {
		t.Fatalf("unexpected provider list: %#v", got["ghrouter/opencode"])
	}
	if !reflect.DeepEqual(got["ghrouter/auto"], []string{"oc/native-ready"}) {
		t.Fatalf("unexpected automatic list: %#v", got["ghrouter/auto"])
	}
	if got["ghrouter/context-1m"] != nil {
		t.Fatalf("unexpected context list: %#v", got["ghrouter/context-1m"])
	}
	if got["ghrouter/reasoning"] != nil {
		t.Fatalf("unexpected reasoning list: %#v", got["ghrouter/reasoning"])
	}
	if got["ghrouter/tool-use"] != nil {
		t.Fatalf("unexpected tool-use list: %#v", got["ghrouter/tool-use"])
	}
	if got["ghrouter/vision"] != nil {
		t.Fatalf("unexpected vision list: %#v", got["ghrouter/vision"])
	}
}

func TestBuildAutomaticModelListsRefreshesExistingCapabilityLists(t *testing.T) {
	now := time.Now()
	providers := []*types.Provider{{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		Enabled: true,
		Models:  []string{"oc/new-vision"},
		ModelInfo: map[string]types.ModelInfo{
			"oc/new-vision": {Source: "native", VerifiedAt: now, HealthStatus: "healthy", Vision: true},
		},
	}}
	lists := BuildAutomaticModelLists(providers, []types.ModelList{{
		Name:   "ghrouter/vision",
		Kind:   "automatic",
		Models: []string{"oc/stale-vision"},
	}})
	for _, list := range lists {
		if list.Name == "ghrouter/vision" {
			if !reflect.DeepEqual(list.Models, []string{"oc/new-vision"}) {
				t.Fatalf("existing capability list was not refreshed: %#v", list.Models)
			}
			return
		}
	}
	t.Fatal("expected ghrouter/vision list")
}

func TestBuildAutomaticModelListsUsesStableOrdering(t *testing.T) {
	now := time.Now().UTC()
	providers := []*types.Provider{
		{
			Name:    "zeta",
			Type:    types.ProviderOpenCode,
			Enabled: true,
			Models:  []string{"oc/z", "oc/a"},
			ModelInfo: map[string]types.ModelInfo{
				"oc/z": {Source: "native", HealthStatus: "healthy", VerifiedAt: now},
				"oc/a": {Source: "native", HealthStatus: "healthy", VerifiedAt: now},
			},
		},
		{
			Name:    "alpha",
			Type:    types.ProviderOpenCode,
			Enabled: true,
			Models:  []string{"oc/y", "oc/b"},
			ModelInfo: map[string]types.ModelInfo{
				"oc/y": {Source: "native", HealthStatus: "healthy", VerifiedAt: now},
				"oc/b": {Source: "native", HealthStatus: "healthy", VerifiedAt: now},
			},
		},
	}

	lists := BuildAutomaticModelLists(providers, nil)
	if got := []string{lists[0].Name, lists[1].Name, lists[2].Name}; !reflect.DeepEqual(got, []string{"ghrouter/alpha", "ghrouter/zeta", "ghrouter/auto"}) {
		t.Fatalf("unexpected generated list order: %#v", got)
	}
	if !reflect.DeepEqual(lists[0].Models, []string{"oc/b", "oc/y"}) {
		t.Fatalf("unexpected alpha member order: %#v", lists[0].Models)
	}
	if !reflect.DeepEqual(lists[2].Models, []string{"oc/a", "oc/b", "oc/y", "oc/z"}) {
		t.Fatalf("unexpected automatic member order: %#v", lists[2].Models)
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
