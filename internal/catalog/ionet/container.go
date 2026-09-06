package ionet

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"
)

const maxContainerEvents = 2000

func (c *Client) ListContainers(ctx context.Context, deploymentID string) (*ContainerList, error) {
	if ValidateIdentifier(deploymentID) != nil {
		return nil, errors.New("deployment ID is invalid")
	}
	var response ContainerList
	endpoint := "/deployment/" + url.PathEscape(deploymentID) + "/containers"
	if err := c.makeDataRequest(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.Total < 0 || response.Total < len(response.Workers) || len(response.Workers) > maxProviderListItems {
		return nil, errInvalidProviderResponse
	}
	seenIDs := make(map[string]struct{}, len(response.Workers))
	for _, container := range response.Workers {
		if validateContainer(container) != nil {
			return nil, errInvalidProviderResponse
		}
		if _, duplicate := seenIDs[container.ContainerID]; duplicate {
			return nil, errInvalidProviderResponse
		}
		seenIDs[container.ContainerID] = struct{}{}
	}
	return &response, nil
}

func (c *Client) GetContainerDetails(ctx context.Context, deploymentID, containerID string) (*Container, error) {
	if ValidateIdentifier(deploymentID) != nil || ValidateIdentifier(containerID) != nil {
		return nil, errors.New("container identifier is invalid")
	}
	var response Container
	endpoint := "/deployment/" + url.PathEscape(deploymentID) + "/container/" + url.PathEscape(containerID)
	if err := c.makeJSONRequest(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if validateContainer(response) != nil {
		return nil, errInvalidProviderResponse
	}
	if response.ContainerID != containerID {
		return nil, errInvalidProviderResponse
	}
	return &response, nil
}

func (c *Client) GetContainerLogsRaw(ctx context.Context, deploymentID, containerID string, options *GetLogsOptions) (string, error) {
	if ValidateIdentifier(deploymentID) != nil || ValidateIdentifier(containerID) != nil {
		return "", errors.New("container identifier is invalid")
	}
	if err := ValidateLogsOptions(options); err != nil {
		return "", err
	}
	values := url.Values{}
	if options.Level != "" {
		values.Set("level", options.Level)
	}
	if options.Stream != "" {
		values.Set("stream", options.Stream)
	}
	values.Set("limit", strconv.Itoa(options.Limit))
	if options.Cursor != "" {
		values.Set("cursor", options.Cursor)
	}
	if options.Follow {
		values.Set("follow", "true")
	}
	if options.StartTime != nil {
		values.Set("start_time", options.StartTime.Format(timeFormat))
	}
	if options.EndTime != nil {
		values.Set("end_time", options.EndTime.Format(timeFormat))
	}
	endpoint := "/deployment/" + url.PathEscape(deploymentID) + "/log/" + url.PathEscape(containerID)
	data, err := c.makeRequest(ctx, http.MethodGet, withQuery(endpoint, values), nil, maxUpstreamLogBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errInvalidProviderResponse
	}
	return string(data), nil
}

const timeFormat = "2006-01-02T15:04:05Z07:00"

func validateContainer(container Container) error {
	if ValidateIdentifier(container.ContainerID) != nil || !validProviderText(container.DeviceID, MaxIdentifierBytes, true) ||
		!validProviderText(container.Hardware, 256, true) || !validProviderText(container.BrandName, 256, true) ||
		!validProviderText(container.Status, 64, false) || container.UptimePercent < 0 || container.UptimePercent > 100 ||
		container.GPUsPerContainer < 0 || container.GPUsPerContainer > MaxGPUCount ||
		!validPublicURL(container.PublicURL) || len(container.ContainerEvents) > maxContainerEvents {
		return errInvalidProviderResponse
	}
	for _, event := range container.ContainerEvents {
		if !validProviderText(event.Message, 64<<10, true) {
			return errInvalidProviderResponse
		}
	}
	return nil
}
