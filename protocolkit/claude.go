package protocolkit

import "encoding/json"

// ClaudeRequest is the Anthropic Messages request.
type ClaudeRequest struct {
	Model         string          `json:"model"`
	Prompt        string          `json:"prompt,omitempty"`
	System        any             `json:"system,omitempty"`
	Messages      []ClaudeMessage `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Metadata      *ClaudeMetadata `json:"metadata,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    any             `json:"tool_choice,omitempty"`
	Thinking      *Thinking       `json:"thinking,omitempty"`
	OutputConfig  json.RawMessage `json:"output_config,omitempty"`
	CacheControl  any             `json:"cache_control,omitempty"`
}

// ClaudeMessage is a message in a Claude request.
type ClaudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ClaudeMediaMessage is a media part of Claude content.
type ClaudeMediaMessage struct {
	Type         string               `json:"type"`
	ID           string               `json:"id,omitempty"`
	Name         string               `json:"name,omitempty"`
	Text         string               `json:"text,omitempty"`
	Model        string               `json:"model,omitempty"`
	Source       *ClaudeMessageSource `json:"source,omitempty"`
	Input        any                  `json:"input,omitempty"`
	ToolUseID    string               `json:"tool_use_id,omitempty"`
	Content      any                  `json:"content,omitempty"`
	IsError      *bool                `json:"is_error,omitempty"`
	Usage        *ClaudeUsage         `json:"usage,omitempty"`
	StopReason   string               `json:"stop_reason,omitempty"`
	CacheControl any                  `json:"cache_control,omitempty"`
}

// ClaudeMessageSource is a Claude media source.
type ClaudeMessageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	Url       string `json:"url,omitempty"`
}

// ClaudeResponse is the Anthropic Messages response.
type ClaudeResponse struct {
	Id           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []ClaudeMediaMessage `json:"content"`
	Completion   string               `json:"completion,omitempty"`
	StopReason   string               `json:"stop_reason"`
	StopSequence string               `json:"stop_sequence,omitempty"`
	Model        string               `json:"model"`
	Error        *ClaudeError         `json:"error,omitempty"`
	Usage        *ClaudeUsage         `json:"usage,omitempty"`
}

// ClaudeUsage is Anthropic token usage.
type ClaudeUsage struct {
	InputTokens              int                  `json:"input_tokens"`
	CacheCreationInputTokens int                  `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                  `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            *ClaudeCacheCreation `json:"cache_creation,omitempty"`
	OutputTokens             int                  `json:"output_tokens"`
	ServerToolUse            *ClaudeServerToolUse `json:"server_tool_use,omitempty"`
}

// ClaudeCacheCreation is Anthropic's TTL-specific cache-write breakdown.
// CacheCreationInputTokens remains the authoritative total when it is larger;
// any unclassified remainder is billed as the default five-minute class.
type ClaudeCacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// ClaudeServerToolUse records server-side tool usage.
type ClaudeServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests,omitempty"`
}

// ClaudeError is the Anthropic error envelope.
type ClaudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Tool is a Claude tool definition.
type Tool struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema *InputSchema `json:"input_schema,omitempty"`
}

// InputSchema is a Claude tool input schema.
type InputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
}

// ClaudeWebSearchTool is the Claude web search tool.
type ClaudeWebSearchTool struct {
	Type         string                       `json:"type"`
	Name         string                       `json:"name"`
	MaxUses      int                          `json:"max_uses,omitempty"`
	UserLocation *ClaudeWebSearchUserLocation `json:"user_location,omitempty"`
}

// ClaudeWebSearchUserLocation is a web search user location.
type ClaudeWebSearchUserLocation struct {
	Type    string `json:"type"`
	City    string `json:"city,omitempty"`
	Region  string `json:"region,omitempty"`
	Country string `json:"country,omitempty"`
}

// ClaudeToolChoice constrains tool use.
type ClaudeToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// Thinking is the Claude extended thinking config.
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
	Display      string `json:"display,omitempty"`
}

// ClaudeMetadata is request metadata.
type ClaudeMetadata struct {
	UserId string `json:"user_id,omitempty"`
}
