package auth

import (
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompleteLoginDoesNotDependOnPostCreateDatabaseReads(t *testing.T) {
	initAuthDB(t)
	user := seedPasswordUser(t, "post-commit-read-outage", "password123")

	injected := errors.New("injected post-create database read failure")
	var sessionCreated atomic.Bool
	const createCallback = "test:mark-user-session-created"
	const queryCallback = "test:fail-post-create-query"
	const rawCallback = "test:fail-post-create-raw"
	require.NoError(t, model.DB.Callback().Create().After("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			sessionCreated.Store(true)
		}
	}))
	failPostCreateRead := func(tx *gorm.DB) {
		if sessionCreated.Load() {
			tx.AddError(injected)
		}
	}
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(queryCallback, failPostCreateRead))
	require.NoError(t, model.DB.Callback().Raw().Before("gorm:raw").Register(rawCallback, failPostCreateRead))
	removeCallbacks := func() {
		_ = model.DB.Callback().Create().Remove(createCallback)
		_ = model.DB.Callback().Query().Remove(queryCallback)
		_ = model.DB.Callback().Raw().Remove(rawCallback)
	}
	t.Cleanup(removeCallbacks)

	sid, access, refresh, expiresAt, err := CompleteLoginWithExpiry(
		user, "127.0.0.1", "test-agent", "password",
	)
	require.NoError(t, err)
	assert.NotEmpty(t, sid)
	assert.NotEmpty(t, access)
	assert.NotEmpty(t, refresh)
	removeCallbacks()

	claims, err := cryptoutil.ParseJWTSigned(access, cryptoutil.SessionSecret())
	require.NoError(t, err)
	require.NotNil(t, claims.ExpiresAt)
	assert.Equal(t, expiresAt, claims.ExpiresAt.Time.Unix())
	var sessions []model.UserSession
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).Find(&sessions).Error)
	require.Len(t, sessions, 1, "a successful login must create exactly one reachable session")
	assert.Equal(t, sid, sessions[0].SID)
}

func TestRefreshRotationDoesNotDependOnPostCASDatabaseReads(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "post-rotation-read-outage")

	injected := errors.New("injected post-rotation database read failure")
	var sessionRotated atomic.Bool
	const updateCallback = "test:mark-user-session-rotated"
	const queryCallback = "test:fail-post-rotation-query"
	require.NoError(t, model.DB.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			sessionRotated.Store(true)
		}
	}))
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if sessionRotated.Load() {
			tx.AddError(injected)
		}
	}))
	removeCallbacks := func() {
		_ = model.DB.Callback().Update().Remove(updateCallback)
		_ = model.DB.Callback().Query().Remove(queryCallback)
	}
	t.Cleanup(removeCallbacks)

	result, err := RefreshSessionWithResult(sid, refresh)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.AccessToken)
	assert.NotEmpty(t, result.RefreshToken)
	removeCallbacks()

	claims, err := cryptoutil.ParseJWTSigned(result.AccessToken, cryptoutil.SessionSecret())
	require.NoError(t, err)
	assert.Equal(t, sid, claims.SessionID)
	assert.EqualValues(t, 2, claims.SessionVersion)
	var stored model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&stored).Error)
	assert.EqualValues(t, 2, stored.Version)
	assert.Equal(t, refreshTokenHash(result.RefreshToken), stored.RefreshHash)
}

func sessionFixture(t *testing.T, username string) (*model.User, string, string, string, *cryptoutil.JWTClaims) {
	t.Helper()
	initAuthDB(t)
	user := seedPasswordUser(t, username, "password123")
	sid, access, refresh, err := CompleteLogin(user, "127.0.0.1", "test-agent", "test")
	require.NoError(t, err)
	claims, err := cryptoutil.ParseJWT(access, cryptoutil.SessionSecret())
	require.NoError(t, err)
	return user, sid, access, refresh, claims
}

