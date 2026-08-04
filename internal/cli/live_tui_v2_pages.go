package cli

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/paginator"
	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"

	"ghrouter/internal/server"
	"ghrouter/internal/types"
)

func modernUsagePage(m liveTUIModel, width int) string {
	telemetry := m.snapshot.Telemetry
	successRate := "unknown"
	if telemetry.Requests > 0 {
		successRate = fmt.Sprintf("%.1f%%", float64(telemetry.Successful)*100/float64(telemetry.Requests))
	}
	kpiWidth := max((width-6)/4, 18)
	kpis := lipgloss.JoinHorizontal(lipgloss.Top,
		modernCard("REQUESTS", fmt.Sprint(telemetry.Requests), "observed since start", "51", kpiWidth),
		spacer(2), modernCard("SUCCESS RATE", successRate, fmt.Sprintf("%d failed", telemetry.Failed), "35", kpiWidth),
		spacer(2), modernCard("FALLBACKS", fmt.Sprint(telemetry.Fallbacks), "provider retries", "208", kpiWidth),
		spacer(2), modernCard("ACTIVE", fmt.Sprint(telemetry.Active), "in flight now", "39", kpiWidth),
	)
	chart := modelUsageChart(telemetry.ModelUsage, telemetry.ModelLatency, max(width/2, 42))
	latency := modelLatencyLines(telemetry.ModelLatency, max(width/2-4, 32))
	return strings.Join([]string{
		modernSectionHeading("MODEL USAGE", "cumulative observed traffic; no synthetic history"),
		kpis,
		lipgloss.JoinHorizontal(lipgloss.Top, modernPanel("REQUESTS BY MODEL", chart, "51", max(width/2-1, 38)), spacer(2), modernPanel("LATENCY", latency, "208", max(width/2-1, 38))),
	}, "\n\n")
}

func modelUsageChart(usage map[string]int, latency map[string]server.ModelLatencySnapshot, width int) []string {
	if len(usage) == 0 {
		return []string{"no model request observed yet", "send a request to populate this chart"}
	}
	keys := sortedUsageKeys(usage)
	lines := make([]string, 0, min(len(keys), 8)+1)
	maxValue := 0
	for _, key := range keys {
		if usage[key] > maxValue {
			maxValue = usage[key]
		}
	}
	for _, key := range keys[:min(len(keys), 8)] {
		label := truncateText(key, max(width-18, 16))
		line := fmt.Sprintf("%-*s %4d %s", max(width/4, 16), label, usage[key], usageBar(usage[key], maxValue))
		if sample := latency[key]; sample.Samples > 0 {
			line += fmt.Sprintf("  p50 %dms", sample.P50Ms)
		}
		lines = append(lines, line)
	}
	return lines
}

func modelLatencyLines(latency map[string]server.ModelLatencySnapshot, width int) []string {
	if len(latency) == 0 {
		return []string{"no latency sample observed yet"}
	}
	keys := make([]string, 0, len(latency))
	for key := range latency {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, min(len(keys), 8))
	for _, key := range keys[:min(len(keys), 8)] {
		sample := latency[key]
		lines = append(lines, fmt.Sprintf("%-*s %d/%dms · n=%d", max(width-24, 12), truncateText(key, max(width-24, 12)), sample.P50Ms, sample.P95Ms, sample.Samples))
	}
	return lines
}

func sortedUsageKeys(usage map[string]int) []string {
	keys := make([]string, 0, len(usage))
	for key := range usage {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if usage[keys[i]] == usage[keys[j]] {
			return keys[i] < keys[j]
		}
		return usage[keys[i]] > usage[keys[j]]
	})
	return keys
}

func modernProvidersPage(m liveTUIModel, width int) string {
	providers := sortedProviders(m.snapshot.Providers)
	listWidth := max(width/3, 30)
	detailWidth := max(width-listWidth-4, 42)
	detail := []string{"select a provider to inspect its observed state"}
	if len(providers) > 0 {
		selected := providers[m.selected%len(providers)]
		catalogModels := selected.CatalogModels
		if len(catalogModels) == 0 {
			catalogModels = selected.Models
		}
		detail = []string{
			"name     " + selected.Name,
			"adapter  " + selected.Type,
			"binary   " + selected.CLIPath,
			"auth     " + selected.Auth,
			"health   " + modernStatusLine(selected.Health, selected.Available),
			"routable " + strings.Join(selected.Models, ", "),
			"catalog  " + strings.Join(catalogModels, ", "),
			"account  " + selected.Account.String(),
		}
	}
	left := modernPanel("PROVIDER FLEET", []string{modernProviderList(m, listWidth, max(m.height-14, 8))}, "51", listWidth)
	right := modernPanel("SELECTED PROVIDER", detail, "208", detailWidth)
	return strings.Join([]string{modernSectionHeading("PROVIDERS", "observed adapters, authentication and catalog state"), lipgloss.JoinHorizontal(lipgloss.Top, left, spacer(2), right)}, "\n\n")
}

