package cli

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func renderLiveTUIView(m liveTUIModel) tea.View {
	if terminalTooSmall(m.width, m.height) {
		view := tea.NewView("resize terminal\nminimum: 40x8")
		view.AltScreen = true
		return view
	}
	view := tea.NewView(modernLiveView(m))
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

func modernLiveView(m liveTUIModel) string {
	width := panelContentWidth(m.width, 120)
	if compactLayout(m.width, m.height) {
		return lipgloss.JoinVertical(lipgloss.Left, modernHeader(m), modernTabs(m), modernCompactGraph(m), modernFooter(m))
	}
	return lipgloss.JoinVertical(lipgloss.Left, modernHeader(m), modernTabs(m), modernPage(m, width), modernFooter(m))
}

func modernHeader(m liveTUIModel) string {
	brand := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("gh") + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render("router")
	state, color := modernRuntimeState(m)
	status := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color)).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(color)).Padding(0, 1).Render(strings.ToUpper(state))
	pulse := modernPulse(m.graphFrame)
	meta := fmt.Sprintf("%s  %d requests  %d active  %d models", pulse, m.snapshot.Telemetry.Requests, m.snapshot.Telemetry.Active, len(m.snapshot.Models))
	left := lipgloss.JoinHorizontal(lipgloss.Top, brand, spacer(3), lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("OPERATIONS"))
	right := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(meta), spacer(2), status)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, spacer(max(panelContentWidth(m.width, 120)-lipgloss.Width(left)-lipgloss.Width(right), 2)), right)
}

func modernTabs(m liveTUIModel) string {
	labels := map[string]string{"dashboard": "LIVE", "usage": "USAGE", "brain-log": "BRAIN LOG", "providers": "PROVIDERS", "models": "CATALOG", "routes": "ROUTES", "control-plane": "CONTROL", "activity": "ACTIVITY", "settings": "SETTINGS"}
	items := make([]string, 0, len(livePanels))
	for index, name := range livePanels {
		label := labels[name]
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
		if index == m.panel {
			style = style.Bold(true).Foreground(lipgloss.Color("255")).Background(lipgloss.Color("24")).BorderBottom(true).BorderForeground(lipgloss.Color("51"))
		}
		items = append(items, style.Render(label))
	}
	return lipgloss.NewStyle().BorderBottom(true).BorderForeground(lipgloss.Color("238")).Width(panelContentWidth(m.width, 120)).Render(strings.Join(items, "  "))
}

func modernPage(m liveTUIModel, width int) string {
	switch livePanels[m.panel] {
	case "dashboard":
		return modernGraphPage(m, width)
	case "usage":
		return modernUsagePage(m, width)
	case "brain-log":
		return modernBrainLogPage(m, width)
	case "providers":
		return modernProvidersPage(m, width)
	case "models":
		return modernModelsPage(m, width)
	case "routes":
		return modernRoutesPage(m, width)
	case "control-plane":
		return modernControlPage(m, width)
	case "activity":
		return modernActivityPage(m, width)
	case "settings":
		return modernSettingsPage(m, width)
	default:
		return modernGraphPage(m, width)
	}
}

func modernFooter(m liveTUIModel) string {
	keymap := dashboardKeyMap()
	var h help.Model = m.help
	h.SetWidth(max(panelContentWidth(m.width, 120)-30, 24))
	return lipgloss.NewStyle().Foreground(lipgloss.Color("244")).PaddingTop(1).Render(fmt.Sprintf("%s  ·  tab switch  ·  %s", strings.ToUpper(tabHint(livePanels[m.panel])), h.View(keymap)))
}

func modernRuntimeState(m liveTUIModel) (string, string) {
	if !m.hasSnapshot {
		return "loading", "39"
	}
	if m.runtimeFailed || m.runtimeErr != nil {
		return "offline", "196"
	}
	if m.stale || m.startupErr != nil || !m.report.Ready() {
		return "degraded", "214"
	}
	return "ready", "35"
}

func modernPulse(frame uint64) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color([]string{"39", "51", "35", "51"}[frame%4])).Render([]string{"·", "•", "●", "•"}[frame%4])
}

func modernCompactGraph(m liveTUIModel) string {
	state := modernRuntimeStateLabel(m)
	if !m.hasSnapshot {
		state = "SERVER connecting · BRAIN loading"
	}
	return modernPanel("LIVE GRAPH", []string{"clients  →  BRAIN  →  model catalog  →  providers", "state: " + state, "traffic: " + fmt.Sprintf("%d requests · %d active", m.snapshot.Telemetry.Requests, m.snapshot.Telemetry.Active)}, "51", panelContentWidth(m.width, 120))
}

func modernRuntimeStateLabel(m liveTUIModel) string {
	state, _ := modernRuntimeState(m)
	if m.lastFetch.IsZero() {
		return state
	}
	return state + " · refreshed " + safeDuration(time.Since(m.lastFetch)) + " ago"
}
