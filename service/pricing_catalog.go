package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

const pricingCatalogMaxBillingExpressionBytes = 64 << 10

// PricingCatalogItem is the public, reference-compatible pricing description
// for one currently routable model. PromptPrice and CompletionPrice preserve
// TokenRouter's explicit USD-per-million convention alongside ratio fields.
type PricingCatalogItem struct {
	ModelName              string                  `json:"model_name"`
	Description            string                  `json:"description,omitempty"`
	Icon                   string                  `json:"icon,omitempty"`
	Tags                   string                  `json:"tags,omitempty"`
	VendorID               int                     `json:"vendor_id,omitempty"`
	QuotaType              int                     `json:"quota_type"`
	ModelRatio             float64                 `json:"model_ratio"`
	ModelPrice             float64                 `json:"model_price"`
	PromptPrice            float64                 `json:"prompt_price"`
	CompletionPrice        float64                 `json:"completion_price"`
	OwnerBy                string                  `json:"owner_by"`
	CompletionRatio        float64                 `json:"completion_ratio"`
	EnableGroup            []string                `json:"enable_groups"`
	SupportedEndpointTypes []constant.EndpointType `json:"supported_endpoint_types"`
	BillingMode            string                  `json:"billing_mode,omitempty"`
	BillingExpr            string                  `json:"billing_expr,omitempty"`
}

type PricingCatalogVendor struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

type PricingCatalog struct {
	Items             []PricingCatalogItem
	Vendors           []PricingCatalogVendor
	GroupRatio        map[string]float64
	UsableGroup       map[string]string
	SupportedEndpoint map[string]common.EndpointInfo
	AutoGroups        []string
	Version           string
}

// BuildPricingCatalog constructs a deterministic snapshot from the live
// routing and pricing caches. Only models in groups visible to the caller and
// backed by enabled channels are returned.
func BuildPricingCatalog(userGroup string) (PricingCatalog, error) {
	usableGroups := GetUserUsableGroups(userGroup)
	groups := make([]string, 0, len(usableGroups))
	for group := range usableGroups {
		if group != "" && group == strings.TrimSpace(group) {
			groups = append(groups, group)
		}
	}
	sort.Strings(groups)
	if len(groups) > endpointCatalogMaxGroups {
		return PricingCatalog{}, ErrEndpointCatalogTooLarge
	}

	modelGroups := make(map[string]map[string]struct{})
	models := make(map[string]bool)
	for _, group := range groups {
		for modelName, enabled := range GetGroupModels(group) {
			if !enabled || modelName == "" || modelName != strings.TrimSpace(modelName) {
				continue
			}
			models[modelName] = true
			set := modelGroups[modelName]
			if set == nil {
				set = make(map[string]struct{})
				modelGroups[modelName] = set
			}
			set[group] = struct{}{}
		}
	}
	if len(models) > endpointCatalogMaxModels {
		return PricingCatalog{}, ErrEndpointCatalogTooLarge
	}

	endpointTypes, err := GetModelSupportedEndpointTypes(groups, models)
	if err != nil {
		return PricingCatalog{}, err
	}
	metadata, err := pricingCatalogMetadata(models)
	if err != nil {
		return PricingCatalog{}, err
	}

	modelNames := make([]string, 0, len(models))
	for modelName := range models {
		modelNames = append(modelNames, modelName)
	}
	sort.Strings(modelNames)
	billing, err := loadPricingCatalogBilling(modelNames)
	if err != nil {
		return PricingCatalog{}, err
	}

	items := make([]PricingCatalogItem, 0, len(modelNames))
	usedVendorIDs := make(map[int]struct{})
	for _, modelName := range modelNames {
		meta := metadata[modelName]
		if meta != nil && meta.Status != 1 {
			continue
		}
		item := PricingCatalogItem{
			ModelName: modelName, OwnerBy: "custom",
			EnableGroup:            sortedStringSet(modelGroups[modelName]),
			SupportedEndpointTypes: append([]constant.EndpointType(nil), endpointTypes[modelName]...),
		}
		if !populatePricingCatalogPrice(&item, billing) {
			continue
		}
		if item.SupportedEndpointTypes == nil {
			item.SupportedEndpointTypes = []constant.EndpointType{}
		}
		if meta != nil {
			item.Description = meta.Description
			item.Icon = meta.Icon
			item.Tags = meta.Tags
			item.VendorID = meta.VendorID
			if meta.VendorID > 0 {
				usedVendorIDs[meta.VendorID] = struct{}{}
			}
		}
		items = append(items, item)
	}

	vendors, err := pricingCatalogVendors(usedVendorIDs)
	if err != nil {
		return PricingCatalog{}, err
	}
	groupRatios := make(map[string]float64)
	for group := range ExportedGroupRatios() {
		if _, visible := usableGroups[group]; visible {
			ratio, _ := EffectiveGroupRatio(userGroup, group)
			if validCatalogRatio(ratio) {
				groupRatios[group] = ratio
			}
		}
	}
	if err := validatePricingCatalogAdjustedPrices(items, groupRatios); err != nil {
		return PricingCatalog{}, err
	}

	catalog := PricingCatalog{
		Items: items, Vendors: vendors,
		GroupRatio: groupRatios, UsableGroup: cloneStringMap(usableGroups),
		SupportedEndpoint: pricingSupportedEndpoints(items),
		AutoGroups:        GetUserAutoGroups(userGroup),
	}
	catalog.Version = pricingCatalogVersion(catalog)
	return catalog, nil
}

