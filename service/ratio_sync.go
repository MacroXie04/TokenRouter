package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	ratioSyncDefaultTimeoutSeconds = 10
	ratioSyncMaxTimeoutSeconds     = 30
	ratioSyncMaxUpstreams          = 8
	ratioSyncMaxURLBytes           = 2048
	ratioSyncMaxNameBytes          = 128
	ratioSyncMaxModelNameBytes     = 256
	ratioSyncMaxStringValueBytes   = 4096
	ratioSyncMaxCredentialBytes    = 8192
	ratioSyncMaxNumericValue       = 1e12
	ratioSyncMaxEntries            = 20_000
	ratioSyncMaxResponseBytes      = int64(10 << 20)
	ratioSyncMaxOutputBytes        = 16 << 20
	ratioSyncMaxRetries            = 3
	ratioSyncDefaultEndpoint       = "/api/pricing"
	ratioSyncOpenRouterEndpoint    = "openrouter"
	ratioSyncOfficialPresetID      = -100
	ratioSyncModelsDevPresetID     = -101
	ratioSyncModelsDevHost         = "models.dev"
	ratioSyncModelsDevPath         = "/api.json"
)

const (
	ratioSyncOfficialPresetName  = "官方倍率预设"
	ratioSyncOfficialPresetURL   = "https://basellm.github.io"
	ratioSyncModelsDevPresetName = "models.dev 价格预设"
	ratioSyncModelsDevPresetURL  = "https://models.dev"
)

var (
	ErrRatioSyncInvalidRequest = errors.New("invalid ratio sync request")
	ErrRatioSyncNoUpstreams    = errors.New("no valid ratio sync upstreams")
	ErrRatioSyncChannelQuery   = errors.New("ratio sync channel query failed")
	ErrRatioSyncOutputTooLarge = errors.New("ratio sync output too large")
)

// RatioSyncChannel is the secret-free channel summary returned to the root
// dashboard. API keys and channel configuration are deliberately never loaded
// by ListRatioSyncChannels.
type RatioSyncChannel struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Status  int    `json:"status"`
	Type    int    `json:"type"`
}

// RatioSyncUpstream identifies one pricing endpoint selected by the operator.
type RatioSyncUpstream struct {
	ID       int    `json:"id,omitempty"`
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Endpoint string `json:"endpoint"`
}

// RatioSyncRequest accepts either explicit upstream descriptions or stored
// channel IDs. Explicit upstreams take precedence, matching the dashboard
// contract.
type RatioSyncRequest struct {
	ChannelIDs []int               `json:"channel_ids"`
	Upstreams  []RatioSyncUpstream `json:"upstreams"`
	Timeout    int                 `json:"timeout"`
}

type RatioSyncTestResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type RatioSyncDifferenceItem struct {
	Current    any             `json:"current"`
	Upstreams  map[string]any  `json:"upstreams"`
	Confidence map[string]bool `json:"confidence"`
}

type RatioSyncData struct {
	Differences map[string]map[string]RatioSyncDifferenceItem `json:"differences"`
	TestResults []RatioSyncTestResult                         `json:"test_results"`
}

var ratioSyncFields = []string{
	"model_ratio",
	"completion_ratio",
	"cache_ratio",
	"create_cache_ratio",
	"image_ratio",
	"audio_ratio",
	"audio_completion_ratio",
	"model_price",
	"billing_mode",
	"billing_expr",
}

var ratioSyncNumericFields = map[string]bool{
	"model_ratio":            true,
	"completion_ratio":       true,
	"cache_ratio":            true,
	"create_cache_ratio":     true,
	"image_ratio":            true,
	"audio_ratio":            true,
	"audio_completion_ratio": true,
	"model_price":            true,
}

// ratioSyncHTTPClient resolves and connects directly through SafeDialContext.
// Proxy is intentionally nil: allowing an environment proxy to resolve the
// operator-provided host would bypass dial-time SSRF validation. Redirects are
// refused because OpenRouter requests may carry a channel credential.
var ratioSyncHTTPClient = &http.Client{
	Timeout: ratioSyncMaxTimeoutSeconds * time.Second,
	Transport: &http.Transport{
		DialContext:           common.SafeDialContext,
		ResponseHeaderTimeout: ratioSyncMaxTimeoutSeconds * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type ratioSyncDBChannel struct {
	ID       int
	Type     int
	Name     string
	Status   int
	BaseURL  string
	Priority *int64
}

func (ratioSyncDBChannel) TableName() string { return model.Channel{}.TableName() }

// ListRatioSyncChannels lists stored channels with an effective base URL and
// appends the two stable built-in presets. The narrow SELECT is an additional
// guard against accidentally serializing credentials.
func ListRatioSyncChannels(ctx context.Context) ([]RatioSyncChannel, error) {
	var rows []ratioSyncDBChannel
	if err := model.DB.WithContext(ctx).
		Select("id", "type", "name", "status", "base_url", "priority").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRatioSyncChannelQuery, err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := int64(0), int64(0)
		if rows[i].Priority != nil {
			left = *rows[i].Priority
		}
		if rows[j].Priority != nil {
			right = *rows[j].Priority
		}
		if left != right {
			return left > right
		}
		return rows[i].ID < rows[j].ID
	})

	channels := make([]RatioSyncChannel, 0, len(rows)+2)
	for _, row := range rows {
		baseURL := effectiveRatioSyncBaseURL(row.Type, row.BaseURL)
		if !safeRatioSyncListingURL(baseURL) {
			continue
		}
		channels = append(channels, RatioSyncChannel{
			ID: row.ID, Name: row.Name, BaseURL: baseURL, Status: row.Status, Type: row.Type,
		})
	}
	channels = append(channels,
		RatioSyncChannel{
			ID: ratioSyncOfficialPresetID, Name: ratioSyncOfficialPresetName,
			BaseURL: ratioSyncOfficialPresetURL, Status: constant.ChannelStatusEnabled,
		},
		RatioSyncChannel{
			ID: ratioSyncModelsDevPresetID, Name: ratioSyncModelsDevPresetName,
			BaseURL: ratioSyncModelsDevPresetURL, Status: constant.ChannelStatusEnabled,
		},
	)
	return channels, nil
}

