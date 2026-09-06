package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	channelUpstreamUpdateBatchSize        = 100
	maxChannelUpstreamModelNames          = 10_000
	maxChannelUpstreamModelNameBytes      = 512
	maxChannelUpstreamSettingsBytes       = 1 << 20
	maxChannelUpstreamBulkResponseEntries = 1_000
	maxChannelUpstreamBulkResponseModels  = 10_000
	maxChannelUpstreamIgnoreRegexRules    = 256
	defaultUpstreamModelMinCheckSeconds   = int64(300)
)

var (
	// ErrChannelChangedDuringDiscovery prevents an upstream response fetched
	// with stale credentials or a stale endpoint from being applied to a newly
	// edited channel.
	ErrChannelChangedDuringDiscovery = errors.New("channel changed during upstream model discovery")
	errChannelUpstreamBulkIneligible = errors.New("channel is not eligible for bulk upstream model updates")

	// SQLite has no row-level SELECT FOR UPDATE. This mutex also keeps local
	// process mutations deterministic; MySQL/PostgreSQL additionally use the
	// database row lock in channelLockForUpdate.
	channelUpstreamUpdateMu sync.Mutex
)

// ChannelUpstreamModelSettings is the reference-visible subset of a channel's
// JSON settings. Encoding merges these keys back into the original object so
// provider-specific and future settings are never discarded.
type ChannelUpstreamModelSettings struct {
	CheckEnabled       bool     `json:"upstream_model_update_check_enabled,omitempty"`
	AutoSyncEnabled    bool     `json:"upstream_model_update_auto_sync_enabled,omitempty"`
	LastCheckTime      int64    `json:"upstream_model_update_last_check_time,omitempty"`
	LastDetectedModels []string `json:"upstream_model_update_last_detected_models,omitempty"`
	LastRemovedModels  []string `json:"upstream_model_update_last_removed_models,omitempty"`
	IgnoredModels      []string `json:"upstream_model_update_ignored_models,omitempty"`
}

// ChannelUpstreamDetection is returned by a single-channel detection and is
// also used internally by the durable all-channel task.
type ChannelUpstreamDetection struct {
	ChannelID       int      `json:"channel_id"`
	ChannelName     string   `json:"channel_name"`
	AddModels       []string `json:"add_models"`
	RemoveModels    []string `json:"remove_models"`
	LastCheckTime   int64    `json:"last_check_time"`
	AutoAddedModels int      `json:"auto_added_models"`
}

// ChannelUpstreamApplication is the atomic result of applying staged changes.
type ChannelUpstreamApplication struct {
	ChannelID             int      `json:"channel_id"`
	ChannelName           string   `json:"channel_name"`
	AddedModels           []string `json:"added_models"`
	RemovedModels         []string `json:"removed_models"`
	IgnoredModels         []string `json:"ignored_models,omitempty"`
	RemainingModels       []string `json:"remaining_models"`
	RemainingRemoveModels []string `json:"remaining_remove_models"`
	Models                string   `json:"models,omitempty"`
	Settings              string   `json:"settings,omitempty"`
	ModelsChanged         bool     `json:"-"`
	StagedChangesHandled  bool     `json:"-"`
}

// ApplyAllChannelUpstreamSummary is the bounded bulk-apply response.
type ApplyAllChannelUpstreamSummary struct {
	ProcessedChannels  int                          `json:"processed_channels"`
	AddedModels        int                          `json:"added_models"`
	RemovedModels      int                          `json:"removed_models"`
	FailedChannelIDs   []int                        `json:"failed_channel_ids"`
	Results            []ChannelUpstreamApplication `json:"results"`
	ResultsTruncated   bool                         `json:"results_truncated,omitempty"`
	FailedIDsTruncated bool                         `json:"failed_channel_ids_truncated,omitempty"`
}

// ChannelUpstreamUpdateSummary is persisted as the model_update task result.
type ChannelUpstreamUpdateSummary struct {
	CheckedChannels      int `json:"checked_channels"`
	ChangedChannels      int `json:"changed_channels"`
	DetectedAddModels    int `json:"detected_add_models"`
	DetectedRemoveModels int `json:"detected_remove_models"`
	FailedChannels       int `json:"failed_channels"`
	AutoAddedModels      int `json:"auto_added_models"`
}

