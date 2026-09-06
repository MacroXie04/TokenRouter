package controller_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// setupSecureTest prepares a DB and returns a signed-in request factory plus
// the user id and session id.
func setupSecureTest(t *testing.T, role int) (http.Handler, func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder, int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	dsn := "file:" + filepath.Join(t.TempDir(), "secure.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{},
		&model.Channel{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{},
		&model.AuthFlow{}, &model.Log{}, &model.Option{}, &model.AuthzRole{}, &model.CasbinRule{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, service.InitCasbin())
	require.NoError(t, service.InitPermissionAuthz())
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))
	require.NoError(t, service.InitWebAuthn())

	user := model.User{Username: "secuser", Password: "pw", Role: role, Status: model.UserStatusEnabled,
		Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	sid, access, refresh, err := service.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	do := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return r, do, user.Id, sid
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), "body: %s", rec.Body.String())
	return m
}

func validTOTP(secret string) string {
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		return ""
	}
	return code
}

func createTestChannel(t *testing.T, name, key string) model.Channel {
	t.Helper()
	w := uint(1)
	ch := model.Channel{
		Name: name, Type: int(constant.ChannelTypeOpenAI), Key: key,
		Status: constant.ChannelStatusEnabled, BaseURL: "https://api.example.com", Models: "gpt-4", Group: "default", Weight: &w,
	}
	require.NoError(t, model.DB.Create(&ch).Error)
	return ch
}

func enableTwoFA(t *testing.T, userId int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: userId, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
}

