package model

import "gorm.io/gorm"

// Vendor is a model vendor/provider registry entry.
type Vendor struct {
	Id          int            `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"type:varchar(128);not null;uniqueIndex:uk_vendor_name_delete_at,priority:1"`
	Description string         `json:"description" gorm:"type:text"`
	Icon        string         `json:"icon" gorm:"type:varchar(128)"`
	Status      int            `json:"status"`
	CreatedTime int64          `json:"created_time"`
	UpdatedTime int64          `json:"updated_time"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index;uniqueIndex:uk_vendor_name_delete_at,priority:2"`
}

func (Vendor) TableName() string { return "vendors" }

// Model is a model metadata registry entry.
type Model struct {
	Id          int            `json:"id" gorm:"primaryKey"`
	ModelName   string         `json:"model_name" gorm:"type:varchar(128);not null;uniqueIndex:uk_model_name_delete_at,priority:1"`
	Description string         `json:"description" gorm:"type:text"`
	Icon        string         `json:"icon" gorm:"type:varchar(128)"`
	Tags        string         `json:"tags" gorm:"type:varchar(255)"`
	VendorID    int            `json:"vendor_id" gorm:"index"`
	Endpoints   string         `json:"endpoints" gorm:"type:text"`
	Status      int            `json:"status"`
	SyncOfficial bool          `json:"sync_official"`
	CreatedTime int64          `json:"created_time"`
	UpdatedTime int64          `json:"updated_time"`
	NameRule    int            `json:"name_rule"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index;uniqueIndex:uk_model_name_delete_at,priority:2"`
}

func (Model) TableName() string { return "models" }

// PrefillGroup is a prefill prompt group.
type PrefillGroup struct {
	Id          int            `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"type:varchar(64);not null;uniqueIndex:uk_prefill_name"`
	Type        string         `json:"type" gorm:"type:varchar(32);index;not null"`
	Items       string         `json:"items" gorm:"type:text"`
	Description string         `json:"description" gorm:"type:varchar(255)"`
	CreatedTime int64          `json:"created_time"`
	UpdatedTime int64          `json:"updated_time"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

func (PrefillGroup) TableName() string { return "prefill_groups" }
