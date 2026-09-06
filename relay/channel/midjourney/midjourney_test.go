package midjourney

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func providerJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func TestModelForActionExactCatalog(t *testing.T) {
	expected := map[Action]string{
		ActionImagine: "mj_imagine", ActionDescribe: "mj_describe", ActionBlend: "mj_blend",
		ActionUpscale: "mj_upscale", ActionVariation: "mj_variation", ActionReroll: "mj_reroll",
		ActionInpaint: "mj_inpaint", ActionModal: "mj_modal", ActionZoom: "mj_zoom",
		ActionCustomZoom: "mj_custom_zoom", ActionShorten: "mj_shorten",
		ActionHighVariation: "mj_high_variation", ActionLowVariation: "mj_low_variation",
		ActionPan: "mj_pan", ActionSwapFace: "swap_face", ActionUpload: "mj_upload",
		ActionVideo: "mj_video", ActionEdits: "mj_edits",
	}
	for action, model := range expected {
		actual, ok := ModelForAction(action)
		assert.True(t, ok, action)
		assert.Equal(t, model, actual, action)
	}
	_, ok := ModelForAction("UNKNOWN")
	assert.False(t, ok)
}

func TestPrepareSubmitExactRouteBodies(t *testing.T) {
	const localID = "task_0123456789abcdef0123456789abcdef"
	const providerID = "provider-task-1"
	tests := []struct {
		name      string
		operation string
		input     string
		path      string
		action    Action
		model     string
		wantBody  string
		boundBody string
	}{
		{"action", "action", `{"customId":"MJ::JOB::upsample::2::uuid","taskId":"` + localID + `"}`, "/mj/submit/action", ActionUpscale, "mj_upscale", `{"customId":"MJ::JOB::upsample::2::uuid","taskId":"` + localID + `"}`, `{"customId":"MJ::JOB::upsample::2::uuid","taskId":"` + providerID + `"}`},
		{"shorten", "shorten", `{"prompt":"a cat"}`, "/mj/submit/shorten", ActionShorten, "mj_shorten", `{"prompt":"a cat"}`, ""},
		{"modal", "modal", `{"maskBase64":"YQ==","taskId":"` + localID + `"}`, "/mj/submit/modal", ActionModal, "mj_modal", `{"maskBase64":"YQ==","taskId":"` + localID + `"}`, `{"maskBase64":"YQ==","taskId":"` + providerID + `"}`},
		{"imagine", "imagine", `{"notifyHook":"https://callback.invalid/hook","prompt":"a cat"}`, "/mj/submit/imagine", ActionImagine, "mj_imagine", `{"prompt":"a cat"}`, ""},
		{"change", "change", `{"action":"VARIATION","index":3,"taskId":"` + localID + `"}`, "/mj/submit/change", ActionVariation, "mj_variation", `{"action":"VARIATION","index":3,"taskId":"` + localID + `"}`, `{"action":"VARIATION","index":3,"taskId":"` + providerID + `"}`},
		{"simple-change", "simple-change", `{"content":"` + localID + ` U2"}`, "/mj/submit/simple-change", ActionUpscale, "mj_upscale", `{"content":"` + localID + ` U2"}`, `{"content":"` + providerID + ` U2"}`},
		{"describe", "describe", `{"base64Array":["YQ=="]}`, "/mj/submit/describe", ActionDescribe, "mj_describe", `{"base64Array":["YQ=="]}`, ""},
		{"blend", "blend", `{"base64Array":["YQ==","Yg=="]}`, "/mj/submit/blend", ActionBlend, "mj_blend", `{"base64Array":["YQ==","Yg=="]}`, ""},
		{"edits", "edits", `{"base64Array":["YQ=="],"prompt":"change it"}`, "/mj/submit/edits", ActionEdits, "mj_edits", `{"base64Array":["YQ=="],"prompt":"change it"}`, ""},
		{"video", "video", `{"taskId":"` + localID + `"}`, "/mj/submit/video", ActionVideo, "mj_video", `{"taskId":"` + localID + `"}`, `{"taskId":"` + providerID + `"}`},
		{"swap", "swap", `{"sourceBase64":"YQ==","targetBase64":"Yg=="}`, "/mj/insight-face/swap", ActionSwapFace, "swap_face", `{"sourceBase64":"YQ==","targetBase64":"Yg=="}`, ""},
		{"upload", "upload-discord-images", `{"base64Array":["YQ=="]}`, "/mj/submit/upload-discord-images", ActionUpload, "mj_upload", `{"base64Array":["YQ=="]}`, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := PrepareSubmit([]byte(test.input), test.operation)
			require.NoError(t, err)
			assert.Equal(t, test.path, prepared.Path)
			assert.Equal(t, test.action, prepared.Action)
			assert.Equal(t, test.model, prepared.Model)
			assert.JSONEq(t, test.wantBody, string(prepared.Body))
			if test.boundBody != "" {
				require.NoError(t, BindProviderTaskID(prepared, providerID))
				assert.JSONEq(t, test.boundBody, string(prepared.Body))
				assert.NotContains(t, string(prepared.Body), localID)
			}
		})
	}
}

