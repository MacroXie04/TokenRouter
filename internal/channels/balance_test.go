package channels

import (
	"encoding/json"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBalance(t *testing.T) {
	assert.Equal(t, 12.34, parseBalance([]byte(`{"balance":12.34}`)))
	assert.Equal(t, 5.0, parseBalance([]byte(`{"total_available":5}`)))
	assert.Equal(t, 100.0, parseBalance([]byte(`{"available_balance":"100.0"}`)))
	assert.Equal(t, 7.5, parseBalance([]byte(`{"data":{"balance":7.5}}`)))
	assert.Equal(t, 0.0, parseBalance([]byte(`{"nope":1}`)))
}

func TestUpdateChannelBalanceResponseIsBounded(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxBalanceResponseBytes)+1)))
	}))
	defer mock.Close()
	channel := model.Channel{Name: "bounded", Type: 1, Key: "k", Status: 1, Balance: 9,
		Setting: `{"balance_url":"` + mock.URL + `"}`}
	require.NoError(t, model.DB.Create(&channel).Error)

	_, err = UpdateChannelBalance(channel.Id)
	require.Error(t, err)
	assert.True(t, errors.Is(err, httpx.ErrBodyTooLarge))
	var got model.Channel
	require.NoError(t, model.DB.First(&got, channel.Id).Error)
	assert.Equal(t, 9.0, got.Balance, "an oversized response must not update the stored balance")
}

func TestUpdateChannelBalance(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	model.LOG_DB = db

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_available": 42.5})
	}))
	defer mock.Close()

	ch := model.Channel{Name: "b", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "k", Status: 1, Setting: `{"balance_url":"` + mock.URL + `"}`}
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
