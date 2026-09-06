package catalog

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/catalog/ionet"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func deploymentSuccess(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func deploymentError(c *gin.Context, message string) {
	c.JSON(http.StatusOK, gin.H{"success": false, "message": message})
}

func deploymentPayloadTooLarge(c *gin.Context) {
	c.JSON(http.StatusRequestEntityTooLarge, gin.H{"success": false, "message": "request payload is too large"})
}

func deploymentUpstreamError(c *gin.Context) {
	deploymentError(c, "io.net request failed")
}

func mapIONetDeployment(deployment ionet.Deployment) map[string]any {
	created := deployment.CreatedAt.Unix()
	if deployment.CreatedAt.IsZero() {
		created = time.Now().Unix()
	}
	hours := deployment.ComputeMinutesRemaining / 60
	minutes := deployment.ComputeMinutesRemaining % 60
	remaining := "completed"
	if hours > 0 {
		remaining = strconv.Itoa(hours) + " hour " + strconv.Itoa(minutes) + " minutes"
	} else if minutes > 0 {
		remaining = strconv.Itoa(minutes) + " minutes"
	}
	return map[string]any{
		"id": deployment.ID, "deployment_name": deployment.Name, "container_name": deployment.Name,
		"status": strings.ToLower(deployment.Status), "type": "Container", "time_remaining": remaining,
		"time_remaining_minutes": deployment.ComputeMinutesRemaining,
		"hardware_info":          fmt.Sprintf("%s %s x%d", deployment.BrandName, deployment.HardwareName, deployment.HardwareQuantity),
		"hardware_name":          deployment.HardwareName, "brand_name": deployment.BrandName,
		"hardware_quantity": deployment.HardwareQuantity, "completed_percent": deployment.CompletedPercent,
		"compute_minutes_served":    deployment.ComputeMinutesServed,
		"compute_minutes_remaining": deployment.ComputeMinutesRemaining, "created_at": created, "updated_at": created,
		"model_name": "", "model_version": "", "instance_count": deployment.HardwareQuantity,
		"resource_config": map[string]any{"cpu": "", "memory": "", "gpu": strconv.Itoa(deployment.HardwareQuantity)},
		"description":     "", "provider": "io.net",
	}
}

func deploymentStatusCounts(total int, deployments []ionet.Deployment) map[string]int64 {
	counts := map[string]int64{"all": int64(total)}
	for _, status := range []string{"running", "completed", "failed", "deployment requested", "termination requested", "destroyed"} {
		counts[status] = 0
	}
	for _, deployment := range deployments {
		status := strings.ToLower(strings.TrimSpace(deployment.Status))
		counts[status]++
	}
	return counts
}

func mapIONetContainer(container ionet.Container) map[string]any {
	events := make([]map[string]any, 0, len(container.ContainerEvents))
	for _, event := range container.ContainerEvents {
		events = append(events, map[string]any{"time": event.Time.Unix(), "message": event.Message})
	}
	return map[string]any{
		"container_id": container.ContainerID, "device_id": container.DeviceID,
		"status": strings.ToLower(strings.TrimSpace(container.Status)), "hardware": container.Hardware,
		"brand_name": container.BrandName, "created_at": container.CreatedAt.Unix(),
		"uptime_percent": container.UptimePercent, "gpus_per_container": container.GPUsPerContainer,
		"public_url": container.PublicURL, "events": events,
	}
}
