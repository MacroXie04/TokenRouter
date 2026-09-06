// Package store defines TokenRouter's persistent entities and database access.
// It supports SQLite, MySQL (>=5.7.8) and PostgreSQL (>=9.6) through GORM v2,
// with an optional separate log database (which may be ClickHouse).
package store

import (
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strconv"
	"strings"
	"time"
)

// DB is the primary database handle.
var DB *gorm.DB

// LOG_DB is the log database handle; it equals DB when no LOG_SQL_DSN is set.
var LOG_DB *gorm.DB

const (
	defaultSQLMaxIdleConns       = 100
	defaultSQLMaxOpenConns       = 1000
	defaultSQLMaxLifetimeSeconds = 60
	maxSQLPoolConnections        = 10_000
	maxSQLMaxLifetimeSeconds     = 30 * 24 * 60 * 60
)

type sqlPoolConfig struct {
	maxIdleConns       int
	maxOpenConns       int
	maxLifetimeSeconds int
}

// InitDB connects to the primary (and optional log) database and migrates.
func InitDB() (err error) {
	poolConfig, err := loadSQLPoolConfig()
	if err != nil {
		return err
	}
	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}
	dsn := os.Getenv("SQL_DSN")
	sqlitePath := os.Getenv("SQLITE_PATH")
	if dsn == "" {
		if sqlitePath == "" {
			sqlitePath = "tokenrouter.db"
		}
		DB, err = gorm.Open(sqlite.Open(sqlitePath), gormConfig)
	} else if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		DB, err = gorm.Open(postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		}), gormConfig)
	} else {
		DB, err = gorm.Open(mysql.Open(dsn), gormConfig)
	}
	if err != nil {
		return err
	}

	if logDSN := os.Getenv("LOG_SQL_DSN"); logDSN != "" && logDSN != dsn {
		LOG_DB, err = openLogDB(logDSN, gormConfig)
		if err != nil {
			logging.SysError("failed to connect log database, falling back to main: " + err.Error())
			LOG_DB = DB
		}
	} else {
		LOG_DB = DB
	}

	if err := applySQLPoolConfig(DB, poolConfig); err != nil {
		return fmt.Errorf("configure primary database pool: %w", err)
	}
	if LOG_DB != DB {
		if err := applySQLPoolConfig(LOG_DB, poolConfig); err != nil {
			return fmt.Errorf("configure log database pool: %w", err)
		}
	}

	return migrateDB()
}

func loadSQLPoolConfig() (sqlPoolConfig, error) {
	maxIdle, err := boundedSQLPoolEnv("SQL_MAX_IDLE_CONNS", defaultSQLMaxIdleConns, 0, maxSQLPoolConnections)
	if err != nil {
		return sqlPoolConfig{}, err
	}
	maxOpen, err := boundedSQLPoolEnv("SQL_MAX_OPEN_CONNS", defaultSQLMaxOpenConns, 1, maxSQLPoolConnections)
	if err != nil {
		return sqlPoolConfig{}, err
	}
	maxLifetime, err := boundedSQLPoolEnv("SQL_MAX_LIFETIME", defaultSQLMaxLifetimeSeconds, 0, maxSQLMaxLifetimeSeconds)
	if err != nil {
		return sqlPoolConfig{}, err
	}
	if maxIdle > maxOpen {
		return sqlPoolConfig{}, errors.New("SQL_MAX_IDLE_CONNS must not exceed SQL_MAX_OPEN_CONNS")
	}
	return sqlPoolConfig{
		maxIdleConns: maxIdle, maxOpenConns: maxOpen, maxLifetimeSeconds: maxLifetime,
	}, nil
}

func boundedSQLPoolEnv(name string, fallback, minimum, maximum int) (int, error) {
	raw, configured := os.LookupEnv(name)
	if !configured || raw == "" {
		return fallback, nil
	}
	if raw != strings.TrimSpace(raw) {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	return value, nil
}

func applySQLPoolConfig(db *gorm.DB, config sqlPoolConfig) error {
	if db == nil {
		return errors.New("database is nil")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(config.maxOpenConns)
	sqlDB.SetMaxIdleConns(config.maxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(config.maxLifetimeSeconds) * time.Second)
	return nil
}

// openLogDB opens a secondary database; ClickHouse log storage is handled by a
// dedicated path that builds MergeTree DDL instead of GORM AutoMigrate.
func openLogDB(dsn string, cfg *gorm.Config) (*gorm.DB, error) {
	if strings.HasPrefix(dsn, "clickhouse://") {
		return openClickHouseLog(dsn, cfg)
	}
	return gorm.Open(pgOrMySQL(dsn), cfg)
}

func pgOrMySQL(dsn string) gorm.Dialector {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		})
	}
	return mysql.Open(dsn)
}
