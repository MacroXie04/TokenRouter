package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestCreateUserTokenWithinLimitIsAtomic(t *testing.T) {
	initSubDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	user := subUser(t, "token-create-limit", 0, "default")

	start := make(chan struct{})
	results := make(chan error, 8)
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- CreateUserTokenWithinLimit(&model.Token{
				UserId: user.Id, Key: fmt.Sprintf("sk-atomic-%d", index), Name: fmt.Sprintf("token-%d", index),
				Status: TokenStatusEnabled,
			}, 1)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		assert.ErrorIs(t, err, ErrUserTokenLimitReached)
	}
	assert.Equal(t, 1, successes)
	var count int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	err := CreateUserTokenWithinLimit(nil, 1)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrUserTokenLimitReached))
}
