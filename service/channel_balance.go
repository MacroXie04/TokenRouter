package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

const maxPersistedChannelBalance = 1_000_000_000_000_000

var ErrChannelBalanceUnsupported = errors.New("尚未实现")

type aiProxyBalanceResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	ErrorCode int    `json:"error_code"`
	Data      struct {
		TotalPoints *float64 `json:"totalPoints"`
	} `json:"data"`
}

type api2GPTBalanceResponse struct {
	TotalRemaining *float64 `json:"total_remaining"`
}

type aigc2DBalanceResponse struct {
	TotalAvailable *float64 `json:"total_available"`
}

type siliconFlowBalanceResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		TotalBalance string `json:"totalBalance"`
	} `json:"data"`
}

type deepSeekBalanceResponse struct {
	BalanceInfos []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
	} `json:"balance_infos"`
}

type openRouterBalanceResponse struct {
	Data struct {
		TotalCredits *float64 `json:"total_credits"`
		TotalUsage   *float64 `json:"total_usage"`
	} `json:"data"`
}

type moonshotBalanceResponse struct {
	Code   int    `json:"code"`
	Scode  string `json:"scode"`
	Status bool   `json:"status"`
	Data   struct {
		AvailableBalance *float64 `json:"available_balance"`
	} `json:"data"`
}

// FetchChannelBalanceContext is the request-scoped balance lookup. It performs
// all upstream parsing before issuing the single-field database update, so a
// failed or malformed provider response cannot partially mutate channel state.
func FetchChannelBalanceContext(ctx context.Context, ch *model.Channel) (float64, error) {
	if ch == nil || ch.Id <= 0 {
		return 0, errors.New("invalid channel")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	credential := firstKey(ch.Key)
	if credential == "" {
		return 0, errors.New("channel has no credential")
	}

	balance, err := fetchChannelBalanceValue(ctx, ch, credential)
	if err != nil {
		return 0, err
	}
	if err := validateChannelBalance(balance); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	updatedAt := common.NowTimestamp()
	result := model.DB.WithContext(ctx).Model(&model.Channel{}).
		Where("id = ? AND type = ? AND key = ? AND base_url = ? AND setting = ?", ch.Id, ch.Type, ch.Key, ch.BaseURL, ch.Setting).
		Updates(map[string]any{
			"balance":              balance,
			"balance_updated_time": updatedAt,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("persist channel balance: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return 0, errors.New("persist channel balance: channel changed or not found")
	}
	ch.Balance = balance
	ch.BalanceUpdatedTime = updatedAt
	return balance, nil
}

func fetchChannelBalanceValue(ctx context.Context, ch *model.Channel, credential string) (float64, error) {
	configuredURL, err := balanceURLChecked(ch)
	if err != nil {
		return 0, err
	}
	if configuredURL != "" {
		body, err := balanceGETContext(ctx, configuredURL, credential)
		if err != nil {
			return 0, err
		}
		return parseBalanceStrict(body)
	}

	switch constant.ChannelType(ch.Type) {
	case constant.ChannelTypeOpenAI:
		return fetchOpenAIBalance(ctx, ch, credential)
	case constant.ChannelTypeCustom:
		if strings.TrimSpace(ch.BaseURL) == "" {
			return 0, errors.New("custom channel has no balance base URL")
		}
		return fetchOpenAIBalance(ctx, ch, credential)
	case constant.ChannelTypeAIProxy:
		body, err := balanceRequest(ctx, "https://aiproxy.io/api/report/getUserOverview", http.Header{"Api-Key": []string{credential}})
		if err != nil {
			return 0, err
		}
		var response aiProxyBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid AIProxy balance response")
		}
		if !response.Success {
			return 0, fmt.Errorf("AIProxy balance request failed (code %d)", response.ErrorCode)
		}
		return requiredProviderBalance("AIProxy", response.Data.TotalPoints)
	case constant.ChannelTypeAPI2GPT:
		body, err := balanceGETContext(ctx, "https://api.api2gpt.com/dashboard/billing/credit_grants", credential)
		if err != nil {
			return 0, err
		}
		var response api2GPTBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid API2GPT balance response")
		}
		return requiredProviderBalance("API2GPT", response.TotalRemaining)
	case constant.ChannelTypeAIGC2D:
		body, err := balanceGETContext(ctx, "https://api.aigc2d.com/dashboard/billing/credit_grants", credential)
		if err != nil {
			return 0, err
		}
		var response aigc2DBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid AIGC2D balance response")
		}
		return requiredProviderBalance("AIGC2D", response.TotalAvailable)
	case constant.ChannelTypeSiliconFlow:
		body, err := balanceGETContext(ctx, "https://api.siliconflow.cn/v1/user/info", credential)
		if err != nil {
			return 0, err
		}
		var response siliconFlowBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid SiliconFlow balance response")
		}
		if response.Code != 20000 {
			return 0, fmt.Errorf("SiliconFlow balance request failed (code %d)", response.Code)
		}
		return parseProviderBalanceText("SiliconFlow", response.Data.TotalBalance)
	case constant.ChannelTypeDeepSeek:
		body, err := balanceGETContext(ctx, "https://api.deepseek.com/user/balance", credential)
		if err != nil {
			return 0, err
		}
		var response deepSeekBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid DeepSeek balance response")
		}
		if len(response.BalanceInfos) > 32 {
			return 0, errors.New("invalid DeepSeek balance response")
		}
		for _, info := range response.BalanceInfos {
			if info.Currency == "CNY" {
				return parseProviderBalanceText("DeepSeek", info.TotalBalance)
			}
		}
		return 0, errors.New("DeepSeek balance response has no CNY balance")
	case constant.ChannelTypeOpenRouter:
		body, err := balanceGETContext(ctx, "https://openrouter.ai/api/v1/credits", credential)
		if err != nil {
			return 0, err
		}
		var response openRouterBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid OpenRouter balance response")
		}
		credits, err := requiredProviderBalance("OpenRouter", response.Data.TotalCredits)
		if err != nil {
			return 0, err
		}
		usage, err := requiredProviderBalance("OpenRouter", response.Data.TotalUsage)
		if err != nil {
			return 0, err
		}
		return credits - usage, nil
	case constant.ChannelTypeMoonshot:
		body, err := balanceGETContext(ctx, "https://api.moonshot.cn/v1/users/me/balance", credential)
		if err != nil {
			return 0, err
		}
		var response moonshotBalanceResponse
		if err := common.Unmarshal(body, &response); err != nil {
			return 0, errors.New("invalid Moonshot balance response")
		}
		if !response.Status || response.Code != 0 {
			return 0, fmt.Errorf("Moonshot balance request failed (code %d)", response.Code)
		}
		cny, err := requiredProviderBalance("Moonshot", response.Data.AvailableBalance)
		if err != nil {
			return 0, err
		}
		price, err := setting.GetTopUpPriceChecked()
		if err != nil {
			return 0, errors.New("invalid CNY/USD conversion price")
		}
		return convertMoonshotBalance(cny, price)
	default:
		return 0, ErrChannelBalanceUnsupported
	}
}

