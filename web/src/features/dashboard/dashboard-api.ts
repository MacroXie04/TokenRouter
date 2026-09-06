import { api } from '../../shared/api/client';
import {
  ConsoleContentContractError,
  parseConsoleAPIInfo,
  parseConsoleFAQ,
  type ConsoleAPIInfo,
  type ConsoleFAQ,
} from './console-content';

export const DASHBOARD_SECTIONS = ['overview', 'models', 'flow', 'users'] as const;
export type DashboardSection = (typeof DASHBOARD_SECTIONS)[number];

export const DASHBOARD_MAX_RANGE_SECONDS = 30 * 24 * 60 * 60;
const DASHBOARD_DEFAULT_RANGE_SECONDS = 24 * 60 * 60;
const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_STATUS_RESPONSE_BYTES = 4 * 1024 * 1024;
const MAX_PUBLIC_CONTENT_RESPONSE_BYTES = 6 * 1024 * 1024;
const MAX_PUBLIC_CONTENT_BYTES = 1_000_000;
const MAX_UPTIME_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_ROWS = 20_000;
const MAX_PERFORMANCE_MODELS = 10_000;
const MAX_RECENT_SUCCESS_RATES = 3;
const MAX_UNIX_SECONDS = 4_102_444_800; // 2100-01-01T00:00:00Z
const MAX_ID = 2_147_483_647;
const MAX_SEARCH_BYTES = 4_096;

type UnknownRecord = Record<string, unknown>;

export interface DashboardQuery {
  startTimestamp: number;
  endTimestamp: number;
  username: string;
}

export interface DashboardSummary {
  quota: number;
  requests: number;
  tokens: number;
}

export type DashboardMetric = 'quota' | 'requests' | 'tokens';
export type DashboardGranularity = 'hour' | 'day' | 'week';

export interface DashboardTimelinePoint extends DashboardSummary {
  timestamp: number;
}

export interface DashboardQuotaRow {
  modelName: string;
  username: string;
  createdAt: number;
  quota: number;
  requests: number;
  tokens: number;
}

export interface DashboardUserRow {
  username: string;
  createdAt: number;
  quota: number;
  requests: number;
  tokens: number;
}

export interface DashboardFlowRow {
  userId?: number;
  username: string;
  nodeName: string;
  tokenId?: number;
  tokenName: string;
  group: string;
  modelName: string;
  channelId?: number;
  channelName: string;
  quota: number;
  requests: number;
  tokens: number;
}

export interface DashboardPerformanceRow {
  modelName: string;
  averageLatencyMs: number;
  successRate: number;
  averageTokensPerSecond: number;
  recentSuccessRates: number[];
}

export interface DashboardGatewayInfo {
  systemName: string;
  version: string;
  nodeName: string;
  serverAddress: string;
  apiInfoEnabled: boolean;
  apiInfo: ConsoleAPIInfo[];
  faqEnabled: boolean;
  faq: ConsoleFAQ[];
  uptimeKumaEnabled: boolean;
}

export interface DashboardUptimeMonitor {
  name: string;
  uptime: number;
  status: 0 | 1 | 2 | 3;
  group: string;
}

export interface DashboardUptimeGroup {
  categoryName: string;
  monitors: DashboardUptimeMonitor[];
}

export type DashboardResource<T> = { ok: true; value: T } | { ok: false };

export interface DashboardOverviewContent {
  notice: DashboardResource<string>;
  gateway: DashboardResource<DashboardGatewayInfo>;
  uptime: DashboardResource<DashboardUptimeGroup[]>;
}

export type DashboardResult =
  | { kind: 'quota'; rows: DashboardQuotaRow[]; summary: DashboardSummary }
  | { kind: 'users'; rows: DashboardUserRow[]; summary: DashboardSummary }
  | { kind: 'flow'; rows: DashboardFlowRow[]; summary: DashboardSummary };

export interface DashboardAggregate {
  label: string;
  quota: number;
  requests: number;
  tokens: number;
}

