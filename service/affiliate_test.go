package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupAffiliateDB(t *testing.T) {
	t.Helper()
	t.Setenv(GenerateDefaultTokenEnvironment, "false")
	dsn := "file:" + filepath.Join(t.TempDir(), "affiliate.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PaymentComplianceConfirmedOption:    "true",
		setting.PaymentComplianceTermsVersionOption: CurrentPaymentComplianceTermsVersion,
		setting.QuotaForInviteeOption:               "30",
		setting.QuotaForInviterOption:               "50",
		setting.QuotaForNewUserOption:               "100",
		setting.QuotaPerUnitOption:                  "10",
	}))
}

func referralUser(username string, quota, inviterID int) *model.User {
	return &model.User{
		Username: username, Password: "password", DisplayName: username,
		Role: 1, Status: model.UserStatusEnabled, Quota: quota,
		InviterId: inviterID, AuthVersion: 1,
	}
}

func TestCreateUserWithReferralCommitsAllCredits(t *testing.T) {
	setupAffiliateDB(t)
	inviter := referralUser("inviter", 7, 0)
	require.NoError(t, model.DB.Create(inviter).Error)
	invitee := referralUser("invitee", 100, inviter.Id)

	require.NoError(t, CreateUserWithReferral(invitee))
	var gotInviter, gotInvitee model.User
	require.NoError(t, model.DB.First(&gotInviter, inviter.Id).Error)
	require.NoError(t, model.DB.First(&gotInvitee, invitee.Id).Error)
	assert.Equal(t, 130, gotInvitee.Quota)
	assert.Equal(t, 1, gotInviter.AffCount)
	assert.Equal(t, 50, gotInviter.AffQuota)
	assert.Equal(t, 50, gotInviter.AffHistoryQuota)
}

func TestCreateUserWithReferralRollsBackAccountWhenCreditFails(t *testing.T) {
	setupAffiliateDB(t)
	inviter := referralUser("inviter", 7, 0)
	require.NoError(t, model.DB.Create(inviter).Error)

	injected := errors.New("injected referral update failure")
	const callback = "test:fail_referral_credit"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	invitee := referralUser("rolled-back", 100, inviter.Id)
	err := CreateUserWithReferral(invitee)
	require.ErrorIs(t, err, injected)

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", invitee.Username).Count(&count).Error)
	assert.Zero(t, count)
	var gotInviter model.User
	require.NoError(t, model.DB.First(&gotInviter, inviter.Id).Error)
	assert.Zero(t, gotInviter.AffCount)
	assert.Zero(t, gotInviter.AffQuota)
}

func TestCreateUserWithReferralRejectsOverflowAtomically(t *testing.T) {
	setupAffiliateDB(t)
	inviter := referralUser("inviter", 7, 0)
	require.NoError(t, model.DB.Create(inviter).Error)
	invitee := referralUser("overflow", int(common.MaxQuota)-10, inviter.Id)
	require.NoError(t, setting.UpdateOption(setting.QuotaForNewUserOption, fmt.Sprintf("%d", common.MaxQuota-10)))
	require.NoError(t, setting.UpdateOption(setting.QuotaForInviteeOption, "20"))

	err := CreateUserWithReferral(invitee)
	require.ErrorIs(t, err, ErrReferralCreditOverflow)
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", invitee.Username).Count(&count).Error)
	assert.Zero(t, count)
	var gotInviter model.User
	require.NoError(t, model.DB.First(&gotInviter, inviter.Id).Error)
	assert.Zero(t, gotInviter.AffCount, "the earlier inviter update must roll back")
	assert.Zero(t, gotInviter.AffQuota)
}

