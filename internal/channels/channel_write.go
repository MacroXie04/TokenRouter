package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/ollama"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// fixAbilityLock prevents concurrent ability-rebuild runs.
var fixAbilityLock sync.Mutex

// channelStatusLock serializes status writes so a multi-key writer cannot
// clobber another's read-modify-write.
var channelStatusLock sync.Mutex

// channelBalanceSweepLock prevents overlapping all-channel refreshes from
// duplicating upstream traffic and racing automatic status changes.
var channelBalanceSweepLock sync.Mutex

const (
	maxChannelBatchIDs = 500
	maxChannelTagBytes = 64
)

var (
	ErrInvalidChannelBatch = errors.New("invalid channel batch")
	ErrInvalidChannelTag   = errors.New("invalid channel tag")
)

func normalizeChannelBatchIDs(channelIDs []int) ([]int, error) {
	if len(channelIDs) == 0 {
		return nil, nil
	}
	if len(channelIDs) > maxChannelBatchIDs {
		return nil, ErrInvalidChannelBatch
	}
	seen := make(map[int]struct{}, len(channelIDs))
	result := make([]int, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			return nil, ErrInvalidChannelBatch
		}
		if _, duplicate := seen[channelID]; duplicate {
			return nil, ErrInvalidChannelBatch
		}
		seen[channelID] = struct{}{}
		result = append(result, channelID)
	}
	return result, nil
}

