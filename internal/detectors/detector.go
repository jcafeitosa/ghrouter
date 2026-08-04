package detectors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/types"
)

type Detector struct{ discovered map[string]*types.Provider }

const nativeDiscoveryTimeout = 5 * time.Second
const acpProbeTimeout = 3 * time.Second
const discoveryCacheTTL = 15 * time.Second

var discoveryCache struct {
	sync.Mutex
	key       string
	expiresAt time.Time
	providers []*types.Provider
}

func NewDetector() *Detector { return &Detector{discovered: make(map[string]*types.Provider)} }

func (d *Detector) DetectAll() ([]*types.Provider, error) {
	return d.detectAll(false)
}

func (d *Detector) DetectAllFresh() ([]*types.Provider, error) {
	return d.detectAll(true)
}

type CLISpec struct {
	Name             string
	ProviderType     types.ProviderType
	Args             []string
	DiscoveryArgs    []string
	ACPArgs          []string
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

func (d *Detector) detectAll(force bool) ([]*types.Provider, error) {
	cacheKey := discoveryCacheKey()
	if !force {
		discoveryCache.Lock()
		if discoveryCache.key == cacheKey && time.Now().Before(discoveryCache.expiresAt) {
			providers := cloneProviders(discoveryCache.providers)
			discoveryCache.Unlock()
			d.remember(providers)
			return providers, nil
		}
		discoveryCache.Unlock()
	}
	specs := []CLISpec{
		{Name: "claude", ProviderType: types.ProviderClaudeCode, Args: []string{"--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence"}},
		{Name: "codex", ProviderType: types.ProviderCodex, Args: []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check"}, DiscoveryEnabled: true},
		{Name: "opencode", ProviderType: types.ProviderOpenCode, Args: []string{"run", "--format", "json", "--pure"}, DiscoveryArgs: []string{"models", "--verbose", "--pure"}, DiscoveryEnabled: true, ACPProbeEnabled: true},
		{Name: "mimo", ProviderType: types.ProviderMimo, Args: []string{"run", "--format", "json", "--pure"}, DiscoveryArgs: []string{"models"}, DiscoveryEnabled: true, ACPProbeEnabled: true},
		{Name: "pi", ProviderType: types.ProviderPi, Args: []string{"--mode", "json", "--print", "--no-session", "--no-context-files"}, DiscoveryArgs: []string{"--list-models"}, DiscoveryEnabled: true},
		{Name: "cursor", ProviderType: types.ProviderCursor, Args: []string{"agent", "-p", "--output-format", "stream-json", "--stream-partial-output"}, ACPArgs: []string{"agent", "--trust", "acp"}, DiscoveryEnabled: true, ACPProbeEnabled: true},
	}
	detected := make([]*types.Provider, len(specs))
	var wg sync.WaitGroup
	for index := range specs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			spec := specs[index]
			path := ResolveCLIPath(spec.ProviderType)
			if path == "" {
				return
			}
			provider := d.buildProvider(spec, path)
			provider.Harness = probeHarnessCapabilities(path, spec.ProviderType)
			provider.Protocol, provider.Origin, provider.CapabilityStatus, provider.FailureReason = classifyProviderCapability(path, spec.ProviderType, spec.ACPProbeEnabled, spec.ACPArgs)
			provider.Harness.ACPHandshakeConfirmed = provider.Protocol == "acp"
			if spec.DiscoveryEnabled && !(spec.ProviderType == types.ProviderCursor && provider.Protocol != "acp") {
				enrichProviderDiscovery(provider, discoverModelsWithTimeout(path, spec.ProviderType, nativeDiscoveryTimeout))
			} else {
				reason := "native model discovery is unavailable for this CLI"
				if spec.ProviderType == types.ProviderCursor && provider.Protocol != "acp" {
					reason = "Cursor ACP handshake was not confirmed"
				}
				provider.Discovery = types.DiscoveryState{
					Status:       types.DiscoveryUnsupported,
					Error:        reason,
					DiscoveredAt: time.Now().UTC(),
				}
			}
			detected[index] = provider
		}(index)
	}
	wg.Wait()
	providers := make([]*types.Provider, 0, len(specs))
	for _, provider := range detected {
		if provider == nil {
			continue
		}
		providers = append(providers, provider)
		d.discovered[provider.Name] = provider
	}
	if nvidia := detectConfiguredNVIDIA(); nvidia != nil {
		providers = append(providers, nvidia)
		d.discovered[nvidia.Name] = nvidia
	}
	discoveryCache.Lock()
	discoveryCache.key = cacheKey
	discoveryCache.expiresAt = time.Now().Add(discoveryCacheTTL)
	discoveryCache.providers = cloneProviders(providers)
	discoveryCache.Unlock()
	return cloneProviders(providers), nil
}

