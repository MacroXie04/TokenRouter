package router_test

import (
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAddChannelRollsBackWhenAbilityCreationFails(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_add_channel_ability"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected ability create failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	rec := do(http.MethodPost, "/api/channel", `{"name":"atomic-create","type":1,"key":"sk-x","models":"gpt-4o","group":"default"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "injected ability create failure")
	var channels, abilities int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "atomic-create").Count(&channels).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Count(&abilities).Error)
	assert.Zero(t, channels)
	assert.Zero(t, abilities)

	rec = do(http.MethodPost, "/api/channel", `{"name":"atomic-create","type":1,"key":"sk-x","models":"gpt-4o","group":"default"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"], "creation response retains the reference envelope")
	assert.NotContains(t, rec.Body.String(), "sk-x", "channel creation must not disclose the upstream key")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "atomic-create").Count(&channels).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Count(&abilities).Error)
	assert.Equal(t, int64(1), channels)
	assert.Equal(t, int64(1), abilities)
}

func TestUpdateChannelRollsBackWhenAbilityRebuildFails(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "atomic-update", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "old-model", "", 0)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "old-model", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_update_channel_ability"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected ability rebuild failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	rec := do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"models":"new-model"}`, channel.Id))
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "injected ability rebuild failure")
	var got model.Channel
	require.NoError(t, model.DB.First(&got, channel.Id).Error)
	assert.Equal(t, "old-model", got.Models)
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "old-model", abilities[0].Model)

	rec = do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"models":"new-model"}`, channel.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&got, channel.Id).Error)
	assert.Equal(t, "new-model", got.Models)
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "new-model", abilities[0].Model)
}

func TestDeleteChannelRollsBackWhenAbilityDeleteFails(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "atomic-delete", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_delete_channel_ability"
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected ability delete failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Delete().Remove(callbackName) })

	rec := do(http.MethodDelete, "/api/channel/"+textutil.Int2Str(channel.Id), "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "injected ability delete failure")
	var channelCount, abilityCount int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Equal(t, int64(1), channelCount)
	assert.Equal(t, int64(1), abilityCount)

	rec = do(http.MethodDelete, "/api/channel/"+textutil.Int2Str(channel.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Zero(t, channelCount)
	assert.Zero(t, abilityCount)
}

func TestAddChannelReturnsAbilityCacheRefreshFailure(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_add_channel_cache_refresh"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected ability cache refresh failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	rec := do(http.MethodPost, "/api/channel", `{"name":"cache-refresh","type":1,"key":"sk-x","models":"gpt-4o","group":"default"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "injected ability cache refresh failure")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "cache-refresh").First(&channel).Error)
	var abilityCount int64
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Equal(t, int64(1), abilityCount, "durable channel/ability rows remain internally consistent")
	require.NoError(t, channelssvc.SyncAbilityCache(), "a later cache refresh can safely retry")
}

