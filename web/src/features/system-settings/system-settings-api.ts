import { api } from '../../api';

export const SYSTEM_SETTINGS_ROOT_ROLE = 100;
export const MAX_OPTION_KEY_BYTES = 128;
export const MAX_OPTION_VALUE_BYTES = 1024 * 1024;

const MAX_RESPONSE_BYTES = 4 * 1024 * 1024;
const MAX_OPTION_COUNT = 4_096;
const MAX_MESSAGE_BYTES = 512;
const MAX_ID = 2_147_483_647;
const MAX_TIMESTAMP = 253_402_300_799;
const MAX_AFFINITY_RULES = 1_000;
const MAX_AFFINITY_ENTRIES = 1_000_000;
const MAX_PERFORMANCE_LOG_FILES = 65_536;
const MAX_PERFORMANCE_LOG_PATH_BYTES = 4_096;
const MAX_PERFORMANCE_LOG_NAME_BYTES = 1_024;
const MAX_LOG_CLEANUP_VALUE = 36_500;
const MAX_RELEASE_RESPONSE_BYTES = 512 * 1_024;
const MAX_RELEASE_NOTES_BYTES = 128 * 1_024;
const RELEASE_REQUEST_TIMEOUT_MS = 10_000;
const REQUEST_TIMEOUT_MS = 15_000;
const REQUEST_LIMITS = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
} as const;
const OPTION_KEY = /^[A-Za-z0-9._:-]{1,128}$/u;
const RELEASE_TAG = /^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$/u;

export const TOKENROUTER_LATEST_RELEASE_URL = 'https://api.github.com/repos/MacroXie04/TokenRouter/releases/latest';

type UnknownRecord = Record<string, unknown>;

export interface SystemOption {
  key: string;
  value: string;
  /** The server confirms that a write-only value exists without returning it. */
  redacted?: boolean;
  /** A synthesized canonical editor value sourced from an old installation. */
  compatibilitySource?: 'InitialQuota';
}

export interface PaymentComplianceStatus {
  confirmed: true;
  termsVersion: 'v1';
  confirmedAt: number;
  confirmedBy: number;
}

export interface AffinityCacheStats {
  enabled: boolean;
  total: number;
  unknown: number;
  byRuleName: Record<string, number>;
  cacheCapacity: number;
  cacheAlgorithm: string;
}

export type PerformanceLogCleanupMode = 'by_count' | 'by_days';

export interface PerformanceLogSummary {
  enabled: boolean;
  logDirectory: string;
  fileCount: number;
  totalSize: number;
  oldestTime?: string;
  newestTime?: string;
}

export interface PerformanceLogCleanupResult {
  deletedCount: number;
  freedBytes: number;
  failedCount: number;
}

export type LogCleanupTaskStatus = 'pending' | 'running' | 'succeeded' | 'failed';

export interface LogCleanupTask {
  id: number;
  taskId: string;
  status: LogCleanupTaskStatus;
  targetTimestamp: number;
  progress: number;
  processed: number;
  total: number;
  deletedCount: number | null;
}

export interface RuntimeVersionInfo {
  currentVersion: string;
  startTimestamp: number;
}

export interface TokenRouterReleaseInfo {
  tagName: string;
  name: string;
  notes: string;
  url: string;
  publishedAt?: string;
}

export class SystemSettingsContractError extends Error {
  constructor() {
    super('Invalid system settings API response');
    this.name = 'SystemSettingsContractError';
  }
}

export class SystemSettingsAccessError extends Error {
  constructor() {
    super('Root access is required');
    this.name = 'SystemSettingsAccessError';
  }
}

export class SystemSettingsRequestError extends Error {
  constructor(message = 'Request failed') {
    super(message);
    this.name = 'SystemSettingsRequestError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new SystemSettingsContractError();
  }
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: readonly string[]): void {
  const keys = new Set(allowed);
  if (Object.keys(value).some((key) => !keys.has(key))) {
    throw new SystemSettingsContractError();
  }
}

function encodedBytes(value: unknown): number {
  try {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) throw new Error('not serializable');
    return new TextEncoder().encode(encoded).byteLength;
  } catch {
    throw new SystemSettingsContractError();
  }
}

