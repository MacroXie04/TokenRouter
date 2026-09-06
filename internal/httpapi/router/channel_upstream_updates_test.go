package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func seedUpstreamUpdateChannel(t *testing.T, name, baseURL, models, settings string) model.Channel {
	t.Helper()
	channel := createSearchChannel(t, name, int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled,
		"default", models, "", 0)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":      "secret-" + name,
		"base_url": baseURL,
		"settings": settings,
	}).Error)
	return channel
}

func TestChannelUpstreamUpdateRouteLifecycle(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer secret-upstream-route", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":[{"id":"old"},{"id":"new"}]}`))
	}))
	t.Cleanup(server.Close)
	channel := seedUpstreamUpdateChannel(t, "upstream-route", server.URL, "old,removed",
		`{"future_provider_setting":{"keep":true},"upstream_model_update_check_enabled":true}`)
	require.NoError(t, model.DB.Create(&[]model.Ability{
		{Group: "default", Model: "old", ChannelId: channel.Id, Enabled: true, Weight: 1},
		{Group: "default", Model: "removed", ChannelId: channel.Id, Enabled: true, Weight: 1},
	}).Error)

	detect := do(http.MethodPost, "/api/channel/upstream_updates/detect", fmt.Sprintf(`{"id":%d}`, channel.Id))
	require.Equal(t, http.StatusOK, detect.Code, detect.Body.String())
	detectBody := decodeBody(t, detect)
	require.Equal(t, true, detectBody["success"])
	detectData := detectBody["data"].(map[string]any)
	assert.Equal(t, float64(channel.Id), detectData["channel_id"])
	assert.Equal(t, []any{"new"}, detectData["add_models"])
	assert.Equal(t, []any{"removed"}, detectData["remove_models"])
	assert.NotContains(t, detect.Body.String(), "secret-upstream-route")

	apply := do(http.MethodPost, "/api/channel/upstream_updates/apply", fmt.Sprintf(
		`{"id":%d,"add_models":["new","forged"],"remove_models":["removed","forged"]}`, channel.Id))
	require.Equal(t, http.StatusOK, apply.Code, apply.Body.String())
	applyBody := decodeBody(t, apply)
	require.Equal(t, true, applyBody["success"])
	applyData := applyBody["data"].(map[string]any)
	assert.Equal(t, []any{"new"}, applyData["added_models"])
	assert.Equal(t, []any{"removed"}, applyData["removed_models"])
	assert.Equal(t, "old,new", applyData["models"])
	assert.Contains(t, applyData["settings"], "future_provider_setting")
	assert.NotContains(t, apply.Body.String(), "secret-upstream-route")

	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	assert.Equal(t, "old,new", reloaded.Models)
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Order("model asc").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	assert.Equal(t, "new", abilities[0].Model)
	assert.Equal(t, "old", abilities[1].Model)

	var audit model.Log
	require.NoError(t, model.DB.Where("content LIKE ?", "channel.upstream_apply%").Order("id desc").First(&audit).Error)
	assert.NotContains(t, audit.Content, "secret-upstream-route")
}

func TestChannelUpstreamUpdateRoutesValidateAndHonorPermissions(t *testing.T) {
	root, admin, plain := setupPermissionTest(t)
	channel := seedUpstreamUpdateChannel(t, "permission-upstream", "https://example.invalid", "old",
		`{"upstream_model_update_check_enabled":true,"upstream_model_update_last_detected_models":["new"]}`)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "old", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)

	// Baseline administrators hold write, so applying a staged update succeeds.
	apply := admin.do(http.MethodPost, "/api/channel/upstream_updates/apply",
		fmt.Sprintf(`{"id":%d,"add_models":["new"]}`, channel.Id))
	require.Equal(t, http.StatusOK, apply.Code, apply.Body.String())
	assert.Equal(t, true, decodeBody(t, apply)["success"])

	// A non-admin never reaches the fine-grained permission layer.
	denied := plain.do(http.MethodPost, "/api/channel/upstream_updates/detect", fmt.Sprintf(`{"id":%d}`, channel.Id))
	assert.Equal(t, http.StatusForbidden, denied.Code)

	// Removing operate denies detect/detect-all but does not affect read/write.
	grant := root.do(http.MethodPut, "/api/user", fmt.Sprintf(
		`{"id":%d,"admin_permissions":{"channel":{"operate":false}}}`, admin.userID))
	require.Equal(t, http.StatusOK, grant.Code, grant.Body.String())
	denied = admin.do(http.MethodPost, "/api/channel/upstream_updates/detect", fmt.Sprintf(`{"id":%d}`, channel.Id))
	assert.Equal(t, http.StatusForbidden, denied.Code)
	denied = admin.do(http.MethodPost, "/api/channel/upstream_updates/detect_all", "")
	assert.Equal(t, http.StatusForbidden, denied.Code)

	// Removing write likewise denies both apply variants.
	grant = root.do(http.MethodPut, "/api/user", fmt.Sprintf(
		`{"id":%d,"admin_permissions":{"channel":{"write":false}}}`, admin.userID))
	require.Equal(t, http.StatusOK, grant.Code, grant.Body.String())
	denied = admin.do(http.MethodPost, "/api/channel/upstream_updates/apply", fmt.Sprintf(`{"id":%d}`, channel.Id))
	assert.Equal(t, http.StatusForbidden, denied.Code)
	denied = admin.do(http.MethodPost, "/api/channel/upstream_updates/apply_all", "")
	assert.Equal(t, http.StatusForbidden, denied.Code)

	for _, body := range []string{"{", `{}`, `{"id":0}`} {
		response := root.do(http.MethodPost, "/api/channel/upstream_updates/apply", body)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, false, decodeBody(t, response)["success"])
	}
}

func TestChannelUpstreamApplyAllAndDetectAllTaskContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	pending := `{"upstream_model_update_check_enabled":true,"upstream_model_update_last_detected_models":["new"],"upstream_model_update_last_removed_models":["old"]}`
	channel := seedUpstreamUpdateChannel(t, "apply-all", "https://example.invalid", "old", pending)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "old", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)

	applyAll := do(http.MethodPost, "/api/channel/upstream_updates/apply_all", "")
	require.Equal(t, http.StatusOK, applyAll.Code, applyAll.Body.String())
	body := decodeBody(t, applyAll)
	require.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, float64(1), data["processed_channels"])
	assert.Equal(t, float64(1), data["added_models"])
	assert.Equal(t, float64(1), data["removed_models"])
	results := data["results"].([]any)
	require.Len(t, results, 1)
	result := results[0].(map[string]any)
	assert.Equal(t, float64(channel.Id), result["channel_id"])
	assert.NotContains(t, result, "models", "bulk result must retain the reference response shape")
	assert.NotContains(t, result, "settings")

	first := do(http.MethodPost, "/api/channel/upstream_updates/detect_all", "")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	firstBody := decodeBody(t, first)
	require.Equal(t, true, firstBody["success"])
	firstData := firstBody["data"].(map[string]any)
	assert.Equal(t, model.SystemTaskStatusPending, firstData["status"])
	taskID := firstData["task_id"].(string)

	var task model.SystemTask
	require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
	assert.Equal(t, model.SystemTaskTypeModelUpdate, task.Type)
	assert.Contains(t, task.Payload, `"manual":true`)

	conflict := do(http.MethodPost, "/api/channel/upstream_updates/detect_all", "")
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	conflictBody := decodeBody(t, conflict)
	assert.Equal(t, false, conflictBody["success"])
	assert.Equal(t, taskID, conflictBody["data"].(map[string]any)["task_id"])
}

func TestChannelSettingsCreateUpdateAndResponseContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	initial := `{"future_setting":"kept","upstream_model_update_check_enabled":true}`
	create := do(http.MethodPost, "/api/channel", fmt.Sprintf(
		`{"name":"settings-contract","type":1,"key":"secret","models":"old","group":"default","settings":%q}`,
		initial))
	require.Equal(t, http.StatusOK, create.Code, create.Body.String())
	require.Equal(t, true, decodeBody(t, create)["success"])

	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "settings-contract").First(&channel).Error)
	assert.JSONEq(t, initial, channel.OtherSettings)

	invalid := do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"settings":"[]"}`, channel.Id))
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.JSONEq(t, initial, channel.OtherSettings, "invalid settings must not partially update the channel")

	next := `{"future_setting":"kept","upstream_model_update_auto_sync_enabled":true,"upstream_model_update_check_enabled":true}`
	update := do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"settings":%q}`, channel.Id, next))
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	require.Equal(t, true, decodeBody(t, update)["success"])
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.JSONEq(t, next, channel.OtherSettings)

	get := do(http.MethodGet, fmt.Sprintf("/api/channel/%d", channel.Id), "")
	require.Equal(t, http.StatusOK, get.Code, get.Body.String())
	data := decodeBody(t, get)["data"].(map[string]any)
	assert.Equal(t, next, data["settings"])
	assert.NotContains(t, data, "other_settings")
	assert.Equal(t, "", data["key"])

	missingAdvanced := do(http.MethodPost, "/api/channel",
		`{"name":"advanced-missing","type":58,"key":"secret","models":"old","group":"default","settings":"{}"}`)
	assert.Equal(t, http.StatusBadRequest, missingAdvanced.Code, missingAdvanced.Body.String())
	assert.Contains(t, decodeBody(t, missingAdvanced)["message"], "advanced_custom is required")

	advancedWithoutModels := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat/completions","converter":"none"}]}}`
	createAdvanced := do(http.MethodPost, "/api/channel", fmt.Sprintf(
		`{"name":"advanced-settings","type":58,"key":"secret","models":"old","group":"default","settings":%q}`,
		advancedWithoutModels))
	require.Equal(t, http.StatusOK, createAdvanced.Code, createAdvanced.Body.String())
	var advanced model.Channel
	require.NoError(t, model.DB.Where("name = ?", "advanced-settings").First(&advanced).Error)

	enableWithoutRoute := `{"upstream_model_update_check_enabled":true,"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat/completions","converter":"none"}]}}`
	update = do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"settings":%q}`, advanced.Id, enableWithoutRoute))
	assert.Equal(t, http.StatusBadRequest, update.Code, update.Body.String())
	assert.Contains(t, decodeBody(t, update)["message"], "require a /v1/models route")
	require.NoError(t, model.DB.First(&advanced, advanced.Id).Error)
	assert.JSONEq(t, advancedWithoutModels, advanced.OtherSettings, "failed validation must not persist settings")

	enableWithRoute := `{"upstream_model_update_check_enabled":true,"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/models","upstream_path":"/provider/models","converter":"none"}]}}`
	update = do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"settings":%q}`, advanced.Id, enableWithRoute))
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	require.NoError(t, model.DB.First(&advanced, advanced.Id).Error)
	assert.JSONEq(t, enableWithRoute, advanced.OtherSettings)

	// Changing an ordinary channel's type cannot create an invalid advanced
	// channel by retaining settings that lack advanced_custom.
	update = do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"type":58}`, channel.Id))
	assert.Equal(t, http.StatusBadRequest, update.Code, update.Body.String())
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, int(channelcatalog.ChannelTypeOpenAI), channel.Type)
}
