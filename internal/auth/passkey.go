package auth

import (
	"encoding/base64"
	"errors"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// Passkey auth-flow purposes.
const (
	PasskeyPurposeRegister = "passkey_register"
	PasskeyPurposeLogin    = "passkey_login"
)

var ErrPasskeyCloneDetected = errors.New("passkey clone warning detected")

// InitWebAuthn validates the initial WebAuthn relying-party snapshot. New
// ceremonies build from the latest atomic setting snapshot so a committed
// admin update or remote Sync takes effect without a process restart.
func InitWebAuthn() error {
	_, _, err := currentWebAuthn()
	return err
}

// EffectivePasskeySetting resolves deployment origin overrides and safe
// defaults, returning exactly the relying-party metadata used by ceremonies.
func EffectivePasskeySetting() (setting.PasskeySetting, error) {
	_, configured, err := currentWebAuthn()
	return configured, err
}

func currentWebAuthn() (*webauthn.WebAuthn, setting.PasskeySetting, error) {
	configured := setting.GetAuthenticationSetting().Passkey
	origins := append([]string(nil), configured.Origins...)
	if raw := strings.TrimSpace(os.Getenv("WEBAUTHN_ORIGINS")); raw != "" {
		parsed, err := setting.ParsePasskeyOrigins(raw, configured.AllowInsecureOrigin)
		if err != nil {
			return nil, configured, err
		}
		origins = parsed
	}
	if len(origins) == 0 {
		if address := strings.TrimSpace(setting.GetOption(setting.ServerAddressOption)); address != "" {
			parsed, err := setting.ParsePasskeyOrigins(address, configured.AllowInsecureOrigin)
			if err != nil {
				return nil, configured, err
			}
			origins = parsed
		}
	}
	if len(origins) == 0 {
		// Keep the historical loopback development default. Plaintext is never
		// inferred for a non-loopback host.
		origins = []string{"http://localhost:3000"}
	}

	rpID := configured.RPID
	if rpID == "" {
		parsed, err := url.Parse(origins[0])
		if err != nil || parsed.Hostname() == "" {
			return nil, configured, errors.New("passkey relying-party ID cannot be derived")
		}
		rpID = strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || !passkeyOriginMatchesRPID(parsed.Hostname(), rpID) {
			return nil, configured, errors.New("passkey origin is not within the relying-party ID")
		}
	}
	displayName := configured.RPDisplayName
	if displayName == "" {
		displayName = setting.GetSiteName()
	}
	selection := protocol.AuthenticatorSelection{
		ResidentKey:             protocol.ResidentKeyRequirementRequired,
		RequireResidentKey:      protocol.ResidentKeyRequired(),
		UserVerification:        protocol.UserVerificationRequirement(configured.UserVerification),
		AuthenticatorAttachment: protocol.AuthenticatorAttachment(configured.AttachmentPreference),
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName:          displayName,
		RPID:                   rpID,
		RPOrigins:              origins,
		AuthenticatorSelection: selection,
	})
	if err != nil {
		return nil, configured, err
	}
	configured.RPID = rpID
	configured.RPDisplayName = displayName
	configured.Origins = origins
	return wa, configured, nil
}

func passkeyOriginMatchesRPID(originHost, rpID string) bool {
	originHost = strings.TrimSuffix(strings.ToLower(originHost), ".")
	rpID = strings.TrimSuffix(strings.ToLower(rpID), ".")
	if originHost == "" || rpID == "" {
		return false
	}
	if originIP, rpIP := net.ParseIP(originHost), net.ParseIP(rpID); originIP != nil || rpIP != nil {
		return originIP != nil && rpIP != nil && originIP.Equal(rpIP)
	}
	return originHost == rpID || strings.HasSuffix(originHost, "."+rpID)
}