export class DashboardContractError extends Error {
  constructor() {
    super('Invalid dashboard API contract');
    this.name = 'DashboardContractError';
  }
}

export class DashboardAccessError extends Error {
  constructor() {
    super('Dashboard section is not available for this role');
    this.name = 'DashboardAccessError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new DashboardContractError();
  }
  return value as UnknownRecord;
}

function payloadBytes(value: unknown): number {
  try {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) throw new DashboardContractError();
    return new TextEncoder().encode(encoded).byteLength;
  } catch (error) {
    if (error instanceof DashboardContractError) throw error;
    throw new DashboardContractError();
  }
}

function allowedKeys(value: UnknownRecord, allowed: readonly string[]): void {
  const keys = new Set(allowed);
  if (Object.keys(value).some((key) => !keys.has(key))) throw new DashboardContractError();
}

function successfulRows(value: unknown): unknown[] {
  if (payloadBytes(value) > MAX_RESPONSE_BYTES) throw new DashboardContractError();
  const envelope = record(value);
  allowedKeys(envelope, ['success', 'message', 'data']);
  if (envelope.success !== true || !Array.isArray(envelope.data) || envelope.data.length > MAX_ROWS) {
    throw new DashboardContractError();
  }
  return envelope.data;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new DashboardContractError();
  }
  return value as number;
}

function optionalInteger(value: unknown, minimum: number, maximum: number): number | undefined {
  if (value === undefined || value === null || value === 0) return undefined;
  return integer(value, minimum, maximum);
}

function text(value: unknown, maximum: number, allowEmpty = false): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximum || value.trim() !== value) {
    throw new DashboardContractError();
  }
  if (!allowEmpty && value === '') throw new DashboardContractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      throw new DashboardContractError();
    }
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) throw new DashboardContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new DashboardContractError();
    }
  }
  return value;
}

function multilineText(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximum) {
    throw new DashboardContractError();
  }
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if ((code <= 0x1f && code !== 0x09 && code !== 0x0a && code !== 0x0d)
      || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      throw new DashboardContractError();
    }
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) throw new DashboardContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new DashboardContractError();
    }
  }
  return value.trim();
}

function optionalText(value: unknown, maximum: number): string {
  if (value === undefined || value === null || value === '') return '';
  return text(value, maximum);
}

function metric(value: unknown): number {
  return integer(value, 0, Number.MAX_SAFE_INTEGER);
}

function decimal(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new DashboardContractError();
  }
  return value;
}

function checkedAdd(left: number, right: number): number {
  const result = left + right;
  if (!Number.isSafeInteger(result) || result < 0) throw new DashboardContractError();
  return result;
}

function summarize(rows: Array<{ quota: number; requests: number; tokens: number }>): DashboardSummary {
  return rows.reduce<DashboardSummary>((summary, row) => ({
    quota: checkedAdd(summary.quota, row.quota),
    requests: checkedAdd(summary.requests, row.requests),
    tokens: checkedAdd(summary.tokens, row.tokens),
  }), { quota: 0, requests: 0, tokens: 0 });
}

const QUOTA_ROW_KEYS = [
  'id', 'user_id', 'username', 'model_name', 'created_at', 'use_group',
  'token_id', 'channel_id', 'node_name', 'token_used', 'count', 'quota',
] as const;

function parseQuotaRow(value: unknown): DashboardQuotaRow {
  const row = record(value);
  allowedKeys(row, QUOTA_ROW_KEYS);
  return {
    modelName: optionalText(row.model_name, 512),
    username: optionalText(row.username, 64),
    createdAt: integer(row.created_at, 1, MAX_UNIX_SECONDS),
    quota: metric(row.quota),
    requests: metric(row.count),
    tokens: metric(row.token_used),
  };
}

function parseUserRow(value: unknown): DashboardUserRow {
  const row = record(value);
  allowedKeys(row, QUOTA_ROW_KEYS);
  return {
    username: optionalText(row.username, 64),
    createdAt: integer(row.created_at, 1, MAX_UNIX_SECONDS),
    quota: metric(row.quota),
    requests: metric(row.count),
    tokens: metric(row.token_used),
  };
}

