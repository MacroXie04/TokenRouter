import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  applyUpstreamUpdates,
  deleteOllamaModel,
  detectUpstreamUpdates,
  fetchChannelModels,
  loadCodexResetCredits,
  loadCodexUsage,
  loadMultiKeyPage,
  loadOllamaVersion,
  manageMultiKey,
  pullOllamaModelStream,
  refreshCodexCredential,
  resetCodexUsage,
  type ChannelSummary,
  type CodexDocument,
  type MultiKeyAction,
  type MultiKeyPage,
  type UpstreamDetection,
} from './channel-api';

type NoticeKind = 'success' | 'error';
type Notify = (kind: NoticeKind, text: string) => void;

const EMPTY_MULTI_KEY_PAGE: MultiKeyPage = {
  keys: [],
  total: 0,
  page: 1,
  pageSize: 20,
  totalPages: 1,
  enabledCount: 0,
  manualDisabledCount: 0,
  autoDisabledCount: 0,
};

function keyStatusLabel(status: number, t: (key: string) => string): string {
  if (status === 1) return t('Enabled');
  if (status === 2) return t('Manually disabled');
  return t('Automatically disabled');
}

export function MultiKeyPanel({
  channel,
  canOperate,
  canSensitiveWrite,
  onClose,
  onChanged,
  notify,
}: {
  channel: ChannelSummary;
  canOperate: boolean;
  canSensitiveWrite: boolean;
  onClose: () => void;
  onChanged: () => void;
  notify: Notify;
}) {
  const { t } = useTranslation();
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<'' | 1 | 2 | 3>('');
  const [result, setResult] = useState<MultiKeyPage>(EMPTY_MULTI_KEY_PAGE);
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [busy, setBusy] = useState('');

  useEffect(() => {
    if (!canOperate) {
      setResult(EMPTY_MULTI_KEY_PAGE);
      setLoading(false);
      setLoadError(false);
      return undefined;
    }
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void loadMultiKeyPage(channel.id, page, 20, status, controller.signal)
      .then((next) => {
        if (controller.signal.aborted) return;
        setResult(next);
        if (next.page !== page) setPage(next.page);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setResult(EMPTY_MULTI_KEY_PAGE);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [canOperate, channel.id, page, reload, status]);

  async function runAction(action: MultiKeyAction, keyIndex?: number) {
    const destructive = action === 'delete_key' || action === 'delete_disabled_keys';
    if (!canOperate || busy || (destructive && !canSensitiveWrite)) return;
    if (destructive && !window.confirm(t('Delete the selected key data? This action cannot be undone.'))) return;
    if (action === 'disable_all_keys' && !window.confirm(t('Disable every enabled key in this channel?'))) return;
    setBusy(`${action}:${keyIndex ?? 'all'}`);
    try {
      await manageMultiKey(channel.id, action, keyIndex);
      notify('success', t('Multi-key operation completed.'));
      setReload((value) => value + 1);
      onChanged();
    } catch {
      notify('error', t('Unable to update channel keys.'));
    } finally {
      setBusy('');
    }
  }

  return (
    <section className="channel-subpanel" aria-labelledby="multi-key-panel-title">
      <div className="channel-heading">
        <div>
          <h3 id="multi-key-panel-title">{t('Manage keys for {{name}}', { name: channel.name })}</h3>
          <p className="muted">{t('Key values stay hidden; irreversible fingerprints identify each entry.')}</p>
        </div>
        <button type="button" className="link" disabled={Boolean(busy)} onClick={onClose}>{t('Close')}</button>
      </div>

      <dl className="channel-stats" aria-label={t('Key status summary')}>
        <div><dt>{t('Enabled')}</dt><dd>{result.enabledCount}</dd></div>
        <div><dt>{t('Manually disabled')}</dt><dd>{result.manualDisabledCount}</dd></div>
        <div><dt>{t('Automatically disabled')}</dt><dd>{result.autoDisabledCount}</dd></div>
      </dl>

      <div className="channel-toolbar">
        <label>
          {t('Key status')}
          <select
            value={status}
            disabled={loading || Boolean(busy)}
            onChange={(event) => {
              const value = event.target.value;
              setPage(1);
              setStatus(value === '' ? '' : Number(value) as 1 | 2 | 3);
            }}
          >
            <option value="">{t('All statuses')}</option>
            <option value="1">{t('Enabled')}</option>
            <option value="2">{t('Manually disabled')}</option>
            <option value="3">{t('Automatically disabled')}</option>
          </select>
        </label>
        <div className="channel-filter-actions">
          <button type="button" disabled={loading || Boolean(busy)} onClick={() => setReload((value) => value + 1)}>{t('Refresh keys')}</button>
          <button type="button" disabled={loading || Boolean(busy) || result.enabledCount === 0} onClick={() => void runAction('disable_all_keys')}>{t('Disable all keys')}</button>
          <button type="button" disabled={loading || Boolean(busy) || result.manualDisabledCount + result.autoDisabledCount === 0} onClick={() => void runAction('enable_all_keys')}>{t('Enable all keys')}</button>
          {canSensitiveWrite && (
            <button type="button" className="danger-link" disabled={loading || Boolean(busy) || result.autoDisabledCount === 0 || result.autoDisabledCount >= result.enabledCount + result.manualDisabledCount + result.autoDisabledCount} onClick={() => void runAction('delete_disabled_keys')}>{t('Delete auto-disabled keys')}</button>
          )}
        </div>
      </div>
      {result.autoDisabledCount > 0 && result.autoDisabledCount >= result.enabledCount + result.manualDisabledCount + result.autoDisabledCount && (
        <p className="muted" role="status">{t('Enable at least one key before deleting auto-disabled keys.')}</p>
      )}

      {loadError ? (
        <div className="channel-state" role="alert">
          <p className="error">{t('Unable to load channel keys.')}</p>
          <button type="button" onClick={() => setReload((value) => value + 1)}>{t('Try again')}</button>
        </div>
      ) : (
        <div className="table-scroll">
          <table aria-busy={loading}>
            <caption className="sr-only">{t('Keys for {{name}}', { name: channel.name })}</caption>
            <thead><tr><th scope="col">{t('Key')}</th><th scope="col">{t('Status')}</th><th scope="col">{t('Disabled reason')}</th><th scope="col">{t('Actions')}</th></tr></thead>
            <tbody>
              {loading && <tr><td colSpan={4} role="status">{t('Loading keys…')}</td></tr>}
              {!loading && result.keys.length === 0 && <tr><td colSpan={4}>{t('No keys match this filter.')}</td></tr>}
              {!loading && result.keys.map((key) => {
                const rowBusy = busy.endsWith(`:${key.index}`);
                return (
                  <tr key={key.index}>
                    <td><code>{key.fingerprint}</code><span className="channel-meta">#{key.index + 1}</span></td>
                    <td>{keyStatusLabel(key.status, t)}</td>
                    <td>{key.hasReason ? t('Reason recorded') : '—'}{key.disabledTime > 0 && <span className="channel-meta">{new Date(key.disabledTime * 1000).toLocaleString()}</span>}</td>
                    <td><div className="channel-actions">
                      <button type="button" className="link" disabled={Boolean(busy)} onClick={() => void runAction(key.status === 1 ? 'disable_key' : 'enable_key', key.index)}>{rowBusy ? t('Updating…') : key.status === 1 ? t('Disable') : t('Enable')}</button>
                      {canSensitiveWrite && <button type="button" className="link danger-link" disabled={Boolean(busy) || result.enabledCount + result.manualDisabledCount + result.autoDisabledCount <= 1} onClick={() => void runAction('delete_key', key.index)}>{rowBusy ? t('Deleting…') : t('Delete')}</button>}
                    </div></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {!loadError && result.total > 0 && (
        <nav className="channel-pagination" aria-label={t('Key pages')}>
          <button type="button" disabled={loading || Boolean(busy) || result.page <= 1} onClick={() => setPage((value) => value - 1)}>{t('Previous')}</button>
          <span>{t('Page {{page}} of {{pages}}', { page: result.page, pages: result.totalPages })}</span>
          <button type="button" disabled={loading || Boolean(busy) || result.page >= result.totalPages} onClick={() => setPage((value) => value + 1)}>{t('Next')}</button>
        </nav>
      )}
    </section>
  );
}

function asDetection(channel: ChannelSummary): UpstreamDetection {
  return {
    channelId: channel.id,
    channelName: channel.name,
    addModels: channel.pendingAddModels,
    removeModels: channel.pendingRemoveModels,
    lastCheckTime: 0,
    autoAddedModels: 0,
  };
}

export function UpstreamUpdatesPanel({
  channel,
  canOperate,
  canWrite,
  onClose,
  onChanged,
  notify,
}: {
  channel: ChannelSummary;
  canOperate: boolean;
  canWrite: boolean;
  onClose: () => void;
  onChanged: () => void;
  notify: Notify;
}) {
  const { t } = useTranslation();
  const [detection, setDetection] = useState(() => asDetection(channel));
  const [selectedAdd, setSelectedAdd] = useState(() => new Set(channel.pendingAddModels));
  const [selectedRemove, setSelectedRemove] = useState(() => new Set(channel.pendingRemoveModels));
  const [loading, setLoading] = useState(channel.pendingAddModels.length === 0 && channel.pendingRemoveModels.length === 0);
  const [error, setError] = useState(false);
  const [applying, setApplying] = useState(false);

  const installDetection = useCallback((next: UpstreamDetection) => {
    setDetection(next);
    setSelectedAdd(new Set(next.addModels));
    setSelectedRemove(new Set(next.removeModels));
  }, []);

  const detect = useCallback(async (signal?: AbortSignal) => {
    if (!canOperate) return;
    setLoading(true);
    setError(false);
    try {
      const next = await detectUpstreamUpdates(channel.id, signal);
      if (signal?.aborted) return;
      installDetection(next);
      notify('success', t('Upstream detection completed.'));
      onChanged();
    } catch {
      if (!signal?.aborted) {
        setError(true);
        notify('error', t('Unable to detect upstream model updates.'));
      }
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [canOperate, channel.id, installDetection, notify, onChanged, t]);

  useEffect(() => {
    if (!canOperate || channel.pendingAddModels.length > 0 || channel.pendingRemoveModels.length > 0) return undefined;
    const controller = new AbortController();
    void detect(controller.signal);
    return () => controller.abort();
  }, [canOperate, channel.pendingAddModels.length, channel.pendingRemoveModels.length, detect]);

  function toggle(model: string, values: Set<string>, setter: (next: Set<string>) => void) {
    const next = new Set(values);
    if (next.has(model)) next.delete(model);
    else next.add(model);
    setter(next);
  }

  async function apply() {
    if (!canWrite || applying || loading) return;
    setApplying(true);
    setError(false);
    try {
      const ignored = detection.addModels.filter((model) => !selectedAdd.has(model));
      const result = await applyUpstreamUpdates(
        channel.id,
        [...selectedAdd],
        [...selectedRemove],
        ignored,
      );
      notify('success', t('Applied {{added}} additions and {{removed}} removals.', {
        added: result.addedModels.length,
        removed: result.removedModels.length,
      }));
      onChanged();
      onClose();
    } catch {
      setError(true);
      notify('error', t('Unable to apply upstream model updates.'));
    } finally {
      setApplying(false);
    }
  }

  const pendingCount = detection.addModels.length + detection.removeModels.length;
  return (
    <section className="channel-subpanel" aria-labelledby="upstream-panel-title">
      <div className="channel-heading">
        <div>
          <h3 id="upstream-panel-title">{t('Upstream updates for {{name}}', { name: channel.name })}</h3>
          <p className="muted">{t('Choose additions and removals to apply. Unselected additions are ignored in future checks.')}</p>
        </div>
        <button type="button" className="link" disabled={loading || applying} onClick={onClose}>{t('Close')}</button>
      </div>
      <div className="channel-filter-actions">
        {canOperate && <button type="button" disabled={loading || applying} onClick={() => void detect()}>{loading ? t('Detecting…') : t('Detect again')}</button>}
        {canWrite && <button type="button" disabled={loading || applying || pendingCount === 0} onClick={() => void apply()}>{applying ? t('Applying…') : t('Apply selected updates')}</button>}
      </div>
      {error && <p className="error" role="alert">{t('The upstream update operation did not complete.')}</p>}
      {loading ? <p role="status">{t('Checking the upstream model catalog…')}</p> : pendingCount === 0 ? (
        <p className="muted">{t('No upstream model changes were detected.')}</p>
      ) : (
        <div className="channel-columns">
          <fieldset>
            <legend>{t('Models to add')} ({selectedAdd.size}/{detection.addModels.length})</legend>
            {detection.addModels.length === 0 ? <p className="muted">{t('None')}</p> : detection.addModels.map((model) => (
              <label key={model}><input type="checkbox" checked={selectedAdd.has(model)} disabled={!canWrite} onChange={() => toggle(model, selectedAdd, setSelectedAdd)} /> <code>{model}</code></label>
            ))}
          </fieldset>
          <fieldset>
            <legend>{t('Models to remove')} ({selectedRemove.size}/{detection.removeModels.length})</legend>
            {detection.removeModels.length === 0 ? <p className="muted">{t('None')}</p> : detection.removeModels.map((model) => (
              <label key={model}><input type="checkbox" checked={selectedRemove.has(model)} disabled={!canWrite} onChange={() => toggle(model, selectedRemove, setSelectedRemove)} /> <code>{model}</code></label>
            ))}
          </fieldset>
        </div>
      )}
    </section>
  );
}

function documentText(document: CodexDocument | null): string {
  return document ? JSON.stringify(document.data, null, 2) : '';
}

function availableResetCredits(document: CodexDocument | null): number {
  if (!document || typeof document.data !== 'object' || document.data === null || Array.isArray(document.data)) return 0;
  const direct = document.data.available_count ?? document.data.credits;
  return typeof direct === 'number' && Number.isSafeInteger(direct) && direct > 0 ? direct : 0;
}

export function CodexPanel({
  channel,
  canOperate,
  canSensitiveWrite,
  onClose,
  notify,
}: {
  channel: ChannelSummary;
  canOperate: boolean;
  canSensitiveWrite: boolean;
  onClose: () => void;
  notify: Notify;
}) {
  const { t } = useTranslation();
  const [usage, setUsage] = useState<CodexDocument | null>(null);
  const [credits, setCredits] = useState<CodexDocument | null>(null);
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [usageError, setUsageError] = useState(false);
  const [creditsError, setCreditsError] = useState(false);
  const [busy, setBusy] = useState('');
  const resetCredits = availableResetCredits(credits);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setUsageError(false);
    setCreditsError(false);
    void Promise.allSettled([
      loadCodexUsage(channel.id, controller.signal),
      loadCodexResetCredits(channel.id, controller.signal),
    ]).then(([nextUsage, nextCredits]) => {
      if (controller.signal.aborted) return;
      if (nextUsage.status === 'fulfilled') setUsage(nextUsage.value);
      else {
        setUsage(null);
        setUsageError(true);
      }
      if (nextCredits.status === 'fulfilled') setCredits(nextCredits.value);
      else {
        setCredits(null);
        setCreditsError(true);
      }
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [channel.id, reload]);

  async function resetUsage() {
    if (!canOperate || busy || !window.confirm(t('Use one Codex reset credit now?'))) return;
    setBusy('reset');
    try {
      await resetCodexUsage(channel.id);
      notify('success', t('Codex usage reset completed.'));
      setReload((value) => value + 1);
    } catch {
      notify('error', t('Unable to reset Codex usage.'));
    } finally {
      setBusy('');
    }
  }

  async function refreshCredential() {
    if (busy || !canSensitiveWrite || !window.confirm(t('Refresh the stored Codex credential?'))) return;
    setBusy('credential');
    try {
      await refreshCodexCredential(channel.id);
      notify('success', t('Codex credential refreshed.'));
    } catch {
      notify('error', t('Unable to refresh the Codex credential.'));
    } finally {
      setBusy('');
    }
  }

  return (
    <section className="channel-subpanel" aria-labelledby="codex-panel-title">
      <div className="channel-heading"><h3 id="codex-panel-title">{t('Codex usage for {{name}}', { name: channel.name })}</h3><button type="button" className="link" disabled={Boolean(busy)} onClick={onClose}>{t('Close')}</button></div>
      <div className="channel-filter-actions">
        <button type="button" disabled={loading || Boolean(busy)} onClick={() => setReload((value) => value + 1)}>{t('Refresh usage')}</button>
        {canOperate && <button type="button" disabled={loading || Boolean(busy) || resetCredits === 0} onClick={() => void resetUsage()}>{busy === 'reset' ? t('Resetting…') : t('Use reset credit')}</button>}
        {canSensitiveWrite && <button type="button" disabled={loading || Boolean(busy)} onClick={() => void refreshCredential()}>{busy === 'credential' ? t('Refreshing credential…') : t('Refresh credential')}</button>}
      </div>
      {loading && <p role="status">{t('Loading Codex usage…')}</p>}
      {!loading && <div className="channel-columns">
        <section><h4>{t('Usage')}</h4>{usageError ? <p className="error" role="alert">{t('Unable to load Codex usage.')}</p> : <pre className="channel-document">{documentText(usage)}</pre>}</section>
        <section><h4>{t('Reset credits')}</h4>{creditsError ? <p className="error" role="alert">{t('Unable to load Codex reset credits.')}</p> : <pre className="channel-document">{documentText(credits)}</pre>}</section>
      </div>}
      {!loading && (usageError || creditsError) && <button type="button" onClick={() => setReload((value) => value + 1)}>{t('Try again')}</button>}
    </section>
  );
}

export function OllamaPanel({
  channel,
  canOperate,
  onClose,
  notify,
}: {
  channel: ChannelSummary;
  canOperate: boolean;
  onClose: () => void;
  notify: Notify;
}) {
  const { t } = useTranslation();
  const [version, setVersion] = useState('');
  const [models, setModels] = useState<string[]>([]);
  const [modelName, setModelName] = useState('');
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [busy, setBusy] = useState('');
  const [pullProgress, setPullProgress] = useState('');
  const pullController = useRef<AbortController | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      pullController.current?.abort();
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(false);
    void Promise.allSettled([
      loadOllamaVersion(channel.id, controller.signal),
      canOperate ? fetchChannelModels(channel.id, controller.signal) : Promise.resolve([]),
    ]).then(([nextVersion, nextModels]) => {
      if (controller.signal.aborted) return;
      setVersion(nextVersion.status === 'fulfilled' ? nextVersion.value : '');
      if (nextModels.status === 'fulfilled') setModels(nextModels.value);
      else {
        setModels([]);
        setError(true);
      }
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [canOperate, channel.id, reload]);

  async function pullModel(event: React.FormEvent) {
    event.preventDefault();
    if (busy || !modelName.trim()) return;
    setBusy('pull');
    setPullProgress(t('Starting pull…'));
    const controller = new AbortController();
    pullController.current = controller;
    try {
      await pullOllamaModelStream(channel.id, modelName, (progress) => {
        if (controller.signal.aborted || !mounted.current) return;
        const ratio = progress.total > 0 ? Math.min(100, Math.round(progress.completed / progress.total * 100)) : 0;
        setPullProgress(ratio > 0 ? `${progress.status || t('Downloading')} · ${ratio}%` : progress.status || t('Downloading'));
      }, controller.signal);
      notify('success', t('Ollama model pulled.'));
      setModelName('');
      setPullProgress('');
      setReload((value) => value + 1);
    } catch {
      if (!mounted.current) return;
      setPullProgress('');
      if (controller.signal.aborted) notify('error', t('Ollama model pull cancelled.'));
      else notify('error', t('Unable to pull the Ollama model.'));
    } finally {
      pullController.current = null;
      if (mounted.current) setBusy('');
    }
  }

  async function removeModel(model: string) {
    if (busy || !window.confirm(t('Delete Ollama model “{{model}}”?', { model }))) return;
    setBusy(`delete:${model}`);
    try {
      await deleteOllamaModel(channel.id, model);
      notify('success', t('Ollama model deleted.'));
      setReload((value) => value + 1);
    } catch {
      notify('error', t('Unable to delete the Ollama model.'));
    } finally {
      setBusy('');
    }
  }

  const modelCount = useMemo(() => models.length, [models.length]);
  return (
    <section className="channel-subpanel" aria-labelledby="ollama-panel-title">
      <div className="channel-heading"><div><h3 id="ollama-panel-title">{t('Ollama models for {{name}}', { name: channel.name })}</h3><p className="muted">{version ? t('Ollama version {{version}}', { version }) : t('Ollama version unavailable')}</p></div><button type="button" className="link" disabled={Boolean(busy)} onClick={onClose}>{t('Close')}</button></div>
      <form className="channel-toolbar" onSubmit={pullModel}>
        <label>{t('Model to pull')}<input value={modelName} maxLength={512} required onChange={(event) => setModelName(event.target.value)} /></label>
        <button type="submit" disabled={loading || Boolean(busy) || !modelName.trim()}>{busy === 'pull' ? t('Pulling…') : t('Pull model')}</button>
        {busy === 'pull' && <button type="button" className="link" onClick={() => pullController.current?.abort()}>{t('Cancel pull')}</button>}
        {canOperate && <button type="button" disabled={loading || Boolean(busy)} onClick={() => setReload((value) => value + 1)}>{t('Refresh models')}</button>}
      </form>
      {pullProgress && <p role="status">{pullProgress}</p>}
      {loading && <p role="status">{t('Loading Ollama models…')}</p>}
      {error && <div className="channel-state" role="alert"><p className="error">{t('Unable to load Ollama models.')}</p><button type="button" onClick={() => setReload((value) => value + 1)}>{t('Try again')}</button></div>}
      {canOperate && !loading && !error && modelCount === 0 && <p className="muted">{t('No Ollama models are installed.')}</p>}
      {canOperate && !loading && !error && modelCount > 0 && <ul className="channel-operation-list">{models.map((model) => <li key={model}><code>{model}</code><button type="button" className="link danger-link" disabled={Boolean(busy)} onClick={() => void removeModel(model)}>{busy === `delete:${model}` ? t('Deleting…') : t('Delete')}</button></li>)}</ul>}
    </section>
  );
}
