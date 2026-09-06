package relay_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const replicateClientModel = "gpt-provider-contract"

var replicateTinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}

func configureReplicateIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(constant.ChannelTypeReplicate), "key": "replicate-provider-secret",
		"models":        replicateClientModel,
		"model_mapping": `{"gpt-provider-contract":"black-forest-labs/flux-1.1-pro"}`,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, service.InitAbilityCache())
	return key, userID, channel
}

func enableReplicateFixedPrice(t *testing.T) int {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"gpt-provider-contract":"reference"}`,
		setting.PerCallModelPriceOption: `{"gpt-provider-contract":0.04}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{}`,
	}))
	return int(0.04 * float64(appcommon.QuotaPerUnit))
}

func assertReplicateSettled(t *testing.T, key string, userID int, channel model.Channel, wantQuota int) {
	t.Helper()
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, replicateClientModel, log.ModelName)
	assert.Equal(t, channel.Id, log.ChannelId)
	assert.Equal(t, wantQuota, log.Quota)
	assert.Positive(t, log.PromptTokens)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-wantQuota, user.Quota)
	assert.Equal(t, wantQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000-wantQuota, token.RemainQuota)
	assert.Equal(t, wantQuota, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, wantQuota, reservation.ActualQuota)
	assert.Equal(t, wantQuota, reservation.ReservedQuota)
	assert.Equal(t, wantQuota, reservation.TokenReserved)
}

func assertReplicateRefunded(t *testing.T, key string, userID int) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Zero(t, reservation.ActualQuota)
	assert.Positive(t, reservation.ReservedQuota)
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestReplicateGenerationWireBase64AndFixedPriceSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	var predictions atomic.Int32
	var downloads atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/gateway/v1/models/black-forest-labs/flux-1.1-pro/predictions":
			predictions.Add(1)
			assert.Equal(t, "Bearer replicate-provider-secret", request.Header.Get("Authorization"))
			assert.Equal(t, "wait", request.Header.Get("Prefer"))
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			var envelope struct {
				Input map[string]any `json:"input"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&envelope))
			assert.Equal(t, "a fox under northern lights", envelope.Input["prompt"])
			assert.Equal(t, "16:9", envelope.Input["aspect_ratio"])
			assert.Equal(t, true, envelope.Input["prompt_upsampling"])
			assert.EqualValues(t, 1, envelope.Input["num_outputs"])
			assert.EqualValues(t, 42, envelope.Input["seed"])
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"status":"succeeded","output":"`+upstream.URL+`/generated.png"}`)
		case "/generated.png":
			downloads.Add(1)
			assert.Empty(t, request.Header.Get("Authorization"), "provider credentials must not be sent to output hosts")
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(replicateTinyPNG)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	key, userID, channel := configureReplicateIntegration(t, upstream.URL+"/gateway")
	wantQuota := enableReplicateFixedPrice(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"gpt-provider-contract","prompt":"a fox under northern lights","size":"1792x1024","quality":"hd","response_format":"b64_json","input":{"seed":42}}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int32(1), predictions.Load())
	assert.Equal(t, int32(1), downloads.Load())
	assert.Contains(t, response.Body.String(), base64.StdEncoding.EncodeToString(replicateTinyPNG))
	assert.NotContains(t, response.Body.String(), upstream.URL)
	assertReplicateSettled(t, key, userID, channel, wantQuota)
}

func TestReplicateMultipartEditUploadWireAndSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	var uploads atomic.Int32
	var predictions atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploads.Add(1)
			assert.Equal(t, "Bearer replicate-provider-secret", request.Header.Get("Authorization"))
			require.NoError(t, request.ParseMultipartForm(9<<20))
			file, header, err := request.FormFile("content")
			require.NoError(t, err)
			defer file.Close()
			assert.Equal(t, "image.png", header.Filename)
			assert.Equal(t, "image/png", header.Header.Get("Content-Type"))
			got, err := io.ReadAll(file)
			require.NoError(t, err)
			assert.Equal(t, replicateTinyPNG, got)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"urls":{"get":"`+upstream.URL+`/uploaded.png"}}`)
		case "/v1/models/black-forest-labs/flux-1.1-pro/predictions":
			predictions.Add(1)
			assert.Equal(t, "Bearer replicate-provider-secret", request.Header.Get("Authorization"))
			var envelope struct {
				Input map[string]any `json:"input"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&envelope))
			assert.Equal(t, "make this blue", envelope.Input["prompt"])
			assert.Equal(t, upstream.URL+"/uploaded.png", envelope.Input["image_prompt"])
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"succeeded","output":["https://cdn.example/edited.png"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	key, userID, channel := configureReplicateIntegration(t, upstream.URL)
	wantQuota := enableReplicateFixedPrice(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", replicateClientModel))
	require.NoError(t, writer.WriteField("prompt", "make this blue"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write(replicateTinyPNG)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int32(1), uploads.Load())
	assert.Equal(t, int32(1), predictions.Load())
	assert.Contains(t, response.Body.String(), "https://cdn.example/edited.png")
	assertReplicateSettled(t, key, userID, channel, wantQuota)
}

func TestReplicateAcceptedPredictionWithUnsafeResultSettlesInsteadOfRefunding(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	var predictions atomic.Int32
	var downloads atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models/black-forest-labs/flux-1.1-pro/predictions":
			predictions.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"succeeded","output":"`+upstream.URL+`/not-an-image"}`)
		case "/not-an-image":
			downloads.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "not image data")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	key, userID, channel := configureReplicateIntegration(t, upstream.URL)
	wantQuota := enableReplicateFixedPrice(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"gpt-provider-contract","prompt":"accepted work","response_format":"b64_json"}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
	assert.Equal(t, int32(1), predictions.Load())
	assert.Equal(t, int32(1), downloads.Load())
	assertReplicateSettled(t, key, userID, channel, wantQuota)
}

