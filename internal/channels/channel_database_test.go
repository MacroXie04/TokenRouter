package channels

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"os"
	"sync"
	"testing"
)

// TestChannelMultiKeyExternalDatabaseConcurrency exercises the durable
// read-lock-update path with stale channel snapshots on a real row-locking
// database. Both independent status changes must survive the race.
func TestChannelMultiKeyExternalDatabaseConcurrency(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	suffix := cryptoutil.BestEffortRandomAlphanumeric(10)
	channel := model.Channel{
		Name:   "multi-key-db-int-" + suffix,
		Type:   int(channelcatalog.ChannelTypeOpenAI),
		Key:    "sk-first-" + suffix + "\nsk-second-" + suffix,
		Status: channelcatalog.ChannelStatusEnabled,
		Group:  userssvc.GroupDefault,
		Models: "gpt-4o",
		ChannelInfo: `{"is_multi_key":true,"multi_key_size":2,` +
			`"multi_key_status_list":{},"multi_key_polling_index":0}`,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	first, second := channel, channel
	start := make(chan struct{})
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		errs <- SetMultiKeyStatus(&first, 0, 2)
	}()
	go func() {
		defer workers.Done()
		<-start
		errs <- SetMultiKeyStatus(&second, 1, 2)
	}()
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var refreshed model.Channel
	require.NoError(t, model.DB.First(&refreshed, channel.Id).Error)
	info := parseChannelInfo(&refreshed)
	assert.Equal(t, 2, keyStatusOf(info, 0))
	assert.Equal(t, 2, keyStatusOf(info, 1))
}
