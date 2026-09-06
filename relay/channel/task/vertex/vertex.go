// Package vertex implements the bounded Vertex AI Veo long-running-operation
// protocol. Durable persistence, accounting, leases, and recovery are owned by
// the relay layer.
package vertex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	geminitask "github.com/tokenrouter/tokenrouter/relay/channel/task/gemini"
	vertexcore "github.com/tokenrouter/tokenrouter/relay/channel/vertex"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName             = "vertex-veo"
	MaxOperationNameBytes   = 1024
	MaxCredentialBytes      = 128 << 10
	MaxInlineVideoBytes     = 48 << 20
	MaxResponseBodyBytes    = 66 << 20
	MaxProviderMessageBytes = 8 << 10
	maxJSONDepth            = 32
	maxJSONNodes            = 200_000
)

var operationComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)

func ModelList() []string       { return geminitask.ModelList() }
func IsModel(value string) bool { return geminitask.IsModel(value) }

// Snapshot is the non-secret channel configuration persisted with a task.
// BaseURL and Region are canonicalized before quota reservation so recovery
// never consults mutable channel configuration.
type Snapshot struct {
	BaseURL    string `json:"base_url"`
	Region     string `json:"region"`
	CustomBase bool   `json:"custom_base"`
}

func PrepareSnapshot(configuredBase, channelOther, otherSettings, originModel string) (Snapshot, error) {
	if !IsModel(originModel) {
		return Snapshot{}, errors.New("Vertex AI Veo model is invalid")
	}
	if err := vertexcore.ValidateTaskChannelSettings(otherSettings); err != nil {
		return Snapshot{}, err
	}
	region, err := vertexcore.ResolveTaskRegion(channelOther, originModel)
	if err != nil {
		return Snapshot{}, err
	}
	baseURL, err := vertexcore.CanonicalTaskBaseURL(configuredBase, region)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{BaseURL: baseURL, Region: region, CustomBase: strings.TrimSpace(configuredBase) != ""}, nil
}

// ValidateSnapshot verifies that a persisted default Google endpoint is still
// derived from its region while retaining explicitly configured custom bases.
func ValidateSnapshot(snapshot Snapshot, mappedModel string) error {
	if !IsModel(mappedModel) || snapshot.Region == "" || snapshot.BaseURL == "" {
		return errors.New("Vertex AI Veo task configuration is invalid")
	}
	region, err := vertexcore.ResolveTaskRegion(snapshot.Region, mappedModel)
	if err != nil || region != snapshot.Region {
		return errors.New("Vertex AI Veo task region is invalid")
	}
	configuredBase := snapshot.BaseURL
	if !snapshot.CustomBase {
		configuredBase = ""
	}
	baseURL, err := vertexcore.CanonicalTaskBaseURL(configuredBase, region)
	if err != nil || baseURL != snapshot.BaseURL {
		return errors.New("Vertex AI Veo task base URL is invalid")
	}
	return nil
}

type Config struct {
	Snapshot      Snapshot
	OtherSettings string
	Credential    string
}

type TaskStatus = geminitask.TaskStatus

const (
	StatusProcessing = geminitask.StatusProcessing
	StatusSucceeded  = geminitask.StatusSucceeded
	StatusFailed     = geminitask.StatusFailed
)

type InlineVideo struct {
	MIMEType     string
	Base64       string
	DecodedBytes int64
}

func (video *InlineVideo) Reader() (io.Reader, error) {
	if video == nil {
		return nil, errors.New("Vertex AI Veo inline video is unavailable")
	}
	validated, err := validateInlineVideo(video.MIMEType, video.Base64)
	if err != nil || validated.DecodedBytes != video.DecodedBytes {
		return nil, errors.New("Vertex AI Veo inline video is invalid")
	}
	return base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(video.Base64)), nil
}

type Task struct {
	ProviderTaskID string
	Status         TaskStatus
	ErrorCode      string
	ErrorMessage   string
	InlineVideo    *InlineVideo
}

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Vertex AI Veo request failed"
	}
	return e.Err.Error()
}

func (e *RequestError) Unwrap() error { return e.Err }

func SubmitWasDispatched(err error) bool {
	var requestError *RequestError
	return errors.As(err, &requestError) && requestError.Dispatched
}

type Client struct {
	HTTPClient    *http.Client
	TokenProvider vertexcore.TaskTokenProvider
}

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil, DialContext: common.SafeDialContext, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout: 90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       90 * time.Second,
	}
}

func (client *Client) httpClient() *http.Client {
	if client != nil && client.HTTPClient != nil {
		return client.HTTPClient
	}
	return NewHTTPClient()
}

