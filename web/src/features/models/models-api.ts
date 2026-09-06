import { api } from '../../api';

export const MODEL_PAGE_SIZE = 20;
export const DEPLOYMENT_PAGE_SIZE = 20;
export const MODEL_SECTIONS = ['metadata', 'deployments'] as const;
export type ModelsSection = (typeof MODEL_SECTIONS)[number];

const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_ID = 2_147_483_647;
const MAX_TOTAL = 1_000_000_000;
const MAX_PAGE = 1_000_000;
const MAX_ROWS = 100;
const MAX_UNIX_SECONDS = 4_102_444_800;
const MAX_METADATA_BYTES = 1024 * 1024;
const MAX_RELATED_ITEMS = 10_000;
const MAX_LOG_BYTES = 4 * 1024 * 1024;
const MAX_LOG_RESPONSE_BYTES = (MAX_LOG_BYTES * 2) + 1_024;
const MAX_DEPLOYMENT_REQUEST_BYTES = 256 * 1024;
const MAX_PROVIDER_ROWS = 5_000;
const MAX_CONTAINER_EVENTS = 2_000;
const MAX_CONTAINER_EVENT_BYTES = 64 * 1024;
const MAX_ENVIRONMENT_VARIABLES = 256;
const MAX_ENVIRONMENT_KEY_BYTES = 128;
const MAX_ENVIRONMENT_VALUE_BYTES = 8_192;
const MAX_ARGUMENT_ITEMS = 128;
const MAX_ARGUMENT_BYTES = 4_096;
const IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/u;
const SYNC_FIELDS = new Set(['description', 'icon', 'tags', 'vendor', 'name_rule', 'status']);
const DEPLOYMENT_STATUSES = new Set([
  'running',
  'completed',
  'failed',
  'deployment requested',
  'termination requested',
  'destroyed',
]);

type UnknownRecord = Record<string, unknown>;

export interface ModelMetadata {
  id: number;
  modelName: string;
  description: string;
  icon: string;
  tags: string;
  vendorId: number;
  endpoints: string;
  status: 0 | 1;
  syncOfficial: 0 | 1;
  createdTime: number;
  updatedTime: number;
  nameRule: 0 | 1 | 2 | 3;
  boundChannels: Array<{ name: string; type: number }>;
  enableGroups: string[];
  matchedModels: string[];
  matchedCount: number;
  supportedEndpointTypes: string[];
}

export interface VendorMetadata {
  id: number;
  name: string;
  description: string;
  icon: string;
  status: 0 | 1;
  createdTime: number;
  updatedTime: number;
}

export interface ModelQuery {
  keyword: string;
  vendor: string;
  status: '' | 'enabled' | 'disabled';
  syncOfficial: '' | 'yes' | 'no';
  page: number;
  pageSize: number;
}

export interface MetadataPage {
  items: ModelMetadata[];
  total: number;
  page: number;
  pageSize: number;
  vendorCounts: Record<number, number>;
}

export interface VendorPage {
  items: VendorMetadata[];
  total: number;
  page: number;
  pageSize: number;
}

export interface ModelMutationInput {
  id?: number;
  modelName: string;
  description: string;
  icon: string;
  tags: string;
  vendorId: number;
  endpoints: string;
  status: 0 | 1;
  syncOfficial: 0 | 1;
  nameRule: 0 | 1 | 2 | 3;
}

export interface VendorMutationInput {
  id?: number;
  name: string;
  description: string;
  icon: string;
  status: 0 | 1;
}

export interface SyncConflict {
  modelName: string;
  fields: string[];
}

export interface SyncPreview {
  missing: string[];
  conflicts: SyncConflict[];
}

export interface SyncResult {
  createdModels: number;
  createdVendors: number;
  updatedModels: number;
  skippedModels: string[];
}

export type SyncLocale = '' | 'en' | 'ja' | 'zh-cn' | 'zh-tw';

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

export class ModelsContractError extends Error {
  constructor() {
    super('Invalid models API response');
    this.name = 'ModelsContractError';
  }
}

export class ModelsAccessError extends Error {
  constructor() {
    super('Administrator access is required');
    this.name = 'ModelsAccessError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new ModelsContractError();
  return value as UnknownRecord;
}

function allowedKeys(value: UnknownRecord, allowed: readonly string[]): void {
  const permitted = new Set(allowed);
  if (Object.keys(value).some((key) => !permitted.has(key))) throw new ModelsContractError();
}

function payloadBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    throw new ModelsContractError();
  }
}