func TestPrepareSubmitRejectsMalformedAndUnboundedInputs(t *testing.T) {
	tests := []struct {
		name, operation, body string
	}{
		{"unknown field", "imagine", `{"prompt":"x","accountFilter":{}}`},
		{"duplicate", "imagine", `{"prompt":"x","prompt":"y"}`},
		{"trailing", "imagine", `{"prompt":"x"}{}`},
		{"blank prompt", "imagine", `{"prompt":""}`},
		{"bad base64", "describe", `{"base64Array":["not-base64"]}`},
		{"describe count", "describe", `{"base64Array":["YQ==","Yg=="]}`},
		{"blend count", "blend", `{"base64Array":["YQ=="]}`},
		{"bad change action", "change", `{"taskId":"x","action":"IMAGINE","index":1}`},
		{"bad change index", "change", `{"taskId":"x","action":"UPSCALE","index":5}`},
		{"bad simple", "simple-change", `{"content":"x U9"}`},
		{"short plus custom id", "action", `{"customId":"MJ","taskId":"x"}`},
		{"unknown plus action", "action", `{"customId":"MJ::unknown::1","taskId":"x"}`},
		{"missing upload", "upload-discord-images", `{}`},
		{"missing swap", "swap", `{"sourceBase64":"YQ=="}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareSubmit([]byte(test.body), test.operation)
			assert.Error(t, err)
		})
	}
	_, err := PrepareSubmit([]byte(strings.Repeat(" ", int(MaxRequestBodyBytes)+1)), "imagine")
	assert.Error(t, err)
}

func TestParsePlusActionDoesNotPanicOnShortCustomIDs(t *testing.T) {
	for _, raw := range []string{"", "MJ", "MJ::", "MJ::JOB", "MJ::JOB::upsample", "MJ::variation"} {
		t.Run(raw, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_, _, err := ParsePlusAction(raw)
				assert.Error(t, err)
			})
		})
	}
}

func TestClientExactProviderWireContract(t *testing.T) {
	const secret = "provider-secret-value"
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, secret, request.Header.Get("mj-api-secret"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		switch calls {
		case 1:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "/base/mj/submit/imagine", request.URL.Path)
			assert.JSONEq(t, `{"prompt":"a cat"}`, string(body))
			return providerJSONResponse(http.StatusOK, `{"code":22,"description":"queued","properties":{"numberOfQueues":1},"result":"provider-task-1"}`), nil
		case 2:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "/base/mj/submit/upload-discord-images", request.URL.Path)
			assert.JSONEq(t, `{"base64Array":["YQ=="]}`, string(body))
			return providerJSONResponse(http.StatusOK, `{"code":1,"description":"ok","result":["https://cdn.example/a.png"]}`), nil
		case 3:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "/base/mj/task/list-by-condition", request.URL.Path)
			assert.JSONEq(t, `{"ids":["provider-task-1"]}`, string(body))
			return providerJSONResponse(http.StatusOK, `[{"id":"provider-task-1","action":"IMAGINE","status":"SUCCESS","progress":"100%","imageUrl":"https://cdn.example/a.png"}]`), nil
		case 4:
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "/base/mj/task/provider-task-1/image-seed", request.URL.Path)
			assert.Empty(t, body)
			return providerJSONResponse(http.StatusOK, `{"code":1,"description":"ok","properties":null,"result":"42"}`), nil
		default:
			t.Fatalf("unexpected provider call %d", calls)
		}
		return nil, errors.New("unexpected provider call")
	})}
	client := &Client{HTTPClient: httpClient}
	prepared, err := PrepareSubmit([]byte(`{"prompt":"a cat"}`), "imagine")
	require.NoError(t, err)
	result, err := client.Submit(context.Background(), "https://provider.example/base", secret, prepared)
	require.NoError(t, err)
	assert.Equal(t, 22, result.Code)
	assert.Equal(t, "provider-task-1", result.Result)

	upload, err := PrepareSubmit([]byte(`{"base64Array":["YQ=="]}`), "upload-discord-images")
	require.NoError(t, err)
	uploadResult, err := client.Upload(context.Background(), "https://provider.example/base", secret, upload)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://cdn.example/a.png"}, uploadResult.Result)

	tasks, err := client.Fetch(context.Background(), "https://provider.example/base", secret, []string{"provider-task-1"})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "provider-task-1", tasks[0].ProviderTaskID)
	assert.Equal(t, "SUCCESS", tasks[0].Status)

	seed, err := client.ImageSeed(context.Background(), "https://provider.example/base", secret, "provider-task-1")
	require.NoError(t, err)
	assert.Equal(t, "42", seed.Result)
	assert.Equal(t, 4, calls)
}

func TestClientProviderFailuresAreBoundedAndCredentialSafe(t *testing.T) {
	const secret = "provider-secret-value"
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"definitive rejection", http.StatusBadRequest, `{"code":24,"description":"raw private rejection","result":""}`},
		{"unknown field", http.StatusOK, `{"code":1,"description":"ok","result":"provider-task","unexpected":true}`},
		{"duplicate field", http.StatusOK, `{"code":1,"code":1,"description":"ok","result":"provider-task"}`},
		{"credential reflection", http.StatusOK, `{"code":1,"description":"provider-secret-value","result":"provider-task"}`},
		{"invalid result", http.StatusOK, `{"code":1,"description":"ok","result":"../provider-task"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return providerJSONResponse(test.status, test.body), nil
			})}
			prepared, err := PrepareSubmit([]byte(`{"prompt":"x"}`), "imagine")
			require.NoError(t, err)
			_, err = (&Client{HTTPClient: httpClient}).Submit(context.Background(), "https://provider.example", secret, prepared)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), secret)
			assert.NotContains(t, err.Error(), "raw private rejection")
			if test.name == "definitive rejection" {
				assert.True(t, IsDefinitiveRejection(err))
			}
		})
	}
}

