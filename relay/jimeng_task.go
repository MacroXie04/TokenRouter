package relay

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const jimengTaskPlatform = "47"

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
	UpstreamTaskID    string `json:"upstream_task_id,omitempty"`
	ResultURL         string `json:"result_url,omitempty"`
	BillingSource     string `json:"billing_source,omitempty"`
	SubscriptionID    int    `json:"subscription_id,omitempty"`
	FundingReserved   int    `json:"funding_reserved,omitempty"`
	TokenID           int    `json:"token_id,omitempty"`
	TokenReserved     bool   `json:"token_reserved,omitempty"`
	SettlementPending bool   `json:"settlement_pending,omitempty"`
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
	start := time.Now()
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
	group := getRelayGroup(c)
	quota, err := service.ComputePerCallQuota(originModel, group, billingUnits)
	if err != nil {
		writeJimengTaskError(c, http.StatusBadRequest, "model_price_error", err.Error())
		return
	}

	token := middleware.GetRelayToken(c)
	tokenReserved := false
	if token != nil && !token.UnlimitedQuota {
		if err := service.ReserveTokenQuota(token.Id, quota); err != nil {
			writeJimengTaskError(c, http.StatusBadRequest, "insufficient_quota", "令牌额度不足")
			return
		}
		tokenReserved = true
	}
	userID := common.GetUserId(c)
	funding, err := service.NewFundingSession(userID, quota)
	if err != nil {
		if tokenReserved {
			_ = service.RefundTokenQuotaReservation(token.Id, quota)
		}
		message := "预扣费失败: " + err.Error()
		code := "pre_consume_failed"
		status := http.StatusInternalServerError
		if service.IsSubscriptionFundingErr(err) {
			message = "订阅额度不足或未配置订阅: " + err.Error()
			code = "insufficient_quota"
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrInsufficientQuota) {
			message = "用户额度不足"
			code = "insufficient_quota"
			status = http.StatusBadRequest
		}
		writeJimengTaskError(c, status, code, message)
		return
	}
	providerMayHaveAccepted := false
	defer func() {
		if providerMayHaveAccepted {
			return
		}
		funding.Refund()
		if tokenReserved {
			_ = service.RefundTokenQuotaReservation(token.Id, quota)
		}
	}()

	now := common.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: model.GenerateTaskID(),
		Platform: jimengTaskPlatform, UserId: userID, Group: group, Quota: quota,
		Action: "generate", Status: model.TaskStatusNotStart, SubmitTime: now,
		Progress: "0%", Data: "null",
	}
	if task.Properties, err = marshalJimengTaskProperties(jimengTaskProperties{
		Input: request.Prompt, OriginModelName: originModel,
	}); err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error())
		return
	}
	reservation := funding.Reservation()
	privateData := jimengTaskPrivateData{
		BillingSource: reservation.Source, SubscriptionID: reservation.SubscriptionId,
		FundingReserved: reservation.Reserved, TokenReserved: tokenReserved,
	}
	if token != nil {
		privateData.TokenID = token.Id
	}
	if task.PrivateData, err = marshalJimengTaskPrivateData(privateData); err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error())
		return
	}
	if err := model.DB.Create(&task).Error; err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to create task")
		return
	}

	retryTimes := common.GetEnvInt("RETRY_TIMES", setting.GetOptionIntOrDefault(setting.RetryTimesOption, 0))
	preferredChannelID, affinityFound := service.GetPreferredChannelByAffinity(c, originModel, group, rawBody)
	ignore := make(map[int]struct{}, retryTimes+1)
	initialChannelID := 0
	var selected *model.Channel
	var upstreamResult *jimeng.SubmitResult
	var upstreamRaw []byte
	var mappedModel string
	var lastErr error
	client := &jimeng.Client{}
	for attempt := 0; attempt <= retryTimes; attempt++ {
		channel, usedAffinity, selectErr := service.GetSatisfiedChannelWithPreferred(group, originModel, preferredChannelID, ignore, nil)
		if selectErr != nil {
			lastErr = selectErr
			break
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
		if err := model.DB.Model(&task).Updates(map[string]any{
			"channel_id": channel.Id, "properties": properties, "updated_at": common.NowTimestamp(),
		}).Error; err != nil {
			lastErr = err
			break
		}
		baseURL := jimengChannelBaseURL(channel)
		selected = channel
		upstreamResult, upstreamRaw, lastErr = client.Submit(c.Request.Context(), baseURL, service.GetChannelKey(channel), payload)
		if lastErr == nil {
			providerMayHaveAccepted = true
			break
		}
		if isAmbiguousJimengSubmitError(lastErr) {
			providerMayHaveAccepted = true
		}
		// Jimeng exposes no idempotency key, so retrying after dispatch could
		// create a second billable provider task even when this response failed.
		break
	}
	reservedTokenID := 0
	if tokenReserved {
		reservedTokenID = token.Id
	}
	if lastErr != nil || selected == nil || upstreamResult == nil {
		message := "failed to submit Jimeng task"
		if lastErr != nil {
			message = lastErr.Error()
		}
		if providerMayHaveAccepted && selected != nil {
			privateJSON, marshalErr := marshalJimengTaskPrivateData(privateData)
			if marshalErr == nil {
				marshalErr = commitJimengAcceptedTask(
					&task, funding, reservedTokenID, quota, selected.Id, privateJSON,
					upstreamRaw, model.TaskStatusUnknown, message,
				)
			}
			if marshalErr != nil {
				common.SysError("persist ambiguous Jimeng task " + task.TaskID + ": " + marshalErr.Error())
				writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_commit_failed",
					"Jimeng may have accepted the task, but local accounting could not be committed",
					gin.H{"task_id": task.TaskID, "status": "unknown"})
				return
			}
			service.CheckAndSendQuotaReminder(userID)
			logOther := funding.BillingLogFields()
			logOther["task_id"] = task.TaskID
			logOther["task_platform"] = jimengTaskPlatform
			logOther["task_billing_units"] = billingUnits
			logOther["provider_outcome"] = "unknown"
			service.RecordConsumeLog(
				userID, common.GetUsername(c), common.GetString(c, common.ContextKeyTokenName), originModel,
				0, 0, quota, int(time.Since(start).Milliseconds()), false, selected.Id, group,
				c.ClientIP(), common.GetRequestId(c), "", common.GetInt(c, common.ContextKeyTokenId), logOther,
			)
			service.RecordChannelAffinity(c, initialChannelID, selected.Id)
			writeJimengTaskErrorData(c, http.StatusBadGateway, "submit_outcome_unknown", message,
				gin.H{"task_id": task.TaskID, "status": "unknown"})
			return
		}
		_ = model.DB.Model(&task).Updates(map[string]any{
			"status": model.TaskStatusFailure, "fail_reason": message,
			"finish_time": common.NowTimestamp(), "progress": "100%", "data": string(upstreamRaw),
			"updated_at": common.NowTimestamp(),
		}).Error
		writeJimengUpstreamError(c, lastErr)
		return
	}

	privateData.UpstreamTaskID = upstreamResult.Data.TaskID
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	if err != nil {
		writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_commit_failed", err.Error(),
			gin.H{"task_id": task.TaskID, "status": "accepted"})
		return
	}
	if err := commitJimengAcceptedTask(
		&task, funding, reservedTokenID, quota, selected.Id, privateJSON,
		upstreamRaw, model.TaskStatusSubmitted, "",
	); err != nil {
		privateData.SettlementPending = true
		pendingJSON, marshalErr := marshalJimengTaskPrivateData(privateData)
		if marshalErr == nil {
			marshalErr = persistJimengPendingSettlement(
				&task, selected.Id, pendingJSON, upstreamRaw, model.TaskStatusSubmitted, "",
			)
		}
		common.SysError("commit accepted Jimeng task " + task.TaskID + ": " + err.Error())
		data := gin.H{"task_id": task.TaskID, "status": "accepted", "settlement_pending": marshalErr == nil}
		if marshalErr != nil {
			common.SysError("unrecovered Jimeng upstream task " + upstreamResult.Data.TaskID +
				" for " + task.TaskID + ": " + marshalErr.Error())
			data["provider_request_id"] = upstreamResult.RequestID
		} else {
			logOther := funding.BillingLogFields()
			logOther["task_id"] = task.TaskID
			logOther["task_platform"] = jimengTaskPlatform
			logOther["task_billing_units"] = billingUnits
			logOther["billing_pending"] = true
			service.RecordConsumeLog(
				userID, common.GetUsername(c), common.GetString(c, common.ContextKeyTokenName), originModel,
				0, 0, quota, int(time.Since(start).Milliseconds()), false, selected.Id, group,
				c.ClientIP(), common.GetRequestId(c), upstreamResult.RequestID,
				common.GetInt(c, common.ContextKeyTokenId), logOther,
			)
			service.RecordChannelAffinity(c, initialChannelID, selected.Id)
		}
		writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_commit_failed",
			"Jimeng accepted the task, but local accounting could not be committed", data)
		return
	}

	service.CheckAndSendQuotaReminder(userID)
	logOther := funding.BillingLogFields()
	logOther["task_id"] = task.TaskID
	logOther["task_platform"] = jimengTaskPlatform
	logOther["task_billing_units"] = billingUnits
	service.RecordConsumeLog(
		userID, common.GetUsername(c), common.GetString(c, common.ContextKeyTokenName), originModel,
		0, 0, quota, int(time.Since(start).Milliseconds()), false, selected.Id, group,
		c.ClientIP(), common.GetRequestId(c), upstreamResult.RequestID,
		common.GetInt(c, common.ContextKeyTokenId), logOther,
	)
	service.RecordChannelAffinity(c, initialChannelID, selected.Id)
	c.JSON(http.StatusOK, newJimengVideoResponse(task, originModel))
}

