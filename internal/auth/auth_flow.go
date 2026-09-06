package auth

import (
	"context"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
)

// Auth flow purposes.
const (
	AuthFlowPurposeLogin2FA     = "login_2fa"
	AuthFlowPurposeOAuth        = "oauth"
	AuthFlowPurposeTelegramBind = "telegram_bind"
)

// ErrInvalidFlowToken is returned when an auth flow is unknown, consumed, or expired.
var ErrInvalidFlowToken = errors.New("invalid flow token")

const (
	authFlowTokenBytes       = 64
	maxAuthFlowPurposeBytes  = 32
	maxAuthFlowProviderBytes = 64
	maxAuthFlowIntentBytes   = 128
	maxAuthFlowSessionBytes  = 64
	maxAuthFlowPayloadBytes  = 64 << 10
	maxAuthFlowTTL           = 24 * time.Hour
)

func validAuthFlowToken(token string) bool {
	if len(token) != authFlowTokenBytes {
		return false
	}
	for _, character := range token {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}

// AuthFlowMatch identifies every immutable field that binds an auth flow to
// its ceremony. ConsumeAuthFlowExact enforces all fields, including zero and
// empty values, so a login flow cannot be reused as a user/session-bound flow
// (or vice versa).
type AuthFlowMatch struct {
	Purpose   string
	Provider  string
	Intent    string
	UserId    int
	SessionId string
}

// CreateAuthFlow creates a short-lived one-time auth ceremony state and returns
// the opaque flow token (only its hash is persisted).
func CreateAuthFlow(purpose, provider, intent string, userId int, sessionId, payload string, ttl time.Duration) (string, error) {
	token, _, err := CreateAuthFlowWithExpiry(purpose, provider, intent, userId, sessionId, payload, ttl)
	return token, err
}

// CreateAuthFlowWithExpiry also returns the absolute primary-database expiry
// used for user-facing ceremony metadata.
func CreateAuthFlowWithExpiry(purpose, provider, intent string, userId int, sessionId, payload string, ttl time.Duration) (string, int64, error) {
	if purpose != strings.TrimSpace(purpose) || provider != strings.TrimSpace(provider) ||
		intent != strings.TrimSpace(intent) || sessionId != strings.TrimSpace(sessionId) ||
		!validOAuthText(purpose, maxAuthFlowPurposeBytes, false) ||
		!validOAuthText(provider, maxAuthFlowProviderBytes, false) ||
		!validOAuthText(intent, maxAuthFlowIntentBytes, true) ||
		!validOAuthText(sessionId, maxAuthFlowSessionBytes, true) ||
		!validOAuthText(payload, maxAuthFlowPayloadBytes, true) ||
		userId < 0 || ttl <= 0 || ttl > maxAuthFlowTTL {
		return "", 0, ErrInvalidFlowToken
	}
	nowUnix, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return "", 0, err
	}
	token, err := cryptoutil.SecureRandomAlphanumeric(64)
	if err != nil {
		return "", 0, err
	}
	createdAt := time.Unix(nowUnix, 0).UTC()
	expiresAt := createdAt.Add(ttl)
	flow := model.AuthFlow{
		TokenHash: cryptoutil.SHA256Hex(token),
		Purpose:   purpose,
		Provider:  provider,
		Intent:    intent,
		UserId:    userId,
		SessionId: sessionId,
		Payload:   payload,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}
	if err := model.DB.Create(&flow).Error; err != nil {
		return "", 0, err
	}
	return token, expiresAt.Unix(), nil
}

// PeekAuthFlow validates a flow without consuming it. It is intended for
// redirect callbacks that must validate state and session binding before a
// potentially transient provider exchange. Callers must later use an atomic
// consume operation before applying the authenticated identity.
func PeekAuthFlow(token, purpose string) (*model.AuthFlow, error) {
	if !validAuthFlowToken(token) || purpose == "" || len(purpose) > maxAuthFlowPurposeBytes {
		return nil, ErrInvalidFlowToken
	}
	nowUnix, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return nil, err
	}
	now := time.Unix(nowUnix, 0).UTC()
	var flow model.AuthFlow
	if err := model.DB.Where(
		"token_hash = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?",
		cryptoutil.SHA256Hex(token), purpose, now,
	).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidFlowToken
		}
		return nil, err
	}
	return &flow, nil
}

