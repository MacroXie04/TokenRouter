package model

// Setup is a single-row install/version marker.
type Setup struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	Version       string `json:"version" gorm:"type:varchar(50);not null"`
	InitializedAt int64  `json:"initialized_at" gorm:"not null"`
}

func (Setup) TableName() string { return "setups" }

// SystemInstance is a cluster node registration/heartbeat.
type SystemInstance struct {
	NodeName  string `json:"node_name" gorm:"primaryKey;type:varchar(128)"`
	Info      string `json:"info" gorm:"type:text"`
	StartedAt int64  `json:"started_at" gorm:"index"`
	LastSeenAt int64 `json:"last_seen_at" gorm:"index"`
	CreatedAt int64  `json:"created_at" gorm:"index"`
	UpdatedAt int64  `json:"updated_at" gorm:"index"`
}

func (SystemInstance) TableName() string { return "system_instances" }

// SystemTask is a background job task. ActiveKey is the task type while the
// task is pending or running (enforcing one active task per type); it is
// cleared on completion so past runs never block new ones.
type SystemTask struct {
	ID        int64   `json:"id" gorm:"primaryKey"`
	TaskID    string  `json:"task_id" gorm:"type:varchar(64);uniqueIndex"`
	Type      string  `json:"type" gorm:"type:varchar(64);index"`
	Status    string  `json:"status" gorm:"type:varchar(32);index"`
	ActiveKey *string `json:"active_key,omitempty" gorm:"type:varchar(64);uniqueIndex"`
	Payload   string `json:"payload" gorm:"type:text"`
	State     string `json:"state" gorm:"type:text"`
	Result    string `json:"result" gorm:"type:text"`
	Error     string `json:"error" gorm:"type:text"`
	LockedBy  string `json:"locked_by" gorm:"type:varchar(128);index"`
	CreatedAt int64  `json:"created_at" gorm:"index"`
	UpdatedAt int64  `json:"updated_at" gorm:"index"`
}

func (SystemTask) TableName() string { return "system_tasks" }

// SystemTaskLock is a distributed task lock keyed by task type.
type SystemTaskLock struct {
	Type        string `json:"type" gorm:"primaryKey;type:varchar(64)"`
	TaskID      string `json:"task_id" gorm:"type:varchar(64);index"`
	LockedBy    string `json:"locked_by" gorm:"type:varchar(128);index"`
	LockedUntil int64  `json:"locked_until" gorm:"index"`
	UpdatedAt   int64  `json:"updated_at" gorm:"index"`
}

func (SystemTaskLock) TableName() string { return "system_task_locks" }

// SystemInstanceStaleAfterSeconds is the reference staleness threshold.
const SystemInstanceStaleAfterSeconds int64 = 90

const (
	SystemInstanceStatusOnline = "online"
	SystemInstanceStatusStale  = "stale"
)

// SystemInstanceResponse is the admin-facing instance shape.
type SystemInstanceResponse struct {
	NodeName          string `json:"node_name"`
	Status            string `json:"status"`
	StaleAfterSeconds int64  `json:"stale_after_seconds"`
	StartedAt         int64  `json:"started_at"`
	LastSeenAt        int64  `json:"last_seen_at"`
	Info              any    `json:"info"`
}

// ToResponse derives the live/stale status against now (reference contract).
func (instance *SystemInstance) ToResponse(now int64) SystemInstanceResponse {
	status := SystemInstanceStatusOnline
	if now-instance.LastSeenAt > SystemInstanceStaleAfterSeconds {
		status = SystemInstanceStatusStale
	}
	return SystemInstanceResponse{
		NodeName:          instance.NodeName,
		Status:            status,
		StaleAfterSeconds: SystemInstanceStaleAfterSeconds,
		StartedAt:         instance.StartedAt,
		LastSeenAt:        instance.LastSeenAt,
		Info:              decodeSystemTaskJSONValue(instance.Info),
	}
}

// ListSystemInstances returns every registered node, newest heartbeat first.
func ListSystemInstances() ([]*SystemInstance, error) {
	var instances []*SystemInstance
	err := DB.Order("last_seen_at desc").Find(&instances).Error
	return instances, err
}

// DeleteStaleSystemInstances removes nodes whose heartbeat is older than the
// stale threshold.
func DeleteStaleSystemInstances(now int64) (int64, error) {
	result := DB.Where("last_seen_at < ?", now-SystemInstanceStaleAfterSeconds).Delete(&SystemInstance{})
	return result.RowsAffected, result.Error
}

// DeleteStaleSystemInstance removes one node only when its heartbeat is
// stale; reports whether anything was deleted.
func DeleteStaleSystemInstance(nodeName string, now int64) (bool, error) {
	result := DB.Where("node_name = ? AND last_seen_at < ?", nodeName, now-SystemInstanceStaleAfterSeconds).Delete(&SystemInstance{})
	return result.RowsAffected > 0, result.Error
}
