package model

import "gorm.io/gorm"

// Redemption is an invite/redeem code.
type Redemption struct {
	Id           int            `json:"id" gorm:"primaryKey"`
	UserId       int            `json:"user_id"`
	Key          string         `json:"key" gorm:"type:char(32);uniqueIndex"`
	Status       int            `json:"status" gorm:"default:1"`
	Name         string         `json:"name" gorm:"index"`
	Quota        int            `json:"quota" gorm:"default:100"`
	CreatedTime  int64          `json:"created_time" gorm:"bigint"`
	RedeemedTime int64          `json:"redeemed_time" gorm:"bigint"`
	UsedUserId   int            `json:"used_user_id"`
	ExpiredTime  int64          `json:"expired_time" gorm:"bigint"`
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
	Id     int   `json:"id" gorm:"primaryKey"`
	UserId int   `json:"user_id" gorm:"index"`
	Amount int64 `json:"amount"`
	// CreditQuota is the immutable internal quota granted by this order.
	// Version zero denotes a legacy row whose pending credit semantics cannot
	// safely be inferred after configuration/code changes.
	CreditQuota                  int64   `json:"-" gorm:"type:bigint;not null;default:0"`
	CreditQuotaVersion           int     `json:"-" gorm:"not null;default:0"`
	Money                        float64 `json:"money"`
	TradeNo                      string  `json:"trade_no" gorm:"type:varchar(255);not null;unique;index"`
	PaymentMethod                string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider              string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	ProviderAmountMinor          int64   `json:"-" gorm:"type:bigint;not null;default:0"`
	ProviderCurrency             string  `json:"-" gorm:"type:varchar(8);not null;default:''"`
	ProviderBindingVersion       int     `json:"-" gorm:"not null;default:0"`
	ProviderSessionId            *string `json:"-" gorm:"type:varchar(255);uniqueIndex:idx_topups_provider_session_id"`
	ProviderOrderType            string  `json:"-" gorm:"type:varchar(32);not null;default:''"`
	ProviderMode                 string  `json:"-" gorm:"type:varchar(32);not null;default:''"`
	ReconciliationState          string  `json:"-" gorm:"type:varchar(64);not null;default:''"`
	ReconciliationDetail         string  `json:"-" gorm:"type:text"`
	CheckoutRequest              string  `json:"-" gorm:"type:text"`
	CheckoutFingerprint          string  `json:"-" gorm:"type:varchar(64);not null;default:''"`
	ProviderCreateIdempotencyKey string  `json:"-" gorm:"type:varchar(255);not null;default:''"`
	ProviderExpiresAt            int64   `json:"-" gorm:"type:bigint;not null;default:0;index"`
	ReconciliationNextAt         int64   `json:"-" gorm:"type:bigint;not null;default:0;index:idx_topup_reconcile,priority:2"`
	ReconciliationAttempts       int     `json:"-" gorm:"not null;default:0"`
	ReconciliationLeaseOwner     string  `json:"-" gorm:"type:varchar(128);not null;default:'';index"`
	ReconciliationLeaseExpiresAt int64   `json:"-" gorm:"type:bigint;not null;default:0;index"`
	CreateTime                   int64   `json:"create_time"`
	CompleteTime                 int64   `json:"complete_time"`
	Status                       string  `json:"status"`
}

func (TopUp) TableName() string { return "top_ups" }
