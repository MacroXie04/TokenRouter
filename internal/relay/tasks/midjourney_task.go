package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/midjourney"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type midjourneyProvider interface {
	Submit(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.Response, error)
	Upload(context.Context, string, string, *midjourney.PreparedRequest) (*midjourney.UploadResponse, error)
	Fetch(context.Context, string, string, []string) ([]midjourney.TaskResult, error)
	ImageSeed(context.Context, string, string, string) (*midjourney.Response, error)
}

var newMidjourneyProvider = func() midjourneyProvider { return &midjourney.Client{} }

// RelayMidjourney implements the complete reference Midjourney and
// MidjourneyPlus route family. Local fetch routes never contact a provider.
func RelayMidjourney(c *gin.Context, state relaycommon.RequestState) {
	mode := channelcatalog.PathToRelayMode(c.Request.URL.Path)
	switch mode {
	case channelcatalog.RelayModeMidjourneyTaskFetch:
		fetchMidjourneyTask(c, state)
	case channelcatalog.RelayModeMidjourneyTaskFetchByCondition:
		fetchMidjourneyTasks(c, state)
	case channelcatalog.RelayModeMidjourneyTaskImageSeed:
		fetchMidjourneyImageSeed(c, state)
	case channelcatalog.RelayModeMidjourneyNotify:
		// The reference router deliberately comments out this route. Keeping the
		// mode fail-closed prevents an accidental unauthenticated callback surface.
		writeMidjourneyError(c, http.StatusNotFound, 4, "Midjourney notify is not routed")
	case channelcatalog.RelayModeUnknown:
		writeMidjourneyError(c, http.StatusNotFound, 4, "unknown Midjourney route")
	default:
		submitMidjourneyTask(c, state, mode)
	}
}

