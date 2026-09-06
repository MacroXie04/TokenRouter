package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay"
	"github.com/tokenrouter/tokenrouter/service"
)

const maxPlaygroundRequestBodyBytes int64 = 16 << 20

// Relay handler wrappers dispatch to the shared relay engine.
func RelayChatCompletions(c *gin.Context)    { relay.Relay(c) }
func RelayCompletions(c *gin.Context)        { relay.Relay(c) }
func RelayEmbeddings(c *gin.Context)         { relay.Relay(c) }
func RelayModerations(c *gin.Context)        { relay.Relay(c) }
func RelayImageGenerations(c *gin.Context)   { relay.Relay(c) }
func RelayImageEdits(c *gin.Context)         { relay.Relay(c) }
func RelayEdits(c *gin.Context)              { relay.Relay(c) }
func RelayAudioSpeech(c *gin.Context)        { relay.Relay(c) }
func RelayAudioTranscription(c *gin.Context) { relay.Relay(c) }
func RelayAudioTranslation(c *gin.Context)   { relay.Relay(c) }
func RelayResponses(c *gin.Context)          { relay.Relay(c) }
func RelayResponsesCompact(c *gin.Context)   { relay.Relay(c) }
func RelayAlphaSearch(c *gin.Context)        { relay.Relay(c) }
func RelayEnginesEmbeddings(c *gin.Context)  { relay.Relay(c) }
func RelayRerank(c *gin.Context)             { relay.Relay(c) }
func RelayRealtime(c *gin.Context)           { relay.RelayWebSocket(c) }
func RelayClaudeMessages(c *gin.Context)     { relay.RelayClaudeMessages(c) }
func RelayGeminiNative(c *gin.Context)       { relay.RelayGeminiNative(c) }
func RelayJimeng(c *gin.Context)             { relay.RelayJimeng(c) }
func RelayKlingTask(c *gin.Context)          { relay.RelayKlingTask(c) }
func RelayKlingTaskFetch(c *gin.Context)     { relay.RelayKlingTaskFetch(c) }
func RelayTask(c *gin.Context)               { relay.RelayVideoTask(c) }
func RelayTaskFetch(c *gin.Context)          { relay.RelayVideoTaskFetch(c) }
func VideoProxy(c *gin.Context)              { relay.VideoProxy(c) }

func playgroundRequestGroup(c *gin.Context) (string, error) {
	body, err := common.ReadAllLimited(c.Request.Body, maxPlaygroundRequestBodyBytes)
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var request struct {
		Group string `json:"group"`
	}
	if err := common.Unmarshal(body, &request); err != nil {
		return "", err
	}
	return request.Group, nil
}

func playgroundGroupAllowed(userGroup, requestedGroup string) bool {
	if requestedGroup == "" {
		requestedGroup = userGroup
	}
	return service.IsUserSelectableGroup(userGroup, requestedGroup)
}

// Playground delegates a dashboard session request to the ordinary relay
// lifecycle with a non-persistent, quota-unlimited token for token accounting.
func Playground(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": "暂不支持使用 access token",
			"type":    "new_api_error",
			"param":   "",
			"code":    "access_denied",
		}})
		return
	}

	userID := common.GetUserId(c)
	user, err := service.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": err.Error(),
			"type":    "new_api_error",
			"param":   "",
			"code":    "query_data_error",
		}})
		return
	}
	group := user.Group
	if group == "" {
		group = service.GroupDefault
	}
	requestedGroup, err := playgroundRequestGroup(c)
	if err != nil {
		writePlaygroundRequestError(c, err)
		return
	}
	if !playgroundGroupAllowed(group, requestedGroup) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"message": "无权访问该分组",
			"type":    "new_api_error",
			"code":    "",
		}})
		return
	}
	if requestedGroup != "" {
		group = requestedGroup
	}
	middleware.SetupRelayTokenContext(c, &model.Token{
		UserId: userID, Name: fmt.Sprintf("playground-%s", group), Group: group,
		UnlimitedQuota: true,
	})
	relay.Relay(c)
}

func writePlaygroundRequestError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	message := "无效的 Playground 请求: " + err.Error()
	if errors.Is(err, common.ErrBodyTooLarge) {
		status = http.StatusRequestEntityTooLarge
		message = "Playground 请求体过大"
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    "new_api_error",
		"code":    "",
	}})
}

func RelaySunoSubmit(c *gin.Context)        { relay.RelaySunoTask(c) }
func RelaySunoFetch(c *gin.Context)         { relay.RelaySunoTaskFetch(c) }
func RelayVideoSubmit(c *gin.Context)       { relay.RelayVideoTask(c) }
func RelayVideoFetch(c *gin.Context)        { relay.RelayVideoTaskFetch(c) }
func RelayMidjourney(c *gin.Context)        { relay.RelayMidjourney(c) }
func RelayMidjourneyImagine(c *gin.Context) { relay.RelayMidjourney(c) }
func RelayMidjourneyFetch(c *gin.Context)   { relay.RelayMidjourney(c) }

