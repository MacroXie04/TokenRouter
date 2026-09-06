import { api } from '../../shared/api/client';

export const TOKEN_PAGE_SIZE = 20;
export const TOKEN_ENABLED = 1;
export const TOKEN_DISABLED = 2;
export const TOKEN_EXPIRED = 3;
export const TOKEN_EXHAUSTED = 4;
export const TOKEN_MAX_QUOTA = 2_147_483_647;

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_PAGE_ITEMS = 100;
const MAX_TOTAL = 10_000_000;
const MAX_MODELS = 2_000;
const MAX_GROUPS = 100;

type UnknownRecord = Record<string, unknown>;

export interface RelayToken {
  id: number;
  name: string;
  maskedKey: string;
  status: number;
  remainQuota: number;
  usedQuota: number;
  unlimitedQuota: boolean;
  expiredTime: number;
  createdTime: number;
  accessedTime: number;
  group: string;
  autoGroups: string[];
  crossGroupRetry: boolean;
  modelLimitsEnabled: boolean;
  modelLimits: string[];
  allowIps: string;
}

export interface TokenPage {
  items: RelayToken[];
  total: number;
  page: number;
  pageSize: number;
}

export interface TokenGroup {
  name: string;
  description: string;
  ratio: string;
}

export interface TokenAutoGroupConfig {
  groups: string[];
  maxCount: number;
}

export interface TokenWriteInput {
  name: string;
  expiredTime: number;
  remainQuota: number;
  unlimitedQuota: boolean;
  modelLimitsEnabled: boolean;
  modelLimits: string[];
  allowIps: string;
  group?: string;
  autoGroups?: string[];
  crossGroupRetry: boolean;
}

export class TokenContractError extends Error {
  constructor() {
    super('Invalid relay-token API response');
    this.name = 'TokenContractError';
  }
}

function asRecord(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new TokenContractError();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new TokenContractError();
  }
  if (encoded.length > MAX_RESPONSE_BYTES) throw new TokenContractError();
}

function asInteger(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new TokenContractError();
  }
  return value as number;
}

function asBoolean(value: unknown): boolean {
  if (typeof value !== 'boolean') throw new TokenContractError();
  return value;
}

function asString(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length > maximum) throw new TokenContractError();
  return value;
}

function optionalString(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : asString(value, maximum);
}

function successfulEnvelope(value: unknown): UnknownRecord {
  boundedPayload(value);
  const envelope = asRecord(value);
  if (envelope.success !== true) throw new TokenContractError();
  return envelope;
}

function maskedKey(value: unknown): string {
  const key = asString(value, 128);
  if (key === '') return '';
  const shortMask = /^\*{1,4}$/;
  const mediumMask = /^[A-Za-z0-9_-]{2}\*{4}[A-Za-z0-9_-]{2}$/;
  const standardMask = /^[A-Za-z0-9_-]{4}\*{10}[A-Za-z0-9_-]{4}$/;
  if (!shortMask.test(key) && !mediumMask.test(key) && !standardMask.test(key)) {
    throw new TokenContractError();
  }
  return key;
}

function stringList(value: unknown, maximumItems: number, maximumLength: number): string[] {
  if (!Array.isArray(value) || value.length > maximumItems) throw new TokenContractError();
  const unique = new Set<string>();
  for (const item of value) {
    const normalized = asString(item, maximumLength).trim();
    if (!normalized || unique.has(normalized)) throw new TokenContractError();
    unique.add(normalized);
  }
  return [...unique];
}

