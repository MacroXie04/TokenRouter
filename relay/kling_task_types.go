package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/kling"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	klingTaskPlatform                = model.TaskOperationPlatformKling
	klingTaskPrivateDataPrefix       = "kling-video-v1:"
	klingTaskMetadataVersion         = 1
	klingTaskMetadataMaxBytes        = 64 << 10
	klingTaskFailReasonMaxBytes      = 4 << 10
	klingTaskStoredDataMaxBytes      = 16 << 10
	klingTaskChannelBaseURLMaxBytes  = 4 << 10
	klingTaskEncryptedSecretMaxBytes = 16 << 10
)

type klingTaskProperties struct {
	Version           int          `json:"version"`
	Family            string       `json:"family"`
	Prompt            string       `json:"prompt"`
	OriginModelName   string       `json:"origin_model_name"`
	UpstreamModelName string       `json:"upstream_model_name"`
	Action            kling.Action `json:"action"`
	Mode              string       `json:"mode"`
	Duration          string       `json:"duration"`
	AspectRatio       string       `json:"aspect_ratio,omitempty"`
	HasInputReference bool         `json:"has_input_reference,omitempty"`
}

type klingTaskPrivateData struct {
	Version                 int                                   `json:"version"`
	RelayReservationID      string                                `json:"relay_reservation_id"`
	ChannelBaseURL          string                                `json:"channel_base_url"`
	EncryptedChannelKey     string                                `json:"encrypted_channel_key"`
	EncryptedUpstreamTaskID string                                `json:"encrypted_upstream_task_id,omitempty"`
	SettlementPending       bool                                  `json:"settlement_pending"`
	CompletionUnits         int                                   `json:"completion_units,omitempty"`
	Pricing                 service.ReferenceAsyncTaskBillingPlan `json:"pricing"`
	BillingSource           string                                `json:"billing_source"`
	SubscriptionID          int                                   `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64                                 `json:"funding_usage_epoch,omitempty"`
	TokenID                 int                                   `json:"token_id,omitempty"`
}

// klingStoredTaskData is the complete provider-derived public state. It is
// intentionally normalized: upstream task/request identifiers and raw bodies
// never enter Task.Data.
type klingStoredTaskData struct {
	Status            kling.TaskStatus `json:"status"`
	StatusMessage     string           `json:"status_message,omitempty"`
	ResultURL         string           `json:"result_url,omitempty"`
	ProviderCreatedAt int64            `json:"provider_created_at,omitempty"`
	ProviderUpdatedAt int64            `json:"provider_updated_at,omitempty"`
}

type klingTaskError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type klingSubmitResponse struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id,omitempty"`
	Object      string          `json:"object"`
	Model       string          `json:"model"`
	Status      string          `json:"status"`
	Progress    int             `json:"progress"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt int64           `json:"completed_at,omitempty"`
	Seconds     string          `json:"seconds,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
	Error       *klingTaskError `json:"error,omitempty"`
}

type klingTaskDTO struct {
	ID         int64           `json:"id"`
	CreatedAt  int64           `json:"created_at"`
	UpdatedAt  int64           `json:"updated_at"`
	TaskID     string          `json:"task_id"`
	Platform   string          `json:"platform"`
	UserID     int             `json:"user_id"`
	Group      string          `json:"group"`
	ChannelID  int             `json:"channel_id"`
	Quota      int             `json:"quota"`
	Action     string          `json:"action"`
	Status     string          `json:"status"`
	FailReason string          `json:"fail_reason"`
	ResultURL  string          `json:"result_url,omitempty"`
	SubmitTime int64           `json:"submit_time"`
	StartTime  int64           `json:"start_time"`
	FinishTime int64           `json:"finish_time"`
	Progress   string          `json:"progress"`
	Properties any             `json:"properties"`
	Data       json.RawMessage `json:"data"`
}

func marshalKlingTaskProperties(properties klingTaskProperties) (string, error) {
	if properties.Version != klingTaskMetadataVersion || properties.Family != "kling" ||
		!validKlingStoredModel(properties.OriginModelName) || !validKlingStoredModel(properties.UpstreamModelName) ||
		!validKlingAction(properties.Action) || properties.Action != klingActionFromInput(properties.HasInputReference) ||
		(properties.Mode != "std" && properties.Mode != "pro") ||
		(properties.Duration != "5" && properties.Duration != "10") ||
		(properties.AspectRatio != "1:1" && properties.AspectRatio != "16:9" && properties.AspectRatio != "9:16") ||
		strings.TrimSpace(properties.Prompt) == "" || utf8.RuneCountInString(properties.Prompt) > kling.MaxPromptRunes ||
		!utf8.ValidString(properties.Prompt) || hasForbiddenKlingPromptControl(properties.Prompt) {
		return "", errors.New("invalid durable Kling task properties")
	}
	encoded, err := common.Marshal(properties)
	if err != nil {
		return "", err
	}
	if len(encoded) > klingTaskMetadataMaxBytes {
		return "", errors.New("Kling task properties are too large")
	}
	return string(encoded), nil
}

