package aws

import (
	"sort"
	"strings"
)

const ChannelName = "aws"

var modelIDMap = map[string]string{
	"claude-3-sonnet-20240229":   "anthropic.claude-3-sonnet-20240229-v1:0",
	"claude-3-opus-20240229":     "anthropic.claude-3-opus-20240229-v1:0",
	"claude-3-haiku-20240307":    "anthropic.claude-3-haiku-20240307-v1:0",
	"claude-3-5-sonnet-20240620": "anthropic.claude-3-5-sonnet-20240620-v1:0",
	"claude-3-5-sonnet-20241022": "anthropic.claude-3-5-sonnet-20241022-v2:0",
	"claude-3-5-haiku-20241022":  "anthropic.claude-3-5-haiku-20241022-v1:0",
	"claude-3-7-sonnet-20250219": "anthropic.claude-3-7-sonnet-20250219-v1:0",
	"claude-sonnet-4-20250514":   "anthropic.claude-sonnet-4-20250514-v1:0",
	"claude-opus-4-20250514":     "anthropic.claude-opus-4-20250514-v1:0",
	"claude-opus-4-1-20250805":   "anthropic.claude-opus-4-1-20250805-v1:0",
	"claude-sonnet-4-5-20250929": "anthropic.claude-sonnet-4-5-20250929-v1:0",
	"claude-sonnet-4-6":          "anthropic.claude-sonnet-4-6",
	"claude-haiku-4-5-20251001":  "anthropic.claude-haiku-4-5-20251001-v1:0",
	"claude-opus-4-5-20251101":   "anthropic.claude-opus-4-5-20251101-v1:0",
	"claude-opus-4-6":            "anthropic.claude-opus-4-6-v1",
	"claude-opus-4-7":            "anthropic.claude-opus-4-7",
	"claude-opus-4-8":            "anthropic.claude-opus-4-8",
	"nova-micro-v1:0":            "amazon.nova-micro-v1:0",
	"nova-lite-v1:0":             "amazon.nova-lite-v1:0",
	"nova-pro-v1:0":              "amazon.nova-pro-v1:0",
	"nova-premier-v1:0":          "amazon.nova-premier-v1:0",
	"nova-canvas-v1:0":           "amazon.nova-canvas-v1:0",
	"nova-reel-v1:0":             "amazon.nova-reel-v1:0",
	"nova-reel-v1:1":             "amazon.nova-reel-v1:1",
	"nova-sonic-v1:0":            "amazon.nova-sonic-v1:0",
}

var crossRegionModels = map[string]map[string]bool{
	"anthropic.claude-3-sonnet-20240229-v1:0":   {"us": true, "eu": true, "ap": true},
	"anthropic.claude-3-opus-20240229-v1:0":     {"us": true},
	"anthropic.claude-3-haiku-20240307-v1:0":    {"us": true, "eu": true, "ap": true},
	"anthropic.claude-3-5-sonnet-20240620-v1:0": {"us": true, "eu": true, "ap": true},
	"anthropic.claude-3-5-sonnet-20241022-v2:0": {"us": true, "ap": true},
	"anthropic.claude-3-5-haiku-20241022-v1:0":  {"us": true},
	"anthropic.claude-3-7-sonnet-20250219-v1:0": {"us": true, "eu": true, "ap": true},
	"anthropic.claude-sonnet-4-20250514-v1:0":   {"us": true, "eu": true, "ap": true},
	"anthropic.claude-opus-4-20250514-v1:0":     {"us": true},
	"anthropic.claude-opus-4-1-20250805-v1:0":   {"us": true},
	"anthropic.claude-sonnet-4-5-20250929-v1:0": {"us": true, "eu": true, "ap": true},
	"anthropic.claude-sonnet-4-6":               {"us": true, "eu": true, "ap": true},
	"anthropic.claude-haiku-4-5-20251001-v1:0":  {"us": true, "eu": true, "ap": true},
	"anthropic.claude-opus-4-5-20251101-v1:0":   {"us": true, "eu": true, "ap": true},
	"anthropic.claude-opus-4-6-v1":              {"us": true, "eu": true, "ap": true},
	"anthropic.claude-opus-4-7":                 {"us": true, "eu": true, "ap": true},
	"anthropic.claude-opus-4-8":                 {"us": true, "eu": true, "ap": true},
	"amazon.nova-micro-v1:0":                    {"us": true, "eu": true, "ap": true},
	"amazon.nova-lite-v1:0":                     {"us": true, "eu": true, "ap": true},
	"amazon.nova-pro-v1:0":                      {"us": true, "eu": true, "ap": true},
	"amazon.nova-premier-v1:0":                  {"us": true},
	"amazon.nova-canvas-v1:0":                   {"us": true, "eu": true, "ap": true},
	"amazon.nova-reel-v1:0":                     {"us": true, "eu": true, "ap": true},
	"amazon.nova-reel-v1:1":                     {"us": true},
	"amazon.nova-sonic-v1:0":                    {"us": true, "eu": true, "ap": true},
}

var crossRegionPrefixes = map[string]string{"us": "us", "eu": "eu", "ap": "apac"}

// ModelList returns the exact reference catalog in deterministic order.
func ModelList() []string {
	models := make([]string, 0, len(modelIDMap))
	for model := range modelIDMap {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// ModelID resolves a public reference model alias to its Bedrock model ID.
// Already-provider-native IDs and inference-profile ARNs pass through.
func ModelID(model string) string {
	model = strings.TrimSpace(model)
	if mapped, ok := modelIDMap[model]; ok {
		return mapped
	}
	return model
}

// IsNovaModel reports whether a public or provider-native model belongs to
// the Amazon Nova family handled by the messages-v1 codec.
func IsNovaModel(model string) bool {
	return strings.Contains(ModelID(model), "nova-")
}

// RegionalModelID applies Bedrock's cross-region inference-profile prefix for
// model/region combinations listed by the reference provider catalog.
func RegionalModelID(model, region string) string {
	model = ModelID(model)
	prefix := regionPrefix(region)
	if !crossRegionModels[model][prefix] {
		return model
	}
	regionalPrefix := crossRegionPrefixes[prefix]
	if regionalPrefix == "" {
		return model
	}
	return regionalPrefix + "." + model
}

func regionPrefix(region string) string {
	if index := strings.IndexByte(region, '-'); index >= 0 {
		return region[:index]
	}
	return region
}
