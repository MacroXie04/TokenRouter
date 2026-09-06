package kling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestModelCatalogAndDefaultTextPayload(t *testing.T) {
	assert.Equal(t, []string{"kling-v1", "kling-v1-6", "kling-v2-master"}, ModelList)

	raw := []byte(`{"model":"kling-v1","prompt":"  a paper bird  "}`)
	model, err := RequestedModel(raw)
	require.NoError(t, err)
	assert.Equal(t, "kling-v1", model)

	prepared, err := PrepareSubmit(raw, model, "vendor-kling")
	require.NoError(t, err)
	assert.Equal(t, ActionTextToVideo, prepared.Action)
	assert.Equal(t, DefaultMode, prepared.Payload.Mode)
	assert.Equal(t, DefaultDuration, prepared.Payload.Duration)
	assert.Equal(t, DefaultCfgScale, prepared.Payload.CfgScale)
	assert.Equal(t, DefaultAspectRatio, prepared.Payload.AspectRatio)
	assert.Equal(t, "vendor-kling", prepared.Payload.Model)
	assert.Equal(t, "vendor-kling", prepared.Payload.ModelName)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(prepared.Body, &wire))
	assert.Equal(t, "  a paper bird  ", wire["prompt"])
	assert.Equal(t, "5", wire["duration"])
	assert.Equal(t, "std", wire["mode"])
	assert.Equal(t, 0.5, wire["cfg_scale"])
	assert.Equal(t, "vendor-kling", wire["model"])
	assert.Equal(t, "vendor-kling", wire["model_name"])
}

func TestPrepareSubmitPreservesNativeFieldsAndProtectsMappedModel(t *testing.T) {
	raw := []byte(`{
		"model":"kling-v1-6",
		"model_name":"kling-v1-6",
		"prompt":"top prompt",
		"image":"https://assets.example/start.png",
		"size":"1280x720",
		"metadata":{
			"model":"attacker-metadata",
			"model_name":"attacker-metadata-name",
			"prompt":"metadata prompt",
			"image_tail":"https://assets.example/end.png",
			"negative_prompt":"no rain",
			"mode":"pro",
			"duration":"10",
			"aspect_ratio":"16:9",
			"cfg_scale":0.75,
			"static_mask":"aGVsbG8=",
			"dynamic_masks":[{"mask":"aGVsbG8=","trajectories":[{"x":0,"y":10000}]}],
			"camera_control":{"type":"simple","config":{"horizontal":-10,"zoom":10}},
			"callback_url":"https://hooks.example/kling",
			"external_task_id":"customer-123"
		}
	}`)
	prepared, err := PrepareSubmit(raw, "kling-v1-6", "mapped-provider-model")
	require.NoError(t, err)
	assert.Equal(t, ActionImageToVideo, prepared.Action)
	payload := prepared.Payload
	assert.Equal(t, "metadata prompt", payload.Prompt)
	assert.Equal(t, "https://assets.example/start.png", payload.Image)
	assert.Equal(t, "https://assets.example/end.png", payload.ImageTail)
	assert.Equal(t, "mapped-provider-model", payload.Model)
	assert.Equal(t, "mapped-provider-model", payload.ModelName)
	assert.Equal(t, "pro", payload.Mode)
	assert.Equal(t, "10", payload.Duration)
	assert.Equal(t, "16:9", payload.AspectRatio)
	assert.Equal(t, 0.75, payload.CfgScale)
	assert.Len(t, payload.DynamicMasks, 1)
	assert.Equal(t, "https://hooks.example/kling", payload.CallbackURL)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(prepared.Body, &wire))
	assert.Equal(t, "mapped-provider-model", wire["model"])
	assert.Equal(t, "mapped-provider-model", wire["model_name"])
	assert.NotContains(t, wire, "metadata")
	assert.NotContains(t, wire, "size")
	assert.NotContains(t, wire, "seconds")
}

