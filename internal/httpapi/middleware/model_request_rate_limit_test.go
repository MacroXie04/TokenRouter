package middleware

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type modelRequestRateLimitStore struct {
	cache.KVStore
	mu        sync.Mutex
	counts    map[string]int64
	expiries  map[string]time.Duration
	incrErr   error
	expireErr error
}

func (store *modelRequestRateLimitStore) Incr(_ context.Context, key string) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.incrErr != nil {
		return 0, store.incrErr
	}
	if store.counts == nil {
		store.counts = make(map[string]int64)
	}
	store.counts[key]++
	return store.counts[key], nil
}

func (store *modelRequestRateLimitStore) Decr(_ context.Context, key string) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.counts == nil {
		store.counts = make(map[string]int64)
	}
	store.counts[key]--
	return store.counts[key], nil
}

func (store *modelRequestRateLimitStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.expireErr != nil {
		return store.expireErr
	}
	if store.expiries == nil {
		store.expiries = make(map[string]time.Duration)
	}
	store.expiries[key] = ttl
	return nil
}

func (store *modelRequestRateLimitStore) countContaining(fragment string) int64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result int64
	for key, count := range store.counts {
		if strings.Contains(key, fragment) {
			result += count
		}
	}
	return result
}

func setupModelRequestRateLimitMiddlewareTest(t *testing.T) *modelRequestRateLimitStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "model-request-rate-limit.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, setting.Init())

	store := &modelRequestRateLimitStore{}
	previousStore := cache.Store
	cache.Store = store
	t.Cleanup(func() { cache.Store = previousStore })
	return store
}

func modelRequestRateLimitRouter(userID int, group string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		requestctx.SetUserId(c, userID)
		requestctx.SetUserGroup(c, group)
		c.Set(requestctx.ContextKeyGroup, group)
		c.Next()
	})
	router.Use(ModelRequestRateLimit())
	router.GET("/", handler)
	return router
}

func runModelRequestRateLimitRequest(router http.Handler) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	return recorder
}

func TestModelRequestRateLimitCountsTotalAndSuccessfulRequestsSeparately(t *testing.T) {
	store := setupModelRequestRateLimitMiddlewareTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRequestRateLimitEnabledOption:         "true",
		setting.ModelRequestRateLimitDurationMinutesOption: "5",
		setting.ModelRequestRateLimitCountOption:           "3",
		setting.ModelRequestRateLimitSuccessCountOption:    "2",
	}))
	var calls atomic.Int32
	router := modelRequestRateLimitRouter(42, "default", func(c *gin.Context) {
		if calls.Add(1) == 1 {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})

	assert.Equal(t, http.StatusInternalServerError, runModelRequestRateLimitRequest(router).Code)
	assert.Equal(t, http.StatusNoContent, runModelRequestRateLimitRequest(router).Code)
	assert.Equal(t, http.StatusNoContent, runModelRequestRateLimitRequest(router).Code)
	limited := runModelRequestRateLimitRequest(router)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Contains(t, limited.Body.String(), `"code":"rate_limit_exceeded"`)
	assert.Equal(t, "300", limited.Header().Get("Retry-After"))
	assert.Equal(t, int64(3), store.countContaining(modelRequestTotalRateLimitPrefix))
	assert.Equal(t, int64(2), store.countContaining(modelRequestSuccessRateLimitPrefix))
	assert.Equal(t, int32(3), calls.Load(), "a rate-limited request must not reach the handler")
}

func TestModelRequestRateLimitUsesGroupOverridesAndLiveUpdates(t *testing.T) {
	setupModelRequestRateLimitMiddlewareTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRequestRateLimitEnabledOption:         "true",
		setting.ModelRequestRateLimitDurationMinutesOption: "5",
		setting.ModelRequestRateLimitCountOption:           "5",
		setting.ModelRequestRateLimitSuccessCountOption:    "5",
		setting.ModelRequestRateLimitGroupOption:           `{"vip":[1,5]}`,
	}))
	router := modelRequestRateLimitRouter(7, "vip", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	assert.Equal(t, http.StatusNoContent, runModelRequestRateLimitRequest(router).Code)
	assert.Equal(t, http.StatusTooManyRequests, runModelRequestRateLimitRequest(router).Code)

	require.NoError(t, setting.UpdateOption(setting.ModelRequestRateLimitEnabledOption, "false"))
	assert.Equal(t, http.StatusNoContent, runModelRequestRateLimitRequest(router).Code,
		"the existing middleware must observe a committed live update")
}