func TestClientRejectsOversizedMalformedAndUnrequestedFetchResponses(t *testing.T) {
	responses := []string{
		`[{"id":"another-task","status":"SUCCESS","progress":"100%"}]`,
		`[{"id":"provider-task","status":"IMPOSSIBLE","progress":"100%"}]`,
		`[{"id":"provider-task","status":"SUCCESS","progress":"101%"}]`,
	}
	for _, body := range responses {
		httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return providerJSONResponse(http.StatusOK, body), nil
		})}
		_, err := (&Client{HTTPClient: httpClient}).Fetch(context.Background(), "https://provider.example",
			"provider-secret-value", []string{"provider-task"})
		assert.Error(t, err)
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return providerJSONResponse(http.StatusOK, strings.Repeat("x", int(MaxResponseBodyBytes)+1)), nil
	})}
	prepared, err := PrepareSubmit([]byte(`{"prompt":"x"}`), "imagine")
	require.NoError(t, err)
	_, err = (&Client{HTTPClient: httpClient}).Submit(context.Background(), "https://provider.example",
		"provider-secret-value", prepared)
	assert.Error(t, err)
}

func TestClientValidatesBaseCredentialsBatchAndProductionTimeouts(t *testing.T) {
	for _, raw := range []string{"", "http://provider.example", "https://user@provider.example", "https://provider.example?q=x", "https://provider.example/../x"} {
		_, err := ValidateBaseURL(raw)
		assert.Error(t, err, raw)
	}
	base, err := ValidateBaseURL("https://provider.example/api/")
	require.NoError(t, err)
	assert.Equal(t, "https://provider.example/api", base)

	client := NewHTTPClient()
	assert.Equal(t, 60*time.Second, client.Timeout)
	request := httptest.NewRequest(http.MethodGet, "https://provider.example", nil)
	response := &http.Response{StatusCode: http.StatusFound, Request: request}
	assert.True(t, errors.Is(client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse))
	_ = response

	_, err = (&Client{}).Fetch(context.Background(), "https://provider.example", "secret", nil)
	assert.Error(t, err)
	ids := make([]string, MaxBatchTasks+1)
	for index := range ids {
		ids[index] = "task-" + string(rune('a'+index%26))
	}
	_, err = (&Client{}).Fetch(context.Background(), "https://provider.example", "secret", ids)
	assert.Error(t, err)
	prepared, err := PrepareSubmit([]byte(`{"prompt":"x"}`), "imagine")
	require.NoError(t, err)
	_, err = (&Client{}).Submit(context.Background(), "http://provider.example", "secret", prepared)
	assert.Error(t, err)
	_, err = (&Client{}).Submit(context.Background(), "https://provider.example", " bad-key ", prepared)
	assert.Error(t, err)
}

func TestProviderResponseRoundTripDoesNotCoerceProperties(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return providerJSONResponse(http.StatusOK, `{"code":21,"description":"exists","properties":{"status":"SUCCESS","imageUrl":"https://cdn.example/a.png"},"result":"provider-task"}`), nil
	})}
	prepared, err := PrepareSubmit([]byte(`{"prompt":"x"}`), "imagine")
	require.NoError(t, err)
	response, err := (&Client{HTTPClient: httpClient}).Submit(context.Background(), "https://provider.example",
		"provider-secret-value", prepared)
	require.NoError(t, err)
	var properties map[string]any
	require.NoError(t, json.Unmarshal(response.Properties, &properties))
	assert.Equal(t, "SUCCESS", properties["status"])
}