func TestPrepareSubmitSupportsReferenceAliasesAndEncodedMetadata(t *testing.T) {
	prepared, err := PrepareSubmit(
		[]byte(`{"model":"kling-v2-master","prompt":"move","images":["https://assets.example/a.png"],"seconds":10,"metadata":"{\"negative_prompt\":\"blur\"}"}`),
		"kling-v2-master", "mapped",
	)
	require.NoError(t, err)
	assert.Equal(t, ActionImageToVideo, prepared.Action)
	assert.Equal(t, "https://assets.example/a.png", prepared.Payload.Image)
	assert.Equal(t, "10", prepared.Payload.Duration)
	assert.Equal(t, "blur", prepared.Payload.NegativePrompt)

	prepared, err = PrepareSubmit(
		[]byte(`{"model":"kling-v1","prompt":"move","input_reference":"https://assets.example/a.png"}`),
		"kling-v1", "mapped",
	)
	require.NoError(t, err)
	assert.Equal(t, ActionImageToVideo, prepared.Action)

	modelNameOnly := []byte(`{"model_name":"kling-v1-6","prompt":"model name selection"}`)
	model, err := RequestedModel(modelNameOnly)
	require.NoError(t, err)
	assert.Equal(t, "kling-v1-6", model)
	prepared, err = PrepareSubmit(modelNameOnly, model, "mapped-from-model-name")
	require.NoError(t, err)
	assert.Equal(t, "mapped-from-model-name", prepared.Payload.Model)
	assert.Equal(t, "mapped-from-model-name", prepared.Payload.ModelName)

	_, err = RequestedModel([]byte(`{"model":"kling-v1","model_name":"kling-v2-master","prompt":"conflict"}`))
	assert.ErrorContains(t, err, "must not conflict")
}

func TestKlingRequestJSONIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"duplicate top level", `{"model":"kling-v1","model":"kling-v2-master","prompt":"x"}`, "must not be repeated"},
		{"duplicate nested", `{"model":"kling-v1","prompt":"x","camera_control":{"type":"a","type":"b"}}`, "must not be repeated"},
		{"trailing", `{"model":"kling-v1","prompt":"x"} {}`, "trailing JSON"},
		{"unknown top level", `{"model":"kling-v1","prompt":"x","surprise":1}`, "invalid Kling JSON"},
		{"unknown metadata", `{"model":"kling-v1","prompt":"x","metadata":{"surprise":1}}`, "invalid Kling metadata"},
		{"nested metadata", `{"model":"kling-v1","prompt":"x","metadata":{"metadata":{}}}`, "nested Kling metadata"},
		{"non object", `[]`, "invalid Kling JSON"},
		{"missing model", `{"prompt":"x"}`, "model or model_name is required"},
		{"unsupported model", `{"model":"not-kling","prompt":"x"}`, "unsupported Kling model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.name == "unknown metadata" || test.name == "nested metadata" {
				_, err = PrepareSubmit([]byte(test.raw), "kling-v1", "mapped")
			} else {
				_, err = RequestedModel([]byte(test.raw))
			}
			assert.ErrorContains(t, err, test.want)
		})
	}

	oversized := bytes.Repeat([]byte{' '}, int(MaxRequestBodyBytes)+1)
	_, err := RequestedModel(oversized)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	_, err = RequestedModel(nil)
	assert.ErrorContains(t, err, "required")
}

