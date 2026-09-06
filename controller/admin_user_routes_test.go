package controller_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestAdminClearUserBindingRoute(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.ExternalIdentityClaim{}))

	target := model.User{
		Username: "bound-target", Password: "password8", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1,
		GitHubId: "github-subject", Email: "bound@example.com", EmailVerified: true,
	}
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&target).Error; err != nil {
			return err
		}
		return model.ClaimUserExternalIdentitiesWithTx(tx, &target)
	}))

	recorder := do(http.MethodDelete, "/api/user/"+common.Int2Str(target.Id)+"/bindings/github", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var refreshed model.User
	require.NoError(t, model.DB.First(&refreshed, target.Id).Error)
	assert.Empty(t, refreshed.GitHubId)
	var claimCount int64
	require.NoError(t, model.DB.Model(&model.ExternalIdentityClaim{}).
		Where("provider = ? AND user_id = ?", model.ExternalIdentityProviderGitHub, target.Id).
		Count(&claimCount).Error)
	assert.Zero(t, claimCount)

	recorder = do(http.MethodDelete, "/api/user/"+common.Int2Str(target.Id)+"/bindings/email", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, model.DB.First(&refreshed, target.Id).Error)
	assert.Empty(t, refreshed.Email)
	assert.False(t, refreshed.EmailVerified)
	assert.Nil(t, refreshed.VerifiedEmailKey)

	recorder = do(http.MethodDelete, "/api/user/"+common.Int2Str(target.Id)+"/bindings/unknown", "")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestAdminDeleteUserRouteAndRoleGuard(t *testing.T) {
	_, do, rootID, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	target := createManagedUser(t, "delete-route-target", constant.RoleCommonUser, model.UserStatusEnabled, "default")
	require.NoError(t, model.DB.Create(&model.Token{UserId: target.Id, Key: "sk-delete-route", Name: "owned"}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "delete-route-session", UserID: target.Id, Version: 1, UserAuthVersion: 1,
		Status: "active", RefreshHash: "delete-route-refresh", ExpiresAt: common.NowTimestamp() + 3600,
	}).Error)

	self := do(http.MethodDelete, "/api/user/"+common.Int2Str(rootID), "")
	assert.Equal(t, http.StatusForbidden, self.Code)

	recorder := do(http.MethodDelete, "/api/user/"+common.Int2Str(target.Id), "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var activeUsers int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Count(&activeUsers).Error)
	assert.Zero(t, activeUsers)
	var retainedUsers int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Where("id = ?", target.Id).Count(&retainedUsers).Error)
	assert.EqualValues(t, 1, retainedUsers)
	var activeTokens int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", target.Id).Count(&activeTokens).Error)
	assert.Zero(t, activeTokens)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "delete-route-session").First(&session).Error)
	assert.NotZero(t, session.RevokedAt)
}
