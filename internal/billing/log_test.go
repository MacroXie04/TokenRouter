package billing

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestRecordSystemLogCheckedPersistsAuditRecord(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previous := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = previous })

	require.NoError(t, RecordSystemLogChecked(42, LogTypeManage, "security.change"))
	var got model.Log
	require.NoError(t, db.First(&got).Error)
	require.Equal(t, 42, got.UserId)
	require.Equal(t, LogTypeManage, got.Type)
	require.Equal(t, "security.change", got.Content)
}

func TestRecordConsumeLogCheckedNormalizesProviderControlledRequestID(t *testing.T) {
	ResetQuotaDataCache()
	t.Cleanup(ResetQuotaDataCache)
	t.Setenv("LOG_SQL_DSN", "")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
	})

	secret := "sk-test-super-secret-provider-request-id"
	require.NoError(t, RecordConsumeLogChecked(
		1, "user", "token", "model", 1, 1, 2, 3, false,
		0, "default", "127.0.0.1", "request-id", secret, 0, nil,
	))
	var got model.Log
	require.NoError(t, db.First(&got).Error)
	assert.True(t, strings.HasPrefix(got.UpstreamRequestId, "sha256:"))
	assert.NotContains(t, got.UpstreamRequestId, secret)

	require.NoError(t, RecordConsumeLogChecked(
		1, "user", "token", "model", 1, 1, 2, 3, false,
		0, "default", "127.0.0.1", "request-id", "req_safe-123", 0, nil,
	))
	got = model.Log{}
	require.NoError(t, db.Order("id desc").First(&got).Error)
	assert.Equal(t, requestctx.NormalizeProviderCorrelationID("req_safe-123"), got.UpstreamRequestId)

	logs, total, err := GetAllLogs(
		LogTypeUnknown, 0, 0, "", "", "", 0, 10, 0, "", "", "req_safe-123",
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, requestctx.NormalizeProviderCorrelationID("req_safe-123"), logs[0].UpstreamRequestId)

	require.NoError(t, db.Create(&model.Log{
		UserId: 1, Type: LogTypeConsume, UpstreamRequestId: "req_safe-123", CreatedAt: 1,
	}).Error)
	_, total, err = GetAllLogs(
		LogTypeUnknown, 0, 0, "", "", "", 0, 10, 0, "", "", "req_safe-123",
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "search must cover both legacy raw and fingerprinted rows")
}

func TestCheckedLogWritesFailClosedWithoutDatabase(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = nil, nil
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	require.Error(t, RecordSystemLogChecked(1, LogTypeManage, "security.change"))
	require.Error(t, RecordTopupLog(1, 10, 1.5, "trade"))
}

func TestRecordConsumeLogCheckedRetainsIPOnlyWithExplicitUserOptIn(t *testing.T) {
	ResetQuotaDataCache()
	t.Cleanup(ResetQuotaDataCache)
	t.Setenv("LOG_SQL_DSN", "")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	users := []model.User{
		{Username: "ip-private", Password: "password", Status: model.UserStatusEnabled},
		{Username: "ip-enabled", Password: "password", Status: model.UserStatusEnabled, Setting: `{"record_ip_log":true}`},
	}
	for i := range users {
		require.NoError(t, db.Create(&users[i]).Error)
		require.NoError(t, RecordConsumeLogChecked(
			users[i].Id, users[i].Username, "token", "model", 1, 1, 2, 3, false,
			0, "default", "203.0.113.42", "request-id", "upstream-id", 0, nil,
		))
	}

	var logs []model.Log
	require.NoError(t, db.Order("id asc").Find(&logs).Error)
	require.Len(t, logs, 2)
	assert.Empty(t, logs[0].Ip)
	assert.Equal(t, "203.0.113.42", logs[1].Ip)
}
