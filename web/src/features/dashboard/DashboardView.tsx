import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { FormEvent, MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import {
  DASHBOARD_MAX_RANGE_SECONDS,
  DASHBOARD_SECTIONS,
  aggregateDashboardRows,
  aggregateDashboardTimeline,
  canAccessDashboardSection,
  canFilterDashboardByUsername,
  dashboardMetricValue,
  dashboardQueryURL,
  dashboardTimelineBuckets,
  defaultDashboardQuery,
  isAdministratorRole,
  loadDashboardOverview,
  loadDashboardPerformance,
  loadDashboardSection,
  normalizeDashboardQuery,
  parseDashboardSearch,
  type DashboardAggregate,
  type DashboardFlowRow,
  type DashboardGranularity,
  type DashboardMetric,
  type DashboardOverviewContent,
  type DashboardPerformanceRow,
  type DashboardQuery,
  type DashboardQuotaRow,
  type DashboardResult,
  type DashboardSection,
  type DashboardSummary,
  type DashboardTimelinePoint,
  type DashboardUserRow,
} from './dashboard-api';
import './dashboard.css';

export interface DashboardViewProps {
  section: DashboardSection;
  role: number;
  search?: string;
  onNavigate?: (target: string) => void;
}

interface FilterDraft {
  startTime: string;
  endTime: string;
  username: string;
}

const SECTION_LABELS: Record<DashboardSection, string> = {
  overview: 'Overview',
  models: 'Models',
  flow: 'Flow',
  users: 'Users',
};

const SECTION_DESCRIPTIONS: Record<DashboardSection, string> = {
  overview: 'A concise view of consumption during the selected period.',
  models: 'Compare usage aggregated by model.',
  flow: 'Trace requests across the available traffic dimensions.',
  users: 'Compare usage aggregated by user.',
};

const MAX_PRESENTED_AGGREGATES = 50;
const MAX_PRESENTED_FLOW_ROWS = 100;
const MAX_PRESENTED_PERFORMANCE_ROWS = 50;
const MAX_PRESENTED_NOTICE_CHARACTERS = 20_000;
const MAX_UNIX_SECONDS = 4_102_444_800;
const DASHBOARD_PREFERENCES_KEY = 'tokenrouter.dashboard.preferences.v1';
const TOP_LIMITS = [5, 10, 20, 50] as const;
const QUICK_RANGE_DAYS = [1, 7, 14, 29] as const;

interface AnalyticsPreferences {
  metric: DashboardMetric;
  granularity: DashboardGranularity;
  topLimit: number;
}

const DEFAULT_ANALYTICS_PREFERENCES: AnalyticsPreferences = {
  metric: 'quota',
  granularity: 'hour',
  topLimit: 10,
};

function dateTimeInputValue(timestamp: number): string {
  const date = new Date(timestamp * 1_000);
  const year = String(date.getFullYear()).padStart(4, '0');
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  const hour = String(date.getHours()).padStart(2, '0');
  const minute = String(date.getMinutes()).padStart(2, '0');
  const second = String(date.getSeconds()).padStart(2, '0');
  return `${year}-${month}-${day}T${hour}:${minute}:${second}`;
}

function draftFromQuery(query: DashboardQuery): FilterDraft {
  return {
    startTime: dateTimeInputValue(query.startTimestamp),
    endTime: dateTimeInputValue(query.endTimestamp),
    username: query.username,
  };
}

function sameQuery(left: DashboardQuery, right: DashboardQuery): boolean {
  return left.startTimestamp === right.startTimestamp
    && left.endTimestamp === right.endTimestamp
    && left.username === right.username;
}

function localDateTime(value: string): number | undefined {
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

function queryFromDraft(draft: FilterDraft, allowUsername: boolean): DashboardQuery {
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

function formatNumber(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(value);
}

function formatDecimal(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value);
}

const METRIC_LABELS: Record<DashboardMetric, string> = {
  quota: 'Quota',
  requests: 'Requests',
  tokens: 'Tokens',
};

function readAnalyticsPreferences(namespace: string): AnalyticsPreferences {
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

function useAnalyticsPreferences(namespace: string) {
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

function metricLabel(metric: DashboardMetric): string {
  return METRIC_LABELS[metric];
}

interface PresentationDimension {
  key: string;
  label: string;
}

interface PresentationAggregate extends DashboardAggregate {
  key: string;
}

function modelDimension(name: string, t: (key: string) => string): PresentationDimension {
  return { key: name ? `model:name:${name}` : 'model:missing', label: name || t('Missing model name') };
}

function userDimension(name: string, t: (key: string) => string): PresentationDimension {
  return { key: name ? `user:name:${name}` : 'user:missing', label: name || t('Missing username') };
}

function aggregatePresentationRows<T extends { quota: number; requests: number; tokens: number }>(
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

function sortAggregates<T extends DashboardAggregate>(rows: T[], metric: DashboardMetric): T[] {
  return [...rows].sort((left, right) => dashboardMetricValue(right, metric) - dashboardMetricValue(left, metric)
    || right.quota - left.quota || left.label.localeCompare(right.label));
}

function formatBucket(timestamp: number, granularity: DashboardGranularity): string {
  const formatter = new Intl.DateTimeFormat(undefined, granularity === 'hour'
    ? { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { year: 'numeric', month: 'short', day: 'numeric' });
  const start = new Date(timestamp * 1_000);
  if (granularity !== 'week') return formatter.format(start);
  const end = new Date(start);
  end.setDate(end.getDate() + 6);
  return `${formatter.format(start)} – ${formatter.format(end)}`;
}

function SummaryCards({ summary, durationSeconds, extra = [] }: {
  summary: DashboardSummary;
  durationSeconds: number;
  extra?: Array<{ label: string; value: number }>;
}) {
  const { t } = useTranslation();
  const rateMultiplier = durationSeconds > 0 && Number.isFinite(durationSeconds)
    ? 60 / durationSeconds : 0;
  const metrics = [
    { label: 'Requests', value: summary.requests, decimal: false },
    { label: 'Tokens', value: summary.tokens, decimal: false },
    { label: 'Quota', value: summary.quota, decimal: false },
    { label: 'Average RPM', value: summary.requests * rateMultiplier, decimal: true },
    { label: 'Average TPM', value: summary.tokens * rateMultiplier, decimal: true },
    ...extra.map((item) => ({ ...item, decimal: false })),
  ];
  return (
    <dl className="dashboard-summary" aria-label={t('Usage summary')}>
      {metrics.map((metric) => (
        <div className="dashboard-summary-card" key={metric.label}>
          <dt>{t(metric.label)}</dt>
          <dd>{metric.decimal ? formatDecimal(metric.value) : formatNumber(metric.value)}</dd>
        </div>
      ))}
    </dl>
  );
}

function AggregateTable({ rows, labelHeading, caption, metric = 'quota', limit = MAX_PRESENTED_AGGREGATES }: {
  rows: Array<DashboardAggregate & { key?: string }>;
  labelHeading: string;
  caption: string;
  metric?: DashboardMetric;
  limit?: number;
}) {
  const { t } = useTranslation();
  const visible = rows.slice(0, limit);
  const maximum = visible.reduce((value, row) => Math.max(value, dashboardMetricValue(row, metric)), 0) || 1;
  return (
    <div className="table-scroll">
      <table className="dashboard-table">
        <caption>{t(caption)}</caption>
        <thead>
          <tr>
            <th scope="col">{t(labelHeading)}</th>
            <th scope="col">{t('Quota')}</th>
            <th scope="col">{t('Requests')}</th>
            <th scope="col">{t('Tokens')}</th>
            <th scope="col" className="dashboard-share-column">{t('Relative usage')}</th>
          </tr>
        </thead>
        <tbody>
          {visible.map((row) => (
            <tr key={row.key ?? row.label}>
              <th scope="row">{row.label}</th>
              <td>{formatNumber(row.quota)}</td>
              <td>{formatNumber(row.requests)}</td>
              <td>{formatNumber(row.tokens)}</td>
              <td className="dashboard-share-column">
                <progress
                  aria-label={t('Relative {{metric}} for {{name}}', {
                    metric: t(metricLabel(metric)).toLocaleLowerCase(),
                    name: row.label,
                  })}
                  max={maximum}
                  value={dashboardMetricValue(row, metric)}
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length > visible.length && (
        <p className="dashboard-limit-note muted">
          {t('Showing the top {{count}} results.', { count: visible.length })}
        </p>
      )}
    </div>
  );
}

function AnalyticsControls({ preferences, onChange, search, onSearch, label }: {
  preferences: AnalyticsPreferences;
  onChange: (preferences: AnalyticsPreferences) => void;
  search?: string;
  onSearch?: (value: string) => void;
  label: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="dashboard-analytics-controls" role="group" aria-label={t(label)}>
      <label>
        {t('Metric')}
        <select
          value={preferences.metric}
          onChange={(event) => onChange({ ...preferences, metric: event.target.value as DashboardMetric })}
        >
          {(Object.keys(METRIC_LABELS) as DashboardMetric[]).map((metric) => (
            <option key={metric} value={metric}>{t(metricLabel(metric))}</option>
          ))}
        </select>
      </label>
      <label>
        {t('Time granularity')}
        <select
          value={preferences.granularity}
          onChange={(event) => onChange({ ...preferences, granularity: event.target.value as DashboardGranularity })}
        >
          <option value="hour">{t('Hourly')}</option>
          <option value="day">{t('Daily')}</option>
          <option value="week">{t('Weekly')}</option>
        </select>
      </label>
      <label>
        {t('Top results')}
        <select
          value={preferences.topLimit}
          onChange={(event) => onChange({ ...preferences, topLimit: Number(event.target.value) })}
        >
          {TOP_LIMITS.map((limit) => <option key={limit} value={limit}>{limit}</option>)}
        </select>
      </label>
      {onSearch && (
        <label>
          {t('Filter models')}
          <input
            autoComplete="off"
            maxLength={512}
            onChange={(event) => onSearch(event.target.value)}
            placeholder={t('Search model names')}
            type="search"
            value={search}
          />
        </label>
      )}
    </div>
  );
}

function DistributionBars({ rows, metric, title, limit }: {
  rows: Array<DashboardAggregate & { key?: string }>;
  metric: DashboardMetric;
  title: string;
  limit: number;
}) {
  const { t } = useTranslation();
  const visible = rows.slice(0, limit);
  const maximum = visible.reduce((value, row) => Math.max(value, dashboardMetricValue(row, metric)), 0) || 1;
  return (
    <section className="dashboard-chart-panel" aria-labelledby={`${title.replace(/\s/gu, '-')}-heading`}>
      <div className="dashboard-chart-heading">
        <h3 id={`${title.replace(/\s/gu, '-')}-heading`}>{t(title)}</h3>
        <span className="muted">{t(metricLabel(metric))}</span>
      </div>
      <ol className="dashboard-ranked-bars" aria-label={t(title)}>
        {visible.map((row, index) => (
          <li key={row.key ?? row.label}>
            <span className="dashboard-rank">{index + 1}</span>
            <span className="dashboard-bar-label">{row.label}</span>
            <progress
              aria-label={t('{{metric}} for {{name}}', { metric: t(metricLabel(metric)), name: row.label })}
              max={maximum}
              value={dashboardMetricValue(row, metric)}
            />
            <output>{formatNumber(dashboardMetricValue(row, metric))}</output>
          </li>
        ))}
      </ol>
    </section>
  );
}

interface TimelineSeries {
  key: string;
  label: string;
  points: DashboardTimelinePoint[];
  isRemainder?: boolean;
}

interface TimelineDataset {
  observedIntervalCount: number;
  series: TimelineSeries[];
}

function buildTimelineSeries(
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

function TimelineChart({ dataset, metric, title, granularity, note }: {
  dataset: TimelineDataset;
  metric: DashboardMetric;
  title: string;
  granularity: DashboardGranularity;
  note?: string;
}) {
  const { t } = useTranslation();
  const { observedIntervalCount, series } = dataset;
  const titleId = useId();
  const descriptionId = useId();
  const points = series[0]?.points ?? [];
  const maximum = series.reduce((seriesMaximum, item) => item.points.reduce(
    (value, point) => Math.max(value, dashboardMetricValue(point, metric)), seriesMaximum,
  ), 0) || 1;
  const firstTimestamp = points[0]?.timestamp ?? 0;
  const lastTimestamp = points[points.length - 1]?.timestamp ?? firstTimestamp;
  const timestampSpan = lastTimestamp - firstTimestamp;
  const coordinates = series.map((item) => item.points.map((point) => {
    const x = timestampSpan === 0 ? 360 : 42 + ((point.timestamp - firstTimestamp) / timestampSpan) * 636;
    const y = 190 - (dashboardMetricValue(point, metric) / maximum) * 150;
    return { x, y };
  }));
  const enoughForTrend = observedIntervalCount >= 4 && series.length > 0;
  const dashPatterns = ['', '8 4', '2 4', '10 3 2 3', '6 2'];
  return (
    <section className="dashboard-chart-panel">
      <div className="dashboard-chart-heading">
        <h3>{t(title)}</h3>
        <span className="muted">{t('{{count}} observed intervals', { count: observedIntervalCount })}</span>
      </div>
      {note && <p className="dashboard-chart-note muted">{note}</p>}
      {enoughForTrend ? (
        <svg
          className="dashboard-line-chart"
          viewBox="0 0 720 220"
          role="img"
          aria-labelledby={`${titleId} ${descriptionId}`}
          preserveAspectRatio="none"
        >
          <title id={titleId}>{t(title)}</title>
          <desc id={descriptionId}>{t('{{metric}} for {{series}} series across {{count}} observed intervals.', {
            metric: t(metricLabel(metric)), series: series.length, count: observedIntervalCount,
          })}</desc>
          <line className="dashboard-chart-axis" x1="42" x2="678" y1="190" y2="190" />
          <line className="dashboard-chart-axis" x1="42" x2="42" y1="40" y2="190" />
          {series.map((item, seriesIndex) => <polyline
            className={`dashboard-chart-line dashboard-chart-series-${seriesIndex + 1}`}
            key={item.key}
            points={coordinates[seriesIndex].map(({ x, y }) => `${x},${y}`).join(' ')}
            strokeDasharray={dashPatterns[seriesIndex]}
          />)}
          {series.length * points.length <= 120 && series.flatMap((item, seriesIndex) => item.points.map((point, index) => (
            <circle
              className={`dashboard-chart-point dashboard-chart-series-${seriesIndex + 1}`}
              cx={coordinates[seriesIndex][index].x}
              cy={coordinates[seriesIndex][index].y}
              key={`${item.key}:${point.timestamp}`}
              r="3"
            />
          )))}
          <text className="dashboard-chart-label" x="42" y="212">{formatBucket(points[0].timestamp, granularity)}</text>
          <text className="dashboard-chart-label" textAnchor="end" x="678" y="212">
            {formatBucket(points[points.length - 1].timestamp, granularity)}
          </text>
        </svg>
      ) : (
        <p className="dashboard-chart-state muted">
          {t('Not enough observed intervals to draw a reliable trend.')}
        </p>
      )}
      {series.length > 0 && <ul className="dashboard-chart-legend" aria-label={t('Chart series')}>
        {series.map((item, index) => <li key={item.key}>
          <span className={`dashboard-series-swatch dashboard-chart-series-${index + 1}`} aria-hidden="true" />
          {item.label}{item.isRemainder && <small>{t('Combined')}</small>}
        </li>)}
      </ul>}
      <details className="dashboard-chart-data">
        <summary>{t('View chart data')}</summary>
        <div className="table-scroll">
          <table className="dashboard-table">
            <caption>{t('{{title}} data', { title: t(title) })}</caption>
            <thead><tr><th scope="col">{t('Time')}</th>{series.map((item) => (
              <th key={item.key} scope="col">
                {item.label}{item.isRemainder ? ` (${t('Combined')})` : ''} · {t(metricLabel(metric))}
              </th>
            ))}</tr></thead>
            <tbody>{points.map((point, pointIndex) => (
              <tr key={point.timestamp}>
                <th scope="row"><time dateTime={new Date(point.timestamp * 1_000).toISOString()}>{formatBucket(point.timestamp, granularity)}</time></th>
                {series.map((item) => <td key={item.key}>{formatNumber(
                  dashboardMetricValue(item.points[pointIndex] ?? point, metric),
                )}</td>)}
              </tr>
            ))}</tbody>
          </table>
        </div>
      </details>
    </section>
  );
}

function ModelsPresentation({ rows, query }: { rows: DashboardQuotaRow[]; query: DashboardQuery }) {
  const { t } = useTranslation();
  const [preferences, setPreferences] = useAnalyticsPreferences('models');
  const [search, setSearch] = useState('');
  const normalizedSearch = search.trim().toLocaleLowerCase();
  const filteredRows = useMemo(() => rows.filter((row) => !normalizedSearch
    || modelDimension(row.modelName, t).label.toLocaleLowerCase().includes(normalizedSearch)), [normalizedSearch, rows, t]);
  const aggregates = useMemo(() => sortAggregates(aggregatePresentationRows(
    filteredRows, (row) => modelDimension(row.modelName, t),
  ), preferences.metric), [filteredRows, preferences.metric, t]);
  const totalModelCount = useMemo(() => new Set(rows.map((row) => modelDimension(row.modelName, t).key)).size, [rows, t]);
  const filteredSummary = useMemo(() => filteredRows.reduce<DashboardSummary>((summary, row) => ({
    quota: summary.quota + row.quota,
    requests: summary.requests + row.requests,
    tokens: summary.tokens + row.tokens,
  }), { quota: 0, requests: 0, tokens: 0 }), [filteredRows]);
  const timelineDataset = useMemo(() => buildTimelineSeries(
    filteredRows,
    (row) => modelDimension('modelName' in row ? row.modelName : '', t),
    aggregates.slice(0, preferences.topLimit),
    preferences.granularity,
    query,
    t('Other models'),
  ), [aggregates, filteredRows, preferences.granularity, preferences.topLimit, query, t]);
  return (
    <section className="dashboard-analytics" aria-labelledby="dashboard-model-analytics-heading">
      <h3 className="sr-only" id="dashboard-model-analytics-heading">{t('Model analytics')}</h3>
      <AnalyticsControls
        label="Model chart preferences and filters"
        onChange={setPreferences}
        onSearch={setSearch}
        preferences={preferences}
        search={search}
      />
      {normalizedSearch && <p className="dashboard-filter-count muted" aria-live="polite">
        {t('Showing {{visible}} of {{total}} models. Filtered charts and table total {{quota}} quota, {{requests}} requests, and {{tokens}} tokens.', {
          visible: aggregates.length,
          total: totalModelCount,
          quota: formatNumber(filteredSummary.quota),
          requests: formatNumber(filteredSummary.requests),
          tokens: formatNumber(filteredSummary.tokens),
        })}
      </p>}
      {filteredRows.length === 0 ? (
        <p className="dashboard-client-empty muted" role="status">{t('No models match this filter.')}</p>
      ) : (
        <div className="dashboard-chart-grid">
          <TimelineChart
            dataset={timelineDataset}
            granularity={preferences.granularity}
            metric={preferences.metric}
            note={aggregates.length > 4
              ? t('Trend shows the four highest-ranked models and combines the remainder as Other models.')
              : undefined}
            title="Model usage over time"
          />
          <DistributionBars
            limit={preferences.topLimit}
            metric={preferences.metric}
            rows={aggregates}
            title="Model consumption distribution"
          />
          <div className="dashboard-chart-wide">
            <AggregateTable
              caption="Usage by model"
              labelHeading="Model"
              limit={preferences.topLimit}
              metric={preferences.metric}
              rows={aggregates}
            />
          </div>
        </div>
      )}
    </section>
  );
}

function UsersPresentation({ rows, query }: { rows: DashboardUserRow[]; query: DashboardQuery }) {
  const { t } = useTranslation();
  const [preferences, setPreferences] = useAnalyticsPreferences('users');
  const aggregates = useMemo(() => sortAggregates(aggregatePresentationRows(
    rows, (row) => userDimension(row.username, t),
  ), preferences.metric), [preferences.metric, rows, t]);
  const timelineDataset = useMemo(() => buildTimelineSeries(
    rows,
    (row) => userDimension('username' in row ? row.username : '', t),
    aggregates.slice(0, preferences.topLimit),
    preferences.granularity,
    query,
  ), [aggregates, preferences.granularity, preferences.topLimit, query, rows, t]);
  return (
    <section className="dashboard-analytics" aria-labelledby="dashboard-user-analytics-heading">
      <h3 className="sr-only" id="dashboard-user-analytics-heading">{t('User analytics')}</h3>
      <AnalyticsControls
        label="User chart preferences"
        onChange={setPreferences}
        preferences={preferences}
      />
      <div className="dashboard-chart-grid">
        <DistributionBars
          limit={preferences.topLimit}
          metric={preferences.metric}
          rows={aggregates}
          title="User consumption ranking"
        />
        <TimelineChart
          dataset={timelineDataset}
          granularity={preferences.granularity}
          metric={preferences.metric}
          note={aggregates.length > 5 ? t('Trend shows the five highest-ranked users.') : undefined}
          title="User consumption trend"
        />
        <div className="dashboard-chart-wide">
          <AggregateTable
            caption="Usage by user"
            labelHeading="User"
            limit={preferences.topLimit}
            metric={preferences.metric}
            rows={aggregates}
          />
        </div>
      </div>
    </section>
  );
}

type FlowNodeKind = 'user' | 'node' | 'token' | 'group' | 'model' | 'channel';
interface FlowNode { kind: FlowNodeKind; key: string; label: string }
interface FlowNodeOption extends FlowNode { value: number; alias: string }

const FLOW_NODE_LABELS: Record<FlowNodeKind, string> = {
  user: 'User', node: 'Node', token: 'Token', group: 'Group', model: 'Model', channel: 'Channel',
};

const FLOW_ROLE_STAGES: Record<1 | 10 | 100, FlowNodeKind[]> = {
  1: ['token', 'group', 'model'],
  10: ['user', 'group', 'model', 'channel'],
  100: ['user', 'node', 'token', 'group', 'model', 'channel'],
};

function flowNodes(row: DashboardFlowRow, role: number,
  t: (key: string, values?: Record<string, unknown>) => string): FlowNode[] {
  const stages = FLOW_ROLE_STAGES[role as 1 | 10 | 100] ?? [];
  const candidates: Array<[FlowNodeKind, string, string]> = [
    ['user', row.username || (row.userId ? t('User #{{id}}', { id: row.userId }) : t('Unknown user')),
      row.userId ? `id:${row.userId}` : row.username ? `name:${row.username}` : 'unknown'],
    ['node', row.nodeName || t('Unknown node'), row.nodeName ? `name:${row.nodeName}` : 'unknown'],
    ['token', row.tokenName || (row.tokenId ? t('Token #{{id}}', { id: row.tokenId }) : t('Unknown token')),
      row.tokenId ? `id:${row.tokenId}` : row.tokenName ? `name:${row.tokenName}` : 'unknown'],
    ['group', row.group, `name:${row.group}`],
    ['model', modelDimension(row.modelName, t).label, row.modelName ? `name:${row.modelName}` : 'empty'],
    ['channel', row.channelName || (row.channelId ? t('Channel #{{id}}', { id: row.channelId }) : t('Unknown channel')),
      row.channelId ? `id:${row.channelId}` : row.channelName ? `name:${row.channelName}` : 'unknown'],
  ];
  return candidates.filter(([kind]) => stages.includes(kind))
    .map(([kind, label, identity]) => ({ kind, label, key: `${kind}\u0000${identity}` }));
}

function flowNodeKindFromKey(key: string): FlowNodeKind {
  return key.slice(0, key.indexOf('\u0000')) as FlowNodeKind;
}

function selectedFlowRows(rows: DashboardFlowRow[], selected: string[],
  role: number, t: (key: string, values?: Record<string, unknown>) => string, ignoredKind?: FlowNodeKind) {
  if (selected.length === 0) return rows;
  const selectedByKind = new Map<FlowNodeKind, Set<string>>();
  selected.forEach((key) => {
    const kind = flowNodeKindFromKey(key);
    if (kind === ignoredKind) return;
    const values = selectedByKind.get(kind) ?? new Set<string>();
    values.add(key);
    selectedByKind.set(kind, values);
  });
  return rows.filter((row) => {
    const keys = new Set(flowNodes(row, role, t).map((node) => node.key));
    return [...selectedByKind.values()].every((values) => [...values].some((key) => keys.has(key)));
  });
}

function presentedFlowOptionLabel(option: FlowNodeOption, showSensitive: boolean): string {
  return showSensitive || option.kind === 'model' ? option.label : option.alias;
}

function flowPath(row: DashboardFlowRow, role: number, options: Map<string, FlowNodeOption>, showSensitive: boolean,
  t: (key: string, values?: Record<string, unknown>) => string): string[] {
  return flowNodes(row, role, t).map((node) => {
    if (showSensitive || node.kind === 'model') return node.label;
    return options.get(node.key)?.alias ?? t('Hidden {{kind}}', { kind: t(FLOW_NODE_LABELS[node.kind]) });
  });
}

function flowPathText(nodes: string[]): string {
  return nodes.join(' → ');
}

function FlowTable({ rows, role, options, showSensitive }: {
  rows: DashboardFlowRow[];
  role: number;
  options: Map<string, FlowNodeOption>;
  showSensitive: boolean;
}) {
  const { t } = useTranslation();
  const visible = rows.slice(0, MAX_PRESENTED_FLOW_ROWS);
  return (
    <div className="table-scroll">
      <table className="dashboard-table dashboard-flow-table">
        <caption>{t('Traffic flow')}</caption>
        <thead><tr><th scope="col">{t('Path')}</th><th scope="col">{t('Quota')}</th>
          <th scope="col">{t('Requests')}</th><th scope="col">{t('Tokens')}</th></tr></thead>
        <tbody>{visible.map((row, index) => {
          const path = flowPathText(flowPath(row, role, options, showSensitive, t));
          return (
            <tr key={index}>
              <th scope="row" className="dashboard-flow-path">{path}</th>
              <td>{formatNumber(row.quota)}</td><td>{formatNumber(row.requests)}</td><td>{formatNumber(row.tokens)}</td>
            </tr>
          );
        })}</tbody>
      </table>
      {rows.length > visible.length && <p className="dashboard-limit-note muted">
        {t('Showing the top {{count}} flow rows.', { count: visible.length })}
      </p>}
    </div>
  );
}

function FlowPresentation({ rows, role }: { rows: DashboardFlowRow[]; role: number }) {
  const { t } = useTranslation();
  const [metric, setMetric] = useState<DashboardMetric>('quota');
  const [showSensitive, setShowSensitive] = useState(false);
  const [selectedKind, setSelectedKind] = useState<FlowNodeKind>('model');
  const [selectedNodes, setSelectedNodes] = useState<string[]>([]);
  const [nodeSearch, setNodeSearch] = useState('');
  const options = useMemo(() => {
    const totals = new Map<string, FlowNodeOption>();
    const counters = new Map<FlowNodeKind, number>();
    rows.forEach((row) => flowNodes(row, role, t).forEach((node) => {
      const current = totals.get(node.key) ?? { ...node, value: 0, alias: '' };
      current.value += dashboardMetricValue(row, metric);
      totals.set(node.key, current);
    }));
    const withAliases = [...totals.values()]
      .sort((left, right) => left.kind.localeCompare(right.kind) || left.key.localeCompare(right.key))
      .map((option) => {
        if (option.kind === 'model') return { ...option, alias: option.label };
        const index = (counters.get(option.kind) ?? 0) + 1;
        counters.set(option.kind, index);
        return { ...option, alias: t('Hidden {{kind}} {{index}}', {
          kind: t(FLOW_NODE_LABELS[option.kind]), index,
        }) };
      });
    return withAliases.sort((left, right) => right.value - left.value || left.label.localeCompare(right.label));
  }, [metric, role, rows, t]);
  const optionMap = useMemo(() => new Map(options.map((option) => [option.key, option])), [options]);
  useEffect(() => {
    setSelectedNodes((current) => {
      const next = current.filter((key) => optionMap.has(key));
      return next.length === current.length ? current : next;
    });
  }, [optionMap]);
  const availableKinds = useMemo(() => (Object.keys(FLOW_NODE_LABELS) as FlowNodeKind[])
    .filter((kind) => options.some((option) => option.kind === kind)), [options]);
  useEffect(() => {
    if (!availableKinds.includes(selectedKind) && availableKinds[0]) setSelectedKind(availableKinds[0]);
  }, [availableKinds, selectedKind]);
  const filtered = useMemo(() => selectedFlowRows(rows, selectedNodes, role, t)
    .sort((left, right) => dashboardMetricValue(right, metric) - dashboardMetricValue(left, metric)),
  [metric, role, rows, selectedNodes, t]);
  const visible = filtered.slice(0, 20);
  const maximum = visible.reduce((value, row) => Math.max(value, dashboardMetricValue(row, metric)), 0) || 1;
  const filteredMetricTotal = filtered.reduce((total, row) => total + dashboardMetricValue(row, metric), 0);
  const normalizedNodeSearch = nodeSearch.trim().toLocaleLowerCase();
  const facetedRows = selectedFlowRows(rows, selectedNodes, role, t, selectedKind);
  const facetedTotals = new Map<string, number>();
  facetedRows.forEach((row) => flowNodes(row, role, t).forEach((node) => {
    if (node.kind === selectedKind) {
      facetedTotals.set(node.key, (facetedTotals.get(node.key) ?? 0) + dashboardMetricValue(row, metric));
    }
  }));
  const allKindOptions = options
    .filter((option) => option.kind === selectedKind
      && (facetedTotals.has(option.key) || selectedNodes.includes(option.key)))
    .map((option) => ({ ...option, value: facetedTotals.get(option.key) ?? 0 }))
    .sort((left, right) => right.value - left.value || left.label.localeCompare(right.label));
  const matchingKindOptions = allKindOptions.filter((option) => !normalizedNodeSearch
    || presentedFlowOptionLabel(option, showSensitive).toLocaleLowerCase().includes(normalizedNodeSearch));
  const pinnedKindOptions = allKindOptions.filter((option) => selectedNodes.includes(option.key));
  const kindOptions = [...matchingKindOptions.slice(0, 50)];
  pinnedKindOptions.forEach((option) => {
    if (!kindOptions.some((candidate) => candidate.key === option.key)) kindOptions.push(option);
  });
  const selectedOptions = selectedNodes
    .map((key) => optionMap.get(key))
    .filter((option): option is FlowNodeOption => option !== undefined);
  const tableVisibleCount = Math.min(filtered.length, MAX_PRESENTED_FLOW_ROWS);
  return (
    <section className="dashboard-flow-analysis" aria-labelledby="dashboard-flow-analysis-heading">
      <div className="dashboard-chart-heading dashboard-flow-heading">
        <div><h3 id="dashboard-flow-analysis-heading">{t('Traffic flow map')}</h3>
          <p className="muted">{t('Each lane follows one observed request path; bar length represents the selected metric.')}</p></div>
        <button
          aria-pressed={showSensitive}
          className="link"
          onClick={() => setShowSensitive((visibleValue) => !visibleValue)}
          type="button"
        >{showSensitive ? t('Hide sensitive labels') : t('Show sensitive labels')}</button>
      </div>
      <div className="dashboard-flow-controls">
        <label>{t('Metric')}<select value={metric} onChange={(event) => setMetric(event.target.value as DashboardMetric)}>
          {(Object.keys(METRIC_LABELS) as DashboardMetric[]).map((item) => (
            <option key={item} value={item}>{t(metricLabel(item))}</option>
          ))}
        </select></label>
        <label>{t('Node type')}<select value={selectedKind} onChange={(event) => setSelectedKind(event.target.value as FlowNodeKind)}>
          {availableKinds.map((kind) => <option key={kind} value={kind}>{t(FLOW_NODE_LABELS[kind])}</option>)}
        </select></label>
        <label>{t('Filter nodes')}<input
          autoComplete="off"
          maxLength={512}
          onChange={(event) => setNodeSearch(event.target.value)}
          placeholder={t('Search available nodes')}
          type="search"
          value={nodeSearch}
        /></label>
        {selectedNodes.length > 0 && <button className="link" type="button" onClick={() => setSelectedNodes([])}>
          {t('Clear node filters')}
        </button>}
      </div>
      {selectedOptions.length > 0 && <ul className="dashboard-active-filters" aria-label={t('Active node filters')}>
        {selectedOptions.map((option) => <li key={option.key}>
          <span>{t(FLOW_NODE_LABELS[option.kind])}:</span>
          <button
            aria-label={t('Remove {{kind}} filter {{name}}', {
              kind: t(FLOW_NODE_LABELS[option.kind]), name: presentedFlowOptionLabel(option, showSensitive),
            })}
            onClick={() => setSelectedNodes((current) => current.filter((key) => key !== option.key))}
            type="button"
          >{presentedFlowOptionLabel(option, showSensitive)} <span aria-hidden="true">×</span></button>
        </li>)}
      </ul>}
      <fieldset className="dashboard-node-filters">
        <legend>{t('Node filters')}</legend>
        {kindOptions.length === 0 ? <p className="muted">{t('No nodes are available.')}</p> : kindOptions.map((option) => (
          <label key={option.key}>
            <input
              checked={selectedNodes.includes(option.key)}
              onChange={() => setSelectedNodes((current) => current.includes(option.key)
                ? current.filter((key) => key !== option.key) : [...current, option.key])}
              type="checkbox"
            />
            <span>{presentedFlowOptionLabel(option, showSensitive)}</span>
            <small>{formatNumber(option.value)}</small>
          </label>
        ))}
        {matchingKindOptions.length > Math.min(matchingKindOptions.length, 50) && <p className="dashboard-limit-note muted">
          {t('Showing {{visible}} of {{total}} matching nodes. Search to narrow the list; selected nodes remain visible.', {
            visible: Math.min(matchingKindOptions.length, 50), total: matchingKindOptions.length,
          })}
        </p>}
      </fieldset>
      <p className="dashboard-filter-count muted" aria-live="polite">
        {t('Map shows {{visible}} of {{matched}} matching flow paths ({{total}} total); table shows {{tableVisible}}.', {
          visible: visible.length, matched: filtered.length, total: rows.length, tableVisible: tableVisibleCount,
        })}{' '}{t('{{value}} {{metric}} across matching paths.', {
          value: formatNumber(filteredMetricTotal), metric: t(metricLabel(metric)).toLocaleLowerCase(),
        })}
      </p>
      {!showSensitive && <p className="dashboard-filter-count muted">
        {t('Labels are hidden in this presentation; authorized network responses still contain the underlying values.')}
      </p>}
      {filtered.length === 0 ? <p className="dashboard-client-empty muted" role="status">
        {t('No flow paths match the selected nodes.')}
      </p> : <>
        <figure className="dashboard-flow-map">
          <figcaption className="sr-only">{t('Traffic flow map')}</figcaption>
          <ol>{visible.map((row, index) => {
            const pathNodes = flowPath(row, role, optionMap, showSensitive, t);
            const path = flowPathText(pathNodes);
            return <li key={index}>
              <div className="dashboard-flow-nodes" aria-label={path}>
                {pathNodes.map((node, nodeIndex) => <span key={nodeIndex}>{node}</span>)}
              </div>
              <div className="dashboard-flow-strength">
                <progress
                  aria-label={t('{{path}}: {{value}} {{metric}}', {
                    path, value: formatNumber(dashboardMetricValue(row, metric)),
                    metric: t(metricLabel(metric)).toLocaleLowerCase(),
                  })}
                  max={maximum}
                  value={dashboardMetricValue(row, metric)}
                />
                <output>{formatNumber(dashboardMetricValue(row, metric))}</output>
              </div>
            </li>;
          })}</ol>
        </figure>
        <FlowTable options={optionMap} role={role} rows={filtered} showSensitive={showSensitive} />
      </>}
    </section>
  );
}

function DashboardPresentation({ result, section, query, role }: {
  result: DashboardResult;
  section: DashboardSection;
  query: DashboardQuery;
  role: number;
}) {
  const { t } = useTranslation();
  const duration = query.endTimestamp - query.startTimestamp;
  const overviewAggregates = useMemo(() => result.kind === 'quota'
    ? aggregatePresentationRows(result.rows, (row) => modelDimension(row.modelName, t)).slice(0, 5)
    : [], [result, t]);
  const userCount = result.kind === 'users'
    ? new Set(result.rows.map((row) => userDimension(row.username, t).key)).size : 0;
  return (
    <>
      <SummaryCards
        durationSeconds={duration}
        extra={result.kind === 'users' ? [{ label: 'Active users', value: userCount }] : []}
        summary={result.summary}
      />
      {result.kind === 'flow' ? <FlowPresentation role={role} rows={result.rows} />
        : result.kind === 'users' ? <UsersPresentation query={query} rows={result.rows} />
          : section === 'models' ? <ModelsPresentation query={query} rows={result.rows} />
            : <AggregateTable rows={overviewAggregates} labelHeading="Model" caption="Top models" />}
      <p className="dashboard-scope-note muted">
        {t('All totals reflect only the selected date range and account scope.')}{' '}
        {t('Usage histograms may update with a short delay.')}
      </p>
    </>
  );
}

function presentedNotice(value: string): string {
  let presented = value.slice(0, MAX_PRESENTED_NOTICE_CHARACTERS);
  const last = presented.charCodeAt(presented.length - 1);
  if (last >= 0xd800 && last <= 0xdbff) presented = presented.slice(0, -1);
  return presented;
}

const UPTIME_STATUS_LABELS = ['Down', 'Operational', 'Pending', 'Maintenance'] as const;

function uptimePercentage(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 2, minimumFractionDigits: 2 })
    .format(value * 100);
}

function DashboardOverviewPanel({ refreshKey }: { refreshKey: number }) {
  const { t } = useTranslation();
  const [content, setContent] = useState<DashboardOverviewContent | null>(null);
  const [loading, setLoading] = useState(true);
  const [retryKey, setRetryKey] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setContent(null);
    const origin = typeof window === 'undefined' ? '' : window.location.origin;
    void loadDashboardOverview(origin, controller.signal).then((next) => {
      if (!controller.signal.aborted) setContent(next);
    }).catch(() => {
      if (!controller.signal.aborted) setContent({ notice: { ok: false }, gateway: { ok: false }, uptime: { ok: false } });
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [refreshKey, retryKey]);
  const gateway = content?.gateway.ok ? content.gateway.value : null;
  const endpoint = gateway?.serverAddress ? `${gateway.serverAddress}/v1` : '';
  return (
    <section className="dashboard-overview-content" aria-labelledby="dashboard-service-heading" aria-busy={loading}>
      <div className="dashboard-chart-heading">
        <h3 id="dashboard-service-heading">{t('Service information')}</h3>
        {!loading && content && Object.values(content).some((resource) => !resource.ok) && (
          <button className="link" onClick={() => setRetryKey((value) => value + 1)} type="button">{t('Try again')}</button>
        )}
      </div>
      {loading ? <p className="muted" role="status">{t('Loading service information…')}</p> : <div className="dashboard-overview-grid">
        <article>
          <h4>{t('Site notice')}</h4>
          {!content?.notice.ok ? <p className="error" role="alert">{t('Unable to load the site notice.')}</p>
            : content.notice.value === '' ? <p className="muted">{t('No site notice at this time.')}</p>
              : <><p className="dashboard-notice">{presentedNotice(content.notice.value)}</p>
                {content.notice.value.length > MAX_PRESENTED_NOTICE_CHARACTERS && <p className="muted dashboard-limit-note">
                  {t('The notice was shortened for this dashboard view.')}
                </p>}</>}
        </article>
        <article>
          <h4>{t('Gateway API')}</h4>
          {!gateway ? <p className="error" role="alert">{t('Unable to load gateway API information.')}</p> : <dl>
            <div><dt>{t('Gateway')}</dt><dd>{gateway.systemName || t('TokenRouter gateway')}</dd></div>
            <div><dt>{t('API endpoint')}</dt><dd>{endpoint ? <a href={endpoint}>{endpoint}</a> : t('Current domain')}</dd></div>
            {gateway.nodeName && <div><dt>{t('Node')}</dt><dd>{gateway.nodeName}</dd></div>}
            {gateway.version && <div><dt>{t('Version')}</dt><dd>{gateway.version}</dd></div>}
          </dl>}
        </article>
        {gateway?.apiInfoEnabled && <article>
          <h4>{t('API information')}</h4>
          {gateway.apiInfo.length === 0 ? <p className="muted">{t('No API information is configured.')}</p> : (
            <ul className="dashboard-content-list">
              {gateway.apiInfo.map((item, index) => <li key={item.id ?? `${item.url}-${index}`}>
                <span className={`dashboard-api-color dashboard-api-color-${item.color}`} aria-hidden="true" />
                <div>
                  <a href={item.url} target="_blank" rel="noreferrer">{item.route}</a>
                  <p>{item.description}</p>
                  <span className="muted">{item.url}</span>
                </div>
              </li>)}
            </ul>
          )}
        </article>}
        {gateway?.faqEnabled && <article>
          <h4>{t('FAQ')}</h4>
          {gateway.faq.length === 0 ? <p className="muted">{t('No FAQ entries are configured.')}</p> : (
            <div className="dashboard-faq-list">
              {gateway.faq.map((item, index) => <details key={item.id ?? `${item.question}-${index}`}>
                <summary>{item.question}</summary>
                <p>{item.answer}</p>
              </details>)}
            </div>
          )}
        </article>}
        {gateway?.uptimeKumaEnabled && <article className="dashboard-uptime-card">
          <h4>{t('Uptime Kuma')}</h4>
          {!content?.uptime.ok ? <p className="error" role="alert">{t('Unable to load Uptime Kuma status.')}</p>
            : content.uptime.value.length === 0 ? <p className="muted">{t('No uptime monitoring groups are configured.')}</p>
              : <div className="dashboard-uptime-groups">
                {content.uptime.value.map((group) => <section key={group.categoryName} aria-label={group.categoryName}>
                  <h5>{group.categoryName}</h5>
                  {group.monitors.length === 0 ? <p className="muted">{t('No monitors were returned for this group.')}</p> : (
                    <ul>
                      {group.monitors.map((monitor, index) => <li key={`${monitor.group}-${monitor.name}-${index}`}>
                        <span
                          className={`dashboard-status-dot status-${monitor.status}`}
                          role="img"
                          aria-label={t(UPTIME_STATUS_LABELS[monitor.status])}
                        />
                        <span>{monitor.name}{monitor.group ? ` (${monitor.group})` : ''}</span>
                        <strong>{t('{{uptime}}% uptime', { uptime: uptimePercentage(monitor.uptime) })}</strong>
                      </li>)}
                    </ul>
                  )}
                </section>)}
              </div>}
        </article>}
      </div>}
    </section>
  );
}

function DashboardPerformancePanel({ refreshKey }: { refreshKey: number }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<DashboardPerformanceRow[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [retryKey, setRetryKey] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setRows(null);
    setLoading(true);
    setFailed(false);
    void loadDashboardPerformance(controller.signal)
      .then((next) => {
        if (!controller.signal.aborted) setRows(next);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [refreshKey, retryKey]);

  const visible = rows?.slice(0, MAX_PRESENTED_PERFORMANCE_ROWS) ?? [];
  return (
    <section
      className="dashboard-performance"
      aria-labelledby="dashboard-performance-heading"
      aria-busy={loading}
      aria-live="polite"
    >
      <h3 id="dashboard-performance-heading">{t('Model performance (24h)')}</h3>
      {loading && <p className="muted" role="status">{t('Loading model performance…')}</p>}
      {!loading && failed && (
        <div className="dashboard-performance-state">
          <p className="error" role="alert">{t('Unable to load model performance.')}</p>
          <button
            type="button"
            aria-label={`${t('Try again')}: ${t('Model performance (24h)')}`}
            onClick={() => setRetryKey((value) => value + 1)}
          >
            {t('Try again')}
          </button>
        </div>
      )}
      {!loading && !failed && rows?.length === 0 && <p className="muted">{t('No performance data yet.')}</p>}
      {!loading && !failed && visible.length > 0 && (
        <div className="table-scroll">
          <table className="dashboard-table dashboard-performance-table">
            <caption className="sr-only">{t('Model performance (24h)')}</caption>
            <thead>
              <tr>
                <th scope="col">{t('Model')}</th>
                <th scope="col">{t('Latency')}</th>
                <th scope="col">{t('Success')}</th>
                <th scope="col">{t('Throughput')}</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => {
                const rate = formatDecimal(row.successRate);
                return (
                  <tr key={row.modelName}>
                    <th scope="row">{row.modelName}</th>
                    <td>{t('{{latency}} ms average latency', { latency: formatNumber(row.averageLatencyMs) })}</td>
                    <td>
                      <span>{t('{{rate}}% success', { rate })}</span>
                      <progress aria-label={`${row.modelName}: ${rate}%`} max={100} value={row.successRate} />
                    </td>
                    <td>{t('{{tps}} tokens/s', { tps: formatDecimal(row.averageTokensPerSecond) })}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {rows && rows.length > visible.length && (
            <p className="dashboard-limit-note muted">
              {t('Showing the top {{count}} results.', { count: visible.length })}
            </p>
          )}
        </div>
      )}
    </section>
  );
}

export function DashboardView({ section, role, search = '', onNavigate }: DashboardViewProps) {
  const { t } = useTranslation();
  const isAdmin = isAdministratorRole(role);
  const allowUsername = canFilterDashboardByUsername(section, role);
  const initialQueryRef = useRef<DashboardQuery | null>(null);
  if (initialQueryRef.current === null) {
    initialQueryRef.current = normalizeDashboardQuery(parseDashboardSearch(search), allowUsername);
  }
  const initialQuery = initialQueryRef.current;
  const [query, setQuery] = useState<DashboardQuery>(initialQuery);
  const [draft, setDraft] = useState<FilterDraft>(() => draftFromQuery(initialQuery));
  const [result, setResult] = useState<DashboardResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [filterError, setFilterError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);
  const previousSearch = useRef(search);
  const allowed = canAccessDashboardSection(section, role);
  const visibleSections = DASHBOARD_SECTIONS.filter((item) => canAccessDashboardSection(item, role));
  const effectiveQuery = useMemo(
    () => normalizeDashboardQuery(query, allowUsername),
    [allowUsername, query],
  );

  useEffect(() => {
    if (previousSearch.current === search) return;
    previousSearch.current = search;
    const next = normalizeDashboardQuery(parseDashboardSearch(search), allowUsername);
    setQuery((current) => (sameQuery(current, next) ? current : next));
    setDraft(draftFromQuery(next));
    setFilterError(false);
  }, [allowUsername, search]);

  useEffect(() => {
    if (!allowed) {
      generation.current += 1;
      setResult(null);
      setLoading(false);
      setLoadError(false);
      return undefined;
    }
    const controller = new AbortController();
    const requestGeneration = generation.current + 1;
    generation.current = requestGeneration;
    setLoading(true);
    setLoadError(false);
    setResult(null);
    void loadDashboardSection(section, role, effectiveQuery, controller.signal)
      .then((next) => {
        if (!controller.signal.aborted && generation.current === requestGeneration) setResult(next);
      })
      .catch(() => {
        if (!controller.signal.aborted && generation.current === requestGeneration) setLoadError(true);
      })
      .finally(() => {
        if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false);
      });
    return () => controller.abort();
  }, [allowed, attempt, effectiveQuery, role, section]);

  function navigate(event: MouseEvent<HTMLAnchorElement>, target: string): void {
    if (!onNavigate) return;
    event.preventDefault();
    onNavigate(target);
  }

  function applyFilters(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (loading || !allowed) return;
    try {
      const next = queryFromDraft(draft, allowUsername);
      setFilterError(false);
      setQuery(next);
      const target = dashboardQueryURL(section, role, next);
      previousSearch.current = target.slice(target.indexOf('?'));
      onNavigate?.(target);
    } catch {
      setFilterError(true);
    }
  }

  function resetFilters(): void {
    if (loading || !allowed) return;
    const next = defaultDashboardQuery();
    setDraft(draftFromQuery(next));
    setFilterError(false);
    setQuery(next);
    const target = dashboardQueryURL(section, role, next);
    previousSearch.current = target.slice(target.indexOf('?'));
    onNavigate?.(target);
  }

  function applyQuickRange(days: (typeof QUICK_RANGE_DAYS)[number]): void {
    if (loading || !allowed) return;
    const current = defaultDashboardQuery();
    const next = normalizeDashboardQuery({
      startTimestamp: current.endTimestamp - days * 86_400,
      endTimestamp: current.endTimestamp,
      username: allowUsername ? draft.username : '',
    }, allowUsername);
    setDraft(draftFromQuery(next));
    setFilterError(false);
    setQuery(next);
    const target = dashboardQueryURL(section, role, next);
    previousSearch.current = target.slice(target.indexOf('?'));
    onNavigate?.(target);
  }

  return (
    <main className="app dashboard-page">
      <header className="header dashboard-heading">
        <div>
          <h1>{t('Dashboard')}</h1>
          <p className="tagline">{t('Understand usage without exposing account or provider secrets.')}</p>
        </div>
        <button
          type="button"
          className="link"
          disabled={loading || !allowed}
          onClick={() => setAttempt((value) => value + 1)}
        >
          {loading ? t('Refreshing…') : t('Refresh')}
        </button>
      </header>

      <nav className="dashboard-sections" aria-label={t('Dashboard sections')}>
        {visibleSections.map((item) => {
          const target = dashboardQueryURL(item, role, effectiveQuery);
          return (
            <a
              aria-current={item === section ? 'page' : undefined}
              className={item === section ? 'active' : ''}
              href={target}
              key={item}
              onClick={(event) => navigate(event, target)}
            >
              {t(SECTION_LABELS[item])}
            </a>
          );
        })}
      </nav>

      <section className="card dashboard-panel" aria-labelledby="dashboard-section-heading">
        <div className="dashboard-panel-heading">
          <div>
            <h2 id="dashboard-section-heading">{t(SECTION_LABELS[section])}</h2>
            <p className="muted">{t(SECTION_DESCRIPTIONS[section])}</p>
          </div>
          {allowed && <span className="dashboard-scope-pill">
            {isAdmin && section !== 'overview' ? t('Administrator scope') : t('Your account only')}
          </span>}
        </div>

        {!allowed ? (
          <div className="dashboard-state dashboard-access-denied" role="alert">
            <h3>{t(section === 'users' ? 'Administrator access required' : 'Dashboard access unavailable')}</h3>
            <p>{t(section === 'users'
              ? 'User analytics is available only to administrators.'
              : 'This account role cannot access dashboard analytics.')}</p>
          </div>
        ) : (
          <>
            <form className="dashboard-filters" onSubmit={applyFilters} noValidate>
              <div className="dashboard-quick-ranges" role="group" aria-label={t('Quick range')}>
                <span>{t('Quick range')}</span>
                {QUICK_RANGE_DAYS.map((days) => (
                  <button key={days} onClick={() => applyQuickRange(days)} type="button">
                    {days === 1 ? t('1 Day') : t('{{days}} Days', { days })}
                  </button>
                ))}
              </div>
              <label>
                {t('Start time')}
                <input
                  max="2099-12-31T23:59:59"
                  min="1970-01-02T00:00:00"
                  onChange={(event) => setDraft((current) => ({ ...current, startTime: event.target.value }))}
                  required
                  step={1}
                  type="datetime-local"
                  value={draft.startTime}
                />
              </label>
              <label>
                {t('End time')}
                <input
                  max="2099-12-31T23:59:59"
                  min="1970-01-02T00:00:00"
                  onChange={(event) => setDraft((current) => ({ ...current, endTime: event.target.value }))}
                  required
                  step={1}
                  type="datetime-local"
                  value={draft.endTime}
                />
              </label>
              {allowUsername && (
                <label>
                  {t('Username (optional)')}
                  <input
                    autoComplete="off"
                    maxLength={64}
                    onChange={(event) => setDraft((current) => ({ ...current, username: event.target.value }))}
                    type="search"
                    value={draft.username}
                  />
                </label>
              )}
              <div className="dashboard-filter-actions">
                <button type="submit" disabled={loading}>{t('Apply filters')}</button>
                <button type="button" className="link" disabled={loading} onClick={resetFilters}>{t('Reset')}</button>
              </div>
              <p className="dashboard-filter-help muted">
                {t('Date ranges are limited to {{days}} days.', { days: DASHBOARD_MAX_RANGE_SECONDS / 86_400 })}
              </p>
              {filterError && (
                <p className="error dashboard-filter-error" role="alert">
                  {t('Choose a valid date range of no more than 30 days.')}
                </p>
              )}
            </form>

            <div className="dashboard-results" aria-busy={loading} aria-live="polite">
              {loading && <p className="dashboard-state muted" role="status">{t('Loading dashboard data…')}</p>}
              {!loading && loadError && (
                <div className="dashboard-state dashboard-error-panel">
                  <p className="error" role="alert">{t('Unable to load dashboard data.')}</p>
                  <button type="button" onClick={() => setAttempt((value) => value + 1)}>{t('Try again')}</button>
                </div>
              )}
              {!loading && !loadError && result && result.rows.length === 0 && (
                <p className="dashboard-state muted">{t('No usage data matches this date range.')}</p>
              )}
              {!loading && !loadError && result && result.rows.length > 0 && (
                <DashboardPresentation query={effectiveQuery} result={result} role={role} section={section} />
              )}
            </div>
            {section === 'overview' && <DashboardOverviewPanel refreshKey={attempt} />}
            {isAdmin && section === 'models' && <DashboardPerformancePanel refreshKey={attempt} />}
          </>
        )}
      </section>
    </main>
  );
}
