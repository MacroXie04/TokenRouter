import { api } from '../../shared/api/client';
import { isChannelProviderType, type ChannelProviderType } from './channel-providers';

export const CHANNEL_PAGE_SIZE = 20;
export const CHANNEL_ENABLED = 1;
export const CHANNEL_AUTO_DISABLED = 2;
export const CHANNEL_MANUALLY_DISABLED = 3;

const MAX_RESPONSE_BYTES = 64 * 1024 * 1024;
const MAX_CHANNELS = 50;
const MAX_MODELS = 10_000;
const MAX_TOTAL = 10_000_000;
const MAX_BULK_CHANNELS = 500;
const MAX_CHANNEL_KEYS = 1_000;
const MAX_DOCUMENT_DEPTH = 8;
const MAX_DOCUMENT_NODES = 4_000;
const MAX_UNIX_SECONDS = 8_640_000_000;

export const CHANNEL_TYPE_OLLAMA = 4;
export const CHANNEL_TYPE_CODEX = 57;

export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue };

export interface ChannelSummary {
  id: number;
  name: string;
  type: number;
  status: number;
  models: string;
  group: string;
  tag: string;
  remark: string;
  balance: number;
  balanceUpdatedTime: number;
  isMultiKey: boolean;
  upstreamCheckEnabled: boolean;
  pendingAddModels: string[];
  pendingRemoveModels: string[];
  testModel?: string;
}

export interface ChannelSensitiveDetail {
  baseUrl: string;
  organization: string;
  // Omitted when the legacy field contains a multi-key credential collection.
  other?: string;
  paramOverride: string;
  headerOverride: string;
  providerSettings: ChannelProviderSettings;
}

export interface ChannelProviderSettings {
  settingSource: string;
  settingsSource: string;
  balanceUrl: string;
  passThroughBodyEnabled: boolean;
  azureResponsesVersion: string;
  vertexKeyType: 'json' | 'api_key';
  awsKeyType: 'auto' | 'ak_sk' | 'api_key';
  advancedCustom: string;
  upstreamCheckEnabled: boolean;
  upstreamAutoSyncEnabled: boolean;
  upstreamIgnoredModels: string;
}

export interface ChannelDetail {
  id: number;
  name: string;
  type: number;
  models: string;
  group: string;
  tag: string;
  remark: string;
  priority: number;
  weight: number;
  testModel: string;
  autoBan: 0 | 1;
  modelMapping: string;
  statusCodeMapping: string;
  sensitive?: ChannelSensitiveDetail;
}

export interface ChannelSearchInput {
  keyword: string;
  group: string;
  model: string;
  status: '' | 'enabled' | 'disabled';
  type: number | null;
  page: number;
  pageSize: number;
  tagMode?: boolean;
  idSort?: boolean;
  sortBy?: '' | 'id' | 'name' | 'priority' | 'balance' | 'response_time' | 'test_time';
  sortOrder?: 'asc' | 'desc';
}

export interface ChannelSearchResult {
  items: ChannelSummary[];
  total: number;
  typeCounts: Record<number, number>;
}

export interface ChannelUpdateInput {
  id: number;
  name: string;
  models: string;
  group: string;
  tag: string;
  remark: string;
  priority?: number;
  weight?: number;
  testModel?: string;
  autoBan?: 0 | 1;
  modelMapping?: string;
  statusCodeMapping?: string;
  type?: number;
  baseUrl?: string;
  organization?: string;
  other?: string;
  paramOverride?: string;
  headerOverride?: string;
  providerSettings?: ChannelProviderSettings;
}

export interface ChannelCreateInput {
  mode: 'single' | 'batch' | 'multi_to_single';
  multi_key_mode: 'random' | 'polling';
  batch_add_set_key_prefix_2_name: boolean;
  name: string;
  type: ChannelProviderType;
  key: string;
  base_url: string;
  models: string;
  group: string;
}

export interface DraftModelDiscoveryInput {
  type: number;
  baseUrl: string;
  key?: string;
  channelId?: number;
  advancedCustom?: string;
  headerOverride?: string;
}

export interface ChannelTestResult {
  success: boolean;
  time: number;
  message?: string;
}

export interface TagUpdateInput {
  tag: string;
  newTag?: string;
  models?: string;
  groups?: string;
  modelMapping?: string;
  priority?: number;
  weight?: number;
  paramOverride?: string;
  headerOverride?: string;
}

export interface MultiKeyEntry {
  index: number;
  fingerprint: string;
  status: 1 | 2 | 3;
  disabledTime: number;
  hasReason: boolean;
}

export interface MultiKeyPage {
  keys: MultiKeyEntry[];
  total: number;
  page: number;
  pageSize: number;
  totalPages: number;
  enabledCount: number;
  manualDisabledCount: number;
  autoDisabledCount: number;
}

export type MultiKeyAction =
  | 'disable_key'
  | 'enable_key'
  | 'enable_all_keys'
  | 'disable_all_keys'
  | 'delete_key'
  | 'delete_disabled_keys';

export interface UpstreamDetection {
  channelId: number;
  channelName: string;
  addModels: string[];
  removeModels: string[];
  lastCheckTime: number;
  autoAddedModels: number;
}

export interface UpstreamApplyResult {
  addedModels: string[];
  removedModels: string[];
  ignoredModels: string[];
  remainingModels: string[];
  remainingRemoveModels: string[];
}

export interface UpstreamApplyAllResult {
  processedChannels: number;
  addedModels: number;
  removedModels: number;
  failedChannelIds: number[];
  resultCount: number;
  resultsTruncated: boolean;
  failedIdsTruncated: boolean;
}

export interface QueuedChannelTask {
  taskId: string;
  status: string;
}

export interface ChannelRepairResult {
  successfulChannels: number;
  failedChannels: number;
}

export interface CodexDocument {
  upstreamStatus: number;
  data: JsonValue;
}

export interface OllamaPullProgress {
  status: string;
  completed: number;
  total: number;
}

type UnknownRecord = Record<string, unknown>;

export class ChannelContractError extends Error {
  constructor() {
    super('Invalid channel API response');
    this.name = 'ChannelContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new ChannelContractError();
  }
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new ChannelContractError();
  }
  if (encoded.length > MAX_RESPONSE_BYTES) throw new ChannelContractError();
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new ChannelContractError();
  }
  return value as number;
}

