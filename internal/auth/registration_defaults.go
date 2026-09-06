package auth

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"unicode"
	"unicode/utf8"
)

const (
	GenerateDefaultTokenEnvironment = "GENERATE_DEFAULT_TOKEN"
	defaultTokenCredentialBytes     = 48
	maxDefaultTokenNameBytes        = 50
	maxRegistrationUsernameBytes    = 64
)

var (
	ErrDefaultTokenPlanInvalid      = errors.New("default token plan is invalid")
	ErrDefaultTokenEntropy          = errors.New("default token credential generation failed")
	ErrPasswordRegistrationDisabled = errors.New("password registration is disabled")
	ErrRegistrationPolicyInvalid    = errors.New("registration policy is invalid")
	ErrRegistrationUsernameInvalid  = errors.New("registration username is invalid")
	ErrRegistrationPasswordInvalid  = errors.New("registration password is invalid")
)

type registrationGateState struct {
	registrationEnabled         bool
	passwordRegistrationEnabled bool
}

// registrationGateStateWithTx reads both password-registration gates in one
// database statement inside the account-creation transaction. PostgreSQL and
// MySQL retain shared row locks until commit, preventing a concurrent disable
// from publishing between authorization and insertion. SQLite supplies one
// coherent transaction snapshot and serializes the subsequent write.
func registrationGateStateWithTx(tx *gorm.DB) (registrationGateState, error) {
	if tx == nil || tx.Dialector == nil {
		return registrationGateState{}, ErrRegistrationPolicyInvalid
	}
	keys := []string{setting.RegistrationEnabledOption, setting.PasswordRegisterEnabledOption}
	query := tx.Select("key", "value").Where("key IN ?", keys)
	if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var options []model.Option
	if err := query.Find(&options).Error; err != nil {
		return registrationGateState{}, err
	}
	values := make(map[string]string, len(options))
	for _, option := range options {
		if _, duplicate := values[option.Key]; duplicate {
			return registrationGateState{}, ErrRegistrationPolicyInvalid
		}
		values[option.Key] = option.Value
	}
	registrationEnabled, err := parseRegistrationGateValue(values, setting.RegistrationEnabledOption)
	if err != nil {
		return registrationGateState{}, err
	}
	passwordEnabled, err := parseRegistrationGateValue(values, setting.PasswordRegisterEnabledOption)
	if err != nil {
		return registrationGateState{}, err
	}
	return registrationGateState{
		registrationEnabled:         registrationEnabled,
		passwordRegistrationEnabled: passwordEnabled,
	}, nil
}

func parseRegistrationGateValue(values map[string]string, key string) (bool, error) {
	raw, present := values[key]
	if !present || raw == "" {
		return true, nil
	}
	switch raw {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s", ErrRegistrationPolicyInvalid, key)
	}
}

func requirePasswordRegistrationEnabledWithTx(tx *gorm.DB) error {
	gates, err := registrationGateStateWithTx(tx)
	if err != nil {
		return err
	}
	if !gates.registrationEnabled {
		return ErrRegistrationDisabled
	}
	if !gates.passwordRegistrationEnabled {
		return ErrPasswordRegistrationDisabled
	}
	return nil
}

// RegistrationMutationPlan captures one immutable settings snapshot and any
// credential entropy before account/identity/referral rows are mutated. OAuth
// intentionally builds it on the new-user branch inside the flow transaction,
// where an entropy failure rolls the provisional flow claim back.
type RegistrationMutationPlan struct {
	quotaForNewUser     int
	defaultUseAutoGroup bool
	defaultGroup        string
	defaultToken        *defaultTokenPlan
}

type defaultTokenPlan struct {
	name        string
	key         string
	group       string
	createdTime int64
}

// GenerateDefaultTokenEnabled reads the opt-in environment contract. Missing,
// empty, or malformed values resolve to false.
func GenerateDefaultTokenEnabled() bool {
	return env.GetEnvBool(GenerateDefaultTokenEnvironment, false)
}

// PlanRegistrationMutation creates a mutation-free plan using one live policy
// snapshot. When token generation is enabled, entropy is obtained now—not
// after any user, identity, referral, or quota mutation has begun.
func PlanRegistrationMutation(username string, generateDefaultToken bool) (RegistrationMutationPlan, error) {
	return planRegistrationMutation(
		setting.GetRegistrationGroupPolicy(),
		username,
		generateDefaultToken,
		wallclock.NowTimestamp(),
		cryptoutil.SecureRandomAlphanumeric,
	)
}

// PlanRegistrationMutationFromEnvironment uses GENERATE_DEFAULT_TOKEN, whose
// secure default is false.
func PlanRegistrationMutationFromEnvironment(username string) (RegistrationMutationPlan, error) {
	return PlanRegistrationMutation(username, GenerateDefaultTokenEnabled())
}

