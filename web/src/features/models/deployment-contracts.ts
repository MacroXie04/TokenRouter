import { MAX_ARGUMENT_BYTES, MAX_ARGUMENT_ITEMS, MAX_CONTAINER_EVENTS, MAX_CONTAINER_EVENT_BYTES, MAX_ENVIRONMENT_KEY_BYTES, MAX_ENVIRONMENT_VALUE_BYTES, MAX_ENVIRONMENT_VARIABLES, MAX_ID, MAX_LOG_BYTES, MAX_LOG_RESPONSE_BYTES, MAX_METADATA_BYTES, MAX_PAGE, MAX_PROVIDER_ROWS, MAX_ROWS, MAX_TOTAL, MAX_UNIX_SECONDS, ModelsContractError, allowedKeys, boolean, boundedArray, envelope, finite, integer, optionalArray, optionalText, record, safeText, validID, validIdentifier } from './model-response';

export const DEPLOYMENT_PAGE_SIZE = 20;

export interface DeploymentAccess {
  provider: 'io.net';
  enabled: boolean;
  configured: boolean;
  canConnect: boolean;
}

export type DeploymentStatus =
  | ''
  | 'running'
  | 'completed'
  | 'failed'
  | 'deployment requested'
  | 'termination requested'
  | 'destroyed';

export interface DeploymentQuery {
  keyword: string;
  status: DeploymentStatus;
  page: number;
  pageSize: number;
}

export interface DeploymentSummary {
  id: string;
  name: string;
  status: string;
  provider: 'io.net';
  timeRemaining: string;
  hardwareInfo: string;
  hardwareName: string;
  brandName: string;
  hardwareQuantity: number;
  completedPercent: number;
  computeMinutesServed: number;
  computeMinutesRemaining: number;
  createdAt: number;
}

export interface DeploymentPage {
  items: DeploymentSummary[];
  total: number;
  page: number;
  pageSize: number;
  statusCounts: Record<string, number>;
}

export interface DeploymentDetail {
  id: string;
  status: string;
  hardwareId: number;
  hardwareName: string;
  brandName: string;
  totalGPUs: number;
  GPUsPerContainer: number;
  totalContainers: number;
  completedPercent: number;
  computeMinutesServed: number;
  computeMinutesRemaining: number;
  amountPaid: number;
  createdAt: number;
}

export interface DeploymentContainer {
  containerId: string;
  deviceId: string;
  status: string;
  hardware: string;
  brandName: string;
  createdAt: number;
  uptimePercent: number;
  GPUsPerContainer: number;
  publicURL: string;
  events: Array<{ time: number; message: string }>;
}

export interface DeploymentCreateInput {
  name: string;
  durationHours: number;
  GPUsPerContainer: number;
  hardwareId: number;
  locationIds: number[];
  replicaCount: number;
  image: string;
  registryUsername: string;
  registrySecret: string;
  trafficPort?: number;
  environmentVariables?: Record<string, string>;
  secretEnvironmentVariables?: Record<string, string>;
  entrypoint?: string[];
  args?: string[];
}

export interface DeploymentHardware {
  id: number;
  name: string;
  brandName: string;
  maxGPUs: number;
  available: boolean;
  availableCount: number;
}

export interface DeploymentHardwareCatalog {
  items: DeploymentHardware[];
  total: number;
  totalAvailable: number;
}

export interface DeploymentReplica {
  locationId: number;
  locationName: string;
  hardwareId: number;
  availableCount: number;
  maxGPUs: number;
}

export interface DeploymentLocation {
  id: number;
  name: string;
  iso2: string;
  region: string;
  country: string;
  available: number;
}

export interface DeploymentLocationCatalog {
  items: DeploymentLocation[];
  total: number;
}

export interface DeploymentPriceEstimate {
  estimatedCost: number;
  currency: string;
  computeCost: number;
  networkCost: number;
  storageCost: number;
  totalCost: number;
  hourlyRate: number;
}

export interface DeploymentUpdateInput {
  image?: string;
  trafficPort?: number;
  registryUsername?: string;
  registrySecret?: string;
  command?: string;
  environmentVariables?: Record<string, string>;
  secretEnvironmentVariables?: Record<string, string>;
  entrypoint?: string[];
  args?: string[];
}

