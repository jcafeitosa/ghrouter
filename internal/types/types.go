package types

import (
	"fmt"
	"time"
)

// Config is the root configuration for the router
type Config struct {
	ListenPort int         `yaml:"listen_port"`
	Providers  []*Provider `yaml:"providers"`
	Routes     []*Route    `yaml:"routes"`
}

// Provider represents a backend provider (CLI-based)
type Provider struct {
	Name       string            `yaml:"name"`
	Type       ProviderType      `yaml:"type"`
	CLIPath    string            `yaml:"cli_path"`
	Args       []string          `yaml:"args"`
	Env        map[string]string `yaml:"env"`
	Models     []string          `yaml:"models"`
	Timeout    time.Duration     `yaml:"timeout"`
	MaxTokens  int               `yaml:"max_tokens"`
	WorkDir    string            `yaml:"work_dir"`
	AuthMethod AuthMethod        `yaml:"auth_method"`
	AuthConfig map[string]string `yaml:"auth_config"`
	Account    ProviderAccount   `yaml:"account"`
	Enabled    bool              `yaml:"enabled"`
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
	Pattern  string   `yaml:"pattern"`
	Provider string   `yaml:"provider"`
	Fallback []string `yaml:"fallback"`
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
