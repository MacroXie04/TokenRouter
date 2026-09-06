package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func ginTestContext(headerKey, headerVal string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(headerKey, headerVal)
	c.Request = req
	return c
}

func TestIPInList(t *testing.T) {
	// Exact match.
	assert.True(t, ipInList("1.2.3.4", "1.2.3.4"))
	// Member of a comma-separated list (with whitespace tolerance).
	assert.True(t, ipInList("1.2.3.4", "1.1.1.1, 1.2.3.4, 5.6.7.8"))
	// Non-member.
	assert.False(t, ipInList("1.2.3.5", "1.2.3.4"))
	// Empty allow-list denies.
	assert.False(t, ipInList("1.2.3.4", ""))
	// Whitespace-only list denies.
	assert.False(t, ipInList("1.2.3.4", "  ,  "))
}

func TestExtractTokenKey(t *testing.T) {
	// The bearer prefix is stripped.
	r := ginTestContext("Authorization", "Bearer sk-abc123")
	assert.Equal(t, "sk-abc123", extractTokenKey(r))
}

func TestExtractTokenKeyUsesMidjourneySecretOnlyOnExactMidjourneyRoutes(t *testing.T) {
	for _, path := range []string{"/mj/submit/imagine", "/fast/mj/task/task_123/fetch"} {
		c := ginTestContext("mj-api-secret", "sk-midjourney-client")
		c.Request.URL.Path = path
		assert.Equal(t, "sk-midjourney-client", extractTokenKey(c), path)
	}
	for _, path := range []string{
		"/v1/chat/completions", "/mj/image/task_123", "/nested/fast/mj/submit/imagine", "/mj/submit/imagines",
		"/mj/notify", "/fast/mj/notify",
	} {
		c := ginTestContext("mj-api-secret", "sk-midjourney-client")
		c.Request.URL.Path = path
		assert.Empty(t, extractTokenKey(c), path)
	}
	c := ginTestContext("Authorization", "Bearer sk-primary")
	c.Request.URL.Path = "/mj/submit/imagine"
	c.Request.Header.Set("mj-api-secret", "sk-fallback")
	assert.Equal(t, "sk-primary", extractTokenKey(c))
}

func TestTokenAuthDynamicallyInheritsCurrentUserGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "dynamic-token-group.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())

	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	t.Cleanup(func() { billingsvc.SetGroupRatios(previousRatios) })

	user := model.User{Username: "dynamic-group", Status: model.UserStatusEnabled, Group: "default"}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-dynamic-group", Status: billingsvc.TokenStatusEnabled,
		UnlimitedQuota: true, Group: "",
	}
	require.NoError(t, model.DB.Create(&token).Error)
	require.NoError(t, userssvc.SetUserGroup(user.Id, "vip"))

	router := gin.New()
	router.Use(TokenAuth())
	observedUserGroup := ""
	router.GET("/", func(c *gin.Context) {
		observedUserGroup = requestctx.GetUserGroup(c)
		c.String(http.StatusOK, GetTokenGroup(c))
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token.Key)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "vip", recorder.Body.String())
	assert.Equal(t, "vip", observedUserGroup, "relay context must retain the caller group separately from routing")

	// Legacy rows can store an empty group; the routing policy already treats
	// that as default, and billing must retain the same canonical identity so a
	// default->using-group special ratio cannot be bypassed.
	require.NoError(t, userssvc.SetUserGroup(user.Id, ""))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "default", recorder.Body.String())
	assert.Equal(t, "default", observedUserGroup)

	require.NoError(t, userssvc.SetUserGroup(user.Id, "vip"))

	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

