package relay

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	jimengTaskPrivateDataV2Prefix     = "jimeng-v2:"
	jimengTaskTextMaxBytes            = jimeng.MaxDurableResponseBytes
	jimengProviderResultURLMaxBytes   = 32 * 1024
	jimengChannelBaseURLMaxBytes      = 4 * 1024
	jimengEncryptedChannelKeyMaxBytes = 16 * 1024
	jimengTaskFailReasonMaxBytes      = 4 * 1024
)

var jimengTaskPlatform = strconv.Itoa(int(constant.ChannelTypeJimeng))

type jimengTaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

type jimengTaskProperties struct {
	Input             string `json:"input"`
	UpstreamModelName string `json:"upstream_model_name,omitempty"`
	OriginModelName   string `json:"origin_model_name,omitempty"`
}

type jimengTaskPrivateData struct {
	UpstreamTaskID          string  `json:"upstream_task_id,omitempty"`
	EncryptedUpstreamTaskID string  `json:"encrypted_upstream_task_id,omitempty"`
	ResultURL               string  `json:"result_url,omitempty"`
	RelayReservationID      string  `json:"relay_reservation_id,omitempty"`
	BillingSource           string  `json:"billing_source,omitempty"`
	BillingGroupRatio       float64 `json:"billing_group_ratio,omitempty"`
	HasBillingGroupRatio    bool    `json:"has_billing_group_ratio,omitempty"`
	HasSpecialGroupRatio    bool    `json:"has_special_group_ratio,omitempty"`
	SubscriptionID          int     `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64   `json:"funding_usage_epoch,omitempty"`
	FundingRequestID        string  `json:"funding_request_id,omitempty"`
	FundingReserved         int     `json:"funding_reserved,omitempty"`
	TokenID                 int     `json:"token_id,omitempty"`
	TokenReserved           bool    `json:"token_reserved,omitempty"`
	TokenUnlimited          bool    `json:"token_unlimited,omitempty"`
	ChannelBaseURL          string  `json:"channel_base_url,omitempty"`
	EncryptedChannelKey     string  `json:"encrypted_channel_key,omitempty"`
	SettlementPending       bool    `json:"settlement_pending,omitempty"`
}

type jimengRecoveryEnvelope struct {
	UserID         int    `json:"user_id"`
	TaskID         string `json:"task_id"`
	ChannelID      int    `json:"channel_id"`
	UpstreamTaskID string `json:"upstream_task_id,omitempty"`
	// PrivateData is read only for compatibility with encrypted recovery tokens
	// issued before the primary-database recovery queue was introduced.
	PrivateData string `json:"private_data,omitempty"`
	Status      string `json:"status"`
	FailReason  string `json:"fail_reason,omitempty"`
}

type jimengVideoResponse struct {
	ID          string            `json:"id"`
	TaskID      string            `json:"task_id,omitempty"`
	Object      string            `json:"object"`
	Model       string            `json:"model"`
	Status      string            `json:"status"`
	Progress    int               `json:"progress"`
	CreatedAt   int64             `json:"created_at"`
	CompletedAt int64             `json:"completed_at,omitempty"`
	Error       *jimengVideoError `json:"error,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
}

type jimengVideoError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func RelayJimeng(c *gin.Context) {
	requestContext, ok := middleware.GetJimengRequest(c)
	if !ok {
		writeJimengTaskError(c, http.StatusInternalServerError, "invalid_request", "Jimeng request context is missing")
		return
	}
	if requestContext.Action == jimeng.FetchAction {
		relayJimengFetch(c, requestContext.Request)
		return
	}
	relayJimengSubmit(c, requestContext.Request, requestContext.RawBody)
}

