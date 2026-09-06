package tasks

import (
	"bytes"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	aliWan "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/ali"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
)

type aliWanProvider interface {
	Submit(context.Context, string, string, *aliWan.PreparedRequest) (*aliWan.Task, []byte, error)
	Fetch(context.Context, string, string, string) (*aliWan.Task, []byte, error)
}

var newAliWanProvider = func() aliWanProvider { return &aliWan.Client{} }
var newAliWanContentHTTPClient = aliWan.NewHTTPClient

// RelayAliWanTask submits a AliWan task through the generic reference video
// surfaces. Provider identity, credentials, and pricing are snapshotted before
// dispatch and are never returned to the caller.
func RelayAliWanTask(c *gin.Context, state relaycommon.RequestState) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, aliWan.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeAliWanTaskError(c, status, "invalid_request", "AliWan request body is invalid or too large", nil)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	userID := state.UserID
	token := state.Token
	groups := state.Groups
	if userID <= 0 || token == nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	if len(groups) == 0 {
		writeAliWanTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}
	originModel, err := aliWan.RequestedModel(raw, contentType)
	if err != nil {
		writeAliWanTaskError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if !state.Allows(originModel) {
		writeAliWanTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return
	}
	channel, usingGroup, err := selectAliWanChannel(groups, originModel)
	if err != nil {
		writeAliWanTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no AliWan channel is available", nil)
		return
	}
	baseURL, err := aliWan.EffectiveBaseURL(channel.BaseURL)
	if err != nil {
		writeAliWanTaskError(c, http.StatusBadRequest, "channel_invalid", err.Error(), nil)
		return
	}
	channelKey := strings.TrimSpace(channelssvc.GetChannelKey(channel))
	if channelKey == "" || len(channelKey) > 8<<10 || strings.ContainsAny(channelKey, "\r\n\x00") {
		writeAliWanTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "AliWan channel has no valid key", nil)
		return
	}
	mappedModel := relaycommon.GetMappedModel(channel, originModel)
	prepared, err := aliWan.PrepareSubmit(raw, contentType, originModel, mappedModel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeAliWanTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}
	action := aliWanActionForPrepared(prepared)
	pricingResolution := prepared.Resolution
	if pricingResolution == "" {
		pricingResolution = prepared.Size
	}
	basePricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, originModel, state.UserGroup, usingGroup,
	)
	if err != nil || !enabled {
		if err != nil {
			logging.SysError("resolve AliWan pricing: " + err.Error())
		}
		writeAliWanTaskError(c, http.StatusBadRequest, "model_price_error",
			"AliWan model requires an explicit reference fixed price or model ratio", nil)
		return
	}
	pricing, err := newAliWanPricingSnapshot(
		basePricing, prepared.UpstreamModel, prepared.Duration, pricingResolution,
	)
	if err != nil {
		writeAliWanTaskError(c, http.StatusBadRequest, "model_price_error", "Alibaba Wan pricing configuration is invalid", nil)
		return
	}
	quota, err := pricing.quota(prepared.UpstreamModel, pricingResolution)
	if err != nil || quota < 0 {
		writeAliWanTaskError(c, http.StatusBadRequest, "model_price_error", "AliWan pricing configuration is invalid", nil)
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	propertiesJSON, err := marshalAliWanTaskProperties(aliWanTaskProperties{
		Version: aliWanTaskMetadataVersion, Family: "ali_wan", Input: prepared.Prompt,
		OriginModelName: originModel, UpstreamModelName: prepared.UpstreamModel,
		Action: action, HasInputReference: prepared.HasInputReference, Duration: prepared.Duration,
		Resolution: pricingResolution, Pricing: pricing,
	})
	if err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, aliWanChannelCredentialBinding(taskID, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect AliWan channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: aliWanTaskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := aliWanTaskPrivateData{
		Version: aliWanTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricing,
	}
	reservation, err := createAliWanReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic AliWan reservation: " + err.Error())
		}
		writeAliWanTaskError(c, status, code, message, nil)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundRejectedAliWanTask(&task, reservation, "AliWan submission stopped before provider dispatch"); cleanupErr != nil {
			logging.SysError("AliWan reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markAliWanTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark AliWan dispatch: " + err.Error())
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_persistence_failed",
			"failed to persist AliWan dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := newAliWanProvider().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if !aliWan.SubmitWasDispatched(submitErr) || definitiveAliWanRejection(submitErr) {
			if refundErr := refundRejectedAliWanTask(&task, reservation, "AliWan provider rejected submission"); refundErr != nil {
				logging.SysError("persist rejected AliWan task " + task.TaskID + ": " + refundErr.Error())
				writeAliWanTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"AliWan submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeAliWanProviderError(c, submitErr)
			return
		}
		if persistErr := markAliWanAmbiguousDispatch(&task, reservation.ReservationID(), aliWanUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist ambiguous AliWan task " + task.TaskID + ": " + persistErr.Error())
			writeAliWanTaskError(c, http.StatusAccepted, "task_commit_pending",
				"AliWan provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeAliWanTaskError(c, http.StatusAccepted, "submit_outcome_unknown", aliWanUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	if provider != nil && provider.Status == aliWan.StatusFailed {
		if refundErr := refundRejectedAliWanTask(&task, reservation, "AliWan provider reported submission failure"); refundErr != nil {
			writeAliWanTaskError(c, http.StatusAccepted, "task_refund_pending", "AliWan task failed and its quota refund is pending",
				gin.H{"task_id": task.TaskID, "status": "failed"})
			return
		}
		finalized = true
		response, responseErr := aliWanTaskResponse(&task)
		if responseErr != nil {
			writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode AliWan task", nil)
			return
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if provider == nil || strings.TrimSpace(provider.ProviderTaskID) == "" {
		if persistErr := markAliWanAmbiguousDispatch(&task, reservation.ReservationID(), aliWanUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist AliWan missing-id submission " + task.TaskID + ": " + persistErr.Error())
		}
		writeAliWanTaskError(c, http.StatusAccepted, "submit_outcome_unknown", aliWanUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	journalErr := persistAcceptedAliWanRecoveryJournal(
		&task, reservation.ReservationID(), provider.ProviderTaskID, action,
	)
	if journalErr != nil {
		logging.SysError("persist accepted AliWan recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	if err := settleAcceptedAliWanTask(&task, reservation, provider.ProviderTaskID, model.TaskOperationDispatching, ""); err != nil {
		logging.SysError("settle accepted AliWan task " + task.TaskID + ": " + err.Error())
		fallbackErr := persistAcceptedAliWanFallback(&task, reservation.ReservationID(), provider.ProviderTaskID)
		data := gin.H{"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil}
		if fallbackErr != nil {
			logging.SysError("persist accepted AliWan fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeAliWanRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted AliWan recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeAliWanTaskError(c, http.StatusAccepted, "task_commit_pending",
			"AliWan task was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeAliWanRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted AliWan recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(aliWanAuditEventID(reservation.ReservationID())); deliverErr != nil {
		logging.SysError("deliver AliWan consume audit: " + deliverErr.Error())
	}
	response, err := aliWanTaskResponse(&task)
	if err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode AliWan task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayAliWanTaskFetch returns only a user-owned local record and never contacts
// AliWan or decrypts the provider credential snapshot.
func RelayAliWanTaskFetch(c *gin.Context, state relaycommon.RequestState) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Query("task_id"))
	}
	if err := validateAliWanTaskPublicID(taskID); err != nil {
		writeAliWanTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var tasks []model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, aliWanTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query AliWan task", nil)
		return
	}
	if len(tasks) == 0 {
		writeAliWanTaskError(c, http.StatusNotFound, "task_not_found", "AliWan task was not found", nil)
		return
	}
	if len(tasks) != 1 {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "AliWan task identity is ambiguous", nil)
		return
	}
	task := &tasks[0]
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "AliWan task metadata is invalid", nil)
		return
	}
	if !authorizeAliWanTaskRead(c, state, task, properties) {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/video/generations/") || c.Request.URL.Path == "/v1/video/fetch" {
		dto, err := aliWanTaskDTOFromModel(task)
		if err != nil {
			writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "AliWan task data is invalid", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
		return
	}
	response, err := aliWanTaskResponse(task)
	if err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "task_data_invalid", "AliWan task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

func relayAliWanTaskContent(c *gin.Context, state relaycommon.RequestState, task *model.Task) {
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeAliWanTaskError(c, http.StatusInternalServerError, "server_error", "AliWan task metadata is invalid", nil)
		return
	}
	if !authorizeAliWanTaskRead(c, state, task, properties) {
		return
	}
	if task.Status != model.TaskStatusSuccess {
		writeAliWanTaskError(c, http.StatusBadRequest, "invalid_request_error",
			"AliWan task is not completed; current status is "+task.Status, nil)
		return
	}
	data, err := decodeAliWanStoredTaskData(task.Data)
	if err != nil || aliWanResultURL(data) == "" {
		writeAliWanTaskError(c, http.StatusInternalServerError, "server_error", "AliWan result URL is unavailable", nil)
		return
	}
	ctx, cancel := contextWithVideoTimeout(c.Request.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, aliWanResultURL(data), nil)
	if err != nil {
		writeAliWanTaskError(c, http.StatusInternalServerError, "server_error", "AliWan result URL is invalid", nil)
		return
	}
	request.Header.Set("Accept", "video/*, application/octet-stream")
	response, err := newAliWanContentHTTPClient().Do(request)
	if err != nil {
		writeAliWanTaskError(c, http.StatusBadGateway, "server_error", "failed to fetch AliWan video content", nil)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeAliWanTaskError(c, http.StatusBadGateway, "server_error", "AliWan content provider returned an error", nil)
		return
	}
	const maxContentBytes int64 = 512 << 20
	if response.ContentLength > maxContentBytes {
		writeAliWanTaskError(c, http.StatusBadGateway, "server_error", "AliWan video content is too large", nil)
		return
	}
	contentType, safe := safeVideoContentType(response.Header.Get("Content-Type"))
	if !safe {
		writeAliWanTaskError(c, http.StatusBadGateway, "server_error", "AliWan content provider returned an unsafe content type", nil)
		return
	}
	copyVideoContentHeaders(c.Writer.Header(), response.Header)
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(response.StatusCode)
	if _, err := io.Copy(c.Writer, io.LimitReader(response.Body, maxContentBytes)); err != nil {
		logging.SysError("stream AliWan content for " + task.TaskID + ": " + err.Error())
	}
}

func selectAliWanChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 && channelcatalog.ChannelType(channel.Type) == channelcatalog.ChannelTypeAli {
			return channel, group, nil
		}
		if channel != nil {
			ignored[channel.Id] = struct{}{}
		}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func authorizeAliWanTaskRead(c *gin.Context, state relaycommon.RequestState, task *model.Task, properties aliWanTaskProperties) bool {
	if state.Token == nil {
		return true
	}
	if !containsVideoGroup(state.Groups, task.Group) {
		writeAliWanTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group", nil)
		return false
	}
	if !state.Allows(properties.OriginModelName) {
		writeAliWanTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveAliWanRejection(err error) bool {
	var upstream *relaycommon.UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 &&
		upstream.StatusCode != http.StatusRequestTimeout && upstream.StatusCode != http.StatusConflict
}

func writeAliWanProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode <= 599 {
		status = upstream.StatusCode
	}
	writeAliWanTaskError(c, status, "upstream_error", "AliWan provider rejected the request", nil)
}

func writeAliWanTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{"message": boundedAliWanFailReason(message),
		"type": "invalid_request_error", "code": code}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}
