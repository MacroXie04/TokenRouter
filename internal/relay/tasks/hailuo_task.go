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
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/hailuo"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
)

type hailuoProvider interface {
	Submit(context.Context, string, string, *hailuo.PreparedRequest) (*hailuo.Task, []byte, error)
	Fetch(context.Context, string, string, string) (*hailuo.Task, []byte, error)
}

var newHailuoProvider = func() hailuoProvider { return &hailuo.Client{} }
var newHailuoContentHTTPClient = hailuo.NewHTTPClient

// RelayHailuoTask submits a Hailuo task through the generic reference video
// surfaces. Provider identity, credentials, and pricing are snapshotted before
// dispatch and are never returned to the caller.
func RelayHailuoTask(c *gin.Context, state relaycommon.RequestState) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, hailuo.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeHailuoTaskError(c, status, "invalid_request", "Hailuo request body is invalid or too large", nil)
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
		writeHailuoTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	if len(groups) == 0 {
		writeHailuoTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}
	originModel, err := hailuo.RequestedModel(raw, contentType)
	if err != nil {
		writeHailuoTaskError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if !state.Allows(originModel) {
		writeHailuoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return
	}
	channel, usingGroup, err := selectHailuoChannel(groups, originModel)
	if err != nil {
		writeHailuoTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no Hailuo channel is available", nil)
		return
	}
	baseURL, err := hailuo.EffectiveBaseURL(channel.BaseURL)
	if err != nil {
		writeHailuoTaskError(c, http.StatusBadRequest, "channel_invalid", err.Error(), nil)
		return
	}
	channelKey := strings.TrimSpace(channelssvc.GetChannelKey(channel))
	if channelKey == "" || strings.ContainsAny(channelKey, "\r\n") {
		writeHailuoTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "Hailuo channel has no valid key", nil)
		return
	}
	mappedModel := relaycommon.GetMappedModel(channel, originModel)
	prepared, err := hailuo.PrepareSubmit(raw, contentType, originModel, mappedModel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeHailuoTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}
	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, originModel, state.UserGroup, usingGroup,
	)
	if err != nil || !enabled {
		if err != nil {
			logging.SysError("resolve Hailuo pricing: " + err.Error())
		}
		writeHailuoTaskError(c, http.StatusBadRequest, "model_price_error",
			"Hailuo model requires an explicit reference fixed price or model ratio", nil)
		return
	}
	quota, err := pricing.PreConsumeQuota()
	if err != nil || quota < 0 {
		writeHailuoTaskError(c, http.StatusBadRequest, "model_price_error", "Hailuo pricing configuration is invalid", nil)
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	propertiesJSON, err := marshalHailuoTaskProperties(hailuoTaskProperties{
		Version: hailuoTaskMetadataVersion, Family: "hailuo", Input: prepared.Prompt,
		OriginModelName: originModel, UpstreamModelName: prepared.UpstreamModel,
		Action: prepared.Action, HasInputReference: prepared.HasInputReference, Duration: prepared.Duration,
		Resolution: prepared.Resolution, Pricing: pricing,
	})
	if err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, hailuoChannelCredentialBinding(taskID, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect Hailuo channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: hailuoTaskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := hailuoTaskPrivateData{
		Version: hailuoTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricing,
	}
	reservation, err := createHailuoReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic Hailuo reservation: " + err.Error())
		}
		writeHailuoTaskError(c, status, code, message, nil)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundRejectedHailuoTask(&task, reservation, "Hailuo submission stopped before provider dispatch"); cleanupErr != nil {
			logging.SysError("Hailuo reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markHailuoTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark Hailuo dispatch: " + err.Error())
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_persistence_failed",
			"failed to persist Hailuo dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := newHailuoProvider().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if !hailuo.SubmitWasDispatched(submitErr) || definitiveHailuoRejection(submitErr) {
			if refundErr := refundRejectedHailuoTask(&task, reservation, "Hailuo provider rejected submission"); refundErr != nil {
				logging.SysError("persist rejected Hailuo task " + task.TaskID + ": " + refundErr.Error())
				writeHailuoTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"Hailuo submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeHailuoProviderError(c, submitErr)
			return
		}
		if persistErr := markHailuoAmbiguousDispatch(&task, reservation.ReservationID(), hailuoUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist ambiguous Hailuo task " + task.TaskID + ": " + persistErr.Error())
			writeHailuoTaskError(c, http.StatusAccepted, "task_commit_pending",
				"Hailuo provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeHailuoTaskError(c, http.StatusAccepted, "submit_outcome_unknown", hailuoUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	if provider != nil && provider.Status == hailuo.StatusFailed {
		if refundErr := refundRejectedHailuoTask(&task, reservation, "Hailuo provider reported submission failure"); refundErr != nil {
			writeHailuoTaskError(c, http.StatusAccepted, "task_refund_pending", "Hailuo task failed and its quota refund is pending",
				gin.H{"task_id": task.TaskID, "status": "failed"})
			return
		}
		finalized = true
		response, responseErr := hailuoTaskResponse(&task)
		if responseErr != nil {
			writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode Hailuo task", nil)
			return
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if provider == nil || strings.TrimSpace(provider.ProviderTaskID) == "" {
		if persistErr := markHailuoAmbiguousDispatch(&task, reservation.ReservationID(), hailuoUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist Hailuo missing-id submission " + task.TaskID + ": " + persistErr.Error())
		}
		writeHailuoTaskError(c, http.StatusAccepted, "submit_outcome_unknown", hailuoUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	journalErr := persistAcceptedHailuoRecoveryJournal(
		&task, reservation.ReservationID(), provider.ProviderTaskID, prepared.Action,
	)
	if journalErr != nil {
		logging.SysError("persist accepted Hailuo recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	if err := settleAcceptedHailuoTask(&task, reservation, provider.ProviderTaskID, model.TaskOperationDispatching, ""); err != nil {
		logging.SysError("settle accepted Hailuo task " + task.TaskID + ": " + err.Error())
		fallbackErr := persistAcceptedHailuoFallback(&task, reservation.ReservationID(), provider.ProviderTaskID)
		data := gin.H{"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil}
		if fallbackErr != nil {
			logging.SysError("persist accepted Hailuo fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeHailuoRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted Hailuo recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeHailuoTaskError(c, http.StatusAccepted, "task_commit_pending",
			"Hailuo task was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeHailuoRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted Hailuo recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(hailuoAuditEventID(reservation.ReservationID())); deliverErr != nil {
		logging.SysError("deliver Hailuo consume audit: " + deliverErr.Error())
	}
	response, err := hailuoTaskResponse(&task)
	if err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode Hailuo task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayHailuoTaskFetch returns only a user-owned local record and never contacts
// Hailuo or decrypts the provider credential snapshot.
func RelayHailuoTaskFetch(c *gin.Context, state relaycommon.RequestState) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Query("task_id"))
	}
	if err := validateHailuoTaskPublicID(taskID); err != nil {
		writeHailuoTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var tasks []model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, hailuoTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Hailuo task", nil)
		return
	}
	if len(tasks) == 0 {
		writeHailuoTaskError(c, http.StatusNotFound, "task_not_found", "Hailuo task was not found", nil)
		return
	}
	if len(tasks) != 1 {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Hailuo task identity is ambiguous", nil)
		return
	}
	task := &tasks[0]
	properties, err := decodeHailuoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Hailuo task metadata is invalid", nil)
		return
	}
	if !authorizeHailuoTaskRead(c, state, task, properties) {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/video/generations/") || c.Request.URL.Path == "/v1/video/fetch" {
		dto, err := hailuoTaskDTOFromModel(task)
		if err != nil {
			writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Hailuo task data is invalid", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
		return
	}
	response, err := hailuoTaskResponse(task)
	if err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Hailuo task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

func relayHailuoTaskContent(c *gin.Context, state relaycommon.RequestState, task *model.Task) {
	properties, err := decodeHailuoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeHailuoTaskError(c, http.StatusInternalServerError, "server_error", "Hailuo task metadata is invalid", nil)
		return
	}
	if !authorizeHailuoTaskRead(c, state, task, properties) {
		return
	}
	if task.Status != model.TaskStatusSuccess {
		writeHailuoTaskError(c, http.StatusBadRequest, "invalid_request_error",
			"Hailuo task is not completed; current status is "+task.Status, nil)
		return
	}
	data, err := decodeHailuoStoredTaskData(task.Data)
	if err != nil || hailuoResultURL(data) == "" {
		writeHailuoTaskError(c, http.StatusInternalServerError, "server_error", "Hailuo result URL is unavailable", nil)
		return
	}
	ctx, cancel := contextWithVideoTimeout(c.Request.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, hailuoResultURL(data), nil)
	if err != nil {
		writeHailuoTaskError(c, http.StatusInternalServerError, "server_error", "Hailuo result URL is invalid", nil)
		return
	}
	request.Header.Set("Accept", "video/*, application/octet-stream")
	response, err := newHailuoContentHTTPClient().Do(request)
	if err != nil {
		writeHailuoTaskError(c, http.StatusBadGateway, "server_error", "failed to fetch Hailuo video content", nil)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeHailuoTaskError(c, http.StatusBadGateway, "server_error", "Hailuo content provider returned an error", nil)
		return
	}
	const maxContentBytes int64 = 512 << 20
	if response.ContentLength > maxContentBytes {
		writeHailuoTaskError(c, http.StatusBadGateway, "server_error", "Hailuo video content is too large", nil)
		return
	}
	contentType, safe := safeVideoContentType(response.Header.Get("Content-Type"))
	if !safe {
		writeHailuoTaskError(c, http.StatusBadGateway, "server_error", "Hailuo content provider returned an unsafe content type", nil)
		return
	}
	copyVideoContentHeaders(c.Writer.Header(), response.Header)
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(response.StatusCode)
	if _, err := io.Copy(c.Writer, io.LimitReader(response.Body, maxContentBytes)); err != nil {
		logging.SysError("stream Hailuo content for " + task.TaskID + ": " + err.Error())
	}
}

func selectHailuoChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 && channelcatalog.ChannelType(channel.Type) == channelcatalog.ChannelTypeMiniMax {
			return channel, group, nil
		}
		if channel != nil {
			ignored[channel.Id] = struct{}{}
		}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func authorizeHailuoTaskRead(c *gin.Context, state relaycommon.RequestState, task *model.Task, properties hailuoTaskProperties) bool {
	if state.Token == nil {
		return true
	}
	if !containsVideoGroup(state.Groups, task.Group) {
		writeHailuoTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group", nil)
		return false
	}
	if !state.Allows(properties.OriginModelName) {
		writeHailuoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveHailuoRejection(err error) bool {
	var upstream *relaycommon.UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 &&
		upstream.StatusCode != http.StatusRequestTimeout && upstream.StatusCode != http.StatusConflict
}

func writeHailuoProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode <= 599 {
		status = upstream.StatusCode
	}
	writeHailuoTaskError(c, status, "upstream_error", "Hailuo provider rejected the request", nil)
}

func writeHailuoTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{"message": boundedHailuoFailReason(message),
		"type": "invalid_request_error", "code": code}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}