func relayJimengSubmit(c *gin.Context, request jimeng.Request, rawBody []byte) {
	originModel := strings.TrimSpace(request.ReqKey)
	if originModel == "" {
		writeJimengTaskError(c, http.StatusBadRequest, "invalid_request", "req_key is required")
		return
	}
	validated, err := jimeng.PrepareSubmitRequest(request, originModel)
	if err != nil {
		writeJimengTaskError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	billingUnits := 1
	if validated.Frames == jimeng.LongVideoFrames {
		billingUnits = 2
	}
	groups := middleware.GetTokenGroups(c)
	if len(groups) == 0 {
		writeJimengTaskError(c, http.StatusForbidden, "group_not_allowed", "令牌分组不可用")
		return
	}
	selected, group, usedAffinity, affinityFound, err := selectInitialJimengChannel(c, groups, originModel, rawBody)
	if err != nil {
		writeJimengUpstreamError(c, err)
		return
	}
	c.Set(common.ContextKeyGroup, group)
	userGroup := common.GetUserGroup(c)
	quota, err := service.ComputePerCallQuotaForUser(originModel, userGroup, group, billingUnits)
	if err != nil {
		writeJimengTaskError(c, http.StatusBadRequest, "model_price_error", err.Error())
		return
	}

	token := middleware.GetRelayToken(c)
	if token == nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "pre_consume_failed", "令牌上下文缺失")
		return
	}
	userID := common.GetUserId(c)
	now := common.NowTimestamp()
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier")
		return
	}
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID,
		Platform: jimengTaskPlatform, UserId: userID, Group: group, Quota: quota,
		Action: "generate", Status: model.TaskStatusNotStart, SubmitTime: now,
		Progress: "0%", Data: "null",
	}
	mappedModel := relaycommon.GetMappedModel(selected, originModel)
	initialPayload, err := jimeng.PrepareSubmitRequest(request, mappedModel)
	if err != nil {
		writeJimengTaskError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	mappedModel = initialPayload.ReqKey
	task.Properties, err = marshalJimengTaskProperties(jimengTaskProperties{
		Input: request.Prompt, UpstreamModelName: mappedModel, OriginModelName: originModel,
	})
	if err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error())
		return
	}
	initialKey := service.GetChannelKey(selected)
	encryptedInitialKey, err := jimengEncrypt(initialKey)
	if err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect channel credentials")
		return
	}
	task.ChannelId = selected.Id
	effectiveGroupRatio, specialGroupRatio := service.EffectiveGroupRatio(userGroup, group)
	privateData := jimengTaskPrivateData{
		ChannelBaseURL: jimengChannelBaseURL(selected), EncryptedChannelKey: encryptedInitialKey,
		BillingGroupRatio: effectiveGroupRatio, HasBillingGroupRatio: true,
		HasSpecialGroupRatio: specialGroupRatio,
	}
	reservation, err := createJimengReservedTask(&task, token, &privateData)
	if err != nil {
		message := "预扣费失败: " + err.Error()
		code := "pre_consume_failed"
		status := http.StatusInternalServerError
		if service.IsSubscriptionFundingErr(err) {
			message = "订阅额度不足或未配置订阅: " + err.Error()
			code = "insufficient_quota"
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrInsufficientQuota) || errors.Is(err, service.ErrInsufficientTokenQuota) {
			message = "用户或令牌额度不足"
			code = "insufficient_quota"
			status = http.StatusBadRequest
		} else {
			common.SysError("create atomic Jimeng reservation: " + err.Error())
		}
		writeJimengTaskError(c, status, code, message)
		return
	}
	providerMayHaveAccepted := false
	reservationFinalized := false
	defer func() {
		if providerMayHaveAccepted || reservationFinalized {
			return
		}
		if cleanupErr := refundJimengTaskReservation(&task, reservation, "submission aborted before provider acceptance", ""); cleanupErr != nil {
			common.SysError("Jimeng atomic reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()

	retryTimes := service.RetryTimes()
	preferredChannelID, affinityFound := service.GetPreferredChannelByAffinity(c, originModel, group, rawBody)
	ignore := make(map[int]struct{}, retryTimes+1)
	initialChannelID := 0
	var upstreamResult *jimeng.SubmitResult
	var upstreamRaw []byte
	var lastErr error
	client := &jimeng.Client{}
	for attempt := 0; attempt <= retryTimes; attempt++ {
		channel := selected
		if attempt > 0 {
			var selectErr error
			channel, usedAffinity, selectErr = service.GetSatisfiedChannelWithPreferred(group, originModel, preferredChannelID, ignore, nil)
			if selectErr != nil {
				lastErr = selectErr
				break
			}
		}
		if attempt == 0 && affinityFound && !usedAffinity && !service.ShouldKeepChannelAffinityOnChannelDisabled() {
			service.ClearCurrentChannelAffinityCache(c)
		}
		if initialChannelID == 0 {
			initialChannelID = channel.Id
		}
		if channel.Type != int(constant.ChannelTypeJimeng) {
			lastErr = fmt.Errorf("channel %d does not support Jimeng tasks", channel.Id)
			ignore[channel.Id] = struct{}{}
			continue
		}
		if usedAffinity {
			service.MarkChannelAffinityUsed(c, group, channel.Id)
		}
		mappedModel = relaycommon.GetMappedModel(channel, originModel)
		payload, prepareErr := jimeng.PrepareSubmitRequest(request, mappedModel)
		if prepareErr != nil {
			lastErr = prepareErr
			break
		}
		mappedModel = payload.ReqKey
		properties, marshalErr := marshalJimengTaskProperties(jimengTaskProperties{
			Input: request.Prompt, UpstreamModelName: mappedModel, OriginModelName: originModel,
		})
		if marshalErr != nil {
			lastErr = marshalErr
			break
		}
		baseURL := jimengChannelBaseURL(channel)
		channelKey := service.GetChannelKey(channel)
		encryptedChannelKey, encryptErr := jimengEncrypt(channelKey)
		if encryptErr != nil {
			lastErr = encryptErr
			break
		}
		privateData.ChannelBaseURL = baseURL
		privateData.EncryptedChannelKey = encryptedChannelKey
		privateJSON, marshalErr := marshalJimengTaskPrivateData(privateData)
		if marshalErr != nil {
			lastErr = marshalErr
			break
		}
		if err := markJimengTaskDispatching(&task, reservation, channel.Id, properties, privateJSON); err != nil {
			lastErr = err
			break
		}
		selected = channel
		providerMayHaveAccepted = true
		upstreamResult, upstreamRaw, lastErr = client.Submit(c.Request.Context(), baseURL, channelKey, payload)
		if lastErr == nil {
			privateData.UpstreamTaskID = upstreamResult.Data.TaskID
			break
		}
		if isAmbiguousJimengSubmitError(lastErr) {
			providerMayHaveAccepted = true
			break
		}
		if !jimeng.SubmitWasDispatched(lastErr) {
			// The provider client authoritatively proved that no request bytes were
			// dispatched. A local marker-write failure must never change that fact
			// into an UNKNOWN charge.
			providerMayHaveAccepted = false
			if transitionErr := markJimengTaskSafeToRefund(&task, reservation); transitionErr != nil {
				lastErr = errors.Join(lastErr, fmt.Errorf("persist safe Jimeng retry boundary: %w", transitionErr))
				break
			}
			ignore[channel.Id] = struct{}{}
			continue
		}
		// Jimeng exposes no idempotency key, so a dispatched submit is never
		// retried even when the provider explicitly rejects it. A response is
		// nevertheless authoritative non-acceptance, so the direct atomic refund
		// below must be used even if persisting this advisory marker fails.
		providerMayHaveAccepted = false
		if transitionErr := markJimengTaskSafeToRefund(&task, reservation); transitionErr != nil {
			lastErr = errors.Join(lastErr, fmt.Errorf("persist safe Jimeng rejection boundary: %w", transitionErr))
		}
		break
	}
	if lastErr != nil || selected == nil || upstreamResult == nil {
		message := "failed to submit Jimeng task"
		if lastErr != nil {
			message = safeJimengProviderErrorMessage(lastErr, message)
		}
		if providerMayHaveAccepted && selected != nil {
			privateJSON, marshalErr := marshalJimengTaskPrivateData(privateData)
			if marshalErr == nil {
				marshalErr = settleJimengAcceptedTask(
					&task, reservation, privateJSON, upstreamRaw, model.TaskStatusUnknown, message,
					model.JimengTaskOperationDispatching, "",
				)
			}
			if marshalErr != nil {
				privateData.SettlementPending = true
				pendingErr := persistJimengPendingOutcome(
					&task, privateData, upstreamRaw, model.TaskStatusUnknown, message,
				)
				common.SysError("persist ambiguous Jimeng task " + task.TaskID + ": " + marshalErr.Error())
				data := gin.H{
					"task_id": task.TaskID, "status": "unknown",
					"settlement_pending": true,
					"recovery_durable":   true,
				}
				if pendingErr != nil {
					data["recovery_degraded"] = true
				}
				// No charge committed, so no consume event exists yet. The durable
				// operation will create it in the same transaction as settlement.
				data["audit_pending"] = true
				service.RecordChannelAffinity(c, initialChannelID, selected.Id)
				// The provider may already be running this task. Return a
				// non-retriable acceptance status so generic HTTP clients and
				// gateways do not replay the submit and create duplicate work.
				writeJimengTaskErrorData(c, http.StatusAccepted, "task_commit_pending",
					"Jimeng may have accepted the task, but local accounting could not be committed", data)
				return
			}
			reservationFinalized = true
			service.CheckAndSendQuotaReminderForReservation(userID, reservation)
			logErr := service.DeliverAuditLogOutboxEvent(jimengAuditEventID(reservation.ReservationID()))
			if logErr != nil {
				common.SysError("deliver ambiguous Jimeng consume audit: " + logErr.Error())
			}
			service.RecordChannelAffinity(c, initialChannelID, selected.Id)
			responseData := gin.H{"task_id": task.TaskID, "status": "unknown", "audit_durable": true}
			if logErr != nil {
				responseData["audit_delivery_pending"] = true
			}
			writeJimengTaskErrorData(c, http.StatusAccepted, "submit_outcome_unknown", message, responseData)
			return
		}
		if err := refundJimengTaskReservation(&task, reservation, message, ""); err != nil {
			reservationFinalized = true
			common.SysError("persist rejected Jimeng task " + task.TaskID + ": " + err.Error())
			data := gin.H{"task_id": task.TaskID, "reservation_state": "pending"}
			if jimengSafeRefundTransitionMatches(task.TaskID, reservation.ReservationID()) {
				data["recovery_durable"] = true
			} else {
				// No primary-database write can communicate a just-observed provider
				// rejection while that database is unavailable. Preserve an encrypted
				// same-node emergency fact, but report its cluster limitation honestly.
				data["recovery_durable"] = false
				data["recovery_degraded"] = true
				if recoveryErr := persistJimengRecovery(jimengRecoveryEnvelope{
					UserID: userID, TaskID: task.TaskID, ChannelID: task.ChannelId,
					Status: model.TaskStatusFailure,
				}); recoveryErr != nil {
					common.SysError("persist secondary Jimeng rejection recovery for " + task.TaskID + ": " + recoveryErr.Error())
				} else {
					data["node_local_recovery"] = true
				}
			}
			writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_cleanup_failed",
				"Jimeng submission failed and cleanup requires attention",
				data)
			return
		}
		reservationFinalized = true
		writeJimengUpstreamError(c, lastErr)
		return
	}

	privateData.UpstreamTaskID = upstreamResult.Data.TaskID
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	if err != nil {
		data := gin.H{
			"task_id": task.TaskID, "status": "accepted",
			"settlement_pending": true,
			"recovery_durable":   true,
			"audit_pending":      true,
		}
		fallbackErr := persistJimengAcceptedOperationFallback(
			&task, reservation.ReservationID(), upstreamResult.Data.TaskID,
		)
		if fallbackErr != nil {
			common.SysError("Jimeng accepted task metadata recovery write failed for " + task.TaskID + ": " + fallbackErr.Error())
			data["status"] = "unknown"
			data["provider_poll_recovery"] = false
			data["recovery_durable"] = false
			data["recovery_degraded"] = true
			if recoveryErr := persistJimengRecovery(jimengRecoveryEnvelope{
				UserID: userID, TaskID: task.TaskID, ChannelID: selected.Id,
				UpstreamTaskID: upstreamResult.Data.TaskID, Status: model.TaskStatusSubmitted,
			}); recoveryErr != nil {
				common.SysError("persist secondary Jimeng metadata recovery for " + task.TaskID + ": " + recoveryErr.Error())
			} else {
				data["node_local_recovery"] = true
			}
		}
		service.RecordChannelAffinity(c, initialChannelID, selected.Id)
		writeJimengTaskErrorData(c, http.StatusAccepted, "task_commit_pending",
			"Jimeng accepted the task and recovery is pending", data)
		return
	}
	if err := settleJimengAcceptedTask(
		&task, reservation, privateJSON, upstreamRaw, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, "",
	); err != nil {
		privateData.SettlementPending = true
		pendingErr := persistJimengPendingOutcome(
			&task, privateData, upstreamRaw, model.TaskStatusSubmitted, "",
		)
		common.SysError("commit accepted Jimeng task " + task.TaskID + ": " + err.Error())
		data := gin.H{
			"task_id": task.TaskID, "status": "accepted",
			"settlement_pending": true,
			"recovery_durable":   true,
		}
		// If the full task write is unavailable, independently preserve the
		// provider id encrypted on the primary-database operation. This narrow
		// update is enough for any worker to finish settlement and polling.
		if pendingErr != nil {
			common.SysError("Jimeng accepted task database recovery write failed for " + task.TaskID + ": " + pendingErr.Error())
			fallbackErr := persistJimengAcceptedOperationFallback(
				&task, reservation.ReservationID(), upstreamResult.Data.TaskID,
			)
			if fallbackErr != nil {
				// The original dispatch marker is still a durable UNKNOWN billing
				// recovery point, but it cannot autonomously poll the accepted task.
				data["status"] = "unknown"
				data["provider_poll_recovery"] = false
				data["recovery_durable"] = false
				data["recovery_degraded"] = true
				if recoveryErr := persistJimengRecovery(jimengRecoveryEnvelope{
					UserID: userID, TaskID: task.TaskID, ChannelID: selected.Id,
					UpstreamTaskID: upstreamResult.Data.TaskID, Status: model.TaskStatusSubmitted,
				}); recoveryErr != nil {
					common.SysError("persist secondary Jimeng recovery for " + task.TaskID + ": " + recoveryErr.Error())
				} else {
					data["node_local_recovery"] = true
				}
			}
		}
		// Audit creation is intentionally deferred with the charge. The recovery
		// worker will atomically settle accounting and enqueue the consume event.
		data["audit_pending"] = true
		service.RecordChannelAffinity(c, initialChannelID, selected.Id)
		writeJimengTaskErrorData(c, http.StatusAccepted, "task_commit_pending",
			"Jimeng accepted the task, but local accounting could not be committed", data)
		return
	}

	reservationFinalized = true
	// Modern tasks are recovered from the primary database. Cleanup of an
	// emergency local compatibility copy is best-effort and never gates the
	// accepted response.
	cleanupErr := removeJimengRecovery(task.TaskID)
	service.CheckAndSendQuotaReminderForReservation(userID, reservation)
	logErr := service.DeliverAuditLogOutboxEvent(jimengAuditEventID(reservation.ReservationID()))
	if logErr != nil {
		common.SysError("deliver accepted Jimeng consume audit: " + logErr.Error())
	}
	service.RecordChannelAffinity(c, initialChannelID, selected.Id)
	response := newJimengVideoResponse(task, originModel)
	if cleanupErr != nil {
		if response.Metadata == nil {
			response.Metadata = make(map[string]any)
		}
		if cleanupErr != nil {
			response.Metadata["recovery_cleanup_pending"] = true
		}
	}
	c.JSON(http.StatusOK, response)
}

func hasMatchingJimengRecovery(taskID string, userID, channelID int, upstreamTaskID string) bool {
	envelope, err := loadJimengRecovery(taskID)
	if err != nil || envelope.UserID != userID || envelope.TaskID != taskID || envelope.ChannelID != channelID {
		return false
	}
	if upstreamTaskID == "" {
		return envelope.UpstreamTaskID == "" && envelope.Status == model.TaskStatusUnknown
	}
	return envelope.UpstreamTaskID == upstreamTaskID && envelope.Status == model.TaskStatusSubmitted
}

func persistJimengRejectedTask(task *model.Task, upstreamRaw []byte, failReason string) error {
	const maxAttempts = 3
	upstreamRaw = durableJimengProviderPayload(upstreamRaw)
	failReason = boundedJimengFailReason(failReason)
	updatedAt := common.NowTimestamp()
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result := model.DB.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": failReason,
				"finish_time": updatedAt, "progress": "100%", "data": string(upstreamRaw),
				"updated_at": updatedAt,
			})
		if result.Error == nil && result.RowsAffected == 1 {
			task.Status = model.TaskStatusFailure
			task.FailReason = failReason
			task.FinishTime = updatedAt
			task.Progress = "100%"
			task.Data = string(upstreamRaw)
			task.UpdatedAt = updatedAt
			return nil
		}
		if result.Error != nil {
			lastErr = result.Error
		} else {
			var current model.Task
			if err := model.DB.First(&current, task.ID).Error; err != nil {
				lastErr = err
			} else if current.Status == model.TaskStatusFailure {
				*task = current
				return nil
			} else {
				lastErr = fmt.Errorf("task %s is already in status %s", task.TaskID, current.Status)
			}
		}
		if attempt < maxAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist rejected Jimeng task: %w", lastErr)
}

