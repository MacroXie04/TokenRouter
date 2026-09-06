package jimeng

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type closeErrorBody struct {
	io.Reader
}

func (closeErrorBody) Close() error {
	return assert.AnError
}

func TestJimengCredentialTransportRequiresSafeHTTPSBase(t *testing.T) {
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	common.InitSSRF()
	client := &Client{}

	for _, baseURL := range []string{
		"http://provider.example",
		"https://user:password@provider.example",
		"https://provider.example?secret=in-url",
		"https://provider.example#fragment",
	} {
		_, err := client.newRequest(t.Context(), baseURL, "access-key|secret-key", SubmitAction, []byte(`{}`))
		require.Error(t, err, baseURL)
	}

	request, err := client.newRequest(t.Context(), "https://provider.example/base", "access-key|secret-key", SubmitAction, []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, "https", request.URL.Scheme)
}

func TestPrepareSubmitRequest(t *testing.T) {
	prepared, err := PrepareSubmitRequest(Request{ReqKey: "ignored", Prompt: "animate this"}, "jimeng_v30p")
	require.NoError(t, err)
	assert.Equal(t, "jimeng_t2v_v30p", prepared.ReqKey)
	assert.Equal(t, DefaultFrames, prepared.Frames)

	prepared, err = PrepareSubmitRequest(Request{
		Prompt: "animate", ImageURLs: []string{"https://example.com/start.png"}, Frames: LongVideoFrames,
	}, "jimeng_v30p")
	require.NoError(t, err)
	assert.Equal(t, "jimeng_i2v_first_v30", prepared.ReqKey)
	assert.Equal(t, LongVideoFrames, prepared.Frames)

	prepared, err = PrepareSubmitRequest(Request{
		Prompt: "animate", ImageURLs: []string{"https://example.com/start.png", "https://example.com/end.png"},
	}, "jimeng_v30p")
	require.NoError(t, err)
	assert.Equal(t, "jimeng_i2v_first_tail_v30", prepared.ReqKey)

	_, err = PrepareSubmitRequest(Request{Prompt: "animate", Frames: 999}, "jimeng-model")
	assert.EqualError(t, err, "frames must be 121 or 241")
	_, err = PrepareSubmitRequest(Request{Prompt: "animate", BinaryDataBase64: []string{"not-base64"}}, "jimeng-model")
	assert.EqualError(t, err, "binary_data_base64 contains invalid base64")
	_, err = PrepareSubmitRequest(Request{
		Prompt: "animate", BinaryDataBase64: []string{"YQ=="}, ImageURLs: []string{"https://example.com/a.png"},
	}, "jimeng-model")
	assert.EqualError(t, err, "binary_data_base64 and image_urls are mutually exclusive")
}

func TestClientSignsDirectAndGatewayRequests(t *testing.T) {
	fixedTime := time.Date(2026, time.August, 14, 12, 34, 56, 0, time.UTC)
	var directRequest *http.Request
	directClient := &Client{
		Now: func() time.Time { return fixedTime },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			directRequest = request.Clone(request.Context())
			return jsonResponse(http.StatusOK, `{"code":10000,"message":"success","request_id":"req-direct","data":{"task_id":"upstream-1"}}`), nil
		})},
	}
	result, _, err := directClient.Submit(t.Context(), "https://visual.example.test", "access|secret", Request{
		ReqKey: "jimeng-model", Prompt: "animate", Frames: DefaultFrames,
	})
	require.NoError(t, err)
	assert.Equal(t, "upstream-1", result.Data.TaskID)
	require.NotNil(t, directRequest)
	assert.Equal(t, "/", directRequest.URL.Path)
	assert.Equal(t, SubmitAction, directRequest.URL.Query().Get("Action"))
	assert.Equal(t, APIVersion, directRequest.URL.Query().Get("Version"))
	assert.Equal(t, "20260814T123456Z", directRequest.Header.Get("X-Date"))
	assert.NotEmpty(t, directRequest.Header.Get("X-Content-Sha256"))
	assertValidAuthorization(t, directRequest, "access", "secret", fixedTime)

	var gatewayRequest *http.Request
	gatewayClient := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gatewayRequest = request.Clone(request.Context())
		return jsonResponse(http.StatusOK, `{"code":10000,"message":"success","request_id":"req-gateway","data":{"task_id":"upstream-2"}}`), nil
	})}}
	_, _, err = gatewayClient.Submit(t.Context(), "https://gateway.example.test/base", "sk-upstream", Request{
		ReqKey: "jimeng-model", Prompt: "animate", Frames: DefaultFrames,
	})
	require.NoError(t, err)
	require.NotNil(t, gatewayRequest)
	assert.Equal(t, "/base/jimeng/", gatewayRequest.URL.Path)
	assert.Equal(t, "Bearer sk-upstream", gatewayRequest.Header.Get("Authorization"))
	assert.Empty(t, gatewayRequest.Header.Get("X-Date"))
}

