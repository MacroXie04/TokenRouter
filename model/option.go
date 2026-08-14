package model

// Option is a key/value system setting row.
type Option struct {
	Key   string `json:"key" gorm:"primaryKey;type:varchar(128)"`
	Value string `json:"value" gorm:"type:text"`
}

func (Option) TableName() string { return "options" }
