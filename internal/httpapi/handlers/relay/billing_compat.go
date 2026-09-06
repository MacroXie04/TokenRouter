package relay

import (
	"fmt"
	"github.com/gin-gonic/gin"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
)

const (
	displayTokenStatOption = setting.DisplayTokenStatEnabledOption
)

type openAISubscriptionResponse struct {
	Object             string  `json:"object"`
	HasPaymentMethod   bool    `json:"has_payment_method"`
	SoftLimitUSD       float64 `json:"soft_limit_usd"`
	HardLimitUSD       float64 `json:"hard_limit_usd"`
	SystemHardLimitUSD float64 `json:"system_hard_limit_usd"`
	AccessUntil        int64   `json:"access_until"`
}

type openAIUsageResponse struct {
	Object     string  `json:"object"`
	TotalUsage float64 `json:"total_usage"`
}

func dashboardBillingError(c *gin.Context, errorType string, err error) {
	c.JSON(http.StatusOK, gin.H{"error": gin.H{
		"message": err.Error(),
		"type":    errorType,
	}})
}

func dashboardQuotaAmount(quota int64) (float64, error) {
	if quota < 0 {
		return 0, fmt.Errorf("invalid negative quota")
	}
	display, err := setting.GetCurrencyDisplaySettingChecked()
	if err != nil {
		return 0, err
	}
	switch display.Type {
	case setting.CurrencyDisplayTypeTokens:
		return float64(quota), nil
	case setting.CurrencyDisplayTypeCNY, setting.CurrencyDisplayTypeCustom:
		return float64(quota) / float64(quotamath.QuotaPerUnit) * display.CurrencyExchangeRate(), nil
	default:
		return float64(quota) / float64(quotamath.QuotaPerUnit), nil
	}
}

func dashboardBillingQuota(c *gin.Context, usageOnly bool) (quota int64, expiredTime int64, unlimited bool, err error) {
	if setting.GetOptionBool(displayTokenStatOption, true) {
		authenticated := middleware.GetRelayToken(c)
		if authenticated == nil || authenticated.Id <= 0 {
			return 0, 0, false, fmt.Errorf("authenticated token is unavailable")
		}
		var token model.Token
		if err = model.DB.First(&token, authenticated.Id).Error; err != nil {
			return 0, 0, false, err
		}
		if usageOnly {
			return int64(token.UsedQuota), 0, token.UnlimitedQuota, nil
		}
		quota = int64(token.RemainQuota) + int64(token.UsedQuota)
		return quota, max(token.ExpiredTime, 0), token.UnlimitedQuota, nil
	}

	userID := requestctx.GetUserId(c)
	if userID <= 0 {
		return 0, 0, false, fmt.Errorf("authenticated user is unavailable")
	}
	var user model.User
	if err = model.DB.Select("id", "quota", "used_quota").First(&user, userID).Error; err != nil {
		return 0, 0, false, err
	}
	if usageOnly {
		return int64(user.UsedQuota), 0, false, nil
	}
	return int64(user.Quota) + int64(user.UsedQuota), 0, false, nil
}

// GetDashboardSubscription implements the legacy OpenAI dashboard billing
// contract used by channel balance probes and existing clients.
func GetDashboardSubscription(c *gin.Context) {
	quota, expiredTime, unlimited, err := dashboardBillingQuota(c, false)
	if err != nil {
		dashboardBillingError(c, "upstream_error", err)
		return
	}
	amount, err := dashboardQuotaAmount(quota)
	if err != nil {
		dashboardBillingError(c, "upstream_error", err)
		return
	}
	if unlimited {
		amount = 100000000
	}
	c.JSON(http.StatusOK, openAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	})
}

// GetDashboardUsage returns the legacy OpenAI usage total in cents.
func GetDashboardUsage(c *gin.Context) {
	quota, _, _, err := dashboardBillingQuota(c, true)
	if err != nil {
		dashboardBillingError(c, "new_api_error", err)
		return
	}
	amount, err := dashboardQuotaAmount(quota)
	if err != nil {
		dashboardBillingError(c, "new_api_error", err)
		return
	}
	c.JSON(http.StatusOK, openAIUsageResponse{Object: "list", TotalUsage: amount * 100})
}
