package engine

import (
	"fmt"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

func validateUsageForBilling(usage *protocolkit.Usage) error {
	if usage == nil {
		return nil
	}
	values := map[string]int{
		"prompt_tokens":                   usage.PromptTokens,
		"completion_tokens":               usage.CompletionTokens,
		"total_tokens":                    usage.TotalTokens,
		"input_tokens":                    usage.InputTokens,
		"output_tokens":                   usage.OutputTokens,
		"prompt_cache_hit_tokens":         usage.PromptCacheHitTokens,
		"prompt_cache_miss_tokens":        usage.PromptCacheMissTokens,
		"prompt_cache_write_tokens":       usage.PromptCacheWriteTokens,
		"prompt_cache_creation_tokens":    usage.PromptCacheCreationTokens,
		"prompt_cache_creation_5m_tokens": usage.PromptCacheCreation5mTokens,
		"prompt_cache_creation_1h_tokens": usage.PromptCacheCreation1hTokens,
		"audio_tokens":                    usage.AudioTokens,
		"reasoning_tokens":                usage.ReasoningTokens,
	}
	if usage.PromptTokensDetails != nil {
		values["prompt_details.cached_tokens"] = usage.PromptTokensDetails.CachedTokens
		values["prompt_details.cached_creation_tokens"] = usage.PromptTokensDetails.CachedCreationTokens
		values["prompt_details.cache_write_tokens"] = usage.PromptTokensDetails.CacheWriteTokens
		values["prompt_details.cache_creation_5m_tokens"] = usage.PromptTokensDetails.CacheCreation5mTokens
		values["prompt_details.cache_creation_1h_tokens"] = usage.PromptTokensDetails.CacheCreation1hTokens
		values["prompt_details.text_tokens"] = usage.PromptTokensDetails.TextTokens
		values["prompt_details.audio_tokens"] = usage.PromptTokensDetails.AudioTokens
		values["prompt_details.image_tokens"] = usage.PromptTokensDetails.ImageTokens
		values["prompt_details.reasoning_tokens"] = usage.PromptTokensDetails.ReasoningTokens
	}
	if usage.CompletionTokensDetails != nil {
		values["completion_details.text_tokens"] = usage.CompletionTokensDetails.TextTokens
		values["completion_details.audio_tokens"] = usage.CompletionTokensDetails.AudioTokens
		values["completion_details.image_tokens"] = usage.CompletionTokensDetails.ImageTokens
		values["completion_details.reasoning_tokens"] = usage.CompletionTokensDetails.ReasoningTokens
	}
	if usage.InputTokensDetails != nil {
		values["input_details.cached_tokens"] = usage.InputTokensDetails.CachedTokens
		values["input_details.cached_creation_tokens"] = usage.InputTokensDetails.CachedCreationTokens
		values["input_details.cache_write_tokens"] = usage.InputTokensDetails.CacheWriteTokens
		values["input_details.cache_creation_5m_tokens"] = usage.InputTokensDetails.CacheCreation5mTokens
		values["input_details.cache_creation_1h_tokens"] = usage.InputTokensDetails.CacheCreation1hTokens
		values["input_details.text_tokens"] = usage.InputTokensDetails.TextTokens
		values["input_details.audio_tokens"] = usage.InputTokensDetails.AudioTokens
		values["input_details.image_tokens"] = usage.InputTokensDetails.ImageTokens
		values["input_details.reasoning_tokens"] = usage.InputTokensDetails.ReasoningTokens
	}
	if usage.OutputTokensDetails != nil {
		values["output_details.text_tokens"] = usage.OutputTokensDetails.TextTokens
		values["output_details.audio_tokens"] = usage.OutputTokensDetails.AudioTokens
		values["output_details.image_tokens"] = usage.OutputTokensDetails.ImageTokens
		values["output_details.reasoning_tokens"] = usage.OutputTokensDetails.ReasoningTokens
	}
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
		if int64(value) > quotamath.MaxQuota {
			return fmt.Errorf("%s is outside the supported range", name)
		}
	}
	return nil
}

func buildLogOther(c *gin.Context, info *RelayInfo, reservation *billingsvc.RelayQuotaReservation) map[string]any {
	other := map[string]any{}
	if info != nil {
		if plan, referencePriced := info.ReferencePricing[info.Group]; referencePriced {
			for key, value := range plan.BillingLogFields() {
				other[key] = value
			}
		} else {
			ratio, special := billingsvc.EffectiveGroupRatio(info.UserGroup, info.Group)
			other["group_ratio"] = ratio
			if special {
				other["user_group_ratio"] = ratio
			}
		}
		for key, value := range info.UsageBillingFields {
			other[key] = value
		}
		for key, value := range info.ToolBillingFields {
			other[key] = value
		}
	}
	if info.QuotaClamp != nil {
		other["quota_saturation"] = info.QuotaClamp
	}
	if info.ToolQuotaClamp != nil {
		other["tool_quota_saturation"] = info.ToolQuotaClamp
	}
	for k, v := range reservation.BillingLogFields() {
		other[k] = v
	}
	channelssvc.AppendChannelAffinityAdminInfo(c, other)
	return other
}
