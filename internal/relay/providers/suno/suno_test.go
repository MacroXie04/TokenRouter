package suno

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestModelCatalogAndActionMapping(t *testing.T) {
	assert.Equal(t, []string{"suno_music", "suno_lyrics"}, ModelList)
	assert.Equal(t, "suno_music", mustModel(t, ActionMusic))
	assert.Equal(t, "suno_lyrics", mustModel(t, ActionLyrics))
	action, ok := ActionForModel("suno_music")
	assert.True(t, ok)
	assert.Equal(t, ActionMusic, action)
	action, err := ParseAction(" music ")
	require.NoError(t, err)
	assert.Equal(t, ActionMusic, action)
	_, err = ParseAction("describe")
	assert.Error(t, err)
}

func TestPrepareSubmitDefaultsAndExactWire(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{"prompt":"hello"}`), "MUSIC")
	require.NoError(t, err)
	assert.Equal(t, ActionMusic, prepared.Action)
	assert.Equal(t, "suno_music", prepared.Model)
	assert.Equal(t, DefaultMusicModelVersion, prepared.Value.Mv)
	assert.JSONEq(t, `{"prompt":"hello","mv":"chirp-v3-0","make_instrumental":false}`, string(prepared.Body))

	prepared, err = PrepareSubmit([]byte(`{
		"gpt_description_prompt":"dreamy", "prompt":"words", "mv":"v4",
		"title":"title", "tags":"tag", "continue_at":12.5,
		"task_id":"source", "continue_clip_id":"clip", "make_instrumental":true
	}`), "music")
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"gpt_description_prompt":"dreamy", "prompt":"words", "mv":"v4",
		"title":"title", "tags":"tag", "continue_at":12.5,
		"task_id":"source", "continue_clip_id":"clip", "make_instrumental":true
	}`, string(prepared.Body))
}

func TestPrepareSubmitStrictValidation(t *testing.T) {
	tests := []struct {
		name   string
		action string
		body   string
	}{
		{"empty", "MUSIC", ``},
		{"unknown field", "MUSIC", `{"unknown":1}`},
		{"duplicate field", "MUSIC", `{"prompt":"a","prompt":"b"}`},
		{"trailing JSON", "MUSIC", `{} {}`},
		{"invalid action", "OTHER", `{}`},
		{"lyrics needs prompt", "LYRICS", `{}`},
		{"lyrics whitespace prompt", "LYRICS", `{"prompt":"  "}`},
		{"negative continue", "MUSIC", `{"continue_at":-1}`},
		{"large continue", "MUSIC", `{"continue_at":3600.01}`},
		{"control prompt", "MUSIC", `{"prompt":"a\u0000b"}`},
		{"blank mv", "MUSIC", `{"mv":" "}`},
		{"control id", "MUSIC", `{"task_id":"a\nb"}`},
		{"space in id", "MUSIC", `{"task_id":"a b"}`},
		{"continuation without task", "MUSIC", `{"continue_clip_id":"clip"}`},
		{"lyrics continuation", "LYRICS", `{"prompt":"words","task_id":"task"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareSubmit([]byte(test.body), test.action)
			assert.Error(t, err)
		})
	}

	_, err := PrepareSubmit([]byte(`{"prompt":"`+strings.Repeat("界", MaxPromptRunes)+`"}`), "LYRICS")
	require.NoError(t, err)
	_, err = PrepareSubmit([]byte(`{"prompt":"`+strings.Repeat("界", MaxPromptRunes+1)+`"}`), "LYRICS")
	assert.Error(t, err)
	_, err = PrepareSubmit([]byte(`{"gpt_description_prompt":"`+strings.Repeat("a", MaxDescriptionPromptRunes+1)+`"}`), "MUSIC")
	assert.Error(t, err)
	_, err = PrepareSubmit([]byte(`{"title":"`+strings.Repeat("a", MaxTitleRunes+1)+`"}`), "MUSIC")
	assert.Error(t, err)
	_, err = PrepareSubmit([]byte(`{"tags":"`+strings.Repeat("a", MaxTagsRunes+1)+`"}`), "MUSIC")
	assert.Error(t, err)
	_, err = PrepareSubmit([]byte(`{"mv":"`+strings.Repeat("a", MaxModelVersionBytes+1)+`"}`), "MUSIC")
	assert.Error(t, err)
	_, err = PrepareSubmit([]byte(`{"task_id":"`+strings.Repeat("a", MaxTaskIDBytes+1)+`"}`), "MUSIC")
	assert.Error(t, err)
	_, err = PrepareSubmit(make([]byte, MaxRequestBodyBytes+1), "MUSIC")
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

func TestWithProviderTaskIDRewritesOnlyTheAuthenticatedIdentifier(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{"task_id":"task_0123456789abcdefghijklmnopqrstuv","continue_clip_id":"clip-1","prompt":"more"}`), "MUSIC")
	require.NoError(t, err)
	rewritten, err := WithProviderTaskID(prepared, "provider-origin")
	require.NoError(t, err)
	assert.Equal(t, "task_0123456789abcdefghijklmnopqrstuv", prepared.Value.TaskID)
	assert.Equal(t, "provider-origin", rewritten.Value.TaskID)
	assert.Equal(t, "clip-1", rewritten.Value.ContinueClipID)
	assert.NotContains(t, string(rewritten.Body), "task_0123456789abcdefghijklmnopqrstuv")
	_, err = WithProviderTaskID(prepared, "provider/origin")
	assert.Error(t, err)
}

