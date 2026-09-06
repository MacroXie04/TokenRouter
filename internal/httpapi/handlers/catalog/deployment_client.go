package catalog

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/catalog/ionet"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"strings"
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

func deploymentClientAndID(c *gin.Context, enterprise bool) (deploymentAPI, string, bool) {
	deploymentID := strings.TrimSpace(c.Param("id"))
	if ionet.ValidateIdentifier(deploymentID) != nil {
		deploymentError(c, "deployment ID is required")
		return nil, "", false
	}
	client, ok := configuredDeploymentClient(c, enterprise)
	return client, deploymentID, ok
}