function text(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length > maximum) throw new ChannelContractError();
  return value;
}

function optionalText(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : text(value, maximum);
}

function finiteNumber(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new ChannelContractError();
  }
  return value;
}

function optionalInteger(value: unknown, minimum: number, maximum: number): number {
  return value === undefined || value === null ? 0 : integer(value, minimum, maximum);
}

function binaryInteger(value: unknown, fallback: 0 | 1): 0 | 1 {
  if (value === undefined || value === null) return fallback;
  const parsed = integer(value, 0, 1);
  if (parsed !== 0 && parsed !== 1) throw new ChannelContractError();
  return parsed;
}

function optionalJSONText(value: unknown, maximum: number): string {
  const raw = optionalText(value, maximum);
  if (!raw.trim()) return raw;
  parseJSONRecord(raw, maximum);
  return raw;
}

function requestJSONText(value: string, maximum: number): string {
  const raw = text(value.trim(), maximum);
  if (!raw) return '';
  parseJSONRecord(raw, maximum);
  return raw;
}

function modelMappingText(value: string): string {
  const raw = requestJSONText(value, 256 * 1024);
  if (!raw) return '';
  const mapping = parseJSONRecord(raw, 256 * 1024);
  if (Object.values(mapping).some((entry) => typeof entry !== 'string')) {
    throw new ChannelContractError();
  }
  return raw;
}

function statusCodeMappingText(value: string): string {
  const raw = requestJSONText(value, 1_024);
  if (!raw) return '';
  const mapping = parseJSONRecord(raw, 1_024);
  for (const [source, target] of Object.entries(mapping)) {
    const sourceCode = Number(source);
    const targetCode = typeof target === 'number' ? target : Number.NaN;
    if (!Number.isInteger(sourceCode) || sourceCode < 100 || sourceCode > 599 ||
        !Number.isInteger(targetCode) || targetCode < 100 || targetCode > 599) {
      throw new ChannelContractError();
    }
  }
  return raw;
}

function replaceControlCharacters(value: string): string {
  return [...value].map((character) => {
    const code = character.codePointAt(0) ?? 0;
    return code < 0x20 || (code >= 0x7f && code <= 0x9f) ? ' ' : character;
  }).join('');
}

function safeChannelDiagnostic(value: unknown): string {
  if (value === undefined || value === null || value === '') return '';
  let diagnostic = replaceControlCharacters(text(value, 4_096))
    .replace(/\bBearer\s+[^\s,;]+/gi, 'Bearer [redacted]')
    .replace(/\b(?:sk|rk|pk)-[A-Za-z0-9._-]{4,}\b/g, '[redacted]')
    .replace(/((?:(?:api[-_ ]?)?key|(?:access[-_ ]?|refresh[-_ ]?)?token|secret|authorization)\s*[=:]\s*)[^\s,;]+/gi, '$1[redacted]')
    .trim();
  if (diagnostic.length > 512) diagnostic = `${diagnostic.slice(0, 509)}...`;
  return diagnostic;
}

function parseJSONRecord(value: unknown, maximum: number): UnknownRecord {
  if (value === undefined || value === null || value === '') return {};
  if (typeof value === 'string') {
    if (value.length > maximum) throw new ChannelContractError();
    try {
      return record(JSON.parse(value));
    } catch {
      throw new ChannelContractError();
    }
  }
  return record(value);
}

function modelList(value: unknown): string[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || value.length > MAX_MODELS) throw new ChannelContractError();
  const unique = new Set<string>();
  for (const candidate of value) {
    const name = text(candidate, 512).trim();
    if (name) unique.add(name);
  }
  return [...unique];
}

function modelValuesFromText(value: string): string[] {
  return value.split(',').map((entry) => entry.trim()).filter(Boolean);
}

function parseChannelInfo(value: unknown): { isMultiKey: boolean } {
  const info = parseJSONRecord(value, 512 * 1024);
  if (info.is_multi_key === undefined) return { isMultiKey: false };
  if (typeof info.is_multi_key !== 'boolean') throw new ChannelContractError();
  return { isMultiKey: info.is_multi_key };
}

function providerOtherWithoutCredentials(value: unknown, isMultiKey: boolean): string | undefined {
  const raw = optionalText(value, 256 * 1024);
  if (!raw || !raw.trim()) return '';
  if (isMultiKey) return undefined;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (Array.isArray(parsed)) return undefined;
    if (typeof parsed === 'object' && parsed !== null && Array.isArray((parsed as UnknownRecord).keys)) {
      return undefined;
    }
  } catch {
    // Provider-specific scalar values are intentionally allowed.
  }
  return raw;
}

function parseUpstreamSettings(value: unknown): {
  enabled: boolean;
  pendingAddModels: string[];
  pendingRemoveModels: string[];
} {
  const settings = parseJSONRecord(value, 512 * 1024);
  const enabled = settings.upstream_model_update_check_enabled;
  if (enabled !== undefined && typeof enabled !== 'boolean') throw new ChannelContractError();
  return {
    enabled: enabled === true,
    pendingAddModels: modelList(settings.upstream_model_update_last_detected_models),
    pendingRemoveModels: modelList(settings.upstream_model_update_last_removed_models),
  };
}

function booleanSetting(settings: UnknownRecord, key: string): boolean {
  const value = settings[key];
  if (value === undefined || value === null) return false;
  if (typeof value !== 'boolean') throw new ChannelContractError();
  return value;
}

