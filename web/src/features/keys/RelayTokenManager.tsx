import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  TOKEN_DISABLED,
  TOKEN_ENABLED,
  TOKEN_EXHAUSTED,
  TOKEN_EXPIRED,
  TOKEN_PAGE_SIZE,
  TOKEN_MAX_QUOTA,
  createToken,
  deleteToken,
  deleteTokens,
  listTokens,
  loadTokenAutoGroups,
  loadTokenGroups,
  loadTokenModels,
  searchTokens,
  updateToken,
  updateTokenStatus,
  type RelayToken,
  type TokenAutoGroupConfig,
  type TokenGroup,
  type TokenWriteInput,
} from './token-api';

interface TokenFormState {
  name: string;
  unlimitedQuota: boolean;
  remainQuota: string;
  neverExpires: boolean;
  expiresAt: string;
  group: string;
  autoGroups: string[];
  crossGroupRetry: boolean;
  modelLimitsEnabled: boolean;
  modelLimits: string[];
  allowIps: string;
}

type Notice = { kind: 'success' | 'error'; text: string } | null;
type FormMode = { kind: 'create' } | { kind: 'edit'; id: number };

const EMPTY_FORM: TokenFormState = {
  name: '',
  unlimitedQuota: true,
  remainQuota: '0',
  neverExpires: true,
  expiresAt: '',
  group: '',
  autoGroups: [],
  crossGroupRetry: false,
  modelLimitsEnabled: false,
  modelLimits: [],
  allowIps: '',
};

