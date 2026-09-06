package service

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func prepareVerifiedRegistration(t *testing.T, email string) (code string, flow model.AuthFlow) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	previous := Mail
	mock := &mockMailer{}
	Mail = mock
	t.Cleanup(func() { Mail = previous })

	require.NoError(t, SendEmailVerificationCode(email))
	require.Len(t, mock.sent, 1)
	require.NoError(t, model.DB.Where("purpose = ?", EmailVerificationPurpose).First(&flow).Error)
	require.Len(t, flow.Payload, 6)
	return flow.Payload, flow
}

func TestCreateUserWithReferralAndVerifiedEmailCommitsOneAtomicRegistration(t *testing.T) {
	setupAffiliateDB(t)
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.UpdateOption(setting.DefaultUseAutoGroupOption, "true"))
	const email = "Verified.User@example.com"
	code, flow := prepareVerifiedRegistration(t, email)

	assert.Equal(t, common.SHA256Hex("verified.user@example.com"), flow.Provider)
	assert.Equal(t, "email", flow.Intent)
	assert.NotContains(t, flow.Provider, "@", "new flows must not persist an address in ceremony metadata")

	inviter := referralUser("verified-inviter", 7, 0)
	require.NoError(t, model.DB.Create(inviter).Error)
	invitee := referralUser("verified-invitee", 100, inviter.Id)
	require.NoError(t, CreateUserWithReferralAndVerifiedEmail(invitee, email, code))

	var stored model.User
	require.NoError(t, model.DB.First(&stored, invitee.Id).Error)
	assert.Equal(t, "verified.user@example.com", stored.Email)
	assert.True(t, stored.EmailVerified)
	require.NotNil(t, stored.VerifiedEmailKey)
	assert.Equal(t, common.SHA256Hex(stored.Email), *stored.VerifiedEmailKey)
	assert.Equal(t, 130, stored.Quota)
	var token model.Token
	require.NoError(t, model.DB.Where("user_id = ?", invitee.Id).First(&token).Error)
	assert.Equal(t, GroupAuto, token.Group)
	assert.True(t, token.UnlimitedQuota)

	var storedFlow model.AuthFlow
	require.NoError(t, model.DB.First(&storedFlow, flow.Id).Error)
	assert.NotNil(t, storedFlow.ConsumedAt)

	replay := referralUser("verification-replay", 0, 0)
	err := CreateUserWithReferralAndVerifiedEmail(replay, email, code)
	assert.ErrorIs(t, err, ErrInvalidVerificationCode)
	var replayCount int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", replay.Username).Count(&replayCount).Error)
	assert.Zero(t, replayCount)
}

func TestEmailVerificationPolicyCannotBeBypassedBetweenIssuanceAndRegistration(t *testing.T) {
	setupAffiliateDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.EmailDomainRestrictionEnabledOption: "true",
		setting.EmailAliasRestrictionEnabledOption:  "true",
		setting.EmailDomainWhitelistOption:          "example.com",
	}))
	previous := Mail
	mailer := &mockMailer{}
	Mail = mailer
	t.Cleanup(func() { Mail = previous })

	assert.ErrorIs(t, SendEmailVerificationCode("member@other.example"), setting.ErrEmailDomainNotAllowed)
	assert.ErrorIs(t, SendEmailVerificationCode("member+tag@example.com"), setting.ErrEmailAliasNotAllowed)
	assert.Empty(t, mailer.sent)
	require.NoError(t, SendEmailVerificationCode("member@example.com"))
	require.Len(t, mailer.sent, 1)
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", EmailVerificationPurpose).First(&flow).Error)

	// Tightening policy after issuance invalidates the old code without
	// consuming it or creating an account.
	require.NoError(t, setting.UpdateOption(setting.EmailDomainWhitelistOption, "new.example"))
	candidate := referralUser("policy-change", 0, 0)
	err := CreateUserWithReferralAndVerifiedEmail(candidate, "member@example.com", flow.Payload)
	assert.ErrorIs(t, err, setting.ErrEmailDomainNotAllowed)
	assert.Zero(t, candidate.Id)
	var storedFlow model.AuthFlow
	require.NoError(t, model.DB.First(&storedFlow, flow.Id).Error)
	assert.Nil(t, storedFlow.ConsumedAt)
}

func TestCreateUserWithReferralAndVerifiedEmailRollsBackCodeAndAccount(t *testing.T) {
	setupAffiliateDB(t)
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	const email = "rollback@example.com"
	code, flow := prepareVerifiedRegistration(t, email)
	inviter := referralUser("rollback-inviter", 7, 0)
	require.NoError(t, model.DB.Create(inviter).Error)

	injected := errors.New("injected verified-registration credit failure")
	const callback = "test:fail_verified_registration_credit"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))

	invitee := referralUser("rollback-invitee", 100, inviter.Id)
	err := CreateUserWithReferralAndVerifiedEmail(invitee, email, code)
	require.ErrorIs(t, err, injected)
	assert.Zero(t, invitee.Id, "the caller must not observe an ID from a rolled-back insert")

	var accountCount int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", invitee.Username).Count(&accountCount).Error)
	assert.Zero(t, accountCount)
	var tokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, tokenCount, "a downstream failure must roll back the planned token")
	var storedFlow model.AuthFlow
	require.NoError(t, model.DB.First(&storedFlow, flow.Id).Error)
	assert.Nil(t, storedFlow.ConsumedAt, "a downstream failure must leave the code retryable")

	require.NoError(t, model.DB.Callback().Update().Remove(callback))
	require.NoError(t, CreateUserWithReferralAndVerifiedEmail(invitee, email, code))
	assert.NotZero(t, invitee.Id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", invitee.Id).Count(&tokenCount).Error)
	assert.EqualValues(t, 1, tokenCount)
}

func TestCreateUserWithVerifiedEmailConcurrentSingleWinner(t *testing.T) {
	setupAffiliateDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	const (
		email    = "single-owner@example.com"
		code     = "428193"
		attempts = 8
	)
	_, err := CreateAuthFlow(
		EmailVerificationPurpose, common.SHA256Hex(email), "email", 0, "", code, time.Minute,
	)
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan error, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for index := range attempts {
		go func(index int) {
			ready.Done()
			<-start
			candidate := referralUser("single-owner-"+string(rune('a'+index)), 0, 0)
			results <- CreateUserWithReferralAndVerifiedEmail(candidate, email, code)
		}(index)
	}
	ready.Wait()
	close(start)

	successes := 0
	for range attempts {
		if createErr := <-results; createErr == nil {
			successes++
		} else {
			assert.ErrorIs(t, createErr, ErrInvalidVerificationCode)
		}
	}
	assert.Equal(t, 1, successes)
	_, key, err := model.NormalizeVerifiedEmail(email)
	require.NoError(t, err)
	var owners int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Where("verified_email_key = ?", key).Count(&owners).Error)
	assert.EqualValues(t, 1, owners)
}
