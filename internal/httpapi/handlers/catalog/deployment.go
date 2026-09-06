package catalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/catalog/ionet"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxDeploymentKeywordBytes = 256
	maxDeploymentQueryBytes   = 16 << 10
)

type deploymentAPI interface {
	GetMaxGPUsPerContainer(context.Context) (*ionet.MaxGPUResponse, error)
	ListDeployments(context.Context, *ionet.ListDeploymentsOptions) (*ionet.DeploymentList, error)
	GetDeployment(context.Context, string) (*ionet.DeploymentDetail, error)
	UpdateDeployment(context.Context, string, *ionet.UpdateDeploymentRequest) (*ionet.UpdateDeploymentResponse, error)
	ExtendDeployment(context.Context, string, *ionet.ExtendDurationRequest) (*ionet.DeploymentDetail, error)
	DeleteDeployment(context.Context, string) (*ionet.UpdateDeploymentResponse, error)
	DeployContainer(context.Context, *ionet.DeploymentRequest) (*ionet.DeploymentResponse, error)
	ListHardwareTypes(context.Context) ([]ionet.HardwareType, int, error)
	ListLocations(context.Context) (*ionet.LocationsResponse, error)
	GetAvailableReplicas(context.Context, int, int) (*ionet.AvailableReplicasResponse, error)
	GetPriceEstimation(context.Context, *ionet.PriceEstimationRequest) (*ionet.PriceEstimationResponse, error)
	CheckClusterNameAvailability(context.Context, string) (bool, error)
	UpdateClusterName(context.Context, string, *ionet.UpdateClusterNameRequest) (*ionet.UpdateClusterNameResponse, error)
	GetContainerLogsRaw(context.Context, string, string, *ionet.GetLogsOptions) (string, error)
	ListContainers(context.Context, string) (*ionet.ContainerList, error)
	GetContainerDetails(context.Context, string, string) (*ionet.Container, error)
}

var deploymentClientFactory = func(apiKey string, enterprise bool) deploymentAPI {
	if enterprise {
		return ionet.NewEnterpriseClient(apiKey)
	}
	return ionet.NewClient(apiKey)
}

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

func deploymentSettings() (bool, string) {
	values := setting.GetOptions(setting.ModelDeploymentIONetEnabledOption, setting.ModelDeploymentIONetAPIKeyOption)
	return values[setting.ModelDeploymentIONetEnabledOption] == "true", strings.TrimSpace(values[setting.ModelDeploymentIONetAPIKeyOption])
}

func configuredDeploymentClient(c *gin.Context, enterprise bool) (deploymentAPI, bool) {
	enabled, apiKey := deploymentSettings()
	if !enabled || ionet.ValidateAPIKey(apiKey) != nil {
		deploymentError(c, "io.net model deployment is not enabled or api key missing")
		return nil, false
	}
	client := deploymentClientFactory(apiKey, enterprise)
	if client == nil {
		deploymentUpstreamError(c)
		return nil, false
	}
	return client, true
}

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

func deploymentClientAndID(c *gin.Context, enterprise bool) (deploymentAPI, string, bool) {
	deploymentID := strings.TrimSpace(c.Param("id"))
	if ionet.ValidateIdentifier(deploymentID) != nil {
		deploymentError(c, "deployment ID is required")
		return nil, "", false
	}
	client, ok := configuredDeploymentClient(c, enterprise)
	return client, deploymentID, ok
}

type deploymentPage struct {
	Page     int
	PageSize int
}

func parseDeploymentPage(c *gin.Context) (deploymentPage, bool) {
	page := 1
	if raw, present, valid := optionalSingleQuery(c, "p", 16); !valid {
		deploymentError(c, "invalid pagination")
		return deploymentPage{}, false
	} else if present {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > ionet.MaxPage {
			deploymentError(c, "invalid pagination")
			return deploymentPage{}, false
		}
		page = parsed
	}
	pageSize := 10
	foundSize := false
	for _, name := range []string{"page_size", "ps", "size"} {
		raw, present, valid := optionalSingleQuery(c, name, 16)
		if !valid || (present && foundSize) {
			deploymentError(c, "invalid pagination")
			return deploymentPage{}, false
		}
		if present {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > ionet.MaxPageSize {
				deploymentError(c, "invalid pagination")
				return deploymentPage{}, false
			}
			pageSize, foundSize = parsed, true
		}
	}
	return deploymentPage{Page: page, PageSize: pageSize}, true
}