func validChannelTag(tag string, allowEmpty bool) bool {
	if tag == "" {
		return allowEmpty
	}
	if len(tag) > maxChannelTagBytes || !utf8.ValidString(tag) {
		return false
	}
	for _, character := range tag {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

// UpdateChannelStatus moves a channel to the given status and reports whether
// the value actually changed. The ability-enabled flag follows the status,
// and other_info records the reason/time (reference contract).
func UpdateChannelStatus(channelId, status int, reason string) bool {
	changed, err := UpdateChannelStatusChecked(channelId, status, reason)
	if err != nil {
		logging.SysError(fmt.Sprintf("update channel %d status failed: %v", channelId, err))
	}
	return changed
}

// UpdateChannelStatusChecked is the error-returning status mutation used by
// management handlers. The channel and its abilities commit together; cache
// refresh failures are reported after the durable transaction commits.
func UpdateChannelStatusChecked(channelId, status int, reason string) (bool, error) {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	changed := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var channel model.Channel
		if err := channelLockForUpdate(tx).First(&channel, channelId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if channel.Status == status {
			return nil
		}
		info := parseOtherInfo(channel.OtherInfo)
		info["status_reason"] = reason
		info["status_time"] = wallclock.NowTimestamp()
		otherInfo, err := jsonutil.Marshal(info)
		if err != nil {
			return fmt.Errorf("encode channel status metadata: %w", err)
		}
		result := tx.Model(&model.Channel{}).Where("id = ?", channelId).Updates(map[string]any{
			"status": status, "other_info": string(otherInfo),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("channel %d status update affected %d rows", channelId, result.RowsAffected)
		}
		if err := tx.Model(&model.Ability{}).Where("channel_id = ?", channelId).
			Update("enabled", status == channelcatalog.ChannelStatusEnabled).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("update channel status: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return changed, fmt.Errorf("refresh ability cache after channel status update: %w", err)
	}
	return changed, nil
}

// UpdateChannelStatusesChecked applies a manual batch status change as one
// transaction so one failed channel/ability write cannot leave a partial
// batch. Missing channel ids retain the historical "unchanged" behavior.
func UpdateChannelStatusesChecked(channelIDs []int, status int, reason string) (int, error) {
	channelIDs, err := normalizeChannelBatchIDs(channelIDs)
	if err != nil {
		return 0, err
	}
	if len(channelIDs) == 0 {
		return 0, nil
	}
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	changedCount := 0
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		for _, channelID := range channelIDs {
			var channel model.Channel
			if err := channelLockForUpdate(tx).First(&channel, channelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return fmt.Errorf("load channel %d: %w", channelID, err)
			}
			if channel.Status == status {
				continue
			}
			info := parseOtherInfo(channel.OtherInfo)
			info["status_reason"] = reason
			info["status_time"] = wallclock.NowTimestamp()
			otherInfo, err := jsonutil.Marshal(info)
			if err != nil {
				return fmt.Errorf("encode channel %d status metadata: %w", channelID, err)
			}
			result := tx.Model(&model.Channel{}).Where("id = ?", channelID).Updates(map[string]any{
				"status": status, "other_info": string(otherInfo),
			})
			if result.Error != nil {
				return fmt.Errorf("update channel %d: %w", channelID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("channel %d status update affected %d rows", channelID, result.RowsAffected)
			}
			if err := tx.Model(&model.Ability{}).Where("channel_id = ?", channelID).
				Update("enabled", status == channelcatalog.ChannelStatusEnabled).Error; err != nil {
				return fmt.Errorf("update channel %d abilities: %w", channelID, err)
			}
			changedCount++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("update channel status batch: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return changedCount, fmt.Errorf("refresh ability cache after channel status batch: %w", err)
	}
	return changedCount, nil
}

// EnableChannelsByTag enables every channel carrying the tag (and its
// abilities).
func EnableChannelsByTag(tag string) error {
	return setChannelsEnabledByTag(tag, channelcatalog.ChannelStatusEnabled, true)
}

// DisableChannelsByTag manually disables every channel carrying the tag (and
// its abilities).
func DisableChannelsByTag(tag string) error {
	return setChannelsEnabledByTag(tag, channelcatalog.ChannelStatusManuallyDisabled, false)
}

func setChannelsEnabledByTag(tag string, status int, enabled bool) error {
	if !validChannelTag(tag, false) {
		return ErrInvalidChannelTag
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := channelLockForUpdate(tx).Where("tag = ?", tag).Select("id").Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		channelIDs := make([]int, 0, len(channels))
		for i := range channels {
			channelIDs = append(channelIDs, channels[i].Id)
		}
		if err := tx.Model(&model.Channel{}).Where("id IN ?", channelIDs).
			Update("status", status).Error; err != nil {
			return err
		}
		return tx.Model(&model.Ability{}).Where("channel_id IN ?", channelIDs).
			Update("enabled", enabled).Error
	}); err != nil {
		return fmt.Errorf("update channels and abilities by tag: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after tag status update: %w", err)
	}
	return nil
}

// EditChannelsByTag applies field updates to every channel with the tag. When
// models or groups change, each affected channel's abilities are rebuilt.
func EditChannelsByTag(tag string, newTag, modelMapping, models, groups *string, priority *int64, weight *uint, paramOverride, headerOverride *string) error {
	if !validChannelTag(tag, false) || newTag != nil && !validChannelTag(*newTag, true) {
		return ErrInvalidChannelTag
	}
	updates := map[string]any{}
	if newTag != nil && *newTag != tag {
		updates["tag"] = *newTag
	}
	if modelMapping != nil {
		updates["model_mapping"] = *modelMapping
	}
	if models != nil && *models != "" {
		updates["models"] = *models
	}
	if groups != nil && *groups != "" {
		updates["group"] = *groups
	}
	if priority != nil {
		updates["priority"] = *priority
	}
	if weight != nil {
		updates["weight"] = *weight
	}
	if paramOverride != nil {
		updates["param_override"] = *paramOverride
	}
	if headerOverride != nil {
		updates["header_override"] = *headerOverride
	}
	rebuildAbilities := (models != nil && *models != "") || (groups != nil && *groups != "")
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := channelLockForUpdate(tx).Where("tag = ?", tag).Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		channelIDs := make([]int, 0, len(channels))
		for i := range channels {
			channelIDs = append(channelIDs, channels[i].Id)
		}
		if len(updates) > 0 {
			if err := tx.Model(&model.Channel{}).Where("id IN ?", channelIDs).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
				return err
			}
		}
		if rebuildAbilities {
			for i := range channels {
				if err := rebuildChannelAbilitiesTx(tx, &channels[i]); err != nil {
					return fmt.Errorf("rebuild channel %d abilities: %w", channels[i].Id, err)
				}
			}
			return nil
		}
		abilityUpdates := map[string]any{}
		if newTag != nil {
			abilityUpdates["tag"] = *newTag
		}
		if priority != nil {
			abilityUpdates["priority"] = *priority
		}
		if weight != nil {
			abilityUpdates["weight"] = *weight
		}
		if len(abilityUpdates) == 0 {
			return nil
		}
		return tx.Model(&model.Ability{}).Where("channel_id IN ?", channelIDs).
			Updates(abilityUpdates).Error
	})
	if err != nil {
		return fmt.Errorf("edit channels and abilities by tag: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after tag edit: %w", err)
	}
	return nil
}

// GetChannelsByTag returns all channels with the given tag.
func GetChannelsByTag(tag string) ([]*model.Channel, error) {
	if !validChannelTag(tag, false) {
		return nil, ErrInvalidChannelTag
	}
	var channels []*model.Channel
	err := model.DB.Where("tag = ?", tag).Find(&channels).Error
	return channels, err
}

// DeleteDisabledChannels hard-deletes auto-disabled and manually-disabled
// channels together with their abilities, returning the deleted count.
func DeleteDisabledChannels() (int64, error) {
	var count int64
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := channelLockForUpdate(tx).Where("status = ? OR status = ?",
			channelcatalog.ChannelStatusAutoDisabled, channelcatalog.ChannelStatusManuallyDisabled).
			Select("id").Find(&channels).Error; err != nil {
			return err
		}
		ids := make([]int, 0, len(channels))
		for _, ch := range channels {
			ids = append(ids, ch.Id)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", ids).Delete(&model.Channel{})
		if res.Error != nil {
			return res.Error
		}
		count = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete disabled channels and abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return count, fmt.Errorf("refresh ability cache after disabled-channel delete: %w", err)
	}
	return count, nil
}

// BatchDeleteChannels hard-deletes channels and their abilities in one
// transaction, returning the deleted channel count.
func BatchDeleteChannels(ids []int) (int64, error) {
	var err error
	ids, err = normalizeChannelBatchIDs(ids)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", ids).Delete(&model.Channel{})
		if res.Error != nil {
			return res.Error
		}
		count = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete channel batch and abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return count, fmt.Errorf("refresh ability cache after channel batch delete: %w", err)
	}
	return count, nil
}

// BatchSetChannelTag updates the tag of the given channels and mirrors the
// tag onto their ability rows.
func BatchSetChannelTag(ids []int, tag *string) error {
	var err error
	ids, err = normalizeChannelBatchIDs(ids)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	tagValue := ""
	if tag != nil {
		tagValue = *tag
	}
	if !validChannelTag(tagValue, true) {
		return ErrInvalidChannelTag
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Channel{}).Where("id IN ?", ids).
			Update("tag", tagValue).Error; err != nil {
			return err
		}
		return tx.Model(&model.Ability{}).Where("channel_id IN ?", ids).
			Update("tag", tagValue).Error
	})
	if err != nil {
		return fmt.Errorf("update channel and ability tags: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after batch tag update: %w", err)
	}
	return nil
}