function parseToken(value: unknown): RelayToken {
  const token = asRecord(value);
  const unlimitedQuota = asBoolean(token.unlimited_quota);
  const remainQuota = asInteger(token.remain_quota, -1, TOKEN_MAX_QUOTA);
  if ((!unlimitedQuota && remainQuota < 0) || (unlimitedQuota && remainQuota < -1)) throw new TokenContractError();
  const rawAutoGroups = token.auto_groups === undefined || token.auto_groups === null ? [] : token.auto_groups;
  const modelLimits = optionalString(token.model_limits, 16_384)
    .split(',')
    .map((model) => model.trim())
    .filter(Boolean);
  if (modelLimits.length > 200 || new Set(modelLimits).size !== modelLimits.length || modelLimits.some((model) => model.length > 255)) {
    throw new TokenContractError();
  }
  return {
    id: asInteger(token.id, 1, 2_147_483_647),
    name: asString(token.name, 50),
    maskedKey: maskedKey(token.key),
    status: asInteger(token.status, TOKEN_ENABLED, TOKEN_EXHAUSTED),
    remainQuota,
    usedQuota: asInteger(token.used_quota, 0, TOKEN_MAX_QUOTA),
    unlimitedQuota,
    expiredTime: asInteger(token.expired_time, -1, 253_402_300_799),
    createdTime: asInteger(token.created_time, 0, 253_402_300_799),
    accessedTime: asInteger(token.accessed_time, 0, 253_402_300_799),
    group: optionalString(token.group, 64),
    autoGroups: stringList(rawAutoGroups, MAX_GROUPS, 64),
    crossGroupRetry: asBoolean(token.cross_group_retry),
    modelLimitsEnabled: asBoolean(token.model_limits_enabled),
    modelLimits,
    allowIps: optionalString(token.allow_ips, 4_096),
  };
}

export function parseTokenPageResponse(value: unknown): TokenPage {
  const data = asRecord(successfulEnvelope(value).data);
  if (!Array.isArray(data.items) || data.items.length > MAX_PAGE_ITEMS) throw new TokenContractError();
  const pageSize = asInteger(data.page_size, 1, MAX_PAGE_ITEMS);
  if (data.items.length > pageSize) throw new TokenContractError();
  return {
    items: data.items.map(parseToken),
    total: asInteger(data.total, 0, MAX_TOTAL),
    page: asInteger(data.page, 1, 1_000_000),
    pageSize,
  };
}

export function parseGroupsResponse(value: unknown): TokenGroup[] {
  const data = asRecord(successfulEnvelope(value).data);
  if (Object.keys(data).length > MAX_GROUPS) throw new TokenContractError();
  return Object.entries(data).map(([name, rawGroup]) => {
    if (!name || name.length > 64 || name.trim() !== name) throw new TokenContractError();
    const group = asRecord(rawGroup);
    const rawRatio = group.ratio;
    if (typeof rawRatio !== 'number' && typeof rawRatio !== 'string') throw new TokenContractError();
    const ratio = String(rawRatio);
    if (ratio.length > 32) throw new TokenContractError();
    return { name, description: asString(group.desc, 128), ratio };
  }).sort((left, right) => left.name.localeCompare(right.name));
}

export function parseAutoGroupsResponse(value: unknown): TokenAutoGroupConfig {
  const data = asRecord(successfulEnvelope(value).data);
  const maxCount = asInteger(data.max_count, 1, MAX_GROUPS);
  return { groups: stringList(data.groups, MAX_GROUPS, 64), maxCount };
}

export function parseModelsResponse(value: unknown): string[] {
  const models = stringList(successfulEnvelope(value).data, MAX_MODELS, 255);
  return models.sort((left, right) => left.localeCompare(right));
}

function normalizeWriteInput(input: TokenWriteInput): UnknownRecord {
  const name = asString(input.name.trim(), 50);
  if (!name) throw new TokenContractError();
  const group = input.group === undefined ? undefined : asString(input.group, 64);
  if (group !== undefined && (!group || group.trim() !== group)) throw new TokenContractError();
  const rawAllowIps = asString(input.allowIps, 4_096);
  const allowIps = rawAllowIps.split(/[\n,]/).map((entry) => entry.trim()).filter(Boolean);
  if (allowIps.length > 64 || allowIps.some((entry) => entry.length > 64 || !/^[0-9a-fA-F.:/]+$/.test(entry))) {
    throw new TokenContractError();
  }
  const modelLimits = stringList(input.modelLimits, 200, 255);
  const body: UnknownRecord = {
    name,
    expired_time: asInteger(input.expiredTime, -1, 253_402_300_799),
    remain_quota: asInteger(input.remainQuota, 0, TOKEN_MAX_QUOTA),
    unlimited_quota: asBoolean(input.unlimitedQuota),
    model_limits_enabled: asBoolean(input.modelLimitsEnabled),
    model_limits: input.modelLimitsEnabled ? modelLimits.join(',') : '',
    allow_ips: allowIps.join(','),
    cross_group_retry: group === 'auto' && asBoolean(input.crossGroupRetry),
  };
  if (group !== undefined) body.group = group;
  if (group === 'auto') {
    const autoGroups = stringList(input.autoGroups, MAX_GROUPS, 64);
    if (autoGroups.length === 0) throw new TokenContractError();
    body.auto_groups = autoGroups;
  }
  return body;
}

