package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ghrouter/internal/observability"
	"ghrouter/internal/types"
)

func TestLocalHTTPProviderRoutesChatCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Fatalf("unexpected local endpoint: %s %s", r.Method, r.URL.Path)
		}
		var payload types.OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode local request: %v", err)
		}
		if got := payload.ChatTemplateKwargs["enable_thinking"]; got != false {
			t.Fatalf("expected no-think option to reach local runtime, got %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"local-1","model":"qwen","choices":[{"message":{"role":"assistant","content":"local answer"}}]}`))
	}))
	defer server.Close()

	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Enabled: true})
	events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}, ChatTemplateKwargs: map[string]any{"enable_thinking": false}})
	var output string
	for events != nil || errorsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				output += event.Delta
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil {
				t.Fatalf("local provider request failed: %v", err)
			}
		}
	}
	if output != "local answer" {
		t.Fatalf("expected local response, got %q", output)
	}
}

func TestMarkHealthyUpdatesRunnerAvailability(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, Models: []string{"model"}, Enabled: true})
	runner.MarkHealthy(125 * time.Millisecond)
	state := runner.GetHealth()
	if state.Status != "healthy" || !state.Available || state.Latency != 125*time.Millisecond || state.LastCheck.IsZero() {
		t.Fatalf("expected marked healthy runner, got %+v", state)
	}
}

func TestLocalHTTPProviderReportsCapacityRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Enabled: true})
	_, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen"})
	err := <-errorsCh
	var capacityErr *CapacityError
	if !errors.As(err, &capacityErr) {
		t.Fatalf("expected typed capacity error, got %v", err)
	}
	if capacityErr.StatusCode != http.StatusTooManyRequests || capacityErr.RetryAfter != 120*time.Second {
		t.Fatalf("unexpected capacity evidence: %+v", capacityErr)
	}
}

func TestBuildPromptIgnoresMalformedContentParts(t *testing.T) {
	runner := &ProviderRunner{}
	req := &types.OpenAIRequest{Messages: []types.OpenAIMessage{{
		Role: "user",
		Content: []interface{}{
			map[string]interface{}{"type": "text", "text": "valid"},
			map[string]interface{}{"type": "text", "text": 42},
			map[string]interface{}{"type": "text"},
		},
	}}}

	got := runner.buildPrompt(req)
	if !strings.Contains(got, "valid") {
		t.Fatalf("expected valid text part to survive, got %q", got)
	}
}

func TestLocalHTTPProviderDefaultsToNoThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload types.OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode local request: %v", err)
		}
		if got := payload.ChatTemplateKwargs["enable_thinking"]; got != false {
			t.Fatalf("expected local requests to disable thinking by default, got %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"local answer"}}]}`))
	}))
	defer server.Close()

	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Enabled: true})
	events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen"})
	for events != nil || errorsCh != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil {
				t.Fatalf("local provider request failed: %v", err)
			}
		}
	}
}

func TestLocalHTTPProviderPreservesToolCallsWithoutContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload types.OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode local request: %v", err)
		}
		if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "list_files" {
			t.Fatalf("expected tool schema to reach local runtime, got %+v", payload.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_files","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()

	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Enabled: true})
	tools := []types.OpenAITool{{Type: "function", Function: types.OpenAIToolFunc{Name: "list_files"}}}
	events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen", Tools: tools})
	var calls []types.OpenAIToolCall
	for events != nil || errorsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				calls = append(calls, event.ToolCalls...)
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil {
				t.Fatalf("local tool request failed: %v", err)
			}
		}
	}
	if len(calls) != 1 || calls[0].Function.Name != "list_files" {
		t.Fatalf("expected one local tool call, got %+v", calls)
	}
}

