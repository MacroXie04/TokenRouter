package auth

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func initAuthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}))
	model.DB = db
	model.LOG_DB = db
}

func seedPasswordUser(t *testing.T, username, password string) *model.User {
	t.Helper()
	hash, err := cryptoutil.PasswordHash(password)
	require.NoError(t, err)
	u := &model.User{
		Username: username, Password: hash, Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, model.DB.Create(u).Error)
	return u
}

func TestLoginAndRefreshRotation(t *testing.T) {
	initAuthDB(t)
	u := seedPasswordUser(t, "alice", "password123")

	user, sid, access, refresh, err := Login("alice", "password123", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	assert.Equal(t, u.Id, user.Id)
	assert.NotEmpty(t, sid)
	assert.NotEmpty(t, access)
	assert.NotEmpty(t, refresh)

	// Rotation: refresh yields new access + refresh tokens.
	access2, refresh2, _, err := RefreshSession(sid, refresh)
	require.NoError(t, err)
	assert.NotEmpty(t, access2)
	assert.NotEmpty(t, refresh2)
	assert.NotEqual(t, refresh, refresh2)

	// Replay outside the bounded response-recovery window must be rejected.
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("previous_valid_until", wallclock.NowTimestamp()-1).Error)
	_, _, _, err = RefreshSession(sid, refresh)
	assert.ErrorIs(t, err, ErrRefreshReplay)
}

func TestRevokeSession(t *testing.T) {
	initAuthDB(t)
	u := seedPasswordUser(t, "bob", "password123")

	_, sid, _, refresh, err := Login("bob", "password123", "127.0.0.1", "test")
	require.NoError(t, err)

	revoked, err := RevokeUserSession(u.Id, sid, "test")
	require.NoError(t, err)
	require.True(t, revoked)
	_, _, _, err = RefreshSession(sid, refresh)
	assert.Error(t, err, "refresh after revocation must fail")
}

func TestLoginWrongPassword(t *testing.T) {
	initAuthDB(t)
	seedPasswordUser(t, "carol", "password123")
	_, _, _, _, err := Login("carol", "wrong", "127.0.0.1", "test")
	assert.Equal(t, ErrInvalidCredentials, err)
}

func TestPasswordLoginAcceptsUniqueUsernameOrEmailAndRejectsAmbiguity(t *testing.T) {
	initAuthDB(t)
	verified := seedPasswordUser(t, "email-owner", "password123")
	normalizedEmail, emailKey, err := model.NormalizeVerifiedEmail("Owner@Example.Test")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(verified).Updates(map[string]any{
		"email": normalizedEmail, "email_verified": true, "verified_email_key": &emailKey,
	}).Error)

	loggedIn, _, _, _, err := Login("  OWNER@example.test  ", "password123", "127.0.0.1", "test")
	require.NoError(t, err)
	assert.Equal(t, verified.Id, loggedIn.Id)

	legacy := seedPasswordUser(t, "legacy-email", "legacy-password")
	require.NoError(t, model.DB.Model(legacy).Updates(map[string]any{
		"email": "legacy@example.test", "email_verified": false, "verified_email_key": nil,
	}).Error)
	loggedIn, err = AuthenticatePassword("legacy@example.test", "legacy-password")
	require.NoError(t, err)
	assert.Equal(t, legacy.Id, loggedIn.Id)

	collision := seedPasswordUser(t, "email-collision", "other-password")
	require.NoError(t, model.DB.Model(collision).Updates(map[string]any{
		"email": "legacy@example.test", "email_verified": false, "verified_email_key": nil,
	}).Error)
	_, err = AuthenticatePassword("legacy@example.test", "legacy-password")
	assert.ErrorIs(t, err, ErrInvalidCredentials)

	usernameCollision := seedPasswordUser(t, "owner@example.test", "username-password")
	loggedIn, err = AuthenticatePassword("owner@example.test", "password123")
	require.NoError(t, err)
	assert.Equal(t, verified.Id, loggedIn.Id,
		"a username collision must not deny the canonical verified-email owner")
	_, err = AuthenticatePassword("owner@example.test", "username-password")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.NotEqual(t, verified.Id, usernameCollision.Id)

	unverifiedCollision := seedPasswordUser(t, "unverified-collision", "other-password")
	require.NoError(t, model.DB.Model(unverifiedCollision).Updates(map[string]any{
		"email": normalizedEmail, "email_verified": false, "verified_email_key": nil,
	}).Error)
	loggedIn, err = AuthenticatePassword("owner@example.test", "password123")
	require.NoError(t, err)
	assert.Equal(t, verified.Id, loggedIn.Id,
		"an unverified duplicate email must not deny the canonical owner")
}