func modernModelsPage(m liveTUIModel, width int) string {
	rows := make([]table.Row, 0, len(m.snapshot.Models))
	for _, model := range m.snapshot.Models {
		state, _ := modernHealthState(model.Health, strings.EqualFold(model.Health, "healthy"))
		kind := "model"
		if model.List {
			kind = "list"
		}
		latency := m.snapshot.Telemetry.ModelLatency[model.ID]
		latencyText := "unknown"
		if latency.Samples > 0 {
			latencyText = fmt.Sprintf("%d/%dms", latency.P50Ms, latency.P95Ms)
		}
		rows = append(rows, table.Row{state + " " + truncateText(model.ID, 28), truncateText(model.OwnedBy, 16), model.Health, kind, latencyText})
	}
	columns := []table.Column{{Title: "MODEL", Width: 31}, {Title: "OWNER", Width: 16}, {Title: "STATE", Width: 12}, {Title: "KIND", Width: 8}, {Title: "P50/P95", Width: 12}}
	component := table.New(table.WithColumns(columns), table.WithRows(rows), table.WithWidth(width), table.WithHeight(max(m.height-14, 8)), table.WithFocused(true))
	pager := paginator.New(paginator.WithPerPage(max(m.height-16, 1)))
	pager.SetTotalPages(len(rows))
	detail := fmt.Sprintf("%d catalog entries · %s", len(rows), pager.View())
	return strings.Join([]string{modernSectionHeading("MODEL CATALOG", "availability is evidence-backed; unknown is not healthy"), component.View(), lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(detail)}, "\n")
}

func modernRoutesPage(m liveTUIModel, width int) string {
	lines := make([]string, 0, len(m.cfg.Routes)+2)
	if len(m.cfg.Routes) == 0 {
		lines = append(lines, "no explicit routes configured", "automatic brain selection remains the active policy")
	} else {
		for _, route := range m.cfg.Routes {
			fallback := "none"
			if len(route.Fallback) > 0 {
				fallback = strings.Join(route.Fallback, " → ")
			}
			lines = append(lines, fmt.Sprintf("%-24s  %-14s  mode=%-12s  fallback=%s", route.Pattern, route.Provider, route.Mode, fallback))
		}
	}
	return strings.Join([]string{modernSectionHeading("ROUTES", "deterministic policy before adaptive selection"), modernPanel("ROUTING TABLE", lines, "208", width)}, "\n\n")
}

func modernControlPage(m liveTUIModel, width int) string {
	lines := []string{"connections  " + fmt.Sprint(len(m.snapshot.Connections)), "pools        " + fmt.Sprint(len(m.snapshot.Pools)), "combos       " + fmt.Sprint(len(m.snapshot.Combos)), "", "↑/↓ select  e edit JSON  d delete  r refresh"}
	for _, resource := range controlPlaneResources(m) {
		lines = append(lines, resource.kind+"/"+resource.name)
	}
	if m.controlPlaneEdit {
		lines = append(lines, "", "editing "+m.controlPlaneKind+"/"+m.controlPlaneName, m.input.View())
	}
	return strings.Join([]string{modernSectionHeading("CONTROL PLANE", "connections, pools and combos are live resources"), modernPanel("RESOURCES", lines, "39", width)}, "\n\n")
}

func modernActivityPage(m liveTUIModel, width int) string {
	component := m.activityTable
	component.SetWidth(width)
	component.SetHeight(max(m.height-16, 8))
	timeline := []string{"REQUEST TIMELINE"}
	for _, event := range m.snapshot.Telemetry.Recent {
		route := "direct"
		if event.Fallback {
			route = "fallback"
		}
		timeline = append(timeline, fmt.Sprintf("%s  %s  %s/%s  %s", event.At.Format("15:04:05"), route, event.Provider, event.Model, event.Status))
	}
	if len(timeline) == 1 {
		timeline = append(timeline, "no request observed yet")
	}
	return strings.Join([]string{modernSectionHeading("ACTIVITY", "request outcomes and routing decisions"), lipgloss.JoinHorizontal(lipgloss.Top, modernPanel("REQUESTS", []string{component.View()}, "51", max(width-42, 40)), spacer(2), modernPanel("DECISIONS", timeline, "208", 38))}, "\n\n")
}