const FLOW_ROW_KEYS = [
  'user_id', 'username', 'node_name', 'token_id', 'token_name', 'use_group',
  'channel_id', 'channel_name', 'model_name', 'token_used', 'count', 'quota',
] as const;

function parseFlowRow(value: unknown, role: number): DashboardFlowRow {
  const row = record(value);
  allowedKeys(row, FLOW_ROW_KEYS);
  const userId = optionalInteger(row.user_id, 1, MAX_ID);
  const tokenId = optionalInteger(row.token_id, 1, MAX_ID);
  const channelId = optionalInteger(row.channel_id, 1, MAX_ID);
  const parsed: DashboardFlowRow = {
    ...(userId === undefined ? {} : { userId }),
    username: optionalText(row.username, 64),
    nodeName: optionalText(row.node_name, 64),
    ...(tokenId === undefined ? {} : { tokenId }),
    tokenName: optionalText(row.token_name, 512),
    group: text(row.use_group, 64),
    modelName: optionalText(row.model_name, 512),
    ...(channelId === undefined ? {} : { channelId }),
    channelName: optionalText(row.channel_name, 512),
    quota: metric(row.quota),
    requests: metric(row.count),
    tokens: metric(row.token_used),
  };
  if (role === 100) return parsed;
  if (role === 10) {
    if (parsed.nodeName || parsed.tokenId !== undefined || parsed.tokenName) throw new DashboardContractError();
    return {
      ...(parsed.userId === undefined ? {} : { userId: parsed.userId }),
      username: parsed.username,
      nodeName: '',
      tokenName: '',
      group: parsed.group,
      modelName: parsed.modelName,
      ...(parsed.channelId === undefined ? {} : { channelId: parsed.channelId }),
      channelName: parsed.channelName,
      quota: parsed.quota,
      requests: parsed.requests,
      tokens: parsed.tokens,
    };
  }
  if (parsed.userId !== undefined || parsed.username || parsed.nodeName
    || parsed.channelId !== undefined || parsed.channelName) {
    throw new DashboardContractError();
  }
  return {
    username: '',
    nodeName: '',
    ...(parsed.tokenId === undefined ? {} : { tokenId: parsed.tokenId }),
    tokenName: parsed.tokenName,
    group: parsed.group,
    modelName: parsed.modelName,
    channelName: '',
    quota: parsed.quota,
    requests: parsed.requests,
    tokens: parsed.tokens,
  };
}

export function parseDashboardResponse(value: unknown, section: DashboardSection, role: number): DashboardResult {
  if (!DASHBOARD_SECTIONS.includes(section) || !canAccessDashboardSection(section, role)) {
    throw new DashboardAccessError();
  }
  const rows = successfulRows(value);
  if (section === 'flow') {
    const parsed = rows.map((row) => parseFlowRow(row, role));
    return { kind: 'flow', rows: parsed, summary: summarize(parsed) };
  }
  if (section === 'users') {
    const parsed = rows.map(parseUserRow);
    return { kind: 'users', rows: parsed, summary: summarize(parsed) };
  }
  const parsed = rows.map(parseQuotaRow);
  return { kind: 'quota', rows: parsed, summary: summarize(parsed) };
}

