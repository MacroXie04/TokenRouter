package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createWriteChannel inserts a channel with full write-batch fields.
func createWriteChannel(t *testing.T, name string, status int, group, models, tag string, priority int64) model.Channel {
	t.Helper()
	ch := createSearchChannel(t, name, int(channelcatalog.ChannelTypeOpenAI), status, group, models, tag, priority)
	return ch
}

func channelByID(t *testing.T, id int) model.Channel {
	t.Helper()
	var ch model.Channel
	require.NoError(t, model.DB.First(&ch, id).Error)
	return ch
}

func TestChannelStatusUpdateEndpoints(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	ch := createWriteChannel(t, "statusch", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	ability := model.Ability{Group: "default", Model: "gpt-4o", ChannelId: ch.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)

	// Disable a single channel.
	rec := do(http.MethodPost, "/api/channel/"+textutil.Int2Str(ch.Id)+"/status", `{"status":3}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, true, body["data"])
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, channelByID(t, ch.Id).Status)
	// other_info records the reason/time.
	info := channelByID(t, ch.Id).OtherInfo
	assert.Contains(t, info, "manual operation")
	// The ability enabled flag follows the status.
	var ab model.Ability
	require.NoError(t, model.DB.First(&ab, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.False(t, ab.Enabled)

	// Same status again: changed=false.
	rec = do(http.MethodPost, "/api/channel/"+textutil.Int2Str(ch.Id)+"/status", `{"status":3}`)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, false, body["data"])

	// Invalid status rejected.
	rec = do(http.MethodPost, "/api/channel/"+textutil.Int2Str(ch.Id)+"/status", `{"status":4}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Batch: two channels, one already disabled -> changed count 1.
	ch2 := createWriteChannel(t, "statusch2", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	rec = do(http.MethodPost, "/api/channel/status/batch", fmt.Sprintf(`{"ids":[%d,%d],"status":3}`, ch.Id, ch2.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(1), body["data"])
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, channelByID(t, ch2.Id).Status)

	// Empty ids rejected.
	rec = do(http.MethodPost, "/api/channel/status/batch", `{"ids":[],"status":3}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestChannelBatchMutationsRejectUnboundedOrAmbiguousIDs(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	channel := createWriteChannel(t, "bounded-batch", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "original", 0)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true, Weight: 1, Tag: &channel.Tag,
	}).Error)

	oversizedIDs := make([]int, 501)
	for index := range oversizedIDs {
		oversizedIDs[index] = index + 1
	}
	for name, request := range map[string]struct {
		method string
		path   string
		body   map[string]any
	}{
		"status": {http.MethodPost, "/api/channel/status/batch", map[string]any{"ids": oversizedIDs, "status": 3}},
		"delete": {http.MethodPost, "/api/channel/batch", map[string]any{"ids": oversizedIDs}},
		"tag":    {http.MethodPost, "/api/channel/batch/tag", map[string]any{"ids": oversizedIDs, "tag": "changed"}},
	} {
		t.Run(name+" oversized", func(t *testing.T) {
			encoded, err := jsonutil.Marshal(request.body)
			require.NoError(t, err)
			rec := do(request.method, request.path, string(encoded))
			assert.Equal(t, false, decodeBody(t, rec)["success"], rec.Body.String())
		})

		t.Run(name+" duplicate", func(t *testing.T) {
			request.body["ids"] = []int{channel.Id, channel.Id}
			encoded, err := jsonutil.Marshal(request.body)
			require.NoError(t, err)
			rec := do(request.method, request.path, string(encoded))
			assert.Equal(t, false, decodeBody(t, rec)["success"], rec.Body.String())
		})
	}

	stored := channelByID(t, channel.Id)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, "original", stored.Tag)
	var ability model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
	assert.True(t, ability.Enabled)
	require.NotNil(t, ability.Tag)
	assert.Equal(t, "original", *ability.Tag)
}

func TestChannelDeleteDisabledEndpoint(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	enabled := createWriteChannel(t, "keepme", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	createWriteChannel(t, "delme1", channelcatalog.ChannelStatusAutoDisabled, "default", "gpt-4o", "", 0)
	createWriteChannel(t, "delme2", channelcatalog.ChannelStatusManuallyDisabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4o", ChannelId: enabled.Id, Enabled: true, Weight: 1}).Error)

	rec := do(http.MethodDelete, "/api/channel/disabled", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(2), body["data"])

	var count int64
	model.DB.Model(&model.Channel{}).Count(&count)
	assert.Equal(t, int64(1), count, "enabled channel must survive")
}

func TestChannelTagEndpoints(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	fast1 := createWriteChannel(t, "fast1", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "fast", 10)
	fast2 := createWriteChannel(t, "fast2", channelcatalog.ChannelStatusEnabled, "default", "claude-sonnet", "fast", 20)
	slow := createWriteChannel(t, "slow1", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o-mini", "slow", 5)

	// Disable by tag.
	rec := do(http.MethodPost, "/api/channel/tag/disabled", `{"tag":"fast"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, channelByID(t, fast1.Id).Status)
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, channelByID(t, fast2.Id).Status)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, channelByID(t, slow.Id).Status)

	// Missing tag rejected.
	rec = do(http.MethodPost, "/api/channel/tag/disabled", `{}`)
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "参数错误", body["message"])

	for _, unsafeTag := range []string{strings.Repeat("x", 65), "fast\u202e"} {
		encoded, err := jsonutil.Marshal(map[string]string{"tag": unsafeTag})
		require.NoError(t, err)
		rec = do(http.MethodPost, "/api/channel/tag/disabled", string(encoded))
		assert.Equal(t, false, decodeBody(t, rec)["success"])
		assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, channelByID(t, fast1.Id).Status)
	}

	// Enable by tag.
	rec = do(http.MethodPost, "/api/channel/tag/enabled", `{"tag":"fast"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, channelByID(t, fast1.Id).Status)

	// Edit: priority bump applies to all channels with the tag.
	rec = do(http.MethodPut, "/api/channel/tag", `{"tag":"fast","priority":99}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NotNil(t, channelByID(t, fast1.Id).Priority)
	assert.Equal(t, int64(99), *channelByID(t, fast1.Id).Priority)
	require.NotNil(t, channelByID(t, fast2.Id).Priority)
	assert.Equal(t, int64(99), *channelByID(t, fast2.Id).Priority)
	require.NotNil(t, channelByID(t, slow.Id).Priority)
	assert.Equal(t, int64(5), *channelByID(t, slow.Id).Priority, "other tags untouched")

	// Edit: rename the tag.
	rec = do(http.MethodPut, "/api/channel/tag", `{"tag":"fast","new_tag":"turbo"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, "turbo", channelByID(t, fast1.Id).Tag)
	assert.Equal(t, "turbo", channelByID(t, fast2.Id).Tag)

	// Edit: invalid JSON override rejected with the reference message.
	rec = do(http.MethodPut, "/api/channel/tag", `{"tag":"turbo","param_override":"not-json"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "参数覆盖必须是合法的 JSON 格式", body["message"])

	// Edit: valid JSON override applies.
	rec = do(http.MethodPut, "/api/channel/tag", `{"tag":"turbo","param_override":"{\"max_tokens\":1}"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, `{"max_tokens":1}`, channelByID(t, fast1.Id).ParamOverride)

	// Edit: missing tag rejected.
	rec = do(http.MethodPut, "/api/channel/tag", `{"priority":1}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "tag不能为空", body["message"])

	// Edit: models update rebuilds abilities for affected channels.
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "stale", ChannelId: fast1.Id, Enabled: true, Weight: 1}).Error)
	rec = do(http.MethodPut, "/api/channel/tag", `{"tag":"turbo","models":"gpt-4o,new-model"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", fast1.Id).Find(&abilities).Error)
	require.Len(t, abilities, 2)
	for _, ab := range abilities {
		assert.NotEqual(t, "stale", ab.Model)
		assert.NotEqual(t, "claude-sonnet", ab.Model)
	}
}

func TestChannelDeleteBatchEndpoint(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	ch1 := createWriteChannel(t, "batch1", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	ch2 := createWriteChannel(t, "batch2", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	ch3 := createWriteChannel(t, "batch3", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4o", ChannelId: ch1.Id, Enabled: true, Weight: 1}).Error)

	rec := do(http.MethodPost, "/api/channel/batch", fmt.Sprintf(`{"ids":[%d,%d]}`, ch1.Id, ch2.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(2), body["data"])

	var chCount, abCount int64
	model.DB.Model(&model.Channel{}).Count(&chCount)
	model.DB.Model(&model.Ability{}).Count(&abCount)
	assert.Equal(t, int64(1), chCount, "only ch3 remains")
	assert.Equal(t, int64(0), abCount, "abilities of deleted channels removed")

	// Empty ids rejected.
	rec = do(http.MethodPost, "/api/channel/batch", `{"ids":[]}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "参数错误", body["message"])
	_ = ch3
}

