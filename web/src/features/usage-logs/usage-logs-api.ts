import { api } from '../../shared/api/client';

export const USAGE_LOG_PAGE_SIZES = [10, 20, 50, 100] as const;
export type UsageLogPageSize = (typeof USAGE_LOG_PAGE_SIZES)[number];
export type UsageLogSection = 'common' | 'drawing' | 'task';

const MAX_RESPONSE_BYTES = 1024 * 1024;
const MAX_ITEMS = 100;
const MAX_TOTAL = 1_000_000_000;
const MAX_PAGE = 1_000_000;
const MAX_UNIX_SECONDS = 4_102_444_800; // 2100-01-01T00:00:00Z
const MAX_UNIX_MILLISECONDS = MAX_UNIX_SECONDS * 1_000;
const PUBLIC_VIDEO_TASK_ID = /^task_[A-Za-z0-9]{32}$/u;
const CONTENT_GATEWAY_TASK_PLATFORMS = new Set([
  '1', // OpenAI-compatible video
  '17', // Alibaba Wan
  '24', // Gemini Veo
  '35', // Hailuo
  '41', // Vertex Veo
  '45', // VolcEngine video
  '52', // Vidu
  '54', // Doubao video
  '55', // Sora
]);

type UnknownRecord = Record<string, unknown>;

export interface UsageLogQuery {
  page: number;
  pageSize: UsageLogPageSize;
  type: number;
  model: string;
  token: string;
  group: string;
  username: string;
  channel: string;
  requestId: string;
  upstreamRequestId: string;
  identifier: string;
  platform: string;
  status: string;
  action: string;
  startTimestamp?: number;
  endTimestamp?: number;
}

export interface CommonUsageLog {
  kind: 'common';
  id: number;
  userId: number;
  createdAt: number;
  type: number;
  username: string;
  tokenName: string;
  modelName: string;
  quota: number;
  promptTokens: number;
  completionTokens: number;
  useTime: number;
  streamed: boolean;
  channelId: number;
  channelName: string;
  group: string;
  requestId: string;
  upstreamRequestId: string;
  content: string;
  ip: string;
  billing: UsageBillingDetails | null;
}

export interface UsageBillingDetails {
  upstreamModelName: string;
  firstResponseTime: number;
  cacheTokens: number;
  audioInputTokens: number;
  audioOutputTokens: number;
  imageOutputTokens: number;
  modelRatio: number | null;
  completionRatio: number | null;
  groupRatio: number | null;
  userGroupRatio: number | null;
  billingMode: string;
  matchedTier: string;
  billingSource: string;
}

export interface DrawingUsageLog {
  kind: 'drawing';
  id: number;
  userId: number;
  channelId: number;
  drawingId: string;
  action: string;
  submitTime: number;
  status: string;
  progress: string;
  quota: number;
  contentURL: string | null;
  prompt: string;
  promptEnglish: string;
  failReason: string;
  startTime: number;
  finishTime: number;
}

export interface TaskUsageLog {
  kind: 'task';
  id: number;
  userId: number;
  username: string;
  channelId: number;
  taskId: string;
  platform: string;
  action: string;
  submitTime: number;
  status: string;
  progress: string;
  quota: number;
  contentURL: string | null;
  group: string;
  input: string;
  upstreamModelName: string;
  originModelName: string;
  failReason: string;
  startTime: number;
  finishTime: number;
}

export type UsageLogItem = CommonUsageLog | DrawingUsageLog | TaskUsageLog;

export interface UsageLogStats {
  quota: number;
  rpm: number;
  tpm: number;
}

export interface UsageLogResult {
  items: UsageLogItem[];
  page: number;
  pageSize: UsageLogPageSize;
  total: number;
  stats: UsageLogStats | null;
}

export class UsageLogContractError extends Error {
  constructor() {
    super('Invalid usage log API response');
    this.name = 'UsageLogContractError';
  }
}