function parseProviderSettings(settingValue: unknown, settingsValue: unknown): ChannelProviderSettings {
  const settingSource = optionalJSONText(settingValue, 256 * 1024);
  const settingsSource = optionalJSONText(settingsValue, 512 * 1024);
  const setting = parseJSONRecord(settingSource, 256 * 1024);
  const settings = parseJSONRecord(settingsSource, 512 * 1024);
  const vertexKeyType = optionalText(settings.vertex_key_type, 16) || 'json';
  if (vertexKeyType !== 'json' && vertexKeyType !== 'api_key') throw new ChannelContractError();
  const awsKeyType = optionalText(settings.aws_key_type, 16) || 'auto';
  if (awsKeyType !== 'auto' && awsKeyType !== 'ak_sk' && awsKeyType !== 'api_key') {
    throw new ChannelContractError();
  }
  const advancedCustom = settings.advanced_custom === undefined || settings.advanced_custom === null
    ? ''
    : JSON.stringify(record(settings.advanced_custom));
  return {
    settingSource,
    settingsSource,
    balanceUrl: optionalText(setting.balance_url, 4_096),
    passThroughBodyEnabled: booleanSetting(setting, 'pass_through_body_enabled'),
    azureResponsesVersion: optionalText(settings.azure_responses_version, 128),
    vertexKeyType,
    awsKeyType,
    advancedCustom,
    upstreamCheckEnabled: booleanSetting(settings, 'upstream_model_update_check_enabled'),
    upstreamAutoSyncEnabled: booleanSetting(settings, 'upstream_model_update_auto_sync_enabled'),
    upstreamIgnoredModels: modelList(settings.upstream_model_update_ignored_models).join(','),
  };
}

function buildProviderSettingJSON(settings: ChannelProviderSettings, type: number): {
  setting: string;
  settings: string;
} {
  const setting = parseJSONRecord(requestJSONText(settings.settingSource, 256 * 1024), 256 * 1024);
  const otherSettings = parseJSONRecord(requestJSONText(settings.settingsSource, 512 * 1024), 512 * 1024);

  const balanceUrl = text(settings.balanceUrl.trim(), 4_096);
  if (balanceUrl) setting.balance_url = balanceUrl;
  else delete setting.balance_url;
  if (type === 33) setting.pass_through_body_enabled = Boolean(settings.passThroughBodyEnabled);
  else delete setting.pass_through_body_enabled;

  const azureResponsesVersion = text(settings.azureResponsesVersion.trim(), 128);
  if (type === 3 && azureResponsesVersion) otherSettings.azure_responses_version = azureResponsesVersion;
  else delete otherSettings.azure_responses_version;

  if (settings.vertexKeyType !== 'json' && settings.vertexKeyType !== 'api_key') {
    throw new ChannelContractError();
  }
  if (type === 41) otherSettings.vertex_key_type = settings.vertexKeyType;
  else delete otherSettings.vertex_key_type;

  if (settings.awsKeyType !== 'auto' && settings.awsKeyType !== 'ak_sk' && settings.awsKeyType !== 'api_key') {
    throw new ChannelContractError();
  }
  if (type === 33 && settings.awsKeyType !== 'auto') otherSettings.aws_key_type = settings.awsKeyType;
  else delete otherSettings.aws_key_type;

  const advancedCustom = requestJSONText(settings.advancedCustom, 512 * 1024);
  if (type === 58 && advancedCustom) otherSettings.advanced_custom = parseJSONRecord(advancedCustom, 512 * 1024);
  else if (type === 58) delete otherSettings.advanced_custom;

  otherSettings.upstream_model_update_check_enabled = Boolean(settings.upstreamCheckEnabled);
  otherSettings.upstream_model_update_auto_sync_enabled = Boolean(
    settings.upstreamCheckEnabled && settings.upstreamAutoSyncEnabled,
  );
  const ignoredModels = requestModels(modelValuesFromText(settings.upstreamIgnoredModels));
  if (ignoredModels.length > 0) otherSettings.upstream_model_update_ignored_models = ignoredModels;
  else delete otherSettings.upstream_model_update_ignored_models;

  return { setting: JSON.stringify(setting), settings: JSON.stringify(otherSettings) };
}

function successfulEnvelope(value: unknown): UnknownRecord {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true) throw new ChannelContractError();
  return envelope;
}

function parseChannel(value: unknown): ChannelSummary {
  const item = record(value);
  // List/search responses must never become a credential-disclosure path.
  if ('key' in item && item.key !== '') throw new ChannelContractError();
  const channelInfo = parseChannelInfo(item.channel_info);
  const upstream = parseUpstreamSettings(item.settings);
  const testModel = optionalText(item.test_model, 255).trim();
  const channel: ChannelSummary = {
    id: integer(item.id, 1, 2_147_483_647),
    name: text(item.name, 191),
    type: integer(item.type, 0, 10_000),
    status: integer(item.status, 0, 100),
    models: text(item.models, 256 * 1024),
    group: text(item.group, 512),
    tag: tagText(optionalText(item.tag, 64), true),
    remark: optionalText(item.remark, 255),
    balance: finiteNumber(item.balance ?? 0, -1_000_000_000_000, 1_000_000_000_000),
    balanceUpdatedTime: optionalInteger(item.balance_updated_time, 0, MAX_UNIX_SECONDS),
    isMultiKey: channelInfo.isMultiKey,
    upstreamCheckEnabled: upstream.enabled,
    pendingAddModels: upstream.pendingAddModels,
    pendingRemoveModels: upstream.pendingRemoveModels,
  };
  if (testModel) channel.testModel = testModel;
  return channel;
}

export function parseChannelDetailResponse(value: unknown, includeSensitive: boolean): ChannelDetail {
  if (typeof includeSensitive !== 'boolean') throw new ChannelContractError();
  const item = record(successfulEnvelope(value).data);
  // A detail response is still never a credential-reveal path. Only the empty
  // masked sentinel accepted by the backend may cross this boundary.
  if ('key' in item && item.key !== '') throw new ChannelContractError();
  const channelInfo = parseChannelInfo(item.channel_info);

  const detail: ChannelDetail = {
    id: integer(item.id, 1, 2_147_483_647),
    name: text(item.name, 191),
    type: integer(item.type, 1, 10_000),
    models: text(item.models, 256 * 1024),
    group: text(item.group, 64),
    tag: tagText(optionalText(item.tag, 64), true),
    remark: optionalText(item.remark, 255),
    priority: optionalInteger(item.priority, Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER),
    weight: optionalInteger(item.weight, 0, 4_294_967_295),
    testModel: optionalText(item.test_model, 255),
    autoBan: binaryInteger(item.auto_ban, 1),
    modelMapping: optionalJSONText(item.model_mapping, 256 * 1024),
    statusCodeMapping: optionalJSONText(item.status_code_mapping, 1_024),
  };

  if (includeSensitive) {
    detail.sensitive = {
      baseUrl: optionalText(item.base_url, 4_096),
      organization: optionalText(item.openai_organization, 512),
      other: providerOtherWithoutCredentials(item.other, channelInfo.isMultiKey),
      paramOverride: optionalJSONText(item.param_override, 256 * 1024),
      headerOverride: optionalJSONText(item.header_override, 256 * 1024),
      providerSettings: parseProviderSettings(item.setting, item.settings),
    };
  }
  return detail;
}