func TestValidateBaseURLAndSafeHTTPClient(t *testing.T) {
	base, err := ValidateBaseURL(" https://api.example.com/base/ ")
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/base", base)
	for _, raw := range []string{
		"", "http://api.example.com", "https://user@example.com", "https://example.com?q=1",
		"https://:443", "https://example.com/#x", "https://example.com/a/../b", "https://example.com/%2e%2e/x",
		"https://example.com/a\\b", "https://example.com\n",
	} {
		_, err := ValidateBaseURL(raw)
		assert.Error(t, err, raw)
	}
	client := NewHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, client.CheckRedirect)
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	assert.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)
	assert.NotZero(t, client.Timeout)
}

func TestClientSubmitExactRequestAndPublicParse(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{"prompt":"song","make_instrumental":true}`), "MUSIC")
	require.NoError(t, err)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/root/suno/submit/MUSIC", request.URL.Path)
		assert.Equal(t, "Bearer provider-secret", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		body, readErr := io.ReadAll(request.Body)
		require.NoError(t, readErr)
		assert.Equal(t, prepared.Body, body)
		_, _ = io.WriteString(w, `{"code":"success","message":"ok","data":"provider-123"}`)
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client()}
	id, err := client.Submit(context.Background(), server.URL+"/root", "provider-secret", prepared)
	require.NoError(t, err)
	assert.Equal(t, "provider-123", id)
}

func TestClientRejectsForgedPreparedRequestBeforeDispatch(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{}`), "MUSIC")
	require.NoError(t, err)
	for _, mutate := range []func(*PreparedRequest){
		func(value *PreparedRequest) { value.Model = "suno_lyrics" },
		func(value *PreparedRequest) { value.Body = []byte(`{"mv":"other","make_instrumental":false}`) },
		func(value *PreparedRequest) { value.Value.Mv = "bad model" },
	} {
		clone := *prepared
		clone.Value = prepared.Value
		clone.Body = append([]byte(nil), prepared.Body...)
		mutate(&clone)
		_, err := (&Client{}).Submit(context.Background(), "https://example.com", "key", &clone)
		assert.Error(t, err)
		assert.False(t, SubmitWasDispatched(err))
	}
}

