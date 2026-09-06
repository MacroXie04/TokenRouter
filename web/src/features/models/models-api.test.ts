import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  ModelsAccessError,
  ModelsContractError,
  assertModelsAdministrator,
  checkDeploymentName,
  createDeployment,
  createModel,
  createVendor,
  deleteDeployment,
  deleteModel,
  deleteVendor,
  estimateDeploymentPrice,
  extendDeployment,
  getDeployment,
  getDeploymentContainer,
  getModel,
  getVendor,
  loadDeploymentAccess,
  loadDeploymentContainers,
  loadDeploymentHardware,
  loadDeploymentLocations,
  loadDeploymentLogs,
  loadDeploymentReplicas,
  loadDeploymentSettings,
  loadDeployments,
  loadMissingModels,
  loadModels,
  loadVendors,
  parseContainerResponse,
  parseContainersResponse,
  parseDeploymentDetailResponse,
  parseDeploymentHardwareResponse,
  parseDeploymentLocationsResponse,
  parseDeploymentNameResponse,
  parseDeploymentPageResponse,
  parseDeploymentPriceResponse,
  parseDeploymentReplicasResponse,
  parseDeploymentSettingsResponse,
  parseLogsResponse,
  parseModelPageResponse,
  parseSyncPreviewResponse,
  previewUpstream,
  renameDeployment,
  setModelStatus,
  syncUpstream,
  testDeploymentConnection,
  updateDeployment,
  updateModel,
  updateVendor,
  type DeploymentCreateInput,
  type ModelMutationInput,
  type VendorMutationInput,
} from './models-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);
const mockedPut = vi.mocked(api.put);
const mockedDelete = vi.mocked(api.delete);

function response(data: unknown) {
  return { data };
}

function success(data: unknown) {
  return { success: true, message: '', data };
}

function model(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    model_name: 'gpt-4o',
    description: 'Fast model',
    icon: 'openai',
    tags: 'chat,vision',
    vendor_id: 3,
    endpoints: '["chat"]',
    status: 1,
    sync_official: 1,
    created_time: 1_780_000_000,
    updated_time: 1_780_000_100,
    name_rule: 0,
    bound_channels: [{ name: 'primary', type: 1 }],
    enable_groups: ['default'],
    quota_types: [1],
    matched_models: [],
    matched_count: 0,
    supported_endpoint_types: ['openai'],
    ...overrides,
  };
}

function vendor(overrides: Record<string, unknown> = {}) {
  return {
    id: 3,
    name: 'OpenAI',
    description: 'Provider',
    icon: 'openai',
    status: 1,
    created_time: 1_780_000_000,
    updated_time: 1_780_000_100,
    ...overrides,
  };
}

function deployment(overrides: Record<string, unknown> = {}) {
  return {
    id: 'cluster:abc',
    deployment_name: 'production-chat',
    container_name: 'production-chat',
    status: 'running',
    type: 'Container',
    time_remaining: '2 hour 10 minutes',
    time_remaining_minutes: 130,
    hardware_info: 'NVIDIA H100 x2',
    hardware_name: 'H100',
    brand_name: 'NVIDIA',
    hardware_quantity: 2,
    completed_percent: 12.5,
    compute_minutes_served: 20,
    compute_minutes_remaining: 130,
    created_at: 1_780_000_000,
    updated_at: 1_780_000_000,
    model_name: '',
    model_version: '',
    instance_count: 2,
    resource_config: { cpu: '', memory: '', gpu: '2' },
    description: '',
    provider: 'io.net',
    ...overrides,
  };
}

function detail(overrides: Record<string, unknown> = {}) {
  return {
    id: 'cluster:abc',
    deployment_name: 'cluster:abc',
    model_name: '',
    model_version: '',
    status: 'running',
    instance_count: 2,
    hardware_id: 9,
    resource_config: { cpu: '', memory: '', gpu: '2' },
    created_at: 1_780_000_000,
    updated_at: 1_780_000_000,
    description: '',
    amount_paid: 1.25,
    completed_percent: 12.5,
    gpus_per_container: 1,
    total_gpus: 2,
    total_containers: 2,
    hardware_name: 'H100',
    brand_name: 'NVIDIA',
    compute_minutes_served: 20,
    compute_minutes_remaining: 130,
    locations: [{ id: 4, iso2: 'US', name: 'California' }],
    container_config: { env_variables: { OPENAI_API_KEY: 'sk-never-render' }, image_url: 'private/image' },
    ...overrides,
  };
}

