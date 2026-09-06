package billing

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// LogTypeUnknown is defined in internal/billing/log.go alongside the other log-type
// constants; a zero log type means "no type filter" in the queries below.

// maxRecentLogItems bounds the per-token recent log listing (reference
// common.MaxRecentItems).
const maxRecentLogItems = 1000

// logSearchCountLimit bounds the user log-search count (reference
// logSearchCountLimit).
const logSearchCountLimit = 10000

const (
	maxLogModelFilterBytes      = 255
	maxLogUsernameFilterBytes   = 64
	maxLogTokenNameFilterBytes  = 50
	maxLogGroupFilterBytes      = 64
	maxLogRequestIDFilterBytes  = 64
	maxLogUpstreamIDFilterBytes = 128
)

// validateLogFilter keeps every value used to build a log query within the
// corresponding persisted-field boundary. Besides bounding database work, it
// rejects invisible control and bidi characters so an attacker cannot create
// misleading operator searches or error/audit output.
func validateLogFilter(name, value string, maximum int) error {
	if value == "" {
		return nil
	}
	if len(value) > maximum || !utf8.ValidString(value) {
		return fmt.Errorf("%s 查询条件无效", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) || (character >= 0x2066 && character <= 0x2069) {
			return fmt.Errorf("%s 查询条件无效", name)
		}
	}
	return nil
}

func validateLogFilters(modelName, username, tokenName, group, requestID, upstreamRequestID string) error {
	filters := []struct {
		name    string
		value   string
		maximum int
	}{
		{name: "model_name", value: modelName, maximum: maxLogModelFilterBytes},
		{name: "username", value: username, maximum: maxLogUsernameFilterBytes},
		{name: "token_name", value: tokenName, maximum: maxLogTokenNameFilterBytes},
		{name: "group", value: group, maximum: maxLogGroupFilterBytes},
		{name: "request_id", value: requestID, maximum: maxLogRequestIDFilterBytes},
		{name: "upstream_request_id", value: upstreamRequestID, maximum: maxLogUpstreamIDFilterBytes},
	}
	for _, filter := range filters {
		if err := validateLogFilter(filter.name, filter.value, filter.maximum); err != nil {
			return err
		}
	}
	return nil
}

// logGroupColumn is the dialect-quoted "group" identifier ("group" is a
// keyword in several SQL dialects).
func logGroupColumn() string {
	if model.UsingPostgreSQL() {
		return `"group"`
	}
	return "`group`"
}

// sanitizeLogLikePattern escapes the ESCAPE character and underscores, then
// validates the wildcard usage (reference rules: no consecutive %%, at most
// two %, at least two literal characters when wildcards are present).
func sanitizeLogLikePattern(input string) (string, error) {
	input = strings.ReplaceAll(input, "!", "!!")
	input = strings.ReplaceAll(input, `_`, `!_`)
	if strings.Contains(input, "%%") {
		return "", errors.New("搜索模式中不允许包含连续的 % 通配符")
	}
	if count := strings.Count(input, "%"); count > 2 {
		return "", errors.New("搜索模式中最多允许包含 2 个 % 通配符")
	} else if count > 0 {
		stripped := strings.ReplaceAll(input, "%", "")
		if len(stripped) < 2 {
			return "", errors.New("使用模糊搜索时，关键词长度至少为 2 个字符")
		}
	}
	return input, nil
}

// applyLogTextFilter applies the reference explicit text filter: a value
// containing % is treated as a sanitized LIKE pattern, otherwise the value
// must match exactly.
func applyLogTextFilter(tx *gorm.DB, column, value string) (*gorm.DB, error) {
	if value == "" {
		return tx, nil
	}
	if strings.Contains(value, "%") {
		pattern, err := sanitizeLogLikePattern(value)
		if err != nil {
			return nil, err
		}
		return tx.Where(column+" LIKE ? ESCAPE '!'", pattern), nil
	}
	return tx.Where(column+" = ?", value), nil
}

func applyUpstreamRequestIDFilter(tx *gorm.DB, value string) *gorm.DB {
	if value == "" {
		return tx
	}
	normalized := requestctx.NormalizeProviderCorrelationID(value)
	if normalized == value {
		return tx.Where("logs.upstream_request_id = ?", value)
	}
	// Match legacy raw rows as well as newly fingerprinted rows during rolling
	// upgrades and historical searches.
	return tx.Where("(logs.upstream_request_id = ? OR logs.upstream_request_id = ?)", value, normalized)
}