// webauthnUser adapts model.User to the webauthn.User interface.
type webauthnUser struct {
	*model.User
	credentials []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte { return []byte(textutil.Int2Str(u.Id)) }

func (u *webauthnUser) WebAuthnName() string { return u.Username }

func (u *webauthnUser) WebAuthnDisplayName() string { return u.DisplayName }

func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// loadPasskeyUser loads a user and their passkey credentials.
func loadPasskeyUser(userId int) (*webauthnUser, error) {
	user, err := userssvc.GetUserByID(userId)
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
			Transport:       decodePasskeyTransports(c.Transports),
			Flags: webauthn.CredentialFlags{
				UserPresent:    c.UserPresent,
				UserVerified:   c.UserVerified,
				BackupEligible: c.BackupEligible,
				BackupState:    c.BackupState,
			},
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
func BeginPasskeyRegistration(userId int, sessionId string) (*protocol.CredentialCreation, string, error) {
	wa, _, err := currentWebAuthn()
	if err != nil {
		return nil, "", err
	}
	if userId <= 0 || sessionId == "" {
		return nil, "", ErrSessionRevoked
	}
	wu, err := loadPasskeyUser(userId)
	if err != nil {
		return nil, "", err
	}
	options, sessionData, err := wa.BeginRegistration(wu)
	if err != nil {
		return nil, "", err
	}
	flowToken, err := storePasskeySession(PasskeyPurposeRegister, userId, sessionId, sessionData)
	if err != nil {
		return nil, "", err
	}
	return options, flowToken, nil
}

// FinishPasskeyRegistration verifies the attestation and stores the credential.
func FinishPasskeyRegistration(userId int, sessionId, flowToken string, response *protocol.ParsedCredentialCreationData) error {
	wa, _, err := currentWebAuthn()
	if err != nil {
		return err
	}
	if userId <= 0 || sessionId == "" {
		return ErrSessionRevoked
	}
	sessionData, err := loadPasskeySession(flowToken, PasskeyPurposeRegister, userId, sessionId)
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
		CloneWarning:    credential.Authenticator.CloneWarning,
		UserPresent:     credential.Flags.UserPresent,
		UserVerified:    credential.Flags.UserVerified,
		BackupEligible:  credential.Flags.BackupEligible,
		BackupState:     credential.Flags.BackupState,
		Transports:      passkeyTransports(credential),
		Attachment:      response.Raw.AuthenticatorAttachment,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	return replacePasskeyCredentialAndRotateSession(&pc, sessionId)
}

// BeginPasskeyLogin generates assertion options (discoverable login).
func BeginPasskeyLogin() (*protocol.CredentialAssertion, string, error) {
	options, flowToken, _, err := BeginPasskeyLoginWithExpiry()
	return options, flowToken, err
}

// BeginPasskeyLoginWithExpiry returns the exact database-clock expiry stored
// with the discoverable-login challenge.
func BeginPasskeyLoginWithExpiry() (*protocol.CredentialAssertion, string, int64, error) {
	wa, configured, err := currentWebAuthn()
	if err != nil {
		return nil, "", 0, err
	}
	options, sessionData, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.UserVerificationRequirement(configured.UserVerification)),
	)
	if err != nil {
		return nil, "", 0, err
	}
	flowToken, expiresAt, err := storePasskeySessionWithExpiry(PasskeyPurposeLogin, 0, "", sessionData)
	if err != nil {
		return nil, "", 0, err
	}
	return options, flowToken, expiresAt, nil
}

// FinishPasskeyLogin verifies the assertion and returns the authenticated user.
func FinishPasskeyLogin(flowToken string, response *protocol.ParsedCredentialAssertionData) (*model.User, error) {
	wa, _, err := currentWebAuthn()
	if err != nil {
		return nil, err
	}
	sessionData, err := loadPasskeySession(flowToken, PasskeyPurposeLogin, 0, "")
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
	if err := persistPasskeyUse(pc.UserID, credential); err != nil {
		return nil, err
	}
	return wu.User, nil
}

