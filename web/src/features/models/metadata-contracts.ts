import { MAX_ID, MAX_METADATA_BYTES, MAX_PAGE, MAX_RELATED_ITEMS, MAX_ROWS, MAX_TOTAL, MAX_UNIX_SECONDS, ModelsContractError, SYNC_FIELDS, allowedKeys, binary, boundedArray, envelope, integer, optionalArray, optionalStringArray, optionalText, record, safeText, validID } from './model-response';

export const MODEL_PAGE_SIZE = 20;

export const MODEL_SECTIONS = ['metadata', 'deployments'] as const;

export type ModelsSection = (typeof MODEL_SECTIONS)[number];

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

export function parseModel(value: unknown): ModelMetadata {
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

export function parseVendor(value: unknown): VendorMetadata {
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

export function parsePage(value: unknown, parser: (item: unknown) => ModelMetadata): MetadataPage {
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

export function validateSyncFieldValue(field: string, value: unknown): void {
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