// RebuildChannelAbilities deletes a channel's ability rows and recreates them
// from its model/group lists (reference AddAbilities semantics).
func RebuildChannelAbilities(ch *model.Channel) error {
	if ch == nil || ch.Id <= 0 {
		return errors.New("channel is missing")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return rebuildChannelAbilitiesTx(tx, ch)
	})
}

func rebuildChannelAbilitiesTx(tx *gorm.DB, ch *model.Channel) error {
	if err := tx.Where("channel_id = ?", ch.Id).Delete(&model.Ability{}).Error; err != nil {
		return err
	}
	abilities := buildChannelAbilities(ch)
	if len(abilities) == 0 {
		return nil
	}
	return tx.Create(&abilities).Error
}

func buildChannelAbilities(ch *model.Channel) []model.Ability {
	models := splitCSV(ch.Models)
	groups := splitCSV(ch.Group)
	if len(groups) == 0 {
		groups = []string{userssvc.GroupDefault}
	}
	weight := uint(channelcatalog.DefaultChannelWeight)
	if ch.Weight != nil {
		weight = *ch.Weight
	}
	seen := make(map[string]struct{})
	abilities := make([]model.Ability, 0, len(models)*len(groups))
	for _, m := range models {
		for _, g := range groups {
			key := g + "|" + m
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			abilities = append(abilities, model.Ability{
				Group:     g,
				Model:     m,
				ChannelId: ch.Id,
				Enabled:   ch.Status == channelcatalog.ChannelStatusEnabled,
				Priority:  ch.Priority,
				Weight:    weight,
				Tag:       optionalAbilityTag(ch.Tag),
			})
		}
	}
	return abilities
}

