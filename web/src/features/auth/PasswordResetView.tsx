import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasUnsafeAuthText, parsePasswordResetLocation } from '../../lib/auth-flow';
import {
  TURNSTILE_DISABLED,
  TurnstileWidget,
  turnstileParams,
  type TurnstileConfig,
} from '../security/TurnstileWidget';
import { AuthLayout } from './AuthLayout';
import { authGet, authPost, isCanceledAuthRequest } from './auth-api';

const MAX_EMAIL_CHARACTERS = 254;
const MAX_RESET_TOKEN_CHARACTERS = 256;
const MAX_RESET_PASSWORD_BYTES = 64;

export function PasswordResetView({
  resetMode,
  turnstileConfig = TURNSTILE_DISABLED,
}: {
  resetMode: boolean;
  turnstileConfig?: TurnstileConfig;
}) {
  const { t } = useTranslation();
  const location = parsePasswordResetLocation(window.location.search);
  const [email, setEmail] = useState(location.email);
  const [code, setCode] = useState(location.credential);
  const [password, setPassword] = useState('');
  const [generatedPassword, setGeneratedPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [cooldown, setCooldown] = useState(0);
  const [turnstileToken, setTurnstileToken] = useState('');
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0);
  const [turnstileFailed, setTurnstileFailed] = useState(false);
  const mountedRef = useRef(true);
  const inFlightRef = useRef(false);
  const operationSequenceRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const generatedMode = resetMode && location.valid && location.generatedMode;
  const invalidLocation = resetMode && !location.valid;
  const requiresTurnstile = !resetMode && turnstileConfig.required;
  const turnstileUnavailable = requiresTurnstile
    && (turnstileConfig.siteKey === '' || turnstileFailed);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      operationSequenceRef.current += 1;
      abortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (cooldown <= 0) return undefined;
    const timer = window.setTimeout(() => {
      setCooldown((seconds) => Math.max(0, seconds - 1));
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [cooldown]);

  async function copyGeneratedPassword() {
    if (!generatedPassword || !navigator.clipboard?.writeText) return;
    try {
      await navigator.clipboard.writeText(generatedPassword);
    } catch {
      // The password stays visible/read-only when clipboard permission is denied.
    }
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (inFlightRef.current) return;
    const normalizedEmail = email.trim().toLowerCase();
    if (
      invalidLocation
      || normalizedEmail.length === 0
      || new TextEncoder().encode(normalizedEmail).byteLength > MAX_EMAIL_CHARACTERS
      || hasUnsafeAuthText(normalizedEmail)
      || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(normalizedEmail)
      || (resetMode && (
        code.length === 0
        || new TextEncoder().encode(code).byteLength > MAX_RESET_TOKEN_CHARACTERS
        || hasUnsafeAuthText(code)
      ))
      || (resetMode && !generatedMode && (
        new TextEncoder().encode(password).byteLength < 8
        || new TextEncoder().encode(password).byteLength > MAX_RESET_PASSWORD_BYTES
      ))
    ) {
      setError(t('Request failed.'));
      return;
    }
    if (requiresTurnstile && turnstileConfig.siteKey === '') {
      setError(t('Human verification is unavailable.'));
      return;
    }
    if (requiresTurnstile && turnstileToken === '') {
      setError(t('Complete the human verification challenge.'));
      return;
    }
    const verificationParams = requiresTurnstile ? turnstileParams(turnstileToken) : undefined;
    if (requiresTurnstile) {
      setTurnstileToken('');
      setTurnstileWidgetKey((value) => value + 1);
    }
    const controller = new AbortController();
    abortRef.current?.abort();
    abortRef.current = controller;
    const operation = ++operationSequenceRef.current;
    inFlightRef.current = true;
    setBusy(true);
    if (generatedMode) setCooldown(30);
    setMessage('');
    setError('');
    try {
      if (resetMode) {
        if (generatedMode) {
          const value = await authPost(
            '/user/reset',
            { email: normalizedEmail, token: code },
            undefined,
            controller.signal,
          );
          if (!mountedRef.current || operationSequenceRef.current !== operation) return;
          if (
            typeof value !== 'string'
            || new TextEncoder().encode(value).byteLength < 8
            || new TextEncoder().encode(value).byteLength > MAX_RESET_PASSWORD_BYTES
            || hasUnsafeAuthText(value)
          ) {
            throw new Error('invalid reset response');
          }
          setGeneratedPassword(value);
          if (navigator.clipboard?.writeText) {
            try {
              await navigator.clipboard.writeText(value);
            } catch {
              // Clipboard access is optional; the password remains visible.
            }
          }
        } else {
          await authPost(
            '/user/reset',
            { email: normalizedEmail, token: code, new_password: password },
            undefined,
            controller.signal,
          );
        }
        if (!mountedRef.current || operationSequenceRef.current !== operation) return;
        setMessage(t('Password reset. You can sign in now.'));
      } else {
        await authGet(
          '/reset_password',
          { email: normalizedEmail, ...verificationParams },
          controller.signal,
        );
        if (!mountedRef.current || operationSequenceRef.current !== operation) return;
        setEmail(normalizedEmail);
        setCooldown(30);
        setMessage(t('If the address is registered, a reset code has been sent.'));
      }
    } catch (requestError) {
      if (
        mountedRef.current
        && operationSequenceRef.current === operation
        && !isCanceledAuthRequest(requestError, controller.signal)
      ) setError(t('Request failed.'));
    } finally {
      if (operationSequenceRef.current === operation) {
        inFlightRef.current = false;
        abortRef.current = null;
        if (mountedRef.current) setBusy(false);
      }
    }
  }

  return (
    <AuthLayout
      title={resetMode ? t('Reset password') : t('Forgot password')}
      description={resetMode
        ? generatedMode
          ? t('Confirm the reset link to generate a new password.')
          : t('Enter the code from your email and choose a new password.')
        : t('Request a one-time password reset code.')}
    >
      <form className="card" onSubmit={submit} aria-busy={busy}>
        <label>
          {t('Email')}
          <input
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value.slice(0, MAX_EMAIL_CHARACTERS))}
            required
            maxLength={MAX_EMAIL_CHARACTERS}
            readOnly={generatedMode}
            autoComplete="email"
            autoFocus
          />
        </label>
        {resetMode && generatedPassword && (
          <label>
            {t('New password')}
            <span className="row">
              <input value={generatedPassword} readOnly autoComplete="off" />
              <button type="button" className="button" onClick={copyGeneratedPassword}>
                {t('Copy password')}
              </button>
            </span>
          </label>
        )}
        {resetMode && !generatedMode && (
          <>
            <label>
              {t('Authorization code')}
              <input
                value={code}
                onChange={(event) => setCode(event.target.value.slice(0, MAX_RESET_TOKEN_CHARACTERS))}
                required
                maxLength={MAX_RESET_TOKEN_CHARACTERS}
                autoComplete="one-time-code"
              />
            </label>
            <label>
              {t('New password')}
              <input
                type="password"
                minLength={8}
                maxLength={64}
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
                autoComplete="new-password"
              />
            </label>
          </>
        )}
        {requiresTurnstile && turnstileConfig.siteKey !== '' && !turnstileFailed && (
          <TurnstileWidget
            key={turnstileWidgetKey}
            className="turnstile-widget"
            siteKey={turnstileConfig.siteKey}
            label={t('Human verification')}
            onVerify={(token) => {
              setTurnstileFailed(false);
              setTurnstileToken(token);
              setError('');
            }}
            onExpire={() => setTurnstileToken('')}
            onError={() => {
              setTurnstileFailed(true);
              setTurnstileToken('');
              setError('');
            }}
          />
        )}
        {turnstileUnavailable && !error && (
          <div className="error-panel" role="alert">
            <p>{t('Human verification is unavailable.')}</p>
            {turnstileConfig.siteKey !== '' && (
              <button
                type="button"
                className="link"
                onClick={() => {
                  setTurnstileFailed(false);
                  setTurnstileWidgetKey((value) => value + 1);
                }}
              >
                {t('Retry human verification')}
              </button>
            )}
          </div>
        )}
        {(invalidLocation || error) && (
          <p className="error" role="alert">{error || t('Request failed.')}</p>
        )}
        {message && <p className="success" role="status">{message}</p>}
        {!generatedPassword && (
          <button
            type="submit"
            disabled={invalidLocation || busy || cooldown > 0 || turnstileUnavailable}
          >
            {busy
              ? t('Please wait…')
              : cooldown > 0
                ? t('Try again in {{seconds}} seconds', { seconds: cooldown })
                : resetMode
                  ? t('Reset password')
                  : t('Send reset code')}
          </button>
        )}
      </form>
      <a className="button" href="/sign-in">{t('Back to sign in')}</a>
    </AuthLayout>
  );
}