func TestNVIDIAHTTPProviderUsesExplicitModelAndCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer nvidia-test-key" {
			t.Fatalf("unexpected NVIDIA request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var payload types.OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode NVIDIA request: %v", err)
		}
		if payload.Model != "meta/llama-3.1-8b-instruct" {
			t.Fatalf("expected upstream NVIDIA model without router prefix, got %q", payload.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"nvidia answer"}}]}`))
	}))
	defer server.Close()

	provider := &types.Provider{
		Name:       "nvidia",
		Type:       types.ProviderNVIDIA,
		BaseURL:    server.URL,
		Models:     []string{"nv/meta/llama-3.1-8b-instruct"},
		AuthConfig: map[string]string{"api_key": "nvidia-test-key"},
		Enabled:    true,
	}
	runner := NewProviderRunner(provider)
	events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: provider.Models[0], Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}})
	var output string
	for events != nil || errorsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				output += event.Delta
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil {
				t.Fatalf("NVIDIA provider request failed: %v", err)
			}
		}
	}
	if output != "nvidia answer" {
		t.Fatalf("expected NVIDIA response, got %q", output)
	}
}

func TestNVIDIAAPIKeyUsesConfiguredEnvironmentName(t *testing.T) {
	t.Setenv("TEAM_NVIDIA_KEY", "configured-env-key")
	provider := &types.Provider{Type: types.ProviderNVIDIA, AuthConfig: map[string]string{"api_key_env": "TEAM_NVIDIA_KEY"}}
	if got := nvidiaAPIKey(provider); got != "configured-env-key" {
		t.Fatalf("configured NVIDIA key = %q, want environment value", got)
	}
}

func TestNVIDIAAPIKeyRotatesEnabledAccounts(t *testing.T) {
	t.Setenv("NVIDIA_TEAM_A", "key-a")
	t.Setenv("NVIDIA_TEAM_B", "key-b")
	provider := &types.Provider{
		Name: "nvidia-pool",
		Type: types.ProviderNVIDIA,
		Accounts: []types.ProviderCredential{
			{Name: "team-a", APIKeyEnv: "NVIDIA_TEAM_A", Enabled: true},
			{Name: "team-b", APIKeyEnv: "NVIDIA_TEAM_B", Enabled: true},
		},
	}
	first := nvidiaAPIKey(provider)
	second := nvidiaAPIKey(provider)
	if first == second || (first != "key-a" && first != "key-b") || (second != "key-a" && second != "key-b") {
		t.Fatalf("expected alternating NVIDIA credentials, got %q then %q", first, second)
	}
}

func TestLocalHTTPProviderRoutesSSEStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"local-stream\",\"choices\":[{\"delta\":{\"content\":\"streamed\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	stream := true
	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Enabled: true})
	events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen", Stream: &stream})
	var output string
	for events != nil || errorsCh != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				output += event.Delta
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil {
				t.Fatalf("local stream failed: %v", err)
			}
		}
	}
	if output != "streamed" {
		t.Fatalf("expected streamed local response, got %q", output)
	}
}

func TestLocalHTTPProviderHonorsProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	}))
	defer server.Close()

	runner := NewProviderRunner(&types.Provider{Name: "local", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Timeout: 10 * time.Millisecond})
	events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen"})
	var gotErr error
	for events != nil || errorsCh != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			if err != nil {
				gotErr = err
			}
		}
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("expected provider timeout, got %v", gotErr)
	}
}

func TestHTTPProviderUsesBoundedTransportTimeout(t *testing.T) {
	if got := httpProviderTimeout(&types.Provider{Type: types.ProviderLocal, Timeout: 5 * time.Minute}); got != 5*time.Minute {
		t.Fatalf("expected configured local HTTP timeout, got %s", got)
	}
	if got := httpProviderTimeout(&types.Provider{Timeout: 250 * time.Millisecond}); got != 250*time.Millisecond {
		t.Fatalf("expected explicit shorter provider timeout, got %s", got)
	}
	if got := httpProviderTimeout(nil); got != defaultRemoteHTTPProviderTimeout {
		t.Fatalf("expected default remote HTTP timeout for nil provider, got %s", got)
	}
}

func TestLocalHTTPProviderSerializesConcurrentModelSwitchRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRequest := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRequest()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			t.Errorf("expected local model-switch requests to be serialized")
			return
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"first"}}]}`))
	}))
	defer server.Close()

	runner := NewProviderRunner(&types.Provider{Name: "local-a", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"qwen"}, Timeout: time.Second})
	secondRunner := NewProviderRunner(&types.Provider{Name: "local-b", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"gemma"}, Timeout: time.Second})
	firstEvents, firstErrors := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "qwen"})
	<-started
	secondCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, secondErrors := secondRunner.Invoke(secondCtx, &types.OpenAIRequest{Model: "gemma"})
	var secondErr error
	for err := range secondErrors {
		if err != nil {
			secondErr = err
		}
	}
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("expected queued local request to honor cancellation, got %v", secondErr)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one in-flight local request, got %d", got)
	}
	releaseRequest()
	for range firstEvents {
	}
	for range firstErrors {
	}
}

func TestBuildCLIArgsUsesOfficialModelFlagPerProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider types.ProviderType
		wantFlag string
	}{
		{name: "claude", provider: types.ProviderClaudeCode, wantFlag: "--model"},
		{name: "codex", provider: types.ProviderCodex, wantFlag: "--model"},
		{name: "opencode", provider: types.ProviderOpenCode, wantFlag: "--model"},
		{name: "mimo", provider: types.ProviderMimo, wantFlag: "--model"},
		{name: "pi", provider: types.ProviderPi, wantFlag: "--model"},
		{name: "cursor", provider: types.ProviderCursor, wantFlag: "--model"},
		{name: "custom", provider: types.ProviderCustom, wantFlag: "-m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requested := "cc/model"
			wantModel := "model"
			if tt.provider == types.ProviderOpenCode {
				requested = "oc/model"
				wantModel = "opencode/model"
			}
			args := buildCLIArgs(&types.Provider{Type: tt.provider, Args: []string{"--print"}}, requested)
			if len(args) != 3 || args[1] != tt.wantFlag || args[2] != wantModel {
				t.Fatalf("expected %q model flag, got %v", tt.wantFlag, args)
			}
		})
	}
}

