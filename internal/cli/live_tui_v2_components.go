package cli

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"

	"ghrouter/internal/server"
)

type modernListItem struct {
	title       string
	description string
}

func (i modernListItem) FilterValue() string { return i.title + " " + i.description }
func (i modernListItem) Title() string       { return i.title }
func (i modernListItem) Description() string { return i.description }

func modernPanel(title string, lines []string, accent string, width int) string {
	if width < 20 {
		width = 20
	}
	body := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(accent)).Padding(0, 1).Render(
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(accent)).Render(title) + "\n" + body,
	)
}

func modernCard(title, value, detail, accent string, width int) string {
	lines := []string{lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(value), lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(detail)}
	return modernPanel(title, lines, accent, width)
}

func modernHealthState(status string, available bool) (string, string) {
	if available || strings.EqualFold(status, "healthy") || strings.EqualFold(status, "available") {
		return "●", "35"
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "cooldown":
		return "◐", "39"
	case "degraded":
		return "◐", "214"
	case "unhealthy", "unavailable", "failed":
		return "●", "196"
	default:
		return "○", "244"
	}
}

func modernNode(node server.RoutingGraphNode, width int) string {
	glyph, color := modernHealthState(node.Status, strings.EqualFold(node.Status, "available"))
	label := truncateText(node.Label, max(width-8, 12))
	meta := node.Kind
	if node.Provider != "" {
		meta += " · " + node.Provider
	}
	if node.LatencySamples > 0 {
		meta += fmt.Sprintf(" · p50 %dms · p95 %dms", node.LatencyMs, node.LatencyP95Ms)
	}
	return lipgloss.NewStyle().Width(width).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(color)).Padding(0, 1).Render(
		lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(glyph) + " " + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(label) + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(meta),
	)
}

func modernNodeColumn(title string, nodes []server.RoutingGraphNode, width int) string {
	lines := []string{lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render(title)}
	if len(nodes) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("○ no observed nodes"))
		return strings.Join(lines, "\n")
	}
	items := make([]string, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, modernNode(node, width))
	}
	return strings.Join(append(lines, items...), "\n")
}

func modernGraphLink(frame uint64, active bool) string {
	if !active {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("──────▶")
	}
	particles := []string{"─·──▶", "──•─▶", "──●─▶", "──•─▶"}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color([]string{"39", "51", "35", "51"}[frame%4])).Render(particles[frame%4])
}

func modernProgress(value, total int, width int) string {
	bar := progress.New(progress.WithWidth(max(width, 12)), progress.WithoutPercentage())
	if total <= 0 {
		return bar.ViewAs(0)
	}
	percent := float64(value) / float64(total)
	if percent > 1 {
		percent = 1
	}
	return bar.ViewAs(percent)
}

func modernProviderList(m liveTUIModel, width, height int) string {
	items := make([]list.Item, 0, len(m.snapshot.Providers))
	for _, provider := range sortedProviders(m.snapshot.Providers) {
		glyph, color := modernHealthState(provider.Health, provider.Available)
		items = append(items, modernListItem{title: glyph + " " + provider.Name, description: provider.Type + " · " + provider.Auth})
		_ = color
	}
	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(2)
	component := list.New(items, delegate, max(width, 24), max(height, 6))
	component.SetShowTitle(false)
	component.SetShowStatusBar(false)
	component.SetShowHelp(false)
	component.SetFilteringEnabled(false)
	if len(items) > 0 {
		component.Select(min(m.selected, len(items)-1))
	}
	return component.View()
}

func modernStatusLine(status string, available bool) string {
	glyph, color := modernHealthState(status, available)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(glyph) + " " + status
}

//lint:ignore U1000 retained for compatibility with the legacy metrics renderer
func modernCountDetail(value int, noun string) string {
	return fmt.Sprintf("%d %s", value, noun)
}
