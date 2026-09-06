import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadDashboardPerformance,
  type DashboardPerformanceRow
} from './dashboard-api';
import { MAX_PRESENTED_PERFORMANCE_ROWS, formatDecimal, formatNumber } from './dashboard-presentation';

export function DashboardPerformancePanel({ refreshKey }: { refreshKey: number }) {
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