export function emptyUsageLogQuery(): UsageLogQuery {
  return {
    page: 1,
    pageSize: 20,
    type: 0,
    model: '',
    token: '',
    group: '',
    username: '',
    channel: '',
    requestId: '',
    upstreamRequestId: '',
    identifier: '',
    platform: '',
    status: '',
    action: '',
  };
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new UsageLogContractError();
  }
  return value as UnknownRecord;
}

function payloadBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    throw new UsageLogContractError();
  }
}

function successfulEnvelope(value: unknown): UnknownRecord {
  if (payloadBytes(value) > MAX_RESPONSE_BYTES) throw new UsageLogContractError();
  const envelope = record(value);
  if (envelope.success !== true || !('data' in envelope)) throw new UsageLogContractError();
  return envelope;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new UsageLogContractError();
  }
  return value as number;
}

function optionalInteger(value: unknown, minimum: number, maximum: number): number {
  if (value === undefined || value === null) return 0;
  return integer(value, minimum, maximum);
}

function finiteNumber(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new UsageLogContractError();
  }
  return value;
}

function safeText(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length > maximum) {
    throw new UsageLogContractError();
  }
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (
      code <= 0x1f
      || (code >= 0x7f && code <= 0x9f)
      || code === 0x061c
      || code === 0x200e
      || code === 0x200f
      || (code >= 0x202a && code <= 0x202e)
      || (code >= 0x2066 && code <= 0x2069)
    ) {
      throw new UsageLogContractError();
    } else if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) throw new UsageLogContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new UsageLogContractError();
    }
  }
  return value;
}

function optionalText(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : safeText(value, maximum);
}

function safeMultilineText(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length > maximum) throw new UsageLogContractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (
      (code <= 0x1f && code !== 0x09 && code !== 0x0a && code !== 0x0d)
      || (code >= 0x7f && code <= 0x9f)
      || code === 0x061c
      || code === 0x200e
      || code === 0x200f
      || (code >= 0x202a && code <= 0x202e)
      || (code >= 0x2066 && code <= 0x2069)
    ) {
      throw new UsageLogContractError();
    } else if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) throw new UsageLogContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new UsageLogContractError();
    }
  }
  return value;
}

function optionalMultilineText(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : safeMultilineText(value, maximum);
}

function optionalFinite(value: unknown, minimum: number, maximum: number): number | null {
  if (value === undefined || value === null) return null;
  return finiteNumber(value, minimum, maximum);
}

function parseBillingDetails(value: unknown): UsageBillingDetails | null {
  if (value === undefined || value === null || value === '') return null;
  if (typeof value !== 'string' || value.length > 128 * 1024) throw new UsageLogContractError();
  let raw: UnknownRecord;
  try {
    raw = record(JSON.parse(value) as unknown);
  } catch {
    throw new UsageLogContractError();
  }
  return {
    upstreamModelName: optionalText(raw.upstream_model_name, 255),
    firstResponseTime: optionalInteger(raw.frt, 0, 2_147_483_647),
    cacheTokens: optionalInteger(raw.cache_tokens, 0, 2_147_483_647),
    audioInputTokens: optionalInteger(raw.audio_input, 0, 2_147_483_647),
    audioOutputTokens: optionalInteger(raw.audio_output, 0, 2_147_483_647),
    imageOutputTokens: optionalInteger(raw.image_output, 0, 2_147_483_647),
    modelRatio: optionalFinite(raw.model_ratio, 0, Number.MAX_SAFE_INTEGER),
    completionRatio: optionalFinite(raw.completion_ratio, 0, Number.MAX_SAFE_INTEGER),
    groupRatio: optionalFinite(raw.group_ratio, 0, Number.MAX_SAFE_INTEGER),
    userGroupRatio: optionalFinite(raw.user_group_ratio, -1, Number.MAX_SAFE_INTEGER),
    billingMode: optionalText(raw.billing_mode, 64),
    matchedTier: optionalText(raw.matched_tier, 128),
    billingSource: optionalText(raw.billing_source, 64),
  };
}

