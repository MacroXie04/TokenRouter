package tasks

import (
	"bytes"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
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
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/doubao"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
)

var newDoubaoTaskClient = func() *doubao.Client { return &doubao.Client{} }
var newDoubaoContentHTTPClient = doubao.NewHTTPClient

func init() {
	operationssvc.RegisterAsyncTaskReconciler(reconcileAsyncDoubaoTasks)
	operationssvc.RegisterAsyncTaskPromoter(PromoteDoubaoTaskRecoveryJournalsContext)
}

// RelayDoubaoTask submits an Ark content-generation task through either the
// general VolcEngine or dedicated Doubao Video channel family.
func RelayDoubaoTask(c *gin.Context) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, doubao.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeDoubaoTaskError(c, status, "invalid_request", "Doubao request body is invalid or too large", nil)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))

	userID := requestctx.GetUserId(c)
	token := middleware.GetRelayToken(c)
	if userID <= 0 || token == nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return
	}
	groups := middleware.GetTokenGroups(c)
	if len(groups) == 0 {
		writeDoubaoTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return
	}
	originModel, err := doubao.RequestedModel(raw)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if !middleware.RelayModelAllowed(c, originModel) {
		writeDoubaoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return
	}
	channel, usingGroup, err := selectDoubaoChannel(groups, originModel)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no Doubao channel is available", nil)
		return
	}
	channelKey := channelssvc.GetChannelKey(channel)
	if strings.TrimSpace(channelKey) == "" {
		writeDoubaoTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "Doubao channel has no available key", nil)
		return
	}
	baseURL, err := doubao.EffectiveBaseURL(channel.BaseURL)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusBadRequest, "channel_invalid", err.Error(), nil)
		return
	}
	if err := doubao.ValidateCredential(channelKey); err != nil {
		writeDoubaoTaskError(c, http.StatusServiceUnavailable, "channel_invalid", "Doubao channel credentials are invalid", nil)
		return
	}
	mappedModel := relaycommon.GetMappedModel(channel, originModel)
	prepared, err := doubao.PrepareSubmit(raw, originModel, mappedModel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeDoubaoTaskError(c, status, "invalid_request", err.Error(), nil)
		return
	}

	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, originModel, requestctx.GetUserGroup(c), usingGroup,
	)
	if err != nil || !enabled {
		if err != nil {
			logging.SysError("resolve Doubao pricing: " + err.Error())
		}
		writeDoubaoTaskError(c, http.StatusBadRequest, "model_price_error",
			"Doubao model requires an explicit reference fixed price or model ratio", nil)
		return
	}
	pricing, err = applyDoubaoVideoInputRatio(pricing, prepared.PriceRatio)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusBadRequest, "model_price_error", err.Error(), nil)
		return
	}
	quota, err := pricing.PreConsumeQuota()
	if err != nil {
		writeDoubaoTaskError(c, http.StatusBadRequest, "model_price_error", err.Error(), nil)
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier", nil)
		return
	}
	platform, ok := doubaoTaskPlatformForChannelType(channel.Type)
	if !ok {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "Doubao channel platform is invalid", nil)
		return
	}
	properties := doubaoTaskProperties{
		Version: doubaoTaskMetadataVersion, Family: "doubao", Prompt: prepared.Payload.Content[len(prepared.Payload.Content)-1].Text,
		OriginModelName: originModel, UpstreamModelName: mappedModel,
		Action: prepared.Action, Resolution: prepared.Payload.Resolution,
		HasVideoInput:   prepared.HasVideoInput,
		VideoInputRatio: decimal.NewFromFloat(prepared.PriceRatio).String(),
	}
	if prepared.Payload.Duration != nil {
		properties.Duration = *prepared.Payload.Duration
	}
	propertiesJSON, err := marshalDoubaoTaskProperties(properties)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", err.Error(), nil)
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, doubaoChannelCredentialBinding(taskID, platform, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect Doubao channel credentials", nil)
		return
	}
	now := wallclock.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: platform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := doubaoTaskPrivateData{
		Version: doubaoTaskMetadataVersion, ChannelBaseURL: baseURL,
		EncryptedChannelKey: encryptedKey, SettlementPending: false, Pricing: pricing,
	}
	reservation, err := createDoubaoReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic Doubao reservation: " + err.Error())
		}
		writeDoubaoTaskError(c, status, code, message, nil)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundDoubaoTask(&task, reservation, nil,
			"Doubao submission stopped before provider dispatch", model.TaskOperationPrepared, ""); cleanupErr != nil {
			logging.SysError("Doubao reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markDoubaoTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark Doubao dispatch: " + err.Error())
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_persistence_failed",
			"failed to persist Doubao dispatch state", gin.H{"task_id": task.TaskID})
		return
	}
	dispatched = true
	provider, _, submitErr := newDoubaoTaskClient().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if !doubao.SubmitWasDispatched(submitErr) || definitiveDoubaoRejection(submitErr) {
			if refundErr := refundDoubaoTask(&task, reservation, nil,
				"Doubao provider rejected submission", model.TaskOperationDispatching, ""); refundErr != nil {
				logging.SysError("persist rejected Doubao task " + task.TaskID + ": " + refundErr.Error())
				writeDoubaoTaskError(c, http.StatusInternalServerError, "task_cleanup_failed",
					"Doubao submission failed and quota cleanup is pending", gin.H{"task_id": task.TaskID})
				return
			}
			finalized = true
			writeDoubaoProviderError(c, submitErr)
			return
		}
		if persistErr := markDoubaoAmbiguousDispatch(&task, reservation.ReservationID(), doubaoUnknownDispatchReason); persistErr != nil {
			logging.SysError("persist ambiguous Doubao task " + task.TaskID + ": " + persistErr.Error())
			writeDoubaoTaskError(c, http.StatusAccepted, "task_commit_pending",
				"Doubao provider outcome is unknown and accounting recovery is pending",
				gin.H{"task_id": task.TaskID, "status": "unknown", "recovery_durable": true})
			return
		}
		finalized = true
		writeDoubaoTaskError(c, http.StatusAccepted, "submit_outcome_unknown", doubaoUnknownDispatchReason,
			gin.H{"task_id": task.TaskID, "status": "unknown"})
		return
	}

	journalErr := persistAcceptedDoubaoRecoveryJournal(&task, reservation.ReservationID(), provider.ProviderTaskID, prepared.Action)
	if journalErr != nil {
		logging.SysError("persist accepted Doubao recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	var commitErr error
	switch provider.Status {
	case doubao.StatusSucceeded:
		commitErr = settleSuccessfulDoubaoTask(&task, reservation, provider, model.TaskOperationDispatching, "")
	case doubao.StatusFailed:
		commitErr = refundDoubaoTask(&task, reservation, provider, provider.StatusMessage,
			model.TaskOperationDispatching, "")
	default:
		commitErr = persistAcceptedDoubaoTask(&task, reservation.ReservationID(), provider,
			model.TaskOperationDispatching, "")
		if commitErr == nil {
			commitErr = reloadDoubaoTask(&task)
		}
	}
	if commitErr != nil {
		logging.SysError("commit accepted Doubao task " + task.TaskID + ": " + commitErr.Error())
		fallbackErr := persistDoubaoProviderStatePending(&task, reservation.ReservationID(), provider,
			model.TaskOperationDispatching, "")
		data := gin.H{
			"task_id": task.TaskID, "status": "accepted", "settlement_pending": true,
			"recovery_durable": fallbackErr == nil || journalErr == nil,
		}
		if fallbackErr != nil {
			logging.SysError("persist accepted Doubao fallback " + task.TaskID + ": " + fallbackErr.Error())
			if journalErr != nil {
				data["recovery_degraded"] = true
			}
		} else if cleanupErr := removeDoubaoRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted Doubao recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeDoubaoTaskError(c, http.StatusAccepted, "task_commit_pending",
			"Doubao task was accepted but local accounting is still being reconciled", data)
		return
	}
	if cleanupErr := removeDoubaoRecoveryJournal(task.TaskID); cleanupErr != nil {
		logging.SysError("remove accepted Doubao recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	billingsvc.CheckAndSendQuotaReminderForReservation(userID, reservation)
	if task.Status == model.TaskStatusSuccess {
		if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(doubaoAuditEventID(reservation.ReservationID())); deliverErr != nil {
			logging.SysError("deliver Doubao consume audit: " + deliverErr.Error())
		}
	}
	response, err := doubaoTaskResponse(&task)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "failed to encode Doubao task", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// RelayDoubaoTaskFetch reads only the user-owned local task. Background polling
// is the sole path that contacts or decrypts the provider snapshot.
func RelayDoubaoTaskFetch(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if err := validateDoubaoTaskPublicID(taskID); err != nil {
		writeDoubaoTaskError(c, http.StatusBadRequest, "invalid_request", "task_id is invalid", nil)
		return
	}
	var tasks []model.Task
	result := model.DB.Where("task_id = ? AND user_id = ? AND platform IN ?", taskID,
		requestctx.GetUserId(c), model.DoubaoVideoTaskOperationPlatforms()).Limit(2).Find(&tasks)
	if result.Error != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query Doubao task", nil)
		return
	}
	if len(tasks) == 0 {
		writeDoubaoTaskError(c, http.StatusNotFound, "task_not_found", "Doubao task was not found", nil)
		return
	}
	if len(tasks) != 1 || !isDoubaoTask(&tasks[0]) {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Doubao task identity is ambiguous or invalid", nil)
		return
	}
	task := &tasks[0]
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Doubao task metadata is invalid", nil)
		return
	}
	if !authorizeDoubaoTaskRead(c, task, properties) {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/video/generations/") || c.Request.URL.Path == "/v1/video/fetch" {
		dto, err := doubaoTaskDTOFromModel(task)
		if err != nil {
			writeDoubaoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Doubao task data is invalid", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": "success", "data": dto})
		return
	}
	response, err := doubaoTaskResponse(task)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Doubao task data is invalid", nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

func relayDoubaoTaskContent(c *gin.Context, task *model.Task) {
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "server_error", "Doubao task metadata is invalid", nil)
		return
	}
	if !authorizeDoubaoTaskRead(c, task, properties) {
		return
	}
	if task.Status != model.TaskStatusSuccess {
		writeDoubaoTaskError(c, http.StatusBadRequest, "invalid_request_error",
			"Doubao task is not completed; current status is "+task.Status, nil)
		return
	}
	data, err := decodeDoubaoStoredTaskData(task.Data)
	if err != nil || data.ResultURL == "" {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "server_error", "Doubao result URL is unavailable", nil)
		return
	}
	ctx, cancel := contextWithVideoTimeout(c.Request.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, data.ResultURL, nil)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusInternalServerError, "server_error", "Doubao result URL is invalid", nil)
		return
	}
	request.Header.Set("Accept", "video/*, application/octet-stream")
	response, err := newDoubaoContentHTTPClient().Do(request)
	if err != nil {
		writeDoubaoTaskError(c, http.StatusBadGateway, "server_error", "failed to fetch Doubao video content", nil)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeDoubaoTaskError(c, http.StatusBadGateway, "server_error", "Doubao content provider returned an error", nil)
		return
	}
	const maxContentBytes int64 = 512 << 20
	if response.ContentLength > maxContentBytes {
		writeDoubaoTaskError(c, http.StatusBadGateway, "server_error", "Doubao video content is too large", nil)
		return
	}
	contentType, safe := safeVideoContentType(response.Header.Get("Content-Type"))
	if !safe {
		writeDoubaoTaskError(c, http.StatusBadGateway, "server_error", "Doubao content provider returned an unsafe content type", nil)
		return
	}
	copyVideoContentHeaders(c.Writer.Header(), response.Header)
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(response.StatusCode)
	if _, err := io.Copy(c.Writer, io.LimitReader(response.Body, maxContentBytes)); err != nil {
		logging.SysError("stream Doubao content for " + task.TaskID + ": " + err.Error())
	}
}

func selectDoubaoChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 &&
			(channelcatalog.ChannelType(channel.Type) == channelcatalog.ChannelTypeDoubaoVideo ||
				channelcatalog.ChannelType(channel.Type) == channelcatalog.ChannelTypeVolcEngine) {
			return channel, group, nil
		}
		if channel != nil {
			ignored[channel.Id] = struct{}{}
		}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func applyDoubaoVideoInputRatio(
	pricing billingsvc.ReferenceAsyncTaskBillingPlan,
	ratio float64,
) (billingsvc.ReferenceAsyncTaskBillingPlan, error) {
	multiplier := decimal.NewFromFloat(ratio)
	if multiplier.LessThanOrEqual(decimal.Zero) || multiplier.GreaterThan(decimal.NewFromInt(100)) {
		return billingsvc.ReferenceAsyncTaskBillingPlan{}, errors.New("Doubao video-input price ratio is invalid")
	}
	if pricing.UseFixedPrice {
		value, err := decimal.NewFromString(pricing.FixedPrice)
		if err != nil {
			return billingsvc.ReferenceAsyncTaskBillingPlan{}, errors.New("Doubao fixed price is invalid")
		}
		pricing.FixedPrice = value.Mul(multiplier).String()
	} else {
		value, err := decimal.NewFromString(pricing.ModelRatio)
		if err != nil {
			return billingsvc.ReferenceAsyncTaskBillingPlan{}, errors.New("Doubao model ratio is invalid")
		}
		pricing.ModelRatio = value.Mul(multiplier).String()
	}
	if err := pricing.Validate(); err != nil {
		return billingsvc.ReferenceAsyncTaskBillingPlan{}, err
	}
	return pricing, nil
}

