package relay

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/engine"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	"github.com/tokenrouter/tokenrouter/internal/relay/tasks"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxPlaygroundRequestBodyBytes int64 = 16 << 20

// Relay handler wrappers dispatch to the shared relay engine.
func RelayChatCompletions(c *gin.Context)    { relayOpenAI(c) }
func RelayCompletions(c *gin.Context)        { relayOpenAI(c) }
func RelayEmbeddings(c *gin.Context)         { relayOpenAI(c) }
func RelayModerations(c *gin.Context)        { relayOpenAI(c) }
func RelayImageGenerations(c *gin.Context)   { relayOpenAI(c) }
func RelayImageEdits(c *gin.Context)         { relayOpenAI(c) }
func RelayEdits(c *gin.Context)              { relayOpenAI(c) }
func RelayAudioSpeech(c *gin.Context)        { relayOpenAI(c) }
func RelayAudioTranscription(c *gin.Context) { relayOpenAI(c) }
func RelayAudioTranslation(c *gin.Context)   { relayOpenAI(c) }
func RelayResponses(c *gin.Context)          { relayOpenAI(c) }
func RelayResponsesCompact(c *gin.Context)   { relayOpenAI(c) }
func RelayAlphaSearch(c *gin.Context)        { relayOpenAI(c) }
func RelayEnginesEmbeddings(c *gin.Context)  { relayOpenAI(c) }
func RelayRerank(c *gin.Context)             { relayOpenAI(c) }
func RelayRealtime(c *gin.Context)           { engine.RelayWebSocket(c, middleware.CaptureRelayRequestState(c)) }
func RelayClaudeMessages(c *gin.Context) {
	engine.RelayClaudeMessages(c, middleware.CaptureRelayRequestState(c))
}
func RelayGeminiNative(c *gin.Context) {
	engine.RelayGeminiNative(c, middleware.CaptureRelayRequestState(c))
}
func RelayJimeng(c *gin.Context) {
	request, ok := middleware.GetJimengRequest(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "invalid_request", "message": "Jimeng request context is missing", "data": nil})
		return
	}
	state := middleware.CaptureRelayRequestState(c)
	if request.Action == jimeng.FetchAction {
		tasks.RelayJimengFetch(c, state, request.Request)
		return
	}
	tasks.RelayJimengSubmit(c, state, request.Request, request.RawBody)
}
func RelayKlingTask(c *gin.Context) { tasks.RelayKlingTask(c, middleware.CaptureRelayRequestState(c)) }
func RelayKlingTaskFetch(c *gin.Context) {
	tasks.RelayKlingTaskFetch(c, middleware.CaptureRelayRequestState(c))
}
func RelayTask(c *gin.Context) { tasks.RelayVideoTask(c, middleware.CaptureRelayRequestState(c)) }
func RelayTaskFetch(c *gin.Context) {
	tasks.RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
}
func VideoProxy(c *gin.Context) { tasks.VideoProxy(c, middleware.CaptureRelayRequestState(c)) }

func playgroundRequestGroup(c *gin.Context) (string, error) {
	body, err := httpx.ReadAllLimited(c.Request.Body, maxPlaygroundRequestBodyBytes)
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var request struct {
		Group string `json:"group"`
	}
	if err := jsonutil.Unmarshal(body, &request); err != nil {
		return "", err
	}
	return request.Group, nil
}

func playgroundGroupAllowed(userGroup, requestedGroup string) bool {
	if requestedGroup == "" {
		requestedGroup = userGroup
	}
	return billingsvc.IsUserSelectableGroup(userGroup, requestedGroup)
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

	userID := requestctx.GetUserId(c)
	user, err := userssvc.GetUserByID(userID)
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
		group = userssvc.GroupDefault
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
	relayOpenAI(c)
}

func writePlaygroundRequestError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	message := "无效的 Playground 请求: " + err.Error()
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		status = http.StatusRequestEntityTooLarge
		message = "Playground 请求体过大"
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    "new_api_error",
		"code":    "",
	}})
}

func RelaySunoSubmit(c *gin.Context) { tasks.RelaySunoTask(c, middleware.CaptureRelayRequestState(c)) }
func RelaySunoFetch(c *gin.Context) {
	tasks.RelaySunoTaskFetch(c, middleware.CaptureRelayRequestState(c))
}
func RelayVideoSubmit(c *gin.Context) {
	tasks.RelayVideoTask(c, middleware.CaptureRelayRequestState(c))
}
func RelayVideoFetch(c *gin.Context) {
	tasks.RelayVideoTaskFetch(c, middleware.CaptureRelayRequestState(c))
}
func RelayMidjourney(c *gin.Context) {
	tasks.RelayMidjourney(c, middleware.CaptureRelayRequestState(c))
}
func RelayMidjourneyImagine(c *gin.Context) {
	tasks.RelayMidjourney(c, middleware.CaptureRelayRequestState(c))
}
func RelayMidjourneyFetch(c *gin.Context) {
	tasks.RelayMidjourney(c, middleware.CaptureRelayRequestState(c))
}

// RelayListModels returns the models available to the token's group in the
// OpenAI-compatible /v1/models shape. Gemini clients (x-goog-api-key header or
// a `key` query parameter) receive the Gemini model-list shape instead,
// mirroring the reference's per-flavor dispatch.
func RelayListModels(c *gin.Context) {
	groups := middleware.GetTokenGroups(c)
	models := middleware.FilterRelayModels(c, billingsvc.GetGroupsModels(groups))
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
	supportedEndpointTypes, err := channelssvc.GetModelSupportedEndpointTypes(groups, models)
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
	models := middleware.FilterRelayModels(c, billingsvc.GetGroupsModels(middleware.GetTokenGroups(c)))
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
	models, err := relayModelsAllowedByPricingPreference(c, billingsvc.GetGroupsModels(groups))
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
	supportedEndpointTypes, err := channelssvc.GetModelSupportedEndpointTypes(groups, map[string]bool{modelId: true})
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
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
	if err != nil {
		return nil, err
	}
	return billingsvc.FilterModelsByReferencePricing(
		models, userssvc.UserSettingsFromRaw(user.Setting).AcceptUnsetRatioModel,
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
