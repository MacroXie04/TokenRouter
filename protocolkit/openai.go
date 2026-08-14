// Package protocolkit is TokenRouter's independent protocol-conversion module.
// It defines the wire DTOs for the OpenAI, Responses, Claude and Gemini
// protocols, plus pure conversion and usage-normalization helpers. It has no
// dependency on the TokenRouter root module and must build standalone
// (GOWORK=off go build ./...).
package protocolkit

// ContentType constants for multimodal message parts.
const (
	ContentTypeText       = "text"
	ContentTypeImageURL   = "image_url"
	ContentTypeInputAudio = "input_audio"
	ContentTypeFile       = "file"
	ContentTypeVideoUrl   = "video_url"
)

// GeneralOpenAIRequest is the OpenAI Chat Completions request DTO. Optional
// scalar fields use pointers so explicit zero values survive re-marshal.
type GeneralOpenAIRequest struct {
	Model               string                    `json:"model"`
	Messages            []Message                 `json:"messages"`
	Prompt              any                       `json:"prompt,omitempty"`
	Prefix              any                       `json:"prefix,omitempty"`
	Suffix              any                       `json:"suffix,omitempty"`
	Stream              bool                      `json:"stream,omitempty"`
	StreamOptions       *StreamOptions            `json:"stream_options,omitempty"`
	Temperature         *float64                  `json:"temperature,omitempty"`
	TopP                *float64                  `json:"top_p,omitempty"`
	N                   *int                      `json:"n,omitempty"`
	MaxTokens           *int                      `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                      `json:"max_completion_tokens,omitempty"`
	FrequencyPenalty    *float64                  `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64                  `json:"presence_penalty,omitempty"`
	LogitBias           map[string]float64        `json:"logit_bias,omitempty"`
	Stop                any                       `json:"stop,omitempty"`
	ResponseFormat      *ResponseFormat           `json:"response_format,omitempty"`
	Tools               []ToolCallRequest         `json:"tools,omitempty"`
	ToolChoice          any                       `json:"tool_choice,omitempty"`
	FunctionCall        any                       `json:"function_call,omitempty"`
	Functions           any                       `json:"functions,omitempty"`
	User                string                    `json:"user,omitempty"`
	Seed                *int64                    `json:"seed,omitempty"`
	ReasoningEffort     string                    `json:"reasoning_effort,omitempty"`
	Reasoning           *Reasoning                `json:"reasoning,omitempty"`
	WebSearchOptions    *WebSearchOptions         `json:"web_search_options,omitempty"`
	Metadata            map[string]any            `json:"metadata,omitempty"`
	Extra               map[string]any            `json:"-"`
}

