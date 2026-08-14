package model

import (
	"time"

	"gorm.io/gorm"
)

func ListPrefillGroups(groupType string) ([]PrefillGroup, error) {
	groups := make([]PrefillGroup, 0)
	query := DB.Model(&PrefillGroup{})
	if groupType != "" {
		query = query.Where("type = ?", groupType)
	}
	if err := query.Order("updated_time DESC").Order("id DESC").Find(&groups).Error; err != nil {
		return nil, err
	}
	return groups, nil
}

func PrefillGroupNameExists(excludeID int, name string) (bool, error) {
	var count int64
	err := DB.Model(&PrefillGroup{}).Where("name = ? AND id <> ?", name, excludeID).Count(&count).Error
	return count > 0, err
}

func InsertPrefillGroup(group *PrefillGroup) error {
	now := time.Now().Unix()
	group.CreatedTime = now
	group.UpdatedTime = now
	return DB.Create(group).Error
}

func UpdatePrefillGroup(group *PrefillGroup) error {
	now := time.Now().Unix()
	result := DB.Model(&PrefillGroup{}).Where("id = ?", group.Id).Updates(map[string]any{
		"name": group.Name, "type": group.Type, "items": group.Items,
		"description": group.Description, "updated_time": now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return DB.First(group, group.Id).Error
}

func DeletePrefillGroupByID(id int) error {
	return DB.Delete(&PrefillGroup{}, id).Error
}