func optionalAbilityTag(tag string) *string {
	if tag == "" {
		return nil
	}
	return &tag
}

func splitCSV(s string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// FixAbilities clears the abilities table and rebuilds it from every channel
// (reference FixAbility contract). Concurrent runs are rejected.
func FixAbilities() (success, fails int, err error) {
	if !fixAbilityLock.TryLock() {
		return 0, 0, errors.New("已经有一个修复任务在运行中，请稍后再试")
	}
	defer fixAbilityLock.Unlock()

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := tx.Find(&channels).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM abilities").Error; err != nil {
			return err
		}
		for i := range channels {
			abilities := buildChannelAbilities(&channels[i])
			if len(abilities) > 0 {
				if err := tx.Create(&abilities).Error; err != nil {
					fails = 1
					return fmt.Errorf("rebuild channel %d abilities: %w", channels[i].Id, err)
				}
			}
			success++
		}
		return nil
	})
	if err != nil {
		return 0, fails, fmt.Errorf("fix abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return success, 0, fmt.Errorf("refresh ability cache after ability rebuild: %w", err)
	}
	return success, 0, nil
}

// CopyChannel clones a channel with its key. The clone gets the suffix on its
// name and, unless resetBalance is false, zeroed balance/used quota.
func CopyChannel(id int, suffix string, resetBalance bool) (*model.Channel, error) {
	if err := validateChannelText("copy suffix", suffix, maxChannelCopySuffixBytes, true, false); err != nil {
		return nil, err
	}
	origin, err := GetChannelByID(id)
	if err != nil {
		return nil, errors.New("获取渠道信息失败，请稍后重试")
	}
	clone := *origin
	clone.Id = 0
	clone.CreatedTime = wallclock.NowTimestamp()
	clone.Name = origin.Name + suffix
	if err := validateChannelText("name", clone.Name, maxChannelNameBytes, false, false); err != nil {
		return nil, err
	}
	clone.TestTime = 0
	clone.ResponseTime = 0
	if resetBalance {
		clone.Balance = 0
		clone.UsedQuota = 0
	}
	if err := CreateChannelWithAbilities(&clone); err != nil {
		return nil, fmt.Errorf("复制渠道失败，请稍后重试: %w", err)
	}
	return &clone, nil
}

// channelUpstreamHTTPClient dials upstreams through the SSRF guard.
const maxChannelUpstreamResponseBytes int64 = 8 << 20

var channelUpstreamHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: httpx.SafeDialContext,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// fetchUpstreamModelsResponse is the OpenAI models-list wire shape.
type fetchUpstreamModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// FetchUpstreamModelsForChannel fetches the model-id list from a channel's
// upstream models endpoint: Gemini uses /v1beta/models, Anthropic adds the
// anthropic-version header, everything else uses /v1/models.
func FetchUpstreamModelsForChannel(ch *model.Channel) ([]string, error) {
	return FetchUpstreamModelsForChannelContext(context.Background(), ch)
}