function envelope(value: unknown, maximumBytes = MAX_RESPONSE_BYTES): UnknownRecord {
  if (payloadBytes(value) > maximumBytes) throw new ModelsContractError();
  const parsed = record(value);
  allowedKeys(parsed, ['success', 'message', 'data']);
  if (parsed.success !== true || !('data' in parsed)) throw new ModelsContractError();
  if (parsed.message !== undefined) safeText(parsed.message, 4_096, true);
  return parsed;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new ModelsContractError();
  }
  return value as number;
}

function finite(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new ModelsContractError();
  }
  return value;
}

function safeText(value: unknown, maximum: number, allowNewlines = false): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximum) throw new ModelsContractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    const permittedWhitespace = allowNewlines && (code === 0x09 || code === 0x0a || code === 0x0d);
    if (!permittedWhitespace && (code <= 0x1f || (code >= 0x7f && code <= 0x9f))) throw new ModelsContractError();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) throw new ModelsContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new ModelsContractError();
    }
  }
  return value;
}

function optionalText(value: unknown, maximum: number, allowNewlines = false): string {
  return value === undefined || value === null ? '' : safeText(value, maximum, allowNewlines);
}

function binary(value: unknown): 0 | 1 {
  const parsed = integer(value, 0, 1);
  return parsed as 0 | 1;
}

function boolean(value: unknown): boolean {
  if (typeof value !== 'boolean') throw new ModelsContractError();
  return value;
}

function boundedArray(value: unknown, maximum = MAX_RELATED_ITEMS): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) throw new ModelsContractError();
  return value;
}

function optionalArray(value: unknown, maximum = MAX_RELATED_ITEMS): unknown[] {
  return value === undefined || value === null ? [] : boundedArray(value, maximum);
}

function optionalStringArray(value: unknown, maximumLength = 512): string[] {
  if (value === undefined || value === null) return [];
  const parsed = boundedArray(value).map((item) => safeText(item, maximumLength));
  if (new Set(parsed).size !== parsed.length) throw new ModelsContractError();
  return parsed;
}

function validID(value: number): number {
  return integer(value, 1, MAX_ID);
}

function validIdentifier(value: unknown): string {
  const parsed = safeText(value, 128);
  if (!IDENTIFIER.test(parsed)) throw new ModelsContractError();
  return parsed;
}

function trimInput(value: string, maximum: number, required = false): string {
  if (typeof value !== 'string') throw new ModelsContractError();
  const parsed = safeText(value.trim(), maximum);
  if (required && parsed === '') throw new ModelsContractError();
  return parsed;
}

function trimInputRunes(value: string, maximumRunes: number, maximumBytes: number, required = false): string {
  const parsed = trimInput(value, maximumBytes, required);
  if ([...parsed].length > maximumRunes) throw new ModelsContractError();
  return parsed;
}

function parseModel(value: unknown): ModelMetadata {
  const item = record(value);
  allowedKeys(item, [
    'id', 'model_name', 'description', 'icon', 'tags', 'vendor_id', 'endpoints', 'status', 'sync_official',
    'created_time', 'updated_time', 'name_rule', 'bound_channels', 'enable_groups', 'quota_types',
    'matched_models', 'matched_count', 'supported_endpoint_types',
  ]);
  const boundChannels = item.bound_channels === undefined || item.bound_channels === null
    ? []
    : boundedArray(item.bound_channels).map((value) => {
      const channel = record(value);
      allowedKeys(channel, ['name', 'type']);
      return { name: safeText(channel.name, 512), type: integer(channel.type, 0, 10_000) };
    });
  if (item.quota_types !== undefined && item.quota_types !== null) {
    boundedArray(item.quota_types).forEach((quotaType) => integer(quotaType, 0, 100));
  }
  return {
    id: validID(item.id as number),
    modelName: safeText(item.model_name, 512),
    description: optionalText(item.description, MAX_METADATA_BYTES, true),
    icon: optionalText(item.icon, 512),
    tags: optionalText(item.tags, 1_024),
    vendorId: item.vendor_id === undefined || item.vendor_id === null ? 0 : integer(item.vendor_id, 0, MAX_ID),
    endpoints: optionalText(item.endpoints, MAX_METADATA_BYTES, true),
    status: binary(item.status),
    syncOfficial: binary(item.sync_official),
    createdTime: integer(item.created_time, 0, MAX_UNIX_SECONDS),
    updatedTime: integer(item.updated_time, 0, MAX_UNIX_SECONDS),
    nameRule: integer(item.name_rule, 0, 3) as 0 | 1 | 2 | 3,
    boundChannels,
    enableGroups: optionalStringArray(item.enable_groups, 512),
    matchedModels: optionalStringArray(item.matched_models, 512),
    matchedCount: item.matched_count === undefined || item.matched_count === null
      ? 0
      : integer(item.matched_count, 0, MAX_TOTAL),
    supportedEndpointTypes: optionalStringArray(item.supported_endpoint_types, 64),
  };
}