func TestKlingValidationBoundaries(t *testing.T) {
	base := `{"model":"kling-v1","prompt":"ok"}`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"origin mismatch", base, "does not match"},
		{"empty mapped", base, "mapped Kling model"},
		{"prompt required", `{"model":"kling-v1","prompt":"  "}`, "prompt is required"},
		{"prompt control", `{"model":"kling-v1","prompt":"a\u0000b"}`, "prompt is invalid"},
		{"prompt too long", requestWithString("prompt", strings.Repeat("界", MaxPromptRunes+1)), "prompt is invalid"},
		{"negative too long", requestWithString("negative_prompt", strings.Repeat("x", MaxNegativePromptRunes+1)), "negative_prompt is invalid"},
		{"bad mode", requestWithValue("mode", `"turbo"`), "mode must be"},
		{"padded mode", requestWithValue("mode", `" std "`), "mode must be"},
		{"bad duration", requestWithValue("duration", `6`), "duration must be"},
		{"padded duration", requestWithValue("duration", `" 5 "`), "duration must be"},
		{"fraction duration", requestWithValue("duration", `5.0`), "duration must be"},
		{"two durations", `{"model":"kling-v1","prompt":"ok","duration":"5","seconds":"10"}`, "must not both"},
		{"cfg low", requestWithValue("cfg_scale", `-0.001`), "cfg_scale"},
		{"cfg high", requestWithValue("cfg_scale", `1.001`), "cfg_scale"},
		{"insecure image", requestWithString("image", "http://assets.example/a.png"), "HTTPS URL or base64"},
		{"padded image URL", requestWithString("image", " https://assets.example/a.png"), "HTTPS URL or base64"},
		{"bad base64", requestWithString("image", "not-base64!"), "HTTPS URL or base64"},
		{"bad data mime", requestWithString("image", "data:text/plain;base64,aGVsbG8="), "image data URL"},
		{"two images", `{"model":"kling-v1","prompt":"ok","images":["aGVsbG8=","aGVsbG8="]}`, "at most one"},
		{"image conflict", `{"model":"kling-v1","prompt":"ok","image":"aGVsbG8=","images":["d29ybGQ="]}`, "conflicting"},
		{"input conflict", `{"model":"kling-v1","prompt":"ok","image":"aGVsbG8=","input_reference":"d29ybGQ="}`, "conflicting"},
		{"unsupported size", requestWithString("size", "800x600"), "unsupported Kling video size"},
		{"size ratio conflict", `{"model":"kling-v1","prompt":"ok","size":"1280x720","aspect_ratio":"9:16"}`, "conflict"},
		{"bad ratio", requestWithString("aspect_ratio", "4:3"), "aspect_ratio"},
		{"too many masks", requestWithValue("dynamic_masks", `[`+strings.Repeat(`{"mask":"aGVsbG8="},`, MaxDynamicMasks)+`{"mask":"aGVsbG8="}]`), "at most 4"},
		{"mask missing", requestWithValue("dynamic_masks", `[{"trajectories":[]}]`), "mask is required"},
		{"trajectory out of range", requestWithValue("dynamic_masks", `[{"mask":"aGVsbG8=","trajectories":[{"x":10001,"y":0}]}]`), "coordinates"},
		{"camera out of range", requestWithValue("camera_control", `{"config":{"zoom":10.1}}`), "camera_control"},
		{"insecure callback", requestWithString("callback_url", "http://hooks.example/kling"), "valid HTTPS"},
		{"external id path", requestWithString("external_task_id", "bad/id"), "unsupported characters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			origin, mapped := "kling-v1", "mapped"
			if test.name == "origin mismatch" {
				origin = "kling-v1-6"
			}
			if test.name == "empty mapped" {
				mapped = ""
			}
			_, err := PrepareSubmit([]byte(test.raw), origin, mapped)
			assert.ErrorContains(t, err, test.want)
		})
	}

	valid := []string{
		requestWithString("prompt", strings.Repeat("界", MaxPromptRunes)),
		requestWithString("prompt", "first line\nsecond line"),
		requestWithValue("cfg_scale", `0`), requestWithValue("cfg_scale", `1`),
		requestWithValue("duration", `5`), requestWithValue("duration", `"10"`),
		requestWithString("image", "data:image/png;base64,aGVsbG8="),
		requestWithString("size", "512x512"), requestWithString("size", "1920x1080"),
		requestWithString("size", "1080x1920"),
		requestWithValue("camera_control", `{"type":"simple","config":{"horizontal":-10,"zoom":10}}`),
	}
	for _, raw := range valid {
		_, err := PrepareSubmit([]byte(raw), "kling-v1", "mapped")
		require.NoError(t, err, raw)
	}

	points := make([]string, MaxTrajectoriesPerMask)
	for index := range points {
		points[index] = `{"x":0,"y":10000}`
	}
	raw := requestWithValue("dynamic_masks", `[{"mask":"aGVsbG8=","trajectories":[`+strings.Join(points, ",")+`]}]`)
	_, err := PrepareSubmit([]byte(raw), "kling-v1", "mapped")
	require.NoError(t, err)
	points = append(points, `{"x":0,"y":0}`)
	raw = requestWithValue("dynamic_masks", `[{"mask":"aGVsbG8=","trajectories":[`+strings.Join(points, ",")+`]}]`)
	_, err = PrepareSubmit([]byte(raw), "kling-v1", "mapped")
	assert.ErrorContains(t, err, "at most 20")
	masks := make([]string, MaxDynamicMasks)
	for index := range masks {
		masks[index] = `{"mask":"aGVsbG8="}`
	}
	_, err = PrepareSubmit([]byte(requestWithValue("dynamic_masks", `[`+strings.Join(masks, ",")+`]`)), "kling-v1", "mapped")
	require.NoError(t, err)

	tooLargeAsset := requestWithString("image", strings.Repeat("A", MaxAssetBytes+1))
	_, err = PrepareSubmit([]byte(tooLargeAsset), "kling-v1", "mapped")
	assert.ErrorContains(t, err, "too large")
	_, err = PrepareSubmit([]byte(requestWithString("image", strings.Repeat("A", MaxAssetBytes))), "kling-v1", "mapped")
	require.NoError(t, err)

	callbackPrefix := "https://hooks.example/"
	callbackAtLimit := callbackPrefix + strings.Repeat("a", MaxCallbackURLBytes-len(callbackPrefix))
	_, err = PrepareSubmit([]byte(requestWithString("callback_url", callbackAtLimit)), "kling-v1", "mapped")
	require.NoError(t, err)
	_, err = PrepareSubmit([]byte(requestWithString("callback_url", callbackAtLimit+"a")), "kling-v1", "mapped")
	assert.ErrorContains(t, err, "valid HTTPS URL")

	_, err = PrepareSubmit([]byte(requestWithString("external_task_id", strings.Repeat("a", MaxExternalTaskIDBytes))), "kling-v1", "mapped")
	require.NoError(t, err)
	_, err = PrepareSubmit([]byte(requestWithString("external_task_id", strings.Repeat("a", MaxExternalTaskIDBytes+1))), "kling-v1", "mapped")
	assert.ErrorContains(t, err, "too large")
	_, err = PrepareSubmit([]byte(base), "kling-v1", strings.Repeat("m", MaxModelBytes))
	require.NoError(t, err)
	_, err = PrepareSubmit([]byte(base), "kling-v1", strings.Repeat("m", MaxModelBytes+1))
	assert.ErrorContains(t, err, "mapped Kling model")
	_, err = PrepareSubmit([]byte(base), "kling-v1", " mapped ")
	assert.ErrorContains(t, err, "mapped Kling model")
}