func TestModelRequestRateLimitFailsClosedWithoutIdentityOrCounterStore(t *testing.T) {
	store := setupModelRequestRateLimitMiddlewareTest(t)
	require.NoError(t, setting.UpdateOption(setting.ModelRequestRateLimitEnabledOption, "true"))

	missingIdentity := modelRequestRateLimitRouter(0, "default", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response := runModelRequestRateLimitRequest(missingIdentity)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"rate_limit_check_failed"`)

	store.incrErr = errors.New("counter unavailable")
	storeFailure := modelRequestRateLimitRouter(9, "default", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response = runModelRequestRateLimitRequest(storeFailure)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.NotContains(t, response.Body.String(), "counter unavailable")

	store.incrErr = nil
	store.expireErr = errors.New("expiry unavailable")
	expiryFailure := modelRequestRateLimitRouter(10, "default", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response = runModelRequestRateLimitRequest(expiryFailure)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.NotContains(t, response.Body.String(), "expiry unavailable")
	assert.Equal(t, int64(0), store.countContaining(modelRequestSuccessRateLimitPrefix),
		"an expiry failure must release the provisional counter")

	previousStore := cache.Store
	cache.Store = nil
	t.Cleanup(func() { cache.Store = previousStore })
	missingStore := modelRequestRateLimitRouter(11, "default", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response = runModelRequestRateLimitRequest(missingStore)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.NotContains(t, response.Body.String(), "unavailable")
}

func TestModelRequestRateLimitConcurrentAdmissionHonorsTotalCap(t *testing.T) {
	setupModelRequestRateLimitMiddlewareTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRequestRateLimitEnabledOption:      "true",
		setting.ModelRequestRateLimitCountOption:        "7",
		setting.ModelRequestRateLimitSuccessCountOption: "100",
	}))
	router := modelRequestRateLimitRouter(77, "default", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	var allowed atomic.Int32
	var denied atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			switch runModelRequestRateLimitRequest(router).Code {
			case http.StatusNoContent:
				allowed.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			}
		}()
	}
	wait.Wait()
	assert.Equal(t, int32(7), allowed.Load())
	assert.Equal(t, int32(57), denied.Load())
}

func TestModelRequestRateLimitConcurrentAdmissionHonorsSuccessCap(t *testing.T) {
	setupModelRequestRateLimitMiddlewareTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRequestRateLimitEnabledOption:      "true",
		setting.ModelRequestRateLimitCountOption:        "0",
		setting.ModelRequestRateLimitSuccessCountOption: "7",
	}))
	router := modelRequestRateLimitRouter(78, "default", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	var allowed atomic.Int32
	var denied atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			switch runModelRequestRateLimitRequest(router).Code {
			case http.StatusNoContent:
				allowed.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			}
		}()
	}
	wait.Wait()
	assert.Equal(t, int32(7), allowed.Load())
	assert.Equal(t, int32(57), denied.Load())
}

func TestModelRequestRateLimitDoesNotCountCanceledSuccess(t *testing.T) {
	store := setupModelRequestRateLimitMiddlewareTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelRequestRateLimitEnabledOption:      "true",
		setting.ModelRequestRateLimitCountOption:        "0",
		setting.ModelRequestRateLimitSuccessCountOption: "1",
	}))
	router := modelRequestRateLimitRouter(79, "default", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(requestContext)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Equal(t, int64(0), store.countContaining(modelRequestSuccessRateLimitPrefix))
	assert.Equal(t, http.StatusNoContent, runModelRequestRateLimitRequest(router).Code,
		"a canceled response must not consume the only successful-request slot")
}
