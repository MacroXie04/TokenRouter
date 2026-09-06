package router

import (
	"encoding/json"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type resetEntropyFailureReader struct{}

func (resetEntropyFailureReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestReferenceEmailQueryRoutes(t *testing.T) {
	t.Setenv("CRITICAL_RATE_LIMIT", "1000000")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	dsn := "file:" + filepath.Join(t.TempDir(), "email-query.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}, &model.User{}))
	model.DB = db

	r := SetUpRouter()

	verification := httptest.NewRecorder()
	r.ServeHTTP(verification, httptest.NewRequest(http.MethodGet,
		"/api/verification?email=query%40example.com", nil))
	require.Equal(t, http.StatusOK, verification.Code, verification.Body.String())
	var flowCount int64
	require.NoError(t, db.Model(&model.AuthFlow{}).Where("purpose = ?", "email_verification").Count(&flowCount).Error)
	assert.EqualValues(t, 1, flowCount)

	reset := httptest.NewRecorder()
	r.ServeHTTP(reset, httptest.NewRequest(http.MethodGet,
		"/api/reset_password?email=unknown%40example.com", nil))
	assert.Equal(t, http.StatusOK, reset.Code, reset.Body.String(), "account existence must remain undisclosed")

	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/verification", nil))
	assert.Equal(t, http.StatusBadRequest, missing.Code)
}

func TestReferenceUserAliasesRetainAuthentication(t *testing.T) {
	r := SetUpRouter()
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/user/2fa/setup"},
		{method: http.MethodGet, path: "/api/user/checkin"},
	} {
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, tc.method+" "+tc.path)
	}
}

func TestReferencePasswordResetTokenContract(t *testing.T) {
	t.Setenv("CRITICAL_RATE_LIMIT", "1000000")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	dsn := "file:" + filepath.Join(t.TempDir(), "password-reset.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}, &model.User{}, &model.UserSession{}))
	model.DB = db

	oldHash, err := cryptoutil.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username:      "reset-user",
		Password:      oldHash,
		Email:         "reset@example.com",
		EmailVerified: true,
		Status:        model.UserStatusEnabled,
	}
	require.NoError(t, db.Create(&user).Error)
	createResetFlow := func(code string) {
		t.Helper()
		_, createErr := authsvc.CreateAuthFlow(
			authsvc.PasswordResetPurpose, "email", "", user.Id, "", code, 15*time.Minute,
		)
		require.NoError(t, createErr)
	}

	router := SetUpRouter()
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/user/reset", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}

	createResetFlow("reference-token")
	generated := request(`{"email":"reset@example.com","token":"reference-token"}`)
	require.Equal(t, http.StatusOK, generated.Code, generated.Body.String())
	var generatedBody struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(generated.Body.Bytes(), &generatedBody))
	assert.True(t, generatedBody.Success)
	assert.Len(t, generatedBody.Data, 16)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.True(t, cryptoutil.PasswordVerify(generatedBody.Data, user.Password))
	assert.False(t, cryptoutil.PasswordVerify("old-password", user.Password))
	assert.Equal(t, http.StatusBadRequest,
		request(`{"email":"reset@example.com","token":"reference-token"}`).Code,
		"a reset token must remain single-use",
	)

	createResetFlow("extension-code")
	chosen := request(`{"email":"reset@example.com","code":"extension-code","new_password":"chosen-password"}`)
	require.Equal(t, http.StatusOK, chosen.Code, chosen.Body.String())
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.True(t, cryptoutil.PasswordVerify("chosen-password", user.Password))

	createResetFlow("retryable-token")
	assert.Equal(t, http.StatusBadRequest,
		request(`{"email":"reset@example.com","token":"retryable-token","code":"different"}`).Code,
		"conflicting credential aliases must fail closed",
	)
	restoreEntropy := cryptoutil.SetSecureRandomReaderForTesting(resetEntropyFailureReader{})
	failed := request(`{"email":"reset@example.com","token":"retryable-token"}`)
	restoreEntropy()
	assert.Equal(t, http.StatusInternalServerError, failed.Code, failed.Body.String())
	retry := request(`{"email":"reset@example.com","token":"retryable-token","new_password":"retry-password"}`)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.True(t, cryptoutil.PasswordVerify("retry-password", user.Password), "entropy failure must not consume the token")
}

func TestReferenceTrailingSlashRoutesDoNotRedirect(t *testing.T) {
	r := SetUpRouter()
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/user/"},
		{method: http.MethodPut, path: "/api/user/"},
		{method: http.MethodGet, path: "/api/channel/"},
		{method: http.MethodPost, path: "/api/channel/"},
		{method: http.MethodPut, path: "/api/channel/"},
		{method: http.MethodGet, path: "/api/redemption/"},
		{method: http.MethodPost, path: "/api/redemption/"},
		{method: http.MethodPut, path: "/api/redemption/"},
		{method: http.MethodGet, path: "/api/log/"},
	} {
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, tc.method+" "+tc.path)
		assert.Empty(t, recorder.Header().Get("Location"), tc.method+" "+tc.path)
	}
}