export function parseDashboardPerformanceResponse(value: unknown): DashboardPerformanceRow[] {
  if (payloadBytes(value) > MAX_RESPONSE_BYTES) throw new DashboardContractError();
  const envelope = record(value);
  allowedKeys(envelope, ['success', 'message', 'data']);
  if (envelope.success !== true) throw new DashboardContractError();
  const data = record(envelope.data);
  allowedKeys(data, ['models']);
  if (!Array.isArray(data.models) || data.models.length > MAX_PERFORMANCE_MODELS) {
    throw new DashboardContractError();
  }
  const seenModels = new Set<string>();
  return data.models.map((value) => {
    const row = record(value);
    allowedKeys(row, [
      'model_name', 'avg_latency_ms', 'success_rate', 'avg_tps', 'recent_success_rates',
    ]);
    const modelName = text(row.model_name, 512);
    if (seenModels.has(modelName)) throw new DashboardContractError();
    seenModels.add(modelName);
    if (row.recent_success_rates !== undefined && !Array.isArray(row.recent_success_rates)) {
      throw new DashboardContractError();
    }
    const recentSuccessRates = (row.recent_success_rates ?? []) as unknown[];
    if (recentSuccessRates.length > MAX_RECENT_SUCCESS_RATES) throw new DashboardContractError();
    return {
      modelName,
      averageLatencyMs: integer(row.avg_latency_ms, 0, Number.MAX_SAFE_INTEGER),
      successRate: decimal(row.success_rate, 0, 100),
      averageTokensPerSecond: decimal(row.avg_tps, 0, 1_000_000_000),
      recentSuccessRates: recentSuccessRates.map((rate) => decimal(rate, 0, 100)),
    };
  });
}

function envelopeData(value: unknown, maximumBytes: number): unknown {
  if (payloadBytes(value) > maximumBytes) throw new DashboardContractError();
  const envelope = record(value);
  allowedKeys(envelope, ['success', 'message', 'data']);
  if (envelope.success !== true) throw new DashboardContractError();
  return envelope.data;
}

function safeServerAddress(value: unknown, fallbackOrigin: string): string {
  const raw = value === undefined || value === null ? '' : text(value, 2_048, true);
  const candidate = raw || fallbackOrigin;
  if (!candidate) return '';
  try {
    const parsed = new URL(candidate);
    if ((parsed.protocol !== 'https:' && parsed.protocol !== 'http:')
      || parsed.username || parsed.password || parsed.search || parsed.hash) {
      throw new DashboardContractError();
    }
    if (raw && parsed.protocol === 'http:') {
      const hostname = parsed.hostname.replace(/\.$/u, '').toLowerCase();
      if (hostname !== 'localhost' && !hostname.endsWith('.localhost')
        && hostname !== '127.0.0.1' && hostname !== '[::1]') {
        throw new DashboardContractError();
      }
    }
    return parsed.href.replace(/\/$/u, '');
  } catch (error) {
    if (error instanceof DashboardContractError) throw error;
    throw new DashboardContractError();
  }
}

export function parseDashboardNoticeResponse(value: unknown): string {
  return multilineText(envelopeData(value, MAX_PUBLIC_CONTENT_RESPONSE_BYTES), MAX_PUBLIC_CONTENT_BYTES);
}

export function parseDashboardGatewayResponse(value: unknown, fallbackOrigin: string): DashboardGatewayInfo {
  const data = record(envelopeData(value, MAX_STATUS_RESPONSE_BYTES));
  const systemName = [data.system_name, data.site_name, data.app_name]
    .map((candidate) => optionalText(candidate, 128))
    .find(Boolean) ?? '';
  if (typeof data.api_info_enabled !== 'boolean' || typeof data.faq_enabled !== 'boolean'
    || typeof data.uptime_kuma_enabled !== 'boolean') throw new DashboardContractError();
  let apiInfo: ConsoleAPIInfo[] = [];
  let faq: ConsoleFAQ[] = [];
  try {
    if (data.api_info_enabled) apiInfo = parseConsoleAPIInfo(data.api_info);
    else if (data.api_info !== undefined) throw new DashboardContractError();
    if (data.faq_enabled) faq = parseConsoleFAQ(data.faq);
    else if (data.faq !== undefined) throw new DashboardContractError();
  } catch (error) {
    if (error instanceof DashboardContractError) throw error;
    if (error instanceof ConsoleContentContractError) throw new DashboardContractError();
    throw error;
  }
  return {
    systemName,
    version: optionalText(data.version, 128),
    nodeName: optionalText(data.node_name, 128),
    serverAddress: safeServerAddress(data.server_address, fallbackOrigin),
    apiInfoEnabled: data.api_info_enabled,
    apiInfo,
    faqEnabled: data.faq_enabled,
    faq,
    uptimeKumaEnabled: data.uptime_kuma_enabled,
  };
}