func issueProof(t *testing.T, do func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder, scope string) string {
	t.Helper()
	rec := do(http.MethodPost, "/api/verify",
		`{"method":"2fa","code":"`+validTOTP("JBSWY3DPEHPK3PXP")+`","scope":"`+scope+`"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return decode(t, rec)["data"].(map[string]any)["proof_token"].(string)
}

type storedTwoFAState struct {
	Secret       string
	Enabled      bool
	BackupHashes []string
}

func snapshotTwoFA(t *testing.T, userId int) storedTwoFAState {
	t.Helper()
	var twoFA model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", userId).First(&twoFA).Error)
	var backups []model.TwoFABackupCode
	require.NoError(t, model.DB.Where("user_id = ?", userId).Order("id").Find(&backups).Error)
	state := storedTwoFAState{Secret: twoFA.Secret, Enabled: twoFA.IsEnabled}
	for _, backup := range backups {
		state.BackupHashes = append(state.BackupHashes, backup.CodeHash)
	}
	return state
}

func seedProtectedTwoFA(t *testing.T, userId int) {
	t.Helper()
	enableTwoFA(t, userId)
	require.NoError(t, model.DB.Create(&model.TwoFABackupCode{
		UserId: userId, CodeHash: common.SHA256Hex("87654321"), CreatedAt: time.Now(),
	}).Error)
}

// --- UniversalVerify (POST /api/verify) ---

func TestUniversalVerify2FA(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleAdminUser)
	enableTwoFA(t, userId)

	rec := do(http.MethodPost, "/api/verify",
		`{"method":"2fa","code":"`+validTOTP("JBSWY3DPEHPK3PXP")+`","scope":"channel.key.read"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decode(t, rec)["data"].(map[string]any)
	assert.NotEmpty(t, data["proof_token"])
	assert.Equal(t, "2fa", data["method"])
	assert.Equal(t, "channel.key.read", data["scope"])
}

func TestUniversalVerifyRejectsBadInput(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleAdminUser)
	enableTwoFA(t, userId)

	// Wrong method: passkey must go through the passkey verify flow.
	rec := do(http.MethodPost, "/api/verify", `{"method":"passkey","scope":"channel.key.read"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Passkey 验证必须使用 Passkey verify 流程")

	// Unknown scope.
	rec = do(http.MethodPost, "/api/verify", `{"method":"2fa","code":"123456","scope":"admin.impersonate"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "不支持的安全验证范围")

	// Wrong code.
	rec = do(http.MethodPost, "/api/verify", `{"method":"2fa","code":"000000","scope":"channel.key.read"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "验证失败")

	// User without 2FA.
	_, do2, _, _ := setupSecureTest(t, constant.RoleAdminUser)
	rec = do2(http.MethodPost, "/api/verify", `{"method":"2fa","code":"123456","scope":"channel.key.read"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "用户未启用2FA")
}

// --- 2FA setup/reset protection ---

func TestTwoFAFirstSetupRemainsSessionAuthenticated(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
	rec := do(http.MethodPost, "/api/user/2fa/start", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decode(t, rec)["data"].(map[string]any)
	require.NotEmpty(t, data["secret"])
	state := snapshotTwoFA(t, userId)
	assert.Equal(t, data["secret"], state.Secret)
	assert.False(t, state.Enabled)
}

func TestTwoFAFirstSetupRejectsPATWithoutSession(t *testing.T) {
	r, _, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userId)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/user/2fa/start", nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_INVALID", decode(t, rec)["code"])
	var count int64
	require.NoError(t, model.DB.Model(&model.TwoFA{}).Where("user_id = ?", userId).Count(&count).Error)
	assert.Zero(t, count)
}

func TestTwoFAEnabledResetRequiresBoundProofWithoutStateLoss(t *testing.T) {
	t.Run("missing proof", func(t *testing.T) {
		_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
		seedProtectedTwoFA(t, userId)
		before := snapshotTwoFA(t, userId)

		rec := do(http.MethodPost, "/api/user/2fa/start", "", nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "SECURITY_PROOF_REQUIRED", decode(t, rec)["code"])
		assert.Equal(t, before, snapshotTwoFA(t, userId))
	})

	t.Run("forged proof", func(t *testing.T) {
		_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
		seedProtectedTwoFA(t, userId)
		before := snapshotTwoFA(t, userId)

		rec := do(http.MethodPost, "/api/user/2fa/start", "",
			map[string]string{"X-Security-Proof": "not.a.jwt"})
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "SECURITY_PROOF_INVALID", decode(t, rec)["code"])
		assert.Equal(t, before, snapshotTwoFA(t, userId))
	})

	t.Run("wrong scope", func(t *testing.T) {
		_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
		seedProtectedTwoFA(t, userId)
		proof := issueProof(t, do, service.SecurityProofScopeChannelKeyRead)
		before := snapshotTwoFA(t, userId)

		rec := do(http.MethodPost, "/api/user/2fa/start", "",
			map[string]string{"X-Security-Proof": proof})
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "SECURITY_PROOF_SCOPE_MISMATCH", decode(t, rec)["code"])
		assert.Equal(t, before, snapshotTwoFA(t, userId))
	})

	t.Run("different session", func(t *testing.T) {
		_, do, userId, sid := setupSecureTest(t, constant.RoleCommonUser)
		seedProtectedTwoFA(t, userId)
		var user model.User
		var session model.UserSession
		require.NoError(t, model.DB.First(&user, userId).Error)
		require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
		proof, _, err := service.IssueSecurityProof(service.SessionIdentity{
			UserID: userId, SessionID: "another-session",
			UserAuthVersion: user.AuthVersion, SessionVersion: session.Version,
		}, service.SecurityProofMethod2FA, []string{service.SecurityProofScopeTwoFAReset})
		require.NoError(t, err)
		before := snapshotTwoFA(t, userId)

		rec := do(http.MethodPost, "/api/user/2fa/start", "",
			map[string]string{"X-Security-Proof": proof})
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "SECURITY_PROOF_INVALID", decode(t, rec)["code"])
		assert.Equal(t, before, snapshotTwoFA(t, userId))
	})

	t.Run("stale auth version", func(t *testing.T) {
		_, do, userId, sid := setupSecureTest(t, constant.RoleCommonUser)
		seedProtectedTwoFA(t, userId)
		proof := issueProof(t, do, service.SecurityProofScopeTwoFAReset)
		before := snapshotTwoFA(t, userId)
		require.NoError(t, service.BumpAuthVersionKeepSession(userId, sid))

		rec := do(http.MethodPost, "/api/user/2fa/start", "",
			map[string]string{"X-Security-Proof": proof})
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, "未登录或会话已过期", decode(t, rec)["message"])
		assert.Equal(t, before, snapshotTwoFA(t, userId))
	})

	t.Run("valid proof", func(t *testing.T) {
		_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
		seedProtectedTwoFA(t, userId)
		proof := issueProof(t, do, service.SecurityProofScopeTwoFAReset)
		before := snapshotTwoFA(t, userId)

		rec := do(http.MethodPost, "/api/user/2fa/start", "",
			map[string]string{"X-Security-Proof": proof})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		after := snapshotTwoFA(t, userId)
		assert.NotEqual(t, before.Secret, after.Secret)
		assert.True(t, after.Enabled, "proved reset must not open an unproved setup window")
		assert.Equal(t, before.BackupHashes, after.BackupHashes)

		// Even directly after an authorized reset, an unproved follow-up reset
		// remains denied and cannot replace the just-issued secret.
		rec = do(http.MethodPost, "/api/user/2fa/start", "", nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, after, snapshotTwoFA(t, userId))
	})
}

