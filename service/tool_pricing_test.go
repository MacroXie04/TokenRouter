package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

func toolPriceSnapshotForTest(t *testing.T, raw string) setting.ToolPriceSnapshot {
	t.Helper()
	snapshot, err := setting.ParseToolPriceSnapshot(raw)
	require.NoError(t, err)
	return snapshot
}

func toolCounterForTest(
	t *testing.T,
	snapshot setting.ToolPriceSnapshot,
	mode ToolBillingMode,
	provider ToolBillingProvider,
	model string,
	declared ...string,
) *ToolUsageCounter {
	t.Helper()
	counter, err := NewToolUsageCounter(snapshot, ToolPricingContext{
		Mode: mode, Provider: provider, ModelName: model, DeclaredBuiltInTools: declared,
	})
	require.NoError(t, err)
	return counter
}

func integerPointer(value int) *int { return &value }

func TestToolPricingContextAndModeAreExplicit(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{}`)
	invalid := []ToolPricingContext{
		{Mode: "unknown", Provider: ToolBillingProviderOpenAI},
		{Mode: ToolBillingModeResponses, Provider: ""},
		{Mode: ToolBillingModeResponses, Provider: ToolBillingProviderOpenAI, ModelName: "bad\nmodel"},
		{Mode: ToolBillingModeResponses, Provider: ToolBillingProviderOpenAI,
			DeclaredBuiltInTools: []string{" bad"}},
	}
	for _, context := range invalid {
		_, err := NewToolUsageCounter(snapshot, context)
		assert.Error(t, err)
	}

	counter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5")
	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "c", Name: "fn"})
	assert.ErrorIs(t, err, ErrToolPricingMode)
	assert.ErrorIs(t, counter.MarkGeminiGoogleSearch(), ErrToolPricingMode)
	assert.ErrorIs(t, counter.MarkAlphaSearchCompleted(), ErrToolPricingMode)
}

func TestResponsesWebSearchUsesDeclaredBuiltInVariantWithoutBillingDeclarations(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{}`)
	tests := []struct {
		name     string
		declared []string
		wantTool string
	}{
		{name: "web search only", declared: []string{setting.ToolWebSearch}, wantTool: setting.ToolWebSearch},
		{name: "preview only", declared: []string{setting.ToolWebSearchPreview}, wantTool: setting.ToolWebSearchPreview},
		{name: "preview wins when both declared", declared: []string{setting.ToolWebSearch, setting.ToolWebSearchPreview}, wantTool: setting.ToolWebSearchPreview},
		{name: "preview fallback", wantTool: setting.ToolWebSearchPreview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			counter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5", test.declared...)
			empty, err := counter.Settle(1)
			require.NoError(t, err)
			assert.Empty(t, empty.Items(), "declarations alone are not calls")

			counter = toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5", test.declared...)
			counted, err := counter.ObserveResponsesOutput(ResponsesToolOutput{
				Type: ResponsesToolCallWebSearch, ID: "ws-1", OutputIndex: integerPointer(0),
			})
			require.NoError(t, err)
			assert.True(t, counted)
			settlement, err := counter.Settle(1)
			require.NoError(t, err)
			require.Len(t, settlement.Items(), 1)
			assert.Equal(t, test.wantTool, settlement.Items()[0].Name)
			assert.Equal(t, 10.0, settlement.Items()[0].Price)
		})
	}
}

func TestExplicitZeroBuiltInIsObservedButNeverBilled(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"file_search":0}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5")
	counted, err := counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFileSearch, ID: "file-1", OutputIndex: integerPointer(0),
	})
	require.NoError(t, err)
	assert.True(t, counted, "built-in execution remains observable even when its terminal price is zero")

	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	assert.Empty(t, settlement.Items())
	assert.Empty(t, settlement.AuditFields())
}

