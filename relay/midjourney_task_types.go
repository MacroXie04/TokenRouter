package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/midjourney"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	midjourneyTaskPlatform                      = model.TaskOperationPlatformMidjourney
	midjourneyTaskMetadataVersion               = 1
	midjourneyTaskPrivateDataPrefix             = "midjourney-task-v1:"
	midjourneyTaskMetadataMaxBytes              = 64 << 10
	midjourneyTaskPrivateDataMaxBytes           = 64 << 10
	midjourneyTaskEncryptedSecretMaxBytes       = 16 << 10
	midjourneyTaskFailReasonMaxBytes            = 4 << 10
	midjourneyFetchRequestMaxBytes        int64 = 64 << 10
)

type midjourneyTaskProperties struct {
	Version         int                                   `json:"version"`
	OriginModelName string                                `json:"origin_model_name"`
	Action          midjourney.Action                     `json:"action"`
	Operation       string                                `json:"operation"`
	Prompt          string                                `json:"prompt,omitempty"`
	ParentTaskID    string                                `json:"parent_task_id,omitempty"`
	Pricing         service.ReferenceAsyncTaskBillingPlan `json:"pricing"`
}

type midjourneyTaskPrivateData struct {
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

type midjourneyStoredTaskData struct {
	CustomID   string                `json:"customId,omitempty"`
	BotType    string                `json:"botType,omitempty"`
	MaskBase64 string                `json:"maskBase64,omitempty"`
	VideoURLs  []midjourney.VideoURL `json:"videoUrls,omitempty"`
}

type midjourneyTaskDTO struct {
	ID          string                `json:"id"`
	Action      string                `json:"action"`
	CustomID    string                `json:"customId"`
	BotType     string                `json:"botType"`
	Prompt      string                `json:"prompt"`
	PromptEn    string                `json:"promptEn"`
	Description string                `json:"description"`
	State       string                `json:"state"`
	SubmitTime  int64                 `json:"submitTime"`
	StartTime   int64                 `json:"startTime"`
	FinishTime  int64                 `json:"finishTime"`
	ImageURL    string                `json:"imageUrl"`
	VideoURL    string                `json:"videoUrl"`
	VideoURLs   []midjourney.VideoURL `json:"videoUrls"`
	Status      string                `json:"status"`
	Progress    string                `json:"progress"`
	FailReason  string                `json:"failReason"`
	Buttons     any                   `json:"buttons"`
	MaskBase64  string                `json:"maskBase64"`
	Properties  any                   `json:"properties"`
}

func marshalMidjourneyTaskProperties(properties midjourneyTaskProperties) (string, error) {
	modelName, ok := midjourney.ModelForAction(properties.Action)
	if properties.Version != midjourneyTaskMetadataVersion || !ok || modelName != properties.OriginModelName ||
		properties.Operation == "" || len(properties.Operation) > 64 ||
		properties.Pricing.ModelName != properties.OriginModelName || properties.Pricing.Validate() != nil {
		return "", errors.New("invalid durable Midjourney task properties")
	}
	if properties.ParentTaskID != "" && validateMidjourneyTaskPublicID(properties.ParentTaskID) != nil {
		return "", errors.New("invalid durable Midjourney parent task")
	}
	encoded, err := common.Marshal(properties)
	if err != nil || len(encoded) > midjourneyTaskMetadataMaxBytes {
		return "", errors.New("Midjourney task properties are too large")
	}
	return string(encoded), nil
}

func decodeMidjourneyTaskProperties(raw string) (midjourneyTaskProperties, error) {
	if raw == "" || len(raw) > midjourneyTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return midjourneyTaskProperties{}, errors.New("invalid Midjourney task properties")
	}
	var properties midjourneyTaskProperties
	if err := strictMidjourneyTaskJSON([]byte(raw), &properties); err != nil {
		return midjourneyTaskProperties{}, errors.New("invalid Midjourney task properties")
	}
	if _, err := marshalMidjourneyTaskProperties(properties); err != nil {
		return midjourneyTaskProperties{}, err
	}
	return properties, nil
}

func marshalMidjourneyTaskPrivateData(privateData midjourneyTaskPrivateData) (string, error) {
	if privateData.RelayReservationID == "" || privateData.ChannelBaseURL == "" ||
		privateData.EncryptedChannelKey == "" ||
		len(privateData.EncryptedChannelKey) > midjourneyTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedProviderTaskID) > midjourneyTaskEncryptedSecretMaxBytes {
		return "", errors.New("invalid durable Midjourney private data")
	}
	if _, err := midjourney.ValidateBaseURL(privateData.ChannelBaseURL); err != nil {
		return "", errors.New("invalid durable Midjourney base URL")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil || len(encoded)+len(midjourneyTaskPrivateDataPrefix) > midjourneyTaskPrivateDataMaxBytes {
		return "", errors.New("Midjourney task private data is too large")
	}
	return midjourneyTaskPrivateDataPrefix + string(encoded), nil
}