func decodeKlingTaskProperties(raw string) (klingTaskProperties, error) {
	if raw == "" || len(raw) > klingTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return klingTaskProperties{}, errors.New("invalid Kling task properties")
	}
	var properties klingTaskProperties
	if err := common.UnmarshalJsonStr(raw, &properties); err != nil {
		return klingTaskProperties{}, errors.New("invalid Kling task properties")
	}
	if _, err := marshalKlingTaskProperties(properties); err != nil {
		return klingTaskProperties{}, err
	}
	return properties, nil
}

func marshalKlingTaskPrivateData(privateData klingTaskPrivateData) (string, error) {
	if privateData.Version != klingTaskMetadataVersion || privateData.RelayReservationID == "" ||
		privateData.ChannelBaseURL == "" || len(privateData.ChannelBaseURL) > klingTaskChannelBaseURLMaxBytes ||
		privateData.EncryptedChannelKey == "" || len(privateData.EncryptedChannelKey) > klingTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedUpstreamTaskID) > klingTaskEncryptedSecretMaxBytes ||
		privateData.CompletionUnits < 0 || int64(privateData.CompletionUnits) > common.MaxQuota ||
		privateData.BillingSource == "" || !validKlingCapturedBaseURL(privateData.ChannelBaseURL) {
		return "", errors.New("invalid durable Kling task private data")
	}
	if err := privateData.Pricing.Validate(); err != nil {
		return "", errors.New("invalid durable Kling task pricing")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil {
		return "", err
	}
	if len(encoded)+len(klingTaskPrivateDataPrefix) > klingTaskMetadataMaxBytes {
		return "", errors.New("Kling task private data is too large")
	}
	return klingTaskPrivateDataPrefix + string(encoded), nil
}

func decodeKlingTaskPrivateData(raw string) (klingTaskPrivateData, error) {
	if !strings.HasPrefix(raw, klingTaskPrivateDataPrefix) || len(raw) > klingTaskMetadataMaxBytes {
		return klingTaskPrivateData{}, errors.New("invalid Kling task private data")
	}
	var privateData klingTaskPrivateData
	if err := common.UnmarshalJsonStr(strings.TrimPrefix(raw, klingTaskPrivateDataPrefix), &privateData); err != nil {
		return klingTaskPrivateData{}, errors.New("invalid Kling task private data")
	}
	if _, err := marshalKlingTaskPrivateData(privateData); err != nil {
		return klingTaskPrivateData{}, err
	}
	return privateData, nil
}

func marshalKlingStoredTaskData(data klingStoredTaskData) (string, error) {
	if !validKlingStoredStatus(data.Status) || !validKlingResultURL(data.ResultURL) ||
		len(data.StatusMessage) > 4<<10 || !utf8.ValidString(data.StatusMessage) ||
		data.ProviderCreatedAt < 0 || data.ProviderUpdatedAt < 0 {
		return "", errors.New("invalid normalized Kling provider state")
	}
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > klingTaskStoredDataMaxBytes {
		return "", errors.New("normalized Kling provider state is too large")
	}
	return string(encoded), nil
}

func decodeKlingStoredTaskData(raw string) (klingStoredTaskData, error) {
	if raw == "" || raw == "null" {
		return klingStoredTaskData{}, nil
	}
	if len(raw) > klingTaskStoredDataMaxBytes || !utf8.ValidString(raw) {
		return klingStoredTaskData{}, errors.New("invalid stored Kling provider state")
	}
	var data klingStoredTaskData
	if err := common.UnmarshalJsonStr(raw, &data); err != nil {
		return klingStoredTaskData{}, errors.New("invalid stored Kling provider state")
	}
	if _, err := marshalKlingStoredTaskData(data); err != nil {
		return klingStoredTaskData{}, err
	}
	return data, nil
}

func normalizedKlingTaskData(provider *kling.Task) (klingStoredTaskData, error) {
	if provider == nil || !validKlingStoredStatus(provider.Status) {
		return klingStoredTaskData{}, errors.New("invalid Kling provider task")
	}
	data := klingStoredTaskData{
		Status: provider.Status, StatusMessage: boundedKlingFailReason(provider.StatusMessage),
		ResultURL: provider.ResultURL, ProviderCreatedAt: provider.CreatedAt, ProviderUpdatedAt: provider.UpdatedAt,
	}
	if provider.StatusMessage == "" {
		data.StatusMessage = ""
	}
	if _, err := marshalKlingStoredTaskData(data); err != nil {
		return klingStoredTaskData{}, err
	}
	return data, nil
}

func klingTaskDTOFromModel(task *model.Task) (klingTaskDTO, error) {
	if !isKlingTask(task) {
		return klingTaskDTO{}, errors.New("invalid Kling task")
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return klingTaskDTO{}, err
	}
	data, err := decodeKlingStoredTaskData(task.Data)
	if err != nil {
		return klingTaskDTO{}, err
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return klingTaskDTO{}, err
	}
	return klingTaskDTO{
		ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId,
		Group: task.Group, ChannelID: task.ChannelId, Quota: task.Quota,
		Action: task.Action, Status: task.Status, FailReason: task.FailReason,
		ResultURL: data.ResultURL, SubmitTime: task.SubmitTime, StartTime: task.StartTime,
		FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: properties, Data: json.RawMessage(encoded),
	}, nil
}