// PasskeyEnabledChecked reports whether the user has a registered passkey and
// distinguishes an empty credential set from a storage failure.
func PasskeyEnabledChecked(userId int) (bool, error) {
	if model.DB == nil {
		return false, errors.New("database is nil")
	}
	var count int64
	if err := model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", userId).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// PasskeyEnabled is the compatibility form for non-authorizing status
// displays. Security decisions must use PasskeyEnabledChecked.
func PasskeyEnabled(userId int) bool {
	enabled, err := PasskeyEnabledChecked(userId)
	if err != nil {
		logging.SysError("query passkey status failed: " + err.Error())
		return false
	}
	return enabled
}

// PasskeyPurposeVerify is the step-up verification auth-flow purpose.
const PasskeyPurposeVerify = "passkey_verify"

// BeginPasskeyVerify starts a passkey step-up assertion for an authenticated
// user. The flow is bound to the user's current session and the requested
// proof scope.
func BeginPasskeyVerify(userId int, sessionId, scope string) (*protocol.CredentialAssertion, string, error) {
	options, flowToken, _, err := BeginPasskeyVerifyWithExpiry(userId, sessionId, scope)
	return options, flowToken, err
}

// BeginPasskeyVerifyWithExpiry returns the exact database-clock expiry stored
// with the session-bound assertion challenge.
func BeginPasskeyVerifyWithExpiry(userId int, sessionId, scope string) (*protocol.CredentialAssertion, string, int64, error) {
	wa, configured, err := currentWebAuthn()
	if err != nil {
		return nil, "", 0, err
	}
	wu, err := loadPasskeyUser(userId)
	if err != nil {
		return nil, "", 0, err
	}
	options, sessionData, err := wa.BeginLogin(
		wu,
		webauthn.WithUserVerification(protocol.UserVerificationRequirement(configured.UserVerification)),
	)
	if err != nil {
		return nil, "", 0, err
	}
	payload, err := jsonutil.Marshal(sessionData)
	if err != nil {
		return nil, "", 0, err
	}
	flowToken, expiresAt, err := CreateAuthFlowWithExpiry(
		PasskeyPurposeVerify, "passkey", scope, userId, sessionId, string(payload), 5*time.Minute,
	)
	if err != nil {
		return nil, "", 0, err
	}
	return options, flowToken, expiresAt, nil
}

// FinishPasskeyVerify validates the step-up assertion, requires the flow to
// belong to the same user and session that started it, and returns the scope
// the proof should carry.
func FinishPasskeyVerify(userId int, sessionId, flowToken string, response *protocol.ParsedCredentialAssertionData) (string, error) {
	wa, _, err := currentWebAuthn()
	if err != nil {
		return "", err
	}
	flow, err := consumeAuthFlowMatching(flowToken, PasskeyPurposeVerify, userId, sessionId)
	if err != nil {
		return "", ErrInvalidFlowToken
	}
	var sd webauthn.SessionData
	if err := jsonutil.UnmarshalJsonStr(flow.Payload, &sd); err != nil {
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
	if err := persistPasskeyUse(userId, credential); err != nil {
		return "", err
	}
	if flow.Intent == "" {
		return "", ErrInvalidFlowToken
	}
	return flow.Intent, nil
}

func passkeyTransports(credential *webauthn.Credential) string {
	if credential == nil || len(credential.Transport) == 0 {
		return ""
	}
	values := make([]string, 0, len(credential.Transport))
	for _, transport := range credential.Transport {
		values = append(values, string(transport))
	}
	return strings.Join(values, ",")
}

func decodePasskeyTransports(encoded string) []protocol.AuthenticatorTransport {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	parts := strings.Split(encoded, ",")
	transports := make([]protocol.AuthenticatorTransport, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			transports = append(transports, protocol.AuthenticatorTransport(part))
		}
	}
	return transports
}

func replacePasskeyCredential(credential *model.PasskeyCredential) error {
	if credential == nil || credential.UserID <= 0 || credential.CredentialID == "" || credential.PublicKey == "" {
		return errors.New("invalid passkey credential")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("user_id = ?", credential.UserID).Delete(&model.PasskeyCredential{}).Error; err != nil {
			return err
		}
		return tx.Create(credential).Error
	})
}

