package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"ghrouter/internal/config"
	"ghrouter/internal/local_brain"
	"ghrouter/internal/server"
	"ghrouter/internal/types"
)

type liveTUIModel struct {
	cfg              *types.Config
	cfgPath          string
	runtime          *server.Server
	source           liveSnapshotSource
	serverErrCh      chan error
	report           local_brain.BootstrapReport
	snapshot         server.LiveSnapshot
	input            textinput.Model
	viewport         viewport.Model
	activityTable    table.Model
	spinner          spinner.Model
	help             help.Model
	panel            int
	selected         int
	width            int
	height           int
	lastFetch        time.Time
	refreshSeq       uint64
	issuedSeq        uint64
	stale            bool
	hasSnapshot      bool
	lastAction       string
	startupErr       error
	runtimeErr       error
	runtimeFailed    bool
	actionErr        error
	actionCtx        context.Context
	actionCancel     context.CancelFunc
	settings         settingsMode
	overlay          overlayMode
	palette          string
	paletteSel       int
	confirmKind      liveActionKind
	filterActive     bool
	graphFrame       uint64
	controlPlaneEdit bool
	controlPlaneKind string
	controlPlaneName string
	nvidiaDraft      types.ProviderCredential
}

type liveTickMsg time.Time
type liveSnapshotMsg struct {
	seq      uint64
	snapshot server.LiveSnapshot
	report   local_brain.BootstrapReport
	err      error
}
type liveRuntimeErrMsg error
type liveActionMsg struct {
	name   string
	output string
	err    error
	cfg    *types.Config
}

type liveActionKind string
type settingsMode string
type liveSnapshotSource interface {
	Snapshot() (server.LiveSnapshot, local_brain.BootstrapReport, error)
	Start(context.Context)
}
type overlayMode string

const (
	liveActionDoctor        liveActionKind = "doctor"
	liveActionSync          liveActionKind = "sync"
	liveActionResetPreview  liveActionKind = "reset-preview"
	liveActionResetApply    liveActionKind = "reset-apply"
	liveActionUpdateCheck   liveActionKind = "update-check"
	liveActionUpdateApply   liveActionKind = "update-apply"
	liveActionControlPut    liveActionKind = "control-plane-put"
	liveActionControlDelete liveActionKind = "control-plane-delete"
)

