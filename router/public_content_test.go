package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

type publicContentRoute struct {
	key    string
	method string
	path   string
	value  string
}

func setupPublicContentRoutes(t *testing.T) (http.Handler, *gorm.DB) {
	t.Helper()
	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "public-content.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB, model.LOG_DB = db, db
	require.NoError(t, setting.Init())

	t.Cleanup(func() {
		// Restore the option snapshot as well as the database pointers so this
		// package-global compatibility cache cannot affect a later test.
		model.DB, model.LOG_DB = oldDB, oldLogDB
		if oldDB == nil || setting.Init() != nil {
			emptyDB, openErr := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
			if assert.NoError(t, openErr) {
				model.DB = emptyDB
				assert.NoError(t, emptyDB.AutoMigrate(&model.Option{}))
				assert.NoError(t, setting.Init())
				if sqlDB, sqlErr := emptyDB.DB(); assert.NoError(t, sqlErr) {
					assert.NoError(t, sqlDB.Close())
				}
				model.DB = oldDB
			}
		}
		if sqlDB, sqlErr := db.DB(); assert.NoError(t, sqlErr) {
			_ = sqlDB.Close() // The storage-failure assertion may already close it.
		}
	})

	return SetUpRouter(), db
}

func performPublicContentRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	// Keep this contract suite isolated from process-global rate-limit counters.
	req.RemoteAddr = "198.51.100.71:41000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func requirePublicContentSuccess(t *testing.T, rec *httptest.ResponseRecorder, expected string) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Header().Values("Set-Cookie"))

	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.ElementsMatch(t, []string{"success", "message", "data"}, mapKeys(envelope))
	var success bool
	var message, data string
	require.NoError(t, json.Unmarshal(envelope["success"], &success))
	require.NoError(t, json.Unmarshal(envelope["message"], &message))
	require.NoError(t, json.Unmarshal(envelope["data"], &data))
	assert.True(t, success)
	assert.Empty(t, message)
	assert.Equal(t, expected, data)
}

