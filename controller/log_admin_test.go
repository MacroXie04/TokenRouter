package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// insertLogRow writes a consume-typed log row.
func insertLogRow(t *testing.T, userId, tokenId, channelId int, username, tokenName, modelName, group string,
	quota, prompt, completion int, createdAt int64, other map[string]any, logType ...int) model.Log {
	t.Helper()
	typ := service.LogTypeConsume
	if len(logType) > 0 {
		typ = logType[0]
	}
	otherJSON := ""
	if other != nil {
		data, err := json.Marshal(other)
		require.NoError(t, err)
		otherJSON = string(data)
	}
	log := model.Log{
		UserId: userId, TokenId: tokenId, ChannelId: channelId, Username: username,
		TokenName: tokenName, ModelName: modelName, Group: group, Type: typ,
		Quota: quota, PromptTokens: prompt, CompletionTokens: completion,
		ChannelName: "upstream-channel", CreatedAt: createdAt, Other: otherJSON,
	}
	require.NoError(t, model.LOG_DB.Create(&log).Error)
	return log
}

// logOtherMap parses the wire "other" field (a JSON-encoded string).
func logOtherMap(t *testing.T, row map[string]any) map[string]any {
	t.Helper()
	raw, ok := row["other"].(string)
	require.True(t, ok, "other must be a JSON string, got %#v", row["other"])
	out := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	return out
}