export function parseDashboardUptimeResponse(value: unknown): DashboardUptimeGroup[] {
  const raw = envelopeData(value, MAX_UPTIME_RESPONSE_BYTES);
  if (!Array.isArray(raw) || raw.length > 20) throw new DashboardContractError();
  const categories = new Set<string>();
  let monitorCount = 0;
  return raw.map((candidate) => {
    const group = record(candidate);
    allowedKeys(group, ['categoryName', 'monitors']);
    const categoryName = text(group.categoryName, 50);
    if (categories.has(categoryName) || !Array.isArray(group.monitors)) throw new DashboardContractError();
    categories.add(categoryName);
    monitorCount += group.monitors.length;
    if (monitorCount > 2_000) throw new DashboardContractError();
    return {
      categoryName,
      monitors: group.monitors.map((candidateMonitor) => {
        const monitor = record(candidateMonitor);
        allowedKeys(monitor, ['name', 'uptime', 'status', 'group']);
        const status = integer(monitor.status, 0, 3) as 0 | 1 | 2 | 3;
        return {
          name: text(monitor.name, 256),
          uptime: decimal(monitor.uptime, 0, 1),
          status,
          group: optionalText(monitor.group, 256),
        };
      }),
    };
  });
}

function settledResource<T>(result: PromiseSettledResult<T>): DashboardResource<T> {
  return result.status === 'fulfilled' ? { ok: true, value: result.value } : { ok: false };
}

export function isAdministratorRole(role: number): boolean {
  return role === 10 || role === 100;
}

export function canAccessDashboardSection(section: DashboardSection, role: number): boolean {
  return (role === 1 || isAdministratorRole(role)) && (section !== 'users' || isAdministratorRole(role));
}

export function canFilterDashboardByUsername(section: DashboardSection, role: number): boolean {
  return isAdministratorRole(role) && (section === 'models' || section === 'flow');
}

function unixTimestamp(value: number): number {
  return integer(value, 1, MAX_UNIX_SECONDS);
}

function normalizedUsername(value: string): string {
  return text(value.trim(), 64, true);
}

export function normalizeDashboardQuery(query: DashboardQuery, allowUsername = true): DashboardQuery {
  const startTimestamp = unixTimestamp(query.startTimestamp);
  const endTimestamp = unixTimestamp(query.endTimestamp);
  if (endTimestamp < startTimestamp || endTimestamp - startTimestamp > DASHBOARD_MAX_RANGE_SECONDS) {
    throw new DashboardContractError();
  }
  return {
    startTimestamp,
    endTimestamp,
    username: allowUsername ? normalizedUsername(query.username) : '',
  };
}

export function defaultDashboardQuery(nowSeconds = Math.floor(Date.now() / 1_000)): DashboardQuery {
  const boundedNow = Math.min(Math.max(Math.floor(nowSeconds), 1 + DASHBOARD_DEFAULT_RANGE_SECONDS), MAX_UNIX_SECONDS);
  const endTimestamp = boundedNow;
  return { startTimestamp: endTimestamp - DASHBOARD_DEFAULT_RANGE_SECONDS, endTimestamp, username: '' };
}

function singleSearchValue(params: URLSearchParams, name: string): string | undefined {
  const values = params.getAll(name);
  return values.length === 1 ? values[0] : undefined;
}

function searchTimestamp(value: string | undefined): number | undefined {
  if (value === undefined || !/^\d{1,10}$/u.test(value)) return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 1 && parsed <= MAX_UNIX_SECONDS ? parsed : undefined;
}

