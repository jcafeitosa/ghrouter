package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"ghrouter/internal/config"
	"ghrouter/internal/local_brain"
	"ghrouter/internal/server"
	"ghrouter/internal/types"
)

type liveTUIModel struct {
	cfg         *types.Config
	cfgPath     string
	runtime     *server.Server
	source      liveSnapshotSource
	serverErrCh chan error
	report      local_brain.BootstrapReport
	snapshot    server.LiveSnapshot
	input       textinput.Model
	panel       int
	selected    int
	frame       int
	width       int
	height      int
	lastFetch   time.Time
	lastAction  string
	startupErr  error
	runtimeErr  error
	actionErr   error
	settings    settingsMode
	overlay     overlayMode
	palette     string
	paletteSel  int
	confirmKind liveActionKind
}

type liveTickMsg time.Time
type liveActionMsg struct {
	name   string
	output string
	err    error
}

type liveActionKind string
type settingsMode string
type liveSnapshotSource interface {
	Snapshot() (server.LiveSnapshot, local_brain.BootstrapReport, error)
	Start(context.Context)
}
type overlayMode string

const (
	liveActionDoctor       liveActionKind = "doctor"
	liveActionSync         liveActionKind = "sync"
	liveActionResetPreview liveActionKind = "reset-preview"
	liveActionResetApply   liveActionKind = "reset-apply"
	liveActionUpdateCheck  liveActionKind = "update-check"
	liveActionUpdateApply  liveActionKind = "update-apply"
)

const (
	settingsModeFilter settingsMode = "filter"
	settingsModePort   settingsMode = "port"
)

const (
	overlayNone    overlayMode = ""
	overlayPalette overlayMode = "palette"
	overlayConfirm overlayMode = "confirm"
	overlayHelp    overlayMode = "help"
)

type paletteCommand struct {
	label      string
	key        string
	requiresOK bool
	action     liveActionKind
}

var paletteCommands = []paletteCommand{
	{label: "Refresh snapshot", key: "r", action: "", requiresOK: false},
	{label: "Run doctor", key: "doctor", action: liveActionDoctor},
	{label: "Sync providers/models", key: "sync", action: liveActionSync},
	{label: "Preview reset", key: "reset preview", action: liveActionResetPreview},
	{label: "Check for update", key: "update check", action: liveActionUpdateCheck},
	{label: "Apply reset", key: "reset apply", action: liveActionResetApply, requiresOK: true},
	{label: "Apply update", key: "update apply", action: liveActionUpdateApply, requiresOK: true},
}

var liveFrames = []string{"◐", "◓", "◑", "◒"}
var livePanels = []string{"dashboard", "providers", "models", "routes", "activity", "settings"}

func newLiveTUIModel(cfg *types.Config, cfgPath string) liveTUIModel {
	input := textinput.New()
	input.Placeholder = "filter providers, routes, or models"
	input.CharLimit = 120
	input.SetWidth(42)
	return liveTUIModel{cfg: cfg, cfgPath: cfgPath, input: input, settings: settingsModeFilter}
}

func (m liveTUIModel) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), textinput.Blink)
}