function parseVendor(value: unknown): VendorMetadata {
  const item = record(value);
  allowedKeys(item, ['id', 'name', 'description', 'icon', 'status', 'created_time', 'updated_time']);
  return {
    id: validID(item.id as number),
    name: safeText(item.name, 512),
    description: optionalText(item.description, MAX_METADATA_BYTES, true),
    icon: optionalText(item.icon, 512),
    status: binary(item.status),
    createdTime: integer(item.created_time, 0, MAX_UNIX_SECONDS),
    updatedTime: integer(item.updated_time, 0, MAX_UNIX_SECONDS),
  };
}

function parsePage(value: unknown, parser: (item: unknown) => ModelMetadata): MetadataPage {
  const data = record(envelope(value).data);
  allowedKeys(data, ['items', 'total', 'page', 'page_size', 'vendor_counts']);
  const items = boundedArray(data.items, MAX_ROWS).map(parser);
  if (new Set(items.map((item) => item.id)).size !== items.length) throw new ModelsContractError();
  const countsValue = data.vendor_counts === undefined || data.vendor_counts === null ? {} : record(data.vendor_counts);
  if (Object.keys(countsValue).length > MAX_RELATED_ITEMS) throw new ModelsContractError();
  const vendorCounts: Record<number, number> = {};
  for (const [key, count] of Object.entries(countsValue)) {
    if (!/^\d{1,10}$/u.test(key)) throw new ModelsContractError();
    vendorCounts[integer(Number(key), 0, MAX_ID)] = integer(count, 0, MAX_TOTAL);
  }
  const total = integer(data.total, 0, MAX_TOTAL);
  const pageSize = integer(data.page_size, 1, MAX_ROWS);
  if (items.length > pageSize || items.length > total) throw new ModelsContractError();
  return {
    items,
    total,
    page: integer(data.page, 1, MAX_PAGE),
    pageSize,
    vendorCounts,
  };
}

export function parseModelPageResponse(value: unknown): MetadataPage {
  return parsePage(value, parseModel);
}

export function parseVendorPageResponse(value: unknown): VendorPage {
  const data = record(envelope(value).data);
  allowedKeys(data, ['items', 'total', 'page', 'page_size']);
  const items = boundedArray(data.items, MAX_ROWS).map(parseVendor);
  if (new Set(items.map((item) => item.id)).size !== items.length) throw new ModelsContractError();
  const total = integer(data.total, 0, MAX_TOTAL);
  const pageSize = integer(data.page_size, 1, MAX_ROWS);
  if (items.length > pageSize || items.length > total) throw new ModelsContractError();
  return {
    items,
    total,
    page: integer(data.page, 1, MAX_PAGE),
    pageSize,
  };
}

export function parseModelResponse(value: unknown): ModelMetadata {
  return parseModel(envelope(value).data);
}

export function parseVendorResponse(value: unknown): VendorMetadata {
  return parseVendor(envelope(value).data);
}

export function parseMissingModelsResponse(value: unknown): string[] {
  const names = boundedArray(envelope(value).data, MAX_RELATED_ITEMS).map((name) => safeText(name, 512));
  if (new Set(names).size !== names.length) throw new ModelsContractError();
  return names;
}

