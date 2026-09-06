package ionet

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
)

const maxProviderListItems = 5000

func (c *Client) DeployContainer(ctx context.Context, request *DeploymentRequest) (*DeploymentResponse, error) {
	if err := ValidateDeploymentRequest(request); err != nil {
		return nil, err
	}
	var response DeploymentResponse
	if err := c.makeJSONRequest(ctx, http.MethodPost, "/deploy", request, &response); err != nil {
		return nil, err
	}
	if ValidateIdentifier(response.DeploymentID) != nil || !validProviderText(response.Status, 64, false) {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func (c *Client) ListDeployments(ctx context.Context, options *ListDeploymentsOptions) (*DeploymentList, error) {
	if err := ValidateListOptions(options); err != nil {
		return nil, err
	}
	values := url.Values{}
	if options != nil {
		addQueryString(values, "status", strings.ToLower(strings.TrimSpace(options.Status)))
		addQueryInt(values, "location_id", options.LocationID)
		addQueryInt(values, "page", options.Page)
		addQueryInt(values, "page_size", options.PageSize)
		addQueryString(values, "sort_by", options.SortBy)
		addQueryString(values, "sort_order", options.SortOrder)
	}
	var response DeploymentList
	if err := c.makeDataRequest(ctx, http.MethodGet, withQuery("/deployments", values), nil, &response); err != nil {
		return nil, err
	}
	if response.Total < 0 || response.Total < len(response.Deployments) || len(response.Deployments) > maxProviderListItems || len(response.Statuses) > 100 {
		return nil, errInvalidProviderResponse
	}
	seenIDs := make(map[string]struct{}, len(response.Deployments))
	for index := range response.Deployments {
		if err := validateDeployment(response.Deployments[index]); err != nil {
			return nil, errInvalidProviderResponse
		}
		if _, duplicate := seenIDs[response.Deployments[index].ID]; duplicate {
			return nil, errInvalidProviderResponse
		}
		seenIDs[response.Deployments[index].ID] = struct{}{}
		response.Deployments[index].GPUCount = response.Deployments[index].HardwareQuantity
		response.Deployments[index].Replicas = response.Deployments[index].HardwareQuantity
	}
	for _, status := range response.Statuses {
		if !validProviderText(status, 64, false) {
			return nil, errInvalidProviderResponse
		}
	}
	return &response, nil
}

func (c *Client) GetDeployment(ctx context.Context, deploymentID string) (*DeploymentDetail, error) {
	if ValidateIdentifier(deploymentID) != nil {
		return nil, errors.New("deployment ID is invalid")
	}
	var response DeploymentDetail
	if err := c.makeDataRequest(ctx, http.MethodGet, "/deployment/"+url.PathEscape(deploymentID), nil, &response); err != nil {
		return nil, err
	}
	if err := validateDeploymentDetail(response); err != nil {
		return nil, errInvalidProviderResponse
	}
	if response.ID != deploymentID {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func (c *Client) UpdateDeployment(ctx context.Context, deploymentID string, request *UpdateDeploymentRequest) (*UpdateDeploymentResponse, error) {
	if ValidateIdentifier(deploymentID) != nil {
		return nil, errors.New("deployment ID is invalid")
	}
	if err := ValidateUpdateDeploymentRequest(request); err != nil {
		return nil, err
	}
	var response UpdateDeploymentResponse
	if err := c.makeJSONRequest(ctx, http.MethodPatch, "/deployment/"+url.PathEscape(deploymentID), request, &response); err != nil {
		return nil, err
	}
	if ValidateIdentifier(response.DeploymentID) != nil || !validProviderText(response.Status, 64, false) {
		return nil, errInvalidProviderResponse
	}
	if response.DeploymentID != deploymentID {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func (c *Client) ExtendDeployment(ctx context.Context, deploymentID string, request *ExtendDurationRequest) (*DeploymentDetail, error) {
	if ValidateIdentifier(deploymentID) != nil {
		return nil, errors.New("deployment ID is invalid")
	}
	if err := ValidateExtendDurationRequest(request); err != nil {
		return nil, err
	}
	var response DeploymentDetail
	endpoint := "/deployment/" + url.PathEscape(deploymentID) + "/extend"
	if err := c.makeDataRequest(ctx, http.MethodPost, endpoint, request, &response); err != nil {
		return nil, err
	}
	if err := validateDeploymentDetail(response); err != nil {
		return nil, errInvalidProviderResponse
	}
	if response.ID != deploymentID {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func (c *Client) DeleteDeployment(ctx context.Context, deploymentID string) (*UpdateDeploymentResponse, error) {
	if ValidateIdentifier(deploymentID) != nil {
		return nil, errors.New("deployment ID is invalid")
	}
	var response UpdateDeploymentResponse
	if err := c.makeJSONRequest(ctx, http.MethodDelete, "/deployment/"+url.PathEscape(deploymentID), nil, &response); err != nil {
		return nil, err
	}
	if ValidateIdentifier(response.DeploymentID) != nil || !validProviderText(response.Status, 64, false) {
		return nil, errInvalidProviderResponse
	}
	if response.DeploymentID != deploymentID {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func (c *Client) GetPriceEstimation(ctx context.Context, request *PriceEstimationRequest) (*PriceEstimationResponse, error) {
	if err := ValidatePriceEstimationRequest(request); err != nil {
		return nil, err
	}
	durationType, durationQuantity, durationHours, hardwareQuantity := normalizedPriceInputs(request)
	currency := strings.TrimSpace(request.Currency)
	if currency == "" {
		currency = "usdc"
	}
	locations, err := common.Marshal(request.LocationIDs)
	if err != nil {
		return nil, errors.New("price estimation request is invalid")
	}
	values := url.Values{}
	values.Set("location_ids", string(locations))
	values.Set("hardware_id", strconv.Itoa(request.HardwareID))
	values.Set("hardware_qty", strconv.Itoa(hardwareQuantity))
	if request.GPUsPerContainer != 0 {
		values.Set("gpus_per_container", strconv.Itoa(request.GPUsPerContainer))
	}
	values.Set("duration_type", durationType)
	values.Set("duration_qty", strconv.Itoa(durationQuantity))
	if request.DurationHours != 0 {
		values.Set("duration_hours", strconv.Itoa(request.DurationHours))
	}
	values.Set("replica_count", strconv.Itoa(request.ReplicaCount))
	values.Set("currency", currency)

	var provider struct {
		IonetFee              float64 `json:"ionet_fee"`
		CurrencyConversionFee float64 `json:"currency_conversion_fee"`
		TotalCostUSDC         float64 `json:"total_cost_usdc"`
	}
	if err := c.makeDataRequest(ctx, http.MethodGet, withQuery("/price", values), nil, &provider); err != nil {
		return nil, err
	}
	compute := provider.TotalCostUSDC - provider.IonetFee - provider.CurrencyConversionFee
	if !finiteNonNegative(provider.TotalCostUSDC) || !finiteNonNegative(provider.IonetFee) ||
		!finiteNonNegative(provider.CurrencyConversionFee) || !finiteNonNegative(compute) || durationHours < 1 {
		return nil, errInvalidProviderResponse
	}
	return &PriceEstimationResponse{
		EstimatedCost:   provider.TotalCostUSDC,
		Currency:        strings.ToUpper(currency),
		EstimationValid: true,
		PriceBreakdown: PriceBreakdown{
			ComputeCost: compute,
			TotalCost:   provider.TotalCostUSDC,
			HourlyRate:  provider.TotalCostUSDC / float64(durationHours),
		},
	}, nil
}

func (c *Client) CheckClusterNameAvailability(ctx context.Context, clusterName string) (bool, error) {
	clusterName = strings.TrimSpace(clusterName)
	if ValidateName(clusterName) != nil {
		return false, errors.New("cluster name is invalid")
	}
	values := url.Values{"cluster_name": []string{clusterName}}
	var response bool
	if err := c.makeJSONRequest(ctx, http.MethodGet, withQuery("/clusters/check_cluster_name_availability", values), nil, &response); err != nil {
		return false, err
	}
	return response, nil
}

func (c *Client) UpdateClusterName(ctx context.Context, clusterID string, request *UpdateClusterNameRequest) (*UpdateClusterNameResponse, error) {
	if ValidateIdentifier(clusterID) != nil || request == nil || ValidateName(request.Name) != nil {
		return nil, errors.New("cluster name update is invalid")
	}
	var response UpdateClusterNameResponse
	endpoint := "/clusters/" + url.PathEscape(clusterID) + "/update-name"
	if err := c.makeJSONRequest(ctx, http.MethodPut, endpoint, request, &response); err != nil {
		return nil, err
	}
	if !validProviderText(response.Status, 64, false) || !validProviderText(response.Message, 1024, true) {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func normalizedPriceInputs(request *PriceEstimationRequest) (string, int, int, int) {
	durationQuantity := request.DurationQty
	if durationQuantity < 1 {
		durationQuantity = request.DurationHours
	}
	hardwareQuantity := request.HardwareQty
	if hardwareQuantity < 1 {
		hardwareQuantity = request.GPUsPerContainer
	}
	durationType := strings.ToLower(strings.TrimSpace(request.DurationType))
	durationHours := durationQuantity
	switch durationType {
	case "day", "days", "daily":
		durationType = "daily"
		durationHours *= 24
	case "week", "weeks", "weekly":
		durationType = "weekly"
		durationHours *= 24 * 7
	case "month", "months", "monthly":
		durationType = "monthly"
		durationHours *= 24 * 30
	default:
		durationType = "hourly"
	}
	return durationType, durationQuantity, durationHours, hardwareQuantity
}

func validateDeployment(deployment Deployment) error {
	if ValidateIdentifier(deployment.ID) != nil || !validProviderText(deployment.Status, 64, false) ||
		!validProviderText(deployment.Name, MaxNameBytes, false) || !finiteRange(deployment.CompletedPercent, 0, 100) ||
		deployment.HardwareQuantity < 0 || deployment.HardwareQuantity > MaxGPUCount*MaxReplicaCount ||
		deployment.ComputeMinutesServed < 0 || deployment.ComputeMinutesRemaining < 0 ||
		!validProviderText(deployment.BrandName, 256, true) || !validProviderText(deployment.HardwareName, 256, true) {
		return errInvalidProviderResponse
	}
	return nil
}

func validateDeploymentDetail(detail DeploymentDetail) error {
	if ValidateIdentifier(detail.ID) != nil || !validProviderText(detail.Status, 64, false) ||
		!finiteNonNegative(detail.AmountPaid) || !finiteRange(detail.CompletedPercent, 0, 100) ||
		detail.TotalGPUs < 0 || detail.TotalGPUs > MaxGPUCount*MaxReplicaCount || detail.GPUsPerContainer < 0 ||
		detail.GPUsPerContainer > MaxGPUCount || detail.TotalContainers < 0 || detail.TotalContainers > MaxReplicaCount ||
		detail.HardwareID < 0 || detail.ComputeMinutesServed < 0 || detail.ComputeMinutesRemaining < 0 ||
		len(detail.Locations) > maxLocations || !validProviderText(detail.HardwareName, 256, true) ||
		!validProviderText(detail.BrandName, 256, true) {
		return errInvalidProviderResponse
	}
	for _, location := range detail.Locations {
		if location.ID < 1 || !validProviderText(location.Name, 256, false) || !validProviderText(location.ISO2, 8, true) {
			return errInvalidProviderResponse
		}
	}
	return nil
}

func validProviderText(value string, max int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > max || !safeOptionalText(value) {
		return false
	}
	return true
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func finiteRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func addQueryString(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func addQueryInt(values url.Values, key string, value int) {
	if value != 0 {
		values.Set(key, strconv.Itoa(value))
	}
}

func withQuery(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}