export function parseChannelSearchResponse(value: unknown): ChannelSearchResult {
  const data = record(successfulEnvelope(value).data);
  if (!Array.isArray(data.items) || data.items.length > MAX_CHANNELS) throw new ChannelContractError();
  const counts = record(data.type_counts);
  if (Object.keys(counts).length > 1_000) throw new ChannelContractError();
  const typeCounts: Record<number, number> = {};
  for (const [rawType, rawCount] of Object.entries(counts)) {
    if (!/^\d{1,5}$/.test(rawType)) throw new ChannelContractError();
    const channelType = integer(Number(rawType), 0, 10_000);
    typeCounts[channelType] = integer(rawCount, 0, MAX_TOTAL);
  }
  return {
    items: data.items.map(parseChannel),
    total: integer(data.total, 0, MAX_TOTAL),
    typeCounts,
  };
}

export function parseModelListResponse(value: unknown): string[] {
  const data = successfulEnvelope(value).data;
  if (!Array.isArray(data) || data.length > MAX_MODELS) throw new ChannelContractError();
  const unique = new Set<string>();
  for (const model of data) {
    const modelName = text(model, 255).trim();
    if (modelName) unique.add(modelName);
  }
  return [...unique].sort((left, right) => left.localeCompare(right));
}

export function parseChannelTestResponse(value: unknown): ChannelTestResult {
  boundedPayload(value);
  const response = record(value);
  if (typeof response.success !== 'boolean') throw new ChannelContractError();
  const message = safeChannelDiagnostic(response.message);
  if (!response.success) return { success: false, time: 0, ...(message ? { message } : {}) };
  if (typeof response.time !== 'number' || !Number.isFinite(response.time) || response.time < 0 || response.time > 300) {
    throw new ChannelContractError();
  }
  return { success: true, time: response.time, ...(message ? { message } : {}) };
}

export function parseMutationResponse(value: unknown): void {
  successfulEnvelope(value);
}

export function parseCountMutationResponse(value: unknown): number {
  const data = successfulEnvelope(value).data;
  return integer(data, 0, MAX_BULK_CHANNELS);
}

export function parseBalanceResponse(value: unknown): number {
  const envelope = successfulEnvelope(value);
  return finiteNumber(envelope.balance, -1_000_000_000_000, 1_000_000_000_000);
}

export function parseCopyResponse(value: unknown): number {
  const data = record(successfulEnvelope(value).data);
  return integer(data.id, 1, 2_147_483_647);
}

export function parseTextDataResponse(value: unknown): string {
  return text(successfulEnvelope(value).data, 16_384);
}

export function parseMultiKeyPageResponse(value: unknown): MultiKeyPage {
  const data = record(successfulEnvelope(value).data);
  if (!Array.isArray(data.keys) || data.keys.length > 100) throw new ChannelContractError();
  const keys = data.keys.map((candidate): MultiKeyEntry => {
    const key = record(candidate);
    const status = integer(key.status, 1, 3);
    if (status !== 1 && status !== 2 && status !== 3) throw new ChannelContractError();
    const reason = optionalText(key.reason, 512);
    const fingerprint = text(key.key_preview, 16);
    if (!/^sha256:[0-9a-f]{8}$/.test(fingerprint)) throw new ChannelContractError();
    return {
      index: integer(key.index, 0, MAX_TOTAL),
      fingerprint,
      status,
      disabledTime: optionalInteger(key.disabled_time, 0, MAX_UNIX_SECONDS),
      hasReason: reason !== '',
    };
  });
  return {
    keys,
    total: integer(data.total, 0, MAX_TOTAL),
    page: integer(data.page, 1, 1_000_000),
    pageSize: integer(data.page_size, 1, 100),
    totalPages: integer(data.total_pages, 1, 1_000_000),
    enabledCount: integer(data.enabled_count, 0, MAX_TOTAL),
    manualDisabledCount: integer(data.manual_disabled_count, 0, MAX_TOTAL),
    autoDisabledCount: integer(data.auto_disabled_count, 0, MAX_TOTAL),
  };
}

export function parseUpstreamDetectionResponse(value: unknown): UpstreamDetection {
  const data = record(successfulEnvelope(value).data);
  return {
    channelId: integer(data.channel_id, 1, 2_147_483_647),
    channelName: text(data.channel_name, 191),
    addModels: modelList(data.add_models),
    removeModels: modelList(data.remove_models),
    lastCheckTime: integer(data.last_check_time, 0, MAX_UNIX_SECONDS),
    autoAddedModels: integer(data.auto_added_models, 0, MAX_MODELS),
  };
}

export function parseUpstreamApplyResponse(value: unknown): UpstreamApplyResult {
  const data = record(successfulEnvelope(value).data);
  return {
    addedModels: modelList(data.added_models),
    removedModels: modelList(data.removed_models),
    ignoredModels: modelList(data.ignored_models),
    remainingModels: modelList(data.remaining_models),
    remainingRemoveModels: modelList(data.remaining_remove_models),
  };
}

export function parseUpstreamApplyAllResponse(value: unknown): UpstreamApplyAllResult {
  const data = record(successfulEnvelope(value).data);
  if (!Array.isArray(data.failed_channel_ids) || data.failed_channel_ids.length > 1_000) {
    throw new ChannelContractError();
  }
  if (!Array.isArray(data.results) || data.results.length > 1_000) throw new ChannelContractError();
  for (const candidate of data.results) {
    const result = record(candidate);
    integer(result.channel_id, 1, 2_147_483_647);
    text(result.channel_name, 191);
    modelList(result.added_models);
    modelList(result.removed_models);
    modelList(result.remaining_models);
    modelList(result.remaining_remove_models);
  }
  if (data.results_truncated !== undefined && typeof data.results_truncated !== 'boolean') throw new ChannelContractError();
  if (data.failed_channel_ids_truncated !== undefined && typeof data.failed_channel_ids_truncated !== 'boolean') throw new ChannelContractError();
  return {
    processedChannels: integer(data.processed_channels, 0, MAX_TOTAL),
    addedModels: integer(data.added_models, 0, MAX_TOTAL),
    removedModels: integer(data.removed_models, 0, MAX_TOTAL),
    failedChannelIds: data.failed_channel_ids.map((id) => integer(id, 1, 2_147_483_647)),
    resultCount: data.results.length,
    resultsTruncated: data.results_truncated === true,
    failedIdsTruncated: data.failed_channel_ids_truncated === true,
  };
}

