package public

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func initSetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv(authsvc.GenerateDefaultTokenEnvironment, "false")
	dsn := "file:" + filepath.Join(t.TempDir(), "setup.db") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// Serializing SQLite's single writer keeps the concurrency assertion
	// deterministic; the singleton primary key remains the cross-database lock.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Setup{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	return db
}

func TestPostSetupUsesCanonicalRegistrationQuotaAndAtomicDefaultToken(t *testing.T) {
	db := initSetupTestDB(t)
	t.Setenv(authsvc.GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.InitialQuotaOption:        "800",
		setting.QuotaForNewUserOption:     "29",
		setting.DefaultUseAutoGroupOption: "true",
		setting.DefaultGroupOption:        "root-default",
	}))

	response := performSetupRequest(setupTestRouter(), "planned-root")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var root model.User
	require.NoError(t, db.Where("username = ?", "planned-root").First(&root).Error)
	assert.Equal(t, 29, root.Quota)
	assert.Equal(t, "root-default", root.Group)
	var token model.Token
	require.NoError(t, db.Where("user_id = ?", root.Id).First(&token).Error)
	assert.Equal(t, userssvc.GroupAuto, token.Group)
	assert.True(t, token.UnlimitedQuota)
	assert.Equal(t, quotamath.QuotaPerUnit, token.RemainQuota)
}

func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/setup", GetSetup)
	router.POST("/api/setup", PostSetup)
	return router
}

func performSetupRequest(router http.Handler, username string) *httptest.ResponseRecorder {
	return performSetupPayload(router, map[string]any{
		"username": username, "password": "strong-password", "confirmPassword": "strong-password",
		"SelfUseModeEnabled": true, "DemoSiteEnabled": false,
	})
}

