package catalog

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/catalog/ionet"
	"math"
	"strconv"
	"strings"
)

func GetModelDeploymentSettings(c *gin.Context) {
	enabled, apiKey := deploymentSettings()
	configured := ionet.ValidateAPIKey(apiKey) == nil
	deploymentSuccess(c, gin.H{
		"provider": "io.net", "enabled": enabled, "configured": configured, "can_connect": enabled && configured,
	})
}

func TestIoNetConnection(c *gin.Context) {
	var request struct {
		APIKey string `json:"api_key"`
	}
	raw, ok := readOptionalDeploymentJSON(c)
	if !ok {
		return
	}
	if len(raw) != 0 && ionet.DecodeRequest(raw, &request) != nil {
		deploymentError(c, "invalid request payload")
		return
	}
	apiKey := strings.TrimSpace(request.APIKey)
	if apiKey == "" {
		_, apiKey = deploymentSettings()
	}
	if ionet.ValidateAPIKey(apiKey) != nil {
		deploymentError(c, "api_key is required")
		return
	}
	client := deploymentClientFactory(apiKey, true)
	if client == nil {
		deploymentError(c, "failed to validate api key")
		return
	}
	result, err := client.GetMaxGPUsPerContainer(c.Request.Context())
	if err != nil || result == nil {
		deploymentError(c, "failed to validate api key")
		return
	}
	totalAvailable := result.Total
	if totalAvailable == 0 {
		for _, hardware := range result.Hardware {
			if hardware.Available > math.MaxInt-totalAvailable {
				deploymentError(c, "failed to validate api key")
				return
			}
			totalAvailable += hardware.Available
		}
	}
	deploymentSuccess(c, gin.H{"hardware_count": len(result.Hardware), "total_available": totalAvailable})
}

func GetAllDeployments(c *gin.Context) {
	page, ok := parseDeploymentPage(c)
	if !ok {
		return
	}
	status, ok := deploymentQuery(c, "status", 64)
	if !ok {
		return
	}
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	options := &ionet.ListDeploymentsOptions{
		Status: strings.ToLower(strings.TrimSpace(status)), Page: page.Page, PageSize: page.PageSize,
		SortBy: "created_at", SortOrder: "desc",
	}
	if ionet.ValidateListOptions(options) != nil {
		deploymentError(c, "invalid deployment query")
		return
	}
	list, err := client.ListDeployments(c.Request.Context(), options)
	if err != nil || list == nil {
		deploymentUpstreamError(c)
		return
	}
	items := make([]map[string]any, 0, len(list.Deployments))
	for _, deployment := range list.Deployments {
		items = append(items, mapIONetDeployment(deployment))
	}
	deploymentSuccess(c, gin.H{
		"page": page.Page, "page_size": page.PageSize, "total": list.Total, "items": items,
		"status_counts": deploymentStatusCounts(list.Total, list.Deployments),
	})
}

func SearchDeployments(c *gin.Context) {
	page, ok := parseDeploymentPage(c)
	if !ok {
		return
	}
	status, ok := deploymentQuery(c, "status", 64)
	if !ok {
		return
	}
	keyword, ok := deploymentQuery(c, "keyword", maxDeploymentKeywordBytes)
	if !ok {
		return
	}
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	options := &ionet.ListDeploymentsOptions{
		Status: strings.ToLower(strings.TrimSpace(status)), Page: page.Page, PageSize: page.PageSize,
		SortBy: "created_at", SortOrder: "desc",
	}
	if ionet.ValidateListOptions(options) != nil {
		deploymentError(c, "invalid deployment query")
		return
	}
	list, err := client.ListDeployments(c.Request.Context(), options)
	if err != nil || list == nil {
		deploymentUpstreamError(c)
		return
	}
	keyword = strings.TrimSpace(keyword)
	filtered := make([]ionet.Deployment, 0, len(list.Deployments))
	if keyword == "" {
		filtered = append(filtered, list.Deployments...)
	} else {
		lowerKeyword := strings.ToLower(keyword)
		for _, deployment := range list.Deployments {
			if strings.Contains(strings.ToLower(deployment.Name), lowerKeyword) {
				filtered = append(filtered, deployment)
			}
		}
	}
	items := make([]map[string]any, 0, len(filtered))
	for _, deployment := range filtered {
		items = append(items, mapIONetDeployment(deployment))
	}
	total := list.Total
	if keyword != "" {
		total = len(filtered)
	}
	deploymentSuccess(c, gin.H{"page": page.Page, "page_size": page.PageSize, "total": total, "items": items})
}

