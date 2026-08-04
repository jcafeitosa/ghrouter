package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ghrouter/internal/account"
	"ghrouter/internal/server"
	"ghrouter/internal/types"
)

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func renderLegacyLiveTUIView(m liveTUIModel) tea.View {
	if terminalTooSmall(m.width, m.height) {
		view := tea.NewView("resize terminal\nminimum: 40x8")
		view.AltScreen = true
		return view
	}
	content := []string{commandCenterHeader(m), navView(m)}
	if compactLayout(m.width, m.height) {
		content = append(content, bannerView(m), compactMetrics(m), compactDashboardPanel(m), footerView(m))
	} else {
		if m.panel == panelIndex("dashboard") {
			content = append(content, commandCenterDashboard(m), footerView(m))
		} else {
			content = append(content, bannerView(m), metricsRow(m), insightRow(m), panelView(m), footerView(m))
		}
	}
	base := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, content...))
	view := base
	if m.overlay == overlayHelp {
		view = overlayView(helpOverlay(m), m.width, m.height)
	}
	if m.overlay == overlayPalette {
		view = overlayView(paletteOverlay(m), m.width, m.height)
	}
	if m.overlay == overlayConfirm {
		view = overlayView(confirmOverlay(m), m.width, m.height)
	}
	view.AltScreen = true
	return view
}

func commandCenterHeader(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	brand := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("gh") +
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render("router")
	eyebrow := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render("CONTROL CENTER")
	tagline := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("local intelligence for every CLI workflow")
	status := statusChip(m)
	pulse := lipgloss.NewStyle().Foreground(lipgloss.Color(activityAccent(m.graphFrame))).Render(activityPulse(m.graphFrame))
	left := lipgloss.JoinHorizontal(lipgloss.Top, eyebrow, spacer(2), brand, spacer(3), pulse, spacer(2), tagline)
	if lipgloss.Width(left)+lipgloss.Width(status)+4 > width {
		return lipgloss.JoinHorizontal(lipgloss.Top, eyebrow, spacer(2), brand, spacer(2), status)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, spacer(max(width-lipgloss.Width(left)-lipgloss.Width(status), 2)), status)
}

func activityPulse(frame uint64) string {
	return []string{"·", "•", "●", "•"}[frame%4]
}

