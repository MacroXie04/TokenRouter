import { api } from '../../shared/api/client';
import { DeploymentAccess, DeploymentContainer, DeploymentCreateInput, DeploymentDetail, DeploymentHardwareCatalog, DeploymentLocationCatalog, DeploymentPage, DeploymentPriceEstimate, DeploymentQuery, DeploymentReplica, DeploymentUpdateInput, parseConnectionResponse, parseContainerResponse, parseContainersResponse, parseDeployment, parseDeploymentCreateMutation, parseDeploymentDeleteMutation, parseDeploymentDetailResponse, parseDeploymentHardwareResponse, parseDeploymentLocationsResponse, parseDeploymentNameResponse, parseDeploymentPageResponse, parseDeploymentPriceResponse, parseDeploymentRenameMutation, parseDeploymentReplicasResponse, parseDeploymentSettingsResponse, parseDeploymentUpdateMutation, parseLogsResponse } from './deployment-contracts';
import { DEPLOYMENT_STATUSES, IDENTIFIER, MAX_ARGUMENT_BYTES, MAX_ARGUMENT_ITEMS, MAX_DEPLOYMENT_REQUEST_BYTES, MAX_ENVIRONMENT_KEY_BYTES, MAX_ENVIRONMENT_VALUE_BYTES, MAX_ENVIRONMENT_VARIABLES, MAX_ID, MAX_LOG_RESPONSE_BYTES, MAX_PAGE, MAX_RESPONSE_BYTES, MAX_ROWS, ModelsContractError, UnknownRecord, envelope, integer, payloadBytes, responseData, responseLimits, safeText, trimInput, validIdentifier } from './model-response';

export const deploymentMutationLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_DEPLOYMENT_REQUEST_BYTES,
};

export function boundedDeploymentBody<T extends UnknownRecord>(body: T): T {
  if (payloadBytes(body) > MAX_DEPLOYMENT_REQUEST_BYTES) throw new ModelsContractError();
  return body;
}

export async function loadDeploymentSettings(signal?: AbortSignal): Promise<DeploymentAccess> {
  return parseDeploymentSettingsResponse(await responseData(api.get<unknown>('/deployments/settings', {
    signal, ...responseLimits,
  })));
}

export async function testDeploymentConnection(signal?: AbortSignal): Promise<void> {
  parseConnectionResponse(await responseData(api.post<unknown>('/deployments/settings/test-connection', {}, {
    signal, ...responseLimits,
  })));
}

export async function loadDeploymentHardware(signal?: AbortSignal): Promise<DeploymentHardwareCatalog> {
  return parseDeploymentHardwareResponse(await responseData(api.get<unknown>('/deployments/hardware-types', {
    signal, ...responseLimits,
  })));
}

export async function loadDeploymentLocations(signal?: AbortSignal): Promise<DeploymentLocationCatalog> {
  return parseDeploymentLocationsResponse(await responseData(api.get<unknown>('/deployments/locations', {
    signal, ...responseLimits,
  })));
}

export async function loadDeploymentReplicas(
  hardwareId: number,
  gpuCount: number,
  signal?: AbortSignal,
): Promise<DeploymentReplica[]> {
  const safeHardwareID = integer(hardwareId, 1, MAX_ID);
  const safeGPUCount = integer(gpuCount, 1, 1_024);
  const rows = parseDeploymentReplicasResponse(await responseData(api.get<unknown>('/deployments/available-replicas', {
    params: { hardware_id: safeHardwareID, gpu_count: safeGPUCount },
    signal,
    ...responseLimits,
  })));
  if (rows.some((row) => row.hardwareId !== safeHardwareID || row.maxGPUs !== safeGPUCount)) {
    throw new ModelsContractError();
  }
  return rows;
}

export async function checkDeploymentName(name: string, signal?: AbortSignal): Promise<boolean> {
  const normalized = trimInput(name, 128, true);
  return parseDeploymentNameResponse(await responseData(api.get<unknown>('/deployments/check-name', {
    params: { name: normalized },
    signal,
    ...responseLimits,
  })), normalized);
}

