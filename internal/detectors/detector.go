package detectors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ghrouter/internal/types"
)

type Detector struct{ discovered map[string]*types.Provider }

func NewDetector() *Detector { return &Detector{discovered: make(map[string]*types.Provider)} }

type CLISpec struct {
	Name             string
	ProviderType     types.ProviderType
	Args             []string
	DiscoveryArgs    []string
	DiscoveryEnabled bool
	ACPProbeEnabled  bool
}

func ResolveCLIPath(provider types.ProviderType) string {
	name := map[types.ProviderType]string{
		types.ProviderClaudeCode: "claude",
		types.ProviderCodex:      "codex",
		types.ProviderOpenCode:   "opencode",
		types.ProviderMimo:       "mimo",
		types.ProviderPi:         "pi",
		types.ProviderCursor:     "cursor",
	}[provider]
	if name == "" {
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}

func (d *Detector) DetectAll() ([]*types.Provider, error) {
	specs := []CLISpec{
		{Name: "claude", ProviderType: types.ProviderClaudeCode, Args: []string{"--print", "--output-format", "stream-json", "--no-session-persistence"}},
		{Name: "codex", ProviderType: types.ProviderCodex, Args: []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check"}},
		{Name: "opencode", ProviderType: types.ProviderOpenCode, Args: []string{"run", "--format", "json", "--no-remote"}, DiscoveryArgs: []string{"models", "--verbose", "--pure"}, DiscoveryEnabled: true, ACPProbeEnabled: true},
		{Name: "mimo", ProviderType: types.ProviderMimo, Args: []string{"run", "--format", "json", "--pure"}, DiscoveryArgs: []string{"models"}, DiscoveryEnabled: true, ACPProbeEnabled: true},
		{Name: "pi", ProviderType: types.ProviderPi, Args: []string{"--mode", "json", "--print", "--no-session", "--no-context-files"}, DiscoveryArgs: []string{"--list-models"}, DiscoveryEnabled: true},
		{Name: "cursor", ProviderType: types.ProviderCursor, Args: []string{"agent", "-p", "--output-format", "stream-json", "--stream-partial-output"}, DiscoveryArgs: []string{"agent", "--list-models"}, DiscoveryEnabled: true},
	}
	providers := make([]*types.Provider, 0, len(specs))
	for _, spec := range specs {
		path := ResolveCLIPath(spec.ProviderType)
		if path == "" {
			continue
		}
		provider := d.buildProvider(spec, path)
		provider.Protocol, provider.Origin, provider.CapabilityStatus, provider.FailureReason = classifyProviderCapability(path, spec.ProviderType, spec.ACPProbeEnabled)
		if spec.DiscoveryEnabled {
			enrichProviderDiscovery(provider, discoverModelsWithTimeout(path, spec.ProviderType, 2*time.Second))
		} else {
			provider.Discovery = types.DiscoveryState{
				Status:       types.DiscoveryUnsupported,
				Error:        "native model discovery is unavailable for this CLI",
				DiscoveredAt: time.Now().UTC(),
			}
		}
		providers = append(providers, provider)
		d.discovered[provider.Name] = provider
	}
	return providers, nil
}

func (d *Detector) buildProvider(spec CLISpec, path string) *types.Provider {
	env := make(map[string]string, len(specsAuthAllowlist(spec.ProviderType))+1)
	for _, key := range specsAuthAllowlist(spec.ProviderType) {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		env["PATH"] = pathEnv
	}
	workDir, _ := os.Getwd()
	return &types.Provider{
		Name:       string(spec.ProviderType),
		Type:       spec.ProviderType,
		CLIPath:    path,
		Args:       append([]string(nil), spec.Args...),
		Env:        env,
		Timeout:    5 * time.Minute,
		MaxTokens:  128000,
		WorkDir:    workDir,
		AuthMethod: types.AuthEnv,
		Enabled:    true,
	}
}

func EnrichProviderModels(provider *types.Provider) {
	if provider == nil {
		return
	}
	merged := make(map[string]types.ModelInfo, len(provider.ModelInfo)+len(provider.Models))
	canonicalModels := make([]string, 0, len(provider.Models)+len(provider.ModelInfo))
	seen := make(map[string]struct{}, len(provider.Models)+len(provider.ModelInfo))
	add := func(model string) {
		model = canonicalModelReference(provider, model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		canonicalModels = append(canonicalModels, model)
	}
	for _, model := range provider.Models {
		add(model)
	}
	for key, info := range provider.ModelInfo {
		model := canonicalModelReference(provider, key)
		if model == "" {
			continue
		}
		add(model)
		info.Provider = provider.Name
		info.Model = model
		if info.Source == "" {
			info.Source = "configured"
		}
		merged[model] = info
	}
	sort.Strings(canonicalModels)
	provider.Models = canonicalModels
	provider.ModelInfo = merged
}

func specsAuthAllowlist(providerType types.ProviderType) []string {
	switch providerType {
	case types.ProviderClaudeCode:
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	case types.ProviderCodex:
		return []string{"OPENAI_API_KEY", "OPENAI_API_BASE", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "CODEX_HOME"}
	case types.ProviderOpenCode:
		return []string{"OPENAI_API_KEY", "OPENAI_API_BASE", "OPENCODE_API_KEY", "OPENCODE_HOME"}
	case types.ProviderMimo:
		return []string{"OPENAI_API_KEY", "MIMO_API_KEY", "MIMO_HOME"}
	case types.ProviderPi:
		return []string{"PI_HOME", "OPENAI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY"}
	case types.ProviderCursor:
		return []string{"CURSOR_API_KEY", "CURSOR_API_ENDPOINT"}
	default:
		return nil
	}
}

func classifyProviderCapability(path string, providerType types.ProviderType, acpProbeEnabled bool) (protocol string, origin string, capabilityStatus string, failureReason string) {
	ok, status, reason := probeHelpForACP(path)
	if status == "timeout" || status == "auth" || status == "unknown" {
		switch providerType {
		case types.ProviderPi:
			return "native_rpc", "native_rpc", status, reason
		default:
			return "native_cli", "native_cli", status, reason
		}
	}
	switch providerType {
	case types.ProviderOpenCode, types.ProviderMimo:
		if ok && acpProbeEnabled {
			if probeACPInitialize(path) {
				return "acp", "native_cli", "supported", ""
			}
		}
		if ok {
			return "native_cli", "native_cli", "unsupported", "ACP initialize handshake not confirmed"
		}
		return "native_cli", "native_cli", "unsupported", "help output does not advertise acp"
	case types.ProviderPi:
		return "native_rpc", "native_rpc", "unsupported", "native rpc contract"
	default:
		return "native_cli", "native_cli", "unsupported", "native cli contract"
	}
}

func probeACPInitialize(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "acp")
	prepareDiscoveryCommand(cmd)
	stdout, stderr, err := runACPInitialize(ctx, cmd)
	if ctx.Err() != nil || err != nil {
		return false
	}
	return hasACPInitializeSuccess(stdout, stderr)
}

func runACPInitialize(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	payload := []byte(`{"method":"initialize","protocolVersion":1,"capabilities":{"catalog":true},"authMethods":["env"]}` + "\n")
	go func() {
		defer stdin.Close()
		_, _ = stdin.Write(payload)
	}()
	stdoutBytes, _ := io.ReadAll(stdout)
	stderrBytes, _ := io.ReadAll(stderr)
	if err := cmd.Wait(); err != nil {
		return stdoutBytes, stderrBytes, err
	}
	return stdoutBytes, stderrBytes, nil
}

func hasACPInitializeSuccess(stdout, stderr []byte) bool {
	if len(stdout) == 0 && len(stderr) == 0 {
		return false
	}
	joined := append(append([]byte(nil), stdout...), stderr...)
	if bytes.Contains(bytes.ToLower(joined), []byte("\"error\"")) || bytes.Contains(bytes.ToLower(joined), []byte("method not found")) || bytes.Contains(bytes.ToLower(joined), []byte("invalid params")) {
		return false
	}
	type initializeResponse struct {
		ProtocolVersion any            `json:"protocolVersion"`
		Result          map[string]any `json:"result"`
	}
	var resp initializeResponse
	if err := json.NewDecoder(bytes.NewReader(stdout)).Decode(&resp); err == nil {
		if resp.ProtocolVersion != nil {
			return true
		}
		if len(resp.Result) > 0 {
			return true
		}
	}
	return false
}

func probeHelpForACP(path string) (bool, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--help")
	prepareDiscoveryCommand(cmd)
	stdout, stderr, err := runBoundedCommand(ctx, cmd)
	if ctx.Err() != nil {
		return false, "timeout", "help probe timed out"
	}
	if err != nil {
		return false, classifyProbeFailure(stdout, stderr, err), "help probe failed"
	}
	joined := bytes.ToLower(append(append([]byte(nil), stdout...), stderr...))
	return bytes.Contains(joined, []byte("acp")), "supported", ""
}

func classifyProbeFailure(stdout, stderr []byte, err error) string {
	_ = stdout
	if bytes.Contains(bytes.ToLower(stderr), []byte("auth")) ||
		bytes.Contains(bytes.ToLower(stderr), []byte("token")) ||
		bytes.Contains(bytes.ToLower(stderr), []byte("unauthor")) ||
		bytes.Contains(bytes.ToLower(stderr), []byte("permission")) {
		return "auth"
	}
	if err != nil {
		return "unknown"
	}
	return "unsupported"
}

func enrichProviderDiscovery(provider *types.Provider, result discoveryResult) {
	if provider == nil {
		return
	}
	if result.status != "" {
		provider.Discovery = types.DiscoveryState{
			Status:       types.DiscoveryStatus(result.status),
			Error:        result.err,
			DiscoveredAt: time.Now().UTC(),
		}
	}
	if result.status != string(types.DiscoverySuccess) {
		return
	}
	merged := make(map[string]types.ModelInfo, len(provider.ModelInfo)+len(result.info))
	for key, value := range provider.ModelInfo {
		merged[key] = value
	}
	known := make(map[string]struct{}, len(provider.Models))
	models := make([]string, 0, len(provider.Models)+len(result.models))
	for _, model := range provider.Models {
		model = canonicalModelReference(provider, model)
		if model == "" {
			continue
		}
		if _, ok := known[model]; ok {
			continue
		}
		known[model] = struct{}{}
		models = append(models, model)
	}
	for _, model := range result.models {
		model = canonicalModelReference(provider, model)
		if model == "" {
			continue
		}
		if _, ok := known[model]; ok {
			continue
		}
		known[model] = struct{}{}
		models = append(models, model)
	}
	for model, info := range result.info {
		model = canonicalModelReference(provider, model)
		if model == "" {
			continue
		}
		info.Provider = provider.Name
		info.Model = canonicalizeModelPath(model)
		if info.Source == "" {
			info.Source = "native"
		}
		merged[model] = info
	}
	provider.Models = models
	provider.ModelInfo = merged
}

func BuildAutomaticModelLists(providers []*types.Provider, existing []types.ModelList) []types.ModelList {
	lists := append([]types.ModelList(nil), existing...)
	providerLists := make(map[string][]string)
	capabilityLists := map[string][]string{
		"ghrouter/context-1m": []string{},
		"ghrouter/reasoning":  []string{},
		"ghrouter/vision":     []string{},
		"ghrouter/tool-use":   []string{},
	}
	all := make([]string, 0)
	for _, provider := range providers {
		if provider == nil || !provider.Enabled {
			continue
		}
		members := make([]string, 0, len(provider.Models))
		for _, model := range provider.Models {
			model = canonicalModelReference(provider, model)
			if model == "" {
				continue
			}
			info := provider.ModelInfo[model]
			if !eligibleForAutomaticList(info, model) {
				continue
			}
			members = append(members, model)
			all = append(all, model)
			if info.ContextWindow >= 1_000_000 {
				capabilityLists["ghrouter/context-1m"] = append(capabilityLists["ghrouter/context-1m"], model)
			}
			if info.Thinking {
				capabilityLists["ghrouter/reasoning"] = append(capabilityLists["ghrouter/reasoning"], model)
			}
			if info.Vision {
				capabilityLists["ghrouter/vision"] = append(capabilityLists["ghrouter/vision"], model)
			}
			if info.ToolUse {
				capabilityLists["ghrouter/tool-use"] = append(capabilityLists["ghrouter/tool-use"], model)
			}
		}
		members = compactModelReferences(members)
		if len(members) > 0 {
			providerLists["ghrouter/"+provider.Name] = members
		}
	}
	for i := range lists {
		if members, ok := providerLists[lists[i].Name]; ok {
			lists[i].Models = append([]string(nil), members...)
		}
		if lists[i].Name == "ghrouter/auto" {
			lists[i].Models = append([]string(nil), all...)
		}
	}
	seen := make(map[string]bool, len(lists))
	for _, list := range lists {
		seen[list.Name] = true
	}
	for name, members := range providerLists {
		if !seen[name] && len(members) > 0 {
			lists = append(lists, types.ModelList{Name: name, Kind: "provider", Strategy: "round-robin", Models: members})
		}
	}
	for name, members := range capabilityLists {
		members = compactModelReferences(members)
		if len(members) == 0 {
			continue
		}
		if !seen[name] {
			lists = append(lists, types.ModelList{Name: name, Kind: "automatic", Strategy: "score", Models: members})
			continue
		}
	}
	if !seen["ghrouter/auto"] && len(all) > 0 {
		lists = append(lists, types.ModelList{Name: "ghrouter/auto", Kind: "automatic", Strategy: "score", Models: all})
	}
	return lists
}

func eligibleForAutomaticList(info types.ModelInfo, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(info.HealthStatus)) {
	case "failed", "unhealthy":
		return false
	case "cooldown":
		if info.CooldownUntil.IsZero() || time.Now().Before(info.CooldownUntil) {
			return false
		}
	}
	return true
}

func compactModelReferences(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	compact := models[:0]
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		compact = append(compact, model)
	}
	return compact
}

