import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import {
  dashboardMetricValue,
  type DashboardAggregate,
  type DashboardGranularity,
  type DashboardMetric,
  type DashboardSummary
} from './dashboard-api';
import { formatBucket, formatDecimal, formatNumber, MAX_PRESENTED_AGGREGATES, METRIC_LABELS, metricLabel, TOP_LIMITS, type AnalyticsPreferences, type TimelineDataset } from './dashboard-presentation';

export function SummaryCards({ summary, durationSeconds, extra = [] }: {
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

export function AggregateTable({ rows, labelHeading, caption, metric = 'quota', limit = MAX_PRESENTED_AGGREGATES }: {
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

export function AnalyticsControls({ preferences, onChange, search, onSearch, label }: {
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

export function DistributionBars({ rows, metric, title, limit }: {
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

export function TimelineChart({ dataset, metric, title, granularity, note }: {
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
