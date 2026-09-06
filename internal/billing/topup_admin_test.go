package billing

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func setupTopUpAdminServiceTest(t *testing.T, compliance bool) {
	t.Helper()
	initWalletDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	require.NoError(t, setting.Init())
	setTopUpServiceCompliance(t, compliance)
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.PaymentComplianceConfirmedOption:    "false",
			setting.PaymentComplianceTermsVersionOption: "",
		})
	})
}

func setTopUpServiceCompliance(t *testing.T, confirmed bool) {
	t.Helper()
	version := ""
	if confirmed {
		version = CurrentPaymentComplianceTermsVersion
	}
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PaymentComplianceConfirmedOption:    strconv.FormatBool(confirmed),
		setting.PaymentComplianceTermsVersionOption: version,
	}))
}

func TestListTopUpsPaginationAndSafeSearch(t *testing.T) {
	setupTopUpAdminServiceTest(t, true)
	for _, tradeNo := range []string{"alpha", "prefix-alpha-suffix", "beta_1"} {
		_, err := CreateTopUpWithTradeNo(1, 10, 1, "alipay", PaymentProviderEpay, tradeNo)
		require.NoError(t, err)
	}

	items, total, err := ListTopUps("", 1, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, items, 2)
	assert.Equal(t, "beta_1", items[0].TradeNo)
	assert.Equal(t, "prefix-alpha-suffix", items[1].TradeNo)

	items, total, err = ListTopUps("", 2, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, items, 1)
	assert.Equal(t, "alpha", items[0].TradeNo)

	items, total, err = ListTopUps("alpha", 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "a keyword without '%' is exact, matching the reference")
	require.Len(t, items, 1)
	assert.Equal(t, "alpha", items[0].TradeNo)

	items, total, err = ListTopUps("%alpha%", 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, items, 2)

	items, total, err = ListTopUps("beta_1", 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "underscore must be searched literally")
	assert.Len(t, items, 1)

	for _, input := range []struct {
		keyword  string
		page     int
		pageSize int
	}{
		{keyword: "%a%", page: 1, pageSize: 10},
		{keyword: strings.Repeat("x", maxTopUpSearchKeywordLength+1), page: 1, pageSize: 10},
		{page: 0, pageSize: 10},
		{page: 1, pageSize: 0},
		{page: 1, pageSize: 101},
		{page: math.MaxInt, pageSize: 100},
	} {
		_, _, err := ListTopUps(input.keyword, input.page, input.pageSize)
		assert.ErrorIs(t, err, ErrTopUpQueryInvalid)
	}
}

func TestListUserTopUpsScopesOwnerWindowPaginationAndSearch(t *testing.T) {
	setupTopUpAdminServiceTest(t, true)
	for _, entry := range []struct {
		owner int
		trade string
	}{
		{owner: 1, trade: "owner-alpha"},
		{owner: 1, trade: "owner-beta_1"},
		{owner: 2, trade: "other-owner"},
		{owner: 1, trade: "owner-old"},
	} {
		_, err := CreateTopUpWithTradeNo(entry.owner, 10, 1, "alipay", PaymentProviderEpay, entry.trade)
		require.NoError(t, err)
	}
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", "owner-old").
		Update("create_time", wallclock.NowTimestamp()-userTopUpHistoryWindowSeconds-1).Error)

	items, total, err := ListUserTopUps(1, "", 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, items, 1)
	assert.Equal(t, "owner-beta_1", items[0].TradeNo)

	items, total, err = ListUserTopUps(1, "", 2, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, items, 1)
	assert.Equal(t, "owner-alpha", items[0].TradeNo)

	items, total, err = ListUserTopUps(1, "%alpha%", 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, "owner-alpha", items[0].TradeNo)

	items, total, err = ListUserTopUps(1, "owner-beta_1", 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "underscore is a literal search character")
	assert.Len(t, items, 1)

	for _, input := range []struct {
		owner    int
		keyword  string
		page     int
		pageSize int
	}{
		{owner: 0, page: 1, pageSize: 10},
		{owner: 1, keyword: "%x%", page: 1, pageSize: 10},
		{owner: 1, page: 0, pageSize: 10},
		{owner: 1, page: 1, pageSize: 101},
	} {
		_, _, listErr := ListUserTopUps(input.owner, input.keyword, input.page, input.pageSize)
		assert.ErrorIs(t, listErr, ErrTopUpQueryInvalid)
	}
}

