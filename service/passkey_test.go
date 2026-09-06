package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func initPasskeyDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
}

func TestWebAuthnInitAndBeginRegistration(t *testing.T) {
	initPasskeyDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PasskeyEnabledOption:              "true",
		setting.PasskeyRPDisplayNameOption:        "Example Passkeys",
		setting.PasskeyRPIDOption:                 "example.com",
		setting.PasskeyOriginsOption:              "https://example.com,https://login.example.com",
		setting.PasskeyUserVerificationOption:     "required",
		setting.PasskeyAttachmentPreferenceOption: "platform",
	}))
	u := newUser(t, 0)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "registration-session", UserID: u.Id, Version: 1, UserAuthVersion: u.AuthVersion,
		Status: SessionStatusActive, RefreshHash: "registration-refresh", ExpiresAt: common.NowTimestamp() + 60,
	}).Error)

	require.NoError(t, InitWebAuthn())
	effective, err := EffectivePasskeySetting()
	require.NoError(t, err)
	assert.Equal(t, "Example Passkeys", effective.RPDisplayName)
	assert.Equal(t, "example.com", effective.RPID)
	assert.Equal(t, []string{"https://example.com", "https://login.example.com"}, effective.Origins)
	options, flowToken, err := BeginPasskeyRegistration(u.Id, "registration-session")
	require.NoError(t, err)
	assert.NotNil(t, options)
	assert.NotEmpty(t, flowToken)
	assert.Equal(t, protocol.VerificationRequired, options.Response.AuthenticatorSelection.UserVerification)
	assert.Equal(t, protocol.Platform, options.Response.AuthenticatorSelection.AuthenticatorAttachment)

	// The challenge/session must be persisted as a one-time auth flow.
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", sha256hex(flowToken)).First(&flow).Error)
	assert.Equal(t, PasskeyPurposeRegister, flow.Purpose)
	assert.Equal(t, u.Id, flow.UserId)
	assert.Equal(t, "registration-session", flow.SessionId)

	_, err = loadPasskeySession(flowToken, PasskeyPurposeRegister, u.Id, "different-session")
	require.ErrorIs(t, err, ErrInvalidFlowToken)
	_, err = loadPasskeySession(flowToken, PasskeyPurposeRegister, u.Id, "registration-session")
	require.NoError(t, err, "a mismatched session must not consume the legitimate ceremony")
}

func TestBeginPasskeyLogin(t *testing.T) {
	initPasskeyDB(t)
	require.NoError(t, InitWebAuthn())

	options, flowToken, err := BeginPasskeyLogin()
	require.NoError(t, err)
	assert.NotNil(t, options)
	assert.NotEmpty(t, flowToken)
}

func TestBeginPasskeyVerifyPersistsLongestSecurityProofScope(t *testing.T) {
	initPasskeyDB(t)
	require.NoError(t, InitWebAuthn())
	u := newUser(t, 0)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: encodeBase64([]byte("credential-id")),
		PublicKey: encodeBase64([]byte("public-key")),
	}).Error)

	options, flowToken, expiresAt, err := BeginPasskeyVerifyWithExpiry(
		u.Id, "session-id", SecurityProofScopeBackupCodeReset,
	)
	require.NoError(t, err)
	assert.NotNil(t, options)
	assert.NotEmpty(t, flowToken)
	assert.Positive(t, expiresAt)
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", sha256hex(flowToken)).First(&flow).Error)
	assert.Equal(t, SecurityProofScopeBackupCodeReset, flow.Intent)
	assert.Greater(t, len(flow.Intent), 16, "the regression scope must exceed the legacy SQL column width")
}