func requestWithString(field, value string) string {
	encoded, _ := json.Marshal(value)
	return requestWithValue(field, string(encoded))
}

func requestWithValue(field, value string) string {
	if field == "prompt" {
		return `{"model":"kling-v1","prompt":` + value + `}`
	}
	return `{"model":"kling-v1","prompt":"ok","` + field + `":` + value + `}`
}

func TestAuthorizationTokenDirectJWTAndRelayMode(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	token, err := AuthorizationToken("access-key|secret-key", now)
	require.NoError(t, err)
	parsed, err := jwt.Parse(token, func(token *jwt.Token) (any, error) {
		assert.Equal(t, "HS256", token.Method.Alg())
		assert.Equal(t, "JWT", token.Header["typ"])
		return []byte("secret-key"), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(func() time.Time { return now }))
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	claims := parsed.Claims.(jwt.MapClaims)
	assert.Equal(t, "access-key", claims["iss"])
	assert.Equal(t, float64(now.Unix()+1800), claims["exp"])
	assert.Equal(t, float64(now.Unix()-5), claims["nbf"])

	wrong, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return []byte("wrong"), nil }, jwt.WithoutClaimsValidation())
	assert.Error(t, err)
	assert.False(t, wrong.Valid)

	relayToken, err := AuthorizationToken("sk-relay-secret", now)
	require.NoError(t, err)
	assert.Equal(t, "sk-relay-secret", relayToken)

	for _, invalid := range []string{"", "access", "|secret", "access|", "a|b|c", " access|secret", "access|secret\n", "sk-", "sk-key\n"} {
		_, err := AuthorizationToken(invalid, now)
		assert.Error(t, err, invalid)
	}
}