func submitMidjourneyTask(c *gin.Context, state relaycommon.RequestState, mode channelcatalog.RelayMode) {
	operation := midjourneyOperationForMode(mode)
	if operation == "" {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "unknown Midjourney operation")
		return
	}
	raw, err := httpx.ReadAllLimited(c.Request.Body, midjourney.MaxRequestBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeMidjourneyError(c, status, 4, "Midjourney request is invalid or too large")
		return
	}
	prepared, err := midjourney.PrepareSubmit(raw, operation)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeMidjourneyError(c, status, 4, err.Error())
		return
	}
	userID := state.UserID
	token := state.Token
	groups := state.Groups
	if userID <= 0 || token == nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "relay token context is missing")
		return
	}
	if len(groups) == 0 {
		writeMidjourneyError(c, http.StatusForbidden, 4, "token group is unavailable")
		return
	}
	if !state.Allows(prepared.Model) {
		writeMidjourneyError(c, http.StatusForbidden, 4, "token is not allowed to access this model")
		return
	}

	channel, usingGroup, baseURL, channelKey, err := resolveMidjourneySubmitChannel(c, state, prepared, groups)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, errMidjourneyTaskAccess) {
			status = http.StatusForbidden
		} else if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, errMidjourneyTaskState) {
			status = http.StatusBadRequest
		}
		writeMidjourneyError(c, status, 4, boundedMidjourneyFailReason(err.Error()))
		return
	}
	pricing, enabled, err := billingsvc.ResolveReferenceAsyncTaskBillingPlanForUser(
		userID, prepared.Model, state.UserGroup, usingGroup,
	)
	if err != nil || !enabled {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "Midjourney model requires explicit reference pricing")
		return
	}
	if prepared.Action == midjourney.ActionInpaint || prepared.Action == midjourney.ActionCustomZoom {
		pricing = billingsvc.ReferenceAsyncTaskBillingPlan{
			Version: 1, ModelName: prepared.Model, GroupRatio: "0", UseFixedPrice: true, FixedPrice: "0",
			FreeModel: billingsvc.ShouldSkipExplicitFreeModelPreConsume(prepared.Model),
		}
	}
	quota, err := pricing.PreConsumeQuota()
	if err != nil || quota < 0 {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "Midjourney pricing configuration is invalid")
		return
	}
	taskID, err := model.GenerateSecureTaskID()
	if err != nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "failed to generate task identifier")
		return
	}
	propertiesJSON, err := marshalMidjourneyTaskProperties(midjourneyTaskProperties{
		Version: midjourneyTaskMetadataVersion, OriginModelName: prepared.Model,
		Action: prepared.Action, Operation: operation, Prompt: prepared.Value.Prompt,
		ParentTaskID: prepared.OriginalTaskID, Pricing: pricing,
	})
	if err != nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "failed to encode task metadata")
		return
	}
	encryptedKey, err := asyncTaskEncryptBound(channelKey,
		midjourneyChannelCredentialBinding(taskID, userID, channel.Id, baseURL))
	if err != nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "failed to protect channel credentials")
		return
	}
	now := wallclock.NowTimestamp()
	prompt := prepared.Value.Prompt
	if operation == "swap" {
		prompt = "InsightFace"
	}
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: midjourneyTaskPlatform,
		UserId: userID, Group: usingGroup, ChannelId: channel.Id, Quota: quota,
		Action: string(prepared.Action), Status: model.TaskStatusNotStart,
		SubmitTime: now, Progress: "0%", Properties: propertiesJSON, Data: "{}",
	}
	mirror := model.Midjourney{
		UserId: userID, Code: 0, Action: string(prepared.Action), MjId: taskID,
		Prompt: prompt, SubmitTime: now * 1000, Status: model.TaskStatusNotStart,
		Progress: "0%", ChannelId: channel.Id, Quota: quota,
	}
	privateData := midjourneyTaskPrivateData{ChannelBaseURL: baseURL, EncryptedChannelKey: encryptedKey}
	reservation, err := createMidjourneyReservedTask(&task, &mirror, token, &privateData)
	if err != nil {
		status, message := http.StatusInternalServerError, "failed to reserve Midjourney quota"
		if billingsvc.IsSubscriptionFundingErr(err) || errors.Is(err, billingsvc.ErrInsufficientQuota) ||
			errors.Is(err, billingsvc.ErrInsufficientTokenQuota) {
			status, message = http.StatusBadRequest, "user, token, or subscription quota is insufficient"
		} else {
			logging.SysError("create atomic Midjourney reservation: " + err.Error())
		}
		writeMidjourneyError(c, status, 4, message)
		return
	}
	dispatched, finalized := false, false
	defer func() {
		if dispatched || finalized {
			return
		}
		_ = refundMidjourneyTask(&task, reservation, nil, 4,
			"submission stopped before provider dispatch", model.TaskOperationPrepared, "")
	}()
	if err := markMidjourneyTaskDispatching(&task, reservation); err != nil {
		logging.SysError("mark Midjourney dispatch: " + err.Error())
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "failed to persist dispatch state")
		return
	}
	dispatched = true

	if prepared.Action == midjourney.ActionUpload {
		handleMidjourneyUploadDispatch(c, prepared, &task, reservation, baseURL, channelKey)
		return
	}
	response, submitErr := newMidjourneyProvider().Submit(c.Request.Context(), baseURL, channelKey, prepared)
	if submitErr != nil {
		if midjourney.IsDefinitiveRejection(submitErr) {
			code := midjourneyProviderErrorCode(submitErr)
			if err := refundMidjourneyTask(&task, reservation, nil, code,
				"Midjourney provider rejected submission", model.TaskOperationDispatching, ""); err != nil {
				logging.SysError("refund rejected Midjourney task: " + err.Error())
				writeMidjourneyError(c, http.StatusInternalServerError, 4, "submission failed and quota cleanup is pending")
				return
			}
			finalized = true
			writeMidjourneyProviderError(c, submitErr)
			return
		}
		if !midjourney.SubmitWasDispatched(submitErr) {
			if err := refundMidjourneyTask(&task, reservation, nil, 5,
				"Midjourney submission stopped before provider dispatch", model.TaskOperationDispatching, ""); err != nil {
				logging.SysError("refund undispatched Midjourney task: " + err.Error())
				writeMidjourneyError(c, http.StatusInternalServerError, 4, "submission failed and quota cleanup is pending")
				return
			}
			finalized = true
			writeMidjourneyError(c, http.StatusBadGateway, 5, "Midjourney submission could not be dispatched")
			return
		}
		if err := markMidjourneyAmbiguousDispatch(&task, reservation.ReservationID(), midjourneyUnknownDispatchReason); err != nil {
			logging.SysError("persist ambiguous Midjourney dispatch: " + err.Error())
		}
		finalized = true
		c.JSON(http.StatusAccepted, midjourney.Response{
			Code: 5, Description: midjourneyUnknownDispatchReason, Result: task.TaskID,
		})
		return
	}
	providerState := midjourneyStateFromSubmit(prepared, response, channelKey)
	if providerState == nil {
		_ = markMidjourneyAmbiguousDispatch(&task, reservation.ReservationID(), "Midjourney provider response is unsafe")
		finalized = true
		writeMidjourneyError(c, http.StatusBadGateway, 5, "Midjourney provider returned an unsafe response")
		return
	}
	journalErr := persistAcceptedMidjourneyRecoveryJournal(&task, reservation.ReservationID(),
		providerState.ProviderTaskID, prepared.Action)
	if journalErr != nil {
		logging.SysError("persist accepted Midjourney recovery journal " + task.TaskID + ": " + journalErr.Error())
	}
	status := normalizeMidjourneyTaskStatus(providerState.Status)
	if status == model.TaskStatusSuccess {
		err = settleSuccessfulMidjourneyTask(&task, reservation, providerState, response.Code,
			model.TaskOperationDispatching, "")
	} else if status == model.TaskStatusFailure {
		err = refundMidjourneyTask(&task, reservation, providerState, response.Code,
			providerState.FailReason, model.TaskOperationDispatching, "")
	} else {
		err = persistAcceptedMidjourneyTask(&task, reservation.ReservationID(), providerState,
			response.Code, model.TaskOperationDispatching, "")
	}
	if err != nil {
		logging.SysError("persist accepted Midjourney task: " + err.Error())
		fallbackErr := persistAcceptedMidjourneyFallback(&task, reservation.ReservationID(),
			providerState.ProviderTaskID)
		if fallbackErr != nil {
			logging.SysError("persist accepted Midjourney fallback " + task.TaskID + ": " + fallbackErr.Error())
		} else if cleanupErr := removeMidjourneyRecoveryJournal(task.TaskID); cleanupErr != nil {
			logging.SysError("remove accepted Midjourney recovery journal " + task.TaskID + ": " + cleanupErr.Error())
		}
		writeMidjourneyError(c, http.StatusAccepted, 5, "Midjourney task was accepted and persistence recovery is pending")
		return
	}
	if cleanupErr := removeMidjourneyRecoveryJournal(task.TaskID); cleanupErr != nil && journalErr == nil {
		logging.SysError("remove accepted Midjourney recovery journal " + task.TaskID + ": " + cleanupErr.Error())
	}
	finalized = true
	if status == model.TaskStatusSuccess {
		_ = billingsvc.DeliverAuditLogOutboxEvent("mj:" + reservation.ReservationID())
	}
	properties := any(nil)
	if len(response.Properties) > 0 && string(response.Properties) != "null" {
		properties = response.Properties
	}
	c.JSON(http.StatusOK, midjourney.Response{
		Code: 1, Description: safeMidjourneyText(response.Description, channelKey),
		Properties: rawMidjourneyProperty(properties), Result: task.TaskID,
	})
}

