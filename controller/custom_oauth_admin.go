package controller

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// CustomOAuthProviderResponse is the admin-facing provider shape. It excludes
// the client secret (write-only) and timestamps.
type CustomOAuthProviderResponse struct {
	Id                    int    `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Icon                  string `json:"icon"`
	Enabled               bool   `json:"enabled"`
	ClientId              string `json:"client_id"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"user_info_endpoint"`
	Scopes                string `json:"scopes"`
	UserIdField           string `json:"user_id_field"`
	UsernameField         string `json:"username_field"`
	DisplayNameField      string `json:"display_name_field"`
	EmailField            string `json:"email_field"`
	WellKnown             string `json:"well_known"`
	AuthStyle             int    `json:"auth_style"`
	AccessPolicy          string `json:"access_policy"`
	AccessDeniedMessage   string `json:"access_denied_message"`
}

func toCustomOAuthProviderResponse(p *model.CustomOAuthProvider) *CustomOAuthProviderResponse {
	return &CustomOAuthProviderResponse{
		Id:                    p.Id,
		Name:                  p.Name,
		Slug:                  p.Slug,
		Icon:                  p.Icon,
		Enabled:               p.Enabled,
		ClientId:              p.ClientId,
		AuthorizationEndpoint: p.AuthorizationEndpoint,
		TokenEndpoint:         p.TokenEndpoint,
		UserInfoEndpoint:      p.UserInfoEndpoint,
		Scopes:                p.Scopes,
		UserIdField:           p.UserIdField,
		UsernameField:         p.UsernameField,
		DisplayNameField:      p.DisplayNameField,
		EmailField:            p.EmailField,
		WellKnown:             p.WellKnown,
		AuthStyle:             p.AuthStyle,
		AccessPolicy:          p.AccessPolicy,
		AccessDeniedMessage:   p.AccessDeniedMessage,
	}
}

// GetCustomOAuthProviders lists all custom OAuth providers (GET
// /api/custom-oauth-provider/, root only).
func GetCustomOAuthProviders(c *gin.Context) {
	providers, err := model.GetAllCustomOAuthProviders()
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	response := make([]*CustomOAuthProviderResponse, len(providers))
	for i, p := range providers {
		response[i] = toCustomOAuthProviderResponse(p)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": response})
}

// GetCustomOAuthProvider returns one provider by id (GET
// /api/custom-oauth-provider/:id, root only).
func GetCustomOAuthProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的 ID"))
		return
	}
	provider, err := model.GetCustomOAuthProviderById(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("未找到该 OAuth 提供商"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": toCustomOAuthProviderResponse(provider)})
}

// CreateCustomOAuthProviderRequest carries a full provider definition; the
// secret is required on create (unlike update, where empty keeps the old one).
type CreateCustomOAuthProviderRequest struct {
	Name                  string `json:"name" binding:"required"`
	Slug                  string `json:"slug" binding:"required"`
	Icon                  string `json:"icon"`
	Enabled               bool   `json:"enabled"`
	ClientId              string `json:"client_id" binding:"required"`
	ClientSecret          string `json:"client_secret" binding:"required"`
	AuthorizationEndpoint string `json:"authorization_endpoint" binding:"required"`
	TokenEndpoint         string `json:"token_endpoint" binding:"required"`
	UserInfoEndpoint      string `json:"user_info_endpoint" binding:"required"`
	Scopes                string `json:"scopes"`
	UserIdField           string `json:"user_id_field"`
	UsernameField         string `json:"username_field"`
	DisplayNameField      string `json:"display_name_field"`
	EmailField            string `json:"email_field"`
	WellKnown             string `json:"well_known"`
	AuthStyle             int    `json:"auth_style"`
	AccessPolicy          string `json:"access_policy"`
	AccessDeniedMessage   string `json:"access_denied_message"`
}

// CreateCustomOAuthProvider creates a provider (POST
// /api/custom-oauth-provider/, root only).
func CreateCustomOAuthProvider(c *gin.Context) {
	var req CreateCustomOAuthProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数: "+err.Error()))
		return
	}
	if model.IsCustomOAuthSlugTaken(req.Slug, 0) {
		c.JSON(http.StatusBadRequest, dto.Fail("该 Slug 已被使用"))
		return
	}
	if service.IsBuiltInOAuthProvider(strings.ToLower(req.Slug)) {
		c.JSON(http.StatusBadRequest, dto.Fail("该 Slug 与内置 OAuth 提供商冲突"))
		return
	}
	provider := &model.CustomOAuthProvider{
		Name:                  req.Name,
		Slug:                  req.Slug,
		Icon:                  req.Icon,
		Enabled:               req.Enabled,
		ClientId:              req.ClientId,
		ClientSecret:          req.ClientSecret,
		AuthorizationEndpoint: req.AuthorizationEndpoint,
		TokenEndpoint:         req.TokenEndpoint,
		UserInfoEndpoint:      req.UserInfoEndpoint,
		Scopes:                req.Scopes,
		UserIdField:           req.UserIdField,
		UsernameField:         req.UsernameField,
		DisplayNameField:      req.DisplayNameField,
		EmailField:            req.EmailField,
		WellKnown:             req.WellKnown,
		AuthStyle:             req.AuthStyle,
		AccessPolicy:          req.AccessPolicy,
		AccessDeniedMessage:   req.AccessDeniedMessage,
	}
	if err := model.CreateCustomOAuthProvider(provider); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "创建成功", "data": toCustomOAuthProviderResponse(provider)})
}

// UpdateCustomOAuthProviderRequest carries a partial update: strings update
// when non-empty; pointer fields update when present (so false/"" can be set).
type UpdateCustomOAuthProviderRequest struct {
	Name                  string  `json:"name"`
	Slug                  string  `json:"slug"`
	Icon                  *string `json:"icon"`
	Enabled               *bool   `json:"enabled"`
	ClientId              string  `json:"client_id"`
	ClientSecret          string  `json:"client_secret"`
	AuthorizationEndpoint string  `json:"authorization_endpoint"`
	TokenEndpoint         string  `json:"token_endpoint"`
	UserInfoEndpoint      string  `json:"user_info_endpoint"`
	Scopes                string  `json:"scopes"`
	UserIdField           string  `json:"user_id_field"`
	UsernameField         string  `json:"username_field"`
	DisplayNameField      string  `json:"display_name_field"`
	EmailField            string  `json:"email_field"`
	WellKnown             *string `json:"well_known"`
	AuthStyle             *int    `json:"auth_style"`
	AccessPolicy          *string `json:"access_policy"`
	AccessDeniedMessage   *string `json:"access_denied_message"`
}

// UpdateCustomOAuthProvider updates a provider (PUT
// /api/custom-oauth-provider/:id, root only).
func UpdateCustomOAuthProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的 ID"))
		return
	}
	var req UpdateCustomOAuthProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数: "+err.Error()))
		return
	}
	provider, err := model.GetCustomOAuthProviderById(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("未找到该 OAuth 提供商"))
		return
	}
	if req.Slug != "" && req.Slug != provider.Slug {
		if model.IsCustomOAuthSlugTaken(req.Slug, id) {
			c.JSON(http.StatusBadRequest, dto.Fail("该 Slug 已被使用"))
			return
		}
		if service.IsBuiltInOAuthProvider(strings.ToLower(req.Slug)) {
			c.JSON(http.StatusBadRequest, dto.Fail("该 Slug 与内置 OAuth 提供商冲突"))
			return
		}
	}
	if req.Name != "" {
		provider.Name = req.Name
	}
	if req.Slug != "" {
		provider.Slug = req.Slug
	}
	if req.Icon != nil {
		provider.Icon = *req.Icon
	}
	if req.Enabled != nil {
		provider.Enabled = *req.Enabled
	}
	if req.ClientId != "" {
		provider.ClientId = req.ClientId
	}
	if req.ClientSecret != "" {
		provider.ClientSecret = req.ClientSecret
	}
	if req.AuthorizationEndpoint != "" {
		provider.AuthorizationEndpoint = req.AuthorizationEndpoint
	}
	if req.TokenEndpoint != "" {
		provider.TokenEndpoint = req.TokenEndpoint
	}
	if req.UserInfoEndpoint != "" {
		provider.UserInfoEndpoint = req.UserInfoEndpoint
	}
	if req.Scopes != "" {
		provider.Scopes = req.Scopes
	}
	if req.UserIdField != "" {
		provider.UserIdField = req.UserIdField
	}
	if req.UsernameField != "" {
		provider.UsernameField = req.UsernameField
	}
	if req.DisplayNameField != "" {
		provider.DisplayNameField = req.DisplayNameField
	}
	if req.EmailField != "" {
		provider.EmailField = req.EmailField
	}
	if req.WellKnown != nil {
		provider.WellKnown = *req.WellKnown
	}
	if req.AuthStyle != nil {
		provider.AuthStyle = *req.AuthStyle
	}
	if req.AccessPolicy != nil {
		provider.AccessPolicy = *req.AccessPolicy
	}
	if req.AccessDeniedMessage != nil {
		provider.AccessDeniedMessage = *req.AccessDeniedMessage
	}
	if err := model.UpdateCustomOAuthProvider(provider); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "更新成功", "data": toCustomOAuthProviderResponse(provider)})
}

// DeleteCustomOAuthProvider deletes a provider (DELETE
// /api/custom-oauth-provider/:id, root only). Deletion is blocked while any
// user binding references the provider.
func DeleteCustomOAuthProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的 ID"))
		return
	}
	if _, err := model.GetCustomOAuthProviderById(id); err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("未找到该 OAuth 提供商"))
		return
	}
	count, err := model.GetBindingCountByProviderId(id)
	if err != nil {
		common.SysError("failed to get binding count for provider " + strconv.Itoa(id) + ": " + err.Error())
		c.JSON(http.StatusInternalServerError, dto.Fail("检查用户绑定时发生错误，请稍后重试"))
		return
	}
	if count > 0 {
		c.JSON(http.StatusBadRequest, dto.Fail("该 OAuth 提供商还有用户绑定，无法删除。请先解除所有用户绑定。"))
		return
	}
	if err := model.DeleteCustomOAuthProvider(id); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "删除成功"})
}

// FetchCustomOAuthDiscoveryRequest addresses an OIDC discovery document either
// directly or via the issuer base URL.
type FetchCustomOAuthDiscoveryRequest struct {
	WellKnownURL string `json:"well_known_url"`
	IssuerURL    string `json:"issuer_url"`
}

// customOAuthDiscoveryClient fetches operator-supplied discovery URLs, so it
// dials through the SSRF guard (the reference uses a plain client here).
var customOAuthDiscoveryClient = &http.Client{
	Timeout:   20 * time.Second,
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: common.SafeDialContext},
}

// FetchCustomOAuthDiscovery fetches an OIDC discovery document server-side
// (POST /api/custom-oauth-provider/discovery, root only) so the dashboard can
// prefill endpoint fields.
func FetchCustomOAuthDiscovery(c *gin.Context) {
	var req FetchCustomOAuthDiscoveryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的请求参数: "+err.Error()))
		return
	}

	wellKnownURL := strings.TrimSpace(req.WellKnownURL)
	issuerURL := strings.TrimSpace(req.IssuerURL)
	if wellKnownURL == "" && issuerURL == "" {
		c.JSON(http.StatusBadRequest, dto.Fail("请先填写 Discovery URL 或 Issuer URL"))
		return
	}

	targetURL := wellKnownURL
	if targetURL == "" {
		targetURL = strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	}
	targetURL = strings.TrimSpace(targetURL)

	parsedURL, err := url.Parse(targetURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		c.JSON(http.StatusBadRequest, dto.Fail("Discovery URL 无效，仅支持 http/https"))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("创建 Discovery 请求失败: "+err.Error()))
		return
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := customOAuthDiscoveryClient.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("获取 Discovery 配置失败: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		c.JSON(http.StatusBadRequest, dto.Fail("获取 Discovery 配置失败: "+message))
		return
	}

	var discovery map[string]any
	if err := common.DecodeJson(resp.Body, &discovery); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("解析 Discovery 配置失败: "+err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"well_known_url": targetURL,
			"discovery":      discovery,
		},
	})
}

// GetUserOAuthBindingsByAdmin lists a target user's custom-provider bindings
// (GET /api/user/:id/oauth/bindings, admin). Role hierarchy applies.
func GetUserOAuthBindingsByAdmin(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("invalid user id"))
		return
	}
	target, err := service.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	if !canManageTargetRole(common.GetRole(c), target.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("no permission"))
		return
	}
	bindings, err := service.ListOAuthBindings(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": bindings})
}

// UnbindCustomOAuthByAdmin removes one custom-provider binding from a target
// user (DELETE /api/user/:id/oauth/bindings/:provider_id, admin). Role
// hierarchy applies; unbinding a non-bound provider succeeds (reference
// semantics).
func UnbindCustomOAuthByAdmin(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("invalid user id"))
		return
	}
	target, err := service.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	if !canManageTargetRole(common.GetRole(c), target.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("no permission"))
		return
	}
	providerId, err := strconv.Atoi(c.Param("provider_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("invalid provider id"))
		return
	}
	if err := model.DeleteUserOAuthBinding(userId, providerId); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"user.oauth_unbind id="+common.Int2Str(userId)+" provider="+common.Int2Str(providerId))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "success"})
}
