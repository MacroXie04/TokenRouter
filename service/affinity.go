package service

import (
	"context"
	"fmt"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
)

// affinityTTL is how long a user+model stays pinned to a channel after a
// successful request.
const affinityTTL = 30 * time.Minute

func affinityKey(userId int, model string) string {
	return fmt.Sprintf("affinity:%d:%s", userId, model)
}

// GetAffinityChannel returns the pinned channel id for a user+model, or 0.
func GetAffinityChannel(userId int, model string) int {
	v, _ := common.Store.Get(context.Background(), affinityKey(userId, model))
	return common.Str2Int(v)
}

// SetAffinityChannel pins a user+model to a channel for the affinity TTL.
func SetAffinityChannel(userId int, model string, channelId int) {
	if channelId <= 0 {
		return
	}
	_ = common.Store.Set(context.Background(), affinityKey(userId, model), common.Int2Str(channelId), affinityTTL)
}
