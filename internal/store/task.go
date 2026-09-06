package store

import (
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
)

const (
	TaskStatusNotStart  = "NOT_START"
	TaskStatusSubmitted = "SUBMITTED"
	TaskStatusQueued    = "QUEUED"
	TaskStatusRunning   = "IN_PROGRESS"
	TaskStatusFailure   = "FAILURE"
	TaskStatusSuccess   = "SUCCESS"
	TaskStatusUnknown   = "UNKNOWN"
)

// Jimeng task-operation states form the primary-database recovery queue for
// asynchronous submissions. A prepared operation has not reached the
// provider and is safe to refund. Dispatching means the network call may have
// been accepted and therefore must never be submitted again. Submitted rows
// have a provider task id in Task.PrivateData and can be polled. Unknown and
// terminal rows are complete recovery outcomes.
const (
	JimengTaskOperationPrepared     = "prepared"
	JimengTaskOperationDispatching  = "dispatching"
	JimengTaskOperationSubmitted    = "submitted"
	JimengTaskOperationUnknown      = "unknown"
	JimengTaskOperationTerminal     = "terminal"
	JimengTaskOperationRefunded     = "refunded"
	JimengTaskOperationManualReview = "manual_review"
)

// Task-operation states form the provider-neutral primary-database recovery
// queue used by asynchronous task adapters. They intentionally mirror the
// Jimeng journal semantics: once an operation is dispatching it is never safe
// to submit again without an upstream idempotency proof.
const (
	TaskOperationPrepared     = "prepared"
	TaskOperationDispatching  = "dispatching"
	TaskOperationSubmitted    = "submitted"
	TaskOperationUnknown      = "unknown"
	TaskOperationTerminal     = "terminal"
	TaskOperationRefunded     = "refunded"
	TaskOperationReversed     = "reversed"
	TaskOperationManualReview = "manual_review"
)

// OpenAI/Sora-compatible video tasks retain the channel family that accepted
// them. Operator and recovery queries must use this exact allowlist and still
// require the Task and TaskOperation platform values to match.
const (
	TaskOperationPlatformOpenAI = "1"
	TaskOperationPlatformSora   = "55"
	// TaskOperationPlatformKling is intentionally not part of the
	// OpenAI/Sora video family. Kling has a different provider protocol,
	// credential format, billing lifecycle, and durable metadata namespace.
	TaskOperationPlatformKling = "50"
	// Suno uses a batch task protocol and a separate recovery queue namespace.
	// It must not be admitted to either the Sora or Kling lifecycle.
	TaskOperationPlatformSuno = "suno"
	// Vidu uses the numeric selected-channel platform retained by the reference
	// generic video task pipeline. Its protocol and recovery state stay
	// separate from the OpenAI/Sora family.
	TaskOperationPlatformVidu = "52"
	// Hailuo retains the selected MiniMax channel type used by the reference
	// generic video task pipeline. Its recovery and metadata namespace is
	// provider-specific and never joins the OpenAI/Sora family.
	TaskOperationPlatformHailuo = "35"
	// Gemini API Veo retains the exact selected channel type. It shares model
	// names with Vertex Veo, but uses a different credential, URL, and recovery
	// namespace and therefore must never be inferred from the model alone.
	TaskOperationPlatformGeminiVeo = "24"
	TaskOperationPlatformVertexVeo = "41"
	// Alibaba Wan retains the exact DashScope channel type. Its provider
	// protocol, credentials, accounting, and recovery namespace are isolated
	// from the OpenAI/Sora video family.
	TaskOperationPlatformAliWan = "17"
	// Doubao video retains the exact selected channel family: the reference
	// accepts both the general VolcEngine channel and the dedicated video
	// channel through one protocol adapter. Neither belongs to Sora.
	TaskOperationPlatformVolcEngine  = "45"
	TaskOperationPlatformDoubaoVideo = "54"
	// Midjourney uses the reference task platform name rather than its numeric
	// channel type because both standard and Plus channels share one recovery
	// queue and wire protocol.
	TaskOperationPlatformMidjourney = "mj"

	// TaskOperationPlatformOpenAIVideo is retained as the legacy Sora-family
	// spelling for callers that create channel-55 fixtures.
	TaskOperationPlatformOpenAIVideo = TaskOperationPlatformSora
)

func IsOpenAIVideoTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformOpenAI || platform == TaskOperationPlatformSora
}

func OpenAIVideoTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformOpenAI, TaskOperationPlatformSora}
}

func IsKlingTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformKling
}

func KlingTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformKling}
}

func IsSunoTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformSuno
}

func SunoTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformSuno}
}

func IsViduTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformVidu
}

func ViduTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformVidu}
}

func IsHailuoTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformHailuo
}

func HailuoTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformHailuo}
}

func IsGeminiVeoTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformGeminiVeo
}

func GeminiVeoTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformGeminiVeo}
}

func IsAliWanTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformAliWan
}

func AliWanTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformAliWan}
}

func IsVeoTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformGeminiVeo || platform == TaskOperationPlatformVertexVeo
}

func VeoTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformGeminiVeo, TaskOperationPlatformVertexVeo}
}

func IsDoubaoVideoTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformVolcEngine || platform == TaskOperationPlatformDoubaoVideo
}

func DoubaoVideoTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformVolcEngine, TaskOperationPlatformDoubaoVideo}
}

func IsMidjourneyTaskOperationPlatform(platform string) bool {
	return platform == TaskOperationPlatformMidjourney
}

func MidjourneyTaskOperationPlatforms() []string {
	return []string{TaskOperationPlatformMidjourney}
}

