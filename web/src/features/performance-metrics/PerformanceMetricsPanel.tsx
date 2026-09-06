import { useEffect, useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { loadPerformanceMetrics } from './performance-api';
import type {
  PerformanceGroup,
  PerformanceMetrics,
  PerformanceModelSummary,
  PerformanceSeriesPoint,
} from './performance-metrics';
import './performance-metrics.css';

type PanelState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; performance: PerformanceMetrics };

type MetricKey = 'avg_ttft_ms' | 'avg_latency_ms' | 'success_rate' | 'avg_tps';

const METRICS: Array<{ key: MetricKey; label: 'TTFT' | 'Latency' | 'Success' | 'Throughput'; suffix: string }> = [
  { key: 'avg_ttft_ms', label: 'TTFT', suffix: 'ms' },
  { key: 'avg_latency_ms', label: 'Latency', suffix: 'ms' },
  { key: 'success_rate', label: 'Success', suffix: '%' },
  { key: 'avg_tps', label: 'Throughput', suffix: 'tokens/s' },
];

function formatMetric(value: number, suffix: string): string {
  const formatted = new Intl.NumberFormat(undefined, {
    maximumFractionDigits: suffix === 'ms' ? 0 : 2,
  }).format(value);
  return `${formatted}${suffix === '%' ? '%' : ` ${suffix}`}`;
}

function sparklinePoints(series: PerformanceSeriesPoint[], metric: MetricKey): string {
  const width = 320;
  const height = 88;
  const inset = 5;
  const values = series.map((point) => point[metric]);
  const minimum = Math.min(...values);
  const maximum = Math.max(...values);
  const range = maximum - minimum;
  const denominator = Math.max(1, series.length - 1);
  const points = series.map((point, index) => {
    const x = inset + (index / denominator) * (width - inset * 2);
    const y = range === 0
      ? height / 2
      : inset + ((maximum - point[metric]) / range) * (height - inset * 2);
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  });
  if (points.length === 1) return `${inset},${height / 2} ${width - inset},${height / 2}`;
  return points.join(' ');
}

function summarySparklinePoints(values: number[]): string {
  const width = 48;
  const height = 16;
  const inset = 1;
  const denominator = Math.max(1, values.length - 1);
  const points = values.map((value, index) => {
    const x = inset + (index / denominator) * (width - inset * 2);
    const y = inset + ((100 - value) / 100) * (height - inset * 2);
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  });
  if (points.length === 1) {
    const y = points[0].split(',')[1];
    return `${inset},${y} ${width - inset},${y}`;
  }
  return points.join(' ');
}

export function PerformanceMetricSparkline({
  group,
  metric,
  label,
  suffix,
  hours,
}: {
  group: PerformanceGroup;
  metric: MetricKey;
  label: string;
  suffix: string;
  hours: number;
}) {
  if (group.series.length === 0) return <span className="performance-no-series">—</span>;
  const latest = group.series.at(-1)?.[metric] ?? group[metric];
  const accessibleLabel = `${label} · ${group.group} · ${hours}h: ${formatMetric(latest, suffix)}`;
  return (
    <svg className="performance-sparkline" viewBox="0 0 320 88" role="img" aria-label={accessibleLabel}>
      <title>{accessibleLabel}</title>
      <line x1="5" y1="83" x2="315" y2="83" />
      <polyline points={sparklinePoints(group.series, metric)} />
    </svg>
  );
}

export function PerformanceSummaryBadge({ summary }: { summary?: PerformanceModelSummary }) {
  const { t } = useTranslation();
  if (!summary) return null;
  const label = `${t('Performance')}: ${t('Success')} ${formatMetric(summary.success_rate, '%')}, ${t('Latency')} ${formatMetric(summary.avg_latency_ms, 'ms')}`;
  return (
    <span className="performance-summary-badge" aria-label={label} title={label}>
      {summary.recent_success_rates.length > 0 && (
        <svg className="performance-summary-sparkline" viewBox="0 0 48 16" aria-hidden="true" focusable="false">
          <polyline points={summarySparklinePoints(summary.recent_success_rates)} />
        </svg>
      )}
      <span>{formatMetric(summary.success_rate, '%')}</span>
      <span>{formatMetric(summary.avg_latency_ms, 'ms')}</span>
    </span>
  );
}