func GetDeployment(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	details, err := client.GetDeployment(c.Request.Context(), deploymentID)
	if err != nil || details == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{
		"id": details.ID, "deployment_name": details.ID, "model_name": "", "model_version": "",
		"status": strings.ToLower(details.Status), "instance_count": details.TotalContainers,
		"hardware_id":     details.HardwareID,
		"resource_config": gin.H{"cpu": "", "memory": "", "gpu": strconv.Itoa(details.TotalGPUs)},
		"created_at":      details.CreatedAt.Unix(), "updated_at": details.CreatedAt.Unix(), "description": "",
		"amount_paid": details.AmountPaid, "completed_percent": details.CompletedPercent,
		"gpus_per_container": details.GPUsPerContainer, "total_gpus": details.TotalGPUs,
		"total_containers": details.TotalContainers, "hardware_name": details.HardwareName,
		"brand_name": details.BrandName, "compute_minutes_served": details.ComputeMinutesServed,
		"compute_minutes_remaining": details.ComputeMinutesRemaining, "locations": details.Locations,
		"container_config": details.ContainerConfig,
	})
}

func GetHardwareTypes(c *gin.Context) {
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	types, totalAvailable, err := client.ListHardwareTypes(c.Request.Context())
	if err != nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{"hardware_types": types, "total": len(types), "total_available": totalAvailable})
}

func GetLocations(c *gin.Context) {
	client, ok := configuredDeploymentClient(c, false)
	if !ok {
		return
	}
	locations, err := client.ListLocations(c.Request.Context())
	if err != nil || locations == nil {
		deploymentUpstreamError(c)
		return
	}
	total := locations.Total
	if total == 0 {
		total = len(locations.Locations)
	}
	deploymentSuccess(c, gin.H{"locations": locations.Locations, "total": total})
}

func GetAvailableReplicas(c *gin.Context) {
	hardwareRaw, ok := requiredDeploymentQuery(c, "hardware_id", 16)
	if !ok {
		return
	}
	hardwareID, err := strconv.Atoi(hardwareRaw)
	if err != nil || hardwareID < 1 || hardwareID > math.MaxInt32 {
		deploymentError(c, "invalid hardware_id parameter")
		return
	}
	gpuCount := 1
	if raw, present, valid := optionalSingleQuery(c, "gpu_count", 16); !valid {
		deploymentError(c, "invalid gpu_count parameter")
		return
	} else if present {
		gpuCount, err = strconv.Atoi(raw)
		if err != nil || gpuCount < 1 || gpuCount > ionet.MaxGPUCount {
			deploymentError(c, "invalid gpu_count parameter")
			return
		}
	}
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	replicas, err := client.GetAvailableReplicas(c.Request.Context(), hardwareID, gpuCount)
	if err != nil || replicas == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, replicas)
}

func GetPriceEstimation(c *gin.Context) {
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	var request ionet.PriceEstimationRequest
	if !bindDeploymentJSON(c, &request) {
		return
	}
	if ionet.ValidatePriceEstimationRequest(&request) != nil {
		deploymentError(c, "invalid price estimation request")
		return
	}
	response, err := client.GetPriceEstimation(c.Request.Context(), &request)
	if err != nil || response == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, response)
}

func CheckClusterNameAvailability(c *gin.Context) {
	name, ok := requiredDeploymentQuery(c, "name", ionet.MaxNameBytes)
	if !ok {
		return
	}
	name = strings.TrimSpace(name)
	if ionet.ValidateName(name) != nil {
		deploymentError(c, "invalid name parameter")
		return
	}
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	available, err := client.CheckClusterNameAvailability(c.Request.Context(), name)
	if err != nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{"available": available, "name": name})
}

func CreateDeployment(c *gin.Context) {
	client, ok := configuredDeploymentClient(c, true)
	if !ok {
		return
	}
	var request ionet.DeploymentRequest
	if !bindDeploymentJSON(c, &request) {
		return
	}
	if ionet.ValidateDeploymentRequest(&request) != nil {
		deploymentError(c, "invalid deployment request")
		return
	}
	response, err := client.DeployContainer(c.Request.Context(), &request)
	if err != nil || response == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{
		"deployment_id": response.DeploymentID, "status": response.Status, "message": "Deployment created successfully",
	})
}

