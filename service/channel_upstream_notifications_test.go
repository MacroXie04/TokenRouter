package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

type upstreamNotificationMailer struct {
	sync.Mutex
	to      []string
	subject []string
	body    []string
	err     error
}

func (m *upstreamNotificationMailer) Send(to, subject, body string) error {
	m.Lock()
	defer m.Unlock()
	m.to = append(m.to, to)
	m.subject = append(m.subject, subject)
	m.body = append(m.body, body)
	return m.err
}

func resetChannelUpstreamNotificationState() {
	channelUpstreamNotificationState.Lock()
	defer channelUpstreamNotificationState.Unlock()
	channelUpstreamNotificationState.lastNotifiedAt = 0
	channelUpstreamNotificationState.lastChangedChannels = 0
	channelUpstreamNotificationState.lastFailedChannels = 0
}

func TestChannelUpstreamNotificationOptInBoundsAndSuppression(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	resetChannelUpstreamNotificationState()
	t.Cleanup(resetChannelUpstreamNotificationState)
	previousMailer := Mail
	mailer := &upstreamNotificationMailer{}
	Mail = mailer
	t.Cleanup(func() { Mail = previousMailer })

	users := []model.User{
		{Username: "watcher", Password: "password8", Email: "watcher@example.com", EmailVerified: true,
			Role: constant.RoleAdminUser, Status: model.UserStatusEnabled, Setting: `{"upstream_model_update_notify_enabled":true}`},
		{Username: "ordinary", Password: "password8", Email: "ordinary@example.com", EmailVerified: true,
			Role: constant.RoleCommonUser, Status: model.UserStatusEnabled, Setting: `{"upstream_model_update_notify_enabled":true}`},
		{Username: "unverified", Password: "password8", Email: "unverified@example.com",
			Role: constant.RoleAdminUser, Status: model.UserStatusEnabled, Setting: `{"upstream_model_update_notify_enabled":true}`},
		{Username: "opted-out", Password: "password8", Email: "out@example.com", EmailVerified: true,
			Role: constant.RoleAdminUser, Status: model.UserStatusEnabled},
	}
	for i := range users {
		require.NoError(t, model.DB.Create(&users[i]).Error)
	}

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"old"},{"id":"new"}]}`))
	}))
	t.Cleanup(good.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)
	settings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{CheckEnabled: true}, nil)
	createChannelForUpstreamUpdate(t, good.URL, "notify-good", "old,removed", settings)
	createChannelForUpstreamUpdate(t, bad.URL, "notify-bad", "old", settings)

	summary, err := RunChannelUpstreamModelUpdateTask(t.Context(), true, false, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, summary.CheckedChannels)
	assert.Equal(t, 1, summary.ChangedChannels)
	assert.Equal(t, 1, summary.FailedChannels)
	require.Len(t, mailer.to, 1)
	assert.Equal(t, "watcher@example.com", mailer.to[0])
	assert.Equal(t, "上游模型巡检通知", mailer.subject[0])
	assert.Contains(t, mailer.body[0], "检测渠道 2 个")
	assert.Contains(t, mailer.body[0], "notify-good (+1 / -1)")
	assert.Contains(t, mailer.body[0], "新增模型示例：new")
	assert.Contains(t, mailer.body[0], "删除模型示例：removed")
	assert.NotContains(t, mailer.body[0], "secret-")

	// The same changed/failed counts are suppressed for 24 hours.
	_, err = RunChannelUpstreamModelUpdateTask(t.Context(), true, false, nil)
	require.NoError(t, err)
	assert.Len(t, mailer.to, 1)
}

func TestChannelUpstreamNotificationPreferenceIsAdminOnlyAndPreserved(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	user := model.User{Username: "notify-setting", Password: "password8", Status: model.UserStatusEnabled,
		Setting: `{"future_setting":{"keep":true}}`}
	require.NoError(t, model.DB.Create(&user).Error)
	enabled := true

	require.NoError(t, UpdateUserSettingWithUpstreamNotification(user.Id, constant.RoleCommonUser, 100, "email", &enabled))
	settings, err := loadUserSettings(user.Id)
	require.NoError(t, err)
	assert.False(t, settings.UpstreamModelUpdateNotifyEnabled)

	// Both the authenticated role and the current stored role must be
	// privileged, so a forged/stale caller role cannot opt in a common user.
	require.NoError(t, UpdateUserSettingWithUpstreamNotification(user.Id, constant.RoleAdminUser, 200, "email", &enabled))
	settings, err = loadUserSettings(user.Id)
	require.NoError(t, err)
	assert.False(t, settings.UpstreamModelUpdateNotifyEnabled)
	require.NoError(t, model.DB.Model(&user).Updates(map[string]any{
		"role": constant.RoleAdminUser, "email": "failure@example.com", "email_verified": true,
	}).Error)
	require.NoError(t, UpdateUserSettingWithUpstreamNotification(user.Id, constant.RoleAdminUser, 200, "email", &enabled))
	settings, err = loadUserSettings(user.Id)
	require.NoError(t, err)
	assert.True(t, settings.UpstreamModelUpdateNotifyEnabled)
	assert.Equal(t, 200, settings.QuotaWarningThreshold)

	var reloaded model.User
	require.NoError(t, model.DB.First(&reloaded, user.Id).Error)
	assert.Contains(t, reloaded.Setting, "future_setting", "independent settings must survive read-modify-write")

	mailer := &upstreamNotificationMailer{err: errors.New("mail unavailable")}
	previousMailer := Mail
	Mail = mailer
	t.Cleanup(func() { Mail = previousMailer })
	resetChannelUpstreamNotificationState()
	// Delivery failures are intentionally isolated from the maintenance task.
	assert.NotPanics(t, func() {
		notifyChannelUpstreamWatchers(channelUpstreamNotificationSummary{ChangedChannels: 1})
	})
	assert.Equal(t, []string{"failure@example.com"}, mailer.to)
}

func TestChannelUpstreamNotificationContentIsBoundedAndSanitized(t *testing.T) {
	channels := make([]channelUpstreamNotificationChannel, 0, 20)
	models := make([]string, 0, 20)
	ids := make([]int, 0, 20)
	for i := 0; i < 20; i++ {
		channels = append(channels, channelUpstreamNotificationChannel{
			Name: fmt.Sprintf("channel-%02d\n%s", i, strings.Repeat("界", 200)), AddCount: 1,
		})
		models = append(models, fmt.Sprintf("model-%02d", i))
		ids = append(ids, i+1)
	}
	content := buildChannelUpstreamNotificationContent(channelUpstreamNotificationSummary{
		ChangedChannels: 20, DetectedAddModels: 20, FailedChannels: 20,
		Channels: channels, AddModelSamples: models, FailedChannelIDs: ids,
	})
	assert.NotContains(t, content, "channel-00\n")
	assert.Contains(t, content, "其余 12 个变更渠道已省略")
	assert.Contains(t, content, "仅展示最多 12 个不同模型")
	assert.Contains(t, content, "其余 10 个已省略")
	assert.NotContains(t, content, "channel-08")
	assert.NotContains(t, content, "model-12")
}

func TestConcurrentUserSettingUpdatesPreserveIndependentFields(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	user := model.User{Username: "concurrent-settings", Password: "password8", Status: model.UserStatusEnabled,
		Role: constant.RoleAdminUser, Setting: `{"future_setting":{"keep":true}}`}
	require.NoError(t, model.DB.Create(&user).Error)
	enabled := true

	start := make(chan struct{})
	errors := make(chan error, 2)
	go func() {
		<-start
		_, err := UpdateUserBillingPreference(user.Id, BillingPreferenceWalletOnly)
		errors <- err
	}()
	go func() {
		<-start
		errors <- UpdateUserSettingWithUpstreamNotification(user.Id, constant.RoleAdminUser, 321, "email", &enabled)
	}()
	close(start)
	require.NoError(t, <-errors)
	require.NoError(t, <-errors)

	settings, err := loadUserSettings(user.Id)
	require.NoError(t, err)
	assert.Equal(t, BillingPreferenceWalletOnly, settings.BillingPreference)
	assert.Equal(t, 321, settings.QuotaWarningThreshold)
	assert.True(t, settings.UpstreamModelUpdateNotifyEnabled)
	var reloaded model.User
	require.NoError(t, model.DB.First(&reloaded, user.Id).Error)
	assert.Contains(t, reloaded.Setting, "future_setting")
}
