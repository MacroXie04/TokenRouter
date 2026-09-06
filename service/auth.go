package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Session-related constants.
const (
	AccessTokenTTL      = 15 * time.Minute
	RefreshTokenTTL     = 30 * 24 * time.Hour
	RefreshReplayWindow = 30 * time.Second

	SessionStatusActive  = "active"
	SessionStatusRevoked = "revoked"
)

// Auth errors.
var (
	ErrInvalidCredentials       = errors.New("用户名或密码错误")
	ErrRefreshTokenInvalid      = errors.New("刷新令牌无效")
	ErrSessionRevoked           = errors.New("会话已撤销")
	ErrRefreshReplay            = errors.New("刷新令牌重放")
	ErrRefreshRace              = errors.New("刷新令牌已被并发轮换")
	ErrSessionLimit             = errors.New("active user session limit reached")
	ErrSessionIssuanceLimit     = errors.New("user session issuance limit reached")
	ErrAuthVersionOverflow      = errors.New("account authentication version is invalid or exhausted")
	ErrSessionVersionOverflow   = errors.New("session version is invalid or exhausted")
	ErrSessionExpiryInvalid     = errors.New("session expiry is invalid")
	ErrPasswordVerificationBusy = errors.New("password verification capacity is busy")
)

// SessionRefreshResult carries the newly rotated credentials together with
// the immutable absolute database expiry. Its fields are deliberately hidden
// from JSON so an accidental controller serialization cannot disclose tokens.
type SessionRefreshResult struct {
	AccessToken  string      `json:"-"`
	RefreshToken string      `json:"-"`
	User         *model.User `json:"-"`
	ExpiresAt    int64       `json:"-"`
	// ValidatedAt is the primary-database timestamp used to validate ExpiresAt.
	// The controller uses the same clock snapshot for cookie Max-Age so process
	// clock skew cannot shorten or extend the refresh credential.
	ValidatedAt int64 `json:"-"`
}