function parseTaskProperties(value: unknown): Pick<TaskUsageLog, 'input' | 'upstreamModelName' | 'originModelName'> {
  const raw = value === undefined || value === null ? {} : record(value);
  return {
    input: optionalMultilineText(raw.input, 32_768),
    upstreamModelName: optionalText(raw.upstream_model_name, 255),
    originModelName: optionalText(raw.origin_model_name, 255),
  };
}

function safeRemoteResult(value: unknown): boolean {
  if (value === undefined || value === null || value === '') return false;
  let parsed: URL;
  try {
    const raw = safeText(value, 8_192);
    if (raw.trim() !== raw || raw.includes('\\')) return false;
    parsed = new URL(raw);
  } catch {
    return false;
  }
  return (parsed.protocol === 'https:' || parsed.protocol === 'http:')
    && Boolean(parsed.hostname)
    && parsed.username === ''
    && parsed.password === ''
    && parsed.hash === '';
}

function hasInlineTaskOutput(value: unknown): boolean {
  if (value === undefined || value === null) return false;
  if (typeof value !== 'object' || Array.isArray(value)) return false;
  const data = value as UnknownRecord;
  if (data.has_inline_video === true && data.inline_mime_type === 'video/mp4') return true;
  return safeRemoteResult(data.result_url);
}

export function drawingGatewayContentURL(drawingId: string): string {
  return `/mj/image/${encodeURIComponent(safeText(drawingId, 191))}`;
}

export function taskGatewayContentURL(taskId: string): string {
  const publicTaskId = safeText(taskId, 191);
  if (!PUBLIC_VIDEO_TASK_ID.test(publicTaskId)) throw new UsageLogContractError();
  return `/v1/videos/${encodeURIComponent(publicTaskId)}/content`;
}

function parseCommonLog(value: unknown): CommonUsageLog {
  const item = record(value);
  return {
    kind: 'common',
    id: integer(item.id, 1, Number.MAX_SAFE_INTEGER),
    userId: optionalInteger(item.user_id, 0, 2_147_483_647),
    createdAt: integer(item.created_at, 0, MAX_UNIX_SECONDS),
    type: integer(item.type, 0, 7),
    username: optionalText(item.username, 64),
    tokenName: optionalText(item.token_name, 64),
    modelName: optionalText(item.model_name, 255),
    quota: optionalInteger(item.quota, -2_147_483_648, 2_147_483_647),
    promptTokens: optionalInteger(item.prompt_tokens, 0, 2_147_483_647),
    completionTokens: optionalInteger(item.completion_tokens, 0, 2_147_483_647),
    useTime: optionalInteger(item.use_time, 0, 2_147_483_647),
    streamed: item.is_stream === undefined || item.is_stream === null
      ? false
      : item.is_stream === true
        ? true
        : item.is_stream === false
          ? false
          : (() => { throw new UsageLogContractError(); })(),
    channelId: optionalInteger(item.channel_id ?? item.channel, 0, 2_147_483_647),
    channelName: optionalText(item.channel_name, 255),
    group: optionalText(item.group, 512),
    requestId: optionalText(item.request_id, 128),
    upstreamRequestId: optionalText(item.upstream_request_id, 128),
    content: optionalMultilineText(item.content, 16_384),
    ip: optionalText(item.ip, 64),
    billing: parseBillingDetails(item.other),
  };
}

function parseDrawingLog(value: unknown): DrawingUsageLog {
  const item = record(value);
  const drawingId = safeText(item.mj_id, 191);
  const status = safeText(item.status, 20);
  const hasResult = safeRemoteResult(item.image_url);
  const hasGatewayPath = drawingId !== '' && !drawingId.includes('/') && !drawingId.includes('\\');
  return {
    kind: 'drawing',
    id: integer(item.id, 1, Number.MAX_SAFE_INTEGER),
    userId: integer(item.user_id, 1, 2_147_483_647),
    channelId: optionalInteger(item.channel_id, 0, 2_147_483_647),
    drawingId,
    action: safeText(item.action, 40),
    submitTime: integer(item.submit_time, 0, MAX_UNIX_MILLISECONDS),
    status,
    progress: optionalText(item.progress, 30),
    quota: optionalInteger(item.quota, -2_147_483_648, 2_147_483_647),
    contentURL: status === 'SUCCESS' && hasResult && hasGatewayPath ? drawingGatewayContentURL(drawingId) : null,
    prompt: optionalMultilineText(item.prompt, 32_768),
    promptEnglish: optionalMultilineText(item.prompt_en, 32_768),
    failReason: optionalMultilineText(item.fail_reason, 16_384),
    startTime: optionalInteger(item.start_time, 0, MAX_UNIX_MILLISECONDS),
    finishTime: optionalInteger(item.finish_time, 0, MAX_UNIX_MILLISECONDS),
  };
}

