package model

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func logCleanupTestDatabase(t *testing.T, configuredLogger logger.Interface) *gorm.DB {
	t.Helper()
	config := &gorm.Config{}
	if configuredLogger != nil {
		config.Logger = configuredLogger
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), config)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	previous := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = previous })
	return db
}

func TestDeleteOldLogBatchPhysicallyDeletesLiveAndLegacySoftDeletedRows(t *testing.T) {
	db := logCleanupTestDatabase(t, nil)
	logs := []Log{
		{CreatedAt: 10, Content: "old-live-one"},
		{CreatedAt: 20, Content: "old-legacy-tombstone"},
		{CreatedAt: 30, Content: "old-live-two"},
		{CreatedAt: 200, Content: "new"},
	}
	require.NoError(t, db.Create(&logs).Error)
	require.NoError(t, db.Delete(&logs[1]).Error)

	total, err := CountOldLog(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total, "legacy tombstones must remain eligible for physical retention cleanup")

	deleted, err := DeleteOldLogBatch(context.Background(), 100, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	total, err = CountOldLog(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total, "each relational mutation must honor the requested batch bound")

	deleted, err = DeleteOldLogBatch(context.Background(), 100, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var oldRows int64
	require.NoError(t, db.Unscoped().Model(&Log{}).Where("created_at < ?", 100).Count(&oldRows).Error)
	assert.Zero(t, oldRows, "retention must physically remove rows, including pre-existing tombstones")
	var allRows int64
	require.NoError(t, db.Unscoped().Model(&Log{}).Count(&allRows).Error)
	assert.Equal(t, int64(1), allRows)

	_, err = DeleteOldLogBatch(context.Background(), 100, maxOldLogDeleteBatchSize+1)
	assert.Error(t, err, "callers cannot turn the bounded primitive into an unbounded ID scan")
}

func TestDeleteOldLogBatchHonorsCanceledContextWithoutMutation(t *testing.T) {
	db := logCleanupTestDatabase(t, nil)
	require.NoError(t, db.Create(&Log{CreatedAt: 10, Content: "keep-on-cancel"}).Error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deleted, err := DeleteOldLogBatch(ctx, 100, 100)
	assert.Zero(t, deleted)
	assert.ErrorIs(t, err, context.Canceled)
	var rows int64
	require.NoError(t, db.Unscoped().Model(&Log{}).Count(&rows).Error)
	assert.Equal(t, int64(1), rows)
}

func TestDeleteOldLogBatchUsesStrictAuthoritativeCutoffBoundary(t *testing.T) {
	db := logCleanupTestDatabase(t, nil)
	const cutoff int64 = 1_000_000
	logs := []Log{
		{CreatedAt: cutoff - 1, Content: "expired"},
		{CreatedAt: cutoff, Content: "boundary"},
		{CreatedAt: cutoff + 1, Content: "fresh"},
	}
	require.NoError(t, db.Create(&logs).Error)

	total, err := CountOldLog(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	deleted, err := DeleteOldLogBatch(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var remaining []Log
	require.NoError(t, db.Unscoped().Order("created_at").Find(&remaining).Error)
	require.Len(t, remaining, 2)
	assert.Equal(t, cutoff, remaining[0].CreatedAt,
		"a row exactly on the DB-clock cutoff must be retained")
	assert.Equal(t, cutoff+1, remaining[1].CreatedAt)
}

type clickHouseNamedDialector struct {
	gorm.Dialector
}

func (clickHouseNamedDialector) Name() string { return "clickhouse" }

type cleanupSQLRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func newCleanupSQLRecorder() *cleanupSQLRecorder {
	return &cleanupSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
}

func (recorder *cleanupSQLRecorder) Trace(
	_ context.Context,
	_ time.Time,
	query func() (string, int64),
	_ error,
) {
	sql, _ := query()
	recorder.mu.Lock()
	recorder.statements = append(recorder.statements, sql)
	recorder.mu.Unlock()
}

func (recorder *cleanupSQLRecorder) reset() {
	recorder.mu.Lock()
	recorder.statements = nil
	recorder.mu.Unlock()
}

func (recorder *cleanupSQLRecorder) sql() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return strings.ToLower(strings.Join(recorder.statements, "\n"))
}

func TestClickHouseOldLogCleanupUsesSchemaCompatibleRawSQL(t *testing.T) {
	recorder := newCleanupSQLRecorder()
	db := logCleanupTestDatabase(t, recorder)
	require.NoError(t, db.Create(&Log{CreatedAt: 10, Content: "old"}).Error)
	recorder.reset()
	db.Config.Dialector = clickHouseNamedDialector{Dialector: db.Config.Dialector}

	deleted, err := DeleteOldLogBatch(context.Background(), 100, 100)
	assert.Zero(t, deleted)
	require.Error(t, err, "SQLite is only a recorder here and must reject the ClickHouse mutation syntax")
	generated := recorder.sql()
	assert.Contains(t, generated, "select count() from logs where created_at < 100")
	assert.Contains(t, generated, "alter table logs delete where created_at < 100 settings mutations_sync = 1")
	assert.NotContains(t, generated, "deleted_at", "ClickHouse logs have no soft-delete column")
	assert.Equal(t, "SELECT count() FROM logs WHERE created_at < ?", clickHouseCountOldLogSQL)
	assert.Equal(t,
		"ALTER TABLE logs DELETE WHERE created_at < ? SETTINGS mutations_sync = 1",
		clickHouseDeleteOldLogSQL,
	)
}
