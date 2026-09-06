package service

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestBumpAuthVersionKeepSessionFailsClosedAtInt64Boundary(t *testing.T) {
	initAuthDB(t)
	user := model.User{
		Username: "auth-version-boundary", Password: "x", Status: model.UserStatusEnabled,
		AuthVersion: math.MaxInt64,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	session := model.UserSession{
		SID: "auth-version-boundary-session", UserID: user.Id, Version: 1,
		UserAuthVersion: user.AuthVersion, Status: SessionStatusActive,
		RefreshHash: common.SHA256Hex("auth-version-boundary-refresh"),
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, model.DB.Create(&session).Error)

	err := BumpAuthVersionKeepSession(user.Id, session.SID)
	require.ErrorIs(t, err, ErrAuthVersionOverflow)

	var gotUser model.User
	var gotSession model.UserSession
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	require.NoError(t, model.DB.First(&gotSession, "sid = ?", session.SID).Error)
	assert.Equal(t, int64(math.MaxInt64), gotUser.AuthVersion)
	assert.Equal(t, int64(math.MaxInt64), gotSession.UserAuthVersion)
}

func TestPasswordResetAuthVersionOverflowRollsBackFlowAndPassword(t *testing.T) {
	initAuthDB(t)
	oldHash, err := common.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username: "reset-version-boundary", Password: oldHash,
		Email: "reset-version-boundary@example.com", EmailVerified: true, Status: model.UserStatusEnabled,
		AuthVersion: math.MaxInt64,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	const code = "729410"
	token, err := CreateAuthFlow(PasswordResetPurpose, "email", "", user.Id, "", code, time.Minute)
	require.NoError(t, err)

	err = ResetPassword(user.Email, code, "new-password")
	require.ErrorIs(t, err, ErrAuthVersionOverflow)

	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, int64(math.MaxInt64), gotUser.AuthVersion)
	assert.True(t, common.PasswordVerify("old-password", gotUser.Password))
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", common.SHA256Hex(token)).First(&flow).Error)
	assert.Nil(t, flow.ConsumedAt, "failed security mutation must leave the one-time flow retryable")
}

func TestRefreshSessionRevokesExhaustedSessionVersion(t *testing.T) {
	initAuthDB(t)
	user := seedPasswordUser(t, "refresh-version-boundary", "password123")
	refresh := "refresh-version-boundary-token"
	session := model.UserSession{
		SID: "refresh-version-boundary-session", UserID: user.Id, Version: math.MaxInt64,
		UserAuthVersion: user.AuthVersion, Status: SessionStatusActive,
		RefreshHash: common.SHA256Hex(refresh), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, model.DB.Create(&session).Error)

	_, _, _, err := RefreshSession(session.SID, refresh)
	require.ErrorIs(t, err, ErrSessionVersionOverflow)

	var got model.UserSession
	require.NoError(t, model.DB.First(&got, "sid = ?", session.SID).Error)
	assert.Equal(t, SessionStatusRevoked, got.Status)
	assert.Equal(t, "session_version_exhausted", got.RevokedReason)
	assert.Equal(t, int64(math.MaxInt64), got.Version)
}
