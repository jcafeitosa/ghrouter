package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/config"
	"ghrouter/internal/detectors"
	"ghrouter/internal/local_brain"
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
	runnerArgs, cfgPath, showVersion := parseArgs(args)
	if showVersion {
		fmt.Fprintln(os.Stdout, Version)
		return 0
	}
	r := &Runner{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Config: cfgPath}
	return r.Run(ctx, runnerArgs)
}

func (r *Runner) Run(ctx context.Context, args []string) int {
	args, cfgPath, showVersion := parseArgs(args)
	if showVersion {
		fmt.Fprintln(r.Stdout, Version)
		return 0
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
	case "serve":
		return r.serve(ctx)
	case "doctor":
		return r.doctor()
	case "init":
		return r.init()
	case "providers":
		return r.providers()
	case "models":
		return r.models()
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
	case "config":
		return r.config()
	case "ping":
		return r.ping()
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
		return r.test(args[1])
	case "explain":
		if len(args) < 2 {
			fmt.Fprintln(r.Stderr, "usage: ghrouter explain <model>")
			return 2
		}
		return r.explain(args[1])
	default:
		fmt.Fprintf(r.Stderr, "unknown command: %s\n", args[0])
		return 2
	}
}

func (r *Runner) serve(ctx context.Context) int {
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
	r.printStartupStatus(cfg)
	srv := server.New(cfg)
	if err := srv.ListenAndServe(ctx); err != nil && !strings.Contains(err.Error(), "http: Server closed") {
		fmt.Fprintf(r.Stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}

func (r *Runner) doctor() int {
	cfg, err := loadConfig(r.Config)
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
	if r.JSON {
		payload, marshalErr := json.MarshalIndent(report.Summary(), "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "doctor render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(payload))
		if err != nil {
			fmt.Fprintln(r.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(r.Stdout, "ready: backend=%s providers=%d\n", report.Backend, len(cfg.Providers))
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
	if err != nil {
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
		fmt.Fprintln(r.Stderr, reportErr)
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
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	if r.JSON {
		type providerEntry struct {
			Name    string                `json:"name"`
			Type    string                `json:"type"`
			CLIPath string                `json:"cli_path"`
			Models  []string              `json:"models"`
			Auth    string                `json:"auth"`
			Account types.ProviderAccount `json:"account"`
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

func (r *Runner) models() int {
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	srv := server.New(cfg)
	if r.JSON {
		type modelEntry struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		}
		out := make([]modelEntry, 0)
		for _, m := range srv.ModelSummaries() {
			out = append(out, modelEntry{ID: m.ID, OwnedBy: m.OwnedBy})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "models render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	for _, m := range srv.ModelSummaries() {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", m.ID, m.OwnedBy)
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
	provs, err := det.DetectAll()
	if err != nil {
		fmt.Fprintf(r.Stderr, "sync failed: %v\n", err)
		return 1
	}
	cfg.Providers = provs
	if err := config.Save(cfgPath, cfg); err != nil {
		fmt.Fprintf(r.Stderr, "sync save failed: %v\n", err)
		return 1
	}

	if r.JSON {
		payload := map[string]any{
			"wrote":     cfgPath,
			"providers": len(provs),
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "sync render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}

	for _, p := range provs {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", p.Name, p.CLIPath)
	}
	return 0
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
		if err != nil {
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
	if err != nil {
		fmt.Fprintln(r.Stderr, err)
		return 1
	}
	return 0
}

func (r *Runner) provision(args []string) int {
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
	summary := report.Summary()
	apply := false
	for _, arg := range args {
		if arg == "--apply" || arg == "-a" {
			apply = true
		}
	}
	if apply {
		if err := applyProvisionPlan(summary.Provision); err != nil {
			fmt.Fprintf(r.Stderr, "provision apply failed: %v\n", err)
			return 1
		}
		if err != nil {
			fmt.Fprintf(r.Stderr, "provision apply warning: %v\n", err)
			err = nil
		}
	}
	if r.JSON {
		payload := map[string]any{
			"ready":     summary.Ready,
			"backend":   summary.Backend,
			"checks":    summary.Checks,
			"provision": summary.Provision,
			"applied":   apply,
		}
		b, marshalErr := json.MarshalIndent(payload, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(r.Stderr, "provision render failed: %v\n", marshalErr)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		if err != nil {
			fmt.Fprintln(r.Stderr, err)
			return 1
		}
		return 0
	}
	for _, action := range summary.Provision {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\t%s\n", action.Provider, action.Backend, action.Action, strings.Join(action.Command, " "))
	}
	if apply {
		fmt.Fprintln(r.Stdout, "apply\tok")
	}
	if err != nil {
		fmt.Fprintln(r.Stderr, err)
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
	return os.WriteFile(planPath, data, 0o600)
}

type configBundle struct {
	Version  string              `json:"version"`
	Config   *types.Config       `json:"config"`
	Snapshot server.LiveSnapshot `json:"snapshot"`
}

func (r *Runner) export() int {
	cfg, err := loadConfig(r.Config)
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
	cfg, err := loadConfig(r.Config)
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
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
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

func (r *Runner) ping() int {
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
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
	if r.JSON {
		payload := map[string]any{"version": Version}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "version render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintln(r.Stdout, Version)
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
		for i := range active {
			if err := removeResetTarget(active[i].Path); err != nil {
				fmt.Fprintf(r.Stderr, "reset failed: %v\n", err)
				return 1
			}
			active[i].Removed = true
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

func (r *Runner) test(model string) int {
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	srv := server.New(cfg)
	provider, resolved := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: model})
	if provider == "" {
		fmt.Fprintf(r.Stderr, "no provider for %s\n", model)
		return 1
	}
	if r.JSON {
		payload := map[string]any{
			"requested": model,
			"provider":  provider,
			"model":     resolved,
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "test render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s\t%s\n", provider, resolved)
	return 0
}

func (r *Runner) explain(model string) int {
	cfg, err := loadConfig(r.Config)
	if err != nil {
		fmt.Fprintf(r.Stderr, "config load failed: %v\n", err)
		return 1
	}
	srv := server.New(cfg)
	provider, resolved := srv.RouteModel(model)
	if provider == "" {
		fmt.Fprintf(r.Stdout, "unrouted\t%s\n", model)
		return 1
	}
	if r.JSON {
		payload := map[string]any{
			"requested": model,
			"provider":  provider,
			"model":     resolved,
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(r.Stderr, "explain render failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(r.Stdout, string(b))
		return 0
	}
	fmt.Fprintf(r.Stdout, "%s\t%s\n", provider, resolved)
	return 0
}

func loadConfig(rawPath string) (*types.Config, error) {
	cfgPath := config.ResolveConfigPath(rawPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		cfg = &types.Config{ListenPort: 9090}
	}
	if len(cfg.Providers) == 0 {
		det := detectors.NewDetector()
		provs, err := det.DetectAll()
		if err != nil {
			return nil, err
		}
		cfg.Providers = provs
	}
	return cfg, nil
}

func parseArgs(args []string) ([]string, string, bool) {
	var out []string
	var cfgPath string
	showVersion := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case "--version", "-v":
			showVersion = true
		default:
			out = append(out, args[i])
		}
	}
	return out, cfgPath, showVersion
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
