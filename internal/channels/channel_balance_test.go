package channels

import (
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type balanceTestRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip balanceTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func setupChannelBalanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "channel-balance.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}))
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
	})
	return db
}

func useChannelBalanceTransport(t *testing.T, transport balanceTestRoundTripper) {
	t.Helper()
	previous := channelUpstreamHTTPClient
	channelUpstreamHTTPClient = &http.Client{Transport: transport}
	t.Cleanup(func() { channelUpstreamHTTPClient = previous })
}

func channelBalanceResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func createChannelBalanceFixture(t *testing.T, channelType channelcatalog.ChannelType, settingJSON string) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Type:               int(channelType),
		Name:               "balance-fixture",
		Key:                "primary-secret\nsecondary-secret",
		Status:             channelcatalog.ChannelStatusEnabled,
		Balance:            91.25,
		BalanceUpdatedTime: 1234,
		Setting:            settingJSON,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

func assertChannelBalanceUnchanged(t *testing.T, channel *model.Channel) {
	t.Helper()
	assert.Equal(t, 91.25, channel.Balance, "the caller's channel snapshot must remain unchanged")
	assert.Equal(t, int64(1234), channel.BalanceUpdatedTime, "the caller's timestamp must remain unchanged")
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, 91.25, stored.Balance, "a failed fetch must not update the stored balance")
	assert.Equal(t, int64(1234), stored.BalanceUpdatedTime, "a failed fetch must not update the stored timestamp")
}

func TestFetchChannelBalanceProviderContracts(t *testing.T) {
	setupChannelBalanceTestDB(t)
	moonshotPrice, err := setting.GetTopUpPriceChecked()
	require.NoError(t, err)

	tests := []struct {
		name            string
		channelType     channelcatalog.ChannelType
		settingJSON     string
		wantURL         string
		wantHeader      string
		wantHeaderValue string
		absentHeader    string
		body            string
		wantBalance     float64
	}{
		{
			name: "AIProxy", channelType: channelcatalog.ChannelTypeAIProxy,
			wantURL:    "https://aiproxy.io/api/report/getUserOverview",
			wantHeader: "Api-Key", wantHeaderValue: "primary-secret", absentHeader: "Authorization",
			body: `{"success":true,"data":{"totalPoints":18.25}}`, wantBalance: 18.25,
		},
		{
			name: "API2GPT", channelType: channelcatalog.ChannelTypeAPI2GPT,
			wantURL:    "https://api.api2gpt.com/dashboard/billing/credit_grants",
			wantHeader: "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body: `{"total_remaining":17.5}`, wantBalance: 17.5,
		},
		{
			name: "AIGC2D", channelType: channelcatalog.ChannelTypeAIGC2D,
			wantURL:    "https://api.aigc2d.com/dashboard/billing/credit_grants",
			wantHeader: "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body: `{"total_available":16.75}`, wantBalance: 16.75,
		},
		{
			name: "SiliconFlow", channelType: channelcatalog.ChannelTypeSiliconFlow,
			wantURL:    "https://api.siliconflow.cn/v1/user/info",
			wantHeader: "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body: `{"code":20000,"data":{"totalBalance":"15.5"}}`, wantBalance: 15.5,
		},
		{
			name: "DeepSeek", channelType: channelcatalog.ChannelTypeDeepSeek,
			wantURL:    "https://api.deepseek.com/user/balance",
			wantHeader: "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body:        `{"balance_infos":[{"currency":"USD","total_balance":"1"},{"currency":"CNY","total_balance":"14.25"}]}`,
			wantBalance: 14.25,
		},
		{
			name: "OpenRouter", channelType: channelcatalog.ChannelTypeOpenRouter,
			wantURL:    "https://openrouter.ai/api/v1/credits",
			wantHeader: "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body: `{"data":{"total_credits":12.5,"total_usage":3}}`, wantBalance: 9.5,
		},
		{
			name: "Moonshot", channelType: channelcatalog.ChannelTypeMoonshot,
			wantURL:    "https://api.moonshot.cn/v1/users/me/balance",
			wantHeader: "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body: `{"code":0,"status":true,"data":{"available_balance":73}}`, wantBalance: 73 / moonshotPrice,
		},
		{
			name: "configured balance_url", channelType: channelcatalog.ChannelTypeAzure,
			settingJSON: `{"balance_url":"https://balance.example.test/custom/credits?view=available"}`,
			wantURL:     "https://balance.example.test/custom/credits?view=available",
			wantHeader:  "Authorization", wantHeaderValue: "Bearer primary-secret", absentHeader: "Api-Key",
			body: `{"data":{"available_balance":"12.125"}}`, wantBalance: 12.125,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := createChannelBalanceFixture(t, test.channelType, test.settingJSON)
			calls := 0
			useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, http.MethodGet, request.Method)
				assert.Equal(t, test.wantURL, request.URL.String())
				assert.Equal(t, test.wantHeaderValue, request.Header.Get(test.wantHeader))
				assert.Empty(t, request.Header.Values(test.absentHeader))
				return channelBalanceResponse(request, http.StatusOK, test.body), nil
			})

			balance, err := FetchChannelBalanceContext(context.Background(), channel)
			require.NoError(t, err)
			assert.InDelta(t, test.wantBalance, balance, 1e-9)
			assert.Equal(t, 1, calls)
			assert.InDelta(t, test.wantBalance, channel.Balance, 1e-9)
			assert.Greater(t, channel.BalanceUpdatedTime, int64(1234))

			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			assert.InDelta(t, test.wantBalance, stored.Balance, 1e-9)
			assert.Equal(t, channel.BalanceUpdatedTime, stored.BalanceUpdatedTime)
		})
	}
}

