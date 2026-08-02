package types

import (
	"fmt"
	"time"
)

// Config is the root configuration for the router
type Config struct {
	ListenPort   int                `yaml:"listen_port"`
	Providers    []*Provider        `yaml:"providers"`
	Routes       []*Route           `yaml:"routes"`
	ModelLists   []ModelList        `yaml:"model_lists" json:"model_lists,omitempty"`
	ModelPolicy  ModelPolicy        `yaml:"model_policy,omitempty" json:"model_policy,omitempty"`
	Connections  []Connection       `yaml:"connections,omitempty" json:"connections,omitempty"`
	Pools        []Pool             `yaml:"pools,omitempty" json:"pools,omitempty"`
	Combos       []Combo            `yaml:"combos,omitempty" json:"combos,omitempty"`
	ACL          ACLConfig          `yaml:"acl"`
	Storage      StorageConfig      `yaml:"storage"`
	RateLimit    RateLimitConfig    `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	Cooldown     CooldownConfig     `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
	Health       HealthConfig       `yaml:"health,omitempty" json:"health,omitempty"`
	Logging      LoggingConfig      `yaml:"logging,omitempty" json:"logging,omitempty"`
	Server       ServerConfig       `yaml:"server,omitempty" json:"server,omitempty"`
	Verification VerificationConfig `yaml:"verification,omitempty" json:"verification,omitempty"`
	LocalBrain   LocalBrainConfig   `yaml:"local_brain,omitempty" json:"local_brain,omitempty"`
}

type Connection struct {
	Name     string            `yaml:"name" json:"name"`
	Provider string            `yaml:"provider" json:"provider"`
	Model    string            `yaml:"model" json:"model"`
	Enabled  bool              `yaml:"enabled" json:"enabled"`
	Metadata map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

type Pool struct {
	Name     string   `yaml:"name" json:"name"`
	Members  []string `yaml:"members" json:"members"`
	Strategy string   `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Enabled  bool     `yaml:"enabled" json:"enabled"`
}

type Combo struct {
	Name     string   `yaml:"name" json:"name"`
	Members  []string `yaml:"members" json:"members"`
	Strategy string   `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Judge    string   `yaml:"judge,omitempty" json:"judge,omitempty"`
	Enabled  bool     `yaml:"enabled" json:"enabled"`
}

type StorageConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
}

type ACLConfig struct {
	Enabled  bool                `yaml:"enabled"`
	Allow    []string            `yaml:"allow"`
	TokenEnv string              `yaml:"token_env"`
	KeysFile string              `yaml:"keys_file"`
	Scopes   map[string][]string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled" json:"enabled"`
	RequestsPerMinute int  `yaml:"requests_per_minute" json:"requests_per_minute"`
	Burst             int  `yaml:"burst" json:"burst"`
}

type CooldownConfig struct {
	Enabled         *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	DefaultDuration time.Duration `yaml:"default_duration,omitempty" json:"default_duration,omitempty"`
	MaxDuration     time.Duration `yaml:"max_duration,omitempty" json:"max_duration,omitempty"`
}