func TestResponsesToolCallsDeduplicateReplayAndSeparateCustomFromBuiltIn(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":5,"free_fn":0}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "model")

	counted, err := counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFunction, ID: "item-1", CallID: "call-1", OutputIndex: integerPointer(0), Name: "lookup",
	})
	require.NoError(t, err)
	assert.True(t, counted)

	// A replay with the same stable ID but a different index remains one call;
	// the new alias is attached so a later replay by index is also deduplicated.
	counted, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFunction, ID: "item-1", CallID: "call-1", OutputIndex: integerPointer(9), Name: "lookup",
	})
	require.NoError(t, err)
	assert.False(t, counted)
	counted, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFunction, ID: "item-new-alias", OutputIndex: integerPointer(9), Name: "lookup",
	})
	require.NoError(t, err)
	assert.False(t, counted)

	counted, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFunction, ID: "free", OutputIndex: integerPointer(10), Name: "free_fn",
	})
	require.NoError(t, err)
	assert.False(t, counted)
	counted, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFunction, ID: "reserved", OutputIndex: integerPointer(11), Name: setting.ToolWebSearch,
	})
	require.NoError(t, err)
	assert.False(t, counted, "a custom function named like a built-in is never billed as that built-in")

	_, err = counter.ObserveResponsesOutput(ResponsesToolOutput{Type: ResponsesToolCallFileSearch})
	assert.ErrorIs(t, err, ErrAmbiguousToolIdentity)
	_, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFunction, ID: "item-1", Name: "different_name",
	})
	assert.ErrorIs(t, err, ErrAmbiguousToolIdentity)

	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, ToolSurchargeItem{
		Name: "lookup", Count: 1, Price: 5, PriceExact: "5", Rule: "lookup",
		PriceSource: setting.ToolPriceSourceOperator, ModelName: "model",
		Mode: ToolBillingModeResponses, Provider: ToolBillingProviderOpenAI,
	}, withoutToolSnapshotVersion(settlement.Items()[0]))
}

func withoutToolSnapshotVersion(item ToolSurchargeItem) ToolSurchargeItem {
	item.SnapshotVersion = 0
	return item
}

func TestToolCounterKeepsRequestStartSnapshotAcrossReload(t *testing.T) {
	oldSnapshot := toolPriceSnapshotForTest(t, `{"lookup":2}`)
	newSnapshot := toolPriceSnapshotForTest(t, `{"lookup":9}`)
	counter := toolCounterForTest(t, oldSnapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")

	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{
		ChoiceIndex: 0, ToolIndex: integerPointer(0), ArrayIndex: 0, ID: "call-1", Name: "lookup",
	})
	require.NoError(t, err)
	assert.Equal(t, 9.0, newSnapshot.Resolve("lookup", "model").Price)
	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, 2.0, settlement.Items()[0].Price)
}

func TestChatStreamStableIDAndIndexFallbackDeduplicate(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":5,"time":3}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOpenAI, "model")

	observations := []struct {
		input   ChatToolCallObservation
		counted bool
	}{
		{ChatToolCallObservation{ChoiceIndex: 0, ToolIndex: integerPointer(0), ArrayIndex: 0, ID: "call-1", Name: "lookup"}, true},
		{ChatToolCallObservation{ChoiceIndex: 0, ToolIndex: integerPointer(0), ArrayIndex: 0, ID: "call-1", Name: "lookup"}, false},
		{ChatToolCallObservation{ChoiceIndex: 0, ToolIndex: nil, ArrayIndex: 2, Name: "time"}, true},
		{ChatToolCallObservation{ChoiceIndex: 0, ToolIndex: nil, ArrayIndex: 2, Name: "time"}, false},
	}
	for _, observation := range observations {
		counted, err := counter.ObserveChatToolCall(observation.input)
		require.NoError(t, err)
		assert.Equal(t, observation.counted, counted)
	}
	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{
		ChoiceIndex: 0, ToolIndex: integerPointer(0), ArrayIndex: 0, ID: "call-1", Name: "time",
	})
	assert.ErrorIs(t, err, ErrAmbiguousToolIdentity)

	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	items := settlement.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "lookup", items[0].Name)
	assert.Equal(t, "time", items[1].Name)
	assert.Equal(t, 1, items[0].Count)
	assert.Equal(t, 1, items[1].Count)
}

func TestClaudeCustomAndServerToolCountsUseIndependentPaths(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":5}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeClaudeMessages, ToolBillingProviderAnthropic, "claude-model")

	for iteration := 0; iteration < 2; iteration++ {
		counted, err := counter.ObserveClaudeToolUse(ClaudeToolUseObservation{
			BlockIndex: integerPointer(0), ID: "toolu-1", Name: "lookup",
		})
		require.NoError(t, err)
		assert.Equal(t, iteration == 0, counted)
	}
	counted, err := counter.ObserveClaudeToolUse(ClaudeToolUseObservation{
		BlockIndex: integerPointer(1), ID: "server-looking", Name: setting.ToolWebSearch,
	})
	require.NoError(t, err)
	assert.False(t, counted)
	require.NoError(t, counter.SetClaudeWebSearchRequests(3))
	require.NoError(t, counter.SetClaudeWebSearchRequests(2), "cumulative replay replaces rather than adds")

	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	items := settlement.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "lookup", items[0].Name)
	assert.Equal(t, 1, items[0].Count)
	assert.Equal(t, setting.ToolWebSearch, items[1].Name)
	assert.Equal(t, 2, items[1].Count)
}

