package channels

import (
	"errors"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	advancedconfig "github.com/tokenrouter/tokenrouter/internal/relay/customconfig"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"sort"
	"strings"
)

const (
	EndpointCatalogMaxGroups = 64
	EndpointCatalogMaxModels = 10_000
	EndpointCatalogBatchSize = 500
)

var (
	ErrEndpointCatalogTooLarge    = errors.New("endpoint catalog request exceeds safe limits")
	ErrEndpointCatalogUnavailable = errors.New("endpoint catalog database is unavailable")
)

// EndpointTypesForChannelTypes derives the usable protocol surfaces for a
// collection of channel types. Input is copied and sorted so callers receive a
// deterministic, independently owned result.
func EndpointTypesForChannelTypes(channelTypes []int) []channelcatalog.EndpointType {
	return EndpointTypesForModelAndChannelTypes("", channelTypes)
}

// EndpointTypesForModelAndChannelTypes additionally applies model-family
// capability rules while retaining deterministic channel preference order.
func EndpointTypesForModelAndChannelTypes(modelName string, channelTypes []int) []channelcatalog.EndpointType {
	ordered := append([]int(nil), channelTypes...)
	sort.Ints(ordered)

	seen := make(map[channelcatalog.EndpointType]struct{})
	result := make([]channelcatalog.EndpointType, 0)
	for _, rawType := range ordered {
		channelType := channelcatalog.ChannelType(rawType)
		if !implementedCatalogChannelType(channelType) {
			continue
		}
		for _, endpointType := range channelcatalog.GetEndpointTypesByChannelTypeForModel(channelType, modelName) {
			if _, duplicate := seen[endpointType]; duplicate {
				continue
			}
			seen[endpointType] = struct{}{}
			result = append(result, endpointType)
		}
	}
	if result == nil {
		return []channelcatalog.EndpointType{}
	}
	return result
}

func implementedCatalogChannelType(channelType channelcatalog.ChannelType) bool {
	if channelcatalog.IsOpenAICompatibleChannelType(channelType) {
		return true
	}
	switch channelType {
	case channelcatalog.ChannelTypeAnthropic, channelcatalog.ChannelTypeAws, channelcatalog.ChannelTypeGemini,
		channelcatalog.ChannelTypeJina, channelcatalog.ChannelTypeSubmodel,
		channelcatalog.ChannelTypeCodex, channelcatalog.ChannelTypeCohere,
		channelcatalog.ChannelTypeDify, channelcatalog.ChannelTypeBaidu,
		channelcatalog.ChannelTypeBaiduV2,
		channelcatalog.ChannelTypeSora,
		channelcatalog.ChannelCloudflare, channelcatalog.ChannelTypeMiniMax,
		channelcatalog.ChannelTypeAdvancedCustom:
		return true
	default:
		return false
	}
}

// GetModelSupportedEndpointTypes returns per-model endpoint metadata for the
// supplied authorized groups and already-filtered model set. It never widens
// model access and omits disabled or missing channels.
func GetModelSupportedEndpointTypes(groups []string, models map[string]bool) (map[string][]channelcatalog.EndpointType, error) {
	if len(groups) > EndpointCatalogMaxGroups || len(models) > EndpointCatalogMaxModels {
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
	result := make(map[string][]channelcatalog.EndpointType, len(models))
	for modelName, enabled := range models {
		if !enabled || modelName == "" || modelName != strings.TrimSpace(modelName) {
			continue
		}
		modelNames = append(modelNames, modelName)
		result[modelName] = []channelcatalog.EndpointType{}
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
	for start := 0; start < len(channelIDs); start += EndpointCatalogBatchSize {
		end := min(start+EndpointCatalogBatchSize, len(channelIDs))
		var rows []channelTypeRow
		if err := model.DB.Model(&model.Channel{}).
			Select("id", "type", "settings").
			Where("id IN ? AND status = ?", channelIDs[start:end], channelcatalog.ChannelStatusEnabled).
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
		endpointTypes := make([]channelcatalog.EndpointType, 0)
		seenEndpointTypes := make(map[channelcatalog.EndpointType]struct{})
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

func endpointTypesForCatalogChannel(modelName string, rawChannelType int, settings string) []channelcatalog.EndpointType {
	channelType := channelcatalog.ChannelType(rawChannelType)
	if channelType != channelcatalog.ChannelTypeAdvancedCustom {
		return EndpointTypesForModelAndChannelTypes(modelName, []int{rawChannelType})
	}
	config, err := advancedconfig.ParseSettings(settings)
	if err != nil {
		// Match the reference's legacy fallback while live dispatch continues
		// to validate and reject malformed Advanced Custom settings.
		return channelcatalog.GetEndpointTypesByChannelTypeForModel(channelType, modelName)
	}
	paths, err := advancedconfig.IncomingPathsForModel(config, modelName)
	if err != nil {
		return channelcatalog.GetEndpointTypesByChannelTypeForModel(channelType, modelName)
	}
	result := make([]channelcatalog.EndpointType, 0, len(paths))
	seen := make(map[channelcatalog.EndpointType]struct{}, len(paths))
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

func advancedCustomEndpointType(path string) (channelcatalog.EndpointType, bool) {
	switch strings.TrimSpace(path) {
	case "/v1/chat/completions":
		return channelcatalog.EndpointTypeOpenAI, true
	case "/v1/responses":
		return channelcatalog.EndpointTypeOpenAIResponse, true
	case "/v1/responses/compact":
		return channelcatalog.EndpointTypeOpenAIResponseCompact, true
	case "/v1/alpha/search":
		return channelcatalog.EndpointTypeOpenAIAlphaSearch, true
	case "/v1/messages":
		return channelcatalog.EndpointTypeAnthropic, true
	case "/v1/rerank":
		return channelcatalog.EndpointTypeJinaRerank, true
	case "/v1/images/generations":
		return channelcatalog.EndpointTypeImageGeneration, true
	case "/v1/embeddings":
		return channelcatalog.EndpointTypeEmbeddings, true
	default:
		if strings.HasPrefix(path, "/v1beta/models/") &&
			(strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent")) {
			return channelcatalog.EndpointTypeGemini, true
		}
		return "", false
	}
}
