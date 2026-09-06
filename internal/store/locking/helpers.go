package locking

import (
	"gorm.io/gorm/clause"
)

// gormExpr builds a parameterized SQL expression for atomic column updates.
func GormExpr(sql string, args ...any) clause.Expr {
	return clause.Expr{SQL: sql, Vars: args}
}
