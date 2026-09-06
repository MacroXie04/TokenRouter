package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/suno"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	sunoTaskPlatform                      = model.TaskOperationPlatformSuno
	sunoTaskPrivateDataPrefix             = "suno-task-v1:"
	sunoTaskMetadataMaxBytes              = 64 << 10
	sunoTaskPrivateDataMaxBytes           = 64 << 10
	sunoTaskEncryptedSecretMaxBytes       = 16 << 10
	sunoTaskFailReasonMaxBytes            = 4 << 10
	sunoFetchRequestMaxBytes        int64 = 64 << 10
	sunoFetchResponseMaxBytes             = 16 << 20
	sunoFetchResponseDataBudget           = 15 << 20
)

type sunoTaskProperties struct {
	Version         int                                      `json:"version"`
	OriginModelName string                                   `json:"origin_model_name"`
	Action          suno.Action                              `json:"action"`
	Pricing         billingsvc.ReferenceAsyncTaskBillingPlan `json:"pricing"`
}

type sunoTaskPrivateData struct {
	RelayReservationID      string `json:"relay_reservation_id"`
	ChannelBaseURL          string `json:"channel_base_url"`
	EncryptedChannelKey     string `json:"encrypted_channel_key"`
	EncryptedProviderTaskID string `json:"encrypted_provider_task_id,omitempty"`
	SettlementPending       bool   `json:"settlement_pending,omitempty"`
	BillingSource           string `json:"billing_source,omitempty"`
	SubscriptionID          int    `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64  `json:"funding_usage_epoch,omitempty"`
	TokenID                 int    `json:"token_id,omitempty"`
}

type sunoTaskDTO struct {
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

func marshalSunoTaskProperties(properties sunoTaskProperties) (string, error) {
	if properties.Version != 1 || properties.OriginModelName == "" || len(properties.OriginModelName) > 256 {
		return "", errors.New("invalid durable Suno task properties")
	}
	if expected, ok := suno.ModelForAction(properties.Action); !ok || expected != properties.OriginModelName {
		return "", errors.New("invalid durable Suno model/action pair")
	}
	if properties.Pricing.ModelName != properties.OriginModelName || properties.Pricing.Validate() != nil {
		return "", errors.New("invalid durable Suno pricing snapshot")
	}
	encoded, err := jsonutil.Marshal(properties)
	if err != nil || len(encoded) > sunoTaskMetadataMaxBytes {
		return "", errors.New("Suno task properties are too large")
	}
	return string(encoded), nil
}

func decodeSunoTaskProperties(raw string) (sunoTaskProperties, error) {
	if raw == "" || len(raw) > sunoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return sunoTaskProperties{}, errors.New("invalid Suno task properties")
	}
	var properties sunoTaskProperties
	if err := strictSunoJSON([]byte(raw), &properties); err != nil {
		return sunoTaskProperties{}, errors.New("invalid Suno task properties")
	}
	if _, err := marshalSunoTaskProperties(properties); err != nil {
		return sunoTaskProperties{}, err
	}
	return properties, nil
}

func marshalSunoTaskPrivateData(privateData sunoTaskPrivateData) (string, error) {
	if privateData.RelayReservationID == "" || privateData.ChannelBaseURL == "" ||
		privateData.EncryptedChannelKey == "" ||
		len(privateData.EncryptedChannelKey) > sunoTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedProviderTaskID) > sunoTaskEncryptedSecretMaxBytes {
		return "", errors.New("invalid durable Suno private data")
	}
	if _, err := suno.ValidateBaseURL(privateData.ChannelBaseURL); err != nil {
		return "", errors.New("invalid durable Suno base URL")
	}
	encoded, err := jsonutil.Marshal(privateData)
	if err != nil || len(encoded)+len(sunoTaskPrivateDataPrefix) > sunoTaskPrivateDataMaxBytes {
		return "", errors.New("Suno task private data is too large")
	}
	return sunoTaskPrivateDataPrefix + string(encoded), nil
}

func decodeSunoTaskPrivateData(raw string) (sunoTaskPrivateData, error) {
	if !strings.HasPrefix(raw, sunoTaskPrivateDataPrefix) || len(raw) > sunoTaskPrivateDataMaxBytes || !utf8.ValidString(raw) {
		return sunoTaskPrivateData{}, errors.New("invalid Suno task private data")
	}
	var privateData sunoTaskPrivateData
	if err := strictSunoJSON([]byte(strings.TrimPrefix(raw, sunoTaskPrivateDataPrefix)), &privateData); err != nil {
		return sunoTaskPrivateData{}, errors.New("invalid Suno task private data")
	}
	if _, err := marshalSunoTaskPrivateData(privateData); err != nil {
		return sunoTaskPrivateData{}, err
	}
	return privateData, nil
}

func sunoChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	return fmt.Sprintf("suno:channel:v1:%s:%d:%d:%s", taskID, userID, channelID, baseURL)
}

func sunoProviderTaskBinding(taskID, reservationID string, userID, channelID int) string {
	return fmt.Sprintf("suno:provider-task:v1:%s:%s:%d:%d", taskID, reservationID, userID, channelID)
}

func validateSunoTaskPublicID(taskID string) error {
	if len(taskID) != len("task_")+32 || !strings.HasPrefix(taskID, "task_") {
		return errors.New("invalid Suno task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return errors.New("invalid Suno task id")
		}
	}
	return nil
}

func isSunoTask(task *model.Task) bool {
	return task != nil && task.Platform == sunoTaskPlatform
}

func boundedSunoFailReason(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, character := range raw {
		if builder.Len() >= sunoTaskFailReasonMaxBytes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > sunoTaskFailReasonMaxBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func sunoTaskDTOFromModel(task *model.Task) (sunoTaskDTO, error) {
	if !isSunoTask(task) || validateSunoTaskPublicID(task.TaskID) != nil {
		return sunoTaskDTO{}, errors.New("invalid Suno task")
	}
	properties, err := decodeSunoTaskProperties(task.Properties)
	if err != nil {
		return sunoTaskDTO{}, err
	}
	if task.ID <= 0 || task.UserId <= 0 || task.ChannelId <= 0 ||
		len(task.Group) == 0 || len(task.Group) > 50 || !utf8.ValidString(task.Group) ||
		strings.TrimSpace(task.Group) != task.Group || strings.IndexFunc(task.Group, unicode.IsControl) >= 0 ||
		task.Action != string(properties.Action) || task.Quota < 0 ||
		task.CreatedAt < 0 || task.UpdatedAt < 0 || task.SubmitTime < 0 || task.StartTime < 0 || task.FinishTime < 0 ||
		task.CreatedAt > suno.MaxProviderTimestamp || task.UpdatedAt > suno.MaxProviderTimestamp ||
		task.SubmitTime > suno.MaxProviderTimestamp || task.StartTime > suno.MaxProviderTimestamp ||
		task.FinishTime > suno.MaxProviderTimestamp || len(task.FailReason) > sunoTaskFailReasonMaxBytes {
		return sunoTaskDTO{}, errors.New("invalid stored Suno task state")
	}
	switch task.Status {
	case model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusQueued,
		model.TaskStatusRunning, model.TaskStatusFailure, model.TaskStatusSuccess, model.TaskStatusUnknown:
	default:
		return sunoTaskDTO{}, errors.New("invalid stored Suno task status")
	}
	switch task.Progress {
	case "0%", "50%", "100%":
	default:
		return sunoTaskDTO{}, errors.New("invalid stored Suno task progress")
	}
	normalizedData, err := normalizeSunoTaskData(task.Data)
	if err != nil {
		return sunoTaskDTO{}, err
	}
	data := append(json.RawMessage(nil), normalizedData...)
	publicProperties := struct {
		Input           string `json:"input"`
		OriginModelName string `json:"origin_model_name"`
	}{OriginModelName: properties.OriginModelName}
	return sunoTaskDTO{
		ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId,
		Group: task.Group, ChannelID: task.ChannelId, Quota: task.Quota,
		Action: task.Action, Status: task.Status, FailReason: boundedSunoFailReason(task.FailReason),
		SubmitTime: task.SubmitTime, StartTime: task.StartTime, FinishTime: task.FinishTime,
		Progress: task.Progress, Properties: publicProperties, Data: data,
	}, nil
}

func strictSunoJSON(raw []byte, destination any) error {
	if err := rejectDuplicateSunoJSONKeys(raw); err != nil {
		return err
	}
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

func normalizeSunoTaskData(raw string) (string, error) {
	if len(raw) > suno.MaxProviderDataBytes {
		return "", errors.New("invalid stored Suno task data")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "null", nil
	}
	if rejectDuplicateSunoJSONKeys([]byte(raw)) != nil {
		return "", errors.New("invalid stored Suno task data")
	}
	return raw, nil
}

func rejectDuplicateSunoJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkSunoJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing Suno JSON")
	}
	return nil
}

func walkSunoJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid Suno JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("invalid Suno JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid Suno JSON object")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate Suno JSON field")
			}
			seen[key] = struct{}{}
			if err := walkSunoJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid Suno JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkSunoJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid Suno JSON array")
		}
	default:
		return errors.New("invalid Suno JSON delimiter")
	}
	return nil
}