func TestNativeMarkersAndLegacySearchPreviewSemantics(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{}`)

	gemini := toolCounterForTest(t, snapshot, ToolBillingModeGeminiNative, ToolBillingProviderGemini, "gemini-2.5")
	require.NoError(t, gemini.MarkGeminiGoogleSearch())
	require.NoError(t, gemini.MarkGeminiGoogleSearch())
	settlement, err := gemini.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, setting.ToolGoogleSearch, settlement.Items()[0].Name)
	assert.Equal(t, 1, settlement.Items()[0].Count)

	alpha := toolCounterForTest(t, snapshot, ToolBillingModeAlphaSearch, ToolBillingProviderOpenAI, "gpt-4o")
	require.NoError(t, alpha.MarkAlphaSearchCompleted())
	settlement, err = alpha.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, setting.ToolWebSearchPreview, settlement.Items()[0].Name)
	assert.Equal(t, 25.0, settlement.Items()[0].Price)

	legacy := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOpenAI, "gpt-4o-search-preview")
	settlement, err = legacy.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, setting.ToolWebSearchPreview, settlement.Items()[0].Name)
	assert.Equal(t, 25.0, settlement.Items()[0].Price)

	responses := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-4o-search-preview")
	settlement, err = responses.Settle(1)
	require.NoError(t, err)
	assert.Empty(t, settlement.Items(), "Responses never infers a call from the model suffix")
}

func TestResponsesImageGenerationDedupCapAndTerminalReset(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5")

	ignored := []ResponsesToolOutput{
		{Type: ResponsesToolCallImageGeneration, ID: "empty", Status: "completed", Result: "  "},
		{Type: ResponsesToolCallImageGeneration, ID: "failed", Status: "failed", Result: "a"},
		{Type: ResponsesToolCallImageGeneration, ID: "partial", Status: "partial", Result: "a"},
		{Type: "image_generation_call.partial_image", ID: "event", Result: "a"},
	}
	for _, output := range ignored {
		counted, err := counter.ObserveResponsesOutput(output)
		require.NoError(t, err)
		assert.False(t, counted)
	}
	first := ResponsesToolOutput{
		Type: ResponsesToolCallImageGeneration, ID: "img-1", CallID: "call-1",
		OutputIndex: integerPointer(0), Status: "completed", Result: "base64-a",
	}
	counted, err := counter.ObserveResponsesOutput(first)
	require.NoError(t, err)
	assert.True(t, counted)
	counted, err = counter.ObserveResponsesOutput(first)
	require.NoError(t, err)
	assert.False(t, counted)

	// Any matching alias deduplicates, and new aliases from the replay bridge
	// later representations of the same completed output.
	counted, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallImageGeneration, ID: "img-1", CallID: "call-2",
		OutputIndex: integerPointer(1), Status: "in_progress", Result: "base64-b",
	})
	require.NoError(t, err)
	assert.False(t, counted)
	counted, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallImageGeneration, ID: "img-2", CallID: "call-2",
		OutputIndex: integerPointer(1), Status: "completed", Result: "base64-b",
	})
	require.NoError(t, err)
	assert.False(t, counted)

	// Non-image output counts survive the failed terminal; pending images do not.
	_, err = counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFileSearch, ID: "file-1", OutputIndex: integerPointer(2),
	})
	require.NoError(t, err)
	require.NoError(t, counter.FinishResponses("incomplete"))
	_, err = counter.ObserveResponsesOutput(first)
	assert.ErrorIs(t, err, ErrToolPricingFinalized)
	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, setting.ToolFileSearch, settlement.Items()[0].Name)

	capCounter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5")
	for index := 0; index < MaxImageGenerationCalls+5; index++ {
		counted, err = capCounter.ObserveResponsesOutput(ResponsesToolOutput{
			Type: ResponsesToolCallImageGeneration, ID: fmt.Sprintf("img-%d", index),
			OutputIndex: integerPointer(index), Status: "completed", Result: fmt.Sprintf("result-%d", index),
		})
		require.NoError(t, err)
		assert.Equal(t, index < MaxImageGenerationCalls, counted)
	}
	settlement, err = capCounter.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, MaxImageGenerationCalls, settlement.Items()[0].Count)

	completedCounter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5")
	counted, err = completedCounter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallImageGeneration, ID: "completed-image",
		Status: "completed", Result: "completed-result",
	})
	require.NoError(t, err)
	assert.True(t, counted)
	require.NoError(t, completedCounter.FinishResponses("completed"))
	settlement, err = completedCounter.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, setting.ToolImageGeneration, settlement.Items()[0].Name)
	assert.Equal(t, 1, settlement.Items()[0].Count)
}

func TestToolCounterResetForRetryClearsAttemptButPreservesSnapshot(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":4}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "old", Name: "lookup"})
	require.NoError(t, err)
	require.NoError(t, counter.ResetForRetry())
	_, err = counter.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "new", Name: "lookup"})
	require.NoError(t, err)
	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, 1, settlement.Items()[0].Count)
	assert.Equal(t, 4.0, settlement.Items()[0].Price)
	assert.ErrorIs(t, counter.ResetForRetry(), ErrToolPricingFinalized)

	responses := toolCounterForTest(t, toolPriceSnapshotForTest(t, `{}`), ToolBillingModeResponses, ToolBillingProviderOpenAI, "gpt-5")
	_, err = responses.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallImageGeneration, ID: "old-image", Status: "completed", Result: "old-result",
	})
	require.NoError(t, err)
	require.NoError(t, responses.FinishResponses("failed"))
	require.NoError(t, responses.ResetForRetry(), "retry reopens a terminal attempt without changing its pricing snapshot")
	_, err = responses.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallImageGeneration, ID: "new-image", Status: "completed", Result: "new-result",
	})
	require.NoError(t, err)
	settlement, err = responses.Settle(1)
	require.NoError(t, err)
	require.Len(t, settlement.Items(), 1)
	assert.Equal(t, 1, settlement.Items()[0].Count)
}

func TestToolSettlementExactArithmeticRoundingSortingAndZeroGroup(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"z_tool":5,"a_tool":2.5,"half":0.001}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	for index, name := range []string{"z_tool", "a_tool", "z_tool"} {
		_, err := counter.ObserveChatToolCall(ChatToolCallObservation{
			ChoiceIndex: 0, ToolIndex: integerPointer(index), ArrayIndex: index, ID: fmt.Sprintf("call-%d", index), Name: name,
		})
		require.NoError(t, err)
	}
	settlement, err := counter.Settle(1.5)
	require.NoError(t, err)
	assert.Equal(t, "9375", settlement.ExactQuota)
	assert.Equal(t, 9_375, settlement.Quota)
	items := settlement.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "a_tool", items[0].Name)
	assert.Equal(t, "z_tool", items[1].Name)

	half := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err = half.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "half", Name: "half"})
	require.NoError(t, err)
	settlement, err = half.Settle(1)
	require.NoError(t, err)
	assert.Equal(t, "0.5", settlement.ExactQuota)
	assert.Equal(t, 1, settlement.Quota, "half is rounded away from zero")

	freeGroup := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err = freeGroup.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "free", Name: "z_tool"})
	require.NoError(t, err)
	settlement, err = freeGroup.Settle(0)
	require.NoError(t, err)
	assert.Zero(t, settlement.Quota)
	require.Len(t, settlement.Items(), 1, "the observed priced call remains auditable on a free group")
}

func TestToolSettlementFailsClosedOnInvalidRatioAndOverflow(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"expensive":1000000000000}`)
	for _, ratio := range []float64{-1, math.NaN(), math.Inf(1), MaxToolBillingRatio + 1} {
		counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
		_, err := counter.Settle(ratio)
		assert.Error(t, err)
	}
	counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "expensive", Name: "expensive"})
	require.NoError(t, err)
	settlement, err := counter.Settle(1)
	assert.Error(t, err)
	require.NotNil(t, settlement.Clamp)
	assert.Equal(t, "overflow", settlement.Clamp.Reason)
}