func canonicalModelReference(provider *types.Provider, model string) string {
	model = canonicalizeModelPath(model)
	if provider == nil || model == "" {
		return model
	}
	if prefix := canonicalPrefixFor(provider.Type); prefix != "" {
		if strings.HasPrefix(model, prefix+"/") {
			return model
		}
		if strings.HasPrefix(model, provider.Name+"/") {
			model = strings.TrimPrefix(model, provider.Name+"/")
		}
		model = trimDuplicatePrefix(model, prefix)
		return prefix + "/" + model
	}
	return model
}

func canonicalPrefixFor(providerType types.ProviderType) string {
	switch providerType {
	case types.ProviderClaudeCode:
		return "cc"
	case types.ProviderCodex:
		return "cx"
	case types.ProviderOpenCode:
		return "oc"
	case types.ProviderMimo:
		return "mi"
	case types.ProviderPi:
		return "pi"
	case types.ProviderCursor:
		return "cu"
	default:
		return ""
	}
}

func canonicalizeModelPath(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "/")
	model = strings.TrimSuffix(model, "/")
	if model == "" {
		return ""
	}
	for _, prefix := range []string{"cc/", "cx/", "oc/", "mi/", "pi/", "cu/"} {
		model = trimDuplicatePrefix(model, prefix[:len(prefix)-1])
	}
	return model
}

