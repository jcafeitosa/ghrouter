package detectors

import (
	"strings"
	"testing"

	"ghrouter/internal/types"
)

func TestParseHarnessHelpBuildsObservedCapabilityInventory(t *testing.T) {
	help := `
Commands:
  run       Execute a prompt
  acp       Start ACP
  serve     Start a server
Options:
  --model provider/model
  --format default|json
  --variant low|high
  --thinking low|high
  --tools read,bash
  --image path
  --session id
  --mcp
`
	got := parseHarnessHelp(help, types.ProviderOpenCode)
	if !got.SupportsModelSelect || !got.SupportsThinking || !got.SupportsTools || !got.SupportsImages {
		t.Fatalf("capability flags were not detected: %+v", got)
	}
	if !got.SupportsNativeJSON || !got.SupportsServer || !got.SupportsSessions || !got.SupportsMCP {
		t.Fatalf("command capabilities were not detected: %+v", got)
	}
	for _, want := range []string{"acp", "run", "serve"} {
		if !containsString(got.Commands, want) {
			t.Fatalf("commands %v missing %q", got.Commands, want)
		}
	}
	if !containsString(got.Flags, "--model") || !containsString(got.Formats, "json") {
		t.Fatalf("flags/formats were not captured: %+v", got)
	}
	if got.Observed() {
		t.Fatal("parser must not claim a probe timestamp")
	}
}

func TestParseHarnessHelpUsesInstalledPiSlashInventory(t *testing.T) {
	got := parseHarnessHelp("pi --mode json --model --thinking", types.ProviderPi)
	if got.SlashCommandsSource != "help+installed-runtime" {
		t.Fatalf("slash source = %q", got.SlashCommandsSource)
	}
	if !containsString(got.SlashCommands, "/model") || !containsString(got.SlashCommands, "/compact") {
		t.Fatalf("slash commands = %v", got.SlashCommands)
	}
	if !strings.Contains(strings.Join(got.SlashCommands, " "), "/settings") {
		t.Fatalf("expected settings command in %v", got.SlashCommands)
	}
}

func TestParseHarnessCommandsHandlesPrefixedUsageLines(t *testing.T) {
	help := `Commands:
  opencode acp       start ACP
  opencode run       execute a prompt
  opencode models    list models

Positionals:
  project            project path
`
	got := parseHarnessCommands(help, types.ProviderOpenCode)
	if strings.Join(got, ",") != "acp,models,run" {
		t.Fatalf("commands = %v", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
