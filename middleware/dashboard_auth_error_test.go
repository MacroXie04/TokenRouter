package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

type dashboardAuthTestFixture struct {
	db     *gorm.DB
	user   model.User
	sid    string
	access string
}

type dashboardAuthErrorEnvelope struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newDashboardAuthTestFixture(t *testing.T, name string) dashboardAuthTestFixture {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), name+".db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		if sqlDB, sqlErr := db.DB(); sqlErr == nil {
			_ = sqlDB.Close()
		}
	})

	user := model.User{
		Username:    "dashboard-" + name,
		Password:    "test-password",
		Role:        constant.RoleCommonUser,
		Status:      model.UserStatusEnabled,
		Group:       service.GroupDefault,
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	now := common.NowTimestamp()
	sid := "dashboard-session-" + name
	session := model.UserSession{
		SID:             sid,
		UserID:          user.Id,
		Version:         1,
		UserAuthVersion: user.AuthVersion,
		Status:          service.SessionStatusActive,
		RefreshHash:     strings.Repeat("a", 64),
		LoginMethod:     "test",
		LastActiveAt:    now,
		ExpiresAt:       now + int64(time.Hour/time.Second),
	}
	require.NoError(t, db.Create(&session).Error)
	access, err := common.GenerateSessionJWT(
		user.Id,
		user.Role,
		sid,
		user.AuthVersion,
		session.Version,
		common.SessionSecret(),
		time.Hour,
	)
	require.NoError(t, err)
	return dashboardAuthTestFixture{db: db, user: user, sid: sid, access: access}
}

func performDashboardAuthRequest(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/dashboard", UserAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user_id": common.GetUserId(c)})
	})
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token})
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func requireDashboardAuthError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) dashboardAuthErrorEnvelope {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	var envelope dashboardAuthErrorEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.False(t, envelope.Success)
	assert.Equal(t, code, envelope.Code)
	assert.NotEmpty(t, envelope.Message)
	return envelope
}

func TestDashboardAuthStableErrorMapping(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		response := performDashboardAuthRequest(t, "")
		envelope := requireDashboardAuthError(t, response, http.StatusUnauthorized, dashboardAuthUnauthorizedCode)
		assert.Equal(t, dashboardAuthMessage, envelope.Message)
	})

	t.Run("invalid token", func(t *testing.T) {
		response := performDashboardAuthRequest(t, "not-a-jwt")
		envelope := requireDashboardAuthError(t, response, http.StatusUnauthorized, dashboardAuthUnauthorizedCode)
		assert.Equal(t, dashboardAuthMessage, envelope.Message)
	})

	t.Run("expired signed token", func(t *testing.T) {
		fixture := newDashboardAuthTestFixture(t, "expired")
		expired, err := common.GenerateSessionJWT(
			fixture.user.Id,
			fixture.user.Role,
			fixture.sid,
			fixture.user.AuthVersion,
			1,
			common.SessionSecret(),
			-time.Second,
		)
		require.NoError(t, err)
		response := performDashboardAuthRequest(t, expired)
		envelope := requireDashboardAuthError(t, response, http.StatusUnauthorized, dashboardAuthExpiredCode)
		assert.Equal(t, dashboardAuthMessage, envelope.Message)
	})

	t.Run("revoked session", func(t *testing.T) {
		fixture := newDashboardAuthTestFixture(t, "revoked")
		require.NoError(t, fixture.db.Model(&model.UserSession{}).Where("sid = ?", fixture.sid).Updates(map[string]any{
			"status":     service.SessionStatusRevoked,
			"revoked_at": common.NowTimestamp(),
		}).Error)
		response := performDashboardAuthRequest(t, fixture.access)
		envelope := requireDashboardAuthError(t, response, http.StatusUnauthorized, dashboardAuthRevokedCode)
		assert.Equal(t, dashboardAuthMessage, envelope.Message)
	})

	t.Run("missing session", func(t *testing.T) {
		fixture := newDashboardAuthTestFixture(t, "missing")
		require.NoError(t, fixture.db.Where("sid = ?", fixture.sid).Delete(&model.UserSession{}).Error)
		response := performDashboardAuthRequest(t, fixture.access)
		requireDashboardAuthError(t, response, http.StatusUnauthorized, dashboardAuthRevokedCode)
	})

	t.Run("database failure", func(t *testing.T) {
		fixture := newDashboardAuthTestFixture(t, "database-failure")
		sqlDB, err := fixture.db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
		response := performDashboardAuthRequest(t, fixture.access)
		envelope := requireDashboardAuthError(t, response, http.StatusInternalServerError, dashboardAuthInternalCode)
		assert.Equal(t, http.StatusText(http.StatusInternalServerError), envelope.Message)
		assert.NotContains(t, strings.ToLower(response.Body.String()), "database")
		assert.NotContains(t, strings.ToLower(response.Body.String()), "closed")
	})

	t.Run("valid token", func(t *testing.T) {
		fixture := newDashboardAuthTestFixture(t, "valid")
		response := performDashboardAuthRequest(t, fixture.access)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), `"user_id":`)
		assert.NotContains(t, response.Body.String(), `"code":`)
	})
}

func TestJWTExpiryMustBeTheOnlyClaimsFailure(t *testing.T) {
	assert.True(t, isSoleJWTExpiryError(jwt.ErrTokenExpired))
	assert.False(t, isSoleJWTExpiryError(jwt.ErrTokenSignatureInvalid))
	assert.False(t, isSoleJWTExpiryError(errors.Join(jwt.ErrTokenExpired, jwt.ErrTokenInvalidIssuer)))
}
