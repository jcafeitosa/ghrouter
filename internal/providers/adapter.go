package providers

import (
	"strings"

	"ghrouter/internal/types"
)

type ProviderAdapter interface {
	Name() string
	BuildArgs(provider *types.Provider, requestedModel, reasoningEffort string) []string
	PromptOnArgs() bool
}

type commandAdapter struct {
	name         string
	modelFlag    string
	promptOnArgs bool
}

func (a commandAdapter) Name() string {
	return a.name
}

func (a commandAdapter) PromptOnArgs() bool {
	return a.promptOnArgs
}

func (a commandAdapter) BuildArgs(provider *types.Provider, requestedModel, reasoningEffort string) []string {
	if provider == nil {
		return nil
	}
	args := append([]string(nil), provider.Args...)
	if a.name == "claude-code" && hasStreamJSONOutput(args) && !hasFlag(args, "--verbose") {
		args = append(args, "--verbose")
	}
	if strings.TrimSpace(requestedModel) != "" && !hasModelFlag(args) && supportsModelSelection(provider) {
		model := nativeModelID(a.name, requestedModel)
		if model != "" {
			args = append(args, a.modelFlag, model)
		}
	}
	return appendReasoningArgs(a.name, args, reasoningEffort, provider)
}

func appendReasoningArgs(adapterName string, args []string, effort string, provider *types.Provider) []string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return args
	}
	if provider != nil && provider.Harness.Observed() && !supportsReasoning(provider, adapterName) {
		return args
	}
	for _, arg := range args {
		if arg == "--effort" || arg == "--thinking" || arg == "--variant" || arg == "--reasoning-effort" || arg == "-c" {
			return args
		}
	}
	switch adapterName {
	case "claude-code":
		return append(args, "--effort", effort)
	case "codex":
		return append(args, "-c", "model_reasoning_effort="+effort)
	case "opencode", "mimo":
		return append(args, "--variant", effort)
	case "pi":
		return append(args, "--thinking", effort)
	case "cursor":
		return append(args, "--reasoning-effort", effort)
	default:
		return args
	}
}

func supportsModelSelection(provider *types.Provider) bool {
	return provider == nil || !provider.Harness.Observed() || provider.Harness.SupportsModelSelect
}

func supportsReasoning(provider *types.Provider, adapterName string) bool {
	if provider == nil || !provider.Harness.Observed() {
		return true
	}
	switch adapterName {
	case "opencode", "mimo", "pi":
		return provider.Harness.SupportsThinking
	default:
		return provider.Harness.SupportsEffort
	}
}

func nativeModelID(adapterName, requested string) string {
	model := stripProviderPrefix(requested)
	if adapterName == "mimo" && (strings.EqualFold(model, "mimo-auto") || strings.EqualFold(model, "auto")) {
		return ""
	}
	if adapterName != "opencode" {
		return model
	}
	model = strings.TrimPrefix(model, "opencode/")
	if strings.Contains(model, "/") {
		return model
	}
	return "opencode/" + model
}

func adapterFor(providerType types.ProviderType) ProviderAdapter {
	if adapter, ok := adapters[providerType]; ok {
		return adapter
	}
	return adapters[types.ProviderCustom]
}

var adapters = map[types.ProviderType]ProviderAdapter{
	types.ProviderClaudeCode: commandAdapter{name: "claude-code", modelFlag: "--model", promptOnArgs: true},
	types.ProviderCodex:      commandAdapter{name: "codex", modelFlag: "--model", promptOnArgs: true},
	types.ProviderOpenCode:   commandAdapter{name: "opencode", modelFlag: "--model", promptOnArgs: true},
	types.ProviderMimo:       commandAdapter{name: "mimo", modelFlag: "--model", promptOnArgs: true},
	types.ProviderPi:         commandAdapter{name: "pi", modelFlag: "--model", promptOnArgs: true},
	types.ProviderCursor:     commandAdapter{name: "cursor", modelFlag: "--model", promptOnArgs: true},
	types.ProviderCustom:     commandAdapter{name: "custom", modelFlag: "-m"},
}

func stripProviderPrefix(model string) string {
	for _, prefix := range []string{"cc/", "cx/", "oc/", "mi/", "pi/", "cu/", "nv/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	return model
}

func hasFlag(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasStreamJSONOutput(args []string) bool {
	for index, arg := range args {
		if arg == "--output-format=stream-json" {
			return true
		}
		if arg == "--output-format" && index+1 < len(args) && args[index+1] == "stream-json" {
			return true
		}
	}
	return false
}
