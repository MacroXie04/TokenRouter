package service

import (
	"context"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// GetChannelKey returns the API key to use for a channel. When the channel
// stores multiple keys (a JSON array, or {"keys": [...]}) in its Other field,
// keys are rotated round-robin per channel; otherwise the single channel key is
// used.
func GetChannelKey(channel *model.Channel) string {
	if channel == nil {
		return ""
	}
	keys := parseMultiKeys(channel.Other)
	if len(keys) <= 1 {
		return channel.Key
	}
	n, err := common.Store.Incr(context.Background(), "channel_key_round:"+common.Int2Str(channel.Id))
	if err != nil {
		return keys[0]
	}
	return keys[int(n)%len(keys)]
}

// parseMultiKeys extracts the multi-key list from a channel's Other field.
func parseMultiKeys(other string) []string {
	if other == "" {
		return nil
	}
	var keys []string
	if err := common.UnmarshalJsonStr(other, &keys); err == nil && len(keys) > 0 {
		return keys
	}
	var obj struct {
		Keys []string `json:"keys"`
	}
	if err := common.UnmarshalJsonStr(other, &obj); err == nil && len(obj.Keys) > 0 {
		return obj.Keys
	}
	return nil
}
