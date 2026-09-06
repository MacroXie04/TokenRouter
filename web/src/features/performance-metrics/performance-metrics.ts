export const PERFORMANCE_SERIES_SCHEMA = 'dbcd0a3c01b55203';
export const MAX_PERFORMANCE_HOURS = 24 * 30;
export const MAX_PERFORMANCE_MODEL_NAME_BYTES = 512;

const MAX_RESPONSE_BYTES = 4 * 1024 * 1024;
const MAX_MODELS = 10_000;
const MAX_GROUPS = 64;
const MAX_GROUP_NAME_BYTES = 128;
const MAX_TOTAL_POINTS = 50_000;
const MAX_UNIX_SECONDS = 4_102_444_800;

type UnknownRecord = Record<string, unknown>;

export interface PerformanceSeriesPoint {
  ts: number;
  avg_ttft_ms: number;
  avg_latency_ms: number;
  success_rate: number;
  avg_tps: number;
}

export interface PerformanceGroup {
  group: string;
  avg_ttft_ms: number;
  avg_latency_ms: number;
  success_rate: number;
  avg_tps: number;
  series: PerformanceSeriesPoint[];
}

export interface PerformanceMetrics {
  model_name: string;
  series_schema: string;
  groups: PerformanceGroup[];
}

export interface PerformanceModelSummary {
  model_name: string;
  avg_latency_ms: number;
  success_rate: number;
  avg_tps: number;
  recent_success_rates: number[];
}

export interface PerformanceSummary {
  models: PerformanceModelSummary[];
}

export class PerformanceContractError extends Error {
  constructor() {
    super('Invalid performance metrics API response');
    this.name = 'PerformanceContractError';
  }
}

function fail(): never {
  throw new PerformanceContractError();
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    fail();
  }
  if (encoded === undefined || new TextEncoder().encode(encoded).byteLength > MAX_RESPONSE_BYTES) fail();
}

function array(value: unknown, maximum: number): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) fail();
  return value;
}

function identifier(value: unknown, maximumBytes: number): string {
  if (typeof value !== 'string' || value !== value.trim() || !value
    || new TextEncoder().encode(value).byteLength > maximumBytes) fail();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) fail();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) fail();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) fail();
  }
  return value;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) fail();
  return value as number;
}

function decimal(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) fail();
  return value;
}

function unique(values: string[]): void {
  if (new Set(values).size !== values.length) fail();
}

function successfulData(value: unknown): UnknownRecord {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true) fail();
  return record(envelope.data);
}

function parseSeriesPoint(value: unknown): PerformanceSeriesPoint {
  const point = record(value);
  return {
    ts: integer(point.ts, 0, MAX_UNIX_SECONDS),
    avg_ttft_ms: integer(point.avg_ttft_ms, 0, Number.MAX_SAFE_INTEGER),
    avg_latency_ms: integer(point.avg_latency_ms, 0, Number.MAX_SAFE_INTEGER),
    success_rate: decimal(point.success_rate, 0, 100),
    avg_tps: decimal(point.avg_tps, 0, 1_000_000_000),
  };
}

export function parsePerformanceMetricsResponse(value: unknown, expectedModel: string): PerformanceMetrics {
  const model = validatePerformanceIdentifier(expectedModel, MAX_PERFORMANCE_MODEL_NAME_BYTES);
  const data = successfulData(value);
  const modelName = identifier(data.model_name, MAX_PERFORMANCE_MODEL_NAME_BYTES);
  if (modelName !== model || data.series_schema !== PERFORMANCE_SERIES_SCHEMA) fail();

  let totalPoints = 0;
  const groups = array(data.groups, MAX_GROUPS).map((rawGroup) => {
    const group = record(rawGroup);
    const rawSeries = array(group.series, MAX_TOTAL_POINTS);
    totalPoints += rawSeries.length;
    if (totalPoints > MAX_TOTAL_POINTS) fail();
    const series = rawSeries.map(parseSeriesPoint);
    for (let index = 1; index < series.length; index += 1) {
      if (series[index].ts <= series[index - 1].ts) fail();
    }
    return {
      group: identifier(group.group, MAX_GROUP_NAME_BYTES),
      avg_ttft_ms: integer(group.avg_ttft_ms, 0, Number.MAX_SAFE_INTEGER),
      avg_latency_ms: integer(group.avg_latency_ms, 0, Number.MAX_SAFE_INTEGER),
      success_rate: decimal(group.success_rate, 0, 100),
      avg_tps: decimal(group.avg_tps, 0, 1_000_000_000),
      series,
    };
  });
  unique(groups.map((group) => group.group));
  return { model_name: modelName, series_schema: PERFORMANCE_SERIES_SCHEMA, groups };
}

export function parsePerformanceSummaryResponse(value: unknown): PerformanceSummary {
  const data = successfulData(value);
  const models = array(data.models, MAX_MODELS).map((rawModel) => {
    const model = record(rawModel);
    const recent = model.recent_success_rates === undefined
      ? []
      : array(model.recent_success_rates, 3).map((rate) => decimal(rate, 0, 100));
    return {
      model_name: identifier(model.model_name, MAX_PERFORMANCE_MODEL_NAME_BYTES),
      avg_latency_ms: integer(model.avg_latency_ms, 0, Number.MAX_SAFE_INTEGER),
      success_rate: decimal(model.success_rate, 0, 100),
      avg_tps: decimal(model.avg_tps, 0, 1_000_000_000),
      recent_success_rates: recent,
    };
  });
  unique(models.map((model) => model.model_name));
  return { models };
}

export function validatePerformanceIdentifier(value: string, maximumBytes: number): string {
  return identifier(value, maximumBytes);
}

export function validatePerformanceHours(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1 || value > MAX_PERFORMANCE_HOURS) {
    throw new PerformanceContractError();
  }
  return value;
}
