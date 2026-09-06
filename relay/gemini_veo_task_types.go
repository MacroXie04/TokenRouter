package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	geminiVeo "github.com/tokenrouter/tokenrouter/relay/channel/task/gemini"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	geminiVeoTaskPlatform                = model.TaskOperationPlatformGeminiVeo
	geminiVeoTaskPrivateDataPrefix       = "veo-task-v1:"
	geminiVeoTaskMetadataVersion         = 1
	geminiVeoTaskMetadataMaxBytes        = 256 << 10
	geminiVeoTaskStoredDataMaxBytes      = 1 << 20
	geminiVeoTaskEncryptedSecretMaxBytes = 192 << 10
	geminiVeoTaskFailReasonMaxBytes      = 4 << 10
	geminiVeoTaskRoutingMaxBytes         = 64 << 10
	geminiVeoTaskMaxInlineVideoBytes     = 48 << 20
	geminiVeoTaskJSONMaxDepth            = 32
	geminiVeoTaskJSONMaxEntries          = 256
)

type geminiVeoTaskProperties struct {
	Version           int                      `json:"version"`
	Family            string                   `json:"family"`
	Input             string                   `json:"input"`
	OriginModelName   string                   `json:"origin_model_name"`
	UpstreamModelName string                   `json:"upstream_model_name"`
	Action            geminiVeo.Action         `json:"action"`
	HasInputReference bool                     `json:"has_input_reference"`
	Duration          int                      `json:"duration"`
	Resolution        string                   `json:"resolution"`
	AspectRatio       string                   `json:"aspect_ratio"`
	Pricing           geminiVeoPricingSnapshot `json:"pricing"`
}