func klingTaskResponse(task *model.Task) (klingSubmitResponse, error) {
	if !isKlingTask(task) {
		return klingSubmitResponse{}, errors.New("invalid Kling task")
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return klingSubmitResponse{}, err
	}
	data, err := decodeKlingStoredTaskData(task.Data)
	if err != nil {
		return klingSubmitResponse{}, err
	}
	response := klingSubmitResponse{
		ID: task.TaskID, TaskID: task.TaskID, Object: "video", Model: properties.OriginModelName,
		Status: klingPublicStatus(task.Status), Progress: klingProgressNumber(task.Progress),
		CreatedAt: task.CreatedAt, Seconds: properties.Duration,
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		response.CompletedAt = task.FinishTime
	}
	if data.ResultURL != "" {
		response.Metadata = map[string]any{"url": data.ResultURL}
	}
	if task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		code := "video_generation_failed"
		if task.Status == model.TaskStatusUnknown {
			code = "submit_outcome_unknown"
		}
		response.Error = &klingTaskError{Message: boundedKlingFailReason(task.FailReason), Code: code}
	}
	return response, nil
}

func isKlingTask(task *model.Task) bool {
	return task != nil && task.Platform == klingTaskPlatform && task.ChannelId > 0 &&
		strings.HasPrefix(task.PrivateData, klingTaskPrivateDataPrefix)
}

func klingChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	material := taskID + "\x00" + klingTaskPlatform + "\x00" + strconv.Itoa(userID) + "\x00" +
		strconv.Itoa(channelID) + "\x00" + baseURL
	return "kling-channel-v1:" + common.SHA256Hex(material)
}

func klingProviderTaskBinding(
	taskID, reservationID string,
	userID, channelID int,
	action kling.Action,
) string {
	material := taskID + "\x00" + reservationID + "\x00" + klingTaskPlatform + "\x00" +
		strconv.Itoa(userID) + "\x00" + strconv.Itoa(channelID) + "\x00" + string(action)
	return "kling-provider-v1:" + common.SHA256Hex(material)
}

func klingRecoveryJournalBinding(taskID string, action kling.Action) string {
	return "kling-journal-v1:" + common.SHA256Hex(taskID+"\x00"+klingTaskPlatform+"\x00"+string(action))
}

func klingTaskOperationIdentityMatches(task *model.Task, operation *model.TaskOperation, reservationID string) bool {
	if !isKlingTask(task) || operation == nil || operation.TaskID != task.TaskID ||
		operation.ReservationID != reservationID || operation.Platform != klingTaskPlatform ||
		operation.Platform != task.Platform || operation.UserID != task.UserId || operation.ChannelID != task.ChannelId {
		return false
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		return false
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	return err == nil && privateData.RelayReservationID == reservationID &&
		privateData.Pricing.ModelName == properties.OriginModelName
}

func validKlingAction(action kling.Action) bool {
	return action == kling.ActionTextToVideo || action == kling.ActionImageToVideo
}

func validKlingStoredModel(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > kling.MaxModelBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func hasForbiddenKlingPromptControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func klingActionFromInput(hasInput bool) kling.Action {
	if hasInput {
		return kling.ActionImageToVideo
	}
	return kling.ActionTextToVideo
}

func klingTaskStatus(status kling.TaskStatus) string {
	switch status {
	case kling.StatusSubmitted:
		return model.TaskStatusSubmitted
	case kling.StatusProcessing:
		return model.TaskStatusRunning
	case kling.StatusSucceeded:
		return model.TaskStatusSuccess
	case kling.StatusFailed:
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}

func validKlingStoredStatus(status kling.TaskStatus) bool {
	return status == kling.StatusSubmitted || status == kling.StatusProcessing ||
		status == kling.StatusSucceeded || status == kling.StatusFailed
}

func klingTaskProgress(status string) string {
	if status == model.TaskStatusSuccess || status == model.TaskStatusFailure || status == model.TaskStatusUnknown {
		return "100%"
	}
	if status == model.TaskStatusRunning {
		return "50%"
	}
	return "0%"
}

func klingPublicStatus(status string) string {
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

func klingProgressNumber(raw string) int {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "%")
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func boundedKlingFailReason(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		message = "Kling video task failed"
	}
	if len(message) <= klingTaskFailReasonMaxBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := klingTaskFailReasonMaxBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

func validKlingCapturedBaseURL(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func validKlingResultURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > kling.MaxCallbackURLBytes || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validateKlingTaskPublicID(taskID string) error {
	if err := validateVideoTaskPublicID(taskID); err != nil {
		return fmt.Errorf("invalid Kling task id")
	}
	return nil
}
