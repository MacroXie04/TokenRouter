package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/suno"
	"github.com/tokenrouter/tokenrouter/service"
)

type sunoProvider interface {
	Submit(context.Context, string, string, *suno.PreparedRequest) (string, error)
	Fetch(context.Context, string, string, []string) ([]suno.TaskResult, error)
}

var newSunoProvider = func() sunoProvider { return &suno.Client{} }

func init() {
	service.RegisterAsyncTaskPromoter(PromoteSunoTaskRecoveryJournalsContext)
	service.RegisterAsyncTaskReconciler(reconcileAsyncSunoTasks)
}

// RelaySunoTask submits a MUSIC or LYRICS task. A quota reservation and
// no-retry dispatch fence are committed before the provider can be contacted.
func RelaySunoTask(c *gin.Context) {
	raw, err := common.ReadAllLimited(c.Request.Body, suno.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, common.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeSunoTaskError(c, status, "invalid_request", "Suno request body is invalid or too large")
		return
	}
	prepared, err := suno.PrepareSubmit(raw, c.Param("action"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, common.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeSunoTaskError(c, status, "invalid_request", err.Error())
		return
	}

	userID := common.GetUserId(c)
	token := middleware.GetRelayToken(c)
	groups := middleware.GetTokenGroups(c)
	if userID <= 0 || token == nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing")
		return
	}
	if len(groups) == 0 {
		writeSunoTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable")
		return
	}
	if !middleware.RelayModelAllowed(c, prepared.Model) {
		writeSunoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model")
		return
	}
	var channel *model.Channel
	var usingGroup, baseURL, channelKey string
	if prepared.Value.TaskID != "" {
		originPublicID := prepared.Value.TaskID
		if validateSunoTaskPublicID(originPublicID) != nil {
			writeSunoTaskError(c, http.StatusBadRequest, "invalid_request", "Suno continuation task_id is invalid")
			return
		}
		var origin model.Task
		if err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", originPublicID,
			userID, sunoTaskPlatform).First(&origin).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeSunoTaskError(c, http.StatusBadRequest, "task_not_exist", "origin Suno task does not exist")
			} else {
				writeSunoTaskError(c, http.StatusInternalServerError, "task_query_failed", "failed to query origin Suno task")
			}
			return
		}
		originProperties, err := decodeSunoTaskProperties(origin.Properties)
		if err != nil || originProperties.OriginModelName != "suno_music" ||
			originProperties.Action != suno.ActionMusic || origin.Action != string(suno.ActionMusic) ||
			origin.Status != model.TaskStatusSuccess {
			writeSunoTaskError(c, http.StatusBadRequest, "task_not_ready", "origin Suno task is not completed music")
			return
		}
		if !containsSunoGroup(groups, origin.Group) || !middleware.RelayModelAllowed(c, originProperties.OriginModelName) {
			writeSunoTaskError(c, http.StatusForbidden, "task_not_allowed", "token is not allowed to continue this Suno task")
			return
		}
		operation, err := loadSunoTaskOperation(origin.TaskID)
		if err != nil || !sunoTaskOperationIdentityMatches(&origin, operation) ||
			operation.State != model.TaskOperationTerminal || operation.SettlementPending ||
			operation.LeaseOwner != "" || operation.EncryptedProviderTaskID == "" {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno provider identifier is unavailable")
			return
		}
		providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
			sunoProviderTaskBinding(origin.TaskID, operation.ReservationID, origin.UserId, origin.ChannelId))
		if err != nil {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno provider identifier cannot be decrypted")
			return
		}
		originPrivate, err := decodeSunoTaskPrivateData(origin.PrivateData)
		if err != nil {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno channel snapshot is invalid")
			return
		}
		if originPrivate.SettlementPending ||
			originPrivate.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno provider identifier is inconsistent")
			return
		}
		channel, err = service.GetChannelByID(origin.ChannelId)
		if err != nil || channel == nil || channel.Status != constant.ChannelStatusEnabled ||
			constant.ChannelType(channel.Type) != constant.ChannelTypeSunoAPI {
			writeSunoTaskError(c, http.StatusBadRequest, "task_channel_disable", "origin Suno channel is unavailable")
			return
		}
		var abilityCount int64
		if err := model.DB.Model(&model.Ability{}).Where(&model.Ability{
			ChannelId: channel.Id, Group: origin.Group, Model: prepared.Model, Enabled: true,
		}).Count(&abilityCount).Error; err != nil || abilityCount != 1 {
			writeSunoTaskError(c, http.StatusBadRequest, "task_channel_disable", "origin Suno channel is unavailable")
			return
		}
		baseURL, err = suno.ValidateBaseURL(originPrivate.ChannelBaseURL)
		if err != nil {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno base URL snapshot is invalid")
			return
		}
		channelKey, err = asyncTaskDecryptBound(originPrivate.EncryptedChannelKey,
			sunoChannelCredentialBinding(origin.TaskID, origin.UserId, origin.ChannelId, originPrivate.ChannelBaseURL))
		if err != nil || strings.TrimSpace(channelKey) == "" {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno credential snapshot cannot be decrypted")
			return
		}
		prepared, err = suno.WithProviderTaskID(prepared, providerID)
		if err != nil {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "origin Suno provider identifier is invalid")
			return
		}
		usingGroup = origin.Group
	} else {
		channel, usingGroup, err = selectSunoChannel(groups, prepared.Model)
		if err != nil {
			writeSunoTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no Suno channel is available")
			return
		}
		baseURL, err = suno.ValidateBaseURL(channel.BaseURL)
		if err != nil {
			writeSunoTaskError(c, http.StatusBadRequest, "channel_invalid", "Suno channel base URL is invalid")
			return
		}
		channelKey = service.GetChannelKey(channel)
		if strings.TrimSpace(channelKey) == "" {
			writeSunoTaskError(c, http.StatusServiceUnavailable, "channel_no_available_key", "Suno channel has no available key")
			return
		}
	}
	pricing, enabled, err := service.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, prepared.Model, common.GetUserGroup(c), usingGroup,
	)
	if err != nil || !enabled {
		writeSunoTaskError(c, http.StatusBadRequest, "model_price_error", "Suno model requires explicit reference pricing")
		return
	}
	quota, err := pricing.PreConsumeQuota()
	if err != nil || quota < 0 {
		writeSunoTaskError(c, http.StatusBadRequest, "model_price_error", "Suno pricing configuration is invalid")
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to generate task identifier")
		return
	}
	propertiesJSON, err := marshalSunoTaskProperties(sunoTaskProperties{
		Version: 1, OriginModelName: prepared.Model, Action: prepared.Action, Pricing: pricing,
	})
	if err != nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to encode Suno task metadata")
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(
		channelKey, sunoChannelCredentialBinding(taskID, userID, channel.Id, baseURL),
	)
	if err != nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to protect Suno channel credentials")
		return
	}
	now := common.NowTimestamp()
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: sunoTaskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "null",
	}
	privateData := sunoTaskPrivateData{ChannelBaseURL: baseURL, EncryptedChannelKey: encryptedKey}
	reservation, err := createSunoReservedTask(&task, token, &privateData)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "pre_consume_failed", "failed to reserve quota"
		if service.IsSubscriptionFundingErr(err) || errors.Is(err, service.ErrInsufficientQuota) ||
			errors.Is(err, service.ErrInsufficientTokenQuota) {
			status, code, message = http.StatusBadRequest, "insufficient_quota", "user, token, or subscription quota is insufficient"
		} else {
			common.SysError("create atomic Suno reservation: " + err.Error())
		}
		writeSunoTaskError(c, status, code, message)
		return
	}
	dispatched := false
	finalized := false
	defer func() {
		if dispatched || finalized {
			return
		}
		if cleanupErr := refundRejectedSunoTask(&task, reservation, "submission aborted before provider dispatch"); cleanupErr != nil {
			common.SysError("Suno reservation cleanup failed for " + task.TaskID + ": " + cleanupErr.Error())
		}
	}()
	if err := markSunoTaskDispatching(&task, reservation); err != nil {
		common.SysError("mark Suno dispatch: " + err.Error())
		writeSunoTaskError(c, http.StatusInternalServerError, "task_persistence_failed", "failed to persist Suno dispatch state")
		return
	}
	dispatched = true
	providerTaskID, submitErr := newSunoProvider().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if suno.IsDefinitiveRejection(submitErr) {
			reason := "Suno provider rejected submission"
			if journalErr := persistRejectedSunoRecoveryJournal(
				&task, reservation.ReservationID(), prepared.Action, reason,
			); journalErr != nil {
				common.SysError("persist rejected Suno recovery journal " + task.TaskID + ": " + journalErr.Error())
			}
			if refundErr := refundRejectedSunoTask(&task, reservation, reason); refundErr != nil {
				common.SysError("persist rejected Suno task " + task.TaskID + ": " + refundErr.Error())
				writeSunoTaskError(c, http.StatusInternalServerError, "task_cleanup_failed", "Suno submission failed and quota cleanup is pending")
				return
			}
			if cleanupErr := removeSunoRecoveryJournal(task.TaskID); cleanupErr != nil {
				common.SysError("remove rejected Suno recovery journal " + task.TaskID + ": " + cleanupErr.Error())
			}
			finalized = true
			writeSunoProviderError(c, submitErr)
			return
		}
		if !suno.SubmitWasDispatched(submitErr) {
			reason := "Suno submission stopped before provider dispatch"
			if journalErr := persistRejectedSunoRecoveryJournal(
				&task, reservation.ReservationID(), prepared.Action, reason,
			); journalErr != nil {
				common.SysError("persist undispatched Suno recovery journal " + task.TaskID + ": " + journalErr.Error())
			}
			if refundErr := refundRejectedSunoTask(&task, reservation, reason); refundErr != nil {
				common.SysError("persist undispatched Suno task " + task.TaskID + ": " + refundErr.Error())
				writeSunoTaskError(c, http.StatusInternalServerError, "task_cleanup_failed", "Suno submission failed and quota cleanup is pending")
				return
			}
			if cleanupErr := removeSunoRecoveryJournal(task.TaskID); cleanupErr != nil {
				common.SysError("remove undispatched Suno recovery journal " + task.TaskID + ": " + cleanupErr.Error())
			}
			finalized = true
			writeSunoTaskError(c, http.StatusBadGateway, "upstream_request_failed", "Suno submission could not be dispatched")
			return
		}
		if settleErr := settleUnknownSunoDispatch(&task, reservation, sunoUnknownDispatchReason, ""); settleErr != nil {
			common.SysError("persist ambiguous Suno task " + task.TaskID + ": " + settleErr.Error())
			writeSunoTaskEnvelope(c, http.StatusAccepted, "task_commit_pending",
				"Suno provider outcome is unknown and accounting recovery is pending", task.TaskID)
			return
		}
		finalized = true
		writeSunoTaskEnvelope(c, http.StatusAccepted, "submit_outcome_unknown", sunoUnknownDispatchReason, task.TaskID)
		return
	}
	journalErr := persistAcceptedSunoRecoveryJournal(&task, reservation.ReservationID(), providerTaskID, prepared.Action)
	if journalErr != nil {
		common.SysError("persist accepted Suno recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	if err := settleAcceptedSunoTask(&task, reservation, providerTaskID, model.TaskOperationDispatching, ""); err != nil {
		common.SysError("settle accepted Suno task " + task.TaskID + ": " + err.Error())
		fallbackErr := persistAcceptedSunoFallback(&task, reservation.ReservationID(), providerTaskID)
		if fallbackErr != nil {
			common.SysError("persist accepted Suno fallback " + task.TaskID + ": " + fallbackErr.Error())
		} else if cleanupErr := removeSunoRecoveryJournal(task.TaskID); cleanupErr != nil {
			common.SysError("remove accepted Suno recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeSunoTaskEnvelope(c, http.StatusAccepted, "task_commit_pending",
			"Suno task was accepted and accounting recovery is pending", task.TaskID)
		return
	}
	if cleanupErr := removeSunoRecoveryJournal(task.TaskID); cleanupErr != nil {
		common.SysError("remove accepted Suno recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	service.CheckAndSendQuotaReminderForReservation(userID, reservation)
	c.JSON(http.StatusOK, gin.H{"code": "success", "message": "", "data": task.TaskID})
}

// RelaySunoTaskFetch implements both the exact POST batch and GET-by-id local
// fetch surfaces. It never calls the provider and never exposes private IDs.
func RelaySunoTaskFetch(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		fetchSunoTaskByID(c)
		return
	}
	fetchSunoTasks(c)
}

type sunoFetchRequest struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

func fetchSunoTasks(c *gin.Context) {
	raw, err := common.ReadAllLimited(c.Request.Body, sunoFetchRequestMaxBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, common.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeSunoTaskError(c, status, "invalid_request", "Suno fetch request is invalid or too large")
		return
	}
	var request sunoFetchRequest
	if err := rejectDuplicateTopLevelSunoFields(raw); err != nil || strictSunoJSON(raw, &request) != nil ||
		len(request.IDs) > suno.MaxBatchTasks {
		writeSunoTaskError(c, http.StatusBadRequest, "invalid_request", "Suno fetch request is invalid")
		return
	}
	if request.Action != "" {
		if _, err := suno.ParseAction(request.Action); err != nil {
			writeSunoTaskError(c, http.StatusBadRequest, "invalid_request", "Suno fetch action is invalid")
			return
		}
	}
	if len(request.IDs) == 0 {
		writeSunoTaskSuccess(c, []sunoTaskDTO{})
		return
	}
	seen := make(map[string]struct{}, len(request.IDs))
	for _, taskID := range request.IDs {
		if validateSunoTaskPublicID(taskID) != nil {
			writeSunoTaskError(c, http.StatusBadRequest, "invalid_request", "Suno fetch contains an invalid task id")
			return
		}
		if _, duplicate := seen[taskID]; duplicate {
			writeSunoTaskError(c, http.StatusBadRequest, "invalid_request", "Suno fetch task ids must be unique")
			return
		}
		seen[taskID] = struct{}{}
	}
	var tasks []model.Task
	if err := model.DB.Where("task_id IN ? AND user_id = ? AND platform = ?", request.IDs,
		common.GetUserId(c), sunoTaskPlatform).Find(&tasks).Error; err != nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "get_tasks_failed", "failed to query Suno tasks")
		return
	}
	byID := make(map[string]*model.Task, len(tasks))
	for index := range tasks {
		byID[tasks[index].TaskID] = &tasks[index]
	}
	output := make([]sunoTaskDTO, 0, len(tasks))
	dataBytes := 0
	for _, taskID := range request.IDs {
		task := byID[taskID]
		if task == nil {
			continue
		}
		properties, err := decodeSunoTaskProperties(task.Properties)
		if err != nil {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Suno task metadata is invalid")
			return
		}
		if !authorizeSunoTaskRead(c, task, properties) {
			return
		}
		dto, err := sunoTaskDTOFromModel(task)
		if err != nil {
			writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Suno task data is invalid")
			return
		}
		dataBytes += len(dto.Data)
		if dataBytes > sunoFetchResponseDataBudget {
			writeSunoTaskError(c, http.StatusInternalServerError, "response_too_large", "Suno fetch response is too large")
			return
		}
		output = append(output, dto)
	}
	writeSunoTaskSuccess(c, output)
}