func TestOpenCodeNativeModelKeepsCLIProviderNamespace(t *testing.T) {
	args := buildCLIArgs(&types.Provider{Type: types.ProviderOpenCode, Args: []string{"run"}}, "oc/big-pickle")
	if want := []string{"run", "--model", "opencode/big-pickle"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("expected OpenCode native model namespace, got %v", args)
	}

	args = buildCLIArgs(&types.Provider{Type: types.ProviderOpenCode, Args: []string{"run"}}, "oc/github-copilot/gpt-5-mini")
	if want := []string{"run", "--model", "github-copilot/gpt-5-mini"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("expected external OpenCode provider namespace, got %v", args)
	}
}

func TestProviderLogsCarryInternalRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'ok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	closeLogs, err := observability.Configure(&logs, observability.Settings{Level: "debug", Format: "text", Output: "stderr", Color: "never"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogs()
	runner := NewProviderRunner(&types.Provider{Name: "custom", Type: types.ProviderCustom, CLIPath: path, Enabled: true})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{RequestID: "req-provider-log", Model: "model"})
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		}
	}
	if !strings.Contains(logs.String(), "request_id=req-provider-log") {
		t.Fatalf("expected provider logs to carry request id, got %q", logs.String())
	}
}

func TestProviderAdaptersExposeNativeInvocationContracts(t *testing.T) {
	for _, provider := range []struct {
		typeValue    types.ProviderType
		name         string
		promptOnArgs bool
	}{
		{typeValue: types.ProviderClaudeCode, name: "claude-code", promptOnArgs: true},
		{typeValue: types.ProviderCodex, name: "codex", promptOnArgs: true},
		{typeValue: types.ProviderOpenCode, name: "opencode", promptOnArgs: true},
		{typeValue: types.ProviderMimo, name: "mimo", promptOnArgs: true},
		{typeValue: types.ProviderPi, name: "pi", promptOnArgs: true},
		{typeValue: types.ProviderCursor, name: "cursor", promptOnArgs: true},
	} {
		adapter := adapterFor(provider.typeValue)
		if adapter.Name() != provider.name || adapter.PromptOnArgs() != provider.promptOnArgs {
			t.Fatalf("unexpected adapter contract for %s: %s/%v", provider.typeValue, adapter.Name(), adapter.PromptOnArgs())
		}
	}
}

func TestProviderAdaptersPropagateReasoningEffort(t *testing.T) {
	tests := []struct {
		providerType types.ProviderType
		want         []string
	}{
		{types.ProviderClaudeCode, []string{"--effort", "high"}},
		{types.ProviderCodex, []string{"-c", "model_reasoning_effort=high"}},
		{types.ProviderOpenCode, []string{"--variant", "high"}},
		{types.ProviderMimo, []string{"--variant", "high"}},
		{types.ProviderPi, []string{"--thinking", "high"}},
		{types.ProviderCursor, []string{"--reasoning-effort", "high"}},
	}
	for _, test := range tests {
		args := adapterFor(test.providerType).BuildArgs(&types.Provider{Type: test.providerType}, "model", "high")
		for _, wanted := range test.want {
			found := false
			for _, arg := range args {
				if arg == wanted {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s args %v missing %q", test.providerType, args, wanted)
			}
		}
	}
}

func TestProviderAdaptersHonorObservedHarnessCapabilities(t *testing.T) {
	observed := time.Now().UTC()
	provider := &types.Provider{
		Type: types.ProviderOpenCode,
		Harness: types.HarnessCapabilities{
			ObservedAt:          observed,
			SupportsModelSelect: true,
			SupportsThinking:    false,
			SupportsEffort:      false,
		},
	}
	args := adapterFor(provider.Type).BuildArgs(provider, "oc/model", "high")
	if !hasModelFlag(args) {
		t.Fatalf("observed model-selection capability should add model: %v", args)
	}
	if containsArg(args, "--variant") || containsArg(args, "--thinking") {
		t.Fatalf("unsupported reasoning capability leaked into args: %v", args)
	}

	provider.Harness.SupportsModelSelect = false
	args = adapterFor(provider.Type).BuildArgs(provider, "oc/model", "")
	if hasModelFlag(args) {
		t.Fatalf("observed unsupported model selection should not add model: %v", args)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestProviderRunnerPassesNativePromptAsArgument(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args")
	cliPath := filepath.Join(tmpDir, "opencode")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nif IFS= read -r line; then printf '%%s\\n' 'stdin-not-empty' >> %q; else printf '%%s\\n' 'stdin-empty' >> %q; fi\nprintf '%%s\\n' '{\"text\":\"native prompt ok\"}'\n", argsPath, argsPath, argsPath)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		CLIPath: cliPath,
		Args:    []string{"run", "--format", "json", "--pure"},
		Models:  []string{"oc/model"},
		WorkDir: tmpDir,
	})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{
		Model:    "oc/model",
		Messages: []types.OpenAIMessage{{Role: "user", Content: "hello native cli"}},
	})
	var output string
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				output += event.Delta
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("native provider invocation failed: %v", err)
			}
		}
	}
	if output != "native prompt ok" {
		t.Fatalf("expected native output, got %q", output)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--model\nopencode/model\n") || !strings.Contains(string(args), "user: hello native cli") {
		t.Fatalf("expected model and positional prompt arguments, got %q", args)
	}
	if !strings.Contains(string(args), "stdin-empty") {
		t.Fatalf("expected native CLI stdin to be closed, got %q", args)
	}
}