func selectInitialJimengChannel(c *gin.Context, groups []string, modelName string, rawBody []byte) (*model.Channel, string, bool, bool, error) {
	for _, group := range groups {
		preferredChannelID, affinityFound := service.GetPreferredChannelByAffinity(c, modelName, group, rawBody)
		ignored := make(map[int]struct{})
		for {
			channel, usedAffinity, err := service.GetSatisfiedChannelWithPreferred(group, modelName, preferredChannelID, ignored, nil)
			if err != nil {
				if errors.Is(err, service.ErrChannelNotFound) {
					break
				}
				return nil, "", false, affinityFound, err
			}
			if channel.Type == int(constant.ChannelTypeJimeng) {
				return channel, group, usedAffinity, affinityFound, nil
			}
			ignored[channel.Id] = struct{}{}
		}
	}
	return nil, "", false, false, service.ErrChannelNotFound
}

func persistJimengDispatchMetadata(task *model.Task, channelID int, properties, privateData string) error {
	const maxAttempts = 3
	if err := validateJimengDurableText("task properties", properties, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	if err := validateJimengDurableText("task private data", privateData, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	updatedAt := common.NowTimestamp()
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result := model.DB.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"channel_id": channelID, "properties": properties,
				"private_data": privateData, "updated_at": updatedAt,
			})
		if result.Error == nil && result.RowsAffected == 1 {
			task.ChannelId = channelID
			task.Properties = properties
			task.PrivateData = privateData
			task.UpdatedAt = updatedAt
			return nil
		}
		if result.Error != nil {
			lastErr = result.Error
		} else {
			var current model.Task
			if err := model.DB.First(&current, task.ID).Error; err != nil {
				lastErr = err
			} else if current.Status == model.TaskStatusNotStart && current.ChannelId == channelID &&
				current.Properties == properties && current.PrivateData == privateData {
				*task = current
				return nil
			} else {
				lastErr = errors.New("Jimeng dispatch metadata changed concurrently")
			}
		}
		if attempt < maxAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Jimeng dispatch metadata: %w", lastErr)
}

