package service

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func setupPrefillServiceTest(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "prefill.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PrefillGroup{}))
	model.DB = db
}

func TestConcurrentPrefillGroupNameUniqueness(t *testing.T) {
	setupPrefillServiceTest(t)
	const workers = 16
	var successes atomic.Int32
	var wait sync.WaitGroup
	errorsSeen := make(chan error, workers)
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wait.Done()
			group := &model.PrefillGroup{Name: "shared", Type: "model", Items: model.JSONValue(`[]`)}
			err := CreatePrefillGroup(group)
			if err == nil {
				successes.Add(1)
				return
			}
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)

	assert.EqualValues(t, 1, successes.Load())
	for err := range errorsSeen {
		assert.True(t, errors.Is(err, ErrPrefillGroupNameExists), "unexpected create error: %v", err)
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.PrefillGroup{}).Where("name = ?", "shared").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestUpdatePrefillGroupDoesNotUpsertMissingID(t *testing.T) {
	setupPrefillServiceTest(t)
	group := &model.PrefillGroup{Id: 999, Name: "missing", Type: "model", Items: model.JSONValue(`[]`)}
	err := UpdatePrefillGroup(group)
	assert.True(t, IsPrefillGroupNotFound(err))
	var count int64
	require.NoError(t, model.DB.Model(&model.PrefillGroup{}).Unscoped().Count(&count).Error)
	assert.Zero(t, count)
}
