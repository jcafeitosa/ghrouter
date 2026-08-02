package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ghrouter/internal/account"
	"ghrouter/internal/server"
)

func renderLiveTUIView(m liveTUIModel) tea.View {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render("Ghrouter — Router Dashboard")
	subtitle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("operator control room")
	status := statusChip(m)

	header := lipgloss.JoinHorizontal(lipgloss.Top, title, spacer(2), subtitle, spacer(2), status)
	content := []string{
		header,
		bannerView(m),
		metricsRow(m),
		insightRow(m),
		panelView(m),
		footerView(m),
	}
	base := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, content...))
	view := base
	if m.overlay == overlayHelp {
		view = overlayView(base, helpOverlay(m))
	}
	if m.overlay == overlayPalette {
		view = overlayView(base, paletteOverlay(m))
	}
	if m.overlay == overlayConfirm {
		view = overlayView(base, confirmOverlay(m))
	}
	view.AltScreen = true
	return view
}

func statusChip(m liveTUIModel) string {
	state := "degraded"
	color := "214"
	if m.startupErr != nil || m.runtimeErr != nil {
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
	width := panelContentWidth(m.width, 90)
	lines := []string{}
	if m.startupErr != nil {
		lines = append(lines, "startup error")
		lines = append(lines, truncateText(fmt.Sprintf("%v", m.startupErr), width))
	} else if m.runtimeErr != nil {
		lines = append(lines, "runtime error")
		lines = append(lines, truncateText(fmt.Sprintf("%v", m.runtimeErr), width))
	} else if m.report.Ready() {
		lines = append(lines, fmt.Sprintf("startup ready  backend=%s  providers=%d  frame=%s", currentBackend(m), len(m.cfg.Providers), liveFrames[m.frame%len(liveFrames)]))
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
	width := m.width
	vertical := width > 0 && width < 150

	serverCard := card("server", []string{
		fmt.Sprintf("port: %d", m.snapshot.ListenPort),
		fmt.Sprintf("uptime: %s", safeDuration(time.Since(m.snapshot.StartedAt))),
		fmt.Sprintf("requests: %d", m.snapshot.Telemetry.Requests),
		fmt.Sprintf("fallbacks: %d", m.snapshot.Telemetry.Fallbacks),
	}, "39", cardWidth(width))

	brainCard := card("brain", []string{
		fmt.Sprintf("backend: %s", currentBackend(m)),
		fmt.Sprintf("model: %s", currentModel(m)),
		fmt.Sprintf("active: %d", m.snapshot.Telemetry.Active),
		fmt.Sprintf("frame: %s", liveFrames[m.frame%len(liveFrames)]),
	}, "208", cardWidth(width))

	healthCard := card("health", []string{
		fmt.Sprintf("healthy: %d", m.snapshot.Health.Healthy),
		fmt.Sprintf("degraded: %d", m.snapshot.Health.Degraded),
		fmt.Sprintf("unhealthy: %d", m.snapshot.Health.Unhealthy),
		fmt.Sprintf("cooldown: %d", m.snapshot.Health.Cooldown),
	}, "35", cardWidth(width))

	telemetryCard := card("telemetry", []string{
		fmt.Sprintf("success: %d", m.snapshot.Telemetry.Successful),
		fmt.Sprintf("failed: %d", m.snapshot.Telemetry.Failed),
		fmt.Sprintf("providers: %d", len(m.snapshot.Telemetry.ProviderUsage)),
		fmt.Sprintf("models: %d", len(m.snapshot.Models)),
	}, "240", cardWidth(width))

	if vertical {
		return lipgloss.JoinVertical(lipgloss.Left, serverCard, healthCard, telemetryCard, brainCard)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, serverCard, spacer(2), brainCard, spacer(2), healthCard, spacer(2), telemetryCard)
}

func insightRow(m liveTUIModel) string {
	width := m.width
	request := requestChart(m)
	load := loadChart(m)
	usage := usageChart(m)
	if width > 0 && width < 150 {
		return lipgloss.JoinVertical(lipgloss.Left, request, load, usage)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, request, spacer(2), load, spacer(2), usage)
}

func panelView(m liveTUIModel) string {
	switch livePanels[m.panel] {
	case "dashboard":
		return dashboardPanel(m)
	case "providers":
		return providersPanel(m)
	case "models":
		return modelsPanel(m)
	case "routes":
		return routesPanel(m)
	case "activity":
		return activityPanel(m)
	case "settings":
		return settingsPanel(m)
	default:
		return dashboardPanel(m)
	}
}

func footerView(m liveTUIModel) string {
	parts := make([]string, 0, len(livePanels)+1)
	for i, name := range livePanels {
		label := name
		if i == m.panel {
			label = "[" + name + "]"
		}
		parts = append(parts, label)
	}
	parts = append(parts, "r refresh", "tab switch", "shift+tab back", "? help", "ctrl+p palette", "q quit")
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("244")).
		Render(truncateText(strings.Join(parts, "   "), panelContentWidth(m.width, 120)))
}

func dashboardPanel(m liveTUIModel) string {
	lines := []string{
		"live overview",
		fmt.Sprintf("loaded brain: %s / %s", currentBackend(m), currentModel(m)),
		fmt.Sprintf("providers online: %d / %d", onlineProviders(m), len(m.snapshot.Providers)),
		fmt.Sprintf("requests: %d  fallbacks: %d  active: %d", m.snapshot.Telemetry.Requests, m.snapshot.Telemetry.Fallbacks, m.snapshot.Telemetry.Active),
		fmt.Sprintf("config: %s", configDisplayPath(m.cfgPath)),
		fmt.Sprintf("last refresh: %s ago", safeDuration(time.Since(m.lastFetch))),
	}
	if len(m.snapshot.Telemetry.ProviderUsage) > 0 {
		lines = append(lines, "top providers: "+topUsageLine(m.snapshot.Telemetry.ProviderUsage, 3))
	}
	if len(m.snapshot.Health.Providers) > 0 {
		lines = append(lines, "health: "+healthSummaryLine(m.snapshot.Health.Providers))
	}
	lines = append(lines, "actions: doctor, sync, reset, update, edit listen port")
	return panel("overview", limitLines(lines, 8), "39", panelContentWidth(m.width, 96))
}

func providersPanel(m liveTUIModel) string {
	lines := []string{"providers"}
	filter := strings.ToLower(strings.TrimSpace(m.input.Value()))
	providers := sortedProviders(m.snapshot.Providers)
	if filter != "" {
		filtered := make([]server.ProviderSnapshot, 0, len(providers))
		for _, p := range providers {
			if strings.Contains(strings.ToLower(p.Name+" "+p.Type+" "+p.CLIPath+" "+strings.Join(p.Models, " ")), filter) {
				filtered = append(filtered, p)
			}
		}
		providers = filtered
	}
	if len(providers) == 0 {
		lines = append(lines, "no providers match the current filter")
		return panel("providers", lines, "196", panelContentWidth(m.width, 100))
	}
	lines = append(lines, providerCardsGrid(m, providers)...)
	lines = append(lines, "")
	lines = append(lines, providerDetailCard(m, providers)...)
	return panel("providers", limitLines(lines, 50), "196", panelContentWidth(m.width, 100))
}

func modelsPanel(m liveTUIModel) string {
	lines := []string{"models"}
	if len(m.snapshot.Models) == 0 {
		lines = append(lines, "no models detected")
		return panel("models", lines, "208", panelContentWidth(m.width, 100))
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
	sort.Slice(m.snapshot.Models, func(i, j int) bool {
		if m.snapshot.Models[i].OwnedBy == m.snapshot.Models[j].OwnedBy {
			return m.snapshot.Models[i].ID < m.snapshot.Models[j].ID
		}
		return m.snapshot.Models[i].OwnedBy < m.snapshot.Models[j].OwnedBy
	})
	for _, model := range m.snapshot.Models {
		lines = append(lines, fmt.Sprintf("%s/%s | health=%s | cost=%s | caps=%s | slots=%s | %dms | max=%d", model.OwnedBy, model.ID, model.Health, model.CostTier, strings.Join(model.Capabilities, ","), strings.Join(model.Slots, ","), model.LatencyMs, model.MaxTokens))
	}
	return panel("models", limitLines(lines, 40), "208", panelContentWidth(m.width, 100))
}

func routesPanel(m liveTUIModel) string {
	lines := []string{"routes"}
	if len(m.cfg.Routes) == 0 {
		lines = append(lines, "no custom routes configured")
		return panel("routes", lines, "214", panelContentWidth(m.width, 100))
	}
	for _, route := range m.cfg.Routes {
		lines = append(lines, truncateText(fmt.Sprintf("%s -> %s", route.Pattern, route.Provider), panelContentWidth(m.width, 100)))
		if len(route.Fallback) > 0 {
			lines = append(lines, "fallback: "+strings.Join(route.Fallback, " → "))
		}
	}
	return panel("routes", limitLines(lines, 40), "214", panelContentWidth(m.width, 100))
}

func activityPanel(m liveTUIModel) string {
	lines := []string{"recent activity"}
	if len(m.snapshot.Telemetry.Recent) == 0 {
		lines = append(lines, "no requests observed yet")
		return panel("activity", lines, "240", panelContentWidth(m.width, 108))
	}
	for _, ev := range m.snapshot.Telemetry.Recent {
		lines = append(lines, truncateText(fmt.Sprintf("%s  %s  %s/%s  %s  fallback=%t  %s", ev.At.Format("15:04:05"), ev.Endpoint, ev.Provider, ev.Model, ev.Status, ev.Fallback, safeDuration(ev.Latency)), panelContentWidth(m.width, 108)))
	}
	return panel("activity", limitLines(lines, 10), "240", panelContentWidth(m.width, 108))
}

func settingsPanel(m liveTUIModel) string {
	lines := []string{"settings"}
	lines = append(lines, fmt.Sprintf("config path: %s", configDisplayPath(m.cfgPath)))
	lines = append(lines, fmt.Sprintf("listen port: %d", m.snapshot.ListenPort))
	lines = append(lines, fmt.Sprintf("mode: %s", m.settings))
	lines = append(lines, fmt.Sprintf("panel filter: %q", m.input.Value()))
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
	lines := []string{
		"help",
		"tab / shift+tab: switch sections",
		"↑ ↓ or j / k: move selection",
		"/ or ctrl+p: open palette",
		"enter: drill in or confirm",
		"esc: close overlay or cancel",
		"r: refresh snapshot",
		"q: quit",
	}
	return panel("help", lines, "245", minInt(panelContentWidth(m.width, 90), 78))
}

func paletteOverlay(m liveTUIModel) string {
	lines := []string{
		"command palette",
		"filter: " + m.palette,
	}
	for _, cmd := range paletteCommands {
		if m.palette == "" || strings.Contains(strings.ToLower(cmd.label+" "+cmd.key), m.palette) {
			lines = append(lines, fmt.Sprintf("%s  [%s]", cmd.label, cmd.key))
		}
	}
	return panel("palette", lines, "208", minInt(panelContentWidth(m.width, 90), 90))
}

func confirmOverlay(m liveTUIModel) string {
	label := string(m.confirmKind)
	lines := []string{
		"confirm action",
		"action: " + label,
		"enter to confirm",
		"esc to cancel",
	}
	return panel("confirm", lines, "196", minInt(panelContentWidth(m.width, 80), 80))
}

func overlayView(base tea.View, overlay string) tea.View {
	return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, base.Content, overlay))
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
		usage := m.snapshot.Telemetry.ProviderUsage[p.Name]
		latency := m.snapshot.Telemetry.LatencyMs[p.Name]
		lines := []string{
			fmt.Sprintf("selected: %t", i == m.selected),
			fmt.Sprintf("type: %s", p.Type),
			fmt.Sprintf("cli: %s", truncateText(p.CLIPath, 28)),
			fmt.Sprintf("health: %s", providerHealthLabel(state.Status, p.Available)),
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
		cards = append(cards, card(strings.ToUpper(p.Name), lines, providerAccent(state.Status), cardWidth(m.width)))
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
	lines := []string{
		"selected provider detail",
		fmt.Sprintf("name: %s", p.Name),
		fmt.Sprintf("type: %s", p.Type),
		fmt.Sprintf("cli: %s", p.CLIPath),
		fmt.Sprintf("models: %s", strings.Join(p.Models, ", ")),
		fmt.Sprintf("health: %s", providerHealthLabel(state.Status, p.Available)),
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
	return []string{panel("provider detail", lines, providerAccent(state.Status), panelContentWidth(m.width, 100))}
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
	default:
		return "244"
	}
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
	healthy, degraded, unhealthy, cooldown, unknown := 0, 0, 0, 0, 0
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
		default:
			unknown++
		}
	}
	return fmt.Sprintf("healthy=%d degraded=%d unhealthy=%d cooldown=%d unknown=%d", healthy, degraded, unhealthy, cooldown, unknown)
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

func clampLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, truncateText(line, width))
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
