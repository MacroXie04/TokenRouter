package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// tokenAutoGroupsInput distinguishes "field absent" from "explicit null/[]"
// in update payloads.
type tokenAutoGroupsInput struct {
	Set    bool
	Groups []string
}

func (input *tokenAutoGroupsInput) UnmarshalJSON(data []byte) error {
	input.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		input.Groups = nil
		return nil
	}
	return common.Unmarshal(data, &input.Groups)
}

// tokenGroupInput preserves an existing group when an update omits the field.
type tokenGroupInput struct {
	Set   bool
	Value string
}

func (input *tokenGroupInput) UnmarshalJSON(data []byte) error {
	input.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		input.Value = ""
		return nil
	}
	return common.Unmarshal(data, &input.Value)
}

// tokenRequest is the create/update payload.
type tokenRequest struct {
	model.Token
	Group      tokenGroupInput      `json:"group"`
	AutoGroups tokenAutoGroupsInput `json:"auto_groups"`
}

// tokenResponse is a masked token with parsed auto-groups.
type tokenResponse struct {
	*model.Token
	AutoGroups []string `json:"auto_groups"`
}

// buildMaskedTokenResponse masks the key and normalizes auto-groups.
func buildMaskedTokenResponse(token *model.Token) *tokenResponse {
	if token == nil {
		return nil
	}
	masked := *token
	masked.Key = token.GetMaskedKey()
	autoGroups, err := token.GetAutoGroups()
	if err != nil {
		common.Logger.Warn("failed to parse token auto groups", "id", token.Id, "err", err.Error())
		autoGroups = nil
	}
	if len(autoGroups) == 0 {
		autoGroups = nil
	}
	return &tokenResponse{Token: &masked, AutoGroups: autoGroups}
}

func buildMaskedTokenResponses(tokens []*model.Token) []*tokenResponse {
	out := make([]*tokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, buildMaskedTokenResponse(t))
	}
	return out
}

// pageInfo is the reference paging response shape.
type pageInfo struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
	Items    any `json:"items"`
}

const maxPageNumber = 1_000_000

// getPageQuery parses the reference paging query and compatibility aliases.
func getPageQuery(c *gin.Context) *pageInfo {
	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	if page < 1 {
		page = 1
	}
	if page > maxPageNumber {
		page = maxPageNumber
	}
	size := 0
	for _, key := range []string{"page_size", "ps", "size"} {
		parsed, err := strconv.Atoi(c.Query(key))
		if err == nil && parsed != 0 {
			size = parsed
			break
		}
	}
	if size < 1 {
		size = 10
	}
	if size > 100 {
		size = 100
	}
	return &pageInfo{Page: page, PageSize: size}
}

func (p *pageInfo) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// validateTokenRequest enforces the shared create/update constraints.
func validateTokenRequest(c *gin.Context, token *model.Token) bool {
	if len(token.Name) > 50 {
		c.JSON(http.StatusBadRequest, dto.Fail("令牌名称不能超过50个字符"))
		return false
	}
	if !token.UnlimitedQuota {
		if token.RemainQuota < 0 {
			c.JSON(http.StatusBadRequest, dto.Fail("令牌额度不能为负数"))
			return false
		}
		if int64(token.RemainQuota) > common.MaxQuota {
			c.JSON(http.StatusBadRequest, dto.Fail("令牌额度超过最大限制"))
			return false
		}
	}
	return true
}

// setTokenAutoGroups validates and stores auto-groups for group "auto" tokens.
func setTokenAutoGroups(c *gin.Context, token *model.Token, userGroup string, groups []string) bool {
	maxCount := setting.GetMaxTokenAutoGroups()
	if err := service.ValidateUserAutoGroups(userGroup, groups, maxCount); err != nil {
		message := "无权使用自动分组"
		switch {
		case errors.Is(err, service.ErrTooManyAutoGroups):
			message = "自动分组数量超过限制"
		case errors.Is(err, service.ErrDuplicateAutoGroup):
			message = "自动分组不能重复"
		case errors.Is(err, service.ErrInvalidAutoGroup):
			message = "自动分组名称无效"
		}
		c.JSON(http.StatusBadRequest, dto.Fail(message))
		return false
	}
	if err := token.SetAutoGroups(groups); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("自动分组解析失败"))
		return false
	}
	return true
}

