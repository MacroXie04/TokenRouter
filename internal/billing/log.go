package billing

import (
	"fmt"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"strings"
)

// Log type constants.
const (
	LogTypeUnknown       = 0
	LogTypeTopup         = 1
	LogTypeConsume       = 2
	LogTypeManage        = 3
	LogTypeSystem        = 4
	LogTypeSendCode      = 5
	LogTypePasswordReset = 6
	// LogTypeRefund is the reference-compatible accounting refund event. The
	// legacy password-reset audit historically shared numeric value 6.
	LogTypeRefund = 6
)

// RecordConsumeLog writes a consumption log to the log database.
func RecordConsumeLog(userId int, username, tokenName, modelName string, promptTokens, completionTokens, quota, useTime int, isStream bool, channelId int, group, ip, requestId, upstreamRequestId string, tokenId int, other map[string]any) {
	if err := RecordConsumeLogChecked(userId, username, tokenName, modelName, promptTokens, completionTokens, quota, useTime, isStream, channelId, group, ip, requestId, upstreamRequestId, tokenId, other); err != nil {
		logging.SysError("record consume log failed: " + err.Error())
	}
}

// RecordConsumeLogChecked durably writes a consumption log and reports
// serialization/storage failures. Realtime accounting uses the checked form
// so a successful charge can never silently lose its audit record.
func RecordConsumeLogChecked(userId int, username, tokenName, modelName string, promptTokens, completionTokens, quota, useTime int, isStream bool, channelId int, group, ip, requestId, upstreamRequestId string, tokenId int, other map[string]any) error {
	upstreamRequestId = requestctx.NormalizeProviderCorrelationID(upstreamRequestId)
	if !userssvc.UserRecordIPLogEnabled(userId) {
		ip = ""
	}
	otherJSON := ""
	if other != nil {
		b, err := jsonutil.Marshal(other)
		if err != nil {
			return fmt.Errorf("marshal consume log metadata: %w", err)
		}
		otherJSON = string(b)
	}
	log := model.Log{
		UserId:            userId,
		Type:              LogTypeConsume,
		Username:          username,
		TokenName:         tokenName,
		ModelName:         modelName,
		Quota:             quota,
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		UseTime:           useTime,
		IsStream:          isStream,
		ChannelId:         channelId,
		ChannelName:       channelssvc.GetChannelName(channelId),
		TokenId:           tokenId,
		Group:             group,
		Ip:                ip,
		RequestId:         requestId,
		UpstreamRequestId: upstreamRequestId,
		Other:             otherJSON,
	}
	if err := persistAuditLogWithOutbox(&log); err != nil {
		return fmt.Errorf("insert consume log: %w", err)
	}
	// Per-hour usage histogram for the data dashboard (reference behavior:
	// recorded at consume time, gated on DataExportEnabled).
	if DataExportEnabled() {
		if err := LogQuotaDataChecked(userId, username, modelName, group, tokenId, channelId,
			quota, promptTokens+completionTokens, log.CreatedAt); err != nil {
			return fmt.Errorf("cache consume histogram: %w", err)
		}
	}
	return nil
}

// RecordSystemLog writes a system/management log and makes failures visible to
// operators. Callers that must fail the surrounding operation should use the
// checked form directly.
func RecordSystemLog(userId int, logType int, content string) {
	if err := RecordSystemLogChecked(userId, logType, content); err != nil {
		logging.SysError("record system log failed: " + err.Error())
	}
}

// RecordSystemLogChecked durably writes a system/management audit record.
func RecordSystemLogChecked(userId int, logType int, content string) error {
	l := model.Log{
		UserId:  userId,
		Type:    logType,
		Content: content,
	}
	if err := persistAuditLogWithOutbox(&l); err != nil {
		return fmt.Errorf("insert system log: %w", err)
	}
	return nil
}

// RecordTopupLog writes a top-up log and surfaces durable-write failures to
// the caller.
func RecordTopupLog(userId int, quota int, money float64, tradeNo string) error {
	l := model.Log{
		UserId:  userId,
		Type:    LogTypeTopup,
		Quota:   quota,
		Content: tradeNo,
	}
	if err := persistAuditLogWithOutbox(&l); err != nil {
		return fmt.Errorf("insert topup log: %w", err)
	}
	return nil
}

func paymentAuditEventID(kind, stableKey string) string {
	return "pay_" + cryptoutil.SHA256Hex(kind + ":" + strings.TrimSpace(stableKey))[:48]
}

// enqueueTopupLogTx couples the financial state change and its audit record
// in the primary database. Delivery to the configured log sink may happen
// after commit without risking silent audit loss.
func enqueueTopupLogTx(tx *gorm.DB, userID, quota int, tradeNo string, createdAt int64) (string, error) {
	eventID := paymentAuditEventID("topup", tradeNo)
	entry := &model.Log{
		AuditEventId: &eventID,
		UserId:       userID,
		Type:         LogTypeTopup,
		Quota:        quota,
		Content:      tradeNo,
		CreatedAt:    createdAt,
	}
	return eventID, EnqueueAuditLogTx(tx, entry)
}

func enqueuePaymentSystemLogTx(tx *gorm.DB, eventKind, stableKey string, userID int, content string, createdAt int64) (string, error) {
	eventID := paymentAuditEventID(eventKind, stableKey)
	entry := &model.Log{
		AuditEventId: &eventID,
		UserId:       userID,
		Type:         LogTypeTopup,
		Content:      content,
		CreatedAt:    createdAt,
	}
	return eventID, EnqueueAuditLogTx(tx, entry)
}
