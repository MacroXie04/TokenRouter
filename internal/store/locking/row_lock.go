package locking

import (
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// subscriptionLockForUpdate applies SELECT ... FOR UPDATE on MySQL/PostgreSQL
// and is a no-op on SQLite, which does not support the syntax.
func SubscriptionLockForUpdate(tx *gorm.DB) *gorm.DB {
	if model.UsingPostgreSQL() || model.UsingMySQL() {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}