export function PerformanceMetricsPanel({
  modelName,
  hours = 24,
  preferredGroup,
  groupLabels = {},
}: {
  modelName: string;
  hours?: number;
  preferredGroup?: string;
  groupLabels?: Record<string, string>;
}) {
  const { t } = useTranslation();
  const headingID = useId();
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<PanelState>({ status: 'loading' });
  const [selectedGroup, setSelectedGroup] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setState({ status: 'loading' });
    loadPerformanceMetrics({ modelName, hours, signal: controller.signal })
      .then((performance) => {
        if (!active) return;
        setState({ status: 'ready', performance });
        setSelectedGroup((previous) => {
          if (preferredGroup && performance.groups.some((group) => group.group === preferredGroup)) return preferredGroup;
          if (performance.groups.some((group) => group.group === previous)) return previous;
          return performance.groups[0]?.group ?? '';
        });
      })
      .catch(() => {
        if (active && !controller.signal.aborted) setState({ status: 'error' });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [attempt, hours, modelName, preferredGroup]);

  const activeGroup = useMemo(() => state.status === 'ready'
    ? state.performance.groups.find((group) => group.group === selectedGroup)
      ?? state.performance.groups[0]
    : undefined, [selectedGroup, state]);
  const heading = hours === 24 ? t('Performance (24h)') : `${t('Performance')} · ${hours}h`;

  return (
    <section className="card performance-panel" aria-labelledby={headingID} aria-busy={state.status === 'loading'}>
      <div className="performance-panel-heading">
        <h2 id={headingID}>{heading}</h2>
        {state.status === 'ready' && state.performance.groups.length > 1 && (
          <label>
            {t('Group')}
            <select value={activeGroup?.group ?? ''} onChange={(event) => setSelectedGroup(event.target.value)}>
              {state.performance.groups.map((group) => (
                <option value={group.group} key={group.group}>{groupLabels[group.group] || group.group}</option>
              ))}
            </select>
          </label>
        )}
      </div>
      {state.status === 'loading' && <p className="muted" role="status">{t('Loading model performance…')}</p>}
      {state.status === 'error' && (
        <div>
          <p className="error" role="alert">{t('Unable to load model performance.')}</p>
          <button type="button" onClick={() => setAttempt((value) => value + 1)}>{t('Try again')}</button>
        </div>
      )}
      {state.status === 'ready' && state.performance.groups.length === 0 && (
        <p className="muted">{t('Performance data is not yet available for this model.')}</p>
      )}
      {state.status === 'ready' && activeGroup && (
        <>
          <dl className="performance-stat-grid">
            {METRICS.map((metric) => (
              <div key={metric.key}>
                <dt>{t(metric.label)}</dt>
                <dd>{formatMetric(activeGroup[metric.key], metric.suffix)}</dd>
              </div>
            ))}
          </dl>
          <div className="performance-chart-grid">
            {METRICS.map((metric) => (
              <figure key={metric.key}>
                <figcaption>{t(metric.label)}</figcaption>
                <PerformanceMetricSparkline
                  group={activeGroup}
                  metric={metric.key}
                  label={t(metric.label)}
                  suffix={metric.suffix === 'tokens/s' ? t('tokens/s') : metric.suffix}
                  hours={hours}
                />
              </figure>
            ))}
          </div>
          <div className="table-scroll">
            <table className="performance-table">
              <caption className="sr-only">{heading}</caption>
              <thead><tr><th scope="col">{t('Group')}</th><th scope="col">{t('TTFT')}</th><th scope="col">{t('Latency')}</th><th scope="col">{t('Success')}</th><th scope="col">{t('Throughput')}</th></tr></thead>
              <tbody>{state.performance.groups.map((group) => (
                <tr key={group.group}>
                  <th scope="row">{groupLabels[group.group] || group.group}</th>
                  <td>{formatMetric(group.avg_ttft_ms, 'ms')}</td>
                  <td>{formatMetric(group.avg_latency_ms, 'ms')}</td>
                  <td>{formatMetric(group.success_rate, '%')}</td>
                  <td>{formatMetric(group.avg_tps, t('tokens/s'))}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </>
      )}
    </section>
  );
}