func safeRatioSyncListingURL(rawURL string) bool {
	if rawURL == "" || len(rawURL) > ratioSyncMaxURLBytes {
		return false
	}
	parsed, err := url.Parse(rawURL)
	return err == nil && validRatioSyncAbsoluteURL(parsed) && parsed.RawQuery == "" && parsed.Fragment == ""
}

func effectiveRatioSyncBaseURL(channelType int, configured string) string {
	baseURL := strings.TrimSpace(configured)
	if baseURL == "" && channelType >= 0 && channelType < len(constant.ChannelBaseURLs) {
		baseURL = constant.ChannelBaseURLs[channelType]
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

type ratioSyncFetchResult struct {
	index int
	name  string
	data  map[string]any
	err   string
}

type ratioSyncSuccessfulUpstream struct {
	name string
	data map[string]any
}

// FetchRatioSyncData fetches, validates, normalizes, and compares the selected
// upstream pricing documents. Individual upstream failures remain in
// test_results; request/DB/output-bound failures are returned to the caller.
func FetchRatioSyncData(ctx context.Context, request RatioSyncRequest) (*RatioSyncData, error) {
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = ratioSyncDefaultTimeoutSeconds
	}
	if timeout > ratioSyncMaxTimeoutSeconds {
		timeout = ratioSyncMaxTimeoutSeconds
	}

	upstreams, err := resolveRatioSyncUpstreams(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(upstreams) == 0 {
		return nil, ErrRatioSyncNoUpstreams
	}

	results := make(chan ratioSyncFetchResult, len(upstreams))
	semaphore := make(chan struct{}, ratioSyncMaxUpstreams)
	var waitGroup sync.WaitGroup
	for index := range upstreams {
		index := index
		upstream := upstreams[index]
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- ratioSyncFetchResult{index: index, name: ratioSyncLabel(upstream), err: "request canceled"}
				return
			}
			results <- fetchRatioSyncUpstream(ctx, index, upstream, time.Duration(timeout)*time.Second)
		}()
	}
	waitGroup.Wait()
	close(results)

	ordered := make([]ratioSyncFetchResult, len(upstreams))
	for result := range results {
		ordered[result.index] = result
	}
	testResults := make([]RatioSyncTestResult, 0, len(ordered))
	successful := make([]ratioSyncSuccessfulUpstream, 0, len(ordered))
	for _, result := range ordered {
		if result.err != "" {
			testResults = append(testResults, RatioSyncTestResult{Name: result.name, Status: "error", Error: result.err})
			continue
		}
		testResults = append(testResults, RatioSyncTestResult{Name: result.name, Status: "success"})
		successful = append(successful, ratioSyncSuccessfulUpstream{name: result.name, data: result.data})
	}

	data := &RatioSyncData{
		Differences: buildRatioSyncDifferences(localRatioSyncData(), successful),
		TestResults: testResults,
	}
	encoded, err := common.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode ratio sync result: %w", err)
	}
	if len(encoded) > ratioSyncMaxOutputBytes {
		return nil, ErrRatioSyncOutputTooLarge
	}
	return data, nil
}

