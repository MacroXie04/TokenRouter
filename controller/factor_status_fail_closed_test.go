package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestLoginFailsClosedWhenTwoFAStatusCannotBeRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}))
	model.DB = db
	model.LOG_DB = db
	hash, err := common.PasswordHash("correct-password")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.User{
		Username: "factor-db-failure", Password: hash, Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, AuthVersion: 1,
	}).Error)
	require.NoError(t, db.Migrator().DropTable(&model.TwoFA{}))

	r := gin.New()
	r.POST("/login", Login)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(
		`{"username":"factor-db-failure","password":"correct-password"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Values("Set-Cookie"))
}

func TestFactorStatusEndpointsFailClosedOnDatabaseErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TwoFA{}, &model.PasskeyCredential{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.Migrator().DropTable(&model.TwoFA{}, &model.PasskeyCredential{}))

	for _, test := range []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "twofa status", path: "/twofa", handler: GetTwoFAStatus},
		{name: "passkey status", path: "/passkey", handler: PasskeyStatus},
		{name: "passkey registration gate", path: "/passkey/register", handler: PasskeyRegisterBegin},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := gin.New()
			r.GET(test.path, func(c *gin.Context) {
				common.SetUserId(c, 1)
				c.Set("dashboard_session_claims", &common.JWTClaims{
					UserID: 1, SessionID: "test-session", UserAuthVersion: 1, SessionVersion: 1,
				})
				test.handler(c)
			})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
		})
	}
}
