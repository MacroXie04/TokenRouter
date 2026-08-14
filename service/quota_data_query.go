package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

// FlowQuotaData is the reference flow-analytics row (token/channel names are
// resolved after the aggregation and never stored).
type FlowQuotaData struct {
	UserID      int    `json:"user_id,omitempty" gorm:"column:user_id"`
	Username    string `json:"username,omitempty" gorm:"column:username"`
	NodeName    string `json:"node_name,omitempty" gorm:"column:node_name"`
	TokenID     int    `json:"token_id,omitempty" gorm:"column:token_id"`
	TokenName   string `json:"token_name,omitempty" gorm:"-"`
	UseGroup    string `json:"use_group" gorm:"column:use_group"`
	ChannelID   int    `json:"channel_id,omitempty" gorm:"column:channel_id"`
	ChannelName string `json:"channel_name,omitempty" gorm:"-"`
	ModelName   string `json:"model_name" gorm:"column:model_name"`
	TokenUsed   int    `json:"token_used" gorm:"column:token_used"`
	Count       int    `json:"count" gorm:"column:count"`
	Quota       int    `json:"quota" gorm:"column:quota"`
}

// GetAllQuotaDates returns the admin quota histogram grouped by
// (model_name, created_at), or by (user_id, username, model_name, created_at)
// when a username filter is given (reference semantics).
func GetAllQuotaDates(startTime, endTime int64, username string) ([]*model.QuotaData, error) {
	if username != "" {
		return GetQuotaDataByUsername(username, startTime, endTime)
	}
	var rows []*model.QuotaData
	err := model.DB.Table(model.QuotaData{}.TableName()).
		Select("model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, created_at").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("model_name, created_at").
		Find(&rows).Error
	return rows, err
}

// GetQuotaDataByUsername returns one user's per-(model, hour) histogram.
func GetQuotaDataByUsername(username string, startTime, endTime int64) ([]*model.QuotaData, error) {
	var rows []*model.QuotaData
	err := model.DB.Table(model.QuotaData{}.TableName()).
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("username = ? and created_at >= ? and created_at <= ?", username, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Find(&rows).Error
	return rows, err
}

// GetQuotaDataByUserId returns the authenticated user's per-(model, hour)
// histogram.
func GetQuotaDataByUserId(userId int, startTime, endTime int64) ([]*model.QuotaData, error) {
	var rows []*model.QuotaData
	err := model.DB.Table(model.QuotaData{}.TableName()).
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ? and created_at >= ? and created_at <= ?", userId, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Find(&rows).Error
	return rows, err
}

// GetQuotaDataGroupByUser returns the admin per-(username, hour) histogram.
func GetQuotaDataGroupByUser(startTime, endTime int64) ([]*model.QuotaData, error) {
	var rows []*model.QuotaData
	err := model.DB.Table(model.QuotaData{}.TableName()).
		Select("username, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("username, created_at").
		Find(&rows).Error
	return rows, err
}

// GetFlowQuotaData returns the flow histogram for the calling role (reference
// contract): root sees node/token dimensions, admin sees user/group/model/
// channel, users see their own token/group/model rows. Only grouped usage
// (use_group <> '') is counted.
func GetFlowQuotaData(startTime, endTime int64, username string, userID, role int) ([]*FlowQuotaData, error) {
	switch {
	case role >= constant.RoleRootUser:
		return getRootFlowQuotaData(startTime, endTime, username)
	case role >= constant.RoleAdminUser:
		return getAdminFlowQuotaData(startTime, endTime, username)
	default:
		return getSelfFlowQuotaData(startTime, endTime, userID)
	}
}

func flowQuotaBaseQuery(startTime, endTime int64) *gorm.DB {
	return model.DB.Table(model.QuotaData{}.TableName()).
		Where("use_group <> ''").
		Where("created_at >= ? and created_at <= ?", startTime, endTime)
}

func getSelfFlowQuotaData(startTime, endTime int64, userID int) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	err := flowQuotaBaseQuery(startTime, endTime).
		Select("token_id, use_group, model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ?", userID).
		Group("token_id, use_group, model_name").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, fillFlowTokenNames(rows)
}

func getAdminFlowQuotaData(startTime, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := flowQuotaBaseQuery(startTime, endTime).
		Select("user_id, username, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, use_group, model_name, channel_id").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, fillFlowChannelNames(rows)
}

func getRootFlowQuotaData(startTime, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := flowQuotaBaseQuery(startTime, endTime).
		Select("user_id, username, node_name, token_id, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, node_name, token_id, use_group, model_name, channel_id").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if err := fillFlowTokenNames(rows); err != nil {
		return rows, err
	}
	return rows, fillFlowChannelNames(rows)
}

// fillFlowTokenNames resolves token names in one query. Deleted tokens are
// intentionally left unresolved (empty name) so the frontend can render a
// localized deleted label (reference semantics).
func fillFlowTokenNames(rows []*FlowQuotaData) error {
	ids := make([]int, 0)
	seen := make(map[int]struct{})
	for _, row := range rows {
		if row.TokenID == 0 {
			continue
		}
		if _, ok := seen[row.TokenID]; ok {
			continue
		}
		seen[row.TokenID] = struct{}{}
		ids = append(ids, row.TokenID)
	}
	if len(ids) == 0 {
		return nil
	}
	var tokens []struct {
		Id   int
		Name string
	}
	if err := model.DB.Model(&model.Token{}).Select("id, name").Where("id IN ?", ids).Find(&tokens).Error; err != nil {
		return err
	}
	names := make(map[int]string, len(tokens))
	for _, token := range tokens {
		names[token.Id] = token.Name
	}
	for _, row := range rows {
		if name := names[row.TokenID]; name != "" {
			row.TokenName = name
		}
	}
	return nil
}

// fillFlowChannelNames resolves channel names in one query; unknown ids fall
// back to "channel-<id>" (reference semantics).
func fillFlowChannelNames(rows []*FlowQuotaData) error {
	ids := make([]int, 0)
	seen := make(map[int]struct{})
	for _, row := range rows {
		if row.ChannelID == 0 {
			continue
		}
		if _, ok := seen[row.ChannelID]; ok {
			continue
		}
		seen[row.ChannelID] = struct{}{}
		ids = append(ids, row.ChannelID)
	}
	if len(ids) == 0 {
		return nil
	}
	var channels []struct {
		Id   int
		Name string
	}
	if err := model.DB.Table(model.Channel{}.TableName()).Select("id, name").Where("id IN ?", ids).Find(&channels).Error; err != nil {
		return err
	}
	names := make(map[int]string, len(channels))
	for _, ch := range channels {
		if ch.Name != "" {
			names[ch.Id] = ch.Name
		}
	}
	for _, row := range rows {
		if name := names[row.ChannelID]; name != "" {
			row.ChannelName = name
			continue
		}
		row.ChannelName = fmt.Sprintf("channel-%d", row.ChannelID)
	}
	return nil
}
