package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestChangePasswordKeepSessionIsAtomicAndRevokesOtherSessions(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.UserSession{})
	oldHash, err := common.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username: "password-change", Password: oldHash, DisplayName: "Before",
		Role: 1, Status: model.UserStatusEnabled, AuthVersion: 7,
	}
	require.NoError(t, db.Create(&user).Error)
	now := common.NowTimestamp()
	for _, sid := range []string{"current-session", "other-session"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Version: 3, UserAuthVersion: user.AuthVersion,
			Status: SessionStatusActive, RefreshHash: "refresh-" + sid,
			ExpiresAt: now + 3600,
		}).Error)
	}

	access, err := ChangePasswordKeepSession(
		user.Id, "current-session", "old-password", "new-password", "After",
	)
	require.NoError(t, err)
	require.NotEmpty(t, access)

	claims, err := common.ParseJWT(access, common.SessionSecret())
	require.NoError(t, err)
	assert.EqualValues(t, 8, claims.UserAuthVersion)
	assert.EqualValues(t, 3, claims.SessionVersion)
	assert.Equal(t, "current-session", claims.SessionID)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.True(t, common.PasswordVerify("new-password", updated.Password))
	assert.False(t, common.PasswordVerify("old-password", updated.Password))
	assert.Equal(t, "After", updated.DisplayName)
	assert.EqualValues(t, 8, updated.AuthVersion)

	var current model.UserSession
	require.NoError(t, db.Where("sid = ?", "current-session").First(&current).Error)
	assert.Equal(t, SessionStatusActive, current.Status)
	assert.EqualValues(t, 8, current.UserAuthVersion)

	var other model.UserSession
	require.NoError(t, db.Where("sid = ?", "other-session").First(&other).Error)
	assert.Equal(t, SessionStatusRevoked, other.Status)
	assert.Equal(t, "password_changed", other.RevokedReason)
	assert.NotZero(t, other.RevokedAt)
}

func TestChangePasswordKeepSessionRejectsBadProofWithoutMutation(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.UserSession{})
	oldHash, err := common.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username: "password-proof", Password: oldHash,
		Role: 1, Status: model.UserStatusEnabled, AuthVersion: 2,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "current-session", UserID: user.Id, Version: 1, UserAuthVersion: 2,
		Status: SessionStatusActive, RefreshHash: "refresh", ExpiresAt: common.NowTimestamp() + 3600,
	}).Error)

	_, err = ChangePasswordKeepSession(user.Id, "current-session", "wrong-password", "new-password", "Changed")
	require.ErrorIs(t, err, ErrInvalidCredentials)

	var unchanged model.User
	require.NoError(t, db.First(&unchanged, user.Id).Error)
	assert.True(t, common.PasswordVerify("old-password", unchanged.Password))
	assert.EqualValues(t, 2, unchanged.AuthVersion)
	assert.Empty(t, unchanged.DisplayName)
}