func TestSessionAccessTokenCarriesAndValidatesLiveSessionIdentity(t *testing.T) {
	user, sid, _, _, claims := sessionFixture(t, "claims-user")

	assert.Equal(t, user.Id, claims.UserID)
	assert.Equal(t, sid, claims.SessionID)
	assert.EqualValues(t, user.AuthVersion, claims.UserAuthVersion)
	assert.EqualValues(t, 1, claims.SessionVersion)
	assert.True(t, cryptoutil.IsDashboardAccessToken(claims))

	session, currentUser, err := ValidateAccessTokenClaims(claims)
	require.NoError(t, err)
	assert.Equal(t, sid, session.SID)
	assert.Equal(t, user.Id, currentUser.Id)
}

func TestValidateSessionBindingRejectsRevokedOrForeignCeremonies(t *testing.T) {
	user, sid, _, _, _ := sessionFixture(t, "ceremony-user")
	require.NoError(t, ValidateSessionBinding(user.Id, sid))
	assert.ErrorIs(t, ValidateSessionBinding(user.Id+1, sid), ErrSessionRevoked)

	revoked, err := RevokeUserSession(user.Id, sid, "ceremony_cancelled")
	require.NoError(t, err)
	require.True(t, revoked)
	assert.ErrorIs(t, ValidateSessionBinding(user.Id, sid), ErrSessionRevoked)
}

func TestSessionAccessTokenRejectsIdentityAndVersionMismatches(t *testing.T) {
	user, _, _, _, claims := sessionFixture(t, "mismatch-user")
	other := seedPasswordUser(t, "mismatch-other", "password123")
	otherSID, _, err := CreateSession(other, "127.0.0.2", "other-agent", "test")
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*cryptoutil.JWTClaims)
	}{
		{name: "wrong user", mutate: func(c *cryptoutil.JWTClaims) { c.UserID = other.Id }},
		{name: "wrong sid", mutate: func(c *cryptoutil.JWTClaims) { c.SessionID = otherSID }},
		{name: "missing sid", mutate: func(c *cryptoutil.JWTClaims) { c.SessionID = "" }},
		{name: "wrong user auth version", mutate: func(c *cryptoutil.JWTClaims) { c.UserAuthVersion++ }},
		{name: "wrong session version", mutate: func(c *cryptoutil.JWTClaims) { c.SessionVersion++ }},
		{name: "wrong token use", mutate: func(c *cryptoutil.JWTClaims) { c.TokenUse = "security_proof" }},
		{name: "expired claims", mutate: func(c *cryptoutil.JWTClaims) {
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Second))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := *claims
			test.mutate(&mutated)
			_, _, err := ValidateAccessTokenClaims(&mutated)
			assert.ErrorIs(t, err, ErrSessionRevoked)
		})
	}

	// Keep the compiler and test intent honest: the original identity remains
	// valid after testing copies of its claims.
	_, currentUser, err := ValidateAccessTokenClaims(claims)
	require.NoError(t, err)
	assert.Equal(t, user.Id, currentUser.Id)
}

func TestSessionAccessTokenRejectsRevokedExpiredDisabledAndStaleState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *model.User, string)
	}{
		{name: "revoked session", mutate: func(t *testing.T, user *model.User, sid string) {
			revoked, err := RevokeUserSession(user.Id, sid, "test")
			require.NoError(t, err)
			require.True(t, revoked)
		}},
		{name: "expired session", mutate: func(t *testing.T, _ *model.User, sid string) {
			require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
				UpdateColumn("expires_at", wallclock.NowTimestamp()-1).Error)
		}},
		{name: "disabled user", mutate: func(t *testing.T, user *model.User, _ string) {
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
				UpdateColumn("status", model.UserStatusDisabled).Error)
		}},
		{name: "user auth version advanced", mutate: func(t *testing.T, user *model.User, _ string) {
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
				UpdateColumn("auth_version", user.AuthVersion+1).Error)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			user, sid, _, _, claims := sessionFixture(t, "state-"+test.name)
			test.mutate(t, user, sid)
			_, _, err := ValidateAccessTokenClaims(claims)
			assert.ErrorIs(t, err, ErrSessionRevoked)
		})
	}
}