func (d *Detector) remember(providers []*types.Provider) {
	for _, provider := range providers {
		if provider != nil {
			d.discovered[provider.Name] = provider
		}
	}
}

func discoveryCacheKey() string {
	parts := []string{os.Getenv("PATH"), currentWorkDir(), os.Getenv("GHR_NVIDIA_MODELS"), os.Getenv("GHR_NVIDIA_DISCOVER_ALL")}
	for _, providerType := range []types.ProviderType{
		types.ProviderClaudeCode,
		types.ProviderCodex,
		types.ProviderOpenCode,
		types.ProviderMimo,
		types.ProviderPi,
		types.ProviderCursor,
	} {
		parts = append(parts, string(providerType)+"="+ResolveCLIPath(providerType))
		for _, key := range specsAuthAllowlist(providerType) {
			if os.Getenv(key) != "" {
				parts = append(parts, key+"=set")
			} else {
				parts = append(parts, key+"=unset")
			}
		}
	}
	return strings.Join(parts, "\x00")
}

func cloneProviders(providers []*types.Provider) []*types.Provider {
	cloned := make([]*types.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		copy := *provider
		copy.Args = append([]string(nil), provider.Args...)
		copy.Models = append([]string(nil), provider.Models...)
		copy.Env = cloneStringMap(provider.Env)
		copy.AuthConfig = cloneStringMap(provider.AuthConfig)
		copy.ModelInfo = make(map[string]types.ModelInfo, len(provider.ModelInfo))
		for model, info := range provider.ModelInfo {
			info.Effort = append([]string(nil), info.Effort...)
			info.Modalities = append([]string(nil), info.Modalities...)
			copy.ModelInfo[model] = info
		}
		copy.Accounts = append([]types.ProviderCredential(nil), provider.Accounts...)
		copy.Harness.Commands = append([]string(nil), provider.Harness.Commands...)
		copy.Harness.Flags = append([]string(nil), provider.Harness.Flags...)
		copy.Harness.Formats = append([]string(nil), provider.Harness.Formats...)
		copy.Harness.SlashCommands = append([]string(nil), provider.Harness.SlashCommands...)
		cloned = append(cloned, &copy)
	}
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func detectConfiguredNVIDIA() *types.Provider {
	if strings.TrimSpace(os.Getenv("NVIDIA_API_KEY")) == "" {
		return nil
	}
	models := make([]string, 0)
	info := make(map[string]types.ModelInfo)
	addModel := func(raw, source string) {
		model := canonicalModelReference(&types.Provider{Type: types.ProviderNVIDIA}, raw)
		if model == "" || !modelIDPattern.MatchString(model) {
			return
		}
		for _, existing := range models {
			if existing == model {
				return
			}
		}
		models = append(models, model)
		modelInfo := types.ModelInfo{Source: source}
		if source == "nvidia_api" {
			modelInfo = classifyNVIDIAModel(model)
		}
		info[model] = modelInfo
	}
	for _, raw := range strings.Split(strings.TrimSpace(os.Getenv("GHR_NVIDIA_MODELS")), ",") {
		addModel(raw, "env")
	}
	if os.Getenv("GHR_NVIDIA_DISCOVER_ALL") == "1" {
		for _, model := range discoverNVIDIAModels() {
			addModel(model, "nvidia_api")
		}
	}
	if len(models) == 0 {
		return nil
	}
	return &types.Provider{
		Name:       "nvidia",
		Type:       types.ProviderNVIDIA,
		BaseURL:    "https://integrate.api.nvidia.com",
		AuthMethod: types.AuthEnv,
		Models:     models,
		ModelInfo:  info,
		Enabled:    true,
		Discovery:  types.DiscoveryState{Status: types.DiscoverySuccess, DiscoveredAt: time.Now().UTC()},
	}
}

func discoverNVIDIAModels() []string {
	request, err := http.NewRequest(http.MethodGet, "https://integrate.api.nvidia.com/v1/models", nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(os.Getenv("NVIDIA_API_KEY")))
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return nil
	}
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ID) != "" {
			models = append(models, item.ID)
		}
	}
	return models
}

