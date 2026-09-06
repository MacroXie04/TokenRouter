package channels

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mockUpstream(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status < 400 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "c1", "object": "chat.completion", "created": 1, "model": "gpt-4",
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "fail"}})
		}
	}))
}

func initHealthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.AutomaticDisableChannelEnabledOption: "true",
		setting.AutomaticEnableChannelEnabledOption:  "true",
		setting.AutomaticDisableStatusCodesOption:    "401,500-599",
	}))
}

type healthPolicyRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn healthPolicyRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func useHealthPolicyResponse(t *testing.T, status int, body string) {
	t.Helper()
	previous := healthHTTPClient
	healthHTTPClient = &http.Client{Transport: healthPolicyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status),
			Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header),
		}, nil
	})}
	t.Cleanup(func() { healthHTTPClient = previous })
}

func TestChannelHealthPolicyGatesAndPassiveMode(t *testing.T) {
	initHealthDB(t)
	useHealthPolicyResponse(t, http.StatusInternalServerError, `{"error":"failure"}`)
	autoBan := 1
	channel := model.Channel{
		Name: "policy", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://health.example.test",
		TestModel: "gpt-4", AutoBan: &autoBan,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	success, _, err := TestAndRecordChannelContextWithMode(
		context.Background(), &channel, wallclock.NowTimestamp(), false,
	)
	assert.False(t, success)
	require.NoError(t, err)
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, stored.Status,
		"passive recovery must never disable an enabled channel")

	require.NoError(t, setting.UpdateOption(setting.AutomaticDisableChannelEnabledOption, "false"))
	success, _, err = TestAndRecordChannelContextWithMode(
		context.Background(), &stored, wallclock.NowTimestamp(), true,
	)
	assert.False(t, success)
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, stored.Status,
		"the global disable gate must dominate a channel's auto-ban flag")
}

func TestChannelHealthLocalErrorsNeverDisableAndKeywordsCanDisable(t *testing.T) {
	initHealthDB(t)
	autoBan := 1
	local := model.Channel{
		Name: "unsupported", Type: int(channelcatalog.ChannelTypeMidjourney), Status: channelcatalog.ChannelStatusEnabled,
		TestModel: "mj", AutoBan: &autoBan,
	}
	require.NoError(t, model.DB.Create(&local).Error)
	success, _, err := testAndRecordChannel(&local, wallclock.NowTimestamp())
	assert.False(t, success)
	require.NoError(t, err)
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, local.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, stored.Status)

	useHealthPolicyResponse(t, http.StatusBadRequest, `{"error":"Permission denied by provider"}`)
	keyword := model.Channel{
		Name: "keyword", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://health.example.test",
		TestModel: "gpt-4", AutoBan: &autoBan,
	}
	require.NoError(t, model.DB.Create(&keyword).Error)
	success, _, err = testAndRecordChannel(&keyword, wallclock.NowTimestamp())
	assert.False(t, success)
	require.NoError(t, err)
	stored = model.Channel{}
	require.NoError(t, model.DB.First(&stored, keyword.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusAutoDisabled, stored.Status)
}

func TestRunChannelHealthTestsReturnsPersistenceFailuresAndContinues(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	initHealthDB(t)
	upstream := mockUpstream(http.StatusOK)
	defer upstream.Close()
	first := model.Channel{Name: "first", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL, TestModel: "gpt-4"}
	second := model.Channel{Name: "second", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL, TestModel: "gpt-4"}
	require.NoError(t, model.DB.Create(&first).Error)
	require.NoError(t, model.DB.Create(&second).Error)

	var failFirst atomic.Bool
	failFirst.Store(true)
	callbackName := "test:fail_one_channel_health_write"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Channel{}).TableName() && failFirst.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected channel health write failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	err := RunChannelHealthTests()
	require.ErrorContains(t, err, "injected channel health write failure")
	var gotFirst, gotSecond model.Channel
	require.NoError(t, model.DB.First(&gotFirst, first.Id).Error)
	require.NoError(t, model.DB.First(&gotSecond, second.Id).Error)
	assert.Zero(t, gotFirst.TestTime)
	assert.Positive(t, gotSecond.TestTime, "later channels must still persist after an earlier failure")

	require.NoError(t, RunChannelHealthTests())
	require.NoError(t, model.DB.First(&gotFirst, first.Id).Error)
	assert.Positive(t, gotFirst.TestTime, "a transient write failure remains retryable")
}