func resolveRatioSyncUpstreams(ctx context.Context, request RatioSyncRequest) ([]RatioSyncUpstream, error) {
	if len(request.Upstreams) > 0 {
		if len(request.Upstreams) > ratioSyncMaxUpstreams {
			return nil, ErrRatioSyncInvalidRequest
		}
		upstreams := make([]RatioSyncUpstream, 0, len(request.Upstreams))
		labels := make(map[string]struct{}, len(request.Upstreams))
		for _, upstream := range request.Upstreams {
			normalized, err := normalizeRatioSyncUpstream(ctx, upstream)
			if err != nil {
				return nil, ErrRatioSyncInvalidRequest
			}
			label := ratioSyncLabel(normalized)
			if _, exists := labels[label]; exists {
				return nil, ErrRatioSyncInvalidRequest
			}
			labels[label] = struct{}{}
			upstreams = append(upstreams, normalized)
		}
		return upstreams, nil
	}

	if len(request.ChannelIDs) == 0 {
		return nil, nil
	}
	if len(request.ChannelIDs) > ratioSyncMaxUpstreams {
		return nil, ErrRatioSyncInvalidRequest
	}
	ids := make([]int, 0, len(request.ChannelIDs))
	seen := make(map[int]struct{}, len(request.ChannelIDs))
	for _, id := range request.ChannelIDs {
		if id <= 0 {
			return nil, ErrRatioSyncInvalidRequest
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var channels []model.Channel
	if err := model.DB.WithContext(ctx).
		Select("id", "type", "name", "status", "base_url").
		Where("id IN ?", ids).
		Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRatioSyncChannelQuery, err)
	}
	byID := make(map[int]model.Channel, len(channels))
	for _, channel := range channels {
		byID[channel.Id] = channel
	}
	upstreams := make([]RatioSyncUpstream, 0, len(channels))
	for _, id := range ids {
		channel, exists := byID[id]
		if !exists {
			continue
		}
		baseURL := effectiveRatioSyncBaseURL(channel.Type, channel.BaseURL)
		if baseURL == "" {
			continue
		}
		upstream := RatioSyncUpstream{
			ID: id, Name: channel.Name, BaseURL: baseURL, Endpoint: ratioSyncDefaultEndpoint,
		}
		if _, err := ratioSyncTargetURL(upstream); err != nil {
			continue
		}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, nil
}

func normalizeRatioSyncUpstream(ctx context.Context, upstream RatioSyncUpstream) (RatioSyncUpstream, error) {
	upstream.Name = strings.TrimSpace(upstream.Name)
	upstream.BaseURL = strings.TrimRight(strings.TrimSpace(upstream.BaseURL), "/")
	upstream.Endpoint = strings.TrimSpace(upstream.Endpoint)
	if upstream.Endpoint == "" {
		upstream.Endpoint = ratioSyncDefaultEndpoint
	}
	if !validRatioSyncText(upstream.Name, ratioSyncMaxNameBytes) ||
		len(upstream.BaseURL) > ratioSyncMaxURLBytes || len(upstream.Endpoint) > ratioSyncMaxURLBytes {
		return RatioSyncUpstream{}, ErrRatioSyncInvalidRequest
	}

	if upstream.Endpoint == ratioSyncOpenRouterEndpoint && upstream.ID > 0 {
		var channel model.Channel
		if err := model.DB.WithContext(ctx).First(&channel, upstream.ID).Error; err != nil {
			return RatioSyncUpstream{}, ErrRatioSyncInvalidRequest
		}
		if constant.ChannelType(channel.Type) != constant.ChannelTypeOpenRouter {
			return RatioSyncUpstream{}, ErrRatioSyncInvalidRequest
		}
		storedBaseURL := effectiveRatioSyncBaseURL(channel.Type, channel.BaseURL)
		if storedBaseURL == "" {
			return RatioSyncUpstream{}, ErrRatioSyncInvalidRequest
		}
		// A stored credential may only be sent to its stored channel endpoint.
		// Ignore the client copy of base_url/name, which may be stale or hostile.
		upstream.BaseURL = storedBaseURL
		upstream.Name = channel.Name
	}
	if _, err := ratioSyncTargetURL(upstream); err != nil {
		return RatioSyncUpstream{}, ErrRatioSyncInvalidRequest
	}
	return upstream, nil
}

func validRatioSyncText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func ratioSyncLabel(upstream RatioSyncUpstream) string {
	if upstream.ID != 0 {
		return fmt.Sprintf("%s(%d)", upstream.Name, upstream.ID)
	}
	return upstream.Name
}

func ratioSyncTargetURL(upstream RatioSyncUpstream) (string, error) {
	base, err := url.Parse(upstream.BaseURL)
	if err != nil || !validRatioSyncAbsoluteURL(base) || base.RawQuery != "" || base.Fragment != "" {
		return "", ErrRatioSyncInvalidRequest
	}
	if upstream.Endpoint == ratioSyncOpenRouterEndpoint {
		base.Path = strings.TrimRight(base.Path, "/") + "/v1/models"
		base.RawPath = ""
		return base.String(), nil
	}

	endpoint, err := url.Parse(upstream.Endpoint)
	if err != nil {
		return "", ErrRatioSyncInvalidRequest
	}
	if endpoint.IsAbs() {
		if !validRatioSyncAbsoluteURL(endpoint) {
			return "", ErrRatioSyncInvalidRequest
		}
		return endpoint.String(), nil
	}
	if endpoint.Host != "" || endpoint.User != nil || endpoint.Fragment != "" {
		return "", ErrRatioSyncInvalidRequest
	}
	path := endpoint.Path
	if path == "" {
		path = ratioSyncDefaultEndpoint
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawPath = ""
	base.RawQuery = endpoint.RawQuery
	return base.String(), nil
}

func validRatioSyncAbsoluteURL(parsed *url.URL) bool {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	if len(parsed.String()) > ratioSyncMaxURLBytes {
		return false
	}
	return true
}

func fetchRatioSyncUpstream(parent context.Context, index int, upstream RatioSyncUpstream, timeout time.Duration) ratioSyncFetchResult {
	result := ratioSyncFetchResult{index: index, name: ratioSyncLabel(upstream)}
	targetURL, err := ratioSyncTargetURL(upstream)
	if err != nil {
		result.err = "invalid upstream URL"
		return result
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var credential string
	if upstream.Endpoint == ratioSyncOpenRouterEndpoint {
		parsedTarget, _ := url.Parse(targetURL)
		if parsedTarget == nil || (parsedTarget.Scheme != "https" && !common.SSRFDisabled()) {
			result.err = "OpenRouter destination must use HTTPS"
			return result
		}
		if upstream.ID <= 0 {
			result.err = "OpenRouter credential unavailable"
			return result
		}
		var channel model.Channel
		if err := model.DB.WithContext(ctx).First(&channel, upstream.ID).Error; err != nil ||
			constant.ChannelType(channel.Type) != constant.ChannelTypeOpenRouter {
			result.err = "OpenRouter channel unavailable"
			return result
		}
		if effectiveRatioSyncBaseURL(channel.Type, channel.BaseURL) != upstream.BaseURL {
			result.err = "OpenRouter channel changed; refresh and retry"
			return result
		}
		credential = strings.TrimSpace(GetChannelKey(&channel))
		if credential == "" || len(credential) > ratioSyncMaxCredentialBytes ||
			strings.IndexFunc(credential, unicode.IsControl) >= 0 {
			result.err = "OpenRouter channel has no enabled credential"
			return result
		}
	}

	var body []byte
	for attempt := 0; attempt < ratioSyncMaxRetries; attempt++ {
		request, buildErr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if buildErr != nil {
			result.err = "invalid upstream URL"
			return result
		}
		request.Header.Set("Accept", "application/json")
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}

		response, requestErr := ratioSyncHTTPClient.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					result.err = "request timed out"
				} else {
					result.err = "request canceled"
				}
				return result
			}
			if attempt+1 < ratioSyncMaxRetries {
				if !waitRatioSyncRetry(ctx, attempt) {
					result.err = "request canceled"
					return result
				}
				continue
			}
			result.err = "upstream request failed"
			return result
		}

		body, err = readRatioSyncResponse(response)
		if err != nil {
			result.err = err.Error()
			return result
		}
		break
	}

	var data map[string]any
	switch {
	case upstream.Endpoint == ratioSyncOpenRouterEndpoint:
		data, err = parseOpenRouterRatioSync(body)
	case isModelsDevRatioSyncURL(targetURL):
		data, err = parseModelsDevRatioSync(body)
	default:
		data, err = parseStandardRatioSync(body)
	}
	if err != nil {
		result.err = "invalid upstream pricing data"
		return result
	}
	result.data = data
	return result
}

