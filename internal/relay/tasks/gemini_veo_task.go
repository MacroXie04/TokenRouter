package tasks

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
)

type geminiVeoProvider interface {
	Submit(context.Context, string, string, *geminiVeo.PreparedRequest) (*geminiVeo.Task, []byte, error)
	Fetch(context.Context, string, string, string) (*geminiVeo.Task, []byte, error)
	Content(context.Context, string, string, string) (*http.Response, error)
}

var newGeminiVeoProvider = func() geminiVeoProvider { return &geminiVeo.Client{} }

func init() {
	RegisterVeoTaskProvider(geminiVeoProviderDescriptor())
	operationssvc.RegisterAsyncTaskPromoter(PromoteGeminiVeoTaskRecoveryJournalsContext)
	operationssvc.RegisterAsyncTaskReconciler(reconcileAsyncGeminiVeoTasks)
}

// RelayGeminiVeoTask submits through the exact Gemini API channel selected by
// the shared Veo dispatcher. It deliberately never performs a second channel
// selection because Vertex advertises the same model names.
func RelayGeminiVeoTask(c *gin.Context, selection VeoTaskChannelSelection) {
	relayVeoTaskWithProvider(c, selection)
}

func relayVeoTaskWithProvider(c *gin.Context, selection VeoTaskChannelSelection) {
	raw := selection.RawRequest
	contentType := normalizedVeoContentType(selection.ContentType)
	if len(raw) == 0 || len(raw) > geminiVeo.MaxRequestBodyBytes || selection.Channel == nil || selection.Channel.Id <= 0 {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request", "Veo channel selection is invalid", nil)
		return
	}
	descriptor, registered := veoTaskProviderByChannelType(channelcatalog.ChannelType(selection.Channel.Type))
	if !registered {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request", "Veo channel selection is invalid", nil)
		return
	}
	userID := requestctx.GetUserId(c)
	token := middleware.GetRelayToken(c)
	groups := middleware.GetTokenGroups(c)
	if userID <= 0 || token == nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	if len(groups) == 0 {
		writeGeminiVeoTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}
	originModel, err := geminiVeo.RequestedModel(raw, contentType)
	if err != nil || originModel != selection.OriginModel {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request", "Gemini Veo request model is invalid", nil)
		return
	}
	if !middleware.RelayModelAllowed(c, originModel) {
		writeGeminiVeoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return
	}
	channel, usingGroup := selection.Channel, selection.UsingGroup
	if usingGroup == "" || !containsVideoGroup(groups, usingGroup) {
		writeGeminiVeoTaskError(c, http.StatusForbidden, "group_not_allowed", "selected Veo group is unavailable", nil)
		return
	}
	mappedModel := relaycommon.GetMappedModel(channel, originModel)
	routingSnapshot, err := descriptor.PrepareRoutingSnapshot(channel, mappedModel)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "channel_invalid", "Veo channel routing is invalid", nil)
		return
	}
	if len(routingSnapshot) == 0 || len(routingSnapshot) > geminiVeoTaskRoutingMaxBytes ||
		descriptor.ValidateRoutingSnapshot(routingSnapshot, mappedModel) != nil {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "channel_invalid", "Veo channel routing is invalid", nil)
		return
	}
	prepared, err := geminiVeo.PrepareSubmit(raw, contentType, originModel, mappedModel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeGeminiVeoTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}
	if err := descriptor.ValidatePrepared(prepared); err != nil {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request", "Veo request is unsupported by the selected channel", nil)
		return
	}
	basePricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, originModel, requestctx.GetUserGroup(c), usingGroup,
	)
	if err != nil || !enabled {
		if err != nil {
			logging.SysError("resolve GeminiVeo pricing: " + err.Error())
		}
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "model_price_error",
			"GeminiVeo model requires an explicit reference fixed price or model ratio", nil)
		return
	}
	pricing, err := newGeminiVeoPricingSnapshot(basePricing, prepared.UpstreamModel, prepared.Duration, prepared.Resolution)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "model_price_error", "Gemini Veo pricing configuration is invalid", nil)
		return
	}
	quota, err := pricing.quota(prepared.UpstreamModel, prepared.Resolution)
	if err != nil || quota < 0 {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "model_price_error", "GeminiVeo pricing configuration is invalid", nil)
		return
	}
	channelKey, err := descriptor.CaptureCredential(channel)
	if err != nil || channelKey == "" || len(channelKey) > descriptor.MaxCredentialBytes {
		writeGeminiVeoTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "Veo channel has no valid credential", nil)
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	propertiesJSON, err := marshalGeminiVeoTaskProperties(geminiVeoTaskProperties{
		Version: geminiVeoTaskMetadataVersion, Family: descriptor.Family, Input: prepared.Prompt,
		OriginModelName: originModel, UpstreamModelName: prepared.UpstreamModel,
		Action: prepared.Action, HasInputReference: prepared.HasImage, Duration: prepared.Duration,
		Resolution: prepared.Resolution, AspectRatio: prepared.AspectRatio, Pricing: pricing,
	})
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, geminiVeoChannelCredentialBinding(taskID, descriptor.Platform, userID, channel.Id, routingSnapshot),
	)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect GeminiVeo channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: descriptor.Platform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := geminiVeoTaskPrivateData{
		Version: geminiVeoTaskMetadataVersion, RoutingSnapshot: routingSnapshot,
		EncryptedChannelKey: encryptedKey, Pricing: pricing,
	}
	reservation, err := createGeminiVeoReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic GeminiVeo reservation: " + err.Error())
		}
		writeGeminiVeoTaskError(c, status, code, message, nil)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundRejectedGeminiVeoTask(&task, reservation, "GeminiVeo submission stopped before provider dispatch"); cleanupErr != nil {
			logging.SysError("GeminiVeo reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markGeminiVeoTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark GeminiVeo dispatch: " + err.Error())
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_persistence_failed",
			"failed to persist GeminiVeo dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := descriptor.Submit(c.Request.Context(), routingSnapshot, channelKey, prepared)
	if submitErr != nil {
		if !descriptor.SubmitWasDispatched(submitErr) || definitiveGeminiVeoRejection(submitErr) {
			if refundErr := refundRejectedGeminiVeoTask(&task, reservation, "GeminiVeo provider rejected submission"); refundErr != nil {
				logging.SysError("persist rejected GeminiVeo task " + task.TaskID + ": " + refundErr.Error())
				writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"GeminiVeo submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeGeminiVeoProviderError(c, submitErr)
			return
		}
		if persistErr := markGeminiVeoAmbiguousDispatch(&task, reservation.ReservationID(), geminiVeoUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist ambiguous GeminiVeo task " + task.TaskID + ": " + persistErr.Error())
			writeGeminiVeoTaskError(c, http.StatusAccepted, "task_commit_pending",
				"GeminiVeo provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeGeminiVeoTaskError(c, http.StatusAccepted, "submit_outcome_unknown", geminiVeoUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	if provider != nil && provider.Status == geminiVeo.StatusFailed {
		if refundErr := refundRejectedGeminiVeoTask(&task, reservation, "GeminiVeo provider reported submission failure"); refundErr != nil {
			writeGeminiVeoTaskError(c, http.StatusAccepted, "task_refund_pending", "GeminiVeo task failed and its quota refund is pending",
				gin.H{"task_id": task.TaskID, "status": "failed"})
			return
		}
		finalized = true
		response, responseErr := geminiVeoTaskResponse(&task)
		if responseErr != nil {
			writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode GeminiVeo task", nil)
			return
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if provider == nil || descriptor.ValidateProviderTaskID(routingSnapshot, mappedModel, provider.ProviderTaskID) != nil {
		if persistErr := markGeminiVeoAmbiguousDispatch(&task, reservation.ReservationID(), geminiVeoUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist GeminiVeo missing-id submission " + task.TaskID + ": " + persistErr.Error())
		}
		writeGeminiVeoTaskError(c, http.StatusAccepted, "submit_outcome_unknown", geminiVeoUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}
	journalErr := persistAcceptedGeminiVeoRecoveryJournal(
		&task, reservation.ReservationID(), provider.ProviderTaskID, prepared.Action,
	)
	if journalErr != nil {
		logging.SysError("persist accepted GeminiVeo recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	if err := settleAcceptedGeminiVeoTask(&task, reservation, provider.ProviderTaskID, model.TaskOperationDispatching, ""); err != nil {
		logging.SysError("settle accepted GeminiVeo task " + task.TaskID + ": " + err.Error())
		fallbackErr := persistAcceptedGeminiVeoFallback(&task, reservation.ReservationID(), provider.ProviderTaskID)
		data := gin.H{"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil}
		if fallbackErr != nil {
			logging.SysError("persist accepted GeminiVeo fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeGeminiVeoRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted GeminiVeo recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeGeminiVeoTaskError(c, http.StatusAccepted, "task_commit_pending",
			"GeminiVeo task was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeGeminiVeoRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted GeminiVeo recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(geminiVeoAuditEventID(reservation.ReservationID())); deliverErr != nil {
		logging.SysError("deliver GeminiVeo consume audit: " + deliverErr.Error())
	}
	response, err := geminiVeoTaskResponse(&task)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode GeminiVeo task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayGeminiVeoTaskFetch returns only a user-owned local record and never contacts
// GeminiVeo or decrypts the provider credential snapshot.
func RelayGeminiVeoTaskFetch(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Query("task_id"))
	}
	if err := validateGeminiVeoTaskPublicID(taskID); err != nil {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var tasks []model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ? AND platform IN ?", taskID,
		requestctx.GetUserId(c), model.VeoTaskOperationPlatforms()).Limit(2).Find(&tasks).Error; err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query GeminiVeo task", nil)
		return
	}
	if len(tasks) == 0 {
		writeGeminiVeoTaskError(c, http.StatusNotFound, "task_not_found", "GeminiVeo task was not found", nil)
		return
	}
	if len(tasks) != 1 {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "GeminiVeo task identity is ambiguous", nil)
		return
	}
	task := &tasks[0]
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "GeminiVeo task metadata is invalid", nil)
		return
	}
	if !authorizeGeminiVeoTaskRead(c, task, properties) {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/video/generations/") || c.Request.URL.Path == "/v1/video/fetch" {
		dto, err := geminiVeoTaskDTOFromModel(task)
		if err != nil {
			writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "GeminiVeo task data is invalid", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
		return
	}
	response, err := geminiVeoTaskResponse(task)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "GeminiVeo task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

func relayGeminiVeoTaskContent(c *gin.Context, task *model.Task) {
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "GeminiVeo task metadata is invalid", nil)
		return
	}
	if !authorizeGeminiVeoTaskRead(c, task, properties) {
		return
	}
	if task.Status != model.TaskStatusSuccess {
		writeGeminiVeoTaskError(c, http.StatusBadRequest, "invalid_request_error",
			"GeminiVeo task is not completed; current status is "+task.Status, nil)
		return
	}
	data, err := decodeGeminiVeoStoredTaskData(task.Data)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Veo result is unavailable", nil)
		return
	}
	if data.HasInlineVideo {
		response, artifactErr := openGeminiVeoInlineArtifact(task, data)
		if artifactErr != nil {
			writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Veo inline result is unavailable", nil)
			return
		}
		streamGeminiVeoContentResponse(c, task, response)
		return
	}
	if geminiVeoResultURL(data) == "" {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Veo result URL is unavailable", nil)
		return
	}
	ctx, cancel := contextWithVideoTimeout(c.Request.Context())
	defer cancel()
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Gemini Veo provider snapshot is invalid", nil)
		return
	}
	descriptor, ok := veoTaskProviderByPlatform(task.Platform)
	if !ok || properties.Family != descriptor.Family ||
		descriptor.ValidateRoutingSnapshot(privateData.RoutingSnapshot, properties.UpstreamModelName) != nil {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Veo provider snapshot is invalid", nil)
		return
	}
	credential, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		geminiVeoChannelCredentialBinding(task.TaskID, task.Platform, task.UserId, task.ChannelId, privateData.RoutingSnapshot))
	if err != nil || credential == "" || len(credential) > descriptor.MaxCredentialBytes {
		writeGeminiVeoTaskError(c, http.StatusInternalServerError, "server_error", "Gemini Veo provider credential is unavailable", nil)
		return
	}
	response, err := descriptor.Content(ctx, privateData.RoutingSnapshot, credential, VeoProviderTask{
		Status: data.State, ResultURL: data.ResultURL,
	})
	if err != nil {
		writeGeminiVeoTaskError(c, http.StatusBadGateway, "server_error", "failed to fetch GeminiVeo video content", nil)
		return
	}
	streamGeminiVeoContentResponse(c, task, response)
}

func streamGeminiVeoContentResponse(c *gin.Context, task *model.Task, response *http.Response) {
	if response == nil || response.Body == nil {
		writeGeminiVeoTaskError(c, http.StatusBadGateway, "server_error", "Veo content provider returned an invalid response", nil)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeGeminiVeoTaskError(c, http.StatusBadGateway, "server_error", "GeminiVeo content provider returned an error", nil)
		return
	}
	if response.ContentLength > geminiVeo.MaxContentBodyBytes {
		writeGeminiVeoTaskError(c, http.StatusBadGateway, "server_error", "GeminiVeo video content is too large", nil)
		return
	}
	contentType, safe := safeVideoContentType(response.Header.Get("Content-Type"))
	if !safe {
		writeGeminiVeoTaskError(c, http.StatusBadGateway, "server_error", "GeminiVeo content provider returned an unsafe content type", nil)
		return
	}
	copyVideoContentHeaders(c.Writer.Header(), response.Header)
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(response.StatusCode)
	if _, err := io.Copy(c.Writer, response.Body); err != nil {
		logging.SysError("stream GeminiVeo content for " + task.TaskID + ": " + err.Error())
	}
}

func authorizeGeminiVeoTaskRead(c *gin.Context, task *model.Task, properties geminiVeoTaskProperties) bool {
	if middleware.GetRelayToken(c) == nil {
		return true
	}
	if !containsVideoGroup(middleware.GetTokenGroups(c), task.Group) {
		writeGeminiVeoTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group", nil)
		return false
	}
	if !middleware.RelayModelAllowed(c, properties.OriginModelName) {
		writeGeminiVeoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveGeminiVeoRejection(err error) bool {
	var upstream *relaycommon.UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 &&
		upstream.StatusCode != http.StatusRequestTimeout && upstream.StatusCode != http.StatusConflict
}

func writeGeminiVeoProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode <= 599 {
		status = upstream.StatusCode
	}
	writeGeminiVeoTaskError(c, status, "upstream_error", "GeminiVeo provider rejected the request", nil)
}

func writeGeminiVeoTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{"message": boundedGeminiVeoFailReason(message),
		"type": "invalid_request_error", "code": code}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}
