import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getData, type User } from '../../shared/api/client';
import {
  isPasskeyLoginSupported,
  parsePasskeyLoginBegin,
  serializeAssertionCredential,
} from '../../shared/browser/webauthn';
import { parsePublicStatus, type PublicStatusInfo } from '../../shared/config/public-status';
import { parseTurnstileConfig, turnstileParams } from "../security";
import { TurnstileWidget } from "../security";
import { authGet, authPost, isCanceledAuthRequest } from './auth-api';
import {
  authenticatedUserFromBundle,
  hasUnsafeAuthText,
  isAuthenticatedUser,
  isLoginChallenge,
  isValidAffiliateCode,
  MAX_WECHAT_CODE_CHARACTERS,
  oauthStartTarget,
  parseOAuthFlowState,
  parseTelegramAuthorization,
  parseWeChatOAuthEntry,
  telegramLoginTarget
} from './auth-flow';
import { AuthLayout } from './AuthLayout';
import { initialAffiliateCode, MAX_EMAIL_CHARACTERS, MAX_LOGIN_IDENTIFIER_BYTES, MAX_PASSWORD_CHARACTERS, MAX_USERNAME_CHARACTERS, oauthCallbackErrorMessage, oauthErrorClearedLocation, safeOAuthCredentialText } from './login-validation';
import { normalizeTelegramBotName } from './TelegramLoginWidget';
import { TelegramLoginDialog, WeChatLoginDialog } from './LoginProviderDialogs';

export function replaceBrowserLocation(target: string): void {
  window.location.assign(target);
}