// FetchUpstreamModelsForChannelContext is the cancellable form used by
// durable maintenance tasks and request-scoped model-update detection. The
// legacy wrapper above remains for callers that do not already own a context.
func FetchUpstreamModelsForChannelContext(ctx context.Context, ch *model.Channel) ([]string, error) {
	if ch == nil {
		return nil, errors.New("channel is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	channelType := channelcatalog.ChannelType(ch.Type)
	if !channelSupportsUpstreamModelDiscovery(channelType) {
		return nil, fmt.Errorf("%s channel model discovery is not supported", channelcatalog.ChannelTypeName(channelType))
	}
	base := strings.TrimSuffix(strings.TrimSpace(ch.BaseURL), "/")
	if base == "" && ch.Type >= 0 && ch.Type < len(channelcatalog.ChannelBaseURLs) {
		base = strings.TrimSuffix(channelcatalog.ChannelBaseURLs[ch.Type], "/")
	}
	if base == "" {
		return nil, errors.New("channel has no upstream base URL")
	}
	if channelType == channelcatalog.ChannelTypeCodex {
		return fetchCodexUpstreamModels(ctx, ch, base)
	}
	if channelType == channelcatalog.ChannelTypeOllama {
		return ollama.FetchModels(ctx, base, GetChannelKey(ch))
	}
	credential := GetChannelKey(ch)
	plan, err := buildChannelModelDiscoveryPlan(ctx, ch, base, credential)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plan.requestURL, nil)
	if err != nil {
		return nil, errors.New("invalid upstream model endpoint")
	}
	req.Header = plan.headers.Clone()
	req.Host = plan.host
	resp, err := channelUpstreamHTTPClient.Do(req)
	if err != nil {
		return nil, sanitizeUpstreamDiscoveryError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream returned status %s", resp.Status)
	}
	body, err := httpx.ReadAllLimited(resp.Body, maxChannelUpstreamResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read upstream models response: %w", err)
	}
	return parseChannelModelDiscoveryResponse(body, plan)
}

func normalizeChannelModelIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
}

func firstKey(key string) string {
	return strings.TrimSpace(strings.Split(key, "\n")[0])
}

// openAISubscriptionResponse is the OpenAI dashboard subscription payload.
type openAISubscriptionResponse struct {
	HasPaymentMethod *bool    `json:"has_payment_method"`
	HardLimitUSD     *float64 `json:"hard_limit_usd"`
}

// openAIUsageResponse is the OpenAI dashboard usage payload (cents).
type openAIUsageResponse struct {
	TotalUsage *float64 `json:"total_usage"`
}

// FetchChannelBalance fetches and persists the upstream balance for a channel.
// The cancellable implementation lives in channel_balance.go; this wrapper is
// retained for background maintenance callers.
func FetchChannelBalance(ch *model.Channel) (float64, error) {
	return FetchChannelBalanceContext(context.Background(), ch)
}

func balanceGET(url, key string) ([]byte, error) {
	return balanceGETContext(context.Background(), url, key)
}

var ErrChannelBalanceSweepRunning = errors.New("channel balance refresh is already running")

// UpdateAllChannelsBalances walks enabled, single-key channels in bounded
// pages. The background wrapper is used by scheduled maintenance.
func UpdateAllChannelsBalances() error {
	return UpdateAllChannelsBalancesContext(context.Background())
}

