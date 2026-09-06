package service

import (
	"context"
	"errors"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

var systemInstanceStartedAt = time.Now().Unix()

type SystemInstanceInfo struct {
	SchemaVersion int                     `json:"schema_version"`
	Node          SystemInstanceNodeInfo  `json:"node"`
	Role          SystemInstanceRoleInfo  `json:"role"`
	Runtime       SystemInstanceRuntime   `json:"runtime"`
	Host          SystemInstanceHostInfo  `json:"host"`
	Resources     SystemInstanceResources `json:"resources"`
}

type SystemInstanceNodeInfo struct {
	Name                    string `json:"name"`
	Source                  string `json:"source"`
	ManuallyConfigured      bool   `json:"manually_configured"`
	ShouldConfigureManually bool   `json:"should_configure_manually"`
}

type SystemInstanceRoleInfo struct {
	// TokenRouter uses distributed leases rather than a statically selected
	// master, so every healthy node is capable of running singleton jobs.
	IsMaster bool `json:"is_master"`
}

type SystemInstanceRuntime struct {
	Version   string `json:"version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	StartedAt int64  `json:"started_at"`
}

type SystemInstanceHostInfo struct {
	Hostname string `json:"hostname"`
}

type SystemInstanceResources struct {
	CPU     SystemInstanceResourceUsage  `json:"cpu"`
	Memory  SystemInstanceResourceUsage  `json:"memory"`
	Storage SystemInstanceStorageMetrics `json:"storage"`
}

type SystemInstanceResourceUsage struct {
	UsagePercent float64 `json:"usage_percent"`
}

type SystemInstanceStorageMetrics struct {
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

// NodeName returns the configured node name for this instance.
func NodeName() string {
	return strings.TrimSpace(common.GetEnv("NODE_NAME", "tokenrouter-node-1"))
}

// RegisterSystemInstance registers (or heartbeats) the current node.
func RegisterSystemInstance() error {
	return RegisterSystemInstanceContext(context.Background())
}

// RegisterSystemInstanceContext is RegisterSystemInstance's cancellable form.
// Runtime metric collection is local and synchronous; cancellation is checked
// both before and after it, and the database heartbeat uses the supplied ctx.
func RegisterSystemInstanceContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("system-instance context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := NodeName()
	info := currentSystemInstanceInfo(name)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Repair only this node's row when the former process-clock implementation
	// left it more than the 90-second tolerance ahead of the database. The
	// predicate samples database time inside its DELETE statement, so a delayed
	// invocation cannot remove a concurrently newer, legitimate heartbeat.
	if _, err := model.DeleteFutureSystemInstanceContext(ctx, name); err != nil {
		return err
	}
	// Stamp the heartbeat after local metric collection so a slow disk probe
	// cannot consume part of the liveness window before the write begins.
	now, err := model.PrimaryDatabaseUnixTimestamp(ctx)
	if err != nil {
		return err
	}
	return model.UpsertSystemInstanceContext(ctx, name, info, systemInstanceStartedAt, now)
}

func currentSystemInstanceInfo(name string) SystemInstanceInfo {
	hostname, _ := os.Hostname()
	hostname = boundedInstanceText(hostname, 255)
	configured := strings.TrimSpace(os.Getenv("NODE_NAME")) != ""

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	memoryPercent := float64(0)
	if memory.Sys > 0 {
		memoryPercent = boundedPercent(float64(memory.Alloc) / float64(memory.Sys) * 100)
	}
	disk := performanceDiskSpaceInfo(".")

	return SystemInstanceInfo{
		SchemaVersion: 1,
		Node: SystemInstanceNodeInfo{
			Name:                    name,
			Source:                  map[bool]string{true: "environment", false: "default"}[configured],
			ManuallyConfigured:      configured,
			ShouldConfigureManually: !configured,
		},
		Role: SystemInstanceRoleInfo{IsMaster: true},
		Runtime: SystemInstanceRuntime{
			Version:   boundedInstanceText(common.Version, 64),
			GOOS:      boundedInstanceText(runtime.GOOS, 32),
			GOARCH:    boundedInstanceText(runtime.GOARCH, 32),
			StartedAt: systemInstanceStartedAt,
		},
		Host: SystemInstanceHostInfo{Hostname: hostname},
		Resources: SystemInstanceResources{
			CPU:    SystemInstanceResourceUsage{UsagePercent: 0},
			Memory: SystemInstanceResourceUsage{UsagePercent: memoryPercent},
			Storage: SystemInstanceStorageMetrics{
				TotalBytes:  disk.Total,
				UsedBytes:   disk.Used,
				FreeBytes:   disk.Free,
				UsedPercent: boundedPercent(disk.UsedPercent),
			},
		},
	}
}

func boundedInstanceText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

func boundedPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// GetSystemInstances lists registered nodes, most recently seen first.
func GetSystemInstances() ([]model.SystemInstance, error) {
	var instances []model.SystemInstance
	if err := model.DB.Order("last_seen_at desc").Find(&instances).Error; err != nil {
		return nil, err
	}
	return instances, nil
}

// IsInstanceStale reports whether a node's heartbeat falls outside the given
// past/future tolerance (default 90s), using the primary database clock.
func IsInstanceStale(instance *model.SystemInstance, thresholdSeconds int64) bool {
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return true
	}
	return IsInstanceStaleAt(instance, now, thresholdSeconds)
}

// IsInstanceStaleAt compares a heartbeat against an explicit shared-clock
// value. Keeping the boundary pure makes both past and future 90/91-second
// boundaries deterministic and prevents callers from silently choosing a
// process clock.
func IsInstanceStaleAt(instance *model.SystemInstance, now, thresholdSeconds int64) bool {
	if instance == nil {
		return true
	}
	if thresholdSeconds <= 0 {
		thresholdSeconds = 90
	}
	staleBefore := int64(math.MinInt64)
	if now >= math.MinInt64+thresholdSeconds {
		staleBefore = now - thresholdSeconds
	}
	invalidAfter := int64(math.MaxInt64)
	if now <= math.MaxInt64-thresholdSeconds {
		invalidAfter = now + thresholdSeconds
	}
	return instance.LastSeenAt < staleBefore || instance.LastSeenAt > invalidAfter
}
