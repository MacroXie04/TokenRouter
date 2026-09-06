package router

import (
	"github.com/gin-gonic/gin"
	relayhandlers "github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/relay"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
)

// setupRelayRouter registers the OpenAI-compatible relay data plane.
func setupRelayRouter(r *gin.Engine) {
	playground := r.Group("/pg")
	playground.Use(middleware.UserAuth())
	playground.POST("/chat/completions", relayhandlers.Playground)

	kling := r.Group("/kling/v1")
	kling.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	kling.OPTIONS("/*path", func(c *gin.Context) { c.AbortWithStatus(204) })
	kling.POST("/videos/text2video", relayhandlers.RelayKlingTask)
	kling.POST("/videos/image2video", relayhandlers.RelayKlingTask)
	kling.GET("/videos/text2video/:task_id", relayhandlers.RelayKlingTaskFetch)
	kling.GET("/videos/image2video/:task_id", relayhandlers.RelayKlingTaskFetch)

	suno := r.Group("/suno")
	suno.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	suno.POST("/submit/:action", relayhandlers.RelaySunoSubmit)
	suno.POST("/fetch", relayhandlers.RelaySunoFetch)
	suno.GET("/fetch/:id", relayhandlers.RelaySunoFetch)

	relay := r.Group("/v1")
	relay.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth(), middleware.ModelRequestRateLimit())
	relay.OPTIONS("/*path", func(c *gin.Context) { c.AbortWithStatus(204) })
	relay.POST("/chat/completions", relayhandlers.RelayChatCompletions)
	relay.POST("/completions", relayhandlers.RelayCompletions)
	relay.POST("/embeddings", relayhandlers.RelayEmbeddings)
	relay.POST("/moderations", relayhandlers.RelayModerations)
	relay.POST("/images/generations", relayhandlers.RelayImageGenerations)
	relay.POST("/images/edits", relayhandlers.RelayImageEdits)
	relay.POST("/audio/speech", relayhandlers.RelayAudioSpeech)
	relay.POST("/audio/transcriptions", relayhandlers.RelayAudioTranscription)
	relay.POST("/audio/translations", relayhandlers.RelayAudioTranslation)
	relay.POST("/responses", relayhandlers.RelayResponses)
	relay.POST("/responses/compact", relayhandlers.RelayResponsesCompact)
	relay.POST("/video/generations", relayhandlers.RelayTask)
	relay.GET("/video/generations/:task_id", relayhandlers.RelayTaskFetch)
	relay.POST("/videos/:video_id/remix", relayhandlers.RelayTask)
	relay.POST("/videos", relayhandlers.RelayTask)
	relay.GET("/videos/:task_id", relayhandlers.RelayTaskFetch)
	relay.POST("/alpha/search", relayhandlers.RelayAlphaSearch)
	relay.POST("/messages", relayhandlers.RelayClaudeMessages)
	relay.POST("/rerank", relayhandlers.RelayRerank)
	relay.POST("/edits", relayhandlers.RelayEdits)
	relay.GET("/realtime", middleware.RequireRelayQueryModel("model"), relayhandlers.RelayRealtime)
	relay.POST("/engines/:model/embeddings", relayhandlers.RelayEnginesEmbeddings)
	relay.POST("/models/*path", relayhandlers.RelayGeminiNative)
	relay.GET("/models", relayhandlers.RelayListModels)
	relay.GET("/models/:model", relayhandlers.RelayRetrieveModel)
	relay.DELETE("/models/:model", relayhandlers.RelayNotImplemented)
	relay.POST("/images/variations", relayhandlers.RelayNotImplemented)
	relay.GET("/files", relayhandlers.RelayNotImplemented)
	relay.POST("/files", relayhandlers.RelayNotImplemented)
	relay.DELETE("/files/:id", relayhandlers.RelayNotImplemented)
	relay.GET("/files/:id", relayhandlers.RelayNotImplemented)
	relay.GET("/files/:id/content", relayhandlers.RelayNotImplemented)
	relay.POST("/fine-tunes", relayhandlers.RelayNotImplemented)
	relay.GET("/fine-tunes", relayhandlers.RelayNotImplemented)
	relay.GET("/fine-tunes/:id", relayhandlers.RelayNotImplemented)
	relay.POST("/fine-tunes/:id/cancel", relayhandlers.RelayNotImplemented)
	relay.GET("/fine-tunes/:id/events", relayhandlers.RelayNotImplemented)

	// Completed video content may be retrieved with either the original relay
	// token or the owning dashboard session. Ownership is checked again before
	// any provider credential is decrypted.
	videoContent := r.Group("/v1")
	videoContent.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenOrUserAuth())
	videoContent.GET("/videos/:task_id/content", relayhandlers.VideoProxy)

	// Gemini protocol surface: /v1beta model listing plus the native Gemini
	// generateContent passthrough.
	v1beta := r.Group("/v1beta")
	v1beta.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth(), middleware.ModelRequestRateLimit())
	v1beta.GET("/models", relayhandlers.RelayListModelsGemini)
	v1beta.GET("/openai/models", relayhandlers.RelayListModels)
	v1beta.POST("/models/*path", relayhandlers.RelayGeminiNative)
	v1beta.OPTIONS("/models/*path", func(c *gin.Context) { c.AbortWithStatus(204) })

	// Jimeng validates the cheap Action selector before authentication, then
	// authenticates and rate-limits before allocating or decoding its large body.
	jimeng := r.Group("/jimeng")
	jimeng.Use(
		middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.JimengActionValidate(),
		middleware.TokenAuth(), middleware.JimengRequestConvert(), middleware.RequireJimengModel(),
	)
	jimeng.POST("/", relayhandlers.RelayJimeng)

	// Legacy video task aliases share the task controller.
	task := r.Group("/v1")
	task.Use(middleware.RelayCORS(), middleware.GlobalRateLimit(), middleware.TokenAuth())
	task.POST("/video/submit", relayhandlers.RelayVideoSubmit)
	task.GET("/video/fetch", relayhandlers.RelayVideoFetch)

	registerMidjourneyRouterGroup(r, "/mj")
	registerMidjourneyRouterGroup(r, "/:mode/mj")
}

