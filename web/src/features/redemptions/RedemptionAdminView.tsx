import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  REDEMPTION_PAGE_SIZE,
  REDEMPTION_STATUS_DISABLED,
  REDEMPTION_STATUS_ENABLED,
  REDEMPTION_STATUS_USED,
  createRedemptions,
  deleteInvalidRedemptions,
  deleteRedemption,
  getRedemptionCode,
  getRedemptionForEdit,
  listRedemptions,
  searchRedemptions,
  updateRedemption,
  updateRedemptionStatus,
  type ManagedRedemption,
  type RedemptionInput,
  type RedemptionQuery,
  type RedemptionStatusFilter,
} from './redemption-api';

type Notice = { kind: 'success' | 'error'; text: string } | null;
type FormDraft = { name: string; quota: string; expiresAt: string; count: string };

const EMPTY_CREATE: FormDraft = { name: '', quota: '500', expiresAt: '', count: '1' };
const ADMIN_ROLE = 10;
const SECRET_DISPLAY_MS = 60_000;

function isExpired(redemption: ManagedRedemption): boolean {
  return redemption.status === REDEMPTION_STATUS_ENABLED
    && redemption.expiredTime > 0
    && redemption.expiredTime < Math.floor(Date.now() / 1000);
}

function toDateTimeLocal(timestamp: number): string {
  if (!timestamp) return '';
  const date = new Date(timestamp * 1000);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
    + `T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function parseDraft(draft: FormDraft, includeCount: boolean): (RedemptionInput & { count: number }) | null {
  const name = draft.name.trim();
  const quota = Number(draft.quota);
  const count = includeCount ? Number(draft.count) : 1;
  let expiredTime = 0;
  if (draft.expiresAt) {
    const milliseconds = Date.parse(draft.expiresAt);
    if (!Number.isFinite(milliseconds)) return null;
    expiredTime = Math.floor(milliseconds / 1000);
  }
  if (
    [...name].length < 1
    || [...name].length > 20
    || !Number.isSafeInteger(quota)
    || quota < 1
    || quota > 2_147_483_647
    || !Number.isSafeInteger(count)
    || count < 1
    || count > 100
    || (expiredTime !== 0 && expiredTime <= Math.floor(Date.now() / 1000))
  ) return null;
  return { name, quota, expiredTime, count };
}

export function RedemptionAdminView({ operatorRole }: { operatorRole: number }) {
  const { t } = useTranslation();
  const authorized = operatorRole >= ADMIN_ROLE;
  const [draftKeyword, setDraftKeyword] = useState('');
  const [draftStatus, setDraftStatus] = useState<RedemptionStatusFilter>('');
  const [query, setQuery] = useState<RedemptionQuery>({
    keyword: '',
    status: '',
    page: 1,
    pageSize: REDEMPTION_PAGE_SIZE,
  });
  const [redemptions, setRedemptions] = useState<ManagedRedemption[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(authorized);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [notice, setNotice] = useState<Notice>(null);
  const [busyAction, setBusyAction] = useState('');
  const [creating, setCreating] = useState(false);
  const [createDraft, setCreateDraft] = useState<FormDraft>(EMPTY_CREATE);
  const [editing, setEditing] = useState<ManagedRedemption | null>(null);
  const [editDraft, setEditDraft] = useState<FormDraft>(EMPTY_CREATE);
  const [createdCodes, setCreatedCodes] = useState<string[]>([]);

  useEffect(() => {
    if (!authorized) return undefined;
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    const request = query.keyword || query.status
      ? searchRedemptions(query, controller.signal)
      : listRedemptions(query.page, query.pageSize, controller.signal);
    void request
      .then((page) => {
        if (controller.signal.aborted) return;
        setRedemptions(page.items);
        setTotal(page.total);
        const lastPage = Math.max(1, Math.ceil(page.total / query.pageSize));
        if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setRedemptions([]);
          setTotal(0);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [authorized, query, reload]);

  useEffect(() => {
    if (createdCodes.length === 0) return undefined;
    const timeout = window.setTimeout(() => setCreatedCodes([]), SECRET_DISPLAY_MS);
    return () => window.clearTimeout(timeout);
  }, [createdCodes]);

  const refresh = useCallback(() => setReload((value) => value + 1), []);
  const pageCount = Math.max(1, Math.ceil(total / query.pageSize));

  function submitSearch(event: React.FormEvent) {
    event.preventDefault();
    setNotice(null);
    setCreatedCodes([]);
    setQuery((current) => ({
      ...current,
      keyword: draftKeyword.trim(),
      status: draftStatus,
      page: 1,
    }));
  }

  function clearSearch() {
    setDraftKeyword('');
    setDraftStatus('');
    setNotice(null);
    setQuery((current) => ({ ...current, keyword: '', status: '', page: 1 }));
  }

  async function submitCreate(event: React.FormEvent) {
    event.preventDefault();
    if (busyAction) return;
    const input = parseDraft(createDraft, true);
    if (!input) {
      setNotice({ kind: 'error', text: t('Enter a valid redemption name, quota, count, and future expiry.') });
      return;
    }
    setBusyAction('create');
    setNotice(null);
    setCreatedCodes([]);
    try {
      const codes = await createRedemptions(input);
      setCreatedCodes(codes);
      setCreateDraft(EMPTY_CREATE);
      setCreating(false);
      setNotice({ kind: 'success', text: t('{{count}} redemption codes created.', { count: codes.length }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to create redemption codes.') });
    } finally {
      setBusyAction('');
    }
  }

  async function openEditor(redemption: ManagedRedemption) {
    if (busyAction || redemption.status !== REDEMPTION_STATUS_ENABLED || isExpired(redemption)) return;
    const action = `load:${redemption.id}`;
    setBusyAction(action);
    setNotice(null);
    setCreatedCodes([]);
    try {
      const current = await getRedemptionForEdit(redemption.id);
      if (current.status !== REDEMPTION_STATUS_ENABLED || isExpired(current)) {
        setNotice({ kind: 'error', text: t('This redemption code can no longer be edited.') });
        refresh();
        return;
      }
      setEditing(current);
      setEditDraft({
        name: current.name,
        quota: String(current.quota),
        expiresAt: toDateTimeLocal(current.expiredTime),
        count: '1',
      });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to load redemption code.') });
    } finally {
      setBusyAction('');
    }
  }

  async function submitEdit(event: React.FormEvent) {
    event.preventDefault();
    if (!editing || busyAction) return;
    const input = parseDraft(editDraft, false);
    if (!input) {
      setNotice({ kind: 'error', text: t('Enter a valid redemption name, quota, count, and future expiry.') });
      return;
    }
    const action = `edit:${editing.id}`;
    setBusyAction(action);
    setNotice(null);
    try {
      await updateRedemption(editing.id, input);
      setEditing(null);
      setNotice({ kind: 'success', text: t('Redemption code updated.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update redemption code.') });
    } finally {
      setBusyAction('');
    }
  }

  async function copyCode(redemption: ManagedRedemption) {
    if (busyAction) return;
    const action = `copy:${redemption.id}`;
    setBusyAction(action);
    setNotice(null);
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      const fullCode = await getRedemptionCode(redemption.id);
      await navigator.clipboard.writeText(fullCode);
      setNotice({ kind: 'success', text: t('Redemption code copied.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to copy redemption code.') });
    } finally {
      setBusyAction('');
    }
  }

  async function copyCreatedCodes() {
    if (busyAction || createdCodes.length === 0) return;
    setBusyAction('copy-created');
    setNotice(null);
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(createdCodes.join('\n'));
      setNotice({ kind: 'success', text: t('New redemption codes copied.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to copy new redemption codes.') });
    } finally {
      setBusyAction('');
    }
  }

  async function toggleStatus(redemption: ManagedRedemption) {
    if (busyAction || redemption.status === REDEMPTION_STATUS_USED || isExpired(redemption)) return;
    const nextStatus = redemption.status === REDEMPTION_STATUS_ENABLED
      ? REDEMPTION_STATUS_DISABLED
      : REDEMPTION_STATUS_ENABLED;
    const action = `status:${redemption.id}`;
    setBusyAction(action);
    setNotice(null);
    try {
      await updateRedemptionStatus(redemption.id, nextStatus);
      setNotice({
        kind: 'success',
        text: nextStatus === REDEMPTION_STATUS_ENABLED
          ? t('Redemption code enabled.')
          : t('Redemption code disabled.'),
      });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update redemption code status.') });
    } finally {
      setBusyAction('');
    }
  }

  async function removeRedemption(redemption: ManagedRedemption) {
    if (busyAction || !window.confirm(t('Delete redemption “{{name}}” (ID {{id}})? This cannot be undone.', {
      name: redemption.name,
      id: redemption.id,
    }))) return;
    const action = `delete:${redemption.id}`;
    setBusyAction(action);
    setNotice(null);
    setCreatedCodes([]);
    try {
      await deleteRedemption(redemption.id);
      if (editing?.id === redemption.id) setEditing(null);
      setNotice({ kind: 'success', text: t('Redemption code deleted.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete redemption code.') });
    } finally {
      setBusyAction('');
    }
  }

  async function removeInvalid() {
    if (busyAction || !window.confirm(t('Delete all used, disabled, and expired redemption codes? This cannot be undone.'))) return;
    setBusyAction('delete-invalid');
    setNotice(null);
    setCreatedCodes([]);
    try {
      const count = await deleteInvalidRedemptions();
      setNotice({ kind: 'success', text: t('{{count}} invalid redemption codes deleted.', { count }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete invalid redemption codes.') });
    } finally {
      setBusyAction('');
    }
  }

  function statusName(redemption: ManagedRedemption): string {
    if (isExpired(redemption)) return t('Expired');
    if (redemption.status === REDEMPTION_STATUS_USED) return t('Used');
    if (redemption.status === REDEMPTION_STATUS_DISABLED) return t('Disabled');
    return t('Unused');
  }

  if (!authorized) {
    return (
      <section className="card redemption-admin">
        <h2>{t('Redemption codes')}</h2>
        <p className="error" role="alert">{t('Administrator access is required.')}</p>
      </section>
    );
  }

  return (
    <section className="card redemption-admin" aria-labelledby="redemption-admin-title">
      <div className="redemption-heading">
        <div>
          <h2 id="redemption-admin-title">{t('Redemption code management')}</h2>
          <p className="muted">{t('Create and manage redeemable quota grants.')}</p>
        </div>
        <div className="redemption-heading-actions">
          <button className="link danger-link" type="button" disabled={Boolean(busyAction)} onClick={() => void removeInvalid()}>
            {busyAction === 'delete-invalid' ? t('Deleting…') : t('Delete invalid')}
          </button>
          <button type="button" disabled={Boolean(busyAction)} onClick={() => {
            setCreating((value) => !value);
            setEditing(null);
            setCreatedCodes([]);
            setNotice(null);
          }} aria-expanded={creating}>
            {creating ? t('Cancel') : t('Create codes')}
          </button>
        </div>
      </div>

      {notice && <p className={notice.kind} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}

      {creating && (
        <form className="redemption-subpanel grid-form" onSubmit={submitCreate} aria-labelledby="create-redemptions-title">
          <h3 id="create-redemptions-title">{t('Create redemption codes')}</h3>
          <label>{t('Name')}
            <input required maxLength={20} value={createDraft.name} autoComplete="off"
              onChange={(event) => setCreateDraft((current) => ({ ...current, name: event.target.value }))} />
          </label>
          <label>{t('Quota per code')}
            <input required type="number" min="1" max="2147483647" step="1" value={createDraft.quota}
              onChange={(event) => setCreateDraft((current) => ({ ...current, quota: event.target.value }))} />
          </label>
          <label>{t('Number of codes')}
            <input required type="number" min="1" max="100" step="1" value={createDraft.count}
              onChange={(event) => setCreateDraft((current) => ({ ...current, count: event.target.value }))} />
          </label>
          <label>{t('Expiry (optional)')}
            <input type="datetime-local" value={createDraft.expiresAt}
              onChange={(event) => setCreateDraft((current) => ({ ...current, expiresAt: event.target.value }))} />
          </label>
          <p className="muted redemption-form-note">{t('New codes are revealed once for 60 seconds. Store them securely.')}</p>
          <button type="submit" disabled={Boolean(busyAction)}>
            {busyAction === 'create' ? t('Creating…') : t('Create codes')}
          </button>
        </form>
      )}

      {createdCodes.length > 0 && (
        <section className="redemption-secret-panel" aria-labelledby="new-redemption-codes-title">
          <h3 id="new-redemption-codes-title">{t('New redemption codes — shown once')}</h3>
          <p>{t('Copy these codes now. They will be hidden automatically after 60 seconds.')}</p>
          <ul>
            {createdCodes.map((value) => <li key={value}><code>{value}</code></li>)}
          </ul>
          <div className="redemption-secret-actions">
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void copyCreatedCodes()}>{t('Copy all')}</button>
            <button className="link" type="button" onClick={() => setCreatedCodes([])}>{t('Dismiss and hide')}</button>
          </div>
        </section>
      )}

      <form className="redemption-filters" role="search" aria-label={t('Search redemption codes')} onSubmit={submitSearch}>
        <label>{t('Name or ID')}
          <input maxLength={64} value={draftKeyword}
            onChange={(event) => setDraftKeyword(event.target.value)} />
        </label>
        <label>{t('Status')}
          <select value={draftStatus} onChange={(event) => setDraftStatus(event.target.value as RedemptionStatusFilter)}>
            <option value="">{t('All statuses')}</option>
            <option value="1">{t('Unused')}</option>
            <option value="2">{t('Disabled')}</option>
            <option value="3">{t('Used')}</option>
            <option value="expired">{t('Expired')}</option>
          </select>
        </label>
        <div className="redemption-filter-actions">
          <button type="submit">{t('Apply filters')}</button>
          <button className="link" type="button" onClick={clearSearch}>{t('Clear filters')}</button>
          <button className="link" type="button" onClick={refresh}>{t('Refresh')}</button>
        </div>
      </form>

      <div className="table-scroll">
        <table aria-label={t('Redemption code results')} aria-busy={loading}>
          <thead>
            <tr>
              <th scope="col">{t('Name')}</th>
              <th scope="col">{t('Code')}</th>
              <th scope="col">{t('Status')}</th>
              <th scope="col">{t('Quota')}</th>
              <th scope="col">{t('Expiry')}</th>
              <th scope="col">{t('Actions')}</th>
            </tr>
          </thead>
          <tbody>
            {loading && <tr><td colSpan={6} className="redemption-state" role="status">{t('Loading redemption codes…')}</td></tr>}
            {!loading && loadError && (
              <tr><td colSpan={6} className="redemption-state">
                <p className="error" role="alert">{t('Unable to load redemption codes.')}</p>
                <button type="button" onClick={refresh}>{t('Try again')}</button>
              </td></tr>
            )}
            {!loading && !loadError && redemptions.length === 0 && (
              <tr><td colSpan={6} className="redemption-state">{t('No redemption codes match these filters.')}</td></tr>
            )}
            {!loading && !loadError && redemptions.map((redemption) => {
              const expired = isExpired(redemption);
              const canEdit = redemption.status === REDEMPTION_STATUS_ENABLED && !expired;
              const canToggle = redemption.status !== REDEMPTION_STATUS_USED && !expired;
              return (
                <tr key={redemption.id}>
                  <td>
                    <strong>{redemption.name}</strong>
                    <span className="redemption-meta">ID {redemption.id} · {t('Owner')} {redemption.userId}</span>
                  </td>
                  <td>
                    <code>{redemption.maskedCode}</code>
                    <button className="link redemption-copy" type="button" disabled={Boolean(busyAction)}
                      onClick={() => void copyCode(redemption)}>{t('Copy code')}</button>
                  </td>
                  <td><span className={`status-pill ${redemption.status === REDEMPTION_STATUS_ENABLED && !expired ? 'enabled' : 'disabled'}`}>
                    {statusName(redemption)}
                  </span></td>
                  <td>{redemption.quota.toLocaleString()}</td>
                  <td>{redemption.expiredTime === 0 ? t('Never expires') : new Date(redemption.expiredTime * 1000).toLocaleString()}</td>
                  <td><div className="redemption-actions">
                    <button className="link" type="button" disabled={Boolean(busyAction) || !canEdit}
                      onClick={() => void openEditor(redemption)}>{busyAction === `load:${redemption.id}` ? t('Loading…') : t('Edit')}</button>
                    {canToggle && <button className="link" type="button" disabled={Boolean(busyAction)}
                      onClick={() => void toggleStatus(redemption)}>
                      {redemption.status === REDEMPTION_STATUS_ENABLED ? t('Disable') : t('Enable')}
                    </button>}
                    <button className="link danger-link" type="button" disabled={Boolean(busyAction)}
                      onClick={() => void removeRedemption(redemption)}>{t('Delete')}</button>
                  </div></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <nav className="redemption-pagination" aria-label={t('Redemption code pages')}>
        <button type="button" disabled={loading || query.page <= 1}
          onClick={() => setQuery((current) => ({ ...current, page: current.page - 1 }))}>{t('Previous')}</button>
        <span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span>
        <button type="button" disabled={loading || query.page >= pageCount}
          onClick={() => setQuery((current) => ({ ...current, page: current.page + 1 }))}>{t('Next')}</button>
      </nav>

      {editing && (
        <form className="redemption-subpanel grid-form" onSubmit={submitEdit}
          aria-label={t('Edit redemption code {{id}}', { id: editing.id })}>
          <h3>{t('Edit redemption code {{id}}', { id: editing.id })}</h3>
          <label>{t('Name')}
            <input required maxLength={20} value={editDraft.name}
              onChange={(event) => setEditDraft((current) => ({ ...current, name: event.target.value }))} />
          </label>
          <label>{t('Quota')}
            <input required type="number" min="1" max="2147483647" step="1" value={editDraft.quota}
              onChange={(event) => setEditDraft((current) => ({ ...current, quota: event.target.value }))} />
          </label>
          <label>{t('Expiry (optional)')}
            <input type="datetime-local" value={editDraft.expiresAt}
              onChange={(event) => setEditDraft((current) => ({ ...current, expiresAt: event.target.value }))} />
          </label>
          <div className="redemption-edit-actions">
            <button type="submit" disabled={Boolean(busyAction)}>{busyAction === `edit:${editing.id}` ? t('Saving…') : t('Save changes')}</button>
            <button className="link" type="button" onClick={() => setEditing(null)}>{t('Cancel')}</button>
          </div>
        </form>
      )}
    </section>
  );
}
