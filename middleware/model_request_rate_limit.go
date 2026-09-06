package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	modelRequestTotalRateLimitPrefix   = "model-request-total"
	modelRequestSuccessRateLimitPrefix = "model-request-success"
)

type modelRequestRateReservation struct {
	storeKey string
	store    common.KVStore
	active   bool
}

// ModelRequestRateLimit applies the option-backed per-user model-request
// policy after relay authentication. The policy is loaded for every request,
// so a committed option update takes effect without rebuilding the router.
// Successful requests are provisionally reserved and released on every error,
// which keeps the success cap concurrency-safe without counting failures.
func ModelRequestRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		config := setting.GetModelRequestRateLimitSetting()
		if !config.Enabled {
			c.Next()
			return
		}
		userID := common.GetUserId(c)
		if userID <= 0 {
			writeModelRequestRateLimitError(c, http.StatusInternalServerError, "rate_limit_check_failed",
				"rate limit identity is unavailable")
			return
		}
		group := GetTokenGroup(c)
		if group == "" {
			group = common.GetUserGroup(c)
		}
		totalLimit, successLimit := config.TotalLimit, config.SuccessLimit
		if limits, found := config.Groups[group]; found {
			totalLimit, successLimit = limits[0], limits[1]
		}
		window := time.Duration(config.DurationMinutes) * time.Minute
		identity := strconv.Itoa(userID)

		successReservation, allowed, err := reserveModelRequestRateLimit(
			c.Request.Context(), modelRequestSuccessRateLimitPrefix, identity, successLimit, window,
		)
		if err != nil {
			writeModelRequestRateLimitError(c, http.StatusInternalServerError, "rate_limit_check_failed",
				"rate limit check failed")
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.FormatInt(int64(window/time.Second), 10))
			writeModelRequestRateLimitError(c, http.StatusTooManyRequests, "rate_limit_exceeded",
				fmt.Sprintf("successful request limit reached: at most %d requests per %d minutes", successLimit, config.DurationMinutes))
			return
		}
		keepSuccessReservation := false
		defer func() {
			if !keepSuccessReservation {
				releaseModelRequestRateLimit(successReservation)
			}
		}()

		totalReservation, allowed, err := reserveModelRequestRateLimit(
			c.Request.Context(), modelRequestTotalRateLimitPrefix, identity, totalLimit, window,
		)
		if err != nil {
			writeModelRequestRateLimitError(c, http.StatusInternalServerError, "rate_limit_check_failed",
				"rate limit check failed")
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.FormatInt(int64(window/time.Second), 10))
			writeModelRequestRateLimitError(c, http.StatusTooManyRequests, "rate_limit_exceeded",
				fmt.Sprintf("total request limit reached: at most %d requests per %d minutes", totalLimit, config.DurationMinutes))
			return
		}

		c.Next()
		keepSuccessReservation = c.Writer.Status() < http.StatusBadRequest && c.Request.Context().Err() == nil
		_ = totalReservation // Total requests remain counted after admission.
	}
}

func reserveModelRequestRateLimit(
	ctx context.Context,
	prefix, identity string,
	limit int,
	window time.Duration,
) (modelRequestRateReservation, bool, error) {
	if limit <= 0 {
		return modelRequestRateReservation{}, true, nil
	}
	windowSeconds := int64(window / time.Second)
	if windowSeconds <= 0 {
		return modelRequestRateReservation{}, false, fmt.Errorf("invalid model request rate-limit window")
	}
	store := common.Store
	if store == nil {
		return modelRequestRateReservation{}, false, fmt.Errorf("model request rate-limit store is unavailable")
	}
	bucket := time.Now().Unix() / windowSeconds
	storeKey := fmt.Sprintf("rate:%s:%s:%d", prefix, identity, bucket)
	count, err := store.Incr(ctx, storeKey)
	if err != nil {
		return modelRequestRateReservation{}, false, err
	}
	reservation := modelRequestRateReservation{storeKey: storeKey, store: store, active: true}
	if err := store.Expire(ctx, storeKey, window+time.Second); err != nil {
		releaseModelRequestRateLimit(reservation)
		return modelRequestRateReservation{}, false, fmt.Errorf("expire model request rate-limit counter: %w", err)
	}
	if count > int64(limit) {
		releaseModelRequestRateLimit(reservation)
		return modelRequestRateReservation{}, false, nil
	}
	return reservation, true, nil
}

func releaseModelRequestRateLimit(reservation modelRequestRateReservation) {
	if !reservation.active || reservation.storeKey == "" || reservation.store == nil {
		return
	}
	// Downstream failures often coincide with a canceled request context. Use a
	// short detached cleanup context so cancellation cannot turn a failed relay
	// into a permanently counted successful request.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := reservation.store.Decr(ctx, reservation.storeKey); err != nil {
		common.Logger.Warn("failed to release model request rate-limit reservation", "err", err.Error())
	}
}

func writeModelRequestRateLimitError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": protocolError{
		Message: message,
		Type:    "rate_limit_error",
		Code:    code,
	}})
}
