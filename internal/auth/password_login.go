package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

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
		_ = cryptoutil.PasswordVerify(candidate, invalidLoginPasswordHash)
		return nil, ErrInvalidCredentials
	}
	verificationPlan, hashCost, hashValid := loginPasswordVerificationPlan(user.Password)
	passwordMatches := false
	for _, verification := range verificationPlan {
		matches := cryptoutil.PasswordVerify(candidate, verification.hash)
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
	cost, err := cryptoutil.ValidatePasswordBcryptHash(hash)
	if err != nil {
		return []loginPasswordVerification{{hash: invalidLoginPasswordHash}}, cryptoutil.PasswordBcryptTargetCost, false
	}
	plan := make([]loginPasswordVerification, 0, cryptoutil.PasswordBcryptTargetCost-cost+1)
	plan = append(plan, loginPasswordVerification{hash: hash, primary: true})
	for dummyCost := cost; dummyCost < cryptoutil.PasswordBcryptTargetCost; dummyCost++ {
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
	if verifiedCost >= cryptoutil.PasswordBcryptTargetCost {
		return nil
	}
	replacement, err := cryptoutil.PasswordHash(password)
	if err != nil {
		logging.SysError("upgrade login password hash: " + err.Error())
		return ErrInvalidCredentials
	}
	result := model.DB.Model(&model.User{}).
		Where("id = ? AND password = ? AND auth_version = ? AND status = ?",
			user.Id, user.Password, user.AuthVersion, model.UserStatusEnabled).
		UpdateColumn("password", replacement)
	if result.Error != nil {
		logging.SysError("upgrade login password hash: " + result.Error.Error())
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
	_ = userssvc.UpdateUserLastLoginAt(created.User.Id, created.DatabaseNow)
	return created.Session.SID, created.AccessToken, created.RefreshToken,
		time.Unix(created.DatabaseNow, 0).UTC().Add(AccessTokenTTL).Unix(), nil
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