func fetchSunoTaskByID(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("id"))
	if validateSunoTaskPublicID(taskID) != nil {
		writeSunoTaskError(c, http.StatusBadRequest, "invalid_request", "Suno task id is invalid")
		return
	}
	var task model.Task
	err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		common.GetUserId(c), sunoTaskPlatform).First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeSunoTaskError(c, http.StatusBadRequest, "task_not_exist", "task_not_exist")
			return
		}
		writeSunoTaskError(c, http.StatusInternalServerError, "get_task_failed", "failed to query Suno task")
		return
	}
	properties, err := decodeSunoTaskProperties(task.Properties)
	if err != nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Suno task metadata is invalid")
		return
	}
	if !authorizeSunoTaskRead(c, &task, properties) {
		return
	}
	dto, err := sunoTaskDTOFromModel(&task)
	if err != nil {
		writeSunoTaskError(c, http.StatusInternalServerError, "task_data_invalid", "Suno task data is invalid")
		return
	}
	writeSunoTaskSuccess(c, dto)
}

func authorizeSunoTaskRead(c *gin.Context, task *model.Task, properties sunoTaskProperties) bool {
	if task == nil || !containsSunoGroup(middleware.GetTokenGroups(c), task.Group) {
		writeSunoTaskError(c, http.StatusForbidden, "group_not_allowed", "token is not allowed to access this task group")
		return false
	}
	if !middleware.RelayModelAllowed(c, properties.OriginModelName) {
		writeSunoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access this model")
		return false
	}
	return true
}

func selectSunoChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := service.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 && constant.ChannelType(channel.Type) == constant.ChannelTypeSunoAPI {
			return channel, group, nil
		}
		if channel == nil || channel.Id <= 0 {
			return nil, "", service.ErrChannelNotFound
		}
		ignored[channel.Id] = struct{}{}
	}
	return nil, "", service.ErrChannelNotFound
}

func containsSunoGroup(groups []string, group string) bool {
	for _, candidate := range groups {
		if candidate == group {
			return true
		}
	}
	return false
}

func rejectDuplicateTopLevelSunoFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid Suno fetch JSON")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid Suno fetch JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate Suno fetch field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	return nil
}

func writeSunoProviderError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	var providerError *suno.ProviderError
	if errors.As(err, &providerError) && providerError.Definitive &&
		providerError.StatusCode >= 400 && providerError.StatusCode <= 499 {
		status = providerError.StatusCode
	}
	writeSunoTaskError(c, status, "upstream_error", "Suno provider rejected the request")
}

func writeSunoTaskError(c *gin.Context, status int, code, message string) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	c.JSON(status, gin.H{"code": code, "message": boundedSunoFailReason(message), "data": nil})
}

func writeSunoTaskEnvelope(c *gin.Context, status int, code, message, taskID string) {
	c.JSON(status, gin.H{"code": code, "message": boundedSunoFailReason(message), "data": taskID})
}

func writeSunoTaskSuccess(c *gin.Context, data any) {
	payload, err := common.Marshal(gin.H{"code": "success", "message": "", "data": data})
	if err != nil || len(payload) > sunoFetchResponseMaxBytes {
		writeSunoTaskError(c, http.StatusInternalServerError, "response_too_large", "Suno fetch response is too large")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}
