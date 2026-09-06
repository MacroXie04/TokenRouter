package billing

import (
	"errors"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"math/rand/v2"
	"time"
)

var (
	// ErrAlreadyCheckedIn is returned when a user has already checked in today.
	ErrAlreadyCheckedIn = errors.New("今日已签到")
	// ErrCheckinDisabled prevents callers from bypassing the public feature gate.
	ErrCheckinDisabled = errors.New("签到功能未启用")
)

// DefaultCheckInQuota is the daily reward when no option is configured.
const DefaultCheckInQuota = 1000

// CheckInResult is the public result of a completed daily check-in.
type CheckInResult struct {
	QuotaAwarded int    `json:"quota_awarded"`
	CheckinDate  string `json:"checkin_date"`
}

// CheckIn retains the service's historical reward-only API for internal
// callers while the HTTP controller uses CheckInWithResult.
func CheckIn(userId int) (int, error) {
	result, err := CheckInWithResult(userId)
	if err != nil {
		return 0, err
	}
	return result.QuotaAwarded, nil
}

// CheckInWithResult records and credits one daily award, returning the exact
// date and quota persisted in the transaction.
func CheckInWithResult(userId int) (*CheckInResult, error) {
	if userId <= 0 {
		return nil, userssvc.ErrUserNotFound
	}
	config := setting.GetCheckinSetting()
	if !config.Enabled {
		return nil, ErrCheckinDisabled
	}
	today := time.Now().Format("2006-01-02")
	reward := config.MinQuota
	if config.MaxQuota > config.MinQuota {
		reward += rand.IntN(config.MaxQuota - config.MinQuota + 1)
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		// Lock the user first so concurrent check-ins for the same account make
		// the read/create/credit decision serially on every supported database.
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}
		var existing model.Checkin
		lookup := tx.Where("user_id = ? AND checkin_date = ?", userId, today).First(&existing)
		if lookup.Error == nil {
			return ErrAlreadyCheckedIn
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		record := model.Checkin{
			UserId:       userId,
			CheckinDate:  today,
			QuotaAwarded: reward,
			CreatedAt:    wallclock.NowTimestamp(),
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		return increaseUserQuotaTx(tx, userId, reward)
	})
	if err != nil {
		return nil, err
	}
	return &CheckInResult{QuotaAwarded: reward, CheckinDate: today}, nil
}

// CheckInStatus reports whether the user has checked in today. Callers that
// expose an API response should use CheckInStatusChecked so a database outage
// cannot masquerade as a successful "not checked in" result.
func CheckInStatus(userId int) bool {
	checkedIn, _ := CheckInStatusChecked(userId)
	return checkedIn
}

// CheckInStatusChecked is the error-returning status lookup.
func CheckInStatusChecked(userId int) (bool, error) {
	today := time.Now().Format("2006-01-02")
	var count int64
	if err := model.DB.Model(&model.Checkin{}).
		Where("user_id = ? AND checkin_date = ?", userId, today).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
