package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

func TestConnectPrintsNativeCopilotEnvironment(t *testing.T) {
	var stdout bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Config: filepath.Join(t.TempDir(), "missing.yaml")}
	if code := r.Run(context.Background(), []string{"connect", "copilot"}); code != 0 {
		t.Fatalf("expected connect to succeed, got %d", code)
	}
	for _, needle := range []string{
		"COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:9090/v1",
		"COPILOT_PROVIDER_TYPE=openai",
		"COPILOT_PROVIDER_WIRE_API=completions",
		"COPILOT_PROVIDER_MODEL_ID=gpt-4o",
		"COPILOT_PROVIDER_WIRE_MODEL=ghrouter/auto",
		"COPILOT_PROVIDER_API_KEY=ghr_gh_",
		"COPILOT_MODEL=ghrouter/auto",
	} {
		if !strings.Contains(stdout.String(), needle) {
			t.Fatalf("expected connect output to contain %q, got %s", needle, stdout.String())
		}
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
	if strings.Join(merged[0].Args, " ") != "custom flag" || merged[0].Models[0] != "cx/custom-model" {
		t.Fatalf("expected manual provider overrides to survive sync, got %+v", merged[0])
	}
	if merged[0].AuthConfig["plan"] != "team" {
		t.Fatalf("expected auth config to survive sync, got %+v", merged[0].AuthConfig)
	}
	if merged[0].BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("expected manual base URL to survive sync, got %q", merged[0].BaseURL)
	}
}

func TestMergeDetectedProvidersRefreshesNativeCatalog(t *testing.T) {
	previous := []*types.Provider{{
		Name: "opencode", Type: types.ProviderOpenCode,
		Models:    []string{"oc/old-model"},
		ModelInfo: map[string]types.ModelInfo{"oc/old-model": {Source: "native"}},
		Enabled:   true,
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
	if failed.HealthStatus != "failed" || !failed.CooldownUntil.Equal(resetAt) || failed.VerificationError == "" {
		t.Fatalf("expected failed verification state, got %+v", failed)
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
		Health: server.HealthSnapshot{Providers: map[string]server.HealthState{
			"codex": {Status: "degraded"},
		}},
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
	digest := sha256.Sum256([]byte("new-binary"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/jcafeitosa/ghrouter/releases/latest":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[{"name":"ghrouter_darwin_arm64","browser_download_url":"`+baseURL+`/asset","digest":"sha256:`+hex.EncodeToString(digest[:])+`"}]}`)
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
	if stderr.Len() != 0 {
		t.Fatalf("expected partial startup status to stay out of stderr, got %s", stderr.String())
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
}

func TestRunTestJSONCommand(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	configBody := "providers:\n  - name: claude-code\n    type: claude-code\n    cli_path: /bin/true\n    models: [cc/claude-sonnet-5]\n    model_info:\n      cc/claude-sonnet-5:\n        health_status: healthy\n        verified_at: 2026-08-02T00:00:00Z\n    enabled: true\n"
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	r := &Runner{Stdout: &stdout, Stderr: &stderr, Config: cfgPath}

	code := r.Run(context.Background(), []string{"--json", "test", "cc/claude-sonnet-5"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"requested"`) {
		t.Fatalf("expected JSON test output, got %s", stdout.String())
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
	if code != 1 {
		t.Fatalf("expected post-check failure for unavailable detected providers, got %d (%s)", code, stderr.String())
	}
	planPath := filepath.Join(home, ".cache", "ghrouter", "models", "provision-plan.json")
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