type geminiVeoTaskPrivateData struct {
	Version                 int                      `json:"version"`
	RelayReservationID      string                   `json:"relay_reservation_id"`
	RoutingSnapshot         string                   `json:"routing_snapshot"`
	EncryptedChannelKey     string                   `json:"encrypted_channel_key"`
	EncryptedProviderTaskID string                   `json:"encrypted_provider_task_id,omitempty"`
	SettlementPending       bool                     `json:"settlement_pending"`
	Pricing                 geminiVeoPricingSnapshot `json:"pricing"`
	BillingSource           string                   `json:"billing_source"`
	SubscriptionID          int                      `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64                    `json:"funding_usage_epoch,omitempty"`
	TokenID                 int                      `json:"token_id,omitempty"`
}

// geminiVeoPricingSnapshot freezes both the reference base price and every
// request-derived multiplier used by the reference Veo adapter.
type geminiVeoPricingSnapshot struct {
	Plan                 service.ReferenceAsyncTaskBillingPlan `json:"plan"`
	Duration             int                                   `json:"duration"`
	ResolutionMultiplier string                                `json:"resolution_multiplier"`
}

func newGeminiVeoPricingSnapshot(plan service.ReferenceAsyncTaskBillingPlan, upstreamModel string,
	duration int, resolution string) (geminiVeoPricingSnapshot, error) {
	multiplier := decimal.NewFromFloat(geminiVeo.ResolutionMultiplier(upstreamModel, resolution)).String()
	snapshot := geminiVeoPricingSnapshot{Plan: plan, Duration: duration, ResolutionMultiplier: multiplier}
	if err := snapshot.validate(upstreamModel, resolution); err != nil {
		return geminiVeoPricingSnapshot{}, err
	}
	return snapshot, nil
}

func (pricing geminiVeoPricingSnapshot) validate(upstreamModel, resolution string) error {
	if pricing.Plan.Validate() != nil || pricing.Duration != 4 && pricing.Duration != 6 && pricing.Duration != 8 ||
		pricing.ResolutionMultiplier == "" || strings.TrimSpace(pricing.ResolutionMultiplier) != pricing.ResolutionMultiplier ||
		len(pricing.ResolutionMultiplier) > 64 {
		return errors.New("invalid Gemini Veo pricing snapshot")
	}
	if !common.IsSafeDecimalLiteral(pricing.ResolutionMultiplier) {
		return errors.New("invalid Gemini Veo resolution multiplier")
	}
	multiplier, err := decimal.NewFromString(pricing.ResolutionMultiplier)
	want := decimal.NewFromFloat(geminiVeo.ResolutionMultiplier(upstreamModel, resolution))
	if err != nil || multiplier.IsNegative() || !multiplier.Equal(want) {
		return errors.New("invalid Gemini Veo resolution multiplier")
	}
	return nil
}

func (pricing geminiVeoPricingSnapshot) quota(upstreamModel, resolution string) (int, error) {
	if err := pricing.validate(upstreamModel, resolution); err != nil {
		return 0, err
	}
	base, err := pricing.Plan.PreConsumeQuota()
	if err != nil {
		return 0, err
	}
	multiplier, _ := decimal.NewFromString(pricing.ResolutionMultiplier)
	return common.QuotaFromDecimalStrict(decimal.NewFromInt(int64(base)).
		Mul(decimal.NewFromInt(int64(pricing.Duration))).Mul(multiplier))
}

// geminiVeoStoredTaskData is the bounded public provider state. Raw provider bodies
// and identifiers are deliberately excluded.
type geminiVeoStoredTaskData struct {
	State            geminiVeo.TaskStatus `json:"state"`
	ErrorCode        string               `json:"error_code,omitempty"`
	ErrorMessage     string               `json:"error_message,omitempty"`
	ResultURL        string               `json:"result_url,omitempty"`
	HasInlineVideo   bool                 `json:"has_inline_video,omitempty"`
	InlineMIMEType   string               `json:"inline_mime_type,omitempty"`
	InlineBytes      int64                `json:"inline_bytes,omitempty"`
	InlineArtifactID string               `json:"inline_artifact_id,omitempty"`
}

type geminiVeoVideoError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type geminiVeoVideoResponse struct {
	ID          string               `json:"id"`
	TaskID      string               `json:"task_id,omitempty"`
	Object      string               `json:"object"`
	Model       string               `json:"model"`
	Status      string               `json:"status"`
	Progress    int                  `json:"progress"`
	CreatedAt   int64                `json:"created_at"`
	CompletedAt int64                `json:"completed_at,omitempty"`
	Seconds     string               `json:"seconds,omitempty"`
	Size        string               `json:"size,omitempty"`
	Metadata    map[string]any       `json:"metadata,omitempty"`
	Error       *geminiVeoVideoError `json:"error,omitempty"`
}

type geminiVeoTaskDTO struct {
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

func marshalGeminiVeoTaskProperties(properties geminiVeoTaskProperties) (string, error) {
	_, validFamily := veoTaskProviderByFamily(properties.Family)
	if properties.Version != geminiVeoTaskMetadataVersion || !validFamily ||
		!geminiVeo.IsModel(properties.OriginModelName) ||
		!geminiVeo.IsModel(properties.UpstreamModelName) ||
		properties.UpstreamModelName == "" || len(properties.UpstreamModelName) > geminiVeo.MaxModelBytes ||
		!geminiVeo.ValidModelSettings(properties.UpstreamModelName, properties.Duration, properties.Resolution, properties.AspectRatio) ||
		!validGeminiVeoStoredText(properties.Input, geminiVeo.MaxPromptBytes, false) ||
		!validGeminiVeoAction(properties.Action) || properties.Pricing.Plan.ModelName != properties.OriginModelName ||
		properties.Pricing.Duration != properties.Duration ||
		properties.Pricing.validate(properties.UpstreamModelName, properties.Resolution) != nil {
		return "", errors.New("invalid durable GeminiVeo task properties")
	}
	encoded, err := common.Marshal(properties)
	if err != nil || len(encoded) > geminiVeoTaskMetadataMaxBytes {
		return "", errors.New("GeminiVeo task properties are too large")
	}
	return string(encoded), nil
}

func decodeGeminiVeoTaskProperties(raw string) (geminiVeoTaskProperties, error) {
	if raw == "" || len(raw) > geminiVeoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return geminiVeoTaskProperties{}, errors.New("invalid GeminiVeo task properties")
	}
	var properties geminiVeoTaskProperties
	if err := strictGeminiVeoJSON([]byte(raw), &properties); err != nil {
		return geminiVeoTaskProperties{}, errors.New("invalid GeminiVeo task properties")
	}
	if _, err := marshalGeminiVeoTaskProperties(properties); err != nil {
		return geminiVeoTaskProperties{}, err
	}
	return properties, nil
}

func marshalGeminiVeoTaskPrivateData(privateData geminiVeoTaskPrivateData) (string, error) {
	if privateData.Version != geminiVeoTaskMetadataVersion || privateData.RelayReservationID == "" ||
		privateData.RoutingSnapshot == "" || len(privateData.RoutingSnapshot) > geminiVeoTaskRoutingMaxBytes ||
		privateData.EncryptedChannelKey == "" || len(privateData.EncryptedChannelKey) > geminiVeoTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedProviderTaskID) > geminiVeoTaskEncryptedSecretMaxBytes ||
		privateData.BillingSource == "" || privateData.Pricing.Plan.Validate() != nil {
		return "", errors.New("invalid durable GeminiVeo task private data")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil || len(encoded)+len(geminiVeoTaskPrivateDataPrefix) > geminiVeoTaskMetadataMaxBytes {
		return "", errors.New("GeminiVeo task private data is too large")
	}
	return geminiVeoTaskPrivateDataPrefix + string(encoded), nil
}

func decodeGeminiVeoTaskPrivateData(raw string) (geminiVeoTaskPrivateData, error) {
	if !strings.HasPrefix(raw, geminiVeoTaskPrivateDataPrefix) || len(raw) > geminiVeoTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return geminiVeoTaskPrivateData{}, errors.New("invalid GeminiVeo task private data")
	}
	var privateData geminiVeoTaskPrivateData
	if err := strictGeminiVeoJSON([]byte(strings.TrimPrefix(raw, geminiVeoTaskPrivateDataPrefix)), &privateData); err != nil {
		return geminiVeoTaskPrivateData{}, errors.New("invalid GeminiVeo task private data")
	}
	if _, err := marshalGeminiVeoTaskPrivateData(privateData); err != nil {
		return geminiVeoTaskPrivateData{}, err
	}
	return privateData, nil
}

func validateGeminiVeoTaskPricingSnapshot(task *model.Task, privateData geminiVeoTaskPrivateData,
	expectedQuota int) (geminiVeoTaskProperties, error) {
	if task == nil || expectedQuota < 0 {
		return geminiVeoTaskProperties{}, errors.New("invalid GeminiVeo pricing coordinates")
	}
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil || properties.Pricing != privateData.Pricing ||
		string(properties.Action) != task.Action {
		return geminiVeoTaskProperties{}, errors.New("GeminiVeo pricing snapshots do not match")
	}
	descriptor, ok := veoTaskProviderByPlatform(task.Platform)
	if !ok || properties.Family != descriptor.Family ||
		descriptor.ValidateRoutingSnapshot(privateData.RoutingSnapshot, properties.UpstreamModelName) != nil {
		return geminiVeoTaskProperties{}, errors.New("GeminiVeo provider snapshot does not match task platform")
	}
	quota, err := privateData.Pricing.quota(properties.UpstreamModelName, properties.Resolution)
	if err != nil || quota != expectedQuota {
		return geminiVeoTaskProperties{}, errors.New("GeminiVeo pricing snapshot does not match quota")
	}
	return properties, nil
}

func marshalGeminiVeoStoredTaskData(data geminiVeoStoredTaskData) (string, error) {
	outputs := 0
	if data.ResultURL != "" {
		outputs++
	}
	if data.HasInlineVideo {
		outputs++
	}
	validInline := !data.HasInlineVideo && data.InlineMIMEType == "" && data.InlineBytes == 0 &&
		data.InlineArtifactID == "" ||
		data.HasInlineVideo && data.InlineMIMEType == "video/mp4" && data.InlineBytes > 0 &&
			data.InlineBytes <= geminiVeoTaskMaxInlineVideoBytes &&
			(data.InlineArtifactID == "" || validGeminiVeoInlineArtifactID(data.InlineArtifactID))
	validOutputState := data.State == geminiVeo.StatusProcessing && outputs == 0 ||
		data.State == geminiVeo.StatusSucceeded && outputs == 1 ||
		data.State == geminiVeo.StatusFailed && outputs == 0
	if !validGeminiVeoStatus(data.State) || !validInline || !validOutputState ||
		!validGeminiVeoStoredText(data.ErrorCode, geminiVeoTaskFailReasonMaxBytes, true) ||
		!validGeminiVeoStoredText(data.ErrorMessage, geminiVeoTaskFailReasonMaxBytes, true) ||
		!validGeminiVeoStoredURL(data.ResultURL) {
		return "", errors.New("invalid normalized GeminiVeo provider state")
	}
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > geminiVeoTaskStoredDataMaxBytes {
		return "", errors.New("normalized GeminiVeo provider state is too large")
	}
	return string(encoded), nil
}

func validGeminiVeoInlineArtifactID(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validGeminiVeoStoredURL(value string) bool {
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

func decodeGeminiVeoStoredTaskData(raw string) (geminiVeoStoredTaskData, error) {
	if raw == "" || raw == "null" {
		return geminiVeoStoredTaskData{}, nil
	}
	if len(raw) > geminiVeoTaskStoredDataMaxBytes || !utf8.ValidString(raw) {
		return geminiVeoStoredTaskData{}, errors.New("invalid stored GeminiVeo provider state")
	}
	var data geminiVeoStoredTaskData
	if err := strictGeminiVeoJSON([]byte(raw), &data); err != nil {
		return geminiVeoStoredTaskData{}, err
	}
	if _, err := marshalGeminiVeoStoredTaskData(data); err != nil {
		return geminiVeoStoredTaskData{}, err
	}
	return data, nil
}

func normalizedGeminiVeoTaskData(provider *VeoProviderTask) (geminiVeoStoredTaskData, error) {
	if provider == nil || !validGeminiVeoStatus(provider.Status) {
		return geminiVeoStoredTaskData{}, errors.New("invalid GeminiVeo provider task")
	}
	data := geminiVeoStoredTaskData{
		State: provider.Status, ErrorCode: normalizedGeminiVeoErrorCode(provider.ErrorCode),
		ResultURL: provider.ResultURL, HasInlineVideo: provider.InlineBase64 != "",
		InlineMIMEType: provider.InlineMIMEType, InlineBytes: provider.InlineBytes,
	}
	if provider.Status == geminiVeo.StatusFailed {
		data.ErrorMessage = "GeminiVeo provider reported task failure"
	}
	if _, err := marshalGeminiVeoStoredTaskData(data); err != nil {
		return geminiVeoStoredTaskData{}, err
	}
	return data, nil
}

func geminiVeoTaskDTOFromModel(task *model.Task) (geminiVeoTaskDTO, error) {
	if !isGeminiVeoTask(task) || validateGeminiVeoTaskPublicID(task.TaskID) != nil {
		return geminiVeoTaskDTO{}, errors.New("invalid GeminiVeo task")
	}
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil {
		return geminiVeoTaskDTO{}, err
	}
	data, err := decodeGeminiVeoStoredTaskData(task.Data)
	if err != nil {
		return geminiVeoTaskDTO{}, err
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return geminiVeoTaskDTO{}, err
	}
	publicProperties := struct {
		Input             string `json:"input"`
		UpstreamModelName string `json:"upstream_model_name"`
		OriginModelName   string `json:"origin_model_name"`
	}{properties.Input, properties.UpstreamModelName, properties.OriginModelName}
	return geminiVeoTaskDTO{ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId, Group: task.Group,
		ChannelID: task.ChannelId, Quota: task.Quota, Action: task.Action, Status: task.Status,
		FailReason: task.FailReason, ResultURL: geminiVeoResultURL(data), SubmitTime: task.SubmitTime,
		StartTime: task.StartTime, FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: publicProperties, Data: encoded}, nil
}

func geminiVeoTaskResponse(task *model.Task) (geminiVeoVideoResponse, error) {
	if !isGeminiVeoTask(task) {
		return geminiVeoVideoResponse{}, errors.New("invalid GeminiVeo task")
	}
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil {
		return geminiVeoVideoResponse{}, err
	}
	data, err := decodeGeminiVeoStoredTaskData(task.Data)
	if err != nil {
		return geminiVeoVideoResponse{}, err
	}
	response := geminiVeoVideoResponse{ID: task.TaskID, TaskID: task.TaskID, Object: "video",
		Model: properties.OriginModelName, Status: geminiVeoPublicStatus(task.Status),
		Progress: geminiVeoProgressNumber(task.Progress), CreatedAt: task.CreatedAt,
		Seconds: strconv.Itoa(properties.Duration), Size: properties.Resolution}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		response.CompletedAt = task.FinishTime
	}
	if resultURL := geminiVeoResultURL(data); resultURL != "" {
		response.Metadata = map[string]any{"url": resultURL}
	}
	if task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		code := data.ErrorCode
		if code == "" && task.Status == model.TaskStatusUnknown {
			code = "submit_outcome_unknown"
		}
		response.Error = &geminiVeoVideoError{Message: boundedGeminiVeoFailReason(task.FailReason), Code: code}
	}
	return response, nil
}

func validGeminiVeoAction(action geminiVeo.Action) bool {
	_, err := geminiVeo.ParseAction(string(action))
	return err == nil
}
func validGeminiVeoStatus(status geminiVeo.TaskStatus) bool {
	return status == geminiVeo.StatusProcessing || status == geminiVeo.StatusSucceeded || status == geminiVeo.StatusFailed
}
func isGeminiVeoTask(task *model.Task) bool {
	return task != nil && model.IsVeoTaskOperationPlatform(task.Platform)
}

func validateGeminiVeoTaskPublicID(taskID string) error {
	if len(taskID) != len("task_")+32 || !strings.HasPrefix(taskID, "task_") {
		return errors.New("invalid GeminiVeo task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return errors.New("invalid GeminiVeo task id")
		}
	}
	return nil
}

func boundedGeminiVeoFailReason(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, character := range raw {
		if builder.Len() >= geminiVeoTaskFailReasonMaxBytes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > geminiVeoTaskFailReasonMaxBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func geminiVeoModelStatus(status geminiVeo.TaskStatus) string {
	switch status {
	case geminiVeo.StatusProcessing:
		return model.TaskStatusRunning
	case geminiVeo.StatusSucceeded:
		return model.TaskStatusSuccess
	case geminiVeo.StatusFailed:
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}

func geminiVeoPublicStatus(status string) string {
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

func geminiVeoTaskProgress(status string) string {
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

func geminiVeoProgressNumber(progress string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(progress, "%"))
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func geminiVeoResultURL(data geminiVeoStoredTaskData) string {
	return data.ResultURL
}

func validGeminiVeoStoredText(value string, maximum int, emptyOK bool) bool {
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

func normalizedGeminiVeoErrorCode(value string) string {
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

func geminiVeoChannelCredentialBinding(taskID, platform string, userID, channelID int, routing string) string {
	return fmt.Sprintf("veo:channel:v1:%s:%s:%d:%d:%s", taskID, platform, userID, channelID, routing)
}

func geminiVeoProviderTaskBinding(taskID, reservationID, platform string, userID, channelID int) string {
	return fmt.Sprintf("veo:provider-task:v1:%s:%s:%s:%d:%d", taskID, reservationID, platform, userID, channelID)
}

func geminiVeoRecoveryJournalBinding(taskID, platform string, action geminiVeo.Action) string {
	return fmt.Sprintf("veo:recovery-journal:v1:%s:%s:%s", taskID, platform, action)
}

func strictGeminiVeoJSON(raw []byte, destination any) error {
	tokens := json.NewDecoder(bytes.NewReader(raw))
	tokens.UseNumber()
	if err := walkStrictGeminiVeoJSON(tokens, 0); err != nil {
		return err
	}
	if token, err := tokens.Token(); !errors.Is(err, io.EOF) || token != nil {
		return errors.New("trailing JSON")
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

func walkStrictGeminiVeoJSON(decoder *json.Decoder, depth int) error {
	if depth > geminiVeoTaskJSONMaxDepth {
		return errors.New("JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			if len(seen) >= geminiVeoTaskJSONMaxEntries {
				return errors.New("JSON object has too many fields")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := walkStrictGeminiVeoJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		entries := 0
		for decoder.More() {
			entries++
			if entries > geminiVeoTaskJSONMaxEntries {
				return errors.New("JSON array has too many entries")
			}
			if err := walkStrictGeminiVeoJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