export async function estimateDeploymentPrice(
  input: Pick<DeploymentCreateInput, 'durationHours' | 'GPUsPerContainer' | 'hardwareId' | 'locationIds' | 'replicaCount'>,
  currency = 'usdc',
  signal?: AbortSignal,
): Promise<DeploymentPriceEstimate> {
  const locations = deploymentLocations(input.locationIds);
  const durationHours = integer(input.durationHours, 1, 43_920);
  const GPUsPerContainer = integer(input.GPUsPerContainer, 1, 1_024);
  const normalizedCurrency = trimInput(currency.toLowerCase(), 8, true);
  if (!IDENTIFIER.test(normalizedCurrency)) throw new ModelsContractError();
  return parseDeploymentPriceResponse(await responseData(api.post<unknown>('/deployments/price-estimation', boundedDeploymentBody({
    location_ids: locations,
    hardware_id: integer(input.hardwareId, 1, MAX_ID),
    gpus_per_container: GPUsPerContainer,
    duration_hours: durationHours,
    replica_count: integer(input.replicaCount, 1, 1_000),
    currency: normalizedCurrency,
    duration_type: 'hour',
    duration_qty: durationHours,
    hardware_qty: GPUsPerContainer,
  }), { signal, ...deploymentMutationLimits })));
}

export async function loadDeploymentAccess(signal?: AbortSignal): Promise<DeploymentAccess> {
  const access = await loadDeploymentSettings(signal);
  if (!access.canConnect) return access;
  await testDeploymentConnection(signal);
  return access;
}

export async function loadDeployments(query: DeploymentQuery, signal?: AbortSignal): Promise<DeploymentPage> {
  const keyword = trimInput(query.keyword, 256);
  const params: Record<string, string | number> = {
    p: integer(query.page, 1, MAX_PAGE),
    page_size: integer(query.pageSize, 1, MAX_ROWS),
  };
  if (keyword) params.keyword = keyword;
  if (query.status) {
    if (!DEPLOYMENT_STATUSES.has(query.status)) throw new ModelsContractError();
    params.status = query.status;
  }
  const path = keyword ? '/deployments/search' : '/deployments/';
  return parseDeploymentPageResponse(await responseData(api.get<unknown>(path, { params, signal, ...responseLimits })));
}

export async function getDeployment(id: string, signal?: AbortSignal): Promise<DeploymentDetail> {
  const safeID = validIdentifier(id);
  return parseDeploymentDetailResponse(await responseData(api.get<unknown>(`/deployments/${encodeURIComponent(safeID)}`, {
    signal, ...responseLimits,
  })));
}

export function deploymentLocations(values: number[]): number[] {
  const locations = values.map((id) => integer(id, 1, MAX_ID));
  if (locations.length < 1 || locations.length > 100 || new Set(locations).size !== locations.length) {
    throw new ModelsContractError();
  }
  return locations;
}

export function environmentInput(value: Record<string, string> | undefined): Record<string, string> | undefined {
  if (value === undefined) return undefined;
  const entries = Object.entries(value);
  if (entries.length === 0 || entries.length > MAX_ENVIRONMENT_VARIABLES) throw new ModelsContractError();
  const parsed: Record<string, string> = {};
  for (const [key, entry] of entries) {
    const safeKey = safeText(key, MAX_ENVIRONMENT_KEY_BYTES);
    if (!safeKey) throw new ModelsContractError();
    parsed[safeKey] = safeText(entry, MAX_ENVIRONMENT_VALUE_BYTES, true);
  }
  return parsed;
}

export function argumentInput(value: string[] | undefined): string[] | undefined {
  if (value === undefined) return undefined;
  if (value.length === 0 || value.length > MAX_ARGUMENT_ITEMS) throw new ModelsContractError();
  return value.map((entry) => {
    const parsed = safeText(entry, MAX_ARGUMENT_BYTES);
    if (!parsed) throw new ModelsContractError();
    return parsed;
  });
}

export function deploymentContainerBody(input: Pick<
  DeploymentCreateInput,
  'replicaCount' | 'trafficPort' | 'environmentVariables' | 'secretEnvironmentVariables' | 'entrypoint' | 'args'
>): UnknownRecord {
  return {
    replica_count: integer(input.replicaCount, 1, 1_000),
    ...(input.trafficPort === undefined ? {} : { traffic_port: integer(input.trafficPort, 1, 65_535) }),
    ...(input.environmentVariables === undefined ? {} : { env_variables: environmentInput(input.environmentVariables) }),
    ...(input.secretEnvironmentVariables === undefined
      ? {}
      : { secret_env_variables: environmentInput(input.secretEnvironmentVariables) }),
    ...(input.entrypoint === undefined ? {} : { entrypoint: argumentInput(input.entrypoint) }),
    ...(input.args === undefined ? {} : { args: argumentInput(input.args) }),
  };
}

export async function createDeployment(input: DeploymentCreateInput): Promise<void> {
  const locations = deploymentLocations(input.locationIds);
  const body = {
    resource_private_name: trimInput(input.name, 128, true),
    duration_hours: integer(input.durationHours, 1, 43_920),
    gpus_per_container: integer(input.GPUsPerContainer, 1, 1_024),
    hardware_id: integer(input.hardwareId, 1, MAX_ID),
    location_ids: locations,
    container_config: deploymentContainerBody(input),
    registry_config: {
      image_url: trimInput(input.image, 2_048, true),
      registry_username: trimInput(input.registryUsername, 4_096),
      registry_secret: safeText(input.registrySecret, 4_096),
    },
  };
  parseDeploymentCreateMutation(await responseData(api.post<unknown>(
    '/deployments/',
    boundedDeploymentBody(body),
    deploymentMutationLimits,
  )));
}

