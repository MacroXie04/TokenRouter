package channels

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"testing"
)

func TestInitAbilityCacheContextCancellationPreservesPublishedSnapshot(t *testing.T) {
	db := testutil.OpenPeriodicContextTestDB(t, &model.Ability{})
	require.NoError(t, db.Create(&model.Ability{
		Group: "replacement", Model: "new-model", Enabled: true,
	}).Error)
	abilityMu.Lock()
	previous := abilityCache
	abilityCache = map[string][]*model.Ability{
		"sentinel:model": {{Group: "sentinel", Model: "model"}},
	}
	abilityMu.Unlock()
	t.Cleanup(func() {
		abilityMu.Lock()
		abilityCache = previous
		abilityMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	callbackName := "test:cancel_ability_rebuild_after_query"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() {
			cancel()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	err := InitAbilityCacheContext(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, map[string]bool{"model": true}, GetGroupModels("sentinel"))
}
