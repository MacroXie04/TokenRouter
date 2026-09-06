import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { User } from '../../shared/api/client';
import { authPost, isCanceledAuthRequest } from './auth-api';
import { hasUnsafeAuthText, isAuthenticatedUser } from './auth-flow';
import { AuthLayout } from './AuthLayout';

const FLOW_TOKEN_MAX_CHARACTERS = 256;
const OTP_LENGTH = 6;
const BACKUP_CODE_LENGTH = 8;

function formatBackupCode(value: string): string {
  const clean = value.replace(/[^a-zA-Z0-9]/g, '').toUpperCase().slice(0, BACKUP_CODE_LENGTH);
  return clean.length > 4 ? `${clean.slice(0, 4)}-${clean.slice(4)}` : clean;
}

export function OtpView({ flowToken, onLoggedIn }: { flowToken: string; onLoggedIn: (user: User) => void }) {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [backupMode, setBackupMode] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const mountedRef = useRef(true);
  const inFlightRef = useRef(false);
  const operationSequenceRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const validFlow = flowToken.length > 0
    && new TextEncoder().encode(flowToken).byteLength <= FLOW_TOKEN_MAX_CHARACTERS
    && !hasUnsafeAuthText(flowToken);
  const normalizedCode = backupMode ? code.replace('-', '') : code;
  const validCode = backupMode
    ? /^[A-Z0-9]{8}$/.test(normalizedCode)
    : /^\d{6}$/.test(normalizedCode);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      operationSequenceRef.current += 1;
      abortRef.current?.abort();
    };
  }, []);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!validFlow || !validCode || inFlightRef.current) return;
    const controller = new AbortController();
    abortRef.current?.abort();
    abortRef.current = controller;
    const operation = ++operationSequenceRef.current;
    inFlightRef.current = true;
    setBusy(true);
    setError('');
    try {
      const user = await authPost(
        '/user/login/2fa',
        { flow_token: flowToken, code: normalizedCode },
        undefined,
        controller.signal,
      );
      if (!mountedRef.current || operationSequenceRef.current !== operation) return;
      if (!isAuthenticatedUser(user)) throw new Error('invalid login response');
      onLoggedIn(user);
    } catch (requestError) {
      if (
        mountedRef.current
        && operationSequenceRef.current === operation
        && !isCanceledAuthRequest(requestError, controller.signal)
      ) setError(t('Authentication failed.'));
    } finally {
      if (operationSequenceRef.current === operation) {
        inFlightRef.current = false;
        abortRef.current = null;
        if (mountedRef.current) setBusy(false);
      }
    }
  }

  function toggleMode() {
    setBackupMode((current) => !current);
    setCode('');
    setError('');
  }

  return (
    <AuthLayout
      title={t('Two-factor authentication')}
      description={t('Enter your authenticator or backup code.')}
    >
      {!validFlow ? (
        <section className="card">
          <p className="error" role="alert">{t('This sign-in challenge has expired.')}</p>
          <a className="button" href="/sign-in">{t('Back to sign in')}</a>
        </section>
      ) : (
        <form className="card" onSubmit={submit} aria-busy={busy}>
          <label>
            {backupMode ? t('Backup code') : t('Verification code')}
            <input
              inputMode={backupMode ? 'text' : 'numeric'}
              autoComplete={backupMode ? 'off' : 'one-time-code'}
              value={code}
              onChange={(event) => setCode(backupMode
                ? formatBackupCode(event.target.value)
                : event.target.value.replace(/\D/g, '').slice(0, OTP_LENGTH))}
              maxLength={backupMode ? BACKUP_CODE_LENGTH + 1 : OTP_LENGTH}
              required
              autoFocus
            />
          </label>
          <p className="muted">
            {backupMode
              ? t('Each backup code can only be used once.')
              : t('Verification code updates every 30 seconds.')}
          </p>
          {error && <p className="error" role="alert">{error}</p>}
          <button type="submit" disabled={busy || !validCode}>
            {busy ? t('Please wait…') : t('Verify')}
          </button>
          <button type="button" className="link" disabled={busy} onClick={toggleMode}>
            {backupMode ? t('Use authenticator code') : t('Use backup code')}
          </button>
          <a className="form-link" href="/sign-in">{t('Back to sign in')}</a>
        </form>
      )}
    </AuthLayout>
  );
}