// RelayListModels returns the models available to the token's group in the
// OpenAI-compatible /v1/models shape. Gemini clients (x-goog-api-key header or
// a `key` query parameter) receive the Gemini model-list shape instead,
// mirroring the reference's per-flavor dispatch.
func RelayListModels(c *gin.Context) {
	groups := middleware.GetTokenGroups(c)
	models := middleware.FilterRelayModels(c, service.GetGroupsModels(groups))
	models, err := relayModelsAllowedByPricingPreference(c, models)
	if err != nil {
		writeRelayModelCatalogError(c)
		return
	}
	names := make([]string, 0, len(models))
	for m := range models {
		names = append(names, m)
	}
	sort.Strings(names)

	if c.GetHeader("x-goog-api-key") != "" || c.Query("key") != "" {
		data := make([]gin.H, 0, len(names))
		for _, n := range names {
			data = append(data, gin.H{"name": n, "displayName": n})
		}
		c.JSON(http.StatusOK, gin.H{"models": data, "nextPageToken": nil})
		return
	}
	supportedEndpointTypes, err := service.GetModelSupportedEndpointTypes(groups, models)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": "Unable to build the model endpoint catalog",
			"type":    "new_api_error",
			"param":   "",
			"code":    "endpoint_catalog_unavailable",
		}})
		return
	}
	data := make([]gin.H, 0, len(names))
	for _, n := range names {
		data = append(data, gin.H{
			"id": n, "object": "model", "owned_by": "tokenrouter",
			"supported_endpoint_types": supportedEndpointTypes[n],
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// RelayListModelsGemini serves GET /v1beta/models in the Gemini model-list
// shape.
func RelayListModelsGemini(c *gin.Context) {
	models := middleware.FilterRelayModels(c, service.GetGroupsModels(middleware.GetTokenGroups(c)))
	models, err := relayModelsAllowedByPricingPreference(c, models)
	if err != nil {
		writeRelayModelCatalogError(c)
		return
	}
	names := make([]string, 0, len(models))
	for m := range models {
		names = append(names, m)
	}
	sort.Strings(names)

	data := make([]gin.H, 0, len(names))
	for _, n := range names {
		data = append(data, gin.H{"name": n, "displayName": n})
	}
	c.JSON(http.StatusOK, gin.H{"models": data, "nextPageToken": nil})
}

// RelayRetrieveModel serves GET /v1/models/:model: the model entry when the
// group has access, otherwise a model_not_found error (matching the
// reference's catalog-based behavior). Anthropic-shaped for Claude clients.
func RelayRetrieveModel(c *gin.Context) {
	modelId := c.Param("model")
	groups := middleware.GetTokenGroups(c)
	models, err := relayModelsAllowedByPricingPreference(c, service.GetGroupsModels(groups))
	if err != nil {
		writeRelayModelCatalogError(c)
		return
	}
	if _, ok := models[modelId]; !ok || !middleware.RelayModelAllowed(c, modelId) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"message": "The model '" + modelId + "' does not exist",
			"type":    "invalid_request_error",
			"param":   "model",
			"code":    "model_not_found",
		}})
		return
	}
	if c.GetHeader("x-api-key") != "" && c.GetHeader("anthropic-version") != "" {
		c.JSON(http.StatusOK, gin.H{
			"id":           modelId,
			"display_name": modelId,
			"created_at":   time.Now().UTC().Format(time.RFC3339),
			"type":         "model",
		})
		return
	}
	supportedEndpointTypes, err := service.GetModelSupportedEndpointTypes(groups, map[string]bool{modelId: true})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": "Unable to build the model endpoint catalog",
			"type":    "new_api_error",
			"param":   "",
			"code":    "endpoint_catalog_unavailable",
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": modelId, "object": "model", "owned_by": "tokenrouter",
		"supported_endpoint_types": supportedEndpointTypes[modelId],
	})
}

func relayModelsAllowedByPricingPreference(c *gin.Context, models map[string]bool) (map[string]bool, error) {
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		return nil, err
	}
	return service.FilterModelsByReferencePricing(
		models, service.UserSettingsFromRaw(user.Setting).AcceptUnsetRatioModel,
	)
}

func writeRelayModelCatalogError(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
		"message": "Unable to build the model catalog",
		"type":    "new_api_error",
		"param":   "",
		"code":    "model_catalog_unavailable",
	}})
}

// RelayNotImplemented mirrors the reference's unimplemented relay endpoints
// (Files, fine-tunes, image variations, model deletion): a structured 501.
func RelayNotImplemented(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{
		"message": "API not implemented",
		"type":    "new_api_error",
		"param":   "",
		"code":    "api_not_implemented",
	}})
}

// RelayNotFound mirrors the reference's fallback for unknown /v1, /api, and
// /assets paths: a structured 404 in the OpenAI error shape.
func RelayNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
		"message": "Invalid URL (" + c.Request.Method + " " + c.Request.URL.Path + ")",
		"type":    "invalid_request_error",
		"param":   "",
		"code":    "",
	}})
}

// RelayNotFoundRoute is the NoRoute handler for relay/dashboard/api prefixes;
// other paths fall through to the web SPA fallback installed by the binary.
func RelayNotFoundRoute(c *gin.Context) {
	p := c.Request.URL.Path
	if strings.HasPrefix(p, "/v1") || strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/assets") {
		RelayNotFound(c)
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}
