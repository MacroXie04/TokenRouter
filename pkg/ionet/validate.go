package ionet

import (
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxIdentifierBytes = 128
	MaxNameBytes       = 128
	MaxPage            = 1_000_000
	MaxPageSize        = 100
	MaxLogLimit        = 1000
	MaxGPUCount        = 1024
	MaxReplicaCount    = 1000
	MaxDurationHours   = 24 * 366 * 5
	maxLocations       = 100
	maxEnvironmentVars = 256
	maxStringList      = 128
	maxEnvKeyBytes     = 128
	maxEnvValueBytes   = 8192
	maxArgumentBytes   = 4096
	maxImageURLBytes   = 2048
	maxRegistryBytes   = 4096
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func ValidateIdentifier(value string) error {
	if value == "" || len(value) > MaxIdentifierBytes || !identifierPattern.MatchString(value) {
		return errors.New("identifier is invalid")
	}
	return nil
}

func ValidateName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxNameBytes || !safeText(value) {
		return errors.New("name is invalid")
	}
	return nil
}

func ValidateListOptions(options *ListDeploymentsOptions) error {
	if options == nil {
		return nil
	}
	if options.Page < 1 || options.Page > MaxPage || options.PageSize < 1 || options.PageSize > MaxPageSize {
		return errors.New("pagination is invalid")
	}
	if options.LocationID < 0 || options.LocationID > math.MaxInt32 || len(options.Status) > 64 || !safeOptionalText(options.Status) {
		return errors.New("deployment filter is invalid")
	}
	status := strings.ToLower(strings.TrimSpace(options.Status))
	switch status {
	case "", "running", "completed", "failed", "deployment requested", "termination requested", "destroyed":
	default:
		return errors.New("deployment status is invalid")
	}
	if options.SortBy != "" && options.SortBy != "created_at" {
		return errors.New("sort field is invalid")
	}
	if options.SortOrder != "" && options.SortOrder != "asc" && options.SortOrder != "desc" {
		return errors.New("sort order is invalid")
	}
	return nil
}

func ValidateDeploymentRequest(request *DeploymentRequest) error {
	if request == nil || ValidateName(request.ResourcePrivateName) != nil {
		return errors.New("deployment request is invalid")
	}
	if request.DurationHours < 1 || request.DurationHours > MaxDurationHours ||
		request.GPUsPerContainer < 1 || request.GPUsPerContainer > MaxGPUCount ||
		request.HardwareID < 1 || request.HardwareID > math.MaxInt32 ||
		validateLocations(request.LocationIDs) != nil ||
		request.ContainerConfig.ReplicaCount < 1 || request.ContainerConfig.ReplicaCount > MaxReplicaCount ||
		validateContainerConfig(request.ContainerConfig) != nil ||
		!boundedText(request.RegistryConfig.ImageURL, 1, maxImageURLBytes) ||
		!boundedText(request.RegistryConfig.RegistryUsername, 0, maxRegistryBytes) ||
		!boundedText(request.RegistryConfig.RegistrySecret, 0, maxRegistryBytes) {
		return errors.New("deployment request is invalid")
	}
	return nil
}

func ValidateUpdateDeploymentRequest(request *UpdateDeploymentRequest) error {
	if request == nil {
		return errors.New("update request is invalid")
	}
	if len(request.EnvVariables) == 0 && len(request.SecretEnvVariables) == 0 && len(request.Entrypoint) == 0 &&
		request.TrafficPort == nil && request.ImageURL == "" && request.RegistryUsername == "" &&
		request.RegistrySecret == "" && len(request.Args) == 0 && request.Command == "" {
		return errors.New("update request is empty")
	}
	if validateEnvironment(request.EnvVariables) != nil || validateEnvironment(request.SecretEnvVariables) != nil ||
		validateStringList(request.Entrypoint) != nil || validateStringList(request.Args) != nil ||
		!boundedText(request.ImageURL, 0, maxImageURLBytes) ||
		!boundedText(request.RegistryUsername, 0, maxRegistryBytes) ||
		!boundedText(request.RegistrySecret, 0, maxRegistryBytes) ||
		!boundedText(request.Command, 0, maxArgumentBytes) {
		return errors.New("update request is invalid")
	}
	if request.TrafficPort != nil && (*request.TrafficPort < 1 || *request.TrafficPort > 65535) {
		return errors.New("update request is invalid")
	}
	return nil
}