function localDateTime(timestamp: number): string {
  if (timestamp < 0) return '';
  const date = new Date(timestamp * 1_000);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

function formFromToken(token: RelayToken): TokenFormState {
  return {
    name: token.name,
    unlimitedQuota: token.unlimitedQuota,
    remainQuota: String(Math.max(0, token.remainQuota)),
    neverExpires: token.expiredTime < 0,
    expiresAt: localDateTime(token.expiredTime),
    group: token.group,
    autoGroups: token.autoGroups,
    crossGroupRetry: token.crossGroupRetry,
    modelLimitsEnabled: token.modelLimitsEnabled,
    modelLimits: token.modelLimits,
    allowIps: token.allowIps.replaceAll(',', '\n'),
  };
}

function normalizeAllowIps(value: string): string | null {
  const entries = value.split(/[\n,]/).map((entry) => entry.trim()).filter(Boolean);
  if (entries.length > 64 || entries.some((entry) => entry.length > 64 || !/^[0-9a-fA-F.:/]+$/.test(entry))) return null;
  return entries.join(',');
}

function writeInput(form: TokenFormState): TokenWriteInput | null {
  const quota = Number(form.remainQuota);
  if (!Number.isSafeInteger(quota) || quota < 0 || quota > TOKEN_MAX_QUOTA) return null;
  const allowIps = normalizeAllowIps(form.allowIps);
  if (allowIps === null) return null;
  let expiredTime = -1;
  if (!form.neverExpires) {
    const timestamp = Math.floor(new Date(form.expiresAt).getTime() / 1_000);
    if (!Number.isSafeInteger(timestamp) || timestamp <= 0 || timestamp > 253_402_300_799) return null;
    expiredTime = timestamp;
  }
  const input: TokenWriteInput = {
    name: form.name,
    expiredTime,
    remainQuota: quota,
    unlimitedQuota: form.unlimitedQuota,
    modelLimitsEnabled: form.modelLimitsEnabled,
    modelLimits: form.modelLimitsEnabled ? form.modelLimits : [],
    allowIps,
    crossGroupRetry: form.group === 'auto' && form.crossGroupRetry,
  };
  if (form.group) input.group = form.group;
  if (form.group === 'auto') input.autoGroups = form.autoGroups;
  return input;
}

function statusKey(status: number): string {
  switch (status) {
    case TOKEN_ENABLED: return 'Enabled';
    case TOKEN_DISABLED: return 'Disabled';
    case TOKEN_EXPIRED: return 'Expired';
    case TOKEN_EXHAUSTED: return 'Exhausted';
    default: return 'Unknown';
  }
}

export function RelayTokenManager() {
  const { t } = useTranslation();
  const [tokens, setTokens] = useState<RelayToken[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [searchDraft, setSearchDraft] = useState('');
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [groups, setGroups] = useState<TokenGroup[]>([]);
  const [autoConfig, setAutoConfig] = useState<TokenAutoGroupConfig>({ groups: [], maxCount: 1 });
  const [models, setModels] = useState<string[]>([]);
  const [metadataError, setMetadataError] = useState(false);
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [formMode, setFormMode] = useState<FormMode | null>(null);
  const [form, setForm] = useState<TokenFormState>(EMPTY_FORM);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    const request = keyword ? searchTokens(keyword, page, controller.signal) : listTokens(page, controller.signal);
    void request
      .then((result) => {
        if (controller.signal.aborted) return;
        setTokens(result.items);
        setTotal(result.total);
        setSelection((current) => new Set([...current].filter((id) => result.items.some((token) => token.id === id))));
        const lastPage = Math.max(1, Math.ceil(result.total / result.pageSize));
        if (page > lastPage) setPage(lastPage);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setTokens([]);
          setTotal(0);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [keyword, page, reload]);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      loadTokenGroups(controller.signal),
      loadTokenAutoGroups(controller.signal),
    ]).then(([nextGroups, nextAutoConfig]) => {
      if (controller.signal.aborted) return;
      setGroups(nextGroups);
      setAutoConfig(nextAutoConfig);
    }).catch(() => {
      if (!controller.signal.aborted) setMetadataError(true);
    });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void loadTokenModels(form.group || undefined, controller.signal)
      .then((nextModels) => {
        if (!controller.signal.aborted) setModels(nextModels);
      })
      .catch(() => {
        if (!controller.signal.aborted) setMetadataError(true);
      });
    return () => controller.abort();
  }, [form.group]);

  const pageCount = Math.max(1, Math.ceil(total / TOKEN_PAGE_SIZE));
  const allVisibleSelected = tokens.length > 0 && tokens.every((token) => selection.has(token.id));
  const groupOptions = useMemo(() => {
    const byName = new Map(groups.map((group) => [group.name, group]));
    if (form.group && !byName.has(form.group)) byName.set(form.group, { name: form.group, description: form.group, ratio: '' });
    if (autoConfig.groups.length > 0 && !byName.has('auto')) byName.set('auto', { name: 'auto', description: t('Automatic routing'), ratio: '' });
    return [...byName.values()].sort((left, right) => left.name.localeCompare(right.name));
  }, [autoConfig.groups.length, form.group, groups, t]);
  const modelOptions = useMemo(() => {
    const combined = new Set([...models, ...form.modelLimits]);
    return [...combined].sort((left, right) => left.localeCompare(right));
  }, [form.modelLimits, models]);

  function refresh() {
    setReload((value) => value + 1);
  }

  function submitSearch(event: React.FormEvent) {
    event.preventDefault();
    const normalized = searchDraft.trim();
    if (normalized.includes('%')) {
      setNotice({ kind: 'error', text: t('Search text cannot contain percent signs.') });
      return;
    }
    setNotice(null);
    setKeyword(normalized);
    setPage(1);
  }

  function clearSearch() {
    setSearchDraft('');
    setKeyword('');
    setPage(1);
    setNotice(null);
  }

  function openCreate() {
    setFormMode({ kind: 'create' });
    setForm(EMPTY_FORM);
    setNotice(null);
  }

  function openEdit(token: RelayToken) {
    setFormMode({ kind: 'edit', id: token.id });
    setForm(formFromToken(token));
    setNotice(null);
  }

  async function saveToken(event: React.FormEvent) {
    event.preventDefault();
    if (!formMode || busy) return;
    const input = writeInput(form);
    if (!input || (form.group === 'auto' && (form.autoGroups.length < 1 || form.autoGroups.length > autoConfig.maxCount))) {
      setNotice({ kind: 'error', text: t('Review the token fields and limits.') });
      return;
    }
    const action = formMode.kind === 'create' ? 'create' : `edit:${formMode.id}`;
    setBusy(action);
    setNotice(null);
    try {
      if (formMode.kind === 'create') await createToken(input);
      else await updateToken(formMode.id, input);
      setFormMode(null);
      setForm(EMPTY_FORM);
      setNotice({ kind: 'success', text: formMode.kind === 'create' ? t('API key created.') : t('API key updated.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: formMode.kind === 'create' ? t('Unable to create API key.') : t('Unable to update API key.') });
    } finally {
      setBusy('');
    }
  }

  async function toggleStatus(token: RelayToken) {
    if (busy || (token.status !== TOKEN_ENABLED && token.status !== TOKEN_DISABLED)) return;
    const enabling = token.status === TOKEN_DISABLED;
    setBusy(`status:${token.id}`);
    setNotice(null);
    try {
      await updateTokenStatus(token.id, enabling ? TOKEN_ENABLED : TOKEN_DISABLED);
      setNotice({ kind: 'success', text: enabling ? t('API key enabled.') : t('API key disabled.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update API key status.') });
    } finally {
      setBusy('');
    }
  }

  async function removeOne(token: RelayToken) {
    if (busy || !window.confirm(t('Delete API key “{{name}}”?', { name: token.name }))) return;
    setBusy(`delete:${token.id}`);
    setNotice(null);
    try {
      await deleteToken(token.id);
      if (formMode?.kind === 'edit' && formMode.id === token.id) setFormMode(null);
      setNotice({ kind: 'success', text: t('API key deleted.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete API key.') });
    } finally {
      setBusy('');
    }
  }

  async function removeSelected() {
    const ids = [...selection];
    if (busy || ids.length === 0 || !window.confirm(t('Delete {{count}} selected API keys?', { count: ids.length }))) return;
    setBusy('batch-delete');
    setNotice(null);
    try {
      const deleted = await deleteTokens(ids);
      setSelection(new Set());
      setNotice({ kind: 'success', text: t('{{count}} API keys deleted.', { count: deleted }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete selected API keys.') });
    } finally {
      setBusy('');
    }
  }

  function selectAllVisible(checked: boolean) {
    setSelection(checked ? new Set(tokens.map((token) => token.id)) : new Set());
  }

  function toggleSelection(id: number, checked: boolean) {
    setSelection((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function toggleAutoGroup(group: string, checked: boolean) {
    setForm((current) => ({
      ...current,
      autoGroups: checked
        ? [...current.autoGroups, group]
        : current.autoGroups.filter((candidate) => candidate !== group),
    }));
  }

  return (
    <section className="card token-manager" aria-labelledby="token-manager-title">
      <div className="token-heading">
        <div>
          <h2 id="token-manager-title">{t('API keys')}</h2>
          <p className="muted">{t('Manage relay access without displaying full credentials.')}</p>
        </div>
        <button type="button" onClick={openCreate}>{t('Create key')}</button>
      </div>

      <form className="token-search" role="search" aria-label={t('Search API keys')} onSubmit={submitSearch}>
        <label>
          {t('Search by key name')}
          <input value={searchDraft} maxLength={50} onChange={(event) => setSearchDraft(event.target.value)} />
        </label>
        <button type="submit">{t('Search')}</button>
        <button type="button" className="link" onClick={clearSearch}>{t('Clear')}</button>
        <button type="button" className="link" disabled={loading} onClick={refresh}>{loading ? t('Refreshing…') : t('Refresh')}</button>
      </form>

      {metadataError && <p className="muted" role="status">{t('Group and model suggestions are unavailable.')}</p>}
      {notice && <p className={notice.kind} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      {selection.size > 0 && (
        <div className="token-bulk-bar" role="toolbar" aria-label={t('Selected API key actions')}>
          <span>{t('{{count}} selected', { count: selection.size })}</span>
          <button type="button" className="danger" disabled={busy === 'batch-delete'} onClick={() => void removeSelected()}>
            {busy === 'batch-delete' ? t('Deleting…') : t('Delete selected')}
          </button>
        </div>
      )}

      {loadError ? (
        <div className="token-state" role="alert">
          <p className="error">{t('Unable to load API keys.')}</p>
          <button type="button" onClick={refresh}>{t('Try again')}</button>
        </div>
      ) : (
        <div className="table-scroll" aria-busy={loading}>
          <table aria-busy={loading}>
            <caption className="sr-only">{t('Relay API keys')}</caption>
            <thead>
              <tr>
                <th scope="col">
                  <span className="sr-only">{t('Select')}</span>
                  <input
                    type="checkbox"
                    aria-label={t('Select all visible API keys')}
                    checked={allVisibleSelected}
                    onChange={(event) => selectAllVisible(event.target.checked)}
                  />
                </th>
                <th scope="col">{t('Name')}</th>
                <th scope="col">{t('Masked key')}</th>
                <th scope="col">{t('Status')}</th>
                <th scope="col">{t('Quota')}</th>
                <th scope="col">{t('Group')}</th>
                <th scope="col">{t('Models')}</th>
                <th scope="col">{t('Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {loading && tokens.length === 0 && <tr><td colSpan={8} role="status">{t('Loading API keys…')}</td></tr>}
              {!loading && tokens.length === 0 && <tr><td colSpan={8}>{keyword ? t('No API keys match this search.') : t('No API keys yet.')}</td></tr>}
              {tokens.map((token) => {
                const rowBusy = busy.endsWith(`:${token.id}`);
                const canToggle = token.status === TOKEN_ENABLED || token.status === TOKEN_DISABLED;
                return (
                  <tr key={token.id}>
                    <td><input type="checkbox" aria-label={t('Select {{name}}', { name: token.name })} checked={selection.has(token.id)} onChange={(event) => toggleSelection(token.id, event.target.checked)} /></td>
                    <td><strong>{token.name}</strong><span className="token-meta">#{token.id}</span></td>
                    <td><code>{token.maskedKey || t('Unavailable')}</code></td>
                    <td><span className={`status-pill ${token.status === TOKEN_ENABLED ? 'enabled' : 'disabled'}`}>{t(statusKey(token.status))}</span></td>
                    <td>{token.unlimitedQuota ? t('Unlimited') : token.remainQuota.toLocaleString()}</td>
                    <td>{token.group || t('Account default')}{token.group === 'auto' && <span className="token-meta">{token.autoGroups.join(' → ')}</span>}</td>
                    <td>{token.modelLimitsEnabled ? token.modelLimits.slice(0, 3).join(', ') || '—' : t('All models')}</td>
                    <td>
                      <div className="token-actions">
                        <button type="button" className="link" aria-expanded={formMode?.kind === 'edit' && formMode.id === token.id} aria-controls="token-form-panel" disabled={rowBusy} onClick={() => openEdit(token)}>{t('Edit')}</button>
                        {canToggle && <button type="button" className="link" disabled={rowBusy} onClick={() => void toggleStatus(token)}>{busy === `status:${token.id}` ? t('Updating…') : token.status === TOKEN_ENABLED ? t('Disable') : t('Enable')}</button>}
                        <button type="button" className="link danger-link" disabled={rowBusy} onClick={() => void removeOne(token)}>{busy === `delete:${token.id}` ? t('Deleting…') : t('Delete')}</button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {!loadError && total > 0 && (
        <nav className="token-pagination" aria-label={t('API key pages')}>
          <button type="button" disabled={loading || page <= 1} onClick={() => setPage((current) => current - 1)}>{t('Previous')}</button>
          <span>{t('Page {{page}} of {{pages}}', { page, pages: pageCount })}</span>
          <button type="button" disabled={loading || page >= pageCount} onClick={() => setPage((current) => current + 1)}>{t('Next')}</button>
        </nav>
      )}

      {formMode && (
        <form id="token-form-panel" className="token-form grid-form" onSubmit={saveToken}>
          <div className="token-heading">
            <h3>{formMode.kind === 'create' ? t('Create API key') : t('Edit API key')}</h3>
            <button type="button" className="link" disabled={Boolean(busy)} onClick={() => setFormMode(null)}>{t('Cancel')}</button>
          </div>
          <label>{t('Name')}<input value={form.name} maxLength={50} required onChange={(event) => setForm((current) => ({ ...current, name: event.target.value }))} /></label>
          <label className="token-checkbox"><input type="checkbox" checked={form.unlimitedQuota} onChange={(event) => setForm((current) => ({ ...current, unlimitedQuota: event.target.checked }))} />{t('Unlimited quota')}</label>
          {!form.unlimitedQuota && <label>{t('Quota')}<input type="number" min={0} max={TOKEN_MAX_QUOTA} step={1} value={form.remainQuota} required onChange={(event) => setForm((current) => ({ ...current, remainQuota: event.target.value }))} /></label>}
          <label className="token-checkbox"><input type="checkbox" checked={form.neverExpires} onChange={(event) => setForm((current) => ({ ...current, neverExpires: event.target.checked }))} />{t('Never expires')}</label>
          {!form.neverExpires && <label>{t('Expires at')}<input type="datetime-local" value={form.expiresAt} required onChange={(event) => setForm((current) => ({ ...current, expiresAt: event.target.value }))} /></label>}
          <label>
            {t('Routing group')}
            <select value={form.group} onChange={(event) => setForm((current) => ({ ...current, group: event.target.value, autoGroups: event.target.value === 'auto' ? current.autoGroups : [], crossGroupRetry: event.target.value === 'auto' && current.crossGroupRetry }))}>
              <option value="">{t('Account default')}</option>
              {groupOptions.map((group) => <option key={group.name} value={group.name}>{group.description || group.name}{group.description !== group.name ? ` (${group.name})` : ''}</option>)}
            </select>
          </label>
          {form.group === 'auto' && (
            <fieldset className="token-choice-group">
              <legend>{t('Automatic group priority (up to {{count}})', { count: autoConfig.maxCount })}</legend>
              {autoConfig.groups.length === 0
                ? <p className="muted">{t('No automatic groups are available.')}</p>
                : autoConfig.groups.map((group) => <label className="token-checkbox" key={group}><input type="checkbox" checked={form.autoGroups.includes(group)} disabled={!form.autoGroups.includes(group) && form.autoGroups.length >= autoConfig.maxCount} onChange={(event) => toggleAutoGroup(group, event.target.checked)} />{group}</label>)}
              <label className="token-checkbox"><input type="checkbox" checked={form.crossGroupRetry} onChange={(event) => setForm((current) => ({ ...current, crossGroupRetry: event.target.checked }))} />{t('Retry across selected groups')}</label>
            </fieldset>
          )}
          <label className="token-checkbox"><input type="checkbox" checked={form.modelLimitsEnabled} onChange={(event) => setForm((current) => ({ ...current, modelLimitsEnabled: event.target.checked }))} />{t('Limit this key to selected models')}</label>
          {form.modelLimitsEnabled && (
            <label>
              {t('Allowed models')}
              <select multiple size={Math.min(8, Math.max(3, modelOptions.length))} value={form.modelLimits} onChange={(event) => setForm((current) => ({ ...current, modelLimits: [...event.target.selectedOptions].map((option) => option.value).slice(0, 200) }))}>
                {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}
              </select>
            </label>
          )}
          <label>{t('IP allowlist')}<textarea rows={3} maxLength={4_096} value={form.allowIps} placeholder={t('One IP address or CIDR per line')} onChange={(event) => setForm((current) => ({ ...current, allowIps: event.target.value }))} /></label>
          <p className="muted">{t('Full API keys are never requested or displayed here.')}</p>
          <button type="submit" disabled={Boolean(busy)}>{busy === 'create' || busy.startsWith('edit:') ? t('Saving…') : t('Save API key')}</button>
        </form>
      )}
    </section>
  );
}