func (client *Client) Submit(ctx context.Context, config Config, prepared *geminitask.PreparedRequest) (*Task, []byte, error) {
	if err := ValidatePrepared(prepared); err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	adapter, meta, err := client.requestAdapter(config, prepared.UpstreamModel)
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	meta.Context = contextOrBackground(ctx)
	endpoint, err := adapter.GetTaskRequestURL(meta, "predictLongRunning", "")
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	expectedProject := adapter.TaskProjectID()
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, endpoint, bytes.NewReader(prepared.Body))
	if err != nil {
		return nil, nil, &RequestError{Err: errors.New("create Vertex AI Veo submit request")}
	}
	if err := adapter.SetupRequestHeader(request, meta); err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	var wroteProviderRequest atomic.Bool
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteProviderRequest.Store(true) },
	}))
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("Vertex AI Veo submit transport failed: %w", relaycommon.SanitizeTransportError(err)), Dispatched: wroteProviderRequest.Load()}
	}
	defer response.Body.Close()
	raw, readErr := readResponse(response)
	if readErr != nil {
		return nil, raw, &RequestError{Err: readErr, Dispatched: true}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, raw, &RequestError{Err: &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}, Dispatched: true}
	}
	var parsed operationResponse
	if strictDecode(raw, &parsed) != nil {
		return nil, raw, &RequestError{Err: errors.New("invalid Vertex AI Veo submit response"), Dispatched: true}
	}
	if parsed.Error != nil {
		return failedTask(parsed.Error), raw, nil
	}
	identity, err := parseOperationName(parsed.Name)
	if err != nil || identity.Project != expectedProject || identity.Region != config.Snapshot.Region || identity.Model != prepared.UpstreamModel {
		return nil, raw, &RequestError{Err: errors.New("invalid Vertex AI Veo operation name"), Dispatched: true}
	}
	return &Task{ProviderTaskID: parsed.Name, Status: StatusProcessing}, raw, nil
}

func (client *Client) Fetch(ctx context.Context, config Config, modelName, operationName string) (*Task, []byte, error) {
	identity, err := parseOperationName(operationName)
	if err != nil || !IsModel(modelName) || identity.Model != modelName || identity.Region != config.Snapshot.Region {
		return nil, nil, errors.New("invalid Vertex AI Veo fetch parameters")
	}
	adapter, meta, err := client.requestAdapter(config, modelName)
	if err != nil {
		return nil, nil, err
	}
	meta.Context = contextOrBackground(ctx)
	endpoint, err := adapter.GetTaskRequestURL(meta, "fetchPredictOperation", identity.Project)
	if err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(fetchOperationPayload{OperationName: operationName})
	if err != nil {
		return nil, nil, errors.New("encode Vertex AI Veo fetch request")
	}
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("create Vertex AI Veo fetch request")
	}
	if err := adapter.SetupRequestHeader(request, meta); err != nil {
		return nil, nil, err
	}
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Vertex AI Veo fetch transport failed: %w", relaycommon.SanitizeTransportError(err))
	}
	defer response.Body.Close()
	raw, readErr := readResponse(response)
	if readErr != nil {
		return nil, raw, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, raw, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	var parsed operationResponse
	if strictDecode(raw, &parsed) != nil {
		return nil, raw, errors.New("invalid Vertex AI Veo fetch response")
	}
	if parsed.Name != "" && parsed.Name != operationName {
		return nil, raw, errors.New("Vertex AI Veo operation name does not match the requested task")
	}
	if parsed.Error != nil {
		task := failedTask(parsed.Error)
		task.ProviderTaskID = operationName
		return task, raw, nil
	}
	if !parsed.Done {
		return &Task{ProviderTaskID: operationName, Status: StatusProcessing}, raw, nil
	}
	if parsed.Response.RAIMediaFilteredCount > 0 {
		return &Task{ProviderTaskID: operationName, Status: StatusFailed,
			ErrorCode: "content_filtered", ErrorMessage: "Vertex AI Veo output was filtered"}, raw, nil
	}
	video, err := extractInlineVideo(&parsed)
	if err != nil {
		return &Task{ProviderTaskID: operationName, Status: StatusFailed,
			ErrorCode: "invalid_output", ErrorMessage: "Vertex AI Veo returned an invalid video result"}, raw, nil
	}
	return &Task{ProviderTaskID: operationName, Status: StatusSucceeded, InlineVideo: video}, raw, nil
}

