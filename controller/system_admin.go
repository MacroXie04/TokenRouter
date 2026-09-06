package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// CreateLogCleanupSystemTask enqueues a log-cleanup task (POST
// /api/system-task/log-cleanup, root only).
func CreateLogCleanupSystemTask(c *gin.Context) {
	targetTimestamp, _ := strconv.ParseInt(c.Query("target_timestamp"), 10, 64)
	if targetTimestamp == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "target timestamp is required"})
		return
	}
	task, err := service.StartLogCleanupTaskContext(c.Request.Context(), targetTimestamp)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": task.ToResponse()})
}

// GetCurrentSystemTask returns the active task of the given type or null
// (GET /api/system-task/current, root only).
func GetCurrentSystemTask(c *gin.Context) {
	taskType := c.Query("type")
	if taskType == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "type is required"})
		return
	}
	task, err := model.GetActiveSystemTask(taskType)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if task == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": task.ToResponse()})
}

// ListSystemTasks lists the newest task rows (GET /api/system-task/list,
// root only).
func ListSystemTasks(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	tasks, err := model.ListSystemTasks(limit)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	responses := make([]model.SystemTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		responses = append(responses, task.ToResponse())
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": responses})
}

// GetSystemTask returns one task by its task id (GET
// /api/system-task/:task_id, root only).
func GetSystemTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "task id is required"})
		return
	}
	task, err := model.GetSystemTaskByTaskID(taskID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "task not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": task.ToResponse()})
}

// ListSystemInstances returns every registered node with its live/stale
// status (GET /api/system-info/instances, root only).
func ListSystemInstances(c *gin.Context) {
	instances, err := model.ListSystemInstances()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	responses := make([]model.SystemInstanceResponse, 0, len(instances))
	for _, instance := range instances {
		responses = append(responses, instance.ToResponse(now))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": responses})
}

// DeleteStaleSystemInstances removes every stale node registration (DELETE
// /api/system-info/stale-instances, root only).
func DeleteStaleSystemInstances(c *gin.Context) {
	now, err := model.PrimaryDatabaseUnixTimestamp(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	deletedCount, err := model.DeleteStaleSystemInstances(now)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"deleted_count": deletedCount}})
}

// DeleteStaleSystemInstance removes one stale node registration (DELETE
// /api/system-info/instances/:node_name, root only).
func DeleteStaleSystemInstance(c *gin.Context) {
	nodeName := c.Param("node_name")
	if strings.TrimSpace(nodeName) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "node name is required"})
		return
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	deleted, err := model.DeleteStaleSystemInstance(nodeName, now)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !deleted {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "instance is not stale or no longer exists"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"deleted_count": 1}})
}
