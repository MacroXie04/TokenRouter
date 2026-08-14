package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

// fixAbilityLock prevents concurrent ability-rebuild runs.
var fixAbilityLock sync.Mutex

// channelStatusLock serializes status writes so a multi-key writer cannot
// clobber another's read-modify-write.
var channelStatusLock sync.Mutex

// UpdateChannelStatus moves a channel to the given status and reports whether
// the value actually changed. The ability-enabled flag follows the status,
// and other_info records the reason/time (reference contract).
func UpdateChannelStatus(channelId, status int, reason string) bool {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	channel, err := GetChannelByID(channelId)
	if err != nil || channel.Status == status {
		return false
	}
	channel.Status = status
	info := parseOtherInfo(channel.OtherInfo)
	info["status_reason"] = reason
	info["status_time"] = common.NowTimestamp()
	if b, err := common.Marshal(info); err == nil {
		channel.OtherInfo = string(b)
	}
	if err := model.DB.Model(channel).Updates(map[string]any{
		"status": status, "other_info": channel.OtherInfo,
	}).Error; err != nil {
		return false
	}
	_ = model.DB.Model(&model.Ability{}).Where("channel_id = ?", channelId).
		Update("enabled", status == constant.ChannelStatusEnabled).Error
	_ = SyncAbilityCache()
	return true
}

// EnableChannelsByTag enables every channel carrying the tag (and its
// abilities).
func EnableChannelsByTag(tag string) error {
	if err := model.DB.Model(&model.Channel{}).Where("tag = ?", tag).
		Update("status", constant.ChannelStatusEnabled).Error; err != nil {
		return err
	}
	_ = model.DB.Model(&model.Ability{}).Where("tag = ?", tag).
		Update("enabled", true).Error
	return SyncAbilityCache()
}

// DisableChannelsByTag manually disables every channel carrying the tag (and
// its abilities).
func DisableChannelsByTag(tag string) error {
	if err := model.DB.Model(&model.Channel{}).Where("tag = ?", tag).
		Update("status", constant.ChannelStatusManuallyDisabled).Error; err != nil {
		return err
	}
	_ = model.DB.Model(&model.Ability{}).Where("tag = ?", tag).
		Update("enabled", false).Error
	return SyncAbilityCache()
}

// EditChannelsByTag applies field updates to every channel with the tag. When
// models or groups change, each affected channel's abilities are rebuilt.
func EditChannelsByTag(tag string, newTag, modelMapping, models, groups *string, priority *int64, weight *uint, paramOverride, headerOverride *string) error {
	updates := map[string]any{}
	updatedTag := tag
	if newTag != nil && *newTag != tag {
		updates["tag"] = *newTag
		updatedTag = *newTag
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
	if len(updates) > 0 {
		if err := model.DB.Model(&model.Channel{}).Where("tag = ?", tag).Updates(updates).Error; err != nil {
			return err
		}
	}

	rebuildAbilities := (models != nil && *models != "") || (groups != nil && *groups != "")
	channels, err := GetChannelsByTag(updatedTag)
	if err != nil {
		return err
	}
	for _, ch := range channels {
		if rebuildAbilities {
			_ = RebuildChannelAbilities(ch)
		} else {
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
			if len(abilityUpdates) > 0 {
				_ = model.DB.Model(&model.Ability{}).Where("channel_id = ?", ch.Id).
					Updates(abilityUpdates).Error
			}
		}
	}
	return SyncAbilityCache()
}

// GetChannelsByTag returns all channels with the given tag.
func GetChannelsByTag(tag string) ([]*model.Channel, error) {
	var channels []*model.Channel
	err := model.DB.Where("tag = ?", tag).Find(&channels).Error
	return channels, err
}

// DeleteDisabledChannels hard-deletes auto-disabled and manually-disabled
// channels together with their abilities, returning the deleted count.
func DeleteDisabledChannels() (int64, error) {
	var channels []model.Channel
	if err := model.DB.Where("status = ? OR status = ?",
		constant.ChannelStatusAutoDisabled, constant.ChannelStatusManuallyDisabled).
		Find(&channels).Error; err != nil {
		return 0, err
	}
	var count int64
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		ids := make([]int, 0, len(channels))
		for _, ch := range channels {
			ids = append(ids, ch.Id)
		}
		if len(ids) == 0 {
			return nil
		}
		res := tx.Where("id IN ?", ids).Delete(&model.Channel{})
		if res.Error != nil {
			return res.Error
		}
		count = res.RowsAffected
		return tx.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error
	})
	if err != nil {
		return 0, err
	}
	_ = SyncAbilityCache()
	return count, nil
}