// ValidateChannelOtherSettings validates the dashboard's opaque settings
// object without discarding provider-specific keys. It is shared by channel
// create/update so malformed update-tracking state cannot enter the database.
func ValidateChannelOtherSettings(raw string) error {
	return ValidateChannelOtherSettingsForType(raw, -1)
}

// ValidateChannelOtherSettingsForType also checks advanced-custom routing
// configuration when present and requires it for Advanced Custom channels.
// Unknown settings keys remain accepted and are preserved by update flows.
func ValidateChannelOtherSettingsForType(raw string, channelType int) error {
	settings, rawFields, err := decodeChannelUpstreamModelSettings(raw)
	if err != nil {
		return err
	}
	var advanced *advancedCustomModelConfig
	if encoded, exists := rawFields["advanced_custom"]; exists && string(encoded) != "null" {
		if err := jsonutil.Unmarshal(encoded, &advanced); err != nil || advanced == nil {
			return errors.New("advanced_custom must be a JSON object")
		}
		if _, err := validateAdvancedCustomModelConfig(advanced); err != nil {
			return err
		}
	}
	if channelType == int(channelcatalog.ChannelTypeAdvancedCustom) {
		if advanced == nil {
			return errors.New("advanced_custom is required")
		}
		if settings.CheckEnabled {
			hasModelListRoute, err := validateAdvancedCustomModelConfig(advanced)
			if err != nil {
				return err
			}
			if !hasModelListRoute {
				return errors.New("advanced custom channels require a /v1/models route when upstream model update checks are enabled")
			}
		}
	}
	return nil
}

