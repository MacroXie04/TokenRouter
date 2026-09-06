package auth

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"math"
	"testing"
	"time"
)

func TestRefreshSessionResultRetainsAbsoluteDatabaseExpiry(t *testing.T) {
	user, sid, _, refresh, _ := sessionFixture(t, "refresh-result-expiry")
	databaseNow, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	wantExpiry := databaseNow + int64((2*time.Hour)/time.Second)
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("expires_at", wantExpiry).Error)

	result, err := RefreshSessionWithResult(sid, refresh)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, user.Id, result.User.Id)
	assert.NotEmpty(t, result.AccessToken)
	assert.NotEmpty(t, result.RefreshToken)
	assert.NotEqual(t, refresh, result.RefreshToken)
	assert.Equal(t, wantExpiry, result.ExpiresAt)
	assert.GreaterOrEqual(t, result.ValidatedAt, databaseNow)
	validatedAfter, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	assert.LessOrEqual(t, result.ValidatedAt, validatedAfter)
	assert.LessOrEqual(t, result.ExpiresAt-result.ValidatedAt, int64(RefreshTokenTTL/time.Second))
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded), "refresh credentials and clock metadata must remain non-JSON")

	var stored model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&stored).Error)
	assert.Equal(t, wantExpiry, stored.ExpiresAt, "rotation must never extend the absolute session expiry")
}

func TestRefreshSessionRejectsInvalidAbsoluteExpiryBeforeRotation(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "refresh-invalid-expiry")
	var before model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&before).Error)
	databaseNow, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	invalidExpiry := databaseNow + int64(RefreshTokenTTL/time.Second) + 60
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("expires_at", invalidExpiry).Error)

	result, err := RefreshSessionWithResult(sid, refresh)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrSessionExpiryInvalid)

	var after model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&after).Error)
	assert.Equal(t, before.Version, after.Version)
	assert.Equal(t, before.RefreshHash, after.RefreshHash)
	assert.Empty(t, after.PreviousRefreshHash)
	assert.Equal(t, SessionStatusActive, after.Status)
}

func TestRefreshSessionRejectsOverflowSizedAbsoluteExpiryBeforeRotation(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "refresh-overflow-expiry")
	var before model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&before).Error)
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("expires_at", int64(math.MaxInt64)).Error)

	result, err := RefreshSessionWithResult(sid, refresh)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrSessionExpiryInvalid)

	var after model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&after).Error)
	assert.Equal(t, before.Version, after.Version)
	assert.Equal(t, before.RefreshHash, after.RefreshHash)
	assert.Empty(t, after.PreviousRefreshHash)
}

func TestRefreshSessionDistinguishesUnknownCredentialFromRevokedSession(t *testing.T) {
	user, sid, _, refresh, _ := sessionFixture(t, "refresh-invalid-credential")
	_, err := RefreshSessionWithResult(sid, refresh+"x")
	assert.ErrorIs(t, err, ErrRefreshTokenInvalid)

	revoked, err := RevokeUserSession(user.Id, sid, "test_revoked")
	require.NoError(t, err)
	require.True(t, revoked)
	_, err = RefreshSessionWithResult(sid, refresh)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}