func decodeMidjourneyTaskPrivateData(raw string) (midjourneyTaskPrivateData, error) {
	if !strings.HasPrefix(raw, midjourneyTaskPrivateDataPrefix) || len(raw) > midjourneyTaskPrivateDataMaxBytes || !utf8.ValidString(raw) {
		return midjourneyTaskPrivateData{}, errors.New("invalid Midjourney task private data")
	}
	var privateData midjourneyTaskPrivateData
	if err := strictMidjourneyTaskJSON([]byte(strings.TrimPrefix(raw, midjourneyTaskPrivateDataPrefix)), &privateData); err != nil {
		return midjourneyTaskPrivateData{}, errors.New("invalid Midjourney task private data")
	}
	if _, err := marshalMidjourneyTaskPrivateData(privateData); err != nil {
		return midjourneyTaskPrivateData{}, err
	}
	return privateData, nil
}

func marshalMidjourneyStoredTaskData(data midjourneyStoredTaskData) (string, error) {
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > midjourney.MaxProviderMetadataBytes {
		return "", errors.New("Midjourney stored task data is invalid")
	}
	return string(encoded), nil
}

func decodeMidjourneyStoredTaskData(raw string) (midjourneyStoredTaskData, error) {
	if strings.TrimSpace(raw) == "" || raw == "null" {
		return midjourneyStoredTaskData{}, nil
	}
	if len(raw) > midjourney.MaxProviderMetadataBytes || !utf8.ValidString(raw) {
		return midjourneyStoredTaskData{}, errors.New("invalid Midjourney stored task data")
	}
	var data midjourneyStoredTaskData
	if err := strictMidjourneyTaskJSON([]byte(raw), &data); err != nil {
		return midjourneyStoredTaskData{}, errors.New("invalid Midjourney stored task data")
	}
	return data, nil
}

func midjourneyTaskDTOFromModel(task *model.Midjourney, generic *model.Task) (midjourneyTaskDTO, error) {
	if task == nil || generic == nil || task.MjId != generic.TaskID || task.UserId != generic.UserId ||
		generic.Platform != midjourneyTaskPlatform || validateMidjourneyTaskPublicID(task.MjId) != nil {
		return midjourneyTaskDTO{}, errors.New("invalid Midjourney task")
	}
	stored, err := decodeMidjourneyStoredTaskData(generic.Data)
	if err != nil {
		return midjourneyTaskDTO{}, err
	}
	videoURLs := stored.VideoURLs
	if task.VideoUrls != "" {
		if err := strictMidjourneyTaskJSON([]byte(task.VideoUrls), &videoURLs); err != nil {
			return midjourneyTaskDTO{}, errors.New("invalid stored Midjourney video URLs")
		}
	}
	var buttons any
	if strings.TrimSpace(task.Buttons) != "" && task.Buttons != "null" {
		if len(task.Buttons) > midjourney.MaxProviderMetadataBytes || json.Unmarshal([]byte(task.Buttons), &buttons) != nil {
			return midjourneyTaskDTO{}, errors.New("invalid stored Midjourney buttons")
		}
	}
	var properties any
	if strings.TrimSpace(task.Properties) != "" && task.Properties != "null" {
		if len(task.Properties) > midjourney.MaxProviderMetadataBytes || json.Unmarshal([]byte(task.Properties), &properties) != nil {
			return midjourneyTaskDTO{}, errors.New("invalid stored Midjourney properties")
		}
	}
	return midjourneyTaskDTO{
		ID: task.MjId, Action: task.Action, CustomID: stored.CustomID, BotType: stored.BotType,
		Prompt: task.Prompt, PromptEn: task.PromptEn, Description: task.Description, State: task.State,
		SubmitTime: task.SubmitTime, StartTime: task.StartTime, FinishTime: task.FinishTime,
		ImageURL: task.ImageUrl, VideoURL: task.VideoUrl, VideoURLs: videoURLs,
		Status: task.Status, Progress: task.Progress, FailReason: task.FailReason,
		Buttons: buttons, MaskBase64: stored.MaskBase64, Properties: properties,
	}, nil
}

func midjourneyChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	return fmt.Sprintf("midjourney:channel:v1:%s:%d:%d:%s", taskID, userID, channelID, baseURL)
}

func midjourneyProviderTaskBinding(taskID, reservationID string, userID, channelID int, action midjourney.Action) string {
	return fmt.Sprintf("midjourney:provider-task:v1:%s:%s:%d:%d:%s", taskID, reservationID, userID, channelID, action)
}

func validateMidjourneyTaskPublicID(taskID string) error {
	if len(taskID) != len("task_")+32 || !strings.HasPrefix(taskID, "task_") {
		return errors.New("invalid Midjourney task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return errors.New("invalid Midjourney task id")
		}
	}
	return nil
}

func boundedMidjourneyFailReason(raw string) string {
	raw = strings.TrimSpace(strings.ToValidUTF8(raw, "�"))
	var builder strings.Builder
	for _, character := range raw {
		if builder.Len() >= midjourneyTaskFailReasonMaxBytes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > midjourneyTaskFailReasonMaxBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func strictMidjourneyTaskJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing Midjourney task JSON")
	}
	return nil
}
