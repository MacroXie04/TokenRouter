package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestMigrateSeparateRelationalLogDatabaseAddsAuditIdempotencyKey(t *testing.T) {
	t.Setenv("LOG_SQL_DSN", "")
	primary, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "primary.db")), &gorm.Config{})
	require.NoError(t, err)
	sink, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sink.db")), &gorm.Config{})
	require.NoError(t, err)

	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = primary, sink
	t.Cleanup(func() { DB, LOG_DB = previousDB, previousLogDB })

	require.NoError(t, migrateLogDB())
	assert.True(t, sink.Migrator().HasTable(&Log{}))
	assert.True(t, sink.Migrator().HasColumn(&Log{}, "AuditEventId"))
	assert.True(t, sink.Migrator().HasIndex(&Log{}, "ux_logs_audit_event_id"))
	require.NoError(t, migrateLogDB(), "separate log migrations must be idempotent")
}

func TestAuditLogOutboxPayloadAndLeaseTokenAreHiddenFromJSON(t *testing.T) {
	event := AuditLogOutbox{
		EventID: "event", Payload: "sensitive-payload", LeaseToken: "secret-fence",
		LastError: "storage internals",
	}
	encoded, err := jsonMarshalForTest(event)
	require.NoError(t, err)
	assert.NotContains(t, encoded, "sensitive-payload")
	assert.NotContains(t, encoded, "secret-fence")
	assert.NotContains(t, encoded, "storage internals")
}

func jsonMarshalForTest(value any) (string, error) {
	encoded, err := common.Marshal(value)
	return string(encoded), err
}
