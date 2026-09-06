package settings

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func TestOptionEntryBoundsPreserveMultilineValuesAndRejectUnsafeInputs(t *testing.T) {
	validMultiline := "{\n\t\"message\": \"你好\",\r\n\t\"enabled\": true\n}"
	require.NoError(t, ValidateOptionEntry("custom.setting-key:1", validMultiline))
	require.NoError(t, ValidateOptionEntry(strings.Repeat("a", MaxOptionKeyBytes), strings.Repeat("x", MaxOptionValueBytes)))
	require.NoError(t, ValidateOptionEntry("field specific", "left\u202eright"),
		"semantic sanitizers remain responsible for otherwise valid UTF-8")

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "empty key", value: "value"},
		{name: "oversized key", key: strings.Repeat("a", MaxOptionKeyBytes+1), value: "value"},
		{name: "invalid key utf8", key: string([]byte{0xff}), value: "value"},
		{name: "oversized value", key: "safe", value: strings.Repeat("x", MaxOptionValueBytes+1)},
		{name: "invalid value utf8", key: "safe", value: string([]byte{0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Error(t, ValidateOptionEntry(test.key, test.value))
		})
	}
}

func TestDirectOptionUpdateBoundsAreAtomic(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		CacheRatioOption: `{"bounded-model":0.25}`,
		"custom.notes":   "line one\nline two",
	}))
	wantPolicy := GetUsageRatioPolicy("bounded-model")

	err := UpdateOptions(map[string]string{
		CacheRatioOption: `{"bounded-model":9}`,
		"oversized":      strings.Repeat("x", MaxOptionValueBytes+1),
	})
	require.Error(t, err)
	assert.Equal(t, `{"bounded-model":0.25}`, GetOption(CacheRatioOption))
	assert.Equal(t, wantPolicy, GetUsageRatioPolicy("bounded-model"))

	var stored int64
	require.NoError(t, model.DB.Model(&model.Option{}).Where("key = ?", "oversized").Count(&stored).Error)
	assert.Zero(t, stored, "validation must happen before the database transaction")
}

func TestOptionSnapshotCountAndAggregateBounds(t *testing.T) {
	rows := make(map[string]string, MaxOptionRows)
	for index := 0; index < MaxOptionRows; index++ {
		rows[fmt.Sprintf("bounded-%04d", index)] = "x"
	}
	require.NoError(t, validateOptionSnapshot(rows))
	rows["one-too-many"] = "x"
	require.ErrorIs(t, validateOptionSnapshot(rows), ErrOptionSnapshotTooLarge)

	aggregate := map[string]string{}
	chunk := strings.Repeat("x", MaxOptionValueBytes)
	for index := 0; index < MaxOptionAggregateBytes/MaxOptionValueBytes; index++ {
		aggregate[fmt.Sprintf("chunk-%d", index)] = chunk
	}
	require.ErrorIs(t, validateOptionSnapshot(aggregate), ErrOptionSnapshotTooLarge,
		"keys make an otherwise exact eight-megabyte value set exceed the aggregate bound")
}

func TestOptionDatabasePreflightRejectsOversizedValueWithoutPublishing(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(CacheRatioOption, `{"db-model":0.5}`))
	wantPolicy := GetUsageRatioPolicy("db-model")

	require.NoError(t, model.DB.Create(&model.Option{
		Key: "out_of_band_oversized", Value: strings.Repeat("x", MaxOptionValueBytes+1),
	}).Error)
	require.ErrorIs(t, Sync(), ErrOptionSnapshotTooLarge)
	assert.Equal(t, `{"db-model":0.5}`, GetOption(CacheRatioOption))
	assert.Equal(t, wantPolicy, GetUsageRatioPolicy("db-model"))
}

func TestOptionDatabaseLoadRejectsInvalidUTF8WithoutPublishing(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(CacheRatioOption, `{"db-model":0.5}`))
	wantPolicy := GetUsageRatioPolicy("db-model")

	require.NoError(t, model.DB.Create(&model.Option{
		Key: "out_of_band_utf8", Value: string([]byte{0xff}),
	}).Error)
	require.Error(t, Sync())
	assert.Equal(t, `{"db-model":0.5}`, GetOption(CacheRatioOption))
	assert.Equal(t, wantPolicy, GetUsageRatioPolicy("db-model"))
}

func TestOptionDatabasePreflightRejectsExcessiveRows(t *testing.T) {
	setupAffinitySettingTest(t)
	stored := make([]model.Option, 0, MaxOptionRows+1)
	for index := 0; index <= MaxOptionRows; index++ {
		stored = append(stored, model.Option{Key: fmt.Sprintf("row-%04d", index), Value: "x"})
	}
	require.NoError(t, model.DB.CreateInBatches(stored, 200).Error)
	require.ErrorIs(t, Sync(), ErrOptionSnapshotTooLarge)
	assert.Empty(t, GetOption("row-0000"), "a rejected database snapshot must not be published")
}

func TestOptionDatabasePreflightRejectsExcessiveAggregate(t *testing.T) {
	setupAffinitySettingTest(t)
	chunk := strings.Repeat("x", MaxOptionValueBytes)
	stored := make([]model.Option, 0, MaxOptionAggregateBytes/MaxOptionValueBytes)
	for index := 0; index < MaxOptionAggregateBytes/MaxOptionValueBytes; index++ {
		stored = append(stored, model.Option{Key: fmt.Sprintf("chunk-%d", index), Value: chunk})
	}
	require.NoError(t, model.DB.CreateInBatches(stored, 2).Error)
	require.ErrorIs(t, Sync(), ErrOptionSnapshotTooLarge)
	assert.Empty(t, GetOption("chunk-0"), "an oversized aggregate must not be published")
}