// BatchDeleteChannels hard-deletes channels and their abilities in one
// transaction, returning the deleted channel count.
func BatchDeleteChannels(ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id IN ?", ids).Delete(&model.Channel{})
		if res.Error != nil {
			return res.Error
		}
		count = res.RowsAffected
		return tx.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error
	})
	if err != nil {
		return 0, err
	}
	_ = SyncAbilityCache()
	return count, nil
}

// BatchSetChannelTag updates the tag of the given channels and mirrors the
// tag onto their ability rows.
func BatchSetChannelTag(ids []int, tag *string) error {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Channel{}).Where("id IN ?", ids).
			Update("tag", tag).Error; err != nil {
			return err
		}
		if tag != nil {
			return tx.Model(&model.Ability{}).Where("channel_id IN ?", ids).
				Update("tag", *tag).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	return SyncAbilityCache()
}

// RebuildChannelAbilities deletes a channel's ability rows and recreates them
// from its model/group lists (reference AddAbilities semantics).
func RebuildChannelAbilities(ch *model.Channel) error {
	if err := model.DB.Where("channel_id = ?", ch.Id).Delete(&model.Ability{}).Error; err != nil {
		return err
	}
	abilities := buildChannelAbilities(ch)
	if len(abilities) == 0 {
		return nil
	}
	return model.DB.Create(&abilities).Error
}

func buildChannelAbilities(ch *model.Channel) []model.Ability {
	models := splitCSV(ch.Models)
	groups := splitCSV(ch.Group)
	if len(groups) == 0 {
		groups = []string{GroupDefault}
	}
	weight := uint(constant.DefaultChannelWeight)
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
				Enabled:   ch.Status == constant.ChannelStatusEnabled,
				Priority:  ch.Priority,
				Weight:    weight,
				Tag:       ch.Tag,
			})
		}
	}
	return abilities
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

	if err := model.DB.Exec("DELETE FROM abilities").Error; err != nil {
		return 0, 0, err
	}
	var channels []model.Channel
	if err := model.DB.Find(&channels).Error; err != nil {
		return 0, 0, err
	}
	for i := range channels {
		if err := RebuildChannelAbilities(&channels[i]); err != nil {
			fails++
		} else {
			success++
		}
	}
	_ = SyncAbilityCache()
	return success, fails, nil
}

// CopyChannel clones a channel with its key. The clone gets the suffix on its
// name and, unless resetBalance is false, zeroed balance/used quota.
func CopyChannel(id int, suffix string, resetBalance bool) (*model.Channel, error) {
	origin, err := GetChannelByID(id)
	if err != nil {
		return nil, errors.New("获取渠道信息失败，请稍后重试")
	}
	clone := *origin
	clone.Id = 0
	clone.CreatedTime = common.NowTimestamp()
	clone.Name = origin.Name + suffix
	clone.TestTime = 0
	clone.ResponseTime = 0
	if resetBalance {
		clone.Balance = 0
		clone.UsedQuota = 0
	}
	if err := model.DB.Create(&clone).Error; err != nil {
		return nil, errors.New("复制渠道失败，请稍后重试")
	}
	_ = SyncAbilityCache()
	return &clone, nil
}

