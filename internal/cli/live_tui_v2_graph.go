package cli

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"ghrouter/internal/server"
)

func modernGraphPage(m liveTUIModel, width int) string {
	graph := modernGraphCanvas(m, width)
	decision := modernBrainDecision(m, max((width-4)/3, 28))
	readiness := modernReadiness(m, max((width-4)/3, 28))
	traffic := modernTraffic(m, max((width-4)/3, 28))
	return strings.Join([]string{
		modernSectionHeading("LIVE / ROUTING GRAPH", "every observed node and edge in the active control plane"),
		graph,
		strings.Join(joinCards([]string{decision, readiness, traffic}, 3), "\n"),
	}, "\n\n")
}

func modernGraphCanvas(m liveTUIModel, width int) string {
	groups := map[string][]server.RoutingGraphNode{}
	for _, node := range m.snapshot.Graph.Nodes {
		groups[node.Kind] = append(groups[node.Kind], node)
	}
	for kind := range groups {
		sort.Slice(groups[kind], func(i, j int) bool { return groups[kind][i].Label < groups[kind][j].Label })
	}
	if len(groups) == 0 {
		return modernPanel("ACTIVE TOPOLOGY", []string{"waiting for the first live snapshot", "the graph will show observed clients, brain routes, lists, models and providers"}, "39", width)
	}
	columnWidth := max((width-18)/5, 19)
	active := m.snapshot.Telemetry.Active > 0
	columns := []string{
		modernNodeColumn("CLIENTS", groups["client"], columnWidth),
		modernGraphLink(m.graphFrame, active),
		modernNodeColumn("BRAIN", groups["brain"], columnWidth),
		modernGraphLink(m.graphFrame, active),
		modernNodeColumn("MODEL GRAPH", append(groups["list"], groups["route"]...), columnWidth),
		modernGraphLink(m.graphFrame, active),
		modernNodeColumn("MODELS", groups["model"], columnWidth),
		modernGraphLink(m.graphFrame, active),
		modernNodeColumn("PROVIDERS", groups["provider"], columnWidth),
	}
	return modernPanel("ACTIVE TOPOLOGY  ·  "+modernEdgeState(m), []string{lipgloss.JoinHorizontal(lipgloss.Top, columns...)}, "51", width)
}

func modernEdgeState(m liveTUIModel) string {
	if m.snapshot.Telemetry.Active > 0 {
		return "traffic moving"
	}
	return "idle · awaiting request"
}

func modernSectionHeading(title, detail string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render(title), spacer(2), lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(detail))
}

func modernBrainDecision(m liveTUIModel, width int) string {
	lines := []string{
		"backend  " + currentBackend(m),
		"model    " + currentModel(m),
		"policy   " + routeModeLabel(m),
		"health   " + healthSummaryLine(m.snapshot.Health.Providers),
	}
	if len(m.snapshot.Telemetry.Recent) > 0 {
		event := m.snapshot.Telemetry.Recent[0]
		lines = append(lines, "last    "+event.Provider+"/"+event.Model)
	}
	return modernPanel("BRAIN DECISION", lines, "208", width)
}

func modernReadiness(m liveTUIModel, width int) string {
	ready := onlineModelCount(m)
	total := len(m.snapshot.Models)
	lines := []string{
		fmt.Sprintf("%d / %d verified catalog models", ready, total),
		modernProgress(ready, total, max(width-4, 12)),
		"green means selectable; unavailable stays visible",
	}
	return modernPanel("CATALOG READINESS", lines, "35", width)
}

func modernTraffic(m liveTUIModel, width int) string {
	lines := []string{
		fmt.Sprintf("success  %d", m.snapshot.Telemetry.Successful),
		fmt.Sprintf("failed   %d", m.snapshot.Telemetry.Failed),
		fmt.Sprintf("fallback %d", m.snapshot.Telemetry.Fallbacks),
		fmt.Sprintf("latency  %s", latencySummary(m)),
	}
	return modernPanel("TRAFFIC SIGNAL", lines, "39", width)
}