export async function updateDeployment(id: string, input: DeploymentUpdateInput): Promise<void> {
  const safeID = validIdentifier(id);
  const registrySecret = input.registrySecret === undefined ? undefined : safeText(input.registrySecret, 4_096);
  if (registrySecret === '') throw new ModelsContractError();
  const body: UnknownRecord = {
    ...(input.image === undefined ? {} : { image_url: trimInput(input.image, 2_048, true) }),
    ...(input.trafficPort === undefined ? {} : { traffic_port: integer(input.trafficPort, 1, 65_535) }),
    ...(input.registryUsername === undefined
      ? {}
      : { registry_username: trimInput(input.registryUsername, 4_096, true) }),
    ...(registrySecret === undefined ? {} : { registry_secret: registrySecret }),
    ...(input.command === undefined ? {} : { command: trimInput(input.command, MAX_ARGUMENT_BYTES, true) }),
    ...(input.environmentVariables === undefined ? {} : { env_variables: environmentInput(input.environmentVariables) }),
    ...(input.secretEnvironmentVariables === undefined
      ? {}
      : { secret_env_variables: environmentInput(input.secretEnvironmentVariables) }),
    ...(input.entrypoint === undefined ? {} : { entrypoint: argumentInput(input.entrypoint) }),
    ...(input.args === undefined ? {} : { args: argumentInput(input.args) }),
  };
  if (Object.keys(body).length === 0) throw new ModelsContractError();
  parseDeploymentUpdateMutation(await responseData(api.put<unknown>(
    `/deployments/${encodeURIComponent(safeID)}`,
    boundedDeploymentBody(body),
    deploymentMutationLimits,
  )), safeID);
}

export async function renameDeployment(id: string, name: string): Promise<void> {
  const safeID = validIdentifier(id);
  const safeName = trimInput(name, 128, true);
  parseDeploymentRenameMutation(await responseData(api.put<unknown>(
    `/deployments/${encodeURIComponent(safeID)}/name`,
    { name: safeName },
    responseLimits,
  )), safeID, safeName);
}

export async function extendDeployment(id: string, durationHours: number): Promise<void> {
  const safeID = validIdentifier(id);
  const result = parseDeployment(envelope(await responseData(api.post<unknown>(
    `/deployments/${encodeURIComponent(safeID)}/extend`,
    { duration_hours: integer(durationHours, 1, 43_920) },
    responseLimits,
  ))).data);
  if (result.id !== safeID) throw new ModelsContractError();
}

export async function deleteDeployment(id: string): Promise<void> {
  const safeID = validIdentifier(id);
  parseDeploymentDeleteMutation(await responseData(api.delete<unknown>(
    `/deployments/${encodeURIComponent(safeID)}`,
    responseLimits,
  )), safeID);
}

export async function loadDeploymentContainers(id: string, signal?: AbortSignal): Promise<DeploymentContainer[]> {
  return parseContainersResponse(await responseData(api.get<unknown>(
    `/deployments/${encodeURIComponent(validIdentifier(id))}/containers`,
    { signal, ...responseLimits },
  )));
}

export async function getDeploymentContainer(
  deploymentId: string,
  containerId: string,
  signal?: AbortSignal,
): Promise<DeploymentContainer> {
  const safeDeploymentID = validIdentifier(deploymentId);
  const safeContainerID = validIdentifier(containerId);
  const parsed = parseContainerResponse(await responseData(api.get<unknown>(
    `/deployments/${encodeURIComponent(safeDeploymentID)}/containers/${encodeURIComponent(safeContainerID)}`,
    { signal, ...responseLimits },
  )), safeDeploymentID);
  if (parsed.containerId !== safeContainerID) throw new ModelsContractError();
  return parsed;
}

export async function loadDeploymentLogs(
  deploymentId: string,
  containerId: string,
  signal?: AbortSignal,
): Promise<string> {
  return parseLogsResponse(await responseData(api.get<unknown>(
    `/deployments/${encodeURIComponent(validIdentifier(deploymentId))}/logs`,
    {
      params: { container_id: validIdentifier(containerId), stream: 'all', limit: 500, follow: false },
      signal,
      maxContentLength: MAX_LOG_RESPONSE_BYTES,
      maxBodyLength: MAX_RESPONSE_BYTES,
    },
  )));
}