func convertMoonshotBalance(cny, price float64) (float64, error) {
	if validateChannelBalance(cny) != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return 0, errors.New("invalid CNY/USD conversion price")
	}
	balance := decimal.NewFromFloat(cny).Div(decimal.NewFromFloat(price)).InexactFloat64()
	if validateChannelBalance(balance) != nil {
		return 0, errors.New("invalid Moonshot balance response")
	}
	return balance, nil
}

func fetchOpenAIBalance(ctx context.Context, ch *model.Channel, credential string) (float64, error) {
	base := strings.TrimSuffix(strings.TrimSpace(ch.BaseURL), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	subBody, err := balanceGETContext(ctx, base+"/v1/dashboard/billing/subscription", credential)
	if err != nil {
		return 0, err
	}
	var subscription openAISubscriptionResponse
	if err := common.Unmarshal(subBody, &subscription); err != nil {
		return 0, errors.New("invalid OpenAI subscription response")
	}
	if subscription.HasPaymentMethod == nil || subscription.HardLimitUSD == nil ||
		validateChannelBalance(*subscription.HardLimitUSD) != nil {
		return 0, errors.New("invalid OpenAI subscription response")
	}

	now := time.Now()
	startDate := now.Format("2006-01") + "-01"
	if !*subscription.HasPaymentMethod {
		startDate = now.AddDate(0, 0, -100).Format("2006-01-02")
	}
	usageURL := fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", base, startDate, now.Format("2006-01-02"))
	usageBody, err := balanceGETContext(ctx, usageURL, credential)
	if err != nil {
		return 0, err
	}
	var usage openAIUsageResponse
	if err := common.Unmarshal(usageBody, &usage); err != nil {
		return 0, errors.New("invalid OpenAI usage response")
	}
	if usage.TotalUsage == nil || validateChannelBalance(*usage.TotalUsage) != nil {
		return 0, errors.New("invalid OpenAI usage response")
	}
	return *subscription.HardLimitUSD - *usage.TotalUsage/100, nil
}

func requiredProviderBalance(provider string, value *float64) (float64, error) {
	if value == nil || validateChannelBalance(*value) != nil {
		return 0, fmt.Errorf("invalid %s balance response", provider)
	}
	return *value, nil
}

func parseProviderBalanceText(provider, value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return 0, fmt.Errorf("invalid %s balance response", provider)
	}
	balance, err := strconv.ParseFloat(value, 64)
	if err != nil || validateChannelBalance(balance) != nil {
		return 0, fmt.Errorf("invalid %s balance response", provider)
	}
	return balance, nil
}

func validateChannelBalance(balance float64) error {
	if math.IsNaN(balance) || math.IsInf(balance, 0) || math.Abs(balance) > maxPersistedChannelBalance {
		return errors.New("invalid upstream balance")
	}
	return nil
}

func balanceGETContext(ctx context.Context, rawURL, key string) ([]byte, error) {
	return balanceRequest(ctx, rawURL, http.Header{"Authorization": []string{"Bearer " + firstKey(key)}})
}

func balanceRequest(ctx context.Context, rawURL string, headers http.Header) ([]byte, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("invalid upstream balance endpoint")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("invalid upstream balance endpoint")
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := channelUpstreamHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("upstream balance request failed: %w", context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("upstream balance request failed: %w", context.DeadlineExceeded)
		}
		return nil, errors.New("upstream balance request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned status %s", resp.Status)
	}
	body, err := common.ReadAllLimited(resp.Body, maxChannelUpstreamResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read upstream balance response: %w", err)
	}
	return body, nil
}
