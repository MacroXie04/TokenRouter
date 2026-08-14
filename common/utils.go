package common

import (
	"fmt"
	"strconv"
	"time"
)

// Sprint formats any value with fmt.Sprintf("%v", v).
func Sprint(v any) string {
	return fmt.Sprintf("%v", v)
}

// NowTimestamp returns the current Unix time in seconds.
func NowTimestamp() int64 {
	return time.Now().Unix()
}

// NowTime returns the current time.
func NowTime() time.Time {
	return time.Now()
}

// TimeToTimestamp converts a time to Unix seconds.
func TimeToTimestamp(t time.Time) int64 {
	return t.Unix()
}

// TimestampToTime converts Unix seconds to a time.
func TimestampToTime(ts int64) time.Time {
	return time.Unix(ts, 0)
}

// Str2Int parses a string to int, returning 0 on error.
func Str2Int(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

// Int2Str formats an int to a string.
func Int2Str(i int) string {
	return strconv.Itoa(i)
}

// SafeFloat64 guards against NaN/Inf when converting to float64 for display.
func SafeFloat64(f float64) float64 {
	if f != f || f > 1e300 || f < -1e300 {
		return 0
	}
	return f
}

// ClampInt bounds v into [min, max].
func ClampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
