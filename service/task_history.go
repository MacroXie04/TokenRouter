package service

import (
	"context"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

// TaskHistoryFilter is the dashboard filter set for generic asynchronous tasks.
type TaskHistoryFilter struct {
	Platform       string
	TaskID         string
	Status         string
	Action         string
	ChannelID      string
	StartTimestamp int64
	EndTimestamp   int64
}

// MidjourneyHistoryFilter is the dashboard filter set for Midjourney tasks.
type MidjourneyHistoryFilter struct {
	ChannelID      string
	MJID           string
	StartTimestamp int64
	EndTimestamp   int64
}

// TaskHistoryPage contains a stable task page and admin-only username enrichment.
type TaskHistoryPage struct {
	Items     []model.Task
	Total     int64
	Usernames map[int]string
}

func applyTaskHistoryFilter(query *gorm.DB, filter TaskHistoryFilter) *gorm.DB {
	if filter.ChannelID != "" {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.Platform != "" {
		query = query.Where("platform = ?", filter.Platform)
	}
	if filter.TaskID != "" {
		query = query.Where("task_id = ?", filter.TaskID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Action != "" {
		query = query.Where("action = ?", filter.Action)
	}
	if filter.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", filter.StartTimestamp)
	}
	if filter.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", filter.EndTimestamp)
	}
	return query
}

// GetTaskHistory lists generic asynchronous tasks newest-first. A non-nil
// userID scopes the query and suppresses the channel identifier.
func GetTaskHistory(ctx context.Context, userID *int, offset, limit int, filter TaskHistoryFilter) (*TaskHistoryPage, error) {
	query := model.DB.WithContext(ctx).Model(&model.Task{})
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
		filter.ChannelID = ""
	}
	query = applyTaskHistoryFilter(query, filter)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	items := make([]model.Task, 0)
	itemsQuery := query.Order("id DESC").Limit(limit).Offset(offset)
	if userID != nil {
		itemsQuery = itemsQuery.Omit("channel_id")
	}
	if err := itemsQuery.Find(&items).Error; err != nil {
		return nil, err
	}

	page := &TaskHistoryPage{Items: items, Total: total}
	if userID == nil {
		usernames, err := getTaskHistoryUsernames(ctx, items)
		if err != nil {
			return nil, err
		}
		page.Usernames = usernames
	}
	return page, nil
}

func getTaskHistoryUsernames(ctx context.Context, tasks []model.Task) (map[int]string, error) {
	usernames := make(map[int]string)
	if len(tasks) == 0 {
		return usernames, nil
	}
	ids := make([]int, 0, len(tasks))
	seen := make(map[int]struct{}, len(tasks))
	for _, task := range tasks {
		if _, ok := seen[task.UserId]; ok {
			continue
		}
		seen[task.UserId] = struct{}{}
		ids = append(ids, task.UserId)
	}
	var users []struct {
		ID       int    `gorm:"column:id"`
		Username string `gorm:"column:username"`
	}
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		usernames[user.ID] = user.Username
	}
	return usernames, nil
}

func applyMidjourneyHistoryFilter(query *gorm.DB, filter MidjourneyHistoryFilter) *gorm.DB {
	if filter.ChannelID != "" {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.MJID != "" {
		query = query.Where("mj_id = ?", filter.MJID)
	}
	if filter.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", filter.StartTimestamp)
	}
	if filter.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", filter.EndTimestamp)
	}
	return query
}

// GetMidjourneyHistory lists Midjourney tasks newest-first. A non-nil userID
// scopes the query and disables the admin-only channel filter.
func GetMidjourneyHistory(ctx context.Context, userID *int, offset, limit int, filter MidjourneyHistoryFilter) ([]model.Midjourney, int64, error) {
	query := model.DB.WithContext(ctx).Model(&model.Midjourney{})
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
		filter.ChannelID = ""
	}
	query = applyMidjourneyHistoryFilter(query, filter)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]model.Midjourney, 0)
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetMidjourneyByMJID loads the stored image source for the public proxy.
func GetMidjourneyByMJID(ctx context.Context, mjID string) (*model.Midjourney, error) {
	var task model.Midjourney
	if err := model.DB.WithContext(ctx).Where("mj_id = ?", mjID).First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}