func (m liveTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case liveTickMsg:
		m.frame = (m.frame + 1) % len(liveFrames)
		return m.withFreshData(), m.refreshCmd()
	case liveActionMsg:
		if msg.err != nil {
			m.lastAction = msg.name + ": failed"
			m.actionErr = msg.err
		} else if strings.TrimSpace(msg.output) != "" {
			m.lastAction = msg.name + ": " + strings.TrimSpace(msg.output)
			m.actionErr = nil
		} else {
			m.lastAction = msg.name + ": ok"
			m.actionErr = nil
		}
		return m.withFreshData(), m.refreshCmd()
	case tea.KeyMsg:
		if m.overlay == overlayPalette {
			return m.handlePaletteKey(msg)
		}
		if m.overlay == overlayConfirm {
			return m.handleConfirmKey(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "?":
			m.overlay = overlayHelp
			return m, nil
		case "ctrl+p", ":":
			m.overlay = overlayPalette
			m.palette = ""
			m.paletteSel = 0
			m.input.SetValue("")
			m.input.Placeholder = "command filter"
			m.input.Focus()
			return m, nil
		case "esc":
			if m.overlay == overlayHelp {
				m.overlay = overlayNone
				return m, nil
			}
			if m.panel == panelIndex("settings") && m.settings == settingsModePort {
				m.settings = settingsModeFilter
				m.input.Placeholder = "filter providers, routes, or models"
				m.input.SetValue("")
				m.input.Blur()
				return m, nil
			}
			return m, tea.Quit
		case "tab":
			m.panel = (m.panel + 1) % len(livePanels)
			m.selected = 0
			return m, nil
		case "shift+tab":
			m.panel = (m.panel - 1 + len(livePanels)) % len(livePanels)
			m.selected = 0
			return m, nil
		case "up", "k":
			if m.panel == panelIndex("providers") {
				m.selected--
				if m.selected < 0 {
					m.selected = max(len(m.snapshot.Providers)-1, 0)
				}
				return m, nil
			}
		case "down", "j":
			if m.panel == panelIndex("providers") {
				if len(m.snapshot.Providers) == 0 {
					m.selected = 0
					return m, nil
				}
				m.selected = (m.selected + 1) % len(m.snapshot.Providers)
				return m, nil
			}
		case "r":
			return m.withFreshData(), m.refreshCmd()
		case "g":
			if m.panel == panelIndex("settings") {
				m.overlay = overlayConfirm
				m.confirmKind = liveActionDoctor
				return m, nil
			}
		case "s":
			if m.panel == panelIndex("settings") {
				m.overlay = overlayConfirm
				m.confirmKind = liveActionSync
				return m, nil
			}
		case "x":
			if m.panel == panelIndex("settings") {
				return m, m.runActionCmd(liveActionResetPreview)
			}
		case "X":
			if m.panel == panelIndex("settings") {
				m.overlay = overlayConfirm
				m.confirmKind = liveActionResetApply
				return m, nil
			}
		case "u":
			if m.panel == panelIndex("settings") {
				return m, m.runActionCmd(liveActionUpdateCheck)
			}
		case "U":
			if m.panel == panelIndex("settings") {
				m.overlay = overlayConfirm
				m.confirmKind = liveActionUpdateApply
				return m, nil
			}
		case "p":
			if m.panel == panelIndex("settings") {
				m.settings = settingsModePort
				m.input.SetValue(fmt.Sprint(m.cfg.ListenPort))
				m.input.Placeholder = "enter new listen port"
				m.input.Focus()
				return m, nil
			}
		case "enter":
			if m.panel == panelIndex("settings") && m.settings == settingsModePort {
				m.settings = settingsModeFilter
				m.input.Blur()
				return m, m.savePortCmd()
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m liveTUIModel) View() tea.View {
	return renderLiveTUIView(m)
}

func (m liveTUIModel) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.overlay = overlayNone
		m.palette = ""
		m.input.Blur()
		return m, nil
	case "enter":
		next, cmd := m.resolvePaletteCommand()
		if next.overlay != overlayNone {
			m.overlay = next.overlay
			m.confirmKind = next.confirmKind
			m.palette = ""
			m.input.Blur()
			return m, nil
		}
		m.overlay = overlayNone
		m.palette = ""
		m.input.Blur()
		if cmd == nil {
			return m, nil
		}
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.palette = strings.ToLower(strings.TrimSpace(m.input.Value()))
	return m, cmd
}

func (m liveTUIModel) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.overlay = overlayNone
		return m, nil
	case "enter":
		kind := m.confirmKind
		m.overlay = overlayNone
		return m, m.runActionCmd(kind)
	}
	return m, nil
}

type paletteResult struct {
	overlay     overlayMode
	confirmKind liveActionKind
}

func (m liveTUIModel) resolvePaletteCommand() (paletteResult, tea.Cmd) {
	query := strings.ToLower(strings.TrimSpace(m.palette))
	for _, item := range paletteCommands {
		if query == "" || strings.Contains(strings.ToLower(item.label+" "+item.key), query) {
			if item.action == "" {
				return paletteResult{}, m.refreshCmd()
			}
			if item.requiresOK {
				return paletteResult{overlay: overlayConfirm, confirmKind: item.action}, nil
			}
			return paletteResult{}, m.runActionCmd(item.action)
		}
	}
	return paletteResult{}, nil
}

func (m liveTUIModel) refreshCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return liveTickMsg(t)
	})
}

func (m liveTUIModel) withFreshData() liveTUIModel {
	m = m.pollServerErr()
	if m.runtime == nil {
		m.runtime = server.New(m.cfg)
		if m.source == nil {
			m.source = embeddedSource{runtime: m.runtime, cfg: m.cfg}
		}
	}
	if m.lastFetch.IsZero() {
		ctx, cancel := context.WithCancel(context.Background())
		m.source.Start(ctx)
		go func() {
			<-time.After(30 * time.Minute)
			cancel()
		}()
	}
	if m.source == nil {
		m.source = embeddedSource{runtime: m.runtime, cfg: m.cfg}
	}
	snapshot, report, err := m.source.Snapshot()
	if err != nil {
		m.startupErr = err
		return m
	}
	m.report = report
	m.startupErr = nil
	m.snapshot = snapshot
	m.lastFetch = time.Now()
	return m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m liveTUIModel) pollServerErr() liveTUIModel {
	if m.serverErrCh == nil {
		return m
	}
	select {
	case err := <-m.serverErrCh:
		if err != nil {
			m.runtimeErr = err
		}
	default:
	}
	return m
}

