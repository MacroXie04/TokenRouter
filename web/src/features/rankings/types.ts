export const RANKING_PERIODS = ['today', 'week', 'month', 'year'] as const;

export type RankingPeriod = (typeof RANKING_PERIODS)[number];

export interface ModelRanking {
  rank: number;
  previous_rank?: number;
  model_name: string;
  vendor: string;
  vendor_icon?: string;
  category: 'all';
  total_tokens: number;
  share: number;
  growth_pct: number;
}

export interface VendorRanking {
  rank: number;
  vendor: string;
  vendor_icon?: string;
  total_tokens: number;
  share: number;
  growth_pct: number;
  models_count: number;
  top_model: string;
}

export interface RankingMover {
  model_name: string;
  vendor: string;
  vendor_icon?: string;
  rank_delta: number;
  current_rank: number;
  growth_pct: number;
}

export interface ModelHistoryPoint {
  ts: string;
  label: string;
  model: string;
  vendor: string;
  tokens: number;
}

export interface ModelHistorySeries {
  points: ModelHistoryPoint[];
  models: Array<{ name: string; vendor: string; total: number }>;
  buckets: number;
}

export interface VendorSharePoint {
  ts: string;
  label: string;
  vendor: string;
  share: number;
  tokens: number;
}

export interface VendorShareSeries {
  points: VendorSharePoint[];
  vendors: Array<{ name: string; total: number; share: number }>;
  buckets: number;
}

export interface RankingsSnapshot {
  models: ModelRanking[];
  vendors: VendorRanking[];
  top_movers: RankingMover[];
  top_droppers: RankingMover[];
  models_history: ModelHistorySeries;
  vendor_share_history: VendorShareSeries;
}

export function isRankingPeriod(value: unknown): value is RankingPeriod {
  return typeof value === 'string' && (RANKING_PERIODS as readonly string[]).includes(value);
}

export function rankingPeriodFromSearch(search: string): RankingPeriod {
  const value = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search).get('period');
  return isRankingPeriod(value) ? value : 'week';
}
