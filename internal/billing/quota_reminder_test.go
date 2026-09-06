package billing

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"path/filepath"
	"sync"
	"testing"
)

func setupReminderTest(t *testing.T) (*gorm.DB, *testutil.RecordingMailer) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "reminder.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}, &model.UserSubscription{}))
	model.DB = db
	require.NoError(t, setting.Init())
	mock := &testutil.RecordingMailer{}
	previousMailer := mailtransport.Mail
	mailtransport.Mail = mock
	t.Cleanup(func() { mailtransport.Mail = previousMailer })
	return db, mock
}

func TestQuotaReminderSendsBelowThreshold(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "1000"))
	user := model.User{Username: "lowquota", Password: "x", Status: model.UserStatusEnabled,
		Email: "low@example.com", EmailVerified: true, Quota: 500, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	CheckAndSendQuotaReminder(user.Id)
	require.Len(t, mock.To, 1)
	assert.Equal(t, "low@example.com", mock.To[0])
	assert.Contains(t, mock.Subject[0], "额度提醒")

	// Cooldown: a second call within the hour does not resend.
	CheckAndSendQuotaReminder(user.Id)
	assert.Len(t, mock.To, 1)
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
	assert.Empty(t, mock.To)

	// Unverified email.
	unverified := model.User{Username: "unverified", Password: "x", Status: model.UserStatusEnabled,
		Email: "u@example.com", Quota: 100, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&unverified).Error)
	CheckAndSendQuotaReminder(unverified.Id)
	assert.Empty(t, mock.To)
}

func TestQuotaReminderDisabledByOption(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "0"))
	user := model.User{Username: "nothresh", Password: "x", Status: model.UserStatusEnabled,
		Email: "n@example.com", EmailVerified: true, Quota: 10, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	CheckAndSendQuotaReminder(user.Id)
	assert.Empty(t, mock.To)
}

func TestQuotaReminderConcurrentCallsReserveSingleDelivery(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "1000"))
	user := model.User{Username: "concurrent-reminder", Password: "password", Status: model.UserStatusEnabled,
		Email: "concurrent@example.com", EmailVerified: true, Quota: 10, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			CheckAndSendQuotaReminder(user.Id)
		}()
	}
	close(start)
	wait.Wait()

	mock.Lock()
	defer mock.Unlock()
	assert.Len(t, mock.To, 1)
}

func TestQuotaReminderFailedDeliveryReleasesReservationForRetry(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "1000"))
	user := model.User{Username: "retry-reminder", Password: "password", Status: model.UserStatusEnabled,
		Email: "retry@example.com", EmailVerified: true, Quota: 10, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	mock.Err = errors.New("mail unavailable")

	CheckAndSendQuotaReminder(user.Id)
	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Zero(t, stored.QuotaReminderAt)

	mock.Lock()
	mock.Err = nil
	mock.Unlock()
	CheckAndSendQuotaReminder(user.Id)
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.NotZero(t, stored.QuotaReminderAt)
	mock.Lock()
	defer mock.Unlock()
	assert.Len(t, mock.To, 2)
}

func TestQuotaReminderUsesLiveSettledSubscriptionBalance(t *testing.T) {
	_, mock := setupReminderTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaRemindThresholdOption, "100"))
	user := model.User{Username: "subscription-reminder", Password: "password", Status: model.UserStatusEnabled,
		Email: "subscription@example.com", EmailVerified: true, Quota: 10_000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, AmountTotal: 1_000, AmountUsed: 950,
		Status: SubscriptionStatusActive, StartTime: 1, EndTime: 4_102_444_800,
	}
	require.NoError(t, model.DB.Create(&subscription).Error)
	reservation := &RelayQuotaReservation{
		settled: true,
		funding: &FundingSession{source: BillingSourceSubscription, subscriptionId: subscription.Id},
	}

	CheckAndSendQuotaReminderForReservation(user.Id, reservation)
	require.Len(t, mock.To, 1)
	assert.Contains(t, mock.Subject[0], "订阅额度提醒")

	// A quota reset after settlement wins over any stale in-memory funding
	// snapshot, so no obsolete low-balance warning is sent.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).
		Update("quota_reminder_at", 0).Error)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", subscription.Id).
		Update("amount_used", 100).Error)
	CheckAndSendQuotaReminderForReservation(user.Id, reservation)
	assert.Len(t, mock.To, 1)
}