func TestNativeProviderTimeoutClosesDetachedDescendantPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setsid is not available on Windows")
	}
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skipf("perl is unavailable: %v", err)
	}

	cliPath := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\nperl -MPOSIX -e 'POSIX::setsid(); sleep 2' &\nprintf '%s\\n' '{\"text\":\"started\"}'\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := NewProviderRunner(&types.Provider{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		CLIPath: cliPath,
		Timeout: 50 * time.Millisecond,
	})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	finished := make(chan error, 1)
	go func() {
		var gotErr error
		for events != nil || errs != nil {
			select {
			case _, ok := <-events:
				if !ok {
					events = nil
				}
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err != nil {
					gotErr = err
				}
			}
		}
		finished <- gotErr
	}()

	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected native provider timeout, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native provider did not return after detached descendant kept stdout open")
	}
}

func TestEmitStreamEventStopsWhenConsumerIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan *StreamEvent, 1)
	events <- &StreamEvent{Delta: "already buffered"}
	cancel()

	if emitStreamEvent(ctx, events, &StreamEvent{Delta: "blocked"}) {
		t.Fatal("expected canceled event delivery to stop")
	}
}

func TestGenericACPProviderRoutesPromptAndModel(t *testing.T) {
	for _, tc := range []struct {
		name         string
		providerType types.ProviderType
		requested    string
		native       string
	}{
		{name: "opencode", providerType: types.ProviderOpenCode, requested: "oc/big-pickle", native: "opencode/big-pickle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			callsPath := filepath.Join(tmpDir, "calls")
			cliPath := filepath.Join(tmpDir, tc.name)
			script := fmt.Sprintf("#!/bin/sh\ninitialized=0\nwhile IFS= read -r line; do\n  printf '%%s\\n' \"$line\" >> %q\n  if printf '%%s' \"$line\" | grep -q '\"method\":\"initialize\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1,\"authMethods\":[]}}'\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"initialized\"'; then\n    initialized=1\n  elif [ \"$initialized\" = 1 ] && printf '%%s' \"$line\" | grep -q '\"method\":\"session/new\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"sessionId\":\"session-1\",\"configOptions\":[{\"id\":\"model\",\"currentValue\":\"%s\",\"options\":[{\"value\":\"%s\"}]}]}}'\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"session/set_config_option\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"configOptions\":[]}}'\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"session/prompt\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"session-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"GENERIC_ACP_OK\"}}}}'\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"stopReason\":\"end_turn\"}}'\n    exit 0\n  fi\ndone\n", callsPath, tc.native, tc.native)
			if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			runner := NewProviderRunner(&types.Provider{
				Name:     tc.name,
				Type:     tc.providerType,
				Protocol: "acp",
				CLIPath:  cliPath,
				Args:     []string{"run", "--format", "json", "--pure"},
				WorkDir:  tmpDir,
				Timeout:  2 * time.Second,
				Enabled:  true,
			})
			events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{
				Model:    tc.requested,
				Messages: []types.OpenAIMessage{{Role: "user", Content: "hello ACP"}},
			})
			var output string
			for events != nil || errs != nil {
				select {
				case event, ok := <-events:
					if !ok {
						events = nil
						continue
					}
					if event != nil {
						output += event.Delta
					}
				case err, ok := <-errs:
					if !ok {
						errs = nil
						continue
					}
					if err != nil {
						t.Fatalf("%s ACP request failed: %v", tc.name, err)
					}
				}
			}
			if output != "GENERIC_ACP_OK" {
				t.Fatalf("expected generic ACP output, got %q", output)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), `"method":"session/set_config_option"`) || !strings.Contains(string(calls), fmt.Sprintf(`"value":"%s"`, tc.native)) {
				t.Fatalf("expected %s ACP model selection, got %q", tc.name, calls)
			}
		})
	}
}