func commitJimengAcceptedTask(
	task *model.Task,
	funding *service.FundingSession,
	tokenId int,
	tokenUnlimited bool,
	quota, channelId int,
	privateJSON string,
	upstreamRaw []byte,
	status, failReason string,
) error {
	const maxAttempts = 3
	if err := validateJimengDurableText("task private data", privateJSON, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	upstreamRaw = durableJimengProviderPayload(upstreamRaw)
	failReason = boundedJimengFailReason(failReason)
	updatedAt := common.NowTimestamp()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := funding.CommitAcceptedPerCallWithAccounting(
			quota, tokenId, tokenUnlimited, channelId, func(tx *gorm.DB) (bool, error) {
				var current model.Task
				if err := tx.First(&current, task.ID).Error; err != nil {
					return false, err
				}
				existingPrivate, err := decodeJimengTaskPrivateData(current.PrivateData)
				if err != nil {
					return false, fmt.Errorf("decode current Jimeng accounting metadata: %w", err)
				}
				expectedPrivate, err := decodeJimengTaskPrivateData(privateJSON)
				if err != nil {
					return false, fmt.Errorf("decode expected Jimeng accounting metadata: %w", err)
				}
				sameAcceptance := current.Status == status && current.ChannelId == channelId &&
					existingPrivate.UpstreamTaskID == expectedPrivate.UpstreamTaskID
				if sameAcceptance && !existingPrivate.SettlementPending {
					return true, nil
				}
				resumingPending := sameAcceptance && existingPrivate.SettlementPending &&
					!expectedPrivate.SettlementPending
				if current.Status != model.TaskStatusNotStart && !resumingPending {
					return false, fmt.Errorf("task %s is already in status %s", task.TaskID, current.Status)
				}
				query := tx.Model(&model.Task{}).Where("id = ?", task.ID)
				if resumingPending {
					query = query.Where("status = ? AND private_data = ?", current.Status, current.PrivateData)
				} else {
					query = query.Where("status = ?", model.TaskStatusNotStart)
				}
				result := query.Updates(map[string]any{
					"channel_id": channelId, "private_data": privateJSON, "data": string(upstreamRaw),
					"status": status, "fail_reason": failReason, "updated_at": updatedAt,
				})
				if result.Error != nil {
					return false, result.Error
				}
				if result.RowsAffected == 0 {
					return false, errors.New("task acceptance state changed concurrently")
				}
				return false, nil
			},
		)
		if err == nil {
			task.ChannelId = channelId
			task.PrivateData = privateJSON
			task.Data = string(upstreamRaw)
			task.Status = status
			task.FailReason = failReason
			task.UpdatedAt = updatedAt
			return nil
		}
		if attempt == maxAttempts-1 {
			return err
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	return errors.New("failed to commit accepted Jimeng task")
}

func persistJimengPendingSettlement(
	task *model.Task,
	channelId int,
	privateJSON string,
	upstreamRaw []byte,
	status, failReason string,
) error {
	const maxAttempts = 3
	if err := validateJimengDurableText("task private data", privateJSON, jimengTaskTextMaxBytes); err != nil {
		return err
	}
	upstreamRaw = durableJimengProviderPayload(upstreamRaw)
	failReason = boundedJimengFailReason(failReason)
	updatedAt := common.NowTimestamp()
	expectedPrivate, err := decodeJimengTaskPrivateData(privateJSON)
	if err != nil {
		return fmt.Errorf("decode pending Jimeng accounting metadata: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result := model.DB.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"channel_id": channelId, "private_data": privateJSON, "data": string(upstreamRaw),
				"status": status, "fail_reason": failReason, "updated_at": updatedAt,
			})
		if result.Error == nil && result.RowsAffected == 1 {
			task.ChannelId = channelId
			task.PrivateData = privateJSON
			task.Data = string(upstreamRaw)
			task.Status = status
			task.FailReason = failReason
			task.UpdatedAt = updatedAt
			return nil
		}
		if result.Error != nil {
			lastErr = result.Error
		} else {
			var current model.Task
			if err := model.DB.First(&current, task.ID).Error; err == nil {
				existingPrivate, decodeErr := decodeJimengTaskPrivateData(current.PrivateData)
				if decodeErr != nil {
					return fmt.Errorf("decode stored pending Jimeng accounting metadata: %w", decodeErr)
				}
				if current.Status == status && current.ChannelId == channelId &&
					existingPrivate.SettlementPending &&
					existingPrivate.UpstreamTaskID == expectedPrivate.UpstreamTaskID {
					*task = current
					return nil
				}
				lastErr = errors.New("pending Jimeng settlement state changed concurrently")
			} else {
				lastErr = err
			}
		}
		if attempt == maxAttempts-1 {
			return fmt.Errorf("failed to persist pending Jimeng settlement: %w", lastErr)
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("failed to persist pending Jimeng settlement: %w", lastErr)
}

func settlePendingJimengTask(task *model.Task, privateData *jimengTaskPrivateData) error {
	return settlePendingJimengTaskWithFence(task, privateData, "", "")
}

func settlePendingJimengTaskWithFence(
	task *model.Task,
	privateData *jimengTaskPrivateData,
	expectedOperationState, leaseOwner string,
) error {
	expectedFundingReservation := task.Quota
	if privateData.BillingSource == service.BillingSourceSubscription && expectedFundingReservation == 0 {
		expectedFundingReservation = 1
	}
	if privateData.FundingReserved != expectedFundingReservation {
		return fmt.Errorf("Jimeng funding reservation mismatch: task=%d reserved=%d", task.Quota, privateData.FundingReserved)
	}
	if privateData.TokenReserved && privateData.TokenID <= 0 {
		return errors.New("Jimeng token reservation is missing its token ID")
	}
	if privateData.RelayReservationID != "" {
		reservation, err := service.RestoreRelayQuotaReservation(privateData.RelayReservationID)
		if err != nil {
			return err
		}
		privateData.SettlementPending = false
		privateJSON, err := marshalJimengTaskPrivateData(*privateData)
		if err != nil {
			return err
		}
		if expectedOperationState == "" {
			expectedOperationState = model.JimengTaskOperationSubmitted
			if task.Status == model.TaskStatusUnknown {
				expectedOperationState = model.JimengTaskOperationDispatching
			}
		}
		if err := settleJimengAcceptedTask(
			task, reservation, privateJSON, []byte(task.Data), task.Status, task.FailReason,
			expectedOperationState, leaseOwner,
		); err != nil {
			privateData.SettlementPending = true
			return err
		}
		updatedPrivate, err := decodeJimengTaskPrivateData(task.PrivateData)
		if err != nil {
			return fmt.Errorf("decode settled Jimeng accounting metadata: %w", err)
		}
		*privateData = updatedPrivate
		return nil
	}
	funding, err := service.RestoreFundingSession(task.UserId, service.FundingReservation{
		Source: privateData.BillingSource, Reserved: privateData.FundingReserved,
		SubscriptionId: privateData.SubscriptionID, UsageEpoch: privateData.FundingUsageEpoch,
		RequestId: privateData.FundingRequestID,
	})
	if err != nil {
		return err
	}
	privateData.SettlementPending = false
	privateJSON, err := marshalJimengTaskPrivateData(*privateData)
	if err != nil {
		return err
	}
	tokenId := privateData.TokenID
	tokenUnlimited := privateData.TokenUnlimited || (tokenId > 0 && !privateData.TokenReserved)
	if err := commitJimengAcceptedTask(
		task, funding, tokenId, tokenUnlimited, task.Quota, task.ChannelId, privateJSON,
		[]byte(task.Data), task.Status, task.FailReason,
	); err != nil {
		privateData.SettlementPending = true
		return err
	}
	updatedPrivate, err := decodeJimengTaskPrivateData(task.PrivateData)
	if err != nil {
		return fmt.Errorf("decode settled Jimeng accounting metadata: %w", err)
	}
	*privateData = updatedPrivate
	return nil
}

func marshalJimengRecoveryEnvelope(envelope jimengRecoveryEnvelope) ([]byte, error) {
	return common.Marshal(envelope)
}

func unmarshalJimengRecoveryEnvelope(data []byte) (jimengRecoveryEnvelope, error) {
	var envelope jimengRecoveryEnvelope
	if err := common.Unmarshal(data, &envelope); err != nil {
		return jimengRecoveryEnvelope{}, err
	}
	return envelope, nil
}

// restoreLegacyJimengRecoveryToken keeps read-only compatibility with
// encrypted recovery tokens issued before recovery moved into the primary
// database. New submissions never emit these tokens.
func restoreLegacyJimengRecoveryToken(
	task *model.Task,
	userID int,
	recoveryToken string,
) (jimengTaskPrivateData, error) {
	plaintext, err := common.DecryptByAES(recoveryToken)
	if err != nil {
		return jimengTaskPrivateData{}, errors.New("invalid Jimeng recovery token")
	}
	var envelope jimengRecoveryEnvelope
	if err := common.UnmarshalJsonStr(plaintext, &envelope); err != nil {
		return jimengTaskPrivateData{}, errors.New("invalid Jimeng recovery token")
	}
	if envelope.UserID != userID || envelope.TaskID != task.TaskID || envelope.ChannelID <= 0 ||
		(task.ChannelId != 0 && envelope.ChannelID != task.ChannelId) {
		return jimengTaskPrivateData{}, errors.New("Jimeng recovery token does not match this task")
	}
	if envelope.Status != model.TaskStatusSubmitted && envelope.Status != model.TaskStatusUnknown {
		return jimengTaskPrivateData{}, errors.New("invalid Jimeng recovery status")
	}
	if envelope.PrivateData == "" {
		return jimengTaskPrivateData{}, errors.New("invalid legacy Jimeng recovery metadata")
	}
	privateData, err := decodeJimengTaskPrivateData(envelope.PrivateData)
	if err != nil || !privateData.SettlementPending ||
		(envelope.Status == model.TaskStatusSubmitted && strings.TrimSpace(privateData.UpstreamTaskID) == "") {
		return jimengTaskPrivateData{}, errors.New("invalid legacy Jimeng recovery metadata")
	}
	if err := persistJimengPendingSettlement(
		task, envelope.ChannelID, envelope.PrivateData, nil, envelope.Status, envelope.FailReason,
	); err != nil {
		return jimengTaskPrivateData{}, err
	}
	return privateData, nil
}

func restoreJimengRecovery(task *model.Task, userID int, envelope jimengRecoveryEnvelope) (jimengTaskPrivateData, error) {
	if envelope.UserID != userID || envelope.TaskID != task.TaskID || envelope.ChannelID <= 0 ||
		(task.ChannelId != 0 && envelope.ChannelID != task.ChannelId) {
		return jimengTaskPrivateData{}, errors.New("Jimeng recovery record does not match this task")
	}
	if envelope.Status != model.TaskStatusSubmitted && envelope.Status != model.TaskStatusUnknown {
		return jimengTaskPrivateData{}, errors.New("invalid Jimeng recovery status")
	}
	if envelope.Status == model.TaskStatusSubmitted && envelope.UpstreamTaskID == "" {
		return jimengTaskPrivateData{}, errors.New("invalid Jimeng recovery metadata")
	}
	privateData, err := decodeJimengTaskPrivateData(task.PrivateData)
	if err != nil {
		return jimengTaskPrivateData{}, fmt.Errorf("decode Jimeng recovery metadata: %w", err)
	}
	// A write-ahead record proves that dispatch was prepared, not that the
	// provider accepted work. Only an accepted upstream id or an independently
	// persisted pending-settlement marker makes charging during recovery safe.
	if envelope.UpstreamTaskID == "" && !privateData.SettlementPending {
		return jimengTaskPrivateData{}, errors.New("Jimeng recovery does not prove provider acceptance")
	}
	if privateData.UpstreamTaskID != "" && envelope.UpstreamTaskID != "" &&
		privateData.UpstreamTaskID != envelope.UpstreamTaskID {
		return jimengTaskPrivateData{}, errors.New("Jimeng recovery upstream task does not match")
	}
	privateData.UpstreamTaskID = envelope.UpstreamTaskID
	privateData.SettlementPending = true
	if privateData.RelayReservationID != "" {
		if err := persistJimengPendingOutcome(
			task, privateData, nil, envelope.Status, envelope.FailReason,
		); err != nil {
			return jimengTaskPrivateData{}, err
		}
		return privateData, nil
	}
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	if err != nil {
		return jimengTaskPrivateData{}, err
	}
	if err := persistJimengPendingSettlement(
		task, envelope.ChannelID, privateJSON, nil, envelope.Status, envelope.FailReason,
	); err != nil {
		return jimengTaskPrivateData{}, err
	}
	return privateData, nil
}

func resolveJimengTaskChannel(task model.Task, privateData jimengTaskPrivateData) (string, string, error) {
	if privateData.RelayReservationID != "" {
		if strings.TrimSpace(privateData.ChannelBaseURL) == "" ||
			strings.TrimSpace(privateData.EncryptedChannelKey) == "" {
			return "", "", errors.New("durable Jimeng credential snapshot is incomplete")
		}
		key, err := jimengDecrypt(privateData.EncryptedChannelKey)
		if err != nil {
			return "", "", fmt.Errorf("durable Jimeng credential snapshot cannot be decrypted: %w", err)
		}
		if strings.TrimSpace(key) == "" {
			return "", "", errors.New("durable Jimeng credential snapshot is empty")
		}
		return privateData.ChannelBaseURL, key, nil
	}
	if privateData.ChannelBaseURL != "" && privateData.EncryptedChannelKey != "" {
		key, err := jimengDecrypt(privateData.EncryptedChannelKey)
		if err == nil && key != "" {
			return privateData.ChannelBaseURL, key, nil
		}
	}
	var channel model.Channel
	if err := model.DB.First(&channel, task.ChannelId).Error; err != nil {
		return "", "", errors.New("task channel not found and its credential snapshot is unavailable")
	}
	if channel.Type != int(constant.ChannelTypeJimeng) {
		return "", "", errors.New("task channel is not a Jimeng channel")
	}
	return jimengChannelBaseURL(&channel), service.GetChannelKey(&channel), nil
}

func isAmbiguousJimengSubmitError(err error) bool {
	return jimeng.SubmitMayHaveBeenAccepted(err)
}

func relayJimengFetch(c *gin.Context, request jimeng.Request) {
	userID := common.GetUserId(c)
	var task model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ?", request.TaskID, userID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJimengTaskError(c, http.StatusBadRequest, "task_not_exist", "task_origin_not_exist")
			return
		}
		writeJimengTaskError(c, http.StatusInternalServerError, "get_task_failed", err.Error())
		return
	}
	if task.Platform != jimengTaskPlatform {
		writeJimengTaskError(c, http.StatusBadRequest, "invalid_api_platform", "task is not a Jimeng task")
		return
	}
	if !containsRelayGroup(middleware.GetTokenGroups(c), task.Group) {
		writeJimengTaskError(c, http.StatusForbidden, "group_not_allowed", "令牌无权访问任务分组")
		return
	}
	var properties jimengTaskProperties
	var privateData jimengTaskPrivateData
	var err error
	if isJimengTerminalTaskStatus(task.Status) {
		// Terminal rows contain only safe result metadata and never need a
		// provider credential or task-id decryption. This keeps historical
		// results readable after retired recovery keys leave the keyring.
		properties, privateData, err = decodeJimengTerminalTaskMetadata(task)
	} else {
		properties, privateData, err = decodeJimengTaskMetadataChecked(task)
	}
	if err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_data_invalid", err.Error())
		return
	}
	modernRecoveryTerminal := false
	var modernOperation *model.JimengTaskOperation
	if privateData.RelayReservationID != "" {
		operation, operationErr := loadJimengTaskOperation(task.TaskID)
		if operationErr != nil {
			writeJimengTaskError(c, http.StatusInternalServerError, "task_recovery_failed", "task recovery state is unavailable")
			return
		}
		modernOperation = operation
		switch operation.State {
		case model.JimengTaskOperationUnknown, model.JimengTaskOperationTerminal, model.JimengTaskOperationRefunded:
			modernRecoveryTerminal = true
			// A node-local compatibility journal must never override a terminal
			// primary-database decision made by the autonomous reconciler.
			_ = removeJimengRecovery(task.TaskID)
		}
	}
	if modernOperation != nil && privateData.UpstreamTaskID == "" &&
		modernOperation.EncryptedProviderTaskID != "" {
		upstreamTaskID, decryptErr := jimengDecrypt(modernOperation.EncryptedProviderTaskID)
		if decryptErr != nil || strings.TrimSpace(upstreamTaskID) == "" {
			writeJimengTaskError(c, http.StatusInternalServerError, "task_recovery_failed",
				"task recovery identifier cannot be decrypted")
			return
		}
		privateData.UpstreamTaskID = upstreamTaskID
	}
	operationHasAcceptedEvidence := modernOperation != nil &&
		(privateData.UpstreamTaskID != "" || modernOperation.EncryptedProviderTaskID != "" ||
			task.Status != model.TaskStatusNotStart)
	if modernOperation != nil && modernOperation.SettlementPending &&
		(modernOperation.State != model.JimengTaskOperationDispatching || operationHasAcceptedEvidence) {
		privateData.SettlementPending = true
		// An independently persisted accepted-id fallback can intentionally
		// leave the Task row at NOT_START. The operation is the authoritative
		// accepted outcome; settle it before any client-triggered provider poll.
		if modernOperation.State == model.JimengTaskOperationSubmitted &&
			task.Status == model.TaskStatusNotStart {
			task.Status = model.TaskStatusSubmitted
		}
	}
	if privateData.RelayReservationID == "" && !privateData.SettlementPending && task.Status != model.TaskStatusNotStart {
		if err := removeJimengRecovery(task.TaskID); err != nil {
			common.SysError("remove stale Jimeng recovery " + task.TaskID + ": " + err.Error())
			writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_recovery_cleanup_failed",
				"task recovery cleanup is still pending", gin.H{
					"task_id": task.TaskID, "recovery_cleanup_pending": true,
				})
			return
		}
	}
	recoveredFromJournal := false
	if request.RecoveryToken != "" && privateData.RelayReservationID == "" &&
		task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure &&
		(task.Status == model.TaskStatusNotStart || privateData.UpstreamTaskID == "") {
		recoveredPrivate, restoreErr := restoreLegacyJimengRecoveryToken(&task, userID, request.RecoveryToken)
		if restoreErr != nil {
			writeJimengTaskError(c, http.StatusBadRequest, "task_recovery_failed", restoreErr.Error())
			return
		}
		privateData = recoveredPrivate
	}
	if !modernRecoveryTerminal && task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure &&
		(task.Status == model.TaskStatusNotStart || privateData.UpstreamTaskID == "") {
		envelope, err := loadJimengRecovery(task.TaskID)
		if err == nil {
			recoveredPrivate, restoreErr := restoreJimengRecovery(&task, userID, envelope)
			if restoreErr != nil {
				writeJimengTaskError(c, http.StatusInternalServerError, "task_recovery_failed", restoreErr.Error())
				return
			}
			privateData = recoveredPrivate
			recoveredFromJournal = true
		} else if !jimengRecoveryNotFound(err) {
			writeJimengTaskError(c, http.StatusInternalServerError, "task_recovery_failed", err.Error())
			return
		}
	}
	hadPendingSettlement := privateData.SettlementPending
	clientClaimActive := false
	defer func() {
		if clientClaimActive && modernOperation != nil {
			_ = releaseClaimedJimengOperation(modernOperation, 0, nil)
		}
	}()
	if hadPendingSettlement {
		var settleErr error
		if modernOperation != nil {
			claimed, ok, claimErr := claimJimengTaskOperationForClient(task.TaskID,
				privateData.RelayReservationID, []string{
					model.JimengTaskOperationDispatching,
					model.JimengTaskOperationSubmitted,
					model.JimengTaskOperationManualReview,
				})
			if claimErr != nil {
				settleErr = claimErr
			} else if !ok {
				c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
				return
			} else {
				modernOperation = claimed
				clientClaimActive = true
				settleErr = settlePendingJimengTaskWithFence(
					&task, &privateData, claimed.State, claimed.LeaseOwner,
				)
				if settleErr == nil {
					if task.Status == model.TaskStatusUnknown {
						clientClaimActive = false
					} else {
						modernOperation.State = model.JimengTaskOperationSubmitted
						modernOperation.SettlementPending = false
					}
				}
			}
		} else {
			settleErr = settlePendingJimengTask(&task, &privateData)
		}
		if settleErr != nil {
			writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_settlement_failed",
				"task accounting is still pending", gin.H{"task_id": task.TaskID, "settlement_pending": true})
			return
		}
		service.CheckAndSendQuotaReminder(userID)
	}
	if privateData.RelayReservationID == "" && (recoveredFromJournal || hadPendingSettlement) {
		if err := removeJimengRecovery(task.TaskID); err != nil {
			common.SysError("remove settled Jimeng recovery " + task.TaskID + ": " + err.Error())
			writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_recovery_cleanup_failed",
				"task accounting committed, but recovery cleanup is still pending", gin.H{
					"task_id": task.TaskID, "recovery_cleanup_pending": true,
				})
			return
		}
	}
	if privateData.RelayReservationID != "" && (recoveredFromJournal || hadPendingSettlement) {
		_ = removeJimengRecovery(task.TaskID)
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
		return
	}
	if privateData.UpstreamTaskID == "" {
		if task.Status == model.TaskStatusUnknown {
			c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
			return
		}
		writeJimengTaskError(c, http.StatusInternalServerError, "task_data_invalid", "task is missing upstream_task_id")
		return
	}
	if modernOperation != nil && !clientClaimActive {
		claimed, ok, claimErr := claimJimengTaskOperationForClient(task.TaskID,
			privateData.RelayReservationID, []string{
				model.JimengTaskOperationSubmitted,
				model.JimengTaskOperationManualReview,
			})
		if claimErr != nil {
			writeJimengTaskError(c, http.StatusInternalServerError, "task_recovery_failed",
				"task recovery state could not be claimed")
			return
		}
		if !ok {
			c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
			return
		}
		modernOperation = claimed
		clientClaimActive = true
	}
	baseURL, channelKey, err := resolveJimengTaskChannel(task, privateData)
	if err != nil {
		writeJimengTaskError(c, http.StatusBadRequest, "channel_not_found", err.Error())
		return
	}
	mappedModel := properties.UpstreamModelName
	if mappedModel == "" {
		mappedModel = properties.OriginModelName
	}
	result, raw, err := (&jimeng.Client{}).Fetch(
		c.Request.Context(), baseURL, channelKey, mappedModel, privateData.UpstreamTaskID,
	)
	result, raw, err = normalizeJimengFetchOutcome(result, raw, err)
	if err != nil {
		writeJimengUpstreamError(c, err)
		return
	}
	previousStatus := task.Status
	if err := applyJimengTaskResult(&task, result, raw, &privateData); err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to encode task state")
		return
	}
	if err := persistJimengTaskResultWithOperation(&task, previousStatus, modernOperation); err != nil {
		common.SysError("persist Jimeng provider result for " + task.TaskID + ": " + err.Error())
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to update task")
		return
	}
	clientClaimActive = false
	c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
}

