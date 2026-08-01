package detectors

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"ghrouter/internal/types"
)

// Detector discovers installed CLIs and auto-configures providers
type Detector struct {
	discovered map[string]*types.Provider
}

func NewDetector() *Detector {
	return &Detector{
		discovered: make(map[string]*types.Provider),
	}
}

// DetectAll scans PATH for known CLIs and returns configured providers
func (d *Detector) DetectAll() ([]*types.Provider, error) {
	cliSpecs := []CLISpec{
		{
			Name:       "claude",
			ProviderType: types.ProviderClaudeCode,
			Args:       []string{"--print", "--output-format=stream-json"},
			ModelsFlag: []string{"--model"},
			ModelAlias: map[string]string{
				"opus":      "claude-opus-5",
				"sonnet":    "claude-sonnet-5",
				"haiku":     "claude-haiku-4-5",
				"fable":     "claude-fable-5",
				"mythos":    "claude-mythos-5",
			},
			Env: map[string]string{
				"CLAUDE_CODE_SIMPLE": "1",
			},
		},
		{
			Name:       "codex",
			ProviderType: types.ProviderCodex,
			Args:       []string{"exec", "--json"},
			ModelsFlag: []string{"-m", "--model"},
			ModelAlias: map[string]string{
				"gpt-4o":       "gpt-4o",
				"gpt-5":        "gpt-5",
				"o3":           "o3",
				"oss":          "openai/o3",
			},
			Env: map[string]string{},
		},
		{
			Name:       "opencode",
			ProviderType: types.ProviderOpenCode,
			Args:       []string{"run", "--format", "json"},
			ModelsFlag: []string{"-m", "--model"},
			ModelAlias: map[string]string{},
			Env: map[string]string{},
		},
		{
			Name:       "mimo",
			ProviderType: types.ProviderMimo,
			Args:       []string{"run", "--format", "json"},
			ModelsFlag: []string{"-m", "--model"},
			ModelAlias: map[string]string{},
			Env: map[string]string{},
		},
		{
			Name:       "pi",
			ProviderType: types.ProviderPi,
			Args:       []string{"--mode", "json", "--print"},
			ModelsFlag: []string{"--model", "--provider"},
			ModelAlias: map[string]string{
				"sonnet:high": "anthropic/claude-sonnet-5:high",
				"opus":        "anthropic/claude-opus-5",
			},
			Env: map[string]string{},
		},
	}

	var providers []*types.Provider
	for _, spec := range cliSpecs {
		path, err := exec.LookPath(spec.Name)
		if err != nil {
			// CLI not in PATH
			continue
		}
		prov, err := d.buildProvider(spec, path)
		if err != nil {
			// Log and continue
			fmt.Fprintf(os.Stderr, "[detector] %s: %v\n", spec.Name, err)
			continue
		}
		providers = append(providers, prov)
		d.discovered[spec.Name] = prov
	}

	return providers, nil
}

// CLISpec defines how to invoke a CLI in headless mode
type CLISpec struct {
	Name         string
	ProviderType types.ProviderType
	Args         []string
	ModelsFlag   []string
	ModelAlias   map[string]string
	Env          map[string]string
}

// buildProvider constructs a Provider from detected CLI
func (d *Detector) buildProvider(spec CLISpec, cliPath string) (*types.Provider, error) {
	// Try to list models from CLI
	models, err := d.listModels(spec, cliPath)
	if err != nil {
		// If listing fails, use aliases as fallback
		models = make([]string, 0, len(spec.ModelAlias))
		for _, v := range spec.ModelAlias {
			models = append(models, v)
		}
	}

	// Build env
	env := make(map[string]string)
	for k, v := range spec.Env {
		env[k] = v
	}
	// Inherit relevant env vars
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_API_BASE",
		"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT",
		"GOOGLE_API_KEY", "GEMINI_API_KEY",
		"CODEX_HOME", "OPENCODE_HOME", "PI_HOME",
	} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}

	// Determine working dir
	workDir := os.Getenv("PWD")
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Prefix models with provider tag for routing
	prefixedModels := make([]string, len(models))
	for i, m := range models {
		prefixedModels[i] = d.prefixModel(spec.ProviderType, m)
	}

	prov := &types.Provider{
		Name:        string(spec.ProviderType),
		Type:        spec.ProviderType,
		CLIPath:     cliPath,
		Args:        spec.Args,
		Env:         env,
		Models:      prefixedModels,
		Timeout:     5 * 60 * 1_000_000_000, // 5 min in ns
		MaxTokens:   128000,
		WorkDir:     workDir,
		AuthMethod:  types.AuthEnv,
		AuthConfig:  env,
		Enabled:     true,
	}

	return prov, nil
}

// listModels attempts to query CLI for available models
func (d *Detector) listModels(spec CLISpec, cliPath string) ([]string, error) {
	var cmd *exec.Cmd
	switch spec.Name {
	case "claude":
		// claude doesn't have a list-models command
		return nil, fmt.Errorf("no list-models command")
	case "codex":
		// codex models --json
		cmd = exec.Command(cliPath, "models", "--json")
	case "opencode":
		// opencode models --json
		cmd = exec.Command(cliPath, "models", "--json")
	case "mimo":
		cmd = exec.Command(cliPath, "models", "--json")
	case "pi":
		cmd = exec.Command(cliPath, "--list-models")
	default:
		return nil, fmt.Errorf("unknown CLI")
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	return d.parseModels(spec.Name, string(out)), nil
}

// parseModels extracts model IDs from CLI output
func (d *Detector) parseModels(name, output string) []string {
	var models []string
	// Try JSON first
	var jsonOut []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(output), &jsonOut); err == nil {
		for _, m := range jsonOut {
			if m.ID != "" {
				models = append(models, m.ID)
			} else if m.Name != "" {
				models = append(models, m.Name)
			} else if m.Model != "" {
				models = append(models, m.Model)
			}
		}
		if len(models) > 0 {
			return models
		}
	}

	// Fallback: grep for model-like patterns
	lines := strings.Split(output, "\n")
	re := regexp.MustCompile(`(?i)(?:model|id|name)[\s:=]+([a-z0-9./_-]+)`)
	for _, line := range lines {
		matches := re.FindStringSubmatch(line)
		if len(matches) > 1 {
			models = append(models, matches[1])
		}
	}

	return models
}

// prefixModel adds provider prefix for routing
func (d *Detector) prefixModel(pt types.ProviderType, model string) string {
	prefix := ""
	switch pt {
	case types.ProviderClaudeCode:
		prefix = "cc/"
	case types.ProviderCodex:
		prefix = "cx/"
	case types.ProviderOpenCode:
		prefix = "oc/"
	case types.ProviderMimo:
		prefix = "mi/"
	case types.ProviderPi:
		prefix = "pi/"
	}
	// Avoid double prefix
	if strings.HasPrefix(model, prefix) {
		return model
	}
	return prefix + model
}

// GetDiscovered returns auto-discovered providers
func (d *Detector) GetDiscovered() map[string]*types.Provider {
	return d.discovered
}