export function parseDashboardSearch(search: string, nowSeconds?: number): DashboardQuery {
  const fallback = defaultDashboardQuery(nowSeconds);
  if (typeof search !== 'string' || new TextEncoder().encode(search).byteLength > MAX_SEARCH_BYTES) return fallback;
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  } catch {
    return fallback;
  }
  const start = searchTimestamp(singleSearchValue(params, 'start_timestamp'));
  const end = searchTimestamp(singleSearchValue(params, 'end_timestamp'));
  const rawUsername = singleSearchValue(params, 'username');
  const username = (() => {
    try {
      return rawUsername === undefined ? '' : normalizedUsername(rawUsername);
    } catch {
      return '';
    }
  })();
  if (start === undefined || end === undefined) return { ...fallback, username };
  try {
    return normalizeDashboardQuery({ startTimestamp: start, endTimestamp: end, username });
  } catch {
    return { ...fallback, username };
  }
}

function queryParams(query: DashboardQuery, allowUsername: boolean): Record<string, string | number> {
  const normalized = normalizeDashboardQuery(query, allowUsername);
  return {
    start_timestamp: normalized.startTimestamp,
    end_timestamp: normalized.endTimestamp,
    ...(normalized.username ? { username: normalized.username } : {}),
  };
}

function endpoint(section: DashboardSection, isAdmin: boolean): string {
  if (section === 'flow') return isAdmin ? '/data/flow' : '/data/flow/self';
  if (section === 'users') {
    if (!isAdmin) throw new DashboardAccessError();
    return '/data/users';
  }
  if (section === 'models' && isAdmin) return '/data';
  return '/data/self';
}