func TestToolSettlementAuditIsDetachedAndReferenceCompatible(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":5}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 0, ID: "call", Name: "lookup"})
	require.NoError(t, err)
	settlement, err := counter.Settle(1)
	require.NoError(t, err)

	items := settlement.Items()
	items[0].Name = "mutated"
	fields := settlement.AuditFields()
	auditItems := fields["tool_surcharges"].([]ToolSurchargeItem)
	auditItems[0].Count = 999
	assert.Equal(t, "lookup", settlement.Items()[0].Name)
	assert.Equal(t, 1, settlement.Items()[0].Count)
	secondFields := settlement.AuditFields()
	assert.Equal(t, 1, secondFields["tool_surcharges"].([]ToolSurchargeItem)[0].Count)

	encoded, err := json.Marshal(secondFields)
	require.NoError(t, err)
	assert.JSONEq(t, `{"tool_surcharges":[{"name":"lookup","count":1,"price":5}]}`, string(encoded))

	again, err := counter.Settle(999)
	require.NoError(t, err)
	assert.Equal(t, settlement.Quota, again.Quota, "settlement is frozen after the first successful call")
	_, err = counter.ObserveChatToolCall(ChatToolCallObservation{ChoiceIndex: 0, ArrayIndex: 1, ID: "later", Name: "lookup"})
	assert.ErrorIs(t, err, ErrToolPricingFinalized)
}

