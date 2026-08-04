package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/config"
	"ghrouter/internal/detectors"
	"ghrouter/internal/local_brain"
	"ghrouter/internal/observability"
	"ghrouter/internal/server"
	"ghrouter/internal/types"
	"ghrouter/internal/update"
)

const Version = "0.1.0-dev"

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Config string
	JSON   bool
}

var newBootstrapper = local_brain.NewBootstrapper

func Run(ctx context.Context, args []string) int {
	runnerArgs, cfgPath, showVersion, showHelp := parseArgs(args)
	_ = loadDotEnv(filepath.Join(filepath.Dir(config.ResolveConfigPath(cfgPath)), ".env"))
	logSettings := observability.Settings{}
	if cfg, loadErr := config.Load(config.ResolveConfigPath(cfgPath)); loadErr == nil {
		if cfg.Logging.Level != "" {
			logSettings.Level = cfg.Logging.Level
		}
		if cfg.Logging.Format != "" {
			logSettings.Format = cfg.Logging.Format
		}
		if cfg.Logging.Output != "" {
			logSettings.Output = cfg.Logging.Output
		}
		if cfg.Logging.File != "" {
			logSettings.File = cfg.Logging.File
		}
		if cfg.Logging.Color != "" {
			logSettings.Color = cfg.Logging.Color
		}
	}
	if value := os.Getenv("GHR_LOG_LEVEL"); value != "" {
		logSettings.Level = value
	}
	if value := os.Getenv("GHR_LOG_FORMAT"); value != "" {
		logSettings.Format = value
	}
	if value := os.Getenv("GHR_LOG_OUTPUT"); value != "" {
		logSettings.Output = value
	}
	if value := os.Getenv("GHR_LOG_FILE"); value != "" {
		logSettings.File = value
	}
	if value := os.Getenv("GHR_LOG_COLOR"); value != "" {
		logSettings.Color = value
	}
	closeLogs, err := observability.Configure(os.Stderr, logSettings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging configuration failed: %v\n", err)
		return 1
	}
	defer closeLogs()
	if showVersion {
		fmt.Fprintln(os.Stdout, Version)
		return 0
	}
	if showHelp {
		r := &Runner{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Config: cfgPath}
		return r.help()
	}
	r := &Runner{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Config: cfgPath}
	observability.Logger("cli").Info("command_started", "command", firstArg(runnerArgs))
	return r.Run(ctx, runnerArgs)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return "dashboard"
	}
	return args[0]
}

func (r *Runner) Run(ctx context.Context, args []string) int {
	args, cfgPath, showVersion, showHelp := parseArgs(args)
	if showVersion {
		fmt.Fprintln(r.Stdout, Version)
		return 0
	}
	if showHelp {
		return r.help()
	}
	if cfgPath != "" {
		r.Config = cfgPath
	}
	if hasJSONFlag(args) {
		r.JSON = true
		args = stripJSONFlag(args)
	}
	if len(args) == 0 {
		return r.live(ctx)
	}

	switch args[0] {
	case "help", "--help", "-h":
		return r.help()
	case "serve":
		return r.serve(ctx)
	case "doctor":
		return r.doctor()
	case "init":
		return r.init()
	case "providers":
		return r.providers()
	case "models":
		return r.models(ctx, args[1:])
	case "sync":
		return r.sync()
	case "bootstrap":
		return r.bootstrap()
	case "provision":
		return r.provision(args[1:])
	case "export":
		return r.export()
	case "import":
		if len(args) < 2 {
			fmt.Fprintln(r.Stderr, "usage: ghrouter import <bundle.json>")
			return 2
		}
		return r.importBundle(args[1])
	case "routes":
		return r.routes()
	case "live":
		return r.live(ctx)
	case "attach":
		return r.attach(ctx, args[1:])
	case "config":
		return r.config()
	case "ping":
		return r.ping(ctx)
	case "connect":
		return r.connect(args[1:])
	case "version":
		return r.version()
	case "update":
		return r.update(args[1:])
	case "reset":
		return r.reset(args[1:])
	case "test":
		if len(args) < 2 {
			fmt.Fprintln(r.Stderr, "usage: ghrouter test <model>")
			return 2
		}
		return r.test(ctx, args[1])
	case "probe":
		if len(args) < 2 {
			fmt.Fprintln(r.Stderr, "usage: ghrouter probe <model>")
			return 2
		}
		return r.probe(ctx, args[1])
	case "verify-models":
		return r.verifyModels(ctx, args[1:])
	case "explain":
		if len(args) < 2 {
			fmt.Fprintln(r.Stderr, "usage: ghrouter explain <model>")
			return 2
		}
		return r.explain(ctx, args[1])
	default:
		fmt.Fprintf(r.Stderr, "unknown command: %s\n", args[0])
		return 2
	}
}

func (r *Runner) help() int {
	fmt.Fprintln(r.Stdout, "ghrouter - local AI routing engine for CLI workflows")
	fmt.Fprintln(r.Stdout, "")
	fmt.Fprintln(r.Stdout, "Usage: ghrouter [--config PATH] [--json] <command>")
	fmt.Fprintln(r.Stdout, "")
	fmt.Fprintln(r.Stdout, "Commands:")
	for _, command := range []string{
		"serve", "live", "attach [url]", "init", "doctor", "sync", "providers", "models [--functional-only] [--provider NAME] [--health STATUS] [--capability NAME] [--cost TIER]", "probe <model>", "verify-models [model...]", "test <model>", "routes", "explain <model>", "connect <copilot|codex|claude|opencode|mimo|pi|cursor|nvidia>", "config", "ping", "bootstrap", "provision", "export", "import <bundle.json>", "reset", "update", "version",
	} {
		fmt.Fprintf(r.Stdout, "  ghrouter %s\n", command)
	}
	fmt.Fprintln(r.Stdout, "")
	fmt.Fprintln(r.Stdout, "Global options: --config PATH, --json, --version, --help")
	return 0
}