export function parseQueuedTaskResponse(value: unknown): QueuedChannelTask {
  const data = record(successfulEnvelope(value).data);
  const taskId = text(data.task_id, 128);
  const status = text(data.status, 32);
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(taskId) || !/^[a-z][a-z0-9_-]{0,31}$/.test(status)) {
    throw new ChannelContractError();
  }
  return {
    taskId,
    status,
  };
}

export function parseChannelRepairResponse(value: unknown): ChannelRepairResult {
  const data = record(successfulEnvelope(value).data);
  return {
    successfulChannels: integer(data.success, 0, MAX_TOTAL),
    failedChannels: integer(data.fails, 0, MAX_TOTAL),
  };
}

function safeDocument(value: unknown, depth: number, budget: { nodes: number }): JsonValue {
  budget.nodes += 1;
  if (budget.nodes > MAX_DOCUMENT_NODES || depth > MAX_DOCUMENT_DEPTH) throw new ChannelContractError();
  if (value === null || typeof value === 'boolean') return value;
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new ChannelContractError();
    return value;
  }
  if (typeof value === 'string') return text(value, 16_384);
  if (Array.isArray(value)) {
    if (value.length > 1_000) throw new ChannelContractError();
    return value.map((entry) => safeDocument(entry, depth + 1, budget));
  }
  const source = record(value);
  const entries = Object.entries(source);
  if (entries.length > 1_000) throw new ChannelContractError();
  const output: { [key: string]: JsonValue } = {};
  for (const [key, entry] of entries) {
    if (key.length > 128) throw new ChannelContractError();
    if (/(?:^|_)(?:access|refresh|id)?_?token$|secret|password|api_?key|private_?key|authorization|credential|cookie|bearer/i.test(key)) {
      output[key] = '[redacted]';
    } else {
      output[key] = safeDocument(entry, depth + 1, budget);
    }
  }
  return output;
}

export function parseCodexDocumentResponse(value: unknown): CodexDocument {
  const envelope = successfulEnvelope(value);
  return {
    upstreamStatus: integer(envelope.upstream_status, 100, 599),
    data: safeDocument(envelope.data, 0, { nodes: 0 }),
  };
}

export function parseCodexRefreshResponse(value: unknown): void {
  const data = record(successfulEnvelope(value).data);
  optionalText(data.expires_at, 128);
  optionalText(data.last_refresh, 128);
  optionalText(data.account_id, 512);
  optionalText(data.email, 320);
  integer(data.channel_id, 1, 2_147_483_647);
  integer(data.channel_type, 0, 10_000);
  text(data.channel_name, 191);
}

export function parseOllamaVersionResponse(value: unknown): string {
  const data = record(successfulEnvelope(value).data);
  return text(data.version, 128);
}

const responseLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

function channelIDs(ids: number[]): number[] {
  if (!Array.isArray(ids) || ids.length === 0 || ids.length > MAX_BULK_CHANNELS) {
    throw new ChannelContractError();
  }
  const normalized = ids.map((id) => integer(id, 1, 2_147_483_647));
  if (new Set(normalized).size !== normalized.length) throw new ChannelContractError();
  return normalized;
}

function hasUnsafeTagCharacter(value: string): boolean {
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    if (code < 0x20 || (code >= 0x7f && code <= 0x9f) || code === 0x061c ||
        code === 0x200e || code === 0x200f || (code >= 0x202a && code <= 0x202e) ||
        (code >= 0x2066 && code <= 0x2069)) return true;
  }
  return false;
}

function tagText(value: string, allowEmpty: boolean): string {
  const normalized = text(value.trim(), 64);
  if ((!allowEmpty && normalized === '') || hasUnsafeTagCharacter(normalized)) {
    throw new ChannelContractError();
  }
  return normalized;
}

export function normalizeChannelTag(value: string, allowEmpty = false): string {
  return tagText(value, allowEmpty);
}

function requestModels(values: string[]): string[] {
  if (!Array.isArray(values) || values.length > MAX_MODELS) throw new ChannelContractError();
  const normalized = values.map((value) => text(value.trim(), 512)).filter(Boolean);
  if (new Set(normalized).size !== normalized.length) throw new ChannelContractError();
  return normalized;
}

export async function searchChannels(input: ChannelSearchInput, signal?: AbortSignal): Promise<ChannelSearchResult> {
  const params: Record<string, string | number | boolean> = {
    p: integer(input.page, 1, 1_000_000),
    page_size: integer(input.pageSize, 1, MAX_CHANNELS),
  };
  if (input.tagMode !== undefined) {
    if (typeof input.tagMode !== 'boolean') throw new ChannelContractError();
    params.tag_mode = input.tagMode;
  }
  if (input.idSort !== undefined) {
    if (typeof input.idSort !== 'boolean') throw new ChannelContractError();
    params.id_sort = input.idSort;
  }
  if (input.sortOrder !== undefined && input.sortOrder !== 'asc' && input.sortOrder !== 'desc') {
    throw new ChannelContractError();
  }
  if (input.sortBy === undefined) {
    // Preserve the existing caller default while allowing the management view
    // to opt into the backend's priority-first default with an empty value.
    params.sort_by = 'id';
    params.sort_order = 'desc';
  } else if (input.sortBy !== '') {
    const validSorts = new Set(['id', 'name', 'priority', 'balance', 'response_time', 'test_time']);
    if (!validSorts.has(input.sortBy)) throw new ChannelContractError();
    params.sort_by = input.sortBy;
    params.sort_order = input.sortOrder === 'asc' ? 'asc' : 'desc';
  }
  const keyword = text(input.keyword.trim(), 128);
  const group = text(input.group.trim(), 512);
  const model = text(input.model.trim(), 255);
  if (keyword) params.keyword = keyword;
  if (group) params.group = group;
  if (model) params.model = model;
  if (input.status) params.status = input.status;
  if (input.type !== null) params.type = integer(input.type, 0, 10_000);

  const response = await api.get<unknown>('/channel/search', { params, signal, ...responseLimits });
  return parseChannelSearchResponse(response.data);
}