func handleMidjourneyUploadDispatch(c *gin.Context, prepared *midjourney.PreparedRequest,
	task *model.Task, reservation *billingsvc.RelayQuotaReservation, baseURL, channelKey string) {
	response, err := newMidjourneyProvider().Upload(c.Request.Context(), baseURL, channelKey, prepared)
	if err != nil {
		if midjourney.IsDefinitiveRejection(err) {
			code := midjourneyProviderErrorCode(err)
			if refundErr := refundMidjourneyTask(task, reservation, nil, code,
				"Midjourney provider rejected upload", model.TaskOperationDispatching, ""); refundErr != nil {
				writeMidjourneyError(c, http.StatusInternalServerError, 4, "upload failed and quota cleanup is pending")
				return
			}
			writeMidjourneyProviderError(c, err)
			return
		}
		if !midjourney.SubmitWasDispatched(err) {
			if refundErr := refundMidjourneyTask(task, reservation, nil, 5,
				"Midjourney upload stopped before provider dispatch", model.TaskOperationDispatching, ""); refundErr != nil {
				writeMidjourneyError(c, http.StatusInternalServerError, 4, "upload failed and quota cleanup is pending")
				return
			}
			writeMidjourneyError(c, http.StatusBadGateway, 5, "Midjourney upload could not be dispatched")
			return
		}
		_ = markMidjourneyAmbiguousDispatch(task, reservation.ReservationID(), midjourneyUnknownDispatchReason)
		c.JSON(http.StatusAccepted, midjourney.Response{Code: 5, Description: midjourneyUnknownDispatchReason, Result: task.TaskID})
		return
	}
	if midjourneyUploadContainsSecret(response, channelKey) {
		_ = markMidjourneyAmbiguousDispatch(task, reservation.ReservationID(), "Midjourney provider response is unsafe")
		writeMidjourneyError(c, http.StatusBadGateway, 5, "Midjourney provider returned an unsafe response")
		return
	}
	hash := sha256.Sum256([]byte(strings.Join(response.Result, "\x00")))
	providerID := "upload_" + hex.EncodeToString(hash[:16])
	state := &midjourney.TaskResult{
		ProviderTaskID: providerID, Action: string(midjourney.ActionUpload), Status: model.TaskStatusSuccess,
		Progress: "100%", ImageURL: response.Result[0], Description: safeMidjourneyText(response.Description, channelKey),
	}
	if err := settleSuccessfulMidjourneyTask(task, reservation, state, response.Code,
		model.TaskOperationDispatching, ""); err != nil {
		logging.SysError("settle Midjourney upload: " + err.Error())
		writeMidjourneyError(c, http.StatusAccepted, 5, "Midjourney upload completed and accounting recovery is pending")
		return
	}
	_ = billingsvc.DeliverAuditLogOutboxEvent("mj:" + reservation.ReservationID())
	c.JSON(http.StatusOK, midjourney.UploadResponse{
		Code: 1, Description: safeMidjourneyText(response.Description, channelKey), Result: response.Result,
	})
}

