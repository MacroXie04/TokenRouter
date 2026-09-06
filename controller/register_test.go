package controller_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupRegisterTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv(service.GenerateDefaultTokenEnvironment, "false")
	dsn := "file:" + filepath.Join(t.TempDir(), "register.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}, &model.AuthFlow{}))
	model.DB = db
	model.LOG_DB = db
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.RegistrationEnabledOption:           "true",
		setting.PasswordRegisterEnabledOption:       "true",
		setting.PasswordLoginEnabledOption:          "true",
		setting.EmailVerificationEnabledOption:      "false",
		setting.EmailDomainRestrictionEnabledOption: "false",
		setting.EmailAliasRestrictionEnabledOption:  "false",
		setting.EmailDomainWhitelistOption:          "gmail.com,163.com,126.com,qq.com,outlook.com,hotmail.com,icloud.com,yahoo.com,foxmail.com",
		setting.TurnstileCheckEnabledOption:         "false",
		setting.TurnstileEnabledOption:              "false",
		setting.TurnstileSecretKeyOption:            "",
	}))
	setPaymentCompliance(t, true)
	require.NoError(t, setting.UpdateOption(setting.QuotaForInviteeOption, "3000"))
	require.NoError(t, setting.UpdateOption(setting.QuotaForInviterOption, "5000"))
}

func TestRegisterEnforcesEmailDomainAndAliasPolicyAtAccountCreation(t *testing.T) {
	setupRegisterTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.EmailDomainRestrictionEnabledOption: "true",
		setting.EmailAliasRestrictionEnabledOption:  "true",
		setting.EmailDomainWhitelistOption:          "example.com",
	}))
	r := router.SetUpRouter()

	wrongDomain := doRegister(t, r, `{"username":"wrong-domain","password":"password123","email":"person@other.example"}`)
	assert.Equal(t, http.StatusBadRequest, wrongDomain.Code, wrongDomain.Body.String())
	assert.Contains(t, wrongDomain.Body.String(), "email domain is not allowed")

	alias := doRegister(t, r, `{"username":"alias-address","password":"password123","email":"person+tag@example.com"}`)
	assert.Equal(t, http.StatusBadRequest, alias.Code, alias.Body.String())
	assert.Contains(t, alias.Body.String(), "email aliases are not allowed")

	allowed := doRegister(t, r, `{"username":"allowed-email","password":"password123","email":"PERSON@example.com"}`)
	require.Equal(t, http.StatusOK, allowed.Code, allowed.Body.String())
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "allowed-email").First(&user).Error)
	assert.Equal(t, "person@example.com", user.Email)
}

