package account

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/types"
)

type Status struct {
	Plan      string    `json:"plan,omitempty" yaml:"plan,omitempty"`
	Balance   *float64  `json:"balance,omitempty" yaml:"balance,omitempty"`
	Currency  string    `json:"currency,omitempty" yaml:"currency,omitempty"`
	ResetAt   time.Time `json:"reset_at,omitempty" yaml:"reset_at,omitempty"`
	Source    string    `json:"source,omitempty" yaml:"source,omitempty"`
	Healthy   bool      `json:"healthy"`
	Available bool      `json:"available"`
}

func (s Status) MarshalJSON() ([]byte, error) {
	type statusAlias Status
	var resetAt *time.Time
	if !s.ResetAt.IsZero() {
		value := s.ResetAt
		resetAt = &value
	}
	return json.Marshal(struct {
		statusAlias
		ResetAt *time.Time `json:"reset_at,omitempty"`
	}{statusAlias: statusAlias(s), ResetAt: resetAt})
}

type nativeStatusCacheEntry struct {
	value     bool
	expiresAt time.Time
}

var nativeStatusCache = struct {
	sync.Mutex
	entries map[string]nativeStatusCacheEntry
}{entries: make(map[string]nativeStatusCacheEntry)}

func Blocked(status Status) bool {
	switch status.Source {
	case "unavailable", "missing-provider", "unknown":
		return false
	default:
		return !status.Available || !status.Healthy
	}
}

func Load(p *types.Provider) Status {
	if p == nil {
		return Status{Available: false, Healthy: false, Source: "missing-provider"}
	}
	if status, ok := loadJSONStatus(p); ok {
		return status
	}
	if isUnsupportedProvider(p) {
		return Status{Available: false, Healthy: false, Source: "unsupported"}
	}
	if hasAccountMetadata(p) {
		return buildAccountMetadataStatus(p)
	}
	nativeCredential := credentialFileExists(p)
	nativeStatus := nativeAuthStatus(p)
	if !hasAuthSignal(p) && !nativeCredential && !nativeStatus {
		if isRecognizedProvider(p) {
			return Status{Available: false, Healthy: false, Source: "auth"}
		}
		return Status{Available: false, Healthy: false, Source: "unavailable"}
	}
	source := "unknown"
	if nativeCredential || nativeStatus {
		source = "native"
	}
	s := Status{Available: true, Healthy: true, Source: source}
	keyBase := providerKey(p.Name)
	s.Plan = firstNonEmpty(
		p.AuthConfig["plan"],
		lookupEnv(keyBase, "PLAN"),
		lookupEnv("GHR_PROVIDER", "PLAN"),
	)
	s.Currency = firstNonEmpty(
		p.AuthConfig["balance_currency"],
		lookupEnv(keyBase, "BALANCE_CURRENCY"),
		lookupEnv("GHR_PROVIDER", "BALANCE_CURRENCY"),
	)
	s.Balance = parseOptionalFloat(firstNonEmpty(
		p.AuthConfig["balance"],
		lookupEnv(keyBase, "BALANCE"),
		lookupEnv("GHR_PROVIDER", "BALANCE"),
	))
	if raw := firstNonEmpty(
		p.AuthConfig["reset_at"],
		lookupEnv(keyBase, "RESET_AT"),
		lookupEnv("GHR_PROVIDER", "RESET_AT"),
	); raw != "" {
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			s.ResetAt = ts
		}
	}
	return s
}

func nativeAuthStatus(p *types.Provider) bool {
	if p == nil || p.CLIPath == "" || p.Type != types.ProviderCursor {
		return false
	}
	args := []string{"agent", "status"}
	if filepath.Base(p.CLIPath) == "agent" {
		args = []string{"status"}
	}
	cacheKey := p.CLIPath + "\x00" + strings.Join(args, "\x00")
	now := time.Now()
	nativeStatusCache.Lock()
	if cached, ok := nativeStatusCache.entries[cacheKey]; ok && now.Before(cached.expiresAt) {
		nativeStatusCache.Unlock()
		return cached.value
	}
	nativeStatusCache.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.CLIPath, args...)
	prepareAuthCommand(cmd)
	output, err := runAuthCommand(ctx, cmd)
	if err != nil || ctx.Err() != nil {
		nativeStatusCache.Lock()
		nativeStatusCache.entries[cacheKey] = nativeStatusCacheEntry{expiresAt: now.Add(30 * time.Second)}
		nativeStatusCache.Unlock()
		return false
	}
	value := strings.Contains(strings.ToLower(string(output)), "logged in")
	nativeStatusCache.Lock()
	nativeStatusCache.entries[cacheKey] = nativeStatusCacheEntry{value: value, expiresAt: time.Now().Add(30 * time.Second)}
	nativeStatusCache.Unlock()
	return value
}

