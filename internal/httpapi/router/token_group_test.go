package router_test

import (
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
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
		UserId: userId, Key: key, Status: billingsvc.TokenStatusEnabled, Name: name,
		CreatedTime: 1000, AccessedTime: 1000, ExpiredTime: -1, RemainQuota: 5000,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	return &token
}

func TestTokenListMaskedAndScoped(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	createTestToken(t, userId, "mine", "sk-abcdefgh12345678zzzz")
	require.NoError(t, model.DB.Create(&model.User{
		Username: "othertok", Password: "x", Role: roles.RoleCommonUser, Status: model.UserStatusEnabled,
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

func TestTokenListPropagatesCountFailure(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	createTestToken(t, userId, "mine", "sk-count-failure")
	var tokenQueries atomic.Int32
	callbackName := "test:fail_token_count"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Token{}).TableName() && tokenQueries.Add(1) == 2 {
			tx.AddError(errors.New("injected token count failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	rec := do(http.MethodGet, "/api/token/", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
}

func TestTokenSearch(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
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
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
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
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	// Name too long.
	longName := strings.Repeat("n", 51)
	rec := do(http.MethodPost, "/api/token/", `{"name":"`+longName+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Negative quota.
	rec = do(http.MethodPost, "/api/token/", `{"name":"neg","remain_quota":-5}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Persisted token quota uses the same int32-safe accounting domain as
	// users, reservations, and logs.
	rec = do(http.MethodPost, "/api/token/", `{"name":"huge","remain_quota":2147483648}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Valid create (unlimited).
	rec = do(http.MethodPost, "/api/token/", `{"name":"ok","unlimited_quota":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"success":true`)
	var created model.Token
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", userId, "ok").First(&created).Error)
	assert.True(t, created.UnlimitedQuota)
	assert.Equal(t, billingsvc.TokenStatusEnabled, created.Status)
	assert.Empty(t, created.Group, "an omitted create group remains a dynamic user-group inheritance marker")

	// Update with status_only=1 only flips the status.
	rec = do(http.MethodPut, "/api/token/?status_only=1", `{"id":`+strconv.Itoa(created.Id)+`,"status":2,"name":"renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&created, created.Id)
	assert.Equal(t, billingsvc.TokenStatusDisabled, created.Status)
	assert.Equal(t, "ok", created.Name, "status_only must not rename")

	// Re-enabling an expired token is rejected.
	var expired model.Token
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: userId, Key: "sk-expired", Status: billingsvc.TokenStatusExpired, Name: "exp",
		CreatedTime: 1000, AccessedTime: 1000, ExpiredTime: 100, RemainQuota: 100,
	}).Error)
	model.DB.Where("key = ?", "sk-expired").First(&expired)
	rec = do(http.MethodPut, "/api/token/?status_only=1", `{"id":`+strconv.Itoa(expired.Id)+`,"status":1}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "已过期")
}

func TestTokenDeleteAndBatch(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
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

	// More than 100 ids and ambiguous duplicate ids are rejected for both
	// destructive and credential-disclosure batches.
	var ids []string
	for i := 0; i < 101; i++ {
		ids = append(ids, strconv.Itoa(i+1))
	}
	for _, path := range []string{"/api/token/batch", "/api/token/batch/keys"} {
		rec = do(http.MethodPost, path, `{"ids":[`+strings.Join(ids, ",")+`]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "100")
		rec = do(http.MethodPost, path, `{"ids":[`+strconv.Itoa(keep.Id)+`,`+strconv.Itoa(keep.Id)+`]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	}
	model.DB.Model(&model.Token{}).Where("id = ?", keep.Id).Count(&count)
	assert.Equal(t, int64(1), count, "invalid destructive batches must not delete a token")
}

func TestTokenMaxCount(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, setting.UpdateOption(setting.MaxUserTokensOption, "2"))
	createTestToken(t, userId, "a", "sk-a")
	createTestToken(t, userId, "b", "sk-b")

	rec := do(http.MethodPost, "/api/token/", `{"name":"c"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
	assert.Contains(t, rec.Body.String(), "已达到最大令牌数量限制")
}

type tokenEntropyFailureReader struct{}

func (tokenEntropyFailureReader) Read([]byte) (int, error) {
	return 0, errors.New("injected entropy failure")
}

func TestTokenCreateEntropyFailureDoesNotPersistCredential(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	restore := cryptoutil.SetSecureRandomReaderForTesting(tokenEntropyFailureReader{})
	t.Cleanup(restore)

	rec := do(http.MethodPost, "/api/token/", `{"name":"must-not-exist","unlimited_quota":true}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	var count int64
	require.NoError(t, model.DB.Model(&model.Token{}).
		Where("user_id = ? AND name = ?", userId, "must-not-exist").Count(&count).Error)
	assert.Zero(t, count)
}

func TestTokenAutoGroups(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	billingsvc.SetGroupRatios(map[string]float64{"vip": 2.0, "default": 1.0, "": 1.0})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.AutoGroupsOption:         `["vip","missing","default"]`,
		setting.MaxTokenAutoGroupsOption: "1",
	}))

	rec := do(http.MethodGet, "/api/token/auto-groups", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			Groups   []string `json:"groups"`
			MaxCount int      `json:"max_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []string{"vip", "default"}, body.Data.Groups, "global priority order must be preserved after permission filtering")
	assert.Equal(t, 1, body.Data.MaxCount)

	// Creating a group-auto token stores JSON auto-groups.
	require.NoError(t, setting.UpdateOption(setting.MaxTokenAutoGroupsOption, "5"))
	rec = do(http.MethodPost, "/api/token/", `{"name":"auto-tok","group":"auto","auto_groups":["vip","default"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created model.Token
	require.NoError(t, model.DB.Where("name = ?", "auto-tok").First(&created).Error)
	groups, err := created.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, groups, "submitted priority order must be stored unchanged")
}

func TestTokenGroupAuthorizationOnCreate(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default","vip":"VIP"}`))
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2, "staff": 1, "team": 1})

	for _, test := range []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{name: "configured group", payload: `{"name":"vip-token","group":"vip"}`, wantStatus: http.StatusOK},
		{name: "configured ratio outside allowlist", payload: `{"name":"staff-token","group":"staff"}`, wantStatus: http.StatusBadRequest},
		{name: "explicit empty group", payload: `{"name":"empty-token","group":""}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := do(http.MethodPost, "/api/token/", test.payload)
			assert.Equal(t, test.wantStatus, rec.Code, rec.Body.String())
		})
	}

	require.NoError(t, userssvc.SetUserGroup(userId, "team"))
	rec := do(http.MethodPost, "/api/token/", `{"name":"own-token","group":"team"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.NoError(t, userssvc.SetUserGroup(userId, "no-ratio"))
	rec = do(http.MethodPost, "/api/token/", `{"name":"own-without-ratio","group":"no-ratio"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestTokenAutoGroupValidationOnCreate(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default","vip":"VIP"}`))
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2, "staff": 1})

	for _, test := range []struct {
		name     string
		groups   string
		wantText string
	}{
		{name: "duplicate", groups: `["vip","vip"]`, wantText: "不能重复"},
		{name: "over limit", groups: `["default","vip","default","vip","default","vip"]`, wantText: "超过限制"},
		{name: "empty", groups: `[""]`, wantText: "名称无效"},
		{name: "reserved", groups: `["auto"]`, wantText: "名称无效"},
		{name: "unauthorized", groups: `["staff"]`, wantText: "无权使用"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := `{"name":"invalid-` + test.name + `","group":"auto","auto_groups":` + test.groups + `}`
			rec := do(http.MethodPost, "/api/token/", payload)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), test.wantText)
		})
	}
}

func TestTokenGroupAuthorizationOnUpdate(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default","vip":"VIP"}`))
	billingsvc.SetGroupRatios(map[string]float64{"default": 1, "vip": 2, "staff": 1, "team": 1})

	token := createTestToken(t, userId, "ordered", "sk-ordered")
	token.Group = userssvc.GroupAuto
	require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
	require.NoError(t, model.DB.Save(token).Error)
	id := strconv.Itoa(token.Id)

	// Omitted group fields retain the existing group and auto-group priority.
	rec := do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var stored model.Token
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Equal(t, userssvc.GroupAuto, stored.Group)
	groups, err := stored.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, groups)

	// An explicitly supplied list is validated and stored in caller order.
	rec = do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"reordered","auto_groups":["default","vip"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	groups, err = stored.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"default", "vip"}, groups)

	rec = do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"forbidden-auto","auto_groups":["staff"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	groups, err = stored.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"default", "vip"}, groups, "a rejected update must not change stored groups")

	// Authorized ordinary groups replace auto mode and clear its stale list.
	rec = do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"ordinary","group":"vip"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Equal(t, "vip", stored.Group)
	assert.Empty(t, stored.AutoGroups)

	rec = do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"ordinary-renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Equal(t, "vip", stored.Group, "an omitted group must not reset an ordinary token")

	rec = do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"forbidden","group":"staff"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Equal(t, "vip", stored.Group)

	require.NoError(t, userssvc.SetUserGroup(userId, "team"))
	rec = do(http.MethodPut, "/api/token/", `{"id":`+id+`,"name":"own","group":"team"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// status_only ignores unrelated group input, as before.
	rec = do(http.MethodPut, "/api/token/?status_only=1", `{"id":`+id+`,"status":2,"group":"staff"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Equal(t, "team", stored.Group)
	assert.Equal(t, billingsvc.TokenStatusDisabled, stored.Status)
}

func TestTokenUsageEndpoint(t *testing.T) {
	r, _, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
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
