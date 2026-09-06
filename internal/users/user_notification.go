package users

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	NotifyTypeEmail   = "email"
	NotifyTypeWebhook = "webhook"
	NotifyTypeBark    = "bark"
	NotifyTypeGotify  = "gotify"

	maxNotificationEmailBytes    = 254
	maxNotificationURLBytes      = 2048
	maxNotificationSecretBytes   = 4096
	maxNotificationTokenBytes    = 2048
	maxNotificationTypeBytes     = 64
	maxNotificationTitleBytes    = 256
	maxNotificationContentBytes  = 64 << 10
	maxNotificationRequestBytes  = 128 << 10
	maxNotificationResponseBytes = int64(64 << 10)
	userNotificationTimeout      = 10 * time.Second
)

var notificationTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// UserNotificationSettingsInput is the complete reference-compatible
// preference form. Credential fields are stored for the authenticated owner
// but are never returned by the self-profile response.
type UserNotificationSettingsInput struct {
	NotifyType                       string
	QuotaWarningThreshold            int
	WebhookURL                       string
	WebhookSecret                    string
	NotificationEmail                string
	BarkURL                          string
	GotifyURL                        string
	GotifyToken                      string
	GotifyPriority                   int
	AcceptUnsetRatioModel            bool
	RecordIPLog                      bool
	UpstreamModelUpdateNotifyEnabled *bool
}

// UserNotification is the bounded, provider-neutral message sent by quota,
// channel-health, channel-test, and upstream-model workflows.
type UserNotification struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type webhookNotificationPayload struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

var defaultUserNotificationHTTPClient = &http.Client{
	Timeout: userNotificationTimeout,
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           httpx.SafeDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var userNotificationClient = struct {
	sync.RWMutex
	client *http.Client
}{client: defaultUserNotificationHTTPClient}

// SetUserNotificationHTTPClientForTesting installs a deterministic transport
// for local contract tests. Production always uses the direct SSRF-safe
// client above.
func SetUserNotificationHTTPClientForTesting(client *http.Client) (restore func()) {
	if client == nil {
		client = defaultUserNotificationHTTPClient
	}
	userNotificationClient.Lock()
	previous := userNotificationClient.client
	userNotificationClient.client = client
	userNotificationClient.Unlock()
	return func() {
		userNotificationClient.Lock()
		userNotificationClient.client = previous
		userNotificationClient.Unlock()
	}
}

func currentUserNotificationHTTPClient() *http.Client {
	userNotificationClient.RLock()
	defer userNotificationClient.RUnlock()
	return userNotificationClient.client
}

// UpdateUserNotificationSettings validates and persists the entire user
// notification/privacy form without clobbering unrelated billing or layout
// preferences. Empty secret fields preserve an existing secret only when the
// endpoint is unchanged, preventing accidental credential reuse for a new
// destination.
func UpdateUserNotificationSettings(userID, callerRole int, input UserNotificationSettingsInput) error {
	if input.QuotaWarningThreshold <= 0 || !quotamath.QuotaWithinBounds(input.QuotaWarningThreshold) {
		return errors.New("quota warning threshold is invalid")
	}
	if input.NotifyType != strings.TrimSpace(input.NotifyType) {
		return errors.New("notification type is invalid")
	}
	switch input.NotifyType {
	case NotifyTypeEmail, NotifyTypeWebhook, NotifyTypeBark, NotifyTypeGotify:
	default:
		return errors.New("notification type is invalid")
	}

	email, err := normalizeNotificationEmail(input.NotificationEmail)
	if err != nil {
		return err
	}
	webhookURL, err := normalizeNotificationURL(input.WebhookURL, true, false)
	if err != nil {
		return fmt.Errorf("webhook URL is invalid: %w", err)
	}
	barkURL, err := normalizeBarkURL(input.BarkURL)
	if err != nil {
		return fmt.Errorf("Bark URL is invalid: %w", err)
	}
	gotifyURL, err := normalizeNotificationURL(input.GotifyURL, false, true)
	if err != nil {
		return fmt.Errorf("Gotify URL is invalid: %w", err)
	}
	if !validNotificationText(input.WebhookSecret, maxNotificationSecretBytes, true, false) {
		return errors.New("webhook secret is invalid")
	}
	if !validNotificationText(input.GotifyToken, maxNotificationTokenBytes, true, false) {
		return errors.New("Gotify token is invalid")
	}
	if input.NotifyType == NotifyTypeWebhook && webhookURL == "" {
		return errors.New("webhook URL is required")
	}
	if input.NotifyType == NotifyTypeBark && barkURL == "" {
		return errors.New("Bark URL is required")
	}
	if input.NotifyType == NotifyTypeGotify && gotifyURL == "" {
		return errors.New("Gotify URL is required")
	}
	priority := input.GotifyPriority
	if priority < 0 || priority > 10 {
		priority = 5
	}

	return updateUserSettingsChecked(userID, func(settings *UserSettings, storedRole int) error {
		webhookSecret := input.WebhookSecret
		if webhookSecret == "" && webhookURL != "" && webhookURL == settings.WebhookURL {
			webhookSecret = settings.WebhookSecret
		}
		gotifyToken := input.GotifyToken
		if gotifyToken == "" && gotifyURL != "" && gotifyURL == settings.GotifyURL {
			gotifyToken = settings.GotifyToken
		}
		if input.NotifyType == NotifyTypeGotify && gotifyToken == "" {
			return errors.New("Gotify token is required")
		}

		settings.QuotaWarningThreshold = input.QuotaWarningThreshold
		settings.QuotaWarningType = input.NotifyType
		settings.NotificationEmail = email
		settings.WebhookURL = webhookURL
		settings.WebhookSecret = webhookSecret
		settings.BarkURL = barkURL
		settings.GotifyURL = gotifyURL
		settings.GotifyToken = gotifyToken
		settings.GotifyPriority = priority
		settings.AcceptUnsetRatioModel = input.AcceptUnsetRatioModel
		settings.RecordIPLog = input.RecordIPLog
		if callerRole >= roles.RoleAdminUser && storedRole >= roles.RoleAdminUser && input.UpstreamModelUpdateNotifyEnabled != nil {
			settings.UpstreamModelUpdateNotifyEnabled = *input.UpstreamModelUpdateNotifyEnabled
		}
		return nil
	})
}

func normalizeNotificationEmail(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw != strings.TrimSpace(raw) || !validNotificationText(raw, maxNotificationEmailBytes, false, false) {
		return "", errors.New("notification email is invalid")
	}
	address, err := mail.ParseAddress(raw)
	if err != nil || address.Address != raw || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("notification email is invalid")
	}
	return raw, nil
}