export function parseSyncPreviewResponse(value: unknown): SyncPreview {
  const data = record(envelope(value).data);
  allowedKeys(data, ['missing', 'conflicts', 'source']);
  const conflicts = optionalArray(data.conflicts, 1_000).map((entry) => {
    const conflict = record(entry);
    allowedKeys(conflict, ['model_name', 'fields']);
    const fields = boundedArray(conflict.fields, 10).map((entry) => {
      const field = record(entry);
      allowedKeys(field, ['field', 'local', 'upstream']);
      const name = safeText(field.field, 32);
      if (!SYNC_FIELDS.has(name)) throw new ModelsContractError();
      validateSyncFieldValue(name, field.local);
      validateSyncFieldValue(name, field.upstream);
      return name;
    });
    if (fields.length === 0) throw new ModelsContractError();
    if (new Set(fields).size !== fields.length) throw new ModelsContractError();
    return {
      modelName: safeText(conflict.model_name, 512),
      fields,
    };
  });
  if (new Set(conflicts.map((conflict) => conflict.modelName)).size !== conflicts.length) throw new ModelsContractError();
  // Source URLs are intentionally not copied into the display model.
  if (data.source !== undefined && data.source !== null) {
    const source = record(data.source);
    allowedKeys(source, ['locale', 'models_url', 'vendors_url']);
    optionalText(source.locale, 16);
    safeText(source.models_url, 4_096);
    safeText(source.vendors_url, 4_096);
  }
  const missing = optionalArray(data.missing, MAX_RELATED_ITEMS).map((name) => safeText(name, 512));
  if (new Set(missing).size !== missing.length) throw new ModelsContractError();
  return { missing, conflicts };
}

function validateSyncFieldValue(field: string, value: unknown): void {
  if (field === 'name_rule') {
    integer(value, 0, 3);
  } else if (field === 'status') {
    binary(value);
  } else if (field === 'description') {
    safeText(value, MAX_METADATA_BYTES, true);
  } else if (field === 'tags') {
    safeText(value, 1_024);
  } else {
    safeText(value, 512);
  }
}

export function parseSyncResultResponse(value: unknown): SyncResult {
  const data = record(envelope(value).data);
  allowedKeys(data, [
    'created_models', 'created_vendors', 'updated_models', 'skipped_models', 'created_list', 'updated_list', 'source',
  ]);
  if (data.created_list !== undefined) optionalStringArray(data.created_list, 512);
  if (data.updated_list !== undefined) optionalStringArray(data.updated_list, 512);
  if (data.source !== undefined && data.source !== null) {
    const source = record(data.source);
    allowedKeys(source, ['locale', 'models_url', 'vendors_url']);
    optionalText(source.locale, 16);
    safeText(source.models_url, 4_096);
    safeText(source.vendors_url, 4_096);
  }
  return {
    createdModels: integer(data.created_models, 0, MAX_TOTAL),
    createdVendors: integer(data.created_vendors, 0, MAX_TOTAL),
    updatedModels: integer(data.updated_models, 0, MAX_TOTAL),
    skippedModels: optionalStringArray(data.skipped_models, 512),
  };
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

function deploymentStatus(value: unknown): string {
  const parsed = safeText(value, 64).trim().toLowerCase();
  if (!parsed) throw new ModelsContractError();
  return parsed;
}

function parseResourceConfig(value: unknown, expectedGPUs: number): void {
  const resource = record(value);
  allowedKeys(resource, ['cpu', 'memory', 'gpu']);
  optionalText(resource.cpu, 256);
  optionalText(resource.memory, 256);
  const gpu = safeText(resource.gpu, 32);
  if (!/^\d+$/u.test(gpu) || Number(gpu) !== expectedGPUs) throw new ModelsContractError();
}

function parseDeployment(value: unknown): DeploymentSummary {
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

function safePublicURL(value: unknown): string {
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

function parseContainer(value: unknown, expectedDeploymentID?: string): DeploymentContainer {
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

function parseNullMutation(value: unknown): void {
  if (envelope(value).data !== null) throw new ModelsContractError();
}

function parseDeploymentCreateMutation(value: unknown): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['deployment_id', 'status', 'message']);
  validIdentifier(data.deployment_id);
  safeText(data.status, 64);
  safeText(data.message, 1_024, true);
}

function parseDeploymentUpdateMutation(value: unknown, expectedID: string): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['status', 'deployment_id']);
  safeText(data.status, 64);
  if (validIdentifier(data.deployment_id) !== expectedID) throw new ModelsContractError();
}

function parseDeploymentRenameMutation(value: unknown, expectedID: string, expectedName: string): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['status', 'message', 'id', 'name']);
  safeText(data.status, 64);
  safeText(data.message, 1_024, true);
  if (validIdentifier(data.id) !== expectedID || safeText(data.name, 128) !== expectedName) {
    throw new ModelsContractError();
  }
}

