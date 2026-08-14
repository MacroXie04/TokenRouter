package service

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

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
	if resp.StatusCode >= 400 {
		return 0, errors.New("balance endpoint returned status " + http.StatusText(resp.StatusCode))
	}
	body, _ := io.ReadAll(resp.Body)
	balance := parseBalance(body)
	_ = model.DB.Model(channel).Updates(map[string]any{
		"balance":             balance,
		"balance_updated_time": common.NowTimestamp(),
	}).Error
	return balance, nil
}

// balanceURL reads the balance endpoint from a channel's Setting JSON.
func balanceURL(channel *model.Channel) string {
	if channel == nil || channel.Setting == "" {
		return ""
	}
	var s struct {
		BalanceURL string `json:"balance_url"`
	}
	if err := common.UnmarshalJsonStr(channel.Setting, &s); err != nil {
		return ""
	}
	return s.BalanceURL
}

// parseBalance extracts a numeric balance from common provider response shapes.
func parseBalance(body []byte) float64 {
	var m map[string]any
	if err := common.Unmarshal(body, &m); err != nil {
		return 0
	}
	for _, key := range []string{"balance", "total_available", "available_balance", "credit_balance", "remaining"} {
		if v, ok := m[key]; ok {
			if f, ok := toFloat(v); ok {
				return f
			}
		}
	}
	// Nested {"data": {"balance": x}}.
	if data, ok := m["data"].(map[string]any); ok {
		for _, key := range []string{"balance", "total_available", "available_balance"} {
			if v, ok := data[key]; ok {
				if f, ok := toFloat(v); ok {
					return f
				}
			}
		}
	}
	return 0
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