function container(overrides: Record<string, unknown> = {}) {
  return {
    container_id: 'worker:1',
    device_id: 'device-1',
    status: 'running',
    hardware: 'H100',
    brand_name: 'NVIDIA',
    created_at: 1_780_000_000,
    uptime_percent: 99,
    gpus_per_container: 1,
    public_url: 'https://worker.example.test/',
    events: [{ time: 1_780_000_100, message: 'Started' }],
    ...overrides,
  };
}

const modelInput: ModelMutationInput = {
  modelName: ' gpt-4o ',
  description: ' Fast ',
  icon: ' openai ',
  tags: ' chat ',
  vendorId: 3,
  endpoints: ' ["chat"] ',
  status: 1,
  syncOfficial: 1,
  nameRule: 0,
};

const vendorInput: VendorMutationInput = {
  name: ' OpenAI ', description: ' Provider ', icon: ' icon ', status: 1,
};

const deploymentInput: DeploymentCreateInput = {
  name: ' production-chat ',
  durationHours: 24,
  GPUsPerContainer: 2,
  hardwareId: 9,
  locationIds: [4, 5],
  replicaCount: 3,
  image: ' registry.example/chat:latest ',
  registryUsername: ' robot ',
  registrySecret: 'registry-password',
};

beforeEach(() => vi.resetAllMocks());

describe('model metadata transport', () => {
  it('uses exact paginated list and search contracts with bounded browser transport', async () => {
    mockedGet.mockResolvedValue(response(success({ items: [model()], total: 21, page: 2, page_size: 20, vendor_counts: { 3: 21 } })) as never);
    const signal = new AbortController().signal;
    const result = await loadModels({
      keyword: ' gpt ', vendor: ' 3 ', status: 'enabled', syncOfficial: 'yes', page: 2, pageSize: 20,
    }, signal);
    expect(result.items[0]).toMatchObject({ id: 7, modelName: 'gpt-4o', vendorId: 3 });
    expect(mockedGet).toHaveBeenCalledWith('/models/search', expect.objectContaining({
      params: { p: 2, page_size: 20, keyword: 'gpt', vendor: '3', status: 'enabled', sync_official: 'yes' },
      signal,
      maxContentLength: 2 * 1024 * 1024,
      maxBodyLength: 2 * 1024 * 1024,
    }));

    mockedGet.mockClear();
    await loadModels({ keyword: '', vendor: '', status: '', syncOfficial: '', page: 1, pageSize: 20 });
    expect(mockedGet).toHaveBeenCalledWith('/models/', expect.objectContaining({ params: { p: 1, page_size: 20 } }));
  });

  it('covers the vendor catalog, detail, CRUD, status, delete, and discovery endpoints', async () => {
    mockedGet
      .mockResolvedValueOnce(response(success({ items: [vendor()], total: 1, page: 1, page_size: 100 })) as never)
      .mockResolvedValueOnce(response(success(model())) as never)
      .mockResolvedValueOnce(response(success(vendor())) as never)
      .mockResolvedValueOnce(response(success(['new-model'])) as never);
    mockedPost
      .mockResolvedValueOnce(response(success(model())) as never)
      .mockResolvedValueOnce(response(success(vendor())) as never);
    mockedPut
      .mockResolvedValueOnce(response(success(model())) as never)
      .mockResolvedValueOnce(response(success(model({ status: 0 }))) as never)
      .mockResolvedValueOnce(response(success(vendor())) as never);
    mockedDelete.mockResolvedValue(response(success(null)) as never);

    const signal = new AbortController().signal;
    await loadVendors(signal, ' open ');
    await getModel(7, signal);
    await getVendor(3, signal);
    await createModel(modelInput);
    await updateModel({ ...modelInput, id: 7 });
    await setModelStatus(7, 0);
    await createVendor(vendorInput);
    await updateVendor({ ...vendorInput, id: 3 });
    await deleteModel(7);
    await deleteVendor(3);
    await loadMissingModels(signal);

    expect(mockedGet.mock.calls.map(([url]) => url)).toEqual([
      '/vendors/search', '/models/7', '/vendors/3', '/models/missing',
    ]);
    expect(mockedGet.mock.calls[0][1]).toEqual(expect.objectContaining({ params: { p: 1, page_size: 100, keyword: 'open' }, signal }));
    expect(mockedPost.mock.calls[0].slice(0, 2)).toEqual(['/models/', {
      model_name: 'gpt-4o', description: 'Fast', icon: 'openai', tags: 'chat', vendor_id: 3,
      endpoints: '["chat"]', status: 1, sync_official: 1, name_rule: 0,
    }]);
    expect(mockedPut.mock.calls[0][0]).toBe('/models/');
    expect(mockedPut.mock.calls[0][1]).toMatchObject({ id: 7 });
    expect(mockedPut.mock.calls[1].slice(0, 2)).toEqual(['/models/?status_only=true', { id: 7, status: 0 }]);
    expect(mockedPost.mock.calls[1][0]).toBe('/vendors/');
    expect(mockedPut.mock.calls[2][0]).toBe('/vendors/');
    expect(mockedDelete.mock.calls.map(([url]) => url)).toEqual(['/models/7', '/vendors/3']);
  });

  it('previews and applies only the locally supported upstream fields without retaining source URLs', async () => {
    const previewPayload = success({
      missing: ['new-model'],
      conflicts: [{ model_name: 'gpt-4o', fields: [{ field: 'description', local: 'old', upstream: 'new' }] }],
      source: { locale: 'en', models_url: 'https://catalog/models.json', vendors_url: 'https://catalog/vendors.json' },
    });
    mockedGet.mockResolvedValue(response(previewPayload) as never);
    mockedPost.mockResolvedValue(response(success({
      created_models: 1, created_vendors: 0, updated_models: 1, skipped_models: [],
      created_list: ['new-model'], updated_list: ['gpt-4o'],
      source: { locale: 'en', models_url: 'https://catalog/models.json', vendors_url: 'https://catalog/vendors.json' },
    })) as never);

    const preview = await previewUpstream('en');
    expect(preview).toEqual({ missing: ['new-model'], conflicts: [{ modelName: 'gpt-4o', fields: ['description'] }] });
    expect(JSON.stringify(preview)).not.toContain('catalog');
    await syncUpstream('en', preview);
    expect(mockedGet).toHaveBeenCalledWith('/models/sync_upstream/preview', expect.objectContaining({ params: { locale: 'en' } }));
    expect(mockedPost.mock.calls[0].slice(0, 2)).toEqual(['/models/sync_upstream', {
      locale: 'en', overwrite: [{ model_name: 'gpt-4o', fields: ['description'] }],
    }]);
    expect(parseSyncPreviewResponse(success({
      missing: null,
      conflicts: null,
      source: { locale: '', models_url: 'https://catalog/models.json', vendors_url: 'https://catalog/vendors.json' },
    }))).toEqual({ missing: [], conflicts: [] });
  });
});