func credentialFileExists(p *types.Provider) bool {
	if p == nil {
		return false
	}
	home := os.Getenv("HOME")
	if home == "" {
		return false
	}
	paths := make([]string, 0, 3)
	switch p.Type {
	case types.ProviderClaudeCode:
		paths = append(paths, ".claude/.credentials.json", ".config/claude/auth.json")
	case types.ProviderCodex:
		paths = append(paths, ".codex/auth.json", ".config/codex/auth.json")
	case types.ProviderOpenCode:
		paths = append(paths, ".local/share/opencode/auth.json", ".config/opencode/auth.json")
	case types.ProviderMimo:
		paths = append(paths, ".local/share/mimocode/auth.json", ".config/mimo/auth.json")
	case types.ProviderPi:
		paths = append(paths, ".pi/agent/auth.json", ".config/pi/auth.json")
	case types.ProviderCursor:
		paths = append(paths, ".cursor/auth.json", ".config/cursor/auth.json")
	default:
		return false
	}
	for _, envKey := range []string{"CODEX_HOME", "OPENCODE_HOME", "MIMO_HOME", "PI_HOME"} {
		if root := os.Getenv(envKey); root != "" {
			paths = append(paths, filepath.Join(root, "auth.json"))
		}
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(home, path)
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}

func buildAccountMetadataStatus(p *types.Provider) Status {
	s := Status{Available: true, Healthy: true, Source: "unknown"}
	keyBase := providerKey(p.Name)
	s.Plan = firstNonEmpty(
		p.AuthConfig["plan"],
		lookupEnv(keyBase, "PLAN"),
		lookupEnv("GHR_PROVIDER", "PLAN"),
	)
	s.Currency = firstNonEmpty(
		p.AuthConfig["balance_currency"],
		lookupEnv(keyBase, "BALANCE_CURRENCY"),
		lookupEnv("GHR_PROVIDER", "BALANCE_CURRENCY"),
	)
	s.Balance = parseOptionalFloat(firstNonEmpty(
		p.AuthConfig["balance"],
		lookupEnv(keyBase, "BALANCE"),
		lookupEnv("GHR_PROVIDER", "BALANCE"),
	))
	if raw := firstNonEmpty(
		p.AuthConfig["reset_at"],
		lookupEnv(keyBase, "RESET_AT"),
		lookupEnv("GHR_PROVIDER", "RESET_AT"),
	); raw != "" {
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			s.ResetAt = ts
		}
	}
	return s
}

