import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ContainersDialog, DeploymentDetails } from './DeploymentDetails';
import { DeploymentCreator, DeploymentUpdater } from './DeploymentEditors';
import {
  DEPLOYMENT_PAGE_SIZE,
  createDeployment,
  deleteDeployment,
  extendDeployment,
  getDeployment,
  getDeploymentContainer,
  loadDeploymentContainers,
  loadDeploymentLogs,
  loadDeploymentSettings,
  loadDeployments,
  renameDeployment,
  testDeploymentConnection,
  updateDeployment,
  type DeploymentAccess,
  type DeploymentContainer,
  type DeploymentDetail,
  type DeploymentPage,
  type DeploymentQuery,
  type DeploymentStatus,
  type DeploymentSummary
} from './models-api';
import { ErrorPanel, LoadingPanel, NoticeBanner, Overlay, type Notice } from './models-ui';

export const EMPTY_DEPLOYMENT_QUERY: DeploymentQuery = {
  keyword: '',
  status: '',
  page: 1,
  pageSize: DEPLOYMENT_PAGE_SIZE,
};

export const DEPLOYMENT_STATUS_OPTIONS: DeploymentStatus[] = [
  '',
  'running',
  'completed',
  'failed',
  'deployment requested',
  'termination requested',
  'destroyed',
];

