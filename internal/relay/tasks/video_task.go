package tasks

import (
	"bytes"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/sora"
	aliWan "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/ali"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/doubao"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/hailuo"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/vidu"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RelayVideoTask submits OpenAI-compatible video creation and remix requests.
// All identifiers returned to the caller are server-generated public task IDs.
func RelayVideoTask(c *gin.Context, state relaycommon.RequestState) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, sora.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeVideoTaskError(c, status, "invalid_request", "video request body is invalid or too large", nil)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	if strings.HasSuffix(c.Request.URL.Path, "/remix") && declaredVeoTaskModel(raw, contentType) != "" {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request",
			"Veo video remix is not supported", nil)
		return
	}
	if !strings.HasSuffix(c.Request.URL.Path, "/remix") {
		if modelName := aliWan.DeclaredModel(raw, contentType); aliWan.IsModel(modelName) {
			c.Request.Body = io.NopCloser(bytes.NewReader(raw))
			RelayAliWanTask(c, state)
			return
		}
		if relayVeoTask(c, state, raw, contentType) {
			return
		}
		if modelName := hailuo.DeclaredModel(raw, contentType); hailuo.IsModel(modelName) {
			c.Request.Body = io.NopCloser(bytes.NewReader(raw))
			RelayHailuoTask(c, state)
			return
		}
		if modelName, modelErr := doubao.RequestedModel(raw); modelErr == nil && doubao.IsModel(modelName) {
			c.Request.Body = io.NopCloser(bytes.NewReader(raw))
			RelayDoubaoTask(c, state)
			return
		}
		if modelName, modelErr := vidu.RequestedModel(raw, contentType); modelErr == nil && vidu.IsModel(modelName) {
			c.Request.Body = io.NopCloser(bytes.NewReader(raw))
			RelayViduTask(c, state)
			return
		}
	}

	userID := state.UserID
	token := state.Token
	if userID <= 0 || token == nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	groups := state.Groups
	if len(groups) == 0 {
		writeVideoTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}

	remix := strings.HasSuffix(c.Request.URL.Path, "/remix")
	var (
		originModel      string
		originProviderID string
		originPublicID   string
		inheritedSeconds int
		inheritedSize    string
		channel          *model.Channel
		usingGroup       string
		baseURL          string
		channelKey       string
		mappedModel      string
	)
	if remix {
		originPublicID = strings.TrimSpace(c.Param("video_id"))
		if err := validateVideoTaskPublicID(originPublicID); err != nil {
			writeVideoTaskError(c, http.StatusBadRequest, "invalid_request", "video_id is invalid", nil)
			return
		}
		var origin model.Task
		if err := model.DB.Where("task_id = ? AND user_id = ?", originPublicID, userID).First(&origin).Error; err != nil {
			status := http.StatusInternalServerError
			code := "task_query_failed"
			message := "failed to query origin video task"
			if errors.Is(err, gorm.ErrRecordNotFound) {
				status, code, message = http.StatusBadRequest, "task_not_exist", "origin video task does not exist"
			}
			writeVideoTaskError(c, status, code, message, nil)
			return
		}
		if !isVideoTaskPlatform(&origin) || origin.Status != model.TaskStatusSuccess {
			writeVideoTaskError(c, http.StatusBadRequest, "task_not_ready", "origin video task is not completed", nil)
			return
		}
		if !containsVideoGroup(groups, origin.Group) {
			writeVideoTaskError(c, http.StatusForbidden, "group_not_allowed", "origin task group is no longer allowed", nil)
			return
		}
		properties, err := decodeVideoTaskProperties(origin.Properties)
		if err != nil {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin task metadata is invalid", nil)
			return
		}
		privateData, err := decodeVideoTaskPrivateData(origin.PrivateData)
		if err != nil || privateData.EncryptedUpstreamTaskID == "" {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin provider task id is unavailable", nil)
			return
		}
		originProviderID, err = asyncTaskDecryptBound(
			privateData.EncryptedUpstreamTaskID,
			videoProviderTaskBinding(origin.TaskID, privateData.RelayReservationID, origin.UserId, origin.ChannelId),
		)
		if err != nil {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin provider task id cannot be decrypted", nil)
			return
		}
		originModel = properties.OriginModelName
		inheritedSeconds, inheritedSize = properties.Seconds, properties.Size
		if !state.Allows(originModel) {
			writeVideoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
			return
		}
		channel, err = channelssvc.GetChannelByID(origin.ChannelId)
		if err != nil || channel.Status != channelcatalog.ChannelStatusEnabled || !videoChannelSupported(channel) {
			writeVideoTaskError(c, http.StatusBadRequest, "task_channel_disable", "origin task channel is unavailable", nil)
			return
		}
		var abilityCount int64
		if err := model.DB.Model(&model.Ability{}).
			Where(&model.Ability{
				ChannelId: channel.Id, Group: origin.Group, Model: originModel, Enabled: true,
			}).
			Count(&abilityCount).Error; err != nil {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to verify origin video channel", nil)
			return
		}
		if abilityCount != 1 {
			writeVideoTaskError(c, http.StatusBadRequest, "task_channel_disable", "origin task channel is unavailable", nil)
			return
		}
		baseURL, err = sora.EffectiveBaseURL(privateData.ChannelBaseURL, channel.Type)
		if err != nil {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin channel snapshot is invalid", nil)
			return
		}
		channelKey, err = asyncTaskDecryptBound(
			privateData.EncryptedChannelKey,
			videoChannelCredentialBinding(origin.TaskID, origin.UserId, origin.ChannelId, privateData.ChannelBaseURL),
		)
		if err != nil || strings.TrimSpace(channelKey) == "" {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin channel snapshot cannot be decrypted", nil)
			return
		}
		mappedModel = properties.UpstreamModelName
		usingGroup = origin.Group
	} else {
		originModel, err = sora.RequestedModel(raw, contentType)
		if err != nil {
			writeVideoTaskError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		if !state.Allows(originModel) {
			writeVideoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
			return
		}
		channel, usingGroup, err = selectVideoChannel(groups, originModel)
		if err != nil {
			writeVideoTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no video channel is available", nil)
			return
		}
		baseURL, err = sora.EffectiveBaseURL(channel.BaseURL, channel.Type)
		if err != nil {
			writeVideoTaskError(c, http.StatusBadRequest, "channel_invalid", err.Error(), nil)
			return
		}
		channelKey = strings.TrimSpace(channelssvc.GetChannelKey(channel))
		if channelKey == "" {
			writeVideoTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "video channel has no available key", nil)
			return
		}
		mappedModel = relaycommon.GetMappedModel(channel, originModel)
	}
	prepared, err := sora.PrepareSubmit(
		raw, contentType, originModel, mappedModel, remix, inheritedSeconds, inheritedSize,
	)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeVideoTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}

	multiplier := decimal.NewFromInt(int64(prepared.Seconds))
	if prepared.Size == "1792x1024" || prepared.Size == "1024x1792" {
		highResolution, _ := decimal.NewFromString("1.666667")
		multiplier = multiplier.Mul(highResolution)
	}
	quota, err := billingsvc.ComputePerCallQuotaMultiplierForUser(
		originModel, state.UserGroup, usingGroup, multiplier,
	)
	if err != nil {
		writeVideoTaskError(c, http.StatusBadRequest, "model_price_error", err.Error(), nil)
		return
	}
	freeModel := billingsvc.ShouldSkipPerCallFreeModelPreConsume(
		originModel, state.UserGroup, usingGroup,
	)
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	properties := videoTaskProperties{
		Input: prepared.Prompt, UpstreamModelName: mappedModel, OriginModelName: originModel,
		Seconds: prepared.Seconds, Size: prepared.Size, RemixedFromVideoID: originPublicID,
		HasInputReference: prepared.HasInputReference,
	}
	propertiesJSON, err := marshalVideoTaskProperties(properties)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey,
		videoChannelCredentialBinding(taskID, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect video channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	taskPlatform := strconv.Itoa(channel.Type)
	if !validVideoTaskPlatform(taskPlatform) {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "video channel platform is invalid", nil)
		return
	}
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: taskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: videoTaskAction(remix, prepared.HasInputReference), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := videoTaskPrivateData{
		ChannelBaseURL: baseURL, EncryptedChannelKey: encryptedKey, FreeModel: freeModel,
	}
	reservation, err := createVideoReservedTask(&task, token, &privateData)
	if err != nil {
		status := http.StatusInternalServerError
		code := "pre_consume_failed"
		message := "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic video reservation: " + err.Error())
		}
		writeVideoTaskError(c, status, code, message, nil)
		return
	}
	dispatched := false
	finalized := false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundRejectedVideoTask(&task, reservation, "submission aborted before provider dispatch"); cleanupErr != nil {
			logging.SysError("video reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markVideoTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark video dispatch: " + err.Error())
		writeVideoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to persist video dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := (&sora.Client{}).Submit(
		c.Request.Context(), baseURL, channelKey, task.TaskID, originProviderID, prepared, remix,
	)
	if submitErr != nil {
		if definitiveVideoRejection(submitErr) {
			if refundErr := refundRejectedVideoTask(&task, reservation, "video provider rejected submission"); refundErr != nil {
				logging.SysError("persist rejected video task " + task.TaskID + ": " + refundErr.Error())
				writeVideoTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"video submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeVideoProviderError(c, submitErr)
			return
		}
		if settleErr := settleUnknownVideoDispatch(
			&task, reservation, videoUnknownDispatchReason, model.TaskOperationDispatching, "",
		); settleErr != nil {
			logging.SysError("persist ambiguous video task " + task.TaskID + ": " + settleErr.Error())
			writeVideoTaskError(c, http.StatusAccepted, "task_commit_pending",
				"video provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeVideoTaskError(c, http.StatusAccepted, "submit_outcome_unknown",
			videoUnknownDispatchReason, gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	providerTaskID := strings.TrimSpace(provider.ID)
	if providerTaskID == "" {
		providerTaskID = strings.TrimSpace(provider.TaskID)
	}
	journalErr := persistAcceptedVideoRecoveryJournal(
		&task, reservation.ReservationID(), providerTaskID,
	)
	if journalErr != nil {
		logging.SysError("persist accepted video recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	if err := settleAcceptedVideoTask(
		&task, reservation, provider, providerTaskID, model.TaskOperationDispatching, "",
	); err != nil {
		logging.SysError("settle accepted video task " + task.TaskID + ": " + err.Error())
		fallbackErr := persistAcceptedVideoFallback(&task, reservation.ReservationID(), providerTaskID, provider)
		data := gin.H{
			"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil,
		}
		if fallbackErr != nil {
			logging.SysError("persist accepted video fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeVideoRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted video recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeVideoTaskError(c, http.StatusAccepted, "task_commit_pending",
			"video was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeVideoRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted video recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if deliverErr := billingsvc.DeliverAuditLogOutboxEvent("video:" + reservation.ReservationID()); deliverErr != nil {
		logging.SysError("deliver video consume audit: " + deliverErr.Error())
	}
	if task.Status == model.TaskStatusFailure {
		operation, loadErr := loadVideoTaskOperation(task.TaskID)
		if loadErr != nil || reverseFailedVideoTask(&task, operation, provider, task.FailReason) != nil {
			writeVideoTaskError(c, http.StatusAccepted, "task_refund_pending",
				"video failed and its quota refund is pending", gin.H{"task_id": task.TaskID, "status": "failed"})
			return
		}
	}
	response, err := videoTaskResponse(&task)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode video task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayVideoTaskFetch returns a user-owned task in either the legacy task DTO
// envelope or the OpenAI video object, based on the requested route.
func RelayVideoTaskFetch(c *gin.Context, state relaycommon.RequestState) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Query("task_id"))
	}
	if err := validateVideoTaskPublicID(taskID); err != nil {
		writeVideoTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var doubaoTasks []model.Task
	doubaoLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform IN ?", taskID,
		state.UserID, model.DoubaoVideoTaskOperationPlatforms()).Limit(2).Find(&doubaoTasks)
	if doubaoLookup.Error != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Doubao task", nil)
		return
	}
	if len(doubaoTasks) > 0 {
		if len(doubaoTasks) != 1 || !isDoubaoTask(&doubaoTasks[0]) {
			writeDoubaoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Doubao task identity is ambiguous", nil)
			return
		}
		RelayDoubaoTaskFetch(c, state)
		return
	}
	var aliWanTasks []model.Task
	aliWanLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, aliWanTaskPlatform).Limit(2).Find(&aliWanTasks)
	if aliWanLookup.Error != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Alibaba Wan task", nil)
		return
	}
	if len(aliWanTasks) > 0 {
		if len(aliWanTasks) != 1 || !isAliWanTask(&aliWanTasks[0]) {
			writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Alibaba Wan task identity is ambiguous", nil)
			return
		}
		RelayAliWanTaskFetch(c, state)
		return
	}
	var viduTask model.Task
	viduLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, viduTaskPlatform).First(&viduTask).Error
	if viduLookup == nil {
		RelayViduTaskFetch(c, state)
		return
	}
	if !errors.Is(viduLookup, gorm.ErrRecordNotFound) {
		writeViduTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Vidu task", nil)
		return
	}
	var geminiVeoTasks []model.Task
	geminiVeoLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform IN ?", taskID,
		state.UserID, model.VeoTaskOperationPlatforms()).Limit(2).Find(&geminiVeoTasks)
	if geminiVeoLookup.Error != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Gemini Veo task", nil)
		return
	}
	if len(geminiVeoTasks) > 0 {
		if len(geminiVeoTasks) != 1 || !isGeminiVeoTask(&geminiVeoTasks[0]) {
			writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Gemini Veo task identity is ambiguous", nil)
			return
		}
		RelayGeminiVeoTaskFetch(c, state)
		return
	}
	var hailuoTasks []model.Task
	hailuoLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, hailuoTaskPlatform).Limit(2).Find(&hailuoTasks)
	if hailuoLookup.Error != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Hailuo task", nil)
		return
	}
	if len(hailuoTasks) > 0 {
		if len(hailuoTasks) != 1 || !isHailuoTask(&hailuoTasks[0]) {
			writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Hailuo task identity is ambiguous", nil)
			return
		}
		RelayHailuoTaskFetch(c, state)
		return
	}
	var task model.Task
	err := model.DB.Where("task_id = ? AND user_id = ?", taskID, state.UserID).First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeVideoTaskError(c, http.StatusNotFound, "task_not_found", "video task was not found", nil)
			return
		}
		writeVideoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query video task", nil)
		return
	}
	if !isVideoTaskPlatform(&task) {
		writeVideoTaskError(c, http.StatusNotFound, "task_not_found", "video task was not found", nil)
		return
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "video task metadata is invalid", nil)
		return
	}
	if !authorizeVideoTaskRead(c, state, &task, properties) {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/video/generations/") || c.Request.URL.Path == "/v1/video/fetch" {
		dto, err := videoTaskDTOFromModel(&task)
		if err != nil {
			writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "video task data is invalid", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
		return
	}
	response, err := videoTaskResponse(&task)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "video task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// VideoProxy streams a completed user-owned video from the original provider
// using the encrypted channel snapshot captured before submission.
func VideoProxy(c *gin.Context, state relaycommon.RequestState) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	taskID := strings.TrimSpace(c.Param("task_id"))
	if err := validateVideoTaskPublicID(taskID); err != nil {
		writeVideoTaskError(c, http.StatusBadRequest, "invalid_request_error", "task_id is invalid", nil)
		return
	}
	var doubaoTasks []model.Task
	doubaoLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform IN ?", taskID,
		state.UserID, model.DoubaoVideoTaskOperationPlatforms()).Limit(2).Find(&doubaoTasks)
	if doubaoLookup.Error != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "server_error", "failed to query Doubao task", nil)
		return
	}
	if len(doubaoTasks) > 0 {
		if len(doubaoTasks) != 1 || !isDoubaoTask(&doubaoTasks[0]) {
			writeDoubaoTaskError(c, http.StatusInternalServerError, "server_error", "Doubao task identity is ambiguous", nil)
			return
		}
		relayDoubaoTaskContent(c, state, &doubaoTasks[0])
		return
	}
	var aliWanTasks []model.Task
	aliWanLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, aliWanTaskPlatform).Limit(2).Find(&aliWanTasks)
	if aliWanLookup.Error != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "server_error", "failed to query Alibaba Wan task", nil)
		return
	}
	if len(aliWanTasks) > 0 {
		if len(aliWanTasks) != 1 || !isAliWanTask(&aliWanTasks[0]) {
			writeAliWanTaskError(c, http.StatusInternalServerError, "server_error", "Alibaba Wan task identity is ambiguous", nil)
			return
		}
		relayAliWanTaskContent(c, state, &aliWanTasks[0])
		return
	}
	var viduTask model.Task
	viduLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, viduTaskPlatform).First(&viduTask).Error
	if viduLookup == nil {
		relayViduTaskContent(c, state, &viduTask)
		return
	}
	if !errors.Is(viduLookup, gorm.ErrRecordNotFound) {
		writeViduTaskError(c, http.StatusInternalServerError, "server_error", "failed to query Vidu task", nil)
		return
	}
	var geminiVeoTasks []model.Task
	geminiVeoLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform IN ?", taskID,
		state.UserID, model.VeoTaskOperationPlatforms()).Limit(2).Find(&geminiVeoTasks)
	if geminiVeoLookup.Error != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "failed to query Gemini Veo task", nil)
		return
	}
	if len(geminiVeoTasks) > 0 {
		if len(geminiVeoTasks) != 1 || !isGeminiVeoTask(&geminiVeoTasks[0]) {
			writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Gemini Veo task identity is ambiguous", nil)
			return
		}
		relayGeminiVeoTaskContent(c, state, &geminiVeoTasks[0])
		return
	}
	var hailuoTasks []model.Task
	hailuoLookup := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, hailuoTaskPlatform).Limit(2).Find(&hailuoTasks)
	if hailuoLookup.Error != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "server_error", "failed to query Hailuo task", nil)
		return
	}
	if len(hailuoTasks) > 0 {
		if len(hailuoTasks) != 1 || !isHailuoTask(&hailuoTasks[0]) {
			writeHailuoTaskError(c, http.StatusInternalServerError, "server_error", "Hailuo task identity is ambiguous", nil)
			return
		}
		relayHailuoTaskContent(c, state, &hailuoTasks[0])
		return
	}
	var task model.Task
	err := model.DB.Where("task_id = ? AND user_id = ?", taskID, state.UserID).First(&task).Error
	if err != nil || !isVideoTaskPlatform(&task) {
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			writeVideoTaskError(c, http.StatusInternalServerError, "server_error", "failed to query video task", nil)
			return
		}
		writeVideoTaskError(c, http.StatusNotFound, "invalid_request_error", "video task was not found", nil)
		return
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "server_error", "video task metadata is invalid", nil)
		return
	}
	if !authorizeVideoTaskRead(c, state, &task, properties) {
		return
	}
	if task.Status != model.TaskStatusSuccess {
		writeVideoTaskError(c, http.StatusBadRequest, "invalid_request_error",
			"video task is not completed; current status is "+task.Status, nil)
		return
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedUpstreamTaskID == "" {
		writeVideoTaskError(c, http.StatusInternalServerError, "server_error", "video recovery metadata is unavailable", nil)
		return
	}
	providerTaskID, err := asyncTaskDecryptBound(
		privateData.EncryptedUpstreamTaskID,
		videoProviderTaskBinding(task.TaskID, privateData.RelayReservationID, task.UserId, task.ChannelId),
	)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "server_error", "video provider identifier cannot be decrypted", nil)
		return
	}
	channelKey, err := asyncTaskDecryptBound(
		privateData.EncryptedChannelKey,
		videoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL),
	)
	if err != nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "server_error", "video channel credential cannot be decrypted", nil)
		return
	}
	ctx, cancel := contextWithVideoTimeout(c.Request.Context())
	defer cancel()
	response, err := (&sora.Client{}).Content(ctx, privateData.ChannelBaseURL, channelKey, providerTaskID)
	if err != nil {
		logging.SysError("fetch video content for " + task.TaskID + ": " + err.Error())
		writeVideoTaskError(c, http.StatusBadGateway, "server_error", "failed to fetch video content", nil)
		return
	}
	defer response.Body.Close()
	contentType, safe := safeVideoContentType(response.Header.Get("Content-Type"))
	if !safe {
		writeVideoTaskError(c, http.StatusBadGateway, "server_error", "video provider returned an unsafe content type", nil)
		return
	}
	copyVideoContentHeaders(c.Writer.Header(), response.Header)
	c.Header("Content-Type", contentType)
	if contentType == "application/octet-stream" && c.Writer.Header().Get("Content-Disposition") == "" {
		c.Header("Content-Disposition", "attachment")
	}
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(response.StatusCode)
	if _, err := io.Copy(c.Writer, response.Body); err != nil {
		logging.SysError("stream video content for " + task.TaskID + ": " + err.Error())
	}
}

func selectVideoChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if videoChannelSupported(channel) {
			return channel, group, nil
		}
		ignored[channel.Id] = struct{}{}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func videoChannelSupported(channel *model.Channel) bool {
	if channel == nil || channel.Id <= 0 {
		return false
	}
	typeID := channelcatalog.ChannelType(channel.Type)
	return typeID == channelcatalog.ChannelTypeSora || typeID == channelcatalog.ChannelTypeOpenAI
}

func containsVideoGroup(groups []string, group string) bool {
	for _, candidate := range groups {
		if candidate == group {
			return true
		}
	}
	return false
}

func authorizeVideoTaskRead(c *gin.Context, state relaycommon.RequestState, task *model.Task, properties videoTaskProperties) bool {
	if state.Token == nil {
		// Dashboard sessions and dashboard PATs are owner-wide identities. The
		// task lookup has already enforced ownership.
		return true
	}
	if !containsVideoGroup(state.Groups, task.Group) {
		writeVideoTaskError(c, http.StatusForbidden, "group_not_allowed",
			"token is not allowed to access this task group", nil)
		return false
	}
	if !state.Allows(properties.OriginModelName) {
		writeVideoTaskError(c, http.StatusForbidden, "model_not_allowed",
			"token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveVideoRejection(err error) bool {
	var upstream *relaycommon.UpstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	return upstream.StatusCode >= 400 && upstream.StatusCode < 500 && upstream.StatusCode != http.StatusRequestTimeout &&
		upstream.StatusCode != http.StatusConflict
}

func loadVideoTaskOperation(taskID string) (*model.TaskOperation, error) {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND platform IN ?", taskID, videoTaskPlatforms()).
		First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func writeVideoProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode <= 599 {
		status = upstream.StatusCode
	}
	writeVideoTaskError(c, status, "upstream_error", "video provider rejected the request", nil)
}

func writeVideoTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{
		"message": boundedVideoFailReason(message), "type": "invalid_request_error", "code": code,
	}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}

func copyVideoContentHeaders(destination, source http.Header) {
	for _, name := range []string{
		"Content-Type", "Content-Length", "Content-Disposition", "ETag", "Last-Modified",
		"Accept-Ranges", "Content-Range",
	} {
		for _, value := range source.Values(name) {
			if len(value) <= 4096 && !strings.ContainsAny(value, "\r\n") {
				destination.Add(name, value)
			}
		}
	}
}

func safeVideoContentType(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "application/octet-stream", true
	}
	if len(value) > 255 || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", false
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "video/") {
		return value, true
	}
	if mediaType == "application/octet-stream" {
		return mediaType, true
	}
	return "", false
}

func contextWithVideoTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 60*time.Second)
}
