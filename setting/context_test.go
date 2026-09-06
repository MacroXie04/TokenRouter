package setting

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestInitContextCancellationNeverPublishesPartialSnapshot(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(SystemNameOption, "baseline-name"))
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", SystemNameOption).Update("value", "replacement-name").Error)

	ctx, cancel := context.WithCancel(context.Background())
	const callback = "test:cancel_setting_init_after_query"
	require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Option{}).TableName() {
			cancel()
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callback) })

	err := InitContext(ctx)
	require.True(t, errors.Is(err, context.Canceled), "unexpected cancellation error: %v", err)
	assert.Equal(t, "baseline-name", GetOption(SystemNameOption),
		"a canceled reload must leave the previously coherent snapshot published")
}

func TestInitContextRejectsNilContext(t *testing.T) {
	setupAffinitySettingTest(t)
	require.ErrorContains(t, InitContext(nil), "context is nil")
}
