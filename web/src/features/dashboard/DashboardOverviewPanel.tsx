import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadDashboardOverview,
  type DashboardOverviewContent
} from './dashboard-api';
import { MAX_PRESENTED_NOTICE_CHARACTERS } from './dashboard-presentation';

export function presentedNotice(value: string): string {
  let presented = value.slice(0, MAX_PRESENTED_NOTICE_CHARACTERS);
  const last = presented.charCodeAt(presented.length - 1);
  if (last >= 0xd800 && last <= 0xdbff) presented = presented.slice(0, -1);
  return presented;
}

export const UPTIME_STATUS_LABELS = ['Down', 'Operational', 'Pending', 'Maintenance'] as const;

export function uptimePercentage(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 2, minimumFractionDigits: 2 })
    .format(value * 100);
}

export function DashboardOverviewPanel({ refreshKey }: { refreshKey: number }) {
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
