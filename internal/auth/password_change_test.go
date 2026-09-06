package auth

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
)

func TestChangePasswordKeepSessionIsAtomicAndRevokesOtherSessions(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.UserSession{})
	oldHash, err := cryptoutil.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username: "password-change", Password: oldHash, DisplayName: "Before",
		Role: 1, Status: model.UserStatusEnabled, AuthVersion: 7,
	}
	require.NoError(t, db.Create(&user).Error)
	now := wallclock.NowTimestamp()
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

	claims, err := cryptoutil.ParseJWT(access, cryptoutil.SessionSecret())
	require.NoError(t, err)
	assert.EqualValues(t, 8, claims.UserAuthVersion)
	assert.EqualValues(t, 3, claims.SessionVersion)
	assert.Equal(t, "current-session", claims.SessionID)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.True(t, cryptoutil.PasswordVerify("new-password", updated.Password))
	assert.False(t, cryptoutil.PasswordVerify("old-password", updated.Password))
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
	oldHash, err := cryptoutil.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username: "password-proof", Password: oldHash,
		Role: 1, Status: model.UserStatusEnabled, AuthVersion: 2,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "current-session", UserID: user.Id, Version: 1, UserAuthVersion: 2,
		Status: SessionStatusActive, RefreshHash: "refresh", ExpiresAt: wallclock.NowTimestamp() + 3600,
	}).Error)

	_, err = ChangePasswordKeepSession(user.Id, "current-session", "wrong-password", "new-password", "Changed")
	require.ErrorIs(t, err, ErrInvalidCredentials)

	var unchanged model.User
	require.NoError(t, db.First(&unchanged, user.Id).Error)
	assert.True(t, cryptoutil.PasswordVerify("old-password", unchanged.Password))
	assert.EqualValues(t, 2, unchanged.AuthVersion)
	assert.Empty(t, unchanged.DisplayName)
}