export function LoginView({
  initialMode = 'login',
  onLoggedIn,
  onTwoFARequired,
  returnTarget = '',
  replaceLocation = replaceBrowserLocation,
}: {
  initialMode?: 'login' | 'register';
  onLoggedIn: (u: User) => void;
  onTwoFARequired: (flowToken: string) => void;
  returnTarget?: string;
  replaceLocation?: (target: string) => void;
}) {
  const { t } = useTranslation();
  const oauthEntry = parseWeChatOAuthEntry(window.location.pathname, window.location.search);
  const callbackMode = oauthEntry.kind !== 'not-callback';
  const [mode, setMode] = useState<'login' | 'register'>(initialMode);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [agreedToLegal, setAgreedToLegal] = useState(false);
  const [email, setEmail] = useState('');
  const [verificationCode, setVerificationCode] = useState('');
  const [affiliateCode, setAffiliateCode] = useState(initialAffiliateCode);
  const [verificationBusy, setVerificationBusy] = useState(false);
  const [verificationCooldown, setVerificationCooldown] = useState(0);
  const [verificationMessage, setVerificationMessage] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [turnstileToken, setTurnstileToken] = useState('');
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0);
  const [turnstileFailed, setTurnstileFailed] = useState(false);
  const [passkeyBusy, setPasskeyBusy] = useState(false);
  const [authStatus, setAuthStatus] = useState<PublicStatusInfo | null>(null);
  const [statusState, setStatusState] = useState<'loading' | 'ready' | 'failed'>('loading');
  const [statusAttempt, setStatusAttempt] = useState(0);
  const [wechatOpen, setWechatOpen] = useState(false);
  const [wechatCode, setWechatCode] = useState('');
  const [wechatBusy, setWechatBusy] = useState(false);
  const [wechatError, setWechatError] = useState('');
  const [telegramOpen, setTelegramOpen] = useState(false);
  const [telegramPending, setTelegramPending] = useState(false);
  const [telegramStarting, setTelegramStarting] = useState(false);
  const [telegramFlowToken, setTelegramFlowToken] = useState('');
  const [telegramError, setTelegramError] = useState('');

  const mountedRef = useRef(true);
  const submitInFlightRef = useRef(false);
  const verificationInFlightRef = useRef(false);
  const passkeyInFlightRef = useRef(false);
  const wechatInFlightRef = useRef(false);
  const telegramInFlightRef = useRef(false);
  const telegramStartInFlightRef = useRef(false);
  const callbackSequenceRef = useRef(0);
  const submitSequenceRef = useRef(0);
  const verificationSequenceRef = useRef(0);
  const passkeySequenceRef = useRef(0);
  const wechatSequenceRef = useRef(0);
  const submitAbortRef = useRef<AbortController | null>(null);
  const verificationAbortRef = useRef<AbortController | null>(null);
  const passkeyAbortRef = useRef<AbortController | null>(null);
  const wechatAbortRef = useRef<AbortController | null>(null);
  const telegramAbortRef = useRef<AbortController | null>(null);
  const wechatDialogRef = useRef<HTMLFormElement | null>(null);
  const wechatTriggerRef = useRef<HTMLButtonElement | null>(null);
  const telegramDialogRef = useRef<HTMLElement | null>(null);
  const telegramTriggerRef = useRef<HTMLButtonElement | null>(null);
  const onLoggedInRef = useRef(onLoggedIn);
  const onTwoFARequiredRef = useRef(onTwoFARequired);
  const translateRef = useRef(t);

  useEffect(() => {
    onLoggedInRef.current = onLoggedIn;
    onTwoFARequiredRef.current = onTwoFARequired;
    translateRef.current = t;
  }, [onLoggedIn, onTwoFARequired, t]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      callbackSequenceRef.current += 1;
      submitSequenceRef.current += 1;
      verificationSequenceRef.current += 1;
      passkeySequenceRef.current += 1;
      wechatSequenceRef.current += 1;
      submitAbortRef.current?.abort();
      verificationAbortRef.current?.abort();
      passkeyAbortRef.current?.abort();
      wechatAbortRef.current?.abort();
      telegramAbortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (callbackMode) return undefined;
    let active = true;
    setStatusState('loading');
    const timer = window.setTimeout(() => {
      void getData<unknown>('/status')
        .then((raw) => {
          if (!active) return;
          const parsed = parsePublicStatus(raw);
          setAuthStatus(parsed);
          setStatusState('ready');
          if (
            parsed.self_use_mode_enabled
            || !parsed.register_enabled
          ) setMode('login');
        })
        .catch(() => {
          if (!active) return;
          setAuthStatus(null);
          setStatusState('failed');
        });
    }, 0);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [callbackMode, statusAttempt]);

  const oauthEntryCode = oauthEntry.kind === 'wechat' ? oauthEntry.code : '';
  useEffect(() => {
    if (oauthEntry.kind !== 'wechat') {
      if (oauthEntry.kind === 'invalid') {
        setError(translateRef.current('External sign-in failed. Please try again.'));
      }
      return undefined;
    }

    const controller = new AbortController();
    const operation = ++callbackSequenceRef.current;
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const bundle = await authGet('/oauth/wechat', { code: oauthEntryCode }, controller.signal);
          const user = authenticatedUserFromBundle(bundle);
          if (!user) throw new Error('invalid WeChat login response');
          if (mountedRef.current && callbackSequenceRef.current === operation) onLoggedInRef.current(user);
        } catch (requestError) {
          if (
            mountedRef.current
            && callbackSequenceRef.current === operation
            && !isCanceledAuthRequest(requestError, controller.signal)
          ) setError(translateRef.current('External sign-in failed. Please try again.'));
        }
      })();
    }, 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [oauthEntry.kind, oauthEntryCode]);

  useEffect(() => {
    if (callbackMode) return;
    const message = oauthCallbackErrorMessage(
      window.location.search,
      t('External sign-in failed. Please try again.'),
    );
    if (message === null) return;
    setError(message);
    window.history.replaceState(
      null,
      '',
      oauthErrorClearedLocation(window.location.pathname, window.location.search, window.location.hash),
    );
  }, [callbackMode, t]);

  useEffect(() => {
    if (verificationCooldown <= 0) return undefined;
    const timer = window.setTimeout(() => {
      setVerificationCooldown((remaining) => Math.max(0, remaining - 1));
    }, 1_000);
    return () => window.clearTimeout(timer);
  }, [verificationCooldown]);

  function closeWeChat() {
    wechatSequenceRef.current += 1;
    wechatAbortRef.current?.abort();
    wechatAbortRef.current = null;
    wechatInFlightRef.current = false;
    setWechatBusy(false);
    setWechatOpen(false);
    setWechatCode('');
    setWechatError('');
    window.setTimeout(() => wechatTriggerRef.current?.focus(), 0);
  }

  function closeTelegram() {
    telegramAbortRef.current?.abort();
    telegramAbortRef.current = null;
    telegramStartInFlightRef.current = false;
    telegramInFlightRef.current = false;
    setTelegramOpen(false);
    setTelegramPending(false);
    setTelegramStarting(false);
    setTelegramFlowToken('');
    setTelegramError('');
    window.setTimeout(() => telegramTriggerRef.current?.focus(), 0);
  }

  useEffect(() => {
    const dialog = wechatOpen ? wechatDialogRef.current : telegramOpen ? telegramDialogRef.current : null;
    if (!dialog) return undefined;
    const initialFocus = dialog.querySelector<HTMLElement>('input, button:not([disabled]), a[href]');
    initialFocus?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        if (wechatOpen) closeWeChat();
        else if (!telegramPending) closeTelegram();
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(
        'input:not([disabled]), button:not([disabled]), a[href]:not([tabindex="-1"])',
      ));
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', keydown);
    return () => document.removeEventListener('keydown', keydown);
  });

  const hasUserAgreement = Boolean(authStatus?.user_agreement_enabled);
  const hasPrivacyPolicy = Boolean(authStatus?.privacy_policy_enabled);
  const requiresLegalConsent = hasUserAgreement || hasPrivacyPolicy;
  const missingLegalConsent = requiresLegalConsent && !agreedToLegal;
  const passwordLoginEnabled = authStatus?.password_login_enabled === true;
  const passwordRegisterEnabled = authStatus?.password_register_enabled === true;
  const registrationAvailable = authStatus?.self_use_mode_enabled === false
    && authStatus.register_enabled;
  const selectedPasswordModeEnabled = mode === 'login' ? passwordLoginEnabled : passwordRegisterEnabled;
  const emailVerificationRequired = mode === 'register' && authStatus?.email_verification === true;
  const turnstile = parseTurnstileConfig(authStatus);
  const turnstileUnavailable = turnstile.required && (turnstile.siteKey === '' || turnstileFailed);
  const passkeyLoginEnabled = mode === 'login' && authStatus?.passkey_login === true;
  const passkeySupported = isPasskeyLoginSupported();
  const telegramConfigured = authStatus?.telegram_oauth === true
    && normalizeTelegramBotName(authStatus.telegram_bot_name) !== null;

  function requireLegalConsent(event?: React.SyntheticEvent): boolean {
    if (!missingLegalConsent) return false;
    event?.preventDefault();
    setError(t('Accept the configured terms to continue.'));
    return true;
  }

  function consumeTurnstileToken(): Record<string, string> | undefined {
    if (!turnstile.required) return undefined;
    const params = turnstileParams(turnstileToken);
    setTurnstileToken('');
    setTurnstileWidgetKey((value) => value + 1);
    return params;
  }

  function verifyTurnstileReady(): boolean {
    if (turnstile.required && turnstile.siteKey === '') {
      setError(t('Human verification is unavailable.'));
      return false;
    }
    if (turnstile.required && turnstileToken === '') {
      setError(t('Complete the human verification challenge.'));
      return false;
    }
    return true;
  }

  async function sendVerificationCode() {
    if (verificationInFlightRef.current) return;
    setError('');
    setVerificationMessage('');
    const normalizedEmail = email.trim().toLowerCase();
    if (
      normalizedEmail.length === 0
      || normalizedEmail.length > MAX_EMAIL_CHARACTERS
      || new TextEncoder().encode(normalizedEmail).byteLength > MAX_EMAIL_CHARACTERS
      || hasUnsafeAuthText(normalizedEmail)
      || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(normalizedEmail)
    ) {
      setError(t('Enter a valid email address.'));
      return;
    }
    if (!verifyTurnstileReady()) return;
    const verification = consumeTurnstileToken();
    const controller = new AbortController();
    verificationAbortRef.current?.abort();
    verificationAbortRef.current = controller;
    const operation = ++verificationSequenceRef.current;
    verificationInFlightRef.current = true;
    setVerificationBusy(true);
    try {
      await authGet('/verification', { email: normalizedEmail, ...verification }, controller.signal);
      if (!mountedRef.current || verificationSequenceRef.current !== operation) return;
      setEmail(normalizedEmail);
      setVerificationCooldown(30);
      setVerificationMessage(t('Verification code sent.'));
    } catch (requestError) {
      if (
        mountedRef.current
        && verificationSequenceRef.current === operation
        && !isCanceledAuthRequest(requestError, controller.signal)
      ) setError(t('Unable to send verification code.'));
    } finally {
      if (verificationSequenceRef.current === operation) {
        verificationInFlightRef.current = false;
        verificationAbortRef.current = null;
        if (mountedRef.current) setVerificationBusy(false);
      }
    }
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (submitInFlightRef.current || !selectedPasswordModeEnabled) return;
    setError('');
    if (requireLegalConsent()) return;

    const normalizedUsername = username.trim();
    const minimumUsernameLength = mode === 'register' ? 3 : 1;
    if (
      normalizedUsername.length < minimumUsernameLength
      || normalizedUsername.length > MAX_USERNAME_CHARACTERS
      || (mode === 'login'
        && new TextEncoder().encode(normalizedUsername).byteLength > MAX_LOGIN_IDENTIFIER_BYTES)
      || hasUnsafeAuthText(normalizedUsername)
      || password.length < 8
      || password.length > MAX_PASSWORD_CHARACTERS
    ) {
      setError(mode === 'login' ? t('Authentication failed.') : t('Request failed.'));
      return;
    }
    if (mode === 'register' && password !== confirmPassword) {
      setError(t('Passwords do not match.'));
      return;
    }
    if (mode === 'register' && !isValidAffiliateCode(affiliateCode)) {
      setError(t('Request failed.'));
      return;
    }
    if (
      emailVerificationRequired
      && (
        email.trim().length === 0
        || email.trim().length > MAX_EMAIL_CHARACTERS
        || new TextEncoder().encode(email.trim()).byteLength > MAX_EMAIL_CHARACTERS
        || hasUnsafeAuthText(email.trim())
        || !/^\d{6}$/.test(verificationCode)
      )
    ) {
      setError(t('Email and verification code are required.'));
      return;
    }
    if (!verifyTurnstileReady()) return;

    const verificationParams = consumeTurnstileToken();
    const controller = new AbortController();
    submitAbortRef.current?.abort();
    submitAbortRef.current = controller;
    const operation = ++submitSequenceRef.current;
    submitInFlightRef.current = true;
    setBusy(true);
    try {
      if (mode === 'register') {
        const payload: Record<string, string> = { username: normalizedUsername, password };
        if (emailVerificationRequired) {
          payload.email = email.trim().toLowerCase();
          payload.verification_code = verificationCode;
        }
        if (affiliateCode !== '') payload.aff_code = affiliateCode;
        await authPost('/user/register', payload, verificationParams, controller.signal);
        if (!mountedRef.current || submitSequenceRef.current !== operation) return;
        setMode('login');
        setPassword('');
        setConfirmPassword('');
        setVerificationCode('');
        return;
      }

      const response = await authPost(
        '/user/login',
        { username: normalizedUsername, password },
        verificationParams,
        controller.signal,
      );
      if (!mountedRef.current || submitSequenceRef.current !== operation) return;
      if (isLoginChallenge(response)) {
        onTwoFARequiredRef.current(response.flow_token);
        return;
      }
      if (!isAuthenticatedUser(response)) throw new Error('invalid authenticated user');
      onLoggedInRef.current(response);
    } catch (requestError) {
      if (
        mountedRef.current
        && submitSequenceRef.current === operation
        && !isCanceledAuthRequest(requestError, controller.signal)
      ) setError(mode === 'login' ? t('Authentication failed.') : t('Request failed.'));
    } finally {
      if (submitSequenceRef.current === operation) {
        submitInFlightRef.current = false;
        submitAbortRef.current = null;
        if (mountedRef.current) setBusy(false);
      }
    }
  }

  async function submitWeChat(event: React.FormEvent) {
    event.preventDefault();
    if (wechatInFlightRef.current) return;
    setWechatError('');
    const normalizedCode = safeOAuthCredentialText(wechatCode, MAX_WECHAT_CODE_CHARACTERS);
    if (!normalizedCode) {
      setWechatError(t('Enter a valid WeChat authorization code.'));
      return;
    }

    const controller = new AbortController();
    wechatAbortRef.current?.abort();
    wechatAbortRef.current = controller;
    const operation = ++wechatSequenceRef.current;
    wechatInFlightRef.current = true;
    setWechatBusy(true);
    try {
      const bundle = await authGet('/oauth/wechat', { code: normalizedCode }, controller.signal);
      if (!mountedRef.current || wechatSequenceRef.current !== operation) return;
      const user = authenticatedUserFromBundle(bundle);
      if (!user) throw new Error('invalid WeChat login response');
      onLoggedInRef.current(user);
    } catch (requestError) {
      if (
        mountedRef.current
        && wechatSequenceRef.current === operation
        && !isCanceledAuthRequest(requestError, controller.signal)
      ) setWechatError(t('External sign-in failed. Please try again.'));
    } finally {
      if (wechatSequenceRef.current === operation) {
        wechatInFlightRef.current = false;
        wechatAbortRef.current = null;
        if (mountedRef.current) setWechatBusy(false);
      }
    }
  }

  async function submitPasskey() {
    if (passkeyInFlightRef.current) return;
    setError('');
    if (requireLegalConsent()) return;
    if (!isPasskeyLoginSupported()) {
      setError(t('Passkey sign-in is unavailable in this browser.'));
      return;
    }

    const controller = new AbortController();
    passkeyAbortRef.current?.abort();
    passkeyAbortRef.current = controller;
    const operation = ++passkeySequenceRef.current;
    passkeyInFlightRef.current = true;
    setPasskeyBusy(true);
    try {
      const beginPayload = await authPost(
        '/user/passkey/login/begin', undefined, undefined, controller.signal,
      );
      if (!mountedRef.current || passkeySequenceRef.current !== operation) return;
      const begin = parsePasskeyLoginBegin(beginPayload);
      const credential = await navigator.credentials.get({
        ...begin.requestOptions,
        signal: controller.signal,
      } as CredentialRequestOptions);
      if (!mountedRef.current || passkeySequenceRef.current !== operation) return;
      if (!credential) {
        setError(t('Passkey sign-in was cancelled or timed out.'));
        return;
      }
      const assertion = serializeAssertionCredential(credential);
      const user = await authPost('/user/passkey/login/finish', {
        flow_token: begin.flowToken,
        ...assertion,
      }, undefined, controller.signal);
      if (!mountedRef.current || passkeySequenceRef.current !== operation) return;
      if (!isAuthenticatedUser(user)) throw new Error('invalid authenticated user');
      onLoggedInRef.current(user);
    } catch (passkeyError) {
      if (
        !mountedRef.current
        || passkeySequenceRef.current !== operation
        || isCanceledAuthRequest(passkeyError, controller.signal)
      ) return;
      const errorName = passkeyError && typeof passkeyError === 'object' && 'name' in passkeyError
        ? String(passkeyError.name)
        : '';
      setError(errorName === 'NotAllowedError'
        ? t('Passkey sign-in was cancelled or timed out.')
        : t('Passkey sign-in failed. Please try again.'));
    } finally {
      if (passkeySequenceRef.current === operation) {
        passkeyInFlightRef.current = false;
        passkeyAbortRef.current = null;
        if (mountedRef.current) setPasskeyBusy(false);
      }
    }
  }

  function switchMode() {
    submitSequenceRef.current += 1;
    verificationSequenceRef.current += 1;
    submitAbortRef.current?.abort();
    verificationAbortRef.current?.abort();
    submitAbortRef.current = null;
    verificationAbortRef.current = null;
    submitInFlightRef.current = false;
    verificationInFlightRef.current = false;
    setBusy(false);
    setVerificationBusy(false);
    setError('');
    setVerificationMessage('');
    setPassword('');
    setConfirmPassword('');
    setVerificationCode('');
    setTurnstileToken('');
    setTurnstileWidgetKey((value) => value + 1);
    setMode((current) => (current === 'login' ? 'register' : 'login'));
  }

  function providerLabel(name: string): string {
    return t('Continue with {{provider}}', { provider: name });
  }

  function completeTelegramAuthorization(value: unknown) {
    if (telegramInFlightRef.current) return;
    const authorization = parseTelegramAuthorization(value);
    if (!authorization || telegramFlowToken === '') {
      setTelegramError(t('External sign-in failed. Please try again.'));
      return;
    }
    telegramInFlightRef.current = true;
    setTelegramError('');
    setTelegramPending(true);
    try {
      const target = telegramLoginTarget(authorization, telegramFlowToken);
      if (!target) throw new Error('invalid Telegram flow');
      replaceLocation(target);
    } catch {
      telegramInFlightRef.current = false;
      setTelegramPending(false);
      setTelegramError(t('External sign-in failed. Please try again.'));
    }
  }

  async function startTelegramLogin() {
    if (!telegramConfigured || !isValidAffiliateCode(affiliateCode)
      || telegramStartInFlightRef.current || requireLegalConsent()) return;
    telegramStartInFlightRef.current = true;
    setTelegramStarting(true);
    setTelegramError('');
    const controller = new AbortController();
    telegramAbortRef.current?.abort();
    telegramAbortRef.current = controller;
    try {
      const raw = await authPost('/oauth/state', {
        provider: 'telegram',
        intent: 'login',
        ...(affiliateCode === '' ? {} : { aff: affiliateCode }),
        ...(returnTarget === '' ? {} : { redirect: returnTarget }),
      }, undefined, controller.signal);
      const flow = parseOAuthFlowState(raw);
      if (!flow) throw new Error('invalid Telegram OAuth state');
      if (!mountedRef.current || controller.signal.aborted) return;
      setTelegramFlowToken(flow.flow_token);
      setTelegramOpen(true);
    } catch (requestError) {
      if (mountedRef.current && !isCanceledAuthRequest(requestError, controller.signal)) {
        setError(t('External sign-in failed. Please try again.'));
      }
    } finally {
      if (telegramAbortRef.current === controller) telegramAbortRef.current = null;
      telegramStartInFlightRef.current = false;
      if (mountedRef.current) setTelegramStarting(false);
    }
  }

  if (callbackMode) {
    return (
      <AuthLayout title={t('Completing external sign-in…')}>
        <section className="card" aria-busy={oauthEntry.kind === 'wechat' && !error}>
          {!error && <p className="muted" role="status">{t('Loading…')}</p>}
          {error && (
            <>
              <p className="error" role="alert">{error}</p>
              <a className="button" href="/sign-in">{t('Back to sign in')}</a>
            </>
          )}
        </section>
      </AuthLayout>
    );
  }

  const pageTitle = mode === 'login' ? t('Sign in') : t('Create an account.');
  const pageDescription = mode === 'login' ? t('Sign in to your console.') : t('Create an account.');
  if (statusState !== 'ready' || !authStatus) {
    return (
      <AuthLayout title={pageTitle} description={pageDescription}>
        <section className="card" aria-busy={statusState === 'loading'}>
          {statusState === 'loading' ? (
            <p className="muted" role="status">{t('Loading…')}</p>
          ) : (
            <div className="error-panel" role="alert">
              <p>{t('Service unavailable')}</p>
              <button type="button" className="link" onClick={() => setStatusAttempt((value) => value + 1)}>
                {t('Retry')}
              </button>
            </div>
          )}
        </section>
      </AuthLayout>
    );
  }

  const providerLink = (provider: string) => oauthStartTarget(provider, affiliateCode, returnTarget) ?? undefined;
  const oauthLinksDisabled = missingLegalConsent || !isValidAffiliateCode(affiliateCode);

  return (
    <AuthLayout title={pageTitle} description={pageDescription}>
      <form className="card" onSubmit={submit} aria-busy={busy}>
        {!selectedPasswordModeEnabled && (
          <p className="muted">{mode === 'login' ? t('Password sign-in is disabled.') : t('Password registration is disabled.')}</p>
        )}
        {selectedPasswordModeEnabled && (
          <>
            <label>
              {mode === 'login' ? t('Username or Email') : t('Username')}
              <input
                value={username}
                onChange={(event) => setUsername(event.target.value.slice(
                  0,
                  mode === 'login' ? MAX_LOGIN_IDENTIFIER_BYTES : MAX_USERNAME_CHARACTERS,
                ))}
                required
                minLength={mode === 'register' ? 3 : 1}
                maxLength={mode === 'login' ? MAX_LOGIN_IDENTIFIER_BYTES : MAX_USERNAME_CHARACTERS}
                autoComplete="username"
                autoFocus
              />
            </label>
            <label>
              {t('Password')}
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value.slice(0, MAX_PASSWORD_CHARACTERS))}
                required
                minLength={8}
                maxLength={MAX_PASSWORD_CHARACTERS}
                autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
              />
            </label>
            {mode === 'register' && (
              <>
                <label>
                  {t('Confirm password')}
                  <input
                    type="password"
                    value={confirmPassword}
                    onChange={(event) => setConfirmPassword(event.target.value.slice(0, MAX_PASSWORD_CHARACTERS))}
                    required
                    minLength={8}
                    maxLength={MAX_PASSWORD_CHARACTERS}
                    autoComplete="new-password"
                  />
                </label>
                {emailVerificationRequired && (
                  <>
                    <label>
                      {t('Email')}
                      <input
                        type="email"
                        value={email}
                        onChange={(event) => {
                          setEmail(event.target.value.slice(0, MAX_EMAIL_CHARACTERS));
                          setVerificationMessage('');
                        }}
                        required
                        maxLength={MAX_EMAIL_CHARACTERS}
                        autoComplete="email"
                      />
                    </label>
                    <div className="verification-code-row">
                      <label>
                        {t('Verification code')}
                        <input
                          value={verificationCode}
                          onChange={(event) => setVerificationCode(event.target.value.replace(/\D/g, '').slice(0, 6))}
                          required
                          inputMode="numeric"
                          pattern="[0-9]{6}"
                          maxLength={6}
                          autoComplete="one-time-code"
                        />
                      </label>
                      <button
                        type="button"
                        className="secondary"
                        disabled={verificationBusy || busy || verificationCooldown > 0 || turnstileUnavailable}
                        onClick={() => void sendVerificationCode()}
                      >
                        {verificationBusy
                          ? t('Please wait…')
                          : verificationCooldown > 0
                            ? t('Resend in {{seconds}} seconds', { seconds: verificationCooldown })
                            : t('Send verification code')}
                      </button>
                    </div>
                    {verificationMessage && <p className="success" role="status">{verificationMessage}</p>}
                  </>
                )}
                <label>
                  {t('Invitation code (optional)')}
                  <input
                    value={affiliateCode}
                    onChange={(event) => setAffiliateCode(event.target.value.slice(0, 32))}
                    maxLength={32}
                    autoComplete="off"
                  />
                </label>
              </>
            )}
          </>
        )}
        {selectedPasswordModeEnabled && turnstile.required && turnstile.siteKey !== '' && !turnstileFailed && (
          <TurnstileWidget
            key={turnstileWidgetKey}
            className="turnstile-widget"
            siteKey={turnstile.siteKey}
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
        {selectedPasswordModeEnabled && turnstileUnavailable && !error && (
          <div className="error-panel" role="alert">
            <p>{t('Human verification is unavailable.')}</p>
            {turnstile.siteKey !== '' && (
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
        {error && <p className="error" role="alert">{error}</p>}
        {requiresLegalConsent && (
          <div className="legal-consent">
            <input
              id="legal-consent"
              type="checkbox"
              checked={agreedToLegal}
              onChange={(event) => setAgreedToLegal(event.target.checked)}
            />
            <label htmlFor="legal-consent">
              {t('I have read and agree to the')}{' '}
              {hasUserAgreement && (
                <a href="/user-agreement" target="_blank" rel="noopener noreferrer">
                  {t('User Agreement')}
                </a>
              )}
              {hasUserAgreement && hasPrivacyPolicy && ` ${t('and the')} `}
              {hasPrivacyPolicy && (
                <a href="/privacy-policy" target="_blank" rel="noopener noreferrer">
                  {t('Privacy Policy')}
                </a>
              )}
              .
            </label>
          </div>
        )}
        {selectedPasswordModeEnabled && (
          <button type="submit" disabled={busy || verificationBusy || missingLegalConsent || turnstileUnavailable}>
            {busy ? t('Please wait…') : mode === 'login' ? t('Sign in') : t('Register')}
          </button>
        )}
        {(mode === 'register' || registrationAvailable) && (
          <button type="button" className="link" disabled={busy || verificationBusy} onClick={switchMode}>
            {mode === 'login' ? t('No account? Register') : t('Have an account? Sign in')}
          </button>
        )}
        {mode === 'login' && passwordLoginEnabled && <a className="form-link" href="/forgot-password">{t('Forgot password?')}</a>}
      </form>

      <div className="oauth-buttons" aria-label="OAuth">
        {passkeyLoginEnabled && (
          <>
            <button
              className="button"
              type="button"
              disabled={passkeyBusy || busy || missingLegalConsent || !passkeySupported}
              aria-describedby={!passkeySupported ? 'passkey-unavailable' : undefined}
              onClick={() => void submitPasskey()}
            >
              {passkeyBusy ? t('Please wait…') : t('Sign in with passkey')}
            </button>
            {!passkeySupported && (
              <p id="passkey-unavailable" className="muted" role="status">
                {t('Passkey sign-in is unavailable in this browser.')}
              </p>
            )}
          </>
        )}
        {authStatus.wechat_login && (
          <button
            ref={wechatTriggerRef}
            className="button"
            type="button"
            disabled={missingLegalConsent || busy}
            onClick={() => {
              if (requireLegalConsent()) return;
              setWechatOpen(true);
              setWechatError('');
            }}
          >
            {t('Continue with WeChat')}
          </button>
        )}
        {authStatus.github_oauth && (
          <a role="link" className="button" href={oauthLinksDisabled ? undefined : providerLink('github')} aria-disabled={oauthLinksDisabled} tabIndex={oauthLinksDisabled ? -1 : undefined} onClick={requireLegalConsent}>{providerLabel('GitHub')}</a>
        )}
        {authStatus.discord_oauth && (
          <a role="link" className="button" href={oauthLinksDisabled ? undefined : providerLink('discord')} aria-disabled={oauthLinksDisabled} tabIndex={oauthLinksDisabled ? -1 : undefined} onClick={requireLegalConsent}>{providerLabel('Discord')}</a>
        )}
        {authStatus.oidc_enabled && (
          <a role="link" className="button" href={oauthLinksDisabled ? undefined : providerLink('oidc')} aria-disabled={oauthLinksDisabled} tabIndex={oauthLinksDisabled ? -1 : undefined} onClick={requireLegalConsent}>{providerLabel(authStatus.oidc_display_name)}</a>
        )}
        {authStatus.linuxdo_oauth && (
          <a role="link" className="button" href={oauthLinksDisabled ? undefined : providerLink('linuxdo')} aria-disabled={oauthLinksDisabled} tabIndex={oauthLinksDisabled ? -1 : undefined} onClick={requireLegalConsent}>{providerLabel('LinuxDO')}</a>
        )}
        {authStatus.telegram_oauth && (
          <>
            <button
              ref={telegramTriggerRef}
              className="button"
              type="button"
              disabled={!telegramConfigured || missingLegalConsent || busy || telegramStarting
                || !isValidAffiliateCode(affiliateCode)}
              aria-describedby={!telegramConfigured ? 'telegram-unavailable' : undefined}
              onClick={() => void startTelegramLogin()}
            >
              {telegramStarting ? t('Please wait…') : providerLabel('Telegram')}
            </button>
            {!telegramConfigured && <p id="telegram-unavailable" className="muted" role="status">{t('Service unavailable')}</p>}
          </>
        )}
        {authStatus.custom_oauth_providers.map((provider) => (
          <a
            role="link"
            key={provider.slug}
            className="button"
            href={oauthLinksDisabled ? undefined : providerLink(provider.slug)}
            aria-disabled={oauthLinksDisabled}
            tabIndex={oauthLinksDisabled ? -1 : undefined}
            onClick={requireLegalConsent}
          >
            {provider.icon && <img src={provider.icon} alt="" width="16" height="16" />}
            {providerLabel(provider.name)}
          </a>
        ))}
      </div>

      {wechatOpen && (
        <WeChatLoginDialog
          dialogRef={wechatDialogRef}
          qrCode={authStatus.wechat_qrcode}
          code={wechatCode}
          error={wechatError}
          busy={wechatBusy}
          onCodeChange={setWechatCode}
          onClose={closeWeChat}
          onSubmit={submitWeChat}
        />
      )}

      {telegramOpen && (
        <TelegramLoginDialog
          dialogRef={telegramDialogRef}
          botName={authStatus.telegram_bot_name}
          pending={telegramPending}
          error={telegramError}
          onClose={closeTelegram}
          onAuthorization={completeTelegramAuthorization}
        />
      )}
    </AuthLayout>
  );
}
