package router_test

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestAddAbilityFailsClosedOnLookupError(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_ability_lookup"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected ability lookup failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	rec := do(http.MethodPost, "/api/ability", `{"group":"default","model":"gpt-4o","channel_id":1}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, "查询能力失败", decodeBody(t, rec)["message"])
	var count int64
	require.NoError(t, model.DB.Model(&model.Ability{}).Count(&count).Error)
	assert.Zero(t, count, "a failed lookup must not be reinterpreted as a missing row")
}

func TestAbilityMutationsReportCacheRefreshFailure(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
		var queryCount atomic.Int32
		callbackName := "test:fail_add_ability_cache_refresh"
		require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.Ability{}).TableName() && queryCount.Add(1) == 2 {
				tx.AddError(errors.New("injected ability cache refresh failure"))
			}
		}))
		t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

		rec := do(http.MethodPost, "/api/ability", `{"group":"default","model":"gpt-4o","channel_id":1}`)
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.Contains(t, decodeBody(t, rec)["message"], "路由缓存刷新失败")
		var count int64
		require.NoError(t, model.DB.Model(&model.Ability{}).Count(&count).Error)
		assert.Equal(t, int64(1), count, "the durable write remains available for a safe retry")
	})

	t.Run("delete", func(t *testing.T) {
		_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "gpt-4o", ChannelId: 1, Enabled: true, Weight: 1,
		}).Error)
		var fail atomic.Bool
		fail.Store(true)
		callbackName := "test:fail_delete_ability_cache_refresh"
		require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.Ability{}).TableName() && fail.CompareAndSwap(true, false) {
				tx.AddError(errors.New("injected ability cache refresh failure"))
			}
		}))
		t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

		rec := do(http.MethodDelete, "/api/ability", `{"group":"default","model":"gpt-4o","channel_id":1}`)
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.Contains(t, decodeBody(t, rec)["message"], "路由缓存刷新失败")
		var count int64
		require.NoError(t, model.DB.Model(&model.Ability{}).Count(&count).Error)
		assert.Zero(t, count)
	})
}

func TestGetAbilitiesPropagatesDatabaseFailure(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	require.NoError(t, model.DB.Migrator().DropTable(&model.Ability{}))
	rec := do(http.MethodGet, "/api/ability", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, "查询能力失败", decodeBody(t, rec)["message"])
}