var (
	errMidjourneyTaskAccess = errors.New("token cannot access the Midjourney task")
	errMidjourneyTaskState  = errors.New("Midjourney task is not in a valid state")
)

func resolveMidjourneySubmitChannel(c *gin.Context, state relaycommon.RequestState, prepared *midjourney.PreparedRequest, groups []string) (
	*model.Channel, string, string, string, error,
) {
	if prepared.OriginalTaskID == "" {
		channel, group, err := selectMidjourneyChannel(groups, prepared.Model)
		if err != nil {
			return nil, "", "", "", errors.New("no Midjourney channel is available")
		}
		baseURL, err := midjourney.ValidateBaseURL(channel.BaseURL)
		if err != nil {
			return nil, "", "", "", errors.New("Midjourney channel base URL is invalid")
		}
		key := channelssvc.GetChannelKey(channel)
		if strings.TrimSpace(key) == "" {
			return nil, "", "", "", errors.New("Midjourney channel has no available key")
		}
		return channel, group, baseURL, key, nil
	}
	parent, parentMirror, _, privateData, providerID, channelKey, err := loadOwnedMidjourneyTaskCredentials(
		c, state, prepared.OriginalTaskID, groups,
	)
	if err != nil {
		return nil, "", "", "", err
	}
	if parent.Status != model.TaskStatusSuccess && prepared.Action != midjourney.ActionModal {
		return nil, "", "", "", errMidjourneyTaskState
	}
	if err := midjourney.BindProviderTaskID(prepared, providerID); err != nil {
		return nil, "", "", "", errors.New("Midjourney parent task identity is invalid")
	}
	prepared.Value.Prompt = parentMirror.Prompt
	var channel model.Channel
	if err := model.DB.Where("id = ? AND status = ?", parent.ChannelId, channelcatalog.ChannelStatusEnabled).
		First(&channel).Error; err != nil {
		return nil, "", "", "", errors.New("Midjourney parent channel is disabled or unavailable")
	}
	if !isMidjourneyChannelType(channel.Type) || channel.Id != parent.ChannelId {
		return nil, "", "", "", errors.New("Midjourney parent channel is invalid")
	}
	return &channel, parent.Group, privateData.ChannelBaseURL, channelKey, nil
}