// UpdateAllChannelsBalancesContext is the request-cancellable form used by
// the dashboard endpoint. Provider failures remain isolated to their channel;
// cancellation and database failures stop the sweep.
func UpdateAllChannelsBalancesContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !channelBalanceSweepLock.TryLock() {
		return ErrChannelBalanceSweepRunning
	}
	defer channelBalanceSweepLock.Unlock()
	pollingInterval := channelBalancePollingInterval()

	const pageSize = 100
	lastID := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var channels []model.Channel
		if err := model.DB.WithContext(ctx).
			Where("status = ? AND id > ?", channelcatalog.ChannelStatusEnabled, lastID).
			Order("id ASC").Limit(pageSize).Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		for i := range channels {
			ch := &channels[i]
			lastID = ch.Id
			if err := ctx.Err(); err != nil {
				return err
			}
			if IsMultiKeyChannel(ch) {
				continue
			}
			balance, err := FetchChannelBalanceContext(ctx, ch)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				logging.SysError(fmt.Sprintf("refresh channel %d balance failed: %v", ch.Id, err))
			} else if balance <= 0 && (ch.AutoBan == nil || *ch.AutoBan != 0) {
				changed, err := UpdateChannelStatusChecked(ch.Id, channelcatalog.ChannelStatusAutoDisabled, "余额不足")
				if err != nil {
					logging.SysError(fmt.Sprintf("disable channel %d after balance refresh failed: %v", ch.Id, err))
				} else if changed {
					if err := notifyRootChannelStatus(ch.Id, ch.Name, channelcatalog.ChannelStatusAutoDisabled, "余额不足"); err != nil {
						logging.SysError(fmt.Sprintf("notify root about channel %d balance status failed", ch.Id))
					}
				}
			}
			if err := waitForChannelBalancePoll(ctx, pollingInterval); err != nil {
				return err
			}
		}
		if len(channels) < pageSize {
			return nil
		}
	}
}