func TestClientSubmitDispatchClassificationAndSanitizedErrors(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{}`), "MUSIC")
	require.NoError(t, err)
	_, err = (&Client{}).Submit(context.Background(), "http://bad.example", "key", prepared)
	assert.False(t, SubmitWasDispatched(err))

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"provider-secret","message":"credential=provider-secret\n`+strings.Repeat("x", 700)+`","data":null}`)
	}))
	defer server.Close()
	_, err = (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "provider-secret", prepared)
	assert.True(t, SubmitWasDispatched(err))
	assert.True(t, IsDefinitiveRejection(err))
	var providerError *ProviderError
	require.ErrorAs(t, err, &providerError)
	assert.Equal(t, "provider_error", providerError.Code)
	assert.LessOrEqual(t, len([]rune(providerError.Message)), MaxProviderMessageRunes)
	assert.NotContains(t, providerError.Message, "provider-secret")
	assert.NotContains(t, providerError.Code, "provider-secret")
	assert.NotContains(t, err.Error(), "provider-secret")

	closed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedClient := closed.Client()
	closed.Close()
	_, err = (&Client{HTTPClient: closedClient}).Submit(
		context.Background(), closed.URL+"/private-tenant-path", "key", prepared,
	)
	assert.Error(t, err)
	assert.True(t, SubmitWasDispatched(err))
	assert.NotContains(t, err.Error(), "private-tenant-path")
}

func TestClientSubmitRejectsMalformedAndOversizedResponses(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{}`), "MUSIC")
	require.NoError(t, err)
	for _, response := range []string{
		`{"code":"success","message":"","data":""}`,
		`{"code":"success","message":"","data":"a/b"}`,
		`{"code":"success","message":"","data":"one"} {}`,
		`{"code":"success","message":"","data":"one","data":"two"}`,
		`{"code":"success","message":"","data":"one","extra":1}`,
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, response)
		}))
		_, err := (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "key", prepared)
		server.Close()
		assert.Error(t, err, response)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", int(MaxResponseBodyBytes)+1))
	}))
	defer server.Close()
	_, err = (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "key", prepared)
	assert.Error(t, err)
	var providerError *ProviderError
	require.ErrorAs(t, err, &providerError)
	assert.Equal(t, "response_too_large", providerError.Code)
}

func TestClientFetchExactBatchAndStatusMapping(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/suno/fetch", request.URL.Path)
		assert.Equal(t, "Bearer key", request.Header.Get("Authorization"))
		body, _ := io.ReadAll(request.Body)
		assert.JSONEq(t, `{"ids":["one","two"]}`, string(body))
		_, _ = io.WriteString(w, `{"code":"success","message":"","data":[
			{"task_id":"one","action":"MUSIC","status":"PROCESSING","fail_reason":"","submit_time":1,"start_time":2,"finish_time":0,"data":{"clips":[]}},
			{"task_id":"two","action":"LYRICS","status":"SUCCESS","fail_reason":"","submit_time":3,"start_time":4,"finish_time":5,"data":null}
		]}`)
	}))
	defer server.Close()
	results, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "key", []string{"one", "two"})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, StatusProcessing, results[0].Status)
	assert.JSONEq(t, `{"clips":[]}`, string(results[0].Data))
	assert.Equal(t, StatusSuccess, results[1].Status)
	assert.Equal(t, "null", string(results[1].Data))
}

func TestClientFetchValidationAndStrictProviderIdentities(t *testing.T) {
	client := &Client{}
	for _, ids := range [][]string{nil, {}, {""}, {"a", "a"}, {"a/b"}} {
		_, err := client.Fetch(context.Background(), "https://example.com", "key", ids)
		assert.Error(t, err)
	}
	tooMany := make([]string, MaxBatchTasks+1)
	for index := range tooMany {
		tooMany[index] = string(rune('a'+index%26)) + strings.Repeat("z", index/26+1)
	}
	_, err := client.Fetch(context.Background(), "https://example.com", "key", tooMany)
	assert.Error(t, err)

	tests := []string{
		`{"code":"success","message":"","data":[{"task_id":"other","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":1,"start_time":0,"finish_time":0,"data":null}]}`,
		`{"code":"success","message":"","data":[{"task_id":"one","action":"MUSIC","status":"unknown","fail_reason":"","submit_time":1,"start_time":0,"finish_time":0,"data":null}]}`,
		`{"code":"success","message":"","data":[{"task_id":"one","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":-1,"start_time":0,"finish_time":0,"data":null}]}`,
		`{"code":"success","message":"","data":[{"task_id":"one","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":10,"start_time":9,"finish_time":11,"data":null}]}`,
		`{"code":"success","message":"","data":[{"task_id":"one","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":1,"start_time":0,"finish_time":0,"data":null},{"task_id":"one","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":1,"start_time":0,"finish_time":0,"data":null}]}`,
		`{"code":"success","message":"","data":[{"task_id":"one","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":1,"start_time":0,"finish_time":0,"data":{"x":1,"x":2}}]}`,
		`{"code":"success","message":"","data":[],"extra":true}`,
	}
	for _, payload := range tests {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, payload)
		}))
		_, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "key", []string{"one"})
		server.Close()
		assert.Error(t, err, payload)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"success","message":"","data":[]}`)
	}))
	defer server.Close()
	results, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "key", []string{"one"})
	require.NoError(t, err, "a bounded missing provider item remains retryable")
	assert.Empty(t, results)
}

