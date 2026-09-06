// Package ionet provides the small, bounded io.net CaaS client used by the
// deployment dashboard. Production constructors always target io.net's fixed
// HTTPS endpoints; callers cannot turn this client into an arbitrary proxy.
package ionet

import "time"

const (
	DefaultEnterpriseBaseURL = "https://api.io.solutions/enterprise/v1/io-cloud/caas"
	DefaultBaseURL           = "https://api.io.solutions/v1/io-cloud/caas"
	DefaultTimeout           = 20 * time.Second

	// MaxRequestBodyBytes is the controller-level ceiling for deployment JSON.
	MaxRequestBodyBytes int64 = 256 << 10
)

// DeploymentRequest represents a container deployment request.
type DeploymentRequest struct {
	ResourcePrivateName string          `json:"resource_private_name"`
	DurationHours       int             `json:"duration_hours"`
	GPUsPerContainer    int             `json:"gpus_per_container"`
	HardwareID          int             `json:"hardware_id"`
	LocationIDs         []int           `json:"location_ids"`
	ContainerConfig     ContainerConfig `json:"container_config"`
	RegistryConfig      RegistryConfig  `json:"registry_config"`
}

type ContainerConfig struct {
	ReplicaCount       int               `json:"replica_count"`
	EnvVariables       map[string]string `json:"env_variables,omitempty"`
	SecretEnvVariables map[string]string `json:"secret_env_variables,omitempty"`
	Entrypoint         []string          `json:"entrypoint,omitempty"`
	TrafficPort        int               `json:"traffic_port,omitempty"`
	Args               []string          `json:"args,omitempty"`
}

type RegistryConfig struct {
	ImageURL         string `json:"image_url"`
	RegistryUsername string `json:"registry_username,omitempty"`
	RegistrySecret   string `json:"registry_secret,omitempty"`
}

type DeploymentResponse struct {
	DeploymentID string `json:"deployment_id"`
	Status       string `json:"status"`
}

type DeploymentDetail struct {
	ID                      string                    `json:"id"`
	Status                  string                    `json:"status"`
	CreatedAt               time.Time                 `json:"created_at"`
	StartedAt               *time.Time                `json:"started_at,omitempty"`
	FinishedAt              *time.Time                `json:"finished_at,omitempty"`
	AmountPaid              float64                   `json:"amount_paid"`
	CompletedPercent        float64                   `json:"completed_percent"`
	TotalGPUs               int                       `json:"total_gpus"`
	GPUsPerContainer        int                       `json:"gpus_per_container"`
	TotalContainers         int                       `json:"total_containers"`
	HardwareName            string                    `json:"hardware_name"`
	HardwareID              int                       `json:"hardware_id"`
	Locations               []DeploymentLocation      `json:"locations"`
	BrandName               string                    `json:"brand_name"`
	ComputeMinutesServed    int                       `json:"compute_minutes_served"`
	ComputeMinutesRemaining int                       `json:"compute_minutes_remaining"`
	ContainerConfig         DeploymentContainerConfig `json:"container_config"`
}

type DeploymentLocation struct {
	ID   int    `json:"id"`
	ISO2 string `json:"iso2"`
	Name string `json:"name"`
}

type DeploymentContainerConfig struct {
	Entrypoint   []string               `json:"entrypoint"`
	EnvVariables map[string]interface{} `json:"env_variables"`
	TrafficPort  int                    `json:"traffic_port"`
	ImageURL     string                 `json:"image_url"`
}

type Container struct {
	DeviceID         string           `json:"device_id"`
	ContainerID      string           `json:"container_id"`
	Hardware         string           `json:"hardware"`
	BrandName        string           `json:"brand_name"`
	CreatedAt        time.Time        `json:"created_at"`
	UptimePercent    int              `json:"uptime_percent"`
	GPUsPerContainer int              `json:"gpus_per_container"`
	Status           string           `json:"status"`
	ContainerEvents  []ContainerEvent `json:"container_events"`
	PublicURL        string           `json:"public_url"`
}

type ContainerEvent struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

type ContainerList struct {
	Total   int         `json:"total"`
	Workers []Container `json:"workers"`
}

type Deployment struct {
	ID                      string    `json:"id"`
	Status                  string    `json:"status"`
	Name                    string    `json:"name"`
	CompletedPercent        float64   `json:"completed_percent"`
	HardwareQuantity        int       `json:"hardware_quantity"`
	BrandName               string    `json:"brand_name"`
	HardwareName            string    `json:"hardware_name"`
	Served                  string    `json:"served"`
	Remaining               string    `json:"remaining"`
	ComputeMinutesServed    int       `json:"compute_minutes_served"`
	ComputeMinutesRemaining int       `json:"compute_minutes_remaining"`
	CreatedAt               time.Time `json:"created_at"`
	GPUCount                int       `json:"-"`
	Replicas                int       `json:"-"`
}