function parseTaskLog(value: unknown): TaskUsageLog {
  const item = record(value);
  const properties = parseTaskProperties(item.properties);
  const taskId = safeText(item.task_id, 191);
  const status = safeText(item.status, 20);
  const platform = safeText(item.platform, 30);
  const hasResult = safeRemoteResult(item.result_url) || hasInlineTaskOutput(item.data);
  const hasContentGateway = PUBLIC_VIDEO_TASK_ID.test(taskId) && CONTENT_GATEWAY_TASK_PLATFORMS.has(platform);
  return {
    kind: 'task',
    id: integer(item.id, 1, Number.MAX_SAFE_INTEGER),
    userId: integer(item.user_id, 1, 2_147_483_647),
    username: optionalText(item.username, 64),
    channelId: optionalInteger(item.channel_id, 0, 2_147_483_647),
    taskId,
    platform,
    action: safeText(item.action, 40),
    submitTime: integer(item.submit_time, 0, MAX_UNIX_SECONDS),
    status,
    progress: optionalText(item.progress, 30),
    quota: optionalInteger(item.quota, -2_147_483_648, 2_147_483_647),
    contentURL: status === 'SUCCESS' && hasResult && hasContentGateway ? taskGatewayContentURL(taskId) : null,
    group: optionalText(item.group, 50),
    ...properties,
    failReason: optionalMultilineText(item.fail_reason, 16_384),
    startTime: optionalInteger(item.start_time, 0, MAX_UNIX_SECONDS),
    finishTime: optionalInteger(item.finish_time, 0, MAX_UNIX_SECONDS),
  };
}

function parsePage(
  value: unknown,
  section: UsageLogSection,
  expectedPage: number,
  expectedPageSize: UsageLogPageSize,
): Omit<UsageLogResult, 'stats'> {
  const data = record(successfulEnvelope(value).data);
  if (!Array.isArray(data.items) || data.items.length > MAX_ITEMS || data.items.length > expectedPageSize) {
    throw new UsageLogContractError();
  }
  const page = integer(data.page, 1, MAX_PAGE);
  const pageSize = integer(data.page_size, 1, MAX_ITEMS);
  if (page !== expectedPage || pageSize !== expectedPageSize || !USAGE_LOG_PAGE_SIZES.includes(pageSize as UsageLogPageSize)) {
    throw new UsageLogContractError();
  }
  let items: UsageLogItem[];
  if (section === 'common') items = data.items.map(parseCommonLog);
  else if (section === 'drawing') items = data.items.map(parseDrawingLog);
  else items = data.items.map(parseTaskLog);
  return {
    items,
    page,
    pageSize: pageSize as UsageLogPageSize,
    total: integer(data.total, 0, MAX_TOTAL),
  };
}

export function parseUsageLogStats(value: unknown): UsageLogStats {
  const data = record(successfulEnvelope(value).data);
  return {
    quota: finiteNumber(data.quota, -Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER),
    rpm: finiteNumber(data.rpm, 0, Number.MAX_SAFE_INTEGER),
    tpm: finiteNumber(data.tpm, 0, Number.MAX_SAFE_INTEGER),
  };
}

function boundedFilter(value: string, maximum: number): string {
  return safeText(value.trim(), maximum);
}