func TestSubmitOutcomeClassification(t *testing.T) {
	payload := Request{ReqKey: "jimeng-model", Prompt: "animate", Frames: DefaultFrames}

	_, _, err := (&Client{}).Submit(t.Context(), "not-a-url", "access|secret", payload)
	require.Error(t, err)
	assert.False(t, SubmitWasDispatched(err))
	assert.False(t, SubmitMayHaveBeenAccepted(err))

	for _, test := range []struct {
		name            string
		response        *http.Response
		dispatched      bool
		mayHaveAccepted bool
	}{
		{name: "client rejection", response: jsonResponse(http.StatusBadRequest, `{"error":"bad request"}`), dispatched: true},
		{name: "redirect", response: jsonResponse(http.StatusTemporaryRedirect, ""), dispatched: true, mayHaveAccepted: true},
		{name: "server failure", response: jsonResponse(http.StatusServiceUnavailable, `{"error":"unavailable"}`), dispatched: true, mayHaveAccepted: true},
		{name: "provider rejection", response: jsonResponse(http.StatusOK, `{"code":50400,"message":"invalid request"}`), dispatched: true},
		{name: "malformed success", response: jsonResponse(http.StatusOK, `not-json`), dispatched: true, mayHaveAccepted: true},
		{name: "missing task id", response: jsonResponse(http.StatusOK, `{"code":10000,"message":"success","data":{}}`), dispatched: true, mayHaveAccepted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return test.response, nil
			})}}
			_, _, err := client.Submit(t.Context(), "https://visual.example.test", "access|secret", payload)
			require.Error(t, err)
			assert.Equal(t, test.dispatched, SubmitWasDispatched(err))
			assert.Equal(t, test.mayHaveAccepted, SubmitMayHaveBeenAccepted(err))
		})
	}
}

func TestSubmitPreservesAcceptedResponseWhenCloseFailsAfterFullRead(t *testing.T) {
	payload := Request{ReqKey: "jimeng-model", Prompt: "animate", Frames: DefaultFrames}
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: closeErrorBody{Reader: strings.NewReader(
				`{"code":10000,"message":"success","data":{"task_id":"accepted-before-close-error"}}`,
			)},
		}, nil
	})}}

	result, raw, err := client.Submit(t.Context(), "https://visual.example.test", "access|secret", payload)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "accepted-before-close-error", result.Data.TaskID)
	assert.Contains(t, string(raw), "accepted-before-close-error")
}

func TestSubmitAcceptsSuccessfulNon200TwoXXResponses(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			payload := Request{ReqKey: "jimeng-model", Prompt: "animate", Frames: DefaultFrames}
			client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(status, `{"code":10000,"data":{"task_id":"accepted-two-xx"}}`), nil
			})}}
			result, _, err := client.Submit(t.Context(), "https://visual.example.test", "access|secret", payload)
			require.NoError(t, err)
			assert.Equal(t, "accepted-two-xx", result.Data.TaskID)
		})
	}
}

func TestJimengResponseAndProviderIDRespectDurableTextLimits(t *testing.T) {
	payload := Request{ReqKey: "jimeng-model", Prompt: "animate", Frames: DefaultFrames}
	t.Run("response body", func(t *testing.T) {
		client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, strings.Repeat("x", MaxDurableResponseBytes+1)), nil
		})}}
		_, _, err := client.Submit(t.Context(), "https://visual.example.test", "access|secret", payload)
		require.ErrorContains(t, err, "durable storage limit")
		assert.True(t, SubmitWasDispatched(err))
		assert.True(t, SubmitMayHaveBeenAccepted(err),
			"an oversized response must never make a dispatched submit retryable")
	})

	t.Run("oversized server error preserves retryable status", func(t *testing.T) {
		client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusServiceUnavailable, strings.Repeat("x", MaxDurableResponseBytes+1)), nil
		})}}
		_, _, err := client.Fetch(t.Context(), "https://visual.example.test", "access|secret", "jimeng-model", "provider-id")
		var upstream *relaycommon.UpstreamError
		require.ErrorAs(t, err, &upstream)
		assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
		assert.False(t, IsDurableResponseError(err), "an oversized 5xx is still an HTTP retry outcome")
	})

	t.Run("provider task id", func(t *testing.T) {
		body := `{"code":10000,"message":"success","data":{"task_id":"` +
			strings.Repeat("i", MaxProviderTaskIDBytes+1) + `"}}`
		client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, body), nil
		})}}
		_, _, err := client.Submit(t.Context(), "https://visual.example.test", "access|secret", payload)
		require.ErrorContains(t, err, "task_id exceeds durable storage limit")
		assert.True(t, SubmitWasDispatched(err))
		assert.True(t, SubmitMayHaveBeenAccepted(err),
			"a successful response with an unusable id must terminate submit retries")
	})
}

func assertValidAuthorization(t *testing.T, request *http.Request, accessKey, secretKey string, now time.Time) {
	t.Helper()
	signedHeaders := "content-type;host;x-content-sha256;x-date"
	canonicalHeaders := "content-type:" + request.Header.Get("Content-Type") + "\n" +
		"host:" + request.URL.Host + "\n" +
		"x-content-sha256:" + request.Header.Get("X-Content-Sha256") + "\n" +
		"x-date:" + request.Header.Get("X-Date") + "\n"
	canonical := strings.Join([]string{
		request.Method, request.URL.EscapedPath(), request.URL.Query().Encode(), canonicalHeaders,
		signedHeaders, request.Header.Get("X-Content-Sha256"),
	}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonical))
	shortDate := now.UTC().Format("20060102")
	xDate := now.UTC().Format("20060102T150405Z")
	scope := shortDate + "/cn-north-1/cv/request"
	stringToSign := "HMAC-SHA256\n" + xDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	dateKey := testHMAC([]byte(secretKey), []byte(shortDate))
	regionKey := testHMAC(dateKey, []byte("cn-north-1"))
	serviceKey := testHMAC(regionKey, []byte("cv"))
	signingKey := testHMAC(serviceKey, []byte("request"))
	signature := hex.EncodeToString(testHMAC(signingKey, []byte(stringToSign)))
	expected := "HMAC-SHA256 Credential=" + accessKey + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	assert.Equal(t, expected, request.Header.Get("Authorization"))
}

func testHMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