func channelBalancePollingInterval() time.Duration {
	seconds := env.GetEnvInt("POLLING_INTERVAL", 0)
	if seconds <= 0 {
		return 0
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func waitForChannelBalancePoll(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// --- multi-key channel info (stored as JSON in channel_info) ---

type multiKeyChannelInfo struct {
	IsMultiKey             bool           `json:"is_multi_key"`
	MultiKeySize           int            `json:"multi_key_size"`
	MultiKeyStatusList     map[int]int    `json:"multi_key_status_list"`
	MultiKeyDisabledReason map[int]string `json:"multi_key_disabled_reason,omitempty"`
	MultiKeyDisabledTime   map[int]int64  `json:"multi_key_disabled_time,omitempty"`
	MultiKeyPollingIndex   int            `json:"multi_key_polling_index"`
}

// parseOtherInfo decodes the channel's other_info JSON object.
func parseOtherInfo(raw string) map[string]any {
	info := map[string]any{}
	if raw != "" {
		_ = jsonutil.UnmarshalJsonStr(raw, &info)
	}
	return info
}

// IsMultiKeyChannel reports whether the channel is in multi-key mode.
func IsMultiKeyChannel(ch *model.Channel) bool {
	info := parseChannelInfo(ch)
	return info.IsMultiKey
}

func parseChannelInfo(ch *model.Channel) multiKeyChannelInfo {
	var info multiKeyChannelInfo
	if ch.ChannelInfo != "" {
		_ = jsonutil.UnmarshalJsonStr(ch.ChannelInfo, &info)
	}
	return info
}

// channelKeys returns the channel's key list (newline-separated in multi-key
// mode).
func channelKeys(ch *model.Channel) []string {
	keys := strings.Split(ch.Key, "\n")
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func keyStatusOf(info multiKeyChannelInfo, index int) int {
	if info.MultiKeyStatusList != nil {
		if s, ok := info.MultiKeyStatusList[index]; ok {
			return s
		}
	}
	return 1
}

// keyStatusCounts returns enabled/manualDisabled/autoDisabled counts.
func keyStatusCounts(info multiKeyChannelInfo, size int) (int, int, int) {
	enabled, manual, auto := 0, 0, 0
	for i := 0; i < size; i++ {
		switch keyStatusOf(info, i) {
		case 1:
			enabled++
		case 2:
			manual++
		case 3:
			auto++
		}
	}
	return enabled, manual, auto
}

// MultiKeyStatus holds one key's status for the management response.
type MultiKeyStatus struct {
	Index        int    `json:"index"`
	Status       int    `json:"status"`
	DisabledTime int64  `json:"disabled_time,omitempty"`
	Reason       string `json:"reason,omitempty"`
	KeyPreview   string `json:"key_preview"`
}

func multiKeyFingerprint(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(digest[:4])
}

// MultiKeyStatusResponse is the get_key_status payload.
type MultiKeyStatusResponse struct {
	Keys                []MultiKeyStatus `json:"keys"`
	Total               int              `json:"total"`
	Page                int              `json:"page"`
	PageSize            int              `json:"page_size"`
	TotalPages          int              `json:"total_pages"`
	EnabledCount        int              `json:"enabled_count"`
	ManualDisabledCount int              `json:"manual_disabled_count"`
	AutoDisabledCount   int              `json:"auto_disabled_count"`
}

// GetMultiKeyStatus returns the paginated key-status view for a multi-key
// channel.
func GetMultiKeyStatus(ch *model.Channel, page, pageSize int, statusFilter *int) (*MultiKeyStatusResponse, error) {
	info := parseChannelInfo(ch)
	keys := channelKeys(ch)
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	if page > 1_000_000 {
		page = 1_000_000
	}
	enabledCount, manualCount, autoCount := keyStatusCounts(info, len(keys))

	filtered := make([]MultiKeyStatus, 0, len(keys))
	for i, key := range keys {
		status := keyStatusOf(info, i)
		if statusFilter != nil && status != *statusFilter {
			continue
		}
		entry := MultiKeyStatus{Index: i, Status: status, KeyPreview: multiKeyFingerprint(key)}
		if status != 1 {
			if info.MultiKeyDisabledTime != nil {
				entry.DisabledTime = info.MultiKeyDisabledTime[i]
			}
			if info.MultiKeyDisabledReason != nil {
				entry.Reason = info.MultiKeyDisabledReason[i]
			}
		}
		filtered = append(filtered, entry)
	}

	totalPages := (len(filtered) + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	pageKeys := []MultiKeyStatus{}
	if start < len(filtered) {
		pageKeys = filtered[start:end]
	}
	return &MultiKeyStatusResponse{
		Keys:                pageKeys,
		Total:               len(filtered),
		Page:                page,
		PageSize:            pageSize,
		TotalPages:          totalPages,
		EnabledCount:        enabledCount,
		ManualDisabledCount: manualCount,
		AutoDisabledCount:   autoCount,
	}, nil
}

// SetMultiKeyStatus marks one key of a multi-key channel as enabled/disabled
// (manual disable = 2; enabling removes the state entries).
func SetMultiKeyStatus(ch *model.Channel, keyIndex, status int) error {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		current, info, keys, err := loadMultiKeyForUpdate(tx, ch.Id)
		if err != nil {
			return err
		}
		if keyIndex < 0 || keyIndex >= len(keys) {
			return errors.New("密钥索引超出范围")
		}
		ensureMultiKeyMaps(&info)
		if status == 1 {
			delete(info.MultiKeyStatusList, keyIndex)
			delete(info.MultiKeyDisabledTime, keyIndex)
			delete(info.MultiKeyDisabledReason, keyIndex)
		} else {
			info.MultiKeyStatusList[keyIndex] = status
		}
		return tx.Model(current).Update("channel_info", mustMarshal(info)).Error
	})
}

// SetAllMultiKeyStatuses enables or disables every key (manual disable = 2);
// returns the number of keys whose state changed.
func SetAllMultiKeyStatuses(ch *model.Channel, status int) (int, error) {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()
	changed := 0
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		current, info, keys, err := loadMultiKeyForUpdate(tx, ch.Id)
		if err != nil {
			return err
		}
		ensureMultiKeyMaps(&info)
		for i := range keys {
			if status == 1 {
				if keyStatusOf(info, i) != 1 {
					delete(info.MultiKeyStatusList, i)
					delete(info.MultiKeyDisabledTime, i)
					delete(info.MultiKeyDisabledReason, i)
					changed++
				}
			} else if keyStatusOf(info, i) == 1 {
				info.MultiKeyStatusList[i] = status
				changed++
			}
		}
		if changed == 0 {
			return nil
		}
		return tx.Model(current).Update("channel_info", mustMarshal(info)).Error
	})
	return changed, err
}

// DeleteMultiKey removes one key from a multi-key channel, reindexing state.
func DeleteMultiKey(ch *model.Channel, keyIndex int) error {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		current, info, keys, err := loadMultiKeyForUpdate(tx, ch.Id)
		if err != nil {
			return err
		}
		if keyIndex < 0 || keyIndex >= len(keys) {
			return errors.New("密钥索引超出范围")
		}
		if len(keys) <= 1 {
			return errors.New("不能删除最后一个密钥")
		}
		remaining, newStatus, newReason, newTime := removeMultiKeys(info, keys, func(i int) bool {
			return i == keyIndex
		})
		info.MultiKeySize = len(remaining)
		info.MultiKeyStatusList = newStatus
		info.MultiKeyDisabledReason = newReason
		info.MultiKeyDisabledTime = newTime
		return tx.Model(current).Updates(map[string]any{
			"key": strings.Join(remaining, "\n"), "channel_info": mustMarshal(info),
		}).Error
	})
}

