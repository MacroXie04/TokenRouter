package users

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strings"
	"testing"
)

type userNotificationRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn userNotificationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func setupUserNotificationTest(t *testing.T, role int, settingJSON string) *model.User {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/notification.db?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	user := &model.User{
		Username: "notification-user", Password: "password", Status: model.UserStatusEnabled,
		Role: role, Email: "account@example.test", EmailVerified: true, Setting: settingJSON,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func TestUpdateUserNotificationSettingsPreservesIndependentDataAndWriteOnlySecrets(t *testing.T) {
	user := setupUserNotificationTest(t, roles.RoleAdminUser,
		`{"billing_preference":"wallet_only","language":"fr","future":{"keep":true}}`)
	enabled := true
	require.NoError(t, UpdateUserNotificationSettings(user.Id, roles.RoleAdminUser, UserNotificationSettingsInput{
		NotifyType: NotifyTypeWebhook, QuotaWarningThreshold: 125,
		WebhookURL: "https://hooks.example.test/quota", WebhookSecret: "hook-secret",
		NotificationEmail: "notify@example.test", GotifyPriority: 3,
		AcceptUnsetRatioModel: true, RecordIPLog: true,
		UpstreamModelUpdateNotifyEnabled: &enabled,
	}))

	settings, err := LoadUserSettings(user.Id)
	require.NoError(t, err)
	assert.Equal(t, NotifyTypeWebhook, settings.QuotaWarningType)
	assert.Equal(t, 125, settings.QuotaWarningThreshold)
	assert.Equal(t, "https://hooks.example.test/quota", settings.WebhookURL)
	assert.Equal(t, "hook-secret", settings.WebhookSecret)
	assert.Equal(t, "notify@example.test", settings.NotificationEmail)
	assert.True(t, settings.AcceptUnsetRatioModel)
	assert.True(t, settings.RecordIPLog)
	assert.True(t, settings.UpstreamModelUpdateNotifyEnabled)
	assert.Equal(t, BillingPreferenceWalletOnly, settings.BillingPreference)
	assert.Equal(t, "fr", settings.Language)

	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Contains(t, stored.Setting, `"future":{"keep":true}`)
	visible := SafeUserSettingsJSON(stored.Setting)
	assert.NotContains(t, visible, "hook-secret")
	var public map[string]any
	require.NoError(t, json.Unmarshal([]byte(visible), &public))
	assert.Equal(t, true, public["webhook_secret_configured"])
	assert.Equal(t, false, public["gotify_token_configured"])

	// An empty secret retains the credential for the same endpoint, but never
	// carries it across to a different destination.
	require.NoError(t, UpdateUserNotificationSettings(user.Id, roles.RoleAdminUser, UserNotificationSettingsInput{
		NotifyType: NotifyTypeWebhook, QuotaWarningThreshold: 130,
		WebhookURL: "https://hooks.example.test/quota", GotifyPriority: 5,
	}))
	settings, err = LoadUserSettings(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "hook-secret", settings.WebhookSecret)
	require.NoError(t, UpdateUserNotificationSettings(user.Id, roles.RoleAdminUser, UserNotificationSettingsInput{
		NotifyType: NotifyTypeWebhook, QuotaWarningThreshold: 131,
		WebhookURL: "https://other.example.test/quota", GotifyPriority: 5,
	}))
	settings, err = LoadUserSettings(user.Id)
	require.NoError(t, err)
	assert.Empty(t, settings.WebhookSecret)

	// Gotify credentials are mandatory for a new endpoint and priority follows
	// the reference's defensive fallback to five.
	err = UpdateUserNotificationSettings(user.Id, roles.RoleAdminUser, UserNotificationSettingsInput{
		NotifyType: NotifyTypeGotify, QuotaWarningThreshold: 132,
		GotifyURL: "https://gotify.example.test", GotifyPriority: 99,
	})
	require.Error(t, err)
	require.NoError(t, UpdateUserNotificationSettings(user.Id, roles.RoleAdminUser, UserNotificationSettingsInput{
		NotifyType: NotifyTypeGotify, QuotaWarningThreshold: 132,
		GotifyURL: "https://gotify.example.test", GotifyToken: "gotify-token", GotifyPriority: 99,
	}))
	settings, err = LoadUserSettings(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 5, settings.GotifyPriority)
	assert.Equal(t, "gotify-token", settings.GotifyToken)
}

func TestUpdateUserNotificationSettingsRejectsUnsafeDestinationsAtomically(t *testing.T) {
	user := setupUserNotificationTest(t, roles.RoleCommonUser,
		`{"notify_type":"email","quota_warning_threshold":100,"upstream_model_update_notify_enabled":true}`)
	tests := []UserNotificationSettingsInput{
		{NotifyType: "sms", QuotaWarningThreshold: 10},
		{NotifyType: NotifyTypeEmail, QuotaWarningThreshold: 0},
		{NotifyType: NotifyTypeEmail, QuotaWarningThreshold: 10, NotificationEmail: "Display <mail@example.test>"},
		{NotifyType: NotifyTypeWebhook, QuotaWarningThreshold: 10, WebhookURL: "http://public.example.test/hook"},
		{NotifyType: NotifyTypeWebhook, QuotaWarningThreshold: 10, WebhookURL: "https://user:pass@example.test/hook"},
		{NotifyType: NotifyTypeWebhook, QuotaWarningThreshold: 10, WebhookURL: "javascript:alert(1)"},
		{NotifyType: NotifyTypeBark, QuotaWarningThreshold: 10, BarkURL: "https://api.example.test/{unknown}"},
		{NotifyType: NotifyTypeGotify, QuotaWarningThreshold: 10, GotifyURL: "https://gotify.example.test?tenant=x", GotifyToken: "token"},
		{NotifyType: NotifyTypeGotify, QuotaWarningThreshold: 10, GotifyURL: "https://gotify.example.test", GotifyToken: "bad\ntoken"},
	}
	for _, input := range tests {
		require.Error(t, UpdateUserNotificationSettings(user.Id, roles.RoleCommonUser, input), "%+v", input)
	}

	settings, err := LoadUserSettings(user.Id)
	require.NoError(t, err)
	assert.Equal(t, NotifyTypeEmail, settings.QuotaWarningType)
	assert.Equal(t, 100, settings.QuotaWarningThreshold)
	assert.True(t, settings.UpstreamModelUpdateNotifyEnabled)

	// A common caller cannot change the administrator-only watcher bit even
	// when the stored account was previously opted in.
	disabled := false
	require.NoError(t, UpdateUserNotificationSettings(user.Id, roles.RoleAdminUser, UserNotificationSettingsInput{
		NotifyType: NotifyTypeEmail, QuotaWarningThreshold: 101,
		UpstreamModelUpdateNotifyEnabled: &disabled,
	}))
	settings, err = LoadUserSettings(user.Id)
	require.NoError(t, err)
	assert.True(t, settings.UpstreamModelUpdateNotifyEnabled)
}

func TestUserNotificationHTTPChannelWireContracts(t *testing.T) {
	requests := make([]*http.Request, 0, 3)
	bodies := make([][]byte, 0, 3)
	restore := SetUserNotificationHTTPClientForTesting(&http.Client{Transport: userNotificationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		requests = append(requests, request.Clone(request.Context()))
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	t.Cleanup(restore)
	notification := UserNotification{Type: "quota_exceed", Title: "Low quota", Content: "Only 10 units remain"}

	webhook := UserSettings{QuotaWarningType: NotifyTypeWebhook, WebhookURL: "https://hooks.example.test/quota", WebhookSecret: "signing-secret"}
	require.NoError(t, SendUserNotification(1, "", false, webhook, notification))
	require.Len(t, requests, 1)
	assert.Equal(t, http.MethodPost, requests[0].Method)
	assert.Equal(t, "https://hooks.example.test/quota", requests[0].URL.String())
	assert.Equal(t, "application/json", requests[0].Header.Get("Content-Type"))
	assert.Empty(t, requests[0].Header.Get("Authorization"))
	mac := hmac.New(sha256.New, []byte("signing-secret"))
	_, _ = mac.Write(bodies[0])
	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), requests[0].Header.Get("X-Webhook-Signature"))
	var webhookBody webhookNotificationPayload
	require.NoError(t, json.Unmarshal(bodies[0], &webhookBody))
	assert.Equal(t, notification.Type, webhookBody.Type)
	assert.Equal(t, notification.Title, webhookBody.Title)
	assert.Equal(t, notification.Content, webhookBody.Content)
	assert.Positive(t, webhookBody.Timestamp)

	bark := UserSettings{QuotaWarningType: NotifyTypeBark, BarkURL: "https://bark.example.test/key/{{title}}/{{content}}"}
	require.NoError(t, SendUserNotification(1, "", false, bark, notification))
	assert.Equal(t, http.MethodGet, requests[1].Method)
	assert.Contains(t, requests[1].URL.String(), "Low+quota/Only+10+units+remain")
	assert.Equal(t, "TokenRouter-Bark-Notify/1.0", requests[1].Header.Get("User-Agent"))

	gotify := UserSettings{QuotaWarningType: NotifyTypeGotify, GotifyURL: "https://gotify.example.test/base/", GotifyToken: "token+/=", GotifyPriority: 8}
	require.NoError(t, SendUserNotification(1, "", false, gotify, notification))
	assert.Equal(t, http.MethodPost, requests[2].Method)
	assert.Equal(t, "/base/message", requests[2].URL.Path)
	assert.Equal(t, "token+/=", requests[2].URL.Query().Get("token"))
	var gotifyBody map[string]any
	require.NoError(t, json.Unmarshal(bodies[2], &gotifyBody))
	assert.Equal(t, float64(8), gotifyBody["priority"])
	assert.Equal(t, notification.Content, gotifyBody["message"])
}

func TestUserNotificationEmailAndResponseBoundsFailClosed(t *testing.T) {
	mailer := &testutil.RecordingMailer{}
	previousMailer := mailtransport.Mail
	mailtransport.Mail = mailer
	t.Cleanup(func() { mailtransport.Mail = previousMailer })
	notification := UserNotification{Type: "quota_exceed", Title: "Low", Content: "Body"}

	err := SendUserNotification(1, "unverified@example.test", false, UserSettings{}, notification)
	require.Error(t, err)
	assert.Empty(t, mailer.To)
	require.NoError(t, SendUserNotification(1, "account@example.test", true, UserSettings{}, notification))
	require.NoError(t, SendUserNotification(1, "", false, UserSettings{
		NotificationEmail: "configured@example.test",
	}, notification))
	assert.Equal(t, []string{"account@example.test", "configured@example.test"}, mailer.To)

	restore := SetUserNotificationHTTPClientForTesting(&http.Client{Transport: userNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxNotificationResponseBytes)+1))), Header: make(http.Header)}, nil
	})})
	t.Cleanup(restore)
	err = SendUserNotification(1, "", false, UserSettings{
		QuotaWarningType: NotifyTypeWebhook, WebhookURL: "https://hooks.example.test/quota",
	}, notification)
	assert.Error(t, err)
}
