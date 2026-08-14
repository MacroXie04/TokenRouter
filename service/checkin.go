package service

import (
	"errors"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// ErrAlreadyCheckedIn is returned when a user has already checked in today.
var ErrAlreadyCheckedIn = errors.New("今日已签到")

// DefaultCheckInQuota is the daily reward when no option is configured.
const DefaultCheckInQuota = 1000

// CheckIn records a daily check-in and credits quota once per day (keyed on the
// server-local date in YYYY-MM-DD).
func CheckIn(userId int) (int, error) {
	today := time.Now().Format("2006-01-02")
	var existing model.Checkin
	if err := model.DB.Where("user_id = ? AND checkin_date = ?", userId, today).First(&existing).Error; err == nil {
		return 0, ErrAlreadyCheckedIn
	}
	reward := getCheckInReward()
	rec := model.Checkin{
		UserId:       userId,
		CheckinDate:  today,
		QuotaAwarded: reward,
		CreatedAt:    common.NowTimestamp(),
	}
	if err := model.DB.Create(&rec).Error; err != nil {
		return 0, err
	}
	if err := IncreaseUserQuota(userId, reward); err != nil {
		return 0, err
	}
	return reward, nil
}

// CheckInStatus reports whether the user has checked in today.
func CheckInStatus(userId int) bool {
	today := time.Now().Format("2006-01-02")
	var count int64
	model.DB.Model(&model.Checkin{}).Where("user_id = ? AND checkin_date = ?", userId, today).Count(&count)
	return count > 0
}

func getCheckInReward() int {
	return setting.GetOptionIntOrDefault("CheckInQuota", DefaultCheckInQuota)
}
