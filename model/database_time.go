package model

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// PrimaryDatabaseUnixTimestamp reads the shared clock through the primary
// database while applying the caller's cancellation and deadline. Callers
// must use this helper instead of dereferencing DB directly so startup and
// degraded-state failures are returned rather than panicking.
func PrimaryDatabaseUnixTimestamp(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, errors.New("database clock context is nil")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if DB == nil {
		return 0, errors.New("database is nil")
	}
	return DatabaseUnixTimestamp(DB.WithContext(ctx))
}

// DatabaseUnixTimestamp returns the primary database server's current Unix
// timestamp. Distributed leases and recovery deadlines must use one shared
// clock; process clocks are not a safe authority in a multi-node deployment.
func DatabaseUnixTimestamp(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, errors.New("database is nil")
	}
	expression, err := databaseTimeExpression(db.Dialector.Name())
	if err != nil {
		return 0, err
	}
	var now int64
	if err := db.Raw("SELECT " + expression).Scan(&now).Error; err != nil {
		return 0, fmt.Errorf("read database clock: %w", err)
	}
	if now <= 0 {
		return 0, errors.New("database clock returned an invalid timestamp")
	}
	return now, nil
}

func databaseTimeExpression(dialect string) (string, error) {
	switch dialect {
	case "sqlite":
		return "CAST(strftime('%s', 'now') AS INTEGER)", nil
	case "mysql":
		return "UNIX_TIMESTAMP()", nil
	case "postgres":
		// CURRENT_TIMESTAMP is fixed at transaction start on PostgreSQL. Leases
		// and recovery deadlines need the database's wall clock at the actual
		// statement, including inside long-running transactions.
		return "CAST(FLOOR(EXTRACT(EPOCH FROM clock_timestamp())) AS BIGINT)", nil
	default:
		return "", fmt.Errorf("unsupported database clock dialect %q", dialect)
	}
}
