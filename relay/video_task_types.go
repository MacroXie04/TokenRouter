package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/sora"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	videoTaskPlatform                 = "55"
	videoTaskOpenAIPlatform           = "1"
	videoTaskPrivateDataPrefix        = "openai-video-v1:"
	videoTaskMetadataMaxBytes         = 64 << 10
	videoTaskFailReasonMaxBytes       = 4 << 10
	videoTaskProviderPayloadMaxBytes  = 1 << 20
	videoTaskChannelBaseURLMaxBytes   = 4 << 10
	videoTaskEncryptedSecretMaxBytes  = 16 << 10
	videoTaskResultContentPathMaxSize = 512
)

type videoTaskProperties struct {
	Input              string `json:"input"`
	UpstreamModelName  string `json:"upstream_model_name"`
	OriginModelName    string `json:"origin_model_name"`
	Seconds            int    `json:"seconds"`
	Size               string `json:"size"`
	RemixedFromVideoID string `json:"remixed_from_video_id,omitempty"`
	HasInputReference  bool   `json:"has_input_reference,omitempty"`
}

type videoTaskPrivateData struct {
	RelayReservationID      string `json:"relay_reservation_id"`
	EncryptedUpstreamTaskID string `json:"encrypted_upstream_task_id,omitempty"`
	ChannelBaseURL          string `json:"channel_base_url"`
	EncryptedChannelKey     string `json:"encrypted_channel_key"`
	SettlementPending       bool   `json:"settlement_pending,omitempty"`
	FreeModel               bool   `json:"free_model,omitempty"`
	BillingSource           string `json:"billing_source,omitempty"`
	SubscriptionID          int    `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64  `json:"funding_usage_epoch,omitempty"`
	TokenID                 int    `json:"token_id,omitempty"`
}

type openAIVideoResponse struct {
	ID                 string              `json:"id"`
	TaskID             string              `json:"task_id,omitempty"`
	Object             string              `json:"object"`
	Model              string              `json:"model"`
	Status             string              `json:"status"`
	Progress           int                 `json:"progress"`
	CreatedAt          int64               `json:"created_at"`
	CompletedAt        int64               `json:"completed_at,omitempty"`
	ExpiresAt          int64               `json:"expires_at,omitempty"`
	Seconds            string              `json:"seconds,omitempty"`
	Size               string              `json:"size,omitempty"`
	RemixedFromVideoID string              `json:"remixed_from_video_id,omitempty"`
	Error              *sora.ResponseError `json:"error,omitempty"`
	Metadata           map[string]any      `json:"metadata,omitempty"`
}

type videoTaskDTO struct {
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

func marshalVideoTaskProperties(properties videoTaskProperties) (string, error) {
	if strings.TrimSpace(properties.OriginModelName) == "" ||
		strings.TrimSpace(properties.UpstreamModelName) == "" ||
		properties.Seconds < 1 || properties.Seconds > sora.MaxSeconds ||
		len(properties.Input) > 32<<10 || !utf8.ValidString(properties.Input) {
		return "", errors.New("invalid durable video task properties")
	}
	encoded, err := common.Marshal(properties)
	if err != nil {
		return "", err
	}
	if len(encoded) > videoTaskMetadataMaxBytes {
		return "", errors.New("video task properties are too large")
	}
	return string(encoded), nil
}

func decodeVideoTaskProperties(raw string) (videoTaskProperties, error) {
	if raw == "" || len(raw) > videoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return videoTaskProperties{}, errors.New("invalid video task properties")
	}
	var properties videoTaskProperties
	if err := common.UnmarshalJsonStr(raw, &properties); err != nil {
		return videoTaskProperties{}, errors.New("invalid video task properties")
	}
	if strings.TrimSpace(properties.OriginModelName) == "" ||
		strings.TrimSpace(properties.UpstreamModelName) == "" ||
		properties.Seconds < 1 || properties.Seconds > sora.MaxSeconds {
		return videoTaskProperties{}, errors.New("invalid video task properties")
	}
	return properties, nil
}

func marshalVideoTaskPrivateData(privateData videoTaskPrivateData) (string, error) {
	if privateData.RelayReservationID == "" || privateData.ChannelBaseURL == "" ||
		len(privateData.ChannelBaseURL) > videoTaskChannelBaseURLMaxBytes ||
		privateData.EncryptedChannelKey == "" ||
		len(privateData.EncryptedChannelKey) > videoTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedUpstreamTaskID) > videoTaskEncryptedSecretMaxBytes ||
		(privateData.BillingSource != "" &&
			privateData.FreeModel != (privateData.BillingSource == service.BillingSourceFreeModel)) {
		return "", errors.New("invalid durable video task private data")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil {
		return "", err
	}
	if len(encoded)+len(videoTaskPrivateDataPrefix) > videoTaskMetadataMaxBytes {
		return "", errors.New("video task private data is too large")
	}
	return videoTaskPrivateDataPrefix + string(encoded), nil
}

func decodeVideoTaskPrivateData(raw string) (videoTaskPrivateData, error) {
	if !strings.HasPrefix(raw, videoTaskPrivateDataPrefix) || len(raw) > videoTaskMetadataMaxBytes {
		return videoTaskPrivateData{}, errors.New("invalid video task private data")
	}
	var privateData videoTaskPrivateData
	if err := common.UnmarshalJsonStr(strings.TrimPrefix(raw, videoTaskPrivateDataPrefix), &privateData); err != nil {
		return videoTaskPrivateData{}, errors.New("invalid video task private data")
	}
	if privateData.RelayReservationID == "" || privateData.ChannelBaseURL == "" ||
		privateData.EncryptedChannelKey == "" ||
		privateData.FreeModel != (privateData.BillingSource == service.BillingSourceFreeModel) {
		return videoTaskPrivateData{}, errors.New("invalid video task private data")
	}
	return privateData, nil
}

func sanitizedVideoResponse(
	taskID string,
	properties videoTaskProperties,
	provider *sora.Response,
	taskStatus string,
	failReason string,
	createdAt, updatedAt int64,
) openAIVideoResponse {
	response := openAIVideoResponse{
		ID: taskID, TaskID: taskID, Object: "video", Model: properties.OriginModelName,
		Status: videoTaskStatus(taskStatus), CreatedAt: createdAt,
		Seconds: strconv.Itoa(properties.Seconds), Size: properties.Size,
		RemixedFromVideoID: properties.RemixedFromVideoID,
	}
	if provider != nil {
		response.Object = provider.Object
		if response.Object == "" {
			response.Object = "video"
		}
		response.Progress = clampVideoProgress(provider.Progress)
		response.CreatedAt = provider.CreatedAt
		if response.CreatedAt <= 0 {
			response.CreatedAt = createdAt
		}
		response.CompletedAt = provider.CompletedAt
		response.ExpiresAt = provider.ExpiresAt
		response.Error = provider.Error
		if provider.Metadata != nil {
			response.Metadata = cloneVideoMetadata(provider.Metadata)
		}
	}
	if taskStatus == model.TaskStatusSuccess || taskStatus == model.TaskStatusFailure || taskStatus == model.TaskStatusUnknown {
		if response.CompletedAt <= 0 {
			response.CompletedAt = updatedAt
		}
	}
	if taskStatus == model.TaskStatusSuccess {
		response.Progress = 100
	}
	if taskStatus == model.TaskStatusFailure && response.Error == nil {
		response.Error = &sora.ResponseError{Message: boundedVideoFailReason(failReason), Code: "video_generation_failed"}
	}
	return response
}

func videoTaskResponse(task *model.Task) (openAIVideoResponse, error) {
	if task == nil || task.TaskID == "" {
		return openAIVideoResponse{}, errors.New("video task is nil")
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return openAIVideoResponse{}, err
	}
	var provider sora.Response
	if raw := strings.TrimSpace(task.Data); raw != "" && raw != "null" {
		if len(raw) > videoTaskProviderPayloadMaxBytes || common.UnmarshalJsonStr(raw, &provider) != nil {
			return openAIVideoResponse{}, errors.New("invalid stored video provider response")
		}
	}
	return sanitizedVideoResponse(
		task.TaskID, properties, &provider, task.Status, task.FailReason, task.CreatedAt, task.UpdatedAt,
	), nil
}

func videoTaskDTOFromModel(task *model.Task) (videoTaskDTO, error) {
	response, err := videoTaskResponse(task)
	if err != nil {
		return videoTaskDTO{}, err
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return videoTaskDTO{}, err
	}
	data, err := common.Marshal(response)
	if err != nil {
		return videoTaskDTO{}, err
	}
	resultURL := ""
	if task.Status == model.TaskStatusSuccess {
		resultURL = videoContentPath(task.TaskID)
	}
	return videoTaskDTO{
		ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId,
		Group: task.Group, ChannelID: task.ChannelId, Quota: task.Quota,
		Action: task.Action, Status: task.Status, FailReason: task.FailReason,
		ResultURL: resultURL, SubmitTime: task.SubmitTime, StartTime: task.StartTime,
		FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: properties, Data: json.RawMessage(data),
	}, nil
}

func videoContentPath(taskID string) string {
	path := "/v1/videos/" + taskID + "/content"
	if len(path) > videoTaskResultContentPathMaxSize {
		return ""
	}
	return path
}

func videoTaskStatus(status string) string {
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

func providerVideoTaskStatus(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return model.TaskStatusQueued, true
	case "processing", "in_progress":
		return model.TaskStatusRunning, true
	case "completed", "succeeded", "success":
		return model.TaskStatusSuccess, true
	case "failed", "cancelled", "canceled":
		return model.TaskStatusFailure, true
	default:
		return model.TaskStatusSubmitted, false
	}
}

func videoTaskProgress(status string, providerProgress int) string {
	if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
		return "100%"
	}
	return strconv.Itoa(clampVideoProgress(providerProgress)) + "%"
}

func clampVideoProgress(progress int) int {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return progress
}

func boundedVideoFailReason(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		message = "video task failed"
	}
	if len(message) <= videoTaskFailReasonMaxBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := videoTaskFailReasonMaxBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

func cloneVideoMetadata(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	encoded, err := common.Marshal(source)
	if err != nil || len(encoded) > videoTaskMetadataMaxBytes {
		return nil
	}
	var result map[string]any
	if common.Unmarshal(encoded, &result) != nil {
		return nil
	}
	return result
}

func videoTaskAction(remix, hasInput bool) string {
	if remix {
		return "remixGenerate"
	}
	if hasInput {
		return "generate"
	}
	return "textGenerate"
}

func isVideoTaskPlatform(task *model.Task) bool {
	return task != nil && validVideoTaskPlatform(task.Platform) && task.ChannelId > 0 &&
		strings.HasPrefix(task.PrivateData, videoTaskPrivateDataPrefix)
}

func validVideoTaskPlatform(platform string) bool {
	return platform == videoTaskOpenAIPlatform || platform == videoTaskPlatform
}

func videoTaskPlatforms() []string {
	return []string{videoTaskOpenAIPlatform, videoTaskPlatform}
}

func videoTaskOperationIdentityMatches(
	task *model.Task,
	operation *model.TaskOperation,
	reservationID string,
) bool {
	return isVideoTaskPlatform(task) && operation != nil && operation.TaskID == task.TaskID &&
		operation.ReservationID == reservationID && operation.Platform == task.Platform &&
		operation.UserID == task.UserId && operation.ChannelID == task.ChannelId
}

func videoChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	material := taskID + "\x00" + strconv.Itoa(userID) + "\x00" + strconv.Itoa(channelID) + "\x00" + baseURL
	return "video-channel-v1:" + common.SHA256Hex(material)
}

func videoProviderTaskBinding(taskID, reservationID string, userID, channelID int) string {
	material := taskID + "\x00" + reservationID + "\x00" + strconv.Itoa(userID) + "\x00" + strconv.Itoa(channelID)
	return "video-provider-v1:" + common.SHA256Hex(material)
}

func videoRecoveryJournalBinding(taskID string) string {
	return "video-journal-v1:" + common.SHA256Hex(taskID)
}

func validateVideoTaskPublicID(taskID string) error {
	if !strings.HasPrefix(taskID, "task_") || len(taskID) != len("task_")+32 {
		return errors.New("invalid video task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return fmt.Errorf("invalid video task id")
		}
	}
	return nil
}