export async function loadEnabledModels(signal?: AbortSignal): Promise<string[]> {
  const response = await api.get<unknown>('/channel/models_enabled', { signal, ...responseLimits });
  return parseModelListResponse(response.data);
}

export async function fetchChannelDetail(
  id: number,
  includeSensitive: boolean,
  signal?: AbortSignal,
): Promise<ChannelDetail> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.get<unknown>(`/channel/${safeID}`, { signal, ...responseLimits });
  return parseChannelDetailResponse(response.data, includeSensitive);
}

export async function testChannel(id: number, model?: string): Promise<ChannelTestResult> {
  const safeID = integer(id, 1, 2_147_483_647);
  const selectedModel = model === undefined ? '' : text(model.trim(), 255);
  const response = await api.get<unknown>(`/channel/test/${safeID}`, selectedModel
    ? { params: { model: selectedModel }, ...responseLimits }
    : responseLimits);
  return parseChannelTestResponse(response.data);
}

export async function testAllChannels(): Promise<QueuedChannelTask> {
  const response = await api.get<unknown>('/channel/test', responseLimits);
  return parseQueuedTaskResponse(response.data);
}

export async function setChannelStatus(id: number, status: typeof CHANNEL_ENABLED | typeof CHANNEL_MANUALLY_DISABLED): Promise<void> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.post<unknown>(`/channel/${safeID}/status`, { status }, responseLimits);
  parseMutationResponse(response.data);
}

export async function fetchChannelModels(id: number, signal?: AbortSignal): Promise<string[]> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.get<unknown>(`/channel/fetch_models/${safeID}`, { signal, ...responseLimits });
  return parseModelListResponse(response.data);
}

export async function discoverDraftModels(input: DraftModelDiscoveryInput, signal?: AbortSignal): Promise<string[]> {
  const body: Record<string, string | number> = {
    type: integer(input.type, 1, 10_000),
    base_url: text(input.baseUrl.trim(), 4_096),
  };
  if (input.key !== undefined) body.key = text(input.key, 512 * 1024);
  if (input.channelId !== undefined) body.channel_id = integer(input.channelId, 1, 2_147_483_647);
  if (input.advancedCustom !== undefined) {
    body.advanced_custom = requestJSONText(input.advancedCustom, 512 * 1024);
  }
  if (input.headerOverride !== undefined) {
    body.header_override = requestJSONText(input.headerOverride, 256 * 1024);
  }
  const response = await api.post<unknown>('/channel/fetch_models', body, { signal, ...responseLimits });
  return parseModelListResponse(response.data);
}

export async function updateChannel(input: ChannelUpdateInput): Promise<void> {
  const body: Record<string, string | number> = {
    id: integer(input.id, 1, 2_147_483_647),
    name: text(input.name.trim(), 191),
    models: text(input.models.trim(), 256 * 1024),
    group: text(input.group.trim(), 64),
    tag: text(input.tag.trim(), 64),
    remark: text(input.remark.trim(), 255),
  };
  if (!body.name) throw new ChannelContractError();
  if (input.priority !== undefined) {
    body.priority = integer(input.priority, Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER);
  }
  if (input.weight !== undefined) body.weight = integer(input.weight, 0, 4_294_967_295);
  if (input.testModel !== undefined) body.test_model = text(input.testModel.trim(), 255);
  if (input.autoBan !== undefined) body.auto_ban = binaryInteger(input.autoBan, 1);
  if (input.modelMapping !== undefined) body.model_mapping = modelMappingText(input.modelMapping);
  if (input.statusCodeMapping !== undefined) {
    body.status_code_mapping = statusCodeMappingText(input.statusCodeMapping);
  }
  if (input.type !== undefined) body.type = integer(input.type, 1, 10_000);
  if (input.baseUrl !== undefined) body.base_url = text(input.baseUrl.trim(), 4_096);
  if (input.organization !== undefined) {
    body.openai_organization = text(input.organization.trim(), 512);
  }
  if (input.other !== undefined) body.other = text(input.other.trim(), 256 * 1024);
  if (input.paramOverride !== undefined) {
    body.param_override = requestJSONText(input.paramOverride, 256 * 1024);
  }
  if (input.headerOverride !== undefined) {
    body.header_override = requestJSONText(input.headerOverride, 256 * 1024);
  }
  if (input.providerSettings !== undefined) {
    const providerType = input.type === undefined ? 0 : integer(input.type, 1, 10_000);
    if (providerType === 0) throw new ChannelContractError();
    Object.assign(body, buildProviderSettingJSON(input.providerSettings, providerType));
  }
  const response = await api.put<unknown>('/channel', body, responseLimits);
  parseMutationResponse(response.data);
}

export async function createChannel(input: ChannelCreateInput): Promise<void> {
  if (input.mode !== 'single' && input.mode !== 'batch' && input.mode !== 'multi_to_single') {
    throw new ChannelContractError();
  }
  if (input.multi_key_mode !== 'random' && input.multi_key_mode !== 'polling') {
    throw new ChannelContractError();
  }
  if (typeof input.batch_add_set_key_prefix_2_name !== 'boolean') throw new ChannelContractError();
  if (!isChannelProviderType(input.type)) throw new ChannelContractError();
  if (input.type === CHANNEL_TYPE_CODEX && input.mode !== 'single') throw new ChannelContractError();

  let key = text(input.key, 512 * 1024);
  if (input.mode !== 'single') {
    if (input.type === 41 && key.trimStart().startsWith('[')) {
      let values: unknown;
      try {
        values = JSON.parse(key);
      } catch {
        throw new ChannelContractError();
      }
      if (!Array.isArray(values) || values.length === 0 || values.length > MAX_CHANNEL_KEYS) throw new ChannelContractError();
    } else {
      const keys = key.split('\n').map((value) => value.trim()).filter(Boolean);
      if (keys.length === 0 || keys.length > MAX_CHANNEL_KEYS || new Set(keys).size !== keys.length) {
        throw new ChannelContractError();
      }
      key = keys.join('\n');
    }
  }
  const channel = {
    name: text(input.name.trim(), 191),
    type: integer(input.type, 1, 10_000),
    key,
    base_url: text(input.base_url.trim(), 4_096),
    models: text(input.models.trim(), 256 * 1024),
    group: text(input.group.trim(), 64),
  };
  if (!channel.name) throw new ChannelContractError();
  const body = {
    mode: input.mode,
    multi_key_mode: input.mode === 'multi_to_single' ? input.multi_key_mode : undefined,
    batch_add_set_key_prefix_2_name: input.mode === 'batch' && input.batch_add_set_key_prefix_2_name,
    channel,
  };
  const response = await api.post<unknown>('/channel', body, responseLimits);
  parseMutationResponse(response.data);
}