func TestResponseBoundsAndAPIKeyValidation(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{}`), "MUSIC")
	require.NoError(t, err)
	for _, key := range []string{"", " key", "key\n", strings.Repeat("k", MaxAPIKeyBytes+1)} {
		_, err := (&Client{}).Submit(context.Background(), "https://example.com", key, prepared)
		assert.Error(t, err)
		assert.False(t, SubmitWasDispatched(err))
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", int(MaxResponseBodyBytes)))
	}))
	defer server.Close()
	_, err = (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "key", prepared)
	assert.Error(t, err)
}

func TestClientNeverFollowsRedirectsEvenWithInjectedClient(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{}`), "MUSIC")
	require.NoError(t, err)
	var redirected bool
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected = true
		_, _ = io.WriteString(w, `{"code":"success","message":"","data":"stolen"}`)
	}))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	_, err = (&Client{HTTPClient: origin.Client()}).Submit(context.Background(), origin.URL, "key", prepared)
	assert.Error(t, err)
	assert.False(t, redirected)
}

func TestProviderRejectionCertaintyAndNestedDataBound(t *testing.T) {
	prepared, err := PrepareSubmit([]byte(`{}`), "MUSIC")
	require.NoError(t, err)
	for _, status := range []int{http.StatusRequestTimeout, http.StatusConflict, http.StatusBadGateway} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"code":"failure","message":"no","data":""}`)
		}))
		_, err := (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "key", prepared)
		server.Close()
		assert.Error(t, err)
		assert.False(t, IsDefinitiveRejection(err), status)
	}

	semanticFailure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"failure","message":"no","data":""}`)
	}))
	_, err = (&Client{HTTPClient: semanticFailure.Client()}).Submit(context.Background(), semanticFailure.URL, "key", prepared)
	semanticFailure.Close()
	assert.True(t, IsDefinitiveRejection(err))

	for _, malformed := range []struct {
		status     int
		definitive bool
	}{
		{status: http.StatusOK, definitive: false},
		{status: http.StatusBadRequest, definitive: true},
		{status: http.StatusRequestTimeout, definitive: false},
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(malformed.status)
			_, _ = io.WriteString(w, `{not-json`)
		}))
		_, err := (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "key", prepared)
		server.Close()
		assert.Equal(t, malformed.definitive, IsDefinitiveRejection(err), malformed.status)
	}

	oversizedData := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"success","message":"","data":[{"task_id":"one","action":"MUSIC","status":"submitted","fail_reason":"","submit_time":1,"start_time":0,"finish_time":0,"data":{"blob":"`)
		_, _ = io.WriteString(w, strings.Repeat("x", MaxProviderDataBytes))
		_, _ = io.WriteString(w, `"}}]}`)
	}))
	_, err = (&Client{HTTPClient: oversizedData.Client()}).Fetch(context.Background(), oversizedData.URL, "key", []string{"one"})
	oversizedData.Close()
	assert.Error(t, err)
}

func mustModel(t *testing.T, action Action) string {
	t.Helper()
	model, ok := ModelForAction(action)
	require.True(t, ok)
	return model
}

func TestProviderErrorUnwrap(t *testing.T) {
	cause := errors.New("decode")
	err := &ProviderError{StatusCode: 502, Cause: cause}
	assert.ErrorIs(t, err, cause)
	encoded, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(encoded), "decode")
}
