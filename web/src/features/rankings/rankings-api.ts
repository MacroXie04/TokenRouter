import { api } from '../../api';
import type {
  ModelHistoryPoint,
  ModelHistorySeries,
  ModelRanking,
  RankingMover,
  RankingPeriod,
  RankingsSnapshot,
  VendorRanking,
  VendorSharePoint,
  VendorShareSeries,
} from './types';
import { isRankingPeriod } from './types';

const MAX_RESPONSE_BYTES = 768 * 1024;
const MAX_MODELS = 20;
const MAX_VENDORS = 100;
const MAX_MOVERS = 6;
const MAX_HISTORY_MODELS = 11;
const MAX_HISTORY_VENDORS = 6;
const MAX_BUCKETS = 400;
const MAX_MODEL_POINTS = 4_500;
const MAX_VENDOR_POINTS = 2_500;
const MAX_NAME_LENGTH = 512;
const MAX_VENDOR_LENGTH = 128;
const MAX_ICON_LENGTH = 128;
const MAX_LABEL_LENGTH = 64;
const MAX_TIMESTAMP_LENGTH = 64;
// A model can legitimately jump from one token to the largest JSON-safe
// aggregate in a period; keep that finite worst case while rejecting infinity.
const MAX_GROWTH = 1_000_000_000_000_000_000;

type UnknownRecord = Record<string, unknown>;

export class RankingsContractError extends Error {
  constructor() {
    super('Invalid rankings API response');
    this.name = 'RankingsContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new RankingsContractError();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new RankingsContractError();
  }
  if (encoded === undefined || new TextEncoder().encode(encoded).byteLength > MAX_RESPONSE_BYTES) {
    throw new RankingsContractError();
  }
}

function array(value: unknown, maximum: number): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) throw new RankingsContractError();
  return value;
}

function text(value: unknown, maximum: number, allowEmpty = false): string {
  if (typeof value !== 'string' || value.length > maximum || value !== value.trim()) {
    throw new RankingsContractError();
  }
  if (!allowEmpty && !value) throw new RankingsContractError();
  return value;
}

function optionalText(value: unknown, maximum: number): string | undefined {
  if (value === undefined || value === null || value === '') return undefined;
  return text(value, maximum);
}

function integer(value: unknown, minimum: number, maximum = Number.MAX_SAFE_INTEGER): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new RankingsContractError();
  }
  return value as number;
}

function decimal(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new RankingsContractError();
  }
  return value;
}

function timestamp(value: unknown): string {
  const parsed = text(value, MAX_TIMESTAMP_LENGTH);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(parsed) || !Number.isFinite(Date.parse(parsed))) {
    throw new RankingsContractError();
  }
  return parsed;
}

function parseModel(value: unknown, expectedRank: number): ModelRanking {
  const item = record(value);
  const rank = integer(item.rank, 1, MAX_MODELS);
  if (rank !== expectedRank) throw new RankingsContractError();
  const icon = optionalText(item.vendor_icon, MAX_ICON_LENGTH);
  const previousRank = item.previous_rank === undefined || item.previous_rank === null
    ? undefined
    : integer(item.previous_rank, 1, 1_000);
  if (item.category !== 'all') throw new RankingsContractError();
  return {
    rank,
    ...(previousRank === undefined ? {} : { previous_rank: previousRank }),
    model_name: text(item.model_name, MAX_NAME_LENGTH),
    vendor: text(item.vendor, MAX_VENDOR_LENGTH),
    ...(icon ? { vendor_icon: icon } : {}),
    category: 'all',
    total_tokens: integer(item.total_tokens, 1),
    share: decimal(item.share, 0, 1),
    growth_pct: decimal(item.growth_pct, -MAX_GROWTH, MAX_GROWTH),
  };
}

function parseVendor(value: unknown, expectedRank: number): VendorRanking {
  const item = record(value);
  const rank = integer(item.rank, 1, MAX_VENDORS);
  if (rank !== expectedRank) throw new RankingsContractError();
  const icon = optionalText(item.vendor_icon, MAX_ICON_LENGTH);
  return {
    rank,
    vendor: text(item.vendor, MAX_VENDOR_LENGTH),
    ...(icon ? { vendor_icon: icon } : {}),
    total_tokens: integer(item.total_tokens, 1),
    share: decimal(item.share, 0, 1),
    growth_pct: decimal(item.growth_pct, -MAX_GROWTH, MAX_GROWTH),
    models_count: integer(item.models_count, 1, 1_000),
    top_model: text(item.top_model, MAX_NAME_LENGTH),
  };
}

function parseMover(value: unknown, direction: 'up' | 'down'): RankingMover {
  const item = record(value);
  const delta = integer(item.rank_delta, -1_000, 1_000);
  if ((direction === 'up' && delta <= 0) || (direction === 'down' && delta >= 0)) {
    throw new RankingsContractError();
  }
  const icon = optionalText(item.vendor_icon, MAX_ICON_LENGTH);
  return {
    model_name: text(item.model_name, MAX_NAME_LENGTH),
    vendor: text(item.vendor, MAX_VENDOR_LENGTH),
    ...(icon ? { vendor_icon: icon } : {}),
    rank_delta: delta,
    current_rank: integer(item.current_rank, 1, 1_000),
    growth_pct: decimal(item.growth_pct, -MAX_GROWTH, MAX_GROWTH),
  };
}

