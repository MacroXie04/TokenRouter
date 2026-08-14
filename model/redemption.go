package model

import "gorm.io/gorm"

// Redemption is an invite/redeem code.
type Redemption struct {
	Id           int            `json:"id" gorm:"primaryKey"`
	UserId       int            `json:"user_id"`
	Key          string         `json:"key" gorm:"type:char(32);uniqueIndex"`
	Status       int            `json:"status"`
	Name         string         `json:"name" gorm:"index;type:varchar(64)"`
	Quota        int            `json:"quota" gorm:"default:100"`
	CreatedTime  int64          `json:"created_time"`
	RedeemedTime int64          `json:"redeemed_time"`
	UsedUserId   int            `json:"used_user_id"`
	ExpiredTime  int64          `json:"expired_time"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Redemption) TableName() string { return "redemptions" }

// Checkin is a daily check-in record.
type Checkin struct {
	Id           int    `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId       int    `json:"user_id" gorm:"not null;uniqueIndex:idx_user_checkin_date"`
	CheckinDate  string `json:"checkin_date" gorm:"type:varchar(10);not null;uniqueIndex:idx_user_checkin_date"`
	QuotaAwarded int    `json:"quota_awarded" gorm:"not null"`
	CreatedAt    int64  `json:"created_at"`
}

func (Checkin) TableName() string { return "checkins" }

// TopUp is a wallet recharge order.
type TopUp struct {
	Id              int    `json:"id" gorm:"primaryKey"`
	UserId          int    `json:"user_id" gorm:"index"`
	Amount          int64  `json:"amount"`
	Money           float64 `json:"money"`
	TradeNo         string `json:"trade_no" gorm:"type:varchar(255);uniqueIndex"`
	PaymentMethod   string `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50)"`
	CreateTime      int64  `json:"create_time"`
	CompleteTime    int64  `json:"complete_time"`
	Status          string `json:"status" gorm:"type:varchar(50)"`
}

func (TopUp) TableName() string { return "top_ups" }
