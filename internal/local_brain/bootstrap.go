package local_brain

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"ghrouter/internal/types"
)

type BootstrapIssue struct {
	Provider string
	Backend  BackendType
	Model    string
	Reason   string
}

type StartupCheck struct {
	Provider string      `json:"provider"`
	Backend  BackendType `json:"backend"`
	Model    string      `json:"model,omitempty"`
	AuthOK   bool        `json:"auth_ok"`
	ModelOK  bool        `json:"model_ok"`
	Ready    bool        `json:"ready"`
	Reason   string      `json:"reason,omitempty"`
	NextStep string      `json:"next_step,omitempty"`
}

type ProvisionAction struct {
	Provider string      `json:"provider"`
	Backend  BackendType `json:"backend"`
	Model    string      `json:"model,omitempty"`
	Action   string      `json:"action"`
	Command  []string    `json:"command,omitempty"`
	Reason   string      `json:"reason,omitempty"`
	ApplyOK  bool        `json:"apply_ok"`
	Source   string      `json:"source,omitempty"`
}

type BootstrapReport struct {
	Backend BackendType
	Issues  []BootstrapIssue
	Checks  []StartupCheck
}

type BootstrapSummary struct {
	Ready       bool              `json:"ready"`
	Backend     BackendType       `json:"backend"`
	Issues      []BootstrapIssue  `json:"issues"`
	Checks      []StartupCheck    `json:"checks"`
	Provision   []ProvisionAction `json:"provision"`
	Suggestions []string          `json:"suggestions"`
}

func (r BootstrapReport) Ready() bool {
	return len(r.Issues) == 0
}

func (r BootstrapReport) SummaryLines() []string {
	lines := make([]string, 0, len(r.Issues)+1)
	if r.Ready() {
		lines = append(lines, "all startup prerequisites are ready")
		return lines
	}
	for _, issue := range r.Issues {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", issue.Provider, issue.Backend, issue.Model, issue.Reason))
	}
	return lines
}

func (r BootstrapReport) Summary() BootstrapSummary {
	issues := make([]BootstrapIssue, len(r.Issues))
	copy(issues, r.Issues)
	checks := make([]StartupCheck, len(r.Checks))
	copy(checks, r.Checks)
	return BootstrapSummary{
		Ready:       r.Ready(),
		Backend:     r.Backend,
		Issues:      issues,
		Checks:      checks,
		Provision:   buildProvisionPlan(r.Checks),
		Suggestions: suggestionsForIssues(r.Issues),
	}
}

func (r BootstrapReport) Error() error {
	if r.Ready() {
		return nil
	}
	var parts []string
	for _, issue := range r.Issues {
		parts = append(parts, fmt.Sprintf("%s/%s: %s", issue.Provider, issue.Model, issue.Reason))
	}
	return fmt.Errorf("startup prerequisites missing: %s", strings.Join(parts, "; "))
}

type BackendAvailability interface {
	IsBackendAvailable(backend BackendType) bool
}

type ModelPresence interface {
	HasModel(backend BackendType, modelID string) bool
}

type ModelPreparer interface {
	Prepare() error
}

type Bootstrapper struct {
	Detector     BackendAvailability
	ModelManager ModelPresence
}

func NewBootstrapper() (*Bootstrapper, error) {
	manager, err := NewModelManager()
	if err != nil {
		return nil, err
	}
	return &Bootstrapper{
		Detector:     &Detector{},
		ModelManager: manager,
	}, nil
}