func authorizeDoubaoTaskRead(c *gin.Context, task *model.Task, properties doubaoTaskProperties) bool {
	if middleware.GetRelayToken(c) == nil {
		return true
	}
	if !containsVideoGroup(middleware.GetTokenGroups(c), task.Group) {
		writeDoubaoTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group", nil)
		return false
	}
	if !middleware.RelayModelAllowed(c, properties.OriginModelName) {
		writeDoubaoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model", nil)
		return false
	}
	return true
}

func definitiveDoubaoRejection(err error) bool {
	var providerErr *doubao.ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	return providerErr.StatusCode >= 400 && providerErr.StatusCode < 500 &&
		providerErr.StatusCode != http.StatusRequestTimeout && providerErr.StatusCode != http.StatusConflict
}

func writeDoubaoProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var providerErr *doubao.ProviderError
	if errors.As(err, &providerErr) && providerErr.StatusCode >= 400 && providerErr.StatusCode <= 599 {
		status = providerErr.StatusCode
	}
	writeDoubaoTaskError(c, status, "upstream_error", "Doubao provider rejected the request", nil)
}

func writeDoubaoTaskError(c *gin.Context, status int, code, message string, data any) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	body := gin.H{"error": gin.H{
		"message": boundedDoubaoFailReason(message), "type": "invalid_request_error", "code": code,
	}}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}
