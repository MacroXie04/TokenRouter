package engine

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/xunfei"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const xunfeiLifecycleCredential = "app-id-1234|api-secret-5678|api-key-9012"

type xunfeiLifecycleWire struct {
	URL  string
	Body []byte
}

func xunfeiLifecycleDialer(t *testing.T, frames []string) (xunfei.DialContextFunc, <-chan xunfeiLifecycleWire) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	bodyChannel := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_, body, err := connection.ReadMessage()
		if err != nil {
			return
		}
		bodyChannel <- append([]byte(nil), body...)
		for _, frame := range frames {
			if err := connection.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	localURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wires := make(chan xunfeiLifecycleWire, 1)
	dial := func(ctx context.Context, signedURL string, header http.Header) (*websocket.Conn, *http.Response, error) {
		connection, response, err := websocket.DefaultDialer.DialContext(ctx, localURL, header)
		if err != nil {
			return connection, response, err
		}
		go func() {
			wires <- xunfeiLifecycleWire{URL: signedURL, Body: <-bodyChannel}
		}()
		return connection, response, nil
	}
	return dial, wires
}

func xunfeiTerminalFrame(code, prompt, completion int, content, message string) string {
	total := prompt + completion
	body, _ := json.Marshal(map[string]any{
		"header": map[string]any{"code": code, "message": message, "sid": "sid", "status": 2},
		"payload": map[string]any{
			"choices": map[string]any{
				"status": 2, "seq": 0,
				"text": []any{map[string]any{"content": content, "role": "assistant", "index": 0}},
			},
			"usage": map[string]any{"text": map[string]any{
				"question_tokens": prompt, "prompt_tokens": prompt,
				"completion_tokens": completion, "total_tokens": total,
			}},
		},
	})
	return string(body)
}

func configureXunfeiLifecycle(t *testing.T) (relayAccountingFixture, *gin.Context, *httptest.ResponseRecorder, *RelayInfo) {
	t.Helper()
	fixture := newRelayAccountingFixture(t, 500_000, 500_000)
	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"accounting-model": {Prompt: 1, Completion: 2},
	})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })
	mapping, err := json.Marshal(map[string]string{"accounting-model": "SparkDesk-v4.0"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeXunfei), "key": xunfeiLifecycleCredential,
		"base_url": "http://127.0.0.1:1/credential-collector", "model_mapping": string(mapping),
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 32)
	info.Request.Messages = []protocolkit.Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "hello"}}
	info.Request.Extra["messages"] = []any{
		map[string]any{"role": "system", "content": "rules"},
		map[string]any{"role": "user", "content": "hello"},
	}
	return fixture, c, recorder, info
}

func dispatchXunfeiLifecycle(adaptor *xunfei.Adaptor) func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error) {
	return func(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
		meta := &relaycommon.Meta{
			Context: c.Request.Context(), Channel: info.Channel, Mode: info.Mode,
			Format:      relaycommon.GetRelayFormat(channelcatalog.ChannelType(info.Channel.Type), info.Mode),
			RequestPath: c.Request.URL.Path, OriginalModelName: info.ModelName,
			ModelName: relaycommon.GetMappedModel(info.Channel, info.ModelName),
			BaseURL:   info.Channel.BaseURL, APIKey: channelssvc.GetChannelKey(info.Channel),
			ClientHeaders: c.Request.Header.Clone(), Request: info.Request,
			RawBody: info.RawBody, RequestContentType: info.RequestContentType,
			APIVersion: info.APIVersion, IsStream: info.IsStream, PromptTokens: info.PromptTokens,
		}
		adaptor.Init(meta)
		requestURL, err := adaptor.GetRequestURL(meta)
		if err != nil {
			return nil, err
		}
		body, err := adaptor.ConvertRequest(meta)
		if err != nil {
			return nil, err
		}
		return adaptor.DoDirectRequest(c, requestURL, body, meta)
	}
}