func activityAccent(frame uint64) string {
	return []string{"39", "51", "35", "51"}[frame%4]
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func commandCenterDashboard(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	if width < 106 {
		return compactCommandCenter(m)
	}
	alert := commandCenterAlert(m, width)
	stage := commandCenterStage(m, width)
	modelGraph := panel("BRAIN MODEL GRAPH", modelAvailabilityGraph(m, width), "208", width)
	rail := commandCenterRail(m, width)
	strip := commandCenterTelemetry(m, width)
	parts := []string{"", stage, modelGraph, rail, strip}
	if alert != "" {
		parts[0] = alert
	}
	return strings.Join(parts, "\n")
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func compactCommandCenter(m liveTUIModel) string {
	lines := []string{
		"GHROUTER  " + strings.ToUpper(currentBackend(m)),
		"model: " + truncateText(currentModel(m), 34),
		"flow: providers → router → compatible API",
		"health: " + healthSummaryLine(m.snapshot.Health.Providers),
		"traffic: " + fmt.Sprintf("%d requests · %d active · %d fallbacks", m.snapshot.Telemetry.Requests, m.snapshot.Telemetry.Active, m.snapshot.Telemetry.Fallbacks),
	}
	return panel("routing engine", lines, "208", panelContentWidth(m.width, 100))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func commandCenterAlert(m liveTUIModel, width int) string {
	if m.startupErr == nil && m.runtimeErr == nil && !m.runtimeFailed && !m.stale {
		return ""
	}
	message := "attention required"
	if m.runtimeFailed {
		message = "router offline"
	} else if m.stale {
		message = "stale snapshot"
	} else if m.startupErr != nil {
		message = "startup prerequisites incomplete"
	}
	detail := ""
	if m.startupErr != nil {
		detail = m.startupErr.Error()
	} else if m.runtimeErr != nil {
		detail = m.runtimeErr.Error()
	} else if m.lastAction != "" {
		detail = m.lastAction
	}
	return panel("status", []string{message, truncateText(detail, width-8)}, "196", width)
}

func commandCenterStage(m liveTUIModel, width int) string {
	nodeWidth := 30
	engineWidth := 39
	apiWidth := 39
	if width < 145 {
		nodeWidth = 26
		engineWidth = 34
		apiWidth = 34
	}
	providers := graphProviders(m)
	leftCards := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		leftCards = append(leftCards, card(
			graphProviderTitle(providers, i),
			commandCenterProviderBody(m, providers, i),
			providerGraphAccent(m, providers, i),
			nodeWidth,
		))
	}
	left := strings.Join(leftCards, "\n")
	engine := card("GHROUTER · ROUTING ENGINE", commandCenterEngineBody(m), "208", engineWidth)
	api := card("OPENAI-COMPATIBLE API", commandCenterAPIBody(m), "51", apiWidth)
	engineHeight := maxBlockHeight(engine, maxBlockHeight(left, 1))
	left = padBlock(left, engineHeight)
	engine = padBlock(engine, engineHeight)
	api = padBlock(api, engineHeight)
	inputFlow := flowColumn(engineHeight, trafficActive(m), m.graphFrame, true)
	outputFlow := flowColumn(engineHeight, trafficActive(m), m.graphFrame, false)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, inputFlow, engine, outputFlow, api)
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render(fmt.Sprintf("LIVE TOPOLOGY  ·  PROVIDERS → ROUTER → API  ·  showing %d/%d providers", len(providers), len(m.snapshot.Providers)))
	return label + "\n" + body
}

func commandCenterProviderBody(m liveTUIModel, providers []server.ProviderSnapshot, index int) []string {
	if index >= len(providers) {
		return []string{"○ empty slot", "waiting for discovery", "model: —", "adapter: —"}
	}
	p := providers[index]
	status := providerSnapshotHealth(m, p)
	model := "—"
	if len(p.Models) > 0 {
		model = p.Models[0]
	}
	return []string{
		statusGlyph(status, p.Available) + " " + providerHealthLabel(status, p.Available),
		"auth: " + shortAuth(p.Auth),
		"model: " + truncateText(model, 22),
		"adapter: " + truncateText(p.Type, 18),
	}
}

func commandCenterEngineBody(m liveTUIModel) []string {
	pulse := []string{"·", "•", "●", "•"}[m.graphFrame%4]
	return []string{
		"        " + pulse + "  route stream  " + pulse,
		"backend: " + currentBackend(m),
		"model:   " + truncateText(currentModel(m), 26),
		"port:    " + serverPort(m),
		"mode:    adaptive routing",
		"health:  " + healthSummaryLine(m.snapshot.Health.Providers),
		"",
		"routing: intent + model score",
		"fallbacks: " + fmt.Sprintf("%d configured", configuredFallbacks(m.cfg)),
		"health: live provider status",
	}
}

func configuredFallbacks(cfg *types.Config) int {
	if cfg == nil {
		return 0
	}
	total := 0
	for _, route := range cfg.Routes {
		total += len(route.Fallback)
	}
	return total
}

func commandCenterAPIBody(m liveTUIModel) []string {
	auth := "not configured"
	if len(m.snapshot.ClientKeys) > 0 {
		auth = "router keys active"
	}
	return []string{
		"status: " + apiStatus(m),
		"POST /v1/chat/completions",
		"GET  /v1/models",
		"POST /v1/messages",
		"streaming: SSE",
		"auth: " + auth,
		"ACL keys: generated",
		"gh: " + clientKey(m, "github"),
		"oa: " + clientKey(m, "openai"),
		"an: " + clientKey(m, "anthropic"),
		"requests: " + fmt.Sprint(m.snapshot.Telemetry.Requests),
		"active:   " + fmt.Sprint(m.snapshot.Telemetry.Active),
	}
}

func apiStatus(m liveTUIModel) string {
	if !m.hasSnapshot {
		return "loading"
	}
	if m.runtimeFailed || m.runtimeErr != nil {
		return "offline"
	}
	if m.startupErr != nil || !m.report.Ready() {
		return "degraded"
	}
	return "ready"
}

func clientKey(m liveTUIModel, client string) string {
	if value := m.snapshot.ClientKeys[client]; value != "" {
		return "configured (masked)"
	}
	return "not generated"
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func commandCenterRail(m liveTUIModel, width int) string {
	checks := len(m.report.Checks)
	ready := 0
	for _, check := range m.report.Checks {
		if check.Ready {
			ready++
		}
	}
	titles := []string{"SMART ROUTING", "LOAD BALANCING", "FAILOVER", "READINESS"}
	values := []string{
		routeModeLabel(m),
		fmt.Sprintf("%d providers · %d ready", len(m.snapshot.Providers), onlineProviderCount(m)),
		fmt.Sprintf("%d fallbacks", m.snapshot.Telemetry.Fallbacks),
		fmt.Sprintf("%d/%d checks", ready, checks),
	}
	cellWidth := max((width-6)/4, 18)
	parts := make([]string, 0, len(titles))
	for i := range titles {
		accent := []string{"51", "35", "208", "239"}[i]
		parts = append(parts, card(titles[i], []string{values[i]}, accent, cellWidth))
	}
	return strings.Join(joinCards(parts, 4), "\n")
}

func commandCenterTelemetry(m liveTUIModel, width int) string {
	values := []float64{float64(m.snapshot.Telemetry.Successful), float64(m.snapshot.Telemetry.Failed), float64(m.snapshot.Telemetry.Fallbacks), float64(m.snapshot.Telemetry.Active)}
	ready, catalog := m.snapshot.Health.Models.VerifiedHealthy, m.snapshot.Health.Models.Catalog
	if catalog == 0 && len(m.snapshot.Models) > 0 {
		ready, catalog = onlineModelCount(m), len(m.snapshot.Models)
	}
	line := fmt.Sprintf("$ ghrouter   %s   ·   %d ready / %d catalog models   ·   %d requests   ·   p95 %s   ·   %s", apiStatus(m), ready, catalog, m.snapshot.Telemetry.Requests, latencySummary(m), sparkline(values, max(width/5, 18)))
	return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1).Width(width).Render(line)
}

func latencySummary(m liveTUIModel) string {
	maxLatency := int64(0)
	for _, latency := range m.snapshot.Telemetry.LatencyMs {
		if latency > maxLatency {
			maxLatency = latency
		}
	}
	return fmt.Sprintf("%dms", maxLatency)
}

func routeModeLabel(m liveTUIModel) string {
	if len(m.cfg.Routes) > 0 {
		return fmt.Sprintf("%d routes", len(m.cfg.Routes))
	}
	return "auto"
}

func statusGlyph(status string, available bool) string {
	if available || strings.EqualFold(status, "healthy") {
		return "●"
	}
	if strings.EqualFold(status, "degraded") || strings.EqualFold(status, "cooldown") {
		return "◐"
	}
	return "○"
}

func flowColumn(height int, active bool, frame uint64, fromProvider bool) string {
	if height < 1 {
		return ""
	}
	lines := make([]string, height)
	for i := range lines {
		lines[i] = "   "
	}
	middle := height / 2
	if active {
		lines[middle] = movingTrafficLine(frame, fromProvider)
	} else {
		lines[middle] = "   ─────▶   "
	}
	return strings.Join(lines, "\n")
}

func maxBlockHeight(block string, fallback int) int {
	height := len(strings.Split(block, "\n"))
	if height < fallback {
		return fallback
	}
	return height
}

func padBlock(block string, height int) string {
	lines := strings.Split(block, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func statusChip(m liveTUIModel) string {
	state := "degraded"
	color := "214"
	if !m.hasSnapshot {
		state = "loading"
		color = "39"
	} else if m.stale {
		state = "stale"
		color = "214"
	} else if m.startupErr != nil || m.runtimeErr != nil || m.runtimeFailed {
		state = "error"
		color = "196"
	} else if m.report.Ready() {
		state = "ready"
		color = "35"
	}
	return lipgloss.NewStyle().
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(color)).
		Foreground(lipgloss.Color(color)).
		Render(strings.ToUpper(state))
}

func bannerView(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	lines := []string{}
	if !m.hasSnapshot {
		lines = append(lines, "connecting to runtime")
		lines = append(lines, "collecting the first live snapshot…")
	} else if m.stale && m.startupErr != nil {
		lines = append(lines, "snapshot stale")
		lines = append(lines, truncateText(fmt.Sprintf("last good refresh %s ago; %v", safeDuration(time.Since(m.lastFetch)), m.startupErr), width))
	} else if m.startupErr != nil {
		lines = append(lines, "startup error")
		lines = append(lines, truncateText(fmt.Sprintf("%v", m.startupErr), width))
	} else if m.runtimeErr != nil || m.runtimeFailed {
		lines = append(lines, "runtime error")
		if m.runtimeFailed {
			lines = append(lines, "server offline; port may be occupied")
		}
		if m.runtimeErr != nil {
			lines = append(lines, truncateText(fmt.Sprintf("%v", m.runtimeErr), width))
		}
	} else if m.report.Ready() {
		lines = append(lines, fmt.Sprintf("startup ready  backend=%s  providers=%d  %s", currentBackend(m), len(m.cfg.Providers), m.spinner.View()))
		lines = append(lines, "all prerequisites satisfied; the router is live and refreshing")
	} else {
		lines = append(lines, fmt.Sprintf("startup pending  backend=%s  providers=%d", currentBackend(m), len(m.cfg.Providers)))
		if len(m.report.Issues) == 0 {
			lines = append(lines, "no bootstrap issues reported yet")
		} else {
			for _, issue := range m.report.Issues {
				lines = append(lines, truncateText(fmt.Sprintf("%s/%s  %s", issue.Provider, issue.Model, issue.Reason), width))
			}
		}
	}
	return panel("startup", limitLines(lines, 4), "239", width)
}

func metricsRow(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	columns := metricColumns(width)
	cardWidth := gridCellWidth(width, columns)
	cards := []string{
		card("server", []string{
			fmt.Sprintf("port: %s", serverPort(m)),
			fmt.Sprintf("uptime: %s", serverUptime(m)),
			fmt.Sprintf("requests: %d", m.snapshot.Telemetry.Requests),
			fmt.Sprintf("fallbacks: %d", m.snapshot.Telemetry.Fallbacks),
		}, "39", cardWidth),
		card("brain", []string{
			fmt.Sprintf("backend: %s", currentBackend(m)),
			fmt.Sprintf("model: %s", currentModel(m)),
			fmt.Sprintf("active: %d", m.snapshot.Telemetry.Active),
			fmt.Sprintf("activity: %s", m.spinner.View()),
		}, "208", cardWidth),
		card("health", []string{
			fmt.Sprintf("healthy: %d", m.snapshot.Health.Healthy),
			fmt.Sprintf("degraded: %d", m.snapshot.Health.Degraded),
			fmt.Sprintf("unhealthy: %d", m.snapshot.Health.Unhealthy),
			fmt.Sprintf("cooldown: %d", m.snapshot.Health.Cooldown),
		}, "35", cardWidth),
		card("telemetry", []string{
			fmt.Sprintf("success: %d", m.snapshot.Telemetry.Successful),
			fmt.Sprintf("failed: %d", m.snapshot.Telemetry.Failed),
			fmt.Sprintf("providers: %d", len(m.snapshot.Telemetry.ProviderUsage)),
			fmt.Sprintf("models: %d", len(m.snapshot.Models)),
		}, "240", cardWidth),
	}
	return strings.Join(joinCards(cards, columns), "\n")
}

func metricColumns(width int) int {
	switch {
	case width >= 145:
		return 4
	case width >= 78:
		return 2
	default:
		return 1
	}
}

func gridCellWidth(total, columns int) int {
	if columns < 1 {
		columns = 1
	}
	if total <= 0 {
		return 30
	}
	return max((total-2*(columns-1))/columns, 26)
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func compactMetrics(m liveTUIModel) string {
	if !m.hasSnapshot {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("SERVER connecting  |  BRAIN loading  |  HEALTH pending  |  TELEMETRY waiting")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(fmt.Sprintf("SERVER %s  |  BRAIN %s/%s  |  HEALTH %s  |  REQUESTS %d", serverPort(m), currentBackend(m), currentModel(m), healthSummaryLine(m.snapshot.Health.Providers), m.snapshot.Telemetry.Requests))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func compactDashboardPanel(m liveTUIModel) string {
	state := "loading"
	if m.runtimeFailed {
		state = "server offline"
	} else if m.hasSnapshot && !m.report.Ready() {
		state = "attention required"
	} else if m.hasSnapshot {
		state = "ready"
	}
	issues := len(m.report.Issues)
	lines := []string{
		fmt.Sprintf("state: %s  issues: %d", state, issues),
		fmt.Sprintf("brain: %s / %s", currentBackend(m), currentModel(m)),
		fmt.Sprintf("health: %s", healthSummaryLine(m.snapshot.Health.Providers)),
	}
	if m.lastAction != "" {
		lines = append(lines, "action: "+truncateText(m.lastAction, panelContentWidth(m.width, 70)))
	}
	return panel("overview", limitLines(lines, 4), "39", panelContentWidth(m.width, 90))
}

func compactLayout(width, height int) bool {
	if height > 0 && height < 30 {
		return true
	}
	return width > 0 && width < 140 && height > 0 && height < 46
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func insightRow(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	columns := insightColumns(width)
	chartWidth := gridCellWidth(width, columns)
	request := requestChart(m)
	load := loadChart(m)
	usage := usageChart(m)
	charts := []string{
		resizeChart(request, chartWidth),
		resizeChart(load, chartWidth),
		resizeChart(usage, chartWidth),
	}
	return strings.Join(joinCards(charts, columns), "\n")
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func insightColumns(width int) int {
	switch {
	case width >= 145:
		return 3
	case width >= 78:
		return 2
	default:
		return 1
	}
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func resizeChart(rendered string, width int) string {
	lines := strings.Split(rendered, "\n")
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func panelView(m liveTUIModel) string {
	switch livePanels[m.panel] {
	case "dashboard":
		return dashboardPanel(m)
	case "brain-log":
		return activityPanel(m)
	case "providers":
		return providersPanel(m)
	case "models":
		return modelsPanel(m)
	case "routes":
		return routesPanel(m)
	case "control-plane":
		return controlPlanePanel(m)
	case "activity":
		return activityPanel(m)
	case "settings":
		return settingsPanel(m)
	default:
		return dashboardPanel(m)
	}
}

func controlPlanePanel(m liveTUIModel) string {
	lines := []string{"control plane", "e edit JSON  d delete  ↑/↓ select  r refresh", ""}
	resources := controlPlaneResources(m)
	if len(resources) == 0 {
		lines = append(lines, "no connections, pools, or combos configured")
	} else {
		for index, resource := range resources {
			marker := "  "
			if index == m.selected {
				marker = "> "
			}
			lines = append(lines, fmt.Sprintf("%s%s/%s", marker, resource.kind, resource.name))
			switch value := resource.resource.(type) {
			case types.Connection:
				lines = append(lines, fmt.Sprintf("   %s/%s enabled=%t", value.Provider, value.Model, value.Enabled))
			case types.Pool:
				lines = append(lines, fmt.Sprintf("   strategy=%s enabled=%t members=%s", value.Strategy, value.Enabled, strings.Join(value.Members, ",")))
			case types.Combo:
				lines = append(lines, fmt.Sprintf("   strategy=%s judge=%s enabled=%t members=%s", value.Strategy, value.Judge, value.Enabled, strings.Join(value.Members, ",")))
			}
		}
	}
	if m.controlPlaneEdit {
		lines = append(lines, "", "editing "+m.controlPlaneKind+"/"+m.controlPlaneName, m.input.View())
	}
	return panel("control-plane", limitLines(lines, 18), "39", panelContentWidth(m.width, 108))
}

func footerView(m liveTUIModel) string {
	tab := livePanels[m.panel]
	context := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(strings.ToUpper(tabLabel(tab))) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("  "+tabHint(tab))
	divider := lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("  ·  ")
	m.help.SetWidth(max(panelContentWidth(m.width, 120)-lipgloss.Width(context)-8, 24))
	shortHelp := m.help.View(dashboardKeyMap())
	line := context + divider + "TAB switch tabs  ·  " + shortHelp
	return lipgloss.NewStyle().Foreground(lipgloss.Color("244")).BorderTop(true).BorderForeground(lipgloss.Color("238")).PaddingTop(1).Render(
		truncateText(line, panelContentWidth(m.width, 120)),
	)
}

func navView(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	items := make([]string, 0, len(livePanels))
	for i, name := range livePanels {
		label := " " + tabLabel(name) + " "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
		if i == m.panel {
			label = "▰ " + tabLabel(name) + " "
			style = style.Bold(true).Foreground(lipgloss.Color("255")).Background(lipgloss.Color("24")).Underline(true)
		} else {
			label = "  " + label
		}
		items = append(items, style.Render(label))
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, items...)
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("  " + tabHint(livePanels[m.panel]))
	if lipgloss.Width(bar)+lipgloss.Width(meta)+2 > width {
		return bar
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, bar, spacer(max(width-lipgloss.Width(bar)-lipgloss.Width(meta), 2)), meta)
}

func tabLabel(name string) string {
	if name == "control-plane" {
		return "CONTROL PLANE"
	}
	return strings.ToUpper(name)
}

func tabHint(name string) string {
	switch name {
	case "dashboard":
		return "live routing overview"
	case "brain-log":
		return "real-time routing decisions"
	case "providers":
		return "adapters and health"
	case "models":
		return "catalog and availability"
	case "routes":
		return "intent and fallback policy"
	case "control-plane":
		return "connections, pools, combos"
	case "activity":
		return "request telemetry"
	case "settings":
		return "runtime controls"
	default:
		return "ghrouter"
	}
}

func dashboardPanel(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	graph := routingGraph(m)
	lines := []string{
		"live overview",
		fmt.Sprintf("loaded brain: %s / %s", currentBackend(m), currentModel(m)),
		fmt.Sprintf("providers online: %d / %d", onlineProviders(m), len(m.snapshot.Providers)),
		fmt.Sprintf("requests: %d  fallbacks: %d  active: %d", m.snapshot.Telemetry.Requests, m.snapshot.Telemetry.Fallbacks, m.snapshot.Telemetry.Active),
		fmt.Sprintf("config: %s", configDisplayPath(m.cfgPath)),
		fmt.Sprintf("last refresh: %s", refreshAge(m.lastFetch)),
	}
	if len(m.snapshot.Telemetry.ProviderUsage) > 0 {
		lines = append(lines, "top providers: "+topUsageLine(m.snapshot.Telemetry.ProviderUsage, 3))
	}
	if len(m.snapshot.Health.Providers) > 0 {
		lines = append(lines, "health: "+healthSummaryLine(m.snapshot.Health.Providers))
	}
	lines = append(lines, "actions: doctor, sync, reset, update, edit listen port")
	overview := panel("overview", limitLines(lines, 9), "39", dashboardColumnWidth(width, false))
	checklist := dashboardChecklist(m, dashboardColumnWidth(width, true))
	if width < 100 {
		return lipgloss.JoinVertical(lipgloss.Left, graph, overview, checklist)
	}
	return lipgloss.JoinVertical(lipgloss.Left, graph, lipgloss.JoinHorizontal(lipgloss.Top, overview, spacer(2), checklist))
}

func routingGraph(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	if width < 94 {
		lines := []string{
			"providers  →  GHROUTER  →  clients",
			graphProviderSummary(m),
			"brain: " + currentBackend(m) + " / " + currentModel(m),
			"clients: gh copilot | claude code | cursor",
		}
		lines = append(lines, modelAvailabilityGraph(m, width)...)
		return panel("routing map", lines, "208", width)
	}

	nodeWidth := 27
	centerWidth := 34
	if width >= 145 {
		nodeWidth = 31
		centerWidth = 38
	}
	providers := graphProviders(m)
	clients := []string{"gh copilot", "claude code", "cursor"}
	rows := make([]string, 0, 18)
	for i := 0; i < 3; i++ {
		left := graphNode(graphProviderTitle(providers, i), graphProviderBody(m, providers, i), providerGraphAccent(m, providers, i), nodeWidth)
		right := graphNode(clients[i], graphClientBody(m, clients[i]), "39", nodeWidth)
		center := strings.Repeat(" ", centerWidth)
		if i == 1 {
			center = graphNode("GHROUTER", graphCenterBody(m, i), "208", centerWidth)
		}
		rows = append(rows, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, left, graphConnector(m, i, true), center, graphConnector(m, i, false), right), "\n")...)
	}
	rows = append(rows, "← provider adapters                                      compatible clients →")
	rows = append(rows, modelAvailabilityGraph(m, width)...)
	return panel("routing map  ·  live topology", rows, "208", width)
}

func modelAvailabilityGraph(m liveTUIModel, width int) []string {
	models := make([]server.RoutingGraphNode, 0, len(m.snapshot.Graph.Nodes))
	for _, node := range m.snapshot.Graph.Nodes {
		if node.Kind == "model" {
			models = append(models, node)
		}
	}
	if len(models) == 0 {
		for _, model := range m.snapshot.Models {
			if !model.List {
				models = append(models, server.RoutingGraphNode{ID: "model/" + model.ID, Kind: "model", Label: model.ID, Status: model.Health, Provider: model.OwnedBy, CooldownUntil: model.CooldownUntil})
			}
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider == models[j].Provider {
			return models[i].Label < models[j].Label
		}
		return models[i].Provider < models[j].Provider
	})
	lines := []string{"", "MODEL AVAILABILITY · BRAIN CATALOG"}
	if len(models) == 0 {
		lines = append(lines, "  ○ no direct model nodes observed")
	} else {
		maxModels := 18
		for index, model := range models {
			if index == maxModels {
				lines = append(lines, fmt.Sprintf("  +%d more model nodes", len(models)-maxModels))
				break
			}
			state := modelAvailabilityState(model)
			mark := lipgloss.NewStyle().Foreground(lipgloss.Color(modelAvailabilityAccent(state))).Render(modelAvailabilityGlyph(state))
			labelWidth := width / 2
			if labelWidth < 24 {
				labelWidth = 24
			}
			label := truncateText(model.Label, labelWidth)
			lines = append(lines, "  "+mark+" "+label+" · "+state)
		}
	}
	lines = append(lines, modelAvailabilityLegend())
	return lines
}

func modelAvailabilityState(model server.RoutingGraphNode) string {
	if !model.CooldownUntil.IsZero() && time.Now().Before(model.CooldownUntil) {
		return "cooldown"
	}
	switch strings.ToLower(strings.TrimSpace(model.Status)) {
	case "healthy", "available":
		return "available"
	case "unhealthy", "unavailable", "failed":
		return "unavailable"
	case "degraded":
		return "degraded"
	case "cooldown":
		return "cooldown"
	default:
		return "unknown"
	}
}

func modelAvailabilityGlyph(state string) string {
	switch state {
	case "available":
		return "●"
	case "cooldown", "degraded":
		return "◐"
	case "unavailable":
		return "●"
	default:
		return "○"
	}
}

func modelAvailabilityAccent(state string) string {
	switch state {
	case "available":
		return "35"
	case "unavailable":
		return "196"
	case "cooldown":
		return "39"
	case "degraded":
		return "214"
	default:
		return "244"
	}
}

func modelAvailabilityLegend() string {
	items := []struct {
		state string
		label string
	}{
		{state: "available", label: "available"},
		{state: "unavailable", label: "unavailable"},
		{state: "cooldown", label: "cooldown"},
		{state: "degraded", label: "degraded"},
		{state: "unknown", label: "unknown"},
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		mark := lipgloss.NewStyle().Foreground(lipgloss.Color(modelAvailabilityAccent(item.state))).Render(modelAvailabilityGlyph(item.state))
		parts = append(parts, mark+" "+item.label)
	}
	return "  legend: " + strings.Join(parts, "  ")
}

func graphProviders(m liveTUIModel) []server.ProviderSnapshot {
	providers := sortedProviders(m.snapshot.Providers)
	if len(providers) > 3 {
		providers = providers[:3]
	}
	return providers
}

func graphProviderTitle(providers []server.ProviderSnapshot, index int) string {
	if index >= len(providers) {
		return "provider slot"
	}
	return providers[index].Name
}

func graphProviderBody(m liveTUIModel, providers []server.ProviderSnapshot, index int) []string {
	if index >= len(providers) {
		return []string{"status: empty", "waiting for discovery", "model: —", "adapter: —"}
	}
	p := providers[index]
	status := providerSnapshotHealth(m, p)
	model := "—"
	if len(p.Models) > 0 {
		model = p.Models[0]
	}
	return []string{
		"status: " + providerHealthLabel(status, p.Available),
		"auth: " + shortAuth(p.Auth),
		"model: " + truncateText(model, 20),
		"adapter: " + truncateText(p.Type, 18),
	}
}

func providerGraphAccent(m liveTUIModel, providers []server.ProviderSnapshot, index int) string {
	if index >= len(providers) {
		return "244"
	}
	return providerAccent(providerSnapshotHealth(m, providers[index]))
}

func graphCenterBody(m liveTUIModel, row int) []string {
	pulse := []string{"·", "•", "●", "•"}[m.graphFrame%4]
	if row != 1 {
		return []string{"route stream: " + pulse, "backend: " + currentBackend(m), "model: " + truncateText(currentModel(m), 22), "port: " + serverPort(m)}
	}
	return []string{"route stream: " + pulse + " live", "backend: " + currentBackend(m), "model: " + truncateText(currentModel(m), 22), "port: " + serverPort(m)}
}

func graphClientBody(m liveTUIModel, client string) []string {
	status := "ready"
	if m.runtimeFailed {
		status = "offline"
	} else if m.snapshot.Telemetry.Active > 0 {
		status = "active"
	}
	return []string{
		"status: " + status,
		"protocol: OpenAI-compatible",
		"endpoint: /v1/chat/completions",
		"requests: " + fmt.Sprint(clientRequests(m, client)),
	}
}

func clientRequests(m liveTUIModel, client string) int {
	needle := strings.ReplaceAll(strings.ToLower(client), " ", "")
	total := 0
	for _, event := range m.snapshot.Telemetry.Recent {
		endpoint := strings.ToLower(event.Endpoint)
		if strings.Contains(endpoint, needle) || (needle == "ghcopilot" && strings.Contains(endpoint, "copilot")) {
			total++
		}
	}
	return total
}

func graphConnector(m liveTUIModel, row int, fromProvider bool) string {
	line := "  ─────────▶  "
	if trafficActive(m) && row == activeGraphRow(m) {
		line = movingTrafficLine(m.graphFrame, fromProvider)
	}
	return strings.Join([]string{"      ", "      ", line, "      ", "      ", "      "}, "\n")
}

func trafficActive(m liveTUIModel) bool {
	if m.snapshot.Telemetry.Active > 0 {
		return true
	}
	if len(m.snapshot.Telemetry.Recent) == 0 {
		return false
	}
	return time.Since(m.snapshot.Telemetry.Recent[len(m.snapshot.Telemetry.Recent)-1].At) < 3*time.Second
}

func activeGraphRow(m liveTUIModel) int {
	providers := graphProviders(m)
	if len(providers) == 0 {
		return 0
	}
	if len(m.snapshot.Telemetry.Recent) > 0 {
		name := m.snapshot.Telemetry.Recent[len(m.snapshot.Telemetry.Recent)-1].Provider
		for i, provider := range providers {
			if provider.Name == name {
				return i
			}
		}
	}
	return int((m.graphFrame / 18) % uint64(minInt(len(providers), 3)))
}

func movingTrafficLine(frame uint64, fromProvider bool) string {
	const trackWidth = 9
	phase := frame % 18
	if !fromProvider {
		phase = (phase + 9) % 18
	}
	position := int(phase)
	if position >= trackWidth {
		position = 17 - position
	}
	return "  " + strings.Repeat("─", position) + "●" + strings.Repeat("─", trackWidth-position) + "▶  "
}

func graphNode(title string, lines []string, accent string, width int) string {
	return card(title, limitLines(lines, 4), accent, width)
}

func graphProviderSummary(m liveTUIModel) string {
	providers := graphProviders(m)
	if len(providers) == 0 {
		return "none detected"
	}
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.Name)
	}
	if len(m.snapshot.Providers) > len(providers) {
		names = append(names, fmt.Sprintf("+%d more", len(m.snapshot.Providers)-len(providers)))
	}
	return strings.Join(names, " | ")
}

func dashboardChecklist(m liveTUIModel, width int) string {
	checks := len(m.report.Checks)
	ready := 0
	for _, check := range m.report.Checks {
		if check.Ready {
			ready++
		}
	}
	state := "waiting for first snapshot"
	if m.hasSnapshot {
		state = "operational"
		if !m.report.Ready() {
			state = "attention required"
		}
		if m.runtimeFailed {
			state = "server offline"
		}
	}
	lines := []string{
		"runtime checklist",
		fmt.Sprintf("state: %s", state),
		fmt.Sprintf("checks: %d/%d ready %s", ready, checks, progressBar(ready, checks, 18)),
		fmt.Sprintf("endpoint: 127.0.0.1:%s", serverPort(m)),
		fmt.Sprintf("brain: %s", currentModel(m)),
		fmt.Sprintf("refresh: %s", refreshAge(m.lastFetch)),
	}
	if m.stale {
		lines = append(lines, "warning: showing last known state")
	}
	if m.actionErr != nil {
		lines = append(lines, "action: "+truncateText(m.actionErr.Error(), width-12))
	} else if m.lastAction != "" {
		lines = append(lines, "action: "+truncateText(m.lastAction, width-12))
	}
	return panel("control rail", limitLines(lines, 10), "208", width)
}

func dashboardColumnWidth(total int, right bool) int {
	if total < 100 {
		return total
	}
	width := (total - 2) / 2
	if right && total > 150 {
		return width
	}
	return width
}

func progressBar(value, total, width int) string {
	if width < 4 {
		width = 4
	}
	if total <= 0 {
		return "[" + strings.Repeat("░", width) + "]"
	}
	if value < 0 {
		value = 0
	}
	if value > total {
		value = total
	}
	filled := value * width / total
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func providersPanel(m liveTUIModel) string {
	lines := []string{"providers"}
	providers := visibleProviders(m)
	if len(providers) == 0 {
		lines = append(lines, "no providers match the current filter")
		return panel("providers", lines, "196", panelContentWidth(m.width, 100))
	}
	lines = append(lines, providerCardsGrid(m, providers)...)
	lines = append(lines, "")
	lines = append(lines, providerDetailCard(m, providers)...)
	return panel("providers", limitLines(lines, 50), "196", panelContentWidth(m.width, 100))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func modelsPanel(m liveTUIModel) string {
	lines := modelsPanelLines(m)
	return scrollablePanel("models", lines, m, "208", panelContentWidth(m.width, 100))
}

func modelsPanelLines(m liveTUIModel) []string {
	lines := []string{"models"}
	filter := liveFilter(m)
	models := append([]server.ModelSummary(nil), m.snapshot.Models...)
	if filter != "" {
		filtered := models[:0]
		for _, model := range models {
			value := strings.ToLower(model.OwnedBy + " " + model.ID + " " + strings.Join(model.Capabilities, " ") + " " + strings.Join(model.Slots, " "))
			if strings.Contains(value, filter) {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	if len(models) == 0 {
		lines = append(lines, "no models detected")
		return lines
	}
	lines = append(lines, "virtual slots:")
	slotNames := []string{"fast-code", "cheap-chat", "strong-reasoning", "long-context", "vision", "tool-use", "auto"}
	for _, slot := range slotNames {
		if model, ok := m.snapshot.Slots[slot]; ok {
			lines = append(lines, fmt.Sprintf("%s -> %s/%s | %s | %s | %dms", slot, model.OwnedBy, model.ID, model.Health, strings.Join(model.Slots, ","), model.LatencyMs))
		}
	}
	lines = append(lines, "")
	lines = append(lines, "catalog:")
	sort.Slice(models, func(i, j int) bool {
		if models[i].OwnedBy == models[j].OwnedBy {
			return models[i].ID < models[j].ID
		}
		return models[i].OwnedBy < models[j].OwnedBy
	})
	for _, model := range models {
		cost := "unknown"
		if model.TokenCost > 0 {
			cost = fmt.Sprintf("%dµ/1k", model.TokenCost)
		}
		lines = append(lines, fmt.Sprintf("%s/%s | health=%s | cost=%s (%s) | caps=%s | slots=%s | %dms | max=%d", model.OwnedBy, model.ID, model.Health, model.CostTier, cost, strings.Join(model.Capabilities, ","), strings.Join(model.Slots, ","), model.LatencyMs, model.MaxTokens))
	}
	return lines
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func routesPanel(m liveTUIModel) string {
	lines := routesPanelLines(m)
	return scrollablePanel("routes", lines, m, "214", panelContentWidth(m.width, 100))
}

func routesPanelLines(m liveTUIModel) []string {
	lines := []string{"routes"}
	filter := liveFilter(m)
	matched := 0
	if len(m.cfg.Routes) == 0 {
		lines = append(lines, "no custom routes configured")
		return lines
	}
	for _, route := range m.cfg.Routes {
		value := strings.ToLower(route.Pattern + " " + route.Provider + " " + strings.Join(route.Fallback, " "))
		if filter != "" && !strings.Contains(value, filter) {
			continue
		}
		matched++
		lines = append(lines, truncateText(fmt.Sprintf("%s -> %s", route.Pattern, route.Provider), panelContentWidth(m.width, 100)))
		if route.Mode != "" {
			lines = append(lines, "mode: "+route.Mode)
		}
		if len(route.Fallback) > 0 {
			lines = append(lines, "fallback: "+strings.Join(route.Fallback, " → "))
		}
		if route.Provider == "fusion" {
			judge := route.Judge
			if judge == "" {
				judge = "none"
			}
			policy := fmt.Sprintf("fusion: candidates=%d judge=%s", route.MaxCandidates, judge)
			if route.FirstComplete {
				policy += " first-complete"
			}
			if route.MaxCostMicros > 0 {
				policy += fmt.Sprintf(" budget=%dµ", route.MaxCostMicros)
			}
			lines = append(lines, policy)
		}
	}
	if matched == 0 {
		lines = append(lines, "no routes match the current filter")
	}
	return lines
}

func activityPanel(m liveTUIModel) string {
	width := panelContentWidth(m.width, 108)
	tableView := m.activityTable
	tableView.SetWidth(max(width-4, 32))
	height := max(m.height-14, 6)
	tableView.SetHeight(height)
	if len(tableView.Rows()) == 0 {
		return panel("activity", []string{"no requests observed yet", "the table will populate as traffic arrives"}, "240", width)
	}
	return panel("activity", strings.Split(strings.TrimSuffix(tableView.View(), "\n"), "\n"), "240", width)
}

func activityTableColumns() []table.Column {
	return []table.Column{
		{Title: "TIME", Width: 8},
		{Title: "ENDPOINT", Width: 18},
		{Title: "PROVIDER", Width: 14},
		{Title: "MODEL", Width: 24},
		{Title: "STATUS", Width: 10},
		{Title: "ROUTE", Width: 8},
	}
}

func activityTableRows(snapshot server.LiveSnapshot) []table.Row {
	rows := make([]table.Row, 0, len(snapshot.Telemetry.Recent))
	for _, ev := range snapshot.Telemetry.Recent {
		route := "direct"
		if ev.Fallback {
			route = "fallback"
		}
		rows = append(rows, table.Row{
			ev.At.Format("15:04:05"),
			truncateText(ev.Endpoint, 18),
			truncateText(ev.Provider, 14),
			truncateText(ev.Model, 24),
			truncateText(ev.Status, 10),
			route,
		})
	}
	return rows
}

func activityPanelLines(m liveTUIModel) []string {
	lines := []string{"recent activity"}
	if len(m.snapshot.Telemetry.Recent) == 0 {
		lines = append(lines, "no requests observed yet")
		return lines
	}
	for _, ev := range m.snapshot.Telemetry.Recent {
		lines = append(lines, truncateText(fmt.Sprintf("%s  %s  %s/%s  %s  fallback=%t  %s", ev.At.Format("15:04:05"), ev.Endpoint, ev.Provider, ev.Model, ev.Status, ev.Fallback, safeDuration(ev.Latency)), panelContentWidth(m.width, 108)))
	}
	return lines
}

func scrollablePanelLines(m liveTUIModel) []string {
	switch livePanels[m.panel] {
	case "models":
		return modelsPanelLines(m)
	case "routes":
		return routesPanelLines(m)
	case "brain-log":
		return activityPanelLines(m)
	case "activity":
		return activityPanelLines(m)
	default:
		return nil
	}
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func scrollablePanel(title string, lines []string, m liveTUIModel, accent string, width int) string {
	vp := m.viewport
	vp.SetWidth(max(width-4, 24))
	height := 12
	if m.height > 0 {
		height = max(m.height-12, 5)
	}
	vp.SetHeight(height)
	vp.SetContent(strings.Join(lines, "\n"))
	return panel(title, strings.Split(vp.View(), "\n"), accent, width)
}

func settingsPanel(m liveTUIModel) string {
	lines := []string{"settings"}
	lines = append(lines, fmt.Sprintf("config path: %s", configDisplayPath(m.cfgPath)))
	lines = append(lines, fmt.Sprintf("listen port: %d", m.snapshot.ListenPort))
	lines = append(lines, fmt.Sprintf("control plane: %d connections | %d pools | %d combos", len(m.snapshot.Connections), len(m.snapshot.Pools), len(m.snapshot.Combos)))
	lines = append(lines, fmt.Sprintf("mode: %s", m.settings))
	lines = append(lines, fmt.Sprintf("panel filter: %q", m.input.Value()))
	lines = append(lines, "client keys: gh="+clientKey(m, "github")+" oa="+clientKey(m, "openai")+" an="+clientKey(m, "anthropic"))
	if strings.TrimSpace(m.lastAction) != "" {
		lines = append(lines, "last action: "+truncateText(m.lastAction, panelContentWidth(m.width, 100)))
	}
	if m.actionErr != nil {
		lines = append(lines, "action error: "+truncateText(m.actionErr.Error(), panelContentWidth(m.width, 100)))
	}
	lines = append(lines, m.input.View())
	lines = append(lines, "actions: g doctor | s sync | x reset preview | X reset apply")
	lines = append(lines, "actions: u update check | U update apply | p edit port")
	lines = append(lines, "controls: enter save port | esc cancel edit | tab switch panels | r refresh | q quit")
	return panel("settings", limitLines(lines, 14), "245", panelContentWidth(m.width, 102))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func requestChart(m liveTUIModel) string {
	values := []float64{
		float64(m.snapshot.Telemetry.Successful),
		float64(m.snapshot.Telemetry.Failed),
		float64(m.snapshot.Telemetry.Fallbacks),
		float64(m.snapshot.Telemetry.Active),
	}
	return panel("request trend", []string{
		"sparkline " + sparkline(values, 18),
		fmt.Sprintf("success=%d failed=%d fallbacks=%d active=%d", m.snapshot.Telemetry.Successful, m.snapshot.Telemetry.Failed, m.snapshot.Telemetry.Fallbacks, m.snapshot.Telemetry.Active),
	}, "39", chartWidth(m.width))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func loadChart(m liveTUIModel) string {
	latencies := make([]float64, 0, len(m.snapshot.Health.Providers))
	names := make([]string, 0, len(m.snapshot.Health.Providers))
	for name := range m.snapshot.Health.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		latencies = append(latencies, float64(m.snapshot.Health.Providers[name].Latency.Milliseconds()))
	}
	if len(latencies) == 0 {
		latencies = []float64{0}
	}
	lines := []string{
		"sparkline " + sparkline(latencies, 18),
		"latency by provider",
	}
	for _, name := range names {
		state := m.snapshot.Health.Providers[name]
		lines = append(lines, truncateText(fmt.Sprintf("%s %s %s", name, state.Status, safeDuration(state.Latency)), 32))
	}
	return panel("latency", limitLines(lines, 8), "35", chartWidth(m.width))
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func usageChart(m liveTUIModel) string {
	usage := m.snapshot.Telemetry.ProviderUsage
	if len(usage) == 0 {
		return panel("usage", []string{"no provider usage yet"}, "214", chartWidth(m.width))
	}
	lines := []string{"provider usage"}
	lines = append(lines, topUsageLine(usage, 6))
	keys := make([]string, 0, len(usage))
	for k := range usage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s %s", k, usageBar(usage[k], maxUsage(usage))))
	}
	return panel("usage", limitLines(lines, 8), "214", chartWidth(m.width))
}

func helpOverlay(m liveTUIModel) string {
	lines := []string{"help", "navigate the dashboard with the same bindings used by the footer", "", m.help.FullHelpView(dashboardKeyMap().FullHelp())}
	return panel("help", lines, "245", minInt(panelContentWidth(m.width, 90), 78))
}

func paletteOverlay(m liveTUIModel) string {
	lines := []string{
		"command palette",
		"filter: " + m.palette,
	}
	for i, cmd := range matchingPaletteCommands(m.palette) {
		marker := "  "
		if i == m.paletteSel {
			marker = "> "
		}
		lines = append(lines, fmt.Sprintf("%s%s  [%s]", marker, cmd.label, cmd.key))
	}
	return panel("palette", lines, "208", minInt(panelContentWidth(m.width, 90), 90))
}

func confirmOverlay(m liveTUIModel) string {
	label := string(m.confirmKind)
	lines := []string{
		"confirm action",
		"action: " + label,
	}
	if m.confirmKind == liveActionResetApply {
		lines = append(lines, "targets: detected global CLI configuration files", "effect: reset provider CLI settings", "risk: configuration files are moved to backup")
	}
	if m.confirmKind == liveActionUpdateApply {
		lines = append(lines, "effect: apply the available Ghrouter update", "risk: replaces the current executable")
	}
	if m.confirmKind == liveActionControlDelete {
		lines = append(lines, "effect: delete "+m.controlPlaneKind+"/"+m.controlPlaneName, "risk: routing resources using it may stop resolving")
	}
	lines = append(lines, "enter to confirm",
		"esc to cancel",
	)
	return panel("confirm", lines, "196", minInt(panelContentWidth(m.width, 80), 80))
}

func overlayView(overlay string, width, height int) tea.View {
	if width <= 0 || height <= 0 {
		return tea.NewView(overlay)
	}
	return tea.NewView(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, overlay))
}

func currentBackend(m liveTUIModel) string {
	if m.report.Backend == "" {
		return "unknown"
	}
	return string(m.report.Backend)
}

func currentModel(m liveTUIModel) string {
	for _, check := range m.report.Checks {
		if check.Model != "" {
			return check.Model
		}
	}
	return "no model loaded"
}

func onlineProviders(m liveTUIModel) int {
	out := 0
	for _, p := range m.snapshot.Providers {
		if p.Available {
			out++
		}
	}
	return out
}

func shortAuth(auth string) string {
	if auth == "" {
		return "ok"
	}
	if strings.Contains(strings.ToLower(auth), "missing") {
		return "missing"
	}
	return auth
}

func accountSummary(a account.Status) string {
	if !a.Available && a.String() == "unavailable" {
		return "unavailable"
	}
	parts := make([]string, 0, 4)
	if a.Plan != "" {
		parts = append(parts, a.Plan)
	}
	if a.Balance != nil {
		parts = append(parts, fmt.Sprintf("%.2f", *a.Balance))
	}
	if a.Currency != "" {
		parts = append(parts, a.Currency)
	}
	if a.Source != "" {
		parts = append(parts, a.Source)
	}
	if len(parts) == 0 {
		return "available"
	}
	return strings.Join(parts, " ")
}

func configDisplayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "config.yaml"
	}
	return path
}

func providerCardsGrid(m liveTUIModel, providers []server.ProviderSnapshot) []string {
	cards := make([]string, 0, len(providers))
	for i, p := range providers {
		state := m.snapshot.Health.Providers[p.Name]
		status := providerSnapshotHealth(m, p)
		usage := m.snapshot.Telemetry.ProviderUsage[p.Name]
		latency := m.snapshot.Telemetry.LatencyMs[p.Name]
		lines := []string{
			fmt.Sprintf("selected: %t", i == m.selected),
			fmt.Sprintf("type: %s", p.Type),
			fmt.Sprintf("cli: %s", truncateText(p.CLIPath, 28)),
			fmt.Sprintf("health: %s", providerHealthLabel(status, p.Available)),
			fmt.Sprintf("auth: %s", shortAuth(p.Auth)),
			fmt.Sprintf("models: %d", len(p.Models)),
			fmt.Sprintf("usage: %d  latency: %dms", usage, latency),
			fmt.Sprintf("account: %s", truncateText(accountSummary(p.Account), 26)),
		}
		if state.Error != "" {
			lines = append(lines, "error: "+truncateText(state.Error, 26))
		}
		if !state.Timestamp.IsZero() {
			lines = append(lines, "seen: "+safeDuration(time.Since(state.Timestamp)))
		}
		cards = append(cards, card(strings.ToUpper(p.Name), lines, providerAccent(status), cardWidth(m.width)))
	}
	return joinCards(cards, gridColumns(m.width))
}

func providerDetailCard(m liveTUIModel, providers []server.ProviderSnapshot) []string {
	if len(providers) == 0 {
		return nil
	}
	if m.selected < 0 || m.selected >= len(providers) {
		m.selected = 0
	}
	p := providers[m.selected]
	state := m.snapshot.Health.Providers[p.Name]
	status := providerSnapshotHealth(m, p)
	lines := []string{
		"selected provider detail",
		fmt.Sprintf("name: %s", p.Name),
		fmt.Sprintf("type: %s", p.Type),
		fmt.Sprintf("cli: %s", p.CLIPath),
		fmt.Sprintf("models: %s", strings.Join(p.Models, ", ")),
		fmt.Sprintf("health: %s", providerHealthLabel(status, p.Available)),
		fmt.Sprintf("auth: %s", shortAuth(p.Auth)),
		fmt.Sprintf("account: %s", accountSummary(p.Account)),
		fmt.Sprintf("usage: %d", m.snapshot.Telemetry.ProviderUsage[p.Name]),
		fmt.Sprintf("latency: %dms", m.snapshot.Telemetry.LatencyMs[p.Name]),
	}
	if state.Error != "" {
		lines = append(lines, "error: "+truncateText(state.Error, 64))
	}
	if !state.Timestamp.IsZero() {
		lines = append(lines, "last check: "+safeDuration(time.Since(state.Timestamp))+" ago")
	}
	lines = append(lines, "keys: ↑ ↓ select provider | tab switch panel")
	return []string{panel("provider detail", lines, providerAccent(status), panelContentWidth(m.width, 100))}
}

func sortedProviders(providers []server.ProviderSnapshot) []server.ProviderSnapshot {
	out := append([]server.ProviderSnapshot(nil), providers...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func providerHealthLabel(status string, available bool) string {
	if status != "" {
		return status
	}
	if available {
		return "healthy"
	}
	return "unknown"
}

func providerAccent(status string) string {
	switch strings.ToLower(status) {
	case "healthy":
		return "35"
	case "degraded":
		return "214"
	case "unhealthy":
		return "196"
	case "cooldown":
		return "208"
	case "unavailable":
		return "196"
	default:
		return "244"
	}
}

func providerSnapshotHealth(m liveTUIModel, provider server.ProviderSnapshot) string {
	if state, ok := m.snapshot.Health.Providers[provider.Name]; ok && state.Status != "" {
		return state.Status
	}
	return providerHealthLabel(provider.Health, provider.Available)
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func onlineProviderCount(m liveTUIModel) int {
	count := 0
	for _, provider := range m.snapshot.Providers {
		status := strings.ToLower(providerSnapshotHealth(m, provider))
		if provider.Available || status == "healthy" {
			count++
		}
	}
	return count
}

func onlineModelCount(m liveTUIModel) int {
	count := 0
	for _, model := range m.snapshot.Models {
		if !model.List && strings.EqualFold(model.Health, "healthy") {
			count++
		}
	}
	return count
}

func topUsageLine(usage map[string]int, limit int) string {
	if len(usage) == 0 {
		return "none"
	}
	type pair struct {
		name  string
		count int
	}
	items := make([]pair, 0, len(usage))
	for name, count := range usage {
		items = append(items, pair{name: name, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].name < items[j].name
		}
		return items[i].count > items[j].count
	})
	if limit > len(items) {
		limit = len(items)
	}
	parts := make([]string, 0, limit)
	for _, item := range items[:limit] {
		parts = append(parts, fmt.Sprintf("%s=%d", item.name, item.count))
	}
	return strings.Join(parts, " | ")
}

func healthSummaryLine(states map[string]server.HealthState) string {
	if len(states) == 0 {
		return "none"
	}
	healthy, degraded, unhealthy, cooldown, unavailable, unknown := 0, 0, 0, 0, 0, 0
	for _, state := range states {
		switch strings.ToLower(state.Status) {
		case "healthy":
			healthy++
		case "degraded":
			degraded++
		case "unhealthy":
			unhealthy++
		case "cooldown":
			cooldown++
		case "unavailable":
			unavailable++
		default:
			unknown++
		}
	}
	return fmt.Sprintf("healthy=%d degraded=%d unhealthy=%d cooldown=%d unavailable=%d unknown=%d", healthy, degraded, unhealthy, cooldown, unavailable, unknown)
}

func usageBar(value, max int) string {
	if max <= 0 {
		max = 1
	}
	if value < 0 {
		value = 0
	}
	if value > max {
		value = max
	}
	width := 10
	filled := (value * width) / max
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func maxUsage(usage map[string]int) int {
	max := 0
	for _, count := range usage {
		if count > max {
			max = count
		}
	}
	if max == 0 {
		return 1
	}
	return max
}

func panel(title string, lines []string, accent string, width int) string {
	all := append([]string{strings.ToUpper(title)}, lines...)
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(accent)).
		Padding(0, 1)
	return style.Render(strings.Join(clampLines(all, width-4), "\n"))
}

func card(title string, lines []string, accent string, width int) string {
	if width < 26 {
		width = 26
	}
	lines = clampLines(append([]string{strings.ToUpper(title)}, lines...), width-4)
	return lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(accent)).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

func joinCards(cards []string, columns int) []string {
	if len(cards) == 0 {
		return nil
	}
	if columns < 1 {
		columns = 1
	}
	rows := make([]string, 0, (len(cards)+columns-1)/columns)
	for i := 0; i < len(cards); i += columns {
		end := i + columns
		if end > len(cards) {
			end = len(cards)
		}
		row := cards[i]
		for _, card := range cards[i+1 : end] {
			row = lipgloss.JoinHorizontal(lipgloss.Top, row, spacer(2), card)
		}
		rows = append(rows, row)
	}
	return rows
}

func gridColumns(width int) int {
	switch {
	case width >= 180:
		return 3
	case width >= 120:
		return 2
	default:
		return 1
	}
}

func cardWidth(width int) int {
	switch {
	case width >= 180:
		return 36
	case width >= 120:
		return 34
	default:
		return 30
	}
}

//lint:ignore U1000 retained as a compatibility renderer for older live layouts
func chartWidth(width int) int {
	if width <= 0 {
		return 36
	}
	if width < 120 {
		return panelContentWidth(width, 100)
	}
	return 34
}

func panelContentWidth(total int, fallback int) int {
	if total <= 0 {
		return fallback
	}
	inner := total - 6
	if inner < 24 {
		return 24
	}
	return inner
}

func terminalTooSmall(width, height int) bool {
	return width > 0 && height > 0 && (width < 40 || height < 8)
}

func liveFilter(m liveTUIModel) string {
	if !m.filterActive {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m.input.Value()))
}

func visibleProviders(m liveTUIModel) []server.ProviderSnapshot {
	providers := sortedProviders(m.snapshot.Providers)
	filter := liveFilter(m)
	if filter == "" {
		return providers
	}
	filtered := make([]server.ProviderSnapshot, 0, len(providers))
	for _, p := range providers {
		value := strings.ToLower(p.Name + " " + p.Type + " " + p.CLIPath + " " + strings.Join(p.Models, " "))
		if strings.Contains(value, filter) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func clampLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if lipgloss.Width(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, lipgloss.NewStyle().MaxWidth(width).Render(line))
	}
	return out
}

func limitLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	out := append([]string(nil), lines[:max-1]...)
	out = append(out, fmt.Sprintf("… %d more", len(lines)-max+1))
	return out
}

func truncateText(text string, width int) string {
	if width <= 0 {
		return text
	}
	if utf8.RuneCountInString(text) <= width {
		return text
	}
	if width <= 1 {
		return text[:width]
	}
	runes := []rune(text)
	return string(runes[:width-1]) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func safeDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	if d == 0 {
		return "0s"
	}
	return d.Truncate(time.Second).String()
}

func refreshAge(at time.Time) string {
	if at.IsZero() {
		return "waiting"
	}
	return safeDuration(time.Since(at)) + " ago"
}

func uptimeDisplay(at time.Time) string {
	if at.IsZero() {
		return "starting"
	}
	return safeDuration(time.Since(at))
}

func serverPort(m liveTUIModel) string {
	if m.runtimeFailed {
		return "OFFLINE"
	}
	if !m.hasSnapshot {
		return "starting"
	}
	return fmt.Sprint(m.snapshot.ListenPort)
}

func serverUptime(m liveTUIModel) string {
	if m.runtimeFailed {
		return "offline"
	}
	return uptimeDisplay(m.snapshot.StartedAt)
}

func spacer(width int) string {
	return strings.Repeat(" ", width)
}

func sparkline(values []float64, width int) string {
	if len(values) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	minValue, maxValue := values[0], values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	if maxValue == minValue {
		return strings.Repeat(string(blocks[len(blocks)-1]), min(len(values), width))
	}
	var b strings.Builder
	for i, value := range values {
		if i >= width {
			break
		}
		norm := (value - minValue) / (maxValue - minValue)
		idx := int(norm * float64(len(blocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}
