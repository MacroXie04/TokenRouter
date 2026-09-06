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
	"github.com/tokenrouter/tokenrouter/relay/channel/vidu"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	viduTaskPlatform                = model.TaskOperationPlatformVidu
	viduTaskPrivateDataPrefix       = "vidu-task-v1:"
	viduTaskMetadataVersion         = 1
	viduTaskMetadataMaxBytes        = 64 << 10
	viduTaskStoredDataMaxBytes      = 1 << 20
	viduTaskEncryptedSecretMaxBytes = 16 << 10
	viduTaskFailReasonMaxBytes      = 4 << 10
	viduTaskChannelBaseURLMaxBytes  = 4 << 10
	viduTaskProviderPayloadMaxBytes = 1 << 20
)

type viduTaskProperties struct {
	Version           int                                   `json:"version"`
	Family            string                                `json:"family"`
	Input             string                                `json:"input"`
	OriginModelName   string                                `json:"origin_model_name"`
	UpstreamModelName string                                `json:"upstream_model_name"`
	Action            vidu.Action                           `json:"action"`
	ImageCount        int                                   `json:"image_count"`
	Duration          int                                   `json:"duration"`
	Resolution        string                                `json:"resolution"`
	Pricing           service.ReferenceAsyncTaskBillingPlan `json:"pricing"`
}

type viduTaskPrivateData struct {
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

// viduStoredTaskData is the bounded public provider state. Raw provider bodies
// and identifiers are deliberately excluded.
type viduStoredTaskData struct {
	State     vidu.TaskStatus `json:"state"`
	ErrCode   string          `json:"err_code,omitempty"`
	Credits   int             `json:"credits,omitempty"`
	Payload   string          `json:"payload,omitempty"`
	Creations []vidu.Creation `json:"creations,omitempty"`
}

type viduVideoError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type viduVideoResponse struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id,omitempty"`
	Object      string          `json:"object"`
	Model       string          `json:"model"`
	Status      string          `json:"status"`
	Progress    int             `json:"progress"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt int64           `json:"completed_at,omitempty"`
	Seconds     string          `json:"seconds,omitempty"`
	Size        string          `json:"size,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
	Error       *viduVideoError `json:"error,omitempty"`
}

type viduTaskDTO struct {
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

func marshalViduTaskProperties(properties viduTaskProperties) (string, error) {
	if properties.Version != viduTaskMetadataVersion || properties.Family != "vidu" ||
		!vidu.IsModel(properties.OriginModelName) ||
		properties.UpstreamModelName == "" || len(properties.UpstreamModelName) > vidu.MaxModelBytes ||
		properties.ImageCount < 0 || properties.ImageCount > vidu.MaxImages ||
		properties.Duration < 1 || properties.Duration > vidu.MaxDuration ||
		properties.Resolution == "" || len(properties.Resolution) > vidu.MaxResolutionBytes ||
		strings.TrimSpace(properties.Input) == "" || utf8.RuneCountInString(properties.Input) > vidu.MaxPromptRunes ||
		!validViduAction(properties.Action) || properties.Pricing.ModelName != properties.OriginModelName ||
		properties.Pricing.Validate() != nil {
		return "", errors.New("invalid durable Vidu task properties")
	}
	encoded, err := common.Marshal(properties)
	if err != nil || len(encoded) > viduTaskMetadataMaxBytes {
		return "", errors.New("Vidu task properties are too large")
	}
	return string(encoded), nil
}

func decodeViduTaskProperties(raw string) (viduTaskProperties, error) {
	if raw == "" || len(raw) > viduTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return viduTaskProperties{}, errors.New("invalid Vidu task properties")
	}
	var properties viduTaskProperties
	if err := strictViduJSON([]byte(raw), &properties); err != nil {
		return viduTaskProperties{}, errors.New("invalid Vidu task properties")
	}
	if _, err := marshalViduTaskProperties(properties); err != nil {
		return viduTaskProperties{}, err
	}
	return properties, nil
}

