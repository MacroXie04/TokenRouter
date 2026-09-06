package channels

// ChannelRequest is the payload to create/edit a channel.
type ChannelRequest struct {
	Name               string `json:"name" binding:"required"`
	Type               int    `json:"type" binding:"required"`
	Key                string `json:"key"`
	OpenAIOrganization string `json:"openai_organization"`
	TestModel          string `json:"test_model"`
	Status             *int   `json:"status"`
	BaseURL            string `json:"base_url"`
	Other              string `json:"other"`
	Models             string `json:"models"`
	Group              string `json:"group"`
	Weight             *uint  `json:"weight"`
	Priority           *int64 `json:"priority"`
	ModelMapping       string `json:"model_mapping"`
	StatusCodeMapping  string `json:"status_code_mapping"`
	AutoBan            *int   `json:"auto_ban"`
	OtherInfo          string `json:"other_info"`
	Tag                string `json:"tag"`
	Remark             string `json:"remark"`
	Setting            string `json:"setting"`
	ParamOverride      string `json:"param_override"`
	HeaderOverride     string `json:"header_override"`
	ChannelInfo        any    `json:"channel_info"`
	Settings           string `json:"settings"`
}