func TestXunfeiLifecycleSettlesExactUsageAndUsesFixedSignedWire(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	assert.IsType(t, &xunfei.Adaptor{}, GetAdaptor(channelcatalog.ChannelTypeXunfei))
	fixture, c, recorder, info := configureXunfeiLifecycle(t)
	dial, wires := xunfeiLifecycleDialer(t, []string{xunfeiTerminalFrame(0, 7, 3, "spark answer", "")})
	adaptor := &xunfei.Adaptor{DialContext: dial, Now: func() time.Time {
		return time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	}}
	require.NoError(t, relayAndSettleWithDispatch(c, info, dispatchXunfeiLifecycle(adaptor)))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"content":"spark answer"`)
	assert.NotContains(t, recorder.Body.String(), "app-id-1234")
	assert.NotContains(t, recorder.Body.String(), "api-secret-5678")
	assert.NotContains(t, recorder.Body.String(), "api-key-9012")

	wire := <-wires
	assert.True(t, strings.HasPrefix(wire.URL, "wss://spark-api.xf-yun.com/v4.0/chat?"))
	assert.NotContains(t, wire.URL, "127.0.0.1")
	var providerBody map[string]any
	require.NoError(t, json.Unmarshal(wire.Body, &providerBody))
	assert.Equal(t, "app-id-1234", providerBody["header"].(map[string]any)["app_id"])
	assert.Equal(t, "4.0Ultra", providerBody["parameter"].(map[string]any)["chat"].(map[string]any)["domain"])
	providerMessages := providerBody["payload"].(map[string]any)["message"].(map[string]any)["text"].([]any)
	require.Len(t, providerMessages, 3)
	assert.Equal(t, "Okay", providerMessages[1].(map[string]any)["content"])

	expectedQuota := billingsvc.ComputeQuota("accounting-model", "default", 7, 3)
	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	require.NoError(t, model.DB.Where("user_id = ?", fixture.user.Id).First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", fixture.user.Id, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 500_000-expectedQuota, user.Quota)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 500_000-expectedQuota, token.RemainQuota)
	assert.Equal(t, expectedQuota, token.UsedQuota)
	assert.EqualValues(t, expectedQuota, channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Equal(t, 7, log.PromptTokens)
	assert.Equal(t, 3, log.CompletionTokens)
	assert.Equal(t, expectedQuota, log.Quota)
	assert.NotContains(t, log.Other, xunfeiLifecycleCredential)
}

func TestXunfeiStreamingLifecycleSettlesTerminalUsage(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	fixture, c, recorder, info := configureXunfeiLifecycle(t)
	info.Request.Stream = true
	dial, _ := xunfeiLifecycleDialer(t, []string{xunfeiTerminalFrame(0, 5, 2, "stream answer", "")})
	adaptor := &xunfei.Adaptor{DialContext: dial, Now: func() time.Time {
		return time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	}}
	require.NoError(t, relayAndSettleWithDispatch(c, info, dispatchXunfeiLifecycle(adaptor)))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"content":"stream answer"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]\n\n"))
	assert.NotContains(t, recorder.Body.String(), xunfeiLifecycleCredential)

	expectedQuota := billingsvc.ComputeQuota("accounting-model", "default", 5, 2)
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.Where("user_id = ?", fixture.user.Id).First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", fixture.user.Id, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Equal(t, 5, log.PromptTokens)
	assert.Equal(t, 2, log.CompletionTokens)
	assert.Equal(t, expectedQuota, log.Quota)
	assert.True(t, log.IsStream)
}

func TestXunfeiProviderFailureIsSanitizedAndRefundsReservation(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	fixture, c, recorder, info := configureXunfeiLifecycle(t)
	dial, _ := xunfeiLifecycleDialer(t, []string{
		xunfeiTerminalFrame(10013, 0, 0, "", "api-secret-5678 api-key-9012"),
	})
	adaptor := &xunfei.Adaptor{DialContext: dial}
	require.NoError(t, relayAndSettleWithDispatch(c, info, dispatchXunfeiLifecycle(adaptor)))
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "app-id-1234")
	assert.NotContains(t, recorder.Body.String(), "api-secret-5678")
	assert.NotContains(t, recorder.Body.String(), "api-key-9012")

	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.Where("user_id = ?", fixture.user.Id).First(&reservation).Error)
	assert.Equal(t, 500_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 500_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Zero(t, reservation.ActualQuota)
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", fixture.user.Id, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}