func marshalViduTaskPrivateData(privateData viduTaskPrivateData) (string, error) {
	if privateData.Version != viduTaskMetadataVersion || privateData.RelayReservationID == "" ||
		privateData.ChannelBaseURL == "" || len(privateData.ChannelBaseURL) > viduTaskChannelBaseURLMaxBytes ||
		privateData.EncryptedChannelKey == "" || len(privateData.EncryptedChannelKey) > viduTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedProviderTaskID) > viduTaskEncryptedSecretMaxBytes ||
		privateData.BillingSource == "" || privateData.Pricing.Validate() != nil {
		return "", errors.New("invalid durable Vidu task private data")
	}
	if normalized, err := vidu.EffectiveBaseURL(privateData.ChannelBaseURL); err != nil || normalized != privateData.ChannelBaseURL {
		return "", errors.New("invalid durable Vidu task base URL")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil || len(encoded)+len(viduTaskPrivateDataPrefix) > viduTaskMetadataMaxBytes {
		return "", errors.New("Vidu task private data is too large")
	}
	return viduTaskPrivateDataPrefix + string(encoded), nil
}

func decodeViduTaskPrivateData(raw string) (viduTaskPrivateData, error) {
	if !strings.HasPrefix(raw, viduTaskPrivateDataPrefix) || len(raw) > viduTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return viduTaskPrivateData{}, errors.New("invalid Vidu task private data")
	}
	var privateData viduTaskPrivateData
	if err := strictViduJSON([]byte(strings.TrimPrefix(raw, viduTaskPrivateDataPrefix)), &privateData); err != nil {
		return viduTaskPrivateData{}, errors.New("invalid Vidu task private data")
	}
	if _, err := marshalViduTaskPrivateData(privateData); err != nil {
		return viduTaskPrivateData{}, err
	}
	return privateData, nil
}

func validateViduTaskPricingSnapshot(task *model.Task, privateData viduTaskPrivateData,
	expectedQuota int) (viduTaskProperties, error) {
	if task == nil || expectedQuota < 0 {
		return viduTaskProperties{}, errors.New("invalid Vidu pricing coordinates")
	}
	properties, err := decodeViduTaskProperties(task.Properties)
	if err != nil || properties.Pricing != privateData.Pricing ||
		string(properties.Action) != task.Action {
		return viduTaskProperties{}, errors.New("Vidu pricing snapshots do not match")
	}
	quota, err := privateData.Pricing.PreConsumeQuota()
	if err != nil || quota != expectedQuota {
		return viduTaskProperties{}, errors.New("Vidu pricing snapshot does not match quota")
	}
	return properties, nil
}

func marshalViduStoredTaskData(data viduStoredTaskData) (string, error) {
	if !validViduStatus(data.State) || data.Credits < 0 || len(data.Payload) > vidu.MaxPayloadBytes ||
		len(data.ErrCode) > viduTaskFailReasonMaxBytes || len(data.Creations) > vidu.MaxImages {
		return "", errors.New("invalid normalized Vidu provider state")
	}
	for _, creation := range data.Creations {
		if len(creation.ID) > 256 || !utf8.ValidString(creation.ID) ||
			!validViduStoredURL(creation.URL) || !validViduStoredURL(creation.CoverURL) {
			return "", errors.New("invalid normalized Vidu creation")
		}
	}
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > viduTaskStoredDataMaxBytes {
		return "", errors.New("normalized Vidu provider state is too large")
	}
	return string(encoded), nil
}