func TestEffectivePasskeySettingsFailClosedAndReloadWithoutRestart(t *testing.T) {
	initPasskeyDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PasskeyEnabledOption: "true",
		setting.PasskeyRPIDOption:    "example.com",
		setting.PasskeyOriginsOption: "https://login.example.com",
	}))
	first, err := EffectivePasskeySetting()
	require.NoError(t, err)
	assert.Equal(t, "example.com", first.RPID)

	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PasskeyRPIDOption:    "new.example",
		setting.PasskeyOriginsOption: "https://auth.new.example",
	}))
	second, err := EffectivePasskeySetting()
	require.NoError(t, err)
	assert.Equal(t, "new.example", second.RPID)
	assert.Equal(t, []string{"https://auth.new.example"}, second.Origins)

	require.NoError(t, setting.UpdateOption(setting.PasskeyOriginsOption, "https://outside.example"))
	_, err = EffectivePasskeySetting()
	assert.Error(t, err, "an origin outside the configured RP ID must disable ceremonies")

	t.Setenv("WEBAUTHN_ORIGINS", "http://public.example")
	_, err = EffectivePasskeySetting()
	assert.Error(t, err, "an unsafe deployment override must not bypass the option policy")
}

func TestPasskeyEnabled(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)

	assert.False(t, PasskeyEnabled(u.Id))
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: u.Id, CredentialID: "abc", PublicKey: "def"}).Error)
	assert.True(t, PasskeyEnabled(u.Id))
}

func TestPasskeyEnabledCheckedDistinguishesEmptySetFromDatabaseFailure(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)

	enabled, err := PasskeyEnabledChecked(u.Id)
	require.NoError(t, err)
	assert.False(t, enabled)

	require.NoError(t, model.DB.Migrator().DropTable(&model.PasskeyCredential{}))
	_, err = PasskeyEnabledChecked(u.Id)
	require.Error(t, err)
}

func TestReplacePasskeyCredentialRollsBackOnInsertFailure(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	old := model.PasskeyCredential{UserID: u.Id, CredentialID: "old", PublicKey: "old-key"}
	require.NoError(t, model.DB.Create(&old).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_passkey_insert BEFORE INSERT ON passkey_credentials
		BEGIN SELECT RAISE(FAIL, 'forced passkey insert failure'); END;
	`).Error)

	err := replacePasskeyCredential(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "new", PublicKey: "new-key",
	})
	require.Error(t, err)
	var credentials []model.PasskeyCredential
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).Find(&credentials).Error)
	require.Len(t, credentials, 1)
	assert.Equal(t, "old", credentials[0].CredentialID)
}

func TestReplacePasskeyCredentialAndRotateSessionIsAtomic(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	require.NoError(t, model.DB.Model(u).Update("auth_version", 1).Error)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "old", PublicKey: "old-key",
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "current", UserID: u.Id, Version: 1, UserAuthVersion: 1,
		Status: SessionStatusActive, RefreshHash: "hash", ExpiresAt: common.NowTimestamp() + 60,
	}).Error)

	require.NoError(t, replacePasskeyCredentialAndRotateSession(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "new", PublicKey: "new-key",
	}, "current"))
	var credentials []model.PasskeyCredential
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).Find(&credentials).Error)
	require.Len(t, credentials, 1)
	assert.Equal(t, "new", credentials[0].CredentialID)
	var stored model.User
	require.NoError(t, model.DB.First(&stored, u.Id).Error)
	assert.EqualValues(t, 2, stored.AuthVersion)
	var current model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "current").First(&current).Error)
	assert.EqualValues(t, 2, current.UserAuthVersion)
}

func TestReplacePasskeyCredentialAndRotateSessionRollsBackOnSessionFailure(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	require.NoError(t, model.DB.Model(u).Update("auth_version", 1).Error)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "old", PublicKey: "old-key",
	}).Error)

	err := replacePasskeyCredentialAndRotateSession(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "new", PublicKey: "new-key",
	}, "missing-session")
	require.ErrorIs(t, err, ErrSessionRevoked)
	var credentials []model.PasskeyCredential
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).Find(&credentials).Error)
	require.Len(t, credentials, 1)
	assert.Equal(t, "old", credentials[0].CredentialID)
	var stored model.User
	require.NoError(t, model.DB.First(&stored, u.Id).Error)
	assert.EqualValues(t, 1, stored.AuthVersion)
}

func TestDeleteAllPasskeysAndRotateSessionIsAtomic(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	require.NoError(t, model.DB.Model(u).Update("auth_version", 1).Error)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "credential", PublicKey: "key",
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "current", UserID: u.Id, Version: 1, UserAuthVersion: 1,
		Status: SessionStatusActive, RefreshHash: "hash", ExpiresAt: common.NowTimestamp() + 60,
	}).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_session_auth_version BEFORE UPDATE ON user_sessions
		BEGIN SELECT RAISE(FAIL, 'forced session update failure'); END;
	`).Error)

	require.Error(t, DeleteAllPasskeysAndRotateSession(u.Id, "current"))
	var credentialCount int64
	require.NoError(t, model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", u.Id).Count(&credentialCount).Error)
	require.EqualValues(t, 1, credentialCount)
	var stored model.User
	require.NoError(t, model.DB.First(&stored, u.Id).Error)
	require.EqualValues(t, 1, stored.AuthVersion)
}