func TestChannelFixAbilitiesEndpoint(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	enabled := createWriteChannel(t, "fixme", channelcatalog.ChannelStatusEnabled, "default,vip", "gpt-4o, claude-sonnet", "", 7)
	disabled := createWriteChannel(t, "fixoff", channelcatalog.ChannelStatusManuallyDisabled, "default", "gpt-4o-mini", "", 0)
	// Stale ability rows that the rebuild must replace.
	require.NoError(t, model.DB.Create(&model.Ability{Group: "bogus", Model: "bogus-model", ChannelId: enabled.Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-4o-mini", ChannelId: disabled.Id, Enabled: true, Weight: 1}).Error)

	rec := do(http.MethodPost, "/api/channel/fix", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	assert.Equal(t, float64(2), data["success"], "both channels rebuilt")
	assert.Equal(t, float64(0), data["fails"])

	var abilities []model.Ability
	require.NoError(t, model.DB.Order("channel_id").Find(&abilities).Error)
	// enabled channel: 2 models x 2 groups = 4 abilities, enabled, priority 7.
	var enabledAbilities, disabledAbilities []model.Ability
	for _, ab := range abilities {
		switch ab.ChannelId {
		case enabled.Id:
			enabledAbilities = append(enabledAbilities, ab)
		case disabled.Id:
			disabledAbilities = append(disabledAbilities, ab)
		}
	}
	require.Len(t, enabledAbilities, 4, "models x groups cross product")
	for _, ab := range enabledAbilities {
		assert.True(t, ab.Enabled)
		require.NotNil(t, ab.Priority)
		assert.Equal(t, int64(7), *ab.Priority)
		assert.NotEqual(t, "bogus-model", ab.Model)
	}
	require.Len(t, disabledAbilities, 1)
	assert.False(t, disabledAbilities[0].Enabled, "disabled channel's ability must be disabled")
}

func TestChannelFetchModelsEndpoints(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer sk-up", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-x"},{"id":"gpt-y"}]}`))
	}))
	defer up.Close()

	ch := createWriteChannel(t, "fetchch", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&ch).Updates(map[string]any{"base_url": up.URL, "key": "sk-up"}).Error)

	// Fetch from a stored channel.
	rec := do(http.MethodGet, "/api/channel/fetch_models/"+textutil.Int2Str(ch.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].([]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	require.Len(t, data, 2)
	assert.Equal(t, "gpt-x", data[0])
	assert.Equal(t, "gpt-y", data[1])

	// Fetch from a request-built channel description.
	rec = do(http.MethodPost, "/api/channel/fetch_models", fmt.Sprintf(`{"type":1,"base_url":%q,"key":"sk-up"}`, up.URL))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok = body["data"].([]any)
	require.True(t, ok)
	require.Len(t, data, 2)

	// Bad request body.
	rec = do(http.MethodPost, "/api/channel/fetch_models", `not-json`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "Invalid request", decodeBody(t, rec)["message"])

	// Failing upstream: reference message prefix.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	badCh := createWriteChannel(t, "fetchbad", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&badCh).Updates(map[string]any{"base_url": down.URL, "key": "k"}).Error)
	rec = do(http.MethodGet, "/api/channel/fetch_models/"+textutil.Int2Str(badCh.Id), "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "获取模型列表失败")

	// Unknown channel id.
	rec = do(http.MethodGet, "/api/channel/fetch_models/999999", "")
	assert.Equal(t, false, decodeBody(t, rec)["success"])
}

func TestChannelFetchModelsAdvancedCustomPreview(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/provider/models", r.URL.Path)
		assert.Equal(t, "override preview-key", r.Header.Get("x-api-key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":[{"id":"custom-model"}]}`))
	}))
	defer up.Close()
	config := `{"advanced_routes":[{"incoming_path":"/v1/models","upstream_path":"/provider/models","converter":"none","auth":{"type":"header","name":"x-api-key","value":"route {api_key}"}}]}`
	headerOverride := `{"x-api-key":"override {api_key}"}`
	rec := do(http.MethodPost, "/api/channel/fetch_models", fmt.Sprintf(
		`{"type":%d,"base_url":%q,"key":"preview-key","advanced_custom":%q,"header_override":%q}`,
		channelcatalog.ChannelTypeAdvancedCustom, up.URL, config, headerOverride,
	))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, []any{"custom-model"}, body["data"])

	// Existing advanced-custom previews use the saved credential even when the
	// request attempts to supply a replacement key. The route itself may still
	// be previewed without persisting it.
	storedUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/edited/models", r.URL.Path)
		assert.Equal(t, "Bearer saved-key", r.Header.Get("Authorization"))
		assert.NotContains(t, r.Header.Get("Authorization"), "request-key")
		_, _ = w.Write([]byte(`{"data":[{"id":"saved-model"}]}`))
	}))
	defer storedUp.Close()
	priority := int64(0)
	weight := uint(1)
	stored := model.Channel{
		Type: int(channelcatalog.ChannelTypeAdvancedCustom), Key: "saved-key", Name: "advanced-preview",
		BaseURL: "http://127.0.0.1:1", Models: "old", Group: "default", Status: channelcatalog.ChannelStatusEnabled,
		Priority: &priority, Weight: &weight,
		OtherSettings: `{"future":true,"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/models","upstream_path":"/saved/models"}]}}`,
	}
	require.NoError(t, model.DB.Create(&stored).Error)
	editedConfig := `{"advanced_routes":[{"incoming_path":"/v1/models","upstream_path":"/edited/models"}]}`
	rec = do(http.MethodPost, "/api/channel/fetch_models", fmt.Sprintf(
		`{"channel_id":%d,"type":1,"base_url":%q,"key":"request-key","advanced_custom":%q}`,
		stored.Id, storedUp.URL, editedConfig,
	))
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"], rec.Body.String())
	assert.Equal(t, []any{"saved-model"}, body["data"])
	var unchanged model.Channel
	require.NoError(t, model.DB.First(&unchanged, stored.Id).Error)
	assert.Contains(t, unchanged.OtherSettings, `"future":true`)
	assert.Contains(t, unchanged.OtherSettings, "/saved/models")

	// A channel id on this preview endpoint is reserved for advanced-custom
	// editing, matching the reference contract.
	ordinary := createWriteChannel(t, "ordinary-preview", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	rec = do(http.MethodPost, "/api/channel/fetch_models", fmt.Sprintf(`{"channel_id":%d}`, ordinary.Id))
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "not an advanced custom channel")

	// TokenRouter deliberately rejects per-channel proxying here: delegating
	// DNS resolution to an arbitrary proxy would bypass the SSRF guard.
	rec = do(http.MethodPost, "/api/channel/fetch_models", fmt.Sprintf(
		`{"type":%d,"base_url":%q,"key":"preview-key","advanced_custom":%q,"proxy":"http://proxy.invalid"}`,
		channelcatalog.ChannelTypeAdvancedCustom, up.URL, config,
	))
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "proxy is not supported")

	rec = do(http.MethodPost, "/api/channel/fetch_models", fmt.Sprintf(
		`{"type":%d,"base_url":%q,"key":"preview-key","advanced_custom":"[]"}`,
		channelcatalog.ChannelTypeAdvancedCustom, up.URL,
	))
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "advanced_custom must be a JSON object")
}

