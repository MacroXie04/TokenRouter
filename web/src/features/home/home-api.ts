import { api } from '../../api';
import { parseRankingRows, type RankingRow } from '../rankings/rankings';

export const MAX_PUBLIC_CONTENT_CHARACTERS = 1_000_000;
const PUBLIC_CONTENT_RESPONSE_BYTES = 6 * 1024 * 1024;
const SUMMARY_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_PERFORMANCE_MODELS = 10_000;
const MAX_MODEL_NAME = 512;

type UnknownRecord = Record<string, unknown>;
export type PublicContentPath = '/notice' | '/home_page_content' | '/about' | '/user-agreement' | '/privacy-policy';

export interface PerformanceSummary {
  model_name: string;
  avg_latency_ms: number;
  success_rate: number;
  avg_tps: number;
}

export class PublicHomeContractError extends Error {
  constructor() {
    super('Invalid public home API response');
    this.name = 'PublicHomeContractError';
  }
}

function fail(): never {
  throw new PublicHomeContractError();
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown, maximum: number): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    fail();
  }
  if (encoded === undefined || new TextEncoder().encode(encoded).byteLength > maximum) fail();
}

function envelopeData(value: unknown, maximum: number): unknown {
  boundedPayload(value, maximum);
  const envelope = record(value);
  if (envelope.success !== true) fail();
  return envelope.data;
}

function finite(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) fail();
  return value;
}

export function parsePublicContentResponse(value: unknown): string {
  const data = envelopeData(value, PUBLIC_CONTENT_RESPONSE_BYTES);
  if (typeof data !== 'string' || data.length > MAX_PUBLIC_CONTENT_CHARACTERS) fail();
  return data.trim();
}

export function parsePerformanceSummaryResponse(value: unknown): PerformanceSummary[] {
  const data = record(envelopeData(value, SUMMARY_RESPONSE_BYTES));
  if (!Array.isArray(data.models) || data.models.length > MAX_PERFORMANCE_MODELS) fail();
  const seen = new Set<string>();
  return data.models.map((raw) => {
    const item = record(raw);
    if (typeof item.model_name !== 'string' || item.model_name.length > MAX_MODEL_NAME) fail();
    const modelName = item.model_name.trim();
    if (!modelName || modelName !== item.model_name || seen.has(modelName)) fail();
    seen.add(modelName);
    if (item.recent_success_rates !== undefined) {
      if (!Array.isArray(item.recent_success_rates) || item.recent_success_rates.length > 3) fail();
      item.recent_success_rates.forEach((rate) => finite(rate, 0, 100));
    }
    return {
      model_name: modelName,
      avg_latency_ms: finite(item.avg_latency_ms, 0, Number.MAX_SAFE_INTEGER),
      success_rate: finite(item.success_rate, 0, 100),
      avg_tps: finite(item.avg_tps, 0, 1_000_000_000),
    };
  });
}

export function parseBasicRankingsResponse(value: unknown): RankingRow[] {
  return parseRankingRows(envelopeData(value, SUMMARY_RESPONSE_BYTES));
}

export async function loadPublicContent(path: PublicContentPath, signal?: AbortSignal): Promise<string> {
  const response = await api.get<unknown>(path, {
    signal,
    maxContentLength: PUBLIC_CONTENT_RESPONSE_BYTES,
    maxBodyLength: PUBLIC_CONTENT_RESPONSE_BYTES,
  });
  return parsePublicContentResponse(response.data);
}

export async function loadPerformanceSummary(signal?: AbortSignal): Promise<PerformanceSummary[]> {
  const response = await api.get<unknown>('/perf-metrics/summary', {
    params: { hours: 24 },
    signal,
    maxContentLength: SUMMARY_RESPONSE_BYTES,
    maxBodyLength: SUMMARY_RESPONSE_BYTES,
  });
  return parsePerformanceSummaryResponse(response.data);
}

export async function loadBasicRankings(signal?: AbortSignal): Promise<RankingRow[]> {
  const response = await api.get<unknown>('/rankings', {
    signal,
    maxContentLength: SUMMARY_RESPONSE_BYTES,
    maxBodyLength: SUMMARY_RESPONSE_BYTES,
  });
  return parseBasicRankingsResponse(response.data);
}