// GetAllTokens lists the signed-in user's tokens (paged, masked keys).
func GetAllTokens(c *gin.Context) {
	userId := common.GetUserId(c)
	page := getPageQuery(c)
	tokens, err := service.GetAllUserTokens(userId, page.Offset(), page.PageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	total, err := service.CountUserTokens(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	page.Total = int(total)
	page.Items = buildMaskedTokenResponses(tokens)
	c.JSON(http.StatusOK, dto.Ok(page))
}

// SearchTokens searches the signed-in user's tokens by name and/or key.
func SearchTokens(c *gin.Context) {
	userId := common.GetUserId(c)
	keyword := c.Query("keyword")
	tokenKey := c.Query("token")
	page := getPageQuery(c)
	tokens, total, err := service.SearchUserTokens(userId, keyword, tokenKey, page.Offset(), page.PageSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	page.Total = int(total)
	page.Items = buildMaskedTokenResponses(tokens)
	c.JSON(http.StatusOK, dto.Ok(page))
}

// GetTokenAutoGroups returns the auto-group candidates for the user's group
// plus the per-token auto-groups limit.
func GetTokenAutoGroups(c *gin.Context) {
	userId := common.GetUserId(c)
	user, err := service.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("获取用户分组失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"groups":    service.GetUserDefaultAutoGroups(user.Group),
		"max_count": setting.GetMaxTokenAutoGroups(),
	}))
}

// GetToken returns one of the user's tokens (masked key).
func GetToken(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的令牌 ID"))
		return
	}
	token, err := service.GetTokenByIds(id, common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("令牌不存在"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(buildMaskedTokenResponse(token)))
}

// GetTokenKey discloses the full key of one of the user's tokens.
func GetTokenKey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的令牌 ID"))
		return
	}
	token, err := service.GetTokenByIds(id, common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("令牌不存在"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"key": token.GetFullKey()}))
}

// AddToken creates a relay token for the signed-in user.
func AddToken(c *gin.Context) {
	var request tokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	token := &request.Token
	if !validateTokenRequest(c, token) {
		return
	}
	userId := common.GetUserId(c)
	maxTokens := setting.GetMaxUserTokens()
	user, err := service.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("获取用户分组失败"))
		return
	}
	userGroup := user.Group
	if userGroup == "" {
		userGroup = service.GroupDefault
	}
	if request.Group.Set {
		token.Group = request.Group.Value
	} else {
		// Empty is the persisted inheritance marker: relay authentication
		// resolves it against the user's current group on every request.
		token.Group = ""
	}
	if token.Group == service.GroupAuto {
		if !setTokenAutoGroups(c, token, userGroup, request.AutoGroups.Groups) {
			return
		}
	} else {
		selectedGroup := token.Group
		if !request.Group.Set {
			selectedGroup = userGroup
		}
		if !service.IsUserSelectableGroup(userGroup, selectedGroup) {
			c.JSON(http.StatusBadRequest, dto.Fail("无权使用该分组"))
			return
		}
		token.CrossGroupRetry = false
		_ = token.SetAutoGroups(nil)
	}
	key, err := common.SecureRandomAlphanumeric(48)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("创建失败"))
		return
	}
	clean := model.Token{
		UserId:             userId,
		Name:               token.Name,
		Key:                "sk-" + key,
		Status:             service.TokenStatusEnabled,
		CreatedTime:        common.NowTimestamp(),
		AccessedTime:       common.NowTimestamp(),
		ExpiredTime:        token.ExpiredTime,
		RemainQuota:        token.RemainQuota,
		UnlimitedQuota:     token.UnlimitedQuota,
		ModelLimitsEnabled: token.ModelLimitsEnabled,
		ModelLimits:        token.ModelLimits,
		AllowIps:           token.AllowIps,
		Group:              token.Group,
		CrossGroupRetry:    token.CrossGroupRetry,
		AutoGroups:         token.AutoGroups,
	}
	if clean.UnlimitedQuota {
		clean.RemainQuota = -1
	}
	if err := service.CreateUserTokenWithinLimit(&clean, maxTokens); err != nil {
		if errors.Is(err, service.ErrUserTokenLimitReached) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "已达到最大令牌数量限制 (" + common.Int2Str(maxTokens) + ")",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("创建失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// UpdateToken updates one of the user's tokens. With ?status_only=1 only the
// status is changed (the reference contract), and expired/exhausted tokens
// cannot be re-enabled.
func UpdateToken(c *gin.Context) {
	userId := common.GetUserId(c)
	statusOnly := c.Query("status_only")
	var request tokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	token := &request.Token
	if !validateTokenRequest(c, token) {
		return
	}
	clean, err := service.GetTokenByIds(token.Id, userId)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("令牌不存在"))
		return
	}
	if token.Status == service.TokenStatusEnabled {
		if clean.Status == service.TokenStatusExpired &&
			clean.ExpiredTime <= common.NowTimestamp() && clean.ExpiredTime != -1 {
			c.JSON(http.StatusBadRequest, dto.Fail("已过期的令牌无法重新启用"))
			return
		}
		if clean.Status == service.TokenStatusExhausted &&
			clean.RemainQuota <= 0 && !clean.UnlimitedQuota {
			c.JSON(http.StatusBadRequest, dto.Fail("已用尽的令牌无法重新启用"))
			return
		}
	}
	if statusOnly != "" {
		clean.Status = token.Status
	} else {
		user, err := service.GetUserByID(userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("获取用户分组失败"))
			return
		}
		userGroup := user.Group
		if userGroup == "" {
			userGroup = service.GroupDefault
		}
		if request.Group.Set {
			if request.Group.Value != service.GroupAuto &&
				!service.IsUserSelectableGroup(userGroup, request.Group.Value) {
				c.JSON(http.StatusBadRequest, dto.Fail("无权使用该分组"))
				return
			}
			clean.Group = request.Group.Value
		}
		clean.Name = token.Name
		clean.ExpiredTime = token.ExpiredTime
		clean.RemainQuota = token.RemainQuota
		clean.UnlimitedQuota = token.UnlimitedQuota
		clean.ModelLimitsEnabled = token.ModelLimitsEnabled
		clean.ModelLimits = token.ModelLimits
		clean.AllowIps = token.AllowIps
		clean.CrossGroupRetry = token.CrossGroupRetry
		if clean.UnlimitedQuota {
			clean.RemainQuota = -1
		}
		if clean.Group != service.GroupAuto {
			clean.CrossGroupRetry = false
			_ = clean.SetAutoGroups(nil)
		} else if request.AutoGroups.Set {
			if !setTokenAutoGroups(c, clean, userGroup, request.AutoGroups.Groups) {
				return
			}
		}
	}
	if err := model.DB.Save(clean).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("更新失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    buildMaskedTokenResponse(clean),
	})
}