func TestEffectiveBaseURLIsFixedAndHTTPS(t *testing.T) {
	base, relay, err := EffectiveBaseURL("", "access|secret")
	require.NoError(t, err)
	assert.Equal(t, DirectBaseURL, base)
	assert.False(t, relay)
	base, relay, err = EffectiveBaseURL(DirectBaseURL+"/", "access|secret")
	require.NoError(t, err)
	assert.Equal(t, DirectBaseURL, base)
	assert.False(t, relay)

	_, _, err = EffectiveBaseURL("https://evil.example", "access|secret")
	assert.ErrorContains(t, err, "must use")
	base, relay, err = EffectiveBaseURL("https://relay.example/base/", "sk-key")
	require.NoError(t, err)
	assert.Equal(t, "https://relay.example/base", base)
	assert.True(t, relay)
	for _, invalid := range []string{"", "http://relay.example", "https://user@relay.example", "https://relay.example?q=1", "https://relay.example#x"} {
		_, _, err := EffectiveBaseURL(invalid, "sk-key")
		assert.Error(t, err, invalid)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClientUsesExactDirectAndRelayWireContracts(t *testing.T) {
	fixedNow := time.Unix(2_000_000_000, 0)
	preparedText, err := PrepareSubmit([]byte(`{"model":"kling-v1","prompt":"wind"}`), "kling-v1", "mapped")
	require.NoError(t, err)
	preparedImage, err := PrepareSubmit([]byte(`{"model":"kling-v1","prompt":"wind","image":"aGVsbG8="}`), "kling-v1", "mapped")
	require.NoError(t, err)

	var mu sync.Mutex
	var seen []string
	directHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, request.Method+" "+request.URL.String())
		mu.Unlock()
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		assert.Equal(t, "kling-sdk/1.0", request.Header.Get("User-Agent"))
		authorization := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		parsed, parseErr := jwt.Parse(authorization, func(*jwt.Token) (any, error) { return []byte("secret"), nil },
			jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(func() time.Time { return fixedNow }))
		require.NoError(t, parseErr)
		assert.True(t, parsed.Valid)
		if request.Method == http.MethodPost {
			body, readErr := io.ReadAll(request.Body)
			require.NoError(t, readErr)
			assert.JSONEq(t, string(preparedText.Body), string(body))
		}
		return jsonHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider-1","task_status":"submitted"}}`), nil
	})}
	direct := &Client{HTTPClient: directHTTP, Now: func() time.Time { return fixedNow }}
	submitted, _, err := direct.Submit(context.Background(), "", "access|secret", preparedText)
	require.NoError(t, err)
	assert.Equal(t, "provider-1", submitted.ProviderTaskID)
	_, _, err = direct.Fetch(context.Background(), DirectBaseURL, "access|secret", ActionTextToVideo, "provider-1")
	require.NoError(t, err)
	_, _, err = direct.Fetch(context.Background(), DirectBaseURL, "access|secret", ActionImageToVideo, "provider-1")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"POST https://api.klingai.com/v1/videos/text2video",
		"GET https://api.klingai.com/v1/videos/text2video/provider-1",
		"GET https://api.klingai.com/v1/videos/image2video/provider-1",
	}, seen)

	relayHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		expectedURL := "https://relay.example/root/kling/v1/videos/image2video"
		if request.Method == http.MethodGet {
			expectedURL += "/provider-2"
		}
		assert.Equal(t, expectedURL, request.URL.String())
		assert.Equal(t, "Bearer sk-relay", request.Header.Get("Authorization"))
		return jsonHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider-2","task_status":"processing"}}`), nil
	})}
	relay := &Client{HTTPClient: relayHTTP, Now: func() time.Time { return fixedNow }}
	result, _, err := relay.Submit(context.Background(), "https://relay.example/root", "sk-relay", preparedImage)
	require.NoError(t, err)
	assert.Equal(t, StatusProcessing, result.Status)
	_, _, err = relay.Fetch(context.Background(), "https://relay.example/root", "sk-relay", ActionImageToVideo, "provider-2")
	require.NoError(t, err)
}

func TestNewHTTPClientIsSSRFSafeAndRefusesRedirects(t *testing.T) {
	client := NewHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	request, err := http.NewRequest(http.MethodGet, "https://redirect.example", nil)
	require.NoError(t, err)
	assert.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)
}