func TestExpiredSignedSessionAccessTokenIsRejected(t *testing.T) {
	user, sid, _, _, claims := sessionFixture(t, "expired-jwt")
	raw, err := cryptoutil.GenerateSessionJWT(user.Id, user.Role, sid, claims.UserAuthVersion, claims.SessionVersion,
		cryptoutil.SessionSecret(), -time.Second)
	require.NoError(t, err)
	_, err = cryptoutil.ParseJWT(raw, cryptoutil.SessionSecret())
	assert.Error(t, err)
}

func TestRefreshRotationImmediatelyInvalidatesPreviousAccessToken(t *testing.T) {
	_, sid, _, refresh, oldClaims := sessionFixture(t, "rotation-user")

	newAccess, newRefresh, _, err := RefreshSession(sid, refresh)
	require.NoError(t, err)
	assert.NotEqual(t, refresh, newRefresh)

	_, _, err = ValidateAccessTokenClaims(oldClaims)
	assert.ErrorIs(t, err, ErrSessionRevoked)
	newClaims, err := cryptoutil.ParseJWT(newAccess, cryptoutil.SessionSecret())
	require.NoError(t, err)
	assert.Greater(t, newClaims.SessionVersion, oldClaims.SessionVersion)
	_, _, err = ValidateAccessTokenClaims(newClaims)
	require.NoError(t, err)
}

func TestRefreshReplayPolicy(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "replay-user")
	firstAccess, firstRotatedRefresh, _, err := RefreshSession(sid, refresh)
	require.NoError(t, err)

	// A duplicate request inside the grace window deterministically recovers
	// the winning rotation, so a lost response cannot strand the browser.
	recoveredAccess, recoveredRefresh, _, err := RefreshSession(sid, refresh)
	require.NoError(t, err)
	assert.NotEmpty(t, firstAccess)
	assert.NotEmpty(t, recoveredAccess)
	assert.Equal(t, firstRotatedRefresh, recoveredRefresh)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, SessionStatusActive, session.Status)

	// Reuse after the grace window revokes the whole token family.
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("previous_valid_until", wallclock.NowTimestamp()-1).Error)
	_, _, _, err = RefreshSession(sid, refresh)
	assert.ErrorIs(t, err, ErrRefreshReplay)
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, SessionStatusRevoked, session.Status)
	assert.Equal(t, "refresh_replay", session.RevokedReason)
}

func TestRefreshRecoveryDoesNotDependOnFreshEntropy(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "refresh-no-fresh-entropy")
	restoreEntropy := cryptoutil.SetSecureRandomReaderForTesting(testutil.EntropyFailureReader{})
	t.Cleanup(restoreEntropy)

	access, rotatedRefresh, _, err := RefreshSession(sid, refresh)
	require.NoError(t, err)
	assert.NotEmpty(t, access)
	assert.Equal(t, deriveNextRefreshToken(sid, refresh), rotatedRefresh)
}

func TestLegacyRefreshDigestRotatesForwardToKeyedDigest(t *testing.T) {
	user, sid, _, refresh, _ := sessionFixture(t, "legacy-refresh-digest")
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("refresh_hash", cryptoutil.SHA256Hex(refresh)).Error)

	_, rotatedRefresh, rotatedUser, err := RefreshSession(sid, refresh)
	require.NoError(t, err)
	assert.Equal(t, user.Id, rotatedUser.Id)

	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, refreshTokenHash(rotatedRefresh), session.RefreshHash)
	assert.NotEqual(t, cryptoutil.SHA256Hex(rotatedRefresh), session.RefreshHash)
	assert.Equal(t, cryptoutil.SHA256Hex(refresh), session.PreviousRefreshHash)
	assert.InDelta(t, wallclock.NowTimestamp()+int64(RefreshReplayWindow/time.Second), session.PreviousValidUntil, 1)
}