const (
	settingsModeFilter     settingsMode = "filter"
	settingsModePort       settingsMode = "port"
	settingsModeNVIDIAName settingsMode = "nvidia-name"
	settingsModeNVIDIAEnv  settingsMode = "nvidia-env"
	settingsModeNVIDIAKey  settingsMode = "nvidia-key"
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

var livePanels = []string{"dashboard", "usage", "brain-log", "providers", "models", "routes", "control-plane", "activity", "settings"}

func newLiveTUIModel(cfg *types.Config, cfgPath string) liveTUIModel {
	actionCtx, actionCancel := context.WithCancel(context.Background())
	input := textinput.New()
	input.Placeholder = "filter providers, routes, or models"
	input.CharLimit = 120
	input.SetWidth(42)
	activityTable := table.New(
		table.WithColumns(activityTableColumns()),
		table.WithFocused(true),
	)
	return liveTUIModel{
		cfg:           cfg,
		cfgPath:       cfgPath,
		input:         input,
		viewport:      viewport.New(),
		activityTable: activityTable,
		spinner:       spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		help:          help.New(),
		settings:      settingsModeFilter,
		issuedSeq:     1,
		actionCtx:     actionCtx,
		actionCancel:  actionCancel,
	}
}

func (m liveTUIModel) Init() tea.Cmd {
	return tea.Batch(m.snapshotCmd(), m.refreshTickCmd(), m.runtimeErrCmd(), func() tea.Msg { return m.spinner.Tick() }, textinput.Blink)
}

func (m liveTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.SetWidth(max(msg.Width-8, 24))
		m.viewport.SetHeight(max(msg.Height-12, 5))
		m.help.SetWidth(msg.Width - 4)
		m.activityTable.SetWidth(max(msg.Width-12, 32))
		m.activityTable.SetHeight(max(msg.Height-16, 6))
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		m.graphFrame++
		return m, cmd
	case liveTickMsg:
		m.issuedSeq++
		return m, tea.Batch(m.snapshotCmd(), m.refreshTickCmd())
	case liveRuntimeErrMsg:
		m.runtimeErr = error(msg)
		m.runtimeFailed = true
		return m, m.runtimeErrCmd()
	case liveSnapshotMsg:
		if msg.seq < m.refreshSeq || msg.seq < m.issuedSeq {
			return m, nil
		}
		m.refreshSeq = msg.seq
		if msg.err != nil {
			m.stale = true
			m.startupErr = msg.err
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.activityTable.SetRows(activityTableRows(msg.snapshot))
		m.report = msg.report
		m.hasSnapshot = true
		m.lastFetch = time.Now()
		m.stale = false
		m.startupErr = nil
		if m.runtime == nil {
			m.runtimeErr = nil
			m.runtimeFailed = false
		}
		return m, nil
	case liveActionMsg:
		if msg.cfg != nil {
			m.cfg = msg.cfg
		}
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
		m.issuedSeq++
		return m, m.snapshotCmd()
	case tea.KeyMsg:
		if m.overlay == overlayPalette {
			return m.handlePaletteKey(msg)
		}
		if m.overlay == overlayConfirm {
			return m.handleConfirmKey(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q":
			if m.actionCancel != nil {
				m.actionCancel()
			}
			return m, tea.Quit
		case "?":
			m.overlay = overlayHelp
			return m, nil
		case "ctrl+p", ":", "/":
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
			if m.panel == panelIndex("settings") && isNVIDIASettingsMode(m.settings) {
				m.settings = settingsModeFilter
				m.nvidiaDraft = types.ProviderCredential{}
				m.input.EchoMode = textinput.EchoNormal
				m.input.SetValue("")
				m.input.Placeholder = "filter providers, routes, or models"
				m.input.Blur()
				return m, nil
			}
			if m.controlPlaneEdit {
				m.controlPlaneEdit = false
				m.controlPlaneKind = ""
				m.controlPlaneName = ""
				m.input.SetValue("")
				m.input.CharLimit = 120
				m.input.Placeholder = "filter providers, routes, or models"
				m.input.Blur()
				return m, nil
			}
			if m.filterActive {
				m.filterActive = false
				m.input.Blur()
				m.input.SetValue("")
				m.input.Placeholder = "filter providers, routes, or models"
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
				providers := visibleProviders(m)
				if len(providers) == 0 {
					m.selected = 0
					return m, nil
				}
				m.selected--
				if m.selected < 0 {
					m.selected = len(providers) - 1
				}
				return m, nil
			}
			if m.panel == panelIndex("control-plane") {
				m.moveControlPlaneSelection(-1)
				return m, nil
			}
			if isScrollablePanel(m.panel) {
				if m.panel == panelIndex("activity") {
					m.activityTable, _ = m.activityTable.Update(msg)
					return m, nil
				}
				var cmd tea.Cmd
				m.viewport.SetContent(strings.Join(scrollablePanelLines(m), "\n"))
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		case "down", "j":
			if m.panel == panelIndex("providers") {
				providers := visibleProviders(m)
				if len(providers) == 0 {
					m.selected = 0
					return m, nil
				}
				m.selected = (m.selected + 1) % len(providers)
				return m, nil
			}
			if m.panel == panelIndex("control-plane") {
				m.moveControlPlaneSelection(1)
				return m, nil
			}
			if isScrollablePanel(m.panel) {
				if m.panel == panelIndex("activity") {
					m.activityTable, _ = m.activityTable.Update(msg)
					return m, nil
				}
				var cmd tea.Cmd
				m.viewport.SetContent(strings.Join(scrollablePanelLines(m), "\n"))
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		case "pgup", "pgdown", "ctrl+b", "ctrl+f", "home", "end":
			if isScrollablePanel(m.panel) {
				if m.panel == panelIndex("activity") {
					m.activityTable, _ = m.activityTable.Update(msg)
					return m, nil
				}
				var cmd tea.Cmd
				m.viewport.SetContent(strings.Join(scrollablePanelLines(m), "\n"))
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		case "r":
			m.issuedSeq++
			return m, m.snapshotCmd()
		case "f":
			if m.panel != panelIndex("settings") {
				m.filterActive = true
				m.input.SetValue("")
				m.settings = settingsModeFilter
				m.input.Placeholder = "filter providers, routes, or models"
				m.input.Focus()
				return m, textinput.Blink
			}
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
				m.filterActive = false
				return m, nil
			}
		case "n":
			if m.panel == panelIndex("settings") && m.settings == settingsModeFilter {
				m.settings = settingsModeNVIDIAName
				m.nvidiaDraft = types.ProviderCredential{Enabled: true}
				m.input.EchoMode = textinput.EchoNormal
				m.input.SetValue("")
				m.input.Placeholder = "NVIDIA account name"
				m.input.Focus()
				return m, textinput.Blink
			}
		case "e":
			if m.panel == panelIndex("control-plane") && !m.controlPlaneEdit {
				if kind, name, resource, ok := selectedControlPlaneResource(m); ok {
					m.controlPlaneEdit = true
					m.controlPlaneKind = kind
					m.controlPlaneName = name
					encoded, _ := json.Marshal(resource)
					m.input.SetValue(string(encoded))
					m.input.CharLimit = 4096
					m.input.Placeholder = "edit JSON resource, enter to apply"
					m.input.Focus()
					return m, nil
				}
			}
		case "d":
			if m.panel == panelIndex("control-plane") && !m.controlPlaneEdit {
				if kind, name, _, ok := selectedControlPlaneResource(m); ok {
					m.controlPlaneKind = kind
					m.controlPlaneName = name
					m.confirmKind = liveActionControlDelete
					m.overlay = overlayConfirm
					return m, nil
				}
			}
		case "enter":
			if m.controlPlaneEdit {
				m.controlPlaneEdit = false
				m.input.Blur()
				m.input.CharLimit = 120
				return m, m.controlPlanePutCmd(m.input.Value())
			}
			if m.panel == panelIndex("settings") && m.settings == settingsModePort {
				m.settings = settingsModeFilter
				m.input.Blur()
				return m, m.savePortCmd()
			}
			if m.panel == panelIndex("settings") {
				switch m.settings {
				case settingsModeNVIDIAName:
					m.nvidiaDraft.Name = strings.TrimSpace(m.input.Value())
					if m.nvidiaDraft.Name == "" {
						m.lastAction = "nvidia account: name is required"
						return m, nil
					}
					m.settings = settingsModeNVIDIAEnv
					m.input.SetValue("")
					m.input.Placeholder = "API key environment variable (optional)"
					return m, nil
				case settingsModeNVIDIAEnv:
					m.nvidiaDraft.APIKeyEnv = strings.TrimSpace(m.input.Value())
					m.settings = settingsModeNVIDIAKey
					m.input.EchoMode = textinput.EchoPassword
					m.input.SetValue("")
					m.input.Placeholder = "NVIDIA API key (optional; masked)"
					return m, nil
				case settingsModeNVIDIAKey:
					m.nvidiaDraft.APIKey = strings.TrimSpace(m.input.Value())
					if m.nvidiaDraft.APIKey == "" && m.nvidiaDraft.APIKeyEnv == "" {
						m.lastAction = "nvidia account: key or environment variable is required"
						return m, nil
					}
					draft := m.nvidiaDraft
					m.settings = settingsModeFilter
					m.nvidiaDraft = types.ProviderCredential{}
					m.input.EchoMode = textinput.EchoNormal
					m.input.SetValue("")
					m.input.Placeholder = "filter providers, routes, or models"
					m.input.Blur()
					return m, m.saveNVIDIAAccountCmd(draft)
				}
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
	case "up", "k":
		commands := matchingPaletteCommands(m.palette)
		if len(commands) > 0 {
			m.paletteSel = (m.paletteSel - 1 + len(commands)) % len(commands)
		}
		return m, nil
	case "down", "j":
		commands := matchingPaletteCommands(m.palette)
		if len(commands) > 0 {
			m.paletteSel = (m.paletteSel + 1) % len(commands)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.palette = strings.ToLower(strings.TrimSpace(m.input.Value()))
	m.paletteSel = 0
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
		if kind == liveActionControlDelete {
			return m, m.controlPlaneDeleteCmd()
		}
		return m, m.runActionCmd(kind)
	}
	return m, nil
}

type paletteResult struct {
	overlay     overlayMode
	confirmKind liveActionKind
}

func (m liveTUIModel) resolvePaletteCommand() (paletteResult, tea.Cmd) {
	commands := matchingPaletteCommands(m.palette)
	if len(commands) == 0 {
		return paletteResult{}, nil
	}
	if m.paletteSel >= len(commands) {
		m.paletteSel = 0
	}
	item := commands[m.paletteSel]
	if item.action == "" {
		m.issuedSeq++
		return paletteResult{}, m.snapshotCmd()
	}
	if item.requiresOK {
		return paletteResult{overlay: overlayConfirm, confirmKind: item.action}, nil
	}
	return paletteResult{}, m.runActionCmd(item.action)
}

func matchingPaletteCommands(query string) []paletteCommand {
	query = strings.ToLower(strings.TrimSpace(query))
	commands := make([]paletteCommand, 0, len(paletteCommands))
	for _, item := range paletteCommands {
		if query == "" || strings.Contains(strings.ToLower(item.label+" "+item.key), query) {
			commands = append(commands, item)
		}
	}
	return commands
}

func (m liveTUIModel) refreshTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return liveTickMsg(t) })
}

func (m liveTUIModel) snapshotCmd() tea.Cmd {
	source := m.source
	seq := m.issuedSeq
	return func() tea.Msg {
		if source == nil {
			return liveSnapshotMsg{seq: seq, err: fmt.Errorf("snapshot source unavailable")}
		}
		snapshot, report, err := source.Snapshot()
		return liveSnapshotMsg{seq: seq, snapshot: snapshot, report: report, err: err}
	}
}

func (m liveTUIModel) runtimeErrCmd() tea.Cmd {
	if m.serverErrCh == nil {
		return nil
	}
	return func() tea.Msg {
		err, ok := <-m.serverErrCh
		if !ok || err == nil {
			return nil
		}
		return liveRuntimeErrMsg(err)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m liveTUIModel) runActionCmd(kind liveActionKind) tea.Cmd {
	ctx := m.actionCtx
	return func() tea.Msg {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return liveActionMsg{name: string(kind), err: ctx.Err()}
			default:
			}
		}
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
		if ctx != nil {
			select {
			case <-ctx.Done():
				return liveActionMsg{name: string(kind), err: ctx.Err()}
			default:
			}
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

func isScrollablePanel(panel int) bool {
	return panel == panelIndex("usage") || panel == panelIndex("models") || panel == panelIndex("routes") || panel == panelIndex("activity") || panel == panelIndex("brain-log")
}

type controlPlaneSelection struct {
	kind     string
	name     string
	resource any
}

func controlPlaneResources(m liveTUIModel) []controlPlaneSelection {
	resources := make([]controlPlaneSelection, 0, len(m.snapshot.Connections)+len(m.snapshot.Pools)+len(m.snapshot.Combos))
	for _, resource := range m.snapshot.Connections {
		resources = append(resources, controlPlaneSelection{kind: "connection", name: resource.Name, resource: types.Connection{Name: resource.Name, Provider: resource.Provider, Model: resource.Model, Enabled: resource.Enabled, Metadata: resource.Metadata}})
	}
	for _, resource := range m.snapshot.Pools {
		resources = append(resources, controlPlaneSelection{kind: "pool", name: resource.Name, resource: types.Pool{Name: resource.Name, Members: append([]string(nil), resource.Members...), Strategy: resource.Strategy, Enabled: resource.Enabled}})
	}
	for _, resource := range m.snapshot.Combos {
		resources = append(resources, controlPlaneSelection{kind: "combo", name: resource.Name, resource: types.Combo{Name: resource.Name, Members: append([]string(nil), resource.Members...), Strategy: resource.Strategy, Judge: resource.Judge, Enabled: resource.Enabled}})
	}
	return resources
}

func selectedControlPlaneResource(m liveTUIModel) (string, string, any, bool) {
	resources := controlPlaneResources(m)
	if len(resources) == 0 {
		return "", "", nil, false
	}
	index := m.selected % len(resources)
	resource := resources[index]
	return resource.kind, resource.name, resource.resource, true
}

func (m *liveTUIModel) moveControlPlaneSelection(delta int) {
	resources := controlPlaneResources(*m)
	if len(resources) == 0 {
		m.selected = 0
		return
	}
	m.selected = (m.selected + delta) % len(resources)
	if m.selected < 0 {
		m.selected += len(resources)
	}
}

func (m liveTUIModel) controlPlaneBaseURL() string {
	if attached, ok := m.source.(attachedSource); ok {
		return strings.TrimRight(attached.baseURL, "/")
	}
	port := m.snapshot.ListenPort
	if port <= 0 && m.cfg != nil {
		port = m.cfg.ListenPort
	}
	if port <= 0 {
		port = 9090
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func (m liveTUIModel) controlPlanePutCmd(raw string) tea.Cmd {
	baseURL, kind, name, ctx := m.controlPlaneBaseURL(), m.controlPlaneKind, m.controlPlaneName, m.actionCtx
	return func() tea.Msg {
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+"/v1/control-plane/"+kind+"/"+url.PathEscape(name), strings.NewReader(raw))
		if err != nil {
			return liveActionMsg{name: string(liveActionControlPut), err: err}
		}
		request.Header.Set("Content-Type", "application/json")
		if token := os.Getenv("GHR_ACCESS_TOKEN"); token != "" {
			request.Header.Set("X-Ghrouter-Token", token)
		}
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			return liveActionMsg{name: string(liveActionControlPut), err: err}
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode >= 300 {
			return liveActionMsg{name: string(liveActionControlPut), output: string(body), err: fmt.Errorf("control-plane update returned %s", response.Status)}
		}
		return liveActionMsg{name: string(liveActionControlPut), output: kind + "/" + name + " updated"}
	}
}

func (m liveTUIModel) controlPlaneDeleteCmd() tea.Cmd {
	baseURL, kind, name, ctx := m.controlPlaneBaseURL(), m.controlPlaneKind, m.controlPlaneName, m.actionCtx
	return func() tea.Msg {
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/v1/control-plane/"+kind+"/"+url.PathEscape(name), nil)
		if err != nil {
			return liveActionMsg{name: string(liveActionControlDelete), err: err}
		}
		if token := os.Getenv("GHR_ACCESS_TOKEN"); token != "" {
			request.Header.Set("X-Ghrouter-Token", token)
		}
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			return liveActionMsg{name: string(liveActionControlDelete), err: err}
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 {
			body, _ := io.ReadAll(response.Body)
			return liveActionMsg{name: string(liveActionControlDelete), output: string(body), err: fmt.Errorf("control-plane delete returned %s", response.Status)}
		}
		return liveActionMsg{name: string(liveActionControlDelete), output: kind + "/" + name + " deleted"}
	}
}

func runLiveTUI(cfg *types.Config, cfgPath string, ctxDone <-chan struct{}) error {
	return runLiveTUIWithSource(cfg, cfgPath, ctxDone, nil)
}

func runLiveTUIWithSource(cfg *types.Config, cfgPath string, ctxDone <-chan struct{}, source liveSnapshotSource) error {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	var runtime *server.Server
	if source == nil {
		runtime = server.NewWithConfigPath(cfg, config.ResolveConfigPath(cfgPath))
		source = embeddedSource{runtime: runtime, cfg: cfg}
		go func() {
			if err := runtime.ListenAndServe(ctx); err != nil && !strings.Contains(err.Error(), "http: Server closed") {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	} else {
		startLiveSource(source, ctx)
	}
	model := newLiveTUIModel(cfg, cfgPath)
	model.source = source
	model.runtime = runtime
	model.serverErrCh = errCh
	prog := tea.NewProgram(model, tea.WithContext(context.Background()))
	defer cancel()
	go func() {
		<-ctxDone
		cancel()
		if model.actionCancel != nil {
			model.actionCancel()
		}
		prog.Quit()
	}()
	_, err := prog.Run()
	return err
}

func startLiveSource(source liveSnapshotSource, ctx context.Context) {
	if source != nil {
		source.Start(ctx)
	}
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
	report, _ := bootstrapper.Check(s.cfg.Providers)
	return s.runtime.LiveSnapshot(), report, nil
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
	var response server.LiveResponse
	if err := fetchJSON(s.client, strings.TrimRight(s.baseURL, "/")+"/live", &response); err != nil {
		return server.LiveSnapshot{}, local_brain.BootstrapReport{}, err
	}
	report := local_brain.BootstrapReport{
		Backend: response.Bootstrap.Backend,
		Issues:  response.Bootstrap.Issues,
		Checks:  response.Bootstrap.Checks,
	}
	return response.Snapshot, report, nil
}

func fetchJSON(client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if token := os.Getenv("GHR_ACCESS_TOKEN"); token != "" {
		req.Header.Set("X-Ghrouter-Token", token)
	}
	resp, err := client.Do(req)
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
		return liveActionMsg{name: "save-port", output: fmt.Sprintf("listen_port=%d saved to %s (restart required)", port, m.cfgPath)}
	}
}

func (m liveTUIModel) saveNVIDIAAccountCmd(draft types.ProviderCredential) tea.Cmd {
	cfgPath := m.cfgPath
	if cfgPath == "" {
		cfgPath = config.ResolveConfigPath("")
	}
	runtime := m.runtime
	return func() tea.Msg {
		if strings.TrimSpace(draft.Name) == "" {
			return liveActionMsg{name: "save-nvidia-account", err: fmt.Errorf("account name is required")}
		}
		if strings.TrimSpace(draft.APIKey) == "" && strings.TrimSpace(draft.APIKeyEnv) == "" {
			return liveActionMsg{name: "save-nvidia-account", err: fmt.Errorf("api key or api key environment variable is required")}
		}
		next, err := config.Load(cfgPath)
		if err != nil {
			return liveActionMsg{name: "save-nvidia-account", err: err}
		}
		var nvidia *types.Provider
		for _, provider := range next.Providers {
			if provider != nil && (provider.Name == "nvidia" || provider.Type == types.ProviderNVIDIA) {
				nvidia = provider
				break
			}
		}
		if nvidia == nil {
			nvidia = &types.Provider{
				Name:       "nvidia",
				Type:       types.ProviderNVIDIA,
				BaseURL:    "https://integrate.api.nvidia.com",
				AuthMethod: types.AuthEnv,
				Models:     configuredNVIDIAModels(),
				Enabled:    true,
			}
			next.Providers = append(next.Providers, nvidia)
		}
		draft.Enabled = true
		replaced := false
		for index := range nvidia.Accounts {
			if nvidia.Accounts[index].Name == draft.Name {
				nvidia.Accounts[index] = draft
				replaced = true
				break
			}
		}
		if !replaced {
			nvidia.Accounts = append(nvidia.Accounts, draft)
		}
		if err := config.Save(cfgPath, next); err != nil {
			return liveActionMsg{name: "save-nvidia-account", err: err}
		}
		output := fmt.Sprintf("account %s saved to %s", draft.Name, cfgPath)
		if runtime != nil {
			if err := runtime.ReloadConfig(next); err == nil {
				output += " and reloaded"
			} else {
				output += "; restart required"
			}
		}
		return liveActionMsg{name: "save-nvidia-account", output: output, cfg: next}
	}
}

func configuredNVIDIAModels() []string {
	models := make([]string, 0)
	for _, raw := range strings.Split(os.Getenv("GHR_NVIDIA_MODELS"), ",") {
		model := strings.TrimSpace(raw)
		if model != "" {
			models = append(models, model)
		}
	}
	return models
}