func TestConcurrentReferralCreditsDoNotLoseUpdates(t *testing.T) {
	setupAffiliateDB(t)
	inviter := referralUser("inviter", 0, 0)
	require.NoError(t, model.DB.Create(inviter).Error)

	const registrations = 12
	start := make(chan struct{})
	errs := make(chan error, registrations)
	var ready sync.WaitGroup
	ready.Add(registrations)
	for i := 0; i < registrations; i++ {
		go func(index int) {
			ready.Done()
			<-start
			errs <- CreateUserWithReferral(referralUser(fmt.Sprintf("invitee-%d", index), 0, inviter.Id))
		}(i)
	}
	ready.Wait()
	close(start)
	for range registrations {
		require.NoError(t, <-errs)
	}

	var got model.User
	require.NoError(t, model.DB.First(&got, inviter.Id).Error)
	assert.Equal(t, registrations, got.AffCount)
	assert.Equal(t, registrations*50, got.AffQuota)
	assert.Equal(t, registrations*50, got.AffHistoryQuota)
	var invitees []model.User
	require.NoError(t, model.DB.Where("inviter_id = ?", inviter.Id).Find(&invitees).Error)
	require.Len(t, invitees, registrations)
	for _, invitee := range invitees {
		assert.Equal(t, 130, invitee.Quota)
	}
}

func TestCreateUserWithReferralUsesCanonicalQuotaAndCreatesTokenInTransaction(t *testing.T) {
	setupAffiliateDB(t)
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.InitialQuotaOption:                  "999",
		setting.QuotaForNewUserOption:               "23",
		setting.DefaultUseAutoGroupOption:           "true",
		setting.DefaultGroupOption:                  "starter",
		setting.QuotaForInviteeOption:               "0",
		setting.QuotaForInviterOption:               "0",
		setting.PaymentComplianceTermsVersionOption: CurrentPaymentComplianceTermsVersion,
	}))

	candidate := referralUser("planned-registration", 777, 0)
	require.NoError(t, CreateUserWithReferral(candidate))
	var stored model.User
	require.NoError(t, model.DB.First(&stored, candidate.Id).Error)
	assert.Equal(t, 23, stored.Quota, "the canonical internal-unit quota must overwrite caller input")
	assert.Equal(t, "starter", stored.Group)

	var tokens []model.Token
	require.NoError(t, model.DB.Where("user_id = ?", candidate.Id).Find(&tokens).Error)
	require.Len(t, tokens, 1)
	assert.Equal(t, GroupAuto, tokens[0].Group)
	assert.True(t, tokens[0].UnlimitedQuota)
	assert.Equal(t, common.QuotaPerUnit, tokens[0].RemainQuota)
	assert.Len(t, tokens[0].Key, len("sk-")+defaultTokenCredentialBytes)
	assert.NotContains(t, tokens[0].Key, candidate.Username)
}

func TestCreateUserWithReferralUsesLegacyQuotaOnlyWhenCanonicalAbsent(t *testing.T) {
	setupAffiliateDB(t)
	require.NoError(t, model.DB.Where("key = ?", setting.QuotaForNewUserOption).Delete(&model.Option{}).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).Where("key = ?", setting.InitialQuotaOption).
		Update("value", "55").Error)
	var legacy model.Option
	if err := model.DB.Where("key = ?", setting.InitialQuotaOption).First(&legacy).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		require.NoError(t, model.DB.Create(&model.Option{Key: setting.InitialQuotaOption, Value: "55"}).Error)
	} else {
		require.NoError(t, err)
	}
	require.NoError(t, setting.Sync())
	assert.True(t, setting.GetRegistrationGroupPolicy().UsesLegacyQuotaFallback())

	candidate := referralUser("legacy-fallback", 999, 0)
	require.NoError(t, CreateUserWithReferral(candidate))
	var stored model.User
	require.NoError(t, model.DB.First(&stored, candidate.Id).Error)
	assert.Equal(t, 55, stored.Quota)
}

func TestCreateUserWithReferralRollsBackPlannedTokenOnDownstreamFailure(t *testing.T) {
	setupAffiliateDB(t)
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	inviter := referralUser("token-rollback-inviter", 0, 0)
	require.NoError(t, model.DB.Create(inviter).Error)

	injected := errors.New("injected post-token referral failure")
	const callback = "test:fail_referral_after_default_token"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	candidate := referralUser("token-rollback-invitee", 900, inviter.Id)
	err := CreateUserWithReferral(candidate)
	require.ErrorIs(t, err, injected)
	assert.Zero(t, candidate.Id)
	var userCount, tokenCount int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", candidate.Username).Count(&userCount).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, userCount)
	assert.Zero(t, tokenCount, "default token must roll back with the user and referral credit")
}

