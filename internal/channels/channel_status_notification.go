package channels

import (
	"errors"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
)

func notifyRootChannelStatus(channelID int, channelName string, status int, reason string) error {
	channelName = boundedChannelUpstreamNotificationLabel(channelName)
	reason = boundedChannelUpstreamNotificationLabel(reason)
	if channelID <= 0 || channelName == "" {
		return errors.New("channel notification identity is invalid")
	}
	title := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelID)
	content := title
	if status == channelcatalog.ChannelStatusAutoDisabled {
		title = fmt.Sprintf("通道「%s」（#%d）已被禁用", channelName, channelID)
		content = title
		if reason != "" {
			content += "，原因：" + reason
		}
	} else if status != channelcatalog.ChannelStatusEnabled {
		return errors.New("channel notification status is invalid")
	}
	return userssvc.NotifyRootUser(userssvc.UserNotification{
		Type:  fmt.Sprintf("channel_update_%d_%d", channelID, status),
		Title: title, Content: content,
	})
}
