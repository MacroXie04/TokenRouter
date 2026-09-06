package service

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Token status constants.
const (
	TokenStatusEnabled   = 1
	TokenStatusDisabled  = 2
	TokenStatusExpired   = 3
	TokenStatusExhausted = 4
)

// ErrTokenNotFound is returned when a token key does not resolve.
var ErrTokenNotFound = errors.New("token not found")

// ErrTokenDisabled is returned when a token is not usable.
var ErrTokenDisabled = errors.New("token disabled")

// ErrTokenExpired is returned when a token has expired.
var ErrTokenExpired = errors.New("token expired")

// ErrInsufficientTokenQuota is returned when an atomic task reservation cannot
// be made without overspending the token.
var ErrInsufficientTokenQuota = errors.New("insufficient token quota")

// ErrTokenQuotaOverflow is returned when a token credit or usage increment
// would exceed the persisted quota range.
var ErrTokenQuotaOverflow = errors.New("token quota overflow")

// ErrUserTokenLimitReached is returned when a new token would exceed the
// operator's per-user token limit.
var ErrUserTokenLimitReached = errors.New("user token limit reached")

// TokenByKey loads an enabled token by its key.
func TokenByKey(key string) (*model.Token, error) {
	if key == "" {
		return nil, ErrTokenNotFound
	}
	var token model.Token
	if err := model.DB.Where("key = ?", key).First(&token).Error; err != nil {
		return nil, ErrTokenNotFound
	}
	return &token, nil
}

// GetTokenByID loads a token by id.
func GetTokenByID(id int) (*model.Token, error) {
	var token model.Token
	if err := model.DB.First(&token, id).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// CheckTokenUsable validates status and expiry.
func CheckTokenUsable(token *model.Token) error {
	if token.Status != TokenStatusEnabled {
		return ErrTokenDisabled
	}
	if token.ExpiredTime > 0 && token.ExpiredTime < common.NowTimestamp() {
		return ErrTokenExpired
	}
	if !token.UnlimitedQuota && token.RemainQuota <= 0 {
		return errors.New("token quota exhausted")
	}
	return nil
}

// IncreaseTokenUsedQuota increments a token's used quota.
func IncreaseTokenUsedQuota(id int, quota int) error {
	return DecreaseTokenQuota(id, quota)
}

// ReserveTokenQuota atomically removes quota from a limited token before an
// asynchronous upstream operation begins. The reservation is committed to
// used_quota only after the provider accepts the task.
func ReserveTokenQuota(id, quota int) error {
	if id <= 0 {
		return ErrTokenNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	result := model.DB.Model(&model.Token{}).
		Where("id = ? AND remain_quota >= ? AND remain_quota <= ?", id, quota, common.MaxQuota).
		UpdateColumn("remain_quota", gormExpr("remain_quota - ?", quota))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := model.DB.Model(&model.Token{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrTokenNotFound
		}
		return ErrInsufficientTokenQuota
	}
	return nil
}

// RefundTokenQuotaReservation releases a failed asynchronous reservation.
func RefundTokenQuotaReservation(id, quota int) error {
	if id <= 0 {
		return ErrTokenNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := subscriptionLockForUpdate(tx).Unscoped().Select("id", "remain_quota").First(&token, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTokenNotFound
			}
			return err
		}
		newRemainQuota, ok := common.AddQuotaWithinBounds(token.RemainQuota, quota)
		if !ok {
			return ErrTokenQuotaOverflow
		}
		result := tx.Unscoped().Model(&model.Token{}).
			Where("id = ? AND remain_quota = ?", id, token.RemainQuota).
			UpdateColumn("remain_quota", newRemainQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTokenNotFound
		}
		return nil
	})
}