func planRegistrationMutation(
	policy setting.RegistrationGroupPolicy,
	username string,
	generateDefaultToken bool,
	now int64,
	generateCredential func(int) (string, error),
) (RegistrationMutationPlan, error) {
	if err := ValidateRegistrationUsername(username); err != nil {
		return RegistrationMutationPlan{}, err
	}
	plan := RegistrationMutationPlan{
		quotaForNewUser:     policy.QuotaForNewUser(),
		defaultUseAutoGroup: policy.DefaultUseAutoGroup(),
		defaultGroup:        policy.DefaultGroup(),
	}
	if !quotamath.QuotaWithinBounds(plan.quotaForNewUser) {
		return RegistrationMutationPlan{}, fmt.Errorf("%w: initial quota", ErrDefaultTokenPlanInvalid)
	}
	if !userssvc.ValidResolvedGroupText(plan.defaultGroup, userssvc.MaxResolvedGroupIdentifierBytes, false) {
		return RegistrationMutationPlan{}, fmt.Errorf("%w: default group", ErrDefaultTokenPlanInvalid)
	}
	if !generateDefaultToken {
		return plan, nil
	}
	if now < 0 || generateCredential == nil {
		return RegistrationMutationPlan{}, ErrDefaultTokenPlanInvalid
	}
	credential, err := generateCredential(defaultTokenCredentialBytes)
	if err != nil {
		return RegistrationMutationPlan{}, fmt.Errorf("%w: %w", ErrDefaultTokenEntropy, err)
	}
	if len(credential) != defaultTokenCredentialBytes {
		return RegistrationMutationPlan{}, fmt.Errorf("%w: credential length", ErrDefaultTokenPlanInvalid)
	}
	for _, character := range credential {
		if character > unicode.MaxASCII || !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return RegistrationMutationPlan{}, fmt.Errorf("%w: credential alphabet", ErrDefaultTokenPlanInvalid)
		}
	}
	group := ""
	if policy.DefaultUseAutoGroup() {
		group = userssvc.GroupAuto
	}
	plan.defaultToken = &defaultTokenPlan{
		name:        defaultTokenName(username),
		key:         "sk-" + credential,
		group:       group,
		createdTime: now,
	}
	return plan, nil
}

// ValidateRegistrationUsername is the shared storage-safe username boundary
// used by both account creation and password-login lookup. Length is measured
// in UTF-8 bytes because that is the durable cross-dialect column contract.
func ValidateRegistrationUsername(username string) error {
	if !userssvc.ValidResolvedGroupText(username, maxRegistrationUsernameBytes, false) {
		return ErrRegistrationUsernameInvalid
	}
	return nil
}

// ValidateRegistrationPassword enforces the public sign-up character contract
// together with bcrypt's stricter 72-byte input ceiling. Multi-byte passwords
// can satisfy the JSON validator's rune count while exceeding bcrypt's limit;
// reject those as a client error before attempting account creation.
func ValidateRegistrationPassword(password string) error {
	if !utf8.ValidString(password) {
		return ErrRegistrationPasswordInvalid
	}
	characters := utf8.RuneCountInString(password)
	if characters < 8 || characters > 64 || len(password) > maxBcryptPasswordBytes {
		return ErrRegistrationPasswordInvalid
	}
	return nil
}

func defaultTokenName(username string) string {
	const suffix = "的初始令牌"
	maximumPrefixBytes := maxDefaultTokenNameBytes - len(suffix)
	for len(username) > maximumPrefixBytes {
		_, size := utf8.DecodeLastRuneInString(username)
		username = username[:len(username)-size]
	}
	return username + suffix
}

// QuotaForNewUser returns the exact internal integer quota captured before the
// transaction. No display-unit conversion occurs at this boundary.
func (p RegistrationMutationPlan) QuotaForNewUser() int { return p.quotaForNewUser }

func (p RegistrationMutationPlan) DefaultUseAutoGroup() bool { return p.defaultUseAutoGroup }

func (p RegistrationMutationPlan) DefaultGroup() string { return p.defaultGroup }

func (p RegistrationMutationPlan) HasDefaultToken() bool { return p.defaultToken != nil }

// applyToUser installs the exact internal quota captured by this plan. Callers
// invoke it before inserting the user so no database default or stale caller
// value can bypass the validated registration policy.
func (p RegistrationMutationPlan) applyToUser(user *model.User) error {
	if user == nil || !quotamath.QuotaWithinBounds(p.quotaForNewUser) {
		return fmt.Errorf("%w: initial quota", ErrDefaultTokenPlanInvalid)
	}
	user.Quota = p.quotaForNewUser
	user.Group = p.defaultGroup
	return nil
}

// createDefaultTokenWithTx inserts the planned token in the caller's existing
// registration transaction. A nil plan is an intentional no-op; insertion or
// validation failure aborts the user/identity/referral transaction as a unit.
func (p RegistrationMutationPlan) createDefaultTokenWithTx(tx *gorm.DB, userID int) error {
	if tx == nil {
		return fmt.Errorf("%w: database transaction", ErrDefaultTokenPlanInvalid)
	}
	token, err := p.DefaultTokenForUser(userID)
	if err != nil || token == nil {
		return err
	}
	return tx.Create(token).Error
}

// InsertPlannedRegistrationUserWithTx inserts a user and its optional default
// token in one caller-owned transaction. The plan overwrites caller-supplied
// quota with its validated internal-unit snapshot before either row is written.
func InsertPlannedRegistrationUserWithTx(tx *gorm.DB, user *model.User, plan RegistrationMutationPlan) error {
	if tx == nil {
		return fmt.Errorf("%w: database transaction", ErrDefaultTokenPlanInvalid)
	}
	if err := plan.applyToUser(user); err != nil {
		return err
	}
	if err := tx.Create(user).Error; err != nil {
		return err
	}
	return plan.createDefaultTokenWithTx(tx, user.Id)
}

// DefaultTokenForUser materializes a detached token row for insertion in the
// caller's existing registration transaction.
func (p RegistrationMutationPlan) DefaultTokenForUser(userID int) (*model.Token, error) {
	if p.defaultToken == nil {
		return nil, nil
	}
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user id", ErrDefaultTokenPlanInvalid)
	}
	token := p.defaultToken
	return &model.Token{
		UserId:             userID,
		Name:               token.name,
		Key:                token.key,
		Status:             billingsvc.TokenStatusEnabled,
		CreatedTime:        token.createdTime,
		AccessedTime:       token.createdTime,
		ExpiredTime:        -1,
		RemainQuota:        quotamath.QuotaPerUnit,
		UnlimitedQuota:     true,
		ModelLimitsEnabled: false,
		Group:              token.group,
	}, nil
}
