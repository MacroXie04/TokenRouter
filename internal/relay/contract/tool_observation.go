package contract

import "github.com/tokenrouter/tokenrouter/protocolkit"

// ToolUsageHooks is a protocol-neutral callback carrier installed by the
// relay lifecycle and consumed by provider response parsers. Keeping only
// primitive observation DTOs here avoids a relay/service import cycle while
// preserving one request-owned, immutable pricing counter across adapters.
type ToolUsageHooks struct {
	ObserveResponsesOutput  func(ToolResponsesObservation) error
	FinishResponses         func(status string) error
	ObserveChatToolCall     func(ToolChatObservation) error
	ObserveClaudeToolUse    func(ToolClaudeObservation) error
	SetClaudeWebSearchCount func(count int) error
	MarkGeminiGoogleSearch  func() error
	MarkAlphaSearchComplete func() error
}

type ToolResponsesObservation struct {
	Type        string
	ID          string
	CallID      string
	OutputIndex *int
	Name        string
	Status      string
	Result      string
}

type ToolChatObservation struct {
	ChoiceIndex int
	ToolIndex   *int
	ArrayIndex  int
	ID          string
	Name        string
}

type ToolClaudeObservation struct {
	BlockIndex *int
	ID         string
	Name       string
}

func (meta *Meta) ToolHooks() *ToolUsageHooks {
	if meta == nil {
		return nil
	}
	return meta.ToolUsage
}

// ObserveClaudeResponse records custom tool_use blocks and the provider's
// authoritative cumulative web-search count before a response is exposed to
// the client. It is shared by native and converted Claude adapters.
func ObserveClaudeResponse(hooks *ToolUsageHooks, response *protocolkit.ClaudeResponse) error {
	if hooks == nil || response == nil {
		return nil
	}
	for index := range response.Content {
		block := &response.Content[index]
		if block.Type != "tool_use" || hooks.ObserveClaudeToolUse == nil {
			continue
		}
		blockIndex := index
		if err := hooks.ObserveClaudeToolUse(ToolClaudeObservation{
			BlockIndex: &blockIndex, ID: block.ID, Name: block.Name,
		}); err != nil {
			return err
		}
	}
	return ObserveClaudeUsage(hooks, response.Usage)
}

// ObserveClaudeUsage replaces the cumulative server-side count rather than
// adding it, making replayed terminal frames idempotent.
func ObserveClaudeUsage(hooks *ToolUsageHooks, usage *protocolkit.ClaudeUsage) error {
	if hooks == nil || hooks.SetClaudeWebSearchCount == nil || usage == nil || usage.ServerToolUse == nil {
		return nil
	}
	return hooks.SetClaudeWebSearchCount(usage.ServerToolUse.WebSearchRequests)
}

// ObserveGeminiResponse marks one Google Search use only when the provider
// emits concrete web-search queries. Grounding containers without searches
// are citations/metadata, not a billable invocation.
func ObserveGeminiResponse(hooks *ToolUsageHooks, response *protocolkit.GeminiChatResponse) error {
	if hooks == nil || hooks.MarkGeminiGoogleSearch == nil || response == nil {
		return nil
	}
	for _, candidate := range response.Candidates {
		if candidate.GroundingMetadata != nil && len(candidate.GroundingMetadata.WebSearchQueries) > 0 {
			return hooks.MarkGeminiGoogleSearch()
		}
	}
	return nil
}