func TestToolObservationAndCountCaps(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeResponses, ToolBillingProviderOpenAI, "model")
	for index := 0; index < MaxObservedToolCalls; index++ {
		_, err := counter.ObserveResponsesOutput(ResponsesToolOutput{
			Type: ResponsesToolCallFileSearch, OutputIndex: integerPointer(index), ID: fmt.Sprintf("id-%d", index),
		})
		require.NoError(t, err)
	}
	_, err := counter.ObserveResponsesOutput(ResponsesToolOutput{
		Type: ResponsesToolCallFileSearch, OutputIndex: integerPointer(MaxObservedToolCalls), ID: "one-too-many",
	})
	assert.ErrorIs(t, err, ErrToolObservationLimit)

	count, capped := saturatedToolCount(MaxBillableToolCallCount, 1, false)
	assert.Equal(t, MaxBillableToolCallCount, count)
	assert.True(t, capped)
	count, capped = saturatedToolCount(MaxBillableToolCallCount-1, 1, false)
	assert.Equal(t, MaxBillableToolCallCount, count)
	assert.False(t, capped)
	count, capped = saturatedToolCount(MaxBillableToolCallCount-1, 2, false)
	assert.Equal(t, MaxBillableToolCallCount, count)
	assert.True(t, capped)
}

func TestToolObservationRejectsUnsafeNamesAndOversizedReportedCount(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":5}`)
	chat := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err := chat.ObserveChatToolCall(ChatToolCallObservation{
		ChoiceIndex: 0, ArrayIndex: 0, ID: "id", Name: "bad\u202ename",
	})
	assert.Error(t, err)
	_, err = chat.ObserveChatToolCall(ChatToolCallObservation{
		ChoiceIndex: 0, ArrayIndex: 0, ID: strings.Repeat("i", MaxToolObservationTextBytes+1), Name: "lookup",
	})
	assert.Error(t, err)

	claude := toolCounterForTest(t, snapshot, ToolBillingModeClaudeMessages, ToolBillingProviderAnthropic, "model")
	assert.ErrorIs(t, claude.SetClaudeWebSearchRequests(MaxBillableToolCallCount+1), ErrToolObservationLimit)
	assert.Error(t, claude.SetClaudeWebSearchRequests(-1))
}

func TestZeroPricePrefixDisablesCustomCallAtObservation(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"lookup":5,"lookup:model-pro*":0}`)
	for _, test := range []struct {
		model string
		count int
	}{
		{model: "model-basic", count: 1},
		{model: "model-pro-v2", count: 0},
	} {
		counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, test.model)
		counted, err := counter.ObserveChatToolCall(ChatToolCallObservation{
			ChoiceIndex: 0, ArrayIndex: 0, ID: "call", Name: "lookup",
		})
		require.NoError(t, err)
		assert.Equal(t, test.count == 1, counted)
		settlement, err := counter.Settle(1)
		require.NoError(t, err)
		assert.Len(t, settlement.Items(), test.count)
	}
}

func TestToolQuotaUsesImmutableAccountingUnit(t *testing.T) {
	snapshot := toolPriceSnapshotForTest(t, `{"one_dollar_per_thousand":1}`)
	counter := toolCounterForTest(t, snapshot, ToolBillingModeChatCompletions, ToolBillingProviderOther, "model")
	_, err := counter.ObserveChatToolCall(ChatToolCallObservation{
		ChoiceIndex: 0, ArrayIndex: 0, ID: "call", Name: "one_dollar_per_thousand",
	})
	require.NoError(t, err)
	settlement, err := counter.Settle(1)
	require.NoError(t, err)
	assert.Equal(t, common.QuotaPerUnit/1000, settlement.Quota)
}
