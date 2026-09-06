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

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/task/doubao"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	doubaoTaskPrivateDataPrefix       = "doubao-video-v1:"
	doubaoTaskMetadataVersion         = 1
	doubaoTaskMetadataMaxBytes        = 64 << 10
	doubaoTaskFailReasonMaxBytes      = 4 << 10
	doubaoTaskStoredDataMaxBytes      = 16 << 10
	doubaoTaskChannelBaseURLMaxBytes  = 4 << 10
	doubaoTaskEncryptedSecretMaxBytes = 16 << 10
)

type doubaoTaskProperties struct {
	Version           int           `json:"version"`
	Family            string        `json:"family"`
	Prompt            string        `json:"prompt"`
	OriginModelName   string        `json:"origin_model_name"`
	UpstreamModelName string        `json:"upstream_model_name"`
	Action            doubao.Action `json:"action"`
	Duration          int           `json:"duration,omitempty"`
	Resolution        string        `json:"resolution,omitempty"`
	HasVideoInput     bool          `json:"has_video_input,omitempty"`
	VideoInputRatio   string        `json:"video_input_ratio"`
}

type doubaoTaskPrivateData struct {
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

// doubaoStoredTaskData is the complete provider-derived public state. It is
// intentionally normalized: upstream task/request identifiers and raw bodies
// never enter Task.Data.
type doubaoStoredTaskData struct {
	Status            doubao.TaskStatus `json:"status"`
	StatusMessage     string            `json:"status_message,omitempty"`
	ErrorCode         string            `json:"error_code,omitempty"`
	ResultURL         string            `json:"result_url,omitempty"`
	ProviderCreatedAt int64             `json:"provider_created_at,omitempty"`
	ProviderUpdatedAt int64             `json:"provider_updated_at,omitempty"`
}

type doubaoTaskError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type doubaoSubmitResponse struct {
	ID          string           `json:"id"`
	TaskID      string           `json:"task_id,omitempty"`
	Object      string           `json:"object"`
	Model       string           `json:"model"`
	Status      string           `json:"status"`
	Progress    int              `json:"progress"`
	CreatedAt   int64            `json:"created_at"`
	CompletedAt int64            `json:"completed_at,omitempty"`
	Seconds     string           `json:"seconds,omitempty"`
	Metadata    map[string]any   `json:"metadata,omitempty"`
	Error       *doubaoTaskError `json:"error,omitempty"`
}

type doubaoTaskDTO struct {
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

func marshalDoubaoTaskProperties(properties doubaoTaskProperties) (string, error) {
	if properties.Version != doubaoTaskMetadataVersion || properties.Family != "doubao" ||
		!validDoubaoStoredModel(properties.OriginModelName) || !validDoubaoStoredModel(properties.UpstreamModelName) ||
		!validDoubaoAction(properties.Action) || properties.Duration < 0 || properties.Duration > doubao.MaxDuration ||
		len(properties.Resolution) > doubao.MaxStringFieldBytes || !utf8.ValidString(properties.Resolution) ||
		strings.TrimSpace(properties.Prompt) == "" || utf8.RuneCountInString(properties.Prompt) > doubao.MaxPromptRunes ||
		!utf8.ValidString(properties.Prompt) || hasForbiddenDoubaoPromptControl(properties.Prompt) {
		return "", errors.New("invalid durable Doubao task properties")
	}
	if !common.IsSafeDecimalLiteral(properties.VideoInputRatio) {
		return "", errors.New("invalid durable Doubao video-input ratio")
	}
	ratio, err := decimal.NewFromString(properties.VideoInputRatio)
	if err != nil || ratio.LessThanOrEqual(decimal.Zero) || ratio.GreaterThan(decimal.NewFromInt(100)) {
		return "", errors.New("invalid durable Doubao video-input ratio")
	}
	encoded, err := common.Marshal(properties)
	if err != nil {
		return "", err
	}
	if len(encoded) > doubaoTaskMetadataMaxBytes {
		return "", errors.New("Doubao task properties are too large")
	}
	return string(encoded), nil
}

func decodeDoubaoTaskProperties(raw string) (doubaoTaskProperties, error) {
	if raw == "" || len(raw) > doubaoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return doubaoTaskProperties{}, errors.New("invalid Doubao task properties")
	}
	var properties doubaoTaskProperties
	if err := common.UnmarshalJsonStr(raw, &properties); err != nil {
		return doubaoTaskProperties{}, errors.New("invalid Doubao task properties")
	}
	if _, err := marshalDoubaoTaskProperties(properties); err != nil {
		return doubaoTaskProperties{}, err
	}
	return properties, nil
}

func marshalDoubaoTaskPrivateData(privateData doubaoTaskPrivateData) (string, error) {
	if privateData.Version != doubaoTaskMetadataVersion || privateData.RelayReservationID == "" ||
		privateData.ChannelBaseURL == "" || len(privateData.ChannelBaseURL) > doubaoTaskChannelBaseURLMaxBytes ||
		privateData.EncryptedChannelKey == "" || len(privateData.EncryptedChannelKey) > doubaoTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedUpstreamTaskID) > doubaoTaskEncryptedSecretMaxBytes ||
		privateData.CompletionUnits < 0 || int64(privateData.CompletionUnits) > common.MaxQuota ||
		privateData.BillingSource == "" || !validDoubaoCapturedBaseURL(privateData.ChannelBaseURL) {
		return "", errors.New("invalid durable Doubao task private data")
	}
	if err := privateData.Pricing.Validate(); err != nil {
		return "", errors.New("invalid durable Doubao task pricing")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil {
		return "", err
	}
	if len(encoded)+len(doubaoTaskPrivateDataPrefix) > doubaoTaskMetadataMaxBytes {
		return "", errors.New("Doubao task private data is too large")
	}
	return doubaoTaskPrivateDataPrefix + string(encoded), nil
}

func decodeDoubaoTaskPrivateData(raw string) (doubaoTaskPrivateData, error) {
	if !strings.HasPrefix(raw, doubaoTaskPrivateDataPrefix) || len(raw) > doubaoTaskMetadataMaxBytes {
		return doubaoTaskPrivateData{}, errors.New("invalid Doubao task private data")
	}
	var privateData doubaoTaskPrivateData
	if err := common.UnmarshalJsonStr(strings.TrimPrefix(raw, doubaoTaskPrivateDataPrefix), &privateData); err != nil {
		return doubaoTaskPrivateData{}, errors.New("invalid Doubao task private data")
	}
	if _, err := marshalDoubaoTaskPrivateData(privateData); err != nil {
		return doubaoTaskPrivateData{}, err
	}
	return privateData, nil
}

func marshalDoubaoStoredTaskData(data doubaoStoredTaskData) (string, error) {
	if !validDoubaoStoredStatus(data.Status) || !validDoubaoResultURL(data.ResultURL) ||
		len(data.StatusMessage) > 4<<10 || !utf8.ValidString(data.StatusMessage) ||
		len(data.ErrorCode) > doubao.MaxProviderCodeBytes || !utf8.ValidString(data.ErrorCode) ||
		data.ProviderCreatedAt < 0 || data.ProviderUpdatedAt < 0 {
		return "", errors.New("invalid normalized Doubao provider state")
	}
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > doubaoTaskStoredDataMaxBytes {
		return "", errors.New("normalized Doubao provider state is too large")
	}
	return string(encoded), nil
}

func decodeDoubaoStoredTaskData(raw string) (doubaoStoredTaskData, error) {
	if raw == "" || raw == "null" {
		return doubaoStoredTaskData{}, nil
	}
	if len(raw) > doubaoTaskStoredDataMaxBytes || !utf8.ValidString(raw) {
		return doubaoStoredTaskData{}, errors.New("invalid stored Doubao provider state")
	}
	var data doubaoStoredTaskData
	if err := common.UnmarshalJsonStr(raw, &data); err != nil {
		return doubaoStoredTaskData{}, errors.New("invalid stored Doubao provider state")
	}
	if _, err := marshalDoubaoStoredTaskData(data); err != nil {
		return doubaoStoredTaskData{}, err
	}
	return data, nil
}

func normalizedDoubaoTaskData(provider *doubao.Task) (doubaoStoredTaskData, error) {
	if provider == nil || !validDoubaoStoredStatus(provider.Status) {
		return doubaoStoredTaskData{}, errors.New("invalid Doubao provider task")
	}
	data := doubaoStoredTaskData{
		Status: provider.Status, StatusMessage: boundedDoubaoFailReason(provider.StatusMessage),
		ErrorCode: provider.ErrorCode,
		ResultURL: provider.ResultURL, ProviderCreatedAt: provider.CreatedAt, ProviderUpdatedAt: provider.UpdatedAt,
	}
	if provider.StatusMessage == "" {
		data.StatusMessage = ""
	}
	if _, err := marshalDoubaoStoredTaskData(data); err != nil {
		return doubaoStoredTaskData{}, err
	}
	return data, nil
}

func doubaoTaskDTOFromModel(task *model.Task) (doubaoTaskDTO, error) {
	if !isDoubaoTask(task) {
		return doubaoTaskDTO{}, errors.New("invalid Doubao task")
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return doubaoTaskDTO{}, err
	}
	data, err := decodeDoubaoStoredTaskData(task.Data)
	if err != nil {
		return doubaoTaskDTO{}, err
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return doubaoTaskDTO{}, err
	}
	return doubaoTaskDTO{
		ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId,
		Group: task.Group, ChannelID: task.ChannelId, Quota: task.Quota,
		Action: task.Action, Status: task.Status, FailReason: task.FailReason,
		ResultURL: data.ResultURL, SubmitTime: task.SubmitTime, StartTime: task.StartTime,
		FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: properties, Data: json.RawMessage(encoded),
	}, nil
}

func doubaoTaskResponse(task *model.Task) (doubaoSubmitResponse, error) {
	if !isDoubaoTask(task) {
		return doubaoSubmitResponse{}, errors.New("invalid Doubao task")
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return doubaoSubmitResponse{}, err
	}
	data, err := decodeDoubaoStoredTaskData(task.Data)
	if err != nil {
		return doubaoSubmitResponse{}, err
	}
	response := doubaoSubmitResponse{
		ID: task.TaskID, TaskID: task.TaskID, Object: "video", Model: properties.OriginModelName,
		Status: doubaoPublicStatus(task.Status), Progress: doubaoProgressNumber(task.Progress),
		CreatedAt: task.CreatedAt,
	}
	if properties.Duration > 0 {
		response.Seconds = strconv.Itoa(properties.Duration)
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
		response.Error = &doubaoTaskError{Message: boundedDoubaoFailReason(task.FailReason), Code: code}
	}
	return response, nil
}

func isDoubaoTask(task *model.Task) bool {
	return task != nil && model.IsDoubaoVideoTaskOperationPlatform(task.Platform) && task.ChannelId > 0 &&
		strings.HasPrefix(task.PrivateData, doubaoTaskPrivateDataPrefix)
}

func doubaoTaskPlatformForChannelType(channelType int) (string, bool) {
	switch channelType {
	case 45:
		return model.TaskOperationPlatformVolcEngine, true
	case 54:
		return model.TaskOperationPlatformDoubaoVideo, true
	default:
		return "", false
	}
}

func doubaoChannelCredentialBinding(taskID, platform string, userID, channelID int, baseURL string) string {
	material := taskID + "\x00" + platform + "\x00" + strconv.Itoa(userID) + "\x00" +
		strconv.Itoa(channelID) + "\x00" + baseURL
	return "doubao-channel-v1:" + common.SHA256Hex(material)
}

func doubaoProviderTaskBinding(
	taskID, reservationID, platform string,
	userID, channelID int,
	action doubao.Action,
) string {
	material := taskID + "\x00" + reservationID + "\x00" + platform + "\x00" +
		strconv.Itoa(userID) + "\x00" + strconv.Itoa(channelID) + "\x00" + string(action)
	return "doubao-provider-v1:" + common.SHA256Hex(material)
}

func doubaoRecoveryJournalBinding(taskID, platform string, action doubao.Action) string {
	return "doubao-journal-v1:" + common.SHA256Hex(taskID+"\x00"+platform+"\x00"+string(action))
}

func doubaoTaskOperationIdentityMatches(task *model.Task, operation *model.TaskOperation, reservationID string) bool {
	if !isDoubaoTask(task) || operation == nil || operation.TaskID != task.TaskID ||
		operation.ReservationID != reservationID || !model.IsDoubaoVideoTaskOperationPlatform(operation.Platform) ||
		operation.Platform != task.Platform || operation.UserID != task.UserId || operation.ChannelID != task.ChannelId {
		return false
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		return false
	}
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	return err == nil && privateData.RelayReservationID == reservationID &&
		privateData.Pricing.ModelName == properties.OriginModelName
}

func validDoubaoAction(action doubao.Action) bool {
	return action == doubao.ActionGenerate
}

func validDoubaoStoredModel(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > doubao.MaxModelBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func hasForbiddenDoubaoPromptControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func doubaoTaskStatus(status doubao.TaskStatus) string {
	switch status {
	case doubao.StatusSubmitted:
		return model.TaskStatusSubmitted
	case doubao.StatusProcessing:
		return model.TaskStatusRunning
	case doubao.StatusSucceeded:
		return model.TaskStatusSuccess
	case doubao.StatusFailed:
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}

func validDoubaoStoredStatus(status doubao.TaskStatus) bool {
	return status == doubao.StatusSubmitted || status == doubao.StatusProcessing ||
		status == doubao.StatusSucceeded || status == doubao.StatusFailed
}

func doubaoTaskProgress(status string) string {
	if status == model.TaskStatusSuccess || status == model.TaskStatusFailure || status == model.TaskStatusUnknown {
		return "100%"
	}
	if status == model.TaskStatusRunning {
		return "50%"
	}
	return "0%"
}

func doubaoPublicStatus(status string) string {
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

func doubaoProgressNumber(raw string) int {
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

func boundedDoubaoFailReason(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		message = "Doubao video task failed"
	}
	if len(message) <= doubaoTaskFailReasonMaxBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := doubaoTaskFailReasonMaxBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

func validDoubaoCapturedBaseURL(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	allowedScheme := parsed.Scheme == "https" || (common.SSRFDisabled() && parsed.Scheme == "http")
	return err == nil && allowedScheme && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func validDoubaoResultURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > doubao.MaxURLBytes || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validateDoubaoTaskPublicID(taskID string) error {
	if err := validateVideoTaskPublicID(taskID); err != nil {
		return fmt.Errorf("invalid Doubao task id")
	}
	return nil
}