func TestChannelBatchTagAndTagModels(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	ch1 := createWriteChannel(t, "tagme1", channelcatalog.ChannelStatusEnabled, "default", "m1,m2,m3", "old", 0)
	ch2 := createWriteChannel(t, "tagme2", channelcatalog.ChannelStatusEnabled, "default", "m1", "old", 0)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m1", ChannelId: ch1.Id, Enabled: true, Weight: 1}).Error)

	rec := do(http.MethodPost, "/api/channel/batch/tag", fmt.Sprintf(`{"ids":[%d,%d],"tag":"bulk"}`, ch1.Id, ch2.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(2), body["data"])
	assert.Equal(t, "bulk", channelByID(t, ch1.Id).Tag)
	assert.Equal(t, "bulk", channelByID(t, ch2.Id).Tag)
	// Ability rows mirror the new tag.
	var ab model.Ability
	require.NoError(t, model.DB.First(&ab).Error)
	require.NotNil(t, ab.Tag)
	assert.Equal(t, "bulk", *ab.Tag)

	// Tag models returns the longest model list.
	rec = do(http.MethodGet, "/api/channel/tag/models?tag=bulk", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "m1,m2,m3", body["data"])

	// Missing tag rejected.
	rec = do(http.MethodGet, "/api/channel/tag/models", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "tag不能为空", decodeBody(t, rec)["message"])

	// Empty ids rejected.
	rec = do(http.MethodPost, "/api/channel/batch/tag", `{"ids":[],"tag":"x"}`)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
}

func TestChannelCopyEndpoint(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	orig := createWriteChannel(t, "origin", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "cp", 9)
	require.NoError(t, model.DB.Model(&orig).Updates(map[string]any{"balance": 42.5, "used_quota": 100}).Error)

	rec := do(http.MethodPost, "/api/channel/copy/"+textutil.Int2Str(orig.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	cloneID, ok := data["id"].(float64)
	require.True(t, ok)
	clone := channelByID(t, int(cloneID))
	assert.NotEqual(t, orig.Id, clone.Id)
	assert.Equal(t, "origin_复制", clone.Name)
	assert.Equal(t, orig.Key, clone.Key, "key is copied")
	assert.Equal(t, float64(0), clone.Balance, "balance reset by default")
	assert.Equal(t, int64(0), clone.UsedQuota)
	assert.Equal(t, 0, clone.ResponseTime)
	assert.Equal(t, int64(0), clone.TestTime)

	// Keep balance + custom suffix.
	rec = do(http.MethodPost, "/api/channel/copy/"+textutil.Int2Str(orig.Id)+"?reset_balance=false&suffix=_v2", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data = decodeBody(t, rec)["data"].(map[string]any)
	clone2 := channelByID(t, int(data["id"].(float64)))
	assert.Equal(t, "origin_v2", clone2.Name)
	assert.Equal(t, float64(42.5), clone2.Balance)
	assert.Equal(t, int64(100), clone2.UsedQuota)

	// Missing origin.
	rec = do(http.MethodPost, "/api/channel/copy/999999", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "获取渠道信息失败，请稍后重试", body["message"])
}

func TestChannelMultiKeyManageEndpoint(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	multikey := createWriteChannel(t, "mk", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&multikey).Updates(map[string]any{
		"key":          "sk-k1\nsk-k2\nsk-k3",
		"channel_info": `{"is_multi_key":true,"multi_key_size":3,"multi_key_status_list":{},"multi_key_polling_index":0}`,
	}).Error)

	// Not multi-key channel rejected.
	plain := createWriteChannel(t, "plain", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	rec := do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status"}`, plain.Id))
	assert.Equal(t, "该渠道不是多密钥模式", decodeBody(t, rec)["message"])

	// Status listing: 3 keys, all enabled.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status"}`, multikey.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	keys, ok := data["keys"].([]any)
	require.True(t, ok)
	require.Len(t, keys, 3)
	assert.Equal(t, float64(3), data["enabled_count"])
	assert.Equal(t, float64(3), data["total"])
	assert.Equal(t, float64(1), data["total_pages"])
	firstKey, ok := keys[0].(map[string]any)
	require.True(t, ok)
	assert.Regexp(t, `^sha256:[0-9a-f]{8}$`, firstKey["key_preview"])
	assert.NotContains(t, rec.Body.String(), "sk-k1", "multi-key status must not disclose credential bytes")
	assert.Equal(t, float64(1), firstKey["status"])

	// Pagination: page_size=2 -> 2 keys on page 1, 1 on page 2.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status","page":2,"page_size":2}`, multikey.Id))
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Len(t, data["keys"].([]any), 1)
	assert.Equal(t, float64(2), data["total_pages"])

	// Extreme cursors are bounded before offset arithmetic.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status","page":2147483647,"page_size":2147483647}`, multikey.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Len(t, data["keys"].([]any), 3)
	assert.Equal(t, float64(1), data["page"], "out-of-range pages clamp to the final available page")
	assert.Equal(t, float64(100), data["page_size"])

	// Disable one key.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"disable_key","key_index":0}`, multikey.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "密钥已禁用", body["message"])

	// Filtered status listing.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status","status":2}`, multikey.Id))
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Len(t, data["keys"].([]any), 1)
	assert.Equal(t, float64(1), data["manual_disabled_count"])

	// Enable it again.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"enable_key","key_index":0}`, multikey.Id))
	assert.Equal(t, "密钥已启用", decodeBody(t, rec)["message"])

	// Disable all.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"disable_all_keys"}`, multikey.Id))
	assert.Equal(t, "已禁用 3 个密钥", decodeBody(t, rec)["message"])
	// No more to disable.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"disable_all_keys"}`, multikey.Id))
	assert.Equal(t, "没有可禁用的密钥", decodeBody(t, rec)["message"])
	// Enable all.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"enable_all_keys"}`, multikey.Id))
	assert.Equal(t, "已启用 3 个密钥", decodeBody(t, rec)["message"])

	// Delete one key (reindexes the rest).
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"delete_key","key_index":1}`, multikey.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "密钥已删除", decodeBody(t, rec)["message"])
	refreshed := channelByID(t, multikey.Id)
	assert.Equal(t, "sk-k1\nsk-k3", refreshed.Key)
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status"}`, multikey.Id))
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Len(t, data["keys"].([]any), 2)

	// Out-of-range index.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"disable_key","key_index":9}`, multikey.Id))
	assert.Equal(t, "密钥索引超出范围", decodeBody(t, rec)["message"])

	// Delete the last keys down to one: cannot delete the last remaining key.
	_ = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"delete_key","key_index":1}`, multikey.Id))
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"delete_key","key_index":0}`, multikey.Id))
	assert.Equal(t, "不能删除最后一个密钥", decodeBody(t, rec)["message"])

	// Auto-disabled keys can be swept.
	autoCh := createWriteChannel(t, "autok", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&autoCh).Updates(map[string]any{
		"key":          "sk-a1\nsk-a2",
		"channel_info": `{"is_multi_key":true,"multi_key_size":2,"multi_key_status_list":{"0":3},"multi_key_disabled_time":{"0":1},"multi_key_disabled_reason":{"0":"upstream failed"},"multi_key_polling_index":0}`,
	}).Error)
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"delete_disabled_keys"}`, autoCh.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "已删除 1 个自动禁用的密钥", body["message"])
	assert.Equal(t, float64(1), body["data"])
	assert.Equal(t, "sk-a2", channelByID(t, autoCh.Id).Key)
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"delete_disabled_keys"}`, autoCh.Id))
	assert.Equal(t, "没有需要删除的自动禁用密钥", decodeBody(t, rec)["message"])

	// A sweep must never erase the entire credential set, even when every key
	// has been automatically disabled.
	allAuto := createWriteChannel(t, "all-auto", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&allAuto).Updates(map[string]any{
		"key":          "sk-z1\nsk-z2",
		"channel_info": `{"is_multi_key":true,"multi_key_size":2,"multi_key_status_list":{"0":3,"1":3},"multi_key_polling_index":0}`,
	}).Error)
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"delete_disabled_keys"}`, allAuto.Id))
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Equal(t, "不能删除所有密钥", decodeBody(t, rec)["message"])
	assert.Equal(t, "sk-z1\nsk-z2", channelByID(t, allAuto.Id).Key)

	// Unknown action.
	rec = do(http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"dance"}`, multikey.Id))
	assert.Equal(t, "不支持的操作", decodeBody(t, rec)["message"])
}

