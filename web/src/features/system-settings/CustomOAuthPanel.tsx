import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import {
  createCustomOAuthProvider,
  deleteCustomOAuthProvider,
  discoverCustomOAuthProvider,
  loadCustomOAuthProviders,
  updateCustomOAuthProvider,
  type CustomOAuthInput,
  type CustomOAuthProvider,
} from './custom-oauth-api';
import { isAbortError } from './system-settings-api';

const EMPTY_PROVIDER: CustomOAuthInput = {
  name: '',
  slug: '',
  icon: '',
  enabled: false,
  clientId: '',
  clientSecret: '',
  authorizationEndpoint: '',
  tokenEndpoint: '',
  userInfoEndpoint: '',
  scopes: 'openid profile email',
  userIdField: 'sub',
  usernameField: 'preferred_username',
  displayNameField: 'name',
  emailField: 'email',
  wellKnown: '',
  authStyle: 0,
  accessPolicy: '',
  accessDeniedMessage: '',
};

type Notice = { kind: 'success' | 'error'; text: string } | null;

function toInput(provider: CustomOAuthProvider): CustomOAuthInput {
  return { ...provider, clientSecret: '' };
}

function OAuthProviderForm({
  editing,
  initial,
  busy,
  onCancel,
  onSave,
}: {
  editing: boolean;
  initial: CustomOAuthInput;
  busy: boolean;
  onCancel: () => void;
  onSave: (input: CustomOAuthInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(initial);
  const [discovering, setDiscovering] = useState(false);
  const [error, setError] = useState('');
  const discoveryController = useRef<AbortController | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      discoveryController.current?.abort();
    };
  }, []);

  async function discover() {
    if (discovering || draft.wellKnown.trim() === '') return;
    discoveryController.current?.abort();
    const controller = new AbortController();
    discoveryController.current = controller;
    setDiscovering(true);
    setError('');
    try {
      const result = await discoverCustomOAuthProvider(draft.wellKnown, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      setDraft((current) => ({
        ...current,
        wellKnown: result.wellKnownUrl,
        authorizationEndpoint: result.authorizationEndpoint || current.authorizationEndpoint,
        tokenEndpoint: result.tokenEndpoint || current.tokenEndpoint,
        userInfoEndpoint: result.userInfoEndpoint || current.userInfoEndpoint,
        scopes: result.scopes || current.scopes,
      }));
    } catch (caught) {
      if (!isAbortError(caught) && mounted.current) setError(t('Unable to load OAuth discovery metadata.'));
    } finally {
      if (mounted.current && !controller.signal.aborted) setDiscovering(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    setError('');
    void onSave(draft).catch(() => {
      if (mounted.current) setError(t('Invalid OAuth provider settings.'));
    });
  }

  return (
    <div className="system-settings-overlay" role="presentation">
      <section className="system-settings-dialog system-settings-oauth-dialog" role="dialog" aria-modal="true" aria-labelledby="oauth-editor-title">
        <header className="system-settings-dialog-header">
          <div>
            <h3 id="oauth-editor-title">{editing ? t('Edit OAuth provider') : t('Add OAuth provider')}</h3>
            <p>{t('Client secrets are write-only. Leave the secret blank while editing to keep the current value.')}</p>
          </div>
          <button type="button" onClick={onCancel} disabled={busy} aria-label={t('Close')}>×</button>
        </header>
        <form className="system-settings-form" onSubmit={submit}>
          <div className="system-settings-form-grid">
            <label>{t('Provider name')}<input autoFocus required maxLength={64} value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /></label>
            <label>{t('Provider slug')}<input required maxLength={64} pattern="[a-z0-9][a-z0-9-]*" value={draft.slug} onChange={(event) => setDraft({ ...draft, slug: event.target.value.toLowerCase() })} /></label>
            <label>{t('Icon')}<input maxLength={128} value={draft.icon} onChange={(event) => setDraft({ ...draft, icon: event.target.value })} /></label>
            <label className="system-settings-checkbox"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} />{t('Enabled')}</label>
          </div>
          <fieldset>
            <legend>{t('OAuth client')}</legend>
            <div className="system-settings-form-grid">
              <label>{t('Client ID')}<input required maxLength={256} autoComplete="off" value={draft.clientId} onChange={(event) => setDraft({ ...draft, clientId: event.target.value })} /></label>
              <label>{t('Client secret')}<input required={!editing} type="password" maxLength={512} autoComplete="new-password" value={draft.clientSecret} placeholder={editing ? t('Keep current secret') : undefined} onChange={(event) => setDraft({ ...draft, clientSecret: event.target.value })} /></label>
              <label>{t('Token authentication style')}
                <select value={draft.authStyle} onChange={(event) => setDraft({ ...draft, authStyle: Number(event.target.value) as 0 | 1 | 2 })}>
                  <option value={0}>{t('Automatic')}</option>
                  <option value={1}>{t('Client secret in request body')}</option>
                  <option value={2}>{t('HTTP Basic authentication')}</option>
                </select>
              </label>
              <label>{t('Scopes')}<input maxLength={256} value={draft.scopes} onChange={(event) => setDraft({ ...draft, scopes: event.target.value })} /></label>
            </div>
          </fieldset>
          <fieldset>
            <legend>{t('Discovery and endpoints')}</legend>
            <div className="system-settings-discovery-row">
              <label>{t('Discovery URL')}<input maxLength={512} type="url" value={draft.wellKnown} onChange={(event) => setDraft({ ...draft, wellKnown: event.target.value })} /></label>
              <button type="button" disabled={busy || discovering || draft.wellKnown.trim() === ''} onClick={() => void discover()}>{discovering ? t('Loading…') : t('Discover')}</button>
            </div>
            <div className="system-settings-form-grid">
              <label>{t('Authorization endpoint')}<input required maxLength={512} type="url" value={draft.authorizationEndpoint} onChange={(event) => setDraft({ ...draft, authorizationEndpoint: event.target.value })} /></label>
              <label>{t('Token endpoint')}<input required maxLength={512} type="url" value={draft.tokenEndpoint} onChange={(event) => setDraft({ ...draft, tokenEndpoint: event.target.value })} /></label>
              <label>{t('User information endpoint')}<input required maxLength={512} type="url" value={draft.userInfoEndpoint} onChange={(event) => setDraft({ ...draft, userInfoEndpoint: event.target.value })} /></label>
            </div>
          </fieldset>
          <fieldset>
            <legend>{t('Profile field mapping')}</legend>
            <div className="system-settings-form-grid">
              <label>{t('User ID field')}<input required maxLength={128} value={draft.userIdField} onChange={(event) => setDraft({ ...draft, userIdField: event.target.value })} /></label>
              <label>{t('Username field')}<input required maxLength={128} value={draft.usernameField} onChange={(event) => setDraft({ ...draft, usernameField: event.target.value })} /></label>
              <label>{t('Display name field')}<input required maxLength={128} value={draft.displayNameField} onChange={(event) => setDraft({ ...draft, displayNameField: event.target.value })} /></label>
              <label>{t('Email field')}<input required maxLength={128} value={draft.emailField} onChange={(event) => setDraft({ ...draft, emailField: event.target.value })} /></label>
            </div>
          </fieldset>
          <label>{t('Access policy (JSON)')}<textarea rows={5} maxLength={64 * 1024} value={draft.accessPolicy} onChange={(event) => setDraft({ ...draft, accessPolicy: event.target.value })} /></label>
          <label>{t('Access denied message')}<input maxLength={512} value={draft.accessDeniedMessage} onChange={(event) => setDraft({ ...draft, accessDeniedMessage: event.target.value })} /></label>
          {error && <p className="system-settings-notice error" role="alert">{error}</p>}
          <footer className="system-settings-dialog-actions">
            <button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button>
            <button type="submit" className="primary" disabled={busy}>{busy ? t('Saving…') : t('Save changes')}</button>
          </footer>
        </form>
      </section>
    </div>
  );
}

export function CustomOAuthPanel() {
  const { t } = useTranslation();
  const mounted = useRef(true);
  const loadGeneration = useRef(0);
  const loadController = useRef<AbortController | null>(null);
  const mutationController = useRef<AbortController | null>(null);
  const [providers, setProviders] = useState<CustomOAuthProvider[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [editing, setEditing] = useState<CustomOAuthProvider | 'new' | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const [pendingDelete, setPendingDelete] = useState<CustomOAuthProvider | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      loadController.current?.abort();
      mutationController.current?.abort();
    };
  }, []);

  async function load() {
    const generation = ++loadGeneration.current;
    loadController.current?.abort();
    const controller = new AbortController();
    loadController.current = controller;
    setLoading(true);
    setLoadError(false);
    try {
      const result = await loadCustomOAuthProviders(controller.signal);
      if (mounted.current && generation === loadGeneration.current && !controller.signal.aborted) setProviders(result);
    } catch (error) {
      if (mounted.current && generation === loadGeneration.current && !isAbortError(error)) setLoadError(true);
    } finally {
      if (mounted.current && generation === loadGeneration.current && !controller.signal.aborted) setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  async function save(input: CustomOAuthInput) {
    if (busy || editing === null) return;
    mutationController.current?.abort();
    const controller = new AbortController();
    mutationController.current = controller;
    setBusy(true);
    setNotice(null);
    try {
      const saved = editing === 'new'
        ? await createCustomOAuthProvider(input, controller.signal)
        : await updateCustomOAuthProvider(editing.id, input, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      setProviders((current) => editing === 'new'
        ? [...current, saved].sort((a, b) => a.id - b.id)
        : current.map((provider) => provider.id === saved.id ? saved : provider));
      setEditing(null);
      setNotice({ kind: 'success', text: t('OAuth provider saved.') });
    } catch (error) {
      if (!isAbortError(error) && mounted.current) {
        setNotice({ kind: 'error', text: error instanceof Error ? error.message : t('Request failed.') });
      }
      throw error;
    } finally {
      if (mounted.current && !controller.signal.aborted) setBusy(false);
    }
  }

  async function remove() {
    if (busy || !pendingDelete) return;
    const target = pendingDelete;
    mutationController.current?.abort();
    const controller = new AbortController();
    mutationController.current = controller;
    setBusy(true);
    setNotice(null);
    try {
      await deleteCustomOAuthProvider(target.id, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      setProviders((current) => current.filter((provider) => provider.id !== target.id));
      setPendingDelete(null);
      setNotice({ kind: 'success', text: t('OAuth provider deleted.') });
    } catch (error) {
      if (!isAbortError(error) && mounted.current) {
        setNotice({ kind: 'error', text: error instanceof Error ? error.message : t('Request failed.') });
      }
    } finally {
      if (mounted.current && !controller.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="system-settings-tool" aria-labelledby="custom-oauth-title">
      <header className="system-settings-tool-heading">
        <div><h3 id="custom-oauth-title">{t('Custom OAuth providers')}</h3><p>{t('Provider secrets are never returned by the server.')}</p></div>
        <button type="button" onClick={() => setEditing('new')}>{t('Add provider')}</button>
      </header>
      {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      {loading ? <p role="status">{t('Loading…')}</p> : loadError ? (
        <div className="system-settings-inline-error" role="alert"><span>{t('Unable to load OAuth providers.')}</span><button type="button" onClick={() => void load()}>{t('Retry')}</button></div>
      ) : providers.length === 0 ? <p className="system-settings-empty">{t('No custom OAuth providers configured.')}</p> : (
        <div className="system-settings-table-wrap">
          <table className="system-settings-table">
            <caption>{t('Custom OAuth providers')}</caption>
            <thead><tr><th scope="col">{t('Provider')}</th><th scope="col">{t('Status')}</th><th scope="col">{t('Client ID')}</th><th scope="col">{t('Actions')}</th></tr></thead>
            <tbody>{providers.map((provider) => (
              <tr key={provider.id}>
                <th scope="row"><span>{provider.name}</span><small>{provider.slug}</small></th>
                <td>{provider.enabled ? t('Enabled') : t('Disabled')}</td>
                <td className="system-settings-break">{provider.clientId}</td>
                <td><div className="system-settings-row-actions"><button type="button" onClick={() => setEditing(provider)}>{t('Edit')}</button><button type="button" className="danger" onClick={() => setPendingDelete(provider)}>{t('Delete')}</button></div></td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      )}
      {editing && (
        <OAuthProviderForm
          key={editing === 'new' ? 'new' : editing.id}
          editing={editing !== 'new'}
          initial={editing === 'new' ? EMPTY_PROVIDER : toInput(editing)}
          busy={busy}
          onCancel={() => { if (!busy) setEditing(null); }}
          onSave={save}
        />
      )}
      {pendingDelete && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-oauth-title" aria-describedby="delete-oauth-description">
            <h3 id="delete-oauth-title">{t('Delete OAuth provider?')}</h3>
            <p id="delete-oauth-description">{t('Users cannot sign in with this provider after it is deleted. The server blocks deletion while bindings still exist.')}</p>
            <strong>{pendingDelete.name}</strong>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={() => setPendingDelete(null)}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={() => void remove()}>{busy ? t('Deleting…') : t('Delete')}</button>
            </footer>
          </section>
        </div>
      )}
    </section>
  );
}