// replacePasskeyCredentialAndRotateSession commits the new authentication
// factor and account-version advance together. Every old access token becomes
// stale immediately while the browser session that completed the ceremony can
// refresh into the new version.
func replacePasskeyCredentialAndRotateSession(credential *model.PasskeyCredential, keepSid string) error {
	if credential == nil || credential.UserID <= 0 || credential.CredentialID == "" ||
		credential.PublicKey == "" || keepSid == "" {
		return errors.New("invalid passkey credential")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("user_id = ?", credential.UserID).Delete(&model.PasskeyCredential{}).Error; err != nil {
			return err
		}
		if err := tx.Create(credential).Error; err != nil {
			return err
		}
		return bumpAuthVersionKeepSession(tx, credential.UserID, keepSid)
	})
}

func persistPasskeyUse(userId int, credential *webauthn.Credential) error {
	if userId <= 0 || credential == nil || len(credential.ID) == 0 {
		return errors.New("invalid passkey authentication state")
	}
	result := model.DB.Model(&model.PasskeyCredential{}).
		Where("user_id = ? AND credential_id = ?", userId, encodeBase64(credential.ID)).
		Updates(map[string]any{
			"sign_count":      credential.Authenticator.SignCount,
			"clone_warning":   credential.Authenticator.CloneWarning,
			"user_present":    credential.Flags.UserPresent,
			"user_verified":   credential.Flags.UserVerified,
			"backup_eligible": credential.Flags.BackupEligible,
			"backup_state":    credential.Flags.BackupState,
			"last_used_at":    time.Now(),
			"updated_at":      time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("passkey credential no longer exists")
	}
	if credential.Authenticator.CloneWarning {
		return ErrPasskeyCloneDetected
	}
	return nil
}

// storePasskeySession persists a WebAuthn session (challenge) in an AuthFlow.
func storePasskeySession(purpose string, userId int, sessionId string, sd *webauthn.SessionData) (string, error) {
	token, _, err := storePasskeySessionWithExpiry(purpose, userId, sessionId, sd)
	return token, err
}

func storePasskeySessionWithExpiry(purpose string, userId int, sessionId string, sd *webauthn.SessionData) (string, int64, error) {
	payload, err := jsonutil.Marshal(sd)
	if err != nil {
		return "", 0, err
	}
	return CreateAuthFlowWithExpiry(purpose, "passkey", "", userId, sessionId, string(payload), 5*time.Minute)
}

// loadPasskeySession consumes a WebAuthn session by its flow token.
func loadPasskeySession(flowToken, purpose string, userId int, sessionId string) (*webauthn.SessionData, error) {
	flow, err := consumeAuthFlowMatching(flowToken, purpose, userId, sessionId)
	if err != nil {
		return nil, err
	}
	var sd webauthn.SessionData
	if err := jsonutil.UnmarshalJsonStr(flow.Payload, &sd); err != nil {
		return nil, err
	}
	return &sd, nil
}

func consumeAuthFlowMatching(flowToken, purpose string, userId int, sessionId string) (*model.AuthFlow, error) {
	if flowToken == "" || purpose == "" || userId < 0 {
		return nil, ErrInvalidFlowToken
	}
	var flow model.AuthFlow
	err := model.DB.Where(
		"token_hash = ? AND purpose = ? AND user_id = ? AND session_id = ?",
		cryptoutil.SHA256Hex(flowToken), purpose, userId, sessionId,
	).First(&flow).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidFlowToken
		}
		return nil, err
	}
	return consumeAuthFlowRecordWithAction(&flow, nil)
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

// DeleteAllPasskeysAndRotateSession removes the factor and rotates the user's
// authentication version in the same transaction. The current session is
// retained for refresh; every other access token becomes stale immediately.
func DeleteAllPasskeysAndRotateSession(userId int, keepSid string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).Delete(&model.PasskeyCredential{}).Error; err != nil {
			return err
		}
		return bumpAuthVersionKeepSession(tx, userId, keepSid)
	})
}

// ResetPasskeysAndRevokeSessions is the administrator reset variant. Factor
// removal and session revocation either both commit or both roll back.
func ResetPasskeysAndRevokeSessions(userId int) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).Delete(&model.PasskeyCredential{}).Error; err != nil {
			return err
		}
		return revokeAllUserSessions(tx, userId, "passkey_reset")
	})
}
