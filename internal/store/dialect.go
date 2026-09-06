package store

import (
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UsingPostgreSQL reports whether the primary DB is PostgreSQL.
func UsingPostgreSQL() bool {
	if DB != nil && DB.Dialector != nil {
		return DB.Dialector.Name() == "postgres"
	}
	return env.UsingPostgreSQL()
}

// UsingMySQL reports whether the primary DB is MySQL.
func UsingMySQL() bool {
	if DB != nil && DB.Dialector != nil {
		return DB.Dialector.Name() == "mysql"
	}
	return env.UsingMySQL()
}

// UsingSQLite reports whether the primary DB is SQLite.
func UsingSQLite() bool {
	if DB != nil && DB.Dialector != nil {
		return DB.Dialector.Name() == "sqlite"
	}
	return env.UsingSQLite()
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