func TestDeleteChannelCacheRefreshFailureIsExplicitAndRetryable(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "delete-cache-refresh", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_delete_channel_cache_refresh"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected delete cache refresh failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	path := "/api/channel/" + textutil.Int2Str(channel.Id)
	rec := do(http.MethodDelete, path, "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "injected delete cache refresh failure")
	var channelCount, abilityCount int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Zero(t, channelCount)
	assert.Zero(t, abilityCount)

	rec = do(http.MethodDelete, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
}

func TestChannelStatusRollsBackWhenAbilityWriteFailsAndRetries(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "status-rollback", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	ability := model.Ability{Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_status_ability_update"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected status ability failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	path := "/api/channel/" + textutil.Int2Str(channel.Id) + "/status"
	rec := do(http.MethodPost, path, `{"status":3}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var gotChannel model.Channel
	var gotAbility model.Ability
	require.NoError(t, model.DB.First(&gotChannel, channel.Id).Error)
	require.NoError(t, model.DB.First(&gotAbility, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, gotChannel.Status)
	assert.True(t, gotAbility.Enabled)

	rec = do(http.MethodPost, path, `{"status":3}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.First(&gotChannel, channel.Id).Error)
	require.NoError(t, model.DB.First(&gotAbility, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, gotChannel.Status)
	assert.False(t, gotAbility.Enabled)
}

func TestTagEditRollsBackAbilityRebuildFailureAndRetries(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "tag-rollback", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "old-model", "atomic", 0)
	tag := "atomic"
	ability := model.Ability{Group: "default", Model: "old-model", ChannelId: channel.Id, Enabled: true, Weight: 1, Tag: &tag}
	require.NoError(t, model.DB.Create(&ability).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_tag_ability_rebuild"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected tag ability rebuild failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	rec := do(http.MethodPut, "/api/channel/tag", `{"tag":"atomic","models":"new-model"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var got model.Channel
	require.NoError(t, model.DB.First(&got, channel.Id).Error)
	assert.Equal(t, "old-model", got.Models)
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "old-model", abilities[0].Model)

	rec = do(http.MethodPut, "/api/channel/tag", `{"tag":"atomic","models":"new-model"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.First(&got, channel.Id).Error)
	assert.Equal(t, "new-model", got.Models)
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "new-model", abilities[0].Model)
}

func TestTagStatusRollsBackAbilityWriteFailureAndRetries(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "tag-status-rollback", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "atomic-status", 0)
	tag := "atomic-status"
	ability := model.Ability{Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true, Weight: 1, Tag: &tag}
	require.NoError(t, model.DB.Create(&ability).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_tag_status_ability_update"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected tag status ability failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	rec := do(http.MethodPost, "/api/channel/tag/disabled", `{"tag":"atomic-status"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var gotChannel model.Channel
	var gotAbility model.Ability
	require.NoError(t, model.DB.First(&gotChannel, channel.Id).Error)
	require.NoError(t, model.DB.First(&gotAbility, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.Equal(t, channelcatalog.ChannelStatusEnabled, gotChannel.Status)
	assert.True(t, gotAbility.Enabled)

	rec = do(http.MethodPost, "/api/channel/tag/disabled", `{"tag":"atomic-status"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.First(&gotChannel, channel.Id).Error)
	require.NoError(t, model.DB.First(&gotAbility, ability.Group, ability.Model, ability.ChannelId).Error)
	assert.Equal(t, channelcatalog.ChannelStatusManuallyDisabled, gotChannel.Status)
	assert.False(t, gotAbility.Enabled)
}

func TestBatchDeleteRollsBackAfterAbilityDeleteAndRetries(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "batch-delete-rollback", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	ability := model.Ability{Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&ability).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_batch_channel_delete"
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Channel{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected batch channel delete failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Delete().Remove(callbackName) })

	body := fmt.Sprintf(`{"ids":[%d]}`, channel.Id)
	rec := do(http.MethodPost, "/api/channel/batch", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var channelCount, abilityCount int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Equal(t, int64(1), channelCount)
	assert.Equal(t, int64(1), abilityCount)

	rec = do(http.MethodPost, "/api/channel/batch", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Zero(t, channelCount)
	assert.Zero(t, abilityCount)
}

func TestFixAbilitiesRollsBackFailedRebuildAndRetries(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "fix-rollback", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "new-model", "", 0)
	stale := model.Ability{Group: "default", Model: "stale-model", ChannelId: channel.Id, Enabled: true, Weight: 1}
	require.NoError(t, model.DB.Create(&stale).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_fix_ability_create"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected fix ability failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	rec := do(http.MethodPost, "/api/channel/fix", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "stale-model", abilities[0].Model)

	rec = do(http.MethodPost, "/api/channel/fix", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "new-model", abilities[0].Model)
}

func TestCopyChannelRollsBackAbilityCreationFailureAndRetries(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	origin := createSearchChannel(t, "copy-rollback", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_copy_ability_create"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected copy ability failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	path := "/api/channel/copy/" + textutil.Int2Str(origin.Id)
	rec := do(http.MethodPost, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var cloneCount int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "copy-rollback_复制").Count(&cloneCount).Error)
	assert.Zero(t, cloneCount)

	rec = do(http.MethodPost, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	var clone model.Channel
	require.NoError(t, model.DB.Where("name = ?", "copy-rollback_复制").First(&clone).Error)
	var abilityCount int64
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", clone.Id).Count(&abilityCount).Error)
	assert.Equal(t, int64(1), abilityCount)
}

func TestChannelTestDoesNotClaimSuccessWhenResultPersistenceFails(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	channel := createSearchChannel(t, "health-write", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{"base_url": upstream.URL, "test_model": "gpt-4o"}).Error)

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_channel_test_result"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Channel{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected health result failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	rec := do(http.MethodGet, "/api/channel/test/"+textutil.Int2Str(channel.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "injected health result failure")

	rec = do(http.MethodGet, "/api/channel/test/"+textutil.Int2Str(channel.Id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
}
