package channels

import (
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	channelUpstreamNotifySuppressSeconds = int64(24 * 60 * 60)
	channelUpstreamNotifyMaxChannels     = 8
	channelUpstreamNotifyMaxModels       = 12
	channelUpstreamNotifyMaxFailures     = 10
	channelUpstreamNotifyMaxLabelBytes   = 256
)

type channelUpstreamNotificationSummary struct {
	CheckedChannels      int
	ChangedChannels      int
	DetectedAddModels    int
	DetectedRemoveModels int
	AutoAddedModels      int
	FailedChannels       int
	FailedChannelIDs     []int
	Channels             []channelUpstreamNotificationChannel
	AddModelSamples      []string
	RemoveModelSamples   []string
}

type channelUpstreamNotificationChannel struct {
	Name        string
	AddCount    int
	RemoveCount int
}

var channelUpstreamNotificationState = struct {
	sync.Mutex
	lastNotifiedAt      int64
	lastChangedChannels int
	lastFailedChannels  int
}{}

func shouldSendChannelUpstreamNotification(now int64, changedChannels, failedChannels int) bool {
	channelUpstreamNotificationState.Lock()
	defer channelUpstreamNotificationState.Unlock()
	if channelUpstreamNotificationState.lastNotifiedAt > 0 &&
		now-channelUpstreamNotificationState.lastNotifiedAt < channelUpstreamNotifySuppressSeconds &&
		channelUpstreamNotificationState.lastChangedChannels == changedChannels &&
		channelUpstreamNotificationState.lastFailedChannels == failedChannels {
		return false
	}
	channelUpstreamNotificationState.lastNotifiedAt = now
	channelUpstreamNotificationState.lastChangedChannels = changedChannels
	channelUpstreamNotificationState.lastFailedChannels = failedChannels
	return true
}

func buildChannelUpstreamNotificationContent(summary channelUpstreamNotificationSummary) string {
	var content strings.Builder
	failedChannels := summary.FailedChannels
	fmt.Fprintf(&content,
		"上游模型巡检摘要：检测渠道 %d 个，发现变更 %d 个，新增 %d 个，删除 %d 个，自动同步新增 %d 个，失败 %d 个。",
		summary.CheckedChannels, summary.ChangedChannels, summary.DetectedAddModels,
		summary.DetectedRemoveModels, summary.AutoAddedModels, failedChannels)
	if len(summary.Channels) > 0 {
		content.WriteString("\n\n变更渠道明细：")
		shown := min(len(summary.Channels), channelUpstreamNotifyMaxChannels)
		for _, channel := range summary.Channels[:shown] {
			fmt.Fprintf(&content, "\n- %s (+%d / -%d)", boundedChannelUpstreamNotificationLabel(channel.Name), channel.AddCount, channel.RemoveCount)
		}
		if summary.ChangedChannels > shown {
			fmt.Fprintf(&content, "\n- 其余 %d 个变更渠道已省略", summary.ChangedChannels-shown)
		}
	}
	if len(summary.AddModelSamples) > 0 {
		shown := min(len(summary.AddModelSamples), channelUpstreamNotifyMaxModels)
		content.WriteString("\n\n新增模型示例：")
		content.WriteString(strings.Join(summary.AddModelSamples[:shown], ", "))
		if summary.DetectedAddModels > shown {
			fmt.Fprintf(&content, "（仅展示最多 %d 个不同模型）", channelUpstreamNotifyMaxModels)
		}
	}
	if len(summary.RemoveModelSamples) > 0 {
		shown := min(len(summary.RemoveModelSamples), channelUpstreamNotifyMaxModels)
		content.WriteString("\n\n删除模型示例：")
		content.WriteString(strings.Join(summary.RemoveModelSamples[:shown], ", "))
		if summary.DetectedRemoveModels > shown {
			fmt.Fprintf(&content, "（仅展示最多 %d 个不同模型）", channelUpstreamNotifyMaxModels)
		}
	}
	if failedChannels > 0 {
		shown := min(len(summary.FailedChannelIDs), channelUpstreamNotifyMaxFailures)
		ids := make([]string, 0, shown)
		for _, channelID := range summary.FailedChannelIDs[:shown] {
			ids = append(ids, textutil.Int2Str(channelID))
		}
		content.WriteString("\n\n失败渠道 ID：")
		content.WriteString(strings.Join(ids, ", "))
		if failedChannels > shown {
			fmt.Fprintf(&content, "（其余 %d 个已省略）", failedChannels-shown)
		}
	}
	return content.String()
}

func boundedChannelUpstreamNotificationLabel(value string) string {
	var result strings.Builder
	result.Grow(min(len(value), channelUpstreamNotifyMaxLabelBytes))
	for _, current := range strings.TrimSpace(value) {
		if unicode.IsControl(current) {
			current = ' '
		}
		size := utf8.RuneLen(current)
		if size < 1 || result.Len()+size > channelUpstreamNotifyMaxLabelBytes {
			break
		}
		result.WriteRune(current)
	}
	return strings.TrimSpace(result.String())
}

func notifyChannelUpstreamWatchers(summary channelUpstreamNotificationSummary) {
	failedChannels := summary.FailedChannels
	if summary.ChangedChannels == 0 && failedChannels == 0 {
		return
	}
	now := wallclock.NowTimestamp()
	if !shouldSendChannelUpstreamNotification(now, summary.ChangedChannels, failedChannels) {
		return
	}
	var users []model.User
	if err := model.DB.Select("id", "email", "email_verified", "role", "status", "setting").
		Where("status = ? AND role >= ?", model.UserStatusEnabled, roles.RoleAdminUser).
		Find(&users).Error; err != nil {
		logging.SysError("query upstream model notification watchers failed")
		return
	}
	content := buildChannelUpstreamNotificationContent(summary)
	for i := range users {
		user := &users[i]
		settings := userssvc.ParseUserSettings(user.Setting)
		if !settings.UpstreamModelUpdateNotifyEnabled {
			continue
		}
		if err := userssvc.SendUserNotification(user.Id, user.Email, user.EmailVerified, settings, userssvc.UserNotification{
			Type: "channel_update", Title: "上游模型巡检通知", Content: content,
		}); err != nil {
			logging.SysError(fmt.Sprintf("notify upstream model watcher %d failed", user.Id))
		}
	}
}

func appendBoundedUniqueModelSamples(destination []string, values []string) []string {
	seen := make(map[string]struct{}, len(destination)+len(values))
	for _, value := range destination {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if len(destination) >= channelUpstreamNotifyMaxModels {
			break
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		destination = append(destination, value)
	}
	return destination
}
