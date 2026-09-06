package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/controller"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
	"github.com/tokenrouter/tokenrouter/web"
)

func main() {
	// Load .env if present (does not override already-set env vars).
	_ = godotenv.Load()

	common.SysLog("TokenRouter starting, version=" + common.Version)
	if err := common.InitializeSessionSecret(); err != nil {
		common.SysError("session secret configuration error: " + err.Error())
		os.Exit(1)
	}

	if err := model.InitDB(); err != nil {
		common.SysError("failed to init database: " + err.Error())
		os.Exit(1)
	}
	if err := common.InitRedis(); err != nil {
		common.SysError("failed to init redis: " + err.Error())
		os.Exit(1)
	}
	common.InitSSRF()
	service.InitMailer()
	if err := runRuntimeInitializers(defaultRuntimeInitializers()); err != nil {
		common.SysError("critical startup initialization failed: " + err.Error())
		os.Exit(1)
	}
	if err := service.StartBackgroundJobs(); err != nil {
		common.SysError("background job configuration error: " + err.Error())
		os.Exit(1)
	}

	controller.StartTime = common.NowTimestamp()

	r := router.SetUpRouter()
	middleware.InitTrustedProxies(r)
	serveEmbedded(r)

	port := common.GetEnv("PORT", "3000")
	srv := newHTTPServer(":"+port, r)

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

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 5 * time.Minute
	httpIdleTimeout       = 2 * time.Minute
	httpMaxHeaderBytes    = 128 << 10
)

// newHTTPServer applies an explicit slow-client safety floor. WriteTimeout is
// intentionally left at zero because relay responses can be long-lived SSE or
// WebSocket streams whose own deadlines are enforced by the relay layer.
func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}

type runtimeInitializer struct {
	name string
	run  func() error
}

// defaultRuntimeInitializers is deliberately ordered. Settings publish first;
// consumers then build routing, authorization, passkey, and pricing snapshots.
// Any failure is fatal because serving with a stale/default snapshot can grant
// unintended access or misprice traffic.
func defaultRuntimeInitializers() []runtimeInitializer {
	return []runtimeInitializer{
		{name: "settings", run: setting.Init},
		{name: "audit outbox privacy", run: service.ScrubDeliveredAuditLogPayloads},
		{name: "relay HTTP transport", run: relay.InitHTTPClient},
		{name: "sensitive words", run: func() error {
			service.LoadSensitiveWords()
			return nil
		}},
		{name: "channel abilities", run: service.InitAbilityCache},
		{name: "authorization engine", run: service.InitCasbin},
		{name: "permission engine", run: service.InitPermissionAuthz},
		{name: "WebAuthn", run: service.InitWebAuthn},
		{name: "pricing", run: service.ReloadPricingOptions},
		{name: "system instance", run: service.RegisterSystemInstance},
	}
}

func runRuntimeInitializers(initializers []runtimeInitializer) error {
	for _, initializer := range initializers {
		if initializer.name == "" || initializer.run == nil {
			return errors.New("invalid runtime initializer")
		}
		if err := initializer.run(); err != nil {
			return fmt.Errorf("%s: %w", initializer.name, err)
		}
	}
	return nil
}

// serveEmbedded installs the embedded frontend after the API and relay routes
// have been registered. Only otherwise-unmatched web requests reach this
// handler, so its caching, compression, and abuse controls cannot change
// responses from the data plane or dashboard API.
func serveEmbedded(r *gin.Engine) {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		common.SysError("embedded dist missing: " + err.Error())
		return
	}
	handler, err := newEmbeddedWebHandler(dist)
	if err != nil {
		common.SysError("embedded web handler unavailable: " + err.Error())
		return
	}
	r.NoRoute(handler.ServeGIN)
}

func fileExists(fsys fs.FS, path string) bool {
	assetPath, ok := normalizedAssetPath(path)
	if !ok || assetPath == "" {
		return false
	}
	info, err := fs.Stat(fsys, assetPath)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}
