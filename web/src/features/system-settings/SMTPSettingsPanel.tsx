import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { SystemOption } from './system-settings-api';
import { isAbortError } from './system-settings-api';
import {
  SMTPSettingsValidationError,
  updateSMTPSettings,
  validateSMTPSettingsUpdate,
  type SMTPSettingsData,
  type SMTPSettingsUpdate,
} from './smtp-settings-api';

type TransportMode = 'none' | 'implicit' | 'starttls';

interface SMTPDraft {
  server: string;
  port: string;
  account: string;
  from: string;
  token: string;
  tokenConfigured: boolean;
  clearToken: boolean;
  transport: TransportMode;
  insecureSkipVerify: boolean;
  forceAuthLogin: boolean;
}

type SMTPUpdater = (input: SMTPSettingsUpdate, signal?: AbortSignal) => Promise<SMTPSettingsData>;

function optionValue(options: ReadonlyMap<string, SystemOption>, key: string, fallback: string): string {
  return options.get(key)?.value ?? fallback;
}

function optionFlag(options: ReadonlyMap<string, SystemOption>, key: string): boolean {
  const value = optionValue(options, key, 'false');
  if (value === 'true') return true;
  if (value === 'false') return false;
  throw new Error('invalid SMTP option');
}

function draftFromOptions(options: readonly SystemOption[]): SMTPDraft {
  const indexed = new Map<string, SystemOption>();
  for (const option of options) {
    if (indexed.has(option.key)) throw new Error('duplicate option');
    indexed.set(option.key, option);
  }
  const port = optionValue(indexed, 'SMTPPort', '587');
  const sslEnabled = optionFlag(indexed, 'SMTPSSLEnabled');
  const startTLSEnabled = optionFlag(indexed, 'SMTPStartTLSEnabled');
  if (sslEnabled && startTLSEnabled) throw new Error('invalid SMTP option');
  const transport: TransportMode = startTLSEnabled ? 'starttls' : sslEnabled || port === '465' ? 'implicit' : 'none';
  const result: SMTPDraft = {
    server: optionValue(indexed, 'SMTPServer', ''),
    port,
    account: optionValue(indexed, 'SMTPAccount', ''),
    from: optionValue(indexed, 'SMTPFrom', ''),
    token: '',
    tokenConfigured: Boolean(indexed.get('SMTPToken')?.redacted || indexed.get('SMTPToken')?.value),
    clearToken: false,
    transport,
    insecureSkipVerify: optionFlag(indexed, 'SMTPInsecureSkipVerify'),
    forceAuthLogin: optionFlag(indexed, 'SMTPForceAuthLogin'),
  };
  validateSMTPSettingsUpdate(toUpdate(result));
  return result;
}

function draftFromResponse(response: SMTPSettingsData): SMTPDraft {
  return {
    server: response.server,
    port: response.port,
    account: response.account,
    from: response.from,
    token: '',
    tokenConfigured: response.tokenConfigured,
    clearToken: false,
    transport: response.startTLSEnabled
      ? 'starttls'
      : response.sslEnabled || response.port === '465' ? 'implicit' : 'none',
    insecureSkipVerify: response.insecureSkipVerify,
    forceAuthLogin: response.forceAuthLogin,
  };
}

function toUpdate(draft: SMTPDraft): SMTPSettingsUpdate {
  return {
    server: draft.server,
    port: draft.port,
    account: draft.account,
    from: draft.from,
    token: draft.token,
    currentTokenConfigured: draft.tokenConfigured,
    clearToken: draft.clearToken,
    sslEnabled: draft.transport === 'implicit',
    startTLSEnabled: draft.transport === 'starttls',
    insecureSkipVerify: draft.insecureSkipVerify,
    forceAuthLogin: draft.forceAuthLogin,
  };
}

function comparable(draft: SMTPDraft): string {
  return JSON.stringify({ ...draft, token: draft.token !== '', clearToken: draft.clearToken });
}

