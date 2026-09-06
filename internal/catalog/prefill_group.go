package catalog

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrPrefillGroupNameExists = errors.New("组名称已存在")
	ErrPrefillGroupIDRequired = errors.New("缺少组 ID")
)

func GetConfiguredGroups() []string {
	ratios := billingsvc.ExportedGroupRatios()
	groups := make([]string, 0, len(ratios))
	for group := range ratios {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

func ListPrefillGroups(groupType string) ([]model.PrefillGroup, error) {
	return model.ListPrefillGroups(strings.TrimSpace(groupType))
}

func CreatePrefillGroup(group *model.PrefillGroup) error {
	if err := normalizePrefillGroup(group); err != nil {
		return err
	}
	exists, err := model.PrefillGroupNameExists(0, group.Name)
	if err != nil {
		return err
	}
	if exists {
		return ErrPrefillGroupNameExists
	}
	if err := model.InsertPrefillGroup(group); err != nil {
		if duplicate, checkErr := model.PrefillGroupNameExists(0, group.Name); checkErr == nil && duplicate {
			return ErrPrefillGroupNameExists
		}
		return err
	}
	return nil
}

func UpdatePrefillGroup(group *model.PrefillGroup) error {
	if group == nil || group.Id == 0 {
		return ErrPrefillGroupIDRequired
	}
	if err := normalizePrefillGroup(group); err != nil {
		return err
	}
	exists, err := model.PrefillGroupNameExists(group.Id, group.Name)
	if err != nil {
		return err
	}
	if exists {
		return ErrPrefillGroupNameExists
	}
	return model.UpdatePrefillGroup(group)
}

func DeletePrefillGroup(id int) error {
	return model.DeletePrefillGroupByID(id)
}

func normalizePrefillGroup(group *model.PrefillGroup) error {
	if group == nil {
		return errors.New("无效的参数")
	}
	group.Name = strings.TrimSpace(group.Name)
	group.Type = strings.TrimSpace(group.Type)
	group.Description = strings.TrimSpace(group.Description)
	if group.Name == "" || group.Type == "" {
		return errors.New("组名称和类型不能为空")
	}
	if utf8.RuneCountInString(group.Name) > 64 {
		return errors.New("组名称长度不能超过 64 个字符")
	}
	if utf8.RuneCountInString(group.Type) > 32 {
		return errors.New("组类型长度不能超过 32 个字符")
	}
	if utf8.RuneCountInString(group.Description) > 255 {
		return errors.New("组描述长度不能超过 255 个字符")
	}
	if len(group.Items) == 0 || strings.TrimSpace(string(group.Items)) == "null" {
		group.Items = model.JSONValue("[]")
	}
	if len(group.Items) > 1024*1024 {
		return errors.New("组项目数据过大")
	}
	if group.Type == "model" || group.Type == "tag" {
		var items []string
		if err := jsonutil.Unmarshal(group.Items, &items); err != nil {
			return fmt.Errorf("组项目必须是字符串数组: %w", err)
		}
		for _, item := range items {
			if utf8.RuneCountInString(item) > 4096 {
				return errors.New("组项目长度不能超过 4096 个字符")
			}
		}
		normalized, err := jsonutil.Marshal(items)
		if err != nil {
			return err
		}
		group.Items = model.JSONValue(normalized)
		return nil
	}
	var items any
	if err := jsonutil.Unmarshal(group.Items, &items); err != nil {
		return fmt.Errorf("组项目不是有效 JSON: %w", err)
	}
	normalized, err := jsonutil.Marshal(items)
	if err != nil {
		return err
	}
	group.Items = model.JSONValue(normalized)
	return nil
}

func IsPrefillGroupNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