func (c CooldownConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type HealthConfig struct {
	Enabled    *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Interval   time.Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout    time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	TestPrompt string        `yaml:"test_prompt,omitempty" json:"test_prompt,omitempty"`
}

func (c HealthConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type LoggingConfig struct {
	Level  string `yaml:"level,omitempty" json:"level,omitempty"`
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	File   string `yaml:"file,omitempty" json:"file,omitempty"`
	Color  string `yaml:"color,omitempty" json:"color,omitempty"`
}

type ServerConfig struct {
	Host         string        `yaml:"host,omitempty" json:"host,omitempty"`
	ReadTimeout  time.Duration `yaml:"read_timeout,omitempty" json:"read_timeout,omitempty"`
	WriteTimeout time.Duration `yaml:"write_timeout,omitempty" json:"write_timeout,omitempty"`
	IdleTimeout  time.Duration `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
}

type VerificationConfig struct {
	Enabled        *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Startup        bool          `yaml:"startup,omitempty" json:"startup,omitempty"`
	Interval       time.Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout        time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Workers        int           `yaml:"workers,omitempty" json:"workers,omitempty"`
	BatchSize      int           `yaml:"batch_size,omitempty" json:"batch_size,omitempty"`
	MaxPerProvider int           `yaml:"max_per_provider,omitempty" json:"max_per_provider,omitempty"`
}

type LocalBrainConfig struct {
	Enabled        bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AutoProvision  bool          `yaml:"auto_provision,omitempty" json:"auto_provision,omitempty"`
	Backend        string        `yaml:"backend,omitempty" json:"backend,omitempty"`
	Model          string        `yaml:"model,omitempty" json:"model,omitempty"`
	Source         string        `yaml:"source,omitempty" json:"source,omitempty"`
	Host           string        `yaml:"host,omitempty" json:"host,omitempty"`
	Port           int           `yaml:"port,omitempty" json:"port,omitempty"`
	StartupTimeout time.Duration `yaml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`
	Restart        bool          `yaml:"restart,omitempty" json:"restart,omitempty"`
	RestartBackoff time.Duration `yaml:"restart_backoff,omitempty" json:"restart_backoff,omitempty"`
	MaxRestarts    int           `yaml:"max_restarts,omitempty" json:"max_restarts,omitempty"`
}

func (c VerificationConfig) IsEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// Provider represents a backend provider (CLI-based)
type Provider struct {
	Name             string               `yaml:"name"`
	Type             ProviderType         `yaml:"type"`
	CLIPath          string               `yaml:"cli_path"`
	BaseURL          string               `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	Args             []string             `yaml:"args"`
	Env              map[string]string    `yaml:"env"`
	Models           []string             `yaml:"models"`
	ModelInfo        map[string]ModelInfo `yaml:"model_info,omitempty" json:"model_info,omitempty"`
	Timeout          time.Duration        `yaml:"timeout"`
	Retries          int                  `yaml:"retries"`
	RetryBackoff     time.Duration        `yaml:"retry_backoff"`
	MaxTokens        int                  `yaml:"max_tokens"`
	WorkDir          string               `yaml:"work_dir"`
	Protocol         string               `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Origin           string               `yaml:"origin,omitempty" json:"origin,omitempty"`
	CapabilityStatus string               `yaml:"capability_status,omitempty" json:"capability_status,omitempty"`
	FailureReason    string               `yaml:"failure_reason,omitempty" json:"failure_reason,omitempty"`
	AuthMethod       AuthMethod           `yaml:"auth_method"`
	AuthConfig       map[string]string    `yaml:"auth_config"`
	Account          ProviderAccount      `yaml:"account"`
	Enabled          bool                 `yaml:"enabled"`
	Discovery        DiscoveryState       `yaml:"discovery,omitempty" json:"discovery,omitempty"`
}

// ProviderType identifies the provider kind
type ProviderType string

const (
	ProviderClaudeCode ProviderType = "claude-code"
	ProviderCodex      ProviderType = "codex"
	ProviderOpenCode   ProviderType = "opencode"
	ProviderMimo       ProviderType = "mimo"
	ProviderPi         ProviderType = "pi"
	ProviderCursor     ProviderType = "cursor"
	ProviderLocal      ProviderType = "local"
	ProviderOpenAI     ProviderType = "openai"
	ProviderAnthropic  ProviderType = "anthropic"
	ProviderAzure      ProviderType = "azure"
	ProviderOllama     ProviderType = "ollama"
	ProviderCustom     ProviderType = "custom"
)

// AuthMethod defines how the provider authenticates
type AuthMethod string

const (
	AuthNone       AuthMethod = "none"
	AuthEnv        AuthMethod = "env"
	AuthFile       AuthMethod = "file"
	AuthSubprocess AuthMethod = "subprocess"
)

// Route maps model patterns to providers
type Route struct {
	Pattern       string        `yaml:"pattern"`
	Provider      string        `yaml:"provider"`
	Fallback      []string      `yaml:"fallback"`
	Mode          string        `yaml:"mode,omitempty" json:"mode,omitempty"`
	Judge         string        `yaml:"judge,omitempty" json:"judge,omitempty"`
	MaxCandidates int           `yaml:"max_candidates,omitempty" json:"max_candidates,omitempty"`
	JudgeTimeout  time.Duration `yaml:"judge_timeout,omitempty" json:"judge_timeout,omitempty"`
	FirstComplete bool          `yaml:"first_complete,omitempty" json:"first_complete,omitempty"`
	MaxCostMicros int64         `yaml:"max_cost_micros,omitempty" json:"max_cost_micros,omitempty"`
}

type ProviderAccount struct {
	Plan      string   `json:"plan,omitempty" yaml:"plan,omitempty"`
	Balance   *float64 `json:"balance,omitempty" yaml:"balance,omitempty"`
	Currency  string   `json:"currency,omitempty" yaml:"currency,omitempty"`
	ResetAt   string   `json:"reset_at,omitempty" yaml:"reset_at,omitempty"`
	Source    string   `json:"source,omitempty" yaml:"source,omitempty"`
	Available bool     `json:"available" yaml:"available"`
}

func (a ProviderAccount) String() string {
	balance := ""
	if a.Balance != nil {
		balance = fmt.Sprintf("%.2f", *a.Balance)
	}
	parts := []string{a.Plan, balance, a.Currency, a.Source}
	out := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += part
	}
	if out == "" {
		return "unavailable"
	}
	return out
}

// OpenAIRequest is the incoming request from gh copilot (OpenAI Chat Completions format)
type OpenAIRequest struct {
	Model       string          `json:"model"`
	SessionID   string          `json:"-"`
	RequestID   string          `json:"-"`
	Messages    []OpenAIMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Stream      *bool           `json:"stream,omitempty"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
	Stop        any             `json:"stop,omitempty"`
}

// OpenAIMessage represents a message in the conversation
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// OpenAIToolCall represents a tool call
type OpenAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction represents a function call
type OpenAIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool represents a tool definition
type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIToolFunc `json:"function"`
}

// OpenAIToolFunc represents a function definition
type OpenAIToolFunc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// OpenAIResponse is the response back to gh copilot
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

// OpenAIChoice represents a completion choice
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// OpenAIUsage tracks token usage
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk for streaming responses
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
}

type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type StreamDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}