func TestLegacyRefreshDigestStillAuthenticatesLogout(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "legacy-refresh-logout")
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("refresh_hash", cryptoutil.SHA256Hex(refresh)).Error)

	revoked, err := RevokeSessionByRefreshToken(sid, refresh)
	require.NoError(t, err)
	assert.True(t, revoked)

	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, SessionStatusRevoked, session.Status)
}

func TestSessionRevocationIsOwnerScopedAndRetainsTombstones(t *testing.T) {
	actor, currentSID, _, _, _ := sessionFixture(t, "owner")
	otherActorSID, _, err := CreateSession(actor, "127.0.0.2", "second", "test")
	require.NoError(t, err)
	foreign := seedPasswordUser(t, "foreign", "password123")
	foreignSID, _, err := CreateSession(foreign, "127.0.0.3", "foreign", "test")
	require.NoError(t, err)

	revoked, err := RevokeUserSession(actor.Id, foreignSID, "user_revoked")
	require.NoError(t, err)
	assert.False(t, revoked)

	revoked, err = RevokeUserSession(actor.Id, otherActorSID, "user_revoked")
	require.NoError(t, err)
	require.True(t, revoked)
	var tombstone model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", otherActorSID).First(&tombstone).Error)
	assert.Equal(t, SessionStatusRevoked, tombstone.Status)
	assert.NotZero(t, tombstone.RevokedAt)

	var foreignSession model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", foreignSID).First(&foreignSession).Error)
	assert.Equal(t, SessionStatusActive, foreignSession.Status)

	active, err := GetUserSessions(actor.Id)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, currentSID, active[0].SID)
}

func TestRevokeOtherSessionsKeepsOnlyCurrentSessionActive(t *testing.T) {
	actor, currentSID, _, _, _ := sessionFixture(t, "revoke-others")
	otherSID, _, err := CreateSession(actor, "127.0.0.2", "second", "test")
	require.NoError(t, err)
	foreign := seedPasswordUser(t, "revoke-others-foreign", "password123")
	foreignSID, _, err := CreateSession(foreign, "127.0.0.3", "foreign", "test")
	require.NoError(t, err)

	// A caller cannot nominate a foreign SID as the session to preserve.
	assert.ErrorIs(t, RevokeOtherSessions(actor.Id, foreignSID), ErrSessionRevoked)
	var stillActive model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", otherSID).First(&stillActive).Error)
	assert.Equal(t, SessionStatusActive, stillActive.Status)

	require.NoError(t, RevokeOtherSessions(actor.Id, currentSID))
	for sid, wantStatus := range map[string]string{
		currentSID: SessionStatusActive,
		otherSID:   SessionStatusRevoked,
		foreignSID: SessionStatusActive,
	} {
		var session model.UserSession
		require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
		assert.Equal(t, wantStatus, session.Status, sid)
	}
}

func TestBumpAuthVersionRevokesOtherSessionsAndReleasesActiveQuota(t *testing.T) {
	setTestSessionPolicy(t, 2, 10, 86400, 7, 5000)
	actor, currentSID, _, _, _ := sessionFixture(t, "auth-version-session-drain")
	otherSID, _, err := CreateSession(actor, "127.0.0.2", "second", "test")
	require.NoError(t, err)

	require.NoError(t, BumpAuthVersionKeepSession(actor.Id, currentSID))
	var current, other, currentUser model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", currentSID).First(&current).Error)
	require.NoError(t, model.DB.Where("sid = ?", otherSID).First(&other).Error)
	assert.Equal(t, SessionStatusActive, current.Status)
	assert.Equal(t, actor.AuthVersion+1, current.UserAuthVersion)
	assert.Equal(t, SessionStatusRevoked, other.Status)
	assert.Equal(t, "auth_version_changed", other.RevokedReason)
	assert.NotZero(t, other.RevokedAt)

	var refreshedUser model.User
	require.NoError(t, model.DB.First(&refreshedUser, actor.Id).Error)
	newSID, _, err := CreateSession(&refreshedUser, "127.0.0.3", "replacement", "test")
	require.NoError(t, err, "revoked stale-version rows must not consume active-session quota")
	require.NoError(t, model.DB.Where("sid = ?", newSID).First(&currentUser).Error)
	assert.Equal(t, SessionStatusActive, currentUser.Status)
}

