package model

import "time"

// CustomOAuthProvider is a custom OAuth/OIDC provider config.
type CustomOAuthProvider struct {
	Id                    int       `json:"id" gorm:"primaryKey"`
	Name                  string    `json:"name" gorm:"type:varchar(64);not null"`
	Slug                  string    `json:"slug" gorm:"type:varchar(64);not null;uniqueIndex"`
	Icon                  string    `json:"icon" gorm:"type:varchar(128);default:''"`
	Enabled               bool      `json:"enabled" gorm:"default:false"`
	ClientId              string    `json:"client_id" gorm:"type:varchar(256)"`
	ClientSecret          string    `json:"-" gorm:"type:varchar(512)"`
	AuthorizationEndpoint string    `json:"authorization_endpoint" gorm:"type:varchar(512)"`
	TokenEndpoint         string    `json:"token_endpoint" gorm:"type:varchar(512)"`
	UserInfoEndpoint      string    `json:"user_info_endpoint" gorm:"type:varchar(512)"`
	Scopes                string    `json:"scopes" gorm:"type:varchar(256);default:'openid profile email'"`
	UserIdField           string    `json:"user_id_field" gorm:"type:varchar(128);default:'sub'"`
	UsernameField         string    `json:"username_field" gorm:"type:varchar(128);default:'preferred_username'"`
	DisplayNameField      string    `json:"display_name_field" gorm:"type:varchar(128);default:'name'"`
	EmailField            string    `json:"email_field" gorm:"type:varchar(128);default:'email'"`
	WellKnown             string    `json:"well_known" gorm:"type:varchar(512)"`
	AuthStyle             int       `json:"auth_style" gorm:"default:0"`
	AccessPolicy          string    `json:"access_policy" gorm:"type:text"`
	AccessDeniedMessage   string    `json:"access_denied_message" gorm:"type:varchar(512)"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (CustomOAuthProvider) TableName() string { return "custom_oauth_providers" }

// UserOAuthBinding binds a user account to an OAuth provider identity.
type UserOAuthBinding struct {
	Id             int       `json:"id" gorm:"primaryKey"`
	UserId         int       `json:"user_id" gorm:"not null;uniqueIndex:ux_user_provider,priority:1"`
	ProviderId     int       `json:"provider_id" gorm:"not null;uniqueIndex:ux_user_provider,priority:2;uniqueIndex:ux_provider_userid,priority:1"`
	ProviderUserId string    `json:"provider_user_id" gorm:"type:varchar(256);not null;uniqueIndex:ux_provider_userid,priority:2"`
	CreatedAt      time.Time `json:"created_at"`
}

func (UserOAuthBinding) TableName() string { return "user_oauth_bindings" }
