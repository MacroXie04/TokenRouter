export const MAX_PRICING_CATALOG_ITEMS = 10_000;
export const MAX_PRICING_MODEL_NAME_LENGTH = 512;
export const MAX_PRICING_QUERY_LENGTH = 4_096;
export const PRICING_PAGE_SIZE = 24;
export const MAX_PRICING_PAGE = 1_000;

const MAX_CATALOG_BYTES = 8 * 1024 * 1024;
const MAX_GROUPS = 64;
const MAX_VENDORS = 10_000;
const MAX_ENDPOINTS = 128;
const MAX_SHORT_TEXT = 128;
const MAX_DESCRIPTION = 1024 * 1024;
const MAX_TAGS = 2_048;
const MAX_PATH = 1_024;
const MAX_BILLING_EXPRESSION = 64 * 1024;

type UnknownRecord = Record<string, unknown>;

export type PricingViewMode = 'card' | 'table';
export type PricingModality = 'text' | 'image' | 'audio' | 'video' | 'embedding' | 'rerank';
export type PricingModalityFilter = '' | PricingModality;
export type PricingSort = 'name' | 'price-low' | 'price-high';
export type PricingQuotaType = '' | 'token' | 'request';

export interface PricingCatalogItem {
  model_name: string;
  description?: string;
  tags?: string;
  vendor_id: number;
  quota_type: 0 | 1;
  model_ratio: number;
  model_price: number;
  prompt_price: number;
  completion_price: number;
  owner_by: string;
  completion_ratio: number;
  enable_groups: string[];
  supported_endpoint_types: string[];
  billing_mode?: 'tiered_expr';
  billing_expr?: string;
}

export interface PricingVendor {
  id: number;
  name: string;
  description?: string;
}

export interface PricingEndpoint {
  path: string;
  method: string;
}

export interface PricingCatalog {
  items: PricingCatalogItem[];
  vendors: PricingVendor[];
  groupRatio: Record<string, number>;
  usableGroup: Record<string, string>;
  supportedEndpoint: Record<string, PricingEndpoint>;
  autoGroups: string[];
  version: string;
}

export interface PricingQueryState {
  search: string;
  sort: PricingSort;
  vendor: string;
  group: string;
  quotaType: PricingQuotaType;
  modality: PricingModalityFilter;
  endpointType: string;
  tag: string;
  view: PricingViewMode;
  page: number;
}


export class PricingContractError extends Error {
  constructor() {
    super('Invalid pricing API response');
    this.name = 'PricingContractError';
  }
}

function fail(): never {
  throw new PricingContractError();
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown, maximum = MAX_CATALOG_BYTES): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    fail();
  }
  if (encoded === undefined || new TextEncoder().encode(encoded).byteLength > maximum) fail();
}

function array(value: unknown, maximum: number): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) fail();
  return value;
}

function text(value: unknown, maximum: number, allowEmpty = false, allowCommonWhitespace = false): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximum) fail();
  const normalized = value.trim();
  if (!allowEmpty && !normalized) fail();
  for (let index = 0; index < normalized.length; index += 1) {
    const code = normalized.charCodeAt(index);
    const commonWhitespace = code === 0x09 || code === 0x0a || code === 0x0d;
    if (((!allowCommonWhitespace || !commonWhitespace) && (code <= 0x1f || (code >= 0x7f && code <= 0x9f)))
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) fail();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = normalized.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) fail();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) fail();
  }
  return normalized;
}

function optionalText(value: unknown, maximum: number, allowCommonWhitespace = false): string | undefined {
  if (value === undefined || value === null || value === '') return undefined;
  const normalized = text(value, maximum, true, allowCommonWhitespace);
  return normalized || undefined;
}

function decimal(value: unknown, minimum = 0, maximum = Number.MAX_VALUE): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) fail();
  return value;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) fail();
  return value as number;
}

function unique(values: string[]): void {
  if (new Set(values).size !== values.length) fail();
}

function keyedTextMap(value: unknown, maximum: number): Record<string, string> {
  const source = record(value);
  const entries = Object.entries(source);
  if (entries.length > maximum) fail();
  const result: Record<string, string> = {};
  for (const [rawKey, rawValue] of entries) {
    const key = text(rawKey, MAX_SHORT_TEXT);
    if (key !== rawKey) fail();
    result[key] = text(rawValue, MAX_DESCRIPTION, true);
  }
  return result;
}

