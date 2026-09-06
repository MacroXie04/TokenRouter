package tasks

import (
	"bytes"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/vidu"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
)

type viduProvider interface {
	Submit(context.Context, string, string, *vidu.PreparedRequest) (*vidu.Task, []byte, error)
	Fetch(context.Context, string, string, string) (*vidu.Task, []byte, error)
}

var newViduProvider = func() viduProvider { return &vidu.Client{} }
var newViduContentHTTPClient = vidu.NewHTTPClient

func init() {
	operationssvc.RegisterAsyncTaskPromoter(PromoteViduTaskRecoveryJournalsContext)
	operationssvc.RegisterAsyncTaskReconciler(reconcileAsyncViduTasks)
}

// RelayViduTask submits a Vidu task through the generic reference video
// surfaces. Provider identity, credentials, and pricing are snapshotted before
// dispatch and are never returned to the caller.
func RelayViduTask(c *gin.Context) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, vidu.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeViduTaskError(c, status, "invalid_request", "Vidu request body is invalid or too large", nil)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	userID := requestctx.GetUserId(c)
	token := middleware.GetRelayToken(c)
	groups := middleware.GetTokenGroups(c)
	if userID <= 0 || token == nil {
		writeViduTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	if len(groups) == 0 {
		writeViduTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}
	originModel, err := vidu.RequestedModel(raw, contentType)
	if err != nil {
		writeViduTaskError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if !middleware.RelayModelAllowed(c, originModel) {
		writeViduTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return
	}
	channel, usingGroup, err := selectViduChannel(groups, originModel)
	if err != nil {
		writeViduTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no Vidu channel is available", nil)
		return
	}
	baseURL, err := vidu.EffectiveBaseURL(channel.BaseURL)
	if err != nil {
		writeViduTaskError(c, http.StatusBadRequest, "channel_invalid", err.Error(), nil)
		return
	}
	channelKey := strings.TrimSpace(channelssvc.GetChannelKey(channel))
	if channelKey == "" || strings.ContainsAny(channelKey, "\r\n") {
		writeViduTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "Vidu channel has no valid key", nil)
		return
	}
	mappedModel := relaycommon.GetMappedModel(channel, originModel)
	prepared, err := vidu.PrepareSubmit(raw, contentType, originModel, mappedModel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeViduTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}
	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, originModel, requestctx.GetUserGroup(c), usingGroup,
	)
	if err != nil || !enabled {
		if err != nil {
			logging.SysError("resolve Vidu pricing: " + err.Error())
		}
		writeViduTaskError(c, http.StatusBadRequest, "model_price_error",
			"Vidu model requires an explicit reference fixed price or model ratio", nil)
		return
	}
	quota, err := pricing.PreConsumeQuota()
	if err != nil || quota < 0 {
		writeViduTaskError(c, http.StatusBadRequest, "model_price_error", "Vidu pricing configuration is invalid", nil)
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	propertiesJSON, err := marshalViduTaskProperties(viduTaskProperties{
		Version: viduTaskMetadataVersion, Family: "vidu", Input: prepared.Payload.Prompt,
		OriginModelName: originModel, UpstreamModelName: prepared.UpstreamModel,
		Action: prepared.Action, ImageCount: len(prepared.Payload.Images), Duration: prepared.Payload.Duration,
		Resolution: prepared.Payload.Resolution, Pricing: pricing,
	})
	if err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, viduChannelCredentialBinding(taskID, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect Vidu channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: viduTaskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := viduTaskPrivateData{
		Version: viduTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, Pricing: pricing,
	}
	reservation, err := createViduReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic Vidu reservation: " + err.Error())
		}
		writeViduTaskError(c, status, code, message, nil)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundRejectedViduTask(&task, reservation, "Vidu submission stopped before provider dispatch"); cleanupErr != nil {
			logging.SysError("Vidu reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markViduTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark Vidu dispatch: " + err.Error())
		writeViduTaskError(c, http.StatusInternalServerError, "task_persistence_failed",
			"failed to persist Vidu dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := newViduProvider().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if !vidu.SubmitWasDispatched(submitErr) || definitiveViduRejection(submitErr) {
			if refundErr := refundRejectedViduTask(&task, reservation, "Vidu provider rejected submission"); refundErr != nil {
				logging.SysError("persist rejected Vidu task " + task.TaskID + ": " + refundErr.Error())
				writeViduTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"Vidu submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeViduProviderError(c, submitErr)
			return
		}
		if persistErr := markViduAmbiguousDispatch(&task, reservation.ReservationID(), viduUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist ambiguous Vidu task " + task.TaskID + ": " + persistErr.Error())
			writeViduTaskError(c, http.StatusAccepted, "task_commit_pending",
				"Vidu provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeViduTaskError(c, http.StatusAccepted, "submit_outcome_unknown", viduUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	if provider != nil && provider.Status == vidu.StatusFailed {
		if refundErr := refundRejectedViduTask(&task, reservation, "Vidu provider reported submission failure"); refundErr != nil {
			writeViduTaskError(c, http.StatusAccepted, "task_refund_pending", "Vidu task failed and its quota refund is pending",
				gin.H{"task_id": task.TaskID, "status": "failed"})
			return
		}
		finalized = true
		response, responseErr := viduTaskResponse(&task)
		if responseErr != nil {
			writeViduTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode Vidu task", nil)
			return
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if provider == nil || strings.TrimSpace(provider.ProviderTaskID) == "" {
		if persistErr := markViduAmbiguousDispatch(&task, reservation.ReservationID(), viduUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist Vidu missing-id submission " + task.TaskID + ": " + persistErr.Error())
		}
		writeViduTaskError(c, http.StatusAccepted, "submit_outcome_unknown", viduUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	journalErr := persistAcceptedViduRecoveryJournal(
		&task, reservation.ReservationID(), provider.ProviderTaskID, prepared.Action,
	)
	if journalErr != nil {
		logging.SysError("persist accepted Vidu recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	if err := settleAcceptedViduTask(&task, reservation, provider.ProviderTaskID, model.TaskOperationDispatching, ""); err != nil {
		logging.SysError("settle accepted Vidu task " + task.TaskID + ": " + err.Error())
		fallbackErr := persistAcceptedViduFallback(&task, reservation.ReservationID(), provider.ProviderTaskID)
		data := gin.H{"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil}
		if fallbackErr != nil {
			logging.SysError("persist accepted Vidu fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeViduRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted Vidu recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeViduTaskError(c, http.StatusAccepted, "task_commit_pending",
			"Vidu task was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeViduRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted Vidu recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(viduAuditEventID(reservation.ReservationID())); deliverErr != nil {
		logging.SysError("deliver Vidu consume audit: " + deliverErr.Error())
	}
	response, err := viduTaskResponse(&task)
	if err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode Vidu task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayViduTaskFetch returns only a user-owned local record and never contacts
// Vidu or decrypts the provider credential snapshot.
func RelayViduTaskFetch(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Query("task_id"))
	}
	if err := validateViduTaskPublicID(taskID); err != nil {
		writeViduTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var tasks []model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		requestctx.GetUserId(c), viduTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Vidu task", nil)
		return
	}
	if len(tasks) == 0 {
		writeViduTaskError(c, http.StatusNotFound, "task_not_found", "Vidu task was not found", nil)
		return
	}
	if len(tasks) != 1 {
		writeViduTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Vidu task identity is ambiguous", nil)
		return
	}
	task := &tasks[0]
	properties, err := decodeViduTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeViduTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Vidu task metadata is invalid", nil)
		return
	}
	if !authorizeViduTaskRead(c, task, properties) {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/video/generations/") || c.Request.URL.Path == "/v1/video/fetch" {
		dto, err := viduTaskDTOFromModel(task)
		if err != nil {
			writeViduTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Vidu task data is invalid", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
		return
	}
	response, err := viduTaskResponse(task)
	if err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Vidu task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

func relayViduTaskContent(c *gin.Context, task *model.Task) {
	properties, err := decodeViduTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeViduTaskError(c, http.StatusInternalServerError, "server_error", "Vidu task metadata is invalid", nil)
		return
	}
	if !authorizeViduTaskRead(c, task, properties) {
		return
	}
	if task.Status != model.TaskStatusSuccess {
		writeViduTaskError(c, http.StatusBadRequest, "invalid_request_error",
			"Vidu task is not completed; current status is "+task.Status, nil)
		return
	}
	data, err := decodeViduStoredTaskData(task.Data)
	if err != nil || viduResultURL(data) == "" {
		writeViduTaskError(c, http.StatusInternalServerError, "server_error", "Vidu result URL is unavailable", nil)
		return
	}
	ctx, cancel := contextWithVideoTimeout(c.Request.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, viduResultURL(data), nil)
	if err != nil {
		writeViduTaskError(c, http.StatusInternalServerError, "server_error", "Vidu result URL is invalid", nil)
		return
	}
	request.Header.Set("Accept", "video/*, application/octet-stream")
	response, err := newViduContentHTTPClient().Do(request)
	if err != nil {
		writeViduTaskError(c, http.StatusBadGateway, "server_error", "failed to fetch Vidu video content", nil)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeViduTaskError(c, http.StatusBadGateway, "server_error", "Vidu content provider returned an error", nil)
		return
	}
	const maxContentBytes int64 = 512 << 20
	if response.ContentLength > maxContentBytes {
		writeViduTaskError(c, http.StatusBadGateway, "server_error", "Vidu video content is too large", nil)
		return
	}
	contentType, safe := safeVideoContentType(response.Header.Get("Content-Type"))
	if !safe {
		writeViduTaskError(c, http.StatusBadGateway, "server_error", "Vidu content provider returned an unsafe content type", nil)
		return
	}
	copyVideoContentHeaders(c.Writer.Header(), response.Header)
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(response.StatusCode)
	if _, err := io.Copy(c.Writer, io.LimitReader(response.Body, maxContentBytes)); err != nil {
		logging.SysError("stream Vidu content for " + task.TaskID + ": " + err.Error())
	}
}

func selectViduChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 && channelcatalog.ChannelType(channel.Type) == channelcatalog.ChannelTypeVidu {
			return channel, group, nil
		}
		if channel != nil {
			ignored[channel.Id] = struct{}{}
		}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func authorizeViduTaskRead(c *gin.Context, task *model.Task, properties viduTaskProperties) bool {
	if middleware.GetRelayToken(c) == nil {
		return true
	}
	if !containsVideoGroup(middleware.GetTokenGroups(c), task.Group) {
		writeViduTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group", nil)
		return false
	}
	if !middleware.RelayModelAllowed(c, properties.OriginModelName) {
		writeViduTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveViduRejection(err error) bool {
	var upstream *relaycommon.UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 &&
		upstream.StatusCode != http.StatusRequestTimeout && upstream.StatusCode != http.StatusConflict
}

func writeViduProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode <= 599 {
		status = upstream.StatusCode
	}
	writeViduTaskError(c, status, "upstream_error", "Vidu provider rejected the request", nil)
}

func writeViduTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{"message": boundedViduFailReason(message),
		"type": "invalid_request_error", "code": code}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}