func loadJSONStatus(p *types.Provider) (Status, bool) {
	raw := firstNonEmpty(
		p.AuthConfig["account_json"],
		lookupEnv(providerKey(p.Name), "ACCOUNT_JSON"),
		lookupEnv("GHR_PROVIDER", "ACCOUNT_JSON"),
	)
	if raw == "" {
		return Status{}, false
	}
	var payload struct {
		Plan      string    `json:"plan,omitempty"`
		Balance   *float64  `json:"balance,omitempty"`
		Currency  string    `json:"currency,omitempty"`
		ResetAt   time.Time `json:"reset_at,omitempty"`
		Source    string    `json:"source,omitempty"`
		Healthy   *bool     `json:"healthy,omitempty"`
		Available *bool     `json:"available,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return Status{Available: false, Healthy: false, Source: "invalid-json"}, true
	}
	status := Status{
		Plan:      payload.Plan,
		Balance:   payload.Balance,
		Currency:  payload.Currency,
		ResetAt:   payload.ResetAt,
		Source:    payload.Source,
		Available: true,
		Healthy:   true,
	}
	if payload.Available != nil {
		status.Available = *payload.Available
	}
	if payload.Healthy != nil {
		status.Healthy = *payload.Healthy
	}
	if status.Source == "" {
		status.Source = "json"
	}
	return status, true
}

func isUnsupportedProvider(p *types.Provider) bool {
	if p == nil {
		return false
	}
	switch p.Type {
	case types.ProviderCustom:
		return true
	case types.ProviderLocal:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(p.Name)) {
	case "custom":
		return true
	default:
		return false
	}
}

func isRecognizedProvider(p *types.Provider) bool {
	if p == nil {
		return false
	}
	switch p.Type {
	case types.ProviderClaudeCode, types.ProviderCodex, types.ProviderOpenCode, types.ProviderMimo, types.ProviderPi, types.ProviderCursor, types.ProviderOpenAI, types.ProviderAnthropic, types.ProviderAzure, types.ProviderOllama, types.ProviderNVIDIA:
		return true
	}
	switch strings.ToLower(strings.TrimSpace(p.Name)) {
	case "claude", "claude-code", "codex", "opencode", "mimo", "pi", "cursor", "openai", "anthropic", "azure", "ollama", "nvidia":
		return true
	default:
		return false
	}
}

func hasAuthSignal(p *types.Provider) bool {
	if p == nil {
		return false
	}
	for _, key := range authEnvKeysForProvider(p) {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	for _, key := range []string{"plan", "balance", "balance_currency", "reset_at", "account_json"} {
		if strings.TrimSpace(p.AuthConfig[key]) != "" {
			return true
		}
	}
	return false
}

func hasAccountMetadata(p *types.Provider) bool {
	if p == nil {
		return false
	}
	for _, key := range []string{"plan", "balance", "balance_currency", "reset_at"} {
		if strings.TrimSpace(p.AuthConfig[key]) != "" {
			return true
		}
	}
	for _, key := range []string{"PLAN", "BALANCE", "BALANCE_CURRENCY", "RESET_AT"} {
		if strings.TrimSpace(lookupEnv(providerKey(p.Name), key)) != "" || strings.TrimSpace(lookupEnv("GHR_PROVIDER", key)) != "" {
			return true
		}
	}
	return false
}

func authEnvKeysForProvider(p *types.Provider) []string {
	if p == nil {
		return nil
	}
	switch p.Type {
	case types.ProviderClaudeCode:
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	case types.ProviderCodex:
		return []string{"OPENAI_API_KEY", "OPENAI_API_BASE", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "CODEX_HOME"}
	case types.ProviderOpenCode:
		return []string{"OPENAI_API_KEY", "OPENAI_API_BASE", "OPENCODE_API_KEY", "OPENCODE_HOME"}
	case types.ProviderMimo:
		return []string{"OPENAI_API_KEY", "MIMO_API_KEY", "MIMO_HOME"}
	case types.ProviderPi:
		return []string{"PI_HOME", "OPENAI_API_KEY"}
	case types.ProviderCursor:
		return []string{"CURSOR_API_KEY", "CURSOR_API_ENDPOINT"}
	case types.ProviderOpenAI:
		return []string{"OPENAI_API_KEY", "OPENAI_API_BASE"}
	case types.ProviderAnthropic:
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	case types.ProviderAzure:
		return []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"}
	case types.ProviderOllama:
		return nil
	case types.ProviderNVIDIA:
		keys := []string{"NVIDIA_API_KEY"}
		if p.AuthConfig != nil {
			if envName := strings.TrimSpace(p.AuthConfig["api_key_env"]); envName != "" && envName != keys[0] {
				keys = append(keys, envName)
			}
		}
		return keys
	default:
		return nil
	}
}

func Weight(status Status) float64 {
	if !status.Available {
		return 0.15
	}
	score := 1.0
	if status.Plan != "" {
		score += 0.05
	}
	if status.Balance != nil {
		switch {
		case *status.Balance <= 0:
			score -= 0.6
		case *status.Balance < 0.2:
			score -= 0.25
		case *status.Balance < 0.5:
			score += 0.05
		case *status.Balance < 0.8:
			score += 0.12
		default:
			score += 0.2
		}
	}
	if !status.ResetAt.IsZero() && time.Until(status.ResetAt) < 24*time.Hour {
		score -= 0.05
	}
	if score < 0.1 {
		return 0.1
	}
	return score
}

func providerKey(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	name = strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(name)
	return "GHR_PROVIDER_" + name
}

func lookupEnv(prefix, key string) string {
	if v := os.Getenv(prefix + "_" + key); v != "" {
		return v
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseOptionalFloat(raw string) *float64 {
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

func (s Status) String() string {
	balance := ""
	if s.Balance != nil {
		balance = fmt.Sprintf("%.2f", *s.Balance)
	}
	return fmt.Sprintf("%s %s %s %s", s.Plan, balance, s.Currency, s.Source)
}