export function parseDeploymentSettingsResponse(value: unknown): DeploymentAccess {
  const data = record(envelope(value).data);
  allowedKeys(data, ['provider', 'enabled', 'configured', 'can_connect']);
  if (data.provider !== 'io.net') throw new ModelsContractError();
  const enabled = boolean(data.enabled);
  const configured = boolean(data.configured);
  const canConnect = boolean(data.can_connect);
  if (canConnect !== (enabled && configured)) throw new ModelsContractError();
  return { provider: 'io.net', enabled, configured, canConnect };
}

export function parseConnectionResponse(value: unknown): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['hardware_count', 'total_available']);
  integer(data.hardware_count, 0, MAX_TOTAL);
  integer(data.total_available, 0, MAX_TOTAL);
}

export function parseDeploymentHardwareResponse(value: unknown): DeploymentHardwareCatalog {
  const data = record(envelope(value).data);
  allowedKeys(data, ['hardware_types', 'total', 'total_available']);
  const items = boundedArray(data.hardware_types, MAX_PROVIDER_ROWS).map((candidate) => {
    const hardware = record(candidate);
    allowedKeys(hardware, [
      'id', 'name', 'description', 'gpu_type', 'gpu_memory', 'max_gpus', 'cpu', 'memory', 'storage',
      'hourly_rate', 'available', 'brand_name', 'available_count',
    ]);
    optionalText(hardware.description, 2_048, true);
    optionalText(hardware.gpu_type, 256);
    integer(hardware.gpu_memory, 0, MAX_TOTAL);
    optionalText(hardware.cpu, 256);
    integer(hardware.memory ?? 0, 0, MAX_TOTAL);
    integer(hardware.storage ?? 0, 0, MAX_TOTAL);
    finite(hardware.hourly_rate, 0, Number.MAX_SAFE_INTEGER);
    return {
      id: validID(hardware.id as number),
      name: safeText(hardware.name, 256),
      brandName: optionalText(hardware.brand_name, 256),
      maxGPUs: integer(hardware.max_gpus, 0, 1_024),
      available: boolean(hardware.available),
      availableCount: integer(hardware.available_count ?? 0, 0, MAX_TOTAL),
    };
  });
  if (new Set(items.map((item) => item.id)).size !== items.length) throw new ModelsContractError();
  const total = integer(data.total, 0, MAX_PROVIDER_ROWS);
  if (total !== items.length) throw new ModelsContractError();
  return { items, total, totalAvailable: integer(data.total_available, 0, MAX_TOTAL) };
}

export function parseDeploymentLocationsResponse(value: unknown): DeploymentLocationCatalog {
  const data = record(envelope(value).data);
  allowedKeys(data, ['locations', 'total']);
  const items = boundedArray(data.locations, MAX_PROVIDER_ROWS).map((candidate) => {
    const location = record(candidate);
    allowedKeys(location, [
      'id', 'name', 'iso2', 'region', 'country', 'latitude', 'longitude', 'available', 'description',
    ]);
    finite(location.latitude ?? 0, -90, 90);
    finite(location.longitude ?? 0, -180, 180);
    optionalText(location.description, 2_048, true);
    return {
      id: validID(location.id as number),
      name: safeText(location.name, 256),
      iso2: optionalText(location.iso2, 8),
      region: optionalText(location.region, 256),
      country: optionalText(location.country, 256),
      available: integer(location.available ?? 0, 0, MAX_TOTAL),
    };
  });
  if (new Set(items.map((item) => item.id)).size !== items.length) throw new ModelsContractError();
  return { items, total: integer(data.total, 0, MAX_TOTAL) };
}

export function parseDeploymentReplicasResponse(value: unknown): DeploymentReplica[] {
  const data = record(envelope(value).data);
  allowedKeys(data, ['replicas']);
  const items = boundedArray(data.replicas, MAX_PROVIDER_ROWS).map((candidate) => {
    const replica = record(candidate);
    allowedKeys(replica, [
      'location_id', 'location_name', 'hardware_id', 'hardware_name', 'available_count', 'max_gpus',
    ]);
    optionalText(replica.hardware_name, 256);
    return {
      locationId: validID(replica.location_id as number),
      locationName: safeText(replica.location_name, 256),
      hardwareId: validID(replica.hardware_id as number),
      availableCount: integer(replica.available_count, 0, MAX_TOTAL),
      maxGPUs: integer(replica.max_gpus, 1, 1_024),
    };
  });
  if (new Set(items.map((item) => item.locationId)).size !== items.length) throw new ModelsContractError();
  return items;
}