func performSetupPayload(router http.Handler, payload any) *httptest.ResponseRecorder {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	body := bytes.NewReader(bodyBytes)
	request := httptest.NewRequest(http.MethodPost, "/api/setup", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestPostSetupConcurrentSingleWinner(t *testing.T) {
	db := initSetupTestDB(t)
	router := setupTestRouter()

	const attempts = 10
	start := make(chan struct{})
	responses := make(chan int, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for i := range attempts {
		go func(index int) {
			ready.Done()
			<-start
			responses <- performSetupRequest(router, "root-"+string(rune('a'+index))).Code
		}(i)
	}
	ready.Wait()
	close(start)

	successes := 0
	for range attempts {
		status := <-responses
		if status == http.StatusOK {
			successes++
		} else {
			assert.Equal(t, http.StatusForbidden, status)
		}
	}
	assert.Equal(t, 1, successes)

	var rootCount int64
	require.NoError(t, db.Model(&model.User{}).Where("role >= ?", roles.RoleRootUser).Count(&rootCount).Error)
	assert.EqualValues(t, 1, rootCount)
	var setupCount int64
	require.NoError(t, db.Model(&model.Setup{}).Count(&setupCount).Error)
	assert.EqualValues(t, 1, setupCount)
	assert.Equal(t, "true", setting.GetOption(setting.SelfUseModeEnabledOption))
	assert.Equal(t, "false", setting.GetOption(setting.DemoSiteEnabledOption))

	status := httptest.NewRecorder()
	router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/setup", nil))
	require.Equal(t, http.StatusOK, status.Code)
	assert.Contains(t, status.Body.String(), `"setup_required":false`)
	assert.Contains(t, status.Body.String(), `"status":true`)
	assert.Contains(t, status.Body.String(), `"root_init":true`)
	assert.Contains(t, status.Body.String(), `"database_type":"sqlite"`)
}

func TestPostSetupFailureRollsBackSingletonClaim(t *testing.T) {
	db := initSetupTestDB(t)
	router := setupTestRouter()
	require.NoError(t, db.Create(&model.User{
		Username: "taken-name", Password: "irrelevant", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled,
	}).Error)

	failed := performSetupRequest(router, "taken-name")
	require.Equal(t, http.StatusInternalServerError, failed.Code, failed.Body.String())
	var setupCount int64
	require.NoError(t, db.Model(&model.Setup{}).Count(&setupCount).Error)
	assert.Zero(t, setupCount, "a failed root insert must release the setup claim")

	retry := performSetupRequest(router, "new-root")
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	var rootCount int64
	require.NoError(t, db.Model(&model.User{}).Where("role >= ?", roles.RoleRootUser).Count(&rootCount).Error)
	assert.EqualValues(t, 1, rootCount)
}

func TestGetSetupKeepsWizardRequiredWhenRootAlreadyExists(t *testing.T) {
	db := initSetupTestDB(t)
	require.NoError(t, db.Create(&model.User{
		Username: "root", Password: "already-hashed", DisplayName: "root",
		Role: roles.RoleRootUser, Status: model.UserStatusEnabled,
	}).Error)

	response := httptest.NewRecorder()
	setupTestRouter().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/setup", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			SetupRequired      bool   `json:"setup_required"`
			Status             bool   `json:"status"`
			RootInit           bool   `json:"root_init"`
			DatabaseType       string `json:"database_type"`
			SelfUseModeEnabled bool   `json:"SelfUseModeEnabled"`
			DemoSiteEnabled    bool   `json:"DemoSiteEnabled"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	assert.True(t, envelope.Success)
	assert.True(t, envelope.Data.SetupRequired)
	assert.False(t, envelope.Data.Status)
	assert.True(t, envelope.Data.RootInit)
	assert.Equal(t, "sqlite", envelope.Data.DatabaseType)
	assert.False(t, envelope.Data.SelfUseModeEnabled)
	assert.False(t, envelope.Data.DemoSiteEnabled)
}

func TestPostSetupExistingRootPersistsModeWithoutReplacingAccount(t *testing.T) {
	db := initSetupTestDB(t)
	require.NoError(t, db.Create(&model.User{
		Username: "root", Password: "already-hashed", DisplayName: "Existing Root",
		Role: roles.RoleRootUser, Status: model.UserStatusEnabled,
	}).Error)

	response := performSetupPayload(setupTestRouter(), map[string]any{
		"SelfUseModeEnabled": false,
		"DemoSiteEnabled":    true,
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var roots []model.User
	require.NoError(t, db.Where("role >= ?", roles.RoleRootUser).Find(&roots).Error)
	require.Len(t, roots, 1)
	assert.Equal(t, "Existing Root", roots[0].DisplayName)
	assert.Equal(t, "false", setting.GetOption(setting.SelfUseModeEnabledOption))
	assert.Equal(t, "true", setting.GetOption(setting.DemoSiteEnabledOption))
	var setupCount int64
	require.NoError(t, db.Model(&model.Setup{}).Count(&setupCount).Error)
	assert.EqualValues(t, 1, setupCount)
}

func TestPostSetupRejectsInvalidCredentialsAndConflictingModes(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "missing confirmation",
			payload: map[string]any{
				"username": "root", "password": "password1",
			},
		},
		{
			name: "mismatched confirmation",
			payload: map[string]any{
				"username": "root", "password": "password1", "confirmPassword": "password2",
			},
		},
		{
			name: "reference username limit",
			payload: map[string]any{
				"username": strings.Repeat("u", 13), "password": "password1", "confirmPassword": "password1",
			},
		},
		{
			name: "bounded password",
			payload: map[string]any{
				"username": "root", "password": strings.Repeat("p", 65), "confirmPassword": strings.Repeat("p", 65),
			},
		},
		{
			name: "mutually exclusive modes",
			payload: map[string]any{
				"username": "root", "password": "password1", "confirmPassword": "password1",
				"SelfUseModeEnabled": true, "DemoSiteEnabled": true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := initSetupTestDB(t)
			response := performSetupPayload(setupTestRouter(), test.payload)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			var count int64
			require.NoError(t, db.Model(&model.Setup{}).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, db.Model(&model.User{}).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, db.Model(&model.Option{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestPostSetupOptionFailureRollsBackRootAndMarkerWithoutLeakingDatabaseError(t *testing.T) {
	db := initSetupTestDB(t)
	t.Setenv(authsvc.GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, db.Exec(`
		CREATE TRIGGER reject_setup_mode
		BEFORE INSERT ON options
		WHEN NEW.key = 'SelfUseModeEnabled'
		BEGIN
			SELECT RAISE(ABORT, 'forced-sensitive-setup-failure');
		END
	`).Error)

	response := performSetupRequest(setupTestRouter(), "new-root")
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), "forced-sensitive-setup-failure")

	for _, value := range []any{&model.Setup{}, &model.User{}, &model.Token{}, &model.Option{}} {
		var count int64
		require.NoError(t, db.Model(value).Count(&count).Error)
		assert.Zero(t, count)
	}
}

func TestPostSetupCommittedStateWinsOverUnrelatedCacheRefreshFailure(t *testing.T) {
	db := initSetupTestDB(t)
	// This simulates an out-of-band database corruption that the live snapshot
	// has not observed yet. It is unrelated to the setup transaction but makes
	// the post-commit full settings refresh fail validation.
	require.NoError(t, db.Create(&model.Option{
		Key: setting.DefaultGroupOption, Value: " unsafe",
	}).Error)

	response := performSetupRequest(setupTestRouter(), "durable-root")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var setupCount, rootCount int64
	require.NoError(t, db.Model(&model.Setup{}).Count(&setupCount).Error)
	require.NoError(t, db.Model(&model.User{}).
		Where("username = ? AND role >= ?", "durable-root", roles.RoleRootUser).
		Count(&rootCount).Error)
	assert.EqualValues(t, 1, setupCount)
	assert.EqualValues(t, 1, rootCount)
	var selfUse model.Option
	require.NoError(t, db.Where("key = ?", setting.SelfUseModeEnabledOption).First(&selfUse).Error)
	assert.Equal(t, "true", selfUse.Value)

	retry := performSetupRequest(setupTestRouter(), "another-root")
	assert.Equal(t, http.StatusForbidden, retry.Code, retry.Body.String())
}

func TestPostSetupCannotOverwriteCompletedModes(t *testing.T) {
	initSetupTestDB(t)
	router := setupTestRouter()
	first := performSetupRequest(router, "new-root")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := performSetupPayload(router, map[string]any{
		"SelfUseModeEnabled": false,
		"DemoSiteEnabled":    true,
	})
	assert.Equal(t, http.StatusForbidden, second.Code, second.Body.String())
	assert.Equal(t, "true", setting.GetOption(setting.SelfUseModeEnabledOption))
	assert.Equal(t, "false", setting.GetOption(setting.DemoSiteEnabledOption))
}