function parseModelHistoryPoint(value: unknown): ModelHistoryPoint {
  const item = record(value);
  return {
    ts: timestamp(item.ts),
    label: text(item.label, MAX_LABEL_LENGTH),
    model: text(item.model, MAX_NAME_LENGTH),
    vendor: text(item.vendor, MAX_VENDOR_LENGTH),
    tokens: integer(item.tokens, 1),
  };
}

function parseModelHistory(value: unknown): ModelHistorySeries {
  const item = record(value);
  const models = array(item.models, MAX_HISTORY_MODELS).map((raw) => {
    const model = record(raw);
    return {
      name: text(model.name, MAX_NAME_LENGTH),
      vendor: text(model.vendor, MAX_VENDOR_LENGTH),
      total: integer(model.total, 1),
    };
  });
  unique(models.map((model) => model.name));
  const allowed = new Set(models.map((model) => model.name));
  const points = array(item.points, MAX_MODEL_POINTS).map(parseModelHistoryPoint);
  unique(points.map((point) => `${point.ts}\u0000${point.model}`));
  if (points.some((point) => !allowed.has(point.model))) throw new RankingsContractError();
  const buckets = integer(item.buckets, 0, MAX_BUCKETS);
  if ((buckets === 0) !== (points.length === 0) || (models.length === 0) !== (points.length === 0)) {
    throw new RankingsContractError();
  }
  return { points, models, buckets };
}

function parseVendorHistoryPoint(value: unknown): VendorSharePoint {
  const item = record(value);
  return {
    ts: timestamp(item.ts),
    label: text(item.label, MAX_LABEL_LENGTH),
    vendor: text(item.vendor, MAX_VENDOR_LENGTH),
    share: decimal(item.share, 0, 1),
    tokens: integer(item.tokens, 1),
  };
}

function parseVendorHistory(value: unknown): VendorShareSeries {
  const item = record(value);
  const vendors = array(item.vendors, MAX_HISTORY_VENDORS).map((raw) => {
    const vendor = record(raw);
    return {
      name: text(vendor.name, MAX_VENDOR_LENGTH),
      total: integer(vendor.total, 1),
      share: decimal(vendor.share, 0, 1),
    };
  });
  unique(vendors.map((vendor) => vendor.name));
  const allowed = new Set(vendors.map((vendor) => vendor.name));
  const points = array(item.points, MAX_VENDOR_POINTS).map(parseVendorHistoryPoint);
  unique(points.map((point) => `${point.ts}\u0000${point.vendor}`));
  if (points.some((point) => !allowed.has(point.vendor))) throw new RankingsContractError();
  const sharesByTimestamp = new Map<string, number>();
  for (const point of points) sharesByTimestamp.set(point.ts, (sharesByTimestamp.get(point.ts) ?? 0) + point.share);
  if ([...sharesByTimestamp.values()].some((share) => share > 1.01)) throw new RankingsContractError();
  const buckets = integer(item.buckets, 0, MAX_BUCKETS);
  if ((buckets === 0) !== (points.length === 0) || (vendors.length === 0) !== (points.length === 0)) {
    throw new RankingsContractError();
  }
  return { points, vendors, buckets };
}

function unique(values: string[]): void {
  if (new Set(values).size !== values.length) throw new RankingsContractError();
}

export function parseRankingsResponse(value: unknown): RankingsSnapshot {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true) throw new RankingsContractError();
  const data = record(envelope.data);
  const models = array(data.models, MAX_MODELS).map((item, index) => parseModel(item, index + 1));
  const vendors = array(data.vendors, MAX_VENDORS).map((item, index) => parseVendor(item, index + 1));
  const topMovers = array(data.top_movers, MAX_MOVERS).map((item) => parseMover(item, 'up'));
  const topDroppers = array(data.top_droppers, MAX_MOVERS).map((item) => parseMover(item, 'down'));
  unique(models.map((model) => model.model_name));
  unique(vendors.map((vendor) => vendor.vendor));
  unique(topMovers.map((mover) => mover.model_name));
  unique(topDroppers.map((mover) => mover.model_name));
  if (models.reduce((sum, model) => sum + model.share, 0) > 1.01) throw new RankingsContractError();
  return {
    models,
    vendors,
    top_movers: topMovers,
    top_droppers: topDroppers,
    models_history: parseModelHistory(data.models_history),
    vendor_share_history: parseVendorHistory(data.vendor_share_history),
  };
}

const responseLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

export async function loadRankings(period: RankingPeriod, signal?: AbortSignal): Promise<RankingsSnapshot> {
  if (!isRankingPeriod(period)) throw new RankingsContractError();
  const response = await api.get<unknown>('/rankings', {
    params: { period },
    signal,
    ...responseLimits,
  });
  return parseRankingsResponse(response.data);
}
