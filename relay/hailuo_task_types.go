package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/task/hailuo"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	hailuoTaskPlatform                = model.TaskOperationPlatformHailuo
	hailuoTaskPrivateDataPrefix       = "hailuo-task-v1:"
	hailuoTaskMetadataVersion         = 1
	hailuoTaskMetadataMaxBytes        = 64 << 10
	hailuoTaskStoredDataMaxBytes      = 1 << 20
	hailuoTaskEncryptedSecretMaxBytes = 16 << 10
	hailuoTaskFailReasonMaxBytes      = 4 << 10
	hailuoTaskChannelBaseURLMaxBytes  = 4 << 10
)

type hailuoTaskProperties struct {
	Version           int                                   `json:"version"`
	Family            string                                `json:"family"`
	Input             string                                `json:"input"`
	OriginModelName   string                                `json:"origin_model_name"`
	UpstreamModelName string                                `json:"upstream_model_name"`
	Action            hailuo.Action                         `json:"action"`
	HasInputReference bool                                  `json:"has_input_reference"`
	Duration          int                                   `json:"duration"`
	Resolution        string                                `json:"resolution"`
	Pricing           service.ReferenceAsyncTaskBillingPlan `json:"pricing"`
}

type hailuoTaskPrivateData struct {
	Version                 int                                   `json:"version"`
	RelayReservationID      string                                `json:"relay_reservation_id"`
	ChannelBaseURL          string                                `json:"channel_base_url"`
	EncryptedChannelKey     string                                `json:"encrypted_channel_key"`
	EncryptedProviderTaskID string                                `json:"encrypted_provider_task_id,omitempty"`
	SettlementPending       bool                                  `json:"settlement_pending"`
	Pricing                 service.ReferenceAsyncTaskBillingPlan `json:"pricing"`
	BillingSource           string                                `json:"billing_source"`
	SubscriptionID          int                                   `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64                                 `json:"funding_usage_epoch,omitempty"`
	TokenID                 int                                   `json:"token_id,omitempty"`
}

// hailuoStoredTaskData is the bounded public provider state. Raw provider bodies
// and identifiers are deliberately excluded.
type hailuoStoredTaskData struct {
	State        hailuo.TaskStatus `json:"state"`
	ErrorCode    string            `json:"error_code,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
	ResultURL    string            `json:"result_url,omitempty"`
	VideoWidth   int               `json:"video_width,omitempty"`
	VideoHeight  int               `json:"video_height,omitempty"`
}

