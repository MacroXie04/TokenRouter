package model

// Channel is an upstream provider channel.
type Channel struct {
	Id                 int     `json:"id" gorm:"primaryKey"`
	Type               int     `json:"type" gorm:"default:0"`
	Key                string  `json:"key" gorm:"not null"`
	OpenAIOrganization string  `json:"openai_organization"`
	TestModel          string  `json:"test_model"`
	Status             int     `json:"status" gorm:"default:1"`
	Name               string  `json:"name" gorm:"index"`
	Weight             *uint   `json:"weight" gorm:"default:0"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint"`
	TestTime           int64   `json:"test_time" gorm:"bigint"`
	ResponseTime       int     `json:"response_time"`
	BaseURL            string  `json:"base_url" gorm:"column:base_url;default:''"`
	Other              string  `json:"other"`
	Balance            float64 `json:"balance"`
	BalanceUpdatedTime int64   `json:"balance_updated_time" gorm:"bigint"`
	Models             string  `json:"models"`
	Group              string  `json:"group" gorm:"type:varchar(64);default:'default'"`
	UsedQuota          int64   `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping       string  `json:"model_mapping" gorm:"type:text"`
	StatusCodeMapping  string  `json:"status_code_mapping" gorm:"type:varchar(1024);default:''"`
	Priority           *int64  `json:"priority" gorm:"bigint"`
	AutoBan            *int    `json:"auto_ban" gorm:"default:1"`
	OtherInfo          string  `json:"other_info"`
	Tag                string  `json:"tag" gorm:"index"`
	Setting            string  `json:"setting" gorm:"type:text"`
	ParamOverride      string  `json:"param_override" gorm:"type:text"`
	HeaderOverride     string  `json:"header_override" gorm:"type:text"`
	Remark             string  `json:"remark" gorm:"type:varchar(255)"`
	ChannelInfo        string  `json:"channel_info" gorm:"type:text"`
	OtherSettings      string  `json:"settings" gorm:"column:settings"`
}

func (Channel) TableName() string { return "channels" }

// Ability is the channel-model routing ability table.
type Ability struct {
	Group     string  `json:"group" gorm:"primaryKey;autoIncrement:false;type:varchar(64)"`
	Model     string  `json:"model" gorm:"primaryKey;autoIncrement:false;type:varchar(255)"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

func (Ability) TableName() string { return "abilities" }
