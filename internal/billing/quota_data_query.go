package billing

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
)

const (
	// DashboardDataMaxRangeSeconds bounds every dashboard histogram query to
	// the same inclusive 30-day window accepted by the frontend.
	DashboardDataMaxRangeSeconds int64 = 30 * 24 * 60 * 60
	// DashboardDataMaxRows caps materialized aggregate rows. Queries fetch one
	// sentinel row beyond the cap so oversized results fail closed rather than
	// returning a silently truncated dashboard.
	DashboardDataMaxRows = 20_000
)

var (
	ErrInvalidDashboardDataRange  = errors.New("invalid dashboard data range")
	ErrDashboardDataRangeTooLarge = errors.New("dashboard data range exceeds 30 days")
	ErrDashboardDataTooLarge      = errors.New("dashboard data exceeds safe limits")
)

// ValidateDashboardDataRange provides defense in depth for callers outside
// the HTTP controllers.
func ValidateDashboardDataRange(startTime, endTime int64) error {
	if startTime <= 0 || endTime <= 0 || endTime < startTime {
		return ErrInvalidDashboardDataRange
	}
	if endTime-startTime > DashboardDataMaxRangeSeconds {
		return ErrDashboardDataRangeTooLarge
	}
	return nil
}

func dashboardDataContextError(ctx context.Context, startTime, endTime int64) error {
	if err := ValidateDashboardDataRange(startTime, endTime); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func boundedDashboardRows[T any](ctx context.Context, rows []T) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) > DashboardDataMaxRows {
		return nil, ErrDashboardDataTooLarge
	}
	return rows, nil
}

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
	return GetAllQuotaDatesContext(context.Background(), startTime, endTime, username)
}