// ConsumeAuthFlowExact atomically consumes a flow only when every ceremony
// field matches. Concurrent callbacks may both finish their provider request,
// but only one can win the conditional consumed_at update.
func ConsumeAuthFlowExact(token string, match AuthFlowMatch) (*model.AuthFlow, error) {
	return ConsumeAuthFlowExactWithAction(token, match, nil)
}

// ConsumeAuthFlowExactWithAction consumes an exactly bound ceremony and
// commits the caller's identity mutation in the same transaction.
func ConsumeAuthFlowExactWithAction(
	token string,
	match AuthFlowMatch,
	action func(*gorm.DB, *model.AuthFlow) error,
) (*model.AuthFlow, error) {
	if !validAuthFlowToken(token) || match.Purpose == "" || match.UserId < 0 ||
		!validOAuthText(match.Purpose, maxAuthFlowPurposeBytes, false) ||
		!validOAuthText(match.Provider, maxAuthFlowProviderBytes, true) ||
		!validOAuthText(match.Intent, maxAuthFlowIntentBytes, true) ||
		!validOAuthText(match.SessionId, maxAuthFlowSessionBytes, true) {
		return nil, ErrInvalidFlowToken
	}
	var flow model.AuthFlow
	if err := model.DB.Where(
		"token_hash = ? AND purpose = ? AND provider = ? AND intent = ? AND user_id = ? AND session_id = ?",
		cryptoutil.SHA256Hex(token), match.Purpose, match.Provider, match.Intent, match.UserId, match.SessionId,
	).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidFlowToken
		}
		return nil, err
	}
	return consumeAuthFlowRecordWithExactAction(&flow, &match, action)
}

// ConsumeAuthFlow validates and atomically single-use-consumes an auth flow
// token. Concurrent callers cannot both succeed.
func ConsumeAuthFlow(token, purpose string) (*model.AuthFlow, error) {
	return ConsumeAuthFlowWithAction(token, purpose, nil)
}

// ConsumeAuthFlowWithAction consumes a flow and performs action in the same
// transaction. If action fails, consumption is rolled back so callers can
// safely retry after a transient downstream failure.
func ConsumeAuthFlowWithAction(
	token string,
	purpose string,
	action func(*gorm.DB, *model.AuthFlow) error,
) (*model.AuthFlow, error) {
	if !validAuthFlowToken(token) || purpose == "" || len(purpose) > maxAuthFlowPurposeBytes {
		return nil, ErrInvalidFlowToken
	}
	hash := cryptoutil.SHA256Hex(token)
	var flow model.AuthFlow
	if err := model.DB.Where("token_hash = ? AND purpose = ?", hash, purpose).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidFlowToken
		}
		return nil, err
	}
	return consumeAuthFlowRecordWithExactAction(&flow, nil, action)
}

func consumeAuthFlowRecordWithAction(
	flow *model.AuthFlow,
	action func(*gorm.DB, *model.AuthFlow) error,
) (*model.AuthFlow, error) {
	return consumeAuthFlowRecordWithExactAction(flow, nil, action)
}

func consumeAuthFlowRecordWithExactAction(
	flow *model.AuthFlow,
	match *AuthFlowMatch,
	action func(*gorm.DB, *model.AuthFlow) error,
) (*model.AuthFlow, error) {
	if flow == nil || flow.Id == 0 {
		return nil, ErrInvalidFlowToken
	}

	consumed := *flow
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		nowUnix, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		now := time.Unix(nowUnix, 0).UTC()
		query := tx.Model(&model.AuthFlow{}).
			Where("id = ? AND consumed_at IS NULL AND expires_at > ?", flow.Id, now)
		if match != nil {
			query = query.Where(
				"token_hash = ? AND purpose = ? AND provider = ? AND intent = ? AND user_id = ? AND session_id = ?",
				flow.TokenHash, match.Purpose, match.Provider, match.Intent, match.UserId, match.SessionId,
			)
		}
		result := query.Update("consumed_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidFlowToken
		}

		consumed.ConsumedAt = &now
		if action != nil {
			if err := action(tx, &consumed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &consumed, nil
}