export function parseDeploymentPriceResponse(value: unknown): DeploymentPriceEstimate {
  const data = record(envelope(value).data);
  allowedKeys(data, ['estimated_cost', 'currency', 'price_breakdown', 'estimation_valid']);
  if (!boolean(data.estimation_valid)) throw new ModelsContractError();
  const breakdown = record(data.price_breakdown);
  allowedKeys(breakdown, ['compute_cost', 'network_cost', 'storage_cost', 'total_cost', 'hourly_rate']);
  const estimatedCost = finite(data.estimated_cost, 0, Number.MAX_SAFE_INTEGER);
  const totalCost = finite(breakdown.total_cost, 0, Number.MAX_SAFE_INTEGER);
  if (estimatedCost !== totalCost) throw new ModelsContractError();
  return {
    estimatedCost,
    currency: validIdentifier(data.currency),
    computeCost: finite(breakdown.compute_cost, 0, Number.MAX_SAFE_INTEGER),
    networkCost: finite(breakdown.network_cost ?? 0, 0, Number.MAX_SAFE_INTEGER),
    storageCost: finite(breakdown.storage_cost ?? 0, 0, Number.MAX_SAFE_INTEGER),
    totalCost,
    hourlyRate: finite(breakdown.hourly_rate, 0, Number.MAX_SAFE_INTEGER),
  };
}

export function parseDeploymentNameResponse(value: unknown, expectedName: string): boolean {
  const data = record(envelope(value).data);
  allowedKeys(data, ['available', 'name']);
  if (safeText(data.name, 128) !== expectedName) throw new ModelsContractError();
  return boolean(data.available);
}

export function deploymentStatus(value: unknown): string {
  const parsed = safeText(value, 64).trim().toLowerCase();
  if (!parsed) throw new ModelsContractError();
  return parsed;
}

export function parseResourceConfig(value: unknown, expectedGPUs: number): void {
  const resource = record(value);
  allowedKeys(resource, ['cpu', 'memory', 'gpu']);
  optionalText(resource.cpu, 256);
  optionalText(resource.memory, 256);
  const gpu = safeText(resource.gpu, 32);
  if (!/^\d+$/u.test(gpu) || Number(gpu) !== expectedGPUs) throw new ModelsContractError();
}

export function parseDeployment(value: unknown): DeploymentSummary {
  const item = record(value);
  allowedKeys(item, [
    'id', 'deployment_name', 'container_name', 'status', 'type', 'time_remaining', 'time_remaining_minutes',
    'hardware_info', 'hardware_name', 'brand_name', 'hardware_quantity', 'completed_percent',
    'compute_minutes_served', 'compute_minutes_remaining', 'created_at', 'updated_at', 'model_name',
    'model_version', 'instance_count', 'resource_config', 'description', 'provider',
  ]);
  if (item.provider !== 'io.net') throw new ModelsContractError();
  const id = validIdentifier(item.id);
  const name = safeText(item.deployment_name, 128);
  if (safeText(item.container_name, 128) !== name || item.type !== 'Container') throw new ModelsContractError();
  const hardwareQuantity = integer(item.hardware_quantity, 0, 1_024_000);
  const computeMinutesRemaining = integer(item.compute_minutes_remaining, 0, Number.MAX_SAFE_INTEGER);
  if (integer(item.time_remaining_minutes, 0, Number.MAX_SAFE_INTEGER) !== computeMinutesRemaining) {
    throw new ModelsContractError();
  }
  const createdAt = integer(item.created_at, 0, MAX_UNIX_SECONDS);
  if (integer(item.updated_at, 0, MAX_UNIX_SECONDS) !== createdAt) throw new ModelsContractError();
  if (integer(item.instance_count, 0, 1_024_000) !== hardwareQuantity) throw new ModelsContractError();
  optionalText(item.model_name, 512);
  optionalText(item.model_version, 512);
  optionalText(item.description, MAX_METADATA_BYTES, true);
  parseResourceConfig(item.resource_config, hardwareQuantity);
  return {
    id,
    name,
    status: deploymentStatus(item.status),
    provider: 'io.net',
    timeRemaining: safeText(item.time_remaining, 128),
    hardwareInfo: safeText(item.hardware_info, 512),
    hardwareName: optionalText(item.hardware_name, 256),
    brandName: optionalText(item.brand_name, 256),
    hardwareQuantity,
    completedPercent: finite(item.completed_percent, 0, 100),
    computeMinutesServed: integer(item.compute_minutes_served, 0, Number.MAX_SAFE_INTEGER),
    computeMinutesRemaining,
    createdAt,
  };
}