func normalizeJimengFetchOutcome(
	result *jimeng.TaskResult,
	raw []byte,
	err error,
) (*jimeng.TaskResult, []byte, error) {
	if !jimeng.IsDurableResponseError(err) {
		return result, raw, err
	}
	// The request was only a status fetch; no submit is retried. Replace the
	// unwritable provider-controlled payload with a small deterministic terminal
	// result so Task and operation can close atomically on every SQL dialect.
	terminal := &jimeng.TaskResult{Message: "provider result exceeds durable storage limit"}
	terminal.Data.Status = "durable_failure"
	return terminal, []byte(`{"status":"failure","reason":"provider_result_storage_limit"}`), nil
}

func containsRelayGroup(groups []string, group string) bool {
	for _, candidate := range groups {
		if candidate == group {
			return true
		}
	}
	return false
}

func persistJimengTaskResult(task *model.Task, expectedStatus string) error {
	return persistJimengTaskResultWithOperation(task, expectedStatus, nil)
}

func persistJimengTaskResultWithOperation(
	task *model.Task,
	expectedStatus string,
	claimedOperation *model.JimengTaskOperation,
) error {
	const maxAttempts = 3
	desired := *task
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{name: "task properties", value: desired.Properties, limit: jimengTaskTextMaxBytes},
		{name: "task private data", value: desired.PrivateData, limit: jimengTaskTextMaxBytes},
		{name: "provider response", value: desired.Data, limit: jimengTaskTextMaxBytes},
		{name: "task failure reason", value: desired.FailReason, limit: jimengTaskFailReasonMaxBytes},
	} {
		if err := validateJimengDurableText(field.name, field.value, field.limit); err != nil {
			return err
		}
	}
	desiredPrivate, privateErr := decodeJimengTaskPrivateData(desired.PrivateData)
	if privateErr != nil {
		return fmt.Errorf("decode desired Jimeng task result: %w", privateErr)
	}
	modernTerminal := desiredPrivate.RelayReservationID != "" && isJimengTerminalTaskStatus(desired.Status)
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var rowsAffected int64
		if modernTerminal {
			lastErr = model.DB.Transaction(func(tx *gorm.DB) error {
				update := tx.Model(&model.Task{}).
					Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, expectedStatus).
					Updates(jimengTaskResultUpdates(desired))
				if update.Error != nil {
					return update.Error
				}
				rowsAffected = update.RowsAffected
				if update.RowsAffected != 1 {
					var current model.Task
					if err := tx.First(&current, task.ID).Error; err != nil ||
						!jimengTaskResultMatches(current, desired) {
						return errors.New("Jimeng task state changed concurrently")
					}
					// MySQL reports changed rather than matched rows by default. An
					// exact same-second replay is a successful CAS, not contention.
					rowsAffected = 1
				}
				now, clockErr := model.DatabaseUnixTimestamp(tx)
				if clockErr != nil {
					return clockErr
				}
				operationQuery := tx.Model(&model.JimengTaskOperation{}).
					Where("task_id = ? AND reservation_id = ?", task.TaskID,
						desiredPrivate.RelayReservationID)
				if claimedOperation != nil {
					operationQuery = operationQuery.Where("id = ? AND state = ? AND lease_owner = ?",
						claimedOperation.ID, claimedOperation.State, claimedOperation.LeaseOwner)
				} else {
					operationQuery = operationQuery.Where("state = ? AND lease_owner = ?",
						model.JimengTaskOperationSubmitted, "")
				}
				operation := operationQuery.
					Updates(map[string]any{
						"state": model.JimengTaskOperationTerminal, "settlement_pending": false,
						"encrypted_provider_task_id": "", "completed_at": now,
						"next_attempt_at": 0, "updated_at": now, "last_error": "",
						"lease_owner": "", "lease_expires_at": 0,
					})
				if operation.Error != nil {
					return operation.Error
				}
				if operation.RowsAffected != 1 {
					return errors.New("Jimeng recovery state changed before terminal result")
				}
				return nil
			})
		} else if claimedOperation != nil && desiredPrivate.RelayReservationID != "" {
			lastErr = model.DB.Transaction(func(tx *gorm.DB) error {
				update := tx.Model(&model.Task{}).
					Where("id = ? AND status = ? AND status NOT IN ?", task.ID, expectedStatus,
						[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
					Updates(jimengTaskResultUpdates(desired))
				if update.Error != nil {
					return update.Error
				}
				rowsAffected = update.RowsAffected
				if update.RowsAffected != 1 {
					var current model.Task
					if err := tx.First(&current, task.ID).Error; err != nil ||
						!jimengTaskResultMatches(current, desired) {
						return errors.New("Jimeng task state changed concurrently")
					}
					rowsAffected = 1
				}
				now, clockErr := model.DatabaseUnixTimestamp(tx)
				if clockErr != nil {
					return clockErr
				}
				nextState := model.JimengTaskOperationSubmitted
				nextAttemptAt := now + jimengOperationRetrySeconds
				if claimedOperation.State == model.JimengTaskOperationManualReview {
					nextState = model.JimengTaskOperationManualReview
					nextAttemptAt = 0
				}
				operation := tx.Model(&model.JimengTaskOperation{}).
					Where("id = ? AND task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?",
						claimedOperation.ID, task.TaskID, desiredPrivate.RelayReservationID,
						claimedOperation.State, claimedOperation.LeaseOwner).
					Updates(map[string]any{
						"state": nextState, "next_attempt_at": nextAttemptAt,
						"lease_owner": "", "lease_expires_at": 0,
						"updated_at": now, "last_error": "", "attempts": 0,
					})
				if operation.Error != nil {
					return operation.Error
				}
				if operation.RowsAffected != 1 {
					return service.ErrRelayQuotaReservationBusy
				}
				return nil
			})
		} else {
			update := model.DB.Model(&model.Task{}).
				Where("id = ? AND status = ? AND status NOT IN ?", task.ID, expectedStatus,
					[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
				Updates(jimengTaskResultUpdates(desired))
			lastErr = update.Error
			rowsAffected = update.RowsAffected
			if lastErr == nil && rowsAffected != 1 {
				lastErr = errors.New("Jimeng task state changed concurrently")
			}
		}
		if lastErr == nil && rowsAffected == 1 {
			return nil
		}

		var current model.Task
		if err := model.DB.First(&current, task.ID).Error; err != nil {
			lastErr = errors.Join(lastErr, fmt.Errorf("verify Jimeng task result: %w", err))
		} else if jimengTaskResultMatches(current, desired) &&
			(!modernTerminal || jimengTerminalOperationMatches(task.TaskID, desiredPrivate.RelayReservationID)) &&
			(claimedOperation == nil || modernTerminal ||
				jimengReleasedClientOperationMatches(claimedOperation, desiredPrivate.RelayReservationID)) {
			*task = current
			return nil
		} else if current.Status != expectedStatus {
			// Exact state proves an ambiguous write committed. A different status
			// means another poll won the compare-and-swap, so return its newer state.
			*task = current
			return nil
		}
		if attempt < maxAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Jimeng task result: %w", lastErr)
}

func jimengTaskResultUpdates(task model.Task) map[string]any {
	return map[string]any{
		"status": task.Status, "progress": task.Progress, "fail_reason": task.FailReason,
		"start_time": task.StartTime, "finish_time": task.FinishTime,
		"private_data": task.PrivateData, "data": task.Data, "updated_at": task.UpdatedAt,
	}
}

func isJimengTerminalTaskStatus(status string) bool {
	return status == model.TaskStatusSuccess || status == model.TaskStatusFailure
}

func jimengTerminalOperationMatches(taskID, reservationID string) bool {
	var operation model.JimengTaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil {
		return false
	}
	return operation.State == model.JimengTaskOperationTerminal && !operation.SettlementPending
}

func jimengReleasedClientOperationMatches(
	claimed *model.JimengTaskOperation,
	reservationID string,
) bool {
	if claimed == nil {
		return true
	}
	var operation model.JimengTaskOperation
	if err := model.DB.Where("id = ? AND task_id = ? AND reservation_id = ?", claimed.ID,
		claimed.TaskID, reservationID).First(&operation).Error; err != nil {
		return false
	}
	expectedState := model.JimengTaskOperationSubmitted
	if claimed.State == model.JimengTaskOperationManualReview {
		expectedState = model.JimengTaskOperationManualReview
	}
	return operation.State == expectedState && operation.LeaseOwner == "" &&
		operation.LeaseExpiresAt == 0
}

func jimengTaskResultMatches(current, desired model.Task) bool {
	return current.Status == desired.Status && current.Progress == desired.Progress &&
		current.FailReason == desired.FailReason && current.StartTime == desired.StartTime &&
		current.FinishTime == desired.FinishTime && current.PrivateData == desired.PrivateData &&
		current.Data == desired.Data && current.UpdatedAt == desired.UpdatedAt
}

func applyJimengTaskResult(task *model.Task, result *jimeng.TaskResult, raw []byte, privateData *jimengTaskPrivateData) error {
	if task == nil || result == nil || privateData == nil {
		return errors.New("invalid Jimeng task result")
	}
	if err := validateJimengDurableText("provider response", string(raw), jimengTaskTextMaxBytes); err != nil {
		return err
	}
	providerStatus := strings.ToLower(strings.TrimSpace(result.Data.Status))
	// A syntactically successful provider response with an unfamiliar state is
	// not progress. Returning a fixed, non-provider-controlled error preserves
	// the current Task row and lets the recovery operation's consecutive-failure
	// budget eventually quarantine it instead of resetting attempts forever.
	switch providerStatus {
	case "in_queue", "queued", "generating", "processing", "running", "in_progress",
		"done", "success", "succeeded", "failed", "failure", "error", "durable_failure", "not_found", "expired":
	default:
		return errors.New("unsupported Jimeng provider task status")
	}
	now := common.NowTimestamp()
	task.UpdatedAt = now
	task.Data = string(durableJimengProviderPayload(raw))
	switch providerStatus {
	case "in_queue", "queued":
		task.Status = model.TaskStatusQueued
		task.Progress = "10%"
	case "generating", "processing", "running", "in_progress":
		task.Status = model.TaskStatusRunning
		if task.StartTime == 0 {
			task.StartTime = now
		}
		task.Progress = "50%"
	case "done", "success", "succeeded":
		if len(result.Data.VideoURL) > jimengProviderResultURLMaxBytes {
			// The provider completed the already-paid work, but its result cannot
			// fit the durable task metadata. Close recovery deterministically
			// instead of retrying the same unwritable response forever.
			task.Status = model.TaskStatusFailure
			task.Progress = "100%"
			task.FinishTime = now
			task.FailReason = "provider result exceeds durable storage limit"
			break
		}
		task.Status = model.TaskStatusSuccess
		task.Progress = "100%"
		task.FinishTime = now
		privateData.ResultURL = result.Data.VideoURL
	case "failed", "failure", "error":
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		task.FailReason = "provider reported task failure"
	case "durable_failure":
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		task.FailReason = "provider result exceeds durable storage limit"
	case "not_found":
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		task.FailReason = "provider task was not found"
	case "expired":
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		task.FailReason = "provider task expired"
	}
	if isJimengTerminalTaskStatus(task.Status) {
		stripJimengTerminalRecoverySecrets(privateData)
	}
	privateJSON, err := marshalJimengTaskPrivateData(*privateData)
	if err != nil && (providerStatus == "done" || providerStatus == "success" || providerStatus == "succeeded") {
		// JSON escaping and the already-persisted credential snapshot contribute
		// to the actual TEXT size. Validate the complete encoded blob, not merely
		// the URL's source length. A completed paid task with an unwritable result
		// is closed with a stable failure instead of polling forever.
		privateData.ResultURL = ""
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		task.FailReason = "provider result exceeds durable storage limit"
		stripJimengTerminalRecoverySecrets(privateData)
		privateJSON, err = marshalJimengTaskPrivateData(*privateData)
	}
	if err != nil {
		return err
	}
	task.PrivateData = privateJSON
	return nil
}

func stripJimengTerminalRecoverySecrets(privateData *jimengTaskPrivateData) {
	if privateData == nil {
		return
	}
	privateData.UpstreamTaskID = ""
	privateData.EncryptedUpstreamTaskID = ""
	privateData.ChannelBaseURL = ""
	privateData.EncryptedChannelKey = ""
	privateData.SettlementPending = false
}

func newJimengVideoResponse(task model.Task, modelName string) jimengVideoResponse {
	var properties jimengTaskProperties
	_ = common.UnmarshalJsonStr(task.Properties, &properties)
	var privateData jimengTaskPrivateData
	if isJimengTerminalTaskStatus(task.Status) {
		privateData, _ = decodeJimengTaskPrivateDataStored(task.PrivateData)
	} else {
		privateData, _ = decodeJimengTaskPrivateData(task.PrivateData)
	}
	if modelName == "" {
		modelName = properties.OriginModelName
	}
	response := jimengVideoResponse{
		ID: task.TaskID, TaskID: task.TaskID, Object: "video", Model: modelName,
		Status: jimengVideoStatus(task.Status), Progress: parseTaskProgress(task.Progress),
		CreatedAt: task.CreatedAt,
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		response.CompletedAt = task.FinishTime
	}
	if privateData.ResultURL != "" {
		response.Metadata = map[string]any{"url": privateData.ResultURL}
	}
	if task.Status == model.TaskStatusFailure {
		// Older rows may contain an upstream message in FailReason. Never reflect
		// provider-controlled details (including echoed credentials or IDs).
		response.Error = &jimengVideoError{Message: "Jimeng task failed", Code: "task_failed"}
	}
	return response
}

func decodeJimengTaskMetadata(task model.Task) (jimengTaskProperties, jimengTaskPrivateData) {
	var properties jimengTaskProperties
	_ = common.UnmarshalJsonStr(task.Properties, &properties)
	privateData, _ := decodeJimengTaskPrivateData(task.PrivateData)
	return properties, privateData
}

func decodeJimengTaskMetadataChecked(task model.Task) (jimengTaskProperties, jimengTaskPrivateData, error) {
	var properties jimengTaskProperties
	if err := common.UnmarshalJsonStr(task.Properties, &properties); err != nil {
		return jimengTaskProperties{}, jimengTaskPrivateData{}, fmt.Errorf("decode Jimeng task properties: %w", err)
	}
	privateData, err := decodeJimengTaskPrivateData(task.PrivateData)
	if err != nil {
		return jimengTaskProperties{}, jimengTaskPrivateData{}, fmt.Errorf("decode Jimeng task accounting metadata: %w", err)
	}
	return properties, privateData, nil
}

func decodeJimengTerminalTaskMetadata(task model.Task) (jimengTaskProperties, jimengTaskPrivateData, error) {
	var properties jimengTaskProperties
	if err := common.UnmarshalJsonStr(task.Properties, &properties); err != nil {
		return jimengTaskProperties{}, jimengTaskPrivateData{}, fmt.Errorf("decode Jimeng task properties: %w", err)
	}
	privateData, err := decodeJimengTaskPrivateDataStored(task.PrivateData)
	if err != nil {
		return jimengTaskProperties{}, jimengTaskPrivateData{}, fmt.Errorf("decode terminal Jimeng task metadata: %w", err)
	}
	return properties, privateData, nil
}

func decodeJimengTaskPrivateData(value string) (jimengTaskPrivateData, error) {
	privateData, err := decodeJimengTaskPrivateDataStored(value)
	if err != nil {
		return jimengTaskPrivateData{}, err
	}
	if privateData.EncryptedUpstreamTaskID != "" {
		providerTaskID, err := jimengDecrypt(privateData.EncryptedUpstreamTaskID)
		if err != nil {
			return jimengTaskPrivateData{}, fmt.Errorf("durable Jimeng provider identifier cannot be decrypted: %w", err)
		}
		if strings.TrimSpace(providerTaskID) == "" {
			return jimengTaskPrivateData{}, errors.New("durable Jimeng provider identifier is empty")
		}
		if privateData.UpstreamTaskID != "" && privateData.UpstreamTaskID != providerTaskID {
			return jimengTaskPrivateData{}, errors.New("durable Jimeng provider identifier mismatch")
		}
		privateData.UpstreamTaskID = providerTaskID
	}
	return privateData, nil
}

func decodeJimengTaskPrivateDataStored(value string) (jimengTaskPrivateData, error) {
	var privateData jimengTaskPrivateData
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, jimengTaskPrivateDataV2Prefix) {
		value = strings.TrimPrefix(value, jimengTaskPrivateDataV2Prefix)
	}
	if err := common.UnmarshalJsonStr(value, &privateData); err != nil {
		return jimengTaskPrivateData{}, err
	}
	return privateData, nil
}

func marshalJimengTaskProperties(properties jimengTaskProperties) (string, error) {
	value, err := common.Marshal(properties)
	if err != nil {
		return "", err
	}
	encoded := string(value)
	if err := validateJimengDurableText("task properties", encoded, jimengTaskTextMaxBytes); err != nil {
		return "", err
	}
	return encoded, nil
}

func marshalJimengTaskPrivateData(privateData jimengTaskPrivateData) (string, error) {
	if privateData.HasSpecialGroupRatio && !privateData.HasBillingGroupRatio {
		return "", errors.New("Jimeng special group ratio is missing its billing ratio")
	}
	if privateData.HasBillingGroupRatio && (privateData.BillingGroupRatio < 0 ||
		math.IsNaN(privateData.BillingGroupRatio) || math.IsInf(privateData.BillingGroupRatio, 0)) {
		return "", errors.New("Jimeng billing group ratio is invalid")
	}
	storedPrivateData := privateData
	if privateData.RelayReservationID != "" && strings.TrimSpace(privateData.UpstreamTaskID) != "" {
		if storedPrivateData.EncryptedUpstreamTaskID == "" {
			encryptedProviderTaskID, err := jimengEncrypt(privateData.UpstreamTaskID)
			if err != nil {
				return "", errors.New("encrypt durable Jimeng provider identifier")
			}
			storedPrivateData.EncryptedUpstreamTaskID = encryptedProviderTaskID
		}
		storedPrivateData.UpstreamTaskID = ""
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{name: "provider task id", value: privateData.UpstreamTaskID, limit: jimeng.MaxProviderTaskIDBytes},
		{name: "provider result URL", value: privateData.ResultURL, limit: jimengProviderResultURLMaxBytes},
		{name: "channel base URL", value: privateData.ChannelBaseURL, limit: jimengChannelBaseURLMaxBytes},
		{name: "encrypted channel key", value: privateData.EncryptedChannelKey, limit: jimengEncryptedChannelKeyMaxBytes},
		{name: "encrypted provider task id", value: storedPrivateData.EncryptedUpstreamTaskID, limit: jimengEncryptedChannelKeyMaxBytes},
	} {
		if err := validateJimengDurableText(field.name, field.value, field.limit); err != nil {
			return "", err
		}
	}
	value, err := common.Marshal(storedPrivateData)
	if err != nil {
		return "", err
	}
	encoded := string(value)
	if privateData.RelayReservationID != "" {
		// A non-JSON version prefix makes pre-upgrade binaries fail closed when
		// reading modern durable-reservation tasks. They cannot silently ignore
		// relay_reservation_id, perform the legacy settlement path, and strip
		// recovery metadata while re-marshalling during a rolling deployment.
		encoded = jimengTaskPrivateDataV2Prefix + encoded
	}
	if err := validateJimengDurableText("task private data", encoded, jimengTaskTextMaxBytes); err != nil {
		return "", err
	}
	return encoded, nil
}

func validateJimengDurableText(name, value string, limit int) error {
	if limit <= 0 || len(value) > limit {
		return fmt.Errorf("Jimeng %s exceeds durable storage limit", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("Jimeng %s is not valid UTF-8", name)
	}
	return nil
}

func durableJimengProviderPayload(raw []byte) []byte {
	if len(raw) > jimengTaskTextMaxBytes || !utf8.Valid(raw) {
		// Partial transport errors can return a body even though the client never
		// reached its normal response validation. The recovery marker and stable
		// outcome are sufficient; discard an unwritable diagnostic payload.
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	var decoded struct {
		Code      int    `json:"code"`
		RequestID string `json:"request_id"`
		Data      struct {
			Status string `json:"status"`
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := common.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	canonical := struct {
		Code               int    `json:"code,omitempty"`
		Outcome            string `json:"outcome,omitempty"`
		Status             string `json:"status,omitempty"`
		RequestFingerprint string `json:"request_fingerprint,omitempty"`
	}{Code: decoded.Code, RequestFingerprint: common.NormalizeProviderCorrelationID(decoded.RequestID)}
	if strings.TrimSpace(decoded.Data.TaskID) != "" {
		canonical.Outcome = "accepted"
	} else if decoded.Code != 0 && decoded.Code != 10000 {
		canonical.Outcome = "rejected"
	}
	switch strings.ToLower(strings.TrimSpace(decoded.Data.Status)) {
	case "in_queue", "queued", "generating", "processing", "running", "in_progress",
		"done", "success", "succeeded", "failed", "failure", "error", "durable_failure", "not_found", "expired":
		canonical.Status = strings.ToLower(strings.TrimSpace(decoded.Data.Status))
	}
	encoded, err := common.Marshal(canonical)
	if err != nil || len(encoded) > jimengTaskTextMaxBytes {
		return nil
	}
	return encoded
}

func boundedJimengFailReason(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= jimengTaskFailReasonMaxBytes {
		return value
	}
	cut := jimengTaskFailReasonMaxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func jimengVideoStatus(status string) string {
	switch status {
	case model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusQueued:
		return "queued"
	case model.TaskStatusRunning:
		return "in_progress"
	case model.TaskStatusSuccess:
		return "completed"
	case model.TaskStatusFailure:
		return "failed"
	default:
		return "unknown"
	}
}

func parseTaskProgress(progress string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(progress, "%"))
	return value
}

func jimengChannelBaseURL(channel *model.Channel) string {
	if strings.TrimSpace(channel.BaseURL) != "" {
		return channel.BaseURL
	}
	index := int(constant.ChannelTypeJimeng)
	if index >= 0 && index < len(constant.ChannelBaseURLs) {
		return constant.ChannelBaseURLs[index]
	}
	return ""
}

func writeJimengTaskError(c *gin.Context, status int, code, message string) {
	writeJimengTaskErrorData(c, status, code, message, nil)
}

func writeJimengTaskErrorData(c *gin.Context, status int, code, message string, data any) {
	c.JSON(status, jimengTaskError{Code: code, Message: message, Data: data})
}

func writeJimengUpstreamError(c *gin.Context, err error) {
	if err == nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "fail_to_fetch_task", "failed to call Jimeng provider")
		return
	}
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) {
		writeJimengTaskError(c, upstream.StatusCode, "fail_to_fetch_task", "Jimeng provider request failed")
		return
	}
	var provider *jimeng.ProviderError
	if errors.As(err, &provider) {
		writeJimengTaskError(c, http.StatusInternalServerError, strconv.Itoa(provider.Code), "Jimeng provider rejected the request")
		return
	}
	writeJimengTaskError(c, http.StatusInternalServerError, "do_request_failed", "failed to call Jimeng provider")
}

func safeJimengProviderErrorMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	var submit *jimeng.SubmitError
	if errors.As(err, &submit) {
		return "Jimeng provider submission failed"
	}
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) {
		return "Jimeng provider request failed"
	}
	var provider *jimeng.ProviderError
	if errors.As(err, &provider) {
		return "Jimeng provider rejected the request"
	}
	return fallback
}