func commitJimengAcceptedTask(
	task *model.Task,
	funding *service.FundingSession,
	tokenId, quota, channelId int,
	privateJSON string,
	upstreamRaw []byte,
	status, failReason string,
) error {
	const maxAttempts = 3
	updatedAt := common.NowTimestamp()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := funding.CommitAcceptedPerCall(quota, tokenId, func(tx *gorm.DB) (bool, error) {
			var current model.Task
			if err := tx.First(&current, task.ID).Error; err != nil {
				return false, err
			}
			_, existingPrivate := decodeJimengTaskMetadata(current)
			_, expectedPrivate := decodeJimengTaskMetadata(model.Task{PrivateData: privateJSON})
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
		})
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
	updatedAt := common.NowTimestamp()
	_, expectedPrivate := decodeJimengTaskMetadata(model.Task{PrivateData: privateJSON})
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
		if result.Error == nil {
			var current model.Task
			if err := model.DB.First(&current, task.ID).Error; err == nil {
				_, existingPrivate := decodeJimengTaskMetadata(current)
				if current.Status == status && current.ChannelId == channelId &&
					existingPrivate.SettlementPending &&
					existingPrivate.UpstreamTaskID == expectedPrivate.UpstreamTaskID {
					*task = current
					return nil
				}
			}
		}
		if attempt == maxAttempts-1 {
			if result.Error != nil {
				return result.Error
			}
			return errors.New("failed to persist pending Jimeng settlement")
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	return errors.New("failed to persist pending Jimeng settlement")
}

func settlePendingJimengTask(task *model.Task, privateData *jimengTaskPrivateData) error {
	funding, err := service.RestoreFundingSession(task.UserId, service.FundingReservation{
		Source: privateData.BillingSource, Reserved: privateData.FundingReserved,
		SubscriptionId: privateData.SubscriptionID,
	})
	if err != nil {
		return err
	}
	privateData.SettlementPending = false
	privateJSON, err := marshalJimengTaskPrivateData(*privateData)
	if err != nil {
		return err
	}
	tokenId := 0
	if privateData.TokenReserved {
		tokenId = privateData.TokenID
	}
	if err := commitJimengAcceptedTask(
		task, funding, tokenId, task.Quota, task.ChannelId, privateJSON,
		[]byte(task.Data), task.Status, task.FailReason,
	); err != nil {
		privateData.SettlementPending = true
		return err
	}
	_, updatedPrivate := decodeJimengTaskMetadata(*task)
	*privateData = updatedPrivate
	return nil
}

func isAmbiguousJimengSubmitError(err error) bool {
	if err == nil {
		return false
	}
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) {
		return false
	}
	var provider *jimeng.ProviderError
	return !errors.As(err, &provider)
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
	properties, privateData := decodeJimengTaskMetadata(task)
	if privateData.SettlementPending {
		if err := settlePendingJimengTask(&task, &privateData); err != nil {
			writeJimengTaskErrorData(c, http.StatusInternalServerError, "task_settlement_failed",
				"task accounting is still pending", gin.H{"task_id": task.TaskID, "settlement_pending": true})
			return
		}
		service.CheckAndSendQuotaReminder(userID)
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
		return
	}
	if privateData.UpstreamTaskID == "" {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_data_invalid", "task is missing upstream_task_id")
		return
	}
	var channel model.Channel
	if err := model.DB.First(&channel, task.ChannelId).Error; err != nil {
		writeJimengTaskError(c, http.StatusBadRequest, "channel_not_found", "task channel not found")
		return
	}
	if channel.Status != constant.ChannelStatusEnabled || channel.Type != int(constant.ChannelTypeJimeng) {
		writeJimengTaskError(c, http.StatusBadRequest, "task_channel_disable", "the channel of the origin task is disabled")
		return
	}
	mappedModel := properties.UpstreamModelName
	if mappedModel == "" {
		mappedModel = relaycommon.GetMappedModel(&channel, properties.OriginModelName)
	}
	result, raw, err := (&jimeng.Client{}).Fetch(
		c.Request.Context(), jimengChannelBaseURL(&channel), service.GetChannelKey(&channel),
		mappedModel, privateData.UpstreamTaskID,
	)
	if err != nil {
		writeJimengUpstreamError(c, err)
		return
	}
	applyJimengTaskResult(&task, result, raw, &privateData)
	if err := persistJimengTaskResult(&task); err != nil {
		writeJimengTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to update task")
		return
	}
	c.JSON(http.StatusOK, newJimengVideoResponse(task, properties.OriginModelName))
}

