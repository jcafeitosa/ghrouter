package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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

func TestLiveActionResetPreviewReturnsResult(t *testing.T) {
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
	if !strings.Contains(msg.output, "claude-code") {
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
	if !strings.Contains(msg.output, "listen_port=8123 saved") {
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
}

func TestRunUpdateJSONCommand(t *testing.T) {
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/jcafeitosa/ghrouter/releases/latest":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[{"name":"ghrouter_darwin_arm64","browser_download_url":"`+baseURL+`/asset"}]}`)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/jcafeitosa/ghrouter/releases/latest":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[{"name":"ghrouter_darwin_arm64","browser_download_url":"`+baseURL+`/asset"}]}`)
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
}

func TestPrintStartupStatusReportsMissingAuthWithoutFailing(t *testing.T) {
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
	if stderr.Len() == 0 {
		t.Fatal("expected startup warning on stderr")
	}
}

func TestRunLiveJSONCommandProducesSnapshot(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "models"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id"`) {
		t.Fatalf("expected JSON models output, got %s", stdout.String())
	}
}

func TestRunTestJSONCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "test", "cc/claude-sonnet-5"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"requested"`) {
		t.Fatalf("expected JSON test output, got %s", stdout.String())
	}
}

func TestRunExplainJSONCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}

	code := r.Run(context.Background(), []string{"--json", "explain", "cc/claude-sonnet-5"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"requested"`) {
		t.Fatalf("expected JSON explain output, got %s", stdout.String())
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

	cfgPath := filepath.Join(home, "provision-apply.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen_port: 9090\nproviders: []\nroutes: []\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("GHR_CONFIG", cfgPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr}
	code := r.Run(context.Background(), []string{"provision", "--apply"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	planPath := filepath.Join(home, ".cache", "ghrouter", "models", "provision-plan.json")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read provision plan: %v", err)
	}
	if !strings.Contains(string(data), `"action"`) {
		t.Fatalf("expected provision plan contents, got %s", string(data))
	}
	if !strings.Contains(stdout.String(), "apply\tok") {
		t.Fatalf("expected apply confirmation, got %s", stdout.String())
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
