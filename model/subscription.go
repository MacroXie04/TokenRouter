package model

// SubscriptionPlan is a subscription product plan.
type SubscriptionPlan struct {
	Id                     int    `json:"id" gorm:"primaryKey"`
	Title                  string `json:"title" gorm:"type:varchar(128);not null"`
	Subtitle               string `json:"subtitle" gorm:"type:varchar(255)"`
	PriceAmount            string `json:"price_amount" gorm:"type:varchar(64);not null"`
	Currency               string `json:"currency" gorm:"type:varchar(8);default:USD"`
	DurationUnit           string `json:"duration_unit" gorm:"type:varchar(16)"`
	DurationValue          int    `json:"duration_value"`
	CustomSeconds          int64  `json:"custom_seconds"`
	Enabled                bool   `json:"enabled"`
	SortOrder              int    `json:"sort_order"`
	AllowBalancePay        *bool  `json:"allow_balance_pay"`
	AllowWalletOverflow    *bool  `json:"allow_wallet_overflow"`
	StripePriceId          string `json:"stripe_price_id" gorm:"type:varchar(128)"`
	CreemProductId         string `json:"creem_product_id" gorm:"type:varchar(128)"`
	WaffoPancakeProductId  string `json:"waffo_pancake_product_id" gorm:"type:varchar(128)"`
	MaxPurchasePerUser     int    `json:"max_purchase_per_user"`
	UpgradeGroup           string `json:"upgrade_group" gorm:"type:varchar(64)"`
	DowngradeGroup         string `json:"downgrade_group" gorm:"type:varchar(64)"`
	TotalAmount            int64  `json:"total_amount"`
	QuotaResetPeriod       string `json:"quota_reset_period" gorm:"type:varchar(16)"`
	QuotaResetCustomSeconds int64 `json:"quota_reset_custom_seconds"`
	CreatedAt              int64  `json:"created_at"`
	UpdatedAt              int64  `json:"updated_at"`
}

func (SubscriptionPlan) TableName() string { return "subscription_plans" }

// SubscriptionOrder is a subscription payment order.
type SubscriptionOrder struct {
	Id              int     `json:"id" gorm:"primaryKey"`
	UserId          int     `json:"user_id" gorm:"index"`
	PlanId          int     `json:"plan_id" gorm:"index"`
	Money           float64 `json:"money"`
	TradeNo         string  `json:"trade_no" gorm:"type:varchar(255);uniqueIndex"`
	PaymentMethod   string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string  `json:"payment_provider" gorm:"type:varchar(50)"`
	Status          string  `json:"status" gorm:"type:varchar(50)"`
	CreateTime      int64   `json:"create_time"`
	CompleteTime    int64   `json:"complete_time"`
	ProviderPayload string  `json:"-" gorm:"type:text"`
}

func (SubscriptionOrder) TableName() string { return "subscription_orders" }

// UserSubscription is an active user subscription instance.
type UserSubscription struct {
	Id                  int    `json:"id" gorm:"primaryKey"`
	UserId              int    `json:"user_id" gorm:"index:idx_user_sub_active,priority:1"`
	PlanId              int    `json:"plan_id" gorm:"index"`
	AmountTotal         int64  `json:"amount_total"`
	AmountUsed          int64  `json:"amount_used"`
	StartTime           int64  `json:"start_time"`
	EndTime             int64  `json:"end_time" gorm:"index;index:idx_user_sub_active,priority:3"`
	Status              string `json:"status" gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"`
	Source              string `json:"source" gorm:"type:varchar(32)"`
	LastResetTime       int64  `json:"last_reset_time"`
	NextResetTime       int64  `json:"next_reset_time" gorm:"index"`
	UpgradeGroup        string `json:"upgrade_group" gorm:"type:varchar(64)"`
	PrevUserGroup       string `json:"prev_user_group" gorm:"type:varchar(64)"`
	DowngradeGroup      string `json:"downgrade_group" gorm:"type:varchar(64)"`
	AllowWalletOverflow bool   `json:"allow_wallet_overflow"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

func (UserSubscription) TableName() string { return "user_subscriptions" }

// SubscriptionPreConsumeRecord is an idempotent subscription pre-consume ledger.
type SubscriptionPreConsumeRecord struct {
	Id                 int    `json:"id" gorm:"primaryKey"`
	RequestId          string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `json:"user_id" gorm:"index"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"index"`
	PreConsumed        int64  `json:"pre_consumed"`
	Status             string `json:"status" gorm:"type:varchar(32);index"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at" gorm:"index"`
}

func (SubscriptionPreConsumeRecord) TableName() string { return "subscription_pre_consume_records" }