func validViduStoredURL(value string) bool {
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

func decodeViduStoredTaskData(raw string) (viduStoredTaskData, error) {
	if raw == "" || raw == "null" {
		return viduStoredTaskData{}, nil
	}
	if len(raw) > viduTaskStoredDataMaxBytes || !utf8.ValidString(raw) {
		return viduStoredTaskData{}, errors.New("invalid stored Vidu provider state")
	}
	var data viduStoredTaskData
	if err := strictViduJSON([]byte(raw), &data); err != nil {
		return viduStoredTaskData{}, err
	}
	if _, err := marshalViduStoredTaskData(data); err != nil {
		return viduStoredTaskData{}, err
	}
	return data, nil
}

func normalizedViduTaskData(provider *vidu.Task) (viduStoredTaskData, error) {
	if provider == nil || !validViduStatus(provider.Status) {
		return viduStoredTaskData{}, errors.New("invalid Vidu provider task")
	}
	data := viduStoredTaskData{State: provider.Status, ErrCode: boundedViduFailReason(provider.ErrorCode),
		Credits: provider.Credits, Payload: provider.Payload,
		Creations: append([]vidu.Creation(nil), provider.Creations...)}
	if _, err := marshalViduStoredTaskData(data); err != nil {
		return viduStoredTaskData{}, err
	}
	return data, nil
}

func viduTaskDTOFromModel(task *model.Task) (viduTaskDTO, error) {
	if !isViduTask(task) || validateViduTaskPublicID(task.TaskID) != nil {
		return viduTaskDTO{}, errors.New("invalid Vidu task")
	}
	properties, err := decodeViduTaskProperties(task.Properties)
	if err != nil {
		return viduTaskDTO{}, err
	}
	data, err := decodeViduStoredTaskData(task.Data)
	if err != nil {
		return viduTaskDTO{}, err
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return viduTaskDTO{}, err
	}
	publicProperties := struct {
		Input             string `json:"input"`
		UpstreamModelName string `json:"upstream_model_name"`
		OriginModelName   string `json:"origin_model_name"`
	}{properties.Input, properties.UpstreamModelName, properties.OriginModelName}
	return viduTaskDTO{ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId, Group: task.Group,
		ChannelID: task.ChannelId, Quota: task.Quota, Action: task.Action, Status: task.Status,
		FailReason: task.FailReason, ResultURL: viduResultURL(data), SubmitTime: task.SubmitTime,
		StartTime: task.StartTime, FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: publicProperties, Data: encoded}, nil
}

func viduTaskResponse(task *model.Task) (viduVideoResponse, error) {
	if !isViduTask(task) {
		return viduVideoResponse{}, errors.New("invalid Vidu task")
	}
	properties, err := decodeViduTaskProperties(task.Properties)
	if err != nil {
		return viduVideoResponse{}, err
	}
	data, err := decodeViduStoredTaskData(task.Data)
	if err != nil {
		return viduVideoResponse{}, err
	}
	response := viduVideoResponse{ID: task.TaskID, TaskID: task.TaskID, Object: "video",
		Model: properties.OriginModelName, Status: viduPublicStatus(task.Status),
		Progress: viduProgressNumber(task.Progress), CreatedAt: task.CreatedAt,
		Seconds: strconv.Itoa(properties.Duration), Size: properties.Resolution}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		response.CompletedAt = task.FinishTime
	}
	if resultURL := viduResultURL(data); resultURL != "" {
		response.Metadata = map[string]any{"url": resultURL}
	}
	if task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		code := data.ErrCode
		if code == "" && task.Status == model.TaskStatusUnknown {
			code = "submit_outcome_unknown"
		}
		response.Error = &viduVideoError{Message: boundedViduFailReason(task.FailReason), Code: code}
	}
	return response, nil
}

func validViduAction(action vidu.Action) bool { _, err := action.Path(); return err == nil }
func validViduStatus(status vidu.TaskStatus) bool {
	return status == vidu.StatusSubmitted || status == vidu.StatusProcessing || status == vidu.StatusSucceeded || status == vidu.StatusFailed
}
func isViduTask(task *model.Task) bool { return task != nil && task.Platform == viduTaskPlatform }

func validateViduTaskPublicID(taskID string) error {
	if len(taskID) != len("task_")+32 || !strings.HasPrefix(taskID, "task_") {
		return errors.New("invalid Vidu task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return errors.New("invalid Vidu task id")
		}
	}
	return nil
}

func boundedViduFailReason(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, character := range raw {
		if builder.Len() >= viduTaskFailReasonMaxBytes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > viduTaskFailReasonMaxBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func viduModelStatus(status vidu.TaskStatus) string {
	switch status {
	case vidu.StatusSubmitted:
		return model.TaskStatusSubmitted
	case vidu.StatusProcessing:
		return model.TaskStatusRunning
	case vidu.StatusSucceeded:
		return model.TaskStatusSuccess
	case vidu.StatusFailed:
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}

func viduPublicStatus(status string) string {
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

func viduTaskProgress(status string) string {
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

func viduProgressNumber(progress string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(progress, "%"))
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func viduResultURL(data viduStoredTaskData) string {
	if len(data.Creations) > 0 {
		return data.Creations[0].URL
	}
	return ""
}

func viduChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	return fmt.Sprintf("vidu:channel:v1:%s:%d:%d:%s", taskID, userID, channelID, baseURL)
}

func viduProviderTaskBinding(taskID, reservationID string, userID, channelID int) string {
	return fmt.Sprintf("vidu:provider-task:v1:%s:%s:%d:%d", taskID, reservationID, userID, channelID)
}

func viduRecoveryJournalBinding(taskID string, action vidu.Action) string {
	return fmt.Sprintf("vidu:recovery-journal:v1:%s:%s", taskID, action)
}

func strictViduJSON(raw []byte, destination any) error {
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