export function SMTPSettingsPanel({
  options,
  onSaved,
  update = updateSMTPSettings,
}: {
  options: readonly SystemOption[];
  onSaved: () => Promise<void> | void;
  update?: SMTPUpdater;
}) {
  const { t } = useTranslation();
  const prefix = useId();
  const mounted = useRef(true);
  const operation = useRef(0);
  const request = useRef<AbortController | null>(null);
  const parsed = useMemo(() => {
    try {
      return { value: draftFromOptions(options), error: false } as const;
    } catch {
      return { value: null, error: true } as const;
    }
  }, [options]);
  const [draft, setDraft] = useState<SMTPDraft | null>(parsed.value);
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      operation.current += 1;
      request.current?.abort();
    };
  }, []);

  useEffect(() => {
    setDraft(parsed.value);
    setBusy(false);
    setConfirming(false);
    setMessage(null);
  }, [parsed]);

  if (parsed.error || draft === null || parsed.value === null) {
    return (
      <section className="system-settings-tool" aria-labelledby={`${prefix}-title`}>
        <h3 id={`${prefix}-title`}>{t('SMTP delivery')}</h3>
        <div className="system-settings-inline-error" role="alert">
          <span>{t('Unable to read SMTP settings.')}</span>
          <button type="button" onClick={() => void onSaved()}>{t('Reload settings')}</button>
        </div>
      </section>
    );
  }

  const initial = parsed.value;
  const dirty = comparable(draft) !== comparable(initial) || draft.token !== '';
  const set = <Key extends keyof SMTPDraft>(key: Key, value: SMTPDraft[Key]) => {
    setDraft((current) => current ? { ...current, [key]: value } : current);
    setMessage(null);
  };

  const commit = async () => {
    if (busy || !dirty) return;
    const currentOperation = ++operation.current;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setConfirming(false);
    setMessage(null);
    try {
      const saved = await update(toUpdate(draft), controller.signal);
      if (!mounted.current || controller.signal.aborted || currentOperation !== operation.current) return;
      setDraft(draftFromResponse(saved));
      setMessage({ kind: 'success', text: t('SMTP settings saved.') });
      await onSaved();
    } catch (error) {
      if (!isAbortError(error) && mounted.current && currentOperation === operation.current) {
        setMessage({
          kind: 'error',
          text: error instanceof SMTPSettingsValidationError ? t(error.message) : t('Unable to save SMTP settings.'),
        });
      }
    } finally {
      if (mounted.current && !controller.signal.aborted && currentOperation === operation.current) setBusy(false);
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (busy || !dirty) return;
    try {
      validateSMTPSettingsUpdate(toUpdate(draft));
    } catch (error) {
      setMessage({
        kind: 'error',
        text: error instanceof SMTPSettingsValidationError ? t(error.message) : t('Enter valid SMTP settings.'),
      });
      return;
    }
    if (draft.insecureSkipVerify && !initial.insecureSkipVerify) setConfirming(true);
    else void commit();
  };

  return (
    <section className="system-settings-tool" aria-labelledby={`${prefix}-title`}>
      <header className="system-settings-tool-heading">
        <div>
          <h3 id={`${prefix}-title`}>{t('SMTP delivery')}</h3>
          <p>{t('Save the complete mail profile atomically. A complete deployment environment profile overrides these stored values.')}</p>
        </div>
        <span className="system-settings-root-badge">{draft.server ? t('Configured') : t('Not configured')}</span>
      </header>
      {!draft.server && <p className="system-settings-unavailable" role="note">{t('SMTP delivery is not configured. Verification and password-reset email cannot be sent.')}</p>}
      <form className="system-settings-form" onSubmit={submit} noValidate aria-busy={busy}>
        <div className="system-settings-form-grid">
          <label htmlFor={`${prefix}-server`}>
            {t('SMTP server')}
            <input id={`${prefix}-server`} value={draft.server} maxLength={253} autoComplete="off" disabled={busy} onChange={(event) => set('server', event.target.value)} />
          </label>
          <label htmlFor={`${prefix}-port`}>
            {t('SMTP port')}
            <input id={`${prefix}-port`} value={draft.port} inputMode="numeric" maxLength={5} disabled={busy} onChange={(event) => set('port', event.target.value)} />
          </label>
          <label htmlFor={`${prefix}-from`}>
            {t('Sender email')}
            <input id={`${prefix}-from`} value={draft.from} maxLength={320} autoComplete="email" disabled={busy} onChange={(event) => set('from', event.target.value)} />
          </label>
          <label htmlFor={`${prefix}-account`}>
            {t('SMTP account')}
            <input id={`${prefix}-account`} value={draft.account} maxLength={512} autoComplete="username" disabled={busy} onChange={(event) => set('account', event.target.value)} />
          </label>
          <label htmlFor={`${prefix}-token`}>
            {t('SMTP password')}
            <input
              id={`${prefix}-token`}
              type="password"
              value={draft.token}
              maxLength={4_096}
              autoComplete="new-password"
              disabled={busy}
              placeholder={draft.tokenConfigured ? t('Configured — enter a replacement') : undefined}
              onChange={(event) => {
                setDraft((current) => current ? { ...current, token: event.target.value, clearToken: false } : current);
                setMessage(null);
              }}
            />
          </label>
          <label htmlFor={`${prefix}-transport`}>
            {t('SMTP transport security')}
            <select id={`${prefix}-transport`} value={draft.transport} disabled={busy} onChange={(event) => set('transport', event.target.value as TransportMode)}>
              <option value="starttls">{t('STARTTLS')}</option>
              <option value="implicit">{t('Implicit TLS')}</option>
              <option value="none">{t('Plaintext (loopback only)')}</option>
            </select>
          </label>
        </div>
        <fieldset>
          <legend>{t('SMTP security and authentication')}</legend>
          <label className="system-settings-checkbox">
            <input
              type="checkbox"
              checked={draft.clearToken}
              disabled={busy || !draft.tokenConfigured && draft.token === ''}
              onChange={(event) => {
                setDraft((current) => current ? { ...current, clearToken: event.target.checked, token: '' } : current);
                setMessage(null);
              }}
            />
            {t('Clear the stored SMTP password')}
          </label>
          <label className="system-settings-checkbox">
            <input type="checkbox" checked={draft.forceAuthLogin} disabled={busy} onChange={(event) => set('forceAuthLogin', event.target.checked)} />
            {t('Force SMTP LOGIN authentication')}
          </label>
          <label className="system-settings-checkbox">
            <input type="checkbox" checked={draft.insecureSkipVerify} disabled={busy} onChange={(event) => set('insecureSkipVerify', event.target.checked)} />
            {t('Disable TLS certificate verification')}
          </label>
        </fieldset>
        <p className="system-settings-field-copy">{t('Remote servers require TLS. Plaintext SMTP is accepted only for localhost or a literal loopback address.')}</p>
        <div className="system-settings-field-actions">
          <button type="button" disabled={busy || !dirty} onClick={() => { setDraft(initial); setMessage(null); }}>{t('Reset')}</button>
          <button type="submit" className="primary" disabled={busy || !dirty}>{busy ? t('Saving…') : t('Save SMTP settings')}</button>
        </div>
        {message && <p className={`system-settings-field-message ${message.kind}`} role={message.kind === 'error' ? 'alert' : 'status'}>{message.text}</p>}
      </form>
      {confirming && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby={`${prefix}-confirm-title`} aria-describedby={`${prefix}-confirm-description`}>
            <h3 id={`${prefix}-confirm-title`}>{t('Disable certificate verification?')}</h3>
            <p id={`${prefix}-confirm-description`}>{t('This makes SMTP vulnerable to an active network attacker. Use it only with a server you control.')}</p>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={() => setConfirming(false)}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={() => void commit()}>{t('Confirm and save')}</button>
            </footer>
          </section>
        </div>
      )}
    </section>
  );
}