func UpdateDeployment(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	var request ionet.UpdateDeploymentRequest
	if !bindDeploymentJSON(c, &request) {
		return
	}
	if ionet.ValidateUpdateDeploymentRequest(&request) != nil {
		deploymentError(c, "invalid deployment update")
		return
	}
	response, err := client.UpdateDeployment(c.Request.Context(), deploymentID, &request)
	if err != nil || response == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{"status": response.Status, "deployment_id": response.DeploymentID})
}

func UpdateDeploymentName(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if !bindDeploymentJSON(c, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if ionet.ValidateName(request.Name) != nil {
		deploymentError(c, "deployment name cannot be empty")
		return
	}
	available, err := client.CheckClusterNameAvailability(c.Request.Context(), request.Name)
	if err != nil {
		deploymentUpstreamError(c)
		return
	}
	if !available {
		deploymentError(c, "deployment name is not available, please choose a different name")
		return
	}
	response, err := client.UpdateClusterName(c.Request.Context(), deploymentID, &ionet.UpdateClusterNameRequest{Name: request.Name})
	if err != nil || response == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{"status": response.Status, "message": response.Message, "id": deploymentID, "name": request.Name})
}

func ExtendDeployment(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	var request ionet.ExtendDurationRequest
	if !bindDeploymentJSON(c, &request) {
		return
	}
	if ionet.ValidateExtendDurationRequest(&request) != nil {
		deploymentError(c, "invalid duration_hours")
		return
	}
	details, err := client.ExtendDeployment(c.Request.Context(), deploymentID, &request)
	if err != nil || details == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, mapIONetDeployment(ionet.Deployment{
		ID: details.ID, Status: details.Status, Name: deploymentID, CompletedPercent: details.CompletedPercent,
		HardwareQuantity: details.TotalGPUs, BrandName: details.BrandName, HardwareName: details.HardwareName,
		ComputeMinutesServed: details.ComputeMinutesServed, ComputeMinutesRemaining: details.ComputeMinutesRemaining,
		CreatedAt: details.CreatedAt,
	}))
}

func DeleteDeployment(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	response, err := client.DeleteDeployment(c.Request.Context(), deploymentID)
	if err != nil || response == nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, gin.H{
		"status": response.Status, "deployment_id": response.DeploymentID,
		"message": "Deployment termination requested successfully",
	})
}

func GetDeploymentLogs(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, false)
	if !ok {
		return
	}
	containerID, ok := requiredDeploymentQuery(c, "container_id", ionet.MaxIdentifierBytes)
	if !ok {
		return
	}
	containerID = strings.TrimSpace(containerID)
	if ionet.ValidateIdentifier(containerID) != nil {
		deploymentError(c, "invalid container_id parameter")
		return
	}
	options, ok := parseDeploymentLogOptions(c)
	if !ok {
		return
	}
	logs, err := client.GetContainerLogsRaw(c.Request.Context(), deploymentID, containerID, options)
	if err != nil {
		deploymentUpstreamError(c)
		return
	}
	deploymentSuccess(c, logs)
}

func ListDeploymentContainers(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	containers, err := client.ListContainers(c.Request.Context(), deploymentID)
	if err != nil || containers == nil {
		deploymentUpstreamError(c)
		return
	}
	items := make([]map[string]any, 0, len(containers.Workers))
	for _, container := range containers.Workers {
		items = append(items, mapIONetContainer(container))
	}
	deploymentSuccess(c, gin.H{"total": containers.Total, "containers": items})
}

func GetContainerDetails(c *gin.Context) {
	client, deploymentID, ok := deploymentClientAndID(c, true)
	if !ok {
		return
	}
	containerID := strings.TrimSpace(c.Param("container_id"))
	if ionet.ValidateIdentifier(containerID) != nil {
		deploymentError(c, "container ID is required")
		return
	}
	details, err := client.GetContainerDetails(c.Request.Context(), deploymentID, containerID)
	if err != nil || details == nil {
		deploymentUpstreamError(c)
		return
	}
	data := mapIONetContainer(*details)
	data["deployment_id"] = deploymentID
	deploymentSuccess(c, data)
}
