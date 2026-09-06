package engine_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	jimengImageClientModel = "gpt-provider-contract"
	jimengImageCredential  = "jimeng-access|jimeng-secret"
)

func configureJimengImageIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeJimeng),
		"key":           jimengImageCredential,
		"models":        jimengImageClientModel,
		"model_mapping": `{"gpt-provider-contract":"jimeng_high_aes_general_v21_L"}`,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	return key, userID, channel
}

func enableJimengImageFixedPrice(t *testing.T) int {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"gpt-provider-contract":"reference"}`,
		setting.PerCallModelPriceOption: `{"gpt-provider-contract":0.04}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{}`,
	}))
	return int(0.04 * float64(quotamath.QuotaPerUnit))
}

func assertJimengImageSettled(t *testing.T, key string, userID int, channel model.Channel, wantQuota int) {
	t.Helper()
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, jimengImageClientModel, log.ModelName)
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
}

func assertJimengImageRefunded(t *testing.T, key string, userID int) {
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
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestJimengImageWireResponseAndFixedPriceSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	encodedImage := base64.StdEncoding.EncodeToString([]byte("jimeng-image"))
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/", request.URL.Path)
		assert.Equal(t, "CVProcess", request.URL.Query().Get("Action"))
		assert.Equal(t, jimeng.APIVersion, request.URL.Query().Get("Version"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.NotEmpty(t, request.Header.Get("X-Date"))
		assert.Contains(t, request.Header.Get("Authorization"), "HMAC-SHA256 Credential=jimeng-access/")
		assert.NotContains(t, request.Header.Get("Authorization"), "jimeng-secret")
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		digest := sha256.Sum256(body)
		assert.Equal(t, hex.EncodeToString(digest[:]), request.Header.Get("X-Content-Sha256"))
		var wire map[string]any
		require.NoError(t, json.Unmarshal(body, &wire))
		assert.Equal(t, jimeng.ImageModel, wire["req_key"])
		assert.Equal(t, "a fox in snow", wire["prompt"])
		assert.EqualValues(t, 42, wire["seed"])
		assert.EqualValues(t, 512, wire["width"])
		assert.EqualValues(t, 512, wire["height"])
		assert.NotContains(t, wire, "model")
		assert.NotContains(t, wire, "group")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":10000,"message":"success","data":{"binary_data_base64":["`+encodedImage+`"]}}`)
	}))
	defer upstream.Close()

	key, userID, channel := configureJimengImageIntegration(t, upstream.URL)
	wantQuota := enableJimengImageFixedPrice(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"gpt-provider-contract","prompt":"a fox in snow","n":1,"seed":42,"size":"512x512","response_format":"b64_json","group":"dashboard-only"}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int32(1), calls.Load())
	assert.Contains(t, response.Body.String(), encodedImage)
	assertJimengImageSettled(t, key, userID, channel, wantQuota)
}

func TestJimengImageDefinitiveRejectionRefundsAndRedactsCredential(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":50400,"message":"jimeng-access and jimeng-secret rejected"}`)
	}))
	defer upstream.Close()
	key, userID, _ := configureJimengImageIntegration(t, upstream.URL)
	enableJimengImageFixedPrice(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"gpt-provider-contract","prompt":"a fox"}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
	assert.Equal(t, int32(1), calls.Load())
	assert.NotContains(t, response.Body.String(), "jimeng-access")
	assert.NotContains(t, response.Body.String(), "jimeng-secret")
	assert.Contains(t, response.Body.String(), "[REDACTED]")
	assertJimengImageRefunded(t, key, userID)
}

func TestJimengImageAcceptedAmbiguousResponseSettlesWithoutReplay(t *testing.T) {
	t.Setenv("RETRY_TIMES", "3")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"image_urls":["https://cdn.example/image.png"]}}`)
	}))
	defer upstream.Close()
	key, userID, channel := configureJimengImageIntegration(t, upstream.URL)
	wantQuota := enableJimengImageFixedPrice(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"gpt-provider-contract","prompt":"accepted work"}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
	assert.Equal(t, int32(1), calls.Load(), "an accepted ambiguous result must not be replayed")
	assertJimengImageSettled(t, key, userID, channel, wantQuota)
}

func TestJimengImageUnsupportedModeFailsBeforeUpstreamAndRefunds(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	key, userID, _ := configureJimengImageIntegration(t, upstream.URL)
	enableJimengImageFixedPrice(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
	assert.Zero(t, calls.Load())
	assertJimengImageRefunded(t, key, userID)
}