func modernBrainLogPage(m liveTUIModel, width int) string {
	lines := []string{"BRAIN DECISION STREAM"}
	if len(m.snapshot.Telemetry.Recent) == 0 {
		lines = append(lines, "no Brain decision observed yet", "this panel becomes live after the first routed request")
	} else {
		for _, event := range m.snapshot.Telemetry.Recent {
			decision := strings.TrimSpace(event.DecisionJSON)
			if decision == "" {
				decision = "decision metadata unavailable"
			}
			fallback := "direct"
			if event.Fallback {
				fallback = "fallback"
			}
			lines = append(lines, fmt.Sprintf("%s  %s  %s/%s  %s  %s", event.At.Format("15:04:05"), fallback, event.Provider, event.Model, event.Status, truncateText(decision, max(width-46, 24))))
			for _, attempt := range event.Attempts {
				lines = append(lines, fmt.Sprintf("  └ attempt %s/%s  %s  %s", attempt.Provider, attempt.Model, attempt.Status, safeDuration(attempt.Latency)))
			}
		}
	}
	return strings.Join([]string{modernSectionHeading("BRAIN LOG", "live routing decisions from the active request stream"), modernPanel("DECISION STREAM", lines, "208", width)}, "\n\n")
}

func modernSettingsPage(m liveTUIModel, width int) string {
	host := m.report.Host
	left := modernPanel("RUNTIME", []string{"config   " + configDisplayPath(m.cfgPath), "port     " + serverPort(m), "backend  " + currentBackend(m), fmt.Sprintf("host     %s/%s", host.OS, host.Architecture), fmt.Sprintf("mlx/llama %t/%t", host.MLXAvailable, host.LlamaCppAvailable), "refresh  every 2s", "state    " + modernRuntimeStateLabel(m)}, "39", max((width-2)/2, 36))
	actions := []string{"g  doctor", "s  sync providers and models", "n  add/update NVIDIA account", "x  preview reset", "X  apply reset", "u  check update", "U  apply update", "p  edit listen port", "", "last  " + truncateText(m.lastAction, 40)}
	if isNVIDIASettingsMode(m.settings) {
		actions = append(actions, "", "NVIDIA account setup", "enter  next field / save", "esc     cancel", "input   "+m.input.View())
	}
	right := modernPanel("ACTIONS", actions, "208", max((width-2)/2, 36))
	accounts := modernPanel("NVIDIA NIM ACCOUNTS", nvidiaAccountLines(m.cfg), "51", max(width-2, 72))
	return strings.Join([]string{modernSectionHeading("SETTINGS", "safe operational controls for the local router"), accounts, lipgloss.JoinHorizontal(lipgloss.Top, left, spacer(2), right)}, "\n\n")
}

func isNVIDIASettingsMode(mode settingsMode) bool {
	switch mode {
	case settingsModeNVIDIAName, settingsModeNVIDIAEnv, settingsModeNVIDIAKey:
		return true
	default:
		return false
	}
}

func nvidiaAccountLines(cfg *types.Config) []string {
	lines := []string{"keys are masked; values are never rendered"}
	if cfg == nil {
		return append(lines, "configuration unavailable")
	}
	for _, provider := range cfg.Providers {
		if provider == nil || (provider.Name != "nvidia" && provider.Type != types.ProviderNVIDIA) {
			continue
		}
		if len(provider.Accounts) == 0 {
			return append(lines, "no accounts configured", "press n to add one")
		}
		for _, account := range provider.Accounts {
			credential := "no key"
			if account.APIKeyEnv != "" {
				credential = "env:" + account.APIKeyEnv
			} else if account.APIKey != "" {
				credential = "inline:key"
			}
			state := "disabled"
			if account.Enabled {
				state = "enabled"
			}
			lines = append(lines, fmt.Sprintf("%-18s %-8s %s", truncateText(account.Name, 18), state, credential))
		}
		return lines
	}
	return append(lines, "NVIDIA provider not configured", "press n to create it")
}