func TestPasswordLoginAcceptsEveryValidRegistrationUsernameLength(t *testing.T) {
	initAuthDB(t)
	for index, username := range []string{strings.Repeat("a", 64), strings.Repeat("界", 21)} {
		user := seedPasswordUser(t, username, "password123")
		loggedIn, err := AuthenticatePassword(username, "password123")
		require.NoError(t, err, index)
		assert.Equal(t, user.Id, loggedIn.Id)
	}
	assert.ErrorIs(t, ValidateRegistrationUsername(strings.Repeat("界", 22)), ErrRegistrationUsernameInvalid)
}

func TestPasswordLoginIdentifierAndDisabledStatusFailClosed(t *testing.T) {
	initAuthDB(t)
	disabled := seedPasswordUser(t, "disabled-login", "password123")
	normalizedEmail, emailKey, err := model.NormalizeVerifiedEmail("disabled@example.test")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(disabled).Updates(map[string]any{
		"email": normalizedEmail, "email_verified": true, "verified_email_key": &emailKey,
		"status": model.UserStatusDisabled,
	}).Error)

	_, err = AuthenticatePassword("disabled@example.test", "wrong-password")
	assert.ErrorIs(t, err, ErrInvalidCredentials, "disabled state must not be disclosed before password proof")
	_, err = AuthenticatePassword("disabled@example.test", "password123")
	assert.EqualError(t, err, "用户已禁用")

	for _, identifier := range []string{"", "login\nname", "login\u202ename", strings.Repeat("x", 51)} {
		_, err = AuthenticatePassword(identifier, "password123")
		assert.ErrorIs(t, err, ErrInvalidCredentials, identifier)
	}
	_, err = AuthenticatePassword("disabled-login", strings.Repeat("x", 129))
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestPasswordLoginInvalidInputsAndStoredHashesUseBcryptSentinel(t *testing.T) {
	validHash, err := cryptoutil.PasswordHash("password123")
	require.NoError(t, err)

	for _, password := range []string{"", strings.Repeat("x", maxBcryptPasswordBytes+1)} {
		candidate, valid := loginPasswordCandidate(password)
		assert.False(t, valid)
		assert.Equal(t, "tokenrouter-invalid-login-sentinel", candidate)
	}
	candidate, valid := loginPasswordCandidate("password123")
	assert.True(t, valid)
	assert.Equal(t, "password123", candidate)

	for _, malformed := range []string{"", "not-a-bcrypt-hash", strings.Repeat("x", 60)} {
		hash, valid := loginPasswordHash(malformed)
		assert.False(t, valid)
		assert.Equal(t, invalidLoginPasswordHash, hash)
	}
	hash, valid := loginPasswordHash(validHash)
	assert.True(t, valid)
	assert.Equal(t, validHash, hash)
	cost4Hash := loginPasswordSentinelAtCost(bcrypt.MinCost)
	hash, valid = loginPasswordHash(cost4Hash)
	assert.True(t, valid, "bounded imported bcrypt costs must remain login-compatible")
	assert.Equal(t, cost4Hash, hash)
	highCostHash := loginPasswordSentinelAtCost(cryptoutil.PasswordBcryptTargetCost + 1)
	require.NotEqual(t, validHash, highCostHash)
	hash, valid = loginPasswordHash(highCostHash)
	assert.False(t, valid, "unbounded attacker-controlled bcrypt work must be rejected before comparison")
	assert.Equal(t, invalidLoginPasswordHash, hash)
	invalidEncoding := validHash[:7] + strings.Repeat("!", 53)
	_, err = bcrypt.Cost([]byte(invalidEncoding))
	require.NoError(t, err, "the regression fixture must pass bcrypt's header-only cost parser")
	hash, valid = loginPasswordHash(invalidEncoding)
	assert.False(t, valid, "invalid bcrypt base64 must take the full sentinel work path")
	assert.Equal(t, invalidLoginPasswordHash, hash)

	initAuthDB(t)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "oauth-only-login", Password: "", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}).Error)
	_, err = AuthenticatePassword("oauth-only-login", "password123")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestPasswordLoginVerificationPlanEqualizesEveryAcceptedCost(t *testing.T) {
	for storedCost := bcrypt.MinCost; storedCost <= cryptoutil.PasswordBcryptTargetCost; storedCost++ {
		storedHash := loginPasswordSentinelAtCost(storedCost)
		plan, parsedCost, valid := loginPasswordVerificationPlan(storedHash)
		require.True(t, valid, storedCost)
		assert.Equal(t, storedCost, parsedCost)

		totalWork := 0
		primaryChecks := 0
		for _, verification := range plan {
			cost, err := bcrypt.Cost([]byte(verification.hash))
			require.NoError(t, err)
			totalWork += 1 << cost
			if verification.primary {
				primaryChecks++
				assert.Equal(t, storedHash, verification.hash)
			}
		}
		assert.Equal(t, 1, primaryChecks)
		assert.Equal(t, 1<<cryptoutil.PasswordBcryptTargetCost, totalWork,
			"accepted cost %d must receive exactly the target work class", storedCost)
	}

	for _, invalidHash := range []string{"", "not-a-bcrypt-hash", loginPasswordSentinelAtCost(cryptoutil.PasswordBcryptTargetCost + 1)} {
		plan, parsedCost, valid := loginPasswordVerificationPlan(invalidHash)
		assert.False(t, valid)
		assert.Equal(t, cryptoutil.PasswordBcryptTargetCost, parsedCost)
		require.Len(t, plan, 1)
		assert.False(t, plan[0].primary)
		cost, err := bcrypt.Cost([]byte(plan[0].hash))
		require.NoError(t, err)
		assert.Equal(t, cryptoutil.PasswordBcryptTargetCost, cost)
	}
}