function channelFilter(value: string): number | undefined {
  const normalized = value.trim();
  if (!normalized) return undefined;
  if (!/^\d{1,10}$/u.test(normalized)) throw new UsageLogContractError();
  return integer(Number(normalized), 1, 2_147_483_647);
}

function addTimeFilters(params: Record<string, string | number>, query: UsageLogQuery, section: UsageLogSection): void {
  const maximum = section === 'drawing' ? MAX_UNIX_MILLISECONDS : MAX_UNIX_SECONDS;
  if (query.startTimestamp !== undefined) params.start_timestamp = integer(query.startTimestamp, 0, maximum);
  if (query.endTimestamp !== undefined) params.end_timestamp = integer(query.endTimestamp, 0, maximum);
  if (query.startTimestamp !== undefined && query.endTimestamp !== undefined && query.startTimestamp > query.endTimestamp) {
    throw new UsageLogContractError();
  }
}

export function buildUsageLogParams(
  section: UsageLogSection,
  isAdmin: boolean,
  query: UsageLogQuery,
): Record<string, string | number> {
  const params: Record<string, string | number> = {
    p: integer(query.page, 1, MAX_PAGE),
    page_size: integer(query.pageSize, 1, MAX_ITEMS),
  };
  addTimeFilters(params, query, section);
  if (section === 'common') {
    params.type = integer(query.type, 0, 7);
    const fields = [
      ['model_name', query.model, 255],
      ['token_name', query.token, 64],
      ['group', query.group, 512],
      ['request_id', query.requestId, 128],
      ['upstream_request_id', query.upstreamRequestId, 128],
    ] as const;
    for (const [key, raw, maximum] of fields) {
      const value = boundedFilter(raw, maximum);
      if (value) params[key] = value;
    }
    if (isAdmin) {
      const username = boundedFilter(query.username, 64);
      const channel = channelFilter(query.channel);
      if (username) params.username = username;
      if (channel !== undefined) params.channel = channel;
    }
  } else {
    const identifier = boundedFilter(query.identifier, 191);
    if (identifier) params[section === 'drawing' ? 'mj_id' : 'task_id'] = identifier;
    if (isAdmin) {
      const channel = channelFilter(query.channel);
      if (channel !== undefined) params.channel_id = channel;
    }
    if (section === 'task') {
      const fields = [
        ['platform', query.platform, 30],
        ['status', query.status, 20],
        ['action', query.action, 40],
      ] as const;
      for (const [key, raw, maximum] of fields) {
        const value = boundedFilter(raw, maximum);
        if (value) params[key] = value;
      }
    }
  }
  return params;
}

const responseLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

function listEndpoint(section: UsageLogSection, isAdmin: boolean): string {
  if (section === 'common') return isAdmin ? '/log' : '/log/self';
  if (section === 'drawing') return isAdmin ? '/mj/' : '/mj/self';
  return isAdmin ? '/task/' : '/task/self';
}

export async function loadUsageLogs(
  section: UsageLogSection,
  isAdmin: boolean,
  query: UsageLogQuery,
  signal?: AbortSignal,
): Promise<UsageLogResult> {
  const params = buildUsageLogParams(section, isAdmin, query);
  const listRequest = api.get<unknown>(listEndpoint(section, isAdmin), {
    params,
    signal,
    ...responseLimits,
  });
  if (section !== 'common') {
    const response = await listRequest;
    return { ...parsePage(response.data, section, query.page, query.pageSize), stats: null };
  }
  const statParams = { ...params };
  delete statParams.p;
  delete statParams.page_size;
  delete statParams.request_id;
  delete statParams.upstream_request_id;
  const statEndpoint = isAdmin ? '/log/stat' : '/log/self/stat';
  const [listResponse, statResponse] = await Promise.all([
    listRequest,
    api.get<unknown>(statEndpoint, { params: statParams, signal, ...responseLimits }),
  ]);
  return {
    ...parsePage(listResponse.data, section, query.page, query.pageSize),
    stats: parseUsageLogStats(statResponse.data),
  };
}
