package model

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

// User status constants.
const (
	UserStatusEnabled  = 1
	UserStatusDisabled = 2
)

// BeforeCreate assigns a unique affiliate code to every new user. The column
// carries a unique index, so a shared empty value would break the second
// insert across all creation paths (register, OAuth, WeChat, import).
func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.AffCode == "" {
		affCode, err := common.SecureRandomAlphanumeric(8)
		if err != nil {
			return err
		}
		u.AffCode = affCode
	}
	if u.AuthVersion <= 0 {
		u.AuthVersion = 1
	}
	if u.EmailVerified {
		email, key, err := NormalizeVerifiedEmail(u.Email)
		if err != nil {
			return err
		}
		u.Email = email
		u.VerifiedEmailKey = &key
	} else {
		u.VerifiedEmailKey = nil
	}
	return nil
}

// User is the core account entity.
type User struct {
	Id               int     `json:"id" gorm:"primaryKey"`
	Username         string  `json:"username" gorm:"unique;index" validate:"max=20"`
	Password         string  `json:"password,omitempty" gorm:"not null" validate:"min=8,max=20"`
	DisplayName      string  `json:"display_name" gorm:"index" validate:"max=20"`
	Role             int     `json:"role" gorm:"type:int;default:1"`
	Status           int     `json:"status" gorm:"type:int;default:1"`
	Email            string  `json:"email" gorm:"index" validate:"max=50"`
	EmailVerified    bool    `json:"email_verified"`
	VerifiedEmailKey *string `json:"-" gorm:"column:verified_email_key;type:char(64);uniqueIndex:ux_users_verified_email_key"`
	QuotaReminderAt  int64   `json:"quota_reminder_at" gorm:"column:quota_reminder_at;default:0"`
	GitHubId         string  `json:"github_id" gorm:"column:github_id;index"`
	DiscordId        string  `json:"discord_id" gorm:"column:discord_id;index"`
	OidcId           string  `json:"oidc_id" gorm:"column:oidc_id;index"`
	WeChatId         string  `json:"wechat_id" gorm:"column:wechat_id;index"`
	TelegramId       string  `json:"telegram_id" gorm:"column:telegram_id;index"`
	AccessToken      *string `json:"access_token,omitempty" gorm:"type:char(32);column:access_token;uniqueIndex"`
	// AdminPermissions is the effective fine-grained permission matrix,
	// populated on demand (never persisted; computed from the permission
	// enforcer). Reference wire contract: json "admin_permissions".
	AdminPermissions map[string]map[string]bool `json:"admin_permissions,omitempty" gorm:"-:all"`
	Quota            int                        `json:"quota" gorm:"type:int;default:0"`
	UsedQuota        int                        `json:"used_quota" gorm:"type:int;default:0;column:used_quota"`
	RequestCount     int                        `json:"request_count" gorm:"type:int;default:0"`
	Group            string                     `json:"group" gorm:"type:varchar(64);default:'default'"`
	AffCode          string                     `json:"aff_code" gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount         int                        `json:"aff_count" gorm:"type:int;default:0;column:aff_count"`
	AffQuota         int                        `json:"aff_quota" gorm:"type:int;default:0;column:aff_quota"`
	AffHistoryQuota  int                        `json:"aff_history_quota" gorm:"type:int;default:0;column:aff_history_quota"`
	InviterId        int                        `json:"inviter_id" gorm:"type:int;column:inviter_id;index"`
	LinuxDOId        string                     `json:"linuxdo_id" gorm:"column:linuxdo_id;index"`
	Setting          string                     `json:"setting" gorm:"type:text;column:setting"`
	Remark           string                     `json:"remark" gorm:"type:varchar(255)" validate:"max=255"`
	StripeCustomer   string                     `json:"stripe_customer" gorm:"index;type:varchar(128);column:stripe_customer"`
	CreatedAt        int64                      `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	LastLoginAt      int64                      `json:"last_login_at" gorm:"default:0;column:last_login_at"`
	AuthVersion      int64                      `json:"auth_version" gorm:"type:bigint;not null;default:1;column:auth_version"`
	DeletedAt        gorm.DeletedAt             `json:"-" gorm:"index"`
}

func (User) TableName() string { return "users" }

// UserSession is the server-side session control plane for access JWTs.
type UserSession struct {
	SID                 string    `json:"sid" gorm:"column:sid;primaryKey;type:varchar(64);not null"`
	UserID              int       `json:"user_id" gorm:"not null;index:idx_user_sessions_user_status_expiry,priority:1;index:idx_user_sessions_user_created,priority:1"`
	Version             int64     `json:"version" gorm:"type:bigint;not null;default:1"`
	UserAuthVersion     int64     `json:"user_auth_version" gorm:"type:bigint;not null"`
	Status              string    `json:"status" gorm:"type:varchar(16);not null;index:idx_user_sessions_user_status_expiry,priority:2;index:idx_user_sessions_status_revoked,priority:1"`
	RefreshHash         string    `json:"-" gorm:"type:char(64);not null;uniqueIndex:ux_user_sessions_refresh_hash"`
	PreviousRefreshHash string    `json:"-" gorm:"type:varchar(64)"`
	PreviousValidUntil  int64     `json:"-" gorm:"type:bigint;not null;default:0"`
	LoginMethod         string    `json:"login_method" gorm:"type:varchar(32);not null"`
	IP                  string    `json:"ip" gorm:"type:varchar(64)"`
	UserAgent           string    `json:"user_agent" gorm:"type:text"`
	CreatedAt           time.Time `json:"created_at" gorm:"index:idx_user_sessions_user_created,priority:2"`
	LastActiveAt        int64     `json:"last_active_at" gorm:"type:bigint;not null"`
	ExpiresAt           int64     `json:"expires_at" gorm:"type:bigint;not null;index:idx_user_sessions_user_status_expiry,priority:3;index:idx_user_sessions_expires_at"`
	RevokedAt           int64     `json:"revoked_at" gorm:"type:bigint;not null;default:0;index:idx_user_sessions_status_revoked,priority:2"`
	RevokedReason       string    `json:"revoked_reason" gorm:"type:varchar(64)"`
}

