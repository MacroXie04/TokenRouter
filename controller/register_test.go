package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupRegisterTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "register.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	require.NoError(t, setting.UpdateOption(setting.RegistrationEnabledOption, "true"))
	setPaymentCompliance(t, true)
	require.NoError(t, setting.UpdateOption(setting.QuotaForInviteeOption, "3000"))
	require.NoError(t, setting.UpdateOption(setting.QuotaForInviterOption, "5000"))
}

func doRegister(t *testing.T, r http.Handler, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
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
	assert.Equal(t, 503000, gotInvitee.Quota, "invitee must receive QuotaForInvitee on top of initial quota")
	assert.Equal(t, 100, gotInviter.Quota, "inviter quota must be unchanged")
	assert.Equal(t, 5000, gotInviter.AffQuota, "inviter must accumulate QuotaForInviter as affiliate quota")
	assert.Equal(t, 1, gotInviter.AffCount)
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
