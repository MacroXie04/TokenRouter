package settings

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	appsetting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxSystemOptionKeyBytes    = appsetting.MaxOptionKeyBytes
	maxSystemOptionValueBytes  = appsetting.MaxOptionValueBytes
	maxSystemOptionRows        = appsetting.MaxOptionRows
	maxSystemOptionResultBytes = appsetting.MaxOptionAggregateBytes
)

// GetOptions returns non-sensitive system options to root operators.
func GetOptions(c *gin.Context) {
	var stored []model.Option
	if err := model.DB.Order("key asc").Limit(maxSystemOptionRows + 1).Find(&stored).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if len(stored) > maxSystemOptionRows {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "系统设置数量超出安全范围"})
		return
	}
	options := make([]model.Option, 0, len(stored)+10)
	seen := make(map[string]bool, len(stored))
	resultBytes := 0
	for _, option := range stored {
		if option.Key == "theme.frontend" || option.Key == appsetting.LegacyPaymentComplianceOption {
			continue
		}
		// The Waffo Pancake key is managed only through its dedicated
		// blank-preserving write endpoint. Omitting the row entirely prevents a
		// generic settings client from learning or accidentally round-tripping
		// this write-only credential identifier.
		if option.Key == appsetting.WaffoPancakePrivateKeyOption {
			continue
		}
		if isSensitiveOptionKey(option.Key) {
			if !validSystemOptionKey(option.Key) || resultBytes > maxSystemOptionResultBytes-len(option.Key) {
				c.JSON(http.StatusOK, gin.H{"success": false, "message": "系统设置数据超出安全范围"})
				return
			}
			option.Value = ""
			option.Redacted = true
			options = append(options, option)
			seen[option.Key] = true
			resultBytes += len(option.Key)
			continue
		}
		if option.Key == appsetting.QuotaPerUnitOption {
			option.Value = strconv.Itoa(quotamath.QuotaPerUnit)
		}
		if !validSystemOptionKey(option.Key) || !validSystemOptionValue(option.Value) ||
			resultBytes > maxSystemOptionResultBytes-len(option.Key)-len(option.Value) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "系统设置数据超出安全范围"})
			return
		}
		options = append(options, option)
		seen[option.Key] = true
		resultBytes += len(option.Key) + len(option.Value)
	}
	for key, value := range appsetting.ChannelAffinityOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range appsetting.CheckinOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range appsetting.ModelPolicyOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range appsetting.UsageRatioOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range appsetting.GrokOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range appsetting.BillingOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	for key, value := range appsetting.AuthenticationOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
			seen[key] = true
		}
	}
	for key, value := range appsetting.OperationsOptionDefaults() {
		if !seen[key] {
			option := model.Option{Key: key, Value: value}
			if isSensitiveOptionKey(key) {
				option.Value = ""
				option.Redacted = true
			}
			options = append(options, option)
			seen[key] = true
		}
	}
	for key, value := range appsetting.ModelRequestRateLimitOptionDefaults() {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
			seen[key] = true
		}
	}
	for key, value := range map[string]string{
		appsetting.ExposeRatioEnabledOption:          "false",
		appsetting.PasskeyEnabledOption:              "false",
		appsetting.GroupGroupRatioOption:             "{}",
		appsetting.TopUpGroupRatioOption:             billingsvc.TopUpGroupRatioOptionDefault(),
		appsetting.ChatsOption:                       "[]",
		appsetting.QuotaPerUnitOption:                strconv.Itoa(quotamath.QuotaPerUnit),
		appsetting.ConsoleAPIInfoOption:              "[]",
		appsetting.ConsoleAPIInfoEnabledOption:       "true",
		appsetting.ConsoleFAQOption:                  "[]",
		appsetting.ConsoleFAQEnabledOption:           "true",
		appsetting.ConsoleUptimeKumaGroupsOption:     "[]",
		appsetting.ConsoleUptimeKumaEnabledOption:    "true",
		appsetting.ConsoleAnnouncementsOption:        "[]",
		appsetting.ConsoleAnnouncementsEnabledOption: "true",
	} {
		if !seen[key] {
			options = append(options, model.Option{Key: key, Value: value})
		}
	}
	if !seen[appsetting.UserUsableGroupsOption] {
		options = append(options, model.Option{
			Key:   appsetting.UserUsableGroupsOption,
			Value: appsetting.UserUsableGroupsOptionDefault(),
		})
	}
	if !seen[appsetting.AutoGroupsOption] {
		options = append(options, model.Option{Key: appsetting.AutoGroupsOption, Value: appsetting.AutoGroupsOptionDefault()})
	}
	if !seen[appsetting.MaxTokenAutoGroupsOption] {
		options = append(options, model.Option{Key: appsetting.MaxTokenAutoGroupsOption, Value: appsetting.MaxTokenAutoGroupsOptionDefault()})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Key < options[j].Key })
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": options})
}

// UpdateOptions updates one system option using the reference key/value shape.
func UpdateOptions(c *gin.Context) {
	var request struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	if !validSystemOptionKey(request.Key) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "设置键超出允许范围"})
		return
	}
	value, ok := systemOptionScalarString(request.Value)
	if !ok || !validSystemOptionValue(value) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "设置值超出允许范围"})
		return
	}
	if isPaymentComplianceOptionKey(request.Key) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "合规确认字段不允许通过通用设置接口修改"})
		return
	}
	if request.Key == appsetting.QuotaPerUnitOption && value != strconv.Itoa(quotamath.QuotaPerUnit) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "QuotaPerUnit is fixed at 500000 and cannot be changed at runtime"})
		return
	}
	if err := appsetting.ValidateBillingOptionUpdate(request.Key, value); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if request.Key == appsetting.QuotaForInviterOption || request.Key == appsetting.QuotaForInviteeOption {
		if isPositiveOptionValue(value) && !billingsvc.PaymentComplianceConfirmed() {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": billingsvc.ErrPaymentComplianceRequired.Error()})
			return
		}
	}
	var err error
	switch request.Key {
	case appsetting.ModelPriceOption:
		err = billingsvc.UpdateModelPriceOption(value)
	case appsetting.GroupRatioOption:
		err = billingsvc.UpdateGroupRatioOption(value)
	case appsetting.GroupGroupRatioOption:
		err = billingsvc.UpdateGroupGroupRatioOption(value)
	case appsetting.TopUpGroupRatioOption:
		err = billingsvc.UpdateTopUpGroupRatioOption(value)
	default:
		err = appsetting.UpdateOption(request.Key, value)
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage, "option.update key="+request.Key)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func isPaymentComplianceOptionKey(key string) bool {
	return key == appsetting.LegacyPaymentComplianceOption || strings.HasPrefix(key, "payment_setting.compliance_")
}

func isSensitiveOptionKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.HasSuffix(normalized, "token") || strings.HasSuffix(normalized, "secret") ||
		strings.HasSuffix(normalized, "key") || strings.HasSuffix(normalized, "password") ||
		strings.HasSuffix(normalized, "credential")
}

func validSystemOptionKey(key string) bool {
	if key == "" || len(key) > maxSystemOptionKeyBytes || !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func validSystemOptionValue(value string) bool {
	if len(value) > maxSystemOptionValueBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x061c ||
			r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069) {
			return false
		}
	}
	return true
}

func systemOptionScalarString(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return typed.String(), true
		}
		parsed, err := typed.Float64()
		return typed.String(), err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return "", false
	}
}

func isPositiveOptionValue(value string) bool {
	if integer, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return integer > 0
	}
	decimal, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && decimal > 0
}