func TestRegenerateBackupCodesRequiresScopedProof(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
	seedProtectedTwoFA(t, userId)
	before := snapshotTwoFA(t, userId)

	rec := do(http.MethodPost, "/api/user/2fa/backup_codes", "", nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", decode(t, rec)["code"])
	assert.Equal(t, before, snapshotTwoFA(t, userId))

	wrongScope := issueProof(t, do, service.SecurityProofScopeTwoFAReset)
	rec = do(http.MethodPost, "/api/user/2fa/backup_codes", "",
		map[string]string{"X-Security-Proof": wrongScope})
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_SCOPE_MISMATCH", decode(t, rec)["code"])
	assert.Equal(t, before, snapshotTwoFA(t, userId))

	proof := issueProof(t, do, service.SecurityProofScopeBackupCodeReset)
	rec = do(http.MethodPost, "/api/user/2fa/backup_codes", "",
		map[string]string{"X-Security-Proof": proof})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	after := snapshotTwoFA(t, userId)
	assert.Equal(t, before.Secret, after.Secret)
	assert.True(t, after.Enabled)
	assert.Len(t, after.BackupHashes, 8)
	assert.NotContains(t, after.BackupHashes, before.BackupHashes[0])
}

func TestDisableTwoFAVerifiesExistingFactorWithoutStateLoss(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
	seedProtectedTwoFA(t, userId)
	before := snapshotTwoFA(t, userId)

	rec := do(http.MethodPost, "/api/user/2fa/disable", `{"code":"not-a-code"}`, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, before, snapshotTwoFA(t, userId))

	// An existing one-time recovery code is an accepted step-up alternative.
	rec = do(http.MethodPost, "/api/user/2fa/disable", `{"code":"87654321"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	after := snapshotTwoFA(t, userId)
	assert.Equal(t, before.Secret, after.Secret)
	assert.False(t, after.Enabled)
	assert.Empty(t, after.BackupHashes)
}

// --- Channel key protection ---

func TestChannelKeyMaskedAndStepUp(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleRootUser)
	ch := createTestChannel(t, "up", "sk-secret-upstream")

	// List and get never disclose the key.
	rec := do(http.MethodGet, "/api/channel", "", nil)
	items := decode(t, rec)["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "", items[0].(map[string]any)["key"])

	rec = do(http.MethodGet, "/api/channel/"+common.Int2Str(ch.Id), "", nil)
	assert.Equal(t, "", decode(t, rec)["data"].(map[string]any)["key"])

	// Key disclosure requires a step-up proof.
	rec = do(http.MethodPost, "/api/channel/"+common.Int2Str(ch.Id)+"/key", "", nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", decode(t, rec)["code"])

	// A forged proof is rejected.
	rec = do(http.MethodPost, "/api/channel/"+common.Int2Str(ch.Id)+"/key", "",
		map[string]string{"X-Security-Proof": "not.a.jwt"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_INVALID", decode(t, rec)["code"])

	// A valid 2FA proof with the right scope discloses the key.
	enableTwoFA(t, userId)
	proof := issueProof(t, do, "channel.key.read")
	rec = do(http.MethodPost, "/api/channel/"+common.Int2Str(ch.Id)+"/key", "",
		map[string]string{"X-Security-Proof": proof})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "sk-secret-upstream", decode(t, rec)["data"].(map[string]any)["key"])
}

func TestLegacyMultiKeyCredentialsStayOffOrdinaryChannelResponses(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleRootUser)
	weight := uint(1)
	channel := model.Channel{
		Name: "legacy-multi-key", Type: int(constant.ChannelTypeOpenAI), Key: "",
		Other: `["legacy-secret-one","legacy-secret-two"]`, Status: constant.ChannelStatusEnabled,
		BaseURL: "https://api.example.com", Models: "gpt-4", Group: "default", Weight: &weight,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	for _, path := range []string{
		"/api/channel",
		"/api/channel/" + common.Int2Str(channel.Id),
		"/api/channel/search?keyword=legacy-multi-key",
	} {
		rec := do(http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, "path: %s; body: %s", path, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "legacy-secret-one", path)
		assert.NotContains(t, rec.Body.String(), "legacy-secret-two", path)
	}

	enableTwoFA(t, userId)
	proof := issueProof(t, do, service.SecurityProofScopeChannelKeyRead)
	rec := do(http.MethodPost, "/api/channel/"+common.Int2Str(channel.Id)+"/key", "",
		map[string]string{"X-Security-Proof": proof})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "legacy-secret-one\nlegacy-secret-two", decode(t, rec)["data"].(map[string]any)["key"])
}

func TestChannelKeyRootOnlyAndScopeCheck(t *testing.T) {
	// A plain admin is not root: RootAuth rejects before the proof check.
	_, do, _, _ := setupSecureTest(t, constant.RoleAdminUser)
	ch := createTestChannel(t, "up", "sk-secret")
	rec := do(http.MethodPost, "/api/channel/"+common.Int2Str(ch.Id)+"/key", "", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Root with a wrong-scope proof gets SCOPE_MISMATCH.
	_, do2, userId2, _ := setupSecureTest(t, constant.RoleRootUser)
	enableTwoFA(t, userId2)
	ch2 := createTestChannel(t, "up2", "sk-secret2")
	proof := issueProof(t, do2, "passkey.register")
	rec = do2(http.MethodPost, "/api/channel/"+common.Int2Str(ch2.Id)+"/key", "",
		map[string]string{"X-Security-Proof": proof})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_SCOPE_MISMATCH", decode(t, rec)["code"])
}

// --- Passkey gates + verify flow ---

func TestPasskeyRegisterGateWith2FA(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)
	previousPasskeyEnabled := setting.GetOption(setting.PasskeyEnabledOption)
	require.NoError(t, setting.UpdateOption(setting.PasskeyEnabledOption, "true"))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateOption(setting.PasskeyEnabledOption, previousPasskeyEnabled))
	})
	enableTwoFA(t, userId)

	// Without a proof the register begin is rejected.
	rec := do(http.MethodPost, "/api/user/passkey/register/begin", "", nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", decode(t, rec)["code"])

	// With a 2FA proof for passkey.register the flow proceeds.
	proof := issueProof(t, do, "passkey.register")
	rec = do(http.MethodPost, "/api/user/passkey/register/begin", "", map[string]string{"X-Security-Proof": proof})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decode(t, rec)["data"].(map[string]any)
	assert.NotEmpty(t, data["flow_token"])

	// Delete with the delete-scope proof.
	proof2 := issueProof(t, do, "passkey.delete")
	rec = do(http.MethodDelete, "/api/user/passkey", "", map[string]string{"X-Security-Proof": proof2})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Passkey 已解绑")
}

func TestPasskeyRegisterRejectsPATBeforeTwoFAStorageAccess(t *testing.T) {
	r, _, userID, _ := setupSecureTest(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)
	require.NoError(t, model.DB.Migrator().DropTable(&model.TwoFA{}))

	req := httptest.NewRequest(http.MethodPost, "/api/user/passkey/register/begin", nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "AUTH_SESSION_REQUIRED", decode(t, rec)["code"])
}

func TestPasskeyDeleteWithout2FA(t *testing.T) {
	_, do, _, _ := setupSecureTest(t, constant.RoleCommonUser)
	// No 2FA and no passkey: the reference contract returns success:false.
	rec := do(http.MethodDelete, "/api/user/passkey", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decode(t, rec)["success"])
	assert.Contains(t, rec.Body.String(), "该用户尚未绑定 Passkey")
}

// validCOSEPublicKey returns a base64-encoded ES256 COSE public key so the
// WebAuthn library can build assertion options for the stored credential.
func validCOSEPublicKey(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	pad := func(b []byte) []byte {
		out := make([]byte, 32)
		copy(out[32-len(b):], b)
		return out
	}
	cose := map[int]any{
		1:  2,  // kty: EC2
		3:  -7, // alg: ES256
		-1: 1,  // crv: P-256
		-2: pad(priv.PublicKey.X.Bytes()),
		-3: pad(priv.PublicKey.Y.Bytes()),
	}
	raw, err := cbor.Marshal(cose)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestPasskeyVerifyBegin(t *testing.T) {
	_, do, userId, _ := setupSecureTest(t, constant.RoleCommonUser)

	// No passkey yet: success:false per the reference contract.
	rec := do(http.MethodPost, "/api/user/passkey/verify/begin", `{"scope":"channel.key.read"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decode(t, rec)["success"])
	assert.Contains(t, rec.Body.String(), "该用户尚未绑定 Passkey")

	// Unsupported scope.
	rec = do(http.MethodPost, "/api/user/passkey/verify/begin", `{"scope":"admin.impersonate"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// With a registered passkey the assertion options are returned.
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: userId, CredentialID: base64.RawURLEncoding.EncodeToString([]byte("credential-1")),
		PublicKey: validCOSEPublicKey(t), Attachment: "platform",
	}).Error)
	rec = do(http.MethodPost, "/api/user/passkey/verify/begin", `{"scope":"channel.key.read"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decode(t, rec)["data"].(map[string]any)
	assert.NotEmpty(t, data["flow_token"])
	assert.NotNil(t, data["options"])

	// Finish with an unknown flow token is rejected.
	rec = do(http.MethodPost, "/api/user/passkey/verify/finish",
		`{"flow_token":"bogus","credential":"{}"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPasskeyVerifyFinishFlowBinding(t *testing.T) {
	// The finish flow must belong to the same user+session that began it.
	// Both users live in the same database.
	gin.SetMode(gin.TestMode)
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	dsn := "file:" + filepath.Join(t.TempDir(), "secure2.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{},
		&model.Channel{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.Log{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))
	require.NoError(t, service.InitWebAuthn())

	userA := model.User{Username: "a", Password: "pw", Role: constant.RoleCommonUser, Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	userB := model.User{Username: "b", Password: "pw", Role: constant.RoleCommonUser, Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&userA).Error)
	require.NoError(t, model.DB.Create(&userB).Error)
	sidA, accessA, refreshA, err := service.CompleteLogin(&userA, "127.0.0.1", "ua", "test")
	require.NoError(t, err)
	sidB, accessB, refreshB, err := service.CompleteLogin(&userB, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	do := func(access, sid, refresh, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: userA.Id, CredentialID: base64.RawURLEncoding.EncodeToString([]byte("credential-1")),
		PublicKey: validCOSEPublicKey(t), Attachment: "platform",
	}).Error)

	rec := do(accessA, sidA, refreshA, http.MethodPost, "/api/user/passkey/verify/begin", `{"scope":"channel.key.read"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	flowToken := decode(t, rec)["data"].(map[string]any)["flow_token"].(string)

	// The stored flow is bound to session A.
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", "passkey_verify").Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, sidA, flows[0].SessionId)
	assert.Equal(t, userA.Id, flows[0].UserId)

	// Session B cannot consume A's flow token: the cross-session binding is
	// checked before any credential validation. The credential must merely
	// parse (syntactically valid base64 fields, flat body) to reach that check.
	authData := base64.StdEncoding.EncodeToString(make([]byte, 37)) // rpIdHash+flags+counter
	parseable := `{"flow_token":"` + flowToken + `","id":"AQID","rawId":"AQID","type":"public-key","response":{"clientDataJSON":"e30","authenticatorData":"` + authData + `","signature":"AQID"}}`
	rec = do(accessB, sidB, refreshB, http.MethodPost, "/api/user/passkey/verify/finish", parseable)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "验证失败")

	// And session A can no longer reuse the consumed token either.
	rec = do(accessA, sidA, refreshA, http.MethodPost, "/api/user/passkey/verify/finish", parseable)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