function ensureBoundedPayload(value: unknown, maximum = MAX_RESPONSE_BYTES): void {
  if (encodedBytes(value) > maximum) throw new SystemSettingsContractError();
}

function safeText(value: unknown, maximumBytes: number, allowNewlines = false): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximumBytes) {
    throw new SystemSettingsContractError();
  }
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    const permittedWhitespace = allowNewlines && (code === 0x09 || code === 0x0a || code === 0x0d);
    if ((!permittedWhitespace && code <= 0x1f) || (code >= 0x7f && code <= 0x9f)
      || code === 0x061c || code === 0x200e || code === 0x200f
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      throw new SystemSettingsContractError();
    }
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) throw new SystemSettingsContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new SystemSettingsContractError();
    }
  }
  return value;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new SystemSettingsContractError();
  }
  return value as number;
}

function optionalRFC3339(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  const parsed = safeText(value, 64);
  const timestamp = Date.parse(parsed);
  if (!Number.isFinite(timestamp) || timestamp < 0 || timestamp > MAX_TIMESTAMP * 1_000) {
    throw new SystemSettingsContractError();
  }
  return parsed;
}

function optionalSafeText(value: unknown, maximumBytes: number, allowNewlines = false): string {
  if (value === undefined || value === null) return '';
  return safeText(value, maximumBytes, allowNewlines);
}

function optionKey(value: unknown): string {
  const parsed = safeText(value, MAX_OPTION_KEY_BYTES);
  if (!OPTION_KEY.test(parsed)) throw new SystemSettingsContractError();
  return parsed;
}

function envelope(value: unknown, requireData: boolean): UnknownRecord {
  ensureBoundedPayload(value);
  const parsed = record(value);
  exactKeys(parsed, ['success', 'message', 'data']);
  const message = parsed.message === undefined ? '' : safeText(parsed.message, MAX_MESSAGE_BYTES, true);
  if (parsed.success !== true) throw new SystemSettingsRequestError(message || 'Request failed');
  if (requireData && !Object.hasOwn(parsed, 'data')) throw new SystemSettingsContractError();
  return parsed;
}

/** Mirrors controller.isSensitiveOptionKey and covers common legacy spellings. */
export function isSensitiveSystemOptionKey(key: string): boolean {
  return /(?:token|secret|key|password|credential)$/iu.test(key);
}

/**
 * Existing installations may have only InitialQuota. The server intentionally
 * retains that value as a fallback only while QuotaForNewUser is absent. Show
 * the effective value in the canonical editor, while marking it so saving the
 * unchanged value still creates the canonical option.
 */
export function normalizeRegistrationSystemOptions(options: readonly SystemOption[]): SystemOption[] {
  if (options.some((option) => option.key === 'QuotaForNewUser')) {
    return options.map((option) => ({ ...option }));
  }
  return options.map((option) => option.key === 'InitialQuota'
    ? { key: 'QuotaForNewUser', value: option.value, compatibilitySource: 'InitialQuota' }
    : { ...option });
}

export function parseSystemOptionsResponse(value: unknown): SystemOption[] {
  const data = envelope(value, true).data;
  if (!Array.isArray(data) || data.length > MAX_OPTION_COUNT) throw new SystemSettingsContractError();
  const seen = new Set<string>();
  const options = data.map((candidate) => {
    const raw = record(candidate);
    exactKeys(raw, ['key', 'value', 'redacted']);
    const key = optionKey(raw.key);
    if (seen.has(key)) throw new SystemSettingsContractError();
    seen.add(key);
    const value = safeText(raw.value, MAX_OPTION_VALUE_BYTES, true);
    if (raw.redacted !== undefined && typeof raw.redacted !== 'boolean') throw new SystemSettingsContractError();
    if (raw.redacted === true && (!isSensitiveSystemOptionKey(key) || value !== '')) {
      throw new SystemSettingsContractError();
    }
    if (isSensitiveSystemOptionKey(key)) return { key, value: '', redacted: raw.redacted === true || value !== '' };
    return { key, value };
  });
  return normalizeRegistrationSystemOptions(options);
}