type hailuoVideoError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type hailuoVideoResponse struct {
	ID          string            `json:"id"`
	TaskID      string            `json:"task_id,omitempty"`
	Object      string            `json:"object"`
	Model       string            `json:"model"`
	Status      string            `json:"status"`
	Progress    int               `json:"progress"`
	CreatedAt   int64             `json:"created_at"`
	CompletedAt int64             `json:"completed_at,omitempty"`
	Seconds     string            `json:"seconds,omitempty"`
	Size        string            `json:"size,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
	Error       *hailuoVideoError `json:"error,omitempty"`
}

type hailuoTaskDTO struct {
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

func marshalHailuoTaskProperties(properties hailuoTaskProperties) (string, error) {
	if properties.Version != hailuoTaskMetadataVersion || properties.Family != "hailuo" ||
		!hailuo.IsModel(properties.OriginModelName) ||
		!hailuo.IsModel(properties.UpstreamModelName) ||
		properties.UpstreamModelName == "" || len(properties.UpstreamModelName) > hailuo.MaxModelBytes ||
		!hailuo.ValidModelSettings(properties.UpstreamModelName, properties.Duration, properties.Resolution) ||
		!validHailuoStoredText(properties.Input, hailuo.MaxPromptBytes, false) ||
		!validHailuoAction(properties.Action) || properties.Pricing.ModelName != properties.OriginModelName ||
		properties.Pricing.Validate() != nil {
		return "", errors.New("invalid durable Hailuo task properties")
	}
	encoded, err := common.Marshal(properties)
	if err != nil || len(encoded) > hailuoTaskMetadataMaxBytes {
		return "", errors.New("Hailuo task properties are too large")
	}
	return string(encoded), nil
}

func decodeHailuoTaskProperties(raw string) (hailuoTaskProperties, error) {
	if raw == "" || len(raw) > hailuoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return hailuoTaskProperties{}, errors.New("invalid Hailuo task properties")
	}
	var properties hailuoTaskProperties
	if err := strictHailuoJSON([]byte(raw), &properties); err != nil {
		return hailuoTaskProperties{}, errors.New("invalid Hailuo task properties")
	}
	if _, err := marshalHailuoTaskProperties(properties); err != nil {
		return hailuoTaskProperties{}, err
	}
	return properties, nil
}

func marshalHailuoTaskPrivateData(privateData hailuoTaskPrivateData) (string, error) {
	if privateData.Version != hailuoTaskMetadataVersion || privateData.RelayReservationID == "" ||
		privateData.ChannelBaseURL == "" || len(privateData.ChannelBaseURL) > hailuoTaskChannelBaseURLMaxBytes ||
		privateData.EncryptedChannelKey == "" || len(privateData.EncryptedChannelKey) > hailuoTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedProviderTaskID) > hailuoTaskEncryptedSecretMaxBytes ||
		privateData.BillingSource == "" || privateData.Pricing.Validate() != nil {
		return "", errors.New("invalid durable Hailuo task private data")
	}
	if normalized, err := hailuo.EffectiveBaseURL(privateData.ChannelBaseURL); err != nil || normalized != privateData.ChannelBaseURL {
		return "", errors.New("invalid durable Hailuo task base URL")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil || len(encoded)+len(hailuoTaskPrivateDataPrefix) > hailuoTaskMetadataMaxBytes {
		return "", errors.New("Hailuo task private data is too large")
	}
	return hailuoTaskPrivateDataPrefix + string(encoded), nil
}

func decodeHailuoTaskPrivateData(raw string) (hailuoTaskPrivateData, error) {
	if !strings.HasPrefix(raw, hailuoTaskPrivateDataPrefix) || len(raw) > hailuoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return hailuoTaskPrivateData{}, errors.New("invalid Hailuo task private data")
	}
	var privateData hailuoTaskPrivateData
	if err := strictHailuoJSON([]byte(strings.TrimPrefix(raw, hailuoTaskPrivateDataPrefix)), &privateData); err != nil {
		return hailuoTaskPrivateData{}, errors.New("invalid Hailuo task private data")
	}
	if _, err := marshalHailuoTaskPrivateData(privateData); err != nil {
		return hailuoTaskPrivateData{}, err
	}
	return privateData, nil
}

func validateHailuoTaskPricingSnapshot(task *model.Task, privateData hailuoTaskPrivateData,
	expectedQuota int) (hailuoTaskProperties, error) {
	if task == nil || expectedQuota < 0 {
		return hailuoTaskProperties{}, errors.New("invalid Hailuo pricing coordinates")
	}
	properties, err := decodeHailuoTaskProperties(task.Properties)
	if err != nil || properties.Pricing != privateData.Pricing ||
		string(properties.Action) != task.Action {
		return hailuoTaskProperties{}, errors.New("Hailuo pricing snapshots do not match")
	}
	quota, err := privateData.Pricing.PreConsumeQuota()
	if err != nil || quota != expectedQuota {
		return hailuoTaskProperties{}, errors.New("Hailuo pricing snapshot does not match quota")
	}
	return properties, nil
}

func marshalHailuoStoredTaskData(data hailuoStoredTaskData) (string, error) {
	if !validHailuoStatus(data.State) ||
		!validHailuoStoredText(data.ErrorCode, hailuoTaskFailReasonMaxBytes, true) ||
		!validHailuoStoredText(data.ErrorMessage, hailuoTaskFailReasonMaxBytes, true) ||
		!validHailuoStoredURL(data.ResultURL) || data.VideoWidth < 0 || data.VideoHeight < 0 {
		return "", errors.New("invalid normalized Hailuo provider state")
	}
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > hailuoTaskStoredDataMaxBytes {
		return "", errors.New("normalized Hailuo provider state is too large")
	}
	return string(encoded), nil
}

func validHailuoStoredURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && parsed.User == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func decodeHailuoStoredTaskData(raw string) (hailuoStoredTaskData, error) {
	if raw == "" || raw == "null" {
		return hailuoStoredTaskData{}, nil
	}
	if len(raw) > hailuoTaskStoredDataMaxBytes || !utf8.ValidString(raw) {
		return hailuoStoredTaskData{}, errors.New("invalid stored Hailuo provider state")
	}
	var data hailuoStoredTaskData
	if err := strictHailuoJSON([]byte(raw), &data); err != nil {
		return hailuoStoredTaskData{}, err
	}
	if _, err := marshalHailuoStoredTaskData(data); err != nil {
		return hailuoStoredTaskData{}, err
	}
	return data, nil
}

func normalizedHailuoTaskData(provider *hailuo.Task) (hailuoStoredTaskData, error) {
	if provider == nil || !validHailuoStatus(provider.Status) {
		return hailuoStoredTaskData{}, errors.New("invalid Hailuo provider task")
	}
	data := hailuoStoredTaskData{
		State:     provider.Status,
		ErrorCode: normalizedHailuoErrorCode(provider.ErrorCode),
		ResultURL: provider.ResultURL, VideoWidth: provider.VideoWidth, VideoHeight: provider.VideoHeight,
	}
	if provider.Status == hailuo.StatusFailed {
		data.ErrorMessage = "Hailuo provider reported task failure"
	}
	if _, err := marshalHailuoStoredTaskData(data); err != nil {
		return hailuoStoredTaskData{}, err
	}
	return data, nil
}

func hailuoTaskDTOFromModel(task *model.Task) (hailuoTaskDTO, error) {
	if !isHailuoTask(task) || validateHailuoTaskPublicID(task.TaskID) != nil {
		return hailuoTaskDTO{}, errors.New("invalid Hailuo task")
	}
	properties, err := decodeHailuoTaskProperties(task.Properties)
	if err != nil {
		return hailuoTaskDTO{}, err
	}
	data, err := decodeHailuoStoredTaskData(task.Data)
	if err != nil {
		return hailuoTaskDTO{}, err
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return hailuoTaskDTO{}, err
	}
	publicProperties := struct {
		Input             string `json:"input"`
		UpstreamModelName string `json:"upstream_model_name"`
		OriginModelName   string `json:"origin_model_name"`
	}{properties.Input, properties.UpstreamModelName, properties.OriginModelName}
	return hailuoTaskDTO{ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId, Group: task.Group,
		ChannelID: task.ChannelId, Quota: task.Quota, Action: task.Action, Status: task.Status,
		FailReason: task.FailReason, ResultURL: hailuoResultURL(data), SubmitTime: task.SubmitTime,
		StartTime: task.StartTime, FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: publicProperties, Data: encoded}, nil
}

func hailuoTaskResponse(task *model.Task) (hailuoVideoResponse, error) {
	if !isHailuoTask(task) {
		return hailuoVideoResponse{}, errors.New("invalid Hailuo task")
	}
	properties, err := decodeHailuoTaskProperties(task.Properties)
	if err != nil {
		return hailuoVideoResponse{}, err
	}
	data, err := decodeHailuoStoredTaskData(task.Data)
	if err != nil {
		return hailuoVideoResponse{}, err
	}
	response := hailuoVideoResponse{ID: task.TaskID, TaskID: task.TaskID, Object: "video",
		Model: properties.OriginModelName, Status: hailuoPublicStatus(task.Status),
		Progress: hailuoProgressNumber(task.Progress), CreatedAt: task.CreatedAt,
		Seconds: strconv.Itoa(properties.Duration), Size: properties.Resolution}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		response.CompletedAt = task.FinishTime
	}
	if resultURL := hailuoResultURL(data); resultURL != "" {
		response.Metadata = map[string]any{"url": resultURL}
	}
	if task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		code := data.ErrorCode
		if code == "" && task.Status == model.TaskStatusUnknown {
			code = "submit_outcome_unknown"
		}
		response.Error = &hailuoVideoError{Message: boundedHailuoFailReason(task.FailReason), Code: code}
	}
	return response, nil
}

func validHailuoAction(action hailuo.Action) bool {
	_, err := hailuo.ParseAction(string(action))
	return err == nil
}
func validHailuoStatus(status hailuo.TaskStatus) bool {
	return status == hailuo.StatusSubmitted || status == hailuo.StatusProcessing || status == hailuo.StatusSucceeded || status == hailuo.StatusFailed
}
func isHailuoTask(task *model.Task) bool { return task != nil && task.Platform == hailuoTaskPlatform }

func validateHailuoTaskPublicID(taskID string) error {
	if len(taskID) != len("task_")+32 || !strings.HasPrefix(taskID, "task_") {
		return errors.New("invalid Hailuo task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return errors.New("invalid Hailuo task id")
		}
	}
	return nil
}

func boundedHailuoFailReason(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, character := range raw {
		if builder.Len() >= hailuoTaskFailReasonMaxBytes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > hailuoTaskFailReasonMaxBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func hailuoModelStatus(status hailuo.TaskStatus) string {
	switch status {
	case hailuo.StatusSubmitted:
		return model.TaskStatusSubmitted
	case hailuo.StatusProcessing:
		return model.TaskStatusRunning
	case hailuo.StatusSucceeded:
		return model.TaskStatusSuccess
	case hailuo.StatusFailed:
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}

func hailuoPublicStatus(status string) string {
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

func hailuoTaskProgress(status string) string {
	switch status {
	case model.TaskStatusNotStart:
		return "0%"
	case model.TaskStatusSubmitted, model.TaskStatusQueued:
		return "10%"
	case model.TaskStatusRunning:
		return "30%"
	default:
		return "100%"
	}
}

func hailuoProgressNumber(progress string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(progress, "%"))
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func hailuoResultURL(data hailuoStoredTaskData) string {
	return data.ResultURL
}

func validHailuoStoredText(value string, maximum int, emptyOK bool) bool {
	if (!emptyOK && strings.TrimSpace(value) == "") || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || (unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t') {
			return false
		}
	}
	return true
}

func normalizedHailuoErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 64 {
		return "provider_error"
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return "provider_error"
	}
	return value
}

func hailuoChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	return fmt.Sprintf("hailuo:channel:v1:%s:%d:%d:%s", taskID, userID, channelID, baseURL)
}

func hailuoProviderTaskBinding(taskID, reservationID string, userID, channelID int) string {
	return fmt.Sprintf("hailuo:provider-task:v1:%s:%s:%d:%d", taskID, reservationID, userID, channelID)
}

func hailuoRecoveryJournalBinding(taskID string, action hailuo.Action) string {
	return fmt.Sprintf("hailuo:recovery-journal:v1:%s:%s", taskID, action)
}

func strictHailuoJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
