package model

// SubscriptionPlan is a subscription product plan.
type SubscriptionPlan struct {
	Id       int    `json:"id" gorm:"primaryKey"`
	Title    string `json:"title" gorm:"type:varchar(128);not null"`
	Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`
	// PriceAmount remains a decimal string in Go/JSON so payment calculations
	// never depend on binary floating-point, while the database enforces the
	// reference DECIMAL(10,6) domain.
	PriceAmount   string `json:"price_amount" gorm:"type:decimal(10,6);not null"`
	Currency      string `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`
	DurationUnit  string `json:"duration_unit" gorm:"type:varchar(16);not null;default:'month'"`
	DurationValue int    `json:"duration_value" gorm:"type:int;not null;default:1"`
	CustomSeconds int64  `json:"custom_seconds" gorm:"type:bigint;not null;default:0"`
	// The migration installs the reference database default for raw inserts.
	// Keeping it out of the GORM tag preserves explicit Enabled=false creates.
	Enabled                 bool   `json:"enabled"`
	SortOrder               int    `json:"sort_order" gorm:"type:int;default:0"`
	AllowBalancePay         *bool  `json:"allow_balance_pay"`
	AllowWalletOverflow     *bool  `json:"allow_wallet_overflow"`
	StripePriceId           string `json:"stripe_price_id" gorm:"type:varchar(128);default:''"`
	CreemProductId          string `json:"creem_product_id" gorm:"type:varchar(128);default:''"`
	WaffoPancakeProductId   string `json:"waffo_pancake_product_id" gorm:"type:varchar(128);default:''"`
	MaxPurchasePerUser      int    `json:"max_purchase_per_user" gorm:"type:int;default:0"`
	UpgradeGroup            string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`
	DowngradeGroup          string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`
	TotalAmount             int64  `json:"total_amount" gorm:"type:bigint;not null;default:0"`
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:'never'"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`
	CreatedAt               int64  `json:"created_at" gorm:"type:bigint"`
	UpdatedAt               int64  `json:"updated_at" gorm:"type:bigint"`
}

func (SubscriptionPlan) TableName() string { return "subscription_plans" }

// SubscriptionOrder is a subscription payment order.
type SubscriptionOrder struct {
	Id                     int     `json:"id" gorm:"primaryKey"`
	UserId                 int     `json:"user_id" gorm:"index"`
	PlanId                 int     `json:"plan_id" gorm:"index"`
	Money                  float64 `json:"money"`
	TradeNo                string  `json:"trade_no" gorm:"type:varchar(255);not null;unique;index"`
	PaymentMethod          string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider        string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	ProviderAmountMinor    int64   `json:"-" gorm:"type:bigint;not null;default:0"`
	ProviderCurrency       string  `json:"-" gorm:"type:varchar(8);not null;default:''"`
	ProviderBindingVersion int     `json:"-" gorm:"not null;default:0"`
	ProviderSessionId      *string `json:"-" gorm:"type:varchar(255);uniqueIndex:idx_subscription_orders_provider_session_id"`
	ProviderOrderType      string  `json:"-" gorm:"type:varchar(32);not null;default:''"`
	ProviderMode           string  `json:"-" gorm:"type:varchar(32);not null;default:''"`
	ProviderPriceId        string  `json:"-" gorm:"type:varchar(255);not null;default:''"`
	EntitlementSnapshot    string  `json:"-" gorm:"type:text"`
	CapacityReserved       bool    `json:"-" gorm:"not null;default:false;index"`
	ReconciliationState    string  `json:"-" gorm:"type:varchar(64);not null;default:''"`
	ReconciliationDetail   string  `json:"-" gorm:"type:text"`
	// CheckoutRequest is the immutable, credential-free request snapshot used
	// to replay an ambiguous provider create with the original idempotency key.
	CheckoutRequest              string `json:"-" gorm:"type:text"`
	CheckoutFingerprint          string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	ProviderCreateIdempotencyKey string `json:"-" gorm:"type:varchar(255);not null;default:''"`
	ProviderExpiresAt            int64  `json:"-" gorm:"type:bigint;not null;default:0;index"`
	ReconciliationNextAt         int64  `json:"-" gorm:"type:bigint;not null;default:0;index:idx_subscription_order_reconcile,priority:2"`
	ReconciliationAttempts       int    `json:"-" gorm:"not null;default:0"`
	ReconciliationLeaseOwner     string `json:"-" gorm:"type:varchar(128);not null;default:'';index"`
	ReconciliationLeaseExpiresAt int64  `json:"-" gorm:"type:bigint;not null;default:0;index"`
	Status                       string `json:"status"`
	CreateTime                   int64  `json:"create_time"`
	CompleteTime                 int64  `json:"complete_time"`
	ProviderPayload              string `json:"-" gorm:"type:text"`
}