func TestTokenAuthRevalidatesExplicitAndAutoGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "runtime-token-groups.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	t.Cleanup(func() { billingsvc.SetGroupRatios(previousRatios) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.UserUsableGroupsOption: `{"default":"Default","vip":"VIP"}`,
		setting.AutoGroupsOption:       `["vip","default"]`,
	}))

	user := model.User{Username: "runtime-groups", Status: model.UserStatusEnabled, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	explicit := model.Token{UserId: user.Id, Key: "sk-explicit-group", Status: billingsvc.TokenStatusEnabled, UnlimitedQuota: true, Group: "vip"}
	auto := model.Token{UserId: user.Id, Key: "sk-auto-group", Status: billingsvc.TokenStatusEnabled, UnlimitedQuota: true, Group: userssvc.GroupAuto, AutoGroups: `["vip","default"]`}
	require.NoError(t, db.Create(&explicit).Error)
	require.NoError(t, db.Create(&auto).Error)

	router := gin.New()
	router.Use(TokenAuth())
	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"group": GetTokenGroup(c), "groups": GetTokenGroups(c)})
	})
	request := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusOK, request(explicit.Key).Code)
	autoResponse := request(auto.Key)
	require.Equal(t, http.StatusOK, autoResponse.Code, autoResponse.Body.String())
	assert.Contains(t, autoResponse.Body.String(), `"group":"vip"`)
	assert.Contains(t, autoResponse.Body.String(), `"groups":["vip","default"]`)
	assert.NotContains(t, autoResponse.Body.String(), `"group":"auto"`)

	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default"}`))
	assert.Equal(t, http.StatusForbidden, request(explicit.Key).Code)
	autoResponse = request(auto.Key)
	require.Equal(t, http.StatusOK, autoResponse.Code)
	assert.Contains(t, autoResponse.Body.String(), `"groups":["default"]`)

	require.NoError(t, setting.UpdateOption(setting.AutoGroupsOption, `[]`))
	auto.AutoGroups = ""
	require.NoError(t, db.Model(&auto).Update("auto_groups", "").Error)
	assert.Equal(t, http.StatusForbidden, request(auto.Key).Code)
}

func TestTokenOrUserAuthKeepsRelayAndDashboardCredentialsSeparated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "token-or-user-auth.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, setting.Init())
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() { billingsvc.SetGroupRatios(previousRatios) })

	blockedPAT := "sk-" + strings.Repeat("x", 29)
	dashboardPAT := strings.Repeat("p", 32)
	owner := model.User{
		Username: "relay-content-owner", Status: model.UserStatusEnabled, Group: "default",
		AccessToken: &blockedPAT,
	}
	dashboardUser := model.User{
		Username: "dashboard-content-owner", Status: model.UserStatusEnabled, Group: "default",
		AccessToken: &dashboardPAT,
	}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&dashboardUser).Error)
	allowedIP := "192.0.2.10"
	token := model.Token{
		UserId: owner.Id, Key: "sk-video-content", Status: billingsvc.TokenStatusEnabled,
		UnlimitedQuota: true, AllowIps: &allowedIP,
	}
	require.NoError(t, db.Create(&token).Error)

	router := gin.New()
	router.Use(TokenOrUserAuth())
	router.GET("/content", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id": requestctx.GetUserId(c), "has_relay_token": GetRelayToken(c) != nil,
		})
	})
	request := func(credential, remoteAddress string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/content", nil)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		req.RemoteAddr = remoteAddress
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	relayResponse := request(token.Key, allowedIP+":443")
	require.Equal(t, http.StatusOK, relayResponse.Code, relayResponse.Body.String())
	assert.Contains(t, relayResponse.Body.String(), `"user_id":`+strconv.Itoa(owner.Id))
	assert.Contains(t, relayResponse.Body.String(), `"has_relay_token":true`)

	assert.Equal(t, http.StatusForbidden, request(token.Key, "198.51.100.8:443").Code)
	assert.Equal(t, http.StatusUnauthorized, request(blockedPAT, allowedIP+":443").Code,
		"an sk-shaped credential must not fall through to dashboard PAT lookup")

	dashboardResponse := request(dashboardPAT, allowedIP+":443")
	require.Equal(t, http.StatusOK, dashboardResponse.Code, dashboardResponse.Body.String())
	assert.Contains(t, dashboardResponse.Body.String(), `"user_id":`+strconv.Itoa(dashboardUser.Id))
	assert.Contains(t, dashboardResponse.Body.String(), `"has_relay_token":false`)
	assert.Equal(t, http.StatusUnauthorized, request("", allowedIP+":443").Code)
}
