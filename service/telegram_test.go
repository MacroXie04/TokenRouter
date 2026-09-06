package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/setting"
)

const telegramTestBotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"

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
	initOAuthDB(t)
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.TelegramOAuthEnabledOption: "true",
		telegramBotNameOption:              "sample_bot",
	}))
	t.Setenv("TELEGRAM_BOT_TOKEN", telegramTestBotToken)
	data := map[string]string{
		"id":         "42",
		"first_name": "Alice",
		"username":   "alice",
		"auth_date":  strconv.FormatInt(time.Now().Unix(), 10),
	}
	data["hash"] = telegramHash(data, telegramTestBotToken)
	assert.True(t, VerifyTelegramLogin(data))

	// Tampered data fails verification.
	data["first_name"] = "Bob"
	assert.False(t, VerifyTelegramLogin(data))

	// The signature alone is insufficient when the use-boundary option is off
	// or the public bot identity is malformed.
	require.NoError(t, setting.UpdateOption(setting.TelegramOAuthEnabledOption, "false"))
	assert.False(t, VerifyTelegramLogin(data))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.TelegramOAuthEnabledOption: "true",
		telegramBotNameOption:              "not-a-bot!",
	}))
	assert.False(t, TelegramOAuthEnabled())
}

func TestVerifyTelegramLoginNoToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	assert.False(t, VerifyTelegramLogin(map[string]string{"hash": "x"}))
}

func TestTelegramLoginRejectsReplayAndUnboundedFields(t *testing.T) {
	const botToken = telegramTestBotToken
	now := time.Unix(1_900_000_000, 0)
	valid := func(authDate string) map[string]string {
		data := map[string]string{
			"id": "9007199254740993", "username": "alice", "auth_date": authDate,
		}
		data["hash"] = telegramHash(data, botToken)
		return data
	}

	assert.True(t, verifyTelegramLoginAt(valid(strconv.FormatInt(now.Unix(), 10)), botToken, now))
	assert.False(t, verifyTelegramLoginAt(
		valid(strconv.FormatInt(now.Add(-telegramLoginMaxAge-time.Second).Unix(), 10)), botToken, now),
		"an old signed assertion must not remain a reusable login credential",
	)
	assert.False(t, verifyTelegramLoginAt(
		valid(strconv.FormatInt(now.Add(telegramLoginFutureSkew+time.Second).Unix(), 10)), botToken, now),
	)

	badID := valid(strconv.FormatInt(now.Unix(), 10))
	badID["id"] = "42x"
	badID["hash"] = telegramHash(badID, botToken)
	assert.False(t, verifyTelegramLoginAt(badID, botToken, now))

	oversized := valid(strconv.FormatInt(now.Unix(), 10))
	oversized["photo_url"] = strings.Repeat("p", maxTelegramFieldValueBytes+1)
	oversized["hash"] = telegramHash(oversized, botToken)
	assert.False(t, verifyTelegramLoginAt(oversized, botToken, now))

	tooMany := valid(strconv.FormatInt(now.Unix(), 10))
	for index := len(tooMany); index <= maxTelegramLoginFields; index++ {
		tooMany["field"+strconv.Itoa(index)] = "value"
	}
	tooMany["hash"] = telegramHash(tooMany, botToken)
	assert.False(t, verifyTelegramLoginAt(tooMany, botToken, now))

	badHash := valid(strconv.FormatInt(now.Unix(), 10))
	badHash["hash"] = strings.Repeat("z", sha256.Size*2)
	assert.False(t, verifyTelegramLoginAt(badHash, botToken, now))
}