func (SubscriptionOrder) TableName() string { return "subscription_orders" }

// UserSubscription is an active user subscription instance.
type UserSubscription struct {
	Id          int   `json:"id" gorm:"primaryKey"`
	UserId      int   `json:"user_id" gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId      int   `json:"plan_id" gorm:"index"`
	AmountTotal int64 `json:"amount_total" gorm:"type:bigint;not null;default:0"`
	AmountUsed  int64 `json:"amount_used" gorm:"type:bigint;not null;default:0"`
	// UsageEpoch fences reservations across quota resets. A reservation made in
	// an older epoch may still become terminal, but can never mutate the current
	// window's AmountUsed counter.
	UsageEpoch     int64  `json:"-" gorm:"type:bigint;not null;default:0"`
	StartTime      int64  `json:"start_time" gorm:"bigint"`
	EndTime        int64  `json:"end_time" gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status         string `json:"status" gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"`
	Source         string `json:"source" gorm:"type:varchar(32);default:'order'"`
	LastResetTime  int64  `json:"last_reset_time" gorm:"type:bigint;default:0"`
	NextResetTime  int64  `json:"next_reset_time" gorm:"type:bigint;default:0;index"`
	UpgradeGroup   string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`
	PrevUserGroup  string `json:"prev_user_group" gorm:"type:varchar(64);default:''"`
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`
	// GroupBaseline is the group to restore after the last overlapping group
	// upgrade ends. It remains stable even when subscriptions expire out of
	// activation order.
	GroupBaseline string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// Reset cadence is copied from the purchased plan. Version zero is a
	// legacy row that may be backfilled once from its plan; versioned rows
	// never consult mutable plan cadence during fulfillment.
	EntitlementVersion              int    `json:"-" gorm:"not null;default:0"`
	EntitlementMigrationState       string `json:"-" gorm:"type:varchar(16);not null;default:'pending';index"`
	QuotaResetPeriodSnapshot        string `json:"-" gorm:"type:varchar(16);not null;default:''"`
	QuotaResetCustomSecondsSnapshot int64  `json:"-" gorm:"type:bigint;not null;default:0"`
	AllowWalletOverflow             bool   `json:"allow_wallet_overflow"`
	CreatedAt                       int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt                       int64  `json:"updated_at" gorm:"bigint"`
}

func (UserSubscription) TableName() string { return "user_subscriptions" }

// SubscriptionPreConsumeRecord is an idempotent subscription pre-consume ledger.
type SubscriptionPreConsumeRecord struct {
	Id                 int    `json:"id" gorm:"primaryKey"`
	RequestId          string `json:"request_id" gorm:"type:varchar(64);not null;uniqueIndex"`
	UserId             int    `json:"user_id" gorm:"index"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"index"`
	PreConsumed        int64  `json:"pre_consumed" gorm:"type:bigint;not null;default:0"`
	UsageEpoch         int64  `json:"-" gorm:"type:bigint;not null;default:0"`
	Status             string `json:"status" gorm:"type:varchar(32);index"`
	CreatedAt          int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt          int64  `json:"updated_at" gorm:"bigint;index"`
}

func (SubscriptionPreConsumeRecord) TableName() string { return "subscription_pre_consume_records" }