// GetAllQuotaDatesContext is the request-scoped form used by dashboard HTTP
// handlers.
func GetAllQuotaDatesContext(ctx context.Context, startTime, endTime int64, username string) ([]*model.QuotaData, error) {
	if err := dashboardDataContextError(ctx, startTime, endTime); err != nil {
		return nil, err
	}
	if username != "" {
		return GetQuotaDataByUsernameContext(ctx, username, startTime, endTime)
	}
	rows := make([]*model.QuotaData, 0)
	err := model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Select("model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, created_at").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("model_name, created_at").
		Order("created_at ASC").
		Order("model_name ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return boundedDashboardRows(ctx, rows)
}

// GetQuotaDataByUsername returns one user's per-(model, hour) histogram.
func GetQuotaDataByUsername(username string, startTime, endTime int64) ([]*model.QuotaData, error) {
	return GetQuotaDataByUsernameContext(context.Background(), username, startTime, endTime)
}

func GetQuotaDataByUsernameContext(ctx context.Context, username string, startTime, endTime int64) ([]*model.QuotaData, error) {
	if err := dashboardDataContextError(ctx, startTime, endTime); err != nil {
		return nil, err
	}
	rows := make([]*model.QuotaData, 0)
	err := model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("username = ? and created_at >= ? and created_at <= ?", username, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Order("created_at ASC").
		Order("model_name ASC").
		Order("user_id ASC").
		Order("username ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return boundedDashboardRows(ctx, rows)
}

// GetQuotaDataByUserId returns the authenticated user's per-(model, hour)
// histogram.
func GetQuotaDataByUserId(userId int, startTime, endTime int64) ([]*model.QuotaData, error) {
	return GetQuotaDataByUserIDContext(context.Background(), userId, startTime, endTime)
}

func GetQuotaDataByUserIDContext(ctx context.Context, userId int, startTime, endTime int64) ([]*model.QuotaData, error) {
	if err := dashboardDataContextError(ctx, startTime, endTime); err != nil {
		return nil, err
	}
	rows := make([]*model.QuotaData, 0)
	err := model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ? and created_at >= ? and created_at <= ?", userId, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Order("created_at ASC").
		Order("model_name ASC").
		Order("user_id ASC").
		Order("username ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return boundedDashboardRows(ctx, rows)
}

// GetQuotaDataGroupByUser returns the admin per-(username, hour) histogram.
func GetQuotaDataGroupByUser(startTime, endTime int64) ([]*model.QuotaData, error) {
	return GetQuotaDataGroupByUserContext(context.Background(), startTime, endTime)
}

func GetQuotaDataGroupByUserContext(ctx context.Context, startTime, endTime int64) ([]*model.QuotaData, error) {
	if err := dashboardDataContextError(ctx, startTime, endTime); err != nil {
		return nil, err
	}
	rows := make([]*model.QuotaData, 0)
	err := model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Select("username, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("username, created_at").
		Order("created_at ASC").
		Order("username ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return boundedDashboardRows(ctx, rows)
}

// GetFlowQuotaData returns the flow histogram for the calling role (reference
// contract): root sees node/token dimensions, admin sees user/group/model/
// channel, users see their own token/group/model rows. Only grouped usage
// (use_group <> ”) is counted.
func GetFlowQuotaData(startTime, endTime int64, username string, userID, role int) ([]*FlowQuotaData, error) {
	return GetFlowQuotaDataContext(context.Background(), startTime, endTime, username, userID, role)
}

func GetFlowQuotaDataContext(ctx context.Context, startTime, endTime int64, username string, userID, role int) ([]*FlowQuotaData, error) {
	if err := dashboardDataContextError(ctx, startTime, endTime); err != nil {
		return nil, err
	}
	switch {
	case role >= roles.RoleRootUser:
		return getRootFlowQuotaData(ctx, startTime, endTime, username)
	case role >= roles.RoleAdminUser:
		return getAdminFlowQuotaData(ctx, startTime, endTime, username)
	default:
		return getSelfFlowQuotaData(ctx, startTime, endTime, userID)
	}
}

func flowQuotaBaseQuery(ctx context.Context, startTime, endTime int64) *gorm.DB {
	return model.DB.WithContext(ctx).Table(model.QuotaData{}.TableName()).
		Where("use_group <> ''").
		Where("created_at >= ? and created_at <= ?", startTime, endTime)
}

func getSelfFlowQuotaData(ctx context.Context, startTime, endTime int64, userID int) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	err := flowQuotaBaseQuery(ctx, startTime, endTime).
		Select("token_id, use_group, model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ?", userID).
		Group("token_id, use_group, model_name").
		Order("quota DESC").
		Order("token_id ASC").
		Order("use_group ASC").
		Order("model_name ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	rows, err = boundedDashboardRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	return rows, fillFlowTokenNames(ctx, rows)
}

func getAdminFlowQuotaData(ctx context.Context, startTime, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := flowQuotaBaseQuery(ctx, startTime, endTime).
		Select("user_id, username, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, use_group, model_name, channel_id").
		Order("quota DESC").
		Order("user_id ASC").
		Order("username ASC").
		Order("use_group ASC").
		Order("model_name ASC").
		Order("channel_id ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	rows, err = boundedDashboardRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	return rows, fillFlowChannelNames(ctx, rows)
}

func getRootFlowQuotaData(ctx context.Context, startTime, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := flowQuotaBaseQuery(ctx, startTime, endTime).
		Select("user_id, username, node_name, token_id, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, node_name, token_id, use_group, model_name, channel_id").
		Order("quota DESC").
		Order("user_id ASC").
		Order("username ASC").
		Order("node_name ASC").
		Order("token_id ASC").
		Order("use_group ASC").
		Order("model_name ASC").
		Order("channel_id ASC").
		Limit(DashboardDataMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	rows, err = boundedDashboardRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if err := fillFlowTokenNames(ctx, rows); err != nil {
		return rows, err
	}
	return rows, fillFlowChannelNames(ctx, rows)
}

// fillFlowTokenNames resolves token names in one query. Deleted tokens are
// intentionally left unresolved (empty name) so the frontend can render a
// localized deleted label (reference semantics).
func fillFlowTokenNames(ctx context.Context, rows []*FlowQuotaData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		return ctx.Err()
	}
	var tokens []struct {
		Id   int
		Name string
	}
	if err := model.DB.WithContext(ctx).Model(&model.Token{}).Select("id, name").Where("id IN ?", ids).Find(&tokens).Error; err != nil {
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
	return ctx.Err()
}

// fillFlowChannelNames resolves channel names in one query; unknown ids fall
// back to "channel-<id>" (reference semantics).
func fillFlowChannelNames(ctx context.Context, rows []*FlowQuotaData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		return ctx.Err()
	}
	var channels []struct {
		Id   int
		Name string
	}
	if err := model.DB.WithContext(ctx).Table(model.Channel{}.TableName()).Select("id, name").Where("id IN ?", ids).Find(&channels).Error; err != nil {
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
	return ctx.Err()
}
