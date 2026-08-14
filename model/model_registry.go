package model

import "gorm.io/gorm"

// Vendor is a model vendor/provider registry entry.
type Vendor struct {
	Id          int            `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"type:varchar(128);not null"`
	ActiveName  *string        `json:"-" gorm:"type:varchar(128)"`
	Description string         `json:"description,omitempty" gorm:"type:text"`
	Icon        string         `json:"icon,omitempty" gorm:"type:varchar(128)"`
	Status      int            `json:"status"`
	CreatedTime int64          `json:"created_time"`
	UpdatedTime int64          `json:"updated_time"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Vendor) TableName() string { return "vendors" }

// Model is a model metadata registry entry.
type Model struct {
	Id           int            `json:"id" gorm:"primaryKey"`
	ModelName    string         `json:"model_name" gorm:"type:varchar(128);not null"`
	ActiveName   *string        `json:"-" gorm:"type:varchar(128)"`
	Description  string         `json:"description,omitempty" gorm:"type:text"`
	Icon         string         `json:"icon,omitempty" gorm:"type:varchar(128)"`
	Tags         string         `json:"tags,omitempty" gorm:"type:varchar(255)"`
	VendorID     int            `json:"vendor_id,omitempty" gorm:"index"`
	Endpoints    string         `json:"endpoints,omitempty" gorm:"type:text"`
	Status       int            `json:"status"`
	SyncOfficial int            `json:"sync_official"`
	CreatedTime  int64          `json:"created_time"`
	UpdatedTime  int64          `json:"updated_time"`
	NameRule     int            `json:"name_rule"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`

	BoundChannels []BoundChannel `json:"bound_channels,omitempty" gorm:"-"`
	EnableGroups  []string       `json:"enable_groups,omitempty" gorm:"-"`
	QuotaTypes    []int          `json:"quota_types,omitempty" gorm:"-"`
	MatchedModels []string       `json:"matched_models,omitempty" gorm:"-"`
	MatchedCount  int            `json:"matched_count,omitempty" gorm:"-"`
}

func (Model) TableName() string { return "models" }

// PrefillGroup is a prefill prompt group.
type PrefillGroup struct {
	Id          int            `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"type:varchar(64);not null;uniqueIndex:uk_prefill_name,where:deleted_at IS NULL"`
	Type        string         `json:"type" gorm:"type:varchar(32);index;not null"`
	Items       JSONValue      `json:"items" gorm:"type:json"`
	Description string         `json:"description,omitempty" gorm:"type:varchar(255)"`
	CreatedTime int64          `json:"created_time"`
	UpdatedTime int64          `json:"updated_time"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

func (PrefillGroup) TableName() string { return "prefill_groups" }
