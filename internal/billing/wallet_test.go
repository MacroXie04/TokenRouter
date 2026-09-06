package billing

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

func initWalletDB(t *testing.T) {
	t.Helper()
	// File-based DB with a busy timeout so concurrent goroutines share the
	// database (in-memory SQLite is per-connection).
	dsn := "file:" + filepath.Join(t.TempDir(), "wallet.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Redemption{}, &model.Checkin{}, &model.TopUp{}, &model.Log{}, &model.AuditLogOutbox{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.CheckinEnabledOption:                "true",
		setting.CheckinMinQuotaOption:               "1000",
		setting.CheckinMaxQuotaOption:               "1000",
		setting.PaymentComplianceConfirmedOption:    "true",
		setting.PaymentComplianceTermsVersionOption: CurrentPaymentComplianceTermsVersion,
	}))
}

func TestRedeemRequiresCurrentPaymentComplianceWithoutClaimingCode(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, 0)
	keys, err := CreateRedemptionBatch(1, "compliance", 125, 0, 1)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOption(setting.PaymentComplianceTermsVersionOption, "v0"))

	_, err = Redeem(user.Id, keys[0])
	require.ErrorIs(t, err, ErrPaymentComplianceRequired)

	var redemption model.Redemption
	require.NoError(t, model.DB.Where("key = ?", keys[0]).First(&redemption).Error)
	assert.Equal(t, RedemptionStatusEnabled, redemption.Status)
	assert.Zero(t, redemption.UsedUserId)
	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Zero(t, stored.Quota)
}

func TestRedeemCreditsOnce(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)

	keys, err := CreateRedemptionBatch(1, "test", 500, 0, 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)

	quota, err := Redeem(u.Id, keys[0])
	require.NoError(t, err)
	assert.Equal(t, 500, quota)

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 500, got.Quota)

	// Second redeem of the same code fails.
	_, err = Redeem(u.Id, keys[0])
	assert.Equal(t, ErrInvalidRedemption, err)
	assert.Equal(t, 500, got.Quota)
}

func TestCreateRedemptionBatchRejectsUnsafeParameters(t *testing.T) {
	initWalletDB(t)
	for _, test := range []struct {
		name   string
		userID int
		quota  int
		count  int
	}{
		{name: "missing owner", userID: 0, quota: 10, count: 1},
		{name: "zero quota", userID: 1, quota: 0, count: 1},
		{name: "negative quota", userID: 1, quota: -10, count: 1},
		{name: "oversized quota", userID: 1, quota: int(quotamath.MaxQuota) + 1, count: 1},
		{name: "zero count", userID: 1, quota: 10, count: 0},
		{name: "oversized batch", userID: 1, quota: 10, count: maxRedemptionBatchSize + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			keys, err := CreateRedemptionBatch(test.userID, "unsafe", test.quota, 0, test.count)
			require.Error(t, err)
			assert.Empty(t, keys)
		})
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.Redemption{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestConcurrentRedeemCreditsExactlyOnce(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)
	keys, err := CreateRedemptionBatch(1, "test", 100, 0, 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)

	const n = 10
	var wg sync.WaitGroup
	successes := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Redeem(u.Id, keys[0])
			successes <- err == nil
		}()
	}
	wg.Wait()
	close(successes)
	count := 0
	for s := range successes {
		if s {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly one redemption must succeed")
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 100, got.Quota)
}

func TestRedeemRollsBackClaimWhenCreditFails(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, 0)
	keys, err := CreateRedemptionBatch(1, "rollback", 250, 0, 1)
	require.NoError(t, err)

	injected := errors.New("injected redemption credit failure")
	const callback = "test:fail_redemption_user_credit"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	_, err = Redeem(user.Id, keys[0])
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Update().Remove(callback))

	var redemption model.Redemption
	require.NoError(t, model.DB.Where("key = ?", keys[0]).First(&redemption).Error)
	assert.Equal(t, RedemptionStatusEnabled, redemption.Status)
	assert.Zero(t, redemption.UsedUserId)
	var unchanged model.User
	require.NoError(t, model.DB.First(&unchanged, user.Id).Error)
	assert.Zero(t, unchanged.Quota)

	quota, err := Redeem(user.Id, keys[0])
	require.NoError(t, err)
	assert.Equal(t, 250, quota)
}

