package model

import "gorm.io/gorm"

// Token is a relay/dashboard API access token.
type Token struct {
	Id                 int            `json:"id" gorm:"primaryKey"`
	UserId             int            `json:"user_id" gorm:"index"`
	Key                string         `json:"key" gorm:"type:varchar(128);uniqueIndex"`
	Status             int            `json:"status"`
	Name               string         `json:"name" gorm:"index;type:varchar(64)"`
	CreatedTime        int64          `json:"created_time"`
	AccessedTime       int64          `json:"accessed_time"`
	ExpiredTime        int64          `json:"expired_time"`
	RemainQuota        int            `json:"remain_quota"`
	UnlimitedQuota     bool           `json:"unlimited_quota"`
	ModelLimitsEnabled bool           `json:"model_limits_enabled"`
	ModelLimits        string         `json:"model_limits" gorm:"type:text"`
	AllowIps           string         `json:"allow_ips" gorm:"type:varchar(128)"`
	UsedQuota          int            `json:"used_quota"`
	Group              string         `json:"group" gorm:"type:varchar(64)"`
	CrossGroupRetry    bool           `json:"cross_group_retry"`
	AutoGroups         string         `json:"auto_groups" gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Token) TableName() string { return "tokens" }
