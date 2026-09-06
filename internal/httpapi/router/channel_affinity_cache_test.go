package router_test

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func configureControllerAffinity(t *testing.T) string {
	t.Helper()
	rules := []setting.ChannelAffinityRule{{
		Name:       "controller rule",
		ModelRegex: []string{"^controller-model$"},
		PathRegex:  []string{"^/v1/responses$"},
		KeySources: []setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Affinity-Key"}},
		TTLSeconds: 120, IncludeRuleName: true, IncludeUsingGroup: true,
	}}
	encoded, err := jsonutil.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ChannelAffinityEnabledOption:           "true",
		setting.ChannelAffinityMaxEntriesOption:        "25",
		setting.ChannelAffinityDefaultTTLSecondsOption: "120",
		setting.ChannelAffinityRulesOption:             string(encoded),
	}))
	channelssvc.ClearChannelAffinityCacheAll()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"controller-model"}`))
	ctx.Request.Header.Set("X-Affinity-Key", "controller-key")
	_, _ = channelssvc.GetPreferredChannelByAffinity(ctx, "controller-model", "default", nil)
	channelssvc.RecordChannelAffinity(ctx, 73, 73)
	return "controller rule"
}

func TestChannelAffinityCacheControllerContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	ruleName := configureControllerAffinity(t)

	rec := do(http.MethodGet, "/api/option/channel_affinity_cache", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.EqualValues(t, 1, data["total"])
	assert.EqualValues(t, 25, data["cache_capacity"])
	assert.Equal(t, "LRU", data["cache_algo"])
	assert.EqualValues(t, 1, data["by_rule_name"].(map[string]any)[ruleName])

	rec = do(http.MethodDelete, "/api/option/channel_affinity_cache", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "缺少参数：rule_name，或使用 all=true 清空全部", decodeBody(t, rec)["message"])

	rec = do(http.MethodDelete, "/api/option/channel_affinity_cache?rule_name="+url.QueryEscape(ruleName), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 1, decodeBody(t, rec)["data"].(map[string]any)["deleted"])

	rec = do(http.MethodDelete, "/api/option/channel_affinity_cache?rule_name=unknown", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "未知规则名称", decodeBody(t, rec)["message"])

	rec = do(http.MethodDelete, "/api/option/channel_affinity_cache?all=true", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.EqualValues(t, 0, decodeBody(t, rec)["data"].(map[string]any)["deleted"])
}

func TestChannelAffinityUsageControllerContractAndRoles(t *testing.T) {
	handler, rootDo, _ := setupChannelRead(t, roles.RoleRootUser)

	rec := rootDo(http.MethodGet, "/api/log/channel_affinity_usage_cache", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing param: rule_name", decodeBody(t, rec)["message"])

	rec = rootDo(http.MethodGet, "/api/log/channel_affinity_usage_cache?rule_name=rule", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "missing param: key_fp", decodeBody(t, rec)["message"])

	rec = rootDo(http.MethodGet, "/api/log/channel_affinity_usage_cache?rule_name=rule&using_group=default&key_fp=abc12345", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "rule", data["rule_name"])
	assert.Equal(t, "default", data["using_group"])
	assert.Equal(t, "abc12345", data["key_fp"])
	assert.EqualValues(t, 0, data["total"])

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/log/channel_affinity_usage_cache?rule_name=r&key_fp=k", nil))
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	_, adminDo, _ := setupChannelRead(t, roles.RoleAdminUser)
	assert.Equal(t, http.StatusOK, adminDo(http.MethodGet, "/api/log/channel_affinity_usage_cache?rule_name=r&key_fp=k", "").Code)
	assert.Equal(t, http.StatusForbidden, adminDo(http.MethodGet, "/api/option/channel_affinity_cache", "").Code)
}

func TestChannelAffinityOptionValidation(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	original := setting.GetOption(setting.ChannelAffinityRulesOption)
	requestBody, err := json.Marshal(map[string]any{
		"key":   setting.ChannelAffinityRulesOption,
		"value": `[{"name":"bad","model_regex":["("],"key_sources":[{"type":"gjson","path":"key"}]}]`,
	})
	require.NoError(t, err)
	rec := do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "invalid regex")
	assert.Equal(t, original, setting.GetOption(setting.ChannelAffinityRulesOption))
}
