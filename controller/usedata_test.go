package controller_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// insertQuotaDataRow writes a histogram row directly (the write path is
// covered by service/quota_data_test.go).
func insertQuotaDataRow(t *testing.T, userId int, username, modelName, group string, tokenId, channelId int,
	quota, count, tokenUsed int, createdAt int64, nodeName string) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID: userId, Username: username, ModelName: modelName, UseGroup: group,
		TokenID: tokenId, ChannelID: channelId, NodeName: nodeName,
		Quota: quota, Count: count, TokenUsed: tokenUsed, CreatedAt: createdAt,
	}).Error)
}

// doAsUser issues a request authenticated as a secondary session built from
// CompleteLogin cookies.
func doAsUser(t *testing.T, handler http.Handler, sid, access, refresh string) func(method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
}

// dataSpanWindow returns (start, end, hour): an hour-aligned reference hour
// ~10 days back plus a 17-day window containing it — safely inside the
// 30-day self-service span limit for every endpoint.
func dataSpanWindow() (int64, int64, int64) {
	now := common.NowTimestamp()
	day := int64(24 * 3600)
	hour := now - now%3600 - 10*day
	start := hour - 15*day
	end := hour + 2*day
	return start, end, hour
}

func TestDataQuotaDatesContract(t *testing.T) {
	_, do, uid := setupChannelRead(t, constant.RoleRootUser)
	start, end, hour := dataSpanWindow()

	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "default", 1, 5, 100, 2, 120, hour, "node-1")
	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "default", 1, 5, 50, 1, 60, hour, "node-2")
	insertQuotaDataRow(t, 2, "otheruser", "claude-sonnet-4", "vip", 2, 5, 300, 3, 30, hour, "node-1")
	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "default", 1, 5, 25, 1, 5, hour+3600, "node-1")

	// Admin /api/data/: grouped by (model, hour) across all users.
	rec := do(http.MethodGet, fmt.Sprintf("/api/data/?start_timestamp=%d&end_timestamp=%d", start, end), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	rows := body["data"].([]any)
	require.Len(t, rows, 3)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		m := r.(map[string]any)
		byKey[fmt.Sprintf("%s|%v", m["model_name"], m["created_at"])] = m
	}
	gpt := byKey[fmt.Sprintf("gpt-4o|%v", float64(hour))]
	assert.Equal(t, float64(150), gpt["quota"], "100+50 summed across nodes in the same hour")
	assert.Equal(t, float64(3), gpt["count"])
	assert.Equal(t, float64(180), gpt["token_used"])
	nextHour := byKey[fmt.Sprintf("gpt-4o|%v", float64(hour+3600))]
	assert.Equal(t, float64(25), nextHour["quota"])
	claude := byKey[fmt.Sprintf("claude-sonnet-4|%v", float64(hour))]
	assert.Equal(t, float64(300), claude["quota"])

	// Username filter: per-(user, model, hour) grouping.
	rec = do(http.MethodGet, fmt.Sprintf("/api/data/?start_timestamp=%d&end_timestamp=%d&username=chreader", start, end), "")
	rows = decodeBody(t, rec)["data"].([]any)
	require.Len(t, rows, 2)
	assert.Equal(t, "chreader", rows[0].(map[string]any)["username"])

	// /api/data/users: per-(username, hour); ordering is unspecified, so
	// verify by aggregated key.
	rec = do(http.MethodGet, fmt.Sprintf("/api/data/users?start_timestamp=%d&end_timestamp=%d", start, end), "")
	usersBody := decodeBody(t, rec)
	require.Equal(t, true, usersBody["success"], "body: %s", rec.Body.String())
	rows = usersBody["data"].([]any)
	require.Len(t, rows, 3)
	userByHour := map[string]map[string]any{}
	for _, r := range rows {
		m := r.(map[string]any)
		userByHour[fmt.Sprintf("%s|%v", m["username"], m["created_at"])] = m
	}
	assert.Equal(t, float64(150), userByHour[fmt.Sprintf("chreader|%v", float64(hour))]["quota"])
	assert.Equal(t, float64(25), userByHour[fmt.Sprintf("chreader|%v", float64(hour+3600))]["quota"])
	assert.Equal(t, float64(300), userByHour[fmt.Sprintf("otheruser|%v", float64(hour))]["quota"])

	// /api/data/self: only the signed-in user, per-(model, hour).
	rec = do(http.MethodGet, fmt.Sprintf("/api/data/self?start_timestamp=%d&end_timestamp=%d", start, end), "")
	rows = decodeBody(t, rec)["data"].([]any)
	require.Len(t, rows, 2, "self body: %s", rec.Body.String())
	for _, r := range rows {
		assert.Equal(t, "chreader", r.(map[string]any)["username"])
	}

	// Span limit: > 1 month rejected with the reference message.
	rec = do(http.MethodGet, fmt.Sprintf("/api/data/self?start_timestamp=%d&end_timestamp=%d", start, start+2592001), "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "时间跨度不能超过 1 个月", body["message"])
}