func TestChannelHealthStatusAndAbilityWriteRollBackTogether(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	initHealthDB(t)
	upstream := mockUpstream(http.StatusInternalServerError)
	defer upstream.Close()
	autoBan := 1
	channel := model.Channel{
		Name: "rollback", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: channelcatalog.ChannelStatusEnabled,
		BaseURL: upstream.URL, TestModel: "gpt-4", AutoBan: &autoBan,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	ability := model.Ability{Group: "default", Model: "gpt-4", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)

	var failAbility atomic.Bool
	failAbility.Store(true)
	callbackName := "test:fail_health_ability_write"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && failAbility.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected health ability failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	success, _, err := testAndRecordChannel(&channel, wallclock.NowTimestamp())
	assert.False(t, success)
	require.ErrorContains(t, err, "injected health ability failure")
	var gotChannel model.Channel
	var gotAbility model.Ability
	require.NoError(t, model.DB.First(&gotChannel, channel.Id).Error)
	require.NoError(t, model.DB.First(&gotAbility, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, gotChannel.Status)
	assert.Zero(t, gotChannel.TestTime)
	assert.True(t, gotAbility.Enabled)

	success, _, err = testAndRecordChannel(&channel, wallclock.NowTimestamp())
	assert.False(t, success)
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&gotChannel, channel.Id).Error)
	require.NoError(t, model.DB.First(&gotAbility, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.Equal(t, channelcatalog.ChannelStatusAutoDisabled, gotChannel.Status)
	assert.Positive(t, gotChannel.TestTime)
	assert.False(t, gotAbility.Enabled)
}

func TestChannelHealthAndAutoDisable(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	initHealthDB(t)

	okUp := mockUpstream(200)
	defer okUp.Close()
	failUp := mockUpstream(500)
	defer failUp.Close()

	autoBan := 1
	okCh := model.Channel{Name: "ok", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: channelcatalog.ChannelStatusEnabled, BaseURL: okUp.URL, TestModel: "gpt-4", AutoBan: &autoBan}
	require.NoError(t, model.DB.Create(&okCh).Error)
	failCh := model.Channel{Name: "fail", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: channelcatalog.ChannelStatusEnabled, BaseURL: failUp.URL, TestModel: "gpt-4", AutoBan: &autoBan}
	require.NoError(t, model.DB.Create(&failCh).Error)

	// A healthy channel passes the health test.
	success, latency := TestChannelHealth(okCh.Id)
	assert.True(t, success)
	assert.GreaterOrEqual(t, latency, 0)

	// The periodic job auto-disables the failing channel.
	require.NoError(t, RunChannelHealthTests())
	var got model.Channel
	require.NoError(t, model.DB.First(&got, failCh.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusAutoDisabled, got.Status)

	// The healthy channel remains enabled.
	var gotOk model.Channel
	require.NoError(t, model.DB.First(&gotOk, okCh.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, gotOk.Status)
}

func TestChannelHealthUsesProviderWireContractAndLeastCredential(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	tests := []struct {
		name            string
		channelType     channelcatalog.ChannelType
		key             string
		other           string
		mappedModel     string
		wantPath        string
		wantQuery       string
		wantHeader      string
		wantHeaderValue string
		forbidden       []string
	}{
		{
			name: "Anthropic", channelType: channelcatalog.ChannelTypeAnthropic, key: "anthropic-secret",
			mappedModel: "claude-upstream", wantPath: "/v1/messages",
			wantHeader: "x-api-key", wantHeaderValue: "anthropic-secret",
		},
		{
			name: "Gemini", channelType: channelcatalog.ChannelTypeGemini, key: "gemini-secret",
			mappedModel: "gemini-upstream", wantPath: "/v1beta/models/gemini-upstream:generateContent",
			wantHeader: "x-goog-api-key", wantHeaderValue: "gemini-secret",
		},
		{
			name: "Azure", channelType: channelcatalog.ChannelTypeAzure, key: "azure-secret", other: "2025-04-01-preview",
			mappedModel: "azure-deployment", wantPath: "/openai/deployments/azure-deployment/chat/completions",
			wantQuery: "api-version=2025-04-01-preview", wantHeader: "api-key", wantHeaderValue: "azure-secret",
		},
		{
			name: "Perplexity", channelType: channelcatalog.ChannelTypePerplexity, key: "perplexity-secret",
			mappedModel: "sonar-pro", wantPath: "/chat/completions",
			wantHeader: "Authorization", wantHeaderValue: "Bearer perplexity-secret",
		},
		{
			name: "Cohere", channelType: channelcatalog.ChannelTypeCohere, key: "cohere-secret",
			mappedModel: "command-r-plus", wantPath: "/v1/chat",
			wantHeader: "Authorization", wantHeaderValue: "Bearer cohere-secret",
		},
		{
			name: "Ali", channelType: channelcatalog.ChannelTypeAli, key: "ali-secret",
			mappedModel: "qwen-plus", wantPath: "/compatible-mode/v1/chat/completions",
			wantHeader: "Authorization", wantHeaderValue: "Bearer ali-secret",
		},
		{
			name: "Zhipu v4", channelType: channelcatalog.ChannelTypeZhipuV4, key: "zhipu-v4-secret",
			mappedModel: "glm-4-plus", wantPath: "/api/paas/v4/chat/completions",
			wantHeader: "Authorization", wantHeaderValue: "Bearer zhipu-v4-secret",
		},
		{
			name: "MiniMax", channelType: channelcatalog.ChannelTypeMiniMax, key: "minimax-secret",
			mappedModel: "MiniMax-M2.7-highspeed", wantPath: "/v1/text/chatcompletion_v2",
			wantHeader: "Authorization", wantHeaderValue: "Bearer minimax-secret",
		},
		{
			name: "Submodel", channelType: channelcatalog.ChannelTypeSubmodel, key: "submodel-secret",
			mappedModel: "openai/gpt-oss-120b", wantPath: "/v1/chat/completions",
			wantHeader: "Authorization", wantHeaderValue: "Bearer submodel-secret",
		},
		{
			name: "Codex", channelType: channelcatalog.ChannelTypeCodex,
			key:         `{"access_token":"codex-access","account_id":"acct-1","refresh_token":"codex-refresh"}`,
			mappedModel: "gpt-5-codex", wantPath: "/backend-api/codex/responses",
			wantHeader: "Authorization", wantHeaderValue: "Bearer codex-access",
			forbidden: []string{"codex-refresh", "refresh_token", `{"access_token"`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initHealthDB(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				assert.Equal(t, test.wantPath, request.URL.Path)
				assert.Equal(t, test.wantQuery, request.URL.RawQuery)
				assert.Equal(t, test.wantHeaderValue, request.Header.Get(test.wantHeader))
				assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
				var payload map[string]any
				require.NoError(t, json.Unmarshal(body, &payload))
				if test.channelType != channelcatalog.ChannelTypeGemini {
					assert.Equal(t, test.mappedModel, payload["model"])
				}
				wire := string(body) + " " + request.Header.Get("Authorization") + " " + request.Header.Get("chatgpt-account-id")
				for _, secret := range test.forbidden {
					assert.NotContains(t, wire, secret)
				}
				if test.channelType == channelcatalog.ChannelTypeCodex {
					assert.Equal(t, "acct-1", request.Header.Get("chatgpt-account-id"))
					assert.Equal(t, "responses=experimental", request.Header.Get("OpenAI-Beta"))
				}
				if test.channelType == channelcatalog.ChannelTypeCohere {
					assert.Equal(t, "application/json", request.Header.Get("Accept"))
					assert.Equal(t, "ping", payload["message"])
					assert.EqualValues(t, 1, payload["max_tokens"])
					assert.Equal(t, false, payload["stream"])
					assert.Empty(t, payload["chat_history"])
					assert.NotContains(t, payload, "messages")
				}
				if test.channelType == channelcatalog.ChannelTypeAli {
					assert.Equal(t, "application/json", request.Header.Get("Accept"))
					assert.EqualValues(t, 1, payload["max_tokens"])
					assert.Len(t, payload["messages"], 1)
				}
				if test.channelType == channelcatalog.ChannelTypeZhipuV4 || test.channelType == channelcatalog.ChannelTypeMiniMax {
					assert.Equal(t, "application/json", request.Header.Get("Accept"))
					assert.EqualValues(t, 1, payload["max_tokens"])
					assert.Len(t, payload["messages"], 1)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			channel := model.Channel{
				Name: "provider-health", Type: int(test.channelType), Key: test.key,
				Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL, TestModel: "client-model",
				ModelMapping: `{"client-model":"` + test.mappedModel + `"}`, Other: test.other,
			}
			require.NoError(t, model.DB.Create(&channel).Error)
			success, _, message := TestChannelHealthDetailed(channel.Id, "")
			assert.True(t, success, message)
			assert.Empty(t, message)
		})
	}
}

func TestZhipuLegacyChannelHealthUsesSignedV3Contract(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	initHealthDB(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/paas/v3/model-api/chatglm_pro/invoke", request.URL.Path)
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		rawAuthorization := request.Header.Get("Authorization")
		assert.NotEmpty(t, rawAuthorization)
		assert.NotContains(t, rawAuthorization, "Bearer ")
		parsed, err := jwt.Parse(rawAuthorization, func(token *jwt.Token) (any, error) {
			assert.Equal(t, jwt.SigningMethodHS256.Alg(), token.Method.Alg())
			assert.Equal(t, "SIGN", token.Header["sign_type"])
			return []byte("health-secret"), nil
		}, jwt.WithoutClaimsValidation())
		require.NoError(t, err)
		require.True(t, parsed.Valid)
		claims := parsed.Claims.(jwt.MapClaims)
		assert.Equal(t, "health-id", claims["api_key"])
		timestamp, ok := claims["timestamp"].(float64)
		require.True(t, ok)
		expiry, ok := claims["exp"].(float64)
		require.True(t, ok)
		assert.InDelta(t, 24*time.Hour/time.Millisecond, expiry-timestamp, 1)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		assert.NotContains(t, payload, "incremental")
		assert.NotContains(t, payload, "model")
		prompt := payload["prompt"].([]any)
		require.Len(t, prompt, 1)
		assert.Equal(t, "ping", prompt[0].(map[string]any)["content"])
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	channel := model.Channel{
		Name: "zhipu-v3-health", Type: int(channelcatalog.ChannelTypeZhipu), Key: "health-id.health-secret",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL, TestModel: "client-model",
		ModelMapping: `{"client-model":"chatglm_pro"}`,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	success, _, message := TestChannelHealthDetailed(channel.Id, "")
	assert.True(t, success, message)
	assert.Empty(t, message)
}

func TestZhipuChannelHealthRequiresCredentialBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name        string
		channelType channelcatalog.ChannelType
		modelName   string
	}{
		{name: "legacy", channelType: channelcatalog.ChannelTypeZhipu, modelName: "chatglm_pro"},
		{name: "v4", channelType: channelcatalog.ChannelTypeZhipuV4, modelName: "glm-4-plus"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
				Type: int(test.channelType), BaseURL: "https://zhipu.example",
			}, test.modelName)
			require.Error(t, err)
			assert.Nil(t, request)
		})
	}
}

func TestCohereChannelHealthRequiresCredentialBeforeDispatch(t *testing.T) {
	request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeCohere), BaseURL: "https://cohere.example",
	}, "command-r-plus")
	require.ErrorContains(t, err, "API key is empty")
	assert.Nil(t, request)
}

