package billing

import (
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"strings"
)

var (
	// ErrTopUpQueryInvalid rejects unsafe pagination and search input.
	ErrTopUpQueryInvalid = errors.New("充值记录查询参数无效")
	// ErrTopUpTradeNoInvalid rejects an empty or overlong manual-completion key.
	ErrTopUpTradeNoInvalid = errors.New("充值订单号无效")
)

const maxTopUpSearchKeywordLength = 255

const userTopUpHistoryWindowSeconds int64 = 30 * 24 * 60 * 60

// ListTopUps returns the full administrative top-up history newest-first.
// A non-empty keyword follows the reference contract: it is an exact trade-no
// match unless the caller supplies up to two validated '%' wildcards.
func ListTopUps(keyword string, page, pageSize int) ([]model.TopUp, int64, error) {
	if model.DB == nil {
		return nil, 0, errors.New("top-up database is unavailable")
	}
	if page < 1 || pageSize < 1 || pageSize > 100 || len(keyword) > maxTopUpSearchKeywordLength {
		return nil, 0, ErrTopUpQueryInvalid
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return nil, 0, ErrTopUpQueryInvalid
	}
	offset := (page - 1) * pageSize

	pattern := ""
	if keyword != "" {
		var err error
		pattern, err = sanitizeLikePattern(keyword)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %v", ErrTopUpQueryInvalid, err)
		}
	}

	items := make([]model.TopUp, 0)
	var total int64
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.TopUp{})
		if keyword != "" {
			query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}

		query = tx.Model(&model.TopUp{})
		if keyword != "" {
			query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
		}
		return query.Order("id desc").Limit(pageSize).Offset(offset).Find(&items).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("query top-up records: %w", err)
	}
	return items, total, nil
}

// ListUserTopUps returns one owner's recent top-up history using the same
// bounded paging and escaped search contract as the administrative list. The
// rolling window keeps an authenticated user's ordinary dashboard request
// from scanning an unbounded lifetime ledger; durable rows are never deleted.
func ListUserTopUps(userID int, keyword string, page, pageSize int) ([]model.TopUp, int64, error) {
	if model.DB == nil {
		return nil, 0, errors.New("top-up database is unavailable")
	}
	if userID <= 0 || page < 1 || pageSize < 1 || pageSize > 100 || len(keyword) > maxTopUpSearchKeywordLength {
		return nil, 0, ErrTopUpQueryInvalid
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return nil, 0, ErrTopUpQueryInvalid
	}
	offset := (page - 1) * pageSize
	pattern := ""
	if keyword != "" {
		var err error
		pattern, err = sanitizeLikePattern(keyword)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %v", ErrTopUpQueryInvalid, err)
		}
	}
	cutoff := wallclock.NowTimestamp() - userTopUpHistoryWindowSeconds
	items := make([]model.TopUp, 0)
	var total int64
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		base := func() *gorm.DB {
			query := tx.Model(&model.TopUp{}).
				Where("user_id = ? AND create_time >= ?", userID, cutoff)
			if keyword != "" {
				query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
			}
			return query
		}
		if err := base().Count(&total).Error; err != nil {
			return err
		}
		return base().Order("id desc").Limit(pageSize).Offset(offset).Find(&items).Error
	})
	if err != nil {
		return nil, 0, fmt.Errorf("query user top-up records: %w", err)
	}
	return items, total, nil
}

// ManualCompleteTopUp validates an administrative recovery request and
// delegates the actual state transition and credit to CompleteTopUp. Recovery
// of an existing pending order remains available when new payments are
// disabled; bounds, status, ownership, amount, and exactly-once checks still
// fail closed.
func ManualCompleteTopUp(tradeNo string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" || len(tradeNo) > 255 {
		return ErrTopUpTradeNoInvalid
	}
	order, err := GetTopUpByTradeNo(tradeNo)
	if err != nil {
		return err
	}
	if order.Id <= 0 || order.UserId <= 0 {
		return ErrTopUpAmountMismatch
	}
	switch order.Status {
	case TopUpStatusSuccess:
		return nil
	case TopUpStatusPending:
		// Continue below.
	default:
		return ErrTopUpStatusInvalid
	}
	if order.Amount <= 0 || order.Amount > quotamath.MaxQuota ||
		order.Money <= 0 || math.IsNaN(order.Money) || math.IsInf(order.Money, 0) {
		return ErrTopUpAmountMismatch
	}
	return CompleteTopUp(order.UserId, order.TradeNo, order.Amount)
}
