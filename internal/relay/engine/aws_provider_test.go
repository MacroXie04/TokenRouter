package engine

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type awsRelayObservation struct {
	method                string
	path                  string
	authorization         string
	contentType           string
	body                  []byte
	reservationDispatched bool
}

type awsRelayRoundTripFunc func(*http.Request) (*http.Response, error)

func (function awsRelayRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func configureAWSAccountingChannel(t *testing.T, fixture relayAccountingFixture, baseURL, credential, otherSettings string) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeAws),
		"key":           credential,
		"base_url":      baseURL,
		"settings":      otherSettings,
		"model_mapping": `{"accounting-model":"claude-3-5-sonnet-20240620"}`,
		"status":        channelcatalog.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
}

func awsAccountingRequest(t *testing.T, token *model.Token) (*RelayInfo, *httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	maximum := 64
	context, recorder, info := newRelayAccountingContext(t, token, maximum)
	info.Request.Messages = []protocolkit.Message{{Role: "user", Content: "hello"}}
	info.Request.Extra = map[string]any{
		"model": "accounting-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "max_tokens": 64,
	}
	info.RawBody = []byte(`{"model":"accounting-model","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`)
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("Anthropic-Beta", "integration-beta")
	return info, recorder, context.Request
}

func TestAWSRelayReservesBeforeIOAndSettlesClaudeSemanticUsage(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	observed := make(chan awsRelayObservation, 1)
	client := &http.Client{Transport: awsRelayRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var reservation model.RelayQuotaReservationRecord
		dispatched := model.DB.Order("id desc").First(&reservation).Error == nil &&
			reservation.Status == model.RelayQuotaReservationStatusDispatched && reservation.ReservedQuota > 0
		observed <- awsRelayObservation{
			method: request.Method, path: request.URL.EscapedPath(), authorization: request.Header.Get("Authorization"),
			contentType: request.Header.Get("Content-Type"), body: body, reservationDispatched: dispatched,
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
			"id":"msg_integration","type":"message","role":"assistant",
			"content":[{"type":"text","text":"bedrock answer"}],"stop_reason":"end_turn",
			"model":"anthropic.claude-3-5-sonnet-20240620-v1:0",
			"usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}
		}`)),
			Request: request,
		}, nil
	})}

	previousClient := relayHTTPClient
	relayHTTPClient = client
	t.Cleanup(func() { relayHTTPClient = previousClient })
	configureAWSAccountingChannel(t, fixture, "https://bedrock.example.test", "bedrock-api-key|us-east-1", `{"aws_key_type":"api_key"}`)

	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"accounting-model": {Prompt: 1, Completion: 1}})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"accounting-model":"tiered_expr"}`,
		"ModelBillingExpr": `{"accounting-model":"p + c"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{"ModelBillingMode": `{}`, "ModelBillingExpr": `{}`})
	})

	info, recorder, request := awsAccountingRequest(t, &fixture.token)
	context := newRelayContextFromRequest(t, request, recorder, &fixture.token)
	err := relayAndSettle(context, middleware.CaptureRelayRequestState(context), info)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "bedrock answer")

	wire := <-observed
	assert.Equal(t, http.MethodPost, wire.method)
	assert.Equal(t, "/model/us.anthropic.claude-3-5-sonnet-20240620-v1:0/invoke", wire.path)
	assert.Equal(t, "Bearer bedrock-api-key", wire.authorization)
	assert.Equal(t, "application/json", wire.contentType)
	assert.True(t, wire.reservationDispatched, "quota must be reserved before the provider observes I/O")
	var providerRequest map[string]any
	require.NoError(t, json.Unmarshal(wire.body, &providerRequest))
	assert.Equal(t, "bedrock-2023-05-31", providerRequest["anthropic_version"])
	assert.Equal(t, []any{"integration-beta"}, providerRequest["anthropic_beta"])
	assert.NotContains(t, providerRequest, "model")
	assert.NotContains(t, providerRequest, "stream")

	// Claude-compatible prompt_tokens includes the five cached tokens. The
	// tiered p+c expression must bill p as the provider's ten uncached input
	// tokens, not the normalized fifteen-token aggregate: (10+4)*0.5 = 7.
	const actualQuota = 7
	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 100_000-actualQuota, user.Quota)
	assert.Equal(t, actualQuota, user.UsedQuota)
	assert.Equal(t, 100_000-actualQuota, token.RemainQuota)
	assert.Equal(t, actualQuota, token.UsedQuota)
	assert.Equal(t, int64(actualQuota), channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, actualQuota, reservation.ActualQuota)
	assert.Equal(t, 15, log.PromptTokens)
	assert.Equal(t, 4, log.CompletionTokens)
	assert.Equal(t, actualQuota, log.Quota)
}

func newRelayContextFromRequest(t *testing.T, request *http.Request, recorder *httptest.ResponseRecorder, token *model.Token) *gin.Context {
	t.Helper()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	requestctx.SetUserId(context, token.UserId)
	requestctx.SetUsername(context, "accounting-user")
	requestctx.SetUserGroup(context, "default")
	middleware.SetupRelayTokenContext(context, token)
	middleware.SetRelayGroupPolicy(context, billingsvc.RelayGroupPolicy{Groups: []string{"default"}})
	return context
}

func TestAWSRelayProviderFailureRefundsAndRedactsCredential(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	requests := 0
	client := &http.Client{Transport: awsRelayRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"message":"bedrock-api-key was denied","__type":"com.amazon#AccessDeniedException"}`)),
			Request:    request,
		}, nil
	})}
	previousClient := relayHTTPClient
	relayHTTPClient = client
	t.Cleanup(func() { relayHTTPClient = previousClient })
	configureAWSAccountingChannel(t, fixture, "https://bedrock.example.test", "bedrock-api-key|us-east-1", `{"aws_key_type":"api_key"}`)

	info, recorder, request := awsAccountingRequest(t, &fixture.token)
	context := newRelayContextFromRequest(t, request, recorder, &fixture.token)
	require.NoError(t, relayAndSettle(context, middleware.CaptureRelayRequestState(context), info))
	assert.Equal(t, 1, requests)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "bedrock-api-key")
	assert.Contains(t, recorder.Body.String(), "[REDACTED]")

	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, 100_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 100_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", billingsvc.LogTypeConsume).Count(&logs).Error)
	assert.Zero(t, logs)
}
