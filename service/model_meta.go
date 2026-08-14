package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func ParseModelStatusFilter(value string) *int {
	return parseModelIntegerFilter(value, map[string]int{"enabled": 1, "disabled": 0})
}

func ParseModelSyncFilter(value string) *int {
	return parseModelIntegerFilter(value, map[string]int{"yes": 1, "no": 0})
}

func parseModelIntegerFilter(value string, aliases map[string]int) *int {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" || normalized == "all" {
		return nil
	}
	if parsed, ok := aliases[normalized]; ok {
		return &parsed
	}
	negative := false
	if strings.HasPrefix(normalized, "-") {
		negative = true
		normalized = strings.TrimPrefix(normalized, "-")
	}
	if normalized == "" {
		return nil
	}
	parsed := 0
	for _, char := range normalized {
		if char < '0' || char > '9' {
			return nil
		}
		parsed = parsed*10 + int(char-'0')
	}
	if negative {
		parsed = -parsed
	}
	return &parsed
}

func ValidateModelMetadata(metadata *model.Model, statusOnly bool) error {
	if metadata.Id < 0 {
		return errors.New("模型 ID 无效")
	}
	if statusOnly {
		return nil
	}
	metadata.ModelName = strings.TrimSpace(metadata.ModelName)
	metadata.Description = strings.TrimSpace(metadata.Description)
	metadata.Icon = strings.TrimSpace(metadata.Icon)
	metadata.Tags = strings.TrimSpace(metadata.Tags)
	metadata.Endpoints = strings.TrimSpace(metadata.Endpoints)
	if metadata.ModelName == "" {
		return errors.New("模型名称不能为空")
	}
	if utf8.RuneCountInString(metadata.ModelName) > 128 {
		return errors.New("模型名称不能超过 128 个字符")
	}
	if utf8.RuneCountInString(metadata.Icon) > 128 {
		return errors.New("模型图标不能超过 128 个字符")
	}
	if utf8.RuneCountInString(metadata.Tags) > 255 {
		return errors.New("模型标签不能超过 255 个字符")
	}
	if len(metadata.Description) > 1<<20 || len(metadata.Endpoints) > 1<<20 {
		return errors.New("模型元数据不能超过 1 MiB")
	}
	if metadata.VendorID < 0 {
		return errors.New("供应商 ID 无效")
	}
	if metadata.NameRule < model.ModelNameRuleExact || metadata.NameRule > model.ModelNameRuleSuffix {
		return errors.New("模型名称规则无效")
	}
	if metadata.SyncOfficial != 0 && metadata.SyncOfficial != 1 {
		return errors.New("sync_official 必须为 0 或 1")
	}
	if metadata.Endpoints != "" {
		var decoded any
		if err := common.Unmarshal([]byte(metadata.Endpoints), &decoded); err != nil {
			return errors.New("endpoints 必须是有效 JSON")
		}
	}
	return nil
}

func EnrichModelMetadata(metadata []*model.Model) error {
	if len(metadata) == 0 {
		return nil
	}
	catalog, err := modelCatalogNames()
	if err != nil {
		return err
	}

	matches := make(map[int][]string, len(metadata))
	allNames := make(map[string]struct{})
	for _, item := range metadata {
		if strings.TrimSpace(item.Endpoints) == "" {
			item.Endpoints = "[]"
		}
		if item.NameRule == model.ModelNameRuleExact {
			matches[item.Id] = []string{item.ModelName}
			allNames[item.ModelName] = struct{}{}
			continue
		}
		matched := make([]string, 0)
		for _, candidate := range catalog {
			if modelRuleMatches(item.NameRule, item.ModelName, candidate) {
				matched = append(matched, candidate)
				allNames[candidate] = struct{}{}
			}
		}
		sort.Strings(matched)
		matches[item.Id] = matched
		item.MatchedModels = matched
		item.MatchedCount = len(matched)
	}

	names := make([]string, 0, len(allNames))
	for name := range allNames {
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	type abilityRow struct {
		Model string `gorm:"column:model"`
		Group string `gorm:"column:group_name"`
	}
	groupColumn := "`group`"
	if model.UsingPostgreSQL() {
		groupColumn = `"group"`
	}
	var abilities []abilityRow
	if err := model.DB.Model(&model.Ability{}).
		Select("model, "+groupColumn+" AS group_name").
		Where("enabled = ? AND model IN ?", true, names).
		Scan(&abilities).Error; err != nil {
		return err
	}

	type channelRow struct {
		Model string `gorm:"column:model"`
		Name  string `gorm:"column:name"`
		Type  int    `gorm:"column:type"`
	}
	var channelRows []channelRow
	if err := model.DB.Table("abilities").
		Select("abilities.model AS model, channels.name AS name, channels.type AS type").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.enabled = ? AND abilities.model IN ?", true, names).
		Distinct().
		Scan(&channelRows).Error; err != nil {
		return err
	}

	groupsByModel := make(map[string][]string)
	for _, row := range abilities {
		groupsByModel[row.Model] = append(groupsByModel[row.Model], row.Group)
	}
	channelsByModel := make(map[string][]model.BoundChannel)
	for _, row := range channelRows {
		channelsByModel[row.Model] = append(channelsByModel[row.Model], model.BoundChannel{Name: row.Name, Type: row.Type})
	}
	priced := ExportedModelPrices()

	for _, item := range metadata {
		groupSet := map[string]struct{}{}
		channelSet := map[string]model.BoundChannel{}
		quotaType := false
		for _, name := range matches[item.Id] {
			for _, group := range groupsByModel[name] {
				if group != "" {
					groupSet[group] = struct{}{}
				}
			}
			for _, channel := range channelsByModel[name] {
				channelSet[fmt.Sprintf("%s\x00%d", channel.Name, channel.Type)] = channel
			}
			if _, ok := priced[name]; ok || len(groupsByModel[name]) > 0 {
				quotaType = true
			}
		}
		item.EnableGroups = sortedSet(groupSet)
		item.BoundChannels = make([]model.BoundChannel, 0, len(channelSet))
		for _, channel := range channelSet {
			item.BoundChannels = append(item.BoundChannels, channel)
		}
		sort.Slice(item.BoundChannels, func(i, j int) bool {
			if item.BoundChannels[i].Name == item.BoundChannels[j].Name {
				return item.BoundChannels[i].Type < item.BoundChannels[j].Type
			}
			return item.BoundChannels[i].Name < item.BoundChannels[j].Name
		})
		if quotaType {
			item.QuotaTypes = []int{1}
		}
	}
	return nil
}

func modelCatalogNames() ([]string, error) {
	set := make(map[string]struct{})
	for name := range ExportedModelPrices() {
		set[name] = struct{}{}
	}
	var abilityNames []string
	if err := model.DB.Model(&model.Ability{}).
		Where("enabled = ?", true).
		Distinct().
		Pluck("model", &abilityNames).Error; err != nil {
		return nil, err
	}
	for _, name := range abilityNames {
		if name != "" {
			set[name] = struct{}{}
		}
	}
	return sortedSet(set), nil
}

func modelRuleMatches(rule int, pattern, candidate string) bool {
	switch rule {
	case model.ModelNameRulePrefix:
		return strings.HasPrefix(candidate, pattern)
	case model.ModelNameRuleContains:
		return strings.Contains(candidate, pattern)
	case model.ModelNameRuleSuffix:
		return strings.HasSuffix(candidate, pattern)
	default:
		return candidate == pattern
	}
}

func sortedSet(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
