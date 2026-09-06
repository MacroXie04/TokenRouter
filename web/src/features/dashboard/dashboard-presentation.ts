import { useEffect, useState } from 'react';
import {
  aggregateDashboardRows,
  aggregateDashboardTimeline,
  dashboardMetricValue,
  dashboardTimelineBuckets,
  normalizeDashboardQuery,
  type DashboardAggregate,
  type DashboardGranularity,
  type DashboardMetric,
  type DashboardQuery,
  type DashboardQuotaRow,
  type DashboardTimelinePoint,
  type DashboardUserRow
} from './dashboard-api';

export interface FilterDraft {
  startTime: string;
  endTime: string;
  username: string;
}

export const MAX_PRESENTED_AGGREGATES = 50;

export const MAX_PRESENTED_FLOW_ROWS = 100;

export const MAX_PRESENTED_PERFORMANCE_ROWS = 50;

export const MAX_PRESENTED_NOTICE_CHARACTERS = 20_000;

export const MAX_UNIX_SECONDS = 4_102_444_800;

export const DASHBOARD_PREFERENCES_KEY = 'tokenrouter.dashboard.preferences.v1';

export const TOP_LIMITS = [5, 10, 20, 50] as const;

export const QUICK_RANGE_DAYS = [1, 7, 14, 29] as const;

export interface AnalyticsPreferences {
  metric: DashboardMetric;
  granularity: DashboardGranularity;
  topLimit: number;
}

export const DEFAULT_ANALYTICS_PREFERENCES: AnalyticsPreferences = {
  metric: 'quota',
  granularity: 'hour',
  topLimit: 10,
};

export function dateTimeInputValue(timestamp: number): string {
  const date = new Date(timestamp * 1_000);
  const year = String(date.getFullYear()).padStart(4, '0');
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  const hour = String(date.getHours()).padStart(2, '0');
  const minute = String(date.getMinutes()).padStart(2, '0');
  const second = String(date.getSeconds()).padStart(2, '0');
  return `${year}-${month}-${day}T${hour}:${minute}:${second}`;
}

export function draftFromQuery(query: DashboardQuery): FilterDraft {
  return {
    startTime: dateTimeInputValue(query.startTimestamp),
    endTime: dateTimeInputValue(query.endTimestamp),
    username: query.username,
  };
}

export function sameQuery(left: DashboardQuery, right: DashboardQuery): boolean {
  return left.startTimestamp === right.startTimestamp
    && left.endTimestamp === right.endTimestamp
    && left.username === right.username;
}

export function localDateTime(value: string): number | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.000)?)?$/u.exec(value);
  if (!match) return undefined;
  const [, yearValue, monthValue, dayValue, hourValue, minuteValue, secondValue = '0'] = match;
  const [year, month, day, hour, minute, second] = [
    yearValue, monthValue, dayValue, hourValue, minuteValue, secondValue,
  ].map(Number);
  const date = new Date(year, month - 1, day, hour, minute, second, 0);
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day
      || date.getHours() !== hour || date.getMinutes() !== minute || date.getSeconds() !== second) return undefined;
  return Math.floor(date.getTime() / 1_000);
}

export function queryFromDraft(draft: FilterDraft, allowUsername: boolean): DashboardQuery {
  const startTimestamp = localDateTime(draft.startTime);
  const endTimestamp = localDateTime(draft.endTime);
  if (startTimestamp === undefined || endTimestamp === undefined) throw new Error('invalid date');
  if (endTimestamp > MAX_UNIX_SECONDS) throw new Error('invalid date');
  return normalizeDashboardQuery({
    startTimestamp,
    endTimestamp,
    username: allowUsername ? draft.username : '',
  }, allowUsername);
}

export function formatNumber(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(value);
}

export function formatDecimal(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value);
}

export const METRIC_LABELS: Record<DashboardMetric, string> = {
  quota: 'Quota',
  requests: 'Requests',
  tokens: 'Tokens',
};

export function readAnalyticsPreferences(namespace: string): AnalyticsPreferences {
  if (typeof window === 'undefined') return DEFAULT_ANALYTICS_PREFERENCES;
  try {
    const raw = window.localStorage.getItem(`${DASHBOARD_PREFERENCES_KEY}.${namespace}`);
    if (!raw || raw.length > 256) return DEFAULT_ANALYTICS_PREFERENCES;
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)
      || Object.keys(parsed).some((key) => !['metric', 'granularity', 'topLimit'].includes(key))
      || !['quota', 'requests', 'tokens'].includes(String(parsed.metric))
      || !['hour', 'day', 'week'].includes(String(parsed.granularity))
      || !TOP_LIMITS.includes(parsed.topLimit as (typeof TOP_LIMITS)[number])) {
      return DEFAULT_ANALYTICS_PREFERENCES;
    }
    return parsed as unknown as AnalyticsPreferences;
  } catch {
    return DEFAULT_ANALYTICS_PREFERENCES;
  }
}

