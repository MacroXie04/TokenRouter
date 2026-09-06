package channels

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"sync"
	"testing"
)

func TestConcurrentMultiKeyStatusMutationsDoNotLoseUpdates(t *testing.T) {
	db := setupChannelUpstreamUpdateTestDB(t)
	channel := model.Channel{
		Name:        "concurrent-multi-key",
		Type:        int(channelcatalog.ChannelTypeOpenAI),
		Key:         "sk-first\nsk-second",
		Status:      channelcatalog.ChannelStatusEnabled,
		Group:       userssvc.GroupDefault,
		Models:      "gpt-4o",
		ChannelInfo: `{"is_multi_key":true,"multi_key_size":2,"multi_key_status_list":{},"multi_key_polling_index":0}`,
	}
	require.NoError(t, db.Create(&channel).Error)

	// Deliberately pass two stale copies. Each mutation must reload and lock the
	// durable row instead of writing a read-modify-write snapshot supplied by
	// its caller.
	first := channel
	second := channel
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
	require.NoError(t, db.First(&refreshed, channel.Id).Error)
	info := parseChannelInfo(&refreshed)
	assert.Equal(t, 2, keyStatusOf(info, 0))
	assert.Equal(t, 2, keyStatusOf(info, 1))
}
