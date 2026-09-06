import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  SYSTEM_SETTINGS_CATEGORIES,
  resolveSettingsPath,
  validateSettingValue,
  type SettingDefinition,
} from './settings-catalog';
import {
  isAbortError,
  redactProvidedSystemOptions,
  updateSystemOption,
  type SystemOption,
} from './system-settings-api';
import './system-settings.css';

export type { SystemOption } from './system-settings-api';

type SaveOption = (input: { key: string; value: string }, signal?: AbortSignal) => Promise<void>;

function safeProvidedOptions(options: readonly SystemOption[]): SystemOption[] {
  try {
    return redactProvidedSystemOptions(options);
  } catch {
    return [];
  }
}

export function SettingEditor({
  option,
  setting,
  saveOption = updateSystemOption,
  onSaved,
}: {
  option?: SystemOption;
  setting: SettingDefinition;
  saveOption?: SaveOption;
  onSaved: () => Promise<void> | void;
}) {
  const { t } = useTranslation();
  const inputId = useId();
  const helpId = `${inputId}-help`;
  const compatibilityId = `${inputId}-compatibility`;
  const errorId = `${inputId}-error`;
  const mounted = useRef(true);
  const request = useRef<AbortController | null>(null);
  const operation = useRef(0);
  const secret = setting.kind === 'secret';
  const fixed = setting.fixedValue !== undefined;
  const original = fixed ? setting.fixedValue ?? '' : secret ? '' : option?.value ?? setting.defaultValue ?? '';
  const configuredSecret = secret && Boolean(option?.redacted || option?.value);
  const legacyQuotaFallback = setting.key === 'QuotaForNewUser' && option?.compatibilitySource === 'InitialQuota';
  const [value, setValue] = useState(original);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);
  const [confirming, setConfirming] = useState(false);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      operation.current += 1;
      request.current?.abort();
    };
  }, []);

  useEffect(() => {
    setValue(original);
    setMessage(null);
    setConfirming(false);
  }, [original, option?.redacted, setting.key]);

  const dirty = secret ? value !== '' : value !== original || legacyQuotaFallback;

  async function commit() {
    if (busy || !dirty) return;
    const currentOperation = ++operation.current;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setConfirming(false);
    setBusy(true);
    setMessage(null);
    try {
      await saveOption({ key: setting.key, value }, controller.signal);
      if (!mounted.current || controller.signal.aborted || currentOperation !== operation.current) return;
      setMessage({ kind: 'success', text: t('Saved.') });
      if (secret) setValue('');
      await onSaved();
    } catch (error) {
      if (!isAbortError(error) && mounted.current && currentOperation === operation.current) {
        setMessage({ kind: 'error', text: t('Request failed.') });
      }
    } finally {
      if (mounted.current && !controller.signal.aborted && currentOperation === operation.current) setBusy(false);
    }
  }

  function requestSave() {
    if (busy || !dirty) return;
    const validation = validateSettingValue(setting, value);
    if (validation) {
      setMessage({ kind: 'error', text: t(validation) });
      return;
    }
    if (setting.confirmation) setConfirming(true);
    else void commit();
  }

  const describedBy = [
    setting.description ? helpId : '',
    legacyQuotaFallback ? compatibilityId : '',
    message?.kind === 'error' ? errorId : '',
  ].filter(Boolean).join(' ') || undefined;
  let control: ReactNode;
  if (fixed) {
    control = (
      <input
        id={inputId}
        aria-describedby={describedBy}
        aria-readonly="true"
        readOnly
        type="text"
        value={value}
      />
    );
  } else if (setting.kind === 'boolean') {
    control = (
      <select
        id={inputId}
        aria-describedby={describedBy}
        aria-invalid={message?.kind === 'error' || undefined}
        required={setting.required}
        value={value}
        onChange={(event) => { setValue(event.target.value); setMessage(null); }}
      >
        <option value="">{t('Use server default')}</option>
        <option value="true">{t('Enabled')}</option>
        <option value="false">{t('Disabled')}</option>
      </select>
    );
  } else if (setting.kind === 'select') {
    control = (
      <select
        id={inputId}
        aria-describedby={describedBy}
        aria-invalid={message?.kind === 'error' || undefined}
        required={setting.required}
        value={value}
        onChange={(event) => { setValue(event.target.value); setMessage(null); }}
      >
        {setting.choices?.map((choice) => <option key={choice.value} value={choice.value}>{t(choice.label)}</option>)}
      </select>
    );
  } else if (setting.kind === 'json' || setting.kind === 'textarea') {
    control = (
      <textarea
        id={inputId}
        className={setting.kind === 'json' ? 'system-settings-code' : undefined}
        aria-describedby={describedBy}
        aria-invalid={message?.kind === 'error' || undefined}
        required={setting.required}
        rows={setting.kind === 'json' ? 8 : 5}
        maxLength={setting.maxLength}
        value={value}
        onChange={(event) => { setValue(event.target.value); setMessage(null); }}
      />
    );
  } else {
    control = (
      <input
        id={inputId}
        aria-describedby={describedBy}
        aria-invalid={message?.kind === 'error' || undefined}
        type={secret ? 'password' : 'text'}
        inputMode={setting.kind === 'integer' ? 'numeric' : setting.kind === 'decimal' ? 'decimal' : undefined}
        min={setting.min}
        max={setting.max}
        required={setting.required}
        maxLength={setting.maxLength}
        value={value}
        placeholder={secret ? configuredSecret ? t('Configured — enter a replacement') : t('Enter a write-only value') : undefined}
        autoComplete={secret ? 'new-password' : undefined}
        onChange={(event) => { setValue(event.target.value); setMessage(null); }}
      />
    );
  }

  return (
    <div className="system-settings-field">
      <div className="system-settings-field-copy">
        <label htmlFor={inputId}>{t(setting.label)}</label>
        {setting.description && <p id={helpId}>{t(setting.description)}</p>}
        {legacyQuotaFallback && (
          <p id={compatibilityId} role="note">
            {t('Legacy InitialQuota is currently supplying this value. Save it to create the canonical QuotaForNewUser option.')}
          </p>
        )}
        {secret && <p>{t('Write-only: the existing value is never loaded into this page.')}</p>}
      </div>
      <div className="system-settings-control">
        {control}
        {!fixed && (
          <div className="system-settings-field-actions">
            <button type="button" disabled={busy || !dirty} onClick={() => { setValue(original); setMessage(null); }}>{t('Reset')}</button>
            <button type="button" className="primary" disabled={busy || !dirty} onClick={requestSave}>
              {busy ? t('Saving…') : t('Save changes')}
            </button>
          </div>
        )}
        {message && (
          <p id={message.kind === 'error' ? errorId : undefined} className={`system-settings-field-message ${message.kind}`} role={message.kind === 'error' ? 'alert' : 'status'}>
            {message.text}
          </p>
        )}
      </div>
      {confirming && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby={`${inputId}-confirm-title`} aria-describedby={`${inputId}-confirm-description`}>
            <h3 id={`${inputId}-confirm-title`}>{t('Confirm setting change')}</h3>
            <p id={`${inputId}-confirm-description`}>{t(setting.confirmation!)}</p>
            <dl className="system-settings-confirm-value">
              <div><dt>{t('Setting')}</dt><dd>{t(setting.label)}</dd></div>
              <div><dt>{t('New value')}</dt><dd>{secret ? t('Write-only value') : value || t('Empty')}</dd></div>
            </dl>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={() => setConfirming(false)}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={() => void commit()}>{t('Confirm and save')}</button>
            </footer>
          </section>
        </div>
      )}
    </div>
  );
}