export function useAnalyticsPreferences(namespace: string) {
  const [preferences, setPreferences] = useState<AnalyticsPreferences>(() => readAnalyticsPreferences(namespace));
  useEffect(() => {
    try {
      window.localStorage.setItem(`${DASHBOARD_PREFERENCES_KEY}.${namespace}`, JSON.stringify(preferences));
    } catch {
      // Storage is optional; the controls remain fully functional without it.
    }
  }, [namespace, preferences]);
  return [preferences, setPreferences] as const;
}

export function metricLabel(metric: DashboardMetric): string {
  return METRIC_LABELS[metric];
}

export interface PresentationDimension {
  key: string;
  label: string;
}

export interface PresentationAggregate extends DashboardAggregate {
  key: string;
}

export function modelDimension(name: string, t: (key: string) => string): PresentationDimension {
  return { key: name ? `model:name:${name}` : 'model:missing', label: name || t('Missing model name') };
}

export function userDimension(name: string, t: (key: string) => string): PresentationDimension {
  return { key: name ? `user:name:${name}` : 'user:missing', label: name || t('Missing username') };
}

export function aggregatePresentationRows<T extends { quota: number; requests: number; tokens: number }>(
  rows: T[],
  dimensionFor: (row: T) => PresentationDimension,
): PresentationAggregate[] {
  const dimensions = rows.map(dimensionFor);
  const syntheticByIdentity = new Map<string, string>();
  const dimensionBySynthetic = new Map<string, PresentationDimension>();
  const syntheticLabels = dimensions.map((dimension) => {
    let synthetic = syntheticByIdentity.get(dimension.key);
    if (!synthetic) {
      synthetic = `dimension-${syntheticByIdentity.size + 1}`;
      syntheticByIdentity.set(dimension.key, synthetic);
      dimensionBySynthetic.set(synthetic, dimension);
    }
    return synthetic;
  });
  return aggregateDashboardRows(rows, syntheticLabels).map((aggregate) => {
    const dimension = dimensionBySynthetic.get(aggregate.label);
    if (!dimension) throw new Error('Missing dashboard presentation dimension');
    return { ...aggregate, key: dimension.key, label: dimension.label };
  });
}

export function sortAggregates<T extends DashboardAggregate>(rows: T[], metric: DashboardMetric): T[] {
  return [...rows].sort((left, right) => dashboardMetricValue(right, metric) - dashboardMetricValue(left, metric)
    || right.quota - left.quota || left.label.localeCompare(right.label));
}

export function formatBucket(timestamp: number, granularity: DashboardGranularity): string {
  const formatter = new Intl.DateTimeFormat(undefined, granularity === 'hour'
    ? { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { year: 'numeric', month: 'short', day: 'numeric' });
  const start = new Date(timestamp * 1_000);
  if (granularity !== 'week') return formatter.format(start);
  const end = new Date(start);
  end.setDate(end.getDate() + 6);
  return `${formatter.format(start)} – ${formatter.format(end)}`;
}

export interface TimelineSeries {
  key: string;
  label: string;
  points: DashboardTimelinePoint[];
  isRemainder?: boolean;
}

export interface TimelineDataset {
  observedIntervalCount: number;
  series: TimelineSeries[];
}

export function buildTimelineSeries(
  rows: Array<DashboardQuotaRow | DashboardUserRow>,
  dimensionFor: (row: DashboardQuotaRow | DashboardUserRow) => PresentationDimension,
  rankedDimensions: PresentationAggregate[],
  granularity: DashboardGranularity,
  query: DashboardQuery,
  otherLabel?: string,
): TimelineDataset {
  const observed = aggregateDashboardTimeline(rows, granularity);
  const buckets = dashboardTimelineBuckets(query, granularity);
  const labelLimit = otherLabel ? 4 : 5;
  const selectedDimensions = rankedDimensions.slice(0, labelLimit);
  const selected = new Set(selectedDimensions.map((dimension) => dimension.key));
  const groups: Array<{
    key: string;
    label: string;
    rows: Array<DashboardQuotaRow | DashboardUserRow>;
    isRemainder?: boolean;
  }> = selectedDimensions.map((dimension) => ({
    key: `item:${dimension.key}`,
    label: dimension.label,
    rows: rows.filter((row) => dimensionFor(row).key === dimension.key),
  }));
  if (otherLabel && rankedDimensions.some((dimension) => !selected.has(dimension.key))) {
    groups.push({
      key: 'combined-remainder',
      label: otherLabel,
      rows: rows.filter((row) => !selected.has(dimensionFor(row).key)),
      isRemainder: true,
    });
  }
  return {
    observedIntervalCount: observed.length,
    series: groups.map((group) => {
    const values = new Map(aggregateDashboardTimeline(group.rows, granularity)
      .map((point) => [point.timestamp, point]));
    return {
      key: group.key,
      label: group.label,
      ...(group.isRemainder ? { isRemainder: true } : {}),
      points: buckets.map((timestamp) => values.get(timestamp) ?? {
        timestamp, quota: 0, requests: 0, tokens: 0,
      }),
    };
    }),
  };
}