describe('deployment transport', () => {
  it('inspects settings first, skips connection when closed, and tests configured access without an API key payload', async () => {
    mockedGet
      .mockResolvedValueOnce(response(success({ provider: 'io.net', enabled: false, configured: true, can_connect: false })) as never)
      .mockResolvedValueOnce(response(success({ provider: 'io.net', enabled: true, configured: true, can_connect: true })) as never)
      .mockResolvedValueOnce(response(success({ provider: 'io.net', enabled: true, configured: true, can_connect: true })) as never);
    mockedPost.mockResolvedValue(response(success({ hardware_count: 3, total_available: 12 })) as never);

    expect(await loadDeploymentAccess()).toMatchObject({ enabled: false, canConnect: false });
    expect(mockedPost).not.toHaveBeenCalled();
    expect(await loadDeploymentAccess()).toMatchObject({ enabled: true, canConnect: true });
    await loadDeploymentSettings();
    await testDeploymentConnection();
    expect(mockedPost).toHaveBeenCalledTimes(2);
    expect(mockedPost.mock.calls[0].slice(0, 2)).toEqual(['/deployments/settings/test-connection', {}]);
    expect(JSON.stringify(mockedPost.mock.calls)).not.toMatch(/api_key|stored-secret/);
  });

  it('uses exact list/search/detail/create/rename/extend/delete/container/log contracts', async () => {
    mockedGet
      .mockResolvedValueOnce(response(success({ items: [deployment()], total: 2, page: 1, page_size: 20, status_counts: { all: 2, running: 1 } })) as never)
      .mockResolvedValueOnce(response(success({ items: [deployment()], total: 1, page: 1, page_size: 20 })) as never)
      .mockResolvedValueOnce(response(success(detail())) as never)
      .mockResolvedValueOnce(response(success({ total: 1, containers: [container()] })) as never)
      .mockResolvedValueOnce(response(success({ ...container(), deployment_id: 'cluster:abc' })) as never)
      .mockResolvedValueOnce(response(success('line one\nline two')) as never);
    mockedPost
      .mockResolvedValueOnce(response(success({
        deployment_id: 'cluster:abc', status: 'deployment requested', message: 'Deployment created successfully',
      })) as never)
      .mockResolvedValueOnce(response(success(deployment())) as never);
    mockedPut.mockResolvedValue(response(success({
      status: 'ok', message: 'renamed', id: 'cluster:abc', name: 'renamed',
    })) as never);
    mockedDelete.mockResolvedValue(response(success({
      status: 'termination requested', deployment_id: 'cluster:abc', message: 'Deployment termination requested successfully',
    })) as never);
    const signal = new AbortController().signal;

    await loadDeployments({ keyword: '', status: 'running', page: 1, pageSize: 20 }, signal);
    await loadDeployments({ keyword: ' chat ', status: '', page: 1, pageSize: 20 }, signal);
    const parsedDetail = await getDeployment('cluster:abc', signal);
    await createDeployment(deploymentInput);
    await renameDeployment('cluster:abc', ' renamed ');
    await extendDeployment('cluster:abc', 48);
    await deleteDeployment('cluster:abc');
    await loadDeploymentContainers('cluster:abc', signal);
    await getDeploymentContainer('cluster:abc', 'worker:1', signal);
    await loadDeploymentLogs('cluster:abc', 'worker:1', signal);

    expect(JSON.stringify(parsedDetail)).not.toMatch(/OPENAI_API_KEY|sk-never-render|container_config/);
    expect(mockedGet.mock.calls.map(([url]) => url)).toEqual([
      '/deployments/', '/deployments/search', '/deployments/cluster%3Aabc',
      '/deployments/cluster%3Aabc/containers', '/deployments/cluster%3Aabc/containers/worker%3A1',
      '/deployments/cluster%3Aabc/logs',
    ]);
    expect(mockedGet.mock.calls[0][1]).toEqual(expect.objectContaining({ params: { p: 1, page_size: 20, status: 'running' }, signal }));
    expect(mockedGet.mock.calls[1][1]).toEqual(expect.objectContaining({ params: { p: 1, page_size: 20, keyword: 'chat' } }));
    expect(mockedGet.mock.calls[5][1]).toEqual(expect.objectContaining({
      params: { container_id: 'worker:1', stream: 'all', limit: 500, follow: false }, signal,
    }));
    expect(mockedPost.mock.calls[0][0]).toBe('/deployments/');
    expect(mockedPost.mock.calls[0][1]).toEqual({
      resource_private_name: 'production-chat', duration_hours: 24, gpus_per_container: 2,
      hardware_id: 9, location_ids: [4, 5], container_config: { replica_count: 3 },
      registry_config: { image_url: 'registry.example/chat:latest', registry_username: 'robot', registry_secret: 'registry-password' },
    });
    expect(mockedPut.mock.calls[0].slice(0, 2)).toEqual(['/deployments/cluster%3Aabc/name', { name: 'renamed' }]);
    expect(mockedPost.mock.calls[1].slice(0, 2)).toEqual(['/deployments/cluster%3Aabc/extend', { duration_hours: 48 }]);
    expect(mockedDelete.mock.calls[0][0]).toBe('/deployments/cluster%3Aabc');
  });

  it('uses bounded hardware, location, replica, name, price, and update contracts', async () => {
    const hardwarePayload = success({
      hardware_types: [{
        id: 9, name: 'H100', description: 'Accelerator', gpu_type: 'H100', gpu_memory: 80,
        max_gpus: 8, cpu: 'x86', memory: 128, storage: 1024, hourly_rate: 2.5,
        available: true, brand_name: 'NVIDIA', available_count: 12,
      }],
      total: 1,
      total_available: 12,
    });
    const locationPayload = success({
      locations: [{
        id: 4, name: 'California', iso2: 'US', region: 'west', country: 'United States',
        latitude: 37.2, longitude: -121.8, available: 6, description: '',
      }],
      total: 1,
    });
    const replicaPayload = success({ replicas: [{
      location_id: 4, location_name: 'California', hardware_id: 9, hardware_name: 'H100',
      available_count: 6, max_gpus: 2,
    }] });
    const pricePayload = success({
      estimated_cost: 15, currency: 'usdc', estimation_valid: true,
      price_breakdown: { compute_cost: 15, network_cost: 0, storage_cost: 0, total_cost: 15, hourly_rate: 2.5 },
    });
    mockedGet
      .mockResolvedValueOnce(response(hardwarePayload) as never)
      .mockResolvedValueOnce(response(locationPayload) as never)
      .mockResolvedValueOnce(response(replicaPayload) as never)
      .mockResolvedValueOnce(response(success({ available: true, name: 'production-chat' })) as never);
    mockedPost.mockResolvedValueOnce(response(pricePayload) as never);
    mockedPut.mockResolvedValueOnce(response(success({ status: 'updated', deployment_id: 'cluster:abc' })) as never);
    const signal = new AbortController().signal;

    expect((await loadDeploymentHardware(signal)).items[0]).toMatchObject({ id: 9, maxGPUs: 8, availableCount: 12 });
    expect((await loadDeploymentLocations(signal)).items[0]).toMatchObject({ id: 4, iso2: 'US' });
    expect(await loadDeploymentReplicas(9, 2, signal)).toEqual([expect.objectContaining({ locationId: 4, maxGPUs: 2 })]);
    await expect(checkDeploymentName(' production-chat ', signal)).resolves.toBe(true);
    await expect(estimateDeploymentPrice({
      durationHours: 24, GPUsPerContainer: 2, hardwareId: 9, locationIds: [4], replicaCount: 3,
    }, 'USDC', signal)).resolves.toMatchObject({ totalCost: 15, currency: 'usdc' });
    await updateDeployment('cluster:abc', {
      image: ' registry.example/chat:v2 ', trafficPort: 8080, registryUsername: 'robot',
      registrySecret: 'secret', command: 'serve', environmentVariables: { MODE: 'fast' },
      secretEnvironmentVariables: { TOKEN: 'hidden' }, entrypoint: ['bash', '-lc'], args: ['--port', '8080'],
    });

    expect(mockedGet.mock.calls.map(([url]) => url)).toEqual([
      '/deployments/hardware-types', '/deployments/locations', '/deployments/available-replicas',
      '/deployments/check-name',
    ]);
    expect(mockedGet.mock.calls[2][1]).toEqual(expect.objectContaining({
      params: { hardware_id: 9, gpu_count: 2 }, signal,
    }));
    expect(mockedGet.mock.calls[3][1]).toEqual(expect.objectContaining({ params: { name: 'production-chat' }, signal }));
    expect(mockedPost.mock.calls[0].slice(0, 2)).toEqual(['/deployments/price-estimation', {
      location_ids: [4], hardware_id: 9, gpus_per_container: 2, duration_hours: 24, replica_count: 3,
      currency: 'usdc', duration_type: 'hour', duration_qty: 24, hardware_qty: 2,
    }]);
    expect(mockedPut.mock.calls[0].slice(0, 2)).toEqual(['/deployments/cluster%3Aabc', {
      image_url: 'registry.example/chat:v2', traffic_port: 8080, registry_username: 'robot',
      registry_secret: 'secret', command: 'serve', env_variables: { MODE: 'fast' },
      secret_env_variables: { TOKEN: 'hidden' }, entrypoint: ['bash', '-lc'], args: ['--port', '8080'],
    }]);

    expect(parseDeploymentHardwareResponse(hardwarePayload).totalAvailable).toBe(12);
    expect(parseDeploymentLocationsResponse(locationPayload).total).toBe(1);
    expect(parseDeploymentReplicasResponse(replicaPayload)).toHaveLength(1);
    expect(parseDeploymentNameResponse(success({ available: false, name: 'taken' }), 'taken')).toBe(false);
    expect(parseDeploymentPriceResponse(pricePayload).hourlyRate).toBe(2.5);
  });
});

