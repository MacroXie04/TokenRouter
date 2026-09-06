package clock

import (
	"time"
)

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
