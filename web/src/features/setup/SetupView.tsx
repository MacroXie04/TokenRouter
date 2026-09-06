import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { postData } from '../../shared/api/client';
import {
  buildSetupPayload,
  initialSetupUsageMode,
  type SetupFormValues,
  type SetupStatus,
  type SetupUsageMode,
} from './setup';

const STEP_KEYS = ['Database', 'Administrator', 'Usage mode', 'Review'] as const;

const MODE_OPTIONS: Array<{
  value: SetupUsageMode;
  title: string;
  description: string;
}> = [
  {
    value: 'external',
    title: 'External operations',
    description: 'Serve multiple users with registration, billing, and quota controls.',
  },
  {
    value: 'self',
    title: 'Personal use',
    description: 'Run a private installation without public account registration.',
  },
  {
    value: 'demo',
    title: 'Demo site',
    description: 'Present a demonstration installation with its mode clearly identified.',
  },
];

function databaseDescription(databaseType: string) {
  switch (databaseType.toLowerCase()) {
    case 'sqlite':
      return 'Keep the SQLite data file on persistent storage and include it in backups.';
    case 'mysql':
      return 'MySQL is connected. Configure automated backups before serving production traffic.';
    case 'postgres':
      return 'PostgreSQL is connected. Configure automated backups before serving production traffic.';
    default:
      return 'The configured database is connected and ready for initialization.';
  }
}

function modeTitle(mode: SetupUsageMode) {
  return MODE_OPTIONS.find((option) => option.value === mode)?.title ?? 'External operations';
}

export function SetupView({ status, onDone }: { status: SetupStatus; onDone: () => void }) {
  const { t } = useTranslation();
  const [step, setStep] = useState(0);
  const [values, setValues] = useState<SetupFormValues>(() => ({
    username: '',
    password: '',
    confirmPassword: '',
    usageMode: initialSetupUsageMode(status),
  }));
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  function update<K extends keyof SetupFormValues>(key: K, value: SetupFormValues[K]) {
    setValues((current) => ({ ...current, [key]: value }));
    setError('');
  }

  function validateAdministrator() {
    if (status.rootInitialized) return true;
    const username = values.username.trim();
    if (username.length < 3 || username.length > 12) {
      setError(t('Username must be 3 to 12 characters.'));
      return false;
    }
    if (values.password.length < 8 || values.password.length > 64) {
      setError(t('Password must be 8 to 64 characters.'));
      return false;
    }
    if (values.password !== values.confirmPassword) {
      setError(t('Passwords do not match.'));
      return false;
    }
    return true;
  }

  function next() {
    setError('');
    if (step === 1 && !validateAdministrator()) return;
    setStep((current) => Math.min(current + 1, STEP_KEYS.length - 1));
  }

  function back() {
    setError('');
    setStep((current) => Math.max(current - 1, 0));
  }

  async function submit() {
    setError('');
    if (!validateAdministrator()) return;
    setBusy(true);
    try {
      await postData('/setup', buildSetupPayload(values, status.rootInitialized));
      onDone();
    } catch {
      setError(t('Initialization failed. Please try again.'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="app setup-app">
      <header className="header setup-header">
        <h1>{t('Set up TokenRouter')}</h1>
        <p className="tagline">{t('Complete these four steps to make this installation ready.')}</p>
      </header>

      <nav className="setup-progress" aria-label={t('Setup progress')}>
        <ol>
          {STEP_KEYS.map((key, index) => (
            <li key={key} aria-current={index === step ? 'step' : undefined} data-complete={index < step || undefined}>
              <span aria-hidden="true">{index + 1}</span>
              {t(key)}
            </li>
          ))}
        </ol>
      </nav>

      <section className="card setup-card" aria-labelledby={`setup-step-${step}`}>
        <p className="muted setup-step-count">{t('Step {{current}} of {{total}}', { current: step + 1, total: STEP_KEYS.length })}</p>

        {step === 0 && (
          <div>
            <h2 id="setup-step-0">{t('Database connection')}</h2>
            <dl className="kv setup-summary">
              <dt>{t('Detected database')}</dt>
              <dd><strong>{status.databaseType}</strong></dd>
              <dt>{t('Status')}</dt>
              <dd className="success">{t('Connected')}</dd>
            </dl>
            <p className="muted">{t(databaseDescription(status.databaseType))}</p>
          </div>
        )}

        {step === 1 && (
          <div>
            <h2 id="setup-step-1">{t('Administrator account')}</h2>
            {status.rootInitialized ? (
              <p className="success" role="status">{t('An administrator already exists. Its credentials will be kept.')}</p>
            ) : (
              <div className="setup-fields">
                <label>
                  {t('Administrator username')}
                  <input
                    value={values.username}
                    onChange={(event) => update('username', event.target.value)}
                    required
                    minLength={3}
                    maxLength={12}
                    autoComplete="username"
                    autoFocus
                  />
                </label>
                <label>
                  {t('Password')}
                  <input
                    type="password"
                    value={values.password}
                    onChange={(event) => update('password', event.target.value)}
                    required
                    minLength={8}
                    maxLength={64}
                    autoComplete="new-password"
                  />
                </label>
                <label>
                  {t('Confirm password')}
                  <input
                    type="password"
                    value={values.confirmPassword}
                    onChange={(event) => update('confirmPassword', event.target.value)}
                    required
                    minLength={8}
                    maxLength={64}
                    autoComplete="new-password"
                  />
                </label>
              </div>
            )}
          </div>
        )}

        {step === 2 && (
          <fieldset className="setup-modes">
            <legend id="setup-step-2">{t('How will you use TokenRouter?')}</legend>
            {MODE_OPTIONS.map((option) => (
              <label key={option.value} data-selected={values.usageMode === option.value || undefined}>
                <input
                  type="radio"
                  name="usage-mode"
                  value={option.value}
                  checked={values.usageMode === option.value}
                  onChange={() => update('usageMode', option.value)}
                  aria-label={t(option.title)}
                />
                <span>
                  <strong>{t(option.title)}</strong>
                  <small>{t(option.description)}</small>
                </span>
              </label>
            ))}
          </fieldset>
        )}

        {step === 3 && (
          <div>
            <h2 id="setup-step-3">{t('Ready to initialize')}</h2>
            <p className="muted">{t('Review these settings before completing initialization.')}</p>
            <dl className="kv setup-summary">
              <dt>{t('Database')}</dt>
              <dd>{status.databaseType}</dd>
              <dt>{t('Administrator')}</dt>
              <dd>{status.rootInitialized ? t('Existing account') : values.username.trim()}</dd>
              <dt>{t('Usage mode')}</dt>
              <dd>{t(modeTitle(values.usageMode))}</dd>
            </dl>
          </div>
        )}

        {error && <p className="error" role="alert">{error}</p>}

        <div className="setup-actions">
          {step > 0 && <button type="button" className="secondary" onClick={back} disabled={busy}>{t('Back')}</button>}
          {step < STEP_KEYS.length - 1 ? (
            <button type="button" onClick={next} disabled={busy}>{t('Next')}</button>
          ) : (
            <button type="button" onClick={submit} disabled={busy}>
              {busy ? t('Initializing…') : t('Initialize system')}
            </button>
          )}
        </div>
      </section>
    </main>
  );
}

