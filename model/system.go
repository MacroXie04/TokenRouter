package model

import (
	"context"
	"errors"
	"math"
	"strings"

	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
)

var ErrInvalidSystemInstance = errors.New("invalid system instance")

const maxSystemInstanceInfoBytes = 64 << 10

// Setup is a single-row install/version marker.
type Setup struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	Version       string `json:"version" gorm:"type:varchar(50);not null"`
	InitializedAt int64  `json:"initialized_at" gorm:"not null"`
}

func (Setup) TableName() string { return "setups" }

// SystemInstance is a cluster node registration/heartbeat.
type SystemInstance struct {
	NodeName   string `json:"node_name" gorm:"primaryKey;type:varchar(128)"`
	Info       string `json:"info" gorm:"type:text"`
	StartedAt  int64  `json:"started_at" gorm:"index"`
	LastSeenAt int64  `json:"last_seen_at" gorm:"index"`
	CreatedAt  int64  `json:"created_at" gorm:"index"`
	UpdatedAt  int64  `json:"updated_at" gorm:"index"`
}

func (SystemInstance) TableName() string { return "system_instances" }

// UpsertSystemInstance creates or atomically refreshes one cluster
// registration. The conflict update is a single portable statement, avoiding
// duplicate-key races between overlapping local heartbeat runs. A delayed
// heartbeat may never overwrite a newer observation of the same node. This
// low-level primitive does not treat an older heartbeat as a repair: the
// service first uses a node-scoped, database-clock-guarded delete for legacy
// far-future rows so monotonic fencing still protects newer valid observations.
func UpsertSystemInstance(nodeName string, info any, startedAt, lastSeenAt int64) error {
	return UpsertSystemInstanceContext(context.Background(), nodeName, info, startedAt, lastSeenAt)
}

// UpsertSystemInstanceContext is UpsertSystemInstance's cancellable form.
func UpsertSystemInstanceContext(ctx context.Context, nodeName string, info any, startedAt, lastSeenAt int64) error {
	if ctx == nil {
		return ErrInvalidSystemInstance
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" || len(nodeName) > 128 || startedAt < 0 || lastSeenAt <= 0 ||
		lastSeenAt > math.MaxInt64-SystemInstanceFutureToleranceSeconds {
		return ErrInvalidSystemInstance
	}
	if startedAt == 0 {
		startedAt = lastSeenAt
	}
	infoText := ""
	if info != nil {
		encoded, err := common.Marshal(info)
		if err != nil || len(encoded) > maxSystemInstanceInfoBytes {
			return ErrInvalidSystemInstance
		}
		infoText = string(encoded)
	}
	instance := &SystemInstance{
		NodeName:   nodeName,
		Info:       infoText,
		StartedAt:  startedAt,
		LastSeenAt: lastSeenAt,
		CreatedAt:  lastSeenAt,
		UpdatedAt:  lastSeenAt,
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep last_seen_at last: MySQL evaluates ON DUPLICATE KEY assignments from
	// left to right, while PostgreSQL and SQLite evaluate against the old row.
	// Every conditional assignment must therefore observe the old heartbeat.
	updates := clause.Set{
		{Column: clause.Column{Name: "info"}, Value: clause.Expr{
			SQL: "CASE WHEN ? > last_seen_at THEN ? ELSE info END", Vars: []any{lastSeenAt, infoText},
		}},
		{Column: clause.Column{Name: "started_at"}, Value: clause.Expr{
			SQL: "CASE WHEN ? > last_seen_at THEN ? ELSE started_at END", Vars: []any{lastSeenAt, startedAt},
		}},
		{Column: clause.Column{Name: "updated_at"}, Value: clause.Expr{
			SQL: "CASE WHEN ? > last_seen_at THEN ? ELSE updated_at END", Vars: []any{lastSeenAt, lastSeenAt},
		}},
		{Column: clause.Column{Name: "last_seen_at"}, Value: clause.Expr{
			SQL: "CASE WHEN ? > last_seen_at THEN ? ELSE last_seen_at END", Vars: []any{lastSeenAt, lastSeenAt},
		}},
	}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_name"}},
		DoUpdates: updates,
	}).Create(instance).Error
}