func (m liveTUIModel) runActionCmd(kind liveActionKind) tea.Cmd {
	return func() tea.Msg {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		r := &Runner{Stdout: stdout, Stderr: stderr, Config: m.cfgPath}
		var code int
		switch kind {
		case liveActionDoctor:
			code = r.doctor()
		case liveActionSync:
			code = r.sync()
		case liveActionResetPreview:
			code = r.reset(nil)
		case liveActionResetApply:
			code = r.reset([]string{"--apply"})
		case liveActionUpdateCheck:
			code = r.update(nil)
		case liveActionUpdateApply:
			code = r.update([]string{"--apply"})
		}
		out := stdout.String()
		if code != 0 {
			return liveActionMsg{name: string(kind), output: out + stderr.String(), err: fmt.Errorf("exit code %d", code)}
		}
		return liveActionMsg{name: string(kind), output: out}
	}
}

func panelIndex(name string) int {
	for i, value := range livePanels {
		if value == name {
			return i
		}
	}
	return 0
}

func runLiveTUI(cfg *types.Config, cfgPath string, ctxDone <-chan struct{}) error {
	return runLiveTUIWithSource(cfg, cfgPath, ctxDone, nil)
}

func runLiveTUIWithSource(cfg *types.Config, cfgPath string, ctxDone <-chan struct{}, source liveSnapshotSource) error {
	runtime := server.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		if err := runtime.ListenAndServe(ctx); err != nil && !strings.Contains(err.Error(), "http: Server closed") {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	model := newLiveTUIModel(cfg, cfgPath).withRuntime(runtime, errCh)
	if source != nil {
		model.source = source
	} else {
		model.source = embeddedSource{runtime: runtime, cfg: cfg}
	}
	prog := tea.NewProgram(model, tea.WithContext(context.Background()))
	go func() {
		<-ctxDone
		cancel()
		prog.Quit()
	}()
	_, err := prog.Run()
	return err
}

func (m liveTUIModel) withRuntime(runtime *server.Server, serverErrCh chan error) liveTUIModel {
	m.runtime = runtime
	m.serverErrCh = serverErrCh
	m = m.withFreshData()
	return m
}

type embeddedSource struct {
	runtime *server.Server
	cfg     *types.Config
}

func (s embeddedSource) Start(ctx context.Context) {
	if s.runtime != nil {
		s.runtime.StartMonitoring(ctx)
	}
}

func (s embeddedSource) Snapshot() (server.LiveSnapshot, local_brain.BootstrapReport, error) {
	if s.runtime == nil || s.cfg == nil {
		return server.LiveSnapshot{}, local_brain.BootstrapReport{}, fmt.Errorf("missing embedded runtime")
	}
	bootstrapper, err := local_brain.NewBootstrapper()
	if err != nil {
		return server.LiveSnapshot{}, local_brain.BootstrapReport{}, err
	}
	report, reportErr := bootstrapper.Check(s.cfg.Providers)
	return s.runtime.LiveSnapshot(), report, reportErr
}

type attachedSource struct {
	baseURL string
	client  *http.Client
}

func (s attachedSource) Start(ctx context.Context) {}

func (s attachedSource) Snapshot() (server.LiveSnapshot, local_brain.BootstrapReport, error) {
	if s.client == nil {
		s.client = &http.Client{Timeout: 5 * time.Second}
	}
	if _, err := url.ParseRequestURI(s.baseURL); err != nil {
		return server.LiveSnapshot{}, local_brain.BootstrapReport{}, err
	}
	var snap server.LiveSnapshot
	if err := fetchJSON(s.client, s.baseURL+"/health", &struct{}{}); err != nil {
		return server.LiveSnapshot{}, local_brain.BootstrapReport{}, err
	}
	if err := fetchJSON(s.client, s.baseURL+"/health", &snap); err != nil {
		return server.LiveSnapshot{}, local_brain.BootstrapReport{}, err
	}
	return snap, local_brain.BootstrapReport{}, nil
}

func fetchJSON(client *http.Client, endpoint string, out any) error {
	resp, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m liveTUIModel) savePortCmd() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	return func() tea.Msg {
		port, err := parseInt(value)
		if err != nil || port <= 0 {
			return liveActionMsg{name: "save-port", err: fmt.Errorf("invalid listen port %q", value)}
		}
		m.cfg.ListenPort = port
		if m.cfgPath == "" {
			m.cfgPath = config.ResolveConfigPath("")
		}
		if m.cfgPath == "" {
			return liveActionMsg{name: "save-port", err: fmt.Errorf("missing config path")}
		}
		if err := config.Save(m.cfgPath, m.cfg); err != nil {
			return liveActionMsg{name: "save-port", err: err}
		}
		return liveActionMsg{name: "save-port", output: fmt.Sprintf("listen_port=%d saved to %s", port, m.cfgPath)}
	}
}
