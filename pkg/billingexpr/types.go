// Package billingexpr implements TokenRouter's tiered expression billing engine.
//
// A single expression string defines a model's complete billing: pricing,
// tier conditions, cache/image/audio differentiation, and request-aware
// multipliers. Expressions are compiled with expr-lang, cached, and evaluated
// against normalized token parameters. See docs/parity reference for the
// expression language semantics (variables p/c/len/cr/cc/cc1h/img/ai/ao/img_o).
package billingexpr

// Usage is the format-agnostic token usage from an upstream response.
type Usage struct {
	// PromptTokens: for GPT/OpenAI-format it is the total input (text + cache +
	// image + audio); for Claude-format it is text-only input.
	PromptTokens int
	// CompletionTokens is the total output token count.
	CompletionTokens int
	// CacheReadTokens / CacheCreationTokens / CacheCreation1hTokens are cache
	// breakdowns reported separately by Claude (and via prompt_tokens_details
	// on OpenAI).
	CacheReadTokens       int
	CacheCreationTokens   int
	CacheCreation1hTokens int
	ImageInputTokens      int
	AudioInputTokens      int
	ImageOutputTokens     int
	AudioOutputTokens     int
	// IsClaudeSemantic reports whether PromptTokens is text-only (true) or
	// total-input (false).
	IsClaudeSemantic bool
}

// TokenParams are the normalized variables passed to an expression.
type TokenParams struct {
	P    float64 // prompt, minus separately-priced subcategories (when used)
	C    float64 // completion, minus separately-priced subcategories (when used)
	Len  float64 // full input context length (never reduced)
	Cr   float64 // cache read tokens
	Cc   float64 // cache creation (5-minute TTL) tokens
	Cc1h float64 // cache creation (1-hour TTL) tokens
	Img  float64 // image input tokens
	Ai   float64 // audio input tokens
	Ao   float64 // audio output tokens
	ImgO float64 // image output tokens
}

// RequestInput supplies request-body and header probes for param()/header().
type RequestInput struct {
	Header map[string]string
	Body   map[string]any
}

// EvalResult is the output of running an expression.
type EvalResult struct {
	Cost        float64
	MatchedTier string
}

// subcategoryVars are the variables that, when referenced, pull their tokens
// out of the base p/c variables during normalization.
var subcategoryVars = []string{"cr", "cc", "cc1h", "img", "ai", "ao", "img_o"}