func TestRedeemRejectsOverflowWithoutConsumingCode(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, math.MaxInt-10)
	keys, err := CreateRedemptionBatch(1, "overflow", 20, 0, 1)
	require.NoError(t, err)

	_, err = Redeem(user.Id, keys[0])
	require.ErrorIs(t, err, ErrUserQuotaOverflow)
	var redemption model.Redemption
	require.NoError(t, model.DB.Where("key = ?", keys[0]).First(&redemption).Error)
	assert.Equal(t, RedemptionStatusEnabled, redemption.Status)
}

func TestCheckInOncePerDay(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)

	reward, err := CheckIn(u.Id)
	require.NoError(t, err)
	assert.Equal(t, DefaultCheckInQuota, reward)
	assert.True(t, CheckInStatus(u.Id))

	_, err = CheckIn(u.Id)
	assert.Equal(t, ErrAlreadyCheckedIn, err)

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, DefaultCheckInQuota, got.Quota)
}

func TestCheckInRollsBackRecordWhenCreditFails(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, 0)
	injected := errors.New("injected check-in credit failure")
	const callback = "test:fail_checkin_user_credit"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	_, err := CheckIn(user.Id)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Update().Remove(callback))

	var records int64
	require.NoError(t, model.DB.Model(&model.Checkin{}).Where("user_id = ?", user.Id).Count(&records).Error)
	assert.Zero(t, records)
	var unchanged model.User
	require.NoError(t, model.DB.First(&unchanged, user.Id).Error)
	assert.Zero(t, unchanged.Quota)

	reward, err := CheckIn(user.Id)
	require.NoError(t, err)
	assert.Equal(t, DefaultCheckInQuota, reward)
}

func TestCheckInRejectsOverflowWithoutRecording(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, math.MaxInt-10)

	_, err := CheckIn(user.Id)
	require.ErrorIs(t, err, ErrUserQuotaOverflow)
	var records int64
	require.NoError(t, model.DB.Model(&model.Checkin{}).Where("user_id = ?", user.Id).Count(&records).Error)
	assert.Zero(t, records)
}

func TestTopUpCompleteIdempotent(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)

	order, err := CreateTopUp(u.Id, 1000, 1.0, "balance", "balance")
	require.NoError(t, err)

	require.NoError(t, CompleteTopUp(u.Id, order.TradeNo, 1000))
	// Second completion must not double-credit.
	require.NoError(t, CompleteTopUp(u.Id, order.TradeNo, 1000))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 1000*quotamath.QuotaPerUnit, got.Quota)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ? AND content = ?", u.Id, LogTypeTopup, order.TradeNo).
		Count(&logs).Error)
	assert.EqualValues(t, 1, logs)
}

func TestTopUpCreditSnapshotOverflowAndLegacyPendingFailClosed(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, 0)
	_, err := CreateTopUpWithTradeNo(user.Id, quotamath.MaxQuota/int64(quotamath.QuotaPerUnit)+1,
		1, "alipay", PaymentProviderEpay, "overflow-credit")
	require.ErrorIs(t, err, ErrTopUpQuotaOverflow)

	legacy := model.TopUp{UserId: user.Id, Amount: 1, Money: 1, TradeNo: "legacy-credit-pending",
		PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay, Status: TopUpStatusPending}
	require.NoError(t, model.DB.Create(&legacy).Error)
	err = CompleteTopUp(user.Id, legacy.TradeNo, legacy.Amount)
	require.ErrorIs(t, err, ErrTopUpLegacyCreditRequiresReview)
	require.NoError(t, model.DB.First(&legacy, legacy.Id).Error)
	assert.Equal(t, TopUpStatusPending, legacy.Status)
	assert.Equal(t, TopUpReconciliationLegacyCredit, legacy.ReconciliationState)
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)

	// A historical successful row is never credited again, even though it has
	// no versioned credit snapshot.
	require.NoError(t, model.DB.Model(&legacy).Update("status", TopUpStatusSuccess).Error)
	require.NoError(t, CompleteTopUp(user.Id, legacy.TradeNo, legacy.Amount))
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
}