func TestMimoUsesNativeRunnerWhenACPIsAdvertised(t *testing.T) {
	tmpDir := t.TempDir()
	callsPath := filepath.Join(tmpDir, "calls")
	cliPath := filepath.Join(tmpDir, "mimo")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" > %q\nprintf '%%s\\n' '{\"text\":\"MIMO_NATIVE_OK\"}'\n", callsPath)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{
		Name: "mimo", Type: types.ProviderMimo, Protocol: "acp", CLIPath: cliPath,
		Args: []string{"run", "--format", "json", "--pure"}, WorkDir: tmpDir, Timeout: 2 * time.Second, Enabled: true,
	})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{
		Model: "mi/mimo-auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello MiMo"}},
	})
	var output string
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				output += event.Delta
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("MiMo native request failed: %v", err)
			}
		}
	}
	if output != "MIMO_NATIVE_OK" {
		t.Fatalf("expected native MiMo output, got %q", output)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "--model") || strings.Contains(string(calls), "mimo-auto") {
		t.Fatalf("virtual MiMo alias must not be passed to native CLI, got %q", calls)
	}
}

func TestProviderRunnerWarmACPPoolReusesProcess(t *testing.T) {
	tmpDir := t.TempDir()
	startsPath := filepath.Join(tmpDir, "starts")
	cliPath := filepath.Join(tmpDir, "opencode")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'start\\n' >> %q\ninitialized=0\nsession=0\nwhile IFS= read -r line; do\n  if printf '%%s' \"$line\" | grep -q '\"method\":\"initialize\"'; then\n    id=$(printf '%%s' \"$line\" | sed 's/.*\"id\":\\([0-9][0-9]*\\).*/\\1/')\n    printf '%%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":$id,\\\"result\\\":{\\\"protocolVersion\\\":1,\\\"authMethods\\\":[]}}\"\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"initialized\"'; then\n    initialized=1\n  elif [ \"$initialized\" = 1 ] && printf '%%s' \"$line\" | grep -q '\"method\":\"session/new\"'; then\n    session=$((session + 1))\n    id=$(printf '%%s' \"$line\" | sed 's/.*\"id\":\\([0-9][0-9]*\\).*/\\1/')\n    printf '%%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":$id,\\\"result\\\":{\\\"sessionId\\\":\\\"session-$session\\\",\\\"configOptions\\\":[]}}\"\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"session/prompt\"'; then\n    id=$(printf '%%s' \"$line\" | sed 's/.*\"id\":\\([0-9][0-9]*\\).*/\\1/')\n    printf '%%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"method\\\":\\\"session/update\\\",\\\"params\\\":{\\\"sessionId\\\":\\\"session-$session\\\",\\\"update\\\":{\\\"sessionUpdate\\\":\\\"agent_message_chunk\\\",\\\"content\\\":{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"POOL_OK_$session\\\"}}}}\"\n    printf '%%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":$id,\\\"result\\\":{\\\"stopReason\\\":\\\"end_turn\\\"}}\"\n  fi\ndone\n", startsPath)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{
		Name:     "opencode",
		Type:     types.ProviderOpenCode,
		Protocol: "acp",
		CLIPath:  cliPath,
		Args:     []string{"run", "--format", "json", "--pure"},
		WorkDir:  tmpDir,
		Timeout:  2 * time.Second,
		Enabled:  true,
		Harness: types.HarnessCapabilities{
			ObservedAt:     time.Now().UTC(),
			SupportsServer: true,
		},
	})
	t.Cleanup(runner.Close)

	for i, want := range []string{"POOL_OK_1", "POOL_OK_2"} {
		events, errorsCh := runner.Invoke(context.Background(), &types.OpenAIRequest{
			Model:    "oc/model",
			Messages: []types.OpenAIMessage{{Role: "user", Content: fmt.Sprintf("request-%d", i)}},
		})
		var output string
		for events != nil || errorsCh != nil {
			select {
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if event != nil {
					output += event.Delta
				}
			case err, ok := <-errorsCh:
				if !ok {
					errorsCh = nil
					continue
				}
				if err != nil {
					t.Fatalf("warm ACP request %d failed: %v", i, err)
				}
			}
		}
		if output != want {
			t.Fatalf("expected warm ACP output %q, got %q", want, output)
		}
	}
	starts, err := os.ReadFile(startsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(starts), "start\n"); got != 1 {
		t.Fatalf("expected one warm ACP process, got %d starts", got)
	}
}

