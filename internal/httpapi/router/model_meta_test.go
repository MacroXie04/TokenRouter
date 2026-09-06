package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelMetadataCRUDSearchAndEnrichment(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.Vendor{}))
	vendor := model.Vendor{Name: "Acme", Status: 1}
	require.NoError(t, model.DB.Create(&vendor).Error)
	channel := createSearchChannel(t, "model-source", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "gpt-meta", "", 1)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-meta", ChannelId: channel.Id, Enabled: true, Weight: 1}).Error)
	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-meta": {Prompt: 1, Completion: 3}})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })

	rec := do(http.MethodPost, "/api/models/", fmt.Sprintf(`{
		"model_name":" gpt-meta ","description":" first ","tags":"chat",
		"vendor_id":%d,"status":1,"sync_official":1,"name_rule":0
	}`, vendor.Id))
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	created := body["data"].(map[string]any)
	id := int(created["id"].(float64))
	assert.Equal(t, "gpt-meta", created["model_name"])
	assert.Equal(t, float64(1), created["sync_official"])

	rec = do(http.MethodPost, "/api/models/", `{"model_name":"gpt-meta","status":1,"sync_official":1}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "模型名称已存在", body["message"])

	rec = do(http.MethodGet, "/api/models/search?keyword=META&vendor=acME&status=enabled&sync_official=yes&page_size=1", "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	data := body["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total"])
	assert.Equal(t, float64(1), data["page_size"])
	items := data["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "[]", item["endpoints"])
	assert.Equal(t, []any{"default"}, item["enable_groups"])
	assert.Equal(t, []any{float64(1)}, item["quota_types"])
	channels := item["bound_channels"].([]any)
	require.Len(t, channels, 1)
	assert.Equal(t, "model-source", channels[0].(map[string]any)["name"])
	assert.Equal(t, float64(1), data["vendor_counts"].(map[string]any)[fmt.Sprintf("%d", vendor.Id)])

	rec = do(http.MethodGet, fmt.Sprintf("/api/models/%d", id), "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"])
	assert.Equal(t, "gpt-meta", body["data"].(map[string]any)["model_name"])

	rec = do(http.MethodPut, "/api/models/?status_only=true", fmt.Sprintf(`{"id":%d,"status":0}`, id))
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "gpt-meta", body["data"].(map[string]any)["model_name"])
	var persisted model.Model
	require.NoError(t, model.DB.First(&persisted, id).Error)
	assert.Equal(t, 0, persisted.Status)
	assert.Equal(t, "gpt-meta", persisted.ModelName)

	rec = do(http.MethodPut, "/api/models/", `{"id":999999,"model_name":"no-upsert","status":1,"sync_official":1}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	var count int64
	require.NoError(t, model.DB.Model(&model.Model{}).Where("model_name = ?", "no-upsert").Count(&count).Error)
	assert.Zero(t, count)

	rec = do(http.MethodDelete, fmt.Sprintf("/api/models/%d", id), "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	rec = do(http.MethodPost, "/api/models/", `{"model_name":"gpt-meta","status":1,"sync_official":1}`)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"], rec.Body.String())
}

func TestModelMetadataRuleEnrichment(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleAdminUser)
	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"claude-alpha": {Prompt: 1, Completion: 2},
		"claude-beta":  {Prompt: 1, Completion: 2},
		"gpt-other":    {Prompt: 1, Completion: 2},
	})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })

	rec := do(http.MethodPost, "/api/models/", `{"model_name":"claude-","status":1,"sync_official":1,"name_rule":1}`)
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	id := int(body["data"].(map[string]any)["id"].(float64))

	rec = do(http.MethodGet, fmt.Sprintf("/api/models/%d", id), "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	data := body["data"].(map[string]any)
	assert.Equal(t, float64(2), data["matched_count"])
	assert.Equal(t, []any{"claude-alpha", "claude-beta"}, data["matched_models"])
}