func loadOwnedMidjourneyTaskCredentials(c *gin.Context, state relaycommon.RequestState, taskID string, groups []string) (
	*model.Task, *model.Midjourney, midjourneyTaskProperties, midjourneyTaskPrivateData, string, string, error,
) {
	if validateMidjourneyTaskPublicID(taskID) != nil {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", gorm.ErrRecordNotFound
	}
	var task model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, midjourneyTaskPlatform).First(&task).Error; err != nil {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", err
	}
	var mirror model.Midjourney
	if err := model.DB.Where("mj_id = ? AND user_id = ? AND channel_id = ?", taskID,
		task.UserId, task.ChannelId).First(&mirror).Error; err != nil {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", err
	}
	if !containsMidjourneyGroup(groups, task.Group) {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", errMidjourneyTaskAccess
	}
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", errors.New("Midjourney task metadata is invalid")
	}
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", errors.New("Midjourney task private metadata is invalid")
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
		privateData.RelayReservationID, midjourneyTaskPlatform).First(&operation).Error; err != nil ||
		!midjourneyTaskOperationIdentityMatches(&task, &mirror, &operation, privateData.RelayReservationID) {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", errors.New("Midjourney task recovery state is invalid")
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		midjourneyProviderTaskBinding(task.TaskID, operation.ReservationID, task.UserId, task.ChannelId, properties.Action))
	if err != nil || strings.TrimSpace(providerID) == "" {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", errors.New("Midjourney provider task identity is unavailable")
	}
	channelKey, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		midjourneyChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil || strings.TrimSpace(channelKey) == "" {
		return nil, nil, midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, "", "", errors.New("Midjourney channel credential is unavailable")
	}
	return &task, &mirror, properties, privateData, providerID, channelKey, nil
}

func fetchMidjourneyTask(c *gin.Context, state relaycommon.RequestState) {
	taskID := strings.TrimSpace(c.Param("id"))
	groups := state.Groups
	if validateMidjourneyTaskPublicID(taskID) != nil {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "task_no_found")
		return
	}
	var generic model.Task
	if err := model.DB.Where("task_id = ? AND user_id = ? AND platform = ?", taskID,
		state.UserID, midjourneyTaskPlatform).First(&generic).Error; err != nil {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "task_no_found")
		return
	}
	if !containsMidjourneyGroup(groups, generic.Group) {
		writeMidjourneyError(c, http.StatusForbidden, 4, "token is not allowed to access this task group")
		return
	}
	properties, err := decodeMidjourneyTaskProperties(generic.Properties)
	if err != nil || !state.Allows(properties.OriginModelName) {
		writeMidjourneyError(c, http.StatusForbidden, 4, "token is not allowed to access this task model")
		return
	}
	var mirror model.Midjourney
	if err := model.DB.Where("mj_id = ? AND user_id = ? AND channel_id = ?", taskID,
		generic.UserId, generic.ChannelId).First(&mirror).Error; err != nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "Midjourney task data is unavailable")
		return
	}
	dto, err := midjourneyTaskDTOFromModel(&mirror, &generic)
	if err != nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "Midjourney task data is invalid")
		return
	}
	rewriteMidjourneyTaskImageURL(&dto, false)
	c.JSON(http.StatusOK, dto)
}

type midjourneyFetchRequest struct {
	IDs []string `json:"ids"`
}

func fetchMidjourneyTasks(c *gin.Context, state relaycommon.RequestState) {
	raw, err := httpx.ReadAllLimited(c.Request.Body, midjourneyFetchRequestMaxBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeMidjourneyError(c, status, 4, "Midjourney task query is invalid or too large")
		return
	}
	var request midjourneyFetchRequest
	if rejectDuplicateTopLevelMidjourneyFields(raw) != nil || strictMidjourneyTaskJSON(raw, &request) != nil ||
		len(request.IDs) > midjourney.MaxBatchTasks {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "Midjourney task query is invalid")
		return
	}
	if len(request.IDs) == 0 {
		c.JSON(http.StatusOK, []midjourneyTaskDTO{})
		return
	}
	seen := make(map[string]struct{}, len(request.IDs))
	for _, id := range request.IDs {
		if validateMidjourneyTaskPublicID(id) != nil {
			writeMidjourneyError(c, http.StatusBadRequest, 4, "Midjourney task query contains an invalid id")
			return
		}
		if _, duplicate := seen[id]; duplicate {
			writeMidjourneyError(c, http.StatusBadRequest, 4, "Midjourney task ids must be unique")
			return
		}
		seen[id] = struct{}{}
	}
	var generic []model.Task
	if err := model.DB.Where("task_id IN ? AND user_id = ? AND platform = ?", request.IDs,
		state.UserID, midjourneyTaskPlatform).Find(&generic).Error; err != nil {
		writeMidjourneyError(c, http.StatusInternalServerError, 4, "failed to query Midjourney tasks")
		return
	}
	byID := make(map[string]*model.Task, len(generic))
	for index := range generic {
		byID[generic[index].TaskID] = &generic[index]
	}
	output := make([]midjourneyTaskDTO, 0, len(generic))
	groups := state.Groups
	for _, id := range request.IDs {
		task := byID[id]
		if task == nil {
			continue
		}
		properties, err := decodeMidjourneyTaskProperties(task.Properties)
		if err != nil || !containsMidjourneyGroup(groups, task.Group) || !state.Allows(properties.OriginModelName) {
			writeMidjourneyError(c, http.StatusForbidden, 4, "token is not allowed to access a requested task")
			return
		}
		var mirror model.Midjourney
		if err := model.DB.Where("mj_id = ? AND user_id = ? AND channel_id = ?", id,
			task.UserId, task.ChannelId).First(&mirror).Error; err != nil {
			writeMidjourneyError(c, http.StatusInternalServerError, 4, "Midjourney task data is unavailable")
			return
		}
		dto, err := midjourneyTaskDTOFromModel(&mirror, task)
		if err != nil {
			writeMidjourneyError(c, http.StatusInternalServerError, 4, "Midjourney task data is invalid")
			return
		}
		rewriteMidjourneyTaskImageURL(&dto, false)
		output = append(output, dto)
	}
	c.JSON(http.StatusOK, output)
}

