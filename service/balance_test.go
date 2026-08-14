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

func TestParseBalance(t *testing.T) {
	assert.Equal(t, 12.34, parseBalance([]byte(`{"balance":12.34}`)))
	assert.Equal(t, 5.0, parseBalance([]byte(`{"total_available":5}`)))
	assert.Equal(t, 100.0, parseBalance([]byte(`{"available_balance":"100.0"}`)))
	assert.Equal(t, 7.5, parseBalance([]byte(`{"data":{"balance":7.5}}`)))
	assert.Equal(t, 0.0, parseBalance([]byte(`{"nope":1}`)))
}

func TestUpdateChannelBalance(t *testing.T) {
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	model.LOG_DB = db

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_available": 42.5})
	}))
	defer mock.Close()

	ch := model.Channel{Name: "b", Type: int(constant.ChannelTypeOpenAI), Key: "k", Status: 1, Setting: `{"balance_url":"` + mock.URL + `"}`}
	require.NoError(t, model.DB.Create(&ch).Error)

	balance, err := UpdateChannelBalance(ch.Id)
	require.NoError(t, err)
	assert.Equal(t, 42.5, balance)

	var got model.Channel
	require.NoError(t, model.DB.First(&got, ch.Id).Error)
	assert.Equal(t, 42.5, got.Balance)
	assert.Greater(t, got.BalanceUpdatedTime, int64(0))

	// A channel without a balance_url returns an error and is unchanged.
	noURL := model.Channel{Name: "n", Type: 1, Key: "k", Status: 1}
	require.NoError(t, model.DB.Create(&noURL).Error)
	_, err = UpdateChannelBalance(noURL.Id)
	assert.Error(t, err)
}