func TestManualCompleteTopUpRecoveryBoundsStatusAndIdempotency(t *testing.T) {
	setupTopUpAdminServiceTest(t, false)
	user := testutil.NewUser(t, 0)
	order, err := CreateTopUpWithTradeNo(user.Id, 100, 1, "alipay", PaymentProviderEpay, "manual-valid")
	require.NoError(t, err)

	assert.ErrorIs(t, ManualCompleteTopUp("missing"), ErrTopUpNotFound)
	assert.ErrorIs(t, ManualCompleteTopUp(" "), ErrTopUpTradeNoInvalid)
	assert.ErrorIs(t, ManualCompleteTopUp(strings.Repeat("x", 256)), ErrTopUpTradeNoInvalid)

	failed, err := CreateTopUpWithTradeNo(user.Id, 10, 1, "alipay", PaymentProviderEpay, "manual-failed")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", failed.Id).Update("status", TopUpStatusFailed).Error)
	assert.ErrorIs(t, ManualCompleteTopUp(failed.TradeNo), ErrTopUpStatusInvalid)

	oversized := model.TopUp{UserId: user.Id, Amount: quotamath.MaxQuota + 1, Money: 1, TradeNo: "manual-oversized", Status: TopUpStatusPending}
	require.NoError(t, model.DB.Create(&oversized).Error)
	assert.ErrorIs(t, ManualCompleteTopUp(oversized.TradeNo), ErrTopUpAmountMismatch)

	invalidOwner := model.TopUp{UserId: 0, Amount: 10, Money: 1, TradeNo: "manual-owner", Status: TopUpStatusPending}
	require.NoError(t, model.DB.Create(&invalidOwner).Error)
	assert.ErrorIs(t, ManualCompleteTopUp(invalidOwner.TradeNo), ErrTopUpAmountMismatch)

	require.NoError(t, ManualCompleteTopUp(order.TradeNo))
	var credited model.User
	require.NoError(t, model.DB.First(&credited, user.Id).Error)
	assert.Equal(t, 100*quotamath.QuotaPerUnit, credited.Quota,
		"an existing order remains recoverable while new-payment compliance is disabled")

	require.NoError(t, ManualCompleteTopUp(order.TradeNo), "an already-successful order is an idempotent no-op")
	require.NoError(t, model.DB.First(&credited, user.Id).Error)
	assert.Equal(t, 100*quotamath.QuotaPerUnit, credited.Quota)
}

func TestConcurrentManualCompleteTopUpCreditsExactlyOnce(t *testing.T) {
	setupTopUpAdminServiceTest(t, true)
	user := testutil.NewUser(t, 0)
	order, err := CreateTopUpWithTradeNo(user.Id, 100, 1, "alipay", PaymentProviderEpay, "manual-concurrent")
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
			errs <- ManualCompleteTopUp(order.TradeNo)
		}()
	}
	ready.Wait()
	close(start)
	for range attempts {
		require.NoError(t, <-errs)
	}

	var userAfter model.User
	require.NoError(t, model.DB.First(&userAfter, user.Id).Error)
	assert.Equal(t, 100*quotamath.QuotaPerUnit, userAfter.Quota)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ? AND content = ?", user.Id, LogTypeTopup, order.TradeNo).
		Count(&logs).Error)
	assert.EqualValues(t, 1, logs)
}

func TestTopUpAdminDatabaseFailuresFailClosed(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	_, _, err := ListTopUps("", 1, 10)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTopUpQueryInvalid)
	assert.Error(t, ManualCompleteTopUp("manual-db-failure"))
}
