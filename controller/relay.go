package controller

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/relay"
	"github.com/tokenrouter/tokenrouter/service"
)

// Relay handler wrappers dispatch to the shared relay engine.
func RelayChatCompletions(c *gin.Context)     { relay.Relay(c) }
func RelayCompletions(c *gin.Context)         { relay.Relay(c) }
func RelayEmbeddings(c *gin.Context)          { relay.Relay(c) }
func RelayModerations(c *gin.Context)         { relay.Relay(c) }
func RelayImageGenerations(c *gin.Context)    { relay.Relay(c) }
func RelayImageEdits(c *gin.Context)          { relay.Relay(c) }
func RelayAudioSpeech(c *gin.Context)         { relay.Relay(c) }
func RelayAudioTranscription(c *gin.Context)  { relay.Relay(c) }
func RelayAudioTranslation(c *gin.Context)    { relay.Relay(c) }
func RelayResponses(c *gin.Context)           { relay.Relay(c) }
func RelayRerank(c *gin.Context)              { relay.Relay(c) }
func RelayRealtime(c *gin.Context)            { relay.RelayWebSocket(c) }

// Task/async platform placeholders follow the reference placeholder behavior:
// they accept the request and return a structured "not configured" result
// rather than pretending to be fully implemented.
func RelaySunoSubmit(c *gin.Context)    { taskNotConfigured(c, "suno") }
func RelaySunoFetch(c *gin.Context)     { taskNotConfigured(c, "suno") }
func RelayVideoSubmit(c *gin.Context)   { taskNotConfigured(c, "video") }
func RelayVideoFetch(c *gin.Context)    { taskNotConfigured(c, "video") }
func RelayMidjourneyImagine(c *gin.Context) { taskNotConfigured(c, "midjourney") }
func RelayMidjourneyFetch(c *gin.Context)   { taskNotConfigured(c, "midjourney") }

func taskNotConfigured(c *gin.Context, platform string) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"success": false,
		"message": "任务平台未配置: " + platform,
	})
}

// RelayListModels returns the models available to the token's group in the
// OpenAI-compatible /v1/models shape.
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

	data := make([]gin.H, 0, len(names))
	for _, n := range names {
		data = append(data, gin.H{"id": n, "object": "model", "owned_by": "tokenrouter"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