func (r *Runner) serve(ctx context.Context) int {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if os.Getenv("GHR_DETACH") == "1" {
		detachProcess()
	}
	if os.Getenv("GHR_AUTO_UPDATE") == "1" {
		if code := r.update([]string{"--apply"}); code != 0 {
			return code
		}
	}
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	brainCfg := *cfg
	brainCfg.Providers = append([]*types.Provider(nil), cfg.Providers...)
	local_brain.EnsureMandatoryConfig(&brainCfg.LocalBrain)
	srv := server.NewWithConfigPath(cfg, config.ResolveConfigPath(r.Config))
	go r.printStartupStatus(cfg)
	runtimeMu := &sync.Mutex{}
	go func() {
		refreshed, refreshErr := loadCurrentConfig(r.Config)
		if refreshErr != nil {
			observability.Logger("discovery").Warn("background_discovery_failed", "error", observability.PublicError(refreshErr))
			return
		}
		runtimeMu.Lock()
		defer runtimeMu.Unlock()
		if reloadErr := srv.ReloadConfig(refreshed); reloadErr == nil {
			return
		}
		configured := make(map[string]bool, len(cfg.Providers))
		for _, provider := range cfg.Providers {
			if provider != nil {
				configured[provider.Name] = true
			}
		}
		for _, provider := range refreshed.Providers {
			if provider != nil && !configured[provider.Name] {
				if attachErr := srv.AttachProvider(provider); attachErr != nil {
					observability.Logger("discovery").Warn("background_provider_attach_failed", "provider", provider.Name, "error", observability.PublicError(attachErr))
				}
			}
		}
	}()
	go func() {
		localSupervisor, brainErr := attachLocalBrain(serveCtx, &brainCfg)
		if brainErr != nil {
			fmt.Fprintf(r.Stderr, "local brain unavailable; routing is degraded to an eligible fast backup: %v\n", brainErr)
			return
		}
		var localProvider *types.Provider
		for _, provider := range brainCfg.Providers {
			if provider != nil && provider.Name == "local-brain" {
				localProvider = provider
				break
			}
		}
		if localProvider == nil {
			observability.Logger("local-brain").Debug("local_brain_disabled")
			return
		}
		observability.Logger("local-brain").Info("local_brain_provider_handoff", "providers", len(brainCfg.Providers))
		observability.Logger("local-brain").Info("local_brain_provider_attach_begin", "models", len(localProvider.Models))
		runtimeMu.Lock()
		attachErr := srv.AttachProvider(localProvider)
		runtimeMu.Unlock()
		if attachErr != nil {
			fmt.Fprintf(r.Stderr, "local brain attach failed; routing remains on backup providers: %v\n", attachErr)
		}
		if localSupervisor != nil {
			<-serveCtx.Done()
			_ = localSupervisor.Stop()
		}
	}()
	reloadSignals := make(chan os.Signal, 1)
	signal.Notify(reloadSignals, syscall.SIGHUP)
	defer signal.Stop(reloadSignals)
	go func() {
		for {
			select {
			case <-serveCtx.Done():
				return
			case <-reloadSignals:
				next, loadErr := loadCurrentConfig(r.Config)
				if loadErr != nil {
					fmt.Fprintf(r.Stderr, "reload failed: %v\n", loadErr)
					continue
				}
				if reloadErr := srv.ReloadConfig(next); reloadErr != nil {
					fmt.Fprintf(r.Stderr, "reload rejected: %v\n", reloadErr)
					continue
				}
				fmt.Fprintln(r.Stderr, "reload ok")
			}
		}
	}()
	if err := srv.ListenAndServe(serveCtx); err != nil && !strings.Contains(err.Error(), "http: Server closed") {
		fmt.Fprintf(r.Stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}

func (r *Runner) doctor() int {
	cfg, err := loadConfigForDiscovery(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	bootstrapper, err := newBootstrapper()
	if err != nil {
		fmt.Fprintf(r.Stderr, "bootstrap failed: %v\n", err)
		return 1
	}
	report, err := bootstrapper.Check(cfg.Providers)
	router := server.NewWithConfigPath(cfg, config.ResolveConfigPath(r.Config))
	routerSnapshot := router.LiveSnapshot()
	routerModels := router.FunctionalModelSummaries()
	routerReady := routerSnapshot.Health.Models.VerifiedHealthy > 0 && len(routerModels) > 0
	routerReason := "ready"
	if !routerReady {
		switch {
		case routerSnapshot.Health.Models.Catalog == 0:
			routerReason = "no catalog models detected"
		case routerSnapshot.Health.Models.VerifiedHealthy == 0:
			routerReason = "no verified healthy model; run ghrouter verify-models"
		default:
			routerReason = "no functional model list is available"
		}
	}
	type doctorOutput struct {
		local_brain.BootstrapSummary
		RouterReady          bool                  `json:"router_ready"`
		RouterReason         string                `json:"router_reason"`
		RouterModelReadiness server.ModelReadiness `json:"router_model_readiness"`
		Build                BuildIdentity         `json:"build"`
	}
	summary := report.Summary()
	output := doctorOutput{
		BootstrapSummary:     summary,
		RouterReady:          routerReady,
		RouterReason:         routerReason,
		RouterModelReadiness: routerSnapshot.Health.Models,
		Build:                CurrentBuildIdentity(),
	}
	if r.JSON {
		payload, marshalErr := json.MarshalIndent(output, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "doctor render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(payload))
		if err != nil || !routerReady {
			fmt.Fprintln(r.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(r.Stdout, "ready: backend=%s providers=%d\n", report.Backend, len(cfg.Providers))
	fmt.Fprintf(r.Stdout, "router_ready: %t reason=%s verified_healthy=%d catalog=%d\n", routerReady, routerReason, routerSnapshot.Health.Models.VerifiedHealthy, routerSnapshot.Health.Models.Catalog)
	fmt.Fprintf(r.Stdout, "binary_sha256: %s\n", output.Build.BinarySHA256)
	for _, line := range report.SummaryLines() {
		fmt.Fprintln(r.Stdout, line)
	}
	for _, check := range report.Summary().Checks {
		if check.NextStep != "" {
			fmt.Fprintf(r.Stdout, "next\t%s\t%s\t%s\t%s\n", check.Provider, check.Backend, check.Model, check.NextStep)
		}
	}
	for _, suggestion := range report.Summary().Suggestions {
		fmt.Fprintf(r.Stdout, "suggestion\t%s\n", suggestion)
	}
	if err != nil || !routerReady {
		fmt.Fprintln(r.Stderr, err)
		return 1
	}
	return 0
}

func (r *Runner) init() int {
	cfgPath := config.ResolveConfigPath(r.Config)
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Fprintf(r.Stderr, "config already exists: %s\n", cfgPath)
		return 1
	}

	det := detectors.NewDetector()
	providers, err := det.DetectAll()
	if err != nil {
		fmt.Fprintf(r.Stderr, "auto-detect failed: %v\n", err)
		return 1
	}

	port := 9090
	if value := r.prompt("listen port", "9090"); value != "" {
		if parsed, err := parseInt(value); err == nil && parsed > 0 {
			port = parsed
		}
	}

	cfg := &types.Config{ListenPort: port, Providers: providers}
	if len(cfg.Providers) == 0 {
		fmt.Fprintln(r.Stdout, "no CLIs detected; generating empty config")
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		fmt.Fprintf(r.Stderr, "save failed: %v\n", err)
		return 1
	}

	if r.JSON {
		detected := make([]string, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			if p != nil && p.Enabled {
				detected = append(detected, p.Name)
			}
		}
		payload := map[string]any{
			"wrote":     cfgPath,
			"providers": len(cfg.Providers),
			"port":      port,
			"detected":  detected,
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "init render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}

	fmt.Fprintf(r.Stdout, "wrote %s\n", cfgPath)
	return 0
}

func (r *Runner) checkStartup(cfg *types.Config) error {
	bootstrapper, err := newBootstrapper()
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	_, err = bootstrapper.Check(cfg.Providers)
	return err
}

func (r *Runner) printStartupStatus(cfg *types.Config) {
	bootstrapper, err := newBootstrapper()
	if err != nil {
		fmt.Fprintf(r.Stderr, "bootstrap failed: %v\n", err)
		return
	}
	report, reportErr := bootstrapper.Check(cfg.Providers)
	if report.Ready() {
		fmt.Fprintf(r.Stdout, "ready: backend=%s providers=%d\n", report.Backend, len(cfg.Providers))
		return
	}
	fmt.Fprintf(r.Stdout, "startup: backend=%s providers=%d\n", report.Backend, len(cfg.Providers))
	for _, line := range report.SummaryLines() {
		fmt.Fprintln(r.Stdout, line)
	}
	for _, check := range report.Summary().Checks {
		if check.NextStep != "" {
			fmt.Fprintf(r.Stdout, "next\t%s\t%s\t%s\t%s\n", check.Provider, check.Backend, check.Model, check.NextStep)
		}
	}
	for _, suggestion := range report.Summary().Suggestions {
		fmt.Fprintf(r.Stdout, "suggestion\t%s\n", suggestion)
	}
	if reportErr != nil {
		msg := reportErr.Error()
		if strings.Contains(msg, "missing auth") || strings.Contains(msg, "no model configured") {
			observability.Logger("startup").Info("optional_providers_unavailable", "details", msg)
		} else {
			observability.Logger("startup").Warn("optional_providers_unavailable", "details", msg)
		}
	}
}

func (r *Runner) prompt(label, def string) string {
	if r.Stdin == nil {
		return def
	}
	fmt.Fprintf(r.Stdout, "%s [%s]: ", label, def)
	scanner := bufio.NewScanner(r.Stdin)
	if !scanner.Scan() {
		return def
	}
	if text := strings.TrimSpace(scanner.Text()); text != "" {
		return text
	}
	return def
}

func parseInt(raw string) (int, error) {
	var value int
	_, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &value)
	return value, err
}

func (r *Runner) providers() int {
	cfg, err := loadCurrentConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	if r.JSON {
		type providerEntry struct {
			Name    string                    `json:"name"`
			Type    string                    `json:"type"`
			CLIPath string                    `json:"cli_path"`
			Models  []string                  `json:"models"`
			Auth    string                    `json:"auth"`
			Account types.ProviderAccount     `json:"account"`
			Harness types.HarnessCapabilities `json:"harness,omitempty"`
		}
		out := make([]providerEntry, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			if !p.Enabled {
				continue
			}
			accountState := account.Load(p)
			out = append(out, providerEntry{
				Name:    p.Name,
				Type:    string(p.Type),
				CLIPath: p.CLIPath,
				Models:  append([]string(nil), p.Models...),
				Auth:    local_brain.AuthReason(p),
				Account: types.ProviderAccount{
					Plan:      accountState.Plan,
					Balance:   accountState.Balance,
					Currency:  accountState.Currency,
					ResetAt:   accountState.ResetAt.Format(time.RFC3339),
					Source:    accountState.Source,
					Available: accountState.Available,
				},
				Harness: p.Harness,
			})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "providers render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	for _, p := range cfg.Providers {
		if p.Enabled {
			accountState := account.Load(p)
			fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", p.Name, p.Type, p.CLIPath, types.ProviderAccount{
				Plan:      accountState.Plan,
				Balance:   accountState.Balance,
				Currency:  accountState.Currency,
				ResetAt:   accountState.ResetAt.Format(time.RFC3339),
				Source:    accountState.Source,
				Available: accountState.Available,
			}.String())
		}
	}
	return 0
}

type modelListFilter struct {
	functionalOnly bool
	provider       string
	health         string
	capability     string
	cost           string
}

func parseModelListFilter(args []string) (modelListFilter, error) {
	filter := modelListFilter{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "--functional-only":
			filter.functionalOnly = true
		case arg == "--provider", arg == "--health", arg == "--capability", arg == "--cost":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return modelListFilter{}, fmt.Errorf("%s requires a value", arg)
			}
			value := strings.TrimSpace(args[index+1])
			index++
			switch arg {
			case "--provider":
				filter.provider = strings.ToLower(value)
			case "--health":
				filter.health = strings.ToLower(value)
			case "--capability":
				filter.capability = strings.ToLower(value)
			case "--cost":
				filter.cost = strings.ToLower(value)
			}
		case strings.HasPrefix(arg, "--provider="):
			filter.provider = strings.ToLower(strings.TrimPrefix(arg, "--provider="))
		case strings.HasPrefix(arg, "--health="):
			filter.health = strings.ToLower(strings.TrimPrefix(arg, "--health="))
		case strings.HasPrefix(arg, "--capability="):
			filter.capability = strings.ToLower(strings.TrimPrefix(arg, "--capability="))
		case strings.HasPrefix(arg, "--cost="):
			filter.cost = strings.ToLower(strings.TrimPrefix(arg, "--cost="))
		default:
			return modelListFilter{}, fmt.Errorf("unknown models option: %s", arg)
		}
	}
	return filter, nil
}

func matchesModelListFilter(model server.ModelSummary, filter modelListFilter) bool {
	if filter.functionalOnly && !model.List && !strings.EqualFold(model.Health, "healthy") {
		return false
	}
	if filter.provider != "" && !strings.EqualFold(model.OwnedBy, filter.provider) {
		return false
	}
	if filter.health != "" && !strings.EqualFold(model.Health, filter.health) {
		return false
	}
	if filter.cost != "" && !strings.EqualFold(model.CostTier, filter.cost) {
		return false
	}
	if filter.capability != "" {
		capability := strings.TrimPrefix(strings.ToLower(filter.capability), "capability:")
		matched := false
		switch capability {
		case "vision":
			matched = model.Vision
		case "tool-use", "tools", "tool_use":
			matched = model.ToolUse
		case "thinking", "reasoning":
			matched = model.Thinking
		default:
			for _, value := range model.Capabilities {
				if strings.EqualFold(value, capability) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (r *Runner) models(ctx context.Context, args []string) int {
	filter, err := parseModelListFilter(args)
	if err != nil {
		fmt.Fprintf(r.Stderr, "models: %v\nusage: ghrouter models [--functional-only] [--provider NAME] [--health STATUS] [--capability NAME] [--cost TIER]\n", err)
		return 2
	}
	cfg, cleanup, err := r.loadRuntimeConfig(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	defer cleanup()
	srv := server.NewWithConfigPath(cfg, config.ResolveConfigPath(r.Config))
	if r.JSON {
		type modelEntry struct {
			ID            string     `json:"id"`
			OwnedBy       string     `json:"owned_by"`
			Health        string     `json:"health"`
			CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
			CostTier      string     `json:"cost_tier,omitempty"`
			Capabilities  []string   `json:"capabilities,omitempty"`
			Slots         []string   `json:"slots,omitempty"`
			ContextWindow int        `json:"context_window,omitempty"`
			MaxOutput     int        `json:"max_output,omitempty"`
			Thinking      bool       `json:"thinking,omitempty"`
			Vision        bool       `json:"vision,omitempty"`
			ToolUse       bool       `json:"tool_use,omitempty"`
			Effort        []string   `json:"effort,omitempty"`
			CatalogSource string     `json:"catalog_source,omitempty"`
			List          bool       `json:"list,omitempty"`
			Members       []string   `json:"members,omitempty"`
		}
		out := make([]modelEntry, 0)
		for _, m := range srv.FunctionalModelSummaries() {
			if !matchesModelListFilter(m, filter) {
				continue
			}
			var cooldownUntil *time.Time
			if !m.CooldownUntil.IsZero() {
				value := m.CooldownUntil
				cooldownUntil = &value
			}
			out = append(out, modelEntry{ID: m.ID, OwnedBy: m.OwnedBy, Health: m.Health, CooldownUntil: cooldownUntil, CostTier: m.CostTier, Capabilities: m.Capabilities, Slots: m.Slots, ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput, Thinking: m.Thinking, Vision: m.Vision, ToolUse: m.ToolUse, Effort: m.Effort, CatalogSource: m.CatalogSource, List: m.List, Members: m.Members})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "models render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	for _, m := range srv.FunctionalModelSummaries() {
		if !matchesModelListFilter(m, filter) {
			continue
		}
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", m.ID, m.OwnedBy, m.Health)
		if !m.CooldownUntil.IsZero() {
			fmt.Fprintf(r.Stdout, "cooldown_until\t%s\n", m.CooldownUntil.Format(time.RFC3339))
		}
	}
	return 0
}

func (r *Runner) sync() int {
	cfgPath := config.ResolveConfigPath(r.Config)
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	det := detectors.NewDetector()
	provs, err := det.DetectAllFresh()
	if err != nil {
		fmt.Fprintf(r.Stderr, "sync failed: %v\n", err)
		return 1
	}
	cfg.Providers = mergeDetectedProviders(cfg.Providers, provs)
	filterExcludedProviderModels(cfg)
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	if err := config.Save(cfgPath, cfg); err != nil {
		fmt.Fprintf(r.Stderr, "sync save failed: %v\n", err)
		return 1
	}

	if r.JSON {
		payload := map[string]any{
			"wrote":     cfgPath,
			"providers": len(cfg.Providers),
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "sync render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}

	for _, p := range cfg.Providers {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", p.Name, p.CLIPath)
	}
	return 0
}

func mergeDetectedProviders(existing, detected []*types.Provider) []*types.Provider {
	byName := make(map[string]*types.Provider, len(existing))
	byType := make(map[types.ProviderType]*types.Provider, len(existing))
	for _, provider := range existing {
		if provider == nil {
			continue
		}
		byName[provider.Name] = provider
		byType[provider.Type] = provider
	}
	out := make([]*types.Provider, 0, len(existing)+len(detected))
	seen := make(map[string]bool)
	for _, provider := range detected {
		if provider == nil {
			continue
		}
		previous := byName[provider.Name]
		if previous == nil {
			previous = byType[provider.Type]
		}
		merged := provider
		if previous != nil {
			merged = mergeProvider(previous, provider)
		}
		out = append(out, merged)
		seen[merged.Name] = true
	}
	for _, provider := range existing {
		if provider != nil && !seen[provider.Name] {
			out = append(out, provider)
		}
	}
	return out
}

func filterExcludedProviderModels(cfg *types.Config) {
	if cfg == nil {
		return
	}
	for _, provider := range cfg.Providers {
		if provider == nil {
			continue
		}
		kept := make([]string, 0, len(provider.Models))
		for _, model := range provider.Models {
			if !matchesModelPolicyReference(model, cfg.ModelPolicy.Excluded) &&
				(provider.Type != types.ProviderCodex || matchesModelPolicyReference(model, codexAllowedModels)) {
				kept = append(kept, model)
			}
		}
		provider.Models = kept
		for model := range provider.ModelInfo {
			if matchesModelPolicyReference(model, cfg.ModelPolicy.Excluded) ||
				(provider.Type == types.ProviderCodex && !matchesModelPolicyReference(model, codexAllowedModels)) {
				delete(provider.ModelInfo, model)
			}
		}
	}
}

var codexAllowedModels = []string{"cx/*sol", "cx/*terra", "cx/*luna", "cx/gpt-5.4-mini"}

func matchesModelPolicyReference(reference string, patterns []string) bool {
	reference = strings.ToLower(strings.TrimSpace(reference))
	for _, raw := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/*") && strings.HasPrefix(reference, strings.TrimSuffix(pattern, "*")) {
			return true
		}
		if matched, err := path.Match(pattern, reference); err == nil && matched {
			return true
		}
	}
	return false
}

func mergeProvider(previous, detected *types.Provider) *types.Provider {
	merged := *detected
	if previous.Name != "" {
		merged.Name = previous.Name
	}
	if len(previous.Args) > 0 {
		merged.Args = append([]string(nil), previous.Args...)
	}
	if len(previous.Env) > 0 {
		merged.Env = copyStringMap(previous.Env)
	}
	if previous.BaseURL != "" {
		merged.BaseURL = previous.BaseURL
	}
	if !detected.Harness.Observed() && previous.Harness.Observed() {
		merged.Harness = previous.Harness
	}
	if len(detected.Models) > 0 {
		merged.Models = append([]string(nil), detected.Models...)
	} else if len(previous.Models) > 0 {
		merged.Models = append([]string(nil), previous.Models...)
	}
	for model, info := range previous.ModelInfo {
		if info.VerifiedAt.IsZero() || info.VerificationError != "" || !strings.EqualFold(strings.TrimSpace(info.HealthStatus), "healthy") {
			continue
		}
		known := false
		for _, configured := range merged.Models {
			for variant := range modelReferenceVariants(&merged, configured) {
				if modelReferenceVariants(&merged, model)[variant] {
					known = true
					break
				}
			}
			if known {
				break
			}
		}
		if !known {
			merged.Models = append(merged.Models, model)
		}
	}
	// Keep metadata aligned with the refreshed model set. Detected metadata wins,
	// while metadata from the prior snapshot is retained only for models that
	// remain present and were not described by the detector.
	merged.ModelInfo = make(map[string]types.ModelInfo, len(detected.ModelInfo)+len(previous.ModelInfo))
	for model, info := range detected.ModelInfo {
		if previousInfo, ok := previous.ModelInfo[model]; ok {
			info.HealthStatus = previousInfo.HealthStatus
			info.CooldownUntil = previousInfo.CooldownUntil
			info.VerifiedAt = previousInfo.VerifiedAt
			info.VerificationError = previousInfo.VerificationError
		}
		merged.ModelInfo[model] = info
	}
	for _, model := range merged.Models {
		if _, ok := merged.ModelInfo[model]; ok {
			continue
		}
		if info, ok := previous.ModelInfo[model]; ok {
			merged.ModelInfo[model] = info
		}
	}
	if previous.Timeout > 0 {
		merged.Timeout = previous.Timeout
	}
	if previous.Retries > 0 {
		merged.Retries = previous.Retries
	}
	if previous.RetryBackoff > 0 {
		merged.RetryBackoff = previous.RetryBackoff
	}
	if previous.MaxTokens > 0 {
		merged.MaxTokens = previous.MaxTokens
	}
	if previous.WorkDir != "" {
		merged.WorkDir = previous.WorkDir
	}
	if previous.AuthMethod != "" {
		merged.AuthMethod = previous.AuthMethod
	}
	if len(previous.AuthConfig) > 0 {
		merged.AuthConfig = copyStringMap(previous.AuthConfig)
	}
	if len(previous.Accounts) > 0 {
		merged.Accounts = append([]types.ProviderCredential(nil), previous.Accounts...)
	}
	merged.Account = previous.Account
	merged.Enabled = previous.Enabled
	return &merged
}

func copyStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
func (r *Runner) bootstrap() int {
	syncCode := r.sync()
	if syncCode != 0 {
		return syncCode
	}

	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}

	bootstrapper, err := local_brain.NewBootstrapper()
	if err != nil {
		fmt.Fprintf(r.Stderr, "bootstrap failed: %v\n", err)
		return 1
	}

	report, err := bootstrapper.Check(cfg.Providers)
	if r.JSON {
		summary := report.Summary()
		payload := map[string]any{
			"synced":      true,
			"ready":       summary.Ready,
			"backend":     summary.Backend,
			"issues":      summary.Issues,
			"checks":      summary.Checks,
			"provision":   summary.Provision,
			"suggestions": summary.Suggestions,
		}
		b, marshalErr := json.MarshalIndent(payload, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "bootstrap render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		if err != nil || !summary.Ready {
			fmt.Fprintln(r.Stderr, err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(r.Stdout, "sync\tok")
	fmt.Fprintf(r.Stdout, "ready: backend=%s providers=%d\n", report.Backend, len(cfg.Providers))
	summary := report.Summary()
	for _, line := range report.SummaryLines() {
		fmt.Fprintln(r.Stdout, line)
	}
	for _, check := range summary.Checks {
		if check.NextStep != "" {
			fmt.Fprintf(r.Stdout, "next\t%s\t%s\t%s\t%s\n", check.Provider, check.Backend, check.Model, check.NextStep)
		}
	}
	for _, suggestion := range summary.Suggestions {
		fmt.Fprintf(r.Stdout, "suggestion\t%s\n", suggestion)
	}
	if err != nil || !summary.Ready {
		fmt.Fprintln(r.Stderr, err)
		return 1
	}
	return 0
}

func (r *Runner) provision(args []string) int {
	cfg, err := loadConfigForDiscovery(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	bootstrapper, err := newBootstrapper()
	if err != nil {
		fmt.Fprintf(r.Stderr, "bootstrap failed: %v\n", err)
		return 1
	}
	report, checkErr := bootstrapper.Check(cfg.Providers)
	summary := report.Summary()
	planned := append([]local_brain.ProvisionAction(nil), summary.Provision...)
	apply := false
	for _, arg := range args {
		if arg == "--apply" || arg == "-a" {
			apply = true
		}
	}
	if apply {
		if err := applyProvisionPlan(planned); err != nil {
			fmt.Fprintf(r.Stderr, "provision apply failed: %v\n", err)
			return 1
		}
		report, checkErr = bootstrapper.Check(cfg.Providers)
		summary = report.Summary()
	}
	if r.JSON {
		payload := map[string]any{
			"ready":             summary.Ready,
			"backend":           summary.Backend,
			"checks":            summary.Checks,
			"provision":         summary.Provision,
			"planned_provision": planned,
			"applied":           apply,
		}
		b, marshalErr := json.MarshalIndent(payload, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "provision render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		if checkErr != nil || !summary.Ready {
			fmt.Fprintln(r.Stderr, checkErr)
			return 1
		}
		return 0
	}
	for _, action := range planned {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", action.Provider, action.Backend, action.Action, strings.Join(action.Command, " "))
	}
	if apply {
		if summary.Ready {
			fmt.Fprintln(r.Stdout, "apply\tready")
		} else {
			fmt.Fprintf(r.Stdout, "apply\tpending\t%d action(s) remain\n", len(summary.Provision))
		}
	}
	if checkErr != nil || !summary.Ready {
		fmt.Fprintln(r.Stderr, checkErr)
		return 1
	}
	return 0
}

func applyProvisionPlan(actions []local_brain.ProvisionAction) error {
	manager, err := local_brain.NewModelManager()
	if err != nil {
		return err
	}
	if err := manager.Prepare(); err != nil {
		return err
	}
	planPath := filepath.Join(manager.CacheDir(), "provision-plan.json")
	data, err := json.MarshalIndent(actions, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		return err
	}
	return local_brain.ExecuteProvisionPlan(context.Background(), actions, nil)
}

type configBundle struct {
	Version  string              `json:"version"`
	Config   *types.Config       `json:"config"`
	Snapshot server.LiveSnapshot `json:"snapshot"`
}

func (r *Runner) export() int {
	cfg, err := loadCurrentConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	bundle := configBundle{
		Version:  Version,
		Config:   cfg,
		Snapshot: server.New(cfg).LiveSnapshot(),
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		fmt.Fprintf(r.Stderr, "export render failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(r.Stdout, string(data))
	return 0
}

func (r *Runner) importBundle(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(r.Stderr, "import read failed: %v\n", err)
		return 1
	}
	var bundle configBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		fmt.Fprintf(r.Stderr, "import parse failed: %v\n", err)
		return 1
	}
	if bundle.Config == nil {
		fmt.Fprintln(r.Stderr, "import bundle missing config")
		return 1
	}
	cfgPath := config.ResolveConfigPath(r.Config)
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	if err := config.Save(cfgPath, bundle.Config); err != nil {
		fmt.Fprintf(r.Stderr, "import save failed: %v\n", err)
		return 1
	}
	if r.JSON {
		payload := map[string]any{
			"imported": cfgPath,
			"version":  bundle.Version,
		}
		out, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "import render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(out))
		return 0
	}
	fmt.Fprintf(r.Stdout, "imported %s\n", cfgPath)
	return 0
}

func (r *Runner) routes() int {
	cfg, err := loadCurrentConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	if r.JSON {
		type routeEntry struct {
			Pattern  string   `json:"pattern"`
			Provider string   `json:"provider"`
			Fallback []string `json:"fallback"`
		}
		out := make([]routeEntry, 0, len(cfg.Routes))
		for _, route := range cfg.Routes {
			out = append(out, routeEntry{
				Pattern:  route.Pattern,
				Provider: route.Provider,
				Fallback: append([]string(nil), route.Fallback...),
			})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "routes render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	for _, route := range cfg.Routes {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", route.Pattern, route.Provider, strings.Join(route.Fallback, ","))
	}
	return 0
}

func (r *Runner) live(ctx context.Context) int {
	cfgPath := config.ResolveConfigPath(r.Config)
	cfg, err := loadCurrentConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	local_brain.EnsureMandatoryConfig(&cfg.LocalBrain)
	localSupervisor, err := attachLocalBrain(ctx, cfg)
	if err != nil {
		fmt.Fprintf(r.Stderr, "local brain unavailable; live is degraded to an eligible fast backup: %v\n", err)
	}
	if localSupervisor != nil {
		defer localSupervisor.Stop()
	}
	srv := server.New(cfg)
	if r.JSON {
		return r.renderLiveJSON(srv)
	}
	if !isInteractiveWriter(r.Stdout) {
		if err := r.renderLiveSnapshot(srv); err != nil {
			fmt.Fprintf(r.Stderr, "live snapshot failed: %v\n", err)
			return 1
		}
		return 0
	}
	if err := runLiveTUI(cfg, cfgPath, ctx.Done()); err != nil {
		fmt.Fprintf(r.Stderr, "live tui failed: %v\n", err)
		return 1
	}
	return 0
}

func attachLocalBrain(ctx context.Context, cfg *types.Config) (*local_brain.Supervisor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("local brain configuration is missing")
	}
	if !cfg.LocalBrain.Enabled {
		return nil, nil
	}
	host := strings.TrimSpace(cfg.LocalBrain.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.LocalBrain.Port
	if port <= 0 {
		port = 19090
	}
	if cfg.ListenPort > 0 && cfg.ListenPort == port {
		return nil, fmt.Errorf("local brain port %d conflicts with router listen port", port)
	}
	model := strings.TrimSpace(cfg.LocalBrain.Model)
	if model == "" {
		model = strings.TrimSpace(cfg.LocalBrain.Source)
	}
	if endpoint, advertisedModel, found := discoverLocalBrainEndpoint(ctx, host, port, cfg.ListenPort, model); found {
		toolCtx, toolCancel := context.WithTimeout(ctx, 30*time.Second)
		toolErr := local_brain.VerifyTools(toolCtx, endpoint, advertisedModel)
		toolCancel()
		if toolErr == nil {
			setLocalBrainProvider(cfg, endpoint, localBrainModels(model, cfg.LocalBrain.AllowModelSwitch), localBrainVerifiedModel(advertisedModel, model), true)
			return nil, nil
		}
		if cfg.LocalBrain.ManagedExternally {
			if probeErr := local_brain.Probe(ctx, endpoint); probeErr != nil {
				return nil, fmt.Errorf("external local brain is not ready: %w", probeErr)
			}
			textCtx, textCancel := context.WithTimeout(ctx, 30*time.Second)
			textErr := local_brain.VerifyText(textCtx, endpoint, advertisedModel)
			textCancel()
			if textErr != nil {
				return nil, fmt.Errorf("external local brain text inference failed: %w", textErr)
			}
			setLocalBrainProvider(cfg, endpoint, localBrainModels(model, cfg.LocalBrain.AllowModelSwitch), localBrainVerifiedModel(advertisedModel, model), false)
			return nil, nil
		}
	}
	if cfg.LocalBrain.ManagedExternally {
		return nil, fmt.Errorf("external local brain is not available at %s:%d", host, port)
	}
	brainConfig := cfg.LocalBrain
	if selectedPort, portErr := selectLocalBrainPort(host, port, cfg.ListenPort); portErr != nil {
		return nil, portErr
	} else if selectedPort != port {
		brainConfig.Port = selectedPort
	}
	supervisor, err := local_brain.NewSupervisor(brainConfig)
	if err != nil {
		return nil, err
	}
	status, err := supervisor.Start(ctx)
	if err != nil {
		return nil, err
	}
	setLocalBrainProvider(cfg, status.URL, localBrainModels(status.Model, cfg.LocalBrain.AllowModelSwitch), status.Model, false)
	return supervisor, nil
}

func discoverLocalBrainEndpoint(ctx context.Context, host string, preferred, routerPort int, configuredModel string) (string, string, bool) {
	if !isLoopbackHost(host) {
		return "", "", false
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := &http.Client{Timeout: 150 * time.Millisecond}
	results := make(chan struct {
		endpoint string
		model    string
	}, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(offset int) {
			defer workers.Done()
			for port := preferred + offset; port < preferred+64; port += 8 {
				if port == routerPort || scanCtx.Err() != nil {
					continue
				}
				endpoint := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
				request, err := http.NewRequestWithContext(scanCtx, http.MethodGet, endpoint+"/v1/models", nil)
				if err != nil {
					continue
				}
				response, err := client.Do(request)
				if err != nil {
					continue
				}
				var payload struct {
					Data []struct {
						ID      string `json:"id"`
						OwnedBy string `json:"owned_by"`
					} `json:"data"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&payload)
				_ = response.Body.Close()
				if decodeErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
					continue
				}
				for _, model := range payload.Data {
					if isLocalBrainEndpointModel(model.ID, model.OwnedBy, configuredModel) {
						select {
						case results <- struct {
							endpoint string
							model    string
						}{endpoint: endpoint, model: model.ID}:
							cancel()
						default:
						}
						return
					}
				}
			}
		}(worker)
	}
	defer workers.Wait()
	completed := make(chan struct{})
	go func() {
		workers.Wait()
		close(completed)
	}()
	select {
	case result := <-results:
		return result.endpoint, result.model, true
	case <-completed:
	}
	return "", "", false
}

func isLocalBrainModel(advertised, configured string) bool {
	advertised = strings.TrimSpace(advertised)
	configured = strings.TrimSpace(configured)
	if strings.EqualFold(advertised, "ghrouter/local-brain") {
		return true
	}
	if configured == "" {
		return false
	}
	if strings.EqualFold(advertised, configured) {
		return true
	}
	advertisedSlug := strings.ToLower(strings.ReplaceAll(path.Base(filepath.Clean(advertised)), "/", "-"))
	configuredSlug := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(configured, "hf://"), "/", "-"))
	return advertisedSlug != "" && advertisedSlug == configuredSlug
}

func isLocalBrainEndpointModel(advertised, ownedBy, configured string) bool {
	if strings.EqualFold(strings.TrimSpace(ownedBy), "ghrouter") {
		return false
	}
	return isLocalBrainModel(advertised, configured)
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func selectLocalBrainPort(host string, preferred, routerPort int) (int, error) {
	if preferred <= 0 {
		preferred = 19090
	}
	for port := preferred; port < preferred+64; port++ {
		if port == routerPort {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no loopback port available for local brain near %d", preferred)
}

func localBrainModels(model string, allowModelSwitch bool) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = local_brain.DefaultModel
	}
	models := []string{model}
	if model != local_brain.DefaultModel || !allowModelSwitch {
		return models
	}
	manager, err := local_brain.NewModelManager()
	if err != nil {
		return models
	}
	if _, err := manager.EnsureModelAvailable(local_brain.BackendMLX, local_brain.DefaultCompanionModel); err == nil {
		models = append(models, local_brain.DefaultCompanionModel)
	}
	return models
}

func setLocalBrainProvider(cfg *types.Config, baseURL string, models []string, verifiedModel string, toolVerified bool) {
	if len(models) == 0 {
		return
	}
	observability.Logger("local-brain").Info("local_brain_attached", "endpoint", strings.TrimRight(strings.TrimSpace(baseURL), "/"), "models", len(models), "verified_model", verifiedModel)
	for _, provider := range cfg.Providers {
		if provider != nil && provider.Name == "local-brain" {
			provider.BaseURL = baseURL
			provider.Models = append([]string(nil), models...)
			markLocalBrainModels(provider, verifiedModel, toolVerified)
			provider.Enabled = true
			return
		}
	}
	provider := &types.Provider{
		Name:       "local-brain",
		Type:       types.ProviderLocal,
		BaseURL:    baseURL,
		Models:     append([]string(nil), models...),
		ModelInfo:  make(map[string]types.ModelInfo, len(models)),
		Timeout:    5 * time.Minute,
		Enabled:    true,
		AuthMethod: types.AuthNone,
	}
	markLocalBrainModels(provider, verifiedModel, toolVerified)
	cfg.Providers = append(cfg.Providers, provider)
}

func localBrainVerifiedModel(advertisedModel, configuredModel string) string {
	if strings.EqualFold(strings.TrimSpace(advertisedModel), "ghrouter/local-brain") && strings.TrimSpace(configuredModel) != "" {
		return configuredModel
	}
	return advertisedModel
}

func markLocalBrainToolModels(provider *types.Provider, verifiedModel string) {
	markLocalBrainModels(provider, verifiedModel, true)
}

func markLocalBrainModels(provider *types.Provider, verifiedModel string, toolVerified bool) {
	if provider == nil {
		return
	}
	if provider.ModelInfo == nil {
		provider.ModelInfo = make(map[string]types.ModelInfo, len(provider.Models))
	}
	for _, model := range provider.Models {
		info := provider.ModelInfo[model]
		info.Provider = provider.Name
		info.Model = model
		verified := isLocalBrainModel(model, verifiedModel) || isLocalBrainModel(verifiedModel, model)
		info.ToolUse = verified && toolVerified
		if verified {
			info.HealthStatus = "healthy"
			info.VerifiedAt = time.Now().UTC()
			info.VerificationError = ""
		} else {
			info.HealthStatus = ""
			info.VerifiedAt = time.Time{}
			info.VerificationError = ""
		}
		if strings.TrimSpace(info.Source) == "" {
			info.Source = "native"
		}
		provider.ModelInfo[model] = info
	}
}

func (r *Runner) attach(ctx context.Context, args []string) int {
	cfgPath := config.ResolveConfigPath(r.Config)
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.ListenPort)
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		baseURL = strings.TrimRight(args[0], "/")
	}
	source := attachedSource{baseURL: baseURL, client: &http.Client{Timeout: 5 * time.Second}}
	if r.JSON || !isInteractiveWriter(r.Stdout) {
		snapshot, report, snapshotErr := source.Snapshot()
		if snapshotErr != nil {
			fmt.Fprintf(r.Stderr, "attach failed: %v\n", snapshotErr)
			return 1
		}
		payload := server.LiveResponse{Snapshot: snapshot, Bootstrap: report.Summary()}
		out, marshalErr := json.MarshalIndent(payload, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "attach render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(out))
		return 0
	}
	if err := runLiveTUIWithSource(cfg, cfgPath, ctx.Done(), source); err != nil {
		fmt.Fprintf(r.Stderr, "attach tui failed: %v\n", err)
		return 1
	}
	return 0
}

func (r *Runner) renderLiveSnapshot(srv *server.Server) error {
	b, err := json.MarshalIndent(srv.LiveSnapshot(), "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(r.Stdout, string(b))
	return nil
}

func (r *Runner) renderLiveJSON(srv *server.Server) int {
	b, err := json.MarshalIndent(srv.LiveSnapshot(), "", "  ")
	if err != nil {
		fmt.Fprintf(r.Stderr, "live snapshot failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(r.Stdout, string(b))
	return 0
}

func isInteractiveWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (r *Runner) config() int {
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(r.Stderr, "config render failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(r.Stdout, string(b))
	return 0
}

func (r *Runner) ping(ctx context.Context) int {
	cfg, cleanup, err := r.loadRuntimeConfig(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	defer cleanup()
	srv := server.New(cfg)
	snap := srv.LiveSnapshot()
	if r.JSON {
		payload := map[string]any{
			"ok":        true,
			"port":      snap.ListenPort,
			"providers": len(snap.Providers),
			"models":    len(snap.Models),
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "ping render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintf(r.Stdout, "ok\tport=%d\tproviders=%d\tmodels=%d\n", snap.ListenPort, len(snap.Providers), len(snap.Models))
	return 0
}

func (r *Runner) version() int {
	identity := CurrentBuildIdentity()
	if r.JSON {
		b, err := json.MarshalIndent(identity, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "version render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s\tbinary_sha256=%s", identity.Version, identity.BinarySHA256)
	if identity.VCSRevision != "" {
		fmt.Fprintf(r.Stdout, "\tvcs_revision=%s", identity.VCSRevision)
	}
	fmt.Fprintln(r.Stdout)
	return 0
}

func (r *Runner) update(args []string) int {
	apply := false
	for _, arg := range args {
		if arg == "--apply" || arg == "-a" {
			apply = true
		}
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	repo := os.Getenv("GHR_UPDATE_REPO")
	if repo == "" {
		repo = "jcafeitosa/ghrouter"
	}
	apiBase := os.Getenv("GHR_UPDATE_API_BASE")
	client := update.NewClient(repo, apiBase, Version, httpClient, update.OSFileSystem{})
	ctx := context.Background()
	if apply {
		target := os.Getenv("GHR_UPDATE_TARGET")
		if target == "" {
			exe, err := os.Executable()
			if err != nil {
				fmt.Fprintf(r.Stderr, "update target failed: %v\n", err)
				return 1
			}
			target = exe
		}
		res, err := client.Apply(ctx, target)
		if err != nil {
			fmt.Fprintf(r.Stderr, "update failed: %v\n", err)
			return 1
		}
		if r.JSON {
			b, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				fmt.Fprintf(r.Stderr, "update render failed: %v\n", err)
				return 1
			}
			fmt.Fprintln(r.Stdout, string(b))
			return 0
		}
		fmt.Fprintf(r.Stdout, "current=%s latest=%s applied=%v target=%s\n", res.CurrentVersion, res.LatestVersion, res.Applied, res.TargetPath)
		return 0
	}
	res, err := client.Check(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "update check failed: %v\n", err)
		return 1
	}
	if r.JSON {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "update render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintf(r.Stdout, "current=%s latest=%s available=%v\n", res.CurrentVersion, res.LatestVersion, res.UpdateAvailable)
	if res.AssetName != "" {
		fmt.Fprintf(r.Stdout, "asset=%s\n", res.AssetName)
	}
	return 0
}

func (r *Runner) reset(args []string) int {
	apply := false
	for _, arg := range args {
		if arg == "--apply" || arg == "-a" {
			apply = true
		}
	}
	targets := discoverResetTargets()
	active := make([]resetTarget, 0, len(targets))
	for _, target := range targets {
		if _, err := os.Stat(target.Path); err == nil {
			active = append(active, target)
		}
	}
	if apply {
		home, _ := os.UserHomeDir()
		backupRoot := resetBackupRoot(home)
		for i := range active {
			backup, err := backupResetTarget(active[i].Path, backupRoot, i)
			if err != nil {
				fmt.Fprintf(r.Stderr, "reset failed: %v\n", err)
				return 1
			}
			active[i].Removed = true
			active[i].Backup = backup
		}
	}
	if r.JSON {
		payload := map[string]any{
			"applied": apply,
			"targets": active,
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "reset render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	if len(active) == 0 {
		fmt.Fprintln(r.Stdout, "no provider config files found")
		return 0
	}
	fmt.Fprintln(r.Stdout, summarizeResetTargets(active))
	if apply {
		fmt.Fprintln(r.Stdout, "reset\tok")
	}
	return 0
}

func (r *Runner) test(ctx context.Context, model string) int {
	cfg, cleanup, err := r.loadRuntimeConfig(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	defer cleanup()
	srv := server.NewWithConfigPath(cfg, config.ResolveConfigPath(r.Config))
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := srv.TestModel(probeCtx, model)
	if r.JSON {
		payload := map[string]any{
			"requested":  model,
			"provider":   result.Provider,
			"model":      result.Model,
			"status":     result.Status,
			"ok":         result.OK,
			"latency_ms": result.LatencyMS,
		}
		if result.Error != "" {
			payload["error"] = result.Error
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "test render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		if !result.OK {
			return 1
		}
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%dms\n", result.Status, result.Provider, result.Model, result.LatencyMS)
	if result.Error != "" {
		fmt.Fprintf(r.Stdout, "error\t%s\n", result.Error)
	}
	if !result.OK {
		return 1
	}
	return 0
}

func (r *Runner) probe(ctx context.Context, model string) int {
	cfg, err := loadCurrentConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	srv := server.New(cfg)
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := srv.TestModel(probeCtx, model)
	applyModelVerification(cfg, []server.ModelTestResult{result}, time.Now().UTC())
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	if err := config.Save(config.ResolveConfigPath(r.Config), cfg); err != nil {
		fmt.Fprintf(r.Stderr, "probe config persistence failed: %v\n", err)
		return 1
	}
	if persistErr := srv.PersistCurrentState(); persistErr != nil {
		fmt.Fprintf(r.Stderr, "probe state persistence failed: %v\n", persistErr)
		return 1
	}
	if r.JSON {
		b, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "probe render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
	} else {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%dms\n", result.Status, result.Provider, result.Model, result.LatencyMS)
		if result.Error != "" {
			fmt.Fprintf(r.Stdout, "error\t%s\n", result.Error)
		}
		if !result.CooldownUntil.IsZero() {
			fmt.Fprintf(r.Stdout, "cooldown_until\t%s\n", result.CooldownUntil.Format(time.RFC3339))
		}
	}
	if !result.OK {
		return 1
	}
	return 0
}

func (r *Runner) verifyModels(ctx context.Context, requested []string) int {
	load := loadConfigForDiscovery
	if len(requested) > 0 {
		load = loadCurrentConfig
	}
	cfg, err := load(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	srv := server.New(cfg)
	var ordered []server.ModelTestResult
	if len(requested) == 0 {
		ordered = srv.VerifyConfiguredModels(ctx)
	}
	targets := make([]string, 0)
	if len(requested) > 0 {
		targets = append(targets, requested...)
	} else if len(ordered) == 0 {
		fmt.Fprintln(r.Stderr, "no models available for verification")
		return 1
	}
	if len(requested) > 0 && len(targets) == 0 {
		fmt.Fprintln(r.Stderr, "no models available for verification")
		return 1
	}
	type result struct {
		index  int
		status server.ModelTestResult
	}
	results := make(chan result, len(targets))
	jobs := make(chan int)
	workers := 4
	if workers > len(targets) {
		workers = len(targets)
	}
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				results <- result{index: index, status: srv.TestModel(probeCtx, targets[index])}
				cancel()
			}
		}()
	}
	go func() {
		for i := range targets {
			jobs <- i
		}
		close(jobs)
		group.Wait()
		close(results)
	}()
	if len(requested) > 0 {
		ordered = make([]server.ModelTestResult, len(targets))
		for item := range results {
			ordered[item.index] = item.status
		}
	}
	failed := 0
	if r.JSON {
		b, marshalErr := json.MarshalIndent(ordered, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "verify render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
	} else {
		for _, item := range ordered {
			fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%dms\n", item.Status, item.Provider, item.Model, item.LatencyMS)
			if item.Error != "" {
				fmt.Fprintf(r.Stdout, "error\t%s\n", item.Error)
			}
			if !item.CooldownUntil.IsZero() {
				fmt.Fprintf(r.Stdout, "cooldown_until\t%s\n", item.CooldownUntil.Format(time.RFC3339))
			}
		}
	}
	for _, item := range ordered {
		if !item.OK && item.Status != "cooldown" {
			failed++
		}
	}
	applyModelVerification(cfg, ordered, time.Now().UTC())
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	if err := config.Save(config.ResolveConfigPath(r.Config), cfg); err != nil {
		fmt.Fprintf(r.Stderr, "verification config persistence failed: %v\n", err)
		return 1
	}
	if persistErr := srv.PersistCurrentState(); persistErr != nil {
		fmt.Fprintf(r.Stderr, "verification state persistence failed: %v\n", persistErr)
		return 1
	}
	if failed > 0 {
		fmt.Fprintf(r.Stderr, "model verification failed: %d/%d\n", failed, len(ordered))
		return 1
	}
	return 0
}

func applyModelVerification(cfg *types.Config, results []server.ModelTestResult, verifiedAt time.Time) {
	if cfg == nil {
		return
	}
	for _, result := range results {
		if result.Provider == "" || result.Model == "" || result.Status == "cooldown" {
			continue
		}
		for _, provider := range cfg.Providers {
			if provider == nil || provider.Name != result.Provider {
				continue
			}
			if provider.ModelInfo == nil {
				provider.ModelInfo = make(map[string]types.ModelInfo)
			}
			if result.Status == "healthy" {
				known := false
				for _, configured := range provider.Models {
					for variant := range modelReferenceVariants(provider, configured) {
						if modelReferenceVariants(provider, result.Model)[variant] {
							known = true
							break
						}
					}
					if known {
						break
					}
				}
				if !known {
					provider.Models = append(provider.Models, result.Model)
				}
			}
			infoKey := modelInfoKeyForVerification(provider, result.Model)
			info := provider.ModelInfo[infoKey]
			info.HealthStatus = result.Status
			info.CooldownUntil = result.CooldownUntil
			if result.Status == "healthy" {
				info.VerifiedAt = verifiedAt
				info.VerificationError = ""
			} else if strings.TrimSpace(result.Error) != "" {
				info.VerificationError = result.Error
			}
			provider.ModelInfo[infoKey] = info
		}
	}
}

func modelInfoKeyForVerification(provider *types.Provider, model string) string {
	if provider == nil {
		return strings.TrimSpace(model)
	}
	model = strings.TrimSpace(model)
	if _, ok := provider.ModelInfo[model]; ok {
		return model
	}
	variants := modelReferenceVariants(provider, model)
	for key := range provider.ModelInfo {
		for variant := range modelReferenceVariants(provider, key) {
			if variants[variant] {
				return key
			}
		}
	}
	return model
}

func modelReferenceVariants(provider *types.Provider, model string) map[string]bool {
	model = strings.TrimSpace(model)
	variants := map[string]bool{}
	if model == "" {
		return variants
	}
	variants[model] = true
	if provider == nil {
		return variants
	}
	if prefix := providerModelPrefix(provider.Type); prefix != "" && strings.HasPrefix(model, prefix) {
		variants[strings.TrimPrefix(model, prefix)] = true
	}
	if provider.Name != "" && strings.HasPrefix(model, provider.Name+"/") {
		variants[strings.TrimPrefix(model, provider.Name+"/")] = true
	}
	return variants
}

func providerModelPrefix(providerType types.ProviderType) string {
	switch providerType {
	case types.ProviderClaudeCode:
		return "cc/"
	case types.ProviderCodex:
		return "cx/"
	case types.ProviderOpenCode:
		return "oc/"
	case types.ProviderMimo:
		return "mi/"
	case types.ProviderPi:
		return "pi/"
	case types.ProviderCursor:
		return "cu/"
	case types.ProviderNVIDIA:
		return "nv/"
	default:
		return ""
	}
}

func (r *Runner) explain(ctx context.Context, model string) int {
	cfg, cleanup, err := r.loadRuntimeConfig(ctx)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	defer cleanup()
	srv := server.New(cfg)
	explanation := srv.ExplainRequest(&types.OpenAIRequest{Model: model})
	if explanation.Selected == nil {
		fmt.Fprintf(r.Stdout, "unrouted\t%s\n", model)
		return 1
	}
	if r.JSON {
		b, err := json.MarshalIndent(explanation, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "explain render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", explanation.Selected.Provider, explanation.Selected.Model, explanation.SelectionSource)
	return 0
}

func loadConfig(rawPath string) (*types.Config, error) {
	cfgPath := config.ResolveConfigPath(rawPath)
	cfg, err := config.Load(cfgPath)
	detected := false
	if err != nil && os.IsNotExist(err) {
		cfg = &types.Config{ListenPort: 9090}
		detected = true
	} else if err != nil {
		return nil, err
	}
	if detected {
		det := detectors.NewDetector()
		provs, err := det.DetectAll()
		if err != nil {
			return nil, err
		}
		cfg.Providers = provs
	}
	for _, provider := range cfg.Providers {
		if provider == nil {
			continue
		}
		if provider.CLIPath != "" {
			pathLooksCompatible := provider.Type != types.ProviderCursor || filepath.Base(provider.CLIPath) == "cursor"
			if pathLooksCompatible {
				if _, statErr := os.Stat(provider.CLIPath); statErr == nil {
					continue
				}
			}
		}
		provider.CLIPath = detectors.ResolveCLIPath(provider.Type)
	}
	if !detected {
		for _, provider := range cfg.Providers {
			detectors.EnrichProviderModels(provider)
		}
	}
	filterExcludedProviderModels(cfg)
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	return cfg, nil
}

func loadCurrentConfig(rawPath string) (*types.Config, error) {
	cfg, err := loadConfig(rawPath)
	if err != nil {
		return nil, err
	}
	det := detectors.NewDetector()
	provs, err := det.DetectAll()
	if err != nil {
		return nil, err
	}
	cfg.Providers = mergeDetectedProviders(cfg.Providers, provs)
	filterExcludedProviderModels(cfg)
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	return cfg, nil
}

func (r *Runner) loadRuntimeConfig(ctx context.Context) (*types.Config, func(), error) {
	cfg, err := loadCurrentConfig(r.Config)
	if err != nil {
		return nil, nil, err
	}
	runtimeCfg := *cfg
	runtimeCfg.Providers = append([]*types.Provider(nil), cfg.Providers...)
	if !runtimeCfg.LocalBrain.ManagedExternally {
		return &runtimeCfg, func() {}, nil
	}
	supervisor, err := attachLocalBrain(ctx, &runtimeCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("attach external local brain: %w", err)
	}
	return &runtimeCfg, func() {
		if supervisor != nil {
			_ = supervisor.Stop()
		}
	}, nil
}

func loadConfigForDiscovery(rawPath string) (*types.Config, error) {
	cfg, err := loadConfig(rawPath)
	if err != nil || len(cfg.Providers) > 0 {
		return cfg, err
	}
	det := detectors.NewDetector()
	providers, err := det.DetectAll()
	if err != nil {
		return nil, err
	}
	cfg.Providers = providers
	filterExcludedProviderModels(cfg)
	cfg.ModelLists = detectors.BuildAutomaticModelLists(cfg.Providers, cfg.ModelLists)
	return cfg, nil
}

func parseArgs(args []string) ([]string, string, bool, bool) {
	var out []string
	var cfgPath string
	showVersion := false
	showHelp := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case "--version", "-v":
			showVersion = true
		case "--help", "-h":
			showHelp = true
		default:
			out = append(out, args[i])
		}
	}
	return out, cfgPath, showVersion, showHelp
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "-j" {
			return true
		}
	}
	return false
}

func stripJSONFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" || arg == "-j" {
			continue
		}
		out = append(out, arg)
	}
	return out
}
