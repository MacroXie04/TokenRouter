package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"sort"
	"strings"
	"testing"
)

func TestChannelCreateAcceptsReferenceEnvelopeAndPersistsProviderFields(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	response := do(http.MethodPost, "/api/channel", `{
		"mode":"single",
		"channel":{
			"name":"reference-envelope","type":1,"key":"sk-envelope","status":3,
			"openai_organization":"org-test","test_model":"gpt-4o-mini",
			"base_url":"https://gateway.example.test","other":"provider-opaque",
			"models":"gpt-4o","group":"default","weight":7,"priority":8,
			"model_mapping":"{\"old\":\"new\"}","status_code_mapping":"{\"429\":500}",
			"auto_ban":0,"other_info":"{\"region\":\"test\"}","tag":"primary",
			"remark":"full contract","setting":"{\"proxy\":\"\"}",
			"param_override":"{\"temperature\":0}",
			"header_override":"{\"X-Test\":\"yes\"}",
			"channel_info":{"operator_note":"preserved"},"settings":"{}"
		}
	}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, true, decodeBody(t, response)["success"])

	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "reference-envelope").First(&channel).Error)
	assert.Equal(t, "sk-envelope", channel.Key)
	assert.Equal(t, "org-test", channel.OpenAIOrganization)
	assert.Equal(t, "gpt-4o-mini", channel.TestModel)
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, channel.Status)
	assert.Equal(t, "https://gateway.example.test", channel.BaseURL)
	assert.Equal(t, "provider-opaque", channel.Other)
	assert.Equal(t, `{"old":"new"}`, channel.ModelMapping)
	assert.Equal(t, `{"429":500}`, channel.StatusCodeMapping)
	require.NotNil(t, channel.AutoBan)
	assert.Equal(t, 0, *channel.AutoBan)
	assert.Equal(t, `{"region":"test"}`, channel.OtherInfo)
	assert.Equal(t, `{"temperature":0}`, channel.ParamOverride)
	assert.Equal(t, `{"X-Test":"yes"}`, channel.HeaderOverride)
	assert.JSONEq(t, `{"operator_note":"preserved"}`, channel.ChannelInfo)
}

func TestChannelCreateBatchAndMultiKeyModesAreBoundedAndSecretSafe(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	response := do(http.MethodPost, "/api/channel", `{
		"mode":"batch","batch_add_set_key_prefix_2_name":true,
		"channel":{"name":"batch","type":1,"key":"sk-first-secret\n\nsk-second-secret ","models":"gpt-4o","group":"default"}
	}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, true, decodeBody(t, response)["success"])
	var batch []model.Channel
	require.NoError(t, model.DB.Where("name LIKE ?", "batch %").Find(&batch).Error)
	require.Len(t, batch, 2)
	sort.Slice(batch, func(i, j int) bool { return batch[i].Key < batch[j].Key })
	assert.Equal(t, []string{"sk-first-secret", "sk-second-secret"}, []string{batch[0].Key, batch[1].Key})
	for _, channel := range batch {
		assert.NotContains(t, channel.Name, "sk-", "batch labels must use fingerprints, never credential prefixes")
		assert.Regexp(t, `^batch [0-9a-f]{8}$`, channel.Name)
	}

	response = do(http.MethodPost, "/api/channel", `{
		"mode":"multi_to_single","multi_key_mode":"polling",
		"channel":{"name":"multi","type":1,"key":"sk-one\n sk-two\n","models":"gpt-4o","group":"default"}
	}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, true, decodeBody(t, response)["success"])
	var multi model.Channel
	require.NoError(t, model.DB.Where("name = ?", "multi").First(&multi).Error)
	assert.Equal(t, "sk-one\nsk-two", multi.Key)
	var info map[string]any
	require.NoError(t, jsonutil.UnmarshalJsonStr(multi.ChannelInfo, &info))
	assert.Equal(t, true, info["is_multi_key"])
	assert.Equal(t, float64(2), info["multi_key_size"])
	assert.Equal(t, "polling", info["multi_key_mode"])

	before := int64(0)
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&before).Error)
	response = do(http.MethodPost, "/api/channel", `{"mode":"unsupported","channel":{"name":"bad","type":1,"key":"sk-bad","models":"gpt-4o"}}`)
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = do(http.MethodPost, "/api/channel", fmt.Sprintf(`{"mode":"batch","channel":{"name":"too-many","type":1,"key":%q,"models":"gpt-4o"}}`, strings.Repeat("x\n", 1001)))
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	after := int64(0)
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&after).Error)
	assert.Equal(t, before, after)
}

func TestChannelUpdatePersistsEveryClassifiedProviderField(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "full-update", int(channelcatalog.ChannelTypeOpenAI),
		channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&channel).Update("channel_info", `{"is_multi_key":true,"multi_key_size":2}`).Error)

	response := do(http.MethodPut, "/api/channel", fmt.Sprintf(`{
		"id":%d,"openai_organization":"org-updated","test_model":"gpt-4.1",
		"other":"opaque-updated","auto_ban":0,"other_info":"{\"region\":\"west\"}",
		"param_override":"{\"temperature\":1}","header_override":"{\"X-Test\":\"updated\"}",
		"channel_info":{"is_multi_key":true,"multi_key_size":2},"multi_key_mode":"random"
	}`, channel.Id))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, true, decodeBody(t, response)["success"])

	updated := channelByID(t, channel.Id)
	assert.Equal(t, "org-updated", updated.OpenAIOrganization)
	assert.Equal(t, "gpt-4.1", updated.TestModel)
	assert.Equal(t, "opaque-updated", updated.Other)
	require.NotNil(t, updated.AutoBan)
	assert.Equal(t, 0, *updated.AutoBan)
	assert.Equal(t, `{"region":"west"}`, updated.OtherInfo)
	assert.Equal(t, `{"temperature":1}`, updated.ParamOverride)
	assert.Equal(t, `{"X-Test":"updated"}`, updated.HeaderOverride)
	assert.JSONEq(t, `{"is_multi_key":true,"multi_key_size":2,"multi_key_mode":"random"}`, updated.ChannelInfo)
}
