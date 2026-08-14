package service

import (
	"encoding/base64"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Passkey auth-flow purposes.
const (
	PasskeyPurposeRegister = "passkey_register"
	PasskeyPurposeLogin    = "passkey_login"
)

// WebAuthn client instance (initialized from RP config).
var wa *webauthn.WebAuthn

// InitWebAuthn configures the WebAuthn relying party from environment/settings.
func InitWebAuthn() error {
	rpDisplayName := setting.GetOptionOrDefault("WebAuthnRPDisplayName", common.ProductName)
	rpID := setting.GetOptionOrDefault("WebAuthnRPID", "localhost")
	origins := common.GetEnvStrings("WEBAUTHN_ORIGINS")
	if len(origins) == 0 {
		origins = []string{"http://localhost:3000"}
	}
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: rpDisplayName,
		RPID:          rpID,
		RPOrigins:     origins,
	})
	if err != nil {
		return err
	}
	wa = w
	return nil
}

// webauthnUser adapts model.User to the webauthn.User interface.
type webauthnUser struct {
	*model.User
	credentials []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte { return []byte(common.Int2Str(u.Id)) }

func (u *webauthnUser) WebAuthnName() string { return u.Username }

func (u *webauthnUser) WebAuthnDisplayName() string { return u.DisplayName }

func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// loadPasskeyUser loads a user and their passkey credentials.
func loadPasskeyUser(userId int) (*webauthnUser, error) {
	user, err := GetUserByID(userId)
	if err != nil {
		return nil, err
	}
	var creds []model.PasskeyCredential
	if err := model.DB.Where("user_id = ?", userId).Find(&creds).Error; err != nil {
		return nil, err
	}
	wu := &webauthnUser{User: user}
	for i := range creds {
		c := creds[i]
		wu.credentials = append(wu.credentials, webauthn.Credential{
			ID:              decodeBase64(c.CredentialID),
			PublicKey:       decodeBase64(c.PublicKey),
			AttestationType: c.AttestationType,
			Authenticator: webauthn.Authenticator{
				AAGUID:       decodeBase64(c.AAGUID),
				SignCount:    c.SignCount,
				CloneWarning: c.CloneWarning,
			},
		})
	}
	return wu, nil
}

// BeginPasskeyRegistration generates credential-creation options and stores the
// session/challenge for the registration ceremony.
func BeginPasskeyRegistration(userId int) (*protocol.CredentialCreation, string, error) {
	if wa == nil {
		return nil, "", errors.New("WebAuthn not initialized")
	}
	wu, err := loadPasskeyUser(userId)
	if err != nil {
		return nil, "", err
	}
	options, sessionData, err := wa.BeginRegistration(wu)
	if err != nil {
		return nil, "", err
	}
	flowToken, err := storePasskeySession(PasskeyPurposeRegister, userId, sessionData)
	if err != nil {
		return nil, "", err
	}
	return options, flowToken, nil
}

// FinishPasskeyRegistration verifies the attestation and stores the credential.
func FinishPasskeyRegistration(userId int, flowToken string, response *protocol.ParsedCredentialCreationData) error {
	if wa == nil {
		return errors.New("WebAuthn not initialized")
	}
	sessionData, err := loadPasskeySession(flowToken, PasskeyPurposeRegister)
	if err != nil {
		return err
	}
	wu, err := loadPasskeyUser(userId)
	if err != nil {
		return err
	}
	credential, err := wa.CreateCredential(wu, *sessionData, response)
	if err != nil {
		return err
	}
	pc := model.PasskeyCredential{
		UserID:          userId,
		CredentialID:    encodeBase64(credential.ID),
		PublicKey:       encodeBase64(credential.PublicKey),
		AttestationType: credential.AttestationType,
		AAGUID:          encodeBase64(credential.Authenticator.AAGUID),
		SignCount:       credential.Authenticator.SignCount,
		Attachment:      response.Raw.AuthenticatorAttachment,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	// One passkey per user: replace any existing.
	_ = model.DB.Where("user_id = ?", userId).Delete(&model.PasskeyCredential{}).Error
	if err := model.DB.Create(&pc).Error; err != nil {
		return err
	}
	return nil
}

// BeginPasskeyLogin generates assertion options (discoverable login).
func BeginPasskeyLogin() (*protocol.CredentialAssertion, string, error) {
	if wa == nil {
		return nil, "", errors.New("WebAuthn not initialized")
	}
	options, sessionData, err := wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, "", err
	}
	flowToken, err := storePasskeySession(PasskeyPurposeLogin, 0, sessionData)
	if err != nil {
		return nil, "", err
	}
	return options, flowToken, nil
}

// FinishPasskeyLogin verifies the assertion and returns the authenticated user.
func FinishPasskeyLogin(flowToken string, response *protocol.ParsedCredentialAssertionData) (*model.User, error) {
	if wa == nil {
		return nil, errors.New("WebAuthn not initialized")
	}
	sessionData, err := loadPasskeySession(flowToken, PasskeyPurposeLogin)
	if err != nil {
		return nil, err
	}
	var pc model.PasskeyCredential
	if err := model.DB.Where("credential_id = ?", encodeBase64(response.RawID)).First(&pc).Error; err != nil {
		return nil, ErrInvalidCredentials
	}
	wu, err := loadPasskeyUser(pc.UserID)
	if err != nil {
		return nil, err
	}
	credential, err := wa.ValidateDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		return wu, nil
	}, *sessionData, response)
	if err != nil {
		return nil, err
	}
	_ = model.DB.Model(&model.PasskeyCredential{}).Where("id = ?", pc.ID).
		Updates(map[string]any{"sign_count": credential.Authenticator.SignCount, "last_used_at": time.Now()}).Error
	return wu.User, nil
}

