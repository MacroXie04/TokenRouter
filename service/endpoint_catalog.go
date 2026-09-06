package service

import (
	"errors"
	"sort"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	advancedconfig "github.com/tokenrouter/tokenrouter/pkg/advancedcustom"
)

const (
	endpointCatalogMaxGroups = 64
	endpointCatalogMaxModels = 10_000
	endpointCatalogBatchSize = 500
)

var (
	ErrEndpointCatalogTooLarge    = errors.New("endpoint catalog request exceeds safe limits")
	ErrEndpointCatalogUnavailable = errors.New("endpoint catalog database is unavailable")
)

// EndpointTypesForChannelTypes derives the usable protocol surfaces for a
// collection of channel types. Input is copied and sorted so callers receive a
// deterministic, independently owned result.
func EndpointTypesForChannelTypes(channelTypes []int) []constant.EndpointType {
	return EndpointTypesForModelAndChannelTypes("", channelTypes)
}

// EndpointTypesForModelAndChannelTypes additionally applies model-family
// capability rules while retaining deterministic channel preference order.
func EndpointTypesForModelAndChannelTypes(modelName string, channelTypes []int) []constant.EndpointType {
	ordered := append([]int(nil), channelTypes...)
	sort.Ints(ordered)

	seen := make(map[constant.EndpointType]struct{})
	result := make([]constant.EndpointType, 0)
	for _, rawType := range ordered {
		channelType := constant.ChannelType(rawType)
		if !implementedCatalogChannelType(channelType) {
			continue
		}
		for _, endpointType := range common.GetEndpointTypesByChannelTypeForModel(channelType, modelName) {
			if _, duplicate := seen[endpointType]; duplicate {
				continue
			}
			seen[endpointType] = struct{}{}
			result = append(result, endpointType)
		}
	}
	if result == nil {
		return []constant.EndpointType{}
	}
	return result
}

func implementedCatalogChannelType(channelType constant.ChannelType) bool {
	if constant.IsOpenAICompatibleChannelType(channelType) {
		return true
	}
	switch channelType {
	case constant.ChannelTypeAnthropic, constant.ChannelTypeAws, constant.ChannelTypeGemini,
		constant.ChannelTypeJina, constant.ChannelTypeSubmodel,
		constant.ChannelTypeCodex, constant.ChannelTypeCohere,
		constant.ChannelTypeDify, constant.ChannelTypeBaidu,
		constant.ChannelTypeBaiduV2,
		constant.ChannelTypeSora,
		constant.ChannelCloudflare, constant.ChannelTypeMiniMax,
		constant.ChannelTypeAdvancedCustom:
		return true
	default:
		return false
	}
}