func TestModelMetadataAuthorization(t *testing.T) {
	r, do, _ := setupChannelRead(t, roles.RoleCommonUser)
	rec := do(http.MethodGet, "/api/models/search", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	request := httptest.NewRequest(http.MethodGet, "/api/models/search", nil)
	request.RemoteAddr = "198.51.100.91:1234"
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, request)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestModelMetadataMissingPreviewAndSync(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleAdminUser)
	require.NoError(t, model.DB.AutoMigrate(&model.Vendor{}))
	localVendor := model.Vendor{Name: "Local", Status: 1}
	require.NoError(t, model.DB.Create(&localVendor).Error)
	local := model.Model{
		ModelName: "sync-me", Description: "old", VendorID: localVendor.Id,
		Status: 1, SyncOfficial: 1,
	}
	require.NoError(t, model.CreateModelMetadata(&local))
	for _, name := range []string{"missing-new", "absent-upstream"} {
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: name, ChannelId: 1, Enabled: true, Weight: 1}).Error)
	}

	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/i18n/en/newapi/models.json":
			_, _ = w.Write([]byte(`[
				{"model_name":"missing-new","description":"created","vendor_name":"Acme","status":0,"name_rule":0,"endpoints":["chat"]},
				{"model_name":"sync-me","description":"new","vendor_name":"Acme","status":0,"name_rule":0}
			]`))
		case "/api/i18n/en/newapi/vendors.json":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"Acme","description":"vendor","status":0}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("SYNC_UPSTREAM_BASE", upstream.URL)
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "1")

	rec := do(http.MethodGet, "/api/models/sync_upstream/preview?locale=en", "")
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	preview := body["data"].(map[string]any)
	assert.Equal(t, []any{"missing-new"}, preview["missing"])
	conflicts := preview["conflicts"].([]any)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "sync-me", conflicts[0].(map[string]any)["model_name"])

	rec = do(http.MethodPost, "/api/models/sync_upstream", `{
		"locale":"en","overwrite":[{"model_name":"sync-me","fields":["description","vendor","status"]}]
	}`)
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	result := body["data"].(map[string]any)
	assert.Equal(t, float64(1), result["created_models"])
	assert.Equal(t, float64(1), result["updated_models"])
	assert.Equal(t, []any{"absent-upstream"}, result["skipped_models"])

	var created model.Model
	require.NoError(t, model.DB.Where("model_name = ?", "missing-new").First(&created).Error)
	assert.Equal(t, 0, created.SyncOfficial)
	assert.Equal(t, 0, created.Status, "explicit upstream disabled status must be preserved")
	assert.Empty(t, created.Endpoints, "upstream endpoint inventory is preview metadata, not routing configuration")
	var updated model.Model
	require.NoError(t, model.DB.Where("model_name = ?", "sync-me").First(&updated).Error)
	assert.Equal(t, "new", updated.Description)
	assert.Equal(t, 0, updated.Status)
	assert.NotEqual(t, localVendor.Id, updated.VendorID)
	var syncedVendor model.Vendor
	require.NoError(t, model.DB.First(&syncedVendor, updated.VendorID).Error)
	assert.Equal(t, 0, syncedVendor.Status)

	rec = do(http.MethodGet, "/api/models/missing", "")
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"])
	assert.Equal(t, []any{"absent-upstream"}, body["data"])

	requestCount := requests.Load()
	require.NoError(t, model.DB.Where("model = ?", "absent-upstream").Delete(&model.Ability{}).Error)
	rec = do(http.MethodPost, "/api/models/sync_upstream", `{}`)
	body = decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	assert.Equal(t, requestCount, requests.Load(), "no-op sync must not contact upstream")

	rec = do(http.MethodPost, "/api/models/sync_upstream", `{not-json}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
}

func TestModelMetadataSyncUpstreamFailureContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleAdminUser)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	t.Setenv("SYNC_UPSTREAM_BASE", upstream.URL)
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "1")

	rec := do(http.MethodGet, "/api/models/sync_upstream/preview", "")
	body := decodeBody(t, rec)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, body["success"])
	assert.True(t, strings.HasPrefix(body["message"].(string), "获取上游模型失败:"))
	require.NotNil(t, body["source_urls"])
}

func TestModelMetadataSyncRejectsVendorFailureEnvelope(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleAdminUser)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models.json") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"message":"vendor catalog unavailable","data":[]}`))
	}))
	defer upstream.Close()
	t.Setenv("SYNC_UPSTREAM_BASE", upstream.URL)
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "1")

	rec := do(http.MethodGet, "/api/models/sync_upstream/preview", "")
	body := decodeBody(t, rec)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "vendor catalog unavailable")
}
