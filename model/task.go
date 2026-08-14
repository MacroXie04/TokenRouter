package model

import "github.com/tokenrouter/tokenrouter/common"

const (
	TaskStatusNotStart  = "NOT_START"
	TaskStatusSubmitted = "SUBMITTED"
	TaskStatusQueued    = "QUEUED"
	TaskStatusRunning   = "IN_PROGRESS"
	TaskStatusFailure   = "FAILURE"
	TaskStatusSuccess   = "SUCCESS"
	TaskStatusUnknown   = "UNKNOWN"
)

// Midjourney is a Midjourney task log.
type Midjourney struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	Code        int    `json:"code"`
	UserId      int    `json:"user_id" gorm:"index"`
	Action      string `json:"action" gorm:"type:varchar(40);index"`
	MjId        string `json:"mj_id" gorm:"index;type:varchar(64)"`
	Prompt      string `json:"prompt" gorm:"type:text"`
	PromptEn    string `json:"prompt_en" gorm:"type:text"`
	Description string `json:"description" gorm:"type:text"`
	State       string `json:"state" gorm:"type:varchar(40)"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	ImageUrl    string `json:"image_url" gorm:"type:text"`
	VideoUrl    string `json:"video_url" gorm:"type:text"`
	VideoUrls   string `json:"video_urls" gorm:"type:text"`
	Status      string `json:"status" gorm:"type:varchar(20);index"`
	Progress    string `json:"progress" gorm:"type:varchar(30);index"`
	FailReason  string `json:"fail_reason" gorm:"type:text"`
	ChannelId   int    `json:"channel_id"`
	Quota       int    `json:"quota"`
	Buttons     string `json:"buttons" gorm:"type:text"`
	Properties  string `json:"properties" gorm:"type:text"`
}

func (Midjourney) TableName() string { return "midjourneys" }

// Task is an async task (song/lyrics/video) record.
type Task struct {
	ID          int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	CreatedAt   int64  `json:"created_at" gorm:"index"`
	UpdatedAt   int64  `json:"updated_at"`
	TaskID      string `json:"task_id" gorm:"type:varchar(191);index"`
	Platform    string `json:"platform" gorm:"type:varchar(30);index"`
	UserId      int    `json:"user_id" gorm:"index"`
	Group       string `json:"group" gorm:"type:varchar(50)"`
	ChannelId   int    `json:"channel_id" gorm:"index"`
	Quota       int    `json:"quota"`
	Action      string `json:"action" gorm:"type:varchar(40);index"`
	Status      string `json:"status" gorm:"type:varchar(20);index"`
	FailReason  string `json:"fail_reason" gorm:"type:text"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	Progress    string `json:"progress" gorm:"type:varchar(20);index"`
	Properties  string `json:"properties" gorm:"type:text"`
	PrivateData string `json:"-" gorm:"column:private_data;type:text"`
	Data        string `json:"data" gorm:"type:text"`
}

func (Task) TableName() string { return "tasks" }

func GenerateTaskID() string {
	return "task_" + common.RandomAlphanumeric(32)
}