func TestResetPasskeysAndRevokeSessionsIsAtomic(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: u.Id, CredentialID: "credential", PublicKey: "key",
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "current", UserID: u.Id, Version: 1, UserAuthVersion: 1,
		Status: SessionStatusActive, RefreshHash: "hash", ExpiresAt: common.NowTimestamp() + 60,
	}).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_session_revoke BEFORE UPDATE ON user_sessions
		BEGIN SELECT RAISE(FAIL, 'forced session revoke failure'); END;
	`).Error)

	require.Error(t, ResetPasskeysAndRevokeSessions(u.Id))
	var credentialCount int64
	require.NoError(t, model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", u.Id).Count(&credentialCount).Error)
	require.EqualValues(t, 1, credentialCount)
}

func TestPersistPasskeyUseIsCheckedAndRecordsCloneWarning(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	rawID := []byte("credential-id")
	stored := model.PasskeyCredential{
		UserID: u.Id, CredentialID: encodeBase64(rawID), PublicKey: "public-key", SignCount: 1,
	}
	require.NoError(t, model.DB.Create(&stored).Error)

	credential := &webauthn.Credential{ID: rawID}
	credential.Authenticator.SignCount = 2
	credential.Flags.UserPresent = true
	credential.Flags.UserVerified = true
	require.NoError(t, persistPasskeyUse(u.Id, credential))
	var got model.PasskeyCredential
	require.NoError(t, model.DB.First(&got, stored.ID).Error)
	assert.Equal(t, uint32(2), got.SignCount)
	assert.True(t, got.UserPresent)
	assert.True(t, got.UserVerified)
	assert.NotNil(t, got.LastUsedAt)

	credential.Authenticator.CloneWarning = true
	err := persistPasskeyUse(u.Id, credential)
	require.ErrorIs(t, err, ErrPasskeyCloneDetected)
	require.NoError(t, model.DB.First(&got, stored.ID).Error)
	assert.True(t, got.CloneWarning, "clone warnings must be durable even though authentication is denied")
}

func TestPersistPasskeyUseFailsClosedOnDatabaseError(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)
	rawID := []byte("credential-id")
	stored := model.PasskeyCredential{UserID: u.Id, CredentialID: encodeBase64(rawID), PublicKey: "public-key"}
	require.NoError(t, model.DB.Create(&stored).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_passkey_update BEFORE UPDATE ON passkey_credentials
		BEGIN SELECT RAISE(FAIL, 'forced passkey update failure'); END;
	`).Error)

	credential := &webauthn.Credential{ID: rawID}
	credential.Authenticator.SignCount = 1
	require.Error(t, persistPasskeyUse(u.Id, credential))
	var got model.PasskeyCredential
	require.NoError(t, model.DB.First(&got, stored.ID).Error)
	assert.Zero(t, got.SignCount)
	assert.Nil(t, got.LastUsedAt)
}

func sha256hex(s string) string {
	return common.SHA256Hex(s)
}