func TestCursorACPProviderRoutesPromptAndModel(t *testing.T) {
	tmpDir := t.TempDir()
	callsPath := filepath.Join(tmpDir, "calls")
	cliPath := filepath.Join(tmpDir, "cursor")
	script := fmt.Sprintf("#!/bin/sh\ninitialized=0\nwhile IFS= read -r line; do\n  printf '%%s\\n' \"$line\" >> %q\n  if printf '%%s' \"$line\" | grep -q '\"method\":\"initialize\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1,\"authMethods\":[]}}'\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"initialized\"'; then\n    initialized=1\n  elif [ \"$initialized\" = 1 ] && printf '%%s' \"$line\" | grep -q '\"method\":\"session/new\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"sessionId\":\"session-1\",\"models\":{\"currentModelId\":\"default[]\",\"availableModels\":[{\"modelId\":\"test-model[]\",\"name\":\"Test Model\"}]}}}'\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"session/set_config_option\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"configOptions\":[]}}'\n  elif printf '%%s' \"$line\" | grep -q '\"method\":\"session/prompt\"'; then\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"session-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"ACP_OK\"}}}}'\n    printf '%%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"stopReason\":\"end_turn\"}}'\n    exit 0\n  fi\ndone\n", callsPath)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{
		Name:     "cursor",
		Type:     types.ProviderCursor,
		Protocol: "acp",
		CLIPath:  cliPath,
		Models:   []string{"cu/test-model"},
		WorkDir:  tmpDir,
		Timeout:  2 * time.Second,
		Enabled:  true,
	})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{
		Model:    "cu/test-model",
		Messages: []types.OpenAIMessage{{Role: "user", Content: "hello ACP"}},
	})
	var output string
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				output += event.Delta
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("Cursor ACP request failed: %v", err)
			}
		}
	}
	if output != "ACP_OK" {
		t.Fatalf("expected Cursor ACP output, got %q", output)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), `"method":"session/set_config_option"`) || !strings.Contains(string(calls), `"value":"test-model[]"`) {
		t.Fatalf("expected Cursor ACP model selection, got %q", calls)
	}
}

func TestCursorACPProviderCancelsDescendantHoldingPipes(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "cursor")
	script := "#!/bin/sh\n(sleep 30) &\nprintf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1,\"authMethods\":[]}}'\nsleep 30\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{
		Name:     "cursor",
		Type:     types.ProviderCursor,
		Protocol: "acp",
		CLIPath:  cliPath,
		WorkDir:  tmpDir,
		Timeout:  50 * time.Millisecond,
		Enabled:  true,
	})
	_, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{
		Model:    "cu/test-model",
		Messages: []types.OpenAIMessage{{Role: "user", Content: "hello ACP"}},
	})
	select {
	case err := <-errs:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected Cursor ACP deadline, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Cursor ACP cancellation did not return within the bound")
	}
}

func TestBuildCLIArgsDoesNotDuplicateConfiguredModelFlag(t *testing.T) {
	args := buildCLIArgs(&types.Provider{Type: types.ProviderClaudeCode, Args: []string{"--model", "existing"}}, "cc/model")
	if len(args) != 2 || args[1] != "existing" {
		t.Fatalf("expected configured model to be preserved, got %v", args)
	}
}

func TestBuildCLIArgsAddsClaudeVerboseForStreamJSON(t *testing.T) {
	args := buildCLIArgs(&types.Provider{
		Type: types.ProviderClaudeCode,
		Args: []string{"--print", "--output-format", "stream-json", "--no-session-persistence"},
	}, "cc/model")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--verbose") {
		t.Fatalf("expected Claude stream-json invocation to include --verbose, got %v", args)
	}
	if !strings.Contains(joined, "--model model") {
		t.Fatalf("expected Claude model argument, got %v", args)
	}
}

func TestProviderRunnerEmitsIncrementalCLIOutput(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "stream-cli")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"text\":\"first\"}'\nsleep 1\nprintf '%s\\n' '{\"text\":\"second\"}'\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runner := NewProviderRunner(&types.Provider{CLIPath: cliPath, Models: []string{"model"}, Enabled: true})
	events, errs := runner.Invoke(ctx, &types.OpenAIRequest{Model: "model", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}})
	select {
	case event := <-events:
		if event == nil || event.Delta != "first" {
			t.Fatalf("expected first incremental event, got %+v", event)
		}
	case err := <-errs:
		t.Fatalf("unexpected provider error: %v", err)
	case <-time.After(700 * time.Millisecond):
		t.Fatal("first event was not emitted before the CLI completed")
	}
}

func TestProviderRunnerDoesNotLeakRouterClientEnvironment(t *testing.T) {
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://127.0.0.1:9090/v1")
	t.Setenv("COPILOT_PROVIDER_API_KEY", "router-token")
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9090")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "router-token")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:9090/v1")
	t.Setenv("OPENAI_API_BASE", "http://127.0.0.1:9090/v1")
	t.Setenv("GHR_ACCESS_TOKEN", "router-token")
	runner := NewProviderRunner(&types.Provider{Env: map[string]string{
		"PROVIDER_ONLY":            "kept",
		"OPENAI_BASE_URL":          "http://127.0.0.1:9090/v1",
		"COPILOT_PROVIDER_API_KEY": "explicit-router-token",
	}})
	env := runner.buildEnv()
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, key := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "OPENAI_BASE_URL", "OPENAI_API_BASE", "GHR_ACCESS_TOKEN"} {
		if strings.Contains(joined, "\n"+key+"=") {
			t.Fatalf("router client variable %s leaked into provider environment", key)
		}
	}
	if !strings.Contains(joined, "\nPROVIDER_ONLY=kept\n") {
		t.Fatal("expected explicit provider environment to be preserved")
	}
}