function parseDeploymentDeleteMutation(value: unknown, expectedID: string): void {
  const data = record(envelope(value).data);
  allowedKeys(data, ['status', 'deployment_id', 'message']);
  safeText(data.status, 64);
  safeText(data.message, 1_024, true);
  if (validIdentifier(data.deployment_id) !== expectedID) throw new ModelsContractError();
}

const responseLimits = { maxContentLength: MAX_RESPONSE_BYTES, maxBodyLength: MAX_RESPONSE_BYTES };
const deploymentMutationLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_DEPLOYMENT_REQUEST_BYTES,
};

function boundedDeploymentBody<T extends UnknownRecord>(body: T): T {
  if (payloadBytes(body) > MAX_DEPLOYMENT_REQUEST_BYTES) throw new ModelsContractError();
  return body;
}

async function responseData(request: Promise<{ data: unknown }>): Promise<unknown> {
  try {
    return (await request).data;
  } catch {
    throw new ModelsContractError();
  }
}

export function assertModelsAdministrator(role: number): void {
  if (role !== 10 && role !== 100) throw new ModelsAccessError();
}

export async function loadModels(query: ModelQuery, signal?: AbortSignal): Promise<MetadataPage> {
  const params: Record<string, string | number> = {
    p: integer(query.page, 1, MAX_PAGE),
    page_size: integer(query.pageSize, 1, MAX_ROWS),
  };
  const keyword = trimInput(query.keyword, 256);
  const vendor = trimInput(query.vendor, 128);
  if (keyword) params.keyword = keyword;
  if (vendor) params.vendor = vendor;
  if (query.status) params.status = query.status;
  if (query.syncOfficial) params.sync_official = query.syncOfficial;
  const path = keyword ? '/models/search' : '/models/';
  return parseModelPageResponse(await responseData(api.get<unknown>(path, { params, signal, ...responseLimits })));
}

export async function loadVendors(
  signal?: AbortSignal,
  keyword = '',
  page = 1,
  pageSize = 100,
): Promise<VendorPage> {
  const safeKeyword = trimInput(keyword, 256);
  const path = safeKeyword ? '/vendors/search' : '/vendors/';
  const params: Record<string, string | number> = {
    p: integer(page, 1, MAX_PAGE),
    page_size: integer(pageSize, 1, MAX_ROWS),
  };
  if (safeKeyword) params.keyword = safeKeyword;
  return parseVendorPageResponse(await responseData(api.get<unknown>(path, { params, signal, ...responseLimits })));
}

export async function getModel(id: number, signal?: AbortSignal): Promise<ModelMetadata> {
  return parseModelResponse(await responseData(api.get<unknown>(`/models/${validID(id)}`, { signal, ...responseLimits })));
}

export async function getVendor(id: number, signal?: AbortSignal): Promise<VendorMetadata> {
  return parseVendorResponse(await responseData(api.get<unknown>(`/vendors/${validID(id)}`, { signal, ...responseLimits })));
}

function modelBody(input: ModelMutationInput, includeID: boolean): UnknownRecord {
  const modelName = trimInputRunes(input.modelName, 128, 512, true);
  const endpoints = trimInput(input.endpoints, MAX_METADATA_BYTES);
  if (endpoints) {
    try {
      JSON.parse(endpoints);
    } catch {
      throw new ModelsContractError();
    }
  }
  const body: UnknownRecord = {
    model_name: modelName,
    description: trimInput(input.description, MAX_METADATA_BYTES),
    icon: trimInputRunes(input.icon, 128, 512),
    tags: trimInputRunes(input.tags, 255, 1_020),
    vendor_id: integer(input.vendorId, 0, MAX_ID),
    endpoints,
    status: binary(input.status),
    sync_official: binary(input.syncOfficial),
    name_rule: integer(input.nameRule, 0, 3),
  };
  if (includeID) body.id = validID(input.id as number);
  return body;
}

export async function createModel(input: ModelMutationInput): Promise<ModelMetadata> {
  return parseModelResponse(await responseData(api.post<unknown>('/models/', modelBody(input, false), responseLimits)));
}

export async function updateModel(input: ModelMutationInput & { id: number }): Promise<ModelMetadata> {
  return parseModelResponse(await responseData(api.put<unknown>('/models/', modelBody(input, true), responseLimits)));
}