// LoginSessionView is the bounded, non-secret representation returned to a
// signed-in browser. Internal version counters and refresh-token state never
// cross the API boundary.
type LoginSessionView struct {
	SID          string `json:"sid"`
	Current      bool   `json:"current"`
	LoginMethod  string `json:"login_method"`
	IP           string `json:"ip"`
	UserAgent    string `json:"user_agent"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

const invalidLoginPasswordHash = "$2y$12$WGIVWvPB3dHTKpgaZwRTAuLd.wsNOOvf1vW2ZwYuho14bIlqDCElW"

const (
	maxBcryptPasswordBytes             = 72
	maxPasswordVerificationConcurrency = 4
)

var passwordVerificationSlots = make(chan struct{}, passwordVerificationConcurrency())

func passwordVerificationConcurrency() int {
	concurrency := runtime.GOMAXPROCS(0)
	if concurrency < 1 {
		return 1
	}
	if concurrency > maxPasswordVerificationConcurrency {
		return maxPasswordVerificationConcurrency
	}
	return concurrency
}

// IssueAccessToken signs a short-lived access JWT for a session.
func IssueAccessToken(user *model.User, sid string) (string, error) {
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return "", err
	}
	return issueAccessTokenAt(user, sid, now)
}

// issueAccessTokenAt validates and signs against one caller-supplied primary
// database clock snapshot.
func issueAccessTokenAt(user *model.User, sid string, now int64) (string, error) {
	if user == nil || user.Id <= 0 || sid == "" {
		return "", ErrSessionRevoked
	}
	var session model.UserSession
	if err := model.DB.Where("sid = ? AND user_id = ?", sid, user.Id).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSessionRevoked
		}
		return "", err
	}
	var currentUser model.User
	if err := model.DB.First(&currentUser, user.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSessionRevoked
		}
		return "", err
	}
	if !validSessionSnapshot(&session, &currentUser, now) {
		return "", ErrSessionRevoked
	}
	return signSessionAccessToken(&session, &currentUser, now)
}

// signSessionAccessToken signs an access token from an already locked and
// validated database snapshot. Callers that mutate the session use this helper
// before committing so a later storage outage cannot strand an active session
// whose credentials were never returned to the user.
func signSessionAccessToken(session *model.UserSession, user *model.User, now int64) (string, error) {
	if !validSessionSnapshot(session, user, now) {
		return "", ErrSessionRevoked
	}
	return common.GenerateSessionJWTAt(
		user.Id,
		user.Role,
		session.SID,
		session.UserAuthVersion,
		session.Version,
		common.SessionSecret(),
		AccessTokenTTL,
		time.Unix(now, 0).UTC(),
	)
}

// ValidateAccessTokenClaims resolves and validates the live session snapshot
// carried by a dashboard access JWT. Validation is deliberately database
// backed on every request so revocation is immediate rather than TTL-bound.
func ValidateAccessTokenClaims(claims *common.JWTClaims) (*model.UserSession, *model.User, error) {
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return nil, nil, err
	}
	return ValidateAccessTokenClaimsAt(claims, now)
}

// ValidateAccessTokenClaimsAt applies live-session checks against the same
// primary-database timestamp used by JWT registered-claim validation.
func ValidateAccessTokenClaimsAt(claims *common.JWTClaims, now int64) (*model.UserSession, *model.User, error) {
	if !common.IsDashboardAccessToken(claims) || claims.UserID <= 0 || claims.SessionID == "" ||
		claims.UserAuthVersion <= 0 || claims.SessionVersion <= 0 || claims.ExpiresAt == nil ||
		claims.ExpiresAt.Time.Unix() <= now {
		return nil, nil, ErrSessionRevoked
	}

	var session model.UserSession
	if err := model.DB.Where("sid = ? AND user_id = ?", claims.SessionID, claims.UserID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrSessionRevoked
		}
		return nil, nil, err
	}
	var user model.User
	if err := model.DB.First(&user, claims.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrSessionRevoked
		}
		return nil, nil, err
	}
	if !validSessionSnapshot(&session, &user, now) ||
		session.Version != claims.SessionVersion ||
		session.UserAuthVersion != claims.UserAuthVersion {
		return nil, nil, ErrSessionRevoked
	}
	return &session, &user, nil
}

func validSessionSnapshot(session *model.UserSession, user *model.User, now int64) bool {
	return session != nil && user != nil && session.SID != "" && session.UserID == user.Id &&
		session.Version > 0 && session.UserAuthVersion > 0 &&
		session.Status == SessionStatusActive && session.RevokedAt == 0 && session.ExpiresAt > now &&
		user.Status == model.UserStatusEnabled && user.AuthVersion == session.UserAuthVersion
}

// ValidateSessionBinding verifies that a security ceremony is still bound to
// a live session snapshot. Redirect callbacks may not be routed through
// UserAuth, so they use the session identifier sealed into their one-time flow
// instead of accepting a stale or revoked ceremony indefinitely.
func ValidateSessionBinding(userId int, sid string) error {
	if userId <= 0 || sid == "" {
		return ErrSessionRevoked
	}
	var session model.UserSession
	if err := model.DB.Where("user_id = ? AND sid = ?", userId, sid).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return err
	}
	if !validSessionSnapshot(&session, &user, now) {
		return ErrSessionRevoked
	}
	return nil
}

// CreateSession creates a server-side session and returns its SID and the
// opaque refresh token (only the hash is persisted).
func CreateSession(user *model.User, ip, userAgent, loginMethod string) (sid, refreshToken string, err error) {
	created, err := createSession(user, ip, userAgent, loginMethod, false)
	if err != nil {
		return "", "", err
	}
	return created.Session.SID, created.RefreshToken, nil
}

type sessionCreationResult struct {
	Session      model.UserSession
	User         model.User
	RefreshToken string
	AccessToken  string
	DatabaseNow  int64
}

func createSession(user *model.User, ip, userAgent, loginMethod string, signAccess bool) (*sessionCreationResult, error) {
	if user == nil || user.Id <= 0 || user.Status != model.UserStatusEnabled || user.AuthVersion <= 0 || model.DB == nil {
		return nil, ErrSessionRevoked
	}
	policy, err := loadUserSessionPolicy()
	if err != nil {
		return nil, err
	}
	loginMethod = normalizeSessionMetadata(loginMethod, 32)
	if loginMethod == "" {
		loginMethod = "unknown"
	}
	ip = normalizeSessionMetadata(ip, 64)
	userAgent = normalizeSessionMetadata(userAgent, 512)

	sid, err := common.SecureRandomAlphanumeric(32)
	if err != nil {
		return nil, err
	}
	refreshToken, err := common.SecureRandomAlphanumeric(64)
	if err != nil {
		return nil, err
	}
	created := &sessionCreationResult{RefreshToken: refreshToken}

	// The process-local stripe closes SQLite's missing row-lock gap, while the
	// owning user row serializes issuance across MySQL/PostgreSQL nodes.
	creationLock := userSessionCreationLock(user.Id)
	creationLock.Lock()
	defer creationLock.Unlock()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentUser model.User
		if err := subscriptionLockForUpdate(tx).
			Select("id", "role", "status", "auth_version").
			Where("id = ?", user.Id).
			First(&currentUser).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionRevoked
			}
			return err
		}
		if currentUser.Status != model.UserStatusEnabled || currentUser.AuthVersion <= 0 ||
			currentUser.AuthVersion != user.AuthVersion {
			return ErrSessionRevoked
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var activeCount int64
		if err := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND status = ? AND expires_at > ?",
				user.Id, SessionStatusActive, now).
			Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount >= policy.activeLimit {
			return ErrSessionLimit
		}
		var issuanceCount int64
		issuanceCutoff := time.Unix(now-int64(policy.issuanceWindow/time.Second), 0)
		if err := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND created_at > ?", user.Id, issuanceCutoff).
			Count(&issuanceCount).Error; err != nil {
			return err
		}
		if issuanceCount >= policy.issuanceLimit {
			return ErrSessionIssuanceLimit
		}

		createdAt := time.Unix(now, 0).UTC()
		session := model.UserSession{
			SID:             sid,
			UserID:          currentUser.Id,
			Version:         1,
			UserAuthVersion: currentUser.AuthVersion,
			Status:          SessionStatusActive,
			RefreshHash:     refreshTokenHash(refreshToken),
			LoginMethod:     loginMethod,
			IP:              ip,
			UserAgent:       userAgent,
			CreatedAt:       createdAt,
			LastActiveAt:    now,
			ExpiresAt:       createdAt.Add(RefreshTokenTTL).Unix(),
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		created.Session = session
		created.User = currentUser
		created.DatabaseNow = now
		if signAccess {
			access, err := signSessionAccessToken(&created.Session, &created.User, now)
			if err != nil {
				return err
			}
			created.AccessToken = access
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// AuthenticatePassword verifies a username-or-email/password pair without
// creating a session (used by the two-step login flow when 2FA is enabled).
// Email lookup fails closed when legacy/unverified duplicates or a username
// collision make the identifier ambiguous.
func AuthenticatePassword(username, password string) (*model.User, error) {
	candidate, candidateValid := loginPasswordCandidate(password)
	if !acquirePasswordVerificationSlot() {
		return nil, ErrPasswordVerificationBusy
	}
	defer releasePasswordVerificationSlot()

	user, err := getUserByLoginIdentifier(username)
	if err != nil {
		// Keep the expensive password step on unknown and ambiguous identifiers
		// so the endpoint does not become a cheap account-enumeration oracle.
		_ = common.PasswordVerify(candidate, invalidLoginPasswordHash)
		return nil, ErrInvalidCredentials
	}
	verificationPlan, hashCost, hashValid := loginPasswordVerificationPlan(user.Password)
	passwordMatches := false
	for _, verification := range verificationPlan {
		matches := common.PasswordVerify(candidate, verification.hash)
		if verification.primary {
			passwordMatches = matches
		}
	}
	if !candidateValid || !hashValid || !passwordMatches {
		return nil, ErrInvalidCredentials
	}
	if user.Status == model.UserStatusDisabled {
		return nil, errors.New("用户已禁用")
	}
	if err := upgradeLoginPasswordHash(user, candidate, hashCost); err != nil {
		return nil, err
	}
	return user, nil
}

func acquirePasswordVerificationSlot() bool {
	select {
	case passwordVerificationSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releasePasswordVerificationSlot() {
	<-passwordVerificationSlots
}

// loginPasswordCandidate maps every input that bcrypt cannot validly verify to
// one fixed, bounded value. Unknown, OAuth-only, malformed-hash, empty, and
// oversized credential paths therefore still perform one comparable bcrypt
// operation instead of exposing a cheap account-existence branch.
func loginPasswordCandidate(password string) (string, bool) {
	if password == "" || len(password) > maxBcryptPasswordBytes {
		return "tokenrouter-invalid-login-sentinel", false
	}
	return password, true
}

func loginPasswordHash(hash string) (string, bool) {
	plan, _, valid := loginPasswordVerificationPlan(hash)
	return plan[0].hash, valid
}

type loginPasswordVerification struct {
	hash    string
	primary bool
}

// loginPasswordVerificationPlan gives every accepted stored cost the same
// bcrypt work class. For a real cost c, the primary comparison contributes
// 2^c work and dummy costs c..target-1 contribute 2^target-2^c, for a total
// of exactly 2^target. Invalid and unknown hashes take one target-cost check.
func loginPasswordVerificationPlan(hash string) ([]loginPasswordVerification, int, bool) {
	cost, err := common.ValidatePasswordBcryptHash(hash)
	if err != nil {
		return []loginPasswordVerification{{hash: invalidLoginPasswordHash}}, common.PasswordBcryptTargetCost, false
	}
	plan := make([]loginPasswordVerification, 0, common.PasswordBcryptTargetCost-cost+1)
	plan = append(plan, loginPasswordVerification{hash: hash, primary: true})
	for dummyCost := cost; dummyCost < common.PasswordBcryptTargetCost; dummyCost++ {
		plan = append(plan, loginPasswordVerification{hash: loginPasswordSentinelAtCost(dummyCost)})
	}
	return plan, cost, true
}

func loginPasswordSentinelAtCost(cost int) string {
	encoded := []byte(invalidLoginPasswordHash)
	encoded[4] = byte('0' + cost/10)
	encoded[5] = byte('0' + cost%10)
	return string(encoded)
}

// upgradeLoginPasswordHash converges a successfully proven legacy credential
// to the target cost. The compare-and-swap prevents a concurrent password
// reset from being overwritten or authenticated with the stale credential.
func upgradeLoginPasswordHash(user *model.User, password string, verifiedCost int) error {
	if verifiedCost >= common.PasswordBcryptTargetCost {
		return nil
	}
	replacement, err := common.PasswordHash(password)
	if err != nil {
		common.SysError("upgrade login password hash: " + err.Error())
		return ErrInvalidCredentials
	}
	result := model.DB.Model(&model.User{}).
		Where("id = ? AND password = ? AND auth_version = ? AND status = ?",
			user.Id, user.Password, user.AuthVersion, model.UserStatusEnabled).
		UpdateColumn("password", replacement)
	if result.Error != nil {
		common.SysError("upgrade login password hash: " + result.Error.Error())
		return ErrInvalidCredentials
	}
	if result.RowsAffected != 1 {
		return ErrInvalidCredentials
	}
	user.Password = replacement
	return nil
}

func getUserByLoginIdentifier(rawIdentifier string) (*model.User, error) {
	identifier := strings.TrimSpace(rawIdentifier)
	if ValidateRegistrationUsername(identifier) != nil || !utf8.ValidString(identifier) ||
		!validLoginIdentifierText(identifier) {
		return nil, ErrInvalidCredentials
	}

	normalizedEmail, emailKey, emailErr := model.NormalizeVerifiedEmail(identifier)
	if emailErr == nil {
		// A durable verified-email claim outranks the username and legacy email
		// namespaces. Otherwise an attacker could register either an unverified
		// duplicate email or a username equal to the victim's email and deny the
		// verified owner password login.
		var verifiedOwner model.User
		result := model.DB.Where("verified_email_key = ?", emailKey).Limit(1).Find(&verifiedOwner)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			return &verifiedOwner, nil
		}
	}

	var usernameOwner model.User
	result := model.DB.Where("username = ?", identifier).Limit(1).Find(&usernameOwner)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 1 {
		return &usernameOwner, nil
	}
	if emailErr != nil {
		return nil, ErrInvalidCredentials
	}

	// Preserve the reference-compatible login path for one legacy unverified
	// email only when neither authoritative namespace owns the identifier.
	var legacyOwners []model.User
	if err := model.DB.Where("email = ? AND email_verified = ? AND verified_email_key IS NULL",
		normalizedEmail, false).Limit(2).Find(&legacyOwners).Error; err != nil {
		return nil, err
	}
	if len(legacyOwners) != 1 {
		return nil, ErrInvalidCredentials
	}
	return &legacyOwners[0], nil
}

func validLoginIdentifierText(value string) bool {
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

// CompleteLogin creates a session and returns the tokens for a user.
func CompleteLogin(user *model.User, ip, userAgent, loginMethod string) (sid, access, refresh string, err error) {
	sid, access, refresh, _, err = CompleteLoginWithExpiry(user, ip, userAgent, loginMethod)
	return sid, access, refresh, err
}

// CompleteLoginWithExpiry returns the access-token expiry derived from the
// same primary-database timestamp used to sign the token. Controllers that
// expose expiry metadata therefore never substitute their process clock.
func CompleteLoginWithExpiry(user *model.User, ip, userAgent, loginMethod string) (sid, access, refresh string, accessExpiresAt int64, err error) {
	created, err := createSession(user, ip, userAgent, loginMethod, true)
	if err != nil {
		return "", "", "", 0, err
	}
	_ = UpdateUserLastLoginAt(created.User.Id, created.DatabaseNow)
	return created.Session.SID, created.AccessToken, created.RefreshToken,
		time.Unix(created.DatabaseNow, 0).UTC().Add(AccessTokenTTL).Unix(), nil
}

// AuthSessionErrorCode maps session lifecycle errors to stable API semantics.
// Unexpected storage or entropy failures stay generic at the controller edge.
func AuthSessionErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, ErrSessionLimit):
		return http.StatusConflict, "AUTH_SESSION_LIMIT"
	case errors.Is(err, ErrSessionIssuanceLimit):
		return http.StatusTooManyRequests, "AUTH_SESSION_ISSUANCE_LIMIT"
	case errors.Is(err, ErrRefreshRace):
		return http.StatusConflict, "AUTH_REFRESH_RACE"
	case errors.Is(err, ErrRefreshTokenInvalid):
		return http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	case errors.Is(err, ErrSessionRevoked), errors.Is(err, ErrRefreshReplay):
		return http.StatusUnauthorized, "AUTH_SESSION_REVOKED"
	default:
		return http.StatusInternalServerError, "AUTH_INTERNAL_ERROR"
	}
}

// Login validates credentials and returns a fresh session + access token.
func Login(username, password, ip, userAgent string) (*model.User, string, string, string, error) {
	user, err := AuthenticatePassword(username, password)
	if err != nil {
		return nil, "", "", "", err
	}
	sid, access, refresh, err := CompleteLogin(user, ip, userAgent, "password")
	if err != nil {
		return nil, "", "", "", err
	}
	return user, sid, access, refresh, nil
}

type loginTwoFAFlowPayload struct {
	AuthVersion int64 `json:"auth_version"`
}

// BeginTwoFALogin creates a short-lived flow bound to the authentication
// version proven by the password step.
func BeginTwoFALogin(user *model.User) (string, error) {
	if user == nil || user.Id <= 0 || user.Status != model.UserStatusEnabled || user.AuthVersion <= 0 {
		return "", ErrInvalidCredentials
	}
	payload, err := common.Marshal(loginTwoFAFlowPayload{AuthVersion: user.AuthVersion})
	if err != nil {
		return "", err
	}
	return CreateAuthFlow(
		AuthFlowPurposeLogin2FA, "password", "", user.Id, "", string(payload), 5*time.Minute,
	)
}

// Login2FA completes a two-step login using a one-time TOTP code or a backup
// code, after the password was already verified and a flow token issued.
func Login2FA(flowToken, code, ip, userAgent string) (*model.User, string, string, string, error) {
	flow, err := PeekAuthFlow(flowToken, AuthFlowPurposeLogin2FA)
	if err != nil {
		if errors.Is(err, ErrInvalidFlowToken) {
			return nil, "", "", "", ErrInvalidFlowToken
		}
		return nil, "", "", "", err
	}
	user, err := GetUserByID(flow.UserId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrUserNotFound) {
			return nil, "", "", "", ErrInvalidCredentials
		}
		return nil, "", "", "", err
	}
	var payload loginTwoFAFlowPayload
	if common.UnmarshalJsonStr(flow.Payload, &payload) != nil || payload.AuthVersion <= 0 ||
		payload.AuthVersion != user.AuthVersion || user.Status != model.UserStatusEnabled {
		return nil, "", "", "", ErrInvalidFlowToken
	}
	enabled, err := TwoFAStatusChecked(user.Id)
	if err != nil {
		return nil, "", "", "", err
	}
	if !enabled {
		return nil, "", "", "", ErrTwoFANotEnabled
	}
	if err := VerifyTwoFA(user.Id, code); err != nil {
		if !errors.Is(err, ErrTwoFAInvalidCode) {
			return nil, "", "", "", err
		}
		backupErr := VerifyBackupCode(user.Id, code)
		if backupErr != nil && !errors.Is(backupErr, ErrTwoFAInvalidCode) {
			return nil, "", "", "", backupErr
		}
		if backupErr != nil {
			return nil, "", "", "", ErrTwoFAInvalidCode
		}
	}
	if _, err := ConsumeAuthFlowExact(flowToken, AuthFlowMatch{
		Purpose: AuthFlowPurposeLogin2FA, Provider: "password", UserId: user.Id,
	}); err != nil {
		if errors.Is(err, ErrInvalidFlowToken) {
			return nil, "", "", "", ErrInvalidFlowToken
		}
		return nil, "", "", "", err
	}
	sid, access, refresh, err := CompleteLogin(user, ip, userAgent, "2fa")
	if err != nil {
		return nil, "", "", "", err
	}
	return user, sid, access, refresh, nil
}

// RefreshSession preserves the original service contract for non-HTTP callers.
func RefreshSession(sid, refreshToken string) (string, string, *model.User, error) {
	result, err := RefreshSessionWithResult(sid, refreshToken)
	if err != nil {
		return "", "", nil, err
	}
	return result.AccessToken, result.RefreshToken, result.User, nil
}

// RefreshSessionWithResult validates and rotates a refresh token while
// returning the session's unchanged absolute expiry for browser-cookie
// lifetime enforcement.
func RefreshSessionWithResult(sid, refreshToken string) (*SessionRefreshResult, error) {
	if sid == "" || refreshToken == "" {
		return nil, ErrRefreshTokenInvalid
	}
	nextRefresh := deriveNextRefreshToken(sid, refreshToken)
	for range 3 {
		var session model.UserSession
		if err := model.DB.Where("sid = ?", sid).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrRefreshTokenInvalid
			}
			return nil, err
		}
		now, err := model.DatabaseUnixTimestamp(model.DB)
		if err != nil {
			return nil, err
		}
		if session.Status != SessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= now ||
			session.Version <= 0 || session.UserAuthVersion <= 0 {
			return nil, ErrSessionRevoked
		}
		if session.ExpiresAt-now > int64(RefreshTokenTTL/time.Second) {
			return nil, ErrSessionExpiryInvalid
		}
		if session.Version == math.MaxInt64 {
			_, revokeErr := revokeActiveSessionAt(session.UserID, session.SID, "session_version_exhausted", now)
			return nil, errors.Join(ErrSessionVersionOverflow, revokeErr)
		}

		var user model.User
		if err := model.DB.First(&user, session.UserID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
			if _, revokeErr := revokeActiveSessionAt(session.UserID, session.SID, "user_invalid", now); revokeErr != nil {
				return nil, revokeErr
			}
			return nil, ErrSessionRevoked
		}
		if user.Status != model.UserStatusEnabled || user.AuthVersion != session.UserAuthVersion {
			if _, err := revokeActiveSessionAt(session.UserID, session.SID, "user_security_changed", now); err != nil {
				return nil, err
			}
			return nil, ErrSessionRevoked
		}

		if refreshTokenHashMatches(session.PreviousRefreshHash, refreshToken) {
			if now <= session.PreviousValidUntil && refreshTokenHashMatches(session.RefreshHash, nextRefresh) {
				access, err := signSessionAccessToken(&session, &user, now)
				if err != nil {
					return nil, err
				}
				return &SessionRefreshResult{
					AccessToken: access, RefreshToken: nextRefresh, User: &user,
					ExpiresAt: session.ExpiresAt, ValidatedAt: now,
				}, nil
			}
			if now <= session.PreviousValidUntil {
				return nil, ErrRefreshRace
			}
			if _, err := revokeActiveSessionAt(session.UserID, session.SID, "refresh_replay", now); err != nil {
				return nil, err
			}
			return nil, ErrRefreshReplay
		}
		if !refreshTokenHashMatches(session.RefreshHash, refreshToken) {
			return nil, ErrRefreshTokenInvalid
		}
		rotatedSession := session
		rotatedSession.Version++
		rotatedSession.RefreshHash = refreshTokenHash(nextRefresh)
		rotatedSession.PreviousRefreshHash = session.RefreshHash
		rotatedSession.PreviousValidUntil = now + int64(RefreshReplayWindow/time.Second)
		rotatedSession.LastActiveAt = now
		access, err := signSessionAccessToken(&rotatedSession, &user, now)
		if err != nil {
			return nil, err
		}
		result := model.DB.Model(&model.UserSession{}).
			Where("sid = ? AND user_id = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND version = ? AND user_auth_version = ? AND refresh_hash = ?",
				session.SID, session.UserID, SessionStatusActive, now, session.Version, session.UserAuthVersion, session.RefreshHash).
			Updates(map[string]any{
				"refresh_hash":          refreshTokenHash(nextRefresh),
				"previous_refresh_hash": session.RefreshHash,
				"previous_valid_until":  now + int64(RefreshReplayWindow/time.Second),
				"last_active_at":        now,
				"version":               session.Version + 1,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		return &SessionRefreshResult{
			AccessToken: access, RefreshToken: nextRefresh, User: &user,
			ExpiresAt: session.ExpiresAt, ValidatedAt: now,
		}, nil
	}
	return nil, ErrRefreshRace
}

func refreshTokenHash(raw string) string {
	mac := hmac.New(sha256.New, []byte(common.SessionSecret()))
	_, _ = mac.Write([]byte("tokenrouter/auth/refresh/v1\x00"))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func deriveNextRefreshToken(sid, current string) string {
	mac := hmac.New(sha256.New, []byte(common.SessionSecret()))
	_, _ = mac.Write([]byte("tokenrouter/auth/refresh-rotate/v1\x00"))
	_, _ = mac.Write([]byte(sid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(current))
	return hex.EncodeToString(mac.Sum(nil))
}

// refreshTokenHashMatches accepts the former unkeyed digest only long enough
// for deployed sessions to rotate forward. Every newly issued or rotated
// refresh token is persisted with the keyed, domain-separated digest.
func refreshTokenHashMatches(stored, raw string) bool {
	if stored == "" || raw == "" {
		return false
	}
	keyed := refreshTokenHash(raw)
	legacy := common.SHA256Hex(raw)
	return hmac.Equal([]byte(stored), []byte(keyed)) || hmac.Equal([]byte(stored), []byte(legacy))
}

func revokeActiveSession(userId int, sid, reason string) (bool, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	return revokeActiveSessionAt(userId, sid, reason, now)
}

func revokeActiveSessionAt(userId int, sid, reason string, now int64) (bool, error) {
	if model.DB == nil || now <= 0 {
		return false, ErrSessionRevoked
	}
	result := model.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
			userId, sid, SessionStatusActive, now).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     now,
			"revoked_reason": reason,
		})
	return result.RowsAffected == 1, result.Error
}

// RevokeUserSession revokes one active session only when it belongs to the
// acting user. A missing, already-revoked, or foreign SID is reported
// identically through the false result.
func RevokeUserSession(userId int, sid, reason string) (bool, error) {
	if userId <= 0 || sid == "" {
		return false, nil
	}
	return revokeActiveSession(userId, sid, reason)
}

// RevokeSessionByRefreshToken authenticates logout with possession of the
// current refresh token (or its immediately previous value during the rotation
// grace window). Possession of a SID alone never authorizes revocation.
func RevokeSessionByRefreshToken(sid, refreshToken string) (bool, error) {
	if sid == "" || refreshToken == "" {
		return false, nil
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	presentedHash := refreshTokenHash(refreshToken)
	legacyPresentedHash := common.SHA256Hex(refreshToken)
	result := model.DB.Model(&model.UserSession{}).
		Where("sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ?", sid, SessionStatusActive, now).
		Where(
			"refresh_hash IN ? OR (previous_refresh_hash IN ? AND previous_valid_until >= ?)",
			[]string{presentedHash, legacyPresentedHash},
			[]string{presentedHash, legacyPresentedHash},
			now,
		).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     now,
			"revoked_reason": "logout",
		})
	return result.RowsAffected == 1, result.Error
}

// RevokeAllUserSessions revokes every active session for a user.
func RevokeAllUserSessions(userId int) error {
	return revokeAllUserSessions(model.DB, userId, "revoke_all")
}

func revokeAllUserSessions(db *gorm.DB, userId int, reason string) error {
	if userId <= 0 {
		return nil
	}
	_, err := revokeUserSessionsInBatches(db, userId, "", reason)
	return err
}

// GetUserSessions lists at most the newest bounded set of currently usable
// sessions. Callers without a current browser identity use this compatibility
// wrapper; HTTP handlers use GetUserSessionsForCurrent so the present session
// is never displaced by newer rows.
func GetUserSessions(userId int) ([]LoginSessionView, error) {
	return GetUserSessionsForCurrent(userId, "")
}

func GetUserSessionsForCurrent(userId int, currentSID string) ([]LoginSessionView, error) {
	if userId <= 0 || model.DB == nil {
		return nil, ErrSessionRevoked
	}
	var user model.User
	if err := model.DB.Select("id", "status", "auth_version").First(&user, userId).Error; err != nil {
		return nil, err
	}
	if user.Status != model.UserStatusEnabled || user.AuthVersion <= 0 {
		return nil, ErrSessionRevoked
	}
	currentSID = strings.TrimSpace(currentSID)
	if len(currentSID) > 64 {
		return nil, ErrSessionRevoked
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, err
	}
	sessions := make([]model.UserSession, 0, userSessionListLimit)
	if currentSID != "" {
		var current []model.UserSession
		if err := model.DB.Where(
			"user_id = ? AND user_auth_version = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND sid = ?",
			userId, user.AuthVersion, SessionStatusActive, now, currentSID,
		).Limit(1).Find(&current).Error; err != nil {
			return nil, err
		}
		if len(current) == 1 {
			sessions = append(sessions, current[0])
		}
	}

	remaining := userSessionListLimit - len(sessions)
	query := model.DB.Where(
		"user_id = ? AND user_auth_version = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
		userId, user.AuthVersion, SessionStatusActive, now,
	)
	if currentSID != "" {
		query = query.Where("sid <> ?", currentSID)
	}
	var others []model.UserSession
	if err := query.Order("last_active_at desc").Order("created_at desc").Limit(remaining).Find(&others).Error; err != nil {
		return nil, err
	}
	sessions = append(sessions, others...)

	views := make([]LoginSessionView, 0, len(sessions))
	for i := range sessions {
		views = append(views, LoginSessionView{
			SID:          sessions[i].SID,
			Current:      currentSID != "" && sessions[i].SID == currentSID,
			LoginMethod:  sessions[i].LoginMethod,
			IP:           sessions[i].IP,
			UserAgent:    sessions[i].UserAgent,
			CreatedAt:    sessions[i].CreatedAt.Unix(),
			LastActiveAt: sessions[i].LastActiveAt,
			ExpiresAt:    sessions[i].ExpiresAt,
		})
	}
	return views, nil
}

// RevokeOtherSessions marks every other active session revoked while retaining
// tombstones for immediate access-token denial and auditability.
func RevokeOtherSessions(userId int, keepSid string) error {
	_, err := RevokeOtherSessionsWithCount(userId, keepSid)
	return err
}

// RevokeOtherSessionsWithCount returns the exact number of other sessions
// changed by this request while retaining the original error-only helper for
// service callers that do not need response metadata.
func RevokeOtherSessionsWithCount(userId int, keepSid string) (int64, error) {
	if userId <= 0 || keepSid == "" {
		return 0, ErrSessionRevoked
	}
	var current model.UserSession
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return 0, err
	}
	if err := model.DB.Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
		userId, keepSid, SessionStatusActive, now).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrSessionRevoked
		}
		return 0, err
	}
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		return 0, err
	}
	if user.Status != model.UserStatusEnabled || user.AuthVersion != current.UserAuthVersion {
		return 0, ErrSessionRevoked
	}
	return revokeUserSessionsInBatchesAt(model.DB, userId, keepSid, "revoke_others", now)
}

func revokeUserSessionsInBatches(db *gorm.DB, userId int, excludedSID, reason string) (int64, error) {
	if db == nil || userId <= 0 {
		return 0, ErrSessionRevoked
	}
	now, err := model.DatabaseUnixTimestamp(db)
	if err != nil {
		return 0, err
	}
	return revokeUserSessionsInBatchesAt(db, userId, excludedSID, reason, now)
}

func revokeUserSessionsInBatchesAt(db *gorm.DB, userId int, excludedSID, reason string, now int64) (int64, error) {
	if db == nil || userId <= 0 || now <= 0 {
		return 0, ErrSessionRevoked
	}
	var total int64
	for {
		query := db.Model(&model.UserSession{}).
			Where("user_id = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
				userId, SessionStatusActive, now)
		if excludedSID != "" {
			query = query.Where("sid <> ?", excludedSID)
		}
		var sids []string
		if err := query.Order("sid asc").Limit(userSessionRevokeBatchSize).Pluck("sid", &sids).Error; err != nil {
			return total, err
		}
		if len(sids) == 0 {
			return total, nil
		}
		result := db.Model(&model.UserSession{}).
			Where("user_id = ? AND sid IN ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
				userId, sids, SessionStatusActive, now).
			Updates(map[string]any{
				"status":         SessionStatusRevoked,
				"revoked_at":     now,
				"revoked_reason": reason,
			})
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
	}
}

// BumpAuthVersionKeepSession increments the user's auth version, immediately
// invalidating all existing access JWTs, and re-syncs one refresh session so
// the client can obtain a replacement access token.
func BumpAuthVersionKeepSession(userId int, keepSid string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return bumpAuthVersionKeepSession(tx, userId, keepSid)
	})
}

func bumpAuthVersionKeepSession(tx *gorm.DB, userId int, keepSid string) error {
	if userId <= 0 || keepSid == "" {
		return ErrSessionRevoked
	}
	user, nextVersion, err := lockNextAuthVersion(tx, userId)
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	var session model.UserSession
	if err := subscriptionLockForUpdate(tx).
		Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND user_auth_version = ?",
			userId, keepSid, SessionStatusActive, now, user.AuthVersion).
		First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	if err := updateAuthVersionCAS(tx, userId, user.AuthVersion, nextVersion, nil); err != nil {
		return err
	}
	result := tx.Model(&model.UserSession{}).
		Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND user_auth_version = ?",
			userId, keepSid, SessionStatusActive, now, user.AuthVersion).
		UpdateColumn("user_auth_version", nextVersion)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSessionRevoked
	}
	return tx.Model(&model.UserSession{}).
		Where("user_id = ? AND sid <> ? AND status = ? AND revoked_at = 0", userId, keepSid, SessionStatusActive).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     now,
			"revoked_reason": "auth_version_changed",
		}).Error
}

// ChangePasswordKeepSession changes a password and advances the account's
// authentication version in one transaction. The browser session that proved
// possession of the old password remains usable; every other active session is
// revoked. A replacement access token is returned because the caller's old JWT
// becomes invalid as soon as the transaction commits.
func ChangePasswordKeepSession(userId int, keepSid, oldPassword, newPassword, displayName string) (string, error) {
	if userId <= 0 || keepSid == "" {
		return "", ErrSessionRevoked
	}
	if len(newPassword) < 8 || len(newPassword) > 64 {
		return "", ErrInvalidCredentials
	}
	passwordHash, err := common.PasswordHash(newPassword)
	if err != nil {
		return "", err
	}

	var accessToken string
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := subscriptionLockForUpdate(tx).
			Select("id", "role", "status", "password", "auth_version").
			First(&user, userId).Error; err != nil {
			return err
		}
		if user.Status != model.UserStatusEnabled {
			return ErrSessionRevoked
		}
		if !common.PasswordVerify(oldPassword, user.Password) {
			return ErrInvalidCredentials
		}
		if user.AuthVersion <= 0 || user.AuthVersion == math.MaxInt64 {
			return ErrAuthVersionOverflow
		}

		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var session model.UserSession
		if err := subscriptionLockForUpdate(tx).
			Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND user_auth_version = ?",
				userId, keepSid, SessionStatusActive, now, user.AuthVersion).
			First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionRevoked
			}
			return err
		}
		if session.Version <= 0 {
			return ErrSessionRevoked
		}

		nextVersion := user.AuthVersion + 1
		userUpdates := map[string]any{"password": passwordHash}
		if displayName != "" {
			userUpdates["display_name"] = displayName
		}
		if err := updateAuthVersionCAS(tx, userId, user.AuthVersion, nextVersion, userUpdates); err != nil {
			return err
		}
		kept := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND user_auth_version = ?",
				userId, keepSid, SessionStatusActive, user.AuthVersion).
			UpdateColumn("user_auth_version", nextVersion)
		if kept.Error != nil {
			return kept.Error
		}
		if kept.RowsAffected != 1 {
			return ErrSessionRevoked
		}
		if err := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND sid <> ? AND status = ? AND revoked_at = 0", userId, keepSid, SessionStatusActive).
			Updates(map[string]any{
				"status":         SessionStatusRevoked,
				"revoked_at":     now,
				"revoked_reason": "password_changed",
			}).Error; err != nil {
			return err
		}

		accessToken, err = common.GenerateSessionJWTAt(
			user.Id, user.Role, keepSid, nextVersion, session.Version,
			common.SessionSecret(), AccessTokenTTL, time.Unix(now, 0).UTC(),
		)
		return err
	})
	if err != nil {
		return "", err
	}
	return accessToken, nil
}

func lockNextAuthVersion(tx *gorm.DB, userId int) (*model.User, int64, error) {
	if tx == nil || userId <= 0 {
		return nil, 0, ErrSessionRevoked
	}
	var user model.User
	if err := subscriptionLockForUpdate(tx).Select("id", "auth_version").First(&user, userId).Error; err != nil {
		return nil, 0, err
	}
	if user.AuthVersion <= 0 || user.AuthVersion == math.MaxInt64 {
		return nil, 0, ErrAuthVersionOverflow
	}
	return &user, user.AuthVersion + 1, nil
}

func updateAuthVersionCAS(
	tx *gorm.DB,
	userId int,
	currentVersion int64,
	nextVersion int64,
	extraUpdates map[string]any,
) error {
	if tx == nil || userId <= 0 || currentVersion <= 0 || currentVersion == math.MaxInt64 ||
		nextVersion != currentVersion+1 || nextVersion <= 0 {
		return ErrAuthVersionOverflow
	}
	updates := make(map[string]any, len(extraUpdates)+1)
	for key, value := range extraUpdates {
		updates[key] = value
	}
	updates["auth_version"] = nextVersion
	result := tx.Model(&model.User{}).
		Where("id = ? AND auth_version = ?", userId, currentVersion).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuthVersionOverflow
	}
	return nil
}