// DeleteFutureSystemInstanceContext removes one legacy process-clock row only
// when its heartbeat is beyond the primary database clock at execution time by
// more than the configured tolerance. The database-time comparison deliberately
// lives in the DELETE statement: a delayed caller cannot carry an old clock
// sample forward and accidentally remove a newer, valid heartbeat.
func DeleteFutureSystemInstanceContext(ctx context.Context, nodeName string) (bool, error) {
	if ctx == nil {
		return false, ErrInvalidSystemInstance
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" || len(nodeName) > 128 {
		return false, ErrInvalidSystemInstance
	}
	if DB == nil {
		return false, errors.New("database is nil")
	}
	clockExpression, err := databaseTimeExpression(DB.Dialector.Name())
	if err != nil {
		return false, err
	}

	// Evaluate the database clock inside a scalar subquery so the statement has
	// one clock sample even on PostgreSQL, where clock_timestamp() is volatile.
	// Saturating the addition preserves the comparison for the full int64 range.
	thresholdExpression := "(SELECT CASE WHEN database_now > ? THEN ? ELSE database_now + ? END " +
		"FROM (SELECT " + clockExpression + " AS database_now) AS system_instance_clock)"
	result := DB.WithContext(ctx).
		Where("node_name = ? AND last_seen_at > "+thresholdExpression,
			nodeName,
			int64(math.MaxInt64-SystemInstanceFutureToleranceSeconds),
			int64(math.MaxInt64),
			SystemInstanceFutureToleranceSeconds,
		).
		Delete(&SystemInstance{})
	return result.RowsAffected > 0, result.Error
}

// SystemTask is a background job task. ActiveKey is the task type while the
// task is pending or running (enforcing one active task per type); it is
// cleared on completion so past runs never block new ones.
type SystemTask struct {
	ID        int64   `json:"id" gorm:"primaryKey"`
	TaskID    string  `json:"task_id" gorm:"type:varchar(64);uniqueIndex"`
	Type      string  `json:"type" gorm:"type:varchar(64);index;index:idx_system_tasks_schedule,priority:1"`
	Status    string  `json:"status" gorm:"type:varchar(32);index;index:idx_system_tasks_schedule,priority:2"`
	ActiveKey *string `json:"active_key,omitempty" gorm:"type:varchar(64);uniqueIndex"`
	Payload   string  `json:"payload" gorm:"type:text"`
	State     string  `json:"state" gorm:"type:text"`
	Result    string  `json:"result" gorm:"type:text"`
	Error     string  `json:"error" gorm:"type:text"`
	LockedBy  string  `json:"locked_by" gorm:"type:varchar(128);index"`
	CreatedAt int64   `json:"created_at" gorm:"index"`
	UpdatedAt int64   `json:"updated_at" gorm:"index;index:idx_system_tasks_schedule,priority:3"`
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

// SystemInstanceFutureToleranceSeconds permits small clock adjustments while
// treating legacy process-clock heartbeats far ahead of the database as
// invalid. A node's service heartbeat can repair only its own such row before
// upsert; the administrative stale cleanup can also remove it.
const SystemInstanceFutureToleranceSeconds int64 = SystemInstanceStaleAfterSeconds

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
	staleBefore, invalidAfter := systemInstanceHeartbeatBounds(now)
	if instance.LastSeenAt < staleBefore || instance.LastSeenAt > invalidAfter {
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

// DeleteStaleSystemInstances removes nodes whose heartbeat is outside the
// allowed past/future window around the caller's trusted clock.
func DeleteStaleSystemInstances(now int64) (int64, error) {
	staleBefore, invalidAfter := systemInstanceHeartbeatBounds(now)
	result := DB.Where("last_seen_at < ? OR last_seen_at > ?", staleBefore, invalidAfter).Delete(&SystemInstance{})
	return result.RowsAffected, result.Error
}

// DeleteStaleSystemInstance removes one node only when its heartbeat is outside
// the allowed past/future window; reports whether anything was deleted.
func DeleteStaleSystemInstance(nodeName string, now int64) (bool, error) {
	staleBefore, invalidAfter := systemInstanceHeartbeatBounds(now)
	result := DB.Where("node_name = ? AND (last_seen_at < ? OR last_seen_at > ?)", nodeName, staleBefore, invalidAfter).
		Delete(&SystemInstance{})
	return result.RowsAffected > 0, result.Error
}

func systemInstanceHeartbeatBounds(now int64) (staleBefore, invalidAfter int64) {
	if now < math.MinInt64+SystemInstanceStaleAfterSeconds {
		staleBefore = math.MinInt64
	} else {
		staleBefore = now - SystemInstanceStaleAfterSeconds
	}
	if now > math.MaxInt64-SystemInstanceFutureToleranceSeconds {
		invalidAfter = math.MaxInt64
	} else {
		invalidAfter = now + SystemInstanceFutureToleranceSeconds
	}
	return staleBefore, invalidAfter
}