func TestAWSChannelHealthUsesBedrockWireAndLeastCredential(t *testing.T) {
	tests := []struct {
		name          string
		credential    string
		otherSettings string
		wantAuth      string
		wantSigned    bool
	}{
		{
			name: "API key", credential: "bedrock-api-key|us-east-1",
			otherSettings: `{"aws_key_type":"api_key"}`, wantAuth: "Bearer bedrock-api-key",
		},
		{
			name: "access and secret key", credential: "access-key|secret-key|us-east-1",
			otherSettings: `{"aws_key_type":"ak_sk"}`, wantSigned: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := &model.Channel{
				Type: int(channelcatalog.ChannelTypeAws), Key: test.credential,
				BaseURL: "https://bedrock.example.test", OtherSettings: test.otherSettings,
				ModelMapping: `{"client-model":"claude-3-5-sonnet-20240620"}`,
			}
			request, err := buildChannelHealthRequest(t.Context(), channel, "client-model")
			require.NoError(t, err)
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t,
				"https://bedrock.example.test/model/us.anthropic.claude-3-5-sonnet-20240620-v1:0/invoke",
				request.URL.String(),
			)
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			assert.Equal(t, "application/json", request.Header.Get("Accept"))
			if test.wantSigned {
				authorization := request.Header.Get("Authorization")
				assert.Contains(t, authorization, "AWS4-HMAC-SHA256 Credential=access-key/")
				assert.Contains(t, authorization, "/us-east-1/bedrock/aws4_request")
				assert.NotContains(t, authorization, "secret-key")
				assert.NotEmpty(t, request.Header.Get("X-Amz-Date"))
				assert.NotEmpty(t, request.Header.Get("X-Amz-Content-Sha256"))
			} else {
				assert.Equal(t, test.wantAuth, request.Header.Get("Authorization"))
				assert.NotContains(t, request.Header.Get("Authorization"), "us-east-1")
			}
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "bedrock-2023-05-31", body["anthropic_version"])
			assert.EqualValues(t, 1, body["max_tokens"])
			assert.Len(t, body["messages"], 1)
			assert.NotContains(t, body, "model")
			wire, err := json.Marshal(body)
			require.NoError(t, err)
			assert.NotContains(t, string(wire), "client-model")
			assert.NotContains(t, string(wire), "secret-key")
		})
	}

	request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeAws), BaseURL: "https://bedrock.example.test",
		Key: "wrong-shape", OtherSettings: `{"aws_key_type":"ak_sk"}`,
	}, "claude-3-haiku-20240307")
	assert.Error(t, err)
	assert.Nil(t, request)
}