func TestParseTaskResponseStatusIdentityResultAndUnits(t *testing.T) {
	statuses := []struct {
		provider string
		want     TaskStatus
	}{
		{"submitted", StatusSubmitted},
		{"processing", StatusProcessing},
		{"succeed", StatusSucceeded},
		{"failed", StatusFailed},
	}
	for _, status := range statuses {
		t.Run(status.provider, func(t *testing.T) {
			body := `{"code":0,"data":{"task_id":"provider-1","task_status":"` + status.provider + `","task_status_msg":" status ","task_result":{"videos":[{"url":"https://cdn.example/first.mp4"},{"url":"https://cdn.example/second.mp4"}]},"created_at":11,"updated_at":22,"final_unit_deduction":"1.01"}}`
			task, raw, err := ParseTaskResponse(jsonHTTPResponse(http.StatusOK, body), "provider-1")
			require.NoError(t, err)
			assert.Equal(t, status.want, task.Status)
			assert.Equal(t, "provider-1", task.ProviderTaskID)
			assert.Equal(t, "status", task.StatusMessage)
			assert.Equal(t, "https://cdn.example/first.mp4", task.ResultURL)
			assert.Equal(t, 2, task.CompletionUnits)
			assert.Equal(t, int64(11), task.CreatedAt)
			assert.Equal(t, int64(22), task.UpdatedAt)
			assert.Equal(t, body, string(raw))
		})
	}

	invalid := []struct {
		name string
		body string
		id   string
		want string
	}{
		{"top level id only", `{"code":0,"task_id":"provider-1","data":{"task_status":"submitted"}}`, "", "data.task_id"},
		{"unknown status", `{"code":0,"data":{"task_id":"provider-1","task_status":"queued"}}`, "", "unknown Kling task status"},
		{"mismatched id", `{"code":0,"data":{"task_id":"provider-other","task_status":"submitted"}}`, "provider-1", "does not match"},
		{"unsafe id", `{"code":0,"data":{"task_id":"../provider","task_status":"submitted"}}`, "", "data.task_id"},
		{"padded id", `{"code":0,"data":{"task_id":" provider-1","task_status":"submitted"}}`, "", "data.task_id"},
		{"padded status", `{"code":0,"data":{"task_id":"provider-1","task_status":"submitted "}}`, "", "unknown Kling task status"},
		{"bad units", `{"code":0,"data":{"task_id":"provider-1","task_status":"succeed","final_unit_deduction":"NaN"}}`, "", "final_unit_deduction"},
		{"bad result URL", `{"code":0,"data":{"task_id":"provider-1","task_status":"succeed","task_result":{"videos":[{"url":"javascript:alert(1)"}]}}}`, "", "invalid video URL"},
		{"trailing response", `{"code":0,"data":{"task_id":"provider-1","task_status":"submitted"}} trailing`, "", "invalid Kling task response"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := ParseTaskResponse(jsonHTTPResponse(http.StatusOK, test.body), test.id)
			assert.ErrorContains(t, err, test.want)
		})
	}
	_, _, err := ParseTaskResponse(nil, "")
	assert.ErrorContains(t, err, "nil")
}

func TestFinalUnitDeductionCeilsAndSaturates(t *testing.T) {
	tests := []struct {
		raw  string
		want int
		ok   bool
	}{
		{"", 0, true}, {"0", 0, true}, {"0.0001", 1, true}, {"1", 1, true}, {"1.01", 2, true},
		{"1.0000000000000000001", 2, true}, {".0000000000000000001", 1, true}, {"1.000e0", 1, true},
		{"1.001e3", 1001, true}, {"1001e-3", 2, true}, {"+2.1", 3, true},
		{"2147483647", int(quotamath.MaxQuota), true}, {"2147483648", int(quotamath.MaxQuota), true},
		{"1e999", int(quotamath.MaxQuota), true}, {"1e999999999999999999999", int(quotamath.MaxQuota), true},
		{"1e-999999999999999999999", 1, true}, {"-0.1", 0, false}, {"NaN", 0, false},
		{"Inf", 0, false}, {"garbage", 0, false}, {"1e", 0, false}, {"1e+", 0, false},
		{"1e--999999999999999999999", 0, false}, {"1.2.3", 0, false},
	}
	for _, test := range tests {
		got, ok := ParseFinalUnitDeduction(test.raw)
		assert.Equal(t, test.ok, ok, test.raw)
		assert.Equal(t, test.want, got, test.raw)
	}
}