func TestFetchChannelBalancePreservesFiniteNegativeProviderBalances(t *testing.T) {
	setupChannelBalanceTestDB(t)
	tests := []struct {
		name        string
		channelType channelcatalog.ChannelType
		body        string
		want        float64
	}{
		{name: "numeric response", channelType: channelcatalog.ChannelTypeAPI2GPT, body: `{"total_remaining":-2.5}`, want: -2.5},
		{name: "text response", channelType: channelcatalog.ChannelTypeSiliconFlow, body: `{"code":20000,"data":{"totalBalance":"-4.75"}}`, want: -4.75},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := createChannelBalanceFixture(t, test.channelType, "")
			useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
				return channelBalanceResponse(request, http.StatusOK, test.body), nil
			})
			balance, err := FetchChannelBalanceContext(context.Background(), channel)
			require.NoError(t, err)
			assert.Equal(t, test.want, balance)
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			assert.Equal(t, test.want, stored.Balance)
		})
	}
}

func TestConvertMoonshotBalanceMatchesReferenceDecimalArithmetic(t *testing.T) {
	balance, err := convertMoonshotBalance(0.3, 0.1)
	require.NoError(t, err)
	assert.Equal(t, 3.0, balance)
}

func TestFetchChannelBalanceRejectsMalformedProviderResponsesWithoutMutation(t *testing.T) {
	setupChannelBalanceTestDB(t)

	tooManyDeepSeekBalances := make([]string, 33)
	for index := range tooManyDeepSeekBalances {
		tooManyDeepSeekBalances[index] = `{"currency":"CNY","total_balance":"1"}`
	}
	tests := []struct {
		name        string
		channelType channelcatalog.ChannelType
		body        string
	}{
		{name: "malformed JSON", channelType: channelcatalog.ChannelTypeAIProxy, body: `{"success":`},
		{name: "AIProxy reports failure", channelType: channelcatalog.ChannelTypeAIProxy, body: `{"success":false,"error_code":401}`},
		{name: "AIProxy omits points", channelType: channelcatalog.ChannelTypeAIProxy, body: `{"success":true,"data":{}}`},
		{name: "API2GPT omits remaining", channelType: channelcatalog.ChannelTypeAPI2GPT, body: `{}`},
		{name: "AIGC2D oversized value", channelType: channelcatalog.ChannelTypeAIGC2D, body: `{"total_available":1000000000000001}`},
		{name: "SiliconFlow reports failure", channelType: channelcatalog.ChannelTypeSiliconFlow, body: `{"code":40001,"data":{"totalBalance":"1"}}`},
		{name: "SiliconFlow non-finite text", channelType: channelcatalog.ChannelTypeSiliconFlow, body: `{"code":20000,"data":{"totalBalance":"NaN"}}`},
		{name: "DeepSeek has no CNY", channelType: channelcatalog.ChannelTypeDeepSeek, body: `{"balance_infos":[{"currency":"USD","total_balance":"1"}]}`},
		{name: "DeepSeek cardinality is bounded", channelType: channelcatalog.ChannelTypeDeepSeek, body: `{"balance_infos":[` + strings.Join(tooManyDeepSeekBalances, ",") + `]}`},
		{name: "DeepSeek non-finite text", channelType: channelcatalog.ChannelTypeDeepSeek, body: `{"balance_infos":[{"currency":"CNY","total_balance":"+Inf"}]}`},
		{name: "OpenRouter omits usage", channelType: channelcatalog.ChannelTypeOpenRouter, body: `{"data":{"total_credits":10}}`},
		{name: "OpenRouter non-finite number", channelType: channelcatalog.ChannelTypeOpenRouter, body: `{"data":{"total_credits":1e9999,"total_usage":1}}`},
		{name: "Moonshot reports failure", channelType: channelcatalog.ChannelTypeMoonshot, body: `{"code":1,"status":false,"data":{"available_balance":73}}`},
		{name: "Moonshot omits balance", channelType: channelcatalog.ChannelTypeMoonshot, body: `{"code":0,"status":true,"data":{}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := createChannelBalanceFixture(t, test.channelType, "")
			useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
				return channelBalanceResponse(request, http.StatusOK, test.body), nil
			})
			_, err := FetchChannelBalanceContext(context.Background(), channel)
			require.Error(t, err)
			assertChannelBalanceUnchanged(t, channel)
		})
	}
}

func TestFetchChannelBalanceRejectsMissingOpenAIFieldsWithoutMutation(t *testing.T) {
	setupChannelBalanceTestDB(t)
	tests := []struct {
		name             string
		subscriptionBody string
		usageBody        string
		wantCalls        int
	}{
		{
			name:             "missing payment method",
			subscriptionBody: `{"hard_limit_usd":20}`,
			wantCalls:        1,
		},
		{
			name:             "missing hard limit",
			subscriptionBody: `{"has_payment_method":true}`,
			wantCalls:        1,
		},
		{
			name:             "missing usage",
			subscriptionBody: `{"has_payment_method":true,"hard_limit_usd":20}`,
			usageBody:        `{}`,
			wantCalls:        2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeOpenAI, "")
			calls := 0
			useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if strings.HasSuffix(request.URL.Path, "/subscription") {
					return channelBalanceResponse(request, http.StatusOK, test.subscriptionBody), nil
				}
				return channelBalanceResponse(request, http.StatusOK, test.usageBody), nil
			})

			_, err := FetchChannelBalanceContext(context.Background(), channel)
			require.Error(t, err)
			assert.Equal(t, test.wantCalls, calls)
			assertChannelBalanceUnchanged(t, channel)
		})
	}
}

func TestFetchChannelBalanceRejectsMalformedConfiguredURLWithoutFallback(t *testing.T) {
	setupChannelBalanceTestDB(t)
	settings := []string{
		`not-json`,
		`null`,
		`{"balance_url":123}`,
		`{"balance_url":"` + strings.Repeat("x", maxBalanceURLBytes+1) + `"}`,
	}
	for _, settingJSON := range settings {
		channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeDeepSeek, settingJSON)
		called := false
		useChannelBalanceTransport(t, func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("must not be called")
		})
		_, err := FetchChannelBalanceContext(context.Background(), channel)
		require.Error(t, err)
		assert.False(t, called, "an invalid override must not fall back and disclose the provider credential")
		assertChannelBalanceUnchanged(t, channel)
	}
}

func TestFetchChannelBalanceCustomChannelWithoutBaseURLDoesNotFallBackToOpenAI(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeCustom, "")
	called := false
	useChannelBalanceTransport(t, func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assert.False(t, called)
	assertChannelBalanceUnchanged(t, channel)
}

func TestFetchChannelBalanceRejectsOversizedResponseWithoutMutation(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeAPI2GPT, "")
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		body := strings.Repeat("x", int(maxChannelUpstreamResponseBytes)+1)
		return channelBalanceResponse(request, http.StatusOK, body), nil
	})
	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	assertChannelBalanceUnchanged(t, channel)
}

type cancelOnReadCloser struct {
	reader io.Reader
	cancel context.CancelFunc
	called bool
}

func (reader *cancelOnReadCloser) Read(destination []byte) (int, error) {
	count, err := reader.reader.Read(destination)
	if !reader.called {
		reader.called = true
		reader.cancel()
	}
	return count, err
}

func (*cancelOnReadCloser) Close() error { return nil }

func TestFetchChannelBalanceCancellationAfterResponseDoesNotMutate(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeAPI2GPT, "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		response := channelBalanceResponse(request, http.StatusOK, "")
		response.Body = &cancelOnReadCloser{
			reader: strings.NewReader(`{"total_remaining":42}`),
			cancel: cancel,
		}
		return response, nil
	})

	_, err := FetchChannelBalanceContext(ctx, channel)
	require.ErrorIs(t, err, context.Canceled)
	assertChannelBalanceUnchanged(t, channel)
}

func TestFetchChannelBalanceSanitizesTransportFailureAndDoesNotMutate(t *testing.T) {
	setupChannelBalanceTestDB(t)
	const settingJSON = `{"balance_url":"https://balance.example.test/credits?api_key=query-secret"}`
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeAzure, settingJSON)
	useChannelBalanceTransport(t, func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{
			Op:  "Get",
			URL: "https://balance.example.test/credits?api_key=query-secret",
			Err: errors.New("dial failed for primary-secret"),
		}
	})

	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assert.Equal(t, "upstream balance request failed", err.Error())
	assert.NotContains(t, err.Error(), "query-secret")
	assert.NotContains(t, err.Error(), "primary-secret")
	assertChannelBalanceUnchanged(t, channel)
}

func TestFetchChannelBalanceHTTPFailureDoesNotMutate(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeAIGC2D, "")
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		return channelBalanceResponse(request, http.StatusUnauthorized, `{"total_available":42}`), nil
	})
	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assertChannelBalanceUnchanged(t, channel)
}

func TestFetchChannelBalanceRejectsNon200SuccessStatus(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeAPI2GPT, "")
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		return channelBalanceResponse(request, http.StatusPartialContent, `{"total_remaining":42}`), nil
	})
	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assertChannelBalanceUnchanged(t, channel)
}

func TestFetchChannelBalanceMissingRowIsNotReportedAsSuccess(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := &model.Channel{
		Id: 999, Type: int(channelcatalog.ChannelTypeAPI2GPT), Key: "primary-secret",
		Balance: 91.25, BalanceUpdatedTime: 1234,
	}
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		return channelBalanceResponse(request, http.StatusOK, `{"total_remaining":42}`), nil
	})
	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assert.Equal(t, 91.25, channel.Balance)
	assert.Equal(t, int64(1234), channel.BalanceUpdatedTime)
}

func TestFetchChannelBalanceDoesNotPersistAcrossEndpointChange(t *testing.T) {
	setupChannelBalanceTestDB(t)
	channel := createChannelBalanceFixture(t, channelcatalog.ChannelTypeAPI2GPT, "")
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).
			Updates(map[string]any{"key": "replacement-secret", "base_url": "https://replacement.example.test"}).Error)
		return channelBalanceResponse(request, http.StatusOK, `{"total_remaining":42}`), nil
	})
	_, err := FetchChannelBalanceContext(context.Background(), channel)
	require.Error(t, err)
	assert.Equal(t, 91.25, channel.Balance)
	assert.Equal(t, int64(1234), channel.BalanceUpdatedTime)
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, "replacement-secret", stored.Key)
	assert.Equal(t, "https://replacement.example.test", stored.BaseURL)
	assert.Equal(t, 91.25, stored.Balance)
	assert.Equal(t, int64(1234), stored.BalanceUpdatedTime)
}

func TestUpdateAllChannelsBalancesHonorsAutoBanAndSkipsMultiKey(t *testing.T) {
	setupChannelBalanceTestDB(t)
	autoBanOff := 0
	autoBanOn := 1
	channels := []*model.Channel{
		{
			Type: int(channelcatalog.ChannelTypeAPI2GPT), Name: "auto-ban-off", Key: "off-secret",
			Status: channelcatalog.ChannelStatusEnabled, AutoBan: &autoBanOff, Balance: 10, BalanceUpdatedTime: 10,
		},
		{
			Type: int(channelcatalog.ChannelTypeAPI2GPT), Name: "auto-ban-on", Key: "on-secret",
			Status: channelcatalog.ChannelStatusEnabled, AutoBan: &autoBanOn, Balance: 10, BalanceUpdatedTime: 10,
		},
		{
			Type: int(channelcatalog.ChannelTypeAPI2GPT), Name: "multi-key", Key: "multi-one\nmulti-two",
			Status: channelcatalog.ChannelStatusEnabled, AutoBan: &autoBanOn, Balance: 10, BalanceUpdatedTime: 10,
			ChannelInfo: `{"is_multi_key":true,"multi_key_size":2}`,
		},
	}
	for _, channel := range channels {
		require.NoError(t, model.DB.Create(channel).Error)
	}
	requests := 0
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		requests++
		return channelBalanceResponse(request, http.StatusOK, `{"total_remaining":-1}`), nil
	})

	require.NoError(t, UpdateAllChannelsBalancesContext(context.Background()))
	assert.Equal(t, 2, requests, "multi-key credentials must never be sent to a balance endpoint")

	var stored []model.Channel
	require.NoError(t, model.DB.Order("id ASC").Find(&stored).Error)
	require.Len(t, stored, 3)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, stored[0].Status)
	assert.Equal(t, -1.0, stored[0].Balance)
	assert.Equal(t, channelcatalog.ChannelStatusAutoDisabled, stored[1].Status)
	assert.Equal(t, -1.0, stored[1].Balance)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, stored[2].Status)
	assert.Equal(t, 10.0, stored[2].Balance)
	assert.Equal(t, int64(10), stored[2].BalanceUpdatedTime)
}

func TestUpdateAllChannelsBalancesStopsOnCancellation(t *testing.T) {
	setupChannelBalanceTestDB(t)
	for _, key := range []string{"first-secret", "second-secret"} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Type: int(channelcatalog.ChannelTypeAPI2GPT), Name: key, Key: key,
			Status: channelcatalog.ChannelStatusEnabled, Balance: 10, BalanceUpdatedTime: 10,
		}).Error)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	requests := 0
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		requests++
		response := channelBalanceResponse(request, http.StatusOK, "")
		response.Body = &cancelOnReadCloser{
			reader: strings.NewReader(`{"total_remaining":42}`),
			cancel: cancel,
		}
		return response, nil
	})

	err := UpdateAllChannelsBalancesContext(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, requests)

	var stored []model.Channel
	require.NoError(t, model.DB.Order("id ASC").Find(&stored).Error)
	require.Len(t, stored, 2)
	for _, channel := range stored {
		assert.Equal(t, 10.0, channel.Balance)
		assert.Equal(t, int64(10), channel.BalanceUpdatedTime)
	}
}

func TestUpdateAllChannelsBalancesRejectsOverlappingSweeps(t *testing.T) {
	setupChannelBalanceTestDB(t)
	require.NoError(t, model.DB.Create(&model.Channel{
		Type: int(channelcatalog.ChannelTypeAPI2GPT), Name: "blocking", Key: "blocking-secret",
		Status: channelcatalog.ChannelStatusEnabled, Balance: 10, BalanceUpdatedTime: 10,
	}).Error)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	t.Cleanup(release)
	useChannelBalanceTransport(t, func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-releaseRequest
		return channelBalanceResponse(request, http.StatusOK, `{"total_remaining":42}`), nil
	})

	firstResult := make(chan error, 1)
	go func() { firstResult <- UpdateAllChannelsBalancesContext(context.Background()) }()
	<-requestStarted
	assert.ErrorIs(t, UpdateAllChannelsBalancesContext(context.Background()), ErrChannelBalanceSweepRunning)
	release()
	require.NoError(t, <-firstResult)
}