// DeleteToken soft-deletes one of the user's tokens.
func DeleteToken(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.DeleteTokenById(id, common.GetUserId(c)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, dto.Fail("令牌不存在"))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("删除失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// tokenBatch is the ids payload for batch operations.
type tokenBatch struct {
	Ids []int `json:"ids"`
}

func validTokenBatchIDs(ids []int) bool {
	if len(ids) == 0 || len(ids) > 100 {
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

// DeleteTokenBatch soft-deletes several of the user's tokens.
func DeleteTokenBatch(c *gin.Context) {
	var batch tokenBatch
	if err := c.ShouldBindJSON(&batch); err != nil || len(batch.Ids) == 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
		return
	}
	if len(batch.Ids) > 100 {
		c.JSON(http.StatusBadRequest, dto.Fail("批量操作最多支持100个"))
		return
	}
	if !validTokenBatchIDs(batch.Ids) {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
		return
	}
	count, err := service.BatchDeleteTokens(batch.Ids, common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("删除失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": count})
}

// GetTokenKeysBatch discloses full keys for up to 100 of the user's tokens.
func GetTokenKeysBatch(c *gin.Context) {
	var batch tokenBatch
	if err := c.ShouldBindJSON(&batch); err != nil || len(batch.Ids) == 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
		return
	}
	if len(batch.Ids) > 100 {
		c.JSON(http.StatusBadRequest, dto.Fail("批量操作最多支持100个"))
		return
	}
	if !validTokenBatchIDs(batch.Ids) {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数"))
		return
	}
	tokens, err := service.GetTokenKeysByIds(batch.Ids, common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询失败"))
		return
	}
	keysMap := make(map[int]string)
	for _, t := range tokens {
		keysMap[t.Id] = t.GetFullKey()
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"keys": keysMap}))
}

// GetTokenUsage reports the usage/status of the relay token in the
// Authorization header (reference token_usage contract).
func GetTokenUsage(c *gin.Context) {
	tokenAny, ok := c.Get(common.ContextKeyToken)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "令牌无效"})
		return
	}
	token, ok := tokenAny.(*model.Token)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "令牌无效"})
		return
	}
	expiredAt := token.ExpiredTime
	if expiredAt == -1 {
		expiredAt = 0
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    true,
		"message": "ok",
		"data": gin.H{
			"object":               "token_usage",
			"name":                 token.Name,
			"total_granted":        token.RemainQuota + token.UsedQuota,
			"total_used":           token.UsedQuota,
			"total_available":      token.RemainQuota,
			"unlimited_quota":      token.UnlimitedQuota,
			"model_limits":         token.GetModelLimitsMap(),
			"model_limits_enabled": token.ModelLimitsEnabled,
			"expires_at":           expiredAt,
		},
	})
}