// PasskeyEnabled reports whether the user has a registered passkey.
func PasskeyEnabled(userId int) bool {
	var count int64
	model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", userId).Count(&count)
	return count > 0
}

// PasskeyPurposeVerify is the step-up verification auth-flow purpose.
const PasskeyPurposeVerify = "passkey_verify"

// BeginPasskeyVerify starts a passkey step-up assertion for an authenticated
// user. The flow is bound to the user's current session and the requested
// proof scope.
func BeginPasskeyVerify(userId int, sessionId, scope string) (*protocol.CredentialAssertion, string, error) {
	if wa == nil {
		return nil, "", errors.New("WebAuthn not initialized")
	}
	wu, err := loadPasskeyUser(userId)
	if err != nil {
		return nil, "", err
	}
	options, sessionData, err := wa.BeginLogin(wu)
	if err != nil {
		return nil, "", err
	}
	payload, err := common.Marshal(sessionData)
	if err != nil {
		return nil, "", err
	}
	flowToken, err := CreateAuthFlow(PasskeyPurposeVerify, "passkey", scope, userId, sessionId, string(payload), 5*time.Minute)
	if err != nil {
		return nil, "", err
	}
	return options, flowToken, nil
}

// FinishPasskeyVerify validates the step-up assertion, requires the flow to
// belong to the same user and session that started it, and returns the scope
// the proof should carry.
func FinishPasskeyVerify(userId int, sessionId, flowToken string, response *protocol.ParsedCredentialAssertionData) (string, error) {
	if wa == nil {
		return "", errors.New("WebAuthn not initialized")
	}
	flow, err := ConsumeAuthFlow(flowToken, PasskeyPurposeVerify)
	if err != nil {
		return "", ErrInvalidFlowToken
	}
	if flow.UserId != userId || flow.SessionId != sessionId {
		return "", ErrInvalidFlowToken
	}
	var sd webauthn.SessionData
	if err := common.UnmarshalJsonStr(flow.Payload, &sd); err != nil {
		return "", err
	}
	wu, err := loadPasskeyUser(userId)
	if err != nil {
		return "", err
	}
	credential, err := wa.ValidateLogin(wu, sd, response)
	if err != nil {
		return "", err
	}
	_ = model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", userId).
		Updates(map[string]any{"sign_count": credential.Authenticator.SignCount, "last_used_at": time.Now()}).Error
	if flow.Intent == "" {
		return "", ErrInvalidFlowToken
	}
	return flow.Intent, nil
}

// storePasskeySession persists a WebAuthn session (challenge) in an AuthFlow.
func storePasskeySession(purpose string, userId int, sd *webauthn.SessionData) (string, error) {
	payload, err := common.Marshal(sd)
	if err != nil {
		return "", err
	}
	return CreateAuthFlow(purpose, "passkey", "", userId, "", string(payload), 5*time.Minute)
}

// loadPasskeySession consumes a WebAuthn session by its flow token.
func loadPasskeySession(flowToken, purpose string) (*webauthn.SessionData, error) {
	flow, err := ConsumeAuthFlow(flowToken, purpose)
	if err != nil {
		return nil, err
	}
	var sd webauthn.SessionData
	if err := common.UnmarshalJsonStr(flow.Payload, &sd); err != nil {
		return nil, err
	}
	return &sd, nil
}

func encodeBase64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeBase64(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(s)
	return b
}

// ListPasskeys returns the user's registered passkeys.
func ListPasskeys(userId int) ([]model.PasskeyCredential, error) {
	var creds []model.PasskeyCredential
	if err := model.DB.Where("user_id = ?", userId).Order("id desc").Find(&creds).Error; err != nil {
		return nil, err
	}
	return creds, nil
}

// DeleteAllPasskeys removes every passkey of a user.
func DeleteAllPasskeys(userId int) error {
	return model.DB.Where("user_id = ?", userId).Delete(&model.PasskeyCredential{}).Error
}
