package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func telegramHash(data map[string]string, botToken string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k + "=" + data[k] + "\n")
	}
	dcs := strings.TrimSuffix(sb.String(), "\n")
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(dcs))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyTelegramLogin(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123456:ABC-DEF")
	data := map[string]string{
		"id":         "42",
		"first_name": "Alice",
		"username":   "alice",
		"auth_date":  "1700000000",
	}
	data["hash"] = telegramHash(data, "123456:ABC-DEF")
	assert.True(t, VerifyTelegramLogin(data))

	// Tampered data fails verification.
	data["first_name"] = "Bob"
	assert.False(t, VerifyTelegramLogin(data))
}

func TestVerifyTelegramLoginNoToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	assert.False(t, VerifyTelegramLogin(map[string]string{"hash": "x"}))
}