export async function setModelStatus(id: number, status: 0 | 1): Promise<void> {
  const value = await responseData(api.put<unknown>('/models/?status_only=true', {
    id: validID(id), status: binary(status),
  }, responseLimits));
  // The backend returns the updated row; parsing it prevents accidental secret expansion.
  parseModelResponse(value);
}

export async function deleteModel(id: number): Promise<void> {
  parseNullMutation(await responseData(api.delete<unknown>(`/models/${validID(id)}`, responseLimits)));
}

function vendorBody(input: VendorMutationInput, includeID: boolean): UnknownRecord {
  const body: UnknownRecord = {
    name: trimInputRunes(input.name, 128, 512, true),
    description: trimInput(input.description, MAX_METADATA_BYTES),
    icon: trimInputRunes(input.icon, 128, 512),
    status: binary(input.status),
  };
  if (includeID) body.id = validID(input.id as number);
  return body;
}

export async function createVendor(input: VendorMutationInput): Promise<VendorMetadata> {
  return parseVendorResponse(await responseData(api.post<unknown>('/vendors/', vendorBody(input, false), responseLimits)));
}

export async function updateVendor(input: VendorMutationInput & { id: number }): Promise<VendorMetadata> {
  return parseVendorResponse(await responseData(api.put<unknown>('/vendors/', vendorBody(input, true), responseLimits)));
}

export async function deleteVendor(id: number): Promise<void> {
  parseNullMutation(await responseData(api.delete<unknown>(`/vendors/${validID(id)}`, responseLimits)));
}

export async function loadMissingModels(signal?: AbortSignal): Promise<string[]> {
  return parseMissingModelsResponse(await responseData(api.get<unknown>('/models/missing', { signal, ...responseLimits })));
}

function localeParam(locale: SyncLocale): Record<string, string> | undefined {
  if (!['', 'en', 'ja', 'zh-cn', 'zh-tw'].includes(locale)) throw new ModelsContractError();
  return locale ? { locale } : undefined;
}

export async function previewUpstream(locale: SyncLocale, signal?: AbortSignal): Promise<SyncPreview> {
  return parseSyncPreviewResponse(await responseData(api.get<unknown>('/models/sync_upstream/preview', {
    params: localeParam(locale), signal, ...responseLimits,
  })));
}

export async function syncUpstream(locale: SyncLocale, preview: SyncPreview): Promise<SyncResult> {
  const missing = boundedArray(preview.missing, MAX_RELATED_ITEMS).map((name) => trimInput(name as string, 512, true));
  if (new Set(missing).size !== missing.length || preview.conflicts.length > 1_000) throw new ModelsContractError();
  const seenModels = new Set<string>();
  const overwrite = preview.conflicts.map((conflict) => ({
    model_name: (() => {
      const name = trimInput(conflict.modelName, 512, true);
      if (seenModels.has(name)) throw new ModelsContractError();
      seenModels.add(name);
      return name;
    })(),
    fields: (() => {
      if (conflict.fields.length < 1 || conflict.fields.length > SYNC_FIELDS.size
        || new Set(conflict.fields).size !== conflict.fields.length) throw new ModelsContractError();
      return conflict.fields.map((field) => {
      if (!SYNC_FIELDS.has(field)) throw new ModelsContractError();
      return field;
      });
    })(),
  }));
  return parseSyncResultResponse(await responseData(api.post<unknown>('/models/sync_upstream', {
    locale: locale || '', overwrite,
  }, responseLimits)));
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

function deploymentLocations(values: number[]): number[] {
  const locations = values.map((id) => integer(id, 1, MAX_ID));
  if (locations.length < 1 || locations.length > 100 || new Set(locations).size !== locations.length) {
    throw new ModelsContractError();
  }
  return locations;
}

function environmentInput(value: Record<string, string> | undefined): Record<string, string> | undefined {
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

function argumentInput(value: string[] | undefined): string[] | undefined {
  if (value === undefined) return undefined;
  if (value.length === 0 || value.length > MAX_ARGUMENT_ITEMS) throw new ModelsContractError();
  return value.map((entry) => {
    const parsed = safeText(entry, MAX_ARGUMENT_BYTES);
    if (!parsed) throw new ModelsContractError();
    return parsed;
  });
}

function deploymentContainerBody(input: Pick<
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
