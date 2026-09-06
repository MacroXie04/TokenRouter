package tasks

import (
	"bytes"
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/kling"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
	"time"
)

var newKlingTaskClient = func() *kling.Client { return &kling.Client{} }

// RelayKlingTask submits either Kling text-to-video or image-to-video. The
// final merged body, not the route spelling, determines the provider action.
func RelayKlingTask(c *gin.Context, state relaycommon.RequestState) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, kling.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeKlingTaskError(c, status, "invalid_request", "Kling request body is invalid or too large", nil)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))

	userID := state.UserID
	token := state.Token
	if userID <= 0 || token == nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	groups := state.Groups
	if len(groups) == 0 {
		writeKlingTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}
	originModel, err := kling.RequestedModel(raw)
	if err != nil {
		writeKlingTaskError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if !state.Allows(originModel) {
		writeKlingTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return
	}
	channel, usingGroup, err := selectKlingChannel(groups, originModel)
	if err != nil {
		writeKlingTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no Kling channel is available", nil)
		return
	}
	channelKey := channelssvc.GetChannelKey(channel)
	if strings.TrimSpace(channelKey) == "" {
		writeKlingTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "Kling channel has no available key", nil)
		return
	}
	baseURL, _, err := kling.EffectiveBaseURL(channel.BaseURL, channelKey)
	if err != nil {
		writeKlingTaskError(c, http.StatusBadRequest, "channel_invalid", err.Error(), nil)
		return
	}
	if _, err := kling.AuthorizationToken(channelKey, time.Now()); err != nil {
		writeKlingTaskError(c, http.StatusServiceUnavailable, "channel_invalid", "Kling channel credentials are invalid", nil)
		return
	}
	mappedModel := relaycommon.GetMappedModel(channel, originModel)
	prepared, err := kling.PrepareSubmit(raw, originModel, mappedModel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeKlingTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}

	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, originModel, state.UserGroup, usingGroup,
	)
	if err != nil || !enabled {
		if err != nil {
			logging.SysError("resolve Kling pricing: " + err.Error())
		}
		writeKlingTaskError(c, http.StatusBadRequest, "model_price_error",
			"Kling model requires an explicit reference fixed price or model ratio", nil)
		return
	}
	quota, err := pricing.PreConsumeQuota()
	if err != nil {
		writeKlingTaskError(c, http.StatusBadRequest, "model_price_error", err.Error(), nil)
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	hasInput := prepared.Payload.Image != "" || prepared.Payload.ImageTail != ""
	properties := klingTaskProperties{
		Version: klingTaskMetadataVersion, Family: "kling", Prompt: prepared.Payload.Prompt,
		OriginModelName: originModel, UpstreamModelName: mappedModel,
		Action: prepared.Action, Mode: prepared.Payload.Mode, Duration: prepared.Payload.Duration,
		AspectRatio: prepared.Payload.AspectRatio, HasInputReference: hasInput,
	}
	propertiesJSON, err := marshalKlingTaskProperties(properties)
	if err != nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, klingChannelCredentialBinding(taskID, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect Kling channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: klingTaskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := klingTaskPrivateData{
		Version: klingTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, SettlementPending: false, Pricing: pricing,
	}
	reservation, err := createKlingReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic Kling reservation: " + err.Error())
		}
		writeKlingTaskError(c, status, code, message, nil)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundKlingTask(&task, reservation, nil,
			"Kling submission stopped before provider dispatch", model.TaskOperationPrepared, ""); cleanupErr != nil {
			logging.SysError("Kling reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markKlingTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark Kling dispatch: " + err.Error())
		writeKlingTaskError(c, http.StatusInternalServerError, "task_persistence_failed",
			"failed to persist Kling dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := newKlingTaskClient().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if !kling.SubmitWasDispatched(submitErr) || definitiveKlingRejection(submitErr) {
			if refundErr := refundKlingTask(&task, reservation, nil,
				"Kling provider rejected submission", model.TaskOperationDispatching, ""); refundErr != nil {
				logging.SysError("persist rejected Kling task " + task.TaskID + ": " + refundErr.Error())
				writeKlingTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"Kling submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeKlingProviderError(c, submitErr)
			return
		}
		if persistErr := markKlingAmbiguousDispatch(&task, reservation.ReservationID(), klingUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist ambiguous Kling task " + task.TaskID + ": " + persistErr.Error())
			writeKlingTaskError(c, http.StatusAccepted, "task_commit_pending",
				"Kling provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeKlingTaskError(c, http.StatusAccepted, "submit_outcome_unknown", klingUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}

	journalErr := persistAcceptedKlingRecoveryJournal(&task, reservation.ReservationID(), provider.ProviderTaskID, prepared.Action)
	if journalErr != nil {
		logging.SysError("persist accepted Kling recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	var commitErr error
	switch provider.Status {
	case kling.StatusSucceeded:
		commitErr = settleSuccessfulKlingTask(&task, reservation, provider, model.TaskOperationDispatching, "")
	case kling.StatusFailed:
		commitErr = refundKlingTask(&task, reservation, provider, provider.StatusMessage,
			model.TaskOperationDispatching, "")
	default:
		commitErr = persistAcceptedKlingTask(&task, reservation.ReservationID(), provider,
			model.TaskOperationDispatching, "")
		if commitErr == nil {
			commitErr = reloadKlingTask(&task)
		}
	}
	if commitErr != nil {
		logging.SysError("commit accepted Kling task " + task.TaskID + ": " + commitErr.Error())
		fallbackErr := persistKlingProviderStatePending(&task, reservation.ReservationID(), provider,
			model.TaskOperationDispatching, "")
		data := gin.H{
			"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil,
		}
		if fallbackErr != nil {
			logging.SysError("persist accepted Kling fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeKlingRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted Kling recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeKlingTaskError(c, http.StatusAccepted, "task_commit_pending",
			"Kling task was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeKlingRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted Kling recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if task.Status == model.TaskStatusSuccess {
		if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(klingAuditEventID(reservation.ReservationID())); deliverErr != nil {
			logging.SysError("deliver Kling consume audit: " + deliverErr.Error())
		}
	}
	response, err := klingTaskResponse(&task)
	if err != nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode Kling task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayKlingTaskFetch reads only the user-owned local task. Background polling
// is the sole path that contacts or decrypts the provider snapshot.
func RelayKlingTaskFetch(c *gin.Context, state relaycommon.RequestState) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if err := validateKlingTaskPublicID(taskID); err != nil {
		writeKlingTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var tasks []model.Task
	result := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, klingTaskPlatform).Limit(2).Find(&tasks)
	if result.Error != nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Kling task", nil)
		return
	}
	if len(tasks) == 0 {
		writeKlingTaskError(c, http.StatusNotFound, "task_not_found", "Kling task was not found", nil)
		return
	}
	if len(tasks) != 1 || !isKlingTask(&tasks[0]) {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Kling task identity is ambiguous or invalid", nil)
		return
	}
	task := &tasks[0]
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Kling task metadata is invalid", nil)
		return
	}
	requestedAction, ok := klingFetchActionFromPath(c.Request.URL.Path)
	if !ok {
		writeKlingTaskError(c, http.StatusBadRequest, "invalid_request", "Kling fetch route is invalid", nil)
		return
	}
	if properties.Action != requestedAction {
		writeKlingTaskError(c, http.StatusNotFound, "task_not_found", "Kling task was not found", nil)
		return
	}
	if !authorizeKlingTaskRead(c, state, task, properties) {
		return
	}
	dto, err := klingTaskDTOFromModel(task)
	if err != nil {
		writeKlingTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Kling task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
}

func klingFetchActionFromPath(path string) (kling.Action, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "kling" || parts[1] != "v1" || parts[2] != "videos" || parts[4] == "" {
		return "", false
	}
	action := kling.Action(parts[3])
	return action, validKlingAction(action)
}

func selectKlingChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 && channelcatalog.ChannelType(channel.Type) == channelcatalog.ChannelTypeKling {
			return channel, group, nil
		}
		if channel != nil {
			ignored[channel.Id] = struct{}{}
		}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func authorizeKlingTaskRead(c *gin.Context, state relaycommon.RequestState, task *model.Task, properties klingTaskProperties) bool {
	if state.Token == nil {
		return true
	}
	if !containsVideoGroup(state.Groups, task.Group) {
		writeKlingTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group", nil)
		return false
	}
	if !state.Allows(properties.OriginModelName) {
		writeKlingTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveKlingRejection(err error) bool {
	var providerErr *kling.ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	return providerErr.StatusCode >= 400 && providerErr.StatusCode < 500 &&
		providerErr.StatusCode != http.StatusRequestTimeout && providerErr.StatusCode != http.StatusConflict
}

func writeKlingProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var providerErr *kling.ProviderError
	if errors.As(err, &providerErr) && providerErr.StatusCode >= 400 && providerErr.StatusCode <= 599 {
		status = providerErr.StatusCode
	}
	writeKlingTaskError(c, status, "upstream_error", "Kling provider rejected the request", nil)
}

func writeKlingTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{
		"message": boundedKlingFailReason(message), "type": "invalid_request_error", "code": code,
	}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}
