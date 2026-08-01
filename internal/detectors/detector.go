package detectors

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"ghrouter/internal/types"
)

type Detector struct{ discovered map[string]*types.Provider }

func NewDetector() *Detector { return &Detector{discovered: make(map[string]*types.Provider)} }

type CLISpec struct {
	Name         string
	ProviderType types.ProviderType
	Args         []string
	Aliases      []string
	Env          map[string]string
}

func (d *Detector) DetectAll() ([]*types.Provider, error) {
	specs := []CLISpec{
		{Name: "claude", ProviderType: types.ProviderClaudeCode, Args: []string{"--print", "--output-format", "stream-json", "--no-session-persistence"}, Aliases: []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5"}, Env: map[string]string{"CLAUDE_CODE_SIMPLE": "1"}},
		{Name: "codex", ProviderType: types.ProviderCodex, Args: []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check"}, Aliases: []string{"gpt-5", "gpt-4o", "o3"}, Env: map[string]string{}},
		{Name: "opencode", ProviderType: types.ProviderOpenCode, Args: []string{"run", "--format", "json", "--no-remote"}, Aliases: []string{}, Env: map[string]string{}},
		{Name: "mimo", ProviderType: types.ProviderMimo, Args: []string{"run", "--format", "json", "--pure"}, Aliases: []string{}, Env: map[string]string{}},
		{Name: "pi", ProviderType: types.ProviderPi, Args: []string{"--mode", "json", "--print", "--no-session", "--no-context-files"}, Aliases: []string{"anthropic/claude-sonnet-5", "openai/gpt-5"}, Env: map[string]string{}},
	}
	providers := make([]*types.Provider, 0, len(specs))
	for _, spec := range specs {
		path, err := exec.LookPath(spec.Name)
		if err != nil {
			continue
		}
		p := d.buildProvider(spec, path)
		providers = append(providers, p)
		d.discovered[p.Name] = p
	}
	return providers, nil
}

func (d *Detector) buildProvider(spec CLISpec, path string) *types.Provider {
	env := make(map[string]string, len(spec.Env)+8)
	for k, v := range spec.Env {
		env[k] = v
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_API_BASE", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "CODEX_HOME", "OPENCODE_HOME", "PI_HOME"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	workDir, _ := os.Getwd()
	models := make([]string, 0, len(spec.Aliases))
	for _, model := range spec.Aliases {
		models = append(models, prefix(spec.ProviderType, model))
	}
	return &types.Provider{Name: string(spec.ProviderType), Type: spec.ProviderType, CLIPath: path, Args: spec.Args, Env: env, Models: models, Timeout: 5 * 60 * 1e9, MaxTokens: 128000, WorkDir: workDir, AuthMethod: types.AuthEnv, Enabled: true}
}

func prefix(provider types.ProviderType, model string) string {
	p := map[types.ProviderType]string{types.ProviderClaudeCode: "cc/", types.ProviderCodex: "cx/", types.ProviderOpenCode: "oc/", types.ProviderMimo: "mi/", types.ProviderPi: "pi/"}[provider]
	if strings.HasPrefix(model, p) {
		return model
	}
	return p + model
}

func (d *Detector) GetDiscovered() map[string]*types.Provider { return d.discovered }
func (d *Detector) String() string                            { return fmt.Sprintf("%d providers discovered", len(d.discovered)) }