export function redactProvidedSystemOptions(options: readonly SystemOption[]): SystemOption[] {
  for (const option of options) {
    if (option.compatibilitySource !== undefined
      && (option.key !== 'QuotaForNewUser' || option.compatibilitySource !== 'InitialQuota')) {
      throw new SystemSettingsContractError();
    }
  }
  const parsed = parseSystemOptionsResponse({
    success: true,
    message: '',
    data: options.map(({ key, value }) => ({ key, value })),
  });
  const redactedKeys = new Set(options.filter((option) => option.redacted).map((option) => option.key));
  const legacyFallbackKeys = new Set(options
    .filter((option) => option.compatibilitySource === 'InitialQuota')
    .map((option) => option.key));
  return parsed.map((option) => ({
    ...option,
    ...(redactedKeys.has(option.key) ? { redacted: true } : {}),
    ...(option.compatibilitySource === 'InitialQuota' || legacyFallbackKeys.has(option.key)
      ? { compatibilitySource: 'InitialQuota' as const } : {}),
  }));
}

export function assertSystemSettingsRoot(role: number): void {
  if (!Number.isSafeInteger(role) || role < SYSTEM_SETTINGS_ROOT_ROLE) {
    throw new SystemSettingsAccessError();
  }
}

export function validateSystemOptionMutation(input: { key: string; value: string | boolean | number }): void {
  optionKey(input.key);
  if (typeof input.value === 'string') {
    safeText(input.value, MAX_OPTION_VALUE_BYTES, true);
  } else if (typeof input.value === 'number') {
    if (!Number.isFinite(input.value)) throw new SystemSettingsContractError();
  } else if (typeof input.value !== 'boolean') {
    throw new SystemSettingsContractError();
  }
}

function abortError(): Error {
  if (typeof DOMException !== 'undefined') return new DOMException('The operation was aborted', 'AbortError');
  const error = new Error('The operation was aborted');
  error.name = 'AbortError';
  return error;
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw abortError();
}

let mutationTail: Promise<void> = Promise.resolve();

/** All settings writes share one queue so related option values never race in the browser. */
export function serializeSystemSettingsMutation<T>(operation: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  const run = mutationTail.then(async () => {
    throwIfAborted(signal);
    return operation();
  }, async () => {
    throwIfAborted(signal);
    return operation();
  });
  mutationTail = run.then(() => undefined, () => undefined);
  return run;
}

