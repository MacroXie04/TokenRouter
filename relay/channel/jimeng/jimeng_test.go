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
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
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