export function parseDeploymentPageResponse(value: unknown): DeploymentPage {
  const data = record(envelope(value).data);
  allowedKeys(data, ['items', 'total', 'page', 'page_size', 'status_counts']);
  const counts: DeploymentPage['statusCounts'] = {};
  if (data.status_counts !== undefined && data.status_counts !== null) {
    const source = record(data.status_counts);
    if (Object.keys(source).length > 16) throw new ModelsContractError();
    for (const [status, count] of Object.entries(source)) {
      const safeStatus = safeText(status, 64);
      if (!safeStatus) throw new ModelsContractError();
      counts[safeStatus] = integer(count, 0, MAX_TOTAL);
    }
  }
  const items = boundedArray(data.items, MAX_ROWS).map(parseDeployment);
  if (new Set(items.map((item) => item.id)).size !== items.length) throw new ModelsContractError();
  const total = integer(data.total, 0, MAX_TOTAL);
  const pageSize = integer(data.page_size, 1, MAX_ROWS);
  if (items.length > pageSize || items.length > total) throw new ModelsContractError();
  return {
    items,
    total,
    page: integer(data.page, 1, MAX_PAGE),
    pageSize,
    statusCounts: counts,
  };
}

export function parseDeploymentDetailResponse(value: unknown): DeploymentDetail {
  const item = record(envelope(value).data);
  allowedKeys(item, [
    'id', 'deployment_name', 'model_name', 'model_version', 'status', 'instance_count', 'hardware_id',
    'resource_config', 'created_at', 'updated_at', 'description', 'amount_paid', 'completed_percent',
    'gpus_per_container', 'total_gpus', 'total_containers', 'hardware_name', 'brand_name',
    'compute_minutes_served', 'compute_minutes_remaining', 'locations', 'container_config',
  ]);
  const id = validIdentifier(item.id);
  if (safeText(item.deployment_name, 128) !== id) throw new ModelsContractError();
  optionalText(item.model_name, 512);
  optionalText(item.model_version, 512);
  optionalText(item.description, MAX_METADATA_BYTES, true);
  const totalGPUs = integer(item.total_gpus, 0, 1_024_000);
  const totalContainers = integer(item.total_containers, 0, 1_000);
  if (integer(item.instance_count, 0, 1_000) !== totalContainers) throw new ModelsContractError();
  parseResourceConfig(item.resource_config, totalGPUs);
  const createdAt = integer(item.created_at, 0, MAX_UNIX_SECONDS);
  if (integer(item.updated_at, 0, MAX_UNIX_SECONDS) !== createdAt) throw new ModelsContractError();
  const locations = optionalArray(item.locations, 100).map((candidate) => {
    const location = record(candidate);
    allowedKeys(location, ['id', 'iso2', 'name']);
    const locationID = validID(location.id as number);
    optionalText(location.iso2, 8);
    safeText(location.name, 256);
    return locationID;
  });
  if (new Set(locations).size !== locations.length) throw new ModelsContractError();
  // The provider may echo environment values. Validate their bounded shape, then discard the whole configuration.
  if (item.container_config !== undefined && item.container_config !== null) {
    const config = record(item.container_config);
    allowedKeys(config, ['entrypoint', 'env_variables', 'traffic_port', 'image_url']);
    if (config.entrypoint !== undefined && config.entrypoint !== null) {
      boundedArray(config.entrypoint, MAX_ARGUMENT_ITEMS).forEach((entry) => safeText(entry, MAX_ARGUMENT_BYTES));
    }
    if (config.env_variables !== undefined && config.env_variables !== null) {
      const environment = record(config.env_variables);
      if (Object.keys(environment).length > MAX_ENVIRONMENT_VARIABLES) throw new ModelsContractError();
      for (const [key, entry] of Object.entries(environment)) {
        safeText(key, MAX_ENVIRONMENT_KEY_BYTES);
        if (typeof entry === 'string') safeText(entry, MAX_ENVIRONMENT_VALUE_BYTES, true);
        else if (entry !== null && typeof entry !== 'number' && typeof entry !== 'boolean') throw new ModelsContractError();
      }
    }
    if (config.traffic_port !== undefined && config.traffic_port !== null) integer(config.traffic_port, 0, 65_535);
    if (config.image_url !== undefined && config.image_url !== null) optionalText(config.image_url, 2_048);
  }
  return {
    id,
    status: deploymentStatus(item.status),
    hardwareId: integer(item.hardware_id, 0, MAX_ID),
    hardwareName: safeText(item.hardware_name, 256),
    brandName: safeText(item.brand_name, 256),
    totalGPUs,
    GPUsPerContainer: integer(item.gpus_per_container, 0, 1_024),
    totalContainers,
    completedPercent: finite(item.completed_percent, 0, 100),
    computeMinutesServed: integer(item.compute_minutes_served, 0, Number.MAX_SAFE_INTEGER),
    computeMinutesRemaining: integer(item.compute_minutes_remaining, 0, Number.MAX_SAFE_INTEGER),
    amountPaid: finite(item.amount_paid, 0, Number.MAX_SAFE_INTEGER),
    createdAt,
  };
}