const responseLimits = { maxContentLength: MAX_RESPONSE_BYTES, maxBodyLength: MAX_RESPONSE_BYTES };

export async function listTokens(page: number, signal?: AbortSignal): Promise<TokenPage> {
  const response = await api.get<unknown>('/token/', {
    params: { p: asInteger(page, 1, 1_000_000), page_size: TOKEN_PAGE_SIZE },
    signal,
    ...responseLimits,
  });
  return parseTokenPageResponse(response.data);
}

export async function searchTokens(keyword: string, page: number, signal?: AbortSignal): Promise<TokenPage> {
  const normalized = asString(keyword.trim(), 50);
  if (!normalized || normalized.includes('%')) throw new TokenContractError();
  const response = await api.get<unknown>('/token/search', {
    params: { keyword: normalized, p: asInteger(page, 1, 1_000_000), page_size: TOKEN_PAGE_SIZE },
    signal,
    ...responseLimits,
  });
  return parseTokenPageResponse(response.data);
}

export async function loadTokenGroups(signal?: AbortSignal): Promise<TokenGroup[]> {
  const response = await api.get<unknown>('/user/self/groups', { signal, ...responseLimits });
  return parseGroupsResponse(response.data);
}

export async function loadTokenAutoGroups(signal?: AbortSignal): Promise<TokenAutoGroupConfig> {
  const response = await api.get<unknown>('/token/auto-groups', { signal, ...responseLimits });
  return parseAutoGroupsResponse(response.data);
}

export async function loadTokenModels(group?: string, signal?: AbortSignal): Promise<string[]> {
  const params = group ? { group: asString(group, 64) } : undefined;
  const response = await api.get<unknown>('/user/models', { params, signal, ...responseLimits });
  return parseModelsResponse(response.data);
}

export async function createToken(input: TokenWriteInput): Promise<void> {
  const response = await api.post<unknown>('/token/', normalizeWriteInput(input), responseLimits);
  successfulEnvelope(response.data);
}

export async function updateToken(id: number, input: TokenWriteInput): Promise<void> {
  const body = { id: asInteger(id, 1, 2_147_483_647), ...normalizeWriteInput(input) };
  const response = await api.put<unknown>('/token/', body, responseLimits);
  parseToken(successfulEnvelope(response.data).data);
}

export async function updateTokenStatus(id: number, status: typeof TOKEN_ENABLED | typeof TOKEN_DISABLED): Promise<void> {
  const response = await api.put<unknown>('/token/', {
    id: asInteger(id, 1, 2_147_483_647),
    status,
  }, { params: { status_only: 1 }, ...responseLimits });
  parseToken(successfulEnvelope(response.data).data);
}

export async function deleteToken(id: number): Promise<void> {
  const response = await api.delete<unknown>(`/token/${asInteger(id, 1, 2_147_483_647)}`, responseLimits);
  successfulEnvelope(response.data);
}

export async function deleteTokens(ids: number[]): Promise<number> {
  if (!Array.isArray(ids) || ids.length < 1 || ids.length > MAX_PAGE_ITEMS || new Set(ids).size !== ids.length) {
    throw new TokenContractError();
  }
  const safeIDs = ids.map((id) => asInteger(id, 1, 2_147_483_647));
  const response = await api.post<unknown>('/token/batch', { ids: safeIDs }, responseLimits);
  const envelope = successfulEnvelope(response.data);
  return asInteger(envelope.data, 0, safeIDs.length);
}
