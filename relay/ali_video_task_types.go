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

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	aliWan "github.com/tokenrouter/tokenrouter/relay/channel/task/ali"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	aliWanTaskPlatform                = model.TaskOperationPlatformAliWan
	aliWanTaskPrivateDataPrefix       = "ali-video-v1:"
	aliWanTaskMetadataVersion         = 1
	aliWanTaskMetadataMaxBytes        = 64 << 10
	aliWanTaskStoredDataMaxBytes      = 1 << 20
	aliWanTaskEncryptedSecretMaxBytes = 16 << 10
	aliWanTaskFailReasonMaxBytes      = 4 << 10
	aliWanTaskChannelBaseURLMaxBytes  = 4 << 10
)

type aliWanAction string

const (
	aliWanActionGenerate     aliWanAction = "generate"
	aliWanActionTextGenerate aliWanAction = "textGenerate"
)

type aliWanPricingSnapshot struct {
	Plan                 service.ReferenceAsyncTaskBillingPlan `json:"plan"`
	Duration             int                                   `json:"duration"`
	ResolutionMultiplier string                                `json:"resolution_multiplier"`
}

func newAliWanPricingSnapshot(plan service.ReferenceAsyncTaskBillingPlan, upstreamModel string,
	duration int, resolution string,
) (aliWanPricingSnapshot, error) {
	snapshot := aliWanPricingSnapshot{
		Plan: plan, Duration: duration,
		ResolutionMultiplier: decimal.NewFromFloat(aliWan.ResolutionMultiplier(upstreamModel, resolution)).String(),
	}
	if err := snapshot.validate(upstreamModel, resolution); err != nil {
		return aliWanPricingSnapshot{}, err
	}
	return snapshot, nil
}

func (pricing aliWanPricingSnapshot) validate(upstreamModel, resolution string) error {
	if pricing.Plan.Validate() != nil || pricing.Duration < aliWan.MinDuration || pricing.Duration > aliWan.MaxDuration ||
		pricing.ResolutionMultiplier == "" || strings.TrimSpace(pricing.ResolutionMultiplier) != pricing.ResolutionMultiplier ||
		len(pricing.ResolutionMultiplier) > 64 || !validAliWanPricingResolution(resolution) {
		return errors.New("invalid Alibaba Wan pricing snapshot")
	}
	if !common.IsSafeDecimalLiteral(pricing.ResolutionMultiplier) {
		return errors.New("invalid Alibaba Wan resolution multiplier")
	}
	multiplier, err := decimal.NewFromString(pricing.ResolutionMultiplier)
	want := decimal.NewFromFloat(aliWan.ResolutionMultiplier(upstreamModel, resolution))
	if err != nil || multiplier.LessThanOrEqual(decimal.Zero) || multiplier.GreaterThan(decimal.NewFromInt(100)) || !multiplier.Equal(want) {
		return errors.New("invalid Alibaba Wan resolution multiplier")
	}
	return nil
}

func (pricing aliWanPricingSnapshot) quota(upstreamModel, resolution string) (int, error) {
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

func validAliWanPricingResolution(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "480P", "720P", "1080P", "832*480", "480*832", "624*624",
		"1280*720", "720*1280", "960*960", "1088*832", "832*1088",
		"1920*1080", "1080*1920", "1440*1440", "1632*1248", "1248*1632":
		return true
	default:
		return false
	}
}

type aliWanTaskProperties struct {
	Version           int                   `json:"version"`
	Family            string                `json:"family"`
	Input             string                `json:"input"`
	OriginModelName   string                `json:"origin_model_name"`
	UpstreamModelName string                `json:"upstream_model_name"`
	Action            aliWanAction          `json:"action"`
	HasInputReference bool                  `json:"has_input_reference"`
	Duration          int                   `json:"duration"`
	Resolution        string                `json:"resolution"`
	Pricing           aliWanPricingSnapshot `json:"pricing"`
}

type aliWanTaskPrivateData struct {
	Version                 int                   `json:"version"`
	RelayReservationID      string                `json:"relay_reservation_id"`
	ChannelBaseURL          string                `json:"channel_base_url"`
	EncryptedChannelKey     string                `json:"encrypted_channel_key"`
	EncryptedProviderTaskID string                `json:"encrypted_provider_task_id,omitempty"`
	SettlementPending       bool                  `json:"settlement_pending"`
	Pricing                 aliWanPricingSnapshot `json:"pricing"`
	BillingSource           string                `json:"billing_source"`
	SubscriptionID          int                   `json:"subscription_id,omitempty"`
	FundingUsageEpoch       int64                 `json:"funding_usage_epoch,omitempty"`
	TokenID                 int                   `json:"token_id,omitempty"`
}

