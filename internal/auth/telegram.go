package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxTelegramLoginFields     = 16
	maxTelegramFieldKeyBytes   = 64
	maxTelegramFieldValueBytes = 4096
	maxTelegramUserIDBytes     = 64
	maxTelegramBotTokenBytes   = 512
	telegramLoginMaxAge        = 5 * time.Minute
	telegramLoginFutureSkew    = 5 * time.Minute
	telegramBotNameOption      = "TelegramBotName"
	telegramBotTokenOption     = "TelegramBotToken"
)

// TelegramBotName returns the normalized public bot username only when it is
// a valid Telegram bot identity. The Login Widget expects the name without a
// leading @.
func TelegramBotName() string {
	name := env.GetEnv("TELEGRAM_BOT_NAME", setting.GetOptionOrDefault(telegramBotNameOption, ""))
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if len(name) < 5 || len(name) > 32 || !strings.EqualFold(name[len(name)-3:], "bot") {
		return ""
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return ""
	}
	return name
}

func telegramBotToken() string {
	return env.GetEnv("TELEGRAM_BOT_TOKEN", setting.GetOptionOrDefault(telegramBotTokenOption, ""))
}

func validTelegramBotToken(token string) bool {
	if token == "" || token != strings.TrimSpace(token) ||
		!validOAuthText(token, maxTelegramBotTokenBytes, false) {
		return false
	}
	parts := strings.Split(token, ":")
	if len(parts) != 2 || len(parts[0]) < 5 || len(parts[0]) > 20 || len(parts[1]) < 20 || len(parts[1]) > 128 {
		return false
	}
	for _, character := range parts[0] {
		if character < '0' || character > '9' {
			return false
		}
	}
	botID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || botID == 0 {
		return false
	}
	for _, character := range parts[1] {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

// TelegramOAuthEnabled is the single use-boundary gate for public status and
// login/bind handlers. Enabling the option without a valid bot name and token
// remains safely disabled.
func TelegramOAuthEnabled() bool {
	return setting.GetOptionBool(setting.TelegramOAuthEnabledOption, false) &&
		TelegramBotName() != "" && validTelegramBotToken(telegramBotToken())
}

// VerifyTelegramLogin verifies the Telegram Login Widget hash. The bot token is
// read from the TELEGRAM_BOT_TOKEN env; when unset, verification fails.
func VerifyTelegramLogin(data map[string]string) bool {
	if !TelegramOAuthEnabled() {
		return false
	}
	return verifyTelegramLoginAt(data, telegramBotToken(), time.Now())
}

func verifyTelegramLoginAt(data map[string]string, botToken string, now time.Time) bool {
	if len(data) < 3 || len(data) > maxTelegramLoginFields ||
		!validTelegramBotToken(botToken) {
		return false
	}
	for key, value := range data {
		if !validOAuthText(key, maxTelegramFieldKeyBytes, false) ||
			!validOAuthText(value, maxTelegramFieldValueBytes, true) {
			return false
		}
	}
	userID := data["id"]
	if !validOAuthText(userID, maxTelegramUserIDBytes, false) {
		return false
	}
	for _, character := range userID {
		if character < '0' || character > '9' {
			return false
		}
	}
	authDateText := data["auth_date"]
	if authDateText == "" || len(authDateText) > 20 {
		return false
	}
	authDateUnix, err := strconv.ParseInt(authDateText, 10, 64)
	if err != nil || authDateUnix <= 0 {
		return false
	}
	authDate := time.Unix(authDateUnix, 0)
	if authDate.Before(now.Add(-telegramLoginMaxAge)) || authDate.After(now.Add(telegramLoginFutureSkew)) {
		return false
	}
	return verifyTelegramHash(data, botToken)
}

// verifyTelegramHash implements Telegram's login data-check-string HMAC.
func verifyTelegramHash(data map[string]string, botToken string) bool {
	hash, ok := data["hash"]
	if !ok || len(hash) != sha256.Size*2 {
		return false
	}
	provided, err := hex.DecodeString(hash)
	if err != nil || len(provided) != sha256.Size {
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
	_, _ = mac.Write([]byte(dcs))
	return hmac.Equal(mac.Sum(nil), provided)
}