export async function deleteChannel(id: number): Promise<void> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.delete<unknown>(`/channel/${safeID}`, responseLimits);
  parseMutationResponse(response.data);
}

export async function setChannelsStatus(
  ids: number[],
  status: typeof CHANNEL_ENABLED | typeof CHANNEL_MANUALLY_DISABLED,
): Promise<number> {
  const response = await api.post<unknown>('/channel/status/batch', {
    ids: channelIDs(ids),
    status: status === CHANNEL_ENABLED ? CHANNEL_ENABLED : CHANNEL_MANUALLY_DISABLED,
  }, responseLimits);
  return parseCountMutationResponse(response.data);
}

export async function deleteChannels(ids: number[]): Promise<number> {
  const response = await api.post<unknown>('/channel/batch', { ids: channelIDs(ids) }, responseLimits);
  return parseCountMutationResponse(response.data);
}

export async function deleteDisabledChannels(): Promise<number> {
  const response = await api.delete<unknown>('/channel/disabled', responseLimits);
  const data = successfulEnvelope(response.data).data;
  return integer(data, 0, MAX_TOTAL);
}

export async function repairChannelAbilities(): Promise<ChannelRepairResult> {
  const response = await api.post<unknown>('/channel/fix', undefined, responseLimits);
  return parseChannelRepairResponse(response.data);
}

export async function setChannelsTag(ids: number[], tag: string): Promise<number> {
  const normalized = tagText(tag, true);
  const response = await api.post<unknown>('/channel/batch/tag', {
    ids: channelIDs(ids),
    tag: normalized || null,
  }, responseLimits);
  return parseCountMutationResponse(response.data);
}

export async function setTagChannelsStatus(tag: string, enabled: boolean): Promise<void> {
  const response = await api.post<unknown>(`/channel/tag/${enabled ? 'enabled' : 'disabled'}`, {
    tag: tagText(tag, false),
  }, responseLimits);
  parseMutationResponse(response.data);
}

export async function loadTagModels(tag: string, signal?: AbortSignal): Promise<string> {
  const response = await api.get<unknown>('/channel/tag/models', {
    params: { tag: tagText(tag, false) },
    signal,
    ...responseLimits,
  });
  return parseTextDataResponse(response.data);
}

export async function updateTagChannels(input: TagUpdateInput): Promise<void> {
  const body: Record<string, string> = { tag: tagText(input.tag, false) };
  if (input.newTag !== undefined) body.new_tag = tagText(input.newTag, true);
  if (input.models !== undefined) body.models = text(input.models.trim(), 256 * 1024);
  if (input.groups !== undefined) body.groups = text(input.groups.trim(), 64);
  if (input.modelMapping !== undefined) {
    const mapping = text(input.modelMapping.trim(), 256 * 1024);
    if (mapping) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(mapping);
      } catch {
        throw new ChannelContractError();
      }
      record(parsed);
    }
    body.model_mapping = mapping;
  }
  const numericBody = body as Record<string, string | number>;
  if (input.priority !== undefined) numericBody.priority = integer(input.priority, Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER);
  if (input.weight !== undefined) numericBody.weight = integer(input.weight, 0, 4_294_967_295);
  for (const [field, value] of [
    ['param_override', input.paramOverride],
    ['header_override', input.headerOverride],
  ] as const) {
    if (value === undefined) continue;
    const json = text(value.trim(), 256 * 1024);
    if (json) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(json);
      } catch {
        throw new ChannelContractError();
      }
      record(parsed);
    }
    numericBody[field] = json;
  }
  if (Object.keys(body).length === 1) throw new ChannelContractError();
  const response = await api.put<unknown>('/channel/tag', numericBody, responseLimits);
  parseMutationResponse(response.data);
}

export async function copyChannel(id: number, suffix: string, resetBalance: boolean): Promise<number> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.post<unknown>(`/channel/copy/${safeID}`, null, {
    params: { suffix: text(suffix, 64), reset_balance: Boolean(resetBalance) },
    ...responseLimits,
  });
  return parseCopyResponse(response.data);
}

export async function refreshChannelBalance(id: number): Promise<number> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.get<unknown>(`/channel/update_balance/${safeID}`, responseLimits);
  return parseBalanceResponse(response.data);
}

export async function refreshAllChannelBalances(): Promise<void> {
  // The backend intentionally walks providers sequentially. Disable the
  // shared 30-second client timeout so the browser does not report failure
  // while the server continues mutating channel balances and statuses.
  const response = await api.get<unknown>('/channel/update_balance', { timeout: 0, ...responseLimits });
  parseMutationResponse(response.data);
}

export async function loadMultiKeyPage(
  channelId: number,
  page: number,
  pageSize: number,
  status: '' | 1 | 2 | 3,
  signal?: AbortSignal,
): Promise<MultiKeyPage> {
  const body: Record<string, string | number> = {
    channel_id: integer(channelId, 1, 2_147_483_647),
    action: 'get_key_status',
    page: integer(page, 1, 1_000_000),
    page_size: integer(pageSize, 1, 100),
  };
  if (status !== '') body.status = integer(status, 1, 3);
  const response = await api.post<unknown>('/channel/multi_key/manage', body, {
    signal,
    ...responseLimits,
  });
  return parseMultiKeyPageResponse(response.data);
}

export async function manageMultiKey(
  channelId: number,
  action: MultiKeyAction,
  keyIndex?: number,
): Promise<void> {
  const body: Record<string, string | number> = {
    channel_id: integer(channelId, 1, 2_147_483_647),
    action,
  };
  if (action === 'disable_key' || action === 'enable_key' || action === 'delete_key') {
    body.key_index = integer(keyIndex, 0, MAX_TOTAL);
  } else if (keyIndex !== undefined) {
    throw new ChannelContractError();
  }
  const response = await api.post<unknown>('/channel/multi_key/manage', body, responseLimits);
  parseMutationResponse(response.data);
}