func classifyNVIDIAModel(model string) types.ModelInfo {
	lower := strings.ToLower(model)
	info := types.ModelInfo{Source: "nvidia_api", Model: model}
	addModality := func(value string) {
		for _, existing := range info.Modalities {
			if existing == value {
				return
			}
		}
		info.Modalities = append(info.Modalities, value)
	}
	switch {
	case strings.Contains(lower, "embed"), strings.Contains(lower, "rerank"), strings.Contains(lower, "retriev"):
		info.Kind = "embedding_retrieval"
		addModality("text")
	case strings.Contains(lower, "image"), strings.Contains(lower, "flux"), strings.Contains(lower, "diffusion"):
		info.Kind = "image"
		addModality("image")
	case strings.Contains(lower, "audio"), strings.Contains(lower, "whisper"), strings.Contains(lower, "riva"), strings.Contains(lower, "voice"):
		info.Kind = "audio"
		addModality("audio")
	case strings.Contains(lower, "vision"), strings.Contains(lower, "-vl"), strings.Contains(lower, "paligemma"), strings.Contains(lower, "fuyu"), strings.Contains(lower, "kosmos"), strings.Contains(lower, "neva"):
		info.Kind = "multimodal"
		info.Vision = true
		addModality("text")
		addModality("image")
	case strings.Contains(lower, "safety"), strings.Contains(lower, "guard"), strings.Contains(lower, "content-safety"):
		info.Kind = "safety"
		addModality("text")
	case strings.Contains(lower, "code"), strings.Contains(lower, "coder"), strings.Contains(lower, "starcoder"), strings.Contains(lower, "codestral"):
		info.Kind = "coding"
		info.ToolUse = true
		addModality("text")
	default:
		info.Kind = "chat"
		addModality("text")
	}
	return info
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
		return []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_API_BASE", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "CODEX_HOME"}
	case types.ProviderOpenCode:
		return []string{"OPENAI_API_KEY", "OPENAI_API_BASE", "OPENCODE_API_KEY", "OPENCODE_HOME"}
	case types.ProviderMimo:
		return []string{"OPENAI_API_KEY", "MIMO_API_KEY", "MIMO_HOME"}
	case types.ProviderPi:
		return []string{"PI_HOME", "PI_CODING_AGENT_DIR", "OPENAI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY"}
	case types.ProviderCursor:
		return []string{"CURSOR_API_KEY", "CURSOR_API_ENDPOINT"}
	default:
		return nil
	}
}