// CommitTokenQuotaReservation records a successful reservation as used quota;
// remain_quota was already deducted atomically by ReserveTokenQuota.
func CommitTokenQuotaReservation(id, quota int) error {
	if id <= 0 {
		return ErrTokenNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := subscriptionLockForUpdate(tx).Unscoped().Select("id", "used_quota").First(&token, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTokenNotFound
			}
			return err
		}
		newUsedQuota, ok := common.AddQuotaWithinBounds(token.UsedQuota, quota)
		if !ok {
			return ErrTokenQuotaOverflow
		}
		result := tx.Unscoped().Model(&model.Token{}).
			Where("id = ? AND used_quota = ?", id, token.UsedQuota).
			UpdateColumn("used_quota", newUsedQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTokenNotFound
		}
		return nil
	})
}

// DecreaseTokenQuota atomically deducts from remain_quota and adds to used_quota.
func DecreaseTokenQuota(id int, quota int) error {
	if id <= 0 {
		return ErrTokenNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := subscriptionLockForUpdate(tx).Unscoped().
			Select("id", "remain_quota", "used_quota").First(&token, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTokenNotFound
			}
			return err
		}
		if !common.QuotaWithinBounds(token.RemainQuota) {
			return ErrTokenQuotaOverflow
		}
		if token.RemainQuota < quota {
			return ErrInsufficientTokenQuota
		}
		newUsedQuota, ok := common.AddQuotaWithinBounds(token.UsedQuota, quota)
		if !ok {
			return ErrTokenQuotaOverflow
		}
		result := tx.Unscoped().Model(&model.Token{}).
			Where("id = ? AND remain_quota = ? AND used_quota = ?", id, token.RemainQuota, token.UsedQuota).
			Updates(map[string]any{
				"remain_quota": token.RemainQuota - quota,
				"used_quota":   newUsedQuota,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTokenNotFound
		}
		return nil
	})
}

// UpdateTokenAccessedTime updates last-access timestamp.
func UpdateTokenAccessedTime(id int, ts int64) error {
	return model.DB.Model(&model.Token{}).Where("id = ?", id).
		UpdateColumn("accessed_time", ts).Error
}

// GetTokenByIds loads a token scoped to its owner.
func GetTokenByIds(id, userId int) (*model.Token, error) {
	if id == 0 || userId == 0 {
		return nil, errors.New("id 或 userId 为空！")
	}
	var token model.Token
	if err := model.DB.Where("id = ? AND user_id = ?", id, userId).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// GetAllUserTokens pages a user's tokens, newest first.
func GetAllUserTokens(userId, offset, limit int) ([]*model.Token, error) {
	var tokens []*model.Token
	err := model.DB.Where("user_id = ?", userId).Order("id desc").
		Limit(limit).Offset(offset).Find(&tokens).Error
	return tokens, err
}

// CountUserTokens counts a user's tokens.
func CountUserTokens(userId int) (int64, error) {
	var total int64
	err := model.DB.Model(&model.Token{}).Where("user_id = ?", userId).Count(&total).Error
	return total, err
}

// CreateUserTokenWithinLimit serializes token creation on the owning user and
// checks the configured limit in the same transaction as the insert. This
// prevents concurrent dashboard requests from racing past the limit.
func CreateUserTokenWithinLimit(token *model.Token, limit int) error {
	if token == nil || token.UserId <= 0 || limit < 1 || limit > setting.MaxMaxUserTokens {
		return errors.New("invalid token creation request")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var owner model.User
		if err := subscriptionLockForUpdate(tx).Select("id").First(&owner, token.UserId).Error; err != nil {
			return err
		}
		var boundary model.Token
		err := tx.Model(&model.Token{}).
			Select("id").
			Where("user_id = ?", token.UserId).
			Order("id").
			Offset(limit - 1).
			Limit(1).
			Take(&boundary).Error
		switch {
		case err == nil:
			return ErrUserTokenLimitReached
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		default:
			return tx.Create(token).Error
		}
	})
}

// searchHardLimit caps token-search result pages (reference contract).
const searchHardLimit = 100

// sanitizeLikePattern escapes LIKE wildcards with the reference's "!" escape
// character and rejects unsafe patterns.
func sanitizeLikePattern(input string) (string, error) {
	input = strings.ReplaceAll(input, "!", "!!")
	input = strings.ReplaceAll(input, "_", "!_")
	if err := validateLikePattern(input); err != nil {
		return "", err
	}
	return input, nil
}