func fetchMidjourneyImageSeed(c *gin.Context, state relaycommon.RequestState) {
	groups := state.Groups
	task, _, properties, privateData, providerID, channelKey, err := loadOwnedMidjourneyTaskCredentials(
		c, state, strings.TrimSpace(c.Param("id")), groups,
	)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errMidjourneyTaskAccess) {
			status = http.StatusForbidden
		}
		writeMidjourneyError(c, status, 4, "task_no_found")
		return
	}
	if !state.Allows(properties.OriginModelName) {
		writeMidjourneyError(c, http.StatusForbidden, 4, "token is not allowed to access this task model")
		return
	}
	var channel model.Channel
	if err := model.DB.Where("id = ? AND status = ?", task.ChannelId, channelcatalog.ChannelStatusEnabled).
		First(&channel).Error; err != nil || !isMidjourneyChannelType(channel.Type) {
		writeMidjourneyError(c, http.StatusBadRequest, 4, "Midjourney task channel is disabled")
		return
	}
	response, err := newMidjourneyProvider().ImageSeed(c.Request.Context(), privateData.ChannelBaseURL,
		channelKey, providerID)
	if err != nil {
		writeMidjourneyProviderError(c, err)
		return
	}
	if response == nil || midjourneyResponseContainsSecret(response, channelKey) {
		writeMidjourneyError(c, http.StatusBadGateway, 5, "Midjourney provider returned an unsafe response")
		return
	}
	response.Description = safeMidjourneyText(response.Description, channelKey)
	c.JSON(http.StatusOK, response)
}

func selectMidjourneyChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 64; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel != nil && channel.Id > 0 && isMidjourneyChannelType(channel.Type) {
			return channel, group, nil
		}
		if channel == nil {
			return nil, "", channelssvc.ErrChannelNotFound
		}
		ignored[channel.Id] = struct{}{}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func isMidjourneyChannelType(channelType int) bool {
	t := channelcatalog.ChannelType(channelType)
	return t == channelcatalog.ChannelTypeMidjourney || t == channelcatalog.ChannelTypeMidjourneyPlus
}

func containsMidjourneyGroup(groups []string, group string) bool {
	for _, candidate := range groups {
		if candidate == group {
			return true
		}
	}
	return false
}

func midjourneyOperationForMode(mode channelcatalog.RelayMode) string {
	switch mode {
	case channelcatalog.RelayModeMidjourneyAction:
		return "action"
	case channelcatalog.RelayModeMidjourneyShorten:
		return "shorten"
	case channelcatalog.RelayModeMidjourneyModal:
		return "modal"
	case channelcatalog.RelayModeMidjourneyImagine:
		return "imagine"
	case channelcatalog.RelayModeMidjourneyChange:
		return "change"
	case channelcatalog.RelayModeMidjourneySimpleChange:
		return "simple-change"
	case channelcatalog.RelayModeMidjourneyDescribe:
		return "describe"
	case channelcatalog.RelayModeMidjourneyBlend:
		return "blend"
	case channelcatalog.RelayModeMidjourneyEdits:
		return "edits"
	case channelcatalog.RelayModeMidjourneyVideo:
		return "video"
	case channelcatalog.RelayModeSwapFace:
		return "swap"
	case channelcatalog.RelayModeMidjourneyUpload:
		return "upload-discord-images"
	default:
		return ""
	}
}

