package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
)

// VerifyTelegramLogin verifies the Telegram Login Widget hash. The bot token is
// read from the TELEGRAM_BOT_TOKEN env; when unset, verification fails.
func VerifyTelegramLogin(data map[string]string) bool {
	botToken := common.GetEnv("TELEGRAM_BOT_TOKEN", "")
	if botToken == "" {
		return false
	}
	return verifyTelegramHash(data, botToken)
}

// verifyTelegramHash implements Telegram's login data-check-string HMAC.
func verifyTelegramHash(data map[string]string, botToken string) bool {
	hash, ok := data["hash"]
	if !ok || hash == "" {
		return false
	}
	keys := make([]string, 0, len(data)-1)
	for k := range data {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(data[k])
		sb.WriteString("\n")
	}
	dcs := strings.TrimSuffix(sb.String(), "\n")

	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(dcs))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(hash))
}