// GetAllLogs lists logs with the reference admin filter contract.
func GetAllLogs(logType int, startTimestamp, endTimestamp int64, modelName, username, tokenName string,
	startIdx, num, channel int, group, requestId, upstreamRequestId string) ([]model.Log, int64, error) {
	if err := validateLogFilters(modelName, username, tokenName, group, requestId, upstreamRequestId); err != nil {
		return nil, 0, err
	}
	tx := model.LOG_DB
	if logType != LogTypeUnknown {
		tx = tx.Where("logs.type = ?", logType)
	}
	var err error
	if tx, err = applyLogTextFilter(tx, "logs.model_name", modelName); err != nil {
		return nil, 0, err
	}
	if tx, err = applyLogTextFilter(tx, "logs.username", username); err != nil {
		return nil, 0, err
	}
	if tokenName != "" {
		tx = tx.Where("logs.token_name = ?", tokenName)
	}
	if requestId != "" {
		tx = tx.Where("logs.request_id = ?", requestId)
	}
	if upstreamRequestId != "" {
		tx = applyUpstreamRequestIDFilter(tx, upstreamRequestId)
	}
	if startTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", endTimestamp)
	}
	if channel != 0 {
		tx = tx.Where("logs.channel_id = ?", channel)
	}
	if group != "" {
		tx = tx.Where("logs."+logGroupColumn()+" = ?", group)
	}
	var total int64
	if err := tx.Model(&model.Log{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := "logs.created_at desc, logs.id desc"
	if model.UsingClickHouseLog() {
		order = "logs.created_at desc, logs.request_id desc"
	}
	var logs []model.Log
	if err := tx.Order(order).Limit(num).Offset(startIdx).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	if model.UsingClickHouseLog() {
		assignDisplayLogIds(logs, startIdx)
	}
	return logs, total, nil
}

// GetUserLogs lists one user's logs with the reference self filter contract;
// the rows are redacted for the user-facing view.
func GetUserLogs(userId, logType int, startTimestamp, endTimestamp int64, modelName, tokenName string,
	startIdx, num int, group, requestId, upstreamRequestId string) ([]model.Log, int64, error) {
	if err := validateLogFilters(modelName, "", tokenName, group, requestId, upstreamRequestId); err != nil {
		return nil, 0, err
	}
	tx := model.LOG_DB.Where("logs.user_id = ?", userId)
	if logType != LogTypeUnknown {
		tx = tx.Where("logs.type = ?", logType)
	}
	var err error
	if tx, err = applyLogTextFilter(tx, "logs.model_name", modelName); err != nil {
		return nil, 0, err
	}
	if tokenName != "" {
		tx = tx.Where("logs.token_name = ?", tokenName)
	}
	if requestId != "" {
		tx = tx.Where("logs.request_id = ?", requestId)
	}
	if upstreamRequestId != "" {
		tx = applyUpstreamRequestIDFilter(tx, upstreamRequestId)
	}
	if startTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", endTimestamp)
	}
	if group != "" {
		tx = tx.Where("logs."+logGroupColumn()+" = ?", group)
	}
	var total int64
	if err := tx.Model(&model.Log{}).Limit(logSearchCountLimit).Count(&total).Error; err != nil {
		return nil, 0, errors.New("查询日志失败")
	}
	order := "logs.id desc"
	if model.UsingClickHouseLog() {
		order = "logs.created_at desc, logs.request_id desc"
	}
	var logs []model.Log
	if err := tx.Order(order).Limit(num).Offset(startIdx).Find(&logs).Error; err != nil {
		return nil, 0, errors.New("查询日志失败")
	}
	FormatUserLogs(logs, startIdx)
	return logs, total, nil
}

// GetLogByTokenId lists the recent logs produced with one relay token.
func GetLogByTokenId(tokenId int) ([]model.Log, error) {
	var logs []model.Log
	if err := model.LOG_DB.Where("token_id = ?", tokenId).Order("id desc").Limit(maxRecentLogItems).Find(&logs).Error; err != nil {
		return nil, err
	}
	FormatUserLogs(logs, 0)
	return logs, nil
}