export interface SystemSettingsEditorProps {
  options: readonly SystemOption[];
  settingsPath?: string;
  onNavigate: (target: string) => void;
  onSaved: () => Promise<void> | void;
  supplements?: Record<string, ReactNode>;
  saveOption?: SaveOption;
}

export function SystemSettingsEditor({
  options,
  settingsPath,
  onNavigate,
  onSaved,
  supplements = {},
  saveOption = updateSystemOption,
}: SystemSettingsEditorProps) {
  const { t } = useTranslation();
  const { category, section } = resolveSettingsPath(settingsPath);
  const safeOptions = useMemo(() => safeProvidedOptions(options), [options]);
  const optionByKey = useMemo(() => new Map(safeOptions.map((option) => [option.key, option])), [safeOptions]);
  const path = `${category.id}/${section.id}`;

  return (
    <section className="card system-settings" aria-labelledby="system-settings-title">
      <header className="system-settings-heading">
        <div>
          <h1 id="system-settings-title">{t('System settings')}</h1>
          <p>{t(section.description)}</p>
        </div>
        <span className="system-settings-root-badge">{t('Root only')}</span>
      </header>
      <nav className="system-settings-tabs system-settings-categories" aria-label={t('Settings categories')}>
        {SYSTEM_SETTINGS_CATEGORIES.map((candidate) => (
          <button
            type="button"
            key={candidate.id}
            className={candidate.id === category.id ? 'active' : undefined}
            aria-current={candidate.id === category.id ? 'page' : undefined}
            onClick={() => onNavigate(`/system-settings/${candidate.id}/${candidate.sections[0].id}`)}
          >
            {t(candidate.label)}
          </button>
        ))}
      </nav>
      <nav className="system-settings-tabs system-settings-sections" aria-label={`${t(category.label)} ${t('Settings')}`}>
        {category.sections.map((candidate) => (
          <button
            type="button"
            key={candidate.id}
            className={candidate.id === section.id ? 'active' : undefined}
            aria-current={candidate.id === section.id ? 'page' : undefined}
            onClick={() => onNavigate(`/system-settings/${category.id}/${candidate.id}`)}
          >
            {t(candidate.title)}
          </button>
        ))}
      </nav>

      <section className="system-settings-section" aria-labelledby="system-settings-section-title">
        <header className="system-settings-section-heading">
          <h2 id="system-settings-section-title">{t(section.title)}</h2>
          <p>{t(section.description)}</p>
        </header>
        {supplements[path]}
        {section.unavailableReason && (
          <div className="system-settings-unavailable" role="note">
            <strong>{t('Not available in this deployment')}</strong>
            <p>{t(section.unavailableReason)}</p>
          </div>
        )}
        {!section.unavailableReason && section.groups.length === 0 && !supplements[path] && (
          <p className="system-settings-empty">{t('No settings are available in this section.')}</p>
        )}
        {section.groups.map((group) => {
          const headingId = `settings-${category.id}-${section.id}-${group.title.replace(/[^a-zA-Z0-9]+/g, '-').toLowerCase()}`;
          return (
            <section className="system-settings-group" key={group.title} aria-labelledby={headingId}>
              <header><h3 id={headingId}>{t(group.title)}</h3>{group.description && <p>{t(group.description)}</p>}</header>
              <div className="system-settings-field-list">
                {group.settings.map((setting) => (
                  <SettingEditor
                    key={setting.key}
                    setting={setting}
                    option={optionByKey.get(setting.key)}
                    saveOption={saveOption}
                    onSaved={onSaved}
                  />
                ))}
              </div>
            </section>
          );
        })}
      </section>
    </section>
  );
}