// validateLikePattern enforces the reference search rules: no doubled %,
// at most two %, and a fuzzy keyword must be at least 2 characters.
func validateLikePattern(input string) error {
	if strings.Contains(input, "%%") {
		return errors.New("搜索模式中不允许包含连续的 % 通配符")
	}
	if count := strings.Count(input, "%"); count > 2 {
		return errors.New("搜索模式中最多允许包含 2 个 % 通配符")
	}
	if strings.Contains(input, "%") && len(strings.ReplaceAll(input, "%", "")) < 2 {
		return errors.New("使用模糊搜索时，关键词长度至少为 2 个字符")
	}
	return nil
}

// SearchUserTokens searches a user's tokens by name and/or key with the
// reference LIKE semantics, paged and capped at searchHardLimit.
func SearchUserTokens(userId int, keyword, tokenKey string, offset, limit int) ([]*model.Token, int64, error) {
	if limit <= 0 || limit > searchHardLimit {
		limit = searchHardLimit
	}
	if offset < 0 {
		offset = 0
	}
	// TokenRouter stores keys with the sk- prefix (the reference stores the
	// raw 48-char key), so the query is matched as-is rather than trimmed.
	// Fuzzy search over a token set above the per-user limit is rejected
	// (the reference protects against full-table LIKE scans).
	if strings.Contains(keyword, "%") || strings.Contains(tokenKey, "%") {
		count, err := CountUserTokens(userId)
		if err != nil {
			return nil, 0, errors.New("获取令牌数量失败")
		}
		if int(count) > setting.GetMaxUserTokens() {
			return nil, 0, errors.New("令牌数量超过上限，仅允许精确搜索，请勿使用 % 通配符")
		}
	}
	query := model.DB.Model(&model.Token{}).Where("user_id = ?", userId)
	if keyword != "" {
		pattern, err := sanitizeLikePattern(keyword)
		if err != nil {
			return nil, 0, err
		}
		query = query.Where("name LIKE ? ESCAPE '!'", pattern)
	}
	if tokenKey != "" {
		pattern, err := sanitizeLikePattern(tokenKey)
		if err != nil {
			return nil, 0, err
		}
		query = query.Where("key LIKE ? ESCAPE '!'", pattern)
	}
	var total int64
	if err := query.Limit(setting.GetMaxUserTokens()).Count(&total).Error; err != nil {
		return nil, 0, errors.New("搜索令牌失败")
	}
	var tokens []*model.Token
	if err := query.Order("id desc").Offset(offset).Limit(limit).Find(&tokens).Error; err != nil {
		return nil, 0, errors.New("搜索令牌失败")
	}
	return tokens, total, nil
}

// DeleteTokenById soft-deletes a token scoped to its owner.
func DeleteTokenById(id, userId int) error {
	if id == 0 || userId == 0 {
		return errors.New("id 或 userId 为空！")
	}
	var token model.Token
	if err := model.DB.Where("id = ? AND user_id = ?", id, userId).First(&token).Error; err != nil {
		return err
	}
	return model.DB.Delete(&token).Error
}

// BatchDeleteTokens soft-deletes tokens scoped to their owner and returns the
// deleted count.
func BatchDeleteTokens(ids []int, userId int) (int, error) {
	if !validTokenBatch(ids, userId) {
		return 0, errors.New("invalid token batch")
	}
	res := model.DB.Where("user_id = ? AND id IN ?", userId, ids).Delete(&model.Token{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// GetTokenKeysByIds loads id+key pairs scoped to the owner.
func GetTokenKeysByIds(ids []int, userId int) ([]model.Token, error) {
	if !validTokenBatch(ids, userId) {
		return nil, errors.New("invalid token batch")
	}
	var tokens []model.Token
	err := model.DB.Select("id", "key").
		Where("user_id = ? AND id IN ?", userId, ids).Find(&tokens).Error
	return tokens, err
}

func validTokenBatch(ids []int, userId int) bool {
	if userId <= 0 || len(ids) == 0 || len(ids) > 100 {
		return false
	}
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