// aliWanStoredTaskData is the bounded public provider state. Raw provider bodies
// and identifiers are deliberately excluded.
type aliWanStoredTaskData struct {
	State        aliWan.TaskStatus `json:"state"`
	ErrorCode    string            `json:"error_code,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
	ResultURL    string            `json:"result_url,omitempty"`
}

type aliWanVideoError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type aliWanVideoResponse struct {
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
	Error       *aliWanVideoError `json:"error,omitempty"`
}

type aliWanTaskDTO struct {
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

func marshalAliWanTaskProperties(properties aliWanTaskProperties) (string, error) {
	if properties.Version != aliWanTaskMetadataVersion || properties.Family != "ali_wan" ||
		!aliWan.IsModel(properties.OriginModelName) ||
		!aliWan.IsModel(properties.UpstreamModelName) ||
		properties.UpstreamModelName == "" || len(properties.UpstreamModelName) > 256 ||
		properties.Duration < aliWan.MinDuration || properties.Duration > aliWan.MaxDuration ||
		!validAliWanPricingResolution(properties.Resolution) ||
		!validAliWanStoredText(properties.Input, aliWan.MaxPromptBytes, true) ||
		(properties.Action == aliWanActionTextGenerate && strings.TrimSpace(properties.Input) == "") ||
		!validAliWanAction(properties.Action) || properties.Pricing.Plan.ModelName != properties.OriginModelName ||
		properties.Pricing.Duration != properties.Duration ||
		properties.Pricing.validate(properties.UpstreamModelName, properties.Resolution) != nil ||
		(properties.HasInputReference && properties.Action != aliWanActionGenerate) ||
		(!properties.HasInputReference && properties.Action != aliWanActionTextGenerate) {
		return "", errors.New("invalid durable AliWan task properties")
	}
	encoded, err := common.Marshal(properties)
	if err != nil || len(encoded) > aliWanTaskMetadataMaxBytes {
		return "", errors.New("AliWan task properties are too large")
	}
	return string(encoded), nil
}

func decodeAliWanTaskProperties(raw string) (aliWanTaskProperties, error) {
	if raw == "" || len(raw) > aliWanTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return aliWanTaskProperties{}, errors.New("invalid AliWan task properties")
	}
	var properties aliWanTaskProperties
	if err := strictAliWanJSON([]byte(raw), &properties); err != nil {
		return aliWanTaskProperties{}, errors.New("invalid AliWan task properties")
	}
	if _, err := marshalAliWanTaskProperties(properties); err != nil {
		return aliWanTaskProperties{}, err
	}
	return properties, nil
}

func marshalAliWanTaskPrivateData(privateData aliWanTaskPrivateData) (string, error) {
	if privateData.Version != aliWanTaskMetadataVersion || privateData.RelayReservationID == "" ||
		privateData.ChannelBaseURL == "" || len(privateData.ChannelBaseURL) > aliWanTaskChannelBaseURLMaxBytes ||
		privateData.EncryptedChannelKey == "" || len(privateData.EncryptedChannelKey) > aliWanTaskEncryptedSecretMaxBytes ||
		len(privateData.EncryptedProviderTaskID) > aliWanTaskEncryptedSecretMaxBytes ||
		privateData.BillingSource == "" || privateData.Pricing.Plan.Validate() != nil {
		return "", errors.New("invalid durable AliWan task private data")
	}
	if normalized, err := aliWan.EffectiveBaseURL(privateData.ChannelBaseURL); err != nil || normalized != privateData.ChannelBaseURL {
		return "", errors.New("invalid durable AliWan task base URL")
	}
	encoded, err := common.Marshal(privateData)
	if err != nil || len(encoded)+len(aliWanTaskPrivateDataPrefix) > aliWanTaskMetadataMaxBytes {
		return "", errors.New("AliWan task private data is too large")
	}
	return aliWanTaskPrivateDataPrefix + string(encoded), nil
}

func decodeAliWanTaskPrivateData(raw string) (aliWanTaskPrivateData, error) {
	if !strings.HasPrefix(raw, aliWanTaskPrivateDataPrefix) || len(raw) > aliWanTaskMetadataMaxBytes || !utf8.ValidString(raw) {
		return aliWanTaskPrivateData{}, errors.New("invalid AliWan task private data")
	}
	var privateData aliWanTaskPrivateData
	if err := strictAliWanJSON([]byte(strings.TrimPrefix(raw, aliWanTaskPrivateDataPrefix)), &privateData); err != nil {
		return aliWanTaskPrivateData{}, errors.New("invalid AliWan task private data")
	}
	if _, err := marshalAliWanTaskPrivateData(privateData); err != nil {
		return aliWanTaskPrivateData{}, err
	}
	return privateData, nil
}

func validateAliWanTaskPricingSnapshot(task *model.Task, privateData aliWanTaskPrivateData,
	expectedQuota int) (aliWanTaskProperties, error) {
	if task == nil || expectedQuota < 0 {
		return aliWanTaskProperties{}, errors.New("invalid AliWan pricing coordinates")
	}
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil || properties.Pricing != privateData.Pricing ||
		string(properties.Action) != task.Action {
		return aliWanTaskProperties{}, errors.New("AliWan pricing snapshots do not match")
	}
	quota, err := privateData.Pricing.quota(properties.UpstreamModelName, properties.Resolution)
	if err != nil || quota != expectedQuota {
		return aliWanTaskProperties{}, errors.New("AliWan pricing snapshot does not match quota")
	}
	return properties, nil
}

func marshalAliWanStoredTaskData(data aliWanStoredTaskData) (string, error) {
	if !validAliWanStatus(data.State) ||
		!validAliWanStoredText(data.ErrorCode, aliWanTaskFailReasonMaxBytes, true) ||
		!validAliWanStoredText(data.ErrorMessage, aliWanTaskFailReasonMaxBytes, true) ||
		!validAliWanStoredURL(data.ResultURL) {
		return "", errors.New("invalid normalized AliWan provider state")
	}
	encoded, err := common.Marshal(data)
	if err != nil || len(encoded) > aliWanTaskStoredDataMaxBytes {
		return "", errors.New("normalized AliWan provider state is too large")
	}
	return string(encoded), nil
}

func validAliWanStoredURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > aliWan.MaxResultURLBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func decodeAliWanStoredTaskData(raw string) (aliWanStoredTaskData, error) {
	if raw == "" || raw == "null" {
		return aliWanStoredTaskData{}, nil
	}
	if len(raw) > aliWanTaskStoredDataMaxBytes || !utf8.ValidString(raw) {
		return aliWanStoredTaskData{}, errors.New("invalid stored AliWan provider state")
	}
	var data aliWanStoredTaskData
	if err := strictAliWanJSON([]byte(raw), &data); err != nil {
		return aliWanStoredTaskData{}, err
	}
	if _, err := marshalAliWanStoredTaskData(data); err != nil {
		return aliWanStoredTaskData{}, err
	}
	return data, nil
}

func normalizedAliWanTaskData(provider *aliWan.Task) (aliWanStoredTaskData, error) {
	if provider == nil || !validAliWanStatus(provider.Status) {
		return aliWanStoredTaskData{}, errors.New("invalid AliWan provider task")
	}
	data := aliWanStoredTaskData{
		State:     provider.Status,
		ErrorCode: normalizedAliWanErrorCode(provider.ErrorCode),
		ResultURL: provider.ResultURL,
	}
	if provider.Status == aliWan.StatusFailed {
		data.ErrorMessage = "Alibaba Wan provider reported task failure"
	}
	if _, err := marshalAliWanStoredTaskData(data); err != nil {
		return aliWanStoredTaskData{}, err
	}
	return data, nil
}

func aliWanTaskDTOFromModel(task *model.Task) (aliWanTaskDTO, error) {
	if !isAliWanTask(task) || validateAliWanTaskPublicID(task.TaskID) != nil {
		return aliWanTaskDTO{}, errors.New("invalid AliWan task")
	}
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil {
		return aliWanTaskDTO{}, err
	}
	data, err := decodeAliWanStoredTaskData(task.Data)
	if err != nil {
		return aliWanTaskDTO{}, err
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return aliWanTaskDTO{}, err
	}
	publicProperties := struct {
		Input             string `json:"input"`
		UpstreamModelName string `json:"upstream_model_name"`
		OriginModelName   string `json:"origin_model_name"`
	}{properties.Input, properties.UpstreamModelName, properties.OriginModelName}
	return aliWanTaskDTO{ID: task.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		TaskID: task.TaskID, Platform: task.Platform, UserID: task.UserId, Group: task.Group,
		ChannelID: task.ChannelId, Quota: task.Quota, Action: task.Action, Status: task.Status,
		FailReason: task.FailReason, ResultURL: aliWanResultURL(data), SubmitTime: task.SubmitTime,
		StartTime: task.StartTime, FinishTime: task.FinishTime, Progress: task.Progress,
		Properties: publicProperties, Data: encoded}, nil
}

func aliWanTaskResponse(task *model.Task) (aliWanVideoResponse, error) {
	if !isAliWanTask(task) {
		return aliWanVideoResponse{}, errors.New("invalid AliWan task")
	}
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil {
		return aliWanVideoResponse{}, err
	}
	data, err := decodeAliWanStoredTaskData(task.Data)
	if err != nil {
		return aliWanVideoResponse{}, err
	}
	response := aliWanVideoResponse{ID: task.TaskID, TaskID: task.TaskID, Object: "video",
		Model: properties.OriginModelName, Status: aliWanPublicStatus(task.Status),
		Progress: aliWanProgressNumber(task.Progress), CreatedAt: task.CreatedAt,
		Seconds: strconv.Itoa(properties.Duration), Size: properties.Resolution}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		response.CompletedAt = task.FinishTime
	}
	if resultURL := aliWanResultURL(data); resultURL != "" {
		response.Metadata = map[string]any{"url": resultURL}
	}
	if task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusUnknown {
		code := data.ErrorCode
		if code == "" && task.Status == model.TaskStatusUnknown {
			code = "submit_outcome_unknown"
		}
		response.Error = &aliWanVideoError{Message: boundedAliWanFailReason(task.FailReason), Code: code}
	}
	return response, nil
}

func validAliWanAction(action aliWanAction) bool {
	_, err := parseAliWanAction(string(action))
	return err == nil
}

func parseAliWanAction(value string) (aliWanAction, error) {
	switch aliWanAction(value) {
	case aliWanActionGenerate, aliWanActionTextGenerate:
		return aliWanAction(value), nil
	default:
		return "", errors.New("invalid Alibaba Wan action")
	}
}

func aliWanActionForPrepared(prepared *aliWan.PreparedRequest) aliWanAction {
	if prepared != nil && prepared.HasInputReference {
		return aliWanActionGenerate
	}
	return aliWanActionTextGenerate
}
func validAliWanStatus(status aliWan.TaskStatus) bool {
	return status == aliWan.StatusSubmitted || status == aliWan.StatusProcessing || status == aliWan.StatusSucceeded || status == aliWan.StatusFailed
}
func isAliWanTask(task *model.Task) bool { return task != nil && task.Platform == aliWanTaskPlatform }

func validateAliWanTaskPublicID(taskID string) error {
	if len(taskID) != len("task_")+32 || !strings.HasPrefix(taskID, "task_") {
		return errors.New("invalid AliWan task id")
	}
	for _, character := range strings.TrimPrefix(taskID, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return errors.New("invalid AliWan task id")
		}
	}
	return nil
}

func boundedAliWanFailReason(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, character := range raw {
		if builder.Len() >= aliWanTaskFailReasonMaxBytes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > aliWanTaskFailReasonMaxBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func aliWanModelStatus(status aliWan.TaskStatus) string {
	switch status {
	case aliWan.StatusSubmitted:
		return model.TaskStatusSubmitted
	case aliWan.StatusProcessing:
		return model.TaskStatusRunning
	case aliWan.StatusSucceeded:
		return model.TaskStatusSuccess
	case aliWan.StatusFailed:
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}

func aliWanPublicStatus(status string) string {
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

func aliWanTaskProgress(status string) string {
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

func aliWanProgressNumber(progress string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(progress, "%"))
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func aliWanResultURL(data aliWanStoredTaskData) string {
	return data.ResultURL
}

func validAliWanStoredText(value string, maximum int, emptyOK bool) bool {
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

func normalizedAliWanErrorCode(value string) string {
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

func aliWanChannelCredentialBinding(taskID string, userID, channelID int, baseURL string) string {
	return fmt.Sprintf("ali-wan:channel:v1:%s:%d:%d:%s", taskID, userID, channelID, baseURL)
}

func aliWanProviderTaskBinding(taskID, reservationID string, userID, channelID int) string {
	return fmt.Sprintf("ali-wan:provider-task:v1:%s:%s:%d:%d", taskID, reservationID, userID, channelID)
}

func aliWanRecoveryJournalBinding(taskID string, action aliWanAction) string {
	return fmt.Sprintf("ali-wan:recovery-journal:v1:%s:%s", taskID, action)
}

func strictAliWanJSON(raw []byte, destination any) error {
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