describe('bounded and redacting response contracts', () => {
  it('rejects failed, malformed, unexpected, credential-bearing, oversized, and overflowing model payloads generically', async () => {
    const invalid = [
      { success: false, message: 'database password sk-private', data: {} },
      success({ items: [{ ...model(), api_key: 'sk-private' }], total: 1, page: 1, page_size: 20, vendor_counts: {} }),
      success({ items: [model({ status: 2 })], total: 1, page: 1, page_size: 20, vendor_counts: {} }),
      success({ items: [model({ model_name: 'bad\u0000model' })], total: 1, page: 1, page_size: 20, vendor_counts: {} }),
      success({ items: new Array(101).fill(model()), total: 101, page: 1, page_size: 100, vendor_counts: {} }),
      success({ items: [], total: Number.MAX_SAFE_INTEGER, page: 1, page_size: 20, vendor_counts: {} }),
      { ...success({ items: [], total: 0, page: 1, page_size: 20, vendor_counts: {} }), padding: 'unexpected' },
    ];
    for (const value of invalid) expect(() => parseModelPageResponse(value)).toThrow(ModelsContractError);

    mockedGet.mockResolvedValue(response({
      success: false, message: 'postgres://admin:secret@db/internal', data: {}, padding: 'x'.repeat(2 * 1024 * 1024),
    }) as never);
    await expect(loadModels({ keyword: '', vendor: '', status: '', syncOfficial: '', page: 1, pageSize: 20 }))
      .rejects.toEqual(new ModelsContractError());
  });

  it('rejects invalid deployment status, unsafe URLs, invalid settings invariants, log overflow, and sync fields', () => {
    expect(() => parseDeploymentPageResponse(success({ items: [deployment({ status: 'bad\u0000status' })], total: 1, page: 1, page_size: 20 }))).toThrow(ModelsContractError);
    expect(() => parseContainerResponse(success(container({ public_url: 'javascript:alert(1)' })))).toThrow(ModelsContractError);
    expect(() => parseContainersResponse(success({ total: 0, containers: [container()] }))).toThrow(ModelsContractError);
    expect(() => parseDeploymentDetailResponse(success(detail({ amount_paid: Number.POSITIVE_INFINITY })))).toThrow(ModelsContractError);
    expect(() => parseDeploymentSettingsResponse(success({ provider: 'io.net', enabled: false, configured: true, can_connect: true }))).toThrow(ModelsContractError);
    expect(() => parseLogsResponse(success('x'.repeat(4 * 1024 * 1024 + 1)))).toThrow(ModelsContractError);
    expect(() => parseSyncPreviewResponse(success({ missing: [], conflicts: [{ model_name: 'gpt', fields: [{ field: 'api_key' }] }], source: { locale: '', models_url: 'x', vendors_url: 'y' } }))).toThrow(ModelsContractError);
    expect(() => parseContainerResponse(success(container({ events: new Array(2_001).fill({ time: 1, message: 'ok' }) })))).toThrow(ModelsContractError);
    expect(() => parseContainerResponse(success(container({ events: [{ time: 1, message: 'x'.repeat(64 * 1024 + 1) }] })))).toThrow(ModelsContractError);
    expect(() => parseDeploymentNameResponse(success({ available: true, name: 'other' }), 'expected')).toThrow(ModelsContractError);
  });

  it('fails closed on authorization and invalid mutation inputs before transport', async () => {
    expect(() => assertModelsAdministrator(1)).toThrow(ModelsAccessError);
    expect(() => assertModelsAdministrator(11)).toThrow(ModelsAccessError);
    expect(() => assertModelsAdministrator(10)).not.toThrow();
    expect(() => assertModelsAdministrator(100)).not.toThrow();
    await expect(createModel({ ...modelInput, endpoints: '{not json}' })).rejects.toThrow(ModelsContractError);
    await expect(createModel({ ...modelInput, modelName: 'x'.repeat(129) })).rejects.toThrow(ModelsContractError);
    await expect(createVendor({ ...vendorInput, name: 'x'.repeat(129) })).rejects.toThrow(ModelsContractError);
    await expect(createDeployment({ ...deploymentInput, locationIds: [4, 4] })).rejects.toThrow(ModelsContractError);
    await expect(createDeployment({
      ...deploymentInput,
      environmentVariables: Object.fromEntries(Array.from({ length: 33 }, (_, index) => [`KEY_${index}`, 'x'.repeat(8_192)])),
    })).rejects.toThrow(ModelsContractError);
    await expect(renameDeployment('../secret', 'name')).rejects.toThrow(ModelsContractError);
    expect(mockedPost).not.toHaveBeenCalled();
    expect(mockedPut).not.toHaveBeenCalled();
  });
});
