package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func setupTwoFAMutationErrorTest(t *testing.T) (*model.User, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{},
	))
	model.DB = db
	model.LOG_DB = db
	user := &model.User{
		Username: "twofa-error-user", Password: "hash", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	const secret = "JBSWY3DPEHPK3PXP"
	require.NoError(t, db.Create(&model.TwoFA{UserId: user.Id, Secret: secret}).Error)
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Unix(now, 0).UTC())
	require.NoError(t, err)
	return user, code
}

func twoFAMutationContext(user *model.User, code string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/user/2fa/enable",
		strings.NewReader(fmt.Sprintf(`{"code":%q}`, code)))
	context.Request.Header.Set("Content-Type", "application/json")
	common.SetUserId(context, user.Id)
	context.Set("dashboard_session_claims", &common.JWTClaims{
		UserID: user.Id, SessionID: "missing-session", UserAuthVersion: 1, SessionVersion: 1,
	})
	return context, recorder
}

func decodeTwoFAMutationError(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestEnableTwoFASessionLossUsesAuthErrorAndRollsBackFactor(t *testing.T) {
	user, code := setupTwoFAMutationErrorTest(t)
	context, recorder := twoFAMutationContext(user, code)

	EnableTwoFA(context)

	require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	assert.Equal(t, "AUTH_SESSION_REVOKED", decodeTwoFAMutationError(t, recorder)["code"])
	assert.Contains(t, strings.Join(recorder.Header().Values("Set-Cookie"), ";"), "access_token=")
	assert.Contains(t, strings.Join(recorder.Header().Values("Set-Cookie"), ";"), "refresh_token=")
	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&factor).Error)
	assert.False(t, factor.IsEnabled)
	var backupCount int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", user.Id).Count(&backupCount).Error)
	assert.Zero(t, backupCount)
}

func TestEnableTwoFAStorageFailureIsInternalAndDoesNotClearCookies(t *testing.T) {
	user, code := setupTwoFAMutationErrorTest(t)
	require.NoError(t, model.DB.Migrator().DropTable(&model.TwoFA{}))
	context, recorder := twoFAMutationContext(user, code)

	EnableTwoFA(context)

	require.Equal(t, http.StatusInternalServerError, recorder.Code, recorder.Body.String())
	assert.Equal(t, "AUTH_INTERNAL_ERROR", decodeTwoFAMutationError(t, recorder)["code"])
	assert.Empty(t, recorder.Header().Values("Set-Cookie"))
}