func normalizeUpstreamModelNames(values []string) ([]string, error) {
	if len(values) > maxChannelUpstreamModelNames {
		return nil, fmt.Errorf("model list exceeds %d entries", maxChannelUpstreamModelNames)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !utf8.ValidString(value) || len(value) > maxChannelUpstreamModelNameBytes {
			return nil, fmt.Errorf("model name must be valid UTF-8 and at most %d bytes", maxChannelUpstreamModelNameBytes)
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, errors.New("model name contains a control character")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validateUpstreamIgnoreRules(values []string) error {
	regexRules := 0
	for _, value := range values {
		if _, ok := strings.CutPrefix(value, "regex:"); ok {
			regexRules++
			if regexRules > maxChannelUpstreamIgnoreRegexRules {
				return fmt.Errorf("ignored model list exceeds %d regex rules", maxChannelUpstreamIgnoreRegexRules)
			}
		}
	}
	return nil
}

func mergeUpstreamModelNames(base, added []string) []string {
	result := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(base)+len(added))
	for _, value := range result {
		seen[value] = struct{}{}
	}
	for _, value := range added {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func subtractUpstreamModelNames(base, removed []string) []string {
	removeSet := make(map[string]struct{}, len(removed))
	for _, value := range removed {
		removeSet[value] = struct{}{}
	}
	result := make([]string, 0, len(base))
	for _, value := range base {
		if _, ok := removeSet[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

func intersectUpstreamModelNames(values, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := allowedSet[value]; ok {
			result = append(result, value)
		}
	}
	return result
}

func decodeChannelUpstreamModelSettings(raw string) (ChannelUpstreamModelSettings, map[string]json.RawMessage, error) {
	settings := ChannelUpstreamModelSettings{}
	rawFields := make(map[string]json.RawMessage)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return settings, rawFields, nil
	}
	if len(trimmed) > maxChannelUpstreamSettingsBytes {
		return settings, nil, fmt.Errorf("channel settings exceed %d bytes", maxChannelUpstreamSettingsBytes)
	}
	if err := jsonutil.Unmarshal([]byte(trimmed), &rawFields); err != nil {
		return settings, nil, errors.New("channel settings must be a JSON object")
	}
	if rawFields == nil {
		return settings, nil, errors.New("channel settings must be a JSON object")
	}
	if err := jsonutil.Unmarshal([]byte(trimmed), &settings); err != nil {
		return settings, nil, errors.New("channel upstream model settings are invalid")
	}
	var err error
	settings.LastDetectedModels, err = normalizeUpstreamModelNames(settings.LastDetectedModels)
	if err != nil {
		return settings, nil, fmt.Errorf("invalid detected model list: %w", err)
	}
	settings.LastRemovedModels, err = normalizeUpstreamModelNames(settings.LastRemovedModels)
	if err != nil {
		return settings, nil, fmt.Errorf("invalid removed model list: %w", err)
	}
	settings.IgnoredModels, err = normalizeUpstreamModelNames(settings.IgnoredModels)
	if err != nil {
		return settings, nil, fmt.Errorf("invalid ignored model list: %w", err)
	}
	if err := validateUpstreamIgnoreRules(settings.IgnoredModels); err != nil {
		return settings, nil, err
	}
	return settings, rawFields, nil
}

func encodeChannelUpstreamModelSettings(settings ChannelUpstreamModelSettings, rawFields map[string]json.RawMessage) (string, error) {
	if err := validateUpstreamIgnoreRules(settings.IgnoredModels); err != nil {
		return "", err
	}
	if rawFields == nil {
		rawFields = make(map[string]json.RawMessage)
	}
	for _, key := range []string{
		"upstream_model_update_check_enabled",
		"upstream_model_update_auto_sync_enabled",
		"upstream_model_update_last_check_time",
		"upstream_model_update_last_detected_models",
		"upstream_model_update_last_removed_models",
		"upstream_model_update_ignored_models",
	} {
		delete(rawFields, key)
	}
	encoded, err := jsonutil.Marshal(settings)
	if err != nil {
		return "", err
	}
	knownFields := make(map[string]json.RawMessage)
	if err := jsonutil.Unmarshal(encoded, &knownFields); err != nil {
		return "", err
	}
	for key, value := range knownFields {
		rawFields[key] = value
	}
	encoded, err = jsonutil.Marshal(rawFields)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxChannelUpstreamSettingsBytes {
		return "", fmt.Errorf("channel settings exceed %d bytes", maxChannelUpstreamSettingsBytes)
	}
	return string(encoded), nil
}

func decodeChannelModelMapping(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return nil, nil
	}
	if len(raw) > maxChannelUpstreamSettingsBytes {
		return nil, errors.New("channel model mapping is too large")
	}
	parsed := make(map[string]string)
	if err := jsonutil.UnmarshalJsonStr(raw, &parsed); err != nil {
		return nil, errors.New("channel model mapping must be a JSON object of strings")
	}
	if len(parsed) > maxChannelUpstreamModelNames {
		return nil, errors.New("channel model mapping has too many entries")
	}
	normalized := make(map[string]string, len(parsed))
	for source, target := range parsed {
		sources, err := normalizeUpstreamModelNames([]string{source})
		if err != nil {
			return nil, fmt.Errorf("invalid model mapping source: %w", err)
		}
		targets, err := normalizeUpstreamModelNames([]string{target})
		if err != nil {
			return nil, fmt.Errorf("invalid model mapping target: %w", err)
		}
		if len(sources) == 0 || len(targets) == 0 {
			continue
		}
		normalized[sources[0]] = targets[0]
	}
	return normalized, nil
}

func collectChannelUpstreamModelChanges(localModels, upstreamModels, ignored []string, mapping map[string]string) ([]string, []string) {
	upstreamSet := make(map[string]struct{}, len(upstreamModels))
	coveredUpstream := make(map[string]struct{}, len(localModels)+len(mapping))
	redirectSources := make(map[string]struct{}, len(mapping))
	ignoredExact := make(map[string]struct{}, len(ignored))
	ignoredPatterns := make([]*regexp.Regexp, 0)
	for _, rule := range ignored {
		if expression, ok := strings.CutPrefix(rule, "regex:"); ok {
			expression = strings.TrimSpace(expression)
			if expression == "" || len(expression) > maxChannelUpstreamModelNameBytes {
				continue
			}
			if compiled, err := regexp.Compile(expression); err == nil {
				ignoredPatterns = append(ignoredPatterns, compiled)
			}
			continue
		}
		ignoredExact[rule] = struct{}{}
	}
	for _, value := range localModels {
		coveredUpstream[value] = struct{}{}
	}
	for _, value := range upstreamModels {
		upstreamSet[value] = struct{}{}
	}
	for source, target := range mapping {
		redirectSources[source] = struct{}{}
		coveredUpstream[target] = struct{}{}
	}

	addModels := make([]string, 0)
	for _, value := range upstreamModels {
		if _, ok := coveredUpstream[value]; ok {
			continue
		}
		if _, ok := ignoredExact[value]; ok {
			continue
		}
		ignoredByPattern := false
		for _, pattern := range ignoredPatterns {
			if pattern.MatchString(value) {
				ignoredByPattern = true
				break
			}
		}
		if ignoredByPattern {
			continue
		}
		addModels = append(addModels, value)
	}
	removeModels := make([]string, 0)
	for _, value := range localModels {
		if _, alias := redirectSources[value]; alias {
			continue
		}
		if _, exists := upstreamSet[value]; !exists {
			removeModels = append(removeModels, value)
		}
	}
	return addModels, removeModels
}

func sameUpstreamDiscoveryEndpoint(before, current *model.Channel) bool {
	return before != nil && current != nil &&
		before.Type == current.Type &&
		before.Key == current.Key &&
		before.BaseURL == current.BaseURL &&
		before.Other == current.Other &&
		before.Setting == current.Setting &&
		before.HeaderOverride == current.HeaderOverride &&
		before.OtherSettings == current.OtherSettings
}

func sanitizeUpstreamDiscoveryError(err error) error {
	if err == nil {
		return nil
	}
	var requestError *url.Error
	if errors.As(err, &requestError) && requestError.Err != nil {
		return fmt.Errorf("upstream model request failed: %w", requestError.Err)
	}
	return err
}

// DetectChannelUpstreamModelUpdates fetches one channel's current upstream
// catalog and atomically stages its differences. Scheduled callers may enable
// auto-application of additions; removals always require explicit review.
func DetectChannelUpstreamModelUpdates(ctx context.Context, channelID int, force, allowAutoApply bool) (*ChannelUpstreamDetection, bool, error) {
	return detectChannelUpstreamModelUpdates(ctx, channelID, force, allowAutoApply, true, false)
}

func detectChannelUpstreamModelUpdates(ctx context.Context, channelID int, force, allowAutoApply, refreshCache, requireEnabled bool) (*ChannelUpstreamDetection, bool, error) {
	if channelID <= 0 {
		return nil, false, errors.New("invalid channel id")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	snapshot, err := GetChannelByID(channelID)
	if err != nil {
		return nil, false, err
	}
	if requireEnabled && snapshot.Status != channelcatalog.ChannelStatusEnabled {
		return nil, false, errChannelUpstreamBulkIneligible
	}
	settings, _, err := decodeChannelUpstreamModelSettings(snapshot.OtherSettings)
	if err != nil {
		return nil, false, err
	}
	if requireEnabled && !settings.CheckEnabled {
		return nil, false, errChannelUpstreamBulkIneligible
	}
	now := wallclock.NowTimestamp()
	if !force {
		minimum := int64(env.GetEnvInt("CHANNEL_UPSTREAM_MODEL_UPDATE_MIN_CHECK_INTERVAL_SECONDS", int(defaultUpstreamModelMinCheckSeconds)))
		if minimum < 0 {
			minimum = defaultUpstreamModelMinCheckSeconds
		}
		if settings.LastCheckTime > 0 && now-settings.LastCheckTime < minimum {
			return &ChannelUpstreamDetection{
				ChannelID: channelID, ChannelName: snapshot.Name,
				AddModels:     append([]string(nil), settings.LastDetectedModels...),
				RemoveModels:  append([]string(nil), settings.LastRemovedModels...),
				LastCheckTime: settings.LastCheckTime,
			}, false, nil
		}
	}

	upstreamModels, fetchErr := FetchUpstreamModelsForChannelContext(ctx, snapshot)
	if fetchErr != nil {
		persistErr := persistChannelUpstreamCheckTime(ctx, snapshot, now, requireEnabled)
		return nil, false, errors.Join(sanitizeUpstreamDiscoveryError(fetchErr), persistErr)
	}
	upstreamModels, err = normalizeUpstreamModelNames(upstreamModels)
	if err != nil {
		persistErr := persistChannelUpstreamCheckTime(ctx, snapshot, now, requireEnabled)
		return nil, false, errors.Join(fmt.Errorf("invalid upstream model list: %w", err), persistErr)
	}

	channelUpstreamUpdateMu.Lock()
	defer channelUpstreamUpdateMu.Unlock()
	result := &ChannelUpstreamDetection{}
	modelsChanged := false
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Channel
		if err := channelLockForUpdate(tx).First(&current, channelID).Error; err != nil {
			return err
		}
		if !sameUpstreamDiscoveryEndpoint(snapshot, &current) {
			return ErrChannelChangedDuringDiscovery
		}
		if requireEnabled && current.Status != channelcatalog.ChannelStatusEnabled {
			return errChannelUpstreamBulkIneligible
		}
		currentSettings, rawFields, err := decodeChannelUpstreamModelSettings(current.OtherSettings)
		if err != nil {
			return err
		}
		if requireEnabled && !currentSettings.CheckEnabled {
			return errChannelUpstreamBulkIneligible
		}
		localModels, err := normalizeUpstreamModelNames(strings.Split(current.Models, ","))
		if err != nil {
			return fmt.Errorf("invalid local model list: %w", err)
		}
		mapping, err := decodeChannelModelMapping(current.ModelMapping)
		if err != nil {
			return err
		}
		addModels, removeModels := collectChannelUpstreamModelChanges(localModels, upstreamModels, currentSettings.IgnoredModels, mapping)
		autoAdded := 0
		if allowAutoApply && currentSettings.AutoSyncEnabled && len(addModels) > 0 {
			nextModels := mergeUpstreamModelNames(localModels, addModels)
			autoAdded = len(nextModels) - len(localModels)
			if autoAdded > 0 {
				current.Models = strings.Join(nextModels, ",")
				modelsChanged = true
			}
			currentSettings.LastDetectedModels = nil
		} else {
			currentSettings.LastDetectedModels = addModels
		}
		currentSettings.LastRemovedModels = removeModels
		currentSettings.LastCheckTime = now
		encodedSettings, err := encodeChannelUpstreamModelSettings(currentSettings, rawFields)
		if err != nil {
			return err
		}
		updates := map[string]any{"settings": encodedSettings}
		if modelsChanged {
			updates["models"] = current.Models
		}
		update := tx.Model(&model.Channel{}).Where("id = ?", current.Id).Updates(updates)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("channel %d update affected %d rows", current.Id, update.RowsAffected)
		}
		if modelsChanged {
			if err := rebuildChannelAbilitiesTx(tx, &current); err != nil {
				return fmt.Errorf("rebuild channel abilities: %w", err)
			}
		}
		current.OtherSettings = encodedSettings
		result = &ChannelUpstreamDetection{
			ChannelID: current.Id, ChannelName: current.Name,
			AddModels:     append([]string(nil), currentSettings.LastDetectedModels...),
			RemoveModels:  append([]string(nil), currentSettings.LastRemovedModels...),
			LastCheckTime: now, AutoAddedModels: autoAdded,
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if modelsChanged && refreshCache {
		if err := SyncAbilityCache(); err != nil {
			return result, true, fmt.Errorf("refresh ability cache after upstream model update: %w", err)
		}
	}
	return result, modelsChanged, nil
}

func persistChannelUpstreamCheckTime(ctx context.Context, snapshot *model.Channel, now int64, requireEnabled bool) error {
	if snapshot == nil {
		return errors.New("channel snapshot is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	channelUpstreamUpdateMu.Lock()
	defer channelUpstreamUpdateMu.Unlock()
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Channel
		if err := channelLockForUpdate(tx).First(&current, snapshot.Id).Error; err != nil {
			return err
		}
		if !sameUpstreamDiscoveryEndpoint(snapshot, &current) {
			return ErrChannelChangedDuringDiscovery
		}
		if requireEnabled && current.Status != channelcatalog.ChannelStatusEnabled {
			return errChannelUpstreamBulkIneligible
		}
		settings, rawFields, err := decodeChannelUpstreamModelSettings(current.OtherSettings)
		if err != nil {
			return err
		}
		if requireEnabled && !settings.CheckEnabled {
			return errChannelUpstreamBulkIneligible
		}
		settings.LastCheckTime = now
		encoded, err := encodeChannelUpstreamModelSettings(settings, rawFields)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Channel{}).Where("id = ?", current.Id).Update("settings", encoded)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("channel %d update affected %d rows", current.Id, result.RowsAffected)
		}
		return nil
	})
}

// ApplyChannelUpstreamModelUpdates applies only changes that are currently
// staged on the channel. Forged/stale model names cannot be injected through
// this operation, and channel plus abilities commit in one transaction.
func ApplyChannelUpstreamModelUpdates(ctx context.Context, channelID int, addInput, ignoreInput, removeInput []string) (*ChannelUpstreamApplication, error) {
	return applyChannelUpstreamModelUpdates(ctx, channelID, addInput, ignoreInput, removeInput, true, false)
}

func applyChannelUpstreamModelUpdates(ctx context.Context, channelID int, addInput, ignoreInput, removeInput []string, refreshCache, requireEligible bool) (*ChannelUpstreamApplication, error) {
	if channelID <= 0 {
		return nil, errors.New("invalid channel id")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	if addInput, err = normalizeUpstreamModelNames(addInput); err != nil {
		return nil, fmt.Errorf("invalid add_models: %w", err)
	}
	if ignoreInput, err = normalizeUpstreamModelNames(ignoreInput); err != nil {
		return nil, fmt.Errorf("invalid ignore_models: %w", err)
	}
	if removeInput, err = normalizeUpstreamModelNames(removeInput); err != nil {
		return nil, fmt.Errorf("invalid remove_models: %w", err)
	}

	channelUpstreamUpdateMu.Lock()
	defer channelUpstreamUpdateMu.Unlock()
	result := &ChannelUpstreamApplication{}
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel model.Channel
		if err := channelLockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		settings, rawFields, err := decodeChannelUpstreamModelSettings(channel.OtherSettings)
		if err != nil {
			return err
		}
		if requireEligible && (channel.Status != channelcatalog.ChannelStatusEnabled || !settings.CheckEnabled) {
			return errChannelUpstreamBulkIneligible
		}
		localModels, err := normalizeUpstreamModelNames(strings.Split(channel.Models, ","))
		if err != nil {
			return fmt.Errorf("invalid local model list: %w", err)
		}
		added := intersectUpstreamModelNames(addInput, settings.LastDetectedModels)
		ignored := intersectUpstreamModelNames(ignoreInput, settings.LastDetectedModels)
		removed := intersectUpstreamModelNames(removeInput, settings.LastRemovedModels)
		removed = subtractUpstreamModelNames(removed, added)
		stagedChangesHandled := len(added) > 0 || len(ignored) > 0 || len(removed) > 0
		nextModels := subtractUpstreamModelNames(mergeUpstreamModelNames(localModels, added), removed)
		modelsChanged := !slices.Equal(localModels, nextModels)
		if modelsChanged {
			channel.Models = strings.Join(nextModels, ",")
		}
		settings.IgnoredModels = mergeUpstreamModelNames(settings.IgnoredModels, ignored)
		settings.IgnoredModels = subtractUpstreamModelNames(settings.IgnoredModels, added)
		settings.LastDetectedModels = subtractUpstreamModelNames(settings.LastDetectedModels, append(append([]string(nil), added...), ignored...))
		settings.LastRemovedModels = subtractUpstreamModelNames(settings.LastRemovedModels, removed)
		settings.LastCheckTime = wallclock.NowTimestamp()
		encodedSettings, err := encodeChannelUpstreamModelSettings(settings, rawFields)
		if err != nil {
			return err
		}
		updates := map[string]any{"settings": encodedSettings}
		if modelsChanged {
			updates["models"] = channel.Models
		}
		update := tx.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(updates)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("channel %d update affected %d rows", channel.Id, update.RowsAffected)
		}
		if modelsChanged {
			if err := rebuildChannelAbilitiesTx(tx, &channel); err != nil {
				return fmt.Errorf("rebuild channel abilities: %w", err)
			}
		}
		result = &ChannelUpstreamApplication{
			ChannelID: channel.Id, ChannelName: channel.Name,
			AddedModels:           append([]string(nil), added...),
			RemovedModels:         append([]string(nil), removed...),
			IgnoredModels:         append([]string(nil), ignored...),
			RemainingModels:       append([]string(nil), settings.LastDetectedModels...),
			RemainingRemoveModels: append([]string(nil), settings.LastRemovedModels...),
			Models:                channel.Models, Settings: encodedSettings, ModelsChanged: modelsChanged,
			StagedChangesHandled: stagedChangesHandled,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.ModelsChanged && refreshCache {
		if err := SyncAbilityCache(); err != nil {
			return result, fmt.Errorf("refresh ability cache after upstream model apply: %w", err)
		}
	}
	return result, nil
}

// ApplyAllChannelUpstreamModelUpdates applies every staged change on enabled,
// opted-in channels. Per-channel failures are isolated and reported, matching
// the operator workflow while preserving atomicity within each channel.
func ApplyAllChannelUpstreamModelUpdates(ctx context.Context) (*ApplyAllChannelUpstreamSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	summary := &ApplyAllChannelUpstreamSummary{
		FailedChannelIDs: make([]int, 0),
		Results:          make([]ChannelUpstreamApplication, 0),
	}
	recordFailure := func(channelID int) {
		if len(summary.FailedChannelIDs) < maxChannelUpstreamBulkResponseEntries {
			summary.FailedChannelIDs = append(summary.FailedChannelIDs, channelID)
		} else {
			summary.FailedIDsTruncated = true
		}
	}
	lastID := 0
	cacheRefreshNeeded := false
	returnedModelNames := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var channels []model.Channel
		query := model.DB.WithContext(ctx).Where("status = ?", channelcatalog.ChannelStatusEnabled).
			Where("id > ?", lastID).Order("id asc").Limit(channelUpstreamUpdateBatchSize)
		if err := query.Find(&channels).Error; err != nil {
			return nil, err
		}
		if len(channels) == 0 {
			break
		}
		lastID = channels[len(channels)-1].Id
		for i := range channels {
			channel := &channels[i]
			settings, _, err := decodeChannelUpstreamModelSettings(channel.OtherSettings)
			if err != nil {
				recordFailure(channel.Id)
				continue
			}
			if !settings.CheckEnabled || (len(settings.LastDetectedModels) == 0 && len(settings.LastRemovedModels) == 0) {
				continue
			}
			application, err := applyChannelUpstreamModelUpdates(ctx, channel.Id, settings.LastDetectedModels, nil, settings.LastRemovedModels, false, true)
			if err != nil {
				if errors.Is(err, errChannelUpstreamBulkIneligible) {
					continue
				}
				recordFailure(channel.Id)
				continue
			}
			if !application.StagedChangesHandled {
				continue
			}
			summary.ProcessedChannels++
			summary.AddedModels += len(application.AddedModels)
			summary.RemovedModels += len(application.RemovedModels)
			cacheRefreshNeeded = cacheRefreshNeeded || application.ModelsChanged
			modelNamesInResult := len(application.AddedModels) + len(application.RemovedModels) +
				len(application.RemainingModels) + len(application.RemainingRemoveModels)
			if len(summary.Results) < maxChannelUpstreamBulkResponseEntries &&
				returnedModelNames+modelNamesInResult <= maxChannelUpstreamBulkResponseModels {
				summary.Results = append(summary.Results, *application)
				returnedModelNames += modelNamesInResult
			} else {
				summary.ResultsTruncated = true
			}
		}
		if len(channels) < channelUpstreamUpdateBatchSize {
			break
		}
	}
	if cacheRefreshNeeded {
		if err := SyncAbilityCache(); err != nil {
			return summary, fmt.Errorf("refresh ability cache after bulk upstream model apply: %w", err)
		}
	}
	return summary, nil
}

// RunChannelUpstreamModelUpdateTask runs the durable all-channel detector.
// Failed providers are counted instead of aborting unrelated channels.
func RunChannelUpstreamModelUpdateTask(ctx context.Context, force, allowAutoApply bool, report func(processed, total int) error) (ChannelUpstreamUpdateSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	summary := ChannelUpstreamUpdateSummary{}
	notification := channelUpstreamNotificationSummary{}
	var total int64
	if err := model.DB.WithContext(ctx).Model(&model.Channel{}).Where("status = ?", channelcatalog.ChannelStatusEnabled).Count(&total).Error; err != nil {
		return summary, err
	}
	lastID := 0
	processed := 0
	cacheRefreshNeeded := false
	pollingIntervalSeconds := env.GetEnvInt("POLLING_INTERVAL", 0)
	if pollingIntervalSeconds < 0 {
		pollingIntervalSeconds = 0
	}
	if pollingIntervalSeconds > 3600 {
		pollingIntervalSeconds = 3600
	}
	pollingInterval := time.Duration(pollingIntervalSeconds) * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		var channels []model.Channel
		query := model.DB.WithContext(ctx).Where("status = ? AND id > ?", channelcatalog.ChannelStatusEnabled, lastID).
			Order("id asc").Limit(channelUpstreamUpdateBatchSize)
		if err := query.Find(&channels).Error; err != nil {
			return summary, err
		}
		if len(channels) == 0 {
			break
		}
		lastID = channels[len(channels)-1].Id
		for i := range channels {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			processed++
			if report != nil {
				if err := report(processed, int(total)); err != nil {
					return summary, err
				}
			}
			settings, _, err := decodeChannelUpstreamModelSettings(channels[i].OtherSettings)
			if err != nil {
				summary.FailedChannels++
				continue
			}
			if !settings.CheckEnabled {
				continue
			}
			detection, changed, err := detectChannelUpstreamModelUpdates(ctx, channels[i].Id, force, allowAutoApply, false, true)
			if errors.Is(err, errChannelUpstreamBulkIneligible) {
				continue
			}
			summary.CheckedChannels++
			if err != nil {
				summary.FailedChannels++
				if len(notification.FailedChannelIDs) < channelUpstreamNotifyMaxFailures {
					notification.FailedChannelIDs = append(notification.FailedChannelIDs, channels[i].Id)
				}
				logging.SysError(fmt.Sprintf("upstream model detection failed for channel %d", channels[i].Id))
				continue
			}
			addCount := len(detection.AddModels) + detection.AutoAddedModels
			removeCount := len(detection.RemoveModels)
			summary.DetectedAddModels += addCount
			summary.DetectedRemoveModels += removeCount
			summary.AutoAddedModels += detection.AutoAddedModels
			if addCount > 0 || removeCount > 0 {
				summary.ChangedChannels++
				if len(notification.Channels) < channelUpstreamNotifyMaxChannels {
					notification.Channels = append(notification.Channels, channelUpstreamNotificationChannel{
						Name: channels[i].Name, AddCount: addCount, RemoveCount: removeCount,
					})
				}
			}
			notification.AddModelSamples = appendBoundedUniqueModelSamples(notification.AddModelSamples, detection.AddModels)
			notification.RemoveModelSamples = appendBoundedUniqueModelSamples(notification.RemoveModelSamples, detection.RemoveModels)
			cacheRefreshNeeded = cacheRefreshNeeded || changed
			if pollingInterval > 0 {
				timer := time.NewTimer(pollingInterval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return summary, ctx.Err()
				case <-timer.C:
				}
			}
		}
		if len(channels) < channelUpstreamUpdateBatchSize {
			break
		}
	}
	if report != nil {
		if err := report(int(total), int(total)); err != nil {
			return summary, err
		}
	}
	if cacheRefreshNeeded {
		if err := SyncAbilityCache(); err != nil {
			return summary, fmt.Errorf("refresh ability cache after model update task: %w", err)
		}
	}
	notification.CheckedChannels = summary.CheckedChannels
	notification.ChangedChannels = summary.ChangedChannels
	notification.DetectedAddModels = summary.DetectedAddModels
	notification.DetectedRemoveModels = summary.DetectedRemoveModels
	notification.AutoAddedModels = summary.AutoAddedModels
	notification.FailedChannels = summary.FailedChannels
	notifyChannelUpstreamWatchers(notification)
	return summary, nil
}
