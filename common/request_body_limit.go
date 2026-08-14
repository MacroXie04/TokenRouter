package common

import (
	"errors"
	"net/http"
)

// ErrRequestBodyTooLarge is returned when a request body exceeds its limit.
var ErrRequestBodyTooLarge = errors.New("request body too large")

// IsRequestBodyTooLargeError reports whether err indicates an oversized
// request body, either from our own limit or from a http.MaxBytesError.
func IsRequestBodyTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return true
	}
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// defaultAnonymousRequestBodyLimitKB is the fallback limit for anonymous
// request bodies (ANONYMOUS_REQUEST_BODY_LIMIT_KB).
const defaultAnonymousRequestBodyLimitKB = 512

// GetAnonymousRequestBodyLimitBytes returns the anonymous request-body limit
// in bytes. ANONYMOUS_REQUEST_BODY_LIMIT_KB < 0 falls back to the default;
// zero disables the limit entirely.
func GetAnonymousRequestBodyLimitBytes() int64 {
	limitKB := GetEnvInt("ANONYMOUS_REQUEST_BODY_LIMIT_KB", defaultAnonymousRequestBodyLimitKB)
	if limitKB < 0 {
		limitKB = defaultAnonymousRequestBodyLimitKB
	}
	return int64(limitKB) << 10
}