const responseLimits = {
  adapter: 'fetch' as const,
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

export async function loadDashboardSection(
  section: DashboardSection,
  role: number,
  query: DashboardQuery,
  signal?: AbortSignal,
): Promise<DashboardResult> {
  if (!DASHBOARD_SECTIONS.includes(section)) throw new DashboardAccessError();
  const isAdmin = isAdministratorRole(role);
  if (!canAccessDashboardSection(section, role)) throw new DashboardAccessError();
  const response = await api.get<unknown>(endpoint(section, isAdmin), {
    params: queryParams(query, canFilterDashboardByUsername(section, role)),
    signal,
    ...responseLimits,
  });
  const result = parseDashboardResponse(response.data, section, role);
  if (result.kind !== 'flow'
    && result.rows.some((row) => row.createdAt < query.startTimestamp || row.createdAt > query.endTimestamp)) {
    throw new DashboardContractError();
  }
  return result;
}

export async function loadDashboardPerformance(signal?: AbortSignal): Promise<DashboardPerformanceRow[]> {
  const response = await api.get<unknown>('/perf-metrics/summary', {
    params: { hours: 24 },
    signal,
    ...responseLimits,
  });
  return parseDashboardPerformanceResponse(response.data);
}

export async function loadDashboardOverview(
  fallbackOrigin: string,
  signal?: AbortSignal,
): Promise<DashboardOverviewContent> {
  const requests = await Promise.allSettled([
    api.get<unknown>('/notice', {
      adapter: 'fetch',
      signal,
      maxContentLength: MAX_PUBLIC_CONTENT_RESPONSE_BYTES,
      maxBodyLength: MAX_PUBLIC_CONTENT_RESPONSE_BYTES,
    }).then((response) => parseDashboardNoticeResponse(response.data)),
    api.get<unknown>('/status', {
      adapter: 'fetch',
      signal,
      maxContentLength: MAX_STATUS_RESPONSE_BYTES,
      maxBodyLength: MAX_STATUS_RESPONSE_BYTES,
    }).then((response) => parseDashboardGatewayResponse(response.data, fallbackOrigin)),
    api.get<unknown>('/uptime/status', {
      adapter: 'fetch',
      signal,
      maxContentLength: MAX_UPTIME_RESPONSE_BYTES,
      maxBodyLength: MAX_UPTIME_RESPONSE_BYTES,
    }).then((response) => parseDashboardUptimeResponse(response.data)),
  ]);
  return {
    notice: settledResource(requests[0]),
    gateway: settledResource(requests[1]),
    uptime: settledResource(requests[2]),
  };
}

export function aggregateDashboardRows(
  rows: Array<{ quota: number; requests: number; tokens: number }>,
  labels: string[],
): DashboardAggregate[] {
  if (rows.length !== labels.length) throw new DashboardContractError();
  const byLabel = new Map<string, DashboardAggregate>();
  rows.forEach((row, index) => {
    const label = text(labels[index], 512);
    const current = byLabel.get(label) ?? { label, quota: 0, requests: 0, tokens: 0 };
    current.quota = checkedAdd(current.quota, row.quota);
    current.requests = checkedAdd(current.requests, row.requests);
    current.tokens = checkedAdd(current.tokens, row.tokens);
    byLabel.set(label, current);
  });
  return [...byLabel.values()].sort((left, right) =>
    right.quota - left.quota
      || right.requests - left.requests
      || right.tokens - left.tokens
      || left.label.localeCompare(right.label));
}

export function dashboardMetricValue(
  row: Pick<DashboardSummary, DashboardMetric>,
  metricName: DashboardMetric,
): number {
  return row[metricName];
}

export function dashboardTimelineBucket(
  timestamp: number,
  granularity: DashboardGranularity,
): number {
  const boundedTimestamp = unixTimestamp(timestamp);
  if (granularity !== 'hour' && granularity !== 'day' && granularity !== 'week') {
    throw new DashboardContractError();
  }
  if (granularity === 'hour') return Math.floor(boundedTimestamp / 3_600) * 3_600;
  const date = new Date(boundedTimestamp * 1_000);
  date.setHours(0, 0, 0, 0);
  if (granularity === 'week') {
    const daysSinceMonday = (date.getDay() + 6) % 7;
    date.setDate(date.getDate() - daysSinceMonday);
  }
  return Math.floor(date.getTime() / 1_000);
}

function nextDashboardTimelineBucket(timestamp: number, granularity: DashboardGranularity): number {
  if (granularity === 'hour') return timestamp + 3_600;
  const date = new Date(timestamp * 1_000);
  date.setDate(date.getDate() + (granularity === 'week' ? 7 : 1));
  return Math.floor(date.getTime() / 1_000);
}

export function dashboardTimelineBuckets(
  query: DashboardQuery,
  granularity: DashboardGranularity,
): number[] {
  const normalized = normalizeDashboardQuery(query, false);
  const first = granularity === 'hour'
    ? Math.ceil(normalized.startTimestamp / 3_600) * 3_600
    : dashboardTimelineBucket(normalized.startTimestamp, granularity);
  const last = dashboardTimelineBucket(normalized.endTimestamp, granularity);
  const buckets: number[] = [];
  for (let timestamp = first; timestamp <= last;) {
    buckets.push(timestamp);
    const next = nextDashboardTimelineBucket(timestamp, granularity);
    if (next <= timestamp || buckets.length > 1_000) throw new DashboardContractError();
    timestamp = next;
  }
  return buckets;
}

export function aggregateDashboardTimeline(
  rows: Array<DashboardQuotaRow | DashboardUserRow>,
  granularity: DashboardGranularity,
): DashboardTimelinePoint[] {
  const buckets = new Map<number, DashboardTimelinePoint>();
  rows.forEach((row) => {
    const timestamp = dashboardTimelineBucket(row.createdAt, granularity);
    const current = buckets.get(timestamp) ?? { timestamp, quota: 0, requests: 0, tokens: 0 };
    current.quota = checkedAdd(current.quota, row.quota);
    current.requests = checkedAdd(current.requests, row.requests);
    current.tokens = checkedAdd(current.tokens, row.tokens);
    buckets.set(timestamp, current);
  });
  return [...buckets.values()].sort((left, right) => left.timestamp - right.timestamp);
}

export function dashboardQueryURL(section: DashboardSection, role: number, query: DashboardQuery): string {
  if (!canAccessDashboardSection(section, role)) throw new DashboardAccessError();
  const normalized = normalizeDashboardQuery(query, canFilterDashboardByUsername(section, role));
  const params = new URLSearchParams({
    start_timestamp: String(normalized.startTimestamp),
    end_timestamp: String(normalized.endTimestamp),
  });
  return `/dashboard/${section}?${params.toString()}`;
}
