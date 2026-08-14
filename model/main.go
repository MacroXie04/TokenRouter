// Package model defines TokenRouter's persistent entities and database access.
// It supports SQLite, MySQL (>=5.7.8) and PostgreSQL (>=9.6) through GORM v2,
// with an optional separate log database (which may be ClickHouse).
package model

import (
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/tokenrouter/tokenrouter/common"
)

// DB is the primary database handle.
var DB *gorm.DB

// LOG_DB is the log database handle; it equals DB when no LOG_SQL_DSN is set.
var LOG_DB *gorm.DB

// AllModels is the ordered list of entities passed to AutoMigrate.
var AllModels = []any{
	&User{},
	&Token{},
	&Channel{},
	&Ability{},
	&Option{},
	&Redemption{},
	&Log{},
	&Midjourney{},
	&TopUp{},
	&QuotaData{},
	&Task{},
	&Model{},
	&Vendor{},
	&PrefillGroup{},
	&Setup{},
	&TwoFA{},
	&TwoFABackupCode{},
	&Checkin{},
	&SubscriptionPlan{},
	&SubscriptionOrder{},
	&UserSubscription{},
	&SubscriptionPreConsumeRecord{},
	&CustomOAuthProvider{},
	&UserOAuthBinding{},
	&PerfMetric{},
	&SystemInstance{},
	&SystemTask{},
	&SystemTaskLock{},
	&CasbinRule{},
	&AuthzRole{},
	&UserSession{},
	&AuthFlow{},
	&ExternalIdentityClaim{},
	&PasskeyCredential{},
}

// InitDB connects to the primary (and optional log) database and migrates.
func InitDB() (err error) {
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
		DB, err = gorm.Open(postgres.Open(dsn), gormConfig)
	} else {
		DB, err = gorm.Open(mysql.Open(dsn), gormConfig)
	}
	if err != nil {
		return err
	}

	if logDSN := os.Getenv("LOG_SQL_DSN"); logDSN != "" && logDSN != dsn {
		LOG_DB, err = openLogDB(logDSN, gormConfig)
		if err != nil {
			common.SysError("failed to connect log database, falling back to main: " + err.Error())
			LOG_DB = DB
		}
	} else {
		LOG_DB = DB
	}

	sqlDB, err := DB.DB()
	if err == nil {
		sqlDB.SetMaxIdleConns(common.GetEnvInt("SQL_MAX_IDLE_CONNS", 100))
		sqlDB.SetMaxOpenConns(common.GetEnvInt("SQL_MAX_OPEN_CONNS", 1000))
		sqlDB.SetConnMaxLifetime(time.Duration(common.GetEnvInt("SQL_MAX_LIFETIME", 60)) * time.Second)
	}

	return migrateDB()
}

// openLogDB opens a secondary database; ClickHouse log storage is handled by a
// dedicated path that builds MergeTree DDL instead of GORM AutoMigrate.
func openLogDB(dsn string, cfg *gorm.Config) (*gorm.DB, error) {
	if strings.HasPrefix(dsn, "clickhouse://") {
		return openClickHouseLog(dsn)
	}
	return gorm.Open(pgOrMySQL(dsn), cfg)
}

func pgOrMySQL(dsn string) gorm.Dialector {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return postgres.Open(dsn)
	}
	return mysql.Open(dsn)
}

// migrateDB runs AutoMigrate for every entity on the primary DB.
func migrateDB() error {
	if err := DB.AutoMigrate(AllModels...); err != nil {
		return err
	}
	return nil
}

// UsingPostgreSQL reports whether the primary DB is PostgreSQL.
func UsingPostgreSQL() bool {
	return common.UsingPostgreSQL()
}

// UsingMySQL reports whether the primary DB is MySQL.
func UsingMySQL() bool {
	return common.UsingMySQL()
}

// UsingSQLite reports whether the primary DB is SQLite.
func UsingSQLite() bool {
	return common.UsingSQLite()
}

// lockForUpdate applies a SELECT ... FOR UPDATE row lock for MySQL/PostgreSQL
// and is a no-op for SQLite, where the syntax is unsupported. GORM v2 requires
// clause.Locking; the legacy gorm:query_option string form is silently ignored.
func lockForUpdate(db *gorm.DB) *gorm.DB {
	if UsingPostgreSQL() || UsingMySQL() {
		return db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return db
}

// groupCol returns the quoted "group" column for raw SQL on the primary DB.
func groupCol() string {
	if UsingPostgreSQL() {
		return `"group"`
	}
	return "`group`"
}

// keyCol returns the quoted "key" column for raw SQL on the primary DB.
func keyCol() string {
	if UsingPostgreSQL() {
		return `"key"`
	}
	return "`key`"
}

// trueVal returns the SQL boolean literal for the primary DB.
func trueVal() string {
	if UsingPostgreSQL() {
		return "true"
	}
	return "1"
}

// falseVal returns the SQL boolean literal for the primary DB.
func falseVal() string {
	if UsingPostgreSQL() {
		return "false"
	}
	return "0"
}
