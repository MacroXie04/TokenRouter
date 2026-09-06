package billing

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func newTokenQuotaDB(t *testing.T) *model.Token {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "token-quota.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	model.DB = db
	token := &model.Token{UserId: 1, Key: "sk-token-quota", Status: TokenStatusEnabled, RemainQuota: 100}
	require.NoError(t, db.Create(token).Error)
	return token
}

func TestTokenQuotaHelpersRejectInvalidAmountsAndMissingRows(t *testing.T) {
	token := newTokenQuotaDB(t)

	for _, operation := range []func(int, int) error{
		ReserveTokenQuota,
		RefundTokenQuotaReservation,
		CommitTokenQuotaReservation,
		DecreaseTokenQuota,
	} {
		require.ErrorIs(t, operation(token.Id, -1), ErrInvalidQuota)
	}
	require.ErrorIs(t, ReserveTokenQuota(token.Id+999, 1), ErrTokenNotFound)
	require.ErrorIs(t, RefundTokenQuotaReservation(token.Id+999, 1), ErrTokenNotFound)
	require.ErrorIs(t, CommitTokenQuotaReservation(token.Id+999, 1), ErrTokenNotFound)
	require.ErrorIs(t, DecreaseTokenQuota(token.Id+999, 1), ErrTokenNotFound)
}

func TestTokenQuotaRefundAndCommitRejectOverflow(t *testing.T) {
	token := newTokenQuotaDB(t)
	require.NoError(t, model.DB.Model(token).Updates(map[string]any{
		"remain_quota": int(quotamath.MaxQuota),
		"used_quota":   int(quotamath.MaxQuota),
	}).Error)

	require.ErrorIs(t, RefundTokenQuotaReservation(token.Id, 1), ErrTokenQuotaOverflow)
	require.ErrorIs(t, CommitTokenQuotaReservation(token.Id, 1), ErrTokenQuotaOverflow)
	require.ErrorIs(t, DecreaseTokenQuota(token.Id, 1), ErrTokenQuotaOverflow)

	var got model.Token
	require.NoError(t, model.DB.First(&got, token.Id).Error)
	assert.Equal(t, int(quotamath.MaxQuota), got.RemainQuota)
	assert.Equal(t, int(quotamath.MaxQuota), got.UsedQuota)
}

func TestDecreaseTokenQuotaIsAtomicOnInsufficientBalance(t *testing.T) {
	token := newTokenQuotaDB(t)
	require.ErrorIs(t, DecreaseTokenQuota(token.Id, 101), ErrInsufficientTokenQuota)

	var got model.Token
	require.NoError(t, model.DB.First(&got, token.Id).Error)
	assert.Equal(t, 100, got.RemainQuota)
	assert.Zero(t, got.UsedQuota)
}
