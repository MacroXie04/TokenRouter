package ionet

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) GetAvailableReplicas(ctx context.Context, hardwareID, gpuCount int) (*AvailableReplicasResponse, error) {
	if hardwareID < 1 || hardwareID > math.MaxInt32 || gpuCount < 1 || gpuCount > MaxGPUCount {
		return nil, errors.New("replica request is invalid")
	}
	values := url.Values{
		"hardware_id":  []string{strconv.Itoa(hardwareID)},
		"hardware_qty": []string{strconv.Itoa(gpuCount)},
	}
	var provider []struct {
		ID                int    `json:"id"`
		Name              string `json:"name"`
		AvailableReplicas int    `json:"available_replicas"`
	}
	if err := c.makeDataRequest(ctx, http.MethodGet, withQuery("/available-replicas", values), nil, &provider); err != nil {
		return nil, err
	}
	if len(provider) > maxProviderListItems {
		return nil, errInvalidProviderResponse
	}
	replicas := make([]AvailableReplica, 0, len(provider))
	for _, item := range provider {
		if item.ID < 1 || item.AvailableReplicas < 0 || !validProviderText(item.Name, 256, false) {
			return nil, errInvalidProviderResponse
		}
		replicas = append(replicas, AvailableReplica{
			LocationID: item.ID, LocationName: item.Name, HardwareID: hardwareID,
			HardwareName: "", AvailableCount: item.AvailableReplicas, MaxGPUs: gpuCount,
		})
	}
	return &AvailableReplicasResponse{Replicas: replicas}, nil
}

func (c *Client) GetMaxGPUsPerContainer(ctx context.Context) (*MaxGPUResponse, error) {
	var response MaxGPUResponse
	if err := c.makeDataRequest(ctx, http.MethodGet, "/hardware/max-gpus-per-container", nil, &response); err != nil {
		return nil, err
	}
	if response.Total < 0 || len(response.Hardware) > maxProviderListItems {
		return nil, errInvalidProviderResponse
	}
	for _, hardware := range response.Hardware {
		if hardware.HardwareID < 1 || hardware.MaxGPUsPerContainer < 0 || hardware.MaxGPUsPerContainer > MaxGPUCount ||
			hardware.Available < 0 || !validProviderText(hardware.HardwareName, 256, true) ||
			!validProviderText(hardware.BrandName, 256, true) {
			return nil, errInvalidProviderResponse
		}
	}
	return &response, nil
}

func (c *Client) ListHardwareTypes(ctx context.Context) ([]HardwareType, int, error) {
	response, err := c.GetMaxGPUsPerContainer(ctx)
	if err != nil {
		return nil, 0, err
	}
	hardwareTypes := make([]HardwareType, 0, len(response.Hardware))
	totalAvailable := response.Total
	for _, hardware := range response.Hardware {
		name := strings.TrimSpace(hardware.HardwareName)
		if name == "" {
			name = "Hardware " + strconv.Itoa(hardware.HardwareID)
		}
		hardwareTypes = append(hardwareTypes, HardwareType{
			ID: hardware.HardwareID, Name: name, MaxGPUs: hardware.MaxGPUsPerContainer,
			Available: hardware.Available > 0, BrandName: strings.TrimSpace(hardware.BrandName),
			AvailableCount: hardware.Available,
		})
		if response.Total == 0 {
			if totalAvailable > math.MaxInt-hardware.Available {
				return nil, 0, errInvalidProviderResponse
			}
			totalAvailable += hardware.Available
		}
	}
	return hardwareTypes, totalAvailable, nil
}

func (c *Client) ListLocations(ctx context.Context) (*LocationsResponse, error) {
	var response LocationsResponse
	if err := c.makeDataRequest(ctx, http.MethodGet, "/locations", nil, &response); err != nil {
		return nil, err
	}
	if response.Total < 0 || len(response.Locations) > maxProviderListItems {
		return nil, errInvalidProviderResponse
	}
	aggregateAvailability := response.Total == 0
	for index := range response.Locations {
		location := &response.Locations[index]
		location.ISO2 = strings.ToUpper(strings.TrimSpace(location.ISO2))
		if location.ID < 1 || location.Available < 0 || !finiteRange(location.Latitude, -90, 90) ||
			!finiteRange(location.Longitude, -180, 180) || !validProviderText(location.Name, 256, false) ||
			!validProviderText(location.ISO2, 8, true) || !validProviderText(location.Region, 256, true) ||
			!validProviderText(location.Country, 256, true) || !validProviderText(location.Description, 2048, true) {
			return nil, errInvalidProviderResponse
		}
		if aggregateAvailability {
			if response.Total > math.MaxInt-location.Available {
				return nil, errInvalidProviderResponse
			}
			response.Total += location.Available
		}
	}
	return &response, nil
}