func TestDataFlowContract(t *testing.T) {
	handler, doRoot, uid := setupChannelRead(t, constant.RoleRootUser)
	start, end, hour := dataSpanWindow()

	// A second session with the admin role for the role-scoped variants.
	adminUser := model.User{Username: "flowadmin", Password: "pw", Role: constant.RoleAdminUser,
		Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&adminUser).Error)
	sid, access, refresh, err := service.CompleteLogin(&adminUser, "127.0.0.8", "ua", "test")
	require.NoError(t, err)
	doAdmin := doAsUser(t, handler, sid, access, refresh)

	ch1 := createSearchChannel(t, "flowchan-a", 1, constant.ChannelStatusEnabled, "default", "", "", 0)
	ch2 := createSearchChannel(t, "", 1, constant.ChannelStatusEnabled, "default", "", "", 0)
	tok := model.Token{UserId: uid, Key: "sk-flow-token", Name: "flow-tok", Status: 1,
		CreatedTime: common.NowTimestamp(), UnlimitedQuota: true}
	require.NoError(t, model.DB.Create(&tok).Error)

	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "default", tok.Id, ch1.Id, 400, 4, 40, hour, "node-1")
	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "vip", tok.Id, ch2.Id, 150, 2, 20, hour, "node-2")
	// Ungrouped usage must be excluded from flow.
	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "", tok.Id, ch1.Id, 999, 9, 90, hour, "node-1")

	// Root view: node + token dimensions included, names resolved.
	rec := doRoot(http.MethodGet, fmt.Sprintf("/api/data/flow?start_timestamp=%d&end_timestamp=%d", start, end), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	rows := body["data"].([]any)
	require.Len(t, rows, 2)
	rootFirst := rows[0].(map[string]any)
	assert.Equal(t, "flow-tok", rootFirst["token_name"])
	assert.Equal(t, float64(400), rootFirst["quota"])
	assert.Equal(t, "flowchan-a", rootFirst["channel_name"])

	// Missing channel name falls back to "channel-<id>" (reference).
	second := rows[1].(map[string]any)
	assert.Equal(t, fmt.Sprintf("channel-%d", ch2.Id), second["channel_name"])

	// Root + username filter.
	rec = doRoot(http.MethodGet, fmt.Sprintf("/api/data/flow?start_timestamp=%d&end_timestamp=%d&username=chreader", start, end), "")
	assert.Len(t, decodeBody(t, rec)["data"].([]any), 2)

	// Admin (non-root) view: no node_name/token_id dimensions.
	rec = doAdmin(http.MethodGet, fmt.Sprintf("/api/data/flow?start_timestamp=%d&end_timestamp=%d", start, end), "")
	rows = decodeBody(t, rec)["data"].([]any)
	require.Len(t, rows, 2)
	adminFirst := rows[0].(map[string]any)
	assert.NotContains(t, adminFirst, "node_name")
	assert.NotContains(t, adminFirst, "token_id")
	assert.Equal(t, "chreader", adminFirst["username"])

	// Validation: both timestamps required, end >= start.
	rec = doRoot(http.MethodGet, "/api/data/flow", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "invalid start_timestamp", body["message"])
	rec = doRoot(http.MethodGet, fmt.Sprintf("/api/data/flow?start_timestamp=%d", start), "")
	assert.Equal(t, "invalid end_timestamp", decodeBody(t, rec)["message"])
	rec = doRoot(http.MethodGet, fmt.Sprintf("/api/data/flow?start_timestamp=%d&end_timestamp=%d", end, start), "")
	assert.Equal(t, "invalid time range", decodeBody(t, rec)["message"])

	// Flow/self: the signed-in user's token/group/model rows with names.
	rec = doRoot(http.MethodGet, fmt.Sprintf("/api/data/flow/self?start_timestamp=%d&end_timestamp=%d", start, end), "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	selfRows := body["data"].([]any)
	require.Len(t, selfRows, 2, "flow/self body: %s", rec.Body.String())
	selfFirst := selfRows[0].(map[string]any)
	assert.Equal(t, "flow-tok", selfFirst["token_name"])
	assert.Equal(t, float64(tok.Id), selfFirst["token_id"])
	assert.NotContains(t, selfFirst, "user_id")

	// Flow/self span limit.
	rec = doRoot(http.MethodGet, fmt.Sprintf("/api/data/flow/self?start_timestamp=%d&end_timestamp=%d", start, start+2592001), "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "时间跨度不能超过 1 个月", body["message"])
}

func TestDataEndpointsValidateUniformRanges(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	start, _, _ := dataSpanWindow()
	endpoints := []string{
		"/api/data/",
		"/api/data/users",
		"/api/data/self",
		"/api/data/flow",
		"/api/data/flow/self",
	}

	for _, endpoint := range endpoints {
		t.Run(strings.TrimPrefix(endpoint, "/api/data/"), func(t *testing.T) {
			rec := do(http.MethodGet, endpoint, "")
			body := decodeBody(t, rec)
			assert.Equal(t, false, body["success"], rec.Body.String())
			assert.Equal(t, "invalid start_timestamp", body["message"])

			rec = do(http.MethodGet, fmt.Sprintf(
				"%s?start_timestamp=%d&end_timestamp=%d", endpoint, start+1, start,
			), "")
			body = decodeBody(t, rec)
			assert.Equal(t, false, body["success"], rec.Body.String())
			assert.Equal(t, "invalid time range", body["message"])

			rec = do(http.MethodGet, fmt.Sprintf(
				"%s?start_timestamp=%d&end_timestamp=%d",
				endpoint, start, start+service.DashboardDataMaxRangeSeconds+1,
			), "")
			body = decodeBody(t, rec)
			assert.Equal(t, false, body["success"], rec.Body.String())
			assert.Equal(t, "时间跨度不能超过 1 个月", body["message"])

			rec = do(http.MethodGet, fmt.Sprintf(
				"%s?start_timestamp=%d&end_timestamp=%d",
				endpoint, start, start+service.DashboardDataMaxRangeSeconds,
			), "")
			body = decodeBody(t, rec)
			assert.Equal(t, true, body["success"], rec.Body.String())
			assert.Equal(t, "", body["message"])
			assert.NotNil(t, body["data"])
		})
	}
}

func TestDataEndpointQueryFailureIsGenericAndRequestScoped(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	start, end, _ := dataSpanWindow()
	deadlineObserved := false
	callbackName := "test:fail_dashboard_data_query"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != (model.QuotaData{}).TableName() {
			return
		}
		deadline, ok := tx.Statement.Context.Deadline()
		deadlineObserved = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second
		tx.AddError(errors.New("injected database password=dashboard-secret"))
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	rec := do(http.MethodGet, fmt.Sprintf(
		"/api/data/?start_timestamp=%d&end_timestamp=%d", start, end,
	), "")
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "unable to load dashboard data", body["message"])
	assert.NotContains(t, rec.Body.String(), "dashboard-secret")
	assert.True(t, deadlineObserved, "dashboard query should inherit a bounded request context")
}

func TestDataEndpointResponseSizeFailsClosed(t *testing.T) {
	_, do, uid := setupChannelRead(t, constant.RoleRootUser)
	start, end, hour := dataSpanWindow()
	marker := "dashboard-sensitive-marker"
	token := model.Token{
		UserId: uid, Key: "sk-oversized-dashboard-token",
		Name: strings.Repeat(marker, 100_000), Status: 1,
		CreatedTime: common.NowTimestamp(), UnlimitedQuota: true,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	insertQuotaDataRow(t, uid, "chreader", "gpt-4o", "default", token.Id, 0, 1, 1, 1, hour, "node-1")

	rec := do(http.MethodGet, fmt.Sprintf(
		"/api/data/flow?start_timestamp=%d&end_timestamp=%d", start, end,
	), "")
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "unable to load dashboard data", body["message"])
	assert.Less(t, rec.Body.Len(), 1_000)
	assert.NotContains(t, rec.Body.String(), marker)
}

func TestDataAdminUsernameFilterIsBoundedAndUnambiguous(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	start, end, _ := dataSpanWindow()
	endpoints := []string{"/api/data/", "/api/data/flow"}
	validUsername := strings.Repeat("界", 64)
	invalidQueries := map[string]string{
		"65 code points":   "username=" + url.QueryEscape(strings.Repeat("界", 65)),
		"duplicate":        "username=alice&username=bob",
		"leading space":    "username=" + url.QueryEscape(" alice"),
		"control":          "username=" + url.QueryEscape("ali\x00ce"),
		"bidi override":    "username=" + url.QueryEscape("ali\u202ece"),
		"invalid encoding": "username=%FF",
	}

	for _, endpoint := range endpoints {
		t.Run(strings.TrimPrefix(endpoint, "/api/data/"), func(t *testing.T) {
			base := fmt.Sprintf("%s?start_timestamp=%d&end_timestamp=%d", endpoint, start, end)
			rec := do(http.MethodGet, base+"&username="+url.QueryEscape(validUsername), "")
			body := decodeBody(t, rec)
			assert.Equal(t, true, body["success"], rec.Body.String())

			for name, query := range invalidQueries {
				t.Run(name, func(t *testing.T) {
					rec := do(http.MethodGet, base+"&"+query, "")
					body := decodeBody(t, rec)
					assert.Equal(t, false, body["success"], rec.Body.String())
					assert.Equal(t, "invalid username", body["message"])
				})
			}
		})
	}
}