// LogStat is the reference quota/rpm/tpm aggregation result.
type LogStat struct {
	Quota int `json:"quota"`
	Rpm   int `json:"rpm"`
	Tpm   int `json:"tpm"`
}

// SumUsedQuota aggregates consumed quota plus the requests-per-minute and
// tokens-per-minute over the trailing 60-second window (reference contract:
// rpm/tpm count only consume-typed rows from the last minute).
func SumUsedQuota(logType int, startTimestamp, endTimestamp int64, modelName, username, tokenName string, channel int, group string) (LogStat, error) {
	var stat LogStat
	if err := validateLogFilters(modelName, username, tokenName, group, "", ""); err != nil {
		return stat, err
	}
	quotaTx := model.LOG_DB.Table("logs").Select("COALESCE(sum(quota), 0) quota")
	rpmTpmTx := model.LOG_DB.Table("logs").
		Select("count(*) rpm, COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0) tpm").
		Where("created_at >= ?", time.Now().Add(-60*time.Second).Unix())

	var err error
	if quotaTx, err = applyLogTextFilter(quotaTx, "username", username); err != nil {
		return stat, err
	}
	if rpmTpmTx, err = applyLogTextFilter(rpmTpmTx, "username", username); err != nil {
		return stat, err
	}
	if tokenName != "" {
		quotaTx = quotaTx.Where("token_name = ?", tokenName)
		rpmTpmTx = rpmTpmTx.Where("token_name = ?", tokenName)
	}
	if startTimestamp != 0 {
		quotaTx = quotaTx.Where("created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		quotaTx = quotaTx.Where("created_at <= ?", endTimestamp)
	}
	if quotaTx, err = applyLogTextFilter(quotaTx, "model_name", modelName); err != nil {
		return stat, err
	}
	if rpmTpmTx, err = applyLogTextFilter(rpmTpmTx, "model_name", modelName); err != nil {
		return stat, err
	}
	if channel != 0 {
		quotaTx = quotaTx.Where("channel_id = ?", channel)
		rpmTpmTx = rpmTpmTx.Where("channel_id = ?", channel)
	}
	if group != "" {
		quotaTx = quotaTx.Where(logGroupColumn()+" = ?", group)
		rpmTpmTx = rpmTpmTx.Where(logGroupColumn()+" = ?", group)
	}
	// The reference aggregates only consume-typed rows here (logType is
	// accepted for call-site parity but not applied).
	quotaTx = quotaTx.Where("type = ?", LogTypeConsume)
	rpmTpmTx = rpmTpmTx.Where("type = ?", LogTypeConsume)
	// Scan into separate rows: a second Scan over the same struct would
	// reset fields not present in the later result set.
	var quotaRow struct {
		Quota int
	}
	if err := quotaTx.Scan(&quotaRow).Error; err != nil {
		return stat, errors.New("查询统计数据失败")
	}
	var rpmTpmRow struct {
		Rpm int
		Tpm int
	}
	if err := rpmTpmTx.Scan(&rpmTpmRow).Error; err != nil {
		return stat, errors.New("查询统计数据失败")
	}
	stat.Quota = quotaRow.Quota
	stat.Rpm = rpmTpmRow.Rpm
	stat.Tpm = rpmTpmRow.Tpm
	return stat, nil
}

// FormatUserLogs redacts logs for user-facing views: channel names are
// cleared, admin-only debug fields are stripped from the Other payload, and
// ids are renumbered for display pagination.
func FormatUserLogs(logs []model.Log, startIdx int) {
	for i := range logs {
		logs[i].ChannelName = ""
		otherMap := map[string]any{}
		if logs[i].Other != "" {
			_ = jsonutil.UnmarshalJsonStr(logs[i].Other, &otherMap)
		}
		if otherMap != nil {
			delete(otherMap, "admin_info")
			delete(otherMap, "audit_info")
		}
		if data, err := jsonutil.Marshal(otherMap); err == nil {
			logs[i].Other = string(data)
		} else {
			logs[i].Other = ""
		}
	}
	assignDisplayLogIds(logs, startIdx)
}

func assignDisplayLogIds(logs []model.Log, startIdx int) {
	for i := range logs {
		logs[i].Id = startIdx + i + 1
	}
}