func TestConcurrentTopUpCompletionCreditsExactlyOnce(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)
	order, err := CreateTopUp(u.Id, 100, 1.0, "stripe", PaymentProviderStripe)
	require.NoError(t, err)

	const attempts = 12
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for range attempts {
		go func() {
			ready.Done()
			<-start
			errs <- CompleteTopUp(u.Id, order.TradeNo, order.Amount)
		}()
	}
	ready.Wait()
	close(start)
	for range attempts {
		require.NoError(t, <-errs)
	}

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 100*quotamath.QuotaPerUnit, got.Quota)
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ? AND content = ?", u.Id, LogTypeTopup, order.TradeNo).
		Count(&logs).Error)
	assert.EqualValues(t, 1, logs)
}

func TestTopUpCompletionRollsBackStatusWhenCreditFails(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)
	order, err := CreateTopUp(u.Id, 100, 1.0, "stripe", PaymentProviderStripe)
	require.NoError(t, err)

	injected := errors.New("injected user-credit failure")
	const callback = "test:fail_topup_user_credit"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(injected)
		}
	}))
	err = CompleteTopUp(u.Id, order.TradeNo, order.Amount)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Update().Remove(callback))

	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Zero(t, got.Quota)

	require.NoError(t, CompleteTopUp(u.Id, order.TradeNo, order.Amount))
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 100*quotamath.QuotaPerUnit, got.Quota, "rollback must leave the order safely retryable")
}

func TestTopUpCompletionRollsBackWhenDurableAuditCannotBeEnqueued(t *testing.T) {
	initWalletDB(t)
	user := testutil.NewUser(t, 0)
	order, err := CreateTopUp(user.Id, 1, 1, "alipay", PaymentProviderEpay)
	require.NoError(t, err)
	injected := errors.New("injected payment audit failure")
	const callback = "test:fail_topup_audit_enqueue"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err = CompleteTopUp(user.Id, order.TradeNo, order.Amount)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var stored model.TopUp
	var got model.User
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Zero(t, got.Quota)
}

func TestTopUpCompletionRejectsUntrustedAmountAndOverflow(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)
	order, err := CreateTopUp(u.Id, 100, 1.0, "stripe", PaymentProviderStripe)
	require.NoError(t, err)

	assert.ErrorIs(t, CompleteTopUp(u.Id, order.TradeNo, 101), ErrTopUpAmountMismatch)
	assert.ErrorIs(t, CompleteTopUp(u.Id, "unknown-trade", 100), ErrTopUpNotFound)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", u.Id).Update("quota", math.MaxInt-50).Error)
	assert.ErrorIs(t, CompleteTopUp(u.Id, order.TradeNo, 100), ErrTopUpQuotaOverflow)

	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
}

func TestCreateTopUpRejectsOutOfDomainFinancialInputs(t *testing.T) {
	initWalletDB(t)
	u := testutil.NewUser(t, 0)
	for name, create := range map[string]func() (*model.TopUp, error){
		"zero user":      func() (*model.TopUp, error) { return CreateTopUp(0, 1, 1, "stripe", "stripe") },
		"zero amount":    func() (*model.TopUp, error) { return CreateTopUp(u.Id, 0, 1, "stripe", "stripe") },
		"large amount":   func() (*model.TopUp, error) { return CreateTopUp(u.Id, quotamath.MaxQuota+1, 1, "stripe", "stripe") },
		"NaN money":      func() (*model.TopUp, error) { return CreateTopUp(u.Id, 1, math.NaN(), "stripe", "stripe") },
		"infinite money": func() (*model.TopUp, error) { return CreateTopUp(u.Id, 1, math.Inf(1), "stripe", "stripe") },
		"negative money": func() (*model.TopUp, error) { return CreateTopUp(u.Id, 1, -1, "stripe", "stripe") },
		"empty trade": func() (*model.TopUp, error) {
			return CreateTopUpWithTradeNo(u.Id, 1, 1, "stripe", "stripe", " ")
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := create()
			require.ErrorIs(t, err, ErrTopUpAmountMismatch)
		})
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	assert.Zero(t, count)
}