export async function detectUpstreamUpdates(id: number, signal?: AbortSignal): Promise<UpstreamDetection> {
  const response = await api.post<unknown>('/channel/upstream_updates/detect', {
    id: integer(id, 1, 2_147_483_647),
  }, { signal, ...responseLimits });
  return parseUpstreamDetectionResponse(response.data);
}

export async function applyUpstreamUpdates(
  id: number,
  selectedAddModels: string[],
  selectedRemoveModels: string[],
  ignoredModels: string[],
): Promise<UpstreamApplyResult> {
  const response = await api.post<unknown>('/channel/upstream_updates/apply', {
    id: integer(id, 1, 2_147_483_647),
    add_models: requestModels(selectedAddModels),
    remove_models: requestModels(selectedRemoveModels),
    ignore_models: requestModels(ignoredModels),
  }, responseLimits);
  return parseUpstreamApplyResponse(response.data);
}

export async function detectAllUpstreamUpdates(): Promise<QueuedChannelTask> {
  const response = await api.post<unknown>('/channel/upstream_updates/detect_all', {}, responseLimits);
  return parseQueuedTaskResponse(response.data);
}

export async function applyAllUpstreamUpdates(): Promise<UpstreamApplyAllResult> {
  const response = await api.post<unknown>('/channel/upstream_updates/apply_all', {}, responseLimits);
  return parseUpstreamApplyAllResponse(response.data);
}

export async function loadCodexUsage(id: number, signal?: AbortSignal): Promise<CodexDocument> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.get<unknown>(`/channel/${safeID}/codex/usage`, { signal, ...responseLimits });
  return parseCodexDocumentResponse(response.data);
}

export async function loadCodexResetCredits(id: number, signal?: AbortSignal): Promise<CodexDocument> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.get<unknown>(`/channel/${safeID}/codex/usage/reset-credits`, { signal, ...responseLimits });
  return parseCodexDocumentResponse(response.data);
}

export async function resetCodexUsage(id: number): Promise<CodexDocument> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.post<unknown>(`/channel/${safeID}/codex/usage/reset`, {}, responseLimits);
  return parseCodexDocumentResponse(response.data);
}

export async function refreshCodexCredential(id: number): Promise<void> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.post<unknown>(`/channel/${safeID}/codex/refresh`, {}, responseLimits);
  parseCodexRefreshResponse(response.data);
}

export async function loadOllamaVersion(id: number, signal?: AbortSignal): Promise<string> {
  const safeID = integer(id, 1, 2_147_483_647);
  const response = await api.get<unknown>(`/channel/ollama/version/${safeID}`, { signal, ...responseLimits });
  return parseOllamaVersionResponse(response.data);
}

export async function pullOllamaModel(channelId: number, modelName: string): Promise<void> {
  const normalizedModel = text(modelName.trim(), 512);
  if (!normalizedModel) throw new ChannelContractError();
  const response = await api.post<unknown>('/channel/ollama/pull', {
    channel_id: integer(channelId, 1, 2_147_483_647),
    model_name: normalizedModel,
  }, responseLimits);
  parseMutationResponse(response.data);
}

function parseOllamaProgressFrame(value: unknown): OllamaPullProgress {
  const frame = record(value);
  if (frame.error !== undefined && text(frame.error, 512).trim()) throw new ChannelContractError();
  return {
    status: optionalText(frame.status ?? frame.message, 512),
    completed: frame.completed === undefined ? 0 : integer(frame.completed, 0, Number.MAX_SAFE_INTEGER),
    total: frame.total === undefined ? 0 : integer(frame.total, 0, Number.MAX_SAFE_INTEGER),
  };
}

export async function pullOllamaModelStream(
  channelId: number,
  modelName: string,
  onProgress: (progress: OllamaPullProgress) => void,
  signal?: AbortSignal,
): Promise<void> {
  const normalizedModel = text(modelName.trim(), 512);
  if (!normalizedModel) throw new ChannelContractError();
  const response = await fetch('/api/channel/ollama/pull/stream', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify({
      channel_id: integer(channelId, 1, 2_147_483_647),
      model_name: normalizedModel,
    }),
    signal,
  });
  if (!response.ok || !response.body) throw new ChannelContractError();
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let bytes = 0;
  let doneEvent = false;
  while (true) {
    const chunk = await reader.read();
    if (chunk.done) break;
    bytes += chunk.value.byteLength;
    if (bytes > 4 * 1024 * 1024) throw new ChannelContractError();
    buffer += decoder.decode(chunk.value, { stream: true });
    if (buffer.length > 128 * 1024 && !/\r?\n\r?\n/.test(buffer)) throw new ChannelContractError();
    const events = buffer.split(/\r?\n\r?\n/);
    buffer = events.pop() ?? '';
    for (const event of events) {
      const data = event.split(/\r?\n/)
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice(5).trimStart())
        .join('\n');
      if (!data) continue;
      if (data === '[DONE]') {
        doneEvent = true;
        continue;
      }
      let payload: unknown;
      try {
        payload = JSON.parse(data);
      } catch {
        throw new ChannelContractError();
      }
      onProgress(parseOllamaProgressFrame(payload));
    }
  }
  buffer += decoder.decode();
  if (buffer.trim()) {
    const data = buffer.trim().startsWith('data:') ? buffer.trim().slice(5).trimStart() : '';
    if (data === '[DONE]') doneEvent = true;
    else if (data) {
      let payload: unknown;
      try {
        payload = JSON.parse(data);
      } catch {
        throw new ChannelContractError();
      }
      onProgress(parseOllamaProgressFrame(payload));
    }
  }
  if (!doneEvent) throw new ChannelContractError();
}

export async function deleteOllamaModel(channelId: number, modelName: string): Promise<void> {
  const normalizedModel = text(modelName.trim(), 512);
  if (!normalizedModel) throw new ChannelContractError();
  const response = await api.delete<unknown>('/channel/ollama/delete', {
    data: {
      channel_id: integer(channelId, 1, 2_147_483_647),
      model_name: normalizedModel,
    },
    ...responseLimits,
  });
  parseMutationResponse(response.data);
}