func TestChannelBalanceEndpoints(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer sk-bal", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			_, _ = w.Write([]byte(`{"has_payment_method":true,"hard_limit_usd":100}`))
		case "/v1/dashboard/billing/usage":
			assert.Contains(t, r.URL.RawQuery, "start_date=")
			assert.Contains(t, r.URL.RawQuery, "end_date=")
			_, _ = w.Write([]byte(`{"total_usage":2500}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer up.Close()

	ch := createWriteChannel(t, "balch", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&ch).Updates(map[string]any{"base_url": up.URL, "key": "sk-bal"}).Error)

	// Single-channel balance: 100 - 2500/100 = 75.
	rec := do(http.MethodGet, "/api/channel/update_balance/"+textutil.Int2Str(ch.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(75), body["balance"])
	refreshed := channelByID(t, ch.Id)
	assert.Equal(t, float64(75), refreshed.Balance)
	assert.Greater(t, refreshed.BalanceUpdatedTime, int64(0))

	// All-channels sweep succeeds.
	rec = do(http.MethodGet, "/api/channel/update_balance", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	// Unsupported provider type reports the reference default.
	other := createWriteChannel(t, "otherch", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&other).Updates(map[string]any{"type": int(channelcatalog.ChannelTypeAnthropic), "base_url": up.URL, "key": "k"}).Error)
	rec = do(http.MethodGet, "/api/channel/update_balance/"+textutil.Int2Str(other.Id), "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "尚未实现", body["message"])

	// Multi-key channels refuse balance queries.
	mk := createWriteChannel(t, "mkbal", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&mk).Updates(map[string]any{
		"key":          "sk-a\nsk-b",
		"channel_info": `{"is_multi_key":true,"multi_key_size":2,"multi_key_polling_index":0}`,
	}).Error)
	rec = do(http.MethodGet, "/api/channel/update_balance/"+textutil.Int2Str(mk.Id), "")
	assert.Equal(t, "多密钥渠道不支持余额查询", decodeBody(t, rec)["message"])

	// A zero/negative balance during the sweep auto-disables the channel.
	zeroUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/dashboard/billing/subscription" {
			_, _ = w.Write([]byte(`{"has_payment_method":true,"hard_limit_usd":10}`))
			return
		}
		_, _ = w.Write([]byte(`{"total_usage":5000}`)) // 10 - 50 = -40
	}))
	defer zeroUp.Close()
	zch := createWriteChannel(t, "zeroch", channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&zch).Updates(map[string]any{"base_url": zeroUp.URL, "key": "k"}).Error)
	// Run the sweep through the all-endpoint after making the mock serve the zero case...
	// The sweep iterates all enabled channels; the first mock returns 75, the zero mock -40.
	_ = do(http.MethodGet, "/api/channel/update_balance", "")
	assert.Equal(t, channelcatalog.ChannelStatusAutoDisabled, channelByID(t, zch.Id).Status, "zero balance disables the channel")
	assert.Contains(t, channelByID(t, zch.Id).OtherInfo, "余额不足")

	// Unknown channel id.
	rec = do(http.MethodGet, "/api/channel/update_balance/999999", "")
	assert.Equal(t, false, decodeBody(t, rec)["success"])
}