func TestPasswordLoginUpgradesLowCostHashWithCompareAndSwap(t *testing.T) {
	initAuthDB(t)
	legacyHash, err := bcrypt.GenerateFromPassword([]byte("legacy-password"), bcrypt.MinCost)
	require.NoError(t, err)
	user := &model.User{
		Username: "legacy-low-cost", Password: string(legacyHash), Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, model.DB.Create(user).Error)

	loggedIn, err := AuthenticatePassword(user.Username, "legacy-password")
	require.NoError(t, err)
	assert.Equal(t, user.Id, loggedIn.Id)
	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	upgradedCost, err := bcrypt.Cost([]byte(stored.Password))
	require.NoError(t, err)
	assert.Equal(t, cryptoutil.PasswordBcryptTargetCost, upgradedCost)
	assert.True(t, cryptoutil.PasswordVerify("legacy-password", stored.Password))

	replacement, err := cryptoutil.PasswordHash("new-password")
	require.NoError(t, err)
	stale := stored
	stale.Password = string(legacyHash)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).UpdateColumn("password", replacement).Error)
	err = upgradeLoginPasswordHash(&stale, "legacy-password", bcrypt.MinCost)
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Equal(t, replacement, stored.Password, "a concurrent password reset must never be overwritten")
}

func TestPasswordLoginRejectsImmediatelyWhenVerificationCapacityIsFull(t *testing.T) {
	for index := 0; index < cap(passwordVerificationSlots); index++ {
		require.True(t, acquirePasswordVerificationSlot())
	}
	defer func() {
		for index := 0; index < cap(passwordVerificationSlots); index++ {
			releasePasswordVerificationSlot()
		}
	}()

	_, err := AuthenticatePassword("capacity-test", "password123")
	assert.ErrorIs(t, err, ErrPasswordVerificationBusy)
}
