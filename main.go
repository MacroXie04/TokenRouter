package main

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/controller"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
	"github.com/tokenrouter/tokenrouter/web"
)

func main() {
	// Load .env if present (does not override already-set env vars).
	_ = godotenv.Load()

	common.SysLog("TokenRouter starting, version=" + common.Version)

	if err := model.InitDB(); err != nil {
		common.SysError("failed to init database: " + err.Error())
		os.Exit(1)
	}
	if err := common.InitRedis(); err != nil {
		common.SysError("failed to init redis: " + err.Error())
	}
	common.InitSSRF()
	service.InitMailer()
	if err := setting.Init(); err != nil {
		common.SysError("failed to load options: " + err.Error())
	}
	service.LoadSensitiveWords()
	if err := service.InitAbilityCache(); err != nil {
		common.SysError("failed to load abilities: " + err.Error())
	}
	if err := service.InitCasbin(); err != nil {
		common.SysError("failed to init authorization engine: " + err.Error())
	}
	if err := service.InitPermissionAuthz(); err != nil {
		common.SysError("failed to init permission engine: " + err.Error())
	}
	if err := service.InitWebAuthn(); err != nil {
		common.SysError("failed to init WebAuthn: " + err.Error())
	}
	if err := service.ReloadPricingOptions(); err != nil {
		common.SysError("failed to load pricing options: " + err.Error())
	}
	if err := service.RegisterSystemInstance(); err != nil {
		common.SysError("failed to register system instance: " + err.Error())
	}
	service.StartBackgroundJobs()

	controller.StartTime = common.NowTimestamp()

	r := router.SetUpRouter()
	middleware.InitTrustedProxies(r)
	serveEmbedded(r)

	port := common.GetEnv("PORT", "3000")
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	go func() {
		common.SysLog("listening on :" + port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			common.SysError("server error: " + err.Error())
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	common.SysLog("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		common.SysError("forced shutdown: " + err.Error())
	}
	common.SysLog("shutdown complete")
}

// serveEmbedded serves the embedded frontend, falling back to index.html for
// client-side routes, and injects analytics (Google Analytics / Umami) into the
// served index.html.
func serveEmbedded(r *gin.Engine) {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		common.SysError("embedded dist missing: " + err.Error())
		return
	}
	fileServer := http.FileServer(http.FS(dist))
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// Relay/dashboard/api paths never fall back to the SPA: unknown
		// endpoints under these prefixes return the structured RelayNotFound.
		if strings.HasPrefix(path, "/v1") || strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/assets") {
			controller.RelayNotFound(c)
			return
		}
		if path != "/" && fileExists(dist, path) {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// Serve index.html with analytics injection.
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		_, _ = c.Writer.Write([]byte(injectAnalytics(string(index))))
	})
}

// injectAnalytics inserts the Google Analytics and Umami script tags before
// </head> when their IDs are configured via environment variables.
func injectAnalytics(html string) string {
	scripts := ""
	if ga := common.GetEnv("GOOGLE_ANALYTICS_ID", ""); ga != "" {
		scripts += `<script async src="https://www.googletagmanager.com/gtag/js?id=` + ga + `"></script>` +
			`<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','` + ga + `');</script>`
	}
	if umami := common.GetEnv("UMAMI_WEBSITE_ID", ""); umami != "" {
		src := common.GetEnv("UMAMI_SCRIPT_URL", "https://analytics.umami.is/script.js")
		scripts += `<script defer src="` + src + `" data-website-id="` + umami + `"></script>`
	}
	if scripts == "" {
		return html
	}
	return strings.Replace(html, "</head>", scripts+"</head>", 1)
}

func fileExists(fsys fs.FS, path string) bool {
	f, err := fsys.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
