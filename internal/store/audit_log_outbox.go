package store

// Audit-log outbox lifecycle states. Pending rows remain retryable until the
// configured log sink has durably accepted the event; delivered rows are kept
// as a content-free idempotency/reconciliation receipt.
const (
	AuditLogOutboxStatusPending   = "pending"
	AuditLogOutboxStatusDelivered = "delivered"
)

// AuditLogOutbox is the primary-database fallback for checked audit writes.
// Payload is intentionally excluded from every JSON view. Pending rows contain
// the complete log record (which may include user and request metadata), while
// successful delivery atomically replaces it with a one-way SHA-256 receipt.
// LeaseToken plus LeaseVersion fence stale workers after an expired lease is
// taken over by another process.
type AuditLogOutbox struct {
	ID              int64  `json:"id" gorm:"primaryKey;autoIncrement;index:idx_audit_log_outbox_scrub,priority:3"`
	EventID         string `json:"event_id" gorm:"type:varchar(64);not null;uniqueIndex:ux_audit_log_outbox_event"`
	Payload         string `json:"-" gorm:"type:text;not null"`
	Status          string `json:"status" gorm:"type:varchar(16);not null;index:idx_audit_log_outbox_due,priority:1;index:idx_audit_log_outbox_scrub,priority:1"`
	PayloadScrubbed bool   `json:"-" gorm:"not null;default:false;index:idx_audit_log_outbox_scrub,priority:2"`
	Attempts        int    `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt   int64  `json:"next_attempt_at" gorm:"not null;default:0;index:idx_audit_log_outbox_due,priority:2"`
	LeaseOwner      string `json:"lease_owner" gorm:"type:varchar(128);index"`
	LeaseToken      string `json:"-" gorm:"type:varchar(128);index"`
	LeaseVersion    int64  `json:"lease_version" gorm:"not null;default:0"`
	LeaseExpiresAt  int64  `json:"lease_expires_at" gorm:"not null;default:0;index"`
	LastError       string `json:"-" gorm:"type:varchar(255)"`
	CreatedAt       int64  `json:"created_at" gorm:"not null;index"`
	UpdatedAt       int64  `json:"updated_at" gorm:"not null;index"`
	DeliveredAt     int64  `json:"delivered_at" gorm:"not null;default:0;index"`
}

func (AuditLogOutbox) TableName() string { return "audit_log_outboxes" }