// findLogItemByModel returns the first item whose model_name matches.
func findLogItemByModel(t *testing.T, items []any, model string) map[string]any {
	t.Helper()
	for _, it := range items {
		m, ok := it.(map[string]any)
		require.True(t, ok)
		if m["model_name"] == model {
			return m
		}
	}
	t.Fatalf("no log item with model_name=%s in %s", model, mustJSON(items))
	return nil
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

func TestLogsStatContract(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	now := common.NowTimestamp()
	insertLogRow(t, 1, 1, 5, "alice", "tok-a", "gpt-4o", "default", 100, 50, 60, now-10, nil)
	insertLogRow(t, 1, 1, 5, "alice", "tok-a", "gpt-4o", "default", 40, 20, 30, now-10, nil)
	insertLogRow(t, 2, 2, 5, "bob", "tok-b", "gpt-4o", "vip", 999, 1, 1, now-10, nil)
	insertLogRow(t, 1, 1, 5, "alice", "tok-a", "claude-sonnet-4", "default", 7, 3, 4, now-10, nil)
	insertLogRow(t, 1, 1, 5, "alice", "tok-a", "gpt-4o", "default", 500, 0, 0, now-3600, nil, service.LogTypeManage)

	rec := do(http.MethodGet, "/api/log/stat", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, float64(1146), data["quota"], "all consume rows: 100+40+999+7")
	assert.Equal(t, float64(4), data["rpm"], "consume rows in the trailing minute")
	assert.Equal(t, float64(169), data["tpm"], "prompt+completion in the trailing minute")

	// Username filter.
	rec = do(http.MethodGet, "/api/log/stat?username=alice", "")
	assert.Equal(t, float64(147), decodeBody(t, rec)["data"].(map[string]any)["quota"])

	// LIKE pattern filter (contains %).
	rec = do(http.MethodGet, "/api/log/stat?model_name=gpt%25", "")
	assert.Equal(t, float64(1139), decodeBody(t, rec)["data"].(map[string]any)["quota"])

	// Group filter (quoted column).
	rec = do(http.MethodGet, "/api/log/stat?group=vip", "")
	assert.Equal(t, float64(999), decodeBody(t, rec)["data"].(map[string]any)["quota"])

	// Channel filter.
	rec = do(http.MethodGet, "/api/log/stat?channel=5", "")
	assert.Equal(t, float64(1146), decodeBody(t, rec)["data"].(map[string]any)["quota"])

	// Timestamp filter excludes everything.
	rec = do(http.MethodGet, "/api/log/stat?start_timestamp="+fmt.Sprintf("%d", now+100), "")
	assert.Equal(t, float64(0), decodeBody(t, rec)["data"].(map[string]any)["quota"])
}

func TestLogsSelfStatContract(t *testing.T) {
	_, do, uid := setupChannelRead(t, constant.RoleRootUser)
	now := common.NowTimestamp()
	insertLogRow(t, uid, 1, 5, "chreader", "tok-a", "gpt-4o", "default", 60, 10, 20, now-5, nil)
	insertLogRow(t, 999, 1, 5, "someone-else", "tok-x", "gpt-4o", "default", 900, 0, 0, now-5, nil)

	rec := do(http.MethodGet, "/api/log/self/stat", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(60), data["quota"], "only the signed-in user's rows count")
	assert.Equal(t, float64(1), data["rpm"])
	assert.Equal(t, float64(30), data["tpm"])
}

func TestLogsListAndSelfContracts(t *testing.T) {
	handler, doRoot, rootID := setupChannelRead(t, constant.RoleRootUser)
	other := model.User{Username: "loguser", Password: "pw", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&other).Error)
	sid, access, refresh, err := service.CompleteLogin(&other, "127.0.0.9", "ua", "test")
	require.NoError(t, err)
	doUser := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	now := common.NowTimestamp()
	insertLogRow(t, other.Id, 11, 3, "loguser", "user-tok", "gpt-4o", "default", 50, 5, 6,
		now-10, map[string]any{"admin_info": "secret", "audit_info": "op", "note": "kept"})
	insertLogRow(t, other.Id, 11, 3, "loguser", "user-tok", "gpt-4o-mini", "vip", 25, 2, 3,
		now-20, map[string]any{"stream_status": "done"})
	insertLogRow(t, rootID, 12, 3, "chreader", "root-tok", "gpt-4o", "default", 900, 0, 0, now-10, nil)

	// Self list: pageInfo shape, only the user's rows, redaction applied.
	rec := doUser(http.MethodGet, "/api/log/self", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, float64(2), data["total"])
	items := data["items"].([]any)
	require.Len(t, items, 2)
	gpt4Item := findLogItemByModel(t, items, "gpt-4o")
	assert.Equal(t, "", gpt4Item["channel_name"], "channel names are redacted for users")
	otherMap := logOtherMap(t, gpt4Item)
	assert.NotContains(t, otherMap, "admin_info")
	assert.NotContains(t, otherMap, "audit_info")
	assert.Equal(t, "kept", otherMap["note"], "non-admin fields survive redaction")

	// Filters: model, group, request id.
	rec = doUser(http.MethodGet, "/api/log/self?model_name=gpt-4o-mini", "")
	assert.Equal(t, float64(1), decodeBody(t, rec)["data"].(map[string]any)["total"])
	rec = doUser(http.MethodGet, "/api/log/self?group=vip", "")
	assert.Equal(t, float64(1), decodeBody(t, rec)["data"].(map[string]any)["total"])
	rec = doUser(http.MethodGet, "/api/log/self?request_id=nope", "")
	assert.Equal(t, float64(0), decodeBody(t, rec)["data"].(map[string]any)["total"])

	// Admin list: all rows, pageInfo shape, channel names preserved.
	rec = doRoot(http.MethodGet, "/api/log", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data = body["data"].(map[string]any)
	assert.Equal(t, float64(3), data["total"])
	adminFirst := data["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "upstream-channel", adminFirst["channel_name"], "admin list keeps channel names")

	// Admin filters: username + request id.
	rec = doRoot(http.MethodGet, "/api/log?username=loguser", "")
	assert.Equal(t, float64(2), decodeBody(t, rec)["data"].(map[string]any)["total"])
	rec = doRoot(http.MethodGet, "/api/log?upstream_request_id=missing", "")
	assert.Equal(t, float64(0), decodeBody(t, rec)["data"].(map[string]any)["total"])

	// Deprecated search endpoints return the reference deprecation response.
	rec = doRoot(http.MethodGet, "/api/log/search", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "该接口已废弃", body["message"])
	rec = doUser(http.MethodGet, "/api/log/self/search", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "该接口已废弃", body["message"])
}

func TestLogByKeyContract(t *testing.T) {
	handler, _, _ := setupChannelRead(t, constant.RoleRootUser)
	now := common.NowTimestamp()
	// A relay token belonging to the root user.
	token := model.Token{UserId: 1, Key: "sk-test-token-key", Name: "logtok", Status: 1,
		CreatedTime: now, UnlimitedQuota: true}
	require.NoError(t, model.DB.Create(&token).Error)
	insertLogRow(t, 1, token.Id, 5, "chreader", "logtok", "gpt-4o", "default", 30, 3, 4,
		now-10, map[string]any{"admin_info": "secret", "visible": "yes"})
	insertLogRow(t, 2, 999, 5, "someone", "other-tok", "gpt-4o", "default", 800, 0, 0, now-10, nil)

	doToken := func(method, path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", auth)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Valid token: only its rows, redacted.
	rec := doToken(http.MethodGet, "/api/log/token", "Bearer sk-test-token-key")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	logs, ok := body["data"].([]any)
	require.True(t, ok)
	require.Len(t, logs, 1)
	row := logs[0].(map[string]any)
	assert.Equal(t, "", row["channel_name"])
	otherMap := logOtherMap(t, row)
	assert.NotContains(t, otherMap, "admin_info")
	assert.Equal(t, "yes", otherMap["visible"])

	// Missing/invalid tokens are rejected by TokenAuthReadOnly.
	rec = doToken(http.MethodGet, "/api/log/token", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = doToken(http.MethodGet, "/api/log/token", "Bearer sk-unknown")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLogsStatRejectsBadPattern(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	rec := do(http.MethodGet, "/api/log/stat?model_name=%25%25", "")
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "搜索模式中不允许包含连续的 % 通配符", body["message"])
}

func TestLogQueriesRejectOversizedControlAndDuplicateFilters(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)

	tests := []struct {
		path        string
		messagePart string
	}{
		{path: "/api/log?model_name=" + strings.Repeat("m", 256), messagePart: "model_name"},
		{path: "/api/log/self?request_id=" + strings.Repeat("r", 65), messagePart: "request_id"},
		{path: "/api/log/stat?group=vip%0Aadmin", messagePart: "group"},
		{path: "/api/log?model_name=a&model_name=b", messagePart: "不能重复"},
		{path: "/api/log?ignored=" + strings.Repeat("x", 16<<10), messagePart: "过大"},
	}
	for _, test := range tests {
		rec := do(http.MethodGet, test.path, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decodeBody(t, rec)
		assert.Equal(t, false, body["success"], test.path)
		assert.Contains(t, body["message"], test.messagePart, test.path)
	}
}