func requireLegalStatusFlags(t *testing.T, rec *httptest.ResponseRecorder, agreement, privacy bool) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var envelope struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.True(t, envelope.Success)
	assert.Empty(t, envelope.Message)
	assert.Equal(t, agreement, envelope.Data["user_agreement_enabled"])
	assert.Equal(t, privacy, envelope.Data["privacy_policy_enabled"])
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func requirePublicContentNotFound(t *testing.T, rec *httptest.ResponseRecorder, method, path string) {
	t.Helper()
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Param   string `json:"param"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, fmt.Sprintf("Invalid URL (%s %s)", method, path), envelope.Error.Message)
	assert.Equal(t, "invalid_request_error", envelope.Error.Type)
	assert.Empty(t, envelope.Error.Param)
	assert.Empty(t, envelope.Error.Code)
}

func TestPublicContentRoutesReferenceContract(t *testing.T) {
	handler, db := setupPublicContentRoutes(t)
	routes := []publicContentRoute{
		{key: "Notice", method: http.MethodGet, path: "/api/notice", value: "# Notice\n\n<script>alert('x')</script>"},
		{key: "legal.user_agreement", method: http.MethodGet, path: "/api/user-agreement", value: "https://legal.example.test/terms"},
		{key: "legal.privacy_policy", method: http.MethodGet, path: "/api/privacy-policy", value: "<h1>Privacy</h1>"},
		{key: "About", method: http.MethodGet, path: "/api/about", value: "# About TokenRouter"},
		{key: "HomePageContent", method: http.MethodGet, path: "/api/home_page_content", value: "Welcome **home**"},
	}

	t.Run("defaults are exact public envelopes", func(t *testing.T) {
		for _, route := range routes {
			rec := performPublicContentRequest(handler, route.method, route.path)
			requirePublicContentSuccess(t, rec, "")
		}
		status := performPublicContentRequest(handler, http.MethodGet, "/api/"+"status")
		requireLegalStatusFlags(t, status, false, false)
	})

	require.NoError(t, setting.UpdateOptions(map[string]string{
		"UserAgreement": "legacy agreement must not shadow reference key",
		"PrivacyPolicy": "legacy policy must not shadow reference key",
	}))
	t.Run("legacy legal keys do not shadow the reference config group", func(t *testing.T) {
		requirePublicContentSuccess(t,
			performPublicContentRequest(handler, http.MethodGet, "/api/user-agreement"), "")
		requirePublicContentSuccess(t,
			performPublicContentRequest(handler, http.MethodGet, "/api/privacy-policy"), "")
		status := performPublicContentRequest(handler, http.MethodGet, "/api/"+"status")
		requireLegalStatusFlags(t, status, false, false)
	})

	updates := make(map[string]string, len(routes))
	for _, route := range routes {
		updates[route.key] = route.value
	}
	require.NoError(t, setting.UpdateOptions(updates))

	t.Run("configured content preserves the reference shapes", func(t *testing.T) {
		for _, route := range routes {
			rec := performPublicContentRequest(handler, route.method, route.path)
			requirePublicContentSuccess(t, rec, route.value)
			if route.key == "Notice" {
				assert.NotContains(t, rec.Body.String(), "<script>", "JSON encoding must not emit executable HTML verbatim")
				assert.Contains(t, rec.Body.String(), `\u003cscript\u003e`)
			}
		}
		status := performPublicContentRequest(handler, http.MethodGet, "/api/"+"status")
		requireLegalStatusFlags(t, status, true, true)
	})

	t.Run("method and path matching stay exact", func(t *testing.T) {
		for _, route := range routes {
			rec := performPublicContentRequest(handler, http.MethodPost, route.path)
			requirePublicContentNotFound(t, rec, http.MethodPost, route.path)

			slashPath := route.path + "/"
			rec = performPublicContentRequest(handler, http.MethodGet, slashPath)
			require.Equal(t, http.StatusMovedPermanently, rec.Code, "%s body: %s", slashPath, rec.Body.String())
			assert.Equal(t, route.path, rec.Header().Get("Location"))
		}
		rec := performPublicContentRequest(handler, http.MethodGet, "/api/notices")
		requirePublicContentNotFound(t, rec, http.MethodGet, "/api/notices")
	})

	t.Run("the documented response boundary remains usable", func(t *testing.T) {
		atLimit := strings.Repeat("x", 1_000_000)
		require.NoError(t, setting.UpdateOption("About", atLimit))
		rec := performPublicContentRequest(handler, http.MethodGet, "/api/about")
		requirePublicContentSuccess(t, rec, atLimit)
	})

	t.Run("oversized options fail closed with a bounded response", func(t *testing.T) {
		oversized := strings.Repeat("x", 1_000_001)
		oversizedUpdates := make(map[string]string, len(routes))
		for _, route := range routes {
			oversizedUpdates[route.key] = oversized
		}
		require.NoError(t, setting.UpdateOptions(oversizedUpdates))
		for _, route := range routes {
			rec := performPublicContentRequest(handler, route.method, route.path)
			require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
			assert.Less(t, rec.Body.Len(), 256)
			assert.NotContains(t, rec.Body.String(), oversized[:128])
			var envelope map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			assert.ElementsMatch(t, []string{"success", "message"}, mapKeys(envelope))
			assert.JSONEq(t, `false`, string(envelope["success"]))
			assert.JSONEq(t, `"public content exceeds the safe display limit"`, string(envelope["message"]))
		}
	})

	configured := make(map[string]string, len(routes))
	for _, route := range routes {
		configured[route.key] = "cached:" + route.value
	}
	require.NoError(t, setting.UpdateOptions(configured))

	t.Run("reads use the synchronized snapshot when storage is unavailable", func(t *testing.T) {
		// A direct storage change is deliberately invisible until setting.Sync;
		// public requests never perform a database read.
		require.NoError(t, db.Model(&model.Option{}).
			Where("key = ?", "Notice").Update("value", "database-only value").Error)
		rec := performPublicContentRequest(handler, http.MethodGet, "/api/notice")
		requirePublicContentSuccess(t, rec, configured["Notice"])

		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
		for _, route := range routes {
			rec = performPublicContentRequest(handler, route.method, route.path)
			requirePublicContentSuccess(t, rec, configured[route.key])
		}
	})
}