// Midjourney is a Midjourney task log.
type Midjourney struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	Code        int    `json:"code"`
	UserId      int    `json:"user_id" gorm:"index"`
	Action      string `json:"action" gorm:"type:varchar(40);index"`
	MjId        string `json:"mj_id" gorm:"index"`
	Prompt      string `json:"prompt"`
	PromptEn    string `json:"prompt_en"`
	Description string `json:"description"`
	State       string `json:"state"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	ImageUrl    string `json:"image_url"`
	VideoUrl    string `json:"video_url"`
	VideoUrls   string `json:"video_urls"`
	Status      string `json:"status" gorm:"type:varchar(20);index"`
	Progress    string `json:"progress" gorm:"type:varchar(30);index"`
	FailReason  string `json:"fail_reason"`
	ChannelId   int    `json:"channel_id"`
	Quota       int    `json:"quota"`
	Buttons     string `json:"buttons"`
	Properties  string `json:"properties"`
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
	FailReason  string `json:"fail_reason"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	Progress    string `json:"progress" gorm:"type:varchar(20);index"`
	Properties  string `json:"properties" gorm:"type:text"`
	PrivateData string `json:"-" gorm:"column:private_data;type:text"`
	Data        string `json:"data" gorm:"type:text"`
}

func (Task) TableName() string { return "tasks" }

// JimengTaskOperation is the cluster-visible recovery and polling queue for a
// Jimeng task. Channel credentials live only in Task.PrivateData and are
// encrypted. EncryptedProviderTaskID is an emergency primary-database copy
// used when an accepted response cannot be written to the task row; the raw
// provider identifier is never logged. LeaseOwner is a fencing token: every
// worker write must still own it, so a stale worker cannot overwrite a
// takeover.
type JimengTaskOperation struct {
	ID                      int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	TaskID                  string `json:"task_id" gorm:"type:varchar(191);not null;uniqueIndex"`
	ReservationID           string `json:"reservation_id" gorm:"type:varchar(64);not null;uniqueIndex"`
	UserID                  int    `json:"user_id" gorm:"not null;index"`
	ChannelID               int    `json:"channel_id" gorm:"not null;index"`
	State                   string `json:"state" gorm:"type:varchar(24);not null;index:idx_jimeng_task_recovery,priority:1"`
	SettlementPending       bool   `json:"settlement_pending" gorm:"not null;default:false"`
	EncryptedProviderTaskID string `json:"-" gorm:"type:text"`
	Attempts                int    `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt           int64  `json:"next_attempt_at" gorm:"index:idx_jimeng_task_recovery,priority:2"`
	LeaseOwner              string `json:"lease_owner" gorm:"type:varchar(128);index"`
	LeaseExpiresAt          int64  `json:"lease_expires_at" gorm:"index"`
	LastError               string `json:"last_error" gorm:"type:varchar(255)"`
	CreatedAt               int64  `json:"created_at" gorm:"index"`
	UpdatedAt               int64  `json:"updated_at" gorm:"index"`
	CompletedAt             int64  `json:"completed_at" gorm:"index"`
}

func (JimengTaskOperation) TableName() string { return "jimeng_task_operations" }

// TaskOperation is a cluster-visible recovery and polling queue for async
// providers other than the legacy Jimeng surface. Provider identifiers are
// stored only as authenticated ciphertext. LeaseOwner is a fencing token: a
// worker may mutate the task only while it still owns the exact lease.
type TaskOperation struct {
	ID                      int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	TaskID                  string `json:"task_id" gorm:"type:varchar(191);not null;uniqueIndex"`
	ReservationID           string `json:"reservation_id" gorm:"type:varchar(64);not null;uniqueIndex"`
	Platform                string `json:"platform" gorm:"type:varchar(30);not null;index:idx_task_operation_recovery,priority:1"`
	UserID                  int    `json:"user_id" gorm:"not null;index"`
	ChannelID               int    `json:"channel_id" gorm:"not null;index"`
	State                   string `json:"state" gorm:"type:varchar(24);not null;index:idx_task_operation_recovery,priority:2"`
	SettlementPending       bool   `json:"settlement_pending" gorm:"not null;default:false"`
	EncryptedProviderTaskID string `json:"-" gorm:"type:text"`
	Attempts                int    `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt           int64  `json:"next_attempt_at" gorm:"index:idx_task_operation_recovery,priority:3"`
	LeaseOwner              string `json:"lease_owner" gorm:"type:varchar(128);index"`
	LeaseExpiresAt          int64  `json:"lease_expires_at" gorm:"index"`
	LastError               string `json:"last_error" gorm:"type:varchar(255)"`
	CreatedAt               int64  `json:"created_at" gorm:"index"`
	UpdatedAt               int64  `json:"updated_at" gorm:"index"`
	CompletedAt             int64  `json:"completed_at" gorm:"index"`
}

func (TaskOperation) TableName() string { return "task_operations" }

// GenerateSecureTaskID returns a task identifier suitable for durable task and
// accounting rows. Entropy failure must abort before dispatch or persistence.
func GenerateSecureTaskID() (string, error) {
	suffix, err := cryptoutil.SecureRandomAlphanumeric(32)
	if err != nil {
		return "", err
	}
	return "task_" + suffix, nil
}

// GenerateTaskID is retained for fixtures and non-durable display values.
// Production task creation must use GenerateSecureTaskID.
func GenerateTaskID() string {
	return "task_" + cryptoutil.BestEffortRandomAlphanumeric(32)
}
