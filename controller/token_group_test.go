package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// doRequest serves a pre-built request through the test router.
func doRequest(r http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func createTestToken(t *testing.T, userId int, name, key string) *model.Token {
	t.Helper()
	token := model.Token{
		UserId: userId, Key: key, Status: service.TokenStatusEnabled, Name: name,
		CreatedTime: 1000, AccessedTime: 1000, ExpiredTime: -1, RemainQuota: 5000,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	return &token
}

func TestTokenListMaskedAndScoped(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	createTestToken(t, userId, "mine", "sk-abcdefgh12345678zzzz")
	require.NoError(t, model.DB.Create(&model.User{
		Username: "othertok", Password: "x", Role: constant.RoleCommonUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var other model.User
	require.NoError(t, model.DB.Where("username = ?", "othertok").First(&other).Error)
	createTestToken(t, other.Id, "theirs", "sk-secret-other")

	rec := do(http.MethodGet, "/api/token/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			Page  int `json:"page"`
			Total int `json:"total"`
			Items []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Data.Total)
	require.Len(t, body.Data.Items, 1)
	assert.Equal(t, "mine", body.Data.Items[0].Name)
	assert.NotEqual(t, "sk-abcdefgh12345678zzzz", body.Data.Items[0].Key, "list must mask keys")
	assert.True(t, strings.Contains(body.Data.Items[0].Key, "****"), "masked key shape")
}

func TestTokenSearch(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	createTestToken(t, userId, "alpha-token", "sk-aaaa")
	createTestToken(t, userId, "beta-token", "sk-bbbb")

	rec := do(http.MethodGet, "/api/token/search?keyword=alpha-token&page_size=10", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha-token")
	assert.NotContains(t, rec.Body.String(), "beta-token")

	// Fuzzy search.
	rec = do(http.MethodGet, "/api/token/search?keyword=al%25&page_size=10", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha-token")

	// Invalid pattern (doubled %, percent-encoded for the wire).
	rec = do(http.MethodGet, "/api/token/search?keyword=a%25%25b", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "连续")

	// Key search with the sk- prefix stripped.
	rec = do(http.MethodGet, "/api/token/search?token=sk-bbbb", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "beta-token")
	assert.NotContains(t, rec.Body.String(), "alpha-token")
}

func TestTokenGetAndKeyDisclosure(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	token := createTestToken(t, userId, "disclose", "sk-full-key-here-1234")

	rec := do(http.MethodGet, "/api/token/"+strconv.Itoa(token.Id), "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"key":"sk-f`)
	assert.NotContains(t, rec.Body.String(), "full-key-here")

	rec = do(http.MethodPost, "/api/token/"+strconv.Itoa(token.Id)+"/key", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "sk-full-key-here-1234")
}

func TestTokenCreateUpdate(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)

	// Name too long.
	longName := strings.Repeat("n", 51)
	rec := do(http.MethodPost, "/api/token/", `{"name":"`+longName+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Negative quota.
	rec = do(http.MethodPost, "/api/token/", `{"name":"neg","remain_quota":-5}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Valid create (unlimited).
	rec = do(http.MethodPost, "/api/token/", `{"name":"ok","unlimited_quota":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"success":true`)
	var created model.Token
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", userId, "ok").First(&created).Error)
	assert.True(t, created.UnlimitedQuota)
	assert.Equal(t, service.TokenStatusEnabled, created.Status)

	// Update with status_only=1 only flips the status.
	rec = do(http.MethodPut, "/api/token/?status_only=1", `{"id":`+strconv.Itoa(created.Id)+`,"status":2,"name":"renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&created, created.Id)
	assert.Equal(t, service.TokenStatusDisabled, created.Status)
	assert.Equal(t, "ok", created.Name, "status_only must not rename")

	// Re-enabling an expired token is rejected.
	var expired model.Token
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: userId, Key: "sk-expired", Status: service.TokenStatusExpired, Name: "exp",
		CreatedTime: 1000, AccessedTime: 1000, ExpiredTime: 100, RemainQuota: 100,
	}).Error)
	model.DB.Where("key = ?", "sk-expired").First(&expired)
	rec = do(http.MethodPut, "/api/token/?status_only=1", `{"id":`+strconv.Itoa(expired.Id)+`,"status":1}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "已过期")
}

func TestTokenDeleteAndBatch(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	t1 := createTestToken(t, userId, "del-1", "sk-del1")
	t2 := createTestToken(t, userId, "del-2", "sk-del2")

	rec := do(http.MethodDelete, "/api/token/"+strconv.Itoa(t1.Id), "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":true`)
	var count int64
	model.DB.Model(&model.Token{}).Where("id = ?", t1.Id).Count(&count)
	assert.Zero(t, count, "soft-deleted token must be gone from queries")

	// Batch delete.
	rec = do(http.MethodPost, "/api/token/batch", `{"ids":[`+strconv.Itoa(t2.Id)+`]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"data":1`)
	model.DB.Model(&model.Token{}).Where("id = ?", t2.Id).Count(&count)
	assert.Zero(t, count)

	// Batch key disclosure.
	keep := createTestToken(t, userId, "keep", "sk-keepme")
	rec = do(http.MethodPost, "/api/token/batch/keys", `{"ids":[`+strconv.Itoa(keep.Id)+`]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "sk-keepme")

	// More than 100 ids is rejected.
	var ids []string
	for i := 0; i < 101; i++ {
		ids = append(ids, strconv.Itoa(i+1))
	}
	rec = do(http.MethodPost, "/api/token/batch/keys", `{"ids":[`+strings.Join(ids, ",")+`]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "100")
}

func TestTokenMaxCount(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	require.NoError(t, setting.UpdateOption(setting.MaxUserTokensOption, "2"))
	createTestToken(t, userId, "a", "sk-a")
	createTestToken(t, userId, "b", "sk-b")

	rec := do(http.MethodPost, "/api/token/", `{"name":"c"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
	assert.Contains(t, rec.Body.String(), "已达到最大令牌数量限制")
}

func TestTokenAutoGroups(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	service.SetGroupRatios(map[string]float64{"vip": 2.0, "default": 1.0, "": 1.0})

	rec := do(http.MethodGet, "/api/token/auto-groups", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			Groups   []string `json:"groups"`
			MaxCount int      `json:"max_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []string{"default", "vip"}, body.Data.Groups)
	assert.Equal(t, 5, body.Data.MaxCount)

	// Creating a group-auto token stores JSON auto-groups.
	rec = do(http.MethodPost, "/api/token/", `{"name":"auto-tok","group":"auto","auto_groups":["vip"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created model.Token
	require.NoError(t, model.DB.Where("name = ?", "auto-tok").First(&created).Error)
	groups, err := created.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip"}, groups)
}

func TestTokenUsageEndpoint(t *testing.T) {
	r, _, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	token := createTestToken(t, userId, "usage-tok", "sk-usagekey")
	token.UsedQuota = 1200
	token.ModelLimitsEnabled = true
	token.ModelLimits = "gpt-4"
	require.NoError(t, model.DB.Save(token).Error)

	req, err := http.NewRequest(http.MethodGet, "/api/usage/token/", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer sk-usagekey")
	rec := doRequest(r, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			Object         string          `json:"object"`
			Name           string          `json:"name"`
			TotalGranted   int             `json:"total_granted"`
			TotalUsed      int             `json:"total_used"`
			TotalAvailable int             `json:"total_available"`
			UnlimitedQuota bool            `json:"unlimited_quota"`
			ModelLimits    map[string]bool `json:"model_limits"`
			ModelLimitsOn  bool            `json:"model_limits_enabled"`
			ExpiresAt      int64           `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "token_usage", body.Data.Object)
	assert.Equal(t, "usage-tok", body.Data.Name)
	assert.Equal(t, 6200, body.Data.TotalGranted)
	assert.Equal(t, 1200, body.Data.TotalUsed)
	assert.Equal(t, 5000, body.Data.TotalAvailable)
	assert.True(t, body.Data.ModelLimits["gpt-4"])

	// Missing token header is rejected.
	req, err = http.NewRequest(http.MethodGet, "/api/usage/token/", nil)
	require.NoError(t, err)
	rec = doRequest(r, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