func waitRatioSyncRetry(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(time.Duration(50*(1<<attempt)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func readRatioSyncResponse(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("upstream request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "json") {
		return nil, errors.New("upstream did not return JSON")
	}
	body, err := common.ReadAllLimited(response.Body, ratioSyncMaxResponseBytes)
	if err != nil {
		if errors.Is(err, common.ErrBodyTooLarge) {
			return nil, errors.New("upstream response too large")
		}
		return nil, errors.New("upstream response could not be read")
	}
	return body, nil
}

type ratioSyncEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

func parseStandardRatioSync(body []byte) (map[string]any, error) {
	var envelope ratioSyncEnvelope
	if err := common.Unmarshal(body, &envelope); err != nil || !envelope.Success {
		return nil, errors.New("invalid upstream envelope")
	}
	if len(envelope.Data) == 0 {
		envelope.Data = []byte("null")
	}

	var rawFields map[string]json.RawMessage
	if err := common.Unmarshal(envelope.Data, &rawFields); err == nil {
		for _, field := range ratioSyncFields {
			if _, exists := rawFields[field]; exists {
				return normalizeRatioSyncFieldMap(rawFields)
			}
		}
	}

	var items []ratioSyncPricingItem
	if err := common.Unmarshal(envelope.Data, &items); err != nil {
		return nil, err
	}
	return pricingItemsToRatioSyncData(items)
}

func normalizeRatioSyncFieldMap(rawFields map[string]json.RawMessage) (map[string]any, error) {
	data := make(map[string]any)
	entryCount := 0
	for _, field := range ratioSyncFields {
		raw, exists := rawFields[field]
		if !exists {
			continue
		}
		if ratioSyncNumericFields[field] {
			values, err := decodeRatioSyncNumericMap(raw)
			if err != nil {
				return nil, err
			}
			entryCount += len(values)
			if entryCount > ratioSyncMaxEntries {
				return nil, errors.New("too many pricing entries")
			}
			normalized := make(map[string]any, len(values))
			for modelName, value := range values {
				if !validRatioSyncModelName(modelName) || !validRatioSyncNumber(value) {
					return nil, errors.New("invalid numeric pricing value")
				}
				normalized[modelName] = value
			}
			data[field] = normalized
			continue
		}
		values, err := decodeRatioSyncStringMap(raw)
		if err != nil {
			return nil, err
		}
		entryCount += len(values)
		if entryCount > ratioSyncMaxEntries {
			return nil, errors.New("too many pricing entries")
		}
		normalized := make(map[string]any, len(values))
		for modelName, value := range values {
			if !validRatioSyncModelName(modelName) || len(value) > ratioSyncMaxStringValueBytes || !utf8.ValidString(value) {
				return nil, errors.New("invalid string pricing value")
			}
			normalized[modelName] = value
		}
		data[field] = normalized
	}
	return data, nil
}

func decodeRatioSyncNumericMap(raw []byte) (map[string]float64, error) {
	var encoded map[string]json.RawMessage
	if err := common.Unmarshal(raw, &encoded); err != nil || encoded == nil {
		return nil, errors.New("pricing field must be an object")
	}
	values := make(map[string]float64, len(encoded))
	for modelName, valueRaw := range encoded {
		if common.GetJsonType(valueRaw) != "number" {
			return nil, errors.New("pricing value must be numeric")
		}
		var value float64
		if err := common.Unmarshal(valueRaw, &value); err != nil || !validRatioSyncNumber(value) {
			return nil, errors.New("invalid numeric pricing value")
		}
		values[modelName] = value
	}
	return values, nil
}

func decodeRatioSyncStringMap(raw []byte) (map[string]string, error) {
	var encoded map[string]json.RawMessage
	if err := common.Unmarshal(raw, &encoded); err != nil || encoded == nil {
		return nil, errors.New("pricing field must be an object")
	}
	values := make(map[string]string, len(encoded))
	for modelName, valueRaw := range encoded {
		if common.GetJsonType(valueRaw) != "string" {
			return nil, errors.New("pricing value must be a string")
		}
		var value string
		if err := common.Unmarshal(valueRaw, &value); err != nil {
			return nil, errors.New("invalid string pricing value")
		}
		values[modelName] = value
	}
	return values, nil
}

type ratioSyncPricingItem struct {
	ModelName            string   `json:"model_name"`
	QuotaType            int      `json:"quota_type"`
	ModelRatio           float64  `json:"model_ratio"`
	ModelPrice           float64  `json:"model_price"`
	CompletionRatio      float64  `json:"completion_ratio"`
	CacheRatio           *float64 `json:"cache_ratio"`
	CreateCacheRatio     *float64 `json:"create_cache_ratio"`
	ImageRatio           *float64 `json:"image_ratio"`
	AudioRatio           *float64 `json:"audio_ratio"`
	AudioCompletionRatio *float64 `json:"audio_completion_ratio"`
	BillingMode          string   `json:"billing_mode"`
	BillingExpr          string   `json:"billing_expr"`
}

func pricingItemsToRatioSyncData(items []ratioSyncPricingItem) (map[string]any, error) {
	if len(items) > ratioSyncMaxEntries {
		return nil, errors.New("too many pricing items")
	}
	fields := make(map[string]map[string]any)
	put := func(field, modelName string, value any) {
		if fields[field] == nil {
			fields[field] = make(map[string]any)
		}
		fields[field][modelName] = value
	}
	for _, item := range items {
		if item.ModelName == "" {
			continue
		}
		if !validRatioSyncModelName(item.ModelName) {
			return nil, errors.New("invalid model name")
		}
		if !validRatioSyncNumber(item.ModelRatio) || !validRatioSyncNumber(item.ModelPrice) ||
			!validRatioSyncNumber(item.CompletionRatio) || !validOptionalRatioSyncNumber(item.CacheRatio) ||
			!validOptionalRatioSyncNumber(item.CreateCacheRatio) || !validOptionalRatioSyncNumber(item.ImageRatio) ||
			!validOptionalRatioSyncNumber(item.AudioRatio) || !validOptionalRatioSyncNumber(item.AudioCompletionRatio) {
			return nil, errors.New("invalid pricing number")
		}
		if len(item.BillingMode) > ratioSyncMaxStringValueBytes || len(item.BillingExpr) > ratioSyncMaxStringValueBytes {
			return nil, errors.New("invalid billing metadata")
		}
		if item.QuotaType == 1 {
			put("model_price", item.ModelName, item.ModelPrice)
		} else {
			put("model_ratio", item.ModelName, item.ModelRatio)
			put("completion_ratio", item.ModelName, item.CompletionRatio)
		}
		for field, value := range map[string]*float64{
			"cache_ratio": item.CacheRatio, "create_cache_ratio": item.CreateCacheRatio,
			"image_ratio": item.ImageRatio, "audio_ratio": item.AudioRatio,
			"audio_completion_ratio": item.AudioCompletionRatio,
		} {
			if value != nil {
				put(field, item.ModelName, *value)
			}
		}
		if item.BillingMode == BillingModeTieredExpr && strings.TrimSpace(item.BillingExpr) != "" {
			put("billing_mode", item.ModelName, BillingModeTieredExpr)
			put("billing_expr", item.ModelName, item.BillingExpr)
		}
	}
	data := make(map[string]any, len(fields))
	for field, values := range fields {
		if len(values) > 0 {
			data[field] = values
		}
	}
	return data, nil
}

type ratioSyncOpenRouterResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Pricing struct {
			Prompt         string `json:"prompt"`
			Completion     string `json:"completion"`
			InputCacheRead string `json:"input_cache_read"`
		} `json:"pricing"`
	} `json:"data"`
}

func parseOpenRouterRatioSync(body []byte) (map[string]any, error) {
	var response ratioSyncOpenRouterResponse
	if err := common.Unmarshal(body, &response); err != nil || len(response.Data) > ratioSyncMaxEntries {
		return nil, errors.New("invalid OpenRouter pricing response")
	}
	modelRatios := make(map[string]any)
	completionRatios := make(map[string]any)
	cacheRatios := make(map[string]any)
	for _, entry := range response.Data {
		if !validRatioSyncModelName(entry.ID) {
			continue
		}
		prompt, promptErr := strconv.ParseFloat(entry.Pricing.Prompt, 64)
		completion, completionErr := strconv.ParseFloat(entry.Pricing.Completion, 64)
		if promptErr != nil && completionErr != nil {
			continue
		}
		if promptErr != nil {
			prompt = 0
		}
		if completionErr != nil {
			completion = 0
		}
		if !validRatioSyncNumber(prompt) || !validRatioSyncNumber(completion) {
			continue
		}
		if prompt == 0 && completion == 0 {
			modelRatios[entry.ID] = float64(0)
			continue
		}
		if prompt <= 0 {
			continue
		}
		// OpenRouter prices are USD per token. The compatibility ratio unit is
		// $0.002 per 1K tokens, or $2 per million tokens.
		modelRatio := roundRatioSync(prompt * float64(common.QuotaPerUnit))
		completionRatio := roundRatioSync(completion / prompt)
		if !validRatioSyncNumber(modelRatio) || !validRatioSyncNumber(completionRatio) {
			continue
		}
		modelRatios[entry.ID] = modelRatio
		completionRatios[entry.ID] = completionRatio
		if cachePrice, parseErr := strconv.ParseFloat(entry.Pricing.InputCacheRead, 64); parseErr == nil &&
			validRatioSyncNumber(cachePrice) {
			cacheRatio := roundRatioSync(cachePrice / prompt)
			if validRatioSyncNumber(cacheRatio) {
				cacheRatios[entry.ID] = cacheRatio
			}
		}
	}
	data := make(map[string]any)
	if len(modelRatios) > 0 {
		data["model_ratio"] = modelRatios
	}
	if len(completionRatios) > 0 {
		data["completion_ratio"] = completionRatios
	}
	if len(cacheRatios) > 0 {
		data["cache_ratio"] = cacheRatios
	}
	return data, nil
}

type ratioSyncModelsDevProvider struct {
	Models map[string]struct {
		Cost struct {
			Input     *float64 `json:"input"`
			Output    *float64 `json:"output"`
			CacheRead *float64 `json:"cache_read"`
		} `json:"cost"`
	} `json:"models"`
}

type ratioSyncModelsDevCandidate struct {
	provider  string
	input     float64
	output    *float64
	cacheRead *float64
}

func parseModelsDevRatioSync(body []byte) (map[string]any, error) {
	var providers map[string]ratioSyncModelsDevProvider
	if err := common.Unmarshal(body, &providers); err != nil || len(providers) == 0 {
		return nil, errors.New("invalid models.dev response")
	}
	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	candidates := make(map[string]ratioSyncModelsDevCandidate)
	entryCount := 0
	for _, providerName := range providerNames {
		provider := providers[providerName]
		entryCount += len(provider.Models)
		if entryCount > ratioSyncMaxEntries {
			return nil, errors.New("too many models.dev entries")
		}
		modelNames := make([]string, 0, len(provider.Models))
		for modelName := range provider.Models {
			modelNames = append(modelNames, modelName)
		}
		sort.Strings(modelNames)
		for _, modelName := range modelNames {
			if !validRatioSyncModelName(modelName) {
				continue
			}
			cost := provider.Models[modelName].Cost
			if cost.Input == nil || !validRatioSyncNumber(*cost.Input) ||
				!validOptionalRatioSyncNumber(cost.Output) {
				continue
			}
			if *cost.Input == 0 && cost.Output != nil && *cost.Output > 0 {
				continue
			}
			candidate := ratioSyncModelsDevCandidate{
				provider: providerName, input: *cost.Input,
				output: copyRatioSyncFloat(cost.Output), cacheRead: copyRatioSyncFloatIfValid(cost.CacheRead),
			}
			current, exists := candidates[modelName]
			if !exists || replaceModelsDevRatioSyncCandidate(current, candidate) {
				candidates[modelName] = candidate
			}
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("models.dev response has no valid pricing")
	}

	modelRatios := make(map[string]any)
	completionRatios := make(map[string]any)
	cacheRatios := make(map[string]any)
	for modelName, candidate := range candidates {
		if candidate.input == 0 {
			modelRatios[modelName] = float64(0)
			continue
		}
		modelRatio := roundRatioSync(candidate.input * float64(common.QuotaPerUnit) / 1_000_000)
		if !validRatioSyncNumber(modelRatio) {
			continue
		}
		modelRatios[modelName] = modelRatio
		if candidate.output != nil {
			completionRatio := roundRatioSync(*candidate.output / candidate.input)
			if validRatioSyncNumber(completionRatio) {
				completionRatios[modelName] = completionRatio
			}
		}
		if candidate.cacheRead != nil {
			cacheRatio := roundRatioSync(*candidate.cacheRead / candidate.input)
			if validRatioSyncNumber(cacheRatio) {
				cacheRatios[modelName] = cacheRatio
			}
		}
	}
	data := map[string]any{"model_ratio": modelRatios}
	if len(completionRatios) > 0 {
		data["completion_ratio"] = completionRatios
	}
	if len(cacheRatios) > 0 {
		data["cache_ratio"] = cacheRatios
	}
	return data, nil
}

func copyRatioSyncFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyRatioSyncFloatIfValid(value *float64) *float64 {
	if value == nil || !validRatioSyncNumber(*value) {
		return nil
	}
	return copyRatioSyncFloat(value)
}

func replaceModelsDevRatioSyncCandidate(current, candidate ratioSyncModelsDevCandidate) bool {
	currentNonZero := current.input > 0
	candidateNonZero := candidate.input > 0
	if currentNonZero != candidateNonZero {
		return candidateNonZero
	}
	if candidateNonZero && !nearlyEqualRatioSync(current.input, candidate.input) {
		return candidate.input < current.input
	}
	return candidate.provider < current.provider
}

func isModelsDevRatioSyncURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), ratioSyncModelsDevHost) {
		return false
	}
	return strings.TrimSuffix(parsed.Path, "/") == ratioSyncModelsDevPath
}