// DeleteAutoDisabledMultiKeys removes keys auto-disabled (status 3) from a
// multi-key channel; returns the removed count.
func DeleteAutoDisabledMultiKeys(ch *model.Channel) (int, error) {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()
	deleted := 0
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		current, info, keys, err := loadMultiKeyForUpdate(tx, ch.Id)
		if err != nil {
			return err
		}
		for i := range keys {
			if keyStatusOf(info, i) == 3 {
				deleted++
			}
		}
		if deleted == 0 {
			return nil
		}
		if deleted == len(keys) {
			return errors.New("不能删除所有密钥")
		}
		remaining, newStatus, newReason, newTime := removeMultiKeys(info, keys, func(i int) bool {
			return keyStatusOf(info, i) == 3
		})
		info.MultiKeySize = len(remaining)
		info.MultiKeyStatusList = newStatus
		info.MultiKeyDisabledReason = newReason
		info.MultiKeyDisabledTime = newTime
		return tx.Model(current).Updates(map[string]any{
			"key": strings.Join(remaining, "\n"), "channel_info": mustMarshal(info),
		}).Error
	})
	return deleted, err
}

func ensureMultiKeyMaps(info *multiKeyChannelInfo) {
	if info.MultiKeyStatusList == nil {
		info.MultiKeyStatusList = map[int]int{}
	}
	if info.MultiKeyDisabledTime == nil {
		info.MultiKeyDisabledTime = map[int]int64{}
	}
	if info.MultiKeyDisabledReason == nil {
		info.MultiKeyDisabledReason = map[int]string{}
	}
}

func loadMultiKeyForUpdate(tx *gorm.DB, channelID int) (*model.Channel, multiKeyChannelInfo, []string, error) {
	var current model.Channel
	if err := channelLockForUpdate(tx).First(&current, channelID).Error; err != nil {
		return nil, multiKeyChannelInfo{}, nil, err
	}
	info := parseChannelInfo(&current)
	if !info.IsMultiKey {
		return nil, multiKeyChannelInfo{}, nil, errors.New("该渠道不是多密钥模式")
	}
	keys := channelKeys(&current)
	if len(keys) == 0 {
		return nil, multiKeyChannelInfo{}, nil, errors.New("多密钥渠道没有可用密钥")
	}
	return &current, info, keys, nil
}

func removeMultiKeys(info multiKeyChannelInfo, keys []string, remove func(int) bool) ([]string, map[int]int, map[int]string, map[int]int64) {
	remaining := make([]string, 0, len(keys))
	newStatus := map[int]int{}
	newReason := map[int]string{}
	newTime := map[int]int64{}
	newIndex := 0
	for i, key := range keys {
		if remove(i) {
			continue
		}
		remaining = append(remaining, key)
		if status := keyStatusOf(info, i); status != 1 {
			newStatus[newIndex] = status
			if info.MultiKeyDisabledReason != nil {
				newReason[newIndex] = info.MultiKeyDisabledReason[i]
			}
			if info.MultiKeyDisabledTime != nil {
				newTime[newIndex] = info.MultiKeyDisabledTime[i]
			}
		}
		newIndex++
	}
	return remaining, newStatus, newReason, newTime
}

func mustMarshal(v any) string {
	b, err := jsonutil.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