func parseDeploymentLogOptions(c *gin.Context) (*ionet.GetLogsOptions, bool) {
	options := &ionet.GetLogsOptions{Limit: 100}
	for name, destination := range map[string]*string{"level": &options.Level, "stream": &options.Stream, "cursor": &options.Cursor} {
		value, present, valid := optionalSingleQuery(c, name, 1024)
		if !valid {
			deploymentError(c, "invalid log query")
			return nil, false
		}
		if present {
			*destination = value
		}
	}
	if raw, present, valid := optionalSingleQuery(c, "limit", 16); !valid {
		deploymentError(c, "invalid log limit")
		return nil, false
	} else if present {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > ionet.MaxLogLimit {
			deploymentError(c, "invalid log limit")
			return nil, false
		}
		options.Limit = limit
	}
	if raw, present, valid := optionalSingleQuery(c, "follow", 5); !valid {
		deploymentError(c, "invalid follow parameter")
		return nil, false
	} else if present {
		if raw != "true" && raw != "false" {
			deploymentError(c, "invalid follow parameter")
			return nil, false
		}
		options.Follow = raw == "true"
	}
	for name, destination := range map[string]**time.Time{"start_time": &options.StartTime, "end_time": &options.EndTime} {
		raw, present, valid := optionalSingleQuery(c, name, 64)
		if !valid {
			deploymentError(c, "invalid log time range")
			return nil, false
		}
		if present {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				deploymentError(c, "invalid log time range")
				return nil, false
			}
			*destination = &parsed
		}
	}
	options.Level = strings.ToLower(strings.TrimSpace(options.Level))
	options.Stream = strings.ToLower(strings.TrimSpace(options.Stream))
	if ionet.ValidateLogsOptions(options) != nil {
		deploymentError(c, "invalid log query")
		return nil, false
	}
	return options, true
}

func bindDeploymentJSON(c *gin.Context, target any) bool {
	raw, ok := readOptionalDeploymentJSON(c)
	if !ok {
		return false
	}
	if len(raw) == 0 || ionet.DecodeRequest(raw, target) != nil {
		deploymentError(c, "invalid request payload")
		return false
	}
	return true
}

func readOptionalDeploymentJSON(c *gin.Context) ([]byte, bool) {
	if c.Request.ContentLength > ionet.MaxRequestBodyBytes {
		deploymentPayloadTooLarge(c)
		return nil, false
	}
	raw, err := httpx.ReadAllLimited(c.Request.Body, ionet.MaxRequestBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			deploymentPayloadTooLarge(c)
		} else {
			deploymentError(c, "invalid request payload")
		}
		return nil, false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, true
	}
	return raw, true
}

func requiredDeploymentQuery(c *gin.Context, name string, maxBytes int) (string, bool) {
	value, present, valid := optionalSingleQuery(c, name, maxBytes)
	if !valid || !present || strings.TrimSpace(value) == "" {
		deploymentError(c, name+" parameter is required")
		return "", false
	}
	return value, true
}

func deploymentQuery(c *gin.Context, name string, maxBytes int) (string, bool) {
	value, _, valid := optionalSingleQuery(c, name, maxBytes)
	if !valid {
		deploymentError(c, "invalid "+name+" parameter")
		return "", false
	}
	return value, true
}

func optionalSingleQuery(c *gin.Context, name string, maxBytes int) (string, bool, bool) {
	if len(c.Request.URL.RawQuery) > maxDeploymentQueryBytes {
		return "", true, false
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return "", true, false
	}
	values, present := query[name]
	if !present {
		return "", false, true
	}
	if len(values) != 1 || len(values[0]) > maxBytes || !safeDeploymentText(values[0]) {
		return "", true, false
	}
	return values[0], true, true
}

func safeDeploymentText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
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
