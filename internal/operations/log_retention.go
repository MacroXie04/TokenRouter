package operations

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultLogRetentionDays = 30
	maxLogRetentionDays     = 100 * 365
)

// CleanupExpiredLogs deletes consumption logs older than the retention period
// (LOG_RETENTION_DAYS, default 30 days; 0 disables cleanup).
func CleanupExpiredLogs() error {
	return CleanupExpiredLogsContext(context.Background())
}

func logRetentionDays() (int64, bool, error) {
	raw, configured := os.LookupEnv("LOG_RETENTION_DAYS")
	if !configured || strings.TrimSpace(raw) == "" {
		return defaultLogRetentionDays, true, nil
	}
	trimmed := strings.TrimSpace(raw)
	days, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || strconv.FormatInt(days, 10) != trimmed || days < 0 || days > maxLogRetentionDays {
		return 0, false, fmt.Errorf("LOG_RETENTION_DAYS must be an integer from 0 to %d", maxLogRetentionDays)
	}
	return days, days > 0, nil
}
