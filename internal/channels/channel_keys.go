package channels

import (
	"context"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
)

// GetChannelKey returns one enabled API key to use for a channel. Current
// multi-key channels store newline-delimited credentials in Key and their
// per-key state in ChannelInfo. Older installations may instead keep a JSON
// array (or {"keys": [...]}) in Other. Never return the complete serialized
// key set: doing so would both break authentication and disclose every
// credential to one upstream request.
func GetChannelKey(channel *model.Channel) string {
	if channel == nil {
		return ""
	}
	if info := parseChannelInfo(channel); info.IsMultiKey {
		all := splitChannelKeys(channel.Key)
		enabled := make([]string, 0, len(all))
		for index, key := range all {
			if keyStatusOf(info, index) == 1 {
				enabled = append(enabled, key)
			}
		}
		return rotateChannelKey(channel.Id, enabled)
	}
	keys := parseMultiKeys(channel.Other)
	if len(keys) > 0 {
		return rotateChannelKey(channel.Id, keys)
	}
	trimmed := strings.TrimSpace(channel.Key)
	// Some provider credentials (notably Codex OAuth snapshots) are JSON
	// objects and may be pretty-printed across multiple lines. Preserve that
	// single structured credential intact.
	if strings.HasPrefix(trimmed, "{") {
		var object map[string]any
		if jsonutil.Unmarshal([]byte(trimmed), &object) == nil {
			return trimmed
		}
	}
	// Be defensive around partially migrated rows whose ChannelInfo marker was
	// lost: a newline-delimited value is still a key collection, not a bearer
	// token. Selecting one credential is safer than transmitting the collection.
	if keys = splitChannelKeys(channel.Key); len(keys) > 1 {
		return rotateChannelKey(channel.Id, keys)
	}
	return trimmed
}

// MaskChannelCredentialsForResponse removes every supported credential
// representation from a channel before it crosses an API response boundary.
// Older installations may keep a multi-key collection in Other even when Key
// has already been omitted by the query.
func MaskChannelCredentialsForResponse(channel *model.Channel) {
	if channel == nil {
		return
	}
	channel.Key = ""
	if len(parseMultiKeys(channel.Other)) > 0 {
		channel.Other = ""
	}
}

// MaskChannelSensitiveFieldsForResponse produces the ChannelRead view. These
// fields are classified as sensitive-write inputs and can contain internal
// endpoints, fixed authorization headers, or legacy credentials; ordinary
// read permission must not make them observable on the wire.
func MaskChannelSensitiveFieldsForResponse(channel *model.Channel) {
	if channel == nil {
		return
	}
	MaskChannelCredentialsForResponse(channel)
	channel.BaseURL = ""
	channel.OpenAIOrganization = ""
	channel.Other = ""
	channel.Setting = ""
	channel.OtherSettings = ""
	channel.ParamOverride = ""
	channel.HeaderOverride = ""
}

// ChannelCredentialForDisclosure returns the complete stored credential for
// the root-only, step-up-protected disclosure endpoint. Current multi-key
// rows use newline-delimited Key values; legacy rows used a JSON collection in
// Other, which is normalized to the same newline-delimited representation.
func ChannelCredentialForDisclosure(channel *model.Channel) string {
	if channel == nil {
		return ""
	}
	if strings.TrimSpace(channel.Key) != "" {
		return channel.Key
	}
	return strings.Join(parseMultiKeys(channel.Other), "\n")
}

func rotateChannelKey(channelID int, keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 {
		return keys[0]
	}
	n, err := cache.Store.Incr(context.Background(), "channel_key_round:"+textutil.Int2Str(channelID))
	if err != nil {
		return keys[0]
	}
	return keys[int(n)%len(keys)]
}

func splitChannelKeys(raw string) []string {
	parts := strings.Split(raw, "\n")
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		if key := strings.TrimSpace(part); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// parseMultiKeys extracts the multi-key list from a channel's Other field.
func parseMultiKeys(other string) []string {
	if other == "" {
		return nil
	}
	var keys []string
	if err := jsonutil.UnmarshalJsonStr(other, &keys); err == nil && len(keys) > 0 {
		return keys
	}
	var obj struct {
		Keys []string `json:"keys"`
	}
	if err := jsonutil.UnmarshalJsonStr(other, &obj); err == nil && len(obj.Keys) > 0 {
		return obj.Keys
	}
	return nil
}
