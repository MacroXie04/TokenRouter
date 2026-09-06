package model

import (
	"errors"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
)

var (
	ErrVendorNameExists   = errors.New("供应商名称已存在")
	vendorMetadataWriteMu sync.Mutex
)

// SearchVendorMetadata returns active vendors in a deterministic order.
func SearchVendorMetadata(keyword string, offset, limit int) ([]*Vendor, int64, error) {
	query := DB.Model(&Vendor{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + strings.ToLower(keyword) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var vendors []*Vendor
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&vendors).Error; err != nil {
		return nil, 0, err
	}
	return vendors, total, nil
}

func GetVendorMetadataByID(id int) (*Vendor, error) {
	var vendor Vendor
	if err := DB.First(&vendor, id).Error; err != nil {
		return nil, err
	}
	return &vendor, nil
}

func CreateVendorMetadata(vendor *Vendor) error {
	vendorMetadataWriteMu.Lock()
	defer vendorMetadataWriteMu.Unlock()
	if vendorNameExists(DB, vendor.Name, 0) {
		return ErrVendorNameExists
	}
	now := common.NowTimestamp()
	vendor.Id = 0
	vendor.CreatedTime = now
	vendor.UpdatedTime = now
	status := vendor.Status
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(vendor).Error; err != nil {
			return err
		}
		return tx.Model(&Vendor{}).Where("id = ?", vendor.Id).
			Update("status", status).Error
	})
	vendor.Status = status
	if err != nil && vendorNameExists(DB, vendor.Name, 0) {
		return ErrVendorNameExists
	}
	return err
}

func UpdateVendorMetadata(vendor *Vendor) error {
	vendorMetadataWriteMu.Lock()
	defer vendorMetadataWriteMu.Unlock()
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Vendor
		query := tx.Where("id = ?", vendor.Id)
		if UsingMySQL() || UsingPostgreSQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&current).Error; err != nil {
			return err
		}
		if current.Name != vendor.Name && vendorNameExists(tx, vendor.Name, vendor.Id) {
			return ErrVendorNameExists
		}
		activeName := vendor.Name
		return tx.Model(&Vendor{}).Where("id = ?", vendor.Id).Updates(map[string]any{
			"name":         vendor.Name,
			"active_name":  activeName,
			"description":  vendor.Description,
			"icon":         vendor.Icon,
			"status":       vendor.Status,
			"updated_time": common.NowTimestamp(),
		}).Error
	})
	if err != nil && vendorNameExists(DB, vendor.Name, vendor.Id) {
		return ErrVendorNameExists
	}
	return err
}

func DeleteVendorMetadata(id int) error {
	vendorMetadataWriteMu.Lock()
	defer vendorMetadataWriteMu.Unlock()
	return DB.Transaction(func(tx *gorm.DB) error {
		var vendor Vendor
		query := tx.Where("id = ?", id)
		if UsingMySQL() || UsingPostgreSQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&vendor).Error; err != nil {
			return err
		}
		if err := tx.Model(&Vendor{}).Where("id = ?", id).Update("active_name", nil).Error; err != nil {
			return err
		}
		result := tx.Delete(&vendor)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func vendorNameExists(tx *gorm.DB, name string, excludedID int) bool {
	var count int64
	query := tx.Model(&Vendor{}).Where("name = ?", name)
	if excludedID > 0 {
		query = query.Where("id <> ?", excludedID)
	}
	return query.Count(&count).Error == nil && count > 0
}