func TestRevokeAllSessionsIsUserScopedAndRetainsTombstones(t *testing.T) {
	actor, firstSID, _, _, _ := sessionFixture(t, "revoke-all")
	secondSID, _, err := CreateSession(actor, "127.0.0.2", "second", "test")
	require.NoError(t, err)
	foreign := seedPasswordUser(t, "revoke-all-foreign", "password123")
	foreignSID, _, err := CreateSession(foreign, "127.0.0.3", "foreign", "test")
	require.NoError(t, err)

	require.NoError(t, RevokeAllUserSessions(actor.Id))
	for _, sid := range []string{firstSID, secondSID} {
		var session model.UserSession
		require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
		assert.Equal(t, SessionStatusRevoked, session.Status)
		assert.Equal(t, "revoke_all", session.RevokedReason)
		assert.NotZero(t, session.RevokedAt)
	}
	var foreignSession model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", foreignSID).First(&foreignSession).Error)
	assert.Equal(t, SessionStatusActive, foreignSession.Status)
}

func TestLogoutRequiresRefreshTokenProof(t *testing.T) {
	_, sid, _, refresh, _ := sessionFixture(t, "logout-proof")
	revoked, err := RevokeSessionByRefreshToken(sid, "wrong-secret")
	require.NoError(t, err)
	assert.False(t, revoked)

	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, SessionStatusActive, session.Status)

	revoked, err = RevokeSessionByRefreshToken(sid, refresh)
	require.NoError(t, err)
	assert.True(t, revoked)
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, SessionStatusRevoked, session.Status)
}

func TestConcurrentRefreshAndRevokeCannotLeaveUsableAccessToken(t *testing.T) {
	user, sid, originalAccess, refresh, _ := sessionFixture(t, "refresh-revoke-race")
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	start := make(chan struct{})
	var wait sync.WaitGroup
	var refreshedAccess string
	var refreshErr error
	var revoked bool
	var revokeErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		refreshedAccess, _, _, refreshErr = RefreshSession(sid, refresh)
	}()
	go func() {
		defer wait.Done()
		<-start
		revoked, revokeErr = RevokeUserSession(user.Id, sid, "concurrent_test")
	}()
	close(start)
	wait.Wait()

	require.NoError(t, revokeErr)
	assert.True(t, revoked)
	if refreshErr != nil {
		assert.True(t, errors.Is(refreshErr, ErrSessionRevoked) || errors.Is(refreshErr, ErrRefreshRace), refreshErr)
	}
	for _, raw := range []string{originalAccess, refreshedAccess} {
		if raw == "" {
			continue
		}
		claims, err := cryptoutil.ParseJWT(raw, cryptoutil.SessionSecret())
		require.NoError(t, err)
		_, _, err = ValidateAccessTokenClaims(claims)
		assert.ErrorIs(t, err, ErrSessionRevoked)
	}
}

func TestSessionDatabaseErrorsArePropagated(t *testing.T) {
	user, sid, _, refresh, _ := sessionFixture(t, "db-errors")
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = GetUserSessions(user.Id)
	assert.Error(t, err)
	_, err = RevokeUserSession(user.Id, sid, "test")
	assert.Error(t, err)
	_, err = RevokeSessionByRefreshToken(sid, refresh)
	assert.Error(t, err)
	assert.Error(t, RevokeAllUserSessions(user.Id))
	assert.Error(t, DeleteUser(user.Id))
}