func (client *Client) requestAdapter(config Config, modelName string) (*vertexcore.Adaptor, *relaycommon.Meta, error) {
	if err := ValidateSnapshot(config.Snapshot, modelName); err != nil {
		return nil, nil, err
	}
	if err := vertexcore.ValidateTaskChannelSettings(config.OtherSettings); err != nil {
		return nil, nil, err
	}
	meta := &relaycommon.Meta{
		Context: context.Background(), Channel: &model.Channel{
			Type: int(constant.ChannelTypeVertexAi), Other: config.Snapshot.Region, OtherSettings: config.OtherSettings,
		},
		Mode: constant.RelayModeChatCompletions, OriginalModelName: modelName,
		ModelName: modelName, BaseURL: config.Snapshot.BaseURL, APIKey: config.Credential,
		Request: &protocolkit.GeneralOpenAIRequest{},
	}
	adapter := &vertexcore.Adaptor{}
	adapter.Init(meta)
	if client != nil && client.TokenProvider != nil {
		adapter.SetTaskTokenProvider(client.TokenProvider)
	}
	return adapter, meta, nil
}

type fetchOperationPayload struct {
	OperationName string `json:"operationName"`
}

type preparedRequestEnvelope struct {
	Parameters struct {
		StorageURI string `json:"storageUri"`
	} `json:"parameters"`
}

// ValidatePrepared applies Vertex-specific pure request checks before quota
// reservation. It performs no credential parsing, token exchange, or I/O.
func ValidatePrepared(prepared *geminitask.PreparedRequest) error {
	if prepared == nil || len(prepared.Body) == 0 || !IsModel(prepared.UpstreamModel) {
		return errors.New("invalid Vertex AI Veo submit parameters")
	}
	return validatePreparedRequest(prepared.Body)
}

func validatePreparedRequest(raw []byte) error {
	if len(raw) == 0 || len(raw) > geminitask.MaxRequestBodyBytes {
		return errors.New("Vertex AI Veo request is invalid")
	}
	var request preparedRequestEnvelope
	if strictDecode(raw, &request) != nil {
		return errors.New("Vertex AI Veo request is invalid")
	}
	// The durable Vertex lifecycle owns inline output. Accepting storageUri
	// would ask Google to place the only result in a caller-controlled bucket,
	// which this gateway cannot durably acquire or authenticate.
	if strings.TrimSpace(request.Parameters.StorageURI) != "" {
		return errors.New("Vertex AI Veo storageUri output is unsupported")
	}
	return nil
}

type providerError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

type operationVideo struct {
	MIMEType           string `json:"mimeType"`
	BytesBase64Encoded string `json:"bytesBase64Encoded"`
	Encoding           string `json:"encoding"`
}

type operationResponse struct {
	Name     string         `json:"name"`
	Done     bool           `json:"done"`
	Error    *providerError `json:"error"`
	Response struct {
		Type                  string           `json:"@type"`
		RAIMediaFilteredCount int              `json:"raiMediaFilteredCount"`
		Videos                []operationVideo `json:"videos"`
		BytesBase64Encoded    string           `json:"bytesBase64Encoded"`
		Encoding              string           `json:"encoding"`
		Video                 string           `json:"video"`
	} `json:"response"`
}

type operationIdentity struct {
	Project string
	Region  string
	Model   string
	ID      string
}

// ValidateOperationName validates Google's canonical Vertex publisher-model
// operation identity without consulting credentials or performing I/O.
func ValidateOperationName(value, expectedModel string) error {
	identity, err := parseOperationName(value)
	if err != nil {
		return err
	}
	if expectedModel != "" && identity.Model != expectedModel {
		return errors.New("Vertex AI Veo operation model does not match the task")
	}
	return nil
}

// ValidateOperationForSnapshot additionally fences an operation to the frozen
// task region. Credential-project equality remains enforced by Client.Fetch.
func ValidateOperationForSnapshot(value string, snapshot Snapshot, expectedModel string) error {
	if err := ValidateSnapshot(snapshot, expectedModel); err != nil {
		return err
	}
	identity, err := parseOperationName(value)
	if err != nil {
		return err
	}
	if identity.Region != snapshot.Region || expectedModel == "" || identity.Model != expectedModel {
		return errors.New("Vertex AI Veo operation does not match the routing snapshot")
	}
	return nil
}

func parseOperationName(value string) (operationIdentity, error) {
	if value == "" || strings.TrimSpace(value) != value || len(value) > MaxOperationNameBytes ||
		!utf8.ValidString(value) || strings.Contains(value, "..") || strings.ContainsAny(value, "\\?#&%\r\n\x00") {
		return operationIdentity{}, errors.New("invalid Vertex AI Veo operation name")
	}
	parts := strings.Split(value, "/")
	if len(parts) != 10 || parts[0] != "projects" || parts[2] != "locations" ||
		parts[4] != "publishers" || parts[5] != "google" || parts[6] != "models" || parts[8] != "operations" {
		return operationIdentity{}, errors.New("invalid Vertex AI Veo operation name")
	}
	for _, index := range []int{1, 3, 7, 9} {
		if !operationComponentPattern.MatchString(parts[index]) {
			return operationIdentity{}, errors.New("invalid Vertex AI Veo operation name")
		}
	}
	if !IsModel(parts[7]) {
		return operationIdentity{}, errors.New("invalid Vertex AI Veo operation model")
	}
	return operationIdentity{Project: parts[1], Region: parts[3], Model: parts[7], ID: parts[9]}, nil
}