// registerMidjourneyRouterGroup mirrors the reference's middleware boundary:
// the image proxy is rate-limited but public, while every task route registered
// after it requires a usable relay token.
func registerMidjourneyRouterGroup(r *gin.Engine, prefix string) {
	mj := r.Group(prefix)
	mj.Use(middleware.RelayCORS(), middleware.GlobalRateLimit())
	mj.GET("/image/:id", relayhandlers.RelayMidjourneyImage)
	mj.Use(middleware.TokenAuth())
	mj.POST("/submit/action", relayhandlers.RelayMidjourney)
	mj.POST("/submit/shorten", relayhandlers.RelayMidjourney)
	mj.POST("/submit/modal", relayhandlers.RelayMidjourney)
	mj.POST("/submit/imagine", relayhandlers.RelayMidjourney)
	mj.POST("/submit/change", relayhandlers.RelayMidjourney)
	mj.POST("/submit/simple-change", relayhandlers.RelayMidjourney)
	mj.POST("/submit/describe", relayhandlers.RelayMidjourney)
	mj.POST("/submit/blend", relayhandlers.RelayMidjourney)
	mj.POST("/submit/edits", relayhandlers.RelayMidjourney)
	mj.POST("/submit/video", relayhandlers.RelayMidjourney)
	mj.GET("/task/:id/fetch", relayhandlers.RelayMidjourney)
	mj.GET("/task/:id/image-seed", relayhandlers.RelayMidjourney)
	mj.POST("/task/list-by-condition", relayhandlers.RelayMidjourney)
	mj.POST("/insight-face/swap", relayhandlers.RelayMidjourney)
	mj.POST("/submit/upload-discord-images", relayhandlers.RelayMidjourney)
}