func ValidateExtendDurationRequest(request *ExtendDurationRequest) error {
	if request == nil || request.DurationHours < 1 || request.DurationHours > MaxDurationHours {
		return errors.New("duration_hours is invalid")
	}
	return nil
}

func ValidatePriceEstimationRequest(request *PriceEstimationRequest) error {
	if request == nil || validateLocations(request.LocationIDs) != nil ||
		request.HardwareID < 1 || request.HardwareID > math.MaxInt32 ||
		request.ReplicaCount < 1 || request.ReplicaCount > MaxReplicaCount ||
		request.DurationHours < 0 || request.DurationQty < 0 || request.HardwareQty < 0 || request.GPUsPerContainer < 0 {
		return errors.New("price estimation request is invalid")
	}
	durationQuantity := request.DurationQty
	if durationQuantity < 1 {
		durationQuantity = request.DurationHours
	}
	if durationQuantity < 1 || durationQuantity > MaxDurationHours {
		return errors.New("price estimation request is invalid")
	}
	hardwareQuantity := request.HardwareQty
	if hardwareQuantity < 1 {
		hardwareQuantity = request.GPUsPerContainer
	}
	if hardwareQuantity < 1 || hardwareQuantity > MaxGPUCount || request.GPUsPerContainer < 0 || request.GPUsPerContainer > MaxGPUCount {
		return errors.New("price estimation request is invalid")
	}
	durationType := strings.ToLower(strings.TrimSpace(request.DurationType))
	switch durationType {
	case "", "hour", "hours", "hourly", "day", "days", "daily", "week", "weeks", "weekly", "month", "months", "monthly":
	default:
		return errors.New("price estimation request is invalid")
	}
	multiplier := 1
	switch durationType {
	case "day", "days", "daily":
		multiplier = 24
	case "week", "weeks", "weekly":
		multiplier = 24 * 7
	case "month", "months", "monthly":
		multiplier = 24 * 30
	}
	if durationQuantity > MaxDurationHours/multiplier {
		return errors.New("price estimation request is invalid")
	}
	currency := strings.TrimSpace(request.Currency)
	if currency != "" && (len(currency) > 8 || !identifierPattern.MatchString(currency)) {
		return errors.New("price estimation request is invalid")
	}
	return nil
}

func ValidateLogsOptions(options *GetLogsOptions) error {
	if options == nil || options.Limit < 1 || options.Limit > MaxLogLimit || len(options.Cursor) > 1024 || !safeOptionalText(options.Cursor) {
		return errors.New("log options are invalid")
	}
	level := strings.ToLower(options.Level)
	switch level {
	case "", "trace", "debug", "info", "warn", "warning", "error", "fatal":
	default:
		return errors.New("log level is invalid")
	}
	switch strings.ToLower(options.Stream) {
	case "", "stdout", "stderr", "all":
	default:
		return errors.New("log stream is invalid")
	}
	if options.StartTime != nil && options.EndTime != nil && options.StartTime.After(*options.EndTime) {
		return errors.New("log time range is invalid")
	}
	return nil
}

func validateContainerConfig(config ContainerConfig) error {
	if validateEnvironment(config.EnvVariables) != nil || validateEnvironment(config.SecretEnvVariables) != nil ||
		validateStringList(config.Entrypoint) != nil || validateStringList(config.Args) != nil {
		return errors.New("container configuration is invalid")
	}
	if config.TrafficPort < 0 || config.TrafficPort > 65535 {
		return errors.New("container configuration is invalid")
	}
	return nil
}

func validateLocations(values []int) error {
	if len(values) == 0 || len(values) > maxLocations {
		return errors.New("locations are invalid")
	}
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value < 1 || value > math.MaxInt32 {
			return errors.New("locations are invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return errors.New("locations are invalid")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateEnvironment(values map[string]string) error {
	if len(values) > maxEnvironmentVars {
		return errors.New("environment is too large")
	}
	for key, value := range values {
		if !boundedText(key, 1, maxEnvKeyBytes) || !boundedText(value, 0, maxEnvValueBytes) {
			return errors.New("environment is invalid")
		}
	}
	return nil
}

func validateStringList(values []string) error {
	if len(values) > maxStringList {
		return errors.New("argument list is too large")
	}
	for _, value := range values {
		if !boundedText(value, 1, maxArgumentBytes) {
			return errors.New("argument list is invalid")
		}
	}
	return nil
}

func boundedText(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func safeOptionalText(value string) bool {
	return value == "" || safeText(value)
}

func safeText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validPublicURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2048 || !safeText(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "https" || parsed.Scheme == "http")
}