type pricingCatalogBilling struct {
	modes                     map[string]string
	expressions               map[string]string
	reference                 referencePricingMaps
	completionRatioConfigured bool
}

func loadPricingCatalogBilling(modelNames []string) (pricingCatalogBilling, error) {
	options := setting.GetOptions(
		setting.ModelBillingModeOption,
		setting.ModelBillingExprOption,
		setting.PerCallModelPriceOption,
		setting.ModelRatioOption,
		setting.CompletionRatioOption,
	)
	modes, err := parseReferenceBillingModes(options[setting.ModelBillingModeOption])
	if err != nil {
		return pricingCatalogBilling{}, err
	}
	result := pricingCatalogBilling{
		modes: modes, expressions: map[string]string{},
		completionRatioConfigured: optionConfigured(options, setting.CompletionRatioOption),
	}
	hasTiered, hasReference := false, false
	for _, modelName := range modelNames {
		switch modes[modelName] {
		case BillingModeDefault, BillingModeRatio:
		case BillingModeTieredExpr:
			hasTiered = true
		case BillingModeReference:
			hasReference = true
		default:
			return pricingCatalogBilling{}, fmt.Errorf(
				"%w: unsupported billing mode %q for model %q",
				ErrReferencePricingConfiguration, modes[modelName], modelName,
			)
		}
	}
	if hasTiered {
		result.expressions, err = parsePricingCatalogBillingExpressions(options[setting.ModelBillingExprOption])
		if err != nil {
			return pricingCatalogBilling{}, err
		}
	}
	if hasReference {
		result.reference, err = loadReferencePricingMaps(referencePricingRaw{
			fixedPrice:                options[setting.PerCallModelPriceOption],
			modelRatio:                options[setting.ModelRatioOption],
			completionRatio:           options[setting.CompletionRatioOption],
			completionRatioConfigured: result.completionRatioConfigured,
		})
		if err != nil {
			return pricingCatalogBilling{}, err
		}
	}
	return result, nil
}

func parsePricingCatalogBillingExpressions(raw string) (map[string]string, error) {
	expressions := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return expressions, nil
	}
	if len(raw) > referencePricingMaxOptionBytes {
		return nil, fmt.Errorf("%w: ModelBillingExpr exceeds %d bytes", ErrReferencePricingConfiguration, referencePricingMaxOptionBytes)
	}
	if err := common.UnmarshalJsonStr(raw, &expressions); err != nil || expressions == nil {
		return nil, fmt.Errorf("%w: ModelBillingExpr must be a JSON object", ErrReferencePricingConfiguration)
	}
	if len(expressions) > referencePricingMaxEntries {
		return nil, fmt.Errorf("%w: ModelBillingExpr has too many entries", ErrReferencePricingConfiguration)
	}
	for modelName, expression := range expressions {
		if !validReferencePricingModel(modelName) || !utf8.ValidString(expression) || len(expression) > pricingCatalogMaxBillingExpressionBytes {
			return nil, fmt.Errorf("%w: invalid ModelBillingExpr entry for %q", ErrReferencePricingConfiguration, modelName)
		}
	}
	return expressions, nil
}

func populatePricingCatalogPrice(item *PricingCatalogItem, billing pricingCatalogBilling) bool {
	modelName := item.ModelName
	if billing.modes[modelName] == BillingModeReference {
		pricingName := referenceMatchingModelName(modelName)
		if fixedPrice, found := billing.reference.fixedPrice[pricingName]; found {
			item.QuotaType = 1
			item.ModelPrice = fixedPrice
			return validCatalogPrice(fixedPrice)
		}
		modelRatio, found := billing.reference.modelRatio[pricingName]
		if !found {
			// This is the exact request-time fallback for users who explicitly
			// accept unpriced reference models. Other users have the model removed
			// from their authenticated model catalog before provider dispatch.
			modelRatio = referenceUnsetModelRatio
		}
		completionRatio := referenceCompletionRatio(
			pricingName, billing.reference.completionRatio, billing.completionRatioConfigured,
		)
		promptPrice := referenceRatioUSDPerMillion(modelRatio)
		completionPrice := promptPrice * completionRatio
		if !validCatalogRatio(modelRatio) || !validCatalogRatio(completionRatio) ||
			!validCatalogPrice(promptPrice) || !validCatalogPrice(completionPrice) {
			return false
		}
		item.QuotaType = 0
		item.ModelRatio = modelRatio
		item.PromptPrice = promptPrice
		item.CompletionPrice = completionPrice
		item.CompletionRatio = completionRatio
		return true
	}

	promptPrice, completionPrice := GetModelPrices(modelName)
	if !validCatalogPrice(promptPrice) || !validCatalogPrice(completionPrice) {
		return false
	}
	completionRatio := 0.0
	if promptPrice > 0 {
		completionRatio = completionPrice / promptPrice
	}
	modelRatio := promptPrice * float64(common.QuotaPerUnit) / 1_000_000
	if !validCatalogRatio(modelRatio) || !validCatalogRatio(completionRatio) {
		return false
	}
	item.QuotaType = 0
	item.ModelRatio = modelRatio
	item.PromptPrice = promptPrice
	item.CompletionPrice = completionPrice
	item.CompletionRatio = completionRatio
	if billing.modes[modelName] == BillingModeTieredExpr {
		if expression := billing.expressions[modelName]; strings.TrimSpace(expression) != "" {
			item.BillingMode = BillingModeTieredExpr
			item.BillingExpr = expression
		}
	}
	return true
}