func TestReplicateUploadFailureRefundsAndNeverStartsPrediction(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	var uploads atomic.Int32
	var predictions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"replicate-provider-secret rejected upload"}}`)
		default:
			predictions.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()

	key, userID, _ := configureReplicateIntegration(t, upstream.URL)
	enableReplicateFixedPrice(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", replicateClientModel))
	require.NoError(t, writer.WriteField("prompt", "edit this"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write(replicateTinyPNG)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
	assert.Equal(t, int32(1), uploads.Load())
	assert.Zero(t, predictions.Load())
	assert.NotContains(t, response.Body.String(), "replicate-provider-secret")
	assert.Contains(t, response.Body.String(), "[REDACTED]")
	assertReplicateRefunded(t, key, userID)
}

func TestReplicateFailuresRefundAndNeverLeakCredentials(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name         string
		upstreamCode int
		upstreamBody string
	}{
		{name: "HTTP error", upstreamCode: http.StatusTooManyRequests, upstreamBody: `{"error":{"message":"replicate-provider-secret is rate limited"}}`},
		{name: "prediction error", upstreamCode: http.StatusOK, upstreamBody: `{"status":"failed","error":{"message":"replicate-provider-secret rejected prediction"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.upstreamCode)
				_, _ = io.WriteString(w, test.upstreamBody)
			}))
			defer upstream.Close()
			key, userID, _ := configureReplicateIntegration(t, upstream.URL)
			enableReplicateFixedPrice(t)
			request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
				`{"model":"gpt-provider-contract","prompt":"draw a fox"}`,
			))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			assert.NotEqual(t, http.StatusOK, response.Code)
			assert.Equal(t, int32(1), calls.Load())
			assert.NotContains(t, response.Body.String(), "replicate-provider-secret")
			assert.Contains(t, response.Body.String(), "[REDACTED]")
			assertReplicateRefunded(t, key, userID)
		})
	}
}

func TestReplicateUnsupportedModeAndInvalidCountFailBeforeUpstream(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`},
		{name: "excessive image count", path: "/v1/images/generations", body: `{"model":"gpt-provider-contract","prompt":"fox","n":9}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			key, userID, _ := configureReplicateIntegration(t, upstream.URL)
			enableReplicateFixedPrice(t)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			assert.NotEqual(t, http.StatusOK, response.Code)
			assert.Zero(t, calls.Load())
			assertReplicateRefunded(t, key, userID)
		})
	}
}