function keyedNumberMap(value: unknown, maximum: number): Record<string, number> {
  const source = record(value);
  const entries = Object.entries(source);
  if (entries.length > maximum) fail();
  const result: Record<string, number> = {};
  for (const [rawKey, rawValue] of entries) {
    const key = text(rawKey, MAX_SHORT_TEXT);
    if (key !== rawKey) fail();
    result[key] = decimal(rawValue);
  }
  return result;
}

function parseStringArray(value: unknown, maximum: number, maximumLength = MAX_SHORT_TEXT): string[] {
  const parsed = array(value, maximum).map((item) => {
    const normalized = text(item, maximumLength);
    if (normalized !== item) fail();
    return normalized;
  });
  unique(parsed);
  return parsed;
}

function parseCatalogItem(value: unknown): PricingCatalogItem {
  const item = record(value);
  const quotaType = integer(item.quota_type, 0, 1);
  const modelName = text(item.model_name, MAX_PRICING_MODEL_NAME_LENGTH);
  if (modelName !== item.model_name) fail();
  const description = optionalText(item.description, MAX_DESCRIPTION, true);
  const tags = optionalText(item.tags, MAX_TAGS);
  const rawBillingMode = item.billing_mode;
  const billingMode = rawBillingMode === undefined || rawBillingMode === null || rawBillingMode === ''
    ? undefined
    : text(rawBillingMode, 64);
  if (billingMode !== undefined && (billingMode !== rawBillingMode || billingMode !== 'tiered_expr')) fail();
  const billingExpr = optionalText(item.billing_expr, MAX_BILLING_EXPRESSION, true);
  if ((billingMode === 'tiered_expr') !== Boolean(billingExpr)) fail();
  return {
    model_name: modelName,
    ...(description ? { description } : {}),
    ...(tags ? { tags } : {}),
    vendor_id: integer(item.vendor_id ?? 0, 0, Number.MAX_SAFE_INTEGER),
    quota_type: quotaType as 0 | 1,
    model_ratio: decimal(item.model_ratio),
    model_price: decimal(item.model_price),
    prompt_price: decimal(item.prompt_price),
    completion_price: decimal(item.completion_price),
    owner_by: text(item.owner_by, MAX_SHORT_TEXT),
    completion_ratio: decimal(item.completion_ratio),
    enable_groups: parseStringArray(item.enable_groups, MAX_GROUPS),
    supported_endpoint_types: parseStringArray(item.supported_endpoint_types, MAX_ENDPOINTS),
    ...(billingMode === 'tiered_expr' ? { billing_mode: billingMode, billing_expr: billingExpr! } : {}),
  };
}

// Kept as a small feature-boundary helper for the home-page catalog preview.
export function parsePricingCatalogItems(value: unknown): PricingCatalogItem[] {
  const items = array(value, MAX_PRICING_CATALOG_ITEMS).map(parseCatalogItem);
  unique(items.map((item) => item.model_name));
  return items;
}

function parseVendors(value: unknown): PricingVendor[] {
  const vendors = array(value, MAX_VENDORS).map((raw) => {
    const vendor = record(raw);
    const description = optionalText(vendor.description, MAX_DESCRIPTION, true);
    return {
      id: integer(vendor.id, 1, Number.MAX_SAFE_INTEGER),
      name: (() => {
        const name = text(vendor.name, MAX_SHORT_TEXT);
        if (name !== vendor.name) fail();
        return name;
      })(),
      ...(description ? { description } : {}),
    };
  });
  unique(vendors.map((vendor) => String(vendor.id)));
  unique(vendors.map((vendor) => vendor.name));
  return vendors;
}

function parseEndpoints(value: unknown): Record<string, PricingEndpoint> {
  const source = record(value);
  const entries = Object.entries(source);
  if (entries.length > MAX_ENDPOINTS) fail();
  const result: Record<string, PricingEndpoint> = {};
  for (const [rawType, rawEndpoint] of entries) {
    const type = text(rawType, MAX_SHORT_TEXT);
    const endpoint = record(rawEndpoint);
    const path = text(endpoint.path, MAX_PATH);
    const method = text(endpoint.method, 16);
    if (type !== rawType || !path.startsWith('/') || path.startsWith('//') || !/^[A-Z]+$/.test(method)) fail();
    result[type] = { path, method };
  }
  return result;
}