func referenceRatioUSDPerMillion(modelRatio float64) float64 {
	return modelRatio * 1_000_000 / float64(common.QuotaPerUnit)
}

func pricingSupportedEndpoints(items []PricingCatalogItem) map[string]common.EndpointInfo {
	result := make(map[string]common.EndpointInfo)
	for _, item := range items {
		for _, endpointType := range item.SupportedEndpointTypes {
			info, ok := common.GetDefaultEndpointInfo(endpointType)
			if ok {
				result[string(endpointType)] = info
			}
		}
	}
	return result
}

func pricingCatalogMetadata(models map[string]bool) (map[string]*model.Model, error) {
	result := make(map[string]*model.Model, len(models))
	if len(models) == 0 {
		return result, nil
	}
	if model.DB == nil {
		return nil, ErrEndpointCatalogUnavailable
	}

	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for start := 0; start < len(names); start += endpointCatalogBatchSize {
		end := min(start+endpointCatalogBatchSize, len(names))
		var exact []model.Model
		if err := model.DB.Where("name_rule = ? AND model_name IN ?", model.ModelNameRuleExact, names[start:end]).
			Find(&exact).Error; err != nil {
			return nil, err
		}
		for index := range exact {
			entry := exact[index]
			copyOfEntry := entry
			result[entry.ModelName] = &copyOfEntry
		}
	}

	var patterns []model.Model
	if err := model.DB.Where("name_rule <> ?", model.ModelNameRuleExact).
		Order("id ASC").Limit(endpointCatalogMaxModels + 1).Find(&patterns).Error; err != nil {
		return nil, err
	}
	if len(patterns) > endpointCatalogMaxModels {
		return nil, ErrEndpointCatalogTooLarge
	}
	for _, entry := range patterns {
		for _, name := range names {
			if result[name] != nil || !modelRuleMatches(entry.NameRule, entry.ModelName, name) {
				continue
			}
			copyOfEntry := entry
			result[name] = &copyOfEntry
		}
	}
	return result, nil
}

func pricingCatalogVendors(ids map[int]struct{}) ([]PricingCatalogVendor, error) {
	if len(ids) == 0 {
		return []PricingCatalogVendor{}, nil
	}
	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Ints(ordered)
	result := make([]PricingCatalogVendor, 0, len(ordered))
	for start := 0; start < len(ordered); start += endpointCatalogBatchSize {
		end := min(start+endpointCatalogBatchSize, len(ordered))
		var vendors []model.Vendor
		if err := model.DB.Where("id IN ?", ordered[start:end]).Order("id ASC").Find(&vendors).Error; err != nil {
			return nil, err
		}
		for _, vendor := range vendors {
			result = append(result, PricingCatalogVendor{
				ID: vendor.Id, Name: vendor.Name, Description: vendor.Description, Icon: vendor.Icon,
			})
		}
	}
	return result, nil
}

func validCatalogPrice(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validCatalogRatio(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validatePricingCatalogAdjustedPrices(items []PricingCatalogItem, groupRatios map[string]float64) error {
	for _, item := range items {
		for _, group := range item.EnableGroup {
			ratio, configured := groupRatios[group]
			if !configured {
				ratio = 1
			}
			for _, price := range []float64{item.ModelPrice, item.PromptPrice, item.CompletionPrice} {
				if !validCatalogPrice(price * ratio) {
					return fmt.Errorf("invalid adjusted pricing for model %q in group %q", item.ModelName, group)
				}
			}
		}
	}
	return nil
}

func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func pricingCatalogVersion(catalog PricingCatalog) string {
	payload, err := common.Marshal(struct {
		Items       []PricingCatalogItem   `json:"items"`
		Vendors     []PricingCatalogVendor `json:"vendors"`
		GroupRatios map[string]float64     `json:"group_ratios"`
	}{catalog.Items, catalog.Vendors, catalog.GroupRatio})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
