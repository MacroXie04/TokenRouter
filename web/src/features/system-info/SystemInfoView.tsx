import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  deleteStaleSystemInstance,
  deleteStaleSystemInstances,
  listSystemInstances,
  listSystemTasks,
  type SystemInstance,
  type SystemTask,
} from './system-info-api';

const INSTANCE_REFRESH_MS = 30_000;
const ACTIVE_TASK_REFRESH_MS = 8_000;

type Notice = { kind: 'success' | 'error'; text: string } | null;

function dateTime(timestamp: number, language: string): string {
  return new Intl.DateTimeFormat(language, {
    dateStyle: 'medium',
    timeStyle: 'medium',
  }).format(new Date(timestamp * 1_000));
}

function percent(value: number | undefined): string {
  return value === undefined ? '—' : `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(value)}%`;
}

function bytes(value: number | undefined): string {
  if (value === undefined) return '—';
  if (value === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: index === 0 ? 0 : 1 }).format(value / (1024 ** index))} ${units[index]}`;
}

function instanceDisplayName(instance: SystemInstance): string {
  return instance.info?.nodeName || instance.nodeName;
}

function runtime(instance: SystemInstance): string {
  const platform = [instance.info?.runtimeOS, instance.info?.runtimeArch].filter(Boolean).join('/');
  return [platform, instance.info?.runtimeVersion].filter(Boolean).join(' · ') || '—';
}

function resourceSummary(instance: SystemInstance): string {
  const info = instance.info;
  if (!info) return '—';
  const storageBytes = info.storageUsedBytes === undefined
    ? ''
    : ` (${bytes(info.storageUsedBytes)} / ${bytes(info.storageTotalBytes)})`;
  return `CPU ${percent(info.cpuPercent)} · RAM ${percent(info.memoryPercent)} · Disk ${percent(info.storageUsedPercent)}${storageBytes}`;
}

function InstancesPanel() {
  const { t, i18n } = useTranslation();
  const [instances, setInstances] = useState<SystemInstance[] | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [fetching, setFetching] = useState(false);
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    const controller = new AbortController();
    setFetching(true);
    setLoadError(false);
    void listSystemInstances(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setInstances(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setInstances(null);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setFetching(false);
      });
    return () => controller.abort();
  }, [reload]);

  useEffect(() => {
    const timer = window.setInterval(() => setReload((value) => value + 1), INSTANCE_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, []);

  const stale = useMemo(() => instances?.filter((instance) => instance.status === 'stale') ?? [], [instances]);

  async function removeOne(instance: SystemInstance) {
    if (busy || !window.confirm(t('Delete stale instance “{{name}}”? Only an expired registration can be removed.', {
      name: instanceDisplayName(instance),
    }))) return;
    setBusy(`one:${instance.nodeName}`);
    setNotice(null);
    try {
      await deleteStaleSystemInstance(instance.nodeName);
      setNotice({ kind: 'success', text: t('Stale instance deleted.') });
      setReload((value) => value + 1);
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete the stale instance.') });
    } finally {
      setBusy('');
    }
  }

  async function removeAll() {
    if (busy || stale.length === 0 || !window.confirm(t('Delete {{count}} stale instances? Only expired registrations will be removed.', {
      count: stale.length,
    }))) return;
    setBusy('all');
    setNotice(null);
    try {
      const deleted = await deleteStaleSystemInstances();
      setNotice({ kind: 'success', text: t('{{count}} stale instances deleted.', { count: deleted }) });
      setReload((value) => value + 1);
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete stale instances.') });
    } finally {
      setBusy('');
    }
  }

  return (
    <section className="card system-panel" aria-labelledby="system-instances-heading" aria-busy={fetching}>
      <div className="system-panel-heading">
        <div>
          <h2 id="system-instances-heading">{t('System instances')}</h2>
          <p className="muted">{t('Cluster registrations refresh automatically every 30 seconds.')}</p>
        </div>
        <div className="system-panel-actions">
          <button type="button" className="link" onClick={() => setReload((value) => value + 1)} disabled={fetching || Boolean(busy)}>
            {fetching && instances !== null ? t('Refreshing…') : t('Refresh')}
          </button>
          <button type="button" className="danger" onClick={() => void removeAll()} disabled={stale.length === 0 || Boolean(busy)}>
            {busy === 'all' ? t('Deleting…') : t('Delete all stale')}
          </button>
        </div>
      </div>

      {notice && <p role="status" className={notice.kind}>{notice.text}</p>}
      {fetching && instances === null ? (
        <p className="system-state muted" role="status">{t('Loading system instances…')}</p>
      ) : loadError ? (
        <div className="system-state" role="alert">
          <p className="error">{t('Unable to load system instances.')}</p>
          <button type="button" onClick={() => setReload((value) => value + 1)}>{t('Try again')}</button>
        </div>
      ) : instances?.length === 0 ? (
        <p className="system-state muted">{t('No instances have reported yet.')}</p>
      ) : (
        <div className="table-scroll">
          <table className="system-table">
            <thead>
              <tr>
                <th>{t('Instance')}</th>
                <th>{t('Status')}</th>
                <th>{t('Role')}</th>
                <th>{t('Runtime')}</th>
                <th>{t('Last seen')}</th>
                <th>{t('Resources')}</th>
                <th>{t('Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {instances?.map((instance) => (
                <tr key={instance.nodeName}>
                  <td>
                    <strong>{instanceDisplayName(instance)}</strong>
                    <span className="system-meta">{instance.info?.hostname || instance.nodeName}</span>
                    <span className="system-meta">{t('Started')}: {dateTime(instance.startedAt, i18n.language)}</span>
                  </td>
                  <td><span className={`status-pill ${instance.status}`}>{t(instance.status)}</span></td>
                  <td>{instance.info?.isMaster === true ? t('Master') : t('Worker')}</td>
                  <td>{runtime(instance)}</td>
                  <td>
                    <time dateTime={new Date(instance.lastSeenAt * 1_000).toISOString()}>
                      {dateTime(instance.lastSeenAt, i18n.language)}
                    </time>
                  </td>
                  <td className="system-resources">{resourceSummary(instance)}</td>
                  <td>
                    {instance.status === 'stale' ? (
                      <button
                        type="button"
                        className="link danger-link"
                        disabled={Boolean(busy)}
                        onClick={() => void removeOne(instance)}
                      >
                        {busy === `one:${instance.nodeName}` ? t('Deleting…') : t('Delete stale')}
                      </button>
                    ) : <span className="muted">—</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

const TASK_LABELS: Record<string, string> = {
  log_cleanup: 'Log cleanup',
  channel_test: 'Batch channel test',
  model_update: 'Batch upstream model update',
  midjourney_poll: 'Drawing task polling',
  async_task_poll: 'Async task polling',
};

function TaskTable({ tasks }: { tasks: SystemTask[] }) {
  const { t, i18n } = useTranslation();
  return (
    <div className="table-scroll">
      <table className="system-table system-task-table">
        <thead>
          <tr>
            <th>{t('Type')}</th>
            <th>{t('Status')}</th>
            <th>{t('Progress')}</th>
            <th>{t('Executor')}</th>
            <th>{t('Updated')}</th>
            <th>{t('Detail')}</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr key={task.taskId}>
              <td>
                <strong>{t(TASK_LABELS[task.type] ?? task.type)}</strong>
                <span className="system-meta">{task.type}</span>
              </td>
              <td><span className={`status-pill ${task.status}`}>{t(task.status)}</span></td>
              <td>
                <div className="task-progress">
                  <progress max={100} value={task.progress ?? 0} aria-label={t('Task progress')} />
                  <span>{task.progress === null ? '—' : `${task.progress}%`}</span>
                </div>
              </td>
              <td><span className="system-code">{task.lockedBy || '—'}</span></td>
              <td>
                <time dateTime={new Date(task.updatedAt * 1_000).toISOString()}>
                  {dateTime(task.updatedAt, i18n.language)}
                </time>
              </td>
              <td className={task.error ? 'error system-detail' : 'muted'} title={task.error || undefined}>
                {task.error || '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TasksPanel() {
  const { t } = useTranslation();
  const [tasks, setTasks] = useState<SystemTask[] | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [fetching, setFetching] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setFetching(true);
    setLoadError(false);
    void listSystemTasks(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setTasks(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setTasks(null);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setFetching(false);
      });
    return () => controller.abort();
  }, [reload]);

  const active = useMemo(() => tasks?.filter((task) => task.status === 'pending' || task.status === 'running') ?? [], [tasks]);
  const history = useMemo(() => tasks?.filter((task) => task.status !== 'pending' && task.status !== 'running') ?? [], [tasks]);

  useEffect(() => {
    if (active.length === 0) return undefined;
    const timer = window.setInterval(() => setReload((value) => value + 1), ACTIVE_TASK_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [active.length]);

  return (
    <section className="card system-panel" aria-labelledby="system-tasks-heading" aria-busy={fetching}>
      <div className="system-panel-heading">
        <div>
          <h2 id="system-tasks-heading">{t('System tasks')}</h2>
          <p className="muted">{t('Recent maintenance work across the cluster.')}</p>
        </div>
        <div className="system-panel-actions">
          <span className="muted system-live" aria-live="polite">
            {active.length > 0 ? t('Auto-refreshing while tasks are active.') : t('Live refresh paused.')}
          </span>
          <button type="button" className="link" onClick={() => setReload((value) => value + 1)} disabled={fetching}>
            {fetching && tasks !== null ? t('Refreshing…') : t('Refresh')}
          </button>
        </div>
      </div>

      {fetching && tasks === null ? (
        <p className="system-state muted" role="status">{t('Loading system tasks…')}</p>
      ) : loadError ? (
        <div className="system-state" role="alert">
          <p className="error">{t('Unable to load system tasks.')}</p>
          <button type="button" onClick={() => setReload((value) => value + 1)}>{t('Try again')}</button>
        </div>
      ) : tasks?.length === 0 ? (
        <p className="system-state muted">{t('No system tasks yet.')}</p>
      ) : (
        <div className="system-task-groups">
          <section aria-labelledby="active-system-tasks">
            <div className="system-group-heading">
              <h3 id="active-system-tasks">{t('Active tasks')}</h3>
              <span className="status-pill">{active.length}</span>
            </div>
            {active.length > 0 ? <TaskTable tasks={active} /> : <p className="system-state muted">{t('No active system tasks.')}</p>}
          </section>
          <section aria-labelledby="system-task-history">
            <div className="system-group-heading">
              <h3 id="system-task-history">{t('Task history')}</h3>
              <span className="status-pill">{history.length}</span>
            </div>
            {history.length > 0 ? <TaskTable tasks={history} /> : <p className="system-state muted">{t('No historical system tasks.')}</p>}
          </section>
        </div>
      )}
    </section>
  );
}

export function SystemInfoView({ onNavigate }: { onNavigate: (target: string) => void }) {
  const { t } = useTranslation();
  return (
    <main className="app system-info">
      <header className="header system-page-heading">
        <div>
          <div className="system-title-row">
            <h1>{t('System information')}</h1>
            <span className="status-pill">{t('Root')}</span>
          </div>
          <p className="tagline">{t('Monitor cluster health and distributed maintenance work.')}</p>
        </div>
        <button type="button" className="link" onClick={() => onNavigate('/dashboard')}>{t('Dashboard')}</button>
      </header>
      <InstancesPanel />
      <TasksPanel />
    </main>
  );
}