export function safePublicURL(value: unknown): string {
  if (value === undefined || value === null || value === '') return '';
  const text = safeText(value, 2_048);
  let parsed: URL;
  try {
    parsed = new URL(text);
  } catch {
    throw new ModelsContractError();
  }
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.hash || !parsed.hostname) {
    throw new ModelsContractError();
  }
  return parsed.toString();
}

export function parseContainer(value: unknown, expectedDeploymentID?: string): DeploymentContainer {
  const item = record(value);
  allowedKeys(item, [
    'container_id', 'device_id', 'status', 'hardware', 'brand_name', 'created_at', 'uptime_percent',
    'gpus_per_container', 'public_url', 'events', 'deployment_id',
  ]);
  if (item.deployment_id !== undefined) {
    const deploymentID = validIdentifier(item.deployment_id);
    if (expectedDeploymentID !== undefined && deploymentID !== expectedDeploymentID) throw new ModelsContractError();
  }
  const events = boundedArray(item.events ?? [], MAX_CONTAINER_EVENTS).map((entry) => {
    const event = record(entry);
    allowedKeys(event, ['time', 'message']);
    return {
      time: integer(event.time, 0, MAX_UNIX_SECONDS),
      message: safeText(event.message, MAX_CONTAINER_EVENT_BYTES),
    };
  });
  return {
    containerId: validIdentifier(item.container_id),
    deviceId: optionalText(item.device_id, 128),
    status: safeText(item.status, 64),
    hardware: optionalText(item.hardware, 256),
    brandName: optionalText(item.brand_name, 256),
    createdAt: integer(item.created_at, 0, MAX_UNIX_SECONDS),
    uptimePercent: integer(item.uptime_percent, 0, 100),
    GPUsPerContainer: integer(item.gpus_per_container, 0, 1_024),
    publicURL: safePublicURL(item.public_url),
    events,
  };
}

export function parseContainersResponse(value: unknown): DeploymentContainer[] {
  const data = record(envelope(value).data);
  allowedKeys(data, ['total', 'containers']);
  const containers = boundedArray(data.containers, MAX_PROVIDER_ROWS).map((item) => parseContainer(item));
  if (new Set(containers.map((item) => item.containerId)).size !== containers.length) throw new ModelsContractError();
  const total = integer(data.total, 0, MAX_PROVIDER_ROWS);
  if (total < containers.length) throw new ModelsContractError();
  return containers;
}

export function parseContainerResponse(value: unknown, expectedDeploymentID?: string): DeploymentContainer {
  return parseContainer(envelope(value).data, expectedDeploymentID);
}

export function parseLogsResponse(value: unknown): string {
  const logs = envelope(value, MAX_LOG_RESPONSE_BYTES).data;
  return safeText(logs, MAX_LOG_BYTES, true);
}

export function parseNullMutation(value: unknown): void {
  if (envelope(value).data !== null) throw new ModelsContractError();
}

export function parseDeploymentCreateMutation(value: unknown): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['deployment_id', 'status', 'message']);
  validIdentifier(data.deployment_id);
  safeText(data.status, 64);
  safeText(data.message, 1_024, true);
}

export function parseDeploymentUpdateMutation(value: unknown, expectedID: string): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['status', 'deployment_id']);
  safeText(data.status, 64);
  if (validIdentifier(data.deployment_id) !== expectedID) throw new ModelsContractError();
}

export function parseDeploymentRenameMutation(value: unknown, expectedID: string, expectedName: string): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['status', 'message', 'id', 'name']);
  safeText(data.status, 64);
  safeText(data.message, 1_024, true);
  if (validIdentifier(data.id) !== expectedID || safeText(data.name, 128) !== expectedName) {
    throw new ModelsContractError();
  }
}

export function parseDeploymentDeleteMutation(value: unknown, expectedID: string): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['status', 'deployment_id', 'message']);
  safeText(data.status, 64);
  safeText(data.message, 1_024, true);
  if (validIdentifier(data.deployment_id) !== expectedID) throw new ModelsContractError();
}