func normalizeNotificationURL(raw string, allowQuery, requireCleanBase bool) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw != strings.TrimSpace(raw) || strings.Contains(raw, `\`) ||
		!validNotificationText(raw, maxNotificationURLBytes, false, false) {
		return "", errors.New("unsafe URL text")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" {
		return "", errors.New("absolute URL required")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("HTTP(S) URL required")
	}
	if parsed.Scheme == "http" && !isLoopbackNotificationHost(parsed.Hostname()) {
		return "", errors.New("HTTPS required for non-loopback host")
	}
	if !allowQuery && parsed.RawQuery != "" {
		return "", errors.New("query string is not allowed")
	}
	if requireCleanBase && (parsed.RawQuery != "" || strings.HasSuffix(parsed.Path, "/message")) {
		return "", errors.New("clean server base URL required")
	}
	return parsed.String(), nil
}

func normalizeBarkURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	withoutTemplates := strings.ReplaceAll(strings.ReplaceAll(raw, "{{title}}", "title"), "{{content}}", "content")
	if strings.ContainsAny(withoutTemplates, "{}") {
		return "", errors.New("unknown template variable")
	}
	if _, err := normalizeNotificationURL(withoutTemplates, true, false); err != nil {
		return "", err
	}
	if len(raw) > maxNotificationURLBytes || !utf8.ValidString(raw) {
		return "", errors.New("URL is too long")
	}
	return raw, nil
}

func isLoopbackNotificationHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func validNotificationText(value string, maximumBytes int, allowEmpty, allowNewlines bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if allowNewlines && (character == '\n' || character == '\r' || character == '\t') {
			continue
		}
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

// SendUserNotification delivers one already-rate-limited notification using
// the owner's selected channel. It never follows redirects or delegates to a
// proxy, and every direct connection is pinned to a validated public IP.
func SendUserNotification(userID int, accountEmail string, accountEmailVerified bool, settings UserSettings, notification UserNotification) error {
	if userID <= 0 || !notificationTypePattern.MatchString(notification.Type) ||
		!validNotificationText(notification.Title, maxNotificationTitleBytes, false, false) ||
		!validNotificationText(notification.Content, maxNotificationContentBytes, false, true) {
		return errors.New("notification payload is invalid")
	}
	notifyType := settings.QuotaWarningType
	if notifyType == "" {
		notifyType = NotifyTypeEmail
	}
	switch notifyType {
	case NotifyTypeEmail:
		destination := settings.NotificationEmail
		if destination == "" {
			if !accountEmailVerified {
				return errors.New("verified notification email is unavailable")
			}
			destination = accountEmail
		}
		if _, err := normalizeNotificationEmail(destination); err != nil {
			return errors.New("notification email is invalid")
		}
		return mailtransport.Mail.Send(destination, notification.Title, notification.Content)
	case NotifyTypeWebhook:
		return sendWebhookUserNotification(settings, notification)
	case NotifyTypeBark:
		return sendBarkUserNotification(settings, notification)
	case NotifyTypeGotify:
		return sendGotifyUserNotification(settings, notification)
	default:
		return errors.New("notification type is invalid")
	}
}

// NotifyRootUser delivers an operator notification to the first enabled root
// account. Callers decide whether delivery failure is fatal; background
// maintenance generally logs and continues after its durable work succeeds.
func NotifyRootUser(notification UserNotification) error {
	var user model.User
	result := model.DB.Select("id", "email", "email_verified", "setting").
		Where("status = ? AND role = ?", model.UserStatusEnabled, roles.RoleRootUser).
		Order("id ASC").Limit(1).Find(&user)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.New("enabled root notification recipient is unavailable")
	}
	return SendUserNotification(user.Id, user.Email, user.EmailVerified,
		UserSettingsFromRaw(user.Setting), notification)
}

func sendWebhookUserNotification(settings UserSettings, notification UserNotification) error {
	destination, err := normalizeNotificationURL(settings.WebhookURL, true, false)
	if err != nil || destination == "" {
		return errors.New("webhook URL is invalid")
	}
	payload, err := jsonutil.Marshal(webhookNotificationPayload{
		Type: notification.Type, Title: notification.Title, Content: notification.Content,
		Timestamp: wallclock.NowTimestamp(),
	})
	if err != nil || len(payload) > maxNotificationRequestBytes {
		return errors.New("webhook payload is invalid")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if settings.WebhookSecret != "" {
		if !validNotificationText(settings.WebhookSecret, maxNotificationSecretBytes, false, false) {
			return errors.New("webhook secret is invalid")
		}
		mac := hmac.New(sha256.New, []byte(settings.WebhookSecret))
		_, _ = mac.Write(payload)
		headers.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	return performUserNotificationRequest(http.MethodPost, destination, headers, payload)
}

func sendBarkUserNotification(settings UserSettings, notification UserNotification) error {
	template, err := normalizeBarkURL(settings.BarkURL)
	if err != nil || template == "" {
		return errors.New("Bark URL is invalid")
	}
	destination := strings.ReplaceAll(template, "{{title}}", url.QueryEscape(notification.Title))
	destination = strings.ReplaceAll(destination, "{{content}}", url.QueryEscape(notification.Content))
	if len(destination) > maxNotificationRequestBytes {
		return errors.New("Bark URL is too long")
	}
	if _, err := normalizeNotificationURL(destination, true, false); err != nil {
		return errors.New("Bark URL is invalid")
	}
	headers := make(http.Header)
	headers.Set("User-Agent", "TokenRouter-Bark-Notify/1.0")
	return performUserNotificationRequest(http.MethodGet, destination, headers, nil)
}

func sendGotifyUserNotification(settings UserSettings, notification UserNotification) error {
	base, err := normalizeNotificationURL(settings.GotifyURL, false, true)
	if err != nil || base == "" || !validNotificationText(settings.GotifyToken, maxNotificationTokenBytes, false, false) {
		return errors.New("Gotify configuration is invalid")
	}
	priority := settings.GotifyPriority
	if priority < 0 || priority > 10 {
		priority = 5
	}
	payload, err := jsonutil.Marshal(struct {
		Title    string `json:"title"`
		Message  string `json:"message"`
		Priority int    `json:"priority"`
	}{notification.Title, notification.Content, priority})
	if err != nil || len(payload) > maxNotificationRequestBytes {
		return errors.New("Gotify payload is invalid")
	}
	destination := strings.TrimRight(base, "/") + "/message?" + url.Values{"token": []string{settings.GotifyToken}}.Encode()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json; charset=utf-8")
	headers.Set("User-Agent", "TokenRouter-Gotify-Notify/1.0")
	return performUserNotificationRequest(http.MethodPost, destination, headers, payload)
}

func performUserNotificationRequest(method, destination string, headers http.Header, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), userNotificationTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, destination, bytes.NewReader(payload))
	if err != nil {
		return errors.New("notification request is invalid")
	}
	request.Header = headers.Clone()
	response, err := currentUserNotificationHTTPClient().Do(request)
	if err != nil {
		return errors.New("notification delivery failed")
	}
	defer response.Body.Close()
	if _, err := httpx.ReadAllLimited(response.Body, maxNotificationResponseBytes); err != nil {
		return errors.New("notification response is invalid")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("notification endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}
