package billing

import (
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

// ReferenceUsageContext selects the provider usage semantic for one
// settlement. ForceAudio is used by explicit audio and Realtime endpoints;
// compatible HTTP endpoints automatically select audio pricing only when the
// captured model policy has an audio ratio and audio tokens were reported.
type ReferenceUsageContext struct {
	IsClaude   bool
	ForceAudio bool
	Realtime   bool
}

// ReferenceUsageSettlement is the complete, immutable result of applying one
// request-time pricing snapshot to authoritative provider usage.
type ReferenceUsageSettlement struct {
	Quota  int
	Clamp  *quotamath.QuotaClamp
	fields map[string]any
}

// BillingLogFields returns a detached copy of the usage-class metadata that
// produced this settlement. The caller merges it with the plan and funding
// snapshots in the final consume log.
func (result ReferenceUsageSettlement) BillingLogFields() map[string]any {
	fields := make(map[string]any, len(result.fields))
	for key, value := range result.fields {
		fields[key] = value
	}
	return fields
}

type referenceUsageSummary struct {
	promptTokens            int64
	completionTokens        int64
	cacheReadTokens         int64
	cacheCreationTokens     int64
	cacheCreationFiveMinute int64
	cacheCreationOneHour    int64
	imageInputTokens        int64
	textInputTokens         int64
	audioInputTokens        int64
	textOutputTokens        int64
	audioOutputTokens       int64
}

// SettlementUsageQuota applies cache, cache-creation, image, and audio token
// classes without changing the established fixed-price calculation. All
// arithmetic stays decimal until the final checked, rounded quota conversion.
func (plan ReferenceBillingPlan) SettlementUsageQuota(usage *protocolkit.Usage, context ReferenceUsageContext) (ReferenceUsageSettlement, error) {
	normalized, err := normalizedReferenceUsage(usage)
	if err != nil {
		return ReferenceUsageSettlement{}, err
	}
	summary, err := summarizeReferenceUsage(normalized)
	if err != nil {
		return ReferenceUsageSettlement{}, err
	}
	audioPricing := context.ForceAudio || ((summary.audioInputTokens > 0 || summary.audioOutputTokens > 0) &&
		(plan.usageRatioPolicy.AudioRatioConfigured || plan.usageRatioPolicy.AudioCompletionConfigured))
	result := ReferenceUsageSettlement{
		fields: referenceUsageLogFields(plan, summary, context, audioPricing),
	}
	if summary.promptTokens == 0 && summary.completionTokens == 0 {
		return result, nil
	}

	groupRatio := decimal.NewFromFloat(plan.groupRatio)
	var quota decimal.Decimal
	if plan.useFixedPrice {
		quota = decimal.NewFromFloat(plan.fixedPrice).
			Mul(decimal.NewFromInt(quotamath.QuotaPerUnit)).
			Mul(groupRatio)
	} else {
		effectiveRatio := decimal.NewFromFloat(plan.modelRatio).Mul(groupRatio)
		if audioPricing {
			quota = calculateReferenceAudioEquivalent(plan, summary).Mul(effectiveRatio)
		} else {
			quota = calculateReferenceTextEquivalent(plan, summary, context.IsClaude).Mul(effectiveRatio)
		}
		if !effectiveRatio.IsZero() && quota.LessThanOrEqual(decimal.Zero) {
			quota = decimal.NewFromInt(1)
		}
	}

	value, err := strictReferenceQuota(quota, true)
	if err != nil {
		var clamp *quotamath.QuotaClamp
		if errors.As(err, &clamp) {
			result.Clamp = clamp
		}
		return result, err
	}
	if !plan.useFixedPrice && plan.modelRatio != 0 && plan.groupRatio != 0 && value == 0 {
		value = 1
	}
	result.Quota = value
	return result, nil
}

func calculateReferenceTextEquivalent(plan ReferenceBillingPlan, summary referenceUsageSummary, isClaude bool) decimal.Decimal {
	base := summary.promptTokens
	base = subtractReferenceUsageBucket(base, summary.cacheReadTokens)
	base = subtractReferenceUsageBucket(base, summary.cacheCreationTokens)
	base = subtractReferenceUsageBucket(base, summary.imageInputTokens)

	prompt := decimal.NewFromInt(base).
		Add(decimal.NewFromInt(summary.cacheReadTokens).Mul(decimal.NewFromFloat(plan.usageRatioPolicy.CacheRatio))).
		Add(decimal.NewFromInt(summary.imageInputTokens).Mul(decimal.NewFromFloat(plan.usageRatioPolicy.ImageRatio)))
	if isClaude {
		prompt = prompt.
			Add(decimal.NewFromInt(summary.cacheCreationFiveMinute).Mul(decimal.NewFromFloat(plan.usageRatioPolicy.CacheCreationFiveMinuteRatio))).
			Add(decimal.NewFromInt(summary.cacheCreationOneHour).Mul(decimal.NewFromFloat(plan.usageRatioPolicy.CacheCreationOneHourRatio)))
	} else {
		prompt = prompt.Add(decimal.NewFromInt(summary.cacheCreationTokens).
			Mul(decimal.NewFromFloat(plan.usageRatioPolicy.CacheCreationRatio)))
	}
	return prompt.Add(decimal.NewFromInt(summary.completionTokens).
		Mul(decimal.NewFromFloat(plan.completionRatio)))
}

func calculateReferenceAudioEquivalent(plan ReferenceBillingPlan, summary referenceUsageSummary) decimal.Decimal {
	return decimal.NewFromInt(summary.textInputTokens).
		Add(decimal.NewFromInt(summary.textOutputTokens).Mul(decimal.NewFromFloat(plan.completionRatio))).
		Add(decimal.NewFromInt(summary.audioInputTokens).Mul(decimal.NewFromFloat(plan.usageRatioPolicy.AudioRatio))).
		Add(decimal.NewFromInt(summary.audioOutputTokens).
			Mul(decimal.NewFromFloat(plan.usageRatioPolicy.AudioRatio)).
			Mul(decimal.NewFromFloat(plan.usageRatioPolicy.AudioCompletionRatio)))
}

func subtractReferenceUsageBucket(total, bucket int64) int64 {
	if total <= 0 || bucket >= total {
		return 0
	}
	if bucket <= 0 {
		return total
	}
	return total - bucket
}

func normalizedReferenceUsage(usage *protocolkit.Usage) (*protocolkit.Usage, error) {
	if usage == nil {
		return &protocolkit.Usage{}, nil
	}
	copyOf := *usage
	copyOf.PromptTokensDetails = cloneReferenceInputDetails(usage.PromptTokensDetails)
	copyOf.InputTokensDetails = cloneReferenceInputDetails(usage.InputTokensDetails)
	copyOf.CompletionTokensDetails = cloneReferenceOutputDetails(usage.CompletionTokensDetails)
	copyOf.OutputTokensDetails = cloneReferenceOutputDetails(usage.OutputTokensDetails)
	if err := validateReferenceUsage(&copyOf); err != nil {
		return nil, err
	}
	protocolkit.NormalizeOpenAIUsageAliases(&copyOf)
	if err := validateReferenceUsage(&copyOf); err != nil {
		return nil, err
	}
	return &copyOf, nil
}

func cloneReferenceInputDetails(details *protocolkit.InputTokenDetails) *protocolkit.InputTokenDetails {
	if details == nil {
		return nil
	}
	copyOf := *details
	return &copyOf
}

func cloneReferenceOutputDetails(details *protocolkit.OutputTokenDetails) *protocolkit.OutputTokenDetails {
	if details == nil {
		return nil
	}
	copyOf := *details
	return &copyOf
}

func validateReferenceUsage(usage *protocolkit.Usage) error {
	if usage == nil {
		return nil
	}
	values := map[string]int{
		"prompt tokens": usage.PromptTokens, "completion tokens": usage.CompletionTokens,
		"total tokens": usage.TotalTokens, "input tokens": usage.InputTokens,
		"output tokens": usage.OutputTokens, "cache read tokens": usage.PromptCacheHitTokens,
		"cache miss tokens": usage.PromptCacheMissTokens, "cache write tokens": usage.PromptCacheWriteTokens,
		"cache creation tokens":             usage.PromptCacheCreationTokens,
		"cache creation five-minute tokens": usage.PromptCacheCreation5mTokens,
		"cache creation one-hour tokens":    usage.PromptCacheCreation1hTokens,
		"audio tokens":                      usage.AudioTokens, "reasoning tokens": usage.ReasoningTokens,
	}
	appendInput := func(prefix string, details *protocolkit.InputTokenDetails) {
		if details == nil {
			return
		}
		values[prefix+" cached tokens"] = details.CachedTokens
		values[prefix+" cached creation tokens"] = details.CachedCreationTokens
		values[prefix+" cache write tokens"] = details.CacheWriteTokens
		values[prefix+" cache creation five-minute tokens"] = details.CacheCreation5mTokens
		values[prefix+" cache creation one-hour tokens"] = details.CacheCreation1hTokens
		values[prefix+" text tokens"] = details.TextTokens
		values[prefix+" audio tokens"] = details.AudioTokens
		values[prefix+" image tokens"] = details.ImageTokens
		values[prefix+" reasoning tokens"] = details.ReasoningTokens
	}
	appendOutput := func(prefix string, details *protocolkit.OutputTokenDetails) {
		if details == nil {
			return
		}
		values[prefix+" text tokens"] = details.TextTokens
		values[prefix+" audio tokens"] = details.AudioTokens
		values[prefix+" image tokens"] = details.ImageTokens
		values[prefix+" reasoning tokens"] = details.ReasoningTokens
	}
	appendInput("prompt", usage.PromptTokensDetails)
	appendInput("input", usage.InputTokensDetails)
	appendOutput("completion", usage.CompletionTokensDetails)
	appendOutput("output", usage.OutputTokensDetails)
	for name, value := range values {
		if err := validateReferenceTokenCount(name, value); err != nil {
			return err
		}
	}
	return nil
}

func summarizeReferenceUsage(usage *protocolkit.Usage) (referenceUsageSummary, error) {
	if usage == nil {
		return referenceUsageSummary{}, nil
	}
	summary := referenceUsageSummary{
		promptTokens:     int64(usage.PromptTokens),
		completionTokens: int64(usage.CompletionTokens),
		cacheReadTokens:  int64(usage.PromptCacheHitTokens),
	}
	cacheCreation := maxReferenceUsageCount(
		usage.PromptCacheMissTokens, usage.PromptCacheCreationTokens, usage.PromptCacheWriteTokens,
	)
	fiveMinute := usage.PromptCacheCreation5mTokens
	oneHour := usage.PromptCacheCreation1hTokens
	if details := usage.PromptTokensDetails; details != nil {
		if details.CachedTokens > 0 {
			summary.cacheReadTokens = int64(details.CachedTokens)
		}
		detailCreation := maxReferenceUsageCount(details.CachedCreationTokens, details.CacheWriteTokens)
		if detailCreation > 0 {
			cacheCreation = detailCreation
		}
		if details.CacheCreation5mTokens > 0 {
			fiveMinute = details.CacheCreation5mTokens
		}
		if details.CacheCreation1hTokens > 0 {
			oneHour = details.CacheCreation1hTokens
		}
		summary.imageInputTokens = int64(details.ImageTokens)
		summary.audioInputTokens = int64(details.AudioTokens)
		summary.textInputTokens = referenceTextRemainder(usage.PromptTokens, details.TextTokens, details.AudioTokens)
	} else {
		summary.textInputTokens = int64(usage.PromptTokens)
	}
	if details := usage.CompletionTokensDetails; details != nil {
		summary.audioOutputTokens = int64(details.AudioTokens)
		summary.textOutputTokens = referenceTextRemainder(usage.CompletionTokens, details.TextTokens, details.AudioTokens)
	} else {
		summary.textOutputTokens = int64(usage.CompletionTokens)
	}

	split, err := addReferenceUsageCounts(int64(fiveMinute), int64(oneHour))
	if err != nil {
		return referenceUsageSummary{}, err
	}
	total := int64(cacheCreation)
	if split < total {
		fiveMinute = int(int64(fiveMinute) + total - split)
		split = total
	} else if split > total {
		total = split
	}
	summary.cacheCreationTokens = total
	summary.cacheCreationFiveMinute = int64(fiveMinute)
	summary.cacheCreationOneHour = int64(oneHour)
	return summary, nil
}

func maxReferenceUsageCount(values ...int) int {
	maximum := 0
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func addReferenceUsageCounts(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > quotamath.MaxQuota || right > quotamath.MaxQuota || left > quotamath.MaxQuota-right {
		return 0, fmt.Errorf("%w: cache creation token buckets overflow", ErrReferencePricingConfiguration)
	}
	return left + right, nil
}

func referenceTextRemainder(parent, explicitText, audio int) int64 {
	if explicitText > 0 {
		return int64(explicitText)
	}
	remaining := int64(parent) - int64(audio)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func referenceUsageLogFields(plan ReferenceBillingPlan, summary referenceUsageSummary, context ReferenceUsageContext, audioPricing bool) map[string]any {
	fields := make(map[string]any)
	if audioPricing {
		if context.Realtime {
			fields["ws"] = true
		} else {
			fields["audio"] = true
		}
		fields["audio_input"] = int(summary.audioInputTokens)
		fields["audio_output"] = int(summary.audioOutputTokens)
		fields["text_input"] = int(summary.textInputTokens)
		fields["text_output"] = int(summary.textOutputTokens)
		fields["audio_ratio"] = plan.usageRatioPolicy.AudioRatio
		fields["audio_completion_ratio"] = plan.usageRatioPolicy.AudioCompletionRatio
		return fields
	}

	fields["cache_tokens"] = int(summary.cacheReadTokens)
	fields["cache_ratio"] = plan.usageRatioPolicy.CacheRatio
	if context.IsClaude {
		fields["claude"] = true
		fields["usage_semantic"] = "anthropic"
	}
	if summary.imageInputTokens != 0 {
		fields["image"] = true
		fields["image_ratio"] = plan.usageRatioPolicy.ImageRatio
		fields["image_output"] = int(summary.imageInputTokens)
	}
	if summary.cacheCreationTokens > 0 {
		fields["cache_creation_tokens"] = int(summary.cacheCreationTokens)
		fields["cache_creation_ratio"] = plan.usageRatioPolicy.CacheCreationRatio
		fields["cache_write_tokens"] = int(summary.cacheCreationTokens)
	}
	if summary.cacheCreationFiveMinute > 0 {
		fields["cache_creation_tokens_5m"] = int(summary.cacheCreationFiveMinute)
		fields["cache_creation_ratio_5m"] = plan.usageRatioPolicy.CacheCreationFiveMinuteRatio
	}
	if summary.cacheCreationOneHour > 0 {
		fields["cache_creation_tokens_1h"] = int(summary.cacheCreationOneHour)
		fields["cache_creation_ratio_1h"] = plan.usageRatioPolicy.CacheCreationOneHourRatio
	}
	return fields
}

// FreeModel reports whether the captured policy deliberately skipped a hold
// for a zero-price/zero-ratio model. Settlement remains authoritative.
func (plan ReferenceBillingPlan) FreeModel() bool {
	return plan.freeModel
}