func classifyProviderCapability(path string, providerType types.ProviderType, acpProbeEnabled bool, acpArgs []string) (protocol string, origin string, capabilityStatus string, failureReason string) {
	if len(acpArgs) == 0 {
		acpArgs = []string{"acp"}
	}
	if providerType == types.ProviderCursor {
		if acpProbeEnabled && probeACPInitializeWithArgs(path, acpArgs, true) {
			return "acp", "native_cli", "supported", ""
		}
		return "native_cli", "native_cli", "unsupported", "ACP initialize handshake not confirmed"
	}
	ok, status, reason := probeHelpForACP(path)
	if providerType == types.ProviderCodex {
		if status == "timeout" || status == "auth" || status == "unknown" {
			return "native_app_server", "native_app_server", status, reason
		}
		return "native_app_server", "native_app_server", "supported", ""
	}
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
			if probeACPInitializeWithArgs(path, acpArgs, false) {
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

func probeACPInitializeWithArgs(path string, args []string, cursorCapabilities bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), acpProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	prepareDiscoveryCommand(cmd)
	stdout, stderr, err := runACPInitializeWithCapabilities(ctx, cmd, cursorCapabilities)
	if ctx.Err() != nil || err != nil {
		return false
	}
	return hasACPInitializeSuccess(stdout, stderr)
}

func runACPInitialize(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	return runACPInitializeWithCapabilities(ctx, cmd, false)
}

func runACPInitializeWithCapabilities(ctx context.Context, cmd *exec.Cmd, cursorCapabilities bool) ([]byte, []byte, error) {
	stdoutFile, err := os.CreateTemp("", "ghrouter-acp-stdout-")
	if err != nil {
		return nil, nil, err
	}
	stdoutPath := stdoutFile.Name()
	defer os.Remove(stdoutPath)
	defer stdoutFile.Close()
	stderrFile, err := os.CreateTemp("", "ghrouter-acp-stderr-")
	if err != nil {
		return nil, nil, err
	}
	stderrPath := stderrFile.Name()
	defer os.Remove(stderrPath)
	defer stderrFile.Close()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	cmd.WaitDelay = 500 * time.Millisecond
	clientCapabilities := map[string]any{}
	if cursorCapabilities {
		clientCapabilities = map[string]any{
			"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		}
	}
	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion":    1,
			"clientCapabilities": clientCapabilities,
			"clientInfo":         map[string]string{"name": "ghrouter", "version": "dev"},
		},
	}
	payload, err := json.Marshal(initialize)
	if err != nil {
		return nil, nil, err
	}
	var stdinWriter *io.PipeWriter
	if cursorCapabilities {
		stdinReader, writer := io.Pipe()
		cmd.Stdin = stdinReader
		stdinWriter = writer
	} else {
		cmd.Stdin = strings.NewReader(string(payload) + "\n")
	}
	if err := cmd.Start(); err != nil {
		if stdinWriter != nil {
			_ = stdinWriter.Close()
		}
		return nil, nil, err
	}
	if stdinWriter != nil {
		go func() {
			message := append(append([]byte(nil), payload...), '\n')
			if cursorCapabilities {
				message = append(message, []byte("{\"jsonrpc\":\"2.0\",\"method\":\"initialized\",\"params\":{}}\n")...)
			}
			if _, err := stdinWriter.Write(message); err != nil {
				_ = stdinWriter.Close()
				return
			}
			timer := time.NewTimer(500 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
			_ = stdinWriter.Close()
		}()
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-waitCh:
	case <-ctx.Done():
		if stdinWriter != nil {
			_ = stdinWriter.CloseWithError(ctx.Err())
		}
		killDiscoveryProcess(cmd)
		runErr, _ = waitDiscoveryProcess(waitCh)
		if runErr == nil {
			runErr = ctx.Err()
		}
	}
	stdoutBytes, stdoutErr := os.ReadFile(stdoutPath)
	if stdoutErr != nil {
		return nil, nil, stdoutErr
	}
	stderrBytes, stderrErr := os.ReadFile(stderrPath)
	if stderrErr != nil {
		return stdoutBytes, nil, stderrErr
	}
	if runErr != nil {
		return stdoutBytes, stderrBytes, runErr
	}
	return stdoutBytes, stderrBytes, nil
}

func hasACPInitializeSuccess(stdout, stderr []byte) bool {
	if len(stdout) == 0 && len(stderr) == 0 {
		return false
	}
	type initializeResponse struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  *struct {
			ProtocolVersion int `json:"protocolVersion"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	for scanner.Scan() {
		var resp initializeResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.JSONRPC == "2.0" && len(resp.ID) > 0 && len(resp.Error) == 0 && resp.Result != nil && resp.Result.ProtocolVersion > 0 {
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
			info, hasInfo := provider.ModelInfo[model]
			if !eligibleForAutomaticList(provider.Type, info, model, hasInfo) {
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
			sort.Strings(members)
			providerLists["ghrouter/"+provider.Name] = members
		}
	}
	sort.Strings(all)
	for name, members := range capabilityLists {
		members = compactModelReferences(members)
		sort.Strings(members)
		capabilityLists[name] = members
	}
	providerListNames := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if provider != nil {
			providerListNames["ghrouter/"+provider.Name] = struct{}{}
		}
	}
	for i := range lists {
		if _, ok := providerListNames[lists[i].Name]; ok {
			lists[i].Models = append([]string(nil), providerLists[lists[i].Name]...)
		}
		if lists[i].Name == "ghrouter/auto" {
			lists[i].Models = append([]string(nil), all...)
		}
		if members, ok := capabilityLists[lists[i].Name]; ok {
			lists[i].Models = append([]string(nil), compactModelReferences(members)...)
		}
	}
	seen := make(map[string]bool, len(lists))
	for _, list := range lists {
		seen[list.Name] = true
	}
	providerNames := make([]string, 0, len(providerLists))
	for name := range providerLists {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		members := providerLists[name]
		if !seen[name] && len(members) > 0 {
			lists = append(lists, types.ModelList{Name: name, Kind: "provider", Strategy: "round-robin", Models: members})
		}
	}
	capabilityNames := []string{"ghrouter/context-1m", "ghrouter/reasoning", "ghrouter/vision", "ghrouter/tool-use"}
	for _, name := range capabilityNames {
		members := capabilityLists[name]
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

func eligibleForAutomaticList(providerType types.ProviderType, info types.ModelInfo, model string, hasInfo bool) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if !hasInfo && strings.TrimSpace(info.Source) == "" {
		return providerType == types.ProviderCustom || providerType == types.ProviderLocal
	}
	if info.VerifiedAt.IsZero() || info.VerificationError != "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(info.HealthStatus), "healthy") {
		return false
	}
	if !info.CooldownUntil.IsZero() {
		return false
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
	case types.ProviderNVIDIA:
		return "nv"
	default:
		return ""
	}
}

func canonicalizeModelPath(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(model, "/") {
		return strings.TrimSuffix(model, "/")
	}
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
	if provider == types.ProviderCodex {
		return discoverCodexModelsWithTimeout(path, timeout)
	}
	if provider == types.ProviderCursor {
		return discoverCursorModelsWithTimeout(path, timeout)
	}
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
	default:
		return nil, false
	}
}

type cursorACPDiscoveryMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *codexRPCError  `json:"error"`
}

type cursorACPInitializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
	AuthMethods     []struct {
		ID string `json:"id"`
	} `json:"authMethods"`
}

type cursorACPSessionResult struct {
	SessionID string `json:"sessionId"`
	Models    struct {
		AvailableModels []cursorACPModel `json:"availableModels"`
	} `json:"models"`
}

type cursorACPModel struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}

func discoverCursorModelsWithTimeout(path string, timeout time.Duration) discoveryResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "agent", "--trust", "acp")
	prepareDiscoveryCommand(cmd)
	cmd.Env = discoveryEnvForProvider(types.ProviderCursor)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP stdin unavailable"}
	}
	stdoutFile, err := os.CreateTemp("", "ghrouter-cursor-stdout-")
	if err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP stdout unavailable"}
	}
	stdoutPath := stdoutFile.Name()
	defer os.Remove(stdoutPath)
	defer stdoutFile.Close()
	stderrFile, err := os.CreateTemp("", "ghrouter-cursor-stderr-")
	if err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP stderr unavailable"}
	}
	stderrPath := stderrFile.Name()
	defer os.Remove(stderrPath)
	defer stderrFile.Close()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	cmd.WaitDelay = 500 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP failed to start"}
	}
	waitCh := make(chan error, 1)
	processDone := make(chan struct{})
	go func() {
		waitCh <- cmd.Wait()
		close(processDone)
	}()
	defer func() {
		_ = stdin.Close()
		killDiscoveryProcess(cmd)
		select {
		case <-waitCh:
		case <-time.After(time.Second):
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			killDiscoveryProcess(cmd)
		case <-processDone:
		}
	}()
	request := func(id int, method string, params any) (cursorACPDiscoveryMessage, error) {
		if err := writeCodexRPC(stdin, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return cursorACPDiscoveryMessage{}, err
		}
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		readMessage := func() (cursorACPDiscoveryMessage, bool) {
			data, readErr := os.ReadFile(stdoutPath)
			if readErr != nil {
				return cursorACPDiscoveryMessage{}, false
			}
			for _, line := range bytes.Split(data, []byte("\n")) {
				var message cursorACPDiscoveryMessage
				if err := json.Unmarshal(line, &message); err != nil || message.Method != "" || !cursorACPDiscoveryIDMatches(message.ID, id) {
					continue
				}
				return message, true
			}
			return cursorACPDiscoveryMessage{}, false
		}
		for {
			select {
			case <-ctx.Done():
				return cursorACPDiscoveryMessage{}, ctx.Err()
			case <-deadline.C:
				return cursorACPDiscoveryMessage{}, context.DeadlineExceeded
			case <-processDone:
				message, ok := readMessage()
				if !ok {
					return cursorACPDiscoveryMessage{}, io.EOF
				}
				if message.Error != nil {
					return cursorACPDiscoveryMessage{}, fmt.Errorf("%s", message.Error.Message)
				}
				return message, nil
			case <-ticker.C:
				if message, ok := readMessage(); ok {
					if message.Error != nil {
						return cursorACPDiscoveryMessage{}, fmt.Errorf("%s", message.Error.Message)
					}
					return message, nil
				}
			}
		}
	}

	initialize, err := request(1, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
		"clientInfo": map[string]string{"name": "ghrouter", "version": "dev"},
	})
	if err != nil {
		return cursorDiscoveryFailure(ctx, err)
	}
	var initializeResult cursorACPInitializeResult
	if err := json.Unmarshal(initialize.Result, &initializeResult); err != nil || initializeResult.ProtocolVersion == 0 {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP initialize returned an invalid response"}
	}
	if err := writeCodexRPC(stdin, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP initialization acknowledgement failed"}
	}
	requestID := 2
	if len(initializeResult.AuthMethods) > 0 {
		if _, err := request(requestID, "authenticate", map[string]string{"methodId": initializeResult.AuthMethods[0].ID}); err != nil {
			return cursorDiscoveryFailure(ctx, err)
		}
		requestID++
	}
	newSession, err := request(requestID, "session/new", map[string]any{"cwd": currentWorkDir(), "mcpServers": []any{}})
	if err != nil {
		return cursorDiscoveryFailure(ctx, err)
	}
	var session cursorACPSessionResult
	if err := json.Unmarshal(newSession.Result, &session); err != nil || session.SessionID == "" {
		return discoveryResult{status: string(types.DiscoveryError), err: "Cursor ACP session/new returned an invalid response"}
	}
	models := make([]string, 0, len(session.Models.AvailableModels))
	info := make(map[string]types.ModelInfo, len(session.Models.AvailableModels))
	seen := make(map[string]struct{}, len(session.Models.AvailableModels))
	for _, model := range session.Models.AvailableModels {
		publicID := cursorACPModelPublicID(model.ModelID)
		if publicID == "" {
			publicID = model.Name
		}
		id := canonicalModelID(types.ProviderCursor, publicID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
		info[id] = types.ModelInfo{Provider: string(types.ProviderCursor), Model: id, Source: "native"}
	}
	if len(models) == 0 {
		return discoveryResult{status: string(types.DiscoveryEmpty), info: info}
	}
	sort.Strings(models)
	return discoveryResult{models: models, info: info, status: string(types.DiscoverySuccess)}
}

func cursorACPModelPublicID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if index := strings.IndexByte(modelID, '['); index >= 0 {
		modelID = modelID[:index]
	}
	if strings.EqualFold(modelID, "default") {
		return "auto"
	}
	return modelID
}

func cursorACPDiscoveryIDMatches(raw json.RawMessage, want int) bool {
	var got int
	return json.Unmarshal(raw, &got) == nil && got == want
}

func cursorDiscoveryFailure(ctx context.Context, err error) discoveryResult {
	if ctx.Err() != nil {
		return discoveryResult{status: string(types.DiscoveryTimeout), err: "Cursor ACP model discovery timed out"}
	}
	if classifyCodexDiscoveryError(err.Error()) == "auth" {
		return discoveryResult{status: string(types.DiscoveryAuth), err: "Cursor ACP model discovery failed authentication"}
	}
	return discoveryResult{status: string(types.DiscoveryError), err: fmt.Sprintf("Cursor ACP model discovery failed: %v", err)}
}

func currentWorkDir() string {
	workDir, err := os.Getwd()
	if err != nil || workDir == "" {
		return "."
	}
	return workDir
}

type codexRPCResponse struct {
	ID     json.RawMessage     `json:"id"`
	Result *codexModelListPage `json:"result,omitempty"`
	Error  *codexRPCError      `json:"error,omitempty"`
}

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexModelListPage struct {
	Data       []codexModel `json:"data"`
	NextCursor *string      `json:"nextCursor"`
}

type codexModel struct {
	ID                        string                 `json:"id"`
	Model                     string                 `json:"model"`
	Hidden                    bool                   `json:"hidden"`
	InputModalities           []string               `json:"inputModalities"`
	SupportedReasoningEfforts []codexReasoningEffort `json:"supportedReasoningEfforts"`
}

type codexReasoningEffort struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

func discoverCodexModelsWithTimeout(path string, timeout time.Duration) discoveryResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.Command(path, "app-server", "--stdio")
	prepareDiscoveryCommand(cmd)
	cmd.Env = discoveryEnvForProvider(types.ProviderCodex)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "codex app-server stdin unavailable"}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "codex app-server stdout unavailable"}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "codex app-server stderr unavailable"}
	}
	cmd.WaitDelay = 500 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return discoveryResult{status: string(types.DiscoveryError), err: "codex app-server failed to start"}
	}
	waitCh := make(chan error, 1)
	waitStarted := false
	finishProcess := func() (error, bool) {
		killDiscoveryProcess(cmd)
		_ = stdout.Close()
		_ = stderr.Close()
		if !waitStarted {
			waitStarted = true
			go func() { waitCh <- cmd.Wait() }()
		}
		return waitDiscoveryProcess(waitCh)
	}
	stderrDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- data
	}()
	processDone := make(chan struct{})
	defer close(processDone)
	go func() {
		select {
		case <-ctx.Done():
			killDiscoveryProcess(cmd)
			_ = stdout.Close()
			_ = stderr.Close()
		case <-processDone:
		}
	}()

	if err := writeCodexRPC(stdin, map[string]any{
		"id":     1,
		"method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "ghrouter", "version": "dev"}},
	}); err != nil {
		_, _ = finishProcess()
		return discoveryResult{status: string(types.DiscoveryError), err: "codex app-server initialize failed"}
	}
	if err := writeCodexRPC(stdin, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		_, _ = finishProcess()
		return discoveryResult{status: string(types.DiscoveryError), err: "codex app-server initialization acknowledgement failed"}
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	models := make([]string, 0)
	info := make(map[string]types.ModelInfo)
	seenPages := make(map[string]struct{})
	cursor := ""
	requestID := 2
	for {
		params := map[string]any{"cursor": nil, "limit": 500, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := writeCodexRPC(stdin, map[string]any{"id": requestID, "method": "model/list", "params": params}); err != nil {
			_, _ = finishProcess()
			return discoveryResult{status: string(types.DiscoveryError), err: "codex model/list request failed"}
		}
		var page *codexModelListPage
		for scanner.Scan() {
			var response codexRPCResponse
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				continue
			}
			if !codexRPCIDMatches(response.ID, requestID) {
				continue
			}
			if response.Error != nil {
				_, _ = finishProcess()
				if classifyCodexDiscoveryError(response.Error.Message) == "auth" {
					return discoveryResult{status: string(types.DiscoveryAuth), err: "codex model discovery failed authentication"}
				}
				return discoveryResult{status: string(types.DiscoveryError), err: "codex model discovery request failed"}
			}
			page = response.Result
			break
		}
		if err := scanner.Err(); err != nil {
			_, _ = finishProcess()
			return discoveryResult{status: string(types.DiscoveryError), err: "codex model discovery stream failed"}
		}
		if page == nil {
			_, _ = finishProcess()
			if ctx.Err() != nil {
				return discoveryResult{status: string(types.DiscoveryTimeout), err: "codex model discovery timed out"}
			}
			return discoveryResult{status: string(types.DiscoveryError), err: "codex model/list returned no response"}
		}
		for _, model := range page.Data {
			if model.Hidden {
				continue
			}
			rawID := model.Model
			if rawID == "" {
				rawID = model.ID
			}
			id := canonicalModelID(types.ProviderCodex, rawID)
			if id == "" {
				continue
			}
			if _, ok := seenPages[id]; ok {
				continue
			}
			seenPages[id] = struct{}{}
			models = append(models, id)
			modelInfo := types.ModelInfo{Model: id, Source: "native", Provider: string(types.ProviderCodex)}
			for _, effort := range model.SupportedReasoningEfforts {
				if effort.ReasoningEffort != "" {
					modelInfo.Effort = append(modelInfo.Effort, effort.ReasoningEffort)
				}
			}
			modelInfo.Thinking = len(modelInfo.Effort) > 0
			for _, modality := range model.InputModalities {
				if strings.EqualFold(modality, "image") {
					modelInfo.Vision = true
				}
			}
			info[id] = modelInfo
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		if _, seen := seenPages[*page.NextCursor]; seen {
			break
		}
		seenPages[*page.NextCursor] = struct{}{}
		cursor = *page.NextCursor
		requestID++
	}
	_, _ = finishProcess()
	stderrBytes := <-stderrDone
	if ctx.Err() != nil {
		return discoveryResult{status: string(types.DiscoveryTimeout), err: "codex model discovery timed out"}
	}
	if len(models) == 0 {
		if classifyCodexDiscoveryError(string(stderrBytes)) == "auth" {
			return discoveryResult{status: string(types.DiscoveryAuth), err: "codex model discovery failed authentication"}
		}
		return discoveryResult{status: string(types.DiscoveryEmpty), info: info}
	}
	sort.Strings(models)
	return discoveryResult{models: models, info: info, status: string(types.DiscoverySuccess)}
}

func writeCodexRPC(w io.Writer, message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func codexRPCIDMatches(raw json.RawMessage, want int) bool {
	var got int
	return json.Unmarshal(raw, &got) == nil && got == want
}

func classifyCodexDiscoveryError(message string) string {
	lower := strings.ToLower(message)
	for _, keyword := range []string{"auth", "token", "unauthor", "permission", "login", "credential"} {
		if strings.Contains(lower, keyword) {
			return "auth"
		}
	}
	return "unknown"
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

const discoveryProcessWaitTimeout = time.Second

// waitDiscoveryProcess bounds cleanup when a CLI or one of its descendants
// does not exit after the process group has been terminated.
func waitDiscoveryProcess(waitCh <-chan error) (error, bool) {
	timer := time.NewTimer(discoveryProcessWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-waitCh:
		return err, true
	case <-timer.C:
		return context.DeadlineExceeded, false
	}
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
	cmd.WaitDelay = 500 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killDiscoveryProcess(cmd)
		case <-done:
		}
	}()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-waitCh:
	case <-ctx.Done():
		killDiscoveryProcess(cmd)
		runErr, _ = waitDiscoveryProcess(waitCh)
		if runErr == nil {
			runErr = ctx.Err()
		}
	}
	close(done)
	if runErr == nil && ctx.Err() != nil {
		runErr = ctx.Err()
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
