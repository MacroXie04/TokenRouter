import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, getData, postData, putData, type ApiResponse } from '../../shared/api/client';
import { MAX_PREFILL_ITEMS, MAX_PREFILL_ITEM_BYTES, MAX_PREFILL_JSON_BYTES, parsePrefillGroups, type PrefillGroup } from './prefill-group-api';

export function PrefillGroupPanel() {
  const { t } = useTranslation();
  const [groups, setGroups] = useState<PrefillGroup[]>([]);
  const [filter, setFilter] = useState('');
  const [editingId, setEditingId] = useState<number | null>(null);
  const [name, setName] = useState('');
  const [type, setType] = useState('model');
  const [items, setItems] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);

  const refreshGroups = useCallback(async () => {
    try {
      const response = await getData<unknown>('/prefill_group/', filter ? { type: filter } : undefined);
      setGroups(parsePrefillGroups(response));
    } catch {
      setGroups([]);
      setMessage({ kind: 'error', text: t('Could not load prefill groups') });
    }
  }, [filter, t]);

  useEffect(() => { void refreshGroups(); }, [refreshGroups]);

  function resetForm() {
    setEditingId(null);
    setName('');
    setType('model');
    setItems('');
    setDescription('');
  }

  function editGroup(group: PrefillGroup) {
    setEditingId(group.id);
    setName(group.name);
    setType(group.type);
    setDescription(group.description ?? '');
    if (typeof group.items === 'string') setItems(group.items);
    else if (Array.isArray(group.items)) setItems(group.items.join('\n'));
    else setItems(JSON.stringify(group.items ?? {}, null, 2));
    setMessage(null);
  }

  async function saveGroup(event: React.FormEvent) {
    event.preventDefault();
    if (busy) return;
    const normalizedName = name.trim();
    if (normalizedName.length === 0 || normalizedName.length > 64 || description.length > 255 || items.length > MAX_PREFILL_JSON_BYTES) {
      setMessage({ kind: 'error', text: t('Prefill group values exceed safe limits.') });
      return;
    }
    let payloadItems: string | string[];
    if (type === 'endpoint') {
      try {
        JSON.parse(items || '{}');
      } catch {
        setMessage({ kind: 'error', text: t('Endpoint items must contain valid JSON.') });
        return;
      }
      payloadItems = items || '{}';
    } else {
      payloadItems = items.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
      if (payloadItems.length > MAX_PREFILL_ITEMS || payloadItems.some((item) => item.length > MAX_PREFILL_ITEM_BYTES)) {
        setMessage({ kind: 'error', text: t('Prefill group items exceed safe limits.') });
        return;
      }
    }
    setBusy(true);
    setMessage(null);
    try {
      const payload = { id: editingId ?? undefined, name: normalizedName, type, items: payloadItems, description };
      if (editingId) await putData<PrefillGroup>('/prefill_group/', payload);
      else await postData<PrefillGroup>('/prefill_group/', payload);
      setMessage({ kind: 'success', text: editingId ? t('Prefill group updated.') : t('Prefill group created.') });
      resetForm();
      await refreshGroups();
    } catch {
      setMessage({ kind: 'error', text: t('Could not save prefill group') });
    } finally {
      setBusy(false);
    }
  }

  async function deleteGroup(group: PrefillGroup) {
    if (busy || !window.confirm(t('Delete prefill group “{{name}}”?', { name: group.name }))) return;
    setBusy(true);
    setMessage(null);
    try {
      const response = await api.delete<ApiResponse<null>>(`/prefill_group/${group.id}`);
      if (!response.data.success) throw new Error(response.data.message || 'Delete failed');
      if (editingId === group.id) resetForm();
      setMessage({ kind: 'success', text: t('Prefill group deleted.') });
      await refreshGroups();
    } catch {
      setMessage({ kind: 'error', text: t('Could not delete prefill group') });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card">
      <h2>{t('Prefill groups')}</h2>
      <p className="muted">{t('Manage reusable model, tag, and endpoint sets used by channel forms.')}</p>
      <label>
        {t('Filter by type')}
        <select value={filter} onChange={(event) => setFilter(event.target.value)}>
          <option value="">{t('All types')}</option>
          <option value="model">{t('Model')}</option>
          <option value="tag">{t('Tag')}</option>
          <option value="endpoint">{t('Endpoint')}</option>
        </select>
      </label>
      <form className="grid-form" onSubmit={saveGroup}>
        <label>{t('Name')}<input value={name} maxLength={64} onChange={(event) => setName(event.target.value)} required /></label>
        <label>{t('Type')}
          <select value={type} onChange={(event) => setType(event.target.value)}>
            <option value="model">{t('Model')}</option>
            <option value="tag">{t('Tag')}</option>
            <option value="endpoint">{t('Endpoint')}</option>
          </select>
        </label>
        <label>{t('Description')}<input value={description} maxLength={255} onChange={(event) => setDescription(event.target.value)} /></label>
        <label>
          {type === 'endpoint' ? t('Endpoint JSON') : t('Items (one per line or comma-separated)')}
          <textarea rows={6} maxLength={MAX_PREFILL_JSON_BYTES} value={items} onChange={(event) => setItems(event.target.value)} />
        </label>
        <div className="inline-form">
          <button type="submit" disabled={busy}>{busy ? t('Saving…') : editingId ? t('Update group') : t('Create group')}</button>
          {editingId && <button type="button" className="link" disabled={busy} onClick={resetForm}>{t('Cancel edit')}</button>}
        </div>
      </form>
      {message && <p className={message.kind}>{message.text}</p>}
      {groups.length === 0 ? <p className="muted">{t('No prefill groups.')}</p> : (
        <ul className="key-list">
          {groups.map((group) => (
            <li key={group.id}>
              <strong>{group.name}</strong>
              <span className="muted">{group.type}</span>
              <span className="muted">{group.description || t('No description')}</span>
              <button type="button" className="link" disabled={busy} onClick={() => editGroup(group)}>{t('Edit')}</button>
              <button type="button" className="link" disabled={busy} onClick={() => void deleteGroup(group)}>{t('Delete')}</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