// channelUpstreamHTTPClient dials upstreams through the SSRF guard.
var channelUpstreamHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: common.SafeDialContext,
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
	base := strings.TrimSuffix(strings.TrimSpace(ch.BaseURL), "/")
	url := base + "/v1/models"
	headers := map[string]string{}
	if ch.Type == int(constant.ChannelTypeGemini) {
		url = base + "/v1beta/models"
		headers["x-goog-api-key"] = ch.Key
	} else {
		headers["Authorization"] = "Bearer " + firstKey(ch.Key)
		if ch.Type == int(constant.ChannelTypeAnthropic) {
			headers["anthropic-version"] = "2023-06-01"
		}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := channelUpstreamHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream returned status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var parsed fetchUpstreamModelsResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if strings.TrimSpace(item.ID) != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

func firstKey(key string) string {
	return strings.TrimSpace(strings.Split(key, "\n")[0])
}

// openAISubscriptionResponse is the OpenAI dashboard subscription payload.
type openAISubscriptionResponse struct {
	HasPaymentMethod bool    `json:"has_payment_method"`
	HardLimitUSD     float64 `json:"hard_limit_usd"`
}

// openAIUsageResponse is the OpenAI dashboard usage payload (cents).
type openAIUsageResponse struct {
	TotalUsage float64 `json:"total_usage"`
}

// FetchChannelBalance fetches the upstream balance for a channel. The
// OpenAI-shaped path performs the reference's two dashboard calls
// (subscription + usage); other provider types report not-implemented, which
// is the reference's own default branch behavior.
func FetchChannelBalance(ch *model.Channel) (float64, error) {
	base := strings.TrimSuffix(strings.TrimSpace(ch.BaseURL), "/")
	if ch.Type != int(constant.ChannelTypeOpenAI) && ch.Type != int(constant.ChannelTypeCustom) {
		return 0, errors.New("尚未实现")
	}
	if base == "" {
		base = "https://api.openai.com"
	}

	subURL := base + "/v1/dashboard/billing/subscription"
	subBody, err := balanceGET(subURL, ch.Key)
	if err != nil {
		return 0, err
	}
	var subscription openAISubscriptionResponse
	if err := common.Unmarshal(subBody, &subscription); err != nil {
		return 0, err
	}

	now := time.Now()
	startDate := now.Format("2006-01") + "-01"
	endDate := now.Format("2006-01-02")
	if !subscription.HasPaymentMethod {
		startDate = now.AddDate(0, 0, -100).Format("2006-01-02")
	}
	usageURL := fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", base, startDate, endDate)
	usageBody, err := balanceGET(usageURL, ch.Key)
	if err != nil {
		return 0, err
	}
	var usage openAIUsageResponse
	if err := common.Unmarshal(usageBody, &usage); err != nil {
		return 0, err
	}

	balance := subscription.HardLimitUSD - usage.TotalUsage/100
	if err := model.DB.Model(ch).Updates(map[string]any{
		"balance":              balance,
		"balance_updated_time": common.NowTimestamp(),
	}).Error; err != nil {
		return 0, err
	}
	return balance, nil
}

func balanceGET(url, key string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+firstKey(key))
	resp, err := channelUpstreamHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream returned status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// UpdateAllChannelsBalances walks enabled, single-key channels and refreshes
// their balances; a channel whose balance drops to zero or below is disabled
// with the reference reason. Per-channel failures are skipped (reference
// behavior).
func UpdateAllChannelsBalances() error {
	var channels []model.Channel
	if err := model.DB.Where("status = ?", constant.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return err
	}
	for i := range channels {
		ch := &channels[i]
		if ch.ChannelInfo != "" {
			if IsMultiKeyChannel(ch) {
				continue
			}
		}
		balance, err := FetchChannelBalance(ch)
		if err != nil {
			continue
		}
		if balance <= 0 {
			_ = UpdateChannelStatus(ch.Id, constant.ChannelStatusAutoDisabled, "余额不足")
		}
	}
	return nil
}

// --- multi-key channel info (stored as JSON in channel_info) ---

type multiKeyChannelInfo struct {
	IsMultiKey             bool              `json:"is_multi_key"`
	MultiKeySize           int               `json:"multi_key_size"`
	MultiKeyStatusList     map[int]int       `json:"multi_key_status_list"`
	MultiKeyDisabledReason map[int]string    `json:"multi_key_disabled_reason,omitempty"`
	MultiKeyDisabledTime   map[int]int64     `json:"multi_key_disabled_time,omitempty"`
	MultiKeyPollingIndex   int               `json:"multi_key_polling_index"`
}

// parseOtherInfo decodes the channel's other_info JSON object.
func parseOtherInfo(raw string) map[string]any {
	info := map[string]any{}
	if raw != "" {
		_ = common.UnmarshalJsonStr(raw, &info)
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
		_ = common.UnmarshalJsonStr(ch.ChannelInfo, &info)
	}
	return info
}

func (info multiKeyChannelInfo) save(ch *model.Channel) error {
	b, err := common.Marshal(info)
	if err != nil {
		return err
	}
	return model.DB.Model(ch).Update("channel_info", string(b)).Error
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

// MultiKeyStatusResponse is the get_key_status payload.
type MultiKeyStatusResponse struct {
	Keys               []MultiKeyStatus `json:"keys"`
	Total              int              `json:"total"`
	Page               int              `json:"page"`
	PageSize           int              `json:"page_size"`
	TotalPages         int              `json:"total_pages"`
	EnabledCount       int              `json:"enabled_count"`
	ManualDisabledCount int             `json:"manual_disabled_count"`
	AutoDisabledCount  int              `json:"auto_disabled_count"`
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
	enabledCount, manualCount, autoCount := keyStatusCounts(info, len(keys))

	filtered := make([]MultiKeyStatus, 0, len(keys))
	for i, key := range keys {
		status := keyStatusOf(info, i)
		if statusFilter != nil && status != *statusFilter {
			continue
		}
		preview := key
		if len(key) > 10 {
			preview = key[:10] + "..."
		}
		entry := MultiKeyStatus{Index: i, Status: status, KeyPreview: preview}
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
	info := parseChannelInfo(ch)
	keys := channelKeys(ch)
	if keyIndex < 0 || keyIndex >= len(keys) {
		return errors.New("密钥索引超出范围")
	}
	if info.MultiKeyStatusList == nil {
		info.MultiKeyStatusList = map[int]int{}
	}
	if info.MultiKeyDisabledTime == nil {
		info.MultiKeyDisabledTime = map[int]int64{}
	}
	if info.MultiKeyDisabledReason == nil {
		info.MultiKeyDisabledReason = map[int]string{}
	}
	if status == 1 {
		delete(info.MultiKeyStatusList, keyIndex)
		delete(info.MultiKeyDisabledTime, keyIndex)
		delete(info.MultiKeyDisabledReason, keyIndex)
	} else {
		info.MultiKeyStatusList[keyIndex] = status
	}
	return info.save(ch)
}

// SetAllMultiKeyStatuses enables or disables every key (manual disable = 2);
// returns the number of keys whose state changed.
func SetAllMultiKeyStatuses(ch *model.Channel, status int) (int, error) {
	info := parseChannelInfo(ch)
	keys := channelKeys(ch)
	if info.MultiKeyStatusList == nil {
		info.MultiKeyStatusList = map[int]int{}
	}
	if info.MultiKeyDisabledTime == nil {
		info.MultiKeyDisabledTime = map[int]int64{}
	}
	if info.MultiKeyDisabledReason == nil {
		info.MultiKeyDisabledReason = map[int]string{}
	}
	changed := 0
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
		return 0, nil
	}
	return changed, info.save(ch)
}

// DeleteMultiKey removes one key from a multi-key channel, reindexing state.
func DeleteMultiKey(ch *model.Channel, keyIndex int) error {
	info := parseChannelInfo(ch)
	keys := channelKeys(ch)
	if keyIndex < 0 || keyIndex >= len(keys) {
		return errors.New("密钥索引超出范围")
	}
	if len(keys) <= 1 {
		return errors.New("不能删除最后一个密钥")
	}
	remaining := make([]string, 0, len(keys)-1)
	newStatus := map[int]int{}
	newReason := map[int]string{}
	newTime := map[int]int64{}
	newIndex := 0
	for i, key := range keys {
		if i == keyIndex {
			continue
		}
		remaining = append(remaining, key)
		if s := keyStatusOf(info, i); s != 1 {
			newStatus[newIndex] = s
			if info.MultiKeyDisabledReason != nil {
				newReason[newIndex] = info.MultiKeyDisabledReason[i]
			}
			if info.MultiKeyDisabledTime != nil {
				newTime[newIndex] = info.MultiKeyDisabledTime[i]
			}
		}
		newIndex++
	}
	info.MultiKeySize = len(remaining)
	info.MultiKeyStatusList = newStatus
	info.MultiKeyDisabledReason = newReason
	info.MultiKeyDisabledTime = newTime
	return model.DB.Model(ch).Updates(map[string]any{
		"key": strings.Join(remaining, "\n"), "channel_info": mustMarshal(info),
	}).Error
}

// DeleteAutoDisabledMultiKeys removes keys auto-disabled (status 3) from a
// multi-key channel; returns the removed count.
func DeleteAutoDisabledMultiKeys(ch *model.Channel) (int, error) {
	info := parseChannelInfo(ch)
	keys := channelKeys(ch)
	remaining := make([]string, 0, len(keys))
	newStatus := map[int]int{}
	newReason := map[int]string{}
	newTime := map[int]int64{}
	newIndex := 0
	deleted := 0
	for i, key := range keys {
		status := keyStatusOf(info, i)
		if status == 3 {
			deleted++
			continue
		}
		remaining = append(remaining, key)
		if status != 1 {
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
	if deleted == 0 {
		return 0, nil
	}
	info.MultiKeySize = len(remaining)
	info.MultiKeyStatusList = newStatus
	info.MultiKeyDisabledReason = newReason
	info.MultiKeyDisabledTime = newTime
	return deleted, model.DB.Model(ch).Updates(map[string]any{
		"key": strings.Join(remaining, "\n"), "channel_info": mustMarshal(info),
	}).Error
}

func mustMarshal(v any) string {
	b, err := common.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
