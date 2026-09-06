import { useTranslation } from 'react-i18next';
import {
  type DeploymentContainer,
  type DeploymentDetail,
  type DeploymentSummary
} from './models-api';
import { Overlay, formatDate, formatNumber } from './models-ui';

export function DeploymentDetails({ detail, onClose }: { detail: DeploymentDetail; onClose: () => void }) {
  const { t } = useTranslation();
  return <Overlay title={t('Deployment details')} onClose={onClose}><dl className="models-detail-grid"><div><dt>{t('Deployment ID')}</dt><dd>{detail.id}</dd></div><div><dt>{t('Status')}</dt><dd>{detail.status}</dd></div><div><dt>{t('Hardware')}</dt><dd>{detail.brandName} {detail.hardwareName} (#{detail.hardwareId})</dd></div><div><dt>{t('GPUs')}</dt><dd>{detail.totalGPUs} · {detail.GPUsPerContainer} {t('per container')}</dd></div><div><dt>{t('Containers')}</dt><dd>{detail.totalContainers}</dd></div><div><dt>{t('Progress')}</dt><dd>{detail.completedPercent}%</dd></div><div><dt>{t('Minutes remaining')}</dt><dd>{formatNumber(detail.computeMinutesRemaining)}</dd></div><div><dt>{t('Amount paid')}</dt><dd>{detail.amountPaid}</dd></div><div><dt>{t('Created')}</dt><dd>{formatDate(detail.createdAt)}</dd></div></dl><footer className="models-dialog-actions"><button type="button" onClick={onClose}>{t('Close')}</button></footer></Overlay>;
}

export function ContainersDialog({ deployment, containers, selected, logs, loading, error = false, onClose, onDetail, onLogs, onRetry }: {
  deployment: DeploymentSummary;
  containers: DeploymentContainer[];
  selected: DeploymentContainer | null;
  logs: string | null;
  loading: boolean;
  onClose: () => void;
  onDetail: (id: string) => void;
  onLogs: (id: string) => void;
  error: boolean;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Overlay title={t('Containers for {{name}}', { name: deployment.name })} onClose={onClose}>
      {loading && <p role="status" className="models-inline-progress">{t('Loading container data…')}</p>}
      {error && <div className="models-inline-error" role="alert"><span>{t('Unable to load container data.')}</span><button type="button" onClick={onRetry}>{t('Retry')}</button></div>}
      {containers.length === 0 && !loading && !error ? <p className="models-empty-note">{t('No containers found.')}</p> : <ul className="models-container-list">{containers.map((container) => <li key={container.containerId}><div><strong>{container.containerId}</strong><span>{container.status} · {container.hardware || t('Unknown hardware')}</span></div><div className="models-row-actions"><button type="button" onClick={() => onDetail(container.containerId)} disabled={loading}>{t('Details')}</button><button type="button" onClick={() => onLogs(container.containerId)} disabled={loading}>{t('Logs')}</button></div></li>)}</ul>}
      {selected && <section className="models-subpanel" aria-label={t('Container details')}><h3>{t('Container details')}</h3><dl><div><dt>{t('Container ID')}</dt><dd>{selected.containerId}</dd></div><div><dt>{t('Device ID')}</dt><dd>{selected.deviceId || '—'}</dd></div><div><dt>{t('Uptime')}</dt><dd>{selected.uptimePercent}%</dd></div><div><dt>{t('GPUs per container')}</dt><dd>{selected.GPUsPerContainer}</dd></div></dl>{selected.publicURL && <a href={selected.publicURL} target="_blank" rel="noreferrer">{t('Open public container URL')}</a>}<h4>{t('Container events')}</h4>{selected.events.length === 0 ? <p className="models-empty-note">{t('No container events.')}</p> : <div className="models-table-wrap compact"><table className="models-table models-events-table"><caption>{t('Container events')}</caption><thead><tr><th scope="col">{t('Time')}</th><th scope="col">{t('Message')}</th></tr></thead><tbody>{selected.events.map((event, index) => <tr key={`${event.time}:${index}`}><td>{formatDate(event.time)}</td><td><pre>{event.message}</pre></td></tr>)}</tbody></table></div>}</section>}
      {logs !== null && <section className="models-subpanel" aria-label={t('Container logs')}><h3>{t('Container logs')}</h3><pre className="models-logs">{logs || t('No log output.')}</pre></section>}
      <footer className="models-dialog-actions"><button type="button" onClick={onClose}>{t('Close')}</button></footer>
    </Overlay>
  );
}