export function DeploymentSection() {
  const { t } = useTranslation();
  const [access, setAccess] = useState<DeploymentAccess | null>(null);
  const [accessPhase, setAccessPhase] = useState<'settings' | 'connection' | 'done'>('settings');
  const [accessError, setAccessError] = useState(false);
  const [connectionError, setConnectionError] = useState(false);
  const [accessReload, setAccessReload] = useState(0);
  const [draft, setDraft] = useState(EMPTY_DEPLOYMENT_QUERY);
  const [query, setQuery] = useState(EMPTY_DEPLOYMENT_QUERY);
  const [page, setPage] = useState<DeploymentPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [busyAction, setBusyAction] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [creatorOpen, setCreatorOpen] = useState(false);
  const [updateTarget, setUpdateTarget] = useState<DeploymentSummary | null>(null);
  const [renameTarget, setRenameTarget] = useState<DeploymentSummary | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [extendTarget, setExtendTarget] = useState<DeploymentSummary | null>(null);
  const [extendHours, setExtendHours] = useState(1);
  const [detail, setDetail] = useState<DeploymentDetail | null>(null);
  const [containerTarget, setContainerTarget] = useState<DeploymentSummary | null>(null);
  const [containers, setContainers] = useState<DeploymentContainer[]>([]);
  const [containerDetail, setContainerDetail] = useState<DeploymentContainer | null>(null);
  const [logs, setLogs] = useState<string | null>(null);
  const [containerError, setContainerError] = useState<{ kind: 'list' | 'detail' | 'logs'; containerId?: string } | null>(null);
  const [inspectionLoading, setInspectionLoading] = useState(false);
  const alive = useRef(true);
  const busyRef = useRef(false);
  const inspectionController = useRef<AbortController | null>(null);

  useEffect(() => () => {
    alive.current = false;
    inspectionController.current?.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setAccess(null);
    setAccessError(false);
    setConnectionError(false);
    setAccessPhase('settings');
    void loadDeploymentSettings(controller.signal).then(async (result) => {
      if (controller.signal.aborted) return;
      setAccess(result);
      if (!result.canConnect) {
        setAccessPhase('done');
        return;
      }
      setAccessPhase('connection');
      try {
        await testDeploymentConnection(controller.signal);
        if (!controller.signal.aborted) setAccessPhase('done');
      } catch {
        if (!controller.signal.aborted) {
          setConnectionError(true);
          setAccessPhase('done');
        }
      }
    }).catch(() => {
      if (!controller.signal.aborted) {
        setAccessError(true);
        setAccessPhase('done');
      }
    });
    return () => controller.abort();
  }, [accessReload]);

  const ready = access?.canConnect === true && accessPhase === 'done' && !connectionError;

  useEffect(() => {
    if (!ready) return;
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void loadDeployments(query, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      setPage(result);
      const lastPage = Math.max(1, Math.ceil(result.total / query.pageSize));
      if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
    }).catch(() => {
      if (!controller.signal.aborted) {
        setPage(null);
        setLoadError(true);
      }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [query, ready, reload]);

  const pageCount = Math.max(1, Math.ceil((page?.total ?? 0) / query.pageSize));

  async function runMutation(action: string, operation: () => Promise<void>, success: string): Promise<boolean> {
    if (busyRef.current) return false;
    busyRef.current = true;
    setBusyAction(action);
    setNotice(null);
    try {
      await operation();
      if (!alive.current) return true;
      setNotice({ kind: 'success', text: success });
      setReload((value) => value + 1);
      return true;
    } catch {
      if (alive.current) setNotice({ kind: 'error', text: t('The deployment operation could not be completed.') });
      return false;
    } finally {
      busyRef.current = false;
      if (alive.current) setBusyAction('');
    }
  }

  function inspect(operation: (signal: AbortSignal) => Promise<void>, onError?: () => void) {
    inspectionController.current?.abort();
    const controller = new AbortController();
    inspectionController.current = controller;
    setInspectionLoading(true);
    setNotice(null);
    void operation(controller.signal).catch(() => {
      if (!controller.signal.aborted) {
        if (onError) onError();
        else setNotice({ kind: 'error', text: t('Unable to load deployment details.') });
      }
    }).finally(() => { if (!controller.signal.aborted) setInspectionLoading(false); });
  }

  function showDetail(id: string) {
    inspect(async (signal) => {
      const result = await getDeployment(id, signal);
      if (!signal.aborted) setDetail(result);
    });
  }

  function showContainers(deployment: DeploymentSummary) {
    setContainerTarget(deployment);
    setContainers([]);
    setContainerDetail(null);
    setLogs(null);
    setContainerError(null);
    inspect(async (signal) => {
      const result = await loadDeploymentContainers(deployment.id, signal);
      if (!signal.aborted) setContainers(result);
    }, () => setContainerError({ kind: 'list' }));
  }

  function showContainerDetail(containerId: string) {
    if (!containerTarget) return;
    setContainerDetail(null);
    setLogs(null);
    setContainerError(null);
    inspect(async (signal) => {
      const result = await getDeploymentContainer(containerTarget.id, containerId, signal);
      if (!signal.aborted) setContainerDetail(result);
    }, () => setContainerError({ kind: 'detail', containerId }));
  }

  function showLogs(containerId: string) {
    if (!containerTarget) return;
    setContainerDetail(null);
    setLogs(null);
    setContainerError(null);
    inspect(async (signal) => {
      const result = await loadDeploymentLogs(containerTarget.id, containerId, signal);
      if (!signal.aborted) setLogs(result);
    }, () => setContainerError({ kind: 'logs', containerId }));
  }

  function retryContainerRequest() {
    if (!containerTarget || !containerError) return;
    const failed = containerError;
    if (failed.kind === 'list') showContainers(containerTarget);
    else if (failed.kind === 'detail' && failed.containerId) showContainerDetail(failed.containerId);
    else if (failed.kind === 'logs' && failed.containerId) showLogs(failed.containerId);
  }

  if (accessPhase === 'settings' || accessPhase === 'connection') {
    return <LoadingPanel label={accessPhase === 'settings' ? t('Loading deployment settings…') : t('Checking deployment connection…')} />;
  }
  if (accessError) return <ErrorPanel message={t('Unable to verify deployment settings.')} retry={() => setAccessReload((value) => value + 1)} />;
  if (!access?.enabled) {
    return <div className="models-access-state" role="status"><h1>{t('Model deployment is disabled')}</h1><p>{t('Enable io.net deployment in model settings before using this page.')}</p></div>;
  }
  if (!access.configured) {
    return <div className="models-access-state" role="status"><h1>{t('Model deployment is not configured')}</h1><p>{t('Add a valid io.net API key in model settings. The key is never shown here.')}</p></div>;
  }
  if (connectionError) {
    return <ErrorPanel message={t('The deployment provider connection could not be verified.')} retry={() => setAccessReload((value) => value + 1)} />;
  }

  return (
    <section className="models-section" aria-labelledby="models-deployments-title">
      <div className="models-section-heading"><div><h1 id="models-deployments-title">{t('Deployments')}</h1><p>{t('Create and operate io.net model containers.')}</p></div><button type="button" className="primary" onClick={() => setCreatorOpen(true)}>{t('Create deployment')}</button></div>
      <NoticeBanner notice={notice} />
      {inspectionLoading && <p role="status" className="models-inline-progress">{t('Loading deployment data…')}</p>}
      <form className="models-filters deployment" role="search" aria-label={t('Search deployments')} onSubmit={(event) => { event.preventDefault(); setQuery({ ...draft, keyword: draft.keyword.trim(), page: 1 }); }}>
        <label>{t('Deployment name')}<input maxLength={256} value={draft.keyword} onChange={(e) => setDraft({ ...draft, keyword: e.target.value })} /></label>
        <label>{t('Status')}<select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as DeploymentStatus })}>{DEPLOYMENT_STATUS_OPTIONS.map((status) => <option key={status || 'all'} value={status}>{status ? t(status) : t('All statuses')}</option>)}</select></label>
        <div className="models-filter-actions"><button type="submit" className="primary">{t('Apply filters')}</button><button type="button" onClick={() => { setDraft(EMPTY_DEPLOYMENT_QUERY); setQuery(EMPTY_DEPLOYMENT_QUERY); }}>{t('Clear filters')}</button></div>
      </form>
      {loading ? <LoadingPanel label={t('Loading deployments…')} /> : loadError ? <ErrorPanel message={t('Unable to load deployments.')} retry={() => setReload((value) => value + 1)} /> : page && page.items.length > 0 ? (
        <div className="models-table-wrap"><table className="models-table" aria-busy="false"><caption>{t('Model deployments')}</caption><thead><tr><th scope="col">{t('Deployment')}</th><th scope="col">{t('Status')}</th><th scope="col">{t('Hardware')}</th><th scope="col">{t('Remaining')}</th><th scope="col">{t('Progress')}</th><th scope="col">{t('Actions')}</th></tr></thead><tbody>
          {page.items.map((deployment) => <tr key={deployment.id}><th scope="row"><span className="models-primary-value">{deployment.name}</span><span className="models-secondary-value">{deployment.id}</span></th><td><span className={`models-status deployment-${deployment.status.replaceAll(' ', '-')}`}>{t(deployment.status)}</span></td><td>{deployment.hardwareInfo}</td><td>{deployment.timeRemaining}</td><td><progress max={100} value={deployment.completedPercent} aria-label={t('Deployment progress for {{name}}', { name: deployment.name })} /> {deployment.completedPercent}%</td><td><div className="models-row-actions">
            <button type="button" onClick={() => showDetail(deployment.id)} disabled={inspectionLoading}>{t('Details')}</button>
            <button type="button" onClick={() => showContainers(deployment)} disabled={inspectionLoading}>{t('Containers')}</button>
            <button type="button" onClick={() => setUpdateTarget(deployment)} disabled={Boolean(busyAction)}>{t('Edit configuration')}</button>
            <button type="button" onClick={() => { setRenameTarget(deployment); setRenameValue(deployment.name); }}>{t('Rename')}</button>
            <button type="button" onClick={() => { setExtendTarget(deployment); setExtendHours(1); }}>{t('Extend')}</button>
            <button type="button" className="danger" disabled={Boolean(busyAction)} onClick={() => { if (window.confirm(t('Delete deployment “{{name}}”?', { name: deployment.name }))) void runMutation(`delete:${deployment.id}`, () => deleteDeployment(deployment.id), t('Deployment termination requested.')); }}>{t('Delete')}</button>
          </div></td></tr>)}
        </tbody></table></div>
      ) : <div className="models-state"><p>{t('No deployments match these filters.')}</p></div>}
      <nav className="models-pagination" aria-label={t('Deployment pages')}><button type="button" disabled={loading || query.page <= 1} onClick={() => setQuery((value) => ({ ...value, page: value.page - 1 }))}>{t('Previous')}</button><span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span><button type="button" disabled={loading || query.page >= pageCount} onClick={() => setQuery((value) => ({ ...value, page: value.page + 1 }))}>{t('Next')}</button></nav>

      {creatorOpen && <DeploymentCreator busy={Boolean(busyAction)} onCancel={() => setCreatorOpen(false)} onSave={async (input) => { const ok = await runMutation('create', () => createDeployment(input), t('Deployment created.')); if (ok && alive.current) setCreatorOpen(false); }} />}
      {updateTarget && <DeploymentUpdater deployment={updateTarget} busy={Boolean(busyAction)} onCancel={() => setUpdateTarget(null)} onSave={async (input) => { const target = updateTarget; const ok = await runMutation('update', () => updateDeployment(target.id, input), t('Deployment updated.')); if (ok && alive.current) setUpdateTarget(null); }} />}
      {renameTarget && <Overlay title={t('Rename deployment')} onClose={() => setRenameTarget(null)} busy={Boolean(busyAction)}><form className="models-form" aria-label={t('Rename deployment')} onSubmit={(event) => { event.preventDefault(); const target = renameTarget; void runMutation('rename', () => renameDeployment(target.id, renameValue), t('Deployment renamed.')).then((ok) => { if (ok && alive.current) setRenameTarget(null); }); }}><label>{t('New deployment name')}<input autoFocus required maxLength={128} value={renameValue} onChange={(e) => setRenameValue(e.target.value)} /></label><footer className="models-dialog-actions"><button type="button" onClick={() => setRenameTarget(null)} disabled={Boolean(busyAction)}>{t('Cancel')}</button><button type="submit" className="primary" disabled={Boolean(busyAction)}>{busyAction === 'rename' ? t('Saving…') : t('Rename')}</button></footer></form></Overlay>}
      {extendTarget && <Overlay title={t('Extend deployment')} onClose={() => setExtendTarget(null)} busy={Boolean(busyAction)}><form className="models-form" aria-label={t('Extend deployment')} onSubmit={(event) => { event.preventDefault(); const target = extendTarget; if (!window.confirm(t('Extend deployment “{{name}}” by {{hours}} hours?', { name: target.name, hours: extendHours }))) return; void runMutation('extend', () => extendDeployment(target.id, extendHours), t('Deployment extended.')).then((ok) => { if (ok && alive.current) setExtendTarget(null); }); }}><label>{t('Additional hours')}<input autoFocus required type="number" min={1} max={43_920} value={extendHours} onChange={(e) => setExtendHours(Number(e.target.value))} /></label><footer className="models-dialog-actions"><button type="button" onClick={() => setExtendTarget(null)} disabled={Boolean(busyAction)}>{t('Cancel')}</button><button type="submit" className="primary" disabled={Boolean(busyAction)}>{busyAction === 'extend' ? t('Extending…') : t('Extend')}</button></footer></form></Overlay>}
      {detail && <DeploymentDetails detail={detail} onClose={() => setDetail(null)} />}
      {containerTarget && <ContainersDialog deployment={containerTarget} containers={containers} selected={containerDetail} logs={logs} loading={inspectionLoading} error={containerError !== null} onRetry={retryContainerRequest} onClose={() => { inspectionController.current?.abort(); setContainerTarget(null); setContainers([]); setContainerDetail(null); setLogs(null); setContainerError(null); setInspectionLoading(false); }} onDetail={showContainerDetail} onLogs={showLogs} />}
    </section>
  );
}
