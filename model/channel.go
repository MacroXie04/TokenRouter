package model

// Channel is an upstream provider channel.
type Channel struct {
	Id                 int    `json:"id" gorm:"primaryKey"`
	Type               int    `json:"type"`
	Key                string `json:"key" gorm:"not null"`
	OpenAIOrganization string `json:"openai_organization" gorm:"type:varchar(128)"`
	TestModel          string `json:"test_model" gorm:"type:varchar(128)"`
	Status             int    `json:"status"`
	Name               string `json:"name" gorm:"index;type:varchar(64)"`
	Weight             *uint  `json:"weight"`
	CreatedTime        int64  `json:"created_time"`
	TestTime           int64  `json:"test_time"`
	ResponseTime       int    `json:"response_time"`
	BaseURL            string `json:"base_url" gorm:"type:text"`
	Other              string `json:"other" gorm:"type:text"`
	Balance            float64 `json:"balance"`
	BalanceUpdatedTime int64   `json:"balance_updated_time"`
	Models             string  `json:"models" gorm:"type:text"`
	Group              string  `json:"group" gorm:"type:varchar(64)"`
	UsedQuota          int64   `json:"used_quota"`
	ModelMapping       string  `json:"model_mapping" gorm:"type:text"`
	StatusCodeMapping  string  `json:"status_code_mapping" gorm:"type:varchar(1024)"`
	Priority           *int64  `json:"priority"`
	AutoBan            *int    `json:"auto_ban"`
	OtherInfo          string  `json:"other_info" gorm:"type:text"`
	Tag                string  `json:"tag" gorm:"index;type:varchar(64)"`
	Setting            string  `json:"setting" gorm:"type:text"`
	ParamOverride      string  `json:"param_override" gorm:"type:text"`
	HeaderOverride     string  `json:"header_override" gorm:"type:text"`
	Remark             string  `json:"remark" gorm:"type:varchar(255)"`
	ChannelInfo        string  `json:"channel_info" gorm:"type:text"`
	OtherSettings      string  `json:"other_settings" gorm:"column:settings;type:text"`
}

func (Channel) TableName() string { return "channels" }

// Ability is the channel-model routing ability table.
type Ability struct {
	Group     string `json:"group" gorm:"primaryKey;autoIncrement:false;type:varchar(64)"`
	Model     string `json:"model" gorm:"primaryKey;autoIncrement:false;type:varchar(255)"`
	ChannelId int    `json:"channel_id" gorm:"primaryKey;autoIncrement:false"`
	Enabled   bool   `json:"enabled"`
	Priority  *int64 `json:"priority" gorm:"index"`
	Weight    uint   `json:"weight" gorm:"index"`
	Tag       string `json:"tag" gorm:"index;type:varchar(64)"`
}

func (Ability) TableName() string { return "abilities" }