func TestProviderRunnerIsolatesInheritedProviderCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("GOOGLE_API_KEY", "google-secret")
	t.Setenv("CURSOR_API_KEY", "cursor-secret")
	t.Setenv("PROVIDER_RUNTIME_MARKER", "must-not-be-inherited")

	runner := NewProviderRunner(&types.Provider{Type: types.ProviderCodex, Env: map[string]string{
		"EXPLICIT_PROVIDER_SETTING": "kept",
	}})
	env := strings.Join(runner.buildEnv(), "\n")
	if !strings.Contains(env, "OPENAI_API_KEY=openai-secret") {
		t.Fatal("current provider credential was not inherited")
	}
	for _, leaked := range []string{"ANTHROPIC_API_KEY=", "GOOGLE_API_KEY=", "CURSOR_API_KEY=", "PROVIDER_RUNTIME_MARKER="} {
		if strings.Contains(env, leaked) {
			t.Fatalf("unrelated inherited environment leaked: %s", leaked)
		}
	}
	if !strings.Contains(env, "EXPLICIT_PROVIDER_SETTING=kept") {
		t.Fatal("explicit provider environment was not preserved")
	}
}

func TestParseLineAndEmitSurfacesStructuredCLIError(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	ch := make(chan *StreamEvent, 1)
	runner.parseLineAndEmit(`{"type":"message_start","message":{"stopReason":"error","errorMessage":"quota exhausted"}}`, ch, "model")
	event := <-ch
	if event == nil || event.Error == nil || !strings.Contains(event.Error.Error(), "quota exhausted") {
		t.Fatalf("expected structured provider error, got %+v", event)
	}
}

func TestParseLineAndEmitSurfacesNestedOpenAIError(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	for _, line := range []string{
		`{"error":{"code":429,"message":"rate limit reached"}}`,
		`{"error":{"message":"authentication required"}}`,
	} {
		ch := make(chan *StreamEvent, 1)
		runner.parseLineAndEmit(line, ch, "model")
		event := <-ch
		if event == nil || event.Error == nil {
			t.Fatalf("expected nested provider error for %s, got %+v", line, event)
		}
		if !strings.Contains(event.Error.Error(), "provider reported error") {
			t.Fatalf("expected structured nested error for %s, got %v", line, event.Error)
		}
	}
}

func TestParseLineAndEmitPreservesStructuredRateLimitReset(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	resetAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	ch := make(chan *StreamEvent, 1)
	runner.parseLineAndEmit(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","reset_at":"`+resetAt+`"}}`, ch, "model")
	event := <-ch
	var capacityErr *CapacityError
	if event == nil || !errors.As(event.Error, &capacityErr) {
		t.Fatalf("expected typed structured capacity error, got %+v", event)
	}
	if capacityErr.StatusCode != http.StatusTooManyRequests || capacityErr.RetryAfter < time.Minute {
		t.Fatalf("expected preserved reset evidence, got %+v", capacityErr)
	}
}

func TestParseLineAndEmitSurfacesProviderCapacityText(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	ch := make(chan *StreamEvent, 1)
	meaningful, err := runner.parseLineAndMaybeEmitContext(context.Background(), "Upgrade your plan to continue", ch, "model", false)
	if meaningful {
		t.Fatal("expected provider capacity text not to count as a successful response")
	}
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected provider capacity error, got %v", err)
	}
}

func TestParseLineAndEmitSurfacesRejectedRateLimitEvent(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	ch := make(chan *StreamEvent, 1)
	runner.parseLineAndEmit(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"seven_day"}}`, ch, "model")
	event := <-ch
	if event == nil || event.Error == nil || !strings.Contains(event.Error.Error(), "rate limit") {
		t.Fatalf("expected rejected rate limit error, got %+v", event)
	}
}

func TestProviderRunnerPreservesStructuredErrorWhenCLIExitsNonZero(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "rate-limit-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"rejected\"}}'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{CLIPath: cliPath, Models: []string{"model"}, Enabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events, errorsCh := runner.Invoke(ctx, &types.OpenAIRequest{Model: "model"})
	var got error
	for events != nil || errorsCh != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
			} else if err != nil && got == nil {
				got = err
			}
		}
	}
	if got == nil || !strings.Contains(got.Error(), "rate limit") {
		t.Fatalf("expected structured rate-limit error, got %v", got)
	}
}

func TestCursorACPErrorPreservesDetailsForCapacityClassification(t *testing.T) {
	var message cursorACPMessage
	if err := json.Unmarshal([]byte(`{"id":4,"error":{"code":-32603,"message":"Internal error","data":{"details":"You've hit your usage limit"}}}`), &message); err != nil {
		t.Fatal(err)
	}
	if message.Error == nil || !strings.Contains(message.Error.Error(), "usage limit") {
		t.Fatalf("expected ACP error details, got %v", message.Error)
	}
	if err := providerOutputError(message.Error.Error()); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("expected capacity classification input, got %v", err)
	}
}

func TestMimoAutoDoesNotPassVirtualAliasToCLI(t *testing.T) {
	args := buildCLIArgs(&types.Provider{Type: types.ProviderMimo, Args: []string{"run", "--format", "json"}}, "mi/mimo-auto")
	for _, arg := range args {
		if arg == "mimo-auto" {
			t.Fatalf("virtual MiMo alias must not be passed to CLI: %v", args)
		}
	}
}

func TestParseLineAndEmitReadsPiAssistantMessageContent(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	ch := make(chan *StreamEvent, 1)
	runner.parseLineAndEmit(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"pi response"}]}}`, ch, "model")
	event := <-ch
	if event == nil || event.Delta != "pi response" {
		t.Fatalf("expected Pi assistant text, got %+v", event)
	}
}

