package model

// CasbinRule stores a Casbin policy rule (RBAC/ABAC).
type CasbinRule struct {
	Id    uint   `json:"id" gorm:"primaryKey"`
	Ptype string `json:"ptype" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:1;index:idx_casbin_rule,priority:1"`
	V0    string `json:"v0" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:2;index:idx_casbin_rule,priority:2"`
	V1    string `json:"v1" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:3;index:idx_casbin_rule,priority:3"`
	V2    string `json:"v2" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:4;index:idx_casbin_rule,priority:4"`
	V3    string `json:"v3" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:5;index:idx_casbin_rule,priority:5"`
	V4    string `json:"v4" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:6;index:idx_casbin_rule,priority:6"`
	V5    string `json:"v5" gorm:"type:varchar(100);uniqueIndex:idx_casbin_rule_unique,priority:7;index:idx_casbin_rule,priority:7"`
}

func (CasbinRule) TableName() string { return "casbin_rule" }

// AuthzRole is an authorization role catalog entry.
type AuthzRole struct {
	Id          uint   `json:"id" gorm:"primaryKey"`
	Key         string `json:"key" gorm:"type:varchar(64);not null;uniqueIndex"`
	Name        string `json:"name" gorm:"type:varchar(100);not null"`
	Description string `json:"description" gorm:"type:text"`
	BuiltIn     bool   `json:"built_in"`
	Enabled     bool   `json:"enabled"`
	Sort        int    `json:"sort"`
	CreatedAt   int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt   int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (AuthzRole) TableName() string { return "authz_roles" }