func persistJimengTaskResult(task *model.Task) error {
	update := model.DB.Model(&model.Task{}).
		Where("id = ? AND status NOT IN ?", task.ID, []string{model.TaskStatusSuccess, model.TaskStatusFailure}).
		Updates(map[string]any{
			"status": task.Status, "progress": task.Progress, "fail_reason": task.FailReason,
			"start_time": task.StartTime, "finish_time": task.FinishTime,
			"private_data": task.PrivateData, "data": task.Data, "updated_at": task.UpdatedAt,
		})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		return model.DB.First(task, task.ID).Error
	}
	return nil
}

func applyJimengTaskResult(task *model.Task, result *jimeng.TaskResult, raw []byte, privateData *jimengTaskPrivateData) {
	now := common.NowTimestamp()
	task.UpdatedAt = now
	task.Data = string(raw)
	switch strings.ToLower(result.Data.Status) {
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
		task.Status = model.TaskStatusSuccess
		task.Progress = "100%"
		task.FinishTime = now
		privateData.ResultURL = result.Data.VideoURL
	case "failed", "failure", "error":
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		task.FailReason = result.Message
	default:
		task.Status = model.TaskStatusUnknown
	}
	task.PrivateData, _ = marshalJimengTaskPrivateData(*privateData)
}