func TestParseLineAndEmitReadsOpenCodePartText(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	ch := make(chan *StreamEvent, 1)
	runner.parseLineAndEmit(`{"type":"text","part":{"type":"text","text":"opencode response"}}`, ch, "model")
	event := <-ch
	if event == nil || event.Delta != "opencode response" {
		t.Fatalf("expected OpenCode part text, got %+v", event)
	}
}

func TestParseLineAndEmitReadsCodexAgentMessage(t *testing.T) {
	runner := NewProviderRunner(&types.Provider{})
	ch := make(chan *StreamEvent, 1)
	runner.parseLineAndEmit(`{"type":"item.completed","item":{"type":"agent_message","text":"codex response"}}`, ch, "model")
	event := <-ch
	if event == nil || event.Delta != "codex response" {
		t.Fatalf("expected Codex agent message text, got %+v", event)
	}
}

func TestProviderRunnerEmptyOutputIsTypedFailure(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "empty-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{Name: "empty", CLIPath: cliPath, WorkDir: tmpDir, Models: []string{"model"}})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			var emptyErr *EmptyResponseError
			if !errors.As(err, &emptyErr) {
				t.Fatalf("expected typed empty response error, got %T: %v", err, err)
			}
		}
	}
}

func TestProviderRunnerRetriesStructuredErrorBeforeEmittingIt(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "retry-cli")
	marker := filepath.Join(tmpDir, "attempted")
	script := fmt.Sprintf("#!/bin/sh\nif [ ! -f %q ]; then touch %q; printf '%%s\\n' '{\"error\":\"transient\"}'; exit 0; fi\nprintf '%%s\\n' '{\"text\":\"retry success\"}'\n", marker, marker)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{Name: "retry", CLIPath: cliPath, WorkDir: tmpDir, Models: []string{"model"}, Retries: 1, RetryBackoff: time.Millisecond})
	events, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	var text string
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event != nil {
				text += event.Delta
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			t.Fatalf("unexpected retry error: %v", err)
		}
	}
	if text != "retry success" {
		t.Fatalf("expected retry output, got %q", text)
	}
}

func TestProviderRunnerAppliesProviderTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "slow-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{Name: "slow", CLIPath: cliPath, WorkDir: tmpDir, Models: []string{"model"}, Timeout: 20 * time.Millisecond})
	_, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	err := <-errs
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected provider timeout, got %v", err)
	}
}

func TestProviderRunnerRequestFailureDoesNotQuarantineProvider(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "failing-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{Name: "failing", CLIPath: cliPath, WorkDir: tmpDir, Models: []string{"model"}})
	_, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	if err := <-errs; err == nil {
		t.Fatal("expected provider failure")
	}
	if !runner.GetHealth().Available {
		t.Fatal("request failure must not permanently quarantine provider")
	}
}

func TestProviderRunnerCircuitBreakerBlocksAndAllowsHalfOpenProbe(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "circuit-cli")
	countPath := filepath.Join(tmpDir, "count")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\nexit 1\n", countPath)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{Name: "circuit", CLIPath: cliPath, WorkDir: tmpDir, Models: []string{"model"}})
	runner.SetCircuitPolicy(CircuitPolicy{Enabled: true, FailureThreshold: 1, OpenDuration: time.Hour})

	_, firstErrs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	if err := <-firstErrs; err == nil {
		t.Fatal("expected first provider failure")
	}
	_, blockedErrs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	var blocked *CircuitOpenError
	if err := <-blockedErrs; !errors.As(err, &blocked) {
		t.Fatalf("expected circuit-open error, got %T: %v", err, err)
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "x" {
		t.Fatalf("open circuit executed CLI unexpectedly: %q", count)
	}

	runner.circuit.mu.Lock()
	runner.circuit.openedAt = time.Now().Add(-2 * time.Hour)
	runner.circuit.mu.Unlock()
	_, probeErrs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	if err := <-probeErrs; err == nil {
		t.Fatal("expected half-open probe failure")
	}
	count, err = os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "xx" {
		t.Fatalf("expected one half-open probe, got %q", count)
	}
}
