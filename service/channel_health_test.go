package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func mockUpstream(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status < 400 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "c1", "object": "chat.completion", "created": 1, "model": "gpt-4",
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
				"usage":  map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "fail"}})
		}
	}))
}

func initHealthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	model.LOG_DB = db
}

func TestChannelHealthAndAutoDisable(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
	initHealthDB(t)

	okUp := mockUpstream(200)
	defer okUp.Close()
	failUp := mockUpstream(500)
	defer failUp.Close()

	autoBan := 1
	okCh := model.Channel{Name: "ok", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: constant.ChannelStatusEnabled, BaseURL: okUp.URL, TestModel: "gpt-4", AutoBan: &autoBan}
	require.NoError(t, model.DB.Create(&okCh).Error)
	failCh := model.Channel{Name: "fail", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: constant.ChannelStatusEnabled, BaseURL: failUp.URL, TestModel: "gpt-4", AutoBan: &autoBan}
	require.NoError(t, model.DB.Create(&failCh).Error)

	// A healthy channel passes the health test.
	success, latency := TestChannelHealth(okCh.Id)
	assert.True(t, success)
	assert.GreaterOrEqual(t, latency, 0)

	// The periodic job auto-disables the failing channel.
	require.NoError(t, RunChannelHealthTests())
	var got model.Channel
	require.NoError(t, model.DB.First(&got, failCh.Id).Error)
	assert.Equal(t, constant.ChannelStatusAutoDisabled, got.Status)

	// The healthy channel remains enabled.
	var gotOk model.Channel
	require.NoError(t, model.DB.First(&gotOk, okCh.Id).Error)
	assert.Equal(t, constant.ChannelStatusEnabled, gotOk.Status)
}
