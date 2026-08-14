package service

import (
	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Log type constants.
const (
	LogTypeUnknown        = 0
	LogTypeTopup          = 1
	LogTypeConsume        = 2
	LogTypeManage         = 3
	LogTypeSystem         = 4
	LogTypeSendCode       = 5
	LogTypePasswordReset  = 6
)

// RecordConsumeLog writes a consumption log to the log database.
func RecordConsumeLog(userId int, username, tokenName, modelName string, promptTokens, completionTokens, quota, useTime int, isStream bool, channelId int, group, ip, requestId, upstreamRequestId string, tokenId int, other map[string]any) {
	otherJSON := ""
	if other != nil {
		if b, err := common.Marshal(other); err == nil {
			otherJSON = string(b)
		}
	}
	createdAt := common.NowTimestamp()
	log := model.Log{
		UserId:            userId,
		Type:              LogTypeConsume,
		TokenName:         tokenName,
		ModelName:         modelName,
		Quota:             quota,
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		UseTime:           useTime,
		IsStream:          isStream,
		ChannelId:         channelId,
		ChannelName:       GetChannelName(channelId),
		TokenId:           tokenId,
		Group:             group,
		Ip:                ip,
		RequestId:         requestId,
		UpstreamRequestId: upstreamRequestId,
		Other:             otherJSON,
		CreatedAt:         createdAt,
	}
	if model.UsingClickHouseLog() {
		// ClickHouse has no auto-increment; use a millisecond timestamp id.
		log.Id = int(common.NowTimestamp() * 1000)
		_ = model.InsertClickHouseLog(&log)
	} else {
		_ = model.LOG_DB.Create(&log).Error
	}
	// Per-hour usage histogram for the data dashboard (reference behavior:
	// recorded at consume time, gated on DataExportEnabled).
	if DataExportEnabled() {
		LogQuotaData(userId, username, modelName, group, tokenId, channelId,
			quota, promptTokens+completionTokens, createdAt)
	}
}

// RecordSystemLog writes a system/management log.
func RecordSystemLog(userId int, logType int, content string) {
	l := model.Log{
		UserId:    userId,
		Type:      logType,
		Content:   content,
		CreatedAt: common.NowTimestamp(),
	}
	_ = model.LOG_DB.Create(&l).Error
}

// RecordTopupLog writes a top-up log.
func RecordTopupLog(userId int, quota int, money float64, tradeNo string) {
	l := model.Log{
		UserId:    userId,
		Type:      LogTypeTopup,
		Quota:     quota,
		Content:   tradeNo,
		CreatedAt: common.NowTimestamp(),
	}
	_ = model.LOG_DB.Create(&l).Error
}
