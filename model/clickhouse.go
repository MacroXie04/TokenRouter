package model

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// openClickHouseLog connects to a ClickHouse log database and creates the log
// MergeTree tables. ClickHouse is optional; when unavailable the caller falls
// back to the primary database. Implemented in Phase 8.
func openClickHouseLog(dsn string) (*gorm.DB, error) {
	return nil, fmt.Errorf("clickhouse log database requires LOG_SQL_DSN with a supported driver")
}

// isClickHouseDSN reports whether a DSN targets ClickHouse.
func isClickHouseDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "clickhouse://")
}

// ensure compile-time linkage for gorm (used by ClickHouse path in Phase 8).
var _ = gorm.ErrRecordNotFound
