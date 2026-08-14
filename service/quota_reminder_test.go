package service

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

type recordingMailer struct {
	to      []string
	subject []string
}

func (m *recordingMailer) Send(to, subject, body string) error {
	m.to = append(m.to, to)
	m.subject = append(m.subject, subject)
	return nil
}

func setupReminderTest(t *testing.T) (*gorm.DB, *recordingMailer) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "reminder.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	mock := &recordingMailer{}
	Mail = mock
	t.Cleanup(func() { Mail = &noopMailer{} })
	return db, mock
}

func TestQuotaReminderSendsBelowThreshold(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "1000"))
	user := model.User{Username: "lowquota", Password: "x", Status: model.UserStatusEnabled,
		Email: "low@example.com", EmailVerified: true, Quota: 500, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	CheckAndSendQuotaReminder(user.Id)
	require.Len(t, mock.to, 1)
	assert.Equal(t, "low@example.com", mock.to[0])
	assert.Contains(t, mock.subject[0], "额度提醒")

	// Cooldown: a second call within the hour does not resend.
	CheckAndSendQuotaReminder(user.Id)
	assert.Len(t, mock.to, 1)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.NotZero(t, got.QuotaReminderAt)
}

func TestQuotaReminderSkipsWhenNotNeeded(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "1000"))

	// Quota above threshold.
	rich := model.User{Username: "rich", Password: "x", Status: model.UserStatusEnabled,
		Email: "rich@example.com", EmailVerified: true, Quota: 5000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&rich).Error)
	CheckAndSendQuotaReminder(rich.Id)
	assert.Empty(t, mock.to)

	// Unverified email.
	unverified := model.User{Username: "unverified", Password: "x", Status: model.UserStatusEnabled,
		Email: "u@example.com", Quota: 100, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&unverified).Error)
	CheckAndSendQuotaReminder(unverified.Id)
	assert.Empty(t, mock.to)
}

func TestQuotaReminderDisabledByOption(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "0"))
	user := model.User{Username: "nothresh", Password: "x", Status: model.UserStatusEnabled,
		Email: "n@example.com", EmailVerified: true, Quota: 10, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	CheckAndSendQuotaReminder(user.Id)
	assert.Empty(t, mock.to)
}