func TestMiniMaxChannelHealthRequiresCredentialBeforeDispatch(t *testing.T) {
	request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeMiniMax), BaseURL: "https://api.minimax.chat",
	}, "MiniMax-M2.7")
	require.ErrorContains(t, err, "API key is empty")
	assert.Nil(t, request)
}

func TestAliChannelHealthRequiresCredentialBeforeDispatch(t *testing.T) {
	request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeAli), BaseURL: "https://dashscope.example",
	}, "qwen-plus")
	require.ErrorContains(t, err, "API key is empty")
	assert.Nil(t, request)
}

func TestDifyChannelHealthUsesChatMessagesWithoutForwardingModel(t *testing.T) {
	channel := &model.Channel{
		Type: int(channelcatalog.ChannelTypeDify), BaseURL: "https://api.dify.test/gateway", Key: "dify-secret",
		ModelMapping: `{"client-model":"application-owned-model"}`,
	}
	request, err := buildChannelHealthRequest(context.Background(), channel, "client-model")
	require.NoError(t, err)
	assert.Equal(t, "https://api.dify.test/gateway/v1/chat-messages", request.URL.String())
	assert.Equal(t, "Bearer dify-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	var payload map[string]any
	require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
	assert.NotContains(t, payload, "model")
	assert.Equal(t, map[string]any{}, payload["inputs"])
	assert.Equal(t, "USER: \nping\n", payload["query"])
	assert.Equal(t, "blocking", payload["response_mode"])
	assert.Equal(t, false, payload["auto_generate_name"])
	assert.Equal(t, []any{}, payload["files"])
	assert.NotEmpty(t, payload["user"])

	channel.Key = ""
	_, err = buildChannelHealthRequest(context.Background(), channel, "client-model")
	assert.ErrorContains(t, err, "API key")
}

func TestCozeChannelHealthUsesBotContract(t *testing.T) {
	channel := &model.Channel{
		Type: int(channelcatalog.ChannelTypeCoze), BaseURL: "https://api.coze.test/gateway", Key: "coze-secret",
		Other: "bot_123", ModelMapping: `{"client-model":"deepseek-v3"}`,
	}
	request, err := buildChannelHealthRequest(context.Background(), channel, "client-model")
	require.NoError(t, err)
	assert.Equal(t, "https://api.coze.test/gateway/v3/chat", request.URL.String())
	assert.Equal(t, "Bearer coze-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	var payload map[string]any
	require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
	assert.Equal(t, "bot_123", payload["bot_id"])
	assert.NotContains(t, payload, "model")
	assert.NotContains(t, payload, "stream")
	assert.Regexp(t, `^tokenrouter-[0-9a-f]{32}$`, payload["user_id"])
	messages := payload["additional_messages"].([]any)
	require.Len(t, messages, 1)
	assert.Equal(t, map[string]any{
		"role": "user", "content": "ping", "content_type": "text",
	}, messages[0])

	channel.Key = ""
	_, err = buildChannelHealthRequest(context.Background(), channel, "client-model")
	assert.ErrorContains(t, err, "API key")
	channel.Key = "coze-secret"
	channel.Other = ""
	_, err = buildChannelHealthRequest(context.Background(), channel, "client-model")
	assert.ErrorContains(t, err, "bot ID")
}

func TestMokaAIChannelHealthUsesEmbeddingContract(t *testing.T) {
	channel := &model.Channel{
		Type: int(channelcatalog.ChannelTypeMokaAI), BaseURL: "https://moka.example/gateway", Key: "moka-secret",
		ModelMapping: `{"client-model":"m3e-large"}`,
	}
	request, err := buildChannelHealthRequest(t.Context(), channel, "client-model")
	require.NoError(t, err)
	assert.Equal(t, "https://moka.example/gateway/embeddings", request.URL.String())
	assert.Equal(t, "Bearer moka-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	var payload map[string]any
	require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
	assert.Equal(t, "m3e-large", payload["model"])
	assert.Equal(t, []any{"ping"}, payload["input"])

	channel.Key = ""
	_, err = buildChannelHealthRequest(t.Context(), channel, "client-model")
	assert.ErrorContains(t, err, "API key")

	channel.Key = "moka-secret"
	_, err = buildChannelHealthRequest(t.Context(), channel, "unsupported-model")
	assert.ErrorContains(t, err, "m3e")
}

func TestPaLMChannelHealthUsesGenerateMessageContract(t *testing.T) {
	request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypePaLM), BaseURL: "https://palm.example/gateway", Key: "palm-secret",
		ModelMapping: `{"client-model":"PaLM-2"}`,
	}, "client-model")
	require.NoError(t, err)
	assert.Equal(t, "https://palm.example/gateway/v1beta2/models/chat-bison-001:generateMessage", request.URL.String())
	assert.Equal(t, "palm-secret", request.Header.Get("x-goog-api-key"))
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	var payload map[string]any
	require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
	prompt := payload["prompt"].(map[string]any)
	messages := prompt["messages"].([]any)
	require.Len(t, messages, 1)
	assert.Equal(t, "0", messages[0].(map[string]any)["author"])
	assert.Equal(t, "ping", messages[0].(map[string]any)["content"])
	assert.EqualValues(t, 1, payload["candidateCount"])

	_, err = buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypePaLM), BaseURL: "https://palm.example", Key: "",
	}, "PaLM-2")
	assert.ErrorContains(t, err, "API key")
}