func TestPasswordAuthPolicyAndVerifiedRegistration(t *testing.T) {
	setupRegisterTest(t)
	r := router.SetUpRouter()

	require.NoError(t, setting.UpdateOption(setting.PasswordRegisterEnabledOption, "false"))
	blocked := doRegister(t, r, `{"username":"blocked","password":"password123"}`)
	assert.Equal(t, http.StatusForbidden, blocked.Code, blocked.Body.String())
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "blocked").Count(&count).Error)
	assert.Zero(t, count)

	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PasswordRegisterEnabledOption:  "true",
		setting.EmailVerificationEnabledOption: "true",
	}))
	missing := doRegister(t, r, `{"username":"missing","password":"password123"}`)
	assert.Equal(t, http.StatusBadRequest, missing.Code, missing.Body.String())

	const email = "verified@example.com"
	const code = "482913"
	_, err := service.CreateAuthFlow(
		service.EmailVerificationPurpose, "email", email, 0, "", code, time.Minute,
	)
	require.NoError(t, err)
	wrong := doRegister(t, r, `{"username":"wrongcode","password":"password123","email":"verified@example.com","verification_code":"000000"}`)
	assert.Equal(t, http.StatusBadRequest, wrong.Code, wrong.Body.String())

	valid := doRegister(t, r, `{"username":"verified","password":"password123","email":" VERIFIED@example.com ","verification_code":"482913"}`)
	require.Equal(t, http.StatusOK, valid.Code, valid.Body.String())
	var registered model.User
	require.NoError(t, model.DB.Where("username = ?", "verified").First(&registered).Error)
	assert.Equal(t, email, registered.Email)
	assert.True(t, registered.EmailVerified)
	require.NotNil(t, registered.VerifiedEmailKey)

	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ? AND payload = ?", service.EmailVerificationPurpose, code).First(&flow).Error)
	assert.NotNil(t, flow.ConsumedAt)
	replay := doRegister(t, r, `{"username":"replay","password":"password123","email":"verified@example.com","verification_code":"482913"}`)
	assert.Equal(t, http.StatusBadRequest, replay.Code, replay.Body.String())
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "replay").Count(&count).Error)
	assert.Zero(t, count)

	require.NoError(t, setting.UpdateOption(setting.PasswordLoginEnabledOption, "false"))
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader(
		`{"username":"verified","password":"password123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	login := httptest.NewRecorder()
	r.ServeHTTP(login, loginRequest)
	assert.Equal(t, http.StatusForbidden, login.Code, login.Body.String())
	assert.Empty(t, login.Result().Cookies())
}

func doRegister(t *testing.T, r http.Handler, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRegisterAndLoginShareUTF8UsernameBoundary(t *testing.T) {
	setupRegisterTest(t)
	r := router.SetUpRouter()
	for _, username := range []string{strings.Repeat("a", 64), strings.Repeat("界", 21)} {
		response := doRegister(t, r, fmt.Sprintf(`{"username":%q,"password":"password123"}`, username))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		user, err := service.AuthenticatePassword(username, "password123")
		require.NoError(t, err)
		assert.Equal(t, username, user.Username)
	}

	tooLong := strings.Repeat("界", 22)
	response := doRegister(t, r, fmt.Sprintf(`{"username":%q,"password":"password123"}`, tooLong))
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", tooLong).Count(&count).Error)
	assert.Zero(t, count)
}

func TestRegisterRejectsPasswordBeyondBcryptByteLimit(t *testing.T) {
	setupRegisterTest(t)
	r := router.SetUpRouter()

	tooManyBytes := strings.Repeat("界", 25) // 25 runes, but 75 UTF-8 bytes.
	response := doRegister(t, r, fmt.Sprintf(`{"username":"wide-password","password":%q}`, tooManyBytes))
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "wide-password").Count(&count).Error)
	assert.Zero(t, count)
}

func TestRegisterInviterAttributionAndBonuses(t *testing.T) {
	setupRegisterTest(t)
	inviter := model.User{Username: "inviter", Password: "x", Role: 1, Status: model.UserStatusEnabled, Quota: 100, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&inviter).Error)
	require.NotEmpty(t, inviter.AffCode, "inviter must get an own aff code on creation")

	r := router.SetUpRouter()

	body := fmt.Sprintf(`{"username":"newbie","password":"password123","aff_code":%q}`, inviter.AffCode)
	rec := doRegister(t, r, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var registered model.User
	require.NoError(t, model.DB.Where("username = ?", "newbie").First(&registered).Error)
	assert.Equal(t, inviter.Id, registered.InviterId, "inviter attribution must be recorded")
	assert.NotEqual(t, inviter.AffCode, registered.AffCode, "new user gets an own code, not the inviter's")
	assert.NotEmpty(t, registered.AffCode)

	// Bonus quota: invitee +3000 directly; inviter accumulates +5000 as
	// affiliate quota (transferable via /api/user/aff_transfer).
	var gotInvitee, gotInviter model.User
	require.NoError(t, model.DB.First(&gotInvitee, registered.Id).Error)
	require.NoError(t, model.DB.First(&gotInviter, inviter.Id).Error)
	assert.Equal(t, 3000, gotInvitee.Quota, "invitee bonus is added to the canonical default quota of zero")
	assert.Equal(t, 100, gotInviter.Quota, "inviter quota must be unchanged")
	assert.Equal(t, 5000, gotInviter.AffQuota, "inviter must accumulate QuotaForInviter as affiliate quota")
	assert.Equal(t, 1, gotInviter.AffCount)
}

func TestRegisterCreatesConfiguredDefaultTokenWithCanonicalQuota(t *testing.T) {
	setupRegisterTest(t)
	t.Setenv(service.GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.InitialQuotaOption:        "700",
		setting.QuotaForNewUserOption:     "31",
		setting.DefaultUseAutoGroupOption: "true",
		setting.DefaultGroupOption:        "signup",
	}))

	response := doRegister(t, router.SetUpRouter(), `{"username":"token-user","password":"password123"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "token-user").First(&user).Error)
	assert.Equal(t, 31, user.Quota)
	assert.Equal(t, "signup", user.Group)
	var token model.Token
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&token).Error)
	assert.Equal(t, service.GroupAuto, token.Group)
	assert.True(t, token.UnlimitedQuota)
}

func TestRegisterWithoutAffCodeIsRepeatable(t *testing.T) {
	setupRegisterTest(t)
	r := router.SetUpRouter()

	// Regression: the unique aff_code index used to reject the second user
	// because every user shared the empty code.
	rec1 := doRegister(t, r, `{"username":"userone","password":"password123"}`)
	require.Equal(t, http.StatusOK, rec1.Code, "body: %s", rec1.Body.String())
	rec2 := doRegister(t, r, `{"username":"usertwo","password":"password123"}`)
	require.Equal(t, http.StatusOK, rec2.Code, "body: %s", rec2.Body.String())

	var u1, u2 model.User
	require.NoError(t, model.DB.Where("username = ?", "userone").First(&u1).Error)
	require.NoError(t, model.DB.Where("username = ?", "usertwo").First(&u2).Error)
	assert.NotEmpty(t, u1.AffCode)
	assert.NotEmpty(t, u2.AffCode)
	assert.NotEqual(t, u1.AffCode, u2.AffCode)
	assert.Zero(t, u1.InviterId)
}

func TestRegisterInvalidAffCodeIsIgnored(t *testing.T) {
	setupRegisterTest(t)
	r := router.SetUpRouter()

	rec := doRegister(t, r, `{"username":"solo","password":"password123","aff_code":"no-such-code"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var u model.User
	require.NoError(t, model.DB.Where("username = ?", "solo").First(&u).Error)
	assert.Zero(t, u.InviterId, "unknown inviter code must not fail registration")
	var body struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Success)
}

func TestRegisterRollsBackNewAccountWhenReferralCreditFails(t *testing.T) {
	setupRegisterTest(t)
	inviter := model.User{Username: "rollback-inviter", Password: "x", Role: 1, Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&inviter).Error)

	injected := errors.New("injected referral credit failure")
	const callback = "test:register_referral_credit_failure"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	r := router.SetUpRouter()
	payload := fmt.Sprintf(`{"username":"rolledback","password":"password123","aff_code":%q}`, inviter.AffCode)
	rec := doRegister(t, r, payload)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "注册失败")
	assert.NotContains(t, rec.Body.String(), injected.Error())

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "rolledback").Count(&count).Error)
	assert.Zero(t, count)
	var gotInviter model.User
	require.NoError(t, model.DB.First(&gotInviter, inviter.Id).Error)
	assert.Zero(t, gotInviter.AffCount)
	assert.Zero(t, gotInviter.AffQuota)
}