// Message is an OpenAI chat message.
type Message struct {
	Role             string          `json:"role"`
	Content          any             `json:"content"`
	Name             string          `json:"name,omitempty"`
	Prefix           *bool           `json:"prefix,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ToolCalls        []ToolCallRequest `json:"tool_calls,omitempty"`
	ToolCallId       string          `json:"tool_call_id,omitempty"`
	FunctionCall     *FunctionRequest `json:"function_call,omitempty"`
	Extra            map[string]any  `json:"-"`
}

// MediaContent is a multimodal content part.
type MediaContent struct {
	Type         string             `json:"type"`
	Text         string             `json:"text,omitempty"`
	ImageURL     *MessageImageUrl   `json:"image_url,omitempty"`
	InputAudio   *MessageInputAudio `json:"input_audio,omitempty"`
	File         *MessageFile       `json:"file,omitempty"`
	VideoURL     *MessageVideoUrl   `json:"video_url,omitempty"`
	CacheControl any                `json:"cache_control,omitempty"`
}

// MessageImageUrl carries an image URL in multimodal content.
type MessageImageUrl struct {
	Url      string `json:"url"`
	Detail   string `json:"detail,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// MessageInputAudio carries inline base64 audio.
type MessageInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// MessageFile carries a file reference.
type MessageFile struct {
	Url       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// MessageVideoUrl carries a video URL reference.
type MessageVideoUrl struct {
	Url      string `json:"url"`
	Detail   string `json:"detail,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// ToolCallRequest is a tool call in a request (also used for responses).
type ToolCallRequest struct {
	Id       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function *FunctionRequest `json:"function,omitempty"`
	Custom   any              `json:"custom,omitempty"`
}

// FunctionRequest is a function tool definition.
type FunctionRequest struct {
	Description string         `json:"description,omitempty"`
	Name        string         `json:"name"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Arguments   string         `json:"arguments,omitempty"`
}

// StreamOptions controls streaming response details.
type StreamOptions struct {
	IncludeUsage        bool `json:"include_usage,omitempty"`
	IncludeObfuscation  bool `json:"include_obfuscation,omitempty"`
}

// ResponseFormat constrains the response shape.
type ResponseFormat struct {
	Type       string           `json:"type,omitempty"`
	JsonSchema *FormatJsonSchema `json:"json_schema,omitempty"`
}

// FormatJsonSchema is a JSON-schema response format.
type FormatJsonSchema struct {
	Description string         `json:"description,omitempty"`
	Name        string         `json:"name,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

// WebSearchOptions is a Claude-style web search option on the OpenAI request.
type WebSearchOptions struct {
	SearchContextSize string             `json:"search_context_size,omitempty"`
	UserLocation      *WebSearchLocation `json:"user_location,omitempty"`
}

// WebSearchLocation is a user location for web search.
type WebSearchLocation struct {
	Type      string  `json:"type,omitempty"`
	City      string  `json:"city,omitempty"`
	Region    string  `json:"region,omitempty"`
	Country   string  `json:"country,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

// Reasoning carries reasoning config (Responses-style).
type Reasoning struct {
	Effort  string           `json:"effort,omitempty"`
	Summary string           `json:"summary,omitempty"`
	Mode    string           `json:"mode,omitempty"`
	Context []ReasoningItem  `json:"context,omitempty"`
}

// ReasoningItem is a reasoning context item.
type ReasoningItem struct {
	Id         string `json:"id,omitempty"`
	Type       string `json:"type,omitempty"`
	Summary    []ResponsesSummaryPart `json:"summary,omitempty"`
}

// ResponsesSummaryPart is a summary content part.
type ResponsesSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Usage is the unified token usage.
type Usage struct {
	PromptTokens            int                  `json:"prompt_tokens"`
	CompletionTokens        int                  `json:"completion_tokens"`
	TotalTokens             int                  `json:"total_tokens"`
	PromptCacheHitTokens    int                  `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens   int                  `json:"prompt_cache_miss_tokens,omitempty"`
	PromptCacheWriteTokens  int                  `json:"prompt_cache_write_tokens,omitempty"`
	PromptCacheCreationTokens int                `json:"prompt_cache_creation_tokens,omitempty"`
	PromptTokensDetails     *InputTokenDetails   `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *OutputTokenDetails  `json:"completion_tokens_details,omitempty"`
	AudioTokens             int                  `json:"audio_tokens,omitempty"`
	ReasoningTokens         int                  `json:"reasoning_tokens,omitempty"`
}

// InputTokenDetails breaks down prompt/input tokens.
type InputTokenDetails struct {
	CachedTokens           int `json:"cached_tokens,omitempty"`
	CachedCreationTokens   int `json:"cached_creation_tokens,omitempty"`
	CacheWriteTokens       int `json:"cache_write_tokens,omitempty"`
	TextTokens             int `json:"text_tokens,omitempty"`
	AudioTokens            int `json:"audio_tokens,omitempty"`
	ImageTokens            int `json:"image_tokens,omitempty"`
	ReasoningTokens        int `json:"reasoning_tokens,omitempty"`
}

// OutputTokenDetails breaks down completion/output tokens.
type OutputTokenDetails struct {
	TextTokens      int `json:"text_tokens,omitempty"`
	AudioTokens     int `json:"audio_tokens,omitempty"`
	ImageTokens     int `json:"image_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ChatCompletionsResponse is a non-stream chat completion response.
type ChatCompletionsResponse struct {
	Id                string                   `json:"id"`
	Object            string                   `json:"object"`
	Created           int64                    `json:"created"`
	Model             string                   `json:"model"`
	SystemFingerprint string                   `json:"system_fingerprint,omitempty"`
	Choices           []ChatCompletionsChoice  `json:"choices"`
	Usage             *Usage                   `json:"usage,omitempty"`
	Error             *OpenAIError             `json:"error,omitempty"`
}

// ChatCompletionsChoice is a non-stream choice.
type ChatCompletionsChoice struct {
	Index        int               `json:"index"`
	Message      *ChatResponseMessage `json:"message,omitempty"`
	FinishReason string            `json:"finish_reason"`
	Logprobs     any               `json:"logprobs,omitempty"`
	Delta        *ChatCompletionsStreamResponseChoiceDelta `json:"delta,omitempty"`
}

// ChatResponseMessage is an assistant message in a response.
type ChatResponseMessage struct {
	Role             string            `json:"role"`
	Content          any               `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	ToolCalls        []ToolCallResponse `json:"tool_calls,omitempty"`
	FunctionCall     *FunctionResponse  `json:"function_call,omitempty"`
	Refusal          string            `json:"refusal,omitempty"`
}

// ToolCallResponse is a tool call in a response.
type ToolCallResponse struct {
	Index    int               `json:"index,omitempty"`
	Id       string            `json:"id"`
	Type     string            `json:"type,omitempty"`
	Function *FunctionResponse `json:"function,omitempty"`
}

// FunctionResponse is a function call in a response.
type FunctionResponse struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name"`
	Parameters  string `json:"parameters,omitempty"`
	Arguments   string `json:"arguments,omitempty"`
}

// ChatCompletionsStreamResponse is a chat stream chunk.
type ChatCompletionsStreamResponse struct {
	Id                string                                `json:"id"`
	Object            string                                `json:"object"`
	Created           int64                                 `json:"created"`
	Model             string                                `json:"model"`
	SystemFingerprint string                                `json:"system_fingerprint,omitempty"`
	Choices           []ChatCompletionsStreamResponseChoice `json:"choices"`
	Usage             *Usage                                `json:"usage,omitempty"`
	Error             *OpenAIError                          `json:"error,omitempty"`
}

// ChatCompletionsStreamResponseChoice is a stream choice.
type ChatCompletionsStreamResponseChoice struct {
	Delta        ChatCompletionsStreamResponseChoiceDelta `json:"delta"`
	Logprobs     any                                      `json:"logprobs,omitempty"`
	FinishReason *string                                  `json:"finish_reason,omitempty"`
	Index        int                                      `json:"index"`
}

// ChatCompletionsStreamResponseChoiceDelta is a stream delta.
type ChatCompletionsStreamResponseChoiceDelta struct {
	Content          string             `json:"content,omitempty"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	Reasoning        string             `json:"reasoning,omitempty"`
	Role             string             `json:"role,omitempty"`
	ToolCalls        []ToolCallResponse `json:"tool_calls,omitempty"`
	FunctionCall     *FunctionResponse  `json:"function_call,omitempty"`
}

// OpenAITextResponse is a non-stream completions (legacy) response.
type OpenAITextResponse struct {
	Id      string                  `json:"id"`
	Model   string                  `json:"model"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Choices []OpenAITextResponseChoice `json:"choices"`
	Usage   *Usage                  `json:"usage"`
	Error   *OpenAIError            `json:"error,omitempty"`
}

// OpenAITextResponseChoice is a legacy completions choice.
type OpenAITextResponseChoice struct {
	Index        int    `json:"index"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
	Logprobs     any    `json:"logprobs,omitempty"`
}

// CompletionsStreamResponse is a legacy completions stream chunk.
type CompletionsStreamResponse struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []CompletionsStreamResponseChoice `json:"choices"`
	Usage   *Usage `json:"usage,omitempty"`
	Error   *OpenAIError `json:"error,omitempty"`
}

// CompletionsStreamResponseChoice is a legacy completions stream choice.
type CompletionsStreamResponseChoice struct {
	Index        int    `json:"index"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// OpenAIError is the standard error envelope.
type OpenAIError struct {
	Message string      `json:"message"`
	Type    string      `json:"type,omitempty"`
	Param   string      `json:"param,omitempty"`
	Code    string      `json:"code,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}
