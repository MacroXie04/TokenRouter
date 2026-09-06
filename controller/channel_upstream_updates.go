package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

type channelUpstreamModelUpdateRequest struct {
	ID           int      `json:"id"`
	AddModels    []string `json:"add_models"`
	RemoveModels []string `json:"remove_models"`
	IgnoreModels []string `json:"ignore_models"`
}

func channelUpstreamOperationError(c *gin.Context, err error) {
	message := "upstream model operation failed"
	if errors.Is(err, gorm.ErrRecordNotFound) {
		message = "channel not found"
	} else if err != nil {
		// Service errors are deliberately credential-free. Returning validation
		// detail is useful to an authenticated operator and mirrors the dashboard
		// API's success=false envelope.
		message = err.Error()
	}
	c.JSON(http.StatusOK, gin.H{"success": false, "message": message})
}

// ApplyChannelUpstreamModelUpdates atomically accepts or ignores a selected
// subset of the currently staged changes for one channel.
func ApplyChannelUpstreamModelUpdates(c *gin.Context) {
	var request channelUpstreamModelUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		channelUpstreamOperationError(c, err)
		return
	}
	if request.ID <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	result, err := service.ApplyChannelUpstreamModelUpdates(
		c.Request.Context(), request.ID, request.AddModels, request.IgnoreModels, request.RemoveModels,
	)
	if err != nil {
		channelUpstreamOperationError(c, err)
		return
	}
	channelAudit(c, "channel.upstream_apply", map[string]any{"id": result.ChannelID})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"id":                      result.ChannelID,
			"added_models":            result.AddedModels,
			"removed_models":          result.RemovedModels,
			"ignored_models":          result.IgnoredModels,
			"remaining_models":        result.RemainingModels,
			"remaining_remove_models": result.RemainingRemoveModels,
			"models":                  result.Models,
			"settings":                result.Settings,
		},
	})
}

// ApplyAllChannelUpstreamModelUpdates accepts all staged additions/removals on
// every enabled channel that opted into update checks.
func ApplyAllChannelUpstreamModelUpdates(c *gin.Context) {
	summary, err := service.ApplyAllChannelUpstreamModelUpdates(c.Request.Context())
	if err != nil {
		channelUpstreamOperationError(c, err)
		return
	}
	results := make([]gin.H, 0, len(summary.Results))
	for _, result := range summary.Results {
		results = append(results, gin.H{
			"channel_id":              result.ChannelID,
			"channel_name":            result.ChannelName,
			"added_models":            result.AddedModels,
			"removed_models":          result.RemovedModels,
			"remaining_models":        result.RemainingModels,
			"remaining_remove_models": result.RemainingRemoveModels,
		})
	}
	data := gin.H{
		"processed_channels": summary.ProcessedChannels,
		"added_models":       summary.AddedModels,
		"removed_models":     summary.RemovedModels,
		"failed_channel_ids": summary.FailedChannelIDs,
		"results":            results,
	}
	if summary.ResultsTruncated {
		data["results_truncated"] = true
	}
	if summary.FailedIDsTruncated {
		data["failed_channel_ids_truncated"] = true
	}
	channelAudit(c, "channel.upstream_apply_all", map[string]any{"count": summary.ProcessedChannels})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

// DetectChannelUpstreamModelUpdates performs an immediate one-channel check.
// It stages differences for review even when auto-sync is enabled.
func DetectChannelUpstreamModelUpdates(c *gin.Context) {
	var request channelUpstreamModelUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		channelUpstreamOperationError(c, err)
		return
	}
	if request.ID <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	result, _, err := service.DetectChannelUpstreamModelUpdates(c.Request.Context(), request.ID, true, false)
	if err != nil {
		channelUpstreamOperationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
}

// DetectAllChannelUpstreamModelUpdates queues the manual variant of the
// lease-fenced model_update task. A concurrent pending/running scan is a 409.
func DetectAllChannelUpstreamModelUpdates(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeModelUpdate, map[string]any{"manual": true})
	if err != nil {
		channelUpstreamOperationError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "已有模型更新任务正在运行或等待中，不能启动本次手动任务",
			"data":    gin.H{"task_id": task.TaskID, "status": task.Status, "type": task.Type},
		})
		return
	}
	channelAudit(c, "channel.upstream_detect_all", map[string]any{"task_id": task.TaskID})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    gin.H{"task_id": task.TaskID, "status": task.Status},
	})
}