func (b *Bootstrapper) Check(providers []*types.Provider) (BootstrapReport, error) {
	report := BootstrapReport{Backend: BackendNone}
	if b == nil || b.Detector == nil || b.ModelManager == nil {
		return report, fmt.Errorf("bootstrapper not configured")
	}
	if preparer, ok := b.ModelManager.(ModelPreparer); ok {
		if err := preparer.Prepare(); err != nil {
			return report, fmt.Errorf("prepare model cache: %w", err)
		}
	}

	for _, p := range providers {
		if p == nil || !p.Enabled {
			continue
		}
		backend := backendForProvider(p.Type)
		if backend == BackendNone {
			continue
		}
		if report.Backend == BackendNone {
			report.Backend = backend
		}
		check := StartupCheck{
			Provider: p.Name,
			Backend:  backend,
			Model:    firstModel(p.Models),
			AuthOK:   AuthReason(p) == "",
			ModelOK:  false,
			Ready:    false,
		}
		if !b.Detector.IsBackendAvailable(backend) {
			check.Reason = fmt.Sprintf("%s backend unavailable", backend)
			check.NextStep = nextStepForIssue(check.Reason, backend, check.Model, p)
			report.Checks = append(report.Checks, check)
			report.Issues = append(report.Issues, BootstrapIssue{
				Provider: p.Name,
				Backend:  backend,
				Model:    check.Model,
				Reason:   check.Reason,
			})
			continue
		}

		if reason := AuthReason(p); reason != "" {
			check.Reason = reason
			check.NextStep = nextStepForIssue(check.Reason, backend, check.Model, p)
			report.Checks = append(report.Checks, check)
			report.Issues = append(report.Issues, BootstrapIssue{
				Provider: p.Name,
				Backend:  backend,
				Model:    check.Model,
				Reason:   reason,
			})
			continue
		}

		model := firstModel(p.Models)
		check.Model = model
		if model == "" {
			check.Reason = "no model configured"
			check.NextStep = nextStepForIssue(check.Reason, backend, check.Model, p)
			report.Checks = append(report.Checks, check)
			report.Issues = append(report.Issues, BootstrapIssue{
				Provider: p.Name,
				Backend:  backend,
				Reason:   check.Reason,
			})
			continue
		}
		if !b.ModelManager.HasModel(backend, model) {
			check.Reason = "model not present in local cache"
			check.NextStep = nextStepForIssue(check.Reason, backend, check.Model, p)
			report.Checks = append(report.Checks, check)
			report.Issues = append(report.Issues, BootstrapIssue{
				Provider: p.Name,
				Backend:  backend,
				Model:    model,
				Reason:   check.Reason,
			})
			continue
		}
		check.ModelOK = true
		check.Ready = true
		report.Checks = append(report.Checks, check)
	}

	return report, report.Error()
}

func backendForProvider(pt types.ProviderType) BackendType {
	switch pt {
	case types.ProviderClaudeCode, types.ProviderCodex, types.ProviderOpenCode, types.ProviderMimo:
		return preferredBackendForHost()
	case types.ProviderPi:
		return BackendMLX
	default:
		return preferredBackendForHost()
	}
}

func preferredBackendForHost() BackendType {
	switch runtime.GOOS {
	case "darwin":
		return BackendMLX
	case "windows", "linux":
		return BackendLLAMACPP
	default:
		return BackendLLAMACPP
	}
}

func firstModel(models []string) string {
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

func AuthReason(p *types.Provider) string {
	if p == nil {
		return "provider missing"
	}
	switch p.Type {
	case types.ProviderClaudeCode:
		if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
			return "missing Anthropic auth (ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN)"
		}
	case types.ProviderCodex:
		if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("CODEX_HOME") == "" {
			return "missing OpenAI auth (OPENAI_API_KEY or CODEX_HOME)"
		}
	case types.ProviderOpenCode:
		if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("OPENCODE_API_KEY") == "" {
			return "missing OpenCode auth (OPENAI_API_KEY or OPENCODE_API_KEY)"
		}
	case types.ProviderMimo:
		if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("MIMO_API_KEY") == "" {
			return "missing Mimo auth (OPENAI_API_KEY or MIMO_API_KEY)"
		}
	case types.ProviderPi:
		if os.Getenv("GOOGLE_API_KEY") == "" && os.Getenv("PI_API_KEY") == "" {
			return "missing Pi auth (GOOGLE_API_KEY or PI_API_KEY)"
		}
	case types.ProviderCursor:
		if os.Getenv("CURSOR_API_KEY") == "" {
			return "missing Cursor auth (CURSOR_API_KEY)"
		}
	}
	return ""
}

func suggestionsForIssues(issues []BootstrapIssue) []string {
	suggestions := make([]string, 0, len(issues))
	for _, issue := range issues {
		switch {
		case strings.Contains(issue.Reason, "backend unavailable"):
			suggestions = append(suggestions, fmt.Sprintf("install or enable %s support before starting %s", issue.Backend, issue.Provider))
		case strings.Contains(issue.Reason, "missing "):
			suggestions = append(suggestions, fmt.Sprintf("set the required auth for %s and retry", issue.Provider))
		case strings.Contains(issue.Reason, "model not present in local cache"):
			suggestions = append(suggestions, fmt.Sprintf("cache or download %s for %s", issue.Model, issue.Provider))
		case strings.Contains(issue.Reason, "no model configured"):
			suggestions = append(suggestions, fmt.Sprintf("configure at least one model for %s", issue.Provider))
		default:
			suggestions = append(suggestions, fmt.Sprintf("review %s/%s: %s", issue.Provider, issue.Model, issue.Reason))
		}
	}
	return suggestions
}

