package service

import (
	"errors"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Auth flow purposes.
const (
	AuthFlowPurposeLogin2FA = "login_2fa"
	AuthFlowPurposeOAuth    = "oauth"
)

// ErrInvalidFlowToken is returned when an auth flow is unknown, consumed, or expired.
var ErrInvalidFlowToken = errors.New("invalid flow token")

// CreateAuthFlow creates a short-lived one-time auth ceremony state and returns
// the opaque flow token (only its hash is persisted).
func CreateAuthFlow(purpose, provider, intent string, userId int, sessionId, payload string, ttl time.Duration) (string, error) {
	token := common.RandomAlphanumeric(64)
	flow := model.AuthFlow{
		TokenHash: common.SHA256Hex(token),
		Purpose:   purpose,
		Provider:  provider,
		Intent:    intent,
		UserId:    userId,
		SessionId: sessionId,
		Payload:   payload,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(ttl),
	}
	if err := model.DB.Create(&flow).Error; err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeAuthFlow validates and single-use-consumes an auth flow token.
func ConsumeAuthFlow(token, purpose string) (*model.AuthFlow, error) {
	hash := common.SHA256Hex(token)
	var flow model.AuthFlow
	if err := model.DB.Where("token_hash = ? AND purpose = ?", hash, purpose).First(&flow).Error; err != nil {
		return nil, ErrInvalidFlowToken
	}
	if flow.ConsumedAt != nil {
		return nil, ErrInvalidFlowToken
	}
	if time.Now().After(flow.ExpiresAt) {
		return nil, ErrInvalidFlowToken
	}
	now := time.Now()
	if err := model.DB.Model(&flow).Update("consumed_at", &now).Error; err != nil {
		return nil, err
	}
	return &flow, nil
}
