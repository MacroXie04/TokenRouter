package store

import (
	"context"
	"errors"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	ModelNameRuleExact = iota
	ModelNameRulePrefix
	ModelNameRuleContains
	ModelNameRuleSuffix
)

var (
	ErrModelNameExists   = errors.New("模型名称已存在")
	modelMetadataWriteMu sync.Mutex
)

type BoundChannel struct {
	Name string `json:"name"`
	Type int    `json:"type"`
}

type ModelSearchFilter struct {
	Keyword      string
	Vendor       string
	Status       *int
	SyncOfficial *int
	Offset       int
	Limit        int
}

func SearchModelMetadata(filter ModelSearchFilter) ([]*Model, int64, error) {
	query := DB.Model(&Model{})
	if filter.Keyword != "" {
		like := "%" + strings.ToLower(filter.Keyword) + "%"
		query = query.Where("LOWER(models.model_name) LIKE ? OR LOWER(models.description) LIKE ? OR LOWER(models.tags) LIKE ?", like, like, like)
	}
	if filter.Vendor != "" {
		if vendorID, ok := parsePositiveInt(filter.Vendor); ok {
			query = query.Where("models.vendor_id = ?", vendorID)
		} else {
			query = query.Joins("JOIN vendors ON vendors.id = models.vendor_id").
				Where("LOWER(vendors.name) LIKE ?", "%"+strings.ToLower(filter.Vendor)+"%")
		}
	}
	if filter.Status != nil {
		query = query.Where("models.status = ?", *filter.Status)
	}
	if filter.SyncOfficial != nil {
		query = query.Where("models.sync_official = ?", *filter.SyncOfficial)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var models []*Model
	if err := query.Order("models.id DESC").Offset(filter.Offset).Limit(filter.Limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}
	return models, total, nil
}

func GetModelMetadataByID(id int) (*Model, error) {
	var metadata Model
	if err := DB.First(&metadata, id).Error; err != nil {
		return nil, err
	}
	return &metadata, nil
}

func GetVendorModelCounts() (map[int64]int64, error) {
	var rows []struct {
		VendorID int64 `gorm:"column:vendor_id"`
		Count    int64 `gorm:"column:count"`
	}
	if err := DB.Model(&Model{}).
		Select("vendor_id, COUNT(*) AS count").
		Group("vendor_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[int64]int64, len(rows))
	for _, row := range rows {
		counts[row.VendorID] = row.Count
	}
	return counts, nil
}

func CreateModelMetadata(metadata *Model) error {
	modelMetadataWriteMu.Lock()
	defer modelMetadataWriteMu.Unlock()
	if modelNameExists(DB, metadata.ModelName, 0) {
		return ErrModelNameExists
	}
	now := wallclock.NowTimestamp()
	metadata.Id = 0
	metadata.CreatedTime = now
	metadata.UpdatedTime = now
	// The reference schema defaults raw inserts to enabled/official, while its
	// model creation contract still permits callers to persist explicit zeroes.
	// GORM applies declared defaults to zero-valued struct fields, so restore the
	// caller's values inside the same transaction before the row is visible.
	status, syncOfficial := metadata.Status, metadata.SyncOfficial
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(metadata).Error; err != nil {
			return err
		}
		return tx.Model(&Model{}).Where("id = ?", metadata.Id).Updates(map[string]any{
			"status": status, "sync_official": syncOfficial,
		}).Error
	})
	metadata.Status = status
	metadata.SyncOfficial = syncOfficial
	if err != nil && modelNameExists(DB, metadata.ModelName, 0) {
		return ErrModelNameExists
	}
	return err
}

func UpdateModelMetadata(metadata *Model, statusOnly bool) error {
	modelMetadataWriteMu.Lock()
	defer modelMetadataWriteMu.Unlock()
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Model
		query := tx.Where("id = ?", metadata.Id)
		if UsingMySQL() || UsingPostgreSQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&current).Error; err != nil {
			return err
		}
		if statusOnly {
			result := tx.Model(&Model{}).Where("id = ?", metadata.Id).Update("status", metadata.Status)
			if result.Error != nil {
				return result.Error
			}
			return nil
		}

		if current.ModelName != metadata.ModelName {
			var count int64
			if err := tx.Model(&Model{}).
				Where("model_name = ? AND id <> ?", metadata.ModelName, metadata.Id).
				Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return ErrModelNameExists
			}
		}
		activeName := metadata.ModelName
		result := tx.Model(&Model{}).Where("id = ?", metadata.Id).Updates(map[string]any{
			"model_name":    metadata.ModelName,
			"active_name":   activeName,
			"description":   metadata.Description,
			"icon":          metadata.Icon,
			"tags":          metadata.Tags,
			"vendor_id":     metadata.VendorID,
			"endpoints":     metadata.Endpoints,
			"status":        metadata.Status,
			"sync_official": metadata.SyncOfficial,
			"name_rule":     metadata.NameRule,
			"updated_time":  wallclock.NowTimestamp(),
		})
		if result.Error != nil {
			return result.Error
		}
		return nil
	})
	if err != nil && modelNameExists(DB, metadata.ModelName, metadata.Id) {
		return ErrModelNameExists
	}
	return err
}

func DeleteModelMetadata(id int) error {
	modelMetadataWriteMu.Lock()
	defer modelMetadataWriteMu.Unlock()
	return DB.Transaction(func(tx *gorm.DB) error {
		var metadata Model
		query := tx.Where("id = ?", id)
		if UsingMySQL() || UsingPostgreSQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&metadata).Error; err != nil {
			return err
		}
		if err := tx.Model(&Model{}).Where("id = ?", id).Update("active_name", nil).Error; err != nil {
			return err
		}
		result := tx.Delete(&metadata)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func GetMissingModelNames() ([]string, error) {
	return GetMissingModelNamesContext(context.Background())
}

func GetMissingModelNamesContext(ctx context.Context) ([]string, error) {
	db := DB.WithContext(ctx)
	var enabled []string
	if err := db.Model(&Ability{}).
		Where("enabled = ?", true).
		Distinct().
		Pluck("model", &enabled).Error; err != nil {
		return nil, err
	}
	if len(enabled) == 0 {
		return []string{}, nil
	}
	var existing []string
	if err := db.Model(&Model{}).Where("model_name IN ?", enabled).Pluck("model_name", &existing).Error; err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, len(existing))
	for _, name := range existing {
		present[name] = struct{}{}
	}
	missing := make([]string, 0)
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

func modelNameExists(tx *gorm.DB, name string, excludeID int) bool {
	var count int64
	query := tx.Model(&Model{}).Where("model_name = ?", name)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	return query.Count(&count).Error == nil && count > 0
}

func parsePositiveInt(value string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	return parsed, err == nil
}