func validRatioSyncModelName(modelName string) bool {
	return modelName == strings.TrimSpace(modelName) && validRatioSyncText(modelName, ratioSyncMaxModelNameBytes)
}

func validRatioSyncNumber(value float64) bool {
	return value >= 0 && value <= ratioSyncMaxNumericValue && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validOptionalRatioSyncNumber(value *float64) bool {
	return value == nil || validRatioSyncNumber(*value)
}

func roundRatioSync(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func localRatioSyncData() map[string]any {
	data := make(map[string]any)
	modelRatios := make(map[string]any)
	completionRatios := make(map[string]any)
	for modelName, price := range ExportedModelPrices() {
		if !validRatioSyncModelName(modelName) || !validRatioSyncNumber(price.Prompt) ||
			!validRatioSyncNumber(price.Completion) {
			continue
		}
		modelRatio := roundRatioSync(price.Prompt * float64(common.QuotaPerUnit) / 1_000_000)
		if validRatioSyncNumber(modelRatio) {
			modelRatios[modelName] = modelRatio
		}
		if price.Prompt > 0 {
			completionRatio := roundRatioSync(price.Completion / price.Prompt)
			if validRatioSyncNumber(completionRatio) {
				completionRatios[modelName] = completionRatio
			}
		}
	}
	if len(modelRatios) > 0 {
		data["model_ratio"] = modelRatios
	}
	if len(completionRatios) > 0 {
		data["completion_ratio"] = completionRatios
	}

	optionKeys := map[string]string{
		"completion_ratio":       "CompletionRatio",
		"cache_ratio":            "CacheRatio",
		"create_cache_ratio":     "CreateCacheRatio",
		"image_ratio":            "ImageRatio",
		"audio_ratio":            "AudioRatio",
		"audio_completion_ratio": "AudioCompletionRatio",
		"model_price":            "PerCallModelPrice",
		"billing_mode":           "ModelBillingMode",
		"billing_expr":           "ModelBillingExpr",
	}
	for field, optionKey := range optionKeys {
		raw := strings.TrimSpace(setting.GetOption(optionKey))
		if raw == "" {
			continue
		}
		var values map[string]any
		if err := common.Unmarshal([]byte(raw), &values); err != nil {
			continue
		}
		normalized := make(map[string]any, len(values))
		for modelName, value := range values {
			if !validRatioSyncModelName(modelName) {
				continue
			}
			if ratioSyncNumericFields[field] {
				if number, ok := ratioSyncFloat(value); ok && validRatioSyncNumber(number) {
					normalized[modelName] = number
				}
				continue
			}
			if text, ok := value.(string); ok && len(text) <= ratioSyncMaxStringValueBytes {
				normalized[modelName] = text
			}
		}
		if len(normalized) > 0 {
			data[field] = normalized
		}
	}
	return data
}

// ExposedRatioData returns the public ratio-config schema used by compatible
// clients. Every core reference field is present even when its map is empty.
func ExposedRatioData() map[string]any {
	data := localRatioSyncData()
	for _, field := range []string{
		"model_ratio", "completion_ratio", "cache_ratio", "create_cache_ratio", "model_price",
	} {
		if _, exists := data[field]; !exists {
			data[field] = map[string]any{}
		}
	}
	return data
}

func ratioSyncFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func buildRatioSyncDifferences(local map[string]any, upstreams []ratioSyncSuccessfulUpstream) map[string]map[string]RatioSyncDifferenceItem {
	differences := make(map[string]map[string]RatioSyncDifferenceItem)
	allModels := make(map[string]struct{})
	for _, field := range ratioSyncFields {
		for modelName := range ratioSyncValueMap(local[field]) {
			allModels[modelName] = struct{}{}
		}
	}
	for _, upstream := range upstreams {
		for _, field := range ratioSyncFields {
			for modelName := range ratioSyncValueMap(upstream.data[field]) {
				allModels[modelName] = struct{}{}
			}
		}
	}

	confidence := make(map[string]map[string]bool, len(upstreams))
	for _, upstream := range upstreams {
		confidence[upstream.name] = make(map[string]bool, len(allModels))
		modelRatios := ratioSyncValueMap(upstream.data["model_ratio"])
		completionRatios := ratioSyncValueMap(upstream.data["completion_ratio"])
		for modelName := range allModels {
			trusted := true
			if len(modelRatios) > 0 && len(completionRatios) > 0 {
				modelRatio, modelOK := ratioSyncFloat(modelRatios[modelName])
				completionRatio, completionOK := ratioSyncFloat(completionRatios[modelName])
				if modelOK && completionOK && nearlyEqualRatioSync(modelRatio, 37.5) && nearlyEqualRatioSync(completionRatio, 1) {
					trusted = false
				}
			}
			confidence[upstream.name][modelName] = trusted
		}
	}

	for modelName := range allModels {
		for _, field := range ratioSyncFields {
			localValue, localExists := ratioSyncValueMap(local[field])[modelName]
			upstreamValues := make(map[string]any, len(upstreams))
			confidenceValues := make(map[string]bool, len(upstreams))
			hasUpstream, hasDifference := false, false
			for _, upstream := range upstreams {
				upstreamValue, upstreamExists := ratioSyncValueMap(upstream.data[field])[modelName]
				if upstreamExists {
					hasUpstream = true
					if localExists && ratioSyncValuesEqual(localValue, upstreamValue) {
						upstreamValue = "same"
					} else {
						hasDifference = true
					}
				} else if !localExists {
					upstreamValue = "same"
				} else {
					upstreamValue = nil
				}
				upstreamValues[upstream.name] = upstreamValue
				confidenceValues[upstream.name] = confidence[upstream.name][modelName]
			}
			if (localExists && hasDifference) || (!localExists && hasUpstream) {
				if differences[modelName] == nil {
					differences[modelName] = make(map[string]RatioSyncDifferenceItem)
				}
				differences[modelName][field] = RatioSyncDifferenceItem{
					Current: localValue, Upstreams: upstreamValues, Confidence: confidenceValues,
				}
			}
		}
	}

	channelHasDifference := make(map[string]bool)
	for _, fields := range differences {
		for _, item := range fields {
			for channelName, value := range item.Upstreams {
				if value != nil && value != "same" {
					channelHasDifference[channelName] = true
				}
			}
		}
	}
	for modelName, fields := range differences {
		for field, item := range fields {
			for channelName := range item.Upstreams {
				if !channelHasDifference[channelName] {
					delete(item.Upstreams, channelName)
					delete(item.Confidence, channelName)
				}
			}
			allSame := true
			for _, value := range item.Upstreams {
				if value != "same" {
					allSame = false
					break
				}
			}
			if len(item.Upstreams) == 0 || allSame {
				delete(fields, field)
			} else {
				fields[field] = item
			}
		}
		if len(fields) == 0 {
			delete(differences, modelName)
		}
	}
	return differences
}

func ratioSyncValueMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func ratioSyncValuesEqual(left, right any) bool {
	leftNumber, leftOK := ratioSyncFloat(left)
	rightNumber, rightOK := ratioSyncFloat(right)
	if leftOK && rightOK {
		return nearlyEqualRatioSync(leftNumber, rightNumber)
	}
	leftString, leftOK := left.(string)
	rightString, rightOK := right.(string)
	return leftOK && rightOK && leftString == rightString
}

func nearlyEqualRatioSync(left, right float64) bool {
	return math.Abs(left-right) < 1e-9
}
