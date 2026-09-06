import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  cleanupPerformanceLogFiles,
  isAbortError,
  loadCurrentLogCleanupTask,
  loadLogCleanupTask,
  loadPerformanceLogSummary,
  startLogCleanupTask,
  type LogCleanupTask,
  type PerformanceLogCleanupMode,
  type PerformanceLogSummary,
} from './system-settings-api';

const LOG_CLEANUP_POLL_MS = 2_000;
const MAX_LOG_CLEANUP_VALUE = 36_500;
const THIRTY_DAYS_MS = 30 * 24 * 60 * 60 * 1_000;

type Notice = { kind: 'success' | 'error'; text: string } | null;
type Confirmation = { kind: 'history'; target: number } | { kind: 'files'; mode: PerformanceLogCleanupMode; value: number } | null;

function localDateTimeValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function parsePastTimestamp(value: string): number | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/u.test(value)) return null;
  const milliseconds = new Date(value).getTime();
  if (!Number.isFinite(milliseconds) || milliseconds < 1_000 || milliseconds > Date.now()) return null;
  return Math.floor(milliseconds / 1_000);
}

function parseCleanupValue(value: string): number | null {
  if (!/^[1-9]\d{0,4}$/u.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed <= MAX_LOG_CLEANUP_VALUE ? parsed : null;
}

function formatBytes(value: number): string {
  if (value === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1_024)), units.length - 1);
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: index === 0 ? 0 : 1 }).format(value / (1_024 ** index))} ${units[index]}`;
}

function formatTime(value: string | undefined, language: string): string {
  if (!value) return '—';
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}

function activeTask(task: LogCleanupTask | null): boolean {
  return task?.status === 'pending' || task?.status === 'running';
}

export function LogMaintenancePanel() {
  const { t, i18n } = useTranslation();
  const mounted = useRef(true);
  const fileLoad = useRef<AbortController | null>(null);
  const taskLoad = useRef<AbortController | null>(null);
  const mutation = useRef<AbortController | null>(null);
  const [summary, setSummary] = useState<PerformanceLogSummary | null>(null);
  const [filesLoading, setFilesLoading] = useState(true);
  const [filesError, setFilesError] = useState(false);
  const [task, setTask] = useState<LogCleanupTask | null>(null);
  const [taskLoading, setTaskLoading] = useState(true);
  const [taskError, setTaskError] = useState(false);
  const [historyTarget, setHistoryTarget] = useState(() => localDateTimeValue(new Date(Date.now() - THIRTY_DAYS_MS)));
  const [fileMode, setFileMode] = useState<PerformanceLogCleanupMode>('by_count');
  const [fileValue, setFileValue] = useState('10');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const [confirmation, setConfirmation] = useState<Confirmation>(null);
  const latestLocalTime = useMemo(() => localDateTimeValue(new Date()), []);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      fileLoad.current?.abort();
      taskLoad.current?.abort();
      mutation.current?.abort();
    };
  }, []);

  const refreshFiles = useCallback(async () => {
    fileLoad.current?.abort();
    const request = new AbortController();
    fileLoad.current = request;
    setFilesLoading(true);
    setFilesError(false);
    try {
      const next = await loadPerformanceLogSummary(request.signal);
      if (mounted.current && !request.signal.aborted) setSummary(next);
    } catch (error) {
      if (mounted.current && !request.signal.aborted && !isAbortError(error)) {
        setSummary(null);
        setFilesError(true);
      }
    } finally {
      if (mounted.current && !request.signal.aborted) setFilesLoading(false);
    }
  }, []);

  const refreshCurrentTask = useCallback(async () => {
    taskLoad.current?.abort();
    const request = new AbortController();
    taskLoad.current = request;
    setTaskLoading(true);
    setTaskError(false);
    try {
      const next = await loadCurrentLogCleanupTask(request.signal);
      if (mounted.current && !request.signal.aborted) setTask(next);
    } catch (error) {
      if (mounted.current && !request.signal.aborted && !isAbortError(error)) setTaskError(true);
    } finally {
      if (mounted.current && !request.signal.aborted) setTaskLoading(false);
    }
  }, []);

  useEffect(() => { void refreshFiles(); }, [refreshFiles]);
  useEffect(() => { void refreshCurrentTask(); }, [refreshCurrentTask]);

  const activeTaskId = task && activeTask(task) ? task.taskId : null;

  useEffect(() => {
    if (!activeTaskId) return undefined;
    let cancelled = false;
    let timer: number | undefined;
    const request = new AbortController();
    const poll = async () => {
      let shouldContinue = true;
      try {
        const next = await loadLogCleanupTask(activeTaskId, request.signal);
        if (!cancelled && !request.signal.aborted) {
          setTask(next);
          setTaskError(false);
          shouldContinue = activeTask(next);
          if (next.status === 'succeeded') {
            const deletedCount = next.deletedCount ?? 0;
            setNotice({
              kind: 'success',
              text: deletedCount === 0
                ? t('No usage-history entries needed cleanup.')
                : t('{{count}} usage-history entries deleted.', { count: deletedCount }),
            });
          } else if (next.status === 'failed') {
            setNotice({ kind: 'error', text: t('Usage-history cleanup failed.') });
          }
        }
      } catch (error) {
        if (!cancelled && !request.signal.aborted && !isAbortError(error)) setTaskError(true);
      }
      if (!cancelled && !request.signal.aborted && shouldContinue) {
        timer = window.setTimeout(() => void poll(), LOG_CLEANUP_POLL_MS);
      }
    };
    timer = window.setTimeout(() => void poll(), LOG_CLEANUP_POLL_MS);
    return () => {
      cancelled = true;
      request.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [activeTaskId, t]);

  function requestHistoryCleanup() {
    const target = parsePastTimestamp(historyTarget);
    if (target === null) {
      setNotice({ kind: 'error', text: t('Choose a valid past date and time.') });
      return;
    }
    setNotice(null);
    setConfirmation({ kind: 'history', target });
  }

  function requestFileCleanup() {
    const value = parseCleanupValue(fileValue);
    if (value === null) {
      setNotice({ kind: 'error', text: t('Enter a whole number from 1 to 36500.') });
      return;
    }
    setNotice(null);
    setConfirmation({ kind: 'files', mode: fileMode, value });
  }

  async function confirmCleanup() {
    if (!confirmation || busy) return;
    const selected = confirmation;
    mutation.current?.abort();
    const request = new AbortController();
    mutation.current = request;
    setBusy(true);
    setNotice(null);
    try {
      if (selected.kind === 'history') {
        const next = await startLogCleanupTask(selected.target, request.signal);
        if (!mounted.current || request.signal.aborted) return;
        setTask(next);
        setTaskError(false);
        setNotice({ kind: 'success', text: t('Usage-history cleanup started.') });
      } else {
        const result = await cleanupPerformanceLogFiles(selected.mode, selected.value, request.signal);
        if (!mounted.current || request.signal.aborted) return;
        setNotice({
          kind: 'success',
          text: result.deletedCount === 0
            ? t('No local log files needed cleanup.')
            : t('{{count}} local log files deleted; {{size}} freed.', {
              count: result.deletedCount,
              size: formatBytes(result.freedBytes),
            }),
        });
        await refreshFiles();
      }
      if (mounted.current && !request.signal.aborted) setConfirmation(null);
    } catch (error) {
      if (!isAbortError(error) && mounted.current) {
        setNotice({
          kind: 'error',
          text: selected.kind === 'history'
            ? t('Unable to start usage-history cleanup.')
            : t('Unable to clean local log files.'),
        });
      }
    } finally {
      if (mounted.current && !request.signal.aborted) setBusy(false);
    }
  }

  const taskStatus = task ? t(task.status) : t('No cleanup task is running.');
  const confirmationDescription = confirmation?.kind === 'history'
    ? t('Currently stored log-sink entries created before {{date}} will be permanently deleted. This action cannot be undone.', {
      date: new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' })
        .format(new Date(confirmation.target * 1_000)),
    })
    : confirmation?.mode === 'by_count'
      ? t('Only the newest {{value}} matching log files will be kept. Older files will be permanently deleted.', { value: confirmation.value })
      : confirmation
        ? t('Matching log files older than {{value}} days will be permanently deleted.', { value: confirmation.value })
        : '';

  return (
    <>
      <section className="system-settings-tool" aria-labelledby="usage-log-cleanup-title">
        <header className="system-settings-tool-heading">
          <div>
            <h3 id="usage-log-cleanup-title">{t('Usage-history cleanup')}</h3>
            <p>{t('Consumption and audit logs remain enabled so settled usage always has a durable record.')}</p>
          </div>
          <button type="button" disabled={taskLoading || busy} onClick={() => void refreshCurrentTask()}>{t('Refresh')}</button>
        </header>
        <div className="system-settings-form">
          <label>
            {t('Delete entries created before')}
            <input type="datetime-local" max={latestLocalTime} value={historyTarget} disabled={busy || activeTask(task)} onChange={(event) => setHistoryTarget(event.target.value)} />
          </label>
          <div className="system-settings-row-actions">
            <button type="button" className="danger" disabled={busy || activeTask(task)} onClick={requestHistoryCleanup}>
              {busy && confirmation?.kind === 'history' ? t('Starting…') : t('Start usage-history cleanup')}
            </button>
          </div>
        </div>
        {taskLoading ? <p role="status">{t('Loading cleanup status…')}</p> : taskError ? (
          <div className="system-settings-inline-error" role="alert">
            <span>{t('Unable to load cleanup status.')}</span>
            <button type="button" onClick={() => void refreshCurrentTask()}>{t('Retry')}</button>
          </div>
        ) : (
          <div className="system-settings-task-status" aria-live="polite">
            <div><strong>{t('Cleanup status')}</strong><span>{taskStatus}</span></div>
            {task && (
              <>
                <progress max={100} value={task.progress} aria-label={t('Cleanup progress')} />
                <p>{t('{{processed}} of {{total}} entries processed.', { processed: task.processed, total: task.total })}</p>
              </>
            )}
          </div>
        )}
      </section>

      <section className="system-settings-tool" aria-labelledby="local-log-cleanup-title">
        <header className="system-settings-tool-heading">
          <div>
            <h3 id="local-log-cleanup-title">{t('Local log files')}</h3>
            <p>{t('Inspect and clean bounded oneapi-*.log files in the deployment-configured directory.')}</p>
          </div>
          <button type="button" disabled={filesLoading || busy} onClick={() => void refreshFiles()}>{t('Refresh')}</button>
        </header>
        {filesLoading ? <p role="status">{t('Loading local log files…')}</p> : filesError || !summary ? (
          <div className="system-settings-inline-error" role="alert">
            <span>{t('Unable to load local log files.')}</span>
            <button type="button" onClick={() => void refreshFiles()}>{t('Retry')}</button>
          </div>
        ) : !summary.enabled ? (
          <p className="system-settings-unavailable" role="note">{t('Local file logging is not configured for this deployment.')}</p>
        ) : (
          <>
            <dl className="system-settings-stats">
              <div><dt>{t('Log directory')}</dt><dd>{summary.logDirectory}</dd></div>
              <div><dt>{t('Log files')}</dt><dd>{new Intl.NumberFormat().format(summary.fileCount)}</dd></div>
              <div><dt>{t('Total size')}</dt><dd>{formatBytes(summary.totalSize)}</dd></div>
              <div><dt>{t('Date range')}</dt><dd>{formatTime(summary.oldestTime, i18n.language)} – {formatTime(summary.newestTime, i18n.language)}</dd></div>
            </dl>
            <div className="system-settings-form-grid system-settings-form">
              <label>
                {t('Retention mode')}
                <select value={fileMode} disabled={busy} onChange={(event) => setFileMode(event.target.value as PerformanceLogCleanupMode)}>
                  <option value="by_count">{t('Keep newest files')}</option>
                  <option value="by_days">{t('Delete files older than days')}</option>
                </select>
              </label>
              <label>
                {fileMode === 'by_count' ? t('Files to keep') : t('Days to keep')}
                <input type="number" inputMode="numeric" min={1} max={MAX_LOG_CLEANUP_VALUE} value={fileValue} disabled={busy} onChange={(event) => setFileValue(event.target.value)} />
              </label>
            </div>
            <div className="system-settings-row-actions">
              <button type="button" className="danger" disabled={busy} onClick={requestFileCleanup}>
                {busy && confirmation?.kind === 'files' ? t('Cleaning…') : t('Clean local log files')}
              </button>
            </div>
          </>
        )}
      </section>

      {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}

      {confirmation && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby="log-cleanup-confirm-title" aria-describedby="log-cleanup-confirm-description">
            <h3 id="log-cleanup-confirm-title">{confirmation.kind === 'history' ? t('Delete usage history?') : t('Clean local log files?')}</h3>
            <p id="log-cleanup-confirm-description">{confirmationDescription}</p>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={() => setConfirmation(null)}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={() => void confirmCleanup()}>{busy ? t('Cleaning…') : t('Confirm cleanup')}</button>
            </footer>
          </section>
        </div>
      )}
    </>
  );
}