// GetModelSupportedEndpointTypes returns per-model endpoint metadata for the
// supplied authorized groups and already-filtered model set. It never widens
// model access and omits disabled or missing channels.
func GetModelSupportedEndpointTypes(groups []string, models map[string]bool) (map[string][]constant.EndpointType, error) {
	if len(groups) > endpointCatalogMaxGroups || len(models) > endpointCatalogMaxModels {
		return nil, ErrEndpointCatalogTooLarge
	}

	uniqueGroups := make([]string, 0, len(groups))
	seenGroups := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if group == "" || group != strings.TrimSpace(group) {
			continue
		}
		if _, exists := seenGroups[group]; exists {
			continue
		}
		seenGroups[group] = struct{}{}
		uniqueGroups = append(uniqueGroups, group)
	}

	modelNames := make([]string, 0, len(models))
	result := make(map[string][]constant.EndpointType, len(models))
	for modelName, enabled := range models {
		if !enabled || modelName == "" || modelName != strings.TrimSpace(modelName) {
			continue
		}
		modelNames = append(modelNames, modelName)
		result[modelName] = []constant.EndpointType{}
	}
	sort.Strings(modelNames)
	if len(uniqueGroups) == 0 || len(modelNames) == 0 {
		return result, nil
	}
	if model.DB == nil {
		return nil, ErrEndpointCatalogUnavailable
	}

	channelIDsByModel := make(map[string]map[int]struct{}, len(modelNames))
	channelIDSet := make(map[int]struct{})
	abilityMu.RLock()
	for _, group := range uniqueGroups {
		for _, modelName := range modelNames {
			for _, ability := range abilityCache[abilityKey(group, modelName)] {
				if ability == nil || !ability.Enabled || ability.ChannelId <= 0 {
					continue
				}
				ids := channelIDsByModel[modelName]
				if ids == nil {
					ids = make(map[int]struct{})
					channelIDsByModel[modelName] = ids
				}
				ids[ability.ChannelId] = struct{}{}
				channelIDSet[ability.ChannelId] = struct{}{}
			}
		}
	}
	abilityMu.RUnlock()

	channelIDs := make([]int, 0, len(channelIDSet))
	for channelID := range channelIDSet {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Ints(channelIDs)
	if len(channelIDs) == 0 {
		return result, nil
	}

	type channelTypeRow struct {
		ID       int    `gorm:"column:id"`
		Type     int    `gorm:"column:type"`
		Settings string `gorm:"column:settings"`
	}
	channelByID := make(map[int]channelTypeRow, len(channelIDs))
	for start := 0; start < len(channelIDs); start += endpointCatalogBatchSize {
		end := min(start+endpointCatalogBatchSize, len(channelIDs))
		var rows []channelTypeRow
		if err := model.DB.Model(&model.Channel{}).
			Select("id", "type", "settings").
			Where("id IN ? AND status = ?", channelIDs[start:end], constant.ChannelStatusEnabled).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			channelByID[row.ID] = row
		}
	}

	for _, modelName := range modelNames {
		idSet := channelIDsByModel[modelName]
		ids := make([]int, 0, len(idSet))
		for channelID := range idSet {
			ids = append(ids, channelID)
		}
		sort.Ints(ids)
		endpointTypes := make([]constant.EndpointType, 0)
		seenEndpointTypes := make(map[constant.EndpointType]struct{})
		for _, channelID := range ids {
			row, exists := channelByID[channelID]
			if !exists {
				continue
			}
			for _, endpointType := range endpointTypesForCatalogChannel(modelName, row.Type, row.Settings) {
				if _, duplicate := seenEndpointTypes[endpointType]; duplicate {
					continue
				}
				seenEndpointTypes[endpointType] = struct{}{}
				endpointTypes = append(endpointTypes, endpointType)
			}
		}
		result[modelName] = endpointTypes
	}
	return result, nil
}

func endpointTypesForCatalogChannel(modelName string, rawChannelType int, settings string) []constant.EndpointType {
	channelType := constant.ChannelType(rawChannelType)
	if channelType != constant.ChannelTypeAdvancedCustom {
		return EndpointTypesForModelAndChannelTypes(modelName, []int{rawChannelType})
	}
	config, err := advancedconfig.ParseSettings(settings)
	if err != nil {
		// Match the reference's legacy fallback while live dispatch continues
		// to validate and reject malformed Advanced Custom settings.
		return common.GetEndpointTypesByChannelTypeForModel(channelType, modelName)
	}
	paths, err := advancedconfig.IncomingPathsForModel(config, modelName)
	if err != nil {
		return common.GetEndpointTypesByChannelTypeForModel(channelType, modelName)
	}
	result := make([]constant.EndpointType, 0, len(paths))
	seen := make(map[constant.EndpointType]struct{}, len(paths))
	for _, path := range paths {
		endpointType, ok := advancedCustomEndpointType(path)
		if !ok {
			continue
		}
		if _, duplicate := seen[endpointType]; duplicate {
			continue
		}
		seen[endpointType] = struct{}{}
		result = append(result, endpointType)
	}
	return result
}

func advancedCustomEndpointType(path string) (constant.EndpointType, bool) {
	switch strings.TrimSpace(path) {
	case "/v1/chat/completions":
		return constant.EndpointTypeOpenAI, true
	case "/v1/responses":
		return constant.EndpointTypeOpenAIResponse, true
	case "/v1/responses/compact":
		return constant.EndpointTypeOpenAIResponseCompact, true
	case "/v1/alpha/search":
		return constant.EndpointTypeOpenAIAlphaSearch, true
	case "/v1/messages":
		return constant.EndpointTypeAnthropic, true
	case "/v1/rerank":
		return constant.EndpointTypeJinaRerank, true
	case "/v1/images/generations":
		return constant.EndpointTypeImageGeneration, true
	case "/v1/embeddings":
		return constant.EndpointTypeEmbeddings, true
	default:
		if strings.HasPrefix(path, "/v1beta/models/") &&
			(strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent")) {
			return constant.EndpointTypeGemini, true
		}
		return "", false
	}
}
