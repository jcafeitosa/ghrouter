package detectors

import (
	"context"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"ghrouter/internal/types"
)

const harnessHelpTimeout = 3 * time.Second

var (
	harnessFlagPattern  = regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	harnessSlashPattern = regexp.MustCompile(`(?m)(?:^|\s)(/[a-z][a-z0-9-]*)\b`)
)

func probeHarnessCapabilities(path string, providerType types.ProviderType) types.HarnessCapabilities {
	capabilities := types.HarnessCapabilities{}
	ctx, cancel := context.WithTimeout(context.Background(), harnessHelpTimeout)
	defer cancel()
	outputs := make([]string, 0, 4)
	for _, args := range harnessHelpArgs(providerType) {
		cmd := execCommandContext(ctx, path, args...)
		prepareDiscoveryCommand(cmd)
		stdout, stderr, err := runBoundedCommand(ctx, cmd)
		if err != nil {
			continue
		}
		outputs = append(outputs, string(stdout), string(stderr))
	}
	if len(outputs) == 0 || ctx.Err() != nil {
		return capabilities
	}
	help := strings.Join(outputs, "\n")
	capabilities = parseHarnessHelp(help, providerType)
	capabilities.Version = probeHarnessVersion(path)
	capabilities.ObservedAt = time.Now().UTC()
	return capabilities
}

func harnessHelpArgs(providerType types.ProviderType) [][]string {
	args := [][]string{{"--help"}}
	switch providerType {
	case types.ProviderCodex:
		args = append(args, []string{"exec", "--help"}, []string{"app-server", "--help"})
	case types.ProviderOpenCode, types.ProviderMimo:
		args = append(args, []string{"run", "--help"}, []string{"acp", "--help"}, []string{"models", "--help"})
	case types.ProviderPi:
		args = append(args, []string{"auth", "--help"})
	case types.ProviderCursor:
		args = append(args, []string{"agent", "--help"}, []string{"agent", "stdio", "--help"})
	}
	return args
}

func probeHarnessVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := execCommandContext(ctx, path, "--version")
	prepareDiscoveryCommand(cmd)
	stdout, stderr, err := runBoundedCommand(ctx, cmd)
	if err != nil || ctx.Err() != nil {
		return ""
	}
	for _, value := range []string{string(stdout), string(stderr)} {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	return ""
}

func parseHarnessHelp(help string, providerType types.ProviderType) types.HarnessCapabilities {
	lower := strings.ToLower(help)
	capabilities := types.HarnessCapabilities{
		Commands:      parseHarnessCommands(help, providerType),
		Flags:         uniqueStrings(harnessFlagPattern.FindAllString(help, -1)),
		Formats:       formatsFromHelp(lower),
		SlashCommands: uniqueSorted(harnessSlashPattern.FindAllStringSubmatch(help, -1), 1),
	}
	if providerType == types.ProviderPi {
		capabilities.SlashCommands = uniqueStrings(append(capabilities.SlashCommands, []string{"/changelog", "/clear", "/compact", "/copy", "/export", "/fork", "/hotkeys", "/import", "/login", "/logout", "/model", "/name", "/new", "/quit", "/reload", "/resume", "/scoped-models", "/session", "/settings", "/share", "/tree", "/trust"}...))
		capabilities.SlashCommandsSource = "help+installed-runtime"
	} else if len(capabilities.SlashCommands) > 0 {
		capabilities.SlashCommandsSource = "help"
	}

	capabilities.AdvertisesACP = hasAny(lower, " acp", "acp ", "acp\n")
	capabilities.SupportsNativeJSON = hasAny(lower, "--format", "--output-format", "--mode json", "--json") && hasAny(lower, "json")
	capabilities.SupportsStreaming = hasAny(lower, "stream-json", "streaming-json", "stream-partial", "include-partial")
	capabilities.SupportsRPC = hasAny(lower, " rpc", "rpc ", "app-server", "stdio")
	capabilities.SupportsServer = hasAny(lower, " serve", "serve ", "\nserve")
	capabilities.SupportsModelSelect = hasAny(lower, "--model", "-m, --model", "-m model")
	capabilities.SupportsEffort = hasAny(lower, "--effort", "reasoning_effort", "reasoning-effort")
	capabilities.SupportsThinking = hasAny(lower, "--thinking", "thinking level", "--variant")
	capabilities.SupportsTools = hasAny(lower, "--tools", "--no-tools", "tool use", "toolcall")
	capabilities.SupportsImages = hasAny(lower, "--image", "images", "vision", "multimodal")
	capabilities.SupportsSessions = hasAny(lower, "--session", "--resume", "--continue", "session")
	capabilities.SupportsMCP = hasAny(lower, " mcp", "mcp ", "\nmcp", "--mcp")
	capabilities.SupportsHeadless = hasAny(lower, "--print", "--single", " run ", " exec ", "headless")
	return capabilities
}

func parseHarnessCommands(help string, providerType types.ProviderType) []string {
	root := map[types.ProviderType]string{
		types.ProviderClaudeCode: "claude",
		types.ProviderCodex:      "codex",
		types.ProviderOpenCode:   "opencode",
		types.ProviderMimo:       "mimo",
		types.ProviderPi:         "pi",
		types.ProviderCursor:     "agent",
	}[providerType]
	values := make([]string, 0)
	inCommands := false
	for _, rawLine := range strings.Split(help, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.EqualFold(line, "Commands:") {
			inCommands = true
			continue
		}
		if inCommands && (strings.HasSuffix(line, ":") || strings.HasPrefix(line, "Positionals")) {
			break
		}
		if !inCommands || line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		candidate := fields[0]
		if root != "" && fields[0] == root && len(fields) > 1 {
			candidate = fields[1]
		}
		candidate = strings.Trim(candidate, "[]<>()")
		if candidate == "" || strings.Contains(candidate, "|") || strings.Contains(candidate, "...") {
			continue
		}
		if strings.HasPrefix(candidate, "-") {
			continue
		}
		values = append(values, candidate)
	}
	return uniqueStrings(values)
}

func formatsFromHelp(help string) []string {
	formats := make([]string, 0, 4)
	for _, format := range []string{"json", "stream-json", "streaming-json", "streaming-messages-json", "text", "plain", "rpc"} {
		if strings.Contains(help, format) {
			formats = append(formats, format)
		}
	}
	return formats
}

func uniqueSorted(matches [][]string, index int) []string {
	seen := make(map[string]struct{}, len(matches))
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		if index >= len(match) {
			continue
		}
		value := strings.TrimSpace(match[index])
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func hasAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func execCommandContext(ctx context.Context, path string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, path, args...)
}