func TestBaiduChannelHealthUsesProviderAuthenticationAndWireContracts(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	var tokenCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		tokenCalls.Add(1)
		assert.Equal(t, "/gateway/oauth/2.0/token", request.URL.Path)
		assert.Equal(t, "client-id", request.URL.Query().Get("client_id"))
		assert.Equal(t, "client-secret", request.URL.Query().Get("client_secret"))
		_, _ = io.WriteString(w, `{"access_token":"health-token","expires_in":7200}`)
	}))
	defer upstream.Close()

	legacy, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeBaidu), BaseURL: upstream.URL + "/gateway", Key: "client-id|client-secret",
	}, "ERNIE-4.0-8K")
	require.NoError(t, err)
	assert.Equal(t, "/gateway/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/completions_pro", legacy.URL.Path)
	assert.Equal(t, "health-token", legacy.URL.Query().Get("access_token"))
	assert.Empty(t, legacy.Header.Get("Authorization"))
	var legacyPayload map[string]any
	require.NoError(t, json.NewDecoder(legacy.Body).Decode(&legacyPayload))
	assert.NotContains(t, legacyPayload, "model")
	assert.EqualValues(t, 2, legacyPayload["max_output_tokens"])
	assert.Equal(t, int32(1), tokenCalls.Load())

	v2, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeBaiduV2), BaseURL: "https://qianfan.example/gateway", Key: "bearer-token|app-id",
	}, "ernie-4.0-turbo-8k")
	require.NoError(t, err)
	assert.Equal(t, "https://qianfan.example/gateway/v2/chat/completions", v2.URL.String())
	assert.Equal(t, "Bearer bearer-token", v2.Header.Get("Authorization"))
	assert.Equal(t, "app-id", v2.Header.Get("appid"))
	var v2Payload map[string]any
	require.NoError(t, json.NewDecoder(v2.Body).Decode(&v2Payload))
	assert.Equal(t, "ernie-4.0-turbo-8k", v2Payload["model"])
}