type DeploymentList struct {
	Deployments []Deployment `json:"deployments"`
	Total       int          `json:"total"`
	Statuses    []string     `json:"statuses"`
}

type AvailableReplica struct {
	LocationID     int    `json:"location_id"`
	LocationName   string `json:"location_name"`
	HardwareID     int    `json:"hardware_id"`
	HardwareName   string `json:"hardware_name"`
	AvailableCount int    `json:"available_count"`
	MaxGPUs        int    `json:"max_gpus"`
}

type AvailableReplicasResponse struct {
	Replicas []AvailableReplica `json:"replicas"`
}

type MaxGPUResponse struct {
	Hardware []MaxGPUInfo `json:"hardware"`
	Total    int          `json:"total"`
}

type MaxGPUInfo struct {
	MaxGPUsPerContainer int    `json:"max_gpus_per_container"`
	Available           int    `json:"available"`
	HardwareID          int    `json:"hardware_id"`
	HardwareName        string `json:"hardware_name"`
	BrandName           string `json:"brand_name"`
}

type PriceEstimationRequest struct {
	LocationIDs      []int  `json:"location_ids"`
	HardwareID       int    `json:"hardware_id"`
	GPUsPerContainer int    `json:"gpus_per_container"`
	DurationHours    int    `json:"duration_hours"`
	ReplicaCount     int    `json:"replica_count"`
	Currency         string `json:"currency"`
	DurationType     string `json:"duration_type"`
	DurationQty      int    `json:"duration_qty"`
	HardwareQty      int    `json:"hardware_qty"`
}

type PriceEstimationResponse struct {
	EstimatedCost   float64        `json:"estimated_cost"`
	Currency        string         `json:"currency"`
	PriceBreakdown  PriceBreakdown `json:"price_breakdown"`
	EstimationValid bool           `json:"estimation_valid"`
}

type PriceBreakdown struct {
	ComputeCost float64 `json:"compute_cost"`
	NetworkCost float64 `json:"network_cost,omitempty"`
	StorageCost float64 `json:"storage_cost,omitempty"`
	TotalCost   float64 `json:"total_cost"`
	HourlyRate  float64 `json:"hourly_rate"`
}

type UpdateDeploymentRequest struct {
	EnvVariables       map[string]string `json:"env_variables,omitempty"`
	SecretEnvVariables map[string]string `json:"secret_env_variables,omitempty"`
	Entrypoint         []string          `json:"entrypoint,omitempty"`
	TrafficPort        *int              `json:"traffic_port,omitempty"`
	ImageURL           string            `json:"image_url,omitempty"`
	RegistryUsername   string            `json:"registry_username,omitempty"`
	RegistrySecret     string            `json:"registry_secret,omitempty"`
	Args               []string          `json:"args,omitempty"`
	Command            string            `json:"command,omitempty"`
}

type ExtendDurationRequest struct {
	DurationHours int `json:"duration_hours"`
}

type UpdateDeploymentResponse struct {
	Status       string `json:"status"`
	DeploymentID string `json:"deployment_id"`
}

type UpdateClusterNameRequest struct {
	Name string `json:"cluster_name"`
}

type UpdateClusterNameResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ListDeploymentsOptions struct {
	Status     string
	LocationID int
	Page       int
	PageSize   int
	SortBy     string
	SortOrder  string
}

type GetLogsOptions struct {
	StartTime *time.Time
	EndTime   *time.Time
	Level     string
	Stream    string
	Limit     int
	Cursor    string
	Follow    bool
}

type HardwareType struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description,omitempty"`
	GPUType        string  `json:"gpu_type"`
	GPUMemory      int     `json:"gpu_memory"`
	MaxGPUs        int     `json:"max_gpus"`
	CPU            string  `json:"cpu,omitempty"`
	Memory         int     `json:"memory,omitempty"`
	Storage        int     `json:"storage,omitempty"`
	HourlyRate     float64 `json:"hourly_rate"`
	Available      bool    `json:"available"`
	BrandName      string  `json:"brand_name,omitempty"`
	AvailableCount int     `json:"available_count,omitempty"`
}

type Location struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	ISO2        string  `json:"iso2,omitempty"`
	Region      string  `json:"region,omitempty"`
	Country     string  `json:"country,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	Available   int     `json:"available,omitempty"`
	Description string  `json:"description,omitempty"`
}

type LocationsResponse struct {
	Locations []Location `json:"locations"`
	Total     int        `json:"total"`
}