func extractInlineVideo(operation *operationResponse) (*InlineVideo, error) {
	if operation == nil {
		return nil, errors.New("Vertex AI Veo operation is nil")
	}
	response := &operation.Response
	if len(response.Videos) > 1 {
		return nil, errors.New("Vertex AI Veo returned multiple video results")
	}
	encoded, mediaType, representations := "", "", 0
	if len(response.Videos) == 1 && strings.TrimSpace(response.Videos[0].BytesBase64Encoded) != "" {
		encoded = response.Videos[0].BytesBase64Encoded
		mediaType = response.Videos[0].MIMEType
		if mediaType == "" {
			mediaType = response.Videos[0].Encoding
		}
		representations++
	}
	if strings.TrimSpace(response.BytesBase64Encoded) != "" {
		encoded, mediaType = response.BytesBase64Encoded, response.Encoding
		representations++
	}
	if strings.TrimSpace(response.Video) != "" {
		encoded, mediaType = response.Video, response.Encoding
		representations++
	}
	if representations != 1 {
		return nil, errors.New("Vertex AI Veo response must contain exactly one inline video")
	}
	return validateInlineVideo(mediaType, encoded)
}

func validateInlineVideo(mediaType, encoded string) (*InlineVideo, error) {
	encoded = strings.TrimSpace(encoded)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || mediaType == "mp4" {
		mediaType = "video/mp4"
	} else if !strings.Contains(mediaType, "/") {
		mediaType = "video/" + mediaType
	}
	if mediaType != "video/mp4" || encoded == "" ||
		len(encoded) > base64.StdEncoding.EncodedLen(MaxInlineVideoBytes) || strings.ContainsAny(encoded, " \t\r\n") {
		return nil, errors.New("Vertex AI Veo inline video is invalid or too large")
	}
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
	head := make([]byte, 512)
	headBytes, readErr := io.ReadFull(decoder, head)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, errors.New("Vertex AI Veo inline video is invalid")
	}
	total := int64(headBytes)
	remaining, err := io.Copy(io.Discard, io.LimitReader(decoder, int64(MaxInlineVideoBytes)+1-total))
	if err != nil {
		return nil, errors.New("Vertex AI Veo inline video is invalid")
	}
	total += remaining
	if total <= 0 || total > MaxInlineVideoBytes || http.DetectContentType(head[:headBytes]) != mediaType {
		return nil, errors.New("Vertex AI Veo inline video MIME type is invalid")
	}
	return &InlineVideo{MIMEType: mediaType, Base64: encoded, DecodedBytes: total}, nil
}

func failedTask(providerErr *providerError) *Task {
	code := "provider_error"
	message := "Vertex AI Veo provider rejected the request"
	if providerErr != nil {
		if candidate := strings.TrimSpace(providerErr.Status); validProviderText(candidate, 256, true) && candidate != "" {
			code = candidate
		}
		if candidate := strings.TrimSpace(providerErr.Message); validProviderText(candidate, MaxProviderMessageBytes, true) && candidate != "" {
			message = candidate
		}
	}
	return &Task{Status: StatusFailed, ErrorCode: code, ErrorMessage: message}
}

func validProviderText(value string, maximum int, emptyOK bool) bool {
	if (!emptyOK && value == "") || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || (unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t') {
			return false
		}
	}
	return true
}

func readResponse(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("Vertex AI Veo response is nil")
	}
	limit := int64(MaxResponseBodyBytes)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: err}
	}
	return raw, nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func strictDecode(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 || len(raw) > MaxResponseBodyBytes || !utf8.Valid(raw) {
		return errors.New("invalid Vertex AI Veo JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	if err := walkJSON(decoder, 0, &nodes); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return errors.New("Vertex AI Veo JSON has trailing data")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Vertex AI Veo JSON has trailing data")
	}
	return nil
}

func walkJSON(decoder *json.Decoder, depth int, nodes *int) error {
	if depth > maxJSONDepth {
		return errors.New("Vertex AI Veo JSON nesting is too deep")
	}
	*nodes++
	if *nodes > maxJSONNodes {
		return errors.New("Vertex AI Veo JSON contains too many values")
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
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || len(key) > 1024 {
				return errors.New("Vertex AI Veo JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("Vertex AI Veo JSON contains a duplicate object key")
			}
			seen[key] = struct{}{}
			if err := walkJSON(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid Vertex AI Veo JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid Vertex AI Veo JSON array")
		}
	default:
		return errors.New("invalid Vertex AI Veo JSON delimiter")
	}
	return nil
}
