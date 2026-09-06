package engine

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	billingexpr "github.com/tokenrouter/tokenrouter/internal/billing/expression"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	relaypolicy "github.com/tokenrouter/tokenrouter/internal/relay/policy"
	awsprovider "github.com/tokenrouter/tokenrouter/internal/relay/providers/aws"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"strings"
	"time"
)

func relayAndSettle(c *gin.Context, state relaycommon.RequestState, info *RelayInfo) error {
	return relayAndSettleWithDispatch(c, state, info, dispatchUpstream)
}

// relayAndSettleWithDispatch is the shared lifecycle; Claude-format relays
// pass their own dispatch (native Anthropic passthrough or OpenAI conversion).
func relayAndSettleWithDispatch(c *gin.Context, state relaycommon.RequestState, info *RelayInfo, dispatch func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error)) error {
	if info == nil || info.Request == nil {
		abortRelay(c, http.StatusInternalServerError, "中继请求上下文无效", "relay_context_invalid")
		return errors.New("relay request context is nil")
	}
	if !state.Allows(info.ModelName) {
		message := "令牌无权访问模型 " + info.ModelName
		if info.Format == channelcatalog.RelayFormatClaude {
			abortClaude(c, http.StatusForbidden, "permission_error", message)
		} else {
			abortRelay(c, http.StatusForbidden, message, "model_not_allowed")
		}
		return nil
	}
	policy := state
	if len(policy.Groups) == 0 {
		writeRelayAccountingFailure(c, info, http.StatusForbidden, "令牌分组不可用", "group_not_allowed")
		return nil
	}
	info.AuthorizedGroups = append([]string(nil), policy.Groups...)
	info.CrossGroupRetry = policy.CrossGroupRetry
	info.Group = info.AuthorizedGroups[0]

	start := time.Now()
	originalWriter := c.Writer
	timedWriter := &firstResponseWriter{ResponseWriter: originalWriter}
	c.Writer = timedWriter
	metricSuccess := false
	defer func() {
		now := time.Now()
		latencyMs := now.Sub(start).Milliseconds()
		ttftMs := int64(0)
		generationMs := latencyMs
		hasTtft := info.IsStream && timedWriter.at.After(start)
		if hasTtft {
			ttftMs = timedWriter.at.Sub(start).Milliseconds()
			generationMs = now.Sub(timedWriter.at).Milliseconds()
		}
		if generationMs <= 0 {
			generationMs = latencyMs
		}
		outputTokens := int64(0)
		if info.Usage != nil {
			outputTokens = int64(info.Usage.CompletionTokens)
		}
		operationssvc.RecordPerfMetricSample(operationssvc.PerfMetricSample{
			Model: info.ModelName, Group: info.Group, LatencyMs: latencyMs,
			TtftMs: ttftMs, HasTtft: hasTtft, Success: metricSuccess,
			OutputTokens: outputTokens, GenerationMs: generationMs,
		})
		c.Writer = originalWriter
	}()
	token := state.Token
	userId := state.UserID
	info.UserID = userId

	info.IsStream = info.Request.Stream
	info.ToolPriceSnapshot = setting.CaptureToolPriceSnapshot().Frozen()
	info.ToolDeclaredNames = declaredToolPricingNames(info)

	// Sensitive-word content moderation (gated on the same defaults as the
	// reference: CheckSensitiveEnabled + CheckSensitiveOnPromptEnabled).
	if relaypolicy.ShouldCheckPromptSensitive() && requestContainsSensitive(info.Request) {
		abortRelay(c, http.StatusBadRequest, "请求包含敏感内容", "invalid_request_error")
		return nil
	}

	// Prompt token estimation (for pre-consume and stream fallback).
	info.PromptTokens = relaycommon.EstimatePromptTokens(info.Request)

	// Atomically reserve both the limited token and the selected user funding
	// source before contacting an upstream. A stale in-memory token balance is
	// never used as the concurrency guard.
	reserved, err := estimateReservationForGroupsChecked(info, info.AuthorizedGroups)
	if err != nil {
		writeRelayAccountingFailure(c, info, http.StatusBadRequest, "预扣额度无效", "invalid_quota")
		return nil
	}
	reservation, err := billingsvc.NewOrdinaryRelayQuotaReservationWithFreeModel(
		userId, token, reserved, allRelayGroupsUseFreeModelPricing(info, info.AuthorizedGroups),
	)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "refund token reservation"):
			logging.SysError("ordinary relay reservation rollback failed: " + err.Error())
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "预扣费回滚失败", "pre_consume_failed")
		case errors.Is(err, billingsvc.ErrInsufficientTokenQuota):
			abortRelay(c, http.StatusBadRequest, "令牌额度不足", "insufficient_quota")
		case billingsvc.IsSubscriptionFundingErr(err):
			abortRelay(c, http.StatusBadRequest, "订阅额度不足或未配置订阅: "+err.Error(), "insufficient_quota")
		case errors.Is(err, billingsvc.ErrInsufficientQuota):
			abortRelay(c, http.StatusBadRequest, "用户额度不足", "insufficient_quota")
		default:
			logging.SysError("ordinary relay reservation failed: " + err.Error())
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "预扣费失败", "pre_consume_failed")
		}
		return nil
	}

	// Non-stream responses are held briefly so a failed accounting transaction
	// can replace an upstream success with a coherent error. The writer spills
	// responses above a fixed bound, preserving large/binary endpoint behavior.
	var deferredWriter *deferredResponseWriter
	if !info.IsStream {
		deferredWriter = newDeferredResponseWriter(originalWriter)
		timedWriter.ResponseWriter = deferredWriter
	}
	discardDeferred := func() {
		if deferredWriter != nil && deferredWriter.Discard() {
			timedWriter.ResponseWriter = originalWriter
		}
	}

	reliabilityPolicy := setting.GetChannelReliabilitySetting()
	retryTimes := reliabilityPolicy.RetryTimes
	ignore := make(map[int]struct{}, retryTimes+1)
	initialChannelID := 0
	initialChannelGroup := ""
	pinnedGroup := ""
	var lastErr error
	upstreamUsageObserved := false
	reservationDispatched := false
	for attempt := 0; attempt <= retryTimes; attempt++ {
		candidateGroups := info.AuthorizedGroups
		if pinnedGroup != "" && !info.CrossGroupRetry {
			candidateGroups = []string{pinnedGroup}
		}
		channel, selectedGroup, usedAffinity, affinityFound, err := selectAuthorizedRelayChannel(c, info, candidateGroups, ignore)
		if err != nil {
			lastErr = err
			break
		}
		if pinnedGroup == "" {
			pinnedGroup = selectedGroup
		}
		info.Group = selectedGroup
		state.SelectGroup(selectedGroup)
		if attempt == 0 && affinityFound && !usedAffinity && !channelssvc.ShouldKeepChannelAffinityOnChannelDisabled() {
			channelssvc.ClearCurrentChannelAffinityCache(c)
		}
		if initialChannelID == 0 {
			initialChannelID = channel.Id
			initialChannelGroup = selectedGroup
		}
		if usedAffinity {
			channelssvc.MarkChannelAffinityUsed(c, info.Group, channel.Id)
		}
		info.Channel = channel
		if err := prepareToolPricingAttempt(info); err != nil {
			lastErr = fmt.Errorf("prepare tool pricing: %w", err)
			break
		}
		if !reservationDispatched {
			// Persist the at-risk transition immediately before the first network
			// dispatch. A crash after this point must never make the recovery sweep
			// treat possibly accepted upstream work as an unused hold.
			if dispatchErr := reservation.MarkDispatched(); dispatchErr != nil {
				refundErr := reservation.Refund()
				discardDeferred()
				writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "无法持久化上游派发状态", "pre_consume_failed")
				return errors.Join(
					fmt.Errorf("mark relay quota reservation dispatched: %w", dispatchErr),
					wrapRelayError("refund undispatched relay quota reservation", refundErr),
				)
			}
			reservationDispatched = true
		}

		usage, rerr := dispatch(c, info)
		if rerr == nil && timedWriter.writeErr != nil {
			rerr = fmt.Errorf("write relay response: %w", timedWriter.writeErr)
		}
		if rerr != nil {
			if channel.Type == int(channelcatalog.ChannelTypeXai) {
				rerr = relaycommon.NormalizeGrokViolationError(rerr)
			}
			if usage != nil {
				info.Usage = usage
				upstreamUsageObserved = true
			}
			lastErr = rerr
			ignore[channel.Id] = struct{}{}
			// Retrying after any bytes or headers reached the client would splice
			// multiple upstream responses together. A partial response is settled
			// below; a fully deferred non-stream failure can still be retried.
			if originalWriter.Written() || upstreamUsageObserved {
				break
			}
			if deferredWriter != nil {
				deferredWriter.Reset()
			}
			affinityFailure := channelssvc.ShouldSkipRetryAfterChannelAffinityFailure(c)
			if !channelssvc.ShouldRetryChannelFailure(reliabilityPolicy, channelssvc.RetryPolicyInput{
				Failure: relayChannelFailure(rerr), AffinityFailure: affinityFailure,
				RemainingRetries: retryTimes - attempt,
			}) {
				break
			}
			continue
		}
		info.Usage = usage
		if info.Mode == channelcatalog.RelayModeAlphaSearch && info.ToolUsageHooks != nil &&
			info.ToolUsageHooks.MarkAlphaSearchComplete != nil {
			if observeErr := info.ToolUsageHooks.MarkAlphaSearchComplete(); observeErr != nil {
				lastErr = fmt.Errorf("observe alpha search completion: %w", observeErr)
				break
			}
		}
		lastErr = nil
		metricSuccess = true
		affinityInitialChannelID := initialChannelID
		if initialChannelGroup != info.Group {
			// The final candidate has a group-scoped affinity key. Never write a
			// failed channel from an earlier group into that group's cache.
			affinityInitialChannelID = info.Channel.Id
		}
		channelssvc.RecordChannelAffinity(c, affinityInitialChannelID, info.Channel.Id)
		break
	}

	responseCommitted := originalWriter.Written()
	if lastErr != nil && !responseCommitted && !upstreamUsageObserved {
		// Refund the reservation: no successful upstream response was produced.
		if refundErr := reservation.Refund(); refundErr != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "预扣额度退款失败", "refund_failed")
			return fmt.Errorf("refund relay quota after upstream failure: %w", errors.Join(lastErr, refundErr))
		}
		if relaycommon.IsGrokViolationError(lastErr) {
			ratio, _ := billingsvc.EffectiveGroupRatio(info.UserGroup, info.Group)
			statusCode := http.StatusBadRequest
			var upstream *relaycommon.UpstreamError
			if errors.As(lastErr, &upstream) && upstream != nil && upstream.StatusCode > 0 {
				statusCode = upstream.StatusCode
			}
			useTime := int(time.Since(start) / time.Second)
			if int64(useTime) > quotamath.MaxQuota {
				useTime = int(quotamath.MaxQuota)
			}
			if _, feeErr := billingsvc.ChargeGrokViolationFee(billingsvc.GrokViolationFeeInput{
				ReservationID: reservation.ReservationID(),
				ChannelID:     info.Channel.Id,
				ModelName:     info.ModelName,
				Group:         info.Group,
				RequestID:     state.RequestID,
				StatusCode:    statusCode,
				UseTime:       useTime,
				IsStream:      info.IsStream,
				GroupRatio:    ratio,
			}); feeErr != nil {
				discardDeferred()
				writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "违规费用记账失败", "violation_fee_accounting_failed")
				return fmt.Errorf("charge post-refund violation fee: %w", errors.Join(lastErr, feeErr))
			}
		}
		discardDeferred()
		relaycommon.WriteUpstreamError(c, lastErr)
		return nil
	}
	usageReported := info.Usage != nil
	if info.Usage == nil {
		info.Usage = &protocolkit.Usage{}
	}
	protocolkit.NormalizeOpenAIUsageAliases(info.Usage)
	if err := validateUsageForBilling(info.Usage); err != nil {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "上游用量无效，无法结算", "settlement_failed")
		return fmt.Errorf("invalid upstream usage: %w", err)
	}
	channelssvc.ObserveChannelAffinityUsage(c, info.Usage, info.Format)

	// Settle actual quota (tiered expression billing when configured).
	isClaude := info.Usage.BillingSemantic == "anthropic" || info.Channel != nil &&
		(info.Channel.Type == int(channelcatalog.ChannelTypeAnthropic) ||
			(info.Channel.Type == int(channelcatalog.ChannelTypeAws) &&
				!awsprovider.IsNovaModel(relaycommon.GetMappedModel(info.Channel, info.ModelName))))
	reqInput := billingexpr.RequestInput{
		Header: headerToMap(c.Request.Header),
		Body:   info.Request.Extra,
	}
	referencePlan, useReferencePricing := info.ReferencePricing[info.Group]
	if useReferencePricing {
		settlementUsage := info.Usage
		if !usageReported {
			// The reference distinguishes an explicit zero-usage report (free)
			// from an omitted usage report, which falls back to the prompt estimate.
			settlementUsage = &protocolkit.Usage{
				PromptTokens: info.PromptTokens,
				TotalTokens:  info.PromptTokens,
			}
		}
		settlement, billingErr := referencePlan.SettlementUsageQuota(
			settlementUsage,
			billingsvc.ReferenceUsageContext{
				IsClaude:   isClaude,
				ForceAudio: isExplicitAudioRelayMode(info.Mode),
			},
		)
		if billingErr != nil {
			info.QuotaClamp = settlement.Clamp
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费计算失败", "settlement_failed")
			return fmt.Errorf("compute reference relay quota: %w", billingErr)
		}
		info.Quota = settlement.Quota
		info.QuotaClamp = settlement.Clamp
		info.UsageBillingFields = settlement.BillingLogFields()
	} else if info.Usage.PromptTokens == 0 && info.Usage.CompletionTokens == 0 {
		// No usage reported (e.g. a stream without a usage chunk); fall back to
		// the prompt estimate with flat pricing.
		info.Quota, info.QuotaClamp = billingsvc.ComputeQuotaForUserChecked(
			info.ModelName, info.UserGroup, info.Group, info.PromptTokens, 0,
		)
	} else {
		var billingErr error
		info.Quota, info.QuotaClamp, billingErr = billingsvc.ComputeBillingQuotaForUser(
			info.ModelName, info.UserGroup, info.Group, isClaude, info.Usage, reqInput,
		)
		if billingErr != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费计算失败", "settlement_failed")
			return fmt.Errorf("compute relay quota: %w", billingErr)
		}
	}
	if info.QuotaClamp != nil {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费额度超出安全范围", "settlement_failed")
		return fmt.Errorf("model quota conversion was not exact: %w", info.QuotaClamp)
	}
	if info.Quota < 0 {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "计费额度无效", "settlement_failed")
		return fmt.Errorf("computed negative relay quota: %d", info.Quota)
	}
	if info.ToolUsageCounter != nil {
		groupRatio, present := info.ToolGroupRatios[info.Group]
		if !present {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费快照缺失", "settlement_failed")
			return errors.New("tool pricing group ratio snapshot is missing")
		}
		toolSettlement, toolErr := info.ToolUsageCounter.Settle(groupRatio)
		info.ToolQuotaClamp = toolSettlement.Clamp
		if toolErr != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费计算失败", "settlement_failed")
			return fmt.Errorf("compute tool surcharge: %w", toolErr)
		}
		if info.ToolQuotaClamp != nil {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费额度超出安全范围", "settlement_failed")
			return fmt.Errorf("tool quota conversion was not exact: %w", info.ToolQuotaClamp)
		}
		combined, ok := quotamath.AddQuotaWithinBounds(info.Quota, toolSettlement.Quota)
		if !ok {
			discardDeferred()
			writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "工具计费额度溢出", "settlement_failed")
			return errors.New("combined model and tool quota exceeds accounting bounds")
		}
		info.Quota = combined
		info.ToolBillingFields = toolSettlement.AuditFields()
	}

	// Reconcile user funding, token remaining quota, token used quota, and user
	// lifetime counters in one transaction. On failure the original reservation
	// stays held for reconciliation; it is never refunded after accepted work.
	if err := reservation.SettleWithChannel(info.Quota, info.Channel.Id); err != nil {
		discardDeferred()
		writeRelayAccountingFailure(c, info, http.StatusInternalServerError, "请求已完成但额度结算失败", "settlement_failed")
		return fmt.Errorf("settle relay quota: %w", err)
	}

	// The finance transaction cannot span a separately configured log database.
	// A log failure is returned and reported, but the already-committed success
	// response is still delivered so clients are not encouraged to retry a
	// request that was charged successfully.
	logErr := billingsvc.RecordConsumeLogChecked(
		userId,
		state.Username,
		state.TokenName,
		info.ModelName,
		max(info.Usage.PromptTokens, info.PromptTokens),
		info.Usage.CompletionTokens,
		info.Quota,
		int(time.Since(start).Milliseconds()),
		info.IsStream,
		info.Channel.Id,
		info.Group,
		c.ClientIP(),
		state.RequestID,
		"",
		state.TokenID,
		buildLogOther(c, info, reservation),
	)
	if lastErr != nil && !responseCommitted && !timedWriter.Written() {
		discardDeferred()
		relaycommon.WriteUpstreamError(c, lastErr)
		return errors.Join(
			fmt.Errorf("upstream failed after reporting billable usage: %w", lastErr),
			wrapRelayError("record relay consume log", logErr),
		)
	}
	var responseErr error
	if deferredWriter != nil {
		responseErr = deferredWriter.Commit()
	}
	// Logging is attempted before any user-configured notification endpoint can
	// delay this handler. The settled reservation selects wallet vs subscription.
	billingsvc.CheckAndSendQuotaReminderForReservation(userId, reservation)
	if lastErr != nil {
		return errors.Join(
			fmt.Errorf("upstream failed after relay response started: %w", lastErr),
			wrapRelayError("record relay consume log", logErr),
			wrapRelayError("commit relay response", responseErr),
		)
	}
	return errors.Join(
		wrapRelayError("record relay consume log", logErr),
		wrapRelayError("commit relay response", responseErr),
	)
}
