package service

import "gorm.io/gorm/clause"

// gormExpr builds a parameterized SQL expression for atomic column updates.
func gormExpr(sql string, args ...any) clause.Expr {
	return clause.Expr{SQL: sql, Vars: args}
}
