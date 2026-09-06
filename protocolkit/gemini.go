package protocolkit

// GeminiChatRequest is the Gemini GenerateContent request.
type GeminiChatRequest struct {
	Requests          []GeminiChatRequestWrapper  `json:"requests,omitempty"`
	Contents          []GeminiChatContent         `json:"contents"`
	SafetySettings    []GeminiChatSafetySettings  `json:"safetySettings,omitempty"`
	GenerationConfig  *GeminiChatGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []GeminiChatTool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig                 `json:"toolConfig,omitempty"`
	SystemInstruction *GeminiChatContent          `json:"systemInstruction,omitempty"`
	Model             string                      `json:"model,omitempty"`
}

// GeminiChatRequestWrapper is a batch request entry.
type GeminiChatRequestWrapper struct {
	Contents         []GeminiChatContent         `json:"contents"`
	GenerationConfig *GeminiChatGenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings   []GeminiChatSafetySettings  `json:"safetySettings,omitempty"`
	Tools            []GeminiChatTool            `json:"tools,omitempty"`
}

// GeminiChatContent is a Gemini content turn.
type GeminiChatContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

// GeminiPart is a Gemini content part.
type GeminiPart struct {
	Text                string                         `json:"text,omitempty"`
	Thought             bool                           `json:"thought,omitempty"`
	ThoughtSignature    string                         `json:"thoughtSignature,omitempty"`
	InlineData          *GeminiInlineData              `json:"inlineData,omitempty"`
	FileData            *GeminiFileData                `json:"fileData,omitempty"`
	FunctionCall        *FunctionCall                  `json:"functionCall,omitempty"`
	FunctionResponse    *GeminiFunctionResponse        `json:"functionResponse,omitempty"`
	ExecutableCode      *GeminiPartExecutableCode      `json:"executableCode,omitempty"`
	CodeExecutionResult *GeminiPartCodeExecutionResult `json:"codeExecutionResult,omitempty"`
}

// GeminiThoughtSignatureBypass is attached to synthetic model-side function
// calls reconstructed from OpenAI history. Gemini requires a non-empty
// signature on these history parts even though the original protocol cannot
// carry the provider-generated opaque signature.
const GeminiThoughtSignatureBypass = "context_engineering_is_the_way_to_go"

// GeminiInlineData is inline base64 media.
type GeminiInlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// GeminiFileData is a file URI reference.
type GeminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileUri  string `json:"fileUri,omitempty"`
}

// FunctionCall is a Gemini function call.
type FunctionCall struct {
	Name string         `json:"name,omitempty"`
	Args map[string]any `json:"args,omitempty"`
}

// GeminiFunctionResponse is a Gemini function response.
type GeminiFunctionResponse struct {
	Name         string `json:"name,omitempty"`
	Response     any    `json:"response,omitempty"`
	WillContinue *bool  `json:"willContinue,omitempty"`
}

// GeminiPartExecutableCode is an executable code part.
type GeminiPartExecutableCode struct {
	Language string `json:"language,omitempty"`
	Code     string `json:"code,omitempty"`
}

// GeminiPartCodeExecutionResult is a code execution result.
type GeminiPartCodeExecutionResult struct {
	Outcome string `json:"outcome,omitempty"`
	Output  string `json:"output,omitempty"`
}