func (UserSession) TableName() string { return "user_sessions" }

// AfterFind accepts padding left by the reference's legacy CHAR(64) refresh
// digest column. Valid digests are hexadecimal, so trimming SQL padding cannot
// change a legitimate token hash.
func (session *UserSession) AfterFind(_ *gorm.DB) error {
	session.PreviousRefreshHash = strings.TrimSpace(session.PreviousRefreshHash)
	return nil
}

// AuthFlow is a one-time short-lived auth ceremony state (OAuth/2FA/passkey).
type AuthFlow struct {
	Id         int64      `json:"id" gorm:"primaryKey"`
	TokenHash  string     `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	Purpose    string     `json:"purpose" gorm:"type:varchar(32);not null;index:idx_auth_flow_purpose_expiry"`
	Provider   string     `json:"provider" gorm:"type:varchar(64)"`
	Intent     string     `json:"intent" gorm:"type:varchar(128)"`
	UserId     int        `json:"user_id" gorm:"index"`
	SessionId  string     `json:"session_id" gorm:"type:varchar(64);index"`
	Payload    string     `json:"payload" gorm:"type:text"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at" gorm:"not null;index:idx_auth_flow_purpose_expiry"`
	ConsumedAt *time.Time `json:"consumed_at" gorm:"index"`
}

func (AuthFlow) TableName() string { return "auth_flows" }

// TwoFA holds per-user TOTP 2FA settings.
type TwoFA struct {
	Id             int            `json:"id" gorm:"primaryKey"`
	UserId         int            `json:"user_id" gorm:"unique;not null;index"`
	Secret         string         `json:"-" gorm:"type:varchar(255);not null"`
	IsEnabled      bool           `json:"is_enabled"`
	FailedAttempts int            `json:"failed_attempts" gorm:"default:0"`
	LockedUntil    *time.Time     `json:"locked_until"`
	LastUsedAt     *time.Time     `json:"last_used_at"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
}

func (TwoFA) TableName() string { return "two_fas" }

// TwoFABackupCode records a 2FA backup code (hashed).
type TwoFABackupCode struct {
	Id        int            `json:"id" gorm:"primaryKey"`
	UserId    int            `json:"user_id" gorm:"not null;index"`
	CodeHash  string         `json:"-" gorm:"type:varchar(255);not null"`
	IsUsed    bool           `json:"is_used"`
	UsedAt    *time.Time     `json:"used_at"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

func (TwoFABackupCode) TableName() string { return "two_fa_backup_codes" }

// PasskeyCredential stores a WebAuthn passkey credential (one per user).
type PasskeyCredential struct {
	ID              int            `json:"id" gorm:"primaryKey"`
	UserID          int            `json:"user_id" gorm:"uniqueIndex:ux_passkey_user;not null"`
	CredentialID    string         `json:"credential_id" gorm:"type:varchar(512);uniqueIndex;not null"`
	PublicKey       string         `json:"-" gorm:"type:text;not null"`
	AttestationType string         `json:"attestation_type" gorm:"type:varchar(255)"`
	AAGUID          string         `json:"aaguid" gorm:"type:varchar(512)"`
	SignCount       uint32         `json:"sign_count" gorm:"default:0"`
	CloneWarning    bool           `json:"clone_warning"`
	UserPresent     bool           `json:"user_present"`
	UserVerified    bool           `json:"user_verified"`
	BackupEligible  bool           `json:"backup_eligible"`
	BackupState     bool           `json:"backup_state"`
	Transports      string         `json:"transports" gorm:"type:text"`
	Attachment      string         `json:"attachment" gorm:"type:varchar(32)"`
	LastUsedAt      *time.Time     `json:"last_used_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

func (PasskeyCredential) TableName() string { return "passkey_credentials" }

// ExternalIdentityClaim is a durable external identity ownership record.
type ExternalIdentityClaim struct {
	Id          int64     `json:"id" gorm:"primaryKey"`
	Provider    string    `json:"provider" gorm:"type:varchar(32);not null;uniqueIndex:idx_external_identity_subject,priority:1;index:idx_external_identity_subject_hash,priority:1;uniqueIndex:idx_external_identity_user,priority:1"`
	Subject     string    `json:"subject" gorm:"type:varchar(128);not null;uniqueIndex:idx_external_identity_subject,priority:2"`
	SubjectHash string    `json:"-" gorm:"column:subject_hash;type:char(64);not null;index:idx_external_identity_subject_hash,priority:2"`
	UserId      int       `json:"user_id" gorm:"not null;index;uniqueIndex:idx_external_identity_user,priority:2"`
	CreatedAt   time.Time `json:"created_at"`
}

func (ExternalIdentityClaim) TableName() string { return "external_identity_claims" }
