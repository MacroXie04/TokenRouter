package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVendorMetadataCRUDSearchAndValidation(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleAdminUser)
	require.NoError(t, model.DB.AutoMigrate(&model.Vendor{}))

	rec := do(http.MethodPost, "/api/vendors/", `{
		"id":999,"name":" Acme ","description":" First vendor ","icon":" box ","status":1
	}`)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	created := body["data"].(map[string]any)
	id := int(created["id"].(float64))
	assert.NotEqual(t, 999, id)
	assert.Equal(t, "Acme", created["name"])
	assert.Equal(t, "First vendor", created["description"])

	rec = do(http.MethodPost, "/api/vendors/", `{"name":"Acme"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "供应商名称已存在", body["message"])

	rec = do(http.MethodGet, "/api/vendors/search?keyword=ACM&page_size=1", "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	page := body["data"].(map[string]any)
	assert.Equal(t, float64(1), page["total"])
	assert.Equal(t, float64(1), page["page_size"])
	require.Len(t, page["items"], 1)

	rec = do(http.MethodGet, fmt.Sprintf("/api/vendors/%d", id), "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	assert.Equal(t, "Acme", body["data"].(map[string]any)["name"])

	rec = do(http.MethodPut, "/api/vendors/", fmt.Sprintf(`{
		"id":%d,"name":"Acme Cloud","description":"Updated","icon":"cloud","status":0
	}`, id))
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	updated := body["data"].(map[string]any)
	assert.Equal(t, "Acme Cloud", updated["name"])
	assert.Equal(t, float64(0), updated["status"])

	rec = do(http.MethodPut, "/api/vendors/", `{"id":999999,"name":"must-not-upsert"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	var count int64
	require.NoError(t, model.DB.Model(&model.Vendor{}).Where("name = ?", "must-not-upsert").Count(&count).Error)
	assert.Zero(t, count)

	rec = do(http.MethodPost, "/api/vendors/", `{"name":"`+strings.Repeat("界", 129)+`"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "128")

	rec = do(http.MethodDelete, fmt.Sprintf("/api/vendors/%d", id), "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())

	// Soft deletion must release the durable uniqueness key so an operator can
	// recreate the same display name without resurrecting the old row.
	rec = do(http.MethodPost, "/api/vendors/", `{"name":"Acme Cloud","status":1}`)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"], rec.Body.String())
}

func TestVendorMetadataAuthorization(t *testing.T) {
	router, do, _ := setupDashboardSession(t, roles.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(&model.Vendor{}))

	rec := do(http.MethodGet, "/api/vendors/", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/vendors/", nil))
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
}