func midjourneyStateFromSubmit(prepared *midjourney.PreparedRequest, response *midjourney.Response,
	channelKey string) *midjourney.TaskResult {
	if prepared == nil || response == nil ||
		(response.Code != 1 && response.Code != 21 && response.Code != 22) ||
		midjourney.ValidateProviderTaskID(response.Result) != nil ||
		midjourneyResponseContainsSecret(response, channelKey) {
		return nil
	}
	state := &midjourney.TaskResult{
		ProviderTaskID: response.Result, Action: string(prepared.Action), Prompt: prepared.Value.Prompt,
		Description: safeMidjourneyText(response.Description, channelKey), Status: model.TaskStatusSubmitted,
		Progress: "0%", Properties: append(json.RawMessage(nil), response.Properties...),
	}
	if response.Code == 21 && len(response.Properties) > 0 && string(response.Properties) != "null" {
		var known struct {
			Status   string `json:"status"`
			ImageURL string `json:"imageUrl"`
		}
		if json.Unmarshal(response.Properties, &known) == nil {
			normalized := normalizeMidjourneyTaskStatus(known.Status)
			if (known.Status != "" && normalized == "") || midjourney.ValidateResultURL(known.ImageURL) != nil {
				return nil
			}
			if normalized != "" {
				state.Status = normalized
			}
			state.ImageURL = known.ImageURL
			if state.Status == model.TaskStatusSuccess || state.Status == model.TaskStatusFailure {
				state.Progress = "100%"
			}
		}
	}
	return state
}

func rewriteMidjourneyTaskImageURL(dto *midjourneyTaskDTO, modePrefixed bool) {
	if dto == nil || dto.ImageURL == "" || !setting.GetOptionBool(setting.MjForwardURLEnabledOption, true) {
		return
	}
	base := strings.TrimRight(setting.GetOptionOrDefault(setting.ServerAddressOption, "http://localhost:3000"), "/")
	dto.ImageURL = base + "/mj/image/" + dto.ID
	if dto.Status != model.TaskStatusSuccess {
		dto.ImageURL += "?rand=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
}

func rawMidjourneyProperty(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return raw
	}
	return nil
}

func midjourneyProviderErrorCode(err error) int {
	var providerError *midjourney.ProviderError
	if errors.As(err, &providerError) && providerError.Code > 0 {
		return providerError.Code
	}
	return 5
}

func writeMidjourneyProviderError(c *gin.Context, err error) {
	code := midjourneyProviderErrorCode(err)
	status := http.StatusBadGateway
	var providerError *midjourney.ProviderError
	if errors.As(err, &providerError) && providerError.Definitive &&
		providerError.StatusCode >= 400 && providerError.StatusCode <= 499 {
		status = providerError.StatusCode
	}
	if code == 30 {
		status = http.StatusTooManyRequests
	}
	writeMidjourneyError(c, status, code, "Midjourney provider rejected the request")
}

func writeMidjourneyError(c *gin.Context, status, code int, description string) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	if code <= 0 {
		code = 5
	}
	c.JSON(status, gin.H{
		"description": boundedMidjourneyFailReason(description), "type": "upstream_error", "code": code,
	})
}

func safeMidjourneyText(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return boundedMidjourneyFailReason(value)
}

func midjourneyResponseContainsSecret(response *midjourney.Response, secret string) bool {
	if response == nil || secret == "" {
		return false
	}
	return strings.Contains(response.Description, secret) || strings.Contains(response.Result, secret) ||
		bytes.Contains(response.Properties, []byte(secret))
}

func midjourneyUploadContainsSecret(response *midjourney.UploadResponse, secret string) bool {
	if response == nil || secret == "" {
		return false
	}
	if strings.Contains(response.Description, secret) {
		return true
	}
	for _, item := range response.Result {
		if strings.Contains(item, secret) {
			return true
		}
	}
	return false
}

func rejectDuplicateTopLevelMidjourneyFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid Midjourney JSON")
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid Midjourney JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate Midjourney field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing Midjourney JSON")
	}
	return nil
}
