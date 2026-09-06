package channels

import (
	"errors"
	"fmt"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const maxBalanceResponseBytes = int64(1 << 20)
const maxBalanceURLBytes = 4096

// UpdateChannelBalance queries a channel's balance endpoint (if configured in
// its Setting JSON as {"balance_url": "..."}) and updates the channel's balance
// and timestamp. Channels without a balance_url are left unchanged.
func UpdateChannelBalance(channelId int) (float64, error) {
	channel, err := GetChannelByID(channelId)
	if err != nil {
		return 0, err
	}
	url := balanceURL(channel)
	if url == "" {
		return channel.Balance, errors.New("channel has no balance_url configured")
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+GetChannelKey(channel))
	resp, err := healthHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, errors.New("balance endpoint returned status " + http.StatusText(resp.StatusCode))
	}
	body, err := httpx.ReadAllLimited(resp.Body, maxBalanceResponseBytes)
	if err != nil {
		return 0, fmt.Errorf("read balance response: %w", err)
	}
	balance, err := parseBalanceStrict(body)
	if err != nil {
		return 0, err
	}
	if err := model.DB.Model(channel).Updates(map[string]any{
		"balance":              balance,
		"balance_updated_time": wallclock.NowTimestamp(),
	}).Error; err != nil {
		return 0, fmt.Errorf("persist channel balance: %w", err)
	}
	return balance, nil
}

// balanceURL reads the balance endpoint from a channel's Setting JSON.
func balanceURL(channel *model.Channel) string {
	balanceURL, _ := balanceURLChecked(channel)
	return balanceURL
}

// balanceURLChecked distinguishes a missing optional override from a malformed
// setting. Callers that can fall back to a provider default must use this form
// so an invalid override never redirects a credential to a different host.
func balanceURLChecked(channel *model.Channel) (string, error) {
	if channel == nil || strings.TrimSpace(channel.Setting) == "" {
		return "", nil
	}
	var values map[string]any
	if err := jsonutil.UnmarshalJsonStr(channel.Setting, &values); err != nil || values == nil {
		return "", errors.New("invalid channel balance setting")
	}
	raw, present := values["balance_url"]
	if !present {
		return "", nil
	}
	configuredURL, ok := raw.(string)
	if !ok {
		return "", errors.New("invalid channel balance setting")
	}
	configuredURL = strings.TrimSpace(configuredURL)
	if len(configuredURL) > maxBalanceURLBytes {
		return "", errors.New("invalid channel balance setting")
	}
	return configuredURL, nil
}

// parseBalance extracts a numeric balance from common provider response shapes.
func parseBalance(body []byte) float64 {
	balance, _ := parseBalanceStrict(body)
	return balance
}

func parseBalanceStrict(body []byte) (float64, error) {
	var m map[string]any
	if err := jsonutil.Unmarshal(body, &m); err != nil {
		return 0, errors.New("invalid balance response")
	}
	for _, key := range []string{"balance", "total_available", "available_balance", "credit_balance", "remaining"} {
		if v, ok := m[key]; ok {
			if f, ok := toFloat(v); ok {
				return f, nil
			}
			return 0, errors.New("invalid balance response")
		}
	}
	// Nested {"data": {"balance": x}}.
	if data, ok := m["data"].(map[string]any); ok {
		for _, key := range []string{"balance", "total_available", "available_balance"} {
			if v, ok := data[key]; ok {
				if f, ok := toFloat(v); ok {
					return f, nil
				}
				return 0, errors.New("invalid balance response")
			}
		}
	}
	return 0, errors.New("balance value is missing")
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		if !math.IsNaN(t) && !math.IsInf(t, 0) && math.Abs(t) <= maxPersistedChannelBalance {
			return t, true
		}
		return 0, false
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) <= maxPersistedChannelBalance {
			return f, true
		}
	}
	return 0, false
}
