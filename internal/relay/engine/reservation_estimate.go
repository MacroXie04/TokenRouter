package engine

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
)

const maxRelayOutputTokens = int(quotamath.MaxQuota / 2)

func estimateReservationChecked(info *RelayInfo) (int, error) {
	return estimateReservationForGroupChecked(info, info.Group)
}

func estimateReservationForGroupsChecked(info *RelayInfo, groups []string) (int, error) {
	if len(groups) == 0 {
		return 0, errors.New("relay has no authorized groups")
	}
	maximum := 0
	for _, group := range groups {
		quota, err := estimateReservationForGroupChecked(info, group)
		if err != nil {
			return 0, err
		}
		if quota > maximum {
			maximum = quota
		}
	}
	return maximum, nil
}

func estimateReservationForGroupChecked(info *RelayInfo, group string) (int, error) {
	if info == nil || info.Request == nil {
		return 0, errors.New("relay request is nil")
	}
	for name, value := range map[string]*int{
		"max_tokens":            info.Request.MaxTokens,
		"max_completion_tokens": info.Request.MaxCompletionTokens,
	} {
		if value != nil && (*value < 0 || *value > maxRelayOutputTokens) {
			return 0, fmt.Errorf("%s is outside the supported range", name)
		}
	}
	if info.PromptTokens < 0 {
		return 0, errors.New("estimated prompt tokens must not be negative")
	}
	estimatedCompletion := 0
	completionLimitProvided := false
	if info.Request.MaxTokens != nil {
		estimatedCompletion = *info.Request.MaxTokens
		completionLimitProvided = true
	} else if info.Request.MaxCompletionTokens != nil {
		estimatedCompletion = *info.Request.MaxCompletionTokens
		completionLimitProvided = true
	}
	referencePlan, useReferencePricing, err := billingsvc.ResolveOrdinaryReferenceBillingPlanForUser(
		info.UserID, info.ModelName, info.UserGroup, group,
	)
	if err != nil {
		return 0, err
	}
	var baseQuota int
	if useReferencePricing {
		if info.ReferencePricing == nil {
			info.ReferencePricing = make(map[string]billingsvc.ReferenceBillingPlan)
		}
		info.ReferencePricing[group] = referencePlan
		if info.FreeModelPricing == nil {
			info.FreeModelPricing = make(map[string]bool)
		}
		info.FreeModelPricing[group] = referencePlan.FreeModel()
		quota, err := referencePlan.PreConsumeQuota(
			info.PromptTokens, estimatedCompletion, completionLimitProvided,
		)
		if err != nil {
			return 0, err
		}
		baseQuota = quota
	} else {
		delete(info.ReferencePricing, group)
		if info.FreeModelPricing == nil {
			info.FreeModelPricing = make(map[string]bool)
		}
		info.FreeModelPricing[group] = billingsvc.ShouldSkipOrdinaryFreeModelPreConsume(
			info.ModelName, info.UserGroup, group,
		)
		if !completionLimitProvided {
			estimatedCompletion = 256
		}
		quota, clamp := billingsvc.ComputeQuotaForUserChecked(
			info.ModelName, info.UserGroup, group, info.PromptTokens, estimatedCompletion,
		)
		if clamp != nil {
			return 0, clamp
		}
		if quota < 0 {
			return 0, fmt.Errorf("reservation quota must not be negative: %d", quota)
		}
		baseQuota = quota
	}
	ratio, _ := billingsvc.EffectiveGroupRatio(info.UserGroup, group)
	if info.ToolGroupRatios == nil {
		info.ToolGroupRatios = make(map[string]float64)
	}
	info.ToolGroupRatios[group] = ratio
	toolFloor, toolPotential, err := billingsvc.MinimumPotentialToolSurcharge(
		info.ToolPriceSnapshot, toolPricingContext(info, billingsvc.ToolBillingProviderOther), ratio,
	)
	if err != nil {
		return 0, err
	}
	if info.ToolPotential == nil {
		info.ToolPotential = make(map[string]bool)
	}
	info.ToolPotential[group] = toolPotential
	combined, ok := quotamath.AddQuotaWithinBounds(baseQuota, toolFloor)
	if !ok {
		return 0, errors.New("combined model and minimum tool reservation exceeds accounting bounds")
	}
	return combined, nil
}

func allRelayGroupsUseFreeModelPricing(info *RelayInfo, groups []string) bool {
	if info == nil || len(groups) == 0 || len(info.FreeModelPricing) == 0 {
		return false
	}
	for _, group := range groups {
		if !info.FreeModelPricing[group] || info.ToolPotential[group] {
			return false
		}
	}
	return true
}

func isExplicitAudioRelayMode(mode channelcatalog.RelayMode) bool {
	switch mode {
	case channelcatalog.RelayModeAudioSpeech,
		channelcatalog.RelayModeAudioTranscription,
		channelcatalog.RelayModeAudioTranslation:
		return true
	default:
		return false
	}
}