export function parsePricingCatalogResponse(value: unknown): PricingCatalog {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true) fail();

  const usableGroup = keyedTextMap(envelope.usable_group, MAX_GROUPS);
  const groupRatio = keyedNumberMap(envelope.group_ratio, MAX_GROUPS);
  const supportedEndpoint = parseEndpoints(envelope.supported_endpoint);
  const vendors = parseVendors(envelope.vendors);
  const items = parsePricingCatalogItems(envelope.data);
  const autoGroups = parseStringArray(envelope.auto_groups, MAX_GROUPS);
  const version = text(envelope.pricing_version, 64);
  if (!/^[a-f0-9]{64}$/i.test(version)) fail();

  const usableGroups = new Set(Object.keys(usableGroup));
  const endpointTypes = new Set(Object.keys(supportedEndpoint));
  const vendorIDs = new Set(vendors.map((vendor) => vendor.id));
  if (Object.keys(groupRatio).some((group) => !usableGroups.has(group))) fail();
  if (autoGroups.some((group) => !usableGroups.has(group))) fail();
  for (const item of items) {
    if (item.vendor_id > 0 && !vendorIDs.has(item.vendor_id)) fail();
    if (item.enable_groups.some((group) => !usableGroups.has(group))) fail();
    if (item.supported_endpoint_types.some((type) => !endpointTypes.has(type))) fail();
    for (const group of item.enable_groups) {
      const ratio = groupRatio[group] ?? 1;
      if ([item.model_price, item.prompt_price, item.completion_price]
        .some((price) => !Number.isFinite(price * ratio))) fail();
    }
  }

  return { items, vendors, groupRatio, usableGroup, supportedEndpoint, autoGroups, version };
}

const QUERY_DEFAULTS: PricingQueryState = {
  search: '', sort: 'name', vendor: '', group: '', quotaType: '', modality: '', endpointType: '', tag: '', view: 'card', page: 1,
};
const SORTS = new Set<PricingSort>(['name', 'price-low', 'price-high']);
const QUOTA_TYPES = new Set<PricingQuotaType>(['', 'token', 'request']);
const MODALITIES = new Set<PricingModalityFilter>(['', 'text', 'image', 'audio', 'video', 'embedding', 'rerank']);

function safeQueryText(value: string | null, maximum: number): string {
  if (!value || new TextEncoder().encode(value).byteLength > maximum || value !== value.trim()) return '';
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) return '';
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return '';
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) return '';
  }
  return value;
}

export function parsePricingQuery(search: string): PricingQueryState {
  if (typeof search !== 'string' || new TextEncoder().encode(search).byteLength > MAX_PRICING_QUERY_LENGTH) {
    return { ...QUERY_DEFAULTS };
  }
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  } catch {
    return { ...QUERY_DEFAULTS };
  }
  const single = (key: string, maximum: number) => (
    params.getAll(key).length === 1 ? safeQueryText(params.get(key), maximum) : ''
  );
  const rawSort = single('sort', 32);
  const rawQuotaType = single('quotaType', 16);
  const rawModality = single('modality', 16);
  const rawView = single('view', 16);
  const rawPage = single('page', 8);
  const parsedPage = /^\d+$/.test(rawPage) ? Number(rawPage) : 1;
  return {
    search: single('search', 200),
    sort: SORTS.has(rawSort as PricingSort) ? rawSort as PricingSort : 'name',
    vendor: single('vendor', MAX_SHORT_TEXT),
    group: single('group', MAX_SHORT_TEXT),
    quotaType: QUOTA_TYPES.has(rawQuotaType as PricingQuotaType) ? rawQuotaType as PricingQuotaType : '',
    modality: MODALITIES.has(rawModality as PricingModalityFilter) ? rawModality as PricingModalityFilter : '',
    endpointType: single('endpointType', MAX_SHORT_TEXT),
    tag: single('tag', MAX_SHORT_TEXT),
    view: rawView === 'table' ? 'table' : 'card',
    page: Number.isSafeInteger(parsedPage) && parsedPage >= 1 && parsedPage <= MAX_PRICING_PAGE ? parsedPage : 1,
  };
}