func newJimengVideoResponse(task model.Task, modelName string) jimengVideoResponse {
	properties, privateData := decodeJimengTaskMetadata(task)
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
		response.Error = &jimengVideoError{Message: task.FailReason, Code: "task_failed"}
	}
	return response
}

func decodeJimengTaskMetadata(task model.Task) (jimengTaskProperties, jimengTaskPrivateData) {
	var properties jimengTaskProperties
	var privateData jimengTaskPrivateData
	_ = common.UnmarshalJsonStr(task.Properties, &properties)
	_ = common.UnmarshalJsonStr(task.PrivateData, &privateData)
	return properties, privateData
}

func marshalJimengTaskProperties(properties jimengTaskProperties) (string, error) {
	value, err := common.Marshal(properties)
	return string(value), err
}

func marshalJimengTaskPrivateData(privateData jimengTaskPrivateData) (string, error) {
	value, err := common.Marshal(privateData)
	return string(value), err
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
		writeJimengTaskError(c, upstream.StatusCode, "fail_to_fetch_task", upstream.Body)
		return
	}
	var provider *jimeng.ProviderError
	if errors.As(err, &provider) {
		writeJimengTaskError(c, http.StatusInternalServerError, strconv.Itoa(provider.Code), provider.Message)
		return
	}
	writeJimengTaskError(c, http.StatusInternalServerError, "do_request_failed", err.Error())
}
