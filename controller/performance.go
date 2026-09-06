package controller

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

const maximumPerformanceCleanupValue = 36500

var performanceControllerMutationMu sync.Mutex

func performanceErrorMessage(err error, fallback string) string {
	switch {
	case errors.Is(err, service.ErrPerformanceInvalidConfig):
		return "invalid performance configuration"
	case errors.Is(err, service.ErrPerformanceUnsafePath):
		return "unsafe directory configuration"
	case errors.Is(err, service.ErrPerformanceDirectoryLimit):
		return "directory entry limit exceeded"
	default:
		return fallback
	}
}

func performanceError(c *gin.Context, err error, fallback string) {
	common.LogError("performance management operation failed", "operation", c.FullPath(), "error_type", fmt.Sprintf("%T", err))
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": performanceErrorMessage(err, fallback),
	})
}

// GetPerformanceStats returns the reference-compatible cache, Go runtime,
// disk, and configuration snapshot. Filesystem enumeration is bounded by the
// service and never follows entries outside the managed cache directory.
func GetPerformanceStats(c *gin.Context) {
	stats, err := service.GetPerformanceStats()
	if err != nil {
		performanceError(c, err, "unable to collect performance statistics")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": stats})
}

// ClearDiskCache removes only regular cache files inactive for ten minutes.
func ClearDiskCache(c *gin.Context) {
	performanceControllerMutationMu.Lock()
	deleted, err := service.CleanupInactiveDiskCache(10 * time.Minute)
	performanceControllerMutationMu.Unlock()
	if err != nil {
		performanceError(c, err, "unable to clear inactive disk cache")
		return
	}
	common.LogInfo("performance management operation completed",
		"action", "performance.clear_disk_cache", "operator_id", common.GetUserId(c), "deleted_count", deleted)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "不活跃的磁盘缓存已清理",
	})
}

// ResetPerformanceStats resets process-local cache hit totals without
// changing the current cache usage snapshot.
func ResetPerformanceStats(c *gin.Context) {
	performanceControllerMutationMu.Lock()
	service.ResetPerformanceStats()
	performanceControllerMutationMu.Unlock()
	common.LogInfo("performance management operation completed",
		"action", "performance.reset_stats", "operator_id", common.GetUserId(c))
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "统计信息已重置",
	})
}

// ForceGC executes one synchronous Go garbage-collection cycle.
func ForceGC(c *gin.Context) {
	performanceControllerMutationMu.Lock()
	runtime.GC()
	performanceControllerMutationMu.Unlock()
	common.LogInfo("performance management operation completed",
		"action", "performance.gc", "operator_id", common.GetUserId(c))
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "GC 已执行",
	})
}

// GetLogFiles returns bounded metadata for regular oneapi-*.log files in the
// explicitly configured log directory.
func GetLogFiles(c *gin.Context) {
	files, err := service.GetPerformanceLogFiles()
	if err != nil {
		performanceError(c, err, "unable to inspect log directory")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    files,
	})
}

// CleanupLogFiles implements the reference by_count/by_days retention modes
// with an explicit upper bound and regular-file-only deletion.
func CleanupLogFiles(c *gin.Context) {
	mode := c.Query("mode")
	if mode != "by_count" && mode != "by_days" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid mode, must be by_count or by_days"})
		return
	}
	value, err := strconv.Atoi(c.Query("value"))
	if err != nil || value < 1 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid value, must be a positive integer"})
		return
	}
	if value > maximumPerformanceCleanupValue {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid value, maximum is 36500"})
		return
	}

	performanceControllerMutationMu.Lock()
	result, err := service.CleanupPerformanceLogFiles(mode, value)
	performanceControllerMutationMu.Unlock()
	if err != nil {
		if errors.Is(err, service.ErrPerformanceLogNotConfigured) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "log directory not configured"})
			return
		}
		performanceError(c, err, "unable to clean log files")
		return
	}
	common.LogInfo("performance management operation completed",
		"action", "performance.clear_logs", "operator_id", common.GetUserId(c),
		"deleted_count", result.DeletedCount, "failed_count", len(result.FailedFiles), "freed_bytes", result.FreedBytes)
	if len(result.FailedFiles) > 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": fmt.Sprintf("部分文件删除失败（%d/%d）", len(result.FailedFiles),
				len(result.FailedFiles)+result.DeletedCount),
			"data": result,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    result,
	})
}