func TestBaiduV2ChannelHealthRequiresCredentialBeforeDispatch(t *testing.T) {
	request, err := buildChannelHealthRequest(t.Context(), &model.Channel{
		Type: int(channelcatalog.ChannelTypeBaiduV2), BaseURL: "https://qianfan.example",
	}, "ernie-4.0-turbo-8k")
	require.ErrorContains(t, err, "API key is invalid")
	assert.Nil(t, request)
}

func TestChannelHealthCurrentMultiKeyNeverTransmitsCollectionOrDisabledKey(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	initHealthDB(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		assert.Equal(t, "Bearer enabled-secret", authorization)
		assert.NotContains(t, authorization, "disabled-secret")
		assert.NotContains(t, authorization, "\n")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	channel := model.Channel{
		Name: "multi-health", Type: int(channelcatalog.ChannelTypeOpenAI),
		Key: "disabled-secret\nenabled-secret", ChannelInfo: `{"is_multi_key":true,"multi_key_size":2,"multi_key_status_list":{"0":2,"1":1}}`,
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL, TestModel: "gpt-4",
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	success, _, message := TestChannelHealthDetailed(channel.Id, "")
	assert.True(t, success, message)
}

func TestScheduledHealthSkipsUnsupportedProviderWithoutAutoDisable(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	initHealthDB(t)

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	autoBan := 1
	channel := model.Channel{
		Name: "unsupported-task", Type: int(channelcatalog.ChannelTypeJimeng), Key: "provider-secret",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: upstream.URL, TestModel: "jimeng-video", AutoBan: &autoBan,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	success, _, message := TestChannelHealthDetailed(channel.Id, "")
	assert.False(t, success)
	assert.True(t, strings.Contains(message, "not supported"), message)
	require.NoError(t, RunChannelHealthTests())
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, channel.Status)
	assert.Zero(t, channel.TestTime)
	assert.Zero(t, calls.Load())
}
