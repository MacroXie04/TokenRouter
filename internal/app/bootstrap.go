package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/public"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	"github.com/tokenrouter/tokenrouter/internal/platform/buildinfo"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	"github.com/tokenrouter/tokenrouter/internal/relay/engine"
	"github.com/tokenrouter/tokenrouter/internal/relay/policy"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/web"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Run starts TokenRouter and blocks until process shutdown.
func Run() {
	// Load .env if present (does not override already-set env vars).
	_ = godotenv.Load()

	logging.SysLog("TokenRouter starting, version=" + buildinfo.Version)
	if err := cryptoutil.InitializeSessionSecret(); err != nil {
		logging.SysError("session secret configuration error: " + err.Error())
		os.Exit(1)
	}

	if err := model.InitDB(); err != nil {
		logging.SysError("failed to init database: " + err.Error())
		os.Exit(1)
	}
	if err := cache.InitRedis(); err != nil {
		logging.SysError("failed to init redis: " + err.Error())
		os.Exit(1)
	}
	httpx.InitSSRF()
	mailtransport.InitMailer(setting.EffectiveSMTPSetting)
	if err := runRuntimeInitializers(defaultRuntimeInitializers()); err != nil {
		logging.SysError("critical startup initialization failed: " + err.Error())
		os.Exit(1)
	}
	configureBackgroundRecovery()
	if err := operationssvc.StartBackgroundJobs(); err != nil {
		logging.SysError("background job configuration error: " + err.Error())
		os.Exit(1)
	}

	public.StartTime = wallclock.NowTimestamp()

	r := router.SetUpRouter()
	middleware.InitTrustedProxies(r)
	serveEmbedded(r)

	port := env.GetEnv("PORT", "3000")
	srv := newHTTPServer(":"+port, r)

	go func() {
		logging.SysLog("listening on :" + port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.SysError("server error: " + err.Error())
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logging.SysLog("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logging.SysError("forced shutdown: " + err.Error())
	}
	logging.SysLog("shutdown complete")
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
		{name: "audit outbox privacy", run: billingsvc.ScrubDeliveredAuditLogPayloads},
		{name: "relay HTTP transport", run: engine.InitHTTPClient},
		{name: "sensitive words", run: func() error {
			policy.LoadSensitiveWords()
			return nil
		}},
		{name: "channel abilities", run: channelssvc.InitAbilityCache},
		{name: "authorization engine", run: auth.InitCasbin},
		{name: "permission engine", run: auth.InitPermissionAuthz},
		{name: "WebAuthn", run: auth.InitWebAuthn},
		{name: "pricing", run: billingsvc.ReloadPricingOptions},
		{name: "system instance", run: operationssvc.RegisterSystemInstance},
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
		logging.SysError("embedded dist missing: " + err.Error())
		return
	}
	handler, err := newEmbeddedWebHandler(dist)
	if err != nil {
		logging.SysError("embedded web handler unavailable: " + err.Error())
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