func trimDuplicatePrefix(model, prefix string) string {
	if prefix == "" {
		return strings.TrimSpace(model)
	}
	for {
		trimmed := strings.TrimPrefix(model, prefix+"/")
		if trimmed == model {
			return model
		}
		model = trimmed
	}
}

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+@~-]*$`)

func discoverModelsWithTimeout(path string, provider types.ProviderType, timeout time.Duration) discoveryResult {
	args, supported := discoveryArgsForProvider(provider)
	if !supported {
		return discoveryResult{status: string(types.DiscoveryUnsupported), err: "native model discovery is unavailable for this CLI"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	prepareDiscoveryCommand(cmd)
	cmd.Stdin = nil
	cmd.Env = discoveryEnvForProvider(provider)
	stdout, stderr, err := runBoundedCommand(ctx, cmd)
	if ctx.Err() != nil {
		return discoveryResult{status: string(types.DiscoveryTimeout), err: "model discovery timed out"}
	}
	if err != nil {
		switch classifyDiscoveryFailure(stdout, stderr, err) {
		case "auth":
			return discoveryResult{status: string(types.DiscoveryAuth), err: "model discovery failed authentication"}
		case "timeout":
			return discoveryResult{status: string(types.DiscoveryTimeout), err: "model discovery timed out"}
		case "unsupported":
			return discoveryResult{status: string(types.DiscoveryUnsupported), err: "native model discovery is unavailable"}
		default:
			return discoveryResult{status: string(types.DiscoveryError), err: "model discovery command failed"}
		}
	}
	models, info := parseModelListing(string(stdout), provider)
	if len(models) == 0 {
		if len(bytes.TrimSpace(stderr)) > 0 {
			return discoveryResult{status: string(types.DiscoveryError), err: "model discovery returned no models", stderr: string(stderr)}
		}
		return discoveryResult{status: string(types.DiscoveryEmpty), info: info}
	}
	return discoveryResult{models: models, info: info, status: string(types.DiscoverySuccess)}
}

func discoveryArgsForProvider(provider types.ProviderType) ([]string, bool) {
	switch provider {
	case types.ProviderOpenCode:
		return []string{"models", "--verbose", "--pure"}, true
	case types.ProviderMimo:
		return []string{"models"}, true
	case types.ProviderPi:
		return []string{"--list-models"}, true
	case types.ProviderCursor:
		return []string{"agent", "--list-models"}, true
	default:
		return nil, false
	}
}

func classifyDiscoveryFailure(stdout, stderr []byte, err error) string {
	_ = stdout
	if bytes.Contains(bytes.ToLower(stderr), []byte("auth")) ||
		bytes.Contains(bytes.ToLower(stderr), []byte("token")) ||
		bytes.Contains(bytes.ToLower(stderr), []byte("unauthor")) ||
		bytes.Contains(bytes.ToLower(stderr), []byte("permission")) {
		return "auth"
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return "timeout"
	}
	if len(bytes.TrimSpace(stderr)) == 0 {
		return "unsupported"
	}
	return "unknown"
}

type discoveryResult struct {
	models []string
	info   map[string]types.ModelInfo
	status string
	err    string
	stderr string
}

func discoveryEnvForProvider(providerType types.ProviderType) []string {
	allow := make(map[string]struct{}, 16)
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		allow[key] = struct{}{}
	}
	for _, key := range specsAuthAllowlist(providerType) {
		allow[key] = struct{}{}
	}
	env := make([]string, 0, len(allow))
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, ok := allow[key]; ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func runBoundedCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	stdoutFile, err := os.CreateTemp("", "ghrouter-discovery-stdout-")
	if err != nil {
		return nil, nil, err
	}
	stdoutPath := stdoutFile.Name()
	defer os.Remove(stdoutPath)
	defer stdoutFile.Close()
	stderrFile, err := os.CreateTemp("", "ghrouter-discovery-stderr-")
	if err != nil {
		return nil, nil, err
	}
	stderrPath := stderrFile.Name()
	defer os.Remove(stderrPath)
	defer stderrFile.Close()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-waitCh:
	case <-ctx.Done():
		killDiscoveryProcess(cmd)
		runErr = <-waitCh
		if runErr == nil {
			runErr = ctx.Err()
		}
	}
	if runErr != nil {
		stdout, _ := os.ReadFile(stdoutPath)
		stderr, _ := os.ReadFile(stderrPath)
		return stdout, stderr, runErr
	}
	if _, err := stdoutFile.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	if _, err := stderrFile.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	stdout, err := io.ReadAll(stdoutFile)
	if err != nil {
		return nil, nil, err
	}
	stderr, err := io.ReadAll(stderrFile)
	if err != nil {
		return nil, nil, err
	}
	return stdout, stderr, nil
}

func parseModelListing(raw string, provider types.ProviderType) ([]string, map[string]types.ModelInfo) {
	seen := make(map[string]bool)
	models := make([]string, 0)
	info := make(map[string]types.ModelInfo)
	add := func(rawID string) string {
		rawID = strings.TrimSpace(stripANSI(rawID))
		if rawID == "" || !modelIDPattern.MatchString(rawID) {
			return ""
		}
		id := canonicalModelID(provider, rawID)
		if id == "" || seen[id] {
			return id
		}
		seen[id] = true
		models = append(models, id)
		return id
	}
	if provider == types.ProviderOpenCode {
		parseOpenCodeModelListing(raw, add, info)
		return models, info
	}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(stripANSI(scanner.Text()))
		if line == "" {
			continue
		}
		switch provider {
		case types.ProviderPi:
			parsePiModelLine(line, add, info)
		case types.ProviderCursor:
			if strings.EqualFold(line, "available models") || strings.HasPrefix(strings.ToLower(line), "tip:") {
				continue
			}
			if strings.HasPrefix(line, "-") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if strings.HasSuffix(fields[0], ":") {
				continue
			}
			id := add(fields[0])
			if id != "" {
				annotateCursorModelInfo(info, id, line)
			}
		default:
			if strings.Contains(line, " ") {
				continue
			}
			add(line)
		}
	}
	return models, info
}

func canonicalModelID(provider types.ProviderType, raw string) string {
	raw = canonicalizeModelPath(raw)
	if raw == "" {
		return ""
	}
	prefix := canonicalPrefixFor(provider)
	if prefix == "" {
		return raw
	}
	if strings.HasPrefix(raw, prefix+"/") {
		return raw
	}
	if strings.HasPrefix(raw, providerTypeName(provider)+"/") {
		raw = strings.TrimPrefix(raw, providerTypeName(provider)+"/")
	}
	return prefix + "/" + trimDuplicatePrefix(raw, prefix)
}

func providerTypeName(provider types.ProviderType) string {
	switch provider {
	case types.ProviderClaudeCode:
		return "claude-code"
	case types.ProviderCodex:
		return "codex"
	case types.ProviderOpenCode:
		return "opencode"
	case types.ProviderMimo:
		return "mimo"
	case types.ProviderPi:
		return "pi"
	case types.ProviderCursor:
		return "cursor"
	default:
		return string(provider)
	}
}

func parsePiModelLine(line string, add func(string) string, info map[string]types.ModelInfo) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	if strings.EqualFold(fields[0], "provider") || strings.EqualFold(fields[0], "model") {
		return
	}
	rawID := fields[0]
	if len(fields) > 1 {
		rawID = fields[0] + "/" + fields[1]
	}
	id := add(rawID)
	if id == "" {
		return
	}
	if len(fields) >= 5 {
		annotatePiModelInfo(info, id, fields)
	}
}

func annotatePiModelInfo(info map[string]types.ModelInfo, id string, fields []string) {
	if info == nil || id == "" || len(fields) < 5 {
		return
	}
	entry := info[id]
	entry.Model = canonicalizeModelPath(id)
	entry.Source = "native"
	entry.ContextWindow = parseSizeToken(fields[2])
	entry.MaxOutput = parseSizeToken(fields[3])
	entry.Thinking = parseYesNoToken(fields[4])
	if len(fields) > 5 {
		entry.Vision = parseYesNoToken(fields[5])
	}
	info[id] = entry
}

func annotateCursorModelInfo(info map[string]types.ModelInfo, id, line string) {
	if info == nil || id == "" {
		return
	}
	entry := info[id]
	entry.Model = canonicalizeModelPath(id)
	entry.Source = "native"
	lower := strings.ToLower(line)
	if strings.Contains(lower, "high") {
		entry.Effort = appendUniqueString(entry.Effort, "high")
	}
	if strings.Contains(lower, "medium") {
		entry.Effort = appendUniqueString(entry.Effort, "medium")
	}
	if strings.Contains(lower, "max") {
		entry.Effort = appendUniqueString(entry.Effort, "max")
	}
	info[id] = entry
}

func parseOpenCodeModelListing(raw string, add func(string) string, info map[string]types.ModelInfo) {
	lines := strings.Split(raw, "\n")
	var currentID string
	var metadataLines []string
	collectingMetadata := false
	flushMetadata := func() {
		if currentID == "" || len(metadataLines) == 0 {
			metadataLines = metadataLines[:0]
			collectingMetadata = false
			return
		}
		entry := info[currentID]
		entry.Model = canonicalizeModelPath(currentID)
		entry.Source = "native"
		if meta := parseOpenCodeMetadata(strings.Join(metadataLines, "\n")); meta != nil {
			entry.ContextWindow = meta.ContextWindow
			entry.MaxOutput = meta.MaxOutput
			entry.Thinking = meta.Thinking
			entry.Vision = meta.Vision
			entry.ToolUse = meta.ToolUse
			entry.Effort = append([]string(nil), meta.Effort...)
			if entry.Effort == nil {
				entry.Effort = nil
			}
		}
		info[currentID] = entry
		metadataLines = metadataLines[:0]
		collectingMetadata = false
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripANSI(line))
		if trimmed == "" {
			flushMetadata()
			continue
		}
		if collectingMetadata {
			metadataLines = append(metadataLines, trimmed)
			continue
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if currentID != "" {
				metadataLines = append(metadataLines, trimmed)
				collectingMetadata = true
			}
			continue
		}
		if strings.Contains(trimmed, "/") {
			flushMetadata()
			currentID = add(strings.Fields(trimmed)[0])
			continue
		}
		flushMetadata()
		currentID = add(strings.Fields(trimmed)[0])
	}
	flushMetadata()
}

type openCodeMetadata struct {
	ContextWindow int
	MaxOutput     int
	Thinking      bool
	Vision        bool
	ToolUse       bool
	Effort        []string
}

func parseOpenCodeMetadata(raw string) *openCodeMetadata {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var payload struct {
		Limit struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		} `json:"limit"`
		Capabilities struct {
			Reasoning bool `json:"reasoning"`
			ToolCall  bool `json:"toolcall"`
			Input     struct {
				Image bool `json:"image"`
			} `json:"input"`
		} `json:"capabilities"`
		Variants map[string]struct {
			ReasoningEffort string `json:"reasoningEffort"`
		} `json:"variants"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	meta := &openCodeMetadata{
		ContextWindow: payload.Limit.Context,
		MaxOutput:     payload.Limit.Output,
		Thinking:      payload.Capabilities.Reasoning,
		Vision:        payload.Capabilities.Input.Image,
		ToolUse:       payload.Capabilities.ToolCall,
	}
	for _, variant := range payload.Variants {
		if variant.ReasoningEffort != "" {
			meta.Effort = appendUniqueString(meta.Effort, variant.ReasoningEffort)
		}
	}
	sort.Strings(meta.Effort)
	return meta
}

func parseSizeToken(raw string) int {
	raw = strings.TrimSpace(strings.ToUpper(raw))
	switch {
	case raw == "":
		return 0
	case strings.HasSuffix(raw, "K"):
		v, err := strconv.Atoi(strings.TrimSuffix(raw, "K"))
		if err != nil {
			return 0
		}
		return v * 1000
	case strings.HasSuffix(raw, "M"):
		v, err := strconv.Atoi(strings.TrimSuffix(raw, "M"))
		if err != nil {
			return 0
		}
		return v * 1_000_000
	default:
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0
		}
		return v
	}
}

func parseYesNoToken(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yes", "true", "1", "on":
		return true
	default:
		return false
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

func (d *Detector) GetDiscovered() map[string]*types.Provider { return d.discovered }
func (d *Detector) String() string                            { return fmt.Sprintf("%d providers discovered", len(d.discovered)) }
