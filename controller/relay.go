package controller

import (
	"bytes"
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

func playgroundRequestGroup(c *gin.Context) (string, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16*1024*1024))
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
	if requestedGroup == "" || requestedGroup == userGroup {
		return true
	}
	for _, group := range service.GetUserAutoGroups(userGroup) {
		if group == requestedGroup {
			return true
		}
	}
	return false
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
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "无效的 Playground 请求: " + err.Error(),
			"type":    "new_api_error",
			"code":    "",
		}})
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

// Task/async platform placeholders follow the reference placeholder behavior:
// they accept the request and return a structured "not configured" result
// rather than pretending to be fully implemented.
func RelaySunoSubmit(c *gin.Context)        { taskNotConfigured(c, "suno") }
func RelaySunoFetch(c *gin.Context)         { taskNotConfigured(c, "suno") }
func RelayVideoSubmit(c *gin.Context)       { taskNotConfigured(c, "video") }
func RelayVideoFetch(c *gin.Context)        { taskNotConfigured(c, "video") }
func RelayMidjourneyImagine(c *gin.Context) { taskNotConfigured(c, "midjourney") }
func RelayMidjourneyFetch(c *gin.Context)   { taskNotConfigured(c, "midjourney") }

func taskNotConfigured(c *gin.Context, platform string) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"success": false,
		"message": "任务平台未配置: " + platform,
	})
}

// RelayListModels returns the models available to the token's group in the
// OpenAI-compatible /v1/models shape. Gemini clients (x-goog-api-key header or
// a `key` query parameter) receive the Gemini model-list shape instead,
// mirroring the reference's per-flavor dispatch.
func RelayListModels(c *gin.Context) {
	group := middleware.GetTokenGroup(c)
	if group == "" {
		group = service.GroupDefault
	}
	models := service.GetGroupModels(group)
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
	data := make([]gin.H, 0, len(names))
	for _, n := range names {
		data = append(data, gin.H{"id": n, "object": "model", "owned_by": "tokenrouter"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// RelayListModelsGemini serves GET /v1beta/models in the Gemini model-list
// shape.
func RelayListModelsGemini(c *gin.Context) {
	group := middleware.GetTokenGroup(c)
	if group == "" {
		group = service.GroupDefault
	}
	models := service.GetGroupModels(group)
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
	group := middleware.GetTokenGroup(c)
	if group == "" {
		group = service.GroupDefault
	}
	models := service.GetGroupModels(group)
	if _, ok := models[modelId]; !ok {
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
	c.JSON(http.StatusOK, gin.H{"id": modelId, "object": "model", "owned_by": "tokenrouter"})
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