export function pricingQueryString(query: PricingQueryState): string {
  const normalized: PricingQueryState = {
    search: safeQueryText(query.search, 200),
    sort: SORTS.has(query.sort) ? query.sort : 'name',
    vendor: safeQueryText(query.vendor, MAX_SHORT_TEXT),
    group: safeQueryText(query.group, MAX_SHORT_TEXT),
    quotaType: QUOTA_TYPES.has(query.quotaType) ? query.quotaType : '',
    modality: MODALITIES.has(query.modality) ? query.modality : '',
    endpointType: safeQueryText(query.endpointType, MAX_SHORT_TEXT),
    tag: safeQueryText(query.tag, MAX_SHORT_TEXT),
    view: query.view === 'table' ? 'table' : 'card',
    page: Number.isSafeInteger(query.page) && query.page >= 1 && query.page <= MAX_PRICING_PAGE ? query.page : 1,
  };
  const params = new URLSearchParams();
  if (normalized.search) params.set('search', normalized.search);
  if (normalized.sort !== 'name') params.set('sort', normalized.sort);
  if (normalized.vendor) params.set('vendor', normalized.vendor);
  if (normalized.group) params.set('group', normalized.group);
  if (normalized.quotaType) params.set('quotaType', normalized.quotaType);
  if (normalized.modality) params.set('modality', normalized.modality);
  if (normalized.endpointType) params.set('endpointType', normalized.endpointType);
  if (normalized.tag) params.set('tag', normalized.tag);
  if (normalized.view === 'table') params.set('view', normalized.view);
  if (normalized.page > 1) params.set('page', String(normalized.page));
  const encoded = params.toString();
  return encoded ? `?${encoded}` : '';
}

export function ownerName(item: PricingCatalogItem, vendors: PricingVendor[]): string {
  const vendor = item.vendor_id > 0 ? vendors.find((entry) => entry.id === item.vendor_id) : undefined;
  return vendor?.name || item.owner_by;
}

export function endpointModality(type: string): PricingModality {
  const normalized = type.toLocaleLowerCase();
  if (normalized.includes('image')) return 'image';
  if (normalized.includes('audio') || normalized.includes('speech')) return 'audio';
  if (normalized.includes('video')) return 'video';
  if (normalized.includes('embedding')) return 'embedding';
  if (normalized.includes('rerank')) return 'rerank';
  return 'text';
}

export function itemModalities(item: PricingCatalogItem): PricingModality[] {
  return [...new Set(item.supported_endpoint_types.map(endpointModality))];
}

export function pricingItemTags(item: PricingCatalogItem): string[] {
  if (!item.tags) return [];
  return [...new Set(item.tags.split(/[,;|\s]+/u).map((tag) => tag.trim()).filter(Boolean))].slice(0, 128);
}

function itemSortPrice(item: PricingCatalogItem): number | null {
  if (item.billing_mode === 'tiered_expr') return null;
  return item.quota_type === 0 ? item.prompt_price : item.model_price;
}

export function filterPricingItems(
  items: PricingCatalogItem[],
  vendors: PricingVendor[],
  query: PricingQueryState,
): PricingCatalogItem[] {
  const needle = query.search.toLocaleLowerCase();
  const filtered = items.filter((item) => {
    const owner = ownerName(item, vendors);
    const searchable = `${item.model_name}\n${item.description ?? ''}\n${item.tags ?? ''}\n${owner}`.toLocaleLowerCase();
    return (!needle || searchable.includes(needle))
      && (!query.group || item.enable_groups.includes(query.group))
      && (!query.vendor || owner === query.vendor)
      && (!query.quotaType || item.quota_type === (query.quotaType === 'token' ? 0 : 1))
      && (!query.modality || itemModalities(item).includes(query.modality))
      && (!query.endpointType || item.supported_endpoint_types.includes(query.endpointType))
      && (!query.tag || pricingItemTags(item).includes(query.tag));
  });
  return filtered.sort((left, right) => {
    if (query.sort === 'price-low' || query.sort === 'price-high') {
      const leftPrice = itemSortPrice(left);
      const rightPrice = itemSortPrice(right);
      if (leftPrice === null && rightPrice !== null) return 1;
      if (rightPrice === null && leftPrice !== null) return -1;
      const delta = (leftPrice ?? 0) - (rightPrice ?? 0);
      if (delta !== 0) return query.sort === 'price-low' ? delta : -delta;
    }
    return left.model_name.localeCompare(right.model_name);
  });
}
