package protocolkit

// OpenAIResponsesRequest is the OpenAI Responses API request.
type OpenAIResponsesRequest struct {
	Model             string            `json:"model"`
	Input             any               `json:"input,omitempty"`
	Include           []string          `json:"include,omitempty"`
	Conversation      string            `json:"conversation,omitempty"`
	ContextManagement any               `json:"context_management,omitempty"`
	Instructions      string            `json:"instructions,omitempty"`
	MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
	MaxInputTokens    *int              `json:"max_input_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	StreamOptions     *StreamOptions    `json:"stream_options,omitempty"`
	Tools             []ToolCallRequest `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Reasoning         *Reasoning        `json:"reasoning,omitempty"`
	Metadata          map[string]any    `json:"metadata,omitempty"`
	User              string            `json:"user,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	Store             *bool             `json:"store,omitempty"`
	Extra             map[string]any    `json:"-"`
}

// OpenAIResponsesResponse is the Responses API response.
type OpenAIResponsesResponse struct {
	Id                 string             `json:"id"`
	Object             string             `json:"object"`
	CreatedAt          int64              `json:"created_at"`
	Status             string             `json:"status"`
	Error              *OpenAIError       `json:"error,omitempty"`
	IncompleteDetails  *IncompleteDetails `json:"incomplete_details,omitempty"`
	Instructions       string             `json:"instructions,omitempty"`
	Model              string             `json:"model"`
	Output             []ResponsesOutput  `json:"output,omitempty"`
	ParallelToolCalls  bool               `json:"parallel_tool_calls"`
	PreviousResponseId string             `json:"previous_response_id,omitempty"`
	Reasoning          *Reasoning         `json:"reasoning,omitempty"`
	Temperature        *float64           `json:"temperature,omitempty"`
	TopP               *float64           `json:"top_p,omitempty"`
	Tools              []ToolCallRequest  `json:"tools,omitempty"`
	Usage              *ResponsesUsage    `json:"usage,omitempty"`
	Metadata           map[string]any     `json:"metadata,omitempty"`
}

// ResponsesUsage is the Responses API usage.
type ResponsesUsage struct {
	InputTokens         int                          `json:"input_tokens"`
	InputTokensDetails  *ResponsesInputTokenDetails  `json:"input_tokens_details,omitempty"`
	OutputTokens        int                          `json:"output_tokens"`
	OutputTokensDetails *ResponsesOutputTokenDetails `json:"output_tokens_details,omitempty"`
	TotalTokens         int                          `json:"total_tokens"`
}

// ResponsesInputTokenDetails breaks down Responses input tokens.
type ResponsesInputTokenDetails struct {
	CachedTokens          int `json:"cached_tokens,omitempty"`
	CachedCreationTokens  int `json:"cached_creation_tokens,omitempty"`
	CacheWriteTokens      int `json:"cache_write_tokens,omitempty"`
	CacheCreation5mTokens int `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens,omitempty"`
	TextTokens            int `json:"text_tokens,omitempty"`
	AudioTokens           int `json:"audio_tokens,omitempty"`
	ImageTokens           int `json:"image_tokens,omitempty"`
	ReasoningTokens       int `json:"reasoning_tokens,omitempty"`
}

// ResponsesOutputTokenDetails breaks down Responses output tokens.
type ResponsesOutputTokenDetails struct {
	TextTokens      int `json:"text_tokens,omitempty"`
	AudioTokens     int `json:"audio_tokens,omitempty"`
	ImageTokens     int `json:"image_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// IncompleteDetails describes an incomplete response.
type IncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

// ResponsesOutput is an output item in a Responses response.
type ResponsesOutput struct {
	Type      string                   `json:"type"`
	Id        string                   `json:"id,omitempty"`
	Status    string                   `json:"status,omitempty"`
	Role      string                   `json:"role,omitempty"`
	Content   []ResponsesOutputContent `json:"content,omitempty"`
	Quality   string                   `json:"quality,omitempty"`
	Size      string                   `json:"size,omitempty"`
	Result    string                   `json:"result,omitempty"`
	CallId    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
}

// ResponsesOutputContent is a content part in a Responses output.
type ResponsesOutputContent struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Annotations []any  `json:"annotations,omitempty"`
}

// ResponsesStreamResponse is a Responses stream event.
type ResponsesStreamResponse struct {
	Type           string                   `json:"type"`
	Response       *OpenAIResponsesResponse `json:"response,omitempty"`
	Delta          string                   `json:"delta,omitempty"`
	Item           *ResponsesOutput         `json:"item,omitempty"`
	OutputIndex    int                      `json:"output_index,omitempty"`
	ContentIndex   int                      `json:"content_index,omitempty"`
	Summary        string                   `json:"summary,omitempty"`
	SequenceNumber int                      `json:"sequence_number,omitempty"`
	Error          *OpenAIError             `json:"error,omitempty"`
}

// Input is a Responses input item.
type Input struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`
}

// MediaInput is a Responses input content part.
type MediaInput struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	FileUrl  string `json:"file_url,omitempty"`
	ImageUrl string `json:"image_url,omitempty"`
}