func TestPasswordRegistrationGatesAreRecheckedCoherentlyInsideTransaction(t *testing.T) {
	setupAffiliateDB(t)
	// Simulate another node changing the durable setting before this process's
	// hot-reload. The cached precheck remains enabled, but account creation must
	// still fail closed at its transactional authorization boundary.
	require.True(t, setting.GetRegistrationGroupPolicy().RegistrationEnabled())
	require.True(t, setting.GetRegistrationGroupPolicy().PasswordRegistrationEnabled())
	require.NoError(t, model.DB.Create(&model.Option{
		Key: setting.RegistrationEnabledOption, Value: "false",
	}).Error)

	globalDisabled := referralUser("registration-disabled-in-transaction", 0, 0)
	err := CreateUserWithReferral(globalDisabled)
	require.ErrorIs(t, err, ErrRegistrationDisabled)
	assert.Zero(t, globalDisabled.Id)

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.RegistrationEnabledOption).Update("value", "true").Error)
	require.NoError(t, model.DB.Create(&model.Option{
		Key: setting.PasswordRegisterEnabledOption, Value: "false",
	}).Error)
	passwordDisabled := referralUser("password-disabled-in-transaction", 0, 0)
	err = CreateUserWithReferral(passwordDisabled)
	require.ErrorIs(t, err, ErrPasswordRegistrationDisabled)
	assert.Zero(t, passwordDisabled.Id)

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.PasswordRegisterEnabledOption).Update("value", "malformed").Error)
	malformed := referralUser("malformed-registration-gate", 0, 0)
	err = CreateUserWithReferral(malformed)
	require.ErrorIs(t, err, ErrRegistrationPolicyInvalid)
	assert.Zero(t, malformed.Id)

	var accountCount, tokenCount int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&accountCount).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, accountCount)
	assert.Zero(t, tokenCount)
}

func TestTransferAffQuotaRejectsOverflowWithoutDebiting(t *testing.T) {
	setupAffiliateDB(t)
	user := referralUser("affiliate", int(common.MaxQuota)-5, 0)
	user.AffQuota = common.QuotaPerUnit
	require.NoError(t, model.DB.Create(user).Error)

	err := TransferAffQuota(user.Id, common.QuotaPerUnit)
	require.ErrorIs(t, err, ErrReferralCreditOverflow)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, common.QuotaPerUnit, got.AffQuota)
	assert.Equal(t, int(common.MaxQuota)-5, got.Quota)
}

func TestTransferAffQuotaRequiresCurrentPaymentComplianceWithoutDebiting(t *testing.T) {
	setupAffiliateDB(t)
	user := referralUser("affiliate-compliance", 25, 0)
	user.AffQuota = common.QuotaPerUnit
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, setting.UpdateOption(setting.PaymentComplianceTermsVersionOption, "v0"))

	err := TransferAffQuota(user.Id, common.QuotaPerUnit)
	require.ErrorIs(t, err, ErrPaymentComplianceRequired)

	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Equal(t, common.QuotaPerUnit, stored.AffQuota)
	assert.Equal(t, 25, stored.Quota)
}

func TestTransferAffQuotaUsesImmutableQuotaUnit(t *testing.T) {
	setupAffiliateDB(t)
	user := referralUser("affiliate-fixed-unit", 25, 0)
	user.AffQuota = common.QuotaPerUnit
	require.NoError(t, model.DB.Create(user).Error)

	// setupAffiliateDB intentionally stores the legacy compatibility value 10.
	// It must not weaken the API boundary below the compiled accounting unit.
	err := TransferAffQuota(user.Id, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("%d", common.QuotaPerUnit))

	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Equal(t, common.QuotaPerUnit, stored.AffQuota)
	assert.Equal(t, 25, stored.Quota)

	require.NoError(t, TransferAffQuota(user.Id, common.QuotaPerUnit))
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Zero(t, stored.AffQuota)
	assert.Equal(t, 25+common.QuotaPerUnit, stored.Quota)
}