export async function loadSystemOptions(signal?: AbortSignal): Promise<SystemOption[]> {
  throwIfAborted(signal);
  const response = await api.get<unknown>('/option/', {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  return parseSystemOptionsResponse(response.data);
}

export function updateSystemOption(
  input: { key: string; value: string | boolean | number },
  signal?: AbortSignal,
): Promise<void> {
  validateSystemOptionMutation(input);
  return serializeSystemSettingsMutation(async () => {
    const response = await api.put<unknown>('/option/', input, {
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    envelope(response.data, false);
  }, signal);
}

function parsePaymentComplianceStatus(value: unknown): PaymentComplianceStatus {
  const raw = record(value);
  exactKeys(raw, ['confirmed', 'terms_version', 'confirmed_at', 'confirmed_by']);
  if (raw.confirmed !== true || raw.terms_version !== 'v1') throw new SystemSettingsContractError();
  return {
    confirmed: true,
    termsVersion: 'v1',
    confirmedAt: integer(raw.confirmed_at, 0, MAX_TIMESTAMP),
    confirmedBy: integer(raw.confirmed_by, 1, MAX_ID),
  };
}

export function confirmPaymentCompliance(signal?: AbortSignal): Promise<PaymentComplianceStatus> {
  return serializeSystemSettingsMutation(async () => {
    const response = await api.post<unknown>(
      '/option/payment_compliance',
      { confirmed: true },
      { signal, timeout: REQUEST_TIMEOUT_MS, ...REQUEST_LIMITS },
    );
    throwIfAborted(signal);
    return parsePaymentComplianceStatus(envelope(response.data, true).data);
  }, signal);
}

export function resetModelPricing(signal?: AbortSignal): Promise<void> {
  return serializeSystemSettingsMutation(async () => {
    const response = await api.post<unknown>('/option/rest_model_ratio', undefined, {
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    envelope(response.data, false);
  }, signal);
}

function parseAffinityStats(value: unknown): AffinityCacheStats {
  const raw = record(value);
  exactKeys(raw, ['enabled', 'total', 'unknown', 'by_rule_name', 'cache_capacity', 'cache_algo']);
  if (typeof raw.enabled !== 'boolean') throw new SystemSettingsContractError();
  const total = integer(raw.total, 0, MAX_AFFINITY_ENTRIES);
  const unknown = integer(raw.unknown, 0, total);
  const cacheCapacity = integer(raw.cache_capacity, 0, MAX_AFFINITY_ENTRIES);
  if (total > cacheCapacity) throw new SystemSettingsContractError();
  const rules = record(raw.by_rule_name);
  if (Object.keys(rules).length > MAX_AFFINITY_RULES) throw new SystemSettingsContractError();
  const byRuleName: Record<string, number> = {};
  let categorized = 0;
  for (const [name, count] of Object.entries(rules)) {
    const safeName = safeText(name, 128);
    if (safeName === '') throw new SystemSettingsContractError();
    const safeCount = integer(count, 0, total);
    categorized += safeCount;
    if (categorized > total) throw new SystemSettingsContractError();
    byRuleName[safeName] = safeCount;
  }
  if (categorized + unknown !== total) throw new SystemSettingsContractError();
  return {
    enabled: raw.enabled,
    total,
    unknown,
    byRuleName,
    cacheCapacity,
    cacheAlgorithm: safeText(raw.cache_algo, 64),
  };
}

export async function loadAffinityCacheStats(signal?: AbortSignal): Promise<AffinityCacheStats> {
  throwIfAborted(signal);
  const response = await api.get<unknown>('/option/channel_affinity_cache', {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  return parseAffinityStats(envelope(response.data, true).data);
}

function parseDeletedCount(value: unknown): number {
  const raw = record(value);
  exactKeys(raw, ['deleted']);
  return integer(raw.deleted, 0, MAX_AFFINITY_ENTRIES);
}

export function clearAffinityCache(
  selection: { all: true } | { ruleName: string },
  signal?: AbortSignal,
): Promise<number> {
  const params = 'all' in selection
    ? { all: true }
    : { rule_name: safeText(selection.ruleName.trim(), 128) };
  if ('rule_name' in params && params.rule_name === '') throw new SystemSettingsContractError();
  return serializeSystemSettingsMutation(async () => {
    const response = await api.delete<unknown>('/option/channel_affinity_cache', {
      params,
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    return parseDeletedCount(envelope(response.data, true).data);
  }, signal);
}

export function parsePerformanceLogSummaryResponse(value: unknown): PerformanceLogSummary {
  const raw = record(envelope(value, true).data);
  exactKeys(raw, ['log_dir', 'enabled', 'file_count', 'total_size', 'oldest_time', 'newest_time', 'files']);
  if (typeof raw.enabled !== 'boolean') throw new SystemSettingsContractError();
  const logDirectory = safeText(raw.log_dir, MAX_PERFORMANCE_LOG_PATH_BYTES);
  const fileCount = integer(raw.file_count, 0, MAX_PERFORMANCE_LOG_FILES);
  const totalSize = integer(raw.total_size, 0, Number.MAX_SAFE_INTEGER);
  const files = raw.files === null ? [] : raw.files;
  if (!Array.isArray(files) || files.length !== fileCount) throw new SystemSettingsContractError();
  const names = new Set<string>();
  let observedSize = 0;
  for (const candidate of files) {
    const file = record(candidate);
    exactKeys(file, ['name', 'size', 'mod_time']);
    const name = safeText(file.name, MAX_PERFORMANCE_LOG_NAME_BYTES);
    if (!/^oneapi-[^/\\]+\.log$/u.test(name) || names.has(name)) throw new SystemSettingsContractError();
    names.add(name);
    const size = integer(file.size, 0, Number.MAX_SAFE_INTEGER);
    if (size > Number.MAX_SAFE_INTEGER - observedSize) throw new SystemSettingsContractError();
    observedSize += size;
    if (!optionalRFC3339(file.mod_time)) throw new SystemSettingsContractError();
  }
  if (observedSize !== totalSize) throw new SystemSettingsContractError();
  const oldestTime = optionalRFC3339(raw.oldest_time);
  const newestTime = optionalRFC3339(raw.newest_time);
  if (!raw.enabled) {
    if (logDirectory !== '' || fileCount !== 0 || totalSize !== 0 || oldestTime || newestTime) {
      throw new SystemSettingsContractError();
    }
  } else if (fileCount === 0) {
    if (oldestTime || newestTime) throw new SystemSettingsContractError();
  } else if (!oldestTime || !newestTime || Date.parse(oldestTime) > Date.parse(newestTime)) {
    throw new SystemSettingsContractError();
  }
  return { enabled: raw.enabled, logDirectory, fileCount, totalSize, oldestTime, newestTime };
}

function parsePerformanceLogCleanupResult(value: unknown): PerformanceLogCleanupResult {
  const raw = record(envelope(value, true).data);
  exactKeys(raw, ['deleted_count', 'freed_bytes', 'failed_files']);
  const deletedCount = integer(raw.deleted_count, 0, MAX_PERFORMANCE_LOG_FILES);
  const freedBytes = integer(raw.freed_bytes, 0, Number.MAX_SAFE_INTEGER);
  if (!Array.isArray(raw.failed_files) || raw.failed_files.length > MAX_PERFORMANCE_LOG_FILES) {
    throw new SystemSettingsContractError();
  }
  const names = new Set<string>();
  raw.failed_files.forEach((candidate) => {
    const name = safeText(candidate, MAX_PERFORMANCE_LOG_NAME_BYTES);
    if (!/^oneapi-[^/\\]+\.log$/u.test(name) || names.has(name)) throw new SystemSettingsContractError();
    names.add(name);
  });
  if (deletedCount + names.size > MAX_PERFORMANCE_LOG_FILES) throw new SystemSettingsContractError();
  return { deletedCount, freedBytes, failedCount: names.size };
}

function parseLogCleanupTask(value: unknown): LogCleanupTask {
  const raw = record(value);
  exactKeys(raw, [
    'id', 'task_id', 'type', 'status', 'active_key', 'payload', 'state', 'result', 'error',
    'locked_by', 'created_at', 'updated_at',
  ]);
  if (raw.type !== 'log_cleanup'
    || !['pending', 'running', 'succeeded', 'failed'].includes(String(raw.status))) {
    throw new SystemSettingsContractError();
  }
  const taskId = safeText(raw.task_id, 64);
  if (!/^[a-f0-9]{32}$/u.test(taskId)) throw new SystemSettingsContractError();
  const status = raw.status as LogCleanupTaskStatus;
  if (raw.active_key !== undefined && raw.active_key !== null && raw.active_key !== 'log_cleanup') {
    throw new SystemSettingsContractError();
  }
  if ((status === 'pending' || status === 'running') && raw.active_key !== 'log_cleanup') {
    throw new SystemSettingsContractError();
  }
  const payload = record(raw.payload);
  exactKeys(payload, ['target_timestamp', 'batch_size']);
  const targetTimestamp = integer(payload.target_timestamp, 1, MAX_TIMESTAMP);
  integer(payload.batch_size, 1, 1_000);

  let progress = 0;
  let processed = 0;
  let total = 0;
  if (raw.state !== undefined && raw.state !== null) {
    const state = record(raw.state);
    exactKeys(state, ['total', 'processed', 'progress', 'remaining']);
    total = integer(state.total, 0, Number.MAX_SAFE_INTEGER);
    processed = integer(state.processed, 0, total);
    progress = integer(state.progress, 0, 100);
    const remaining = integer(state.remaining, 0, total);
    if (remaining !== total - processed || (total === 0 && progress !== 0 && status !== 'succeeded')) {
      throw new SystemSettingsContractError();
    }
  }

  let deletedCount: number | null = null;
  if (raw.result !== undefined && raw.result !== null) {
    const result = record(raw.result);
    exactKeys(result, ['deleted_count']);
    deletedCount = integer(result.deleted_count, 0, Number.MAX_SAFE_INTEGER);
    if (status !== 'succeeded' || deletedCount !== processed) throw new SystemSettingsContractError();
  }
  if (status === 'succeeded' && (deletedCount === null || progress !== 100)) {
    throw new SystemSettingsContractError();
  }
  safeText(raw.error, 2_048, true);
  safeText(raw.locked_by, 128);
  integer(raw.id, 1, Number.MAX_SAFE_INTEGER);
  integer(raw.created_at, 0, MAX_TIMESTAMP);
  integer(raw.updated_at, 0, MAX_TIMESTAMP);
  return {
    id: raw.id as number,
    taskId,
    status,
    targetTimestamp,
    progress,
    processed,
    total,
    deletedCount,
  };
}

function parseOptionalLogCleanupTaskResponse(value: unknown): LogCleanupTask | null {
  const data = envelope(value, true).data;
  return data === null ? null : parseLogCleanupTask(data);
}

export async function loadPerformanceLogSummary(signal?: AbortSignal): Promise<PerformanceLogSummary> {
  throwIfAborted(signal);
  const response = await api.get<unknown>('/performance/logs', {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  return parsePerformanceLogSummaryResponse(response.data);
}

export function cleanupPerformanceLogFiles(
  mode: PerformanceLogCleanupMode,
  value: number,
  signal?: AbortSignal,
): Promise<PerformanceLogCleanupResult> {
  if ((mode !== 'by_count' && mode !== 'by_days')
    || !Number.isSafeInteger(value) || value < 1 || value > MAX_LOG_CLEANUP_VALUE) {
    throw new SystemSettingsContractError();
  }
  return serializeSystemSettingsMutation(async () => {
    const response = await api.delete<unknown>('/performance/logs', {
      params: { mode, value },
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    return parsePerformanceLogCleanupResult(response.data);
  }, signal);
}

export async function loadCurrentLogCleanupTask(signal?: AbortSignal): Promise<LogCleanupTask | null> {
  throwIfAborted(signal);
  const response = await api.get<unknown>('/system-task/current', {
    params: { type: 'log_cleanup' },
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  return parseOptionalLogCleanupTaskResponse(response.data);
}

export async function loadLogCleanupTask(taskId: string, signal?: AbortSignal): Promise<LogCleanupTask> {
  if (!/^[a-f0-9]{32}$/u.test(taskId)) throw new SystemSettingsContractError();
  throwIfAborted(signal);
  const response = await api.get<unknown>(`/system-task/${encodeURIComponent(taskId)}`, {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  const task = parseOptionalLogCleanupTaskResponse(response.data);
  if (!task) throw new SystemSettingsContractError();
  return task;
}

export function startLogCleanupTask(targetTimestamp: number, signal?: AbortSignal): Promise<LogCleanupTask> {
  const now = Math.floor(Date.now() / 1_000);
  if (!Number.isSafeInteger(targetTimestamp) || targetTimestamp < 1
    || targetTimestamp > MAX_TIMESTAMP || targetTimestamp > now) {
    throw new SystemSettingsContractError();
  }
  return serializeSystemSettingsMutation(async () => {
    const response = await api.post<unknown>('/system-task/log-cleanup', undefined, {
      params: { target_timestamp: targetTimestamp },
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    const task = parseOptionalLogCleanupTaskResponse(response.data);
    if (!task) throw new SystemSettingsContractError();
    return task;
  }, signal);
}

export function parseRuntimeVersionResponse(value: unknown): RuntimeVersionInfo {
  const data = record(envelope(value, true).data);
  const currentVersion = safeText(data.version, 128);
  if (currentVersion === '' || currentVersion.trim() !== currentVersion) {
    throw new SystemSettingsContractError();
  }
  return {
    currentVersion,
    startTimestamp: integer(data.start_time, 1, MAX_TIMESTAMP),
  };
}

function safeTokenRouterReleaseURL(value: unknown, tagName: string): string {
  const source = safeText(value, 2_048);
  try {
    const parsed = new URL(source);
    const prefix = '/MacroXie04/TokenRouter/releases/tag/';
    const encodedTag = parsed.pathname.startsWith(prefix) ? parsed.pathname.slice(prefix.length) : '';
    if (parsed.protocol !== 'https:' || parsed.hostname !== 'github.com' || parsed.port !== ''
      || parsed.username !== '' || parsed.password !== '' || parsed.search !== '' || parsed.hash !== ''
      || encodedTag === '' || encodedTag.includes('/') || decodeURIComponent(encodedTag) !== tagName) {
      throw new SystemSettingsContractError();
    }
    return parsed.toString();
  } catch (error) {
    if (error instanceof SystemSettingsContractError) throw error;
    throw new SystemSettingsContractError();
  }
}

export function parseTokenRouterReleaseResponse(value: unknown): TokenRouterReleaseInfo {
  ensureBoundedPayload(value, MAX_RELEASE_RESPONSE_BYTES);
  const raw = record(value);
  const tagName = safeText(raw.tag_name, 128);
  if (!RELEASE_TAG.test(tagName)) throw new SystemSettingsContractError();
  const publishedAt = optionalRFC3339(raw.published_at);
  return {
    tagName,
    name: optionalSafeText(raw.name, 512),
    notes: optionalSafeText(raw.body, MAX_RELEASE_NOTES_BYTES, true),
    url: safeTokenRouterReleaseURL(raw.html_url, tagName),
    ...(publishedAt ? { publishedAt } : {}),
  };
}

async function readBoundedReleaseJSON(response: Response): Promise<unknown> {
  const contentLength = response.headers.get('content-length');
  if (contentLength !== null
    && (!/^\d+$/u.test(contentLength) || Number(contentLength) > MAX_RELEASE_RESPONSE_BYTES)) {
    await response.body?.cancel().catch(() => undefined);
    throw new SystemSettingsContractError();
  }
  const contentType = response.headers.get('content-type');
  if (contentType !== null && !/^application\/(?:json|[^;]+\+json)(?:\s*;|$)/iu.test(contentType)) {
    await response.body?.cancel().catch(() => undefined);
    throw new SystemSettingsContractError();
  }
  if (!response.body) throw new SystemSettingsContractError();

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    for (;;) {
      const next = await reader.read();
      if (next.done) break;
      if (!(next.value instanceof Uint8Array)) throw new SystemSettingsContractError();
      length += next.value.byteLength;
      if (length > MAX_RELEASE_RESPONSE_BYTES) throw new SystemSettingsContractError();
      chunks.push(next.value);
    }
    reader.releaseLock();
  } catch (error) {
    await reader.cancel().catch(() => undefined);
    throw error;
  }

  const payload = new Uint8Array(length);
  let offset = 0;
  chunks.forEach((chunk) => {
    payload.set(chunk, offset);
    offset += chunk.byteLength;
  });
  try {
    return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(payload)) as unknown;
  } catch {
    throw new SystemSettingsContractError();
  }
}

export async function loadRuntimeVersion(signal?: AbortSignal): Promise<RuntimeVersionInfo> {
  throwIfAborted(signal);
  const response = await api.get<unknown>('/status', {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  return parseRuntimeVersionResponse(response.data);
}

export async function loadLatestTokenRouterRelease(signal?: AbortSignal): Promise<TokenRouterReleaseInfo> {
  throwIfAborted(signal);
  const request = new AbortController();
  let timedOut = false;
  const abort = () => request.abort();
  signal?.addEventListener('abort', abort, { once: true });
  const timer = globalThis.setTimeout(() => {
    timedOut = true;
    request.abort();
  }, RELEASE_REQUEST_TIMEOUT_MS);
  try {
    const response = await fetch(TOKENROUTER_LATEST_RELEASE_URL, {
      method: 'GET',
      headers: { Accept: 'application/vnd.github+json' },
      signal: request.signal,
      cache: 'no-store',
      credentials: 'omit',
      redirect: 'error',
      referrerPolicy: 'no-referrer',
    });
    if (!response.ok) {
      await response.body?.cancel().catch(() => undefined);
      throw new SystemSettingsRequestError();
    }
    const payload = await readBoundedReleaseJSON(response);
    throwIfAborted(signal);
    return parseTokenRouterReleaseResponse(payload);
  } catch (error) {
    if (timedOut) throw new SystemSettingsRequestError();
    throw error;
  } finally {
    globalThis.clearTimeout(timer);
    signal?.removeEventListener('abort', abort);
  }
}

function comparableVersion(value: string): string {
  return value.replace(/^v(?=\d)/u, '');
}

export function isCurrentTokenRouterRelease(currentVersion: string, tagName: string): boolean {
  return currentVersion !== 'dev'
    && RELEASE_TAG.test(currentVersion)
    && RELEASE_TAG.test(tagName)
    && comparableVersion(currentVersion) === comparableVersion(tagName);
}

export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}