// GeminiChatGenerationConfig is Gemini generation config.
type GeminiChatGenerationConfig struct {
	Temperature        *float64              `json:"temperature,omitempty"`
	TopP               *float64              `json:"topP,omitempty"`
	TopK               *float64              `json:"topK,omitempty"`
	MaxOutputTokens    *int                  `json:"maxOutputTokens,omitempty"`
	CandidateCount     *int                  `json:"candidateCount,omitempty"`
	StopSequences      []string              `json:"stopSequences,omitempty"`
	ResponseMimeType   string                `json:"responseMimeType,omitempty"`
	ResponseModalities []string              `json:"responseModalities,omitempty"`
	ResponseSchema     map[string]any        `json:"responseSchema,omitempty"`
	ThinkingConfig     *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

// GeminiThinkingConfig is Gemini thinking config.
type GeminiThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

// ToolConfig configures function calling.
type ToolConfig struct {
	FunctionCallingConfig            *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
	RetrievalConfig                  *RetrievalConfig       `json:"retrievalConfig,omitempty"`
	IncludeServerSideToolInvocations *bool                  `json:"includeServerSideToolInvocations,omitempty"`
}

// FunctionCallingConfig configures function calling mode.
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// RetrievalConfig configures retrieval (RAG).
type RetrievalConfig struct {
	LanguageCode string `json:"languageCode,omitempty"`
}

// GeminiChatTool is a Gemini tool.
type GeminiChatTool struct {
	GoogleSearch          any              `json:"googleSearch,omitempty"`
	GoogleSearchRetrieval any              `json:"googleSearchRetrieval,omitempty"`
	CodeExecution         any              `json:"codeExecution,omitempty"`
	FunctionDeclarations  []GeminiFunction `json:"functionDeclarations,omitempty"`
}

// GeminiFunction is a function declaration.
type GeminiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// GeminiChatSafetySettings is a safety setting.
type GeminiChatSafetySettings struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GeminiChatResponse is the Gemini GenerateContent response.
type GeminiChatResponse struct {
	Candidates     []GeminiChatCandidate     `json:"candidates,omitempty"`
	PromptFeedback *GeminiChatPromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *GeminiUsageMetadata      `json:"usageMetadata,omitempty"`
	Error          *GeminiError              `json:"error,omitempty"`
}

// GeminiChatCandidate is a response candidate.
type GeminiChatCandidate struct {
	Content           *GeminiChatContent       `json:"content,omitempty"`
	FinishReason      string                   `json:"finishReason,omitempty"`
	Index             int                      `json:"index,omitempty"`
	SafetyRatings     []GeminiChatSafetyRating `json:"safetyRatings,omitempty"`
	GroundingMetadata *GeminiGroundingMetadata `json:"groundingMetadata,omitempty"`
}

// GeminiChatSafetyRating is a safety rating.
type GeminiChatSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

// GeminiChatPromptFeedback is prompt feedback.
type GeminiChatPromptFeedback struct {
	BlockReason   string                   `json:"blockReason,omitempty"`
	SafetyRatings []GeminiChatSafetyRating `json:"safetyRatings,omitempty"`
}

// GeminiUsageMetadata is Gemini token usage.
type GeminiUsageMetadata struct {
	PromptTokenCount           int                             `json:"promptTokenCount"`
	ToolUsePromptTokenCount    int                             `json:"toolUsePromptTokenCount,omitempty"`
	CandidatesTokenCount       int                             `json:"candidatesTokenCount"`
	ThoughtsTokenCount         int                             `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount    int                             `json:"cachedContentTokenCount,omitempty"`
	TotalTokenCount            int                             `json:"totalTokenCount"`
	PromptTokensDetails        []GeminiPromptTokensDetails     `json:"promptTokensDetails,omitempty"`
	ToolUsePromptTokensDetails []GeminiPromptTokensDetails     `json:"toolUsePromptTokensDetails,omitempty"`
	CandidatesTokensDetails    []GeminiCandidatesTokensDetails `json:"candidatesTokensDetails,omitempty"`
}

// GeminiPromptTokensDetails breaks down prompt tokens.
type GeminiPromptTokensDetails struct {
	Modality   string `json:"modality"`
	TokenCount int    `json:"tokenCount"`
}

// GeminiCandidatesTokensDetails breaks down output tokens.
type GeminiCandidatesTokensDetails struct {
	Modality   string `json:"modality"`
	TokenCount int    `json:"tokenCount"`
}

// GeminiError is the Gemini error envelope.
type GeminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// GeminiGroundingMetadata carries grounding/citation metadata.
type GeminiGroundingMetadata struct {
	GroundingChunks   []any    `json:"groundingChunks,omitempty"`
	GroundingSupports []any    `json:"groundingSupports,omitempty"`
	WebSearchQueries  []string `json:"webSearchQueries,omitempty"`
}

// GeminiEmbeddingRequest is the Gemini embedding request.
type GeminiEmbeddingRequest struct {
	Model                string `json:"model"`
	Content              any    `json:"content"`
	TaskType             string `json:"taskType,omitempty"`
	Title                string `json:"title,omitempty"`
	OutputDimensionality *int   `json:"outputDimensionality,omitempty"`
}

// GeminiEmbeddingResponse is the Gemini embedding response.
type GeminiEmbeddingResponse struct {
	Embeddings []ContentEmbedding `json:"embeddings,omitempty"`
}

// ContentEmbedding is an embedding vector.
type ContentEmbedding struct {
	Values []float64 `json:"values"`
}

// GeminiImageRequest is the Imagen request.
type GeminiImageRequest struct {
	Instances  []GeminiImageInstance  `json:"instances"`
	Parameters *GeminiImageParameters `json:"parameters,omitempty"`
}

// GeminiImageInstance is an Imagen prompt instance.
type GeminiImageInstance struct {
	Prompt string `json:"prompt"`
}

// GeminiImageParameters are Imagen parameters.
type GeminiImageParameters struct {
	SampleCount      int    `json:"sampleCount,omitempty"`
	AspectRatio      string `json:"aspectRatio,omitempty"`
	PersonGeneration string `json:"personGeneration,omitempty"`
}
