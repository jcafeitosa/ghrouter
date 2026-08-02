package providers

import (
	"strings"

	"ghrouter/internal/types"
)

type ProviderAdapter interface {
	Name() string
	BuildArgs(provider *types.Provider, requestedModel string) []string
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

func (a commandAdapter) BuildArgs(provider *types.Provider, requestedModel string) []string {
	if provider == nil {
		return nil
	}
	args := append([]string(nil), provider.Args...)
	if strings.TrimSpace(requestedModel) == "" || hasModelFlag(args) {
		return args
	}
	model := stripProviderPrefix(requestedModel)
	return append(args, a.modelFlag, model)
}

func adapterFor(providerType types.ProviderType) ProviderAdapter {
	if adapter, ok := adapters[providerType]; ok {
		return adapter
	}
	return adapters[types.ProviderCustom]
}

var adapters = map[types.ProviderType]ProviderAdapter{
	types.ProviderClaudeCode: commandAdapter{name: "claude-code", modelFlag: "--model"},
	types.ProviderCodex:      commandAdapter{name: "codex", modelFlag: "--model"},
	types.ProviderOpenCode:   commandAdapter{name: "opencode", modelFlag: "--model"},
	types.ProviderMimo:       commandAdapter{name: "mimo", modelFlag: "--model"},
	types.ProviderPi:         commandAdapter{name: "pi", modelFlag: "--model"},
	types.ProviderCursor:     commandAdapter{name: "cursor", modelFlag: "--model", promptOnArgs: true},
	types.ProviderCustom:     commandAdapter{name: "custom", modelFlag: "-m"},
}

func stripProviderPrefix(model string) string {
	for _, prefix := range []string{"cc/", "cx/", "oc/", "mi/", "pi/", "cu/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	return model
}