func nextStepForIssue(reason string, backend BackendType, model string, p *types.Provider) string {
	switch {
	case strings.Contains(reason, "backend unavailable"):
		if backend == BackendMLX {
			return "install or enable MLX support, then rerun ghrouter bootstrap"
		}
		if backend == BackendLLAMACPP {
			return "install or enable llama.cpp support, then rerun ghrouter bootstrap"
		}
		return "install the required backend support, then rerun ghrouter bootstrap"
	case strings.Contains(reason, "missing "):
		return fmt.Sprintf("set the required auth for %s and rerun ghrouter bootstrap", p.Name)
	case strings.Contains(reason, "model not present in local cache"):
		return fmt.Sprintf("place %s in the local cache for %s and rerun ghrouter bootstrap", model, p.Name)
	case strings.Contains(reason, "no model configured"):
		return fmt.Sprintf("configure at least one model for %s and rerun ghrouter bootstrap", p.Name)
	default:
		return fmt.Sprintf("review %s/%s and rerun ghrouter bootstrap", p.Name, model)
	}
}

func buildProvisionPlan(checks []StartupCheck) []ProvisionAction {
	plan := make([]ProvisionAction, 0, len(checks))
	for _, check := range checks {
		if check.Ready {
			continue
		}
		action := ProvisionAction{
			Provider: check.Provider,
			Backend:  check.Backend,
			Model:    check.Model,
			Reason:   check.Reason,
		}
		switch {
		case strings.Contains(check.Reason, "backend unavailable"):
			action.Action = "backend_setup"
			action.Command = backendSetupCommand(check.Backend)
		case strings.Contains(check.Reason, "missing "):
			action.Action = "auth_setup"
		case strings.Contains(check.Reason, "model not present in local cache"):
			action.Action = "model_cache"
			action.ApplyOK = true
			action.Source = modelSourceForBackend(check.Backend, check.Model)
			action.Command = modelDownloadCommand(check.Backend, check.Model)
		case strings.Contains(check.Reason, "no model configured"):
			action.Action = "configure_model"
		default:
			action.Action = "review"
		}
		plan = append(plan, action)
	}
	return plan
}

func backendSetupCommand(backend BackendType) []string {
	switch backend {
	case BackendMLX:
		return []string{"python3", "-m", "pip", "install", "mlx"}
	case BackendLLAMACPP:
		return []string{"brew", "install", "llama.cpp"}
	default:
		return nil
	}
}

func modelSourceForBackend(backend BackendType, model string) string {
	switch backend {
	case BackendMLX:
		return "huggingface://mlx-community/" + sanitizeModelSlug(model)
	case BackendLLAMACPP:
		return "huggingface://ggml-org/" + sanitizeModelSlug(model)
	default:
		return "huggingface://" + sanitizeModelSlug(model)
	}
}

func modelDownloadCommand(backend BackendType, model string) []string {
	slug := sanitizeModelSlug(model)
	switch backend {
	case BackendMLX:
		return []string{"hf", "download", "mlx-community/" + slug, "--local-dir", modelCachePath(backend, model)}
	case BackendLLAMACPP:
		return []string{"hf", "download", "ggml-org/" + slug, "--local-dir", modelCachePath(backend, model)}
	default:
		return []string{"hf", "download", slug, "--local-dir", modelCachePath(backend, model)}
	}
}

func modelCachePath(backend BackendType, model string) string {
	base, err := NewModelManager()
	if err != nil {
		return ""
	}
	switch backend {
	case BackendMLX:
		return base.CacheDir() + "/mlx/" + sanitizeModelSlug(model)
	case BackendLLAMACPP:
		return base.CacheDir() + "/" + sanitizeModelSlug(model) + ".gguf"
	default:
		return base.CacheDir() + "/" + sanitizeModelSlug(model)
	}
}

func sanitizeModelSlug(model string) string {
	slug := strings.TrimSpace(model)
	slug = strings.TrimPrefix(slug, "cc/")
	slug = strings.TrimPrefix(slug, "cx/")
	slug = strings.TrimPrefix(slug, "oc/")
	slug = strings.TrimPrefix(slug, "mi/")
	slug = strings.TrimPrefix(slug, "pi/")
	slug = strings.ReplaceAll(slug, "/", "-")
	return slug
}