func TestProviderErrorsAreBoundedAndSanitized(t *testing.T) {
	body := `{"code":4001,"message":" bad\nmessage\u0000 "}`
	_, raw, err := ParseTaskResponse(jsonHTTPResponse(http.StatusOK, body), "")
	require.Error(t, err)
	assert.Equal(t, body, string(raw))
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Equal(t, "4001", provider.Code)
	assert.Equal(t, "bad message", provider.Message)
	assert.NotContains(t, err.Error(), "bad")

	httpBody := `{"code":429,"message":"slow down"}`
	_, _, err = ParseTaskResponse(jsonHTTPResponse(http.StatusTooManyRequests, httpBody), "")
	require.ErrorAs(t, err, &provider)
	assert.Equal(t, http.StatusTooManyRequests, provider.StatusCode)
	assert.Equal(t, "slow down", provider.Message)
	assert.NotContains(t, err.Error(), "slow down")

	oversizedSuccess := strings.Repeat("x", int(MaxResponseBodyBytes)+1)
	_, _, err = ParseTaskResponse(jsonHTTPResponse(http.StatusOK, oversizedSuccess), "")
	require.ErrorAs(t, err, &provider)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)

	prefix := `{"code":0,"data":{"task_id":"provider","task_status":"submitted"},"padding":"`
	suffix := `"}`
	exactlyBounded := prefix + strings.Repeat("x", int(MaxResponseBodyBytes)-len(prefix)-len(suffix)) + suffix
	task, raw, err := ParseTaskResponse(jsonHTTPResponse(http.StatusOK, exactlyBounded), "provider")
	require.NoError(t, err)
	assert.Equal(t, StatusSubmitted, task.Status)
	assert.Len(t, raw, int(MaxResponseBodyBytes))

	oversizedError := strings.Repeat("x", int(relaycommon.MaxUpstreamErrorBodyBytes)+1)
	_, _, err = ParseTaskResponse(jsonHTTPResponse(http.StatusBadGateway, oversizedError), "")
	require.ErrorAs(t, err, &provider)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestSubmitDispatchClassificationAndFetchValidation(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{"model":"kling-v1","prompt":"wind"}`), "kling-v1", "mapped")
	require.NoError(t, err)
	transportFailure := errors.New("network down")
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportFailure
	})}}
	_, _, err = client.Submit(context.Background(), "", "access|secret", prepared)
	require.Error(t, err)
	assert.True(t, SubmitWasDispatched(err))
	assert.ErrorIs(t, err, transportFailure)

	_, _, err = client.Submit(context.Background(), "", "bad-key", prepared)
	require.Error(t, err)
	assert.False(t, SubmitWasDispatched(err))
	_, _, err = client.Submit(context.Background(), "", "access|secret", nil)
	assert.False(t, SubmitWasDispatched(err))
	_, _, err = client.Submit(context.Background(), "", "access|secret", &PreparedRequest{
		Body: bytes.Repeat([]byte{'x'}, int(MaxRequestBodyBytes)+1), Action: ActionTextToVideo,
	})
	assert.False(t, SubmitWasDispatched(err))
	_, _, err = client.Fetch(context.Background(), "", "access|secret", Action("unknown"), "provider")
	assert.ErrorContains(t, err, "action")
	_, _, err = client.Fetch(context.Background(), "", "access|secret", ActionTextToVideo, "bad/id")
	assert.Error(t, err)

	parseFailureClient := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"code":0,"data":{"task_id":"provider","task_status":"mystery"}}`), nil
	})}}
	_, _, err = parseFailureClient.Submit(context.Background(), "", "access|secret", prepared)
	assert.True(t, SubmitWasDispatched(err))
}

func TestResponseDecoderRetainsForwardCompatibleFields(t *testing.T) {
	body := `{"code":0,"new_envelope_field":true,"data":{"task_id":"provider","task_status":"submitted","new_data_field":{"x":1}}}`
	task, _, err := ParseTaskResponse(jsonHTTPResponse(http.StatusOK, body), "provider")
	require.NoError(t, err)
	assert.Equal(t, StatusSubmitted, task.Status)
}

func TestParseFinalUnitDeductionRejectsPathologicalLength(t *testing.T) {
	value, ok := ParseFinalUnitDeduction(strings.Repeat("9", 65))
	assert.False(t, ok)
	assert.Zero(t, value)
	value, ok = ParseFinalUnitDeduction(strconvFloat(math.Inf(-1)))
	assert.False(t, ok)
	assert.Zero(t, value)
}

func strconvFloat(value float64) string {
	if math.IsInf(value, -1) {
		return "-Inf"
	}
	return ""
}
