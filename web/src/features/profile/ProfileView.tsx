import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { SESSION_EXPIRED_EVENT } from '../../shared/api/client';
import { TurnstileWidget } from '../security/TurnstileWidget';
import {
  beginPasskeyRegistration,
  bindEmail,
  bindWeChat,
  changePassword,
  createOAuthBindingURL,
  deleteAccount,
  deletePasskeys,
  disableTwoFactor,
  enableTwoFactor,
  finishPasskeyRegistration,
  generateAccessToken,
  getOAuthBindings,
  getOAuthCatalog,
  getPasskeys,
  getPasskeyStatus,
  getProfile,
  getSessions,
  getTwoFactorStatus,
  regenerateBackupCodes,
  revokeOtherSessions,
  revokeSession,
  sendEmailVerification,
  startTelegramBinding,
  startTwoFactorSetup,
  unbindOAuth,
  updateDisplayName,
  updateLanguage,
  updateNotificationSettings,
  updateSidebarModules,
  verifyTwoFactor,
  type CustomOAuthBinding,
  type LoginSession,
  type ProfileAccount,
  type ProfileNotificationSettings,
  type SidebarModules,
  type TelegramBindingFlow,
  type TwoFactorSetup
} from './profile-api';
import { compactIdentifier, defaultExternalNavigate, enabledSidebarModules, hasControlCharacters, LANGUAGE_OPTIONS, MAX_GOTIFY_TOKEN_BYTES, MAX_NOTIFICATION_EMAIL_BYTES, MAX_NOTIFICATION_QUOTA, MAX_NOTIFICATION_URL_BYTES, MAX_WEBHOOK_SECRET_BYTES, NOTIFICATION_OPTIONS, resource, SIDEBAR_SECTIONS, sidebarPreferenceEnabled, validDisplayName, validEmailAddress, validFactorCode, validNotificationEmail, validNotificationURL, validProofCode, validWriteOnlyCredential, type MutationResult, type Notice, type OAuthOverview, type Resource, type SecurityOverview } from './profile-view-model';
import {
  passkeyRegistrationSupported,
  preparePasskeyCreationOptions,
  serializePasskeyRegistrationCredential,
} from './profile-webauthn';
import { EmptyOrFailure } from './ProfileResourceState';
import { ProfileBackupCodes, ProfileSessionsPanel } from './ProfileSessionPanels';

export interface ProfileViewProps {
  onNavigate?: (target: string) => void;
  onProfileChange?: (profile: ProfileAccount) => void;
  onExternalNavigate?: (target: string) => void;
}

export function ProfileView({
  onNavigate,
  onProfileChange,
  onExternalNavigate = defaultExternalNavigate,
}: ProfileViewProps) {
  const { t, i18n } = useTranslation();
  const i18nRef = useRef(i18n);
  i18nRef.current = i18n;
  const mounted = useRef(true);
  const mutationLocked = useRef(false);
  const activeMutation = useRef<AbortController | null>(null);
  const telegramWidget = useRef<HTMLDivElement | null>(null);

  const [profile, setProfile] = useState<Resource<ProfileAccount>>(resource);
  const [security, setSecurity] = useState<Resource<SecurityOverview>>(resource);
  const [sessions, setSessions] = useState<Resource<LoginSession[]>>(resource);
  const [oauth, setOAuth] = useState<Resource<OAuthOverview>>(resource);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<Notice>(null);

  const [displayName, setDisplayName] = useState('');
  const [oldPassword, setOldPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');

  const [twoFactorSetup, setTwoFactorSetup] = useState<TwoFactorSetup | null>(null);
  const [twoFactorCode, setTwoFactorCode] = useState('');
  const [disableCode, setDisableCode] = useState('');
  const [disableConfirmed, setDisableConfirmed] = useState(false);
  const [regenerateCode, setRegenerateCode] = useState('');
  const [regenerateConfirmed, setRegenerateConfirmed] = useState(false);
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  const [backupCodesSaved, setBackupCodesSaved] = useState(false);

  const [passkeyCode, setPasskeyCode] = useState('');
  const [weChatCode, setWeChatCode] = useState('');
  const [accessToken, setAccessToken] = useState<string | null>(null);
  const [accessTokenConfirmed, setAccessTokenConfirmed] = useState(false);
  const [emailAddress, setEmailAddress] = useState('');
  const [emailCode, setEmailCode] = useState('');
  const [emailCodeTarget, setEmailCodeTarget] = useState<string | null>(null);
  const [turnstileToken, setTurnstileToken] = useState('');
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0);
  const [turnstileFailed, setTurnstileFailed] = useState(false);
  const [telegramFlow, setTelegramFlow] = useState<TelegramBindingFlow | null>(null);
  const [deleteConfirmation, setDeleteConfirmation] = useState('');
  const [deleteAcknowledged, setDeleteAcknowledged] = useState(false);
  const [sidebarModules, setSidebarModules] = useState<SidebarModules | null>(null);
  const [notificationSettings, setNotificationSettings] = useState<ProfileNotificationSettings | null>(null);
  const [webhookSecret, setWebhookSecret] = useState('');
  const [gotifyToken, setGotifyToken] = useState('');

  const loadProfile = useCallback(async (signal?: AbortSignal) => {
    if (mounted.current && !signal?.aborted) setProfile((previous) => ({ ...previous, loading: true, error: false }));
    try {
      const account = await getProfile(signal);
      if (!mounted.current || signal?.aborted) return;
      if (account.language && i18nRef.current.language !== account.language) {
        void i18nRef.current.changeLanguage(account.language).catch(() => undefined);
      }
      setProfile({ data: account, loading: false, error: false });
      setDisplayName(account.displayName);
      setSidebarModules(account.sidebarModules);
      setNotificationSettings(account.notificationSettings);
      setWebhookSecret('');
      setGotifyToken('');
      onProfileChange?.(account);
    } catch {
      if (mounted.current && !signal?.aborted) setProfile((previous) => ({ ...previous, loading: false, error: true }));
    }
  }, [onProfileChange]);

  const loadSecurity = useCallback(async (signal?: AbortSignal) => {
    if (mounted.current && !signal?.aborted) setSecurity((previous) => ({ ...previous, loading: true, error: false }));
    try {
      const [twoFactor, passkey, passkeys] = await Promise.all([
        getTwoFactorStatus(signal),
        getPasskeyStatus(signal),
        getPasskeys(signal),
      ]);
      if (passkey.enabled !== (passkeys.length > 0)) throw new Error('inconsistent passkey state');
      if (!mounted.current || signal?.aborted) return;
      setSecurity({
        data: {
          twoFactorEnabled: twoFactor.enabled,
          passkeyEnabled: passkey.enabled,
          passkeys,
        },
        loading: false,
        error: false,
      });
    } catch {
      if (mounted.current && !signal?.aborted) setSecurity((previous) => ({ ...previous, loading: false, error: true }));
    }
  }, []);

  const loadSessions = useCallback(async (signal?: AbortSignal) => {
    if (mounted.current && !signal?.aborted) setSessions((previous) => ({ ...previous, loading: true, error: false }));
    try {
      const next = await getSessions(signal);
      if (mounted.current && !signal?.aborted) setSessions({ data: next, loading: false, error: false });
    } catch {
      if (mounted.current && !signal?.aborted) setSessions((previous) => ({ ...previous, loading: false, error: true }));
    }
  }, []);

  const loadOAuth = useCallback(async (signal?: AbortSignal) => {
    if (mounted.current && !signal?.aborted) setOAuth((previous) => ({ ...previous, loading: true, error: false }));
    try {
      const [catalog, bindings] = await Promise.all([
        getOAuthCatalog(signal),
        getOAuthBindings(signal),
      ]);
      if (!mounted.current || signal?.aborted) return;
      setOAuth({ data: { catalog, bindings }, loading: false, error: false });
    } catch {
      if (mounted.current && !signal?.aborted) setOAuth((previous) => ({ ...previous, loading: false, error: true }));
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    const controller = new AbortController();
    void loadProfile(controller.signal);
    void loadSecurity(controller.signal);
    void loadSessions(controller.signal);
    void loadOAuth(controller.signal);
    return () => {
      mounted.current = false;
      controller.abort();
      activeMutation.current?.abort();
    };
  }, [loadOAuth, loadProfile, loadSecurity, loadSessions]);

  const emailVerificationSettingsReady = !oauth.loading && !oauth.error && oauth.data !== null;
  const turnstileRequired = oauth.data?.catalog.turnstileRequired === true;
  const turnstileSiteKey = oauth.data?.catalog.turnstileSiteKey ?? null;
  const turnstileUnavailable = emailVerificationSettingsReady && turnstileRequired
    && (turnstileSiteKey === null || turnstileFailed);

  const telegramBotName = oauth.data?.catalog.telegramBotName ?? null;
  useEffect(() => {
    const container = telegramWidget.current;
    if (!container) return undefined;
    container.replaceChildren();
    if (!telegramFlow || !telegramBotName) return undefined;
    const script = document.createElement('script');
    script.async = true;
    script.src = 'https://telegram.org/js/telegram-widget.js?22';
    script.referrerPolicy = 'no-referrer';
    script.setAttribute('data-telegram-login', telegramBotName);
    script.setAttribute('data-size', 'large');
    script.setAttribute('data-auth-url', telegramFlow.callbackURL);
    script.setAttribute('data-request-access', 'write');
    container.appendChild(script);
    return () => container.replaceChildren();
  }, [telegramBotName, telegramFlow]);

  const runMutation = useCallback(async <T,>(
    key: string,
    failureText: string | ((error: unknown) => string),
    operation: (signal: AbortSignal) => Promise<T>,
  ): Promise<MutationResult<T>> => {
    if (mutationLocked.current) return { ok: false };
    mutationLocked.current = true;
    const controller = new AbortController();
    activeMutation.current = controller;
    if (mounted.current) {
      setBusy(key);
      setNotice(null);
    }
    try {
      const value = await operation(controller.signal);
      if (!mounted.current || controller.signal.aborted) return { ok: false };
      return { ok: true, value };
    } catch (error) {
      if (mounted.current && !controller.signal.aborted) {
        const text = typeof failureText === 'function' ? failureText(error) : failureText;
        setNotice({ kind: 'error', text });
      }
      return { ok: false };
    } finally {
      if (activeMutation.current === controller) activeMutation.current = null;
      mutationLocked.current = false;
      if (mounted.current && !controller.signal.aborted) setBusy(null);
    }
  }, []);

  const updateSecurityState = useCallback((update: (value: SecurityOverview) => SecurityOverview) => {
    setSecurity((previous) => previous.data
      ? { ...previous, data: update(previous.data), error: false }
      : previous);
  }, []);

  async function saveDisplayName(event: FormEvent) {
    event.preventDefault();
    const normalized = validDisplayName(displayName);
    if (!normalized) {
      setNotice({ kind: 'error', text: t('Enter a display name of 1–64 characters.') });
      return;
    }
    const result = await runMutation('display-name', t('Unable to update your display name.'),
      (signal) => updateDisplayName(normalized, signal));
    if (!result.ok || !profile.data) return;
    const updated = { ...profile.data, displayName: normalized };
    setProfile({ data: updated, loading: false, error: false });
    setDisplayName(normalized);
    onProfileChange?.(updated);
    setNotice({ kind: 'success', text: t('Display name updated.') });
  }

  async function saveLanguage(rawLanguage: string) {
    const language = LANGUAGE_OPTIONS.find((option) => option.code === rawLanguage)?.code;
    if (!language) {
      setNotice({ kind: 'error', text: t('Select a supported interface language.') });
      return;
    }
    if (profile.data?.language === language) return;
    const result = await runMutation('language', t('Unable to save your language preference.'),
      (signal) => updateLanguage(language, signal));
    if (!result.ok || !profile.data) return;
    const updated = { ...profile.data, language };
    setProfile({ data: updated, loading: false, error: false });
    onProfileChange?.(updated);
    void i18nRef.current.changeLanguage(language).catch(() => undefined);
    setNotice({ kind: 'success', text: t('Language preference saved.') });
  }

  function setSidebarPreference(section: keyof SidebarModules, key: string, enabled: boolean) {
    setSidebarModules((previous) => {
      if (!previous || !Object.prototype.hasOwnProperty.call(previous[section], key)) return previous;
      return {
        ...previous,
        [section]: { ...previous[section], [key]: enabled },
      } as SidebarModules;
    });
  }

  async function saveSidebarPreferences(event: FormEvent) {
    event.preventDefault();
    if (!sidebarModules) return;
    const snapshot = sidebarModules;
    const result = await runMutation('sidebar', t('Unable to save your navigation preferences.'),
      (signal) => updateSidebarModules(snapshot, signal));
    if (!result.ok || !profile.data) return;
    const updated = { ...profile.data, sidebarModules: snapshot };
    setProfile({ data: updated, loading: false, error: false });
    onProfileChange?.(updated);
    setNotice({ kind: 'success', text: t('Navigation preferences saved.') });
  }

  function updateNotificationField<K extends keyof ProfileNotificationSettings>(
    field: K,
    value: ProfileNotificationSettings[K],
  ) {
    setNotificationSettings((previous) => previous ? { ...previous, [field]: value } : previous);
  }

  async function saveNotificationPreferences(event: FormEvent) {
    event.preventDefault();
    const account = profile.data;
    const settings = notificationSettings;
    if (!account || !settings) return;
    if (!Number.isInteger(settings.quotaWarningThreshold)
      || settings.quotaWarningThreshold < 1 || settings.quotaWarningThreshold > MAX_NOTIFICATION_QUOTA) {
      setNotice({ kind: 'error', text: t('Enter a quota warning threshold between 1 and 2,147,483,647.') });
      return;
    }
    if (!validNotificationEmail(settings.notificationEmail)) {
      setNotice({ kind: 'error', text: t('Enter a valid notification email address.') });
      return;
    }
    if (settings.notifyType === 'email' && settings.notificationEmail === ''
      && (!account.emailVerified || account.email === '')) {
      setNotice({ kind: 'error', text: t('Enter a notification email because this account has no verified email.') });
      return;
    }
    if (!validNotificationURL(settings.webhookURL, 'webhook')) {
      setNotice({ kind: 'error', text: t('Enter a safe HTTPS webhook URL.') });
      return;
    }
    if (!validNotificationURL(settings.barkURL, 'bark')) {
      setNotice({ kind: 'error', text: t('Enter a safe Bark URL using only the supported template variables.') });
      return;
    }
    if (!validNotificationURL(settings.gotifyURL, 'gotify')) {
      setNotice({ kind: 'error', text: t('Enter a safe Gotify server base URL without a query or message path.') });
      return;
    }
    if ((settings.notifyType === 'webhook' && settings.webhookURL === '')
      || (settings.notifyType === 'bark' && settings.barkURL === '')
      || (settings.notifyType === 'gotify' && settings.gotifyURL === '')) {
      setNotice({ kind: 'error', text: t('Complete the destination fields for the selected notification method.') });
      return;
    }
    if (!Number.isInteger(settings.gotifyPriority) || settings.gotifyPriority < 0 || settings.gotifyPriority > 10) {
      setNotice({ kind: 'error', text: t('Gotify priority must be an integer from 0 to 10.') });
      return;
    }
    if (!validWriteOnlyCredential(webhookSecret, MAX_WEBHOOK_SECRET_BYTES)
      || (webhookSecret !== '' && settings.webhookURL === '')) {
      setNotice({ kind: 'error', text: t('Enter a valid webhook secret only with a webhook URL.') });
      return;
    }
    if (!validWriteOnlyCredential(gotifyToken, MAX_GOTIFY_TOKEN_BYTES)
      || (gotifyToken !== '' && settings.gotifyURL === '')) {
      setNotice({ kind: 'error', text: t('Enter a valid Gotify token only with a Gotify server URL.') });
      return;
    }
    const saved = account.notificationSettings;
    const canPreserveGotifyToken = saved.gotifyTokenConfigured && settings.gotifyURL === saved.gotifyURL;
    if (settings.notifyType === 'gotify' && gotifyToken === '' && !canPreserveGotifyToken) {
      setNotice({ kind: 'error', text: t('Enter a Gotify application token for this server.') });
      return;
    }

    const webhookSecretValue = webhookSecret;
    const gotifyTokenValue = gotifyToken;
    setWebhookSecret('');
    setGotifyToken('');
    const result = await runMutation('notification-settings', t('Unable to save notification and privacy settings.'),
      (signal) => updateNotificationSettings({
        notifyType: settings.notifyType,
        quotaWarningThreshold: settings.quotaWarningThreshold,
        notificationEmail: settings.notificationEmail,
        webhookURL: settings.webhookURL,
        webhookSecret: webhookSecretValue,
        barkURL: settings.barkURL,
        gotifyURL: settings.gotifyURL,
        gotifyToken: gotifyTokenValue,
        gotifyPriority: settings.gotifyPriority,
        acceptUnsetRatioModel: settings.acceptUnsetRatioModel,
        recordIPLog: settings.recordIPLog,
        ...(account.role >= 10
          ? { upstreamModelUpdateNotifyEnabled: settings.upstreamModelUpdateNotifyEnabled }
          : {}),
      }, account.role, signal));
    if (!result.ok) return;
    const updatedSettings: ProfileNotificationSettings = {
      ...settings,
      webhookSecretConfigured: webhookSecretValue !== ''
        || (saved.webhookSecretConfigured && settings.webhookURL === saved.webhookURL),
      gotifyTokenConfigured: gotifyTokenValue !== '' || canPreserveGotifyToken,
    };
    const updated = { ...account, notificationSettings: updatedSettings };
    setNotificationSettings(updatedSettings);
    setProfile({ data: updated, loading: false, error: false });
    onProfileChange?.(updated);
    setNotice({ kind: 'success', text: t('Notification and privacy settings saved.') });
  }

  async function savePassword(event: FormEvent) {
    event.preventDefault();
    if (!oldPassword || oldPassword.length > 128) {
      setNotice({ kind: 'error', text: t('Enter your current password.') });
      return;
    }
    if (newPassword.length < 8 || newPassword.length > 64) {
      setNotice({ kind: 'error', text: t('New password must be 8–64 characters.') });
      return;
    }
    if (newPassword !== confirmPassword) {
      setNotice({ kind: 'error', text: t('New passwords do not match.') });
      return;
    }
    const current = oldPassword;
    const replacement = newPassword;
    setOldPassword('');
    setNewPassword('');
    setConfirmPassword('');
    const result = await runMutation('password', t('Unable to change your password.'),
      (signal) => changePassword(current, replacement, signal));
    if (!result.ok) return;
    setNotice({ kind: 'success', text: t('Password changed. Other login sessions were signed out.') });
    void loadSessions();
  }

  async function replaceAccessToken(event: FormEvent) {
    event.preventDefault();
    if (!accessTokenConfirmed) {
      setNotice({ kind: 'error', text: t('Confirm that your existing access token will stop working.') });
      return;
    }
    // Never retain a previous one-time credential while requesting its replacement.
    setAccessToken(null);
    setAccessTokenConfirmed(false);
    const result = await runMutation('access-token', t('Unable to generate a new access token.'), generateAccessToken);
    if (!result.ok) return;
    setAccessToken(result.value);
    setNotice({ kind: 'success', text: t('New access token generated. Save it now; it will not be shown again.') });
  }

  async function copyAccessToken() {
    const token = accessToken;
    if (!token) return;
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(token);
      if (mounted.current && accessToken === token) {
        setNotice({ kind: 'success', text: t('Access token copied.') });
      }
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to copy the access token.') });
    }
  }

  function hideAccessToken() {
    setAccessToken(null);
    setNotice({ kind: 'success', text: t('Access token hidden.') });
  }

  async function sendEmailCode() {
    const normalized = validEmailAddress(emailAddress);
    if (!normalized) {
      setNotice({ kind: 'error', text: t('Enter a valid email address of at most 50 characters.') });
      return;
    }
    if (!emailVerificationSettingsReady) {
      setNotice({ kind: 'error', text: t('Human verification settings are unavailable.') });
      return;
    }
    if (turnstileUnavailable) {
      setNotice({ kind: 'error', text: t('Human verification is unavailable.') });
      return;
    }
    if (turnstileRequired && turnstileToken === '') {
      setNotice({ kind: 'error', text: t('Complete the human verification challenge.') });
      return;
    }
    const verificationToken = turnstileRequired ? turnstileToken : undefined;
    if (turnstileRequired) {
      setTurnstileToken('');
      setTurnstileWidgetKey((value) => value + 1);
    }
    setEmailCode('');
    setEmailCodeTarget(null);
    const result = await runMutation('email-send', t('Unable to send the verification code.'),
      (signal) => sendEmailVerification(normalized, verificationToken, signal));
    if (!result.ok) return;
    setEmailAddress(normalized);
    setEmailCodeTarget(normalized);
    setNotice({ kind: 'success', text: t('Verification code sent. Check your email.') });
  }

  async function verifyAndBindEmail(event: FormEvent) {
    event.preventDefault();
    const normalized = validEmailAddress(emailAddress);
    const code = emailCode.trim();
    if (!normalized || normalized !== emailCodeTarget || !validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Send a code to this email, then enter its 6 digits.') });
      return;
    }
    setEmailCode('');
    const result = await runMutation('email-bind', t('Unable to verify and bind the email address.'),
      (signal) => bindEmail(normalized, code, signal));
    if (!result.ok || !profile.data) return;
    const updated = { ...profile.data, email: normalized, emailVerified: true };
    setProfile({ data: updated, loading: false, error: false });
    setEmailAddress('');
    setEmailCodeTarget(null);
    onProfileChange?.(updated);
    setNotice({ kind: 'success', text: t('Email address verified and bound.') });
  }

  async function beginTwoFactorSetup() {
    const result = await runMutation('twofa-start', t('Unable to start two-factor setup.'), startTwoFactorSetup);
    if (!result.ok) return;
    setTwoFactorSetup(result.value);
    setTwoFactorCode('');
    setNotice({ kind: 'success', text: t('Authenticator setup is ready.') });
  }

  function cancelTwoFactorSetup() {
    if (!window.confirm(t('Cancel two-factor setup? The displayed secret will be hidden.'))) return;
    setTwoFactorSetup(null);
    setTwoFactorCode('');
    setNotice(null);
  }

  async function completeTwoFactorSetup(event: FormEvent) {
    event.preventDefault();
    const code = twoFactorCode.trim();
    if (!validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code.') });
      return;
    }
    setTwoFactorCode('');
    const result = await runMutation('twofa-enable', t('Unable to enable two-factor authentication.'),
      (signal) => enableTwoFactor(code, signal));
    if (!result.ok) return;
    setTwoFactorSetup(null);
    setBackupCodes(result.value);
    setBackupCodesSaved(false);
    updateSecurityState((value) => ({ ...value, twoFactorEnabled: true }));
    setNotice({ kind: 'success', text: t('Two-factor authentication enabled. Save the recovery codes now.') });
  }

  async function turnOffTwoFactor(event: FormEvent) {
    event.preventDefault();
    const code = disableCode.trim();
    if (!validFactorCode(code)) {
      setNotice({ kind: 'error', text: t('Enter a valid authenticator or backup code.') });
      return;
    }
    if (!disableConfirmed) {
      setNotice({ kind: 'error', text: t('Confirm that you understand two-factor protection will be removed.') });
      return;
    }
    setDisableCode('');
    setDisableConfirmed(false);
    const result = await runMutation('twofa-disable', t('Unable to disable two-factor authentication.'),
      (signal) => disableTwoFactor(code, signal));
    if (!result.ok) return;
    setBackupCodes([]);
    updateSecurityState((value) => ({ ...value, twoFactorEnabled: false }));
    setNotice({ kind: 'success', text: t('Two-factor authentication disabled.') });
  }

  async function replaceBackupCodes(event: FormEvent) {
    event.preventDefault();
    const code = regenerateCode.trim();
    if (!validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code.') });
      return;
    }
    if (!regenerateConfirmed) {
      setNotice({ kind: 'error', text: t('Confirm that existing backup codes will stop working.') });
      return;
    }
    setRegenerateCode('');
    setRegenerateConfirmed(false);
    const result = await runMutation('twofa-backup', t('Unable to regenerate backup codes.'), async (signal) => {
      const proof = await verifyTwoFactor('twofa.backup_codes.regenerate', code, signal);
      return regenerateBackupCodes(proof, signal);
    });
    if (!result.ok) return;
    setBackupCodes(result.value);
    setBackupCodesSaved(false);
    setNotice({ kind: 'success', text: t('New backup codes generated. Previous codes no longer work.') });
  }

  function hideBackupCodes() {
    if (!backupCodesSaved) {
      setNotice({ kind: 'error', text: t('Confirm that you saved the backup codes before hiding them.') });
      return;
    }
    setBackupCodes([]);
    setBackupCodesSaved(false);
    setNotice({ kind: 'success', text: t('Backup codes hidden.') });
  }

  async function registerPasskey() {
    if (!passkeyRegistrationSupported()) {
      setNotice({ kind: 'error', text: t('Passkey registration is not supported on this device.') });
      return;
    }
    const twoFactorEnabled = security.data?.twoFactorEnabled === true;
    const code = passkeyCode.trim();
    if (twoFactorEnabled && !validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code for this security change.') });
      return;
    }
    setPasskeyCode('');
    const result = await runMutation(
      'passkey-register',
      (error) => error instanceof DOMException && error.name === 'NotAllowedError'
        ? t('Passkey registration was canceled.')
        : t('Unable to register a passkey.'),
      async (signal) => {
        const proof = twoFactorEnabled
          ? await verifyTwoFactor('passkey.register', code, signal)
          : undefined;
        const begin = await beginPasskeyRegistration(proof, signal);
        const publicKey = preparePasskeyCreationOptions(begin.publicKey);
        const credential = await navigator.credentials.create({ publicKey } as unknown as CredentialCreationOptions);
        if (!credential) throw new DOMException('canceled', 'NotAllowedError');
        if (signal.aborted) throw new DOMException('aborted', 'AbortError');
        const serialized = serializePasskeyRegistrationCredential(credential);
        await finishPasskeyRegistration(begin.flowToken, serialized, signal);
      },
    );
    if (!result.ok) return;
    updateSecurityState((value) => ({ ...value, passkeyEnabled: true }));
    setNotice({ kind: 'success', text: t('Passkey registered.') });
    void loadSecurity();
  }

  async function removePasskey() {
    if (!window.confirm(t('Remove all passkeys from this account?'))) return;
    const twoFactorEnabled = security.data?.twoFactorEnabled === true;
    const code = passkeyCode.trim();
    if (twoFactorEnabled && !validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code for this security change.') });
      return;
    }
    setPasskeyCode('');
    const result = await runMutation('passkey-delete', t('Unable to remove the passkey.'), async (signal) => {
      const proof = twoFactorEnabled
        ? await verifyTwoFactor('passkey.delete', code, signal)
        : undefined;
      await deletePasskeys(proof, signal);
    });
    if (!result.ok) return;
    updateSecurityState((value) => ({ ...value, passkeyEnabled: false, passkeys: [] }));
    setNotice({ kind: 'success', text: t('Passkey removed.') });
  }

  async function signOutSession(session: LoginSession) {
    if (!window.confirm(t('Sign out session {{session}}? It may be this browser.', {
      session: compactIdentifier(session.sid),
    }))) return;
    const result = await runMutation('session-revoke', t('Unable to sign out that session.'),
      (signal) => revokeSession(session.sid, signal));
    if (!result.ok) return;
    setSessions((previous) => ({
      data: previous.data?.filter((item) => item.sid !== session.sid) ?? [],
      loading: false,
      error: false,
    }));
    setNotice({ kind: 'success', text: t('Session signed out.') });
    // If this was the current session, the authenticated probe receives a 401
    // and the shared API interceptor invalidates the app shell immediately.
    void getProfile().catch(() => undefined);
  }

  async function signOutOtherSessions() {
    if (!window.confirm(t('Sign out every other session? This browser will stay signed in.'))) return;
    const result = await runMutation('sessions-revoke-others', t('Unable to sign out other sessions.'), async (signal) => {
      await revokeOtherSessions(signal);
      return getSessions(signal);
    });
    if (!result.ok) return;
    setSessions({ data: result.value, loading: false, error: false });
    setNotice({ kind: 'success', text: t('Other sessions signed out.') });
  }

  async function connectOAuth(providerId: number) {
    const provider = oauth.data?.catalog.customProviders.find((item) => item.id === providerId);
    if (!provider) return;
    const result = await runMutation('oauth-bind', t('Unable to start the provider connection.'),
      (signal) => createOAuthBindingURL(provider, window.location.origin, signal));
    if (!result.ok) return;
    setNotice({ kind: 'success', text: t('Continue with the identity provider to finish connecting.') });
    onExternalNavigate(result.value);
  }

  async function disconnectOAuth(binding: CustomOAuthBinding) {
    if (!window.confirm(t('Disconnect {{provider}} from this account?', { provider: binding.providerName }))) return;
    const result = await runMutation('oauth-unbind', t('Unable to disconnect the provider.'),
      (signal) => unbindOAuth(binding.providerId, signal));
    if (!result.ok) return;
    setOAuth((previous) => previous.data ? {
      ...previous,
      data: {
        ...previous.data,
        bindings: previous.data.bindings.filter((item) => item.providerId !== binding.providerId),
      },
    } : previous);
    setNotice({ kind: 'success', text: t('{{provider}} disconnected.', { provider: binding.providerName }) });
  }

  async function connectWeChat(event: FormEvent) {
    event.preventDefault();
    const code = weChatCode.trim();
    if (!code || code.length > 512 || hasControlCharacters(code)) {
      setNotice({ kind: 'error', text: t('Enter a valid WeChat authorization code.') });
      return;
    }
    setWeChatCode('');
    const result = await runMutation('wechat-bind', t('Unable to connect WeChat.'),
      (signal) => bindWeChat(code, signal));
    if (!result.ok) return;
    setNotice({ kind: 'success', text: t('WeChat connected.') });
  }

  async function beginTelegramBinding() {
    setTelegramFlow(null);
    const result = await runMutation('telegram-bind', t('Unable to start Telegram binding.'),
      (signal) => startTelegramBinding(window.location.origin, signal));
    if (!result.ok) return;
    setTelegramFlow(result.value);
    setNotice({ kind: 'success', text: t('Use the Telegram sign-in control to finish binding your account.') });
  }

  async function deleteOwnAccount(event: FormEvent) {
    event.preventDefault();
    const account = profile.data;
    if (!account || account.role === 100) return;
    if (deleteConfirmation !== account.username || !deleteAcknowledged) {
      setNotice({ kind: 'error', text: t('Enter your exact username and acknowledge the permanent account action.') });
      return;
    }
    setDeleteConfirmation('');
    setDeleteAcknowledged(false);
    setAccessToken(null);
    const result = await runMutation('account-delete', t('Unable to delete your account.'), deleteAccount);
    if (!result.ok) return;
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    onNavigate?.('/sign-in');
  }

  const formattedDate = useCallback((date: Date): string => {
    try {
      return new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
    } catch {
      return t('Unknown time');
    }
  }, [i18n.language, t]);

  const renderProfile = (): ReactNode => {
    if (profile.loading || profile.error || !profile.data) {
      return (
        <EmptyOrFailure
          loading={profile.loading}
          error={profile.error}
          loadingText={t('Loading profile…')}
          errorText={t('Unable to load your profile.')}
          onRetry={() => { void loadProfile(); }}
        />
      );
    }
    return (
      <>
        <dl className="kv">
          <dt>{t('Username')}</dt><dd>{profile.data.username}</dd>
          <dt>{t('Email')}</dt><dd>{profile.data.email || t('Not connected')}</dd>
          <dt>{t('Group')}</dt><dd>{profile.data.group}</dd>
        </dl>
        <fieldset className="user-subpanel">
          <legend>{t('Interface preferences')}</legend>
          <label>
            {t('Interface language')}
            <select
              value={profile.data.language
                ?? LANGUAGE_OPTIONS.find((option) => option.code === i18n.language)?.code
                ?? 'en'}
              onChange={(event) => { void saveLanguage(event.target.value); }}
              disabled={busy !== null}
              aria-describedby="profile-language-help"
            >
              {LANGUAGE_OPTIONS.map((option) => (
                <option key={option.code} value={option.code}>{option.label}</option>
              ))}
            </select>
          </label>
          <p id="profile-language-help" className="muted">
            {t('Your saved language follows you across signed-in devices.')}
          </p>
          {profile.data.role !== 100 && sidebarModules && (
            <form className="grid-form" aria-label={t('Personal navigation preferences')} onSubmit={saveSidebarPreferences}>
              <p className="muted">{t('Choose which personal navigation areas and links are visible.')}</p>
              {SIDEBAR_SECTIONS.map((section) => (
                <fieldset className="user-subpanel" key={section.key}>
                  <legend>{t(section.label)}</legend>
                  <label>
                    <input
                      type="checkbox"
                      checked={sidebarModules[section.key].enabled}
                      onChange={(event) => setSidebarPreference(section.key, 'enabled', event.target.checked)}
                      disabled={busy !== null}
                    />
                    {t('Show this navigation area')}
                  </label>
                  {section.modules.map((module) => (
                    <label key={module.key}>
                      <input
                        type="checkbox"
                        checked={sidebarPreferenceEnabled(sidebarModules, section.key, module.key)}
                        onChange={(event) => setSidebarPreference(section.key, module.key, event.target.checked)}
                        disabled={busy !== null || !sidebarModules[section.key].enabled}
                      />
                      {t(module.label)}
                    </label>
                  ))}
                </fieldset>
              ))}
              <div className="row">
                <button type="submit" disabled={busy !== null}>{t('Save navigation preferences')}</button>
                <button
                  type="button"
                  className="link"
                  onClick={() => setSidebarModules(enabledSidebarModules())}
                  disabled={busy !== null}
                >
                  {t('Reset navigation choices')}
                </button>
              </div>
            </form>
          )}
        </fieldset>
        <fieldset className="user-subpanel">
          <legend>{t('Verified email')}</legend>
          <p className="muted">
            {profile.data.emailVerified
              ? t('Current verified email: {{email}}', { email: profile.data.email })
              : profile.data.email
                ? t('Current email is not verified: {{email}}', { email: profile.data.email })
                : t('No verified email is connected.')}
          </p>
          <form className="grid-form" aria-label={t('Verify and bind email')} onSubmit={verifyAndBindEmail}>
            <label>
              {t('Email address')}
              <input
                type="email"
                value={emailAddress}
                onChange={(event) => {
                  const next = event.target.value;
                  setEmailAddress(next);
                  if (next.trim().toLowerCase() !== emailCodeTarget) {
                    setEmailCodeTarget(null);
                    setEmailCode('');
                  }
                }}
                required
                maxLength={50}
                autoComplete="email"
                disabled={busy !== null}
              />
            </label>
            {oauth.loading && <p className="muted" role="status">{t('Loading human verification settings…')}</p>}
            {oauth.error && (
              <p className="error" role="alert">{t('Human verification settings are unavailable.')}</p>
            )}
            {emailVerificationSettingsReady && turnstileRequired && turnstileSiteKey && !turnstileFailed && (
              <TurnstileWidget
                key={turnstileWidgetKey}
                className="turnstile-widget"
                siteKey={turnstileSiteKey}
                label={t('Human verification')}
                onVerify={(token) => {
                  setTurnstileFailed(false);
                  setTurnstileToken(token);
                  setNotice(null);
                }}
                onExpire={() => setTurnstileToken('')}
                onError={() => {
                  setTurnstileFailed(true);
                  setTurnstileToken('');
                }}
              />
            )}
            {turnstileUnavailable && (
              <div className="error-panel" role="alert">
                <p>{t('Human verification is unavailable.')}</p>
                {turnstileSiteKey && (
                  <button
                    type="button"
                    className="link"
                    onClick={() => {
                      setTurnstileFailed(false);
                      setTurnstileToken('');
                      setTurnstileWidgetKey((value) => value + 1);
                    }}
                  >
                    {t('Retry human verification')}
                  </button>
                )}
              </div>
            )}
            <button
              type="button"
              onClick={() => { void sendEmailCode(); }}
              disabled={busy !== null || !emailVerificationSettingsReady || turnstileUnavailable
                || (turnstileRequired && turnstileToken === '')}
            >
              {emailCodeTarget ? t('Send another verification code') : t('Send verification code')}
            </button>
            {emailCodeTarget && (
              <>
                <p className="muted" role="status">
                  {t('A 6-digit code was sent to {{email}}.', { email: emailCodeTarget })}
                </p>
                <label>
                  {t('Email verification code')}
                  <input
                    value={emailCode}
                    onChange={(event) => setEmailCode(event.target.value)}
                    inputMode="numeric"
                    pattern="[0-9]{6}"
                    minLength={6}
                    maxLength={6}
                    autoComplete="one-time-code"
                    required
                    disabled={busy !== null}
                  />
                </label>
                <button type="submit" disabled={busy !== null}>{t('Verify and bind email')}</button>
              </>
            )}
          </form>
        </fieldset>
        <form className="grid-form" aria-label={t('Update display name')} onSubmit={saveDisplayName}>
          <label>
            {t('Display name')}
            <input
              value={displayName}
              onChange={(event) => setDisplayName(event.target.value)}
              required
              maxLength={64}
              autoComplete="name"
              disabled={busy !== null}
            />
          </label>
          <button type="submit" disabled={busy !== null}>{t('Save display name')}</button>
        </form>
        <form className="grid-form" aria-label={t('Change password')} onSubmit={savePassword}>
          <label>
            {t('Current password')}
            <input
              type="password"
              value={oldPassword}
              onChange={(event) => setOldPassword(event.target.value)}
              required
              maxLength={128}
              autoComplete="current-password"
              disabled={busy !== null}
            />
          </label>
          <label>
            {t('New password')}
            <input
              type="password"
              value={newPassword}
              onChange={(event) => setNewPassword(event.target.value)}
              required
              minLength={8}
              maxLength={64}
              autoComplete="new-password"
              disabled={busy !== null}
            />
          </label>
          <label>
            {t('Confirm new password')}
            <input
              type="password"
              value={confirmPassword}
              onChange={(event) => setConfirmPassword(event.target.value)}
              required
              minLength={8}
              maxLength={64}
              autoComplete="new-password"
              disabled={busy !== null}
            />
          </label>
          <button type="submit" disabled={busy !== null}>{t('Change password')}</button>
        </form>
      </>
    );
  };

  const renderNotifications = (): ReactNode => {
    if (profile.loading || profile.error || !profile.data || !notificationSettings) {
      return (
        <EmptyOrFailure
          loading={profile.loading}
          error={profile.error || !profile.data || !notificationSettings}
          loadingText={t('Loading notification and privacy settings…')}
          errorText={t('Unable to load notification and privacy settings. Changes are disabled.')}
          onRetry={() => { void loadProfile(); }}
        />
      );
    }
    const saved = profile.data.notificationSettings;
    const sameWebhookEndpoint = notificationSettings.webhookURL === saved.webhookURL;
    const sameGotifyEndpoint = notificationSettings.gotifyURL === saved.gotifyURL;
    const webhookCredentialPreserved = saved.webhookSecretConfigured && sameWebhookEndpoint;
    const gotifyCredentialPreserved = saved.gotifyTokenConfigured && sameGotifyEndpoint;

    return (
      <form className="grid-form" aria-label={t('Notification and privacy settings')} onSubmit={saveNotificationPreferences}>
        <label>
          {t('Notification method')}
          <select
            value={notificationSettings.notifyType}
            onChange={(event) => {
              const next = NOTIFICATION_OPTIONS.find((option) => option.value === event.target.value);
              if (next) updateNotificationField('notifyType', next.value);
            }}
            disabled={busy !== null}
          >
            {NOTIFICATION_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>{t(option.label)}</option>
            ))}
          </select>
        </label>
        <label>
          {t('Quota warning threshold')}
          <input
            type="number"
            min={1}
            max={MAX_NOTIFICATION_QUOTA}
            step={1}
            value={notificationSettings.quotaWarningThreshold}
            onChange={(event) => updateNotificationField('quotaWarningThreshold', Number(event.target.value))}
            required
            disabled={busy !== null}
            aria-describedby="profile-quota-warning-help"
          />
        </label>
        <p id="profile-quota-warning-help" className="muted">
          {t('Send a notification when the remaining quota drops below this many quota units.')}
        </p>
        <p className="muted">
          {t('Webhook secrets and Gotify tokens are write-only. Stored values are never displayed.')}
        </p>

        {notificationSettings.notifyType === 'email' && (
          <fieldset className="user-subpanel">
            <legend>{t('Email delivery')}</legend>
            <label>
              {t('Notification email')}
              <input
                type="email"
                value={notificationSettings.notificationEmail}
                onChange={(event) => updateNotificationField('notificationEmail', event.target.value)}
                maxLength={MAX_NOTIFICATION_EMAIL_BYTES}
                autoComplete="email"
                disabled={busy !== null}
                aria-describedby="profile-notification-email-help"
              />
            </label>
            <p id="profile-notification-email-help" className="muted">
              {t('Leave blank to use the verified email on this account.')}
            </p>
          </fieldset>
        )}

        {notificationSettings.notifyType === 'webhook' && (
          <fieldset className="user-subpanel">
            <legend>{t('Webhook delivery')}</legend>
            <label>
              {t('Webhook URL')}
              <input
                type="url"
                value={notificationSettings.webhookURL}
                onChange={(event) => updateNotificationField('webhookURL', event.target.value)}
                maxLength={MAX_NOTIFICATION_URL_BYTES}
                placeholder="https://example.com/notifications"
                required
                disabled={busy !== null}
              />
            </label>
            <label>
              {t('Webhook signing secret')}
              <input
                type="password"
                value={webhookSecret}
                onChange={(event) => setWebhookSecret(event.target.value)}
                maxLength={MAX_WEBHOOK_SECRET_BYTES}
                autoComplete="new-password"
                disabled={busy !== null}
                aria-describedby="profile-webhook-secret-help"
              />
            </label>
            <p id="profile-webhook-secret-help" className="muted">
              {webhookSecret !== ''
                ? t('A replacement webhook secret is ready to save. Its value will be cleared from this form after submission.')
                : webhookCredentialPreserved
                  ? t('A webhook secret is configured for this saved endpoint. Leave this blank to keep it.')
                  : saved.webhookSecretConfigured
                    ? t('The configured webhook secret belongs to the previous endpoint. Leaving this blank removes it.')
                    : t('No webhook signing secret is configured. Leaving this blank keeps webhook signing disabled.')}
            </p>
          </fieldset>
        )}

        {notificationSettings.notifyType === 'bark' && (
          <fieldset className="user-subpanel">
            <legend>{t('Bark delivery')}</legend>
            <label>
              {t('Bark push URL')}
              <input
                type="url"
                value={notificationSettings.barkURL}
                onChange={(event) => updateNotificationField('barkURL', event.target.value)}
                maxLength={MAX_NOTIFICATION_URL_BYTES}
                placeholder="https://api.day.app/device-key"
                required
                disabled={busy !== null}
                aria-describedby="profile-bark-url-help"
              />
            </label>
            <p id="profile-bark-url-help" className="muted">
              {t('Bark URLs may use these template placeholders:')} <code>{'{{title}}'}</code>, <code>{'{{content}}'}</code>
            </p>
          </fieldset>
        )}

        {notificationSettings.notifyType === 'gotify' && (
          <fieldset className="user-subpanel">
            <legend>{t('Gotify delivery')}</legend>
            <label>
              {t('Gotify server URL')}
              <input
                type="url"
                value={notificationSettings.gotifyURL}
                onChange={(event) => updateNotificationField('gotifyURL', event.target.value)}
                maxLength={MAX_NOTIFICATION_URL_BYTES}
                placeholder="https://gotify.example.com"
                required
                disabled={busy !== null}
              />
            </label>
            <label>
              {t('Gotify application token')}
              <input
                type="password"
                value={gotifyToken}
                onChange={(event) => setGotifyToken(event.target.value)}
                maxLength={MAX_GOTIFY_TOKEN_BYTES}
                autoComplete="new-password"
                disabled={busy !== null}
                required={!gotifyCredentialPreserved}
                aria-describedby="profile-gotify-token-help"
              />
            </label>
            <p id="profile-gotify-token-help" className="muted">
              {gotifyToken !== ''
                ? t('A replacement Gotify token is ready to save. Its value will be cleared from this form after submission.')
                : gotifyCredentialPreserved
                  ? t('A Gotify token is configured for this saved server. Leave this blank to keep it.')
                  : saved.gotifyTokenConfigured
                    ? t('The configured Gotify token belongs to the previous server. Enter a replacement before saving.')
                    : t('Enter the application token created by this Gotify server.')}
            </p>
            <label>
              {t('Gotify priority')}
              <input
                type="number"
                min={0}
                max={10}
                step={1}
                value={notificationSettings.gotifyPriority}
                onChange={(event) => updateNotificationField('gotifyPriority', Number(event.target.value))}
                required
                disabled={busy !== null}
              />
            </label>
          </fieldset>
        )}

        <fieldset className="user-subpanel">
          <legend>{t('Usage and privacy preferences')}</legend>
          {profile.data.role >= 10 && (
            <label>
              <input
                type="checkbox"
                checked={notificationSettings.upstreamModelUpdateNotifyEnabled}
                onChange={(event) => updateNotificationField('upstreamModelUpdateNotifyEnabled', event.target.checked)}
                disabled={busy !== null}
              />
              {t('Notify me when upstream model checks find changes or failures.')}
            </label>
          )}
          <label>
            <input
              type="checkbox"
              checked={notificationSettings.acceptUnsetRatioModel}
              onChange={(event) => updateNotificationField('acceptUnsetRatioModel', event.target.checked)}
              disabled={busy !== null}
            />
            {t('Allow models that do not have a configured price ratio.')}
          </label>
          <label>
            <input
              type="checkbox"
              checked={notificationSettings.recordIPLog}
              onChange={(event) => updateNotificationField('recordIPLog', event.target.checked)}
              disabled={busy !== null}
            />
            {t('Record my IP address in usage and error logs.')}
          </label>
          <p className="muted">
            {t('IP address logging is off by default. Enable it only if you want this diagnostic information retained.')}
          </p>
        </fieldset>
        <button type="submit" disabled={busy !== null}>{t('Save notification and privacy settings')}</button>
      </form>
    );
  };

  const renderSecurity = (): ReactNode => {
    if (security.loading || security.error || !security.data) {
      return (
        <EmptyOrFailure
          loading={security.loading}
          error={security.error}
          loadingText={t('Loading security settings…')}
          errorText={t('Unable to load security settings. Security changes are disabled.')}
          onRetry={() => { void loadSecurity(); }}
        />
      );
    }
    const passkeys = security.data.passkeys;
    return (
      <>
        <ProfileBackupCodes codes={backupCodes} saved={backupCodesSaved} onSavedChange={setBackupCodesSaved} onHide={hideBackupCodes} />
        <fieldset className="user-subpanel">
          <legend>{t('Dashboard access token')}</legend>
          <p className="muted">
            {t('Existing access tokens cannot be viewed. Creating a replacement immediately invalidates the current token.')}
          </p>
          {accessToken && (
            <div className="user-subpanel" role="status" aria-label={t('New one-time access token')}>
              <label>
                {t('Access token')}
                <input
                  value={accessToken}
                  readOnly
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={t('Access token value')}
                />
              </label>
              <p className="muted">{t('Copy and save this token now. It disappears when hidden or when you leave this page.')}</p>
              <div className="row">
                <button type="button" onClick={() => { void copyAccessToken(); }} disabled={busy !== null}>
                  {t('Copy access token')}
                </button>
                <button type="button" className="link" onClick={hideAccessToken} disabled={busy !== null}>
                  {t('Hide access token')}
                </button>
              </div>
            </div>
          )}
          <form className="grid-form" aria-label={t('Regenerate access token')} onSubmit={replaceAccessToken}>
            <label>
              <input
                type="checkbox"
                checked={accessTokenConfirmed}
                onChange={(event) => setAccessTokenConfirmed(event.target.checked)}
                disabled={busy !== null}
              />
              {t('Invalidate my existing access token and create a replacement.')}
            </label>
            <button type="submit" disabled={busy !== null || !accessTokenConfirmed}>
              {t('Generate new access token')}
            </button>
          </form>
        </fieldset>
        <fieldset className="user-subpanel">
          <legend>{t('Two-factor authentication')}</legend>
          <p className="muted">
            {security.data.twoFactorEnabled ? t('Enabled') : t('Disabled')}
          </p>
          {!security.data.twoFactorEnabled && !twoFactorSetup && (
            <button type="button" onClick={() => { void beginTwoFactorSetup(); }} disabled={busy !== null}>
              {t('Set up two-factor authentication')}
            </button>
          )}
          {twoFactorSetup && (
            <div className="user-subpanel">
              <p>{t('Add this account to your authenticator app, then enter its 6-digit code.')}</p>
              <p>{t('Setup key')}: <code>{twoFactorSetup.secret}</code></p>
              <details>
                <summary>{t('Show provisioning URI')}</summary>
                <code>{twoFactorSetup.provisioningURL}</code>
              </details>
              <form className="inline-form" aria-label={t('Complete two-factor setup')} onSubmit={completeTwoFactorSetup}>
                <label>
                  {t('Authenticator code')}
                  <input
                    value={twoFactorCode}
                    onChange={(event) => setTwoFactorCode(event.target.value)}
                    inputMode="numeric"
                    pattern="[0-9]{6}"
                    minLength={6}
                    maxLength={6}
                    autoComplete="one-time-code"
                    required
                    disabled={busy !== null}
                  />
                </label>
                <button type="submit" disabled={busy !== null}>{t('Enable two-factor authentication')}</button>
                <button type="button" className="link" onClick={cancelTwoFactorSetup} disabled={busy !== null}>{t('Cancel')}</button>
              </form>
            </div>
          )}
          {security.data.twoFactorEnabled && (
            <>
              <form className="grid-form" aria-label={t('Regenerate backup codes')} onSubmit={replaceBackupCodes}>
                <label>
                  {t('Authenticator code')}
                  <input
                    value={regenerateCode}
                    onChange={(event) => setRegenerateCode(event.target.value)}
                    inputMode="numeric"
                    pattern="[0-9]{6}"
                    minLength={6}
                    maxLength={6}
                    autoComplete="one-time-code"
                    required
                    disabled={busy !== null}
                  />
                </label>
                <label>
                  <input
                    type="checkbox"
                    checked={regenerateConfirmed}
                    onChange={(event) => setRegenerateConfirmed(event.target.checked)}
                    disabled={busy !== null}
                  />
                  {t('Invalidate my existing backup codes.')}
                </label>
                <button type="submit" disabled={busy !== null || !regenerateConfirmed}>{t('Generate new backup codes')}</button>
              </form>
              <form className="grid-form" aria-label={t('Disable two-factor authentication')} onSubmit={turnOffTwoFactor}>
                <label>
                  {t('Authenticator or backup code')}
                  <input
                    value={disableCode}
                    onChange={(event) => setDisableCode(event.target.value)}
                    minLength={6}
                    maxLength={32}
                    autoComplete="one-time-code"
                    required
                    disabled={busy !== null}
                  />
                </label>
                <label>
                  <input
                    type="checkbox"
                    checked={disableConfirmed}
                    onChange={(event) => setDisableConfirmed(event.target.checked)}
                    disabled={busy !== null}
                  />
                  {t('I understand this removes two-factor protection and all backup codes.')}
                </label>
                <button type="submit" disabled={busy !== null || !disableConfirmed}>{t('Disable two-factor authentication')}</button>
              </form>
            </>
          )}
        </fieldset>

        <fieldset className="user-subpanel">
          <legend>{t('Passkeys')}</legend>
          <p className="muted">{security.data.passkeyEnabled ? t('Enabled') : t('Disabled')}</p>
          {passkeys.length === 0 ? (
            <p className="muted">{t('No passkey is registered.')}</p>
          ) : (
            <ul className="key-list">
              {passkeys.map((passkey) => (
                <li key={passkey.id}>
                  <strong>{passkey.attachment === 'platform' ? t('This-device passkey') : t('Security key or synced passkey')}</strong>
                  <span className="muted">
                    {passkey.lastUsedAt
                      ? t('Last used {{date}}', { date: formattedDate(new Date(passkey.lastUsedAt)) })
                      : t('Not used yet')}
                  </span>
                  {passkey.backupEligible && (
                    <span className="muted">{passkey.backupState ? t('Backed up') : t('Not backed up')}</span>
                  )}
                  {passkey.cloneWarning && <span className="error" role="alert">{t('Authenticator clone warning reported.')}</span>}
                </li>
              ))}
            </ul>
          )}
          {security.data.twoFactorEnabled && (
            <label>
              {t('Authenticator code for passkey changes')}
              <input
                value={passkeyCode}
                onChange={(event) => setPasskeyCode(event.target.value)}
                inputMode="numeric"
                pattern="[0-9]{6}"
                minLength={6}
                maxLength={6}
                autoComplete="one-time-code"
                disabled={busy !== null}
              />
            </label>
          )}
          <div className="row">
            {!security.data.passkeyEnabled && (
              <button type="button" onClick={() => { void registerPasskey(); }} disabled={busy !== null}>
                {t('Register a passkey')}
              </button>
            )}
            {security.data.passkeyEnabled && (
              <button type="button" onClick={() => { void removePasskey(); }} disabled={busy !== null}>
                {t('Remove passkey')}
              </button>
            )}
          </div>
        </fieldset>
      </>
    );
  };

  const renderOAuth = (): ReactNode => {
    if (oauth.loading || oauth.error || !oauth.data) {
      return (
        <EmptyOrFailure
          loading={oauth.loading}
          error={oauth.error}
          loadingText={t('Loading connected accounts…')}
          errorText={t('Unable to load connected accounts. Connection changes are disabled.')}
          onRetry={() => { void loadOAuth(); }}
        />
      );
    }
    const { catalog, bindings } = oauth.data;
    const knownProviderIds = new Set(catalog.customProviders.map((provider) => provider.id));
    const enabledBuiltIns = catalog.builtIn.filter((provider) => provider.enabled);
    const hasAnything = enabledBuiltIns.length > 0 || catalog.weChatEnabled
      || catalog.telegramEnabled || catalog.customProviders.length > 0 || bindings.length > 0;
    if (!hasAnything) return <p className="muted">{t('No account-connection providers are available.')}</p>;

    return (
      <>
        {enabledBuiltIns.length > 0 && (
          <div className="user-subpanel">
            <h3>{t('Built-in sign-in providers')}</h3>
            <ul>
              {enabledBuiltIns.map((provider) => <li key={provider.name}>{provider.name} · {t('Available')}</li>)}
            </ul>
            <p className="muted">
              {t('Connection and removal controls for these built-in providers are not available here.')}
            </p>
          </div>
        )}

        {catalog.customProviders.length > 0 && (
          <div className="user-subpanel">
            <h3>{t('Organization sign-in providers')}</h3>
            <ul className="key-list">
              {catalog.customProviders.map((provider) => {
                const binding = bindings.find((item) => item.providerId === provider.id);
                return (
                  <li key={provider.id}>
                    <strong>{provider.name}</strong>
                    <span className="muted">
                      {binding
                        ? t('Connected as {{account}}', { account: compactIdentifier(binding.providerUserId) })
                        : t('Not connected')}
                    </span>
                    {binding ? (
                      <button type="button" className="link" onClick={() => { void disconnectOAuth(binding); }} disabled={busy !== null}>
                        {t('Disconnect')}
                      </button>
                    ) : (
                      <button type="button" onClick={() => { void connectOAuth(provider.id); }} disabled={busy !== null}>
                        {t('Connect')}
                      </button>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        )}

        {bindings.some((binding) => !knownProviderIds.has(binding.providerId)) && (
          <div className="user-subpanel">
            <h3>{t('Inactive provider connections')}</h3>
            <ul className="key-list">
              {bindings.filter((binding) => !knownProviderIds.has(binding.providerId)).map((binding) => (
                <li key={binding.providerId}>
                  <strong>{binding.providerName}</strong>
                  <span className="muted">{t('Connected; new sign-ins are currently unavailable.')}</span>
                  <button type="button" className="link" onClick={() => { void disconnectOAuth(binding); }} disabled={busy !== null}>
                    {t('Disconnect')}
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}

        {catalog.telegramEnabled && catalog.telegramBotName && (
          <div className="user-subpanel">
            <h3>{t('Telegram')}</h3>
            <p className="muted">
              {profile.data?.telegramConnected
                ? t('Telegram is connected to this account.')
                : t('Start a one-time Telegram binding flow.')}
            </p>
            {!profile.data?.telegramConnected && (
              <button
                type="button"
                onClick={() => { void beginTelegramBinding(); }}
                disabled={busy !== null || profile.loading || profile.error}
              >
                {telegramFlow ? t('Restart Telegram binding') : t('Start Telegram binding')}
              </button>
            )}
            {!profile.data?.telegramConnected && telegramFlow && (
              <div className="user-subpanel" role="status" aria-live="polite">
                <p>{t('Complete the connection with Telegram below. The one-time flow expires after a short time.')}</p>
                <div ref={telegramWidget} role="group" aria-label={t('Telegram sign-in control')} />
              </div>
            )}
          </div>
        )}

        {catalog.weChatEnabled && (
          <form className="grid-form" aria-label={t('Connect WeChat')} onSubmit={connectWeChat}>
            {catalog.weChatQRCode && (
              <img src={catalog.weChatQRCode} alt={t('WeChat authorization QR code')} referrerPolicy="no-referrer" />
            )}
            <label>
              {t('WeChat authorization code')}
              <input
                value={weChatCode}
                onChange={(event) => setWeChatCode(event.target.value)}
                required
                maxLength={512}
                autoComplete="off"
                disabled={busy !== null}
              />
            </label>
            <button type="submit" disabled={busy !== null}>{t('Connect WeChat')}</button>
            <p className="muted">{t('This endpoint can connect WeChat, but it does not report whether WeChat is already connected.')}</p>
          </form>
        )}
      </>
    );
  };

  const renderDangerZone = (): ReactNode => {
    if (!profile.data) return <p className="muted">{t('Account deletion is unavailable until your profile loads.')}</p>;
    if (profile.data.role === 100) {
      return <p className="muted">{t('The root administrator account cannot be deleted.')}</p>;
    }
    return (
      <form className="grid-form" aria-label={t('Delete account')} onSubmit={deleteOwnAccount}>
        <p className="error">
          {t('Deleting your account revokes its sessions and credentials. You cannot undo this action from the application.')}
        </p>
        <label>
          {t('Type {{username}} to confirm', { username: profile.data.username })}
          <input
            value={deleteConfirmation}
            onChange={(event) => setDeleteConfirmation(event.target.value)}
            maxLength={64}
            autoComplete="off"
            spellCheck={false}
            disabled={busy !== null}
          />
        </label>
        <label>
          <input
            type="checkbox"
            checked={deleteAcknowledged}
            onChange={(event) => setDeleteAcknowledged(event.target.checked)}
            disabled={busy !== null}
          />
          {t('I understand this account action cannot be undone here.')}
        </label>
        <button
          type="submit"
          disabled={busy !== null || deleteConfirmation !== profile.data.username || !deleteAcknowledged}
        >
          {t('Delete account')}
        </button>
      </form>
    );
  };

  return (
    <main className="app">
      <header className="header row">
        <div>
          <h1>{t('Profile and security')}</h1>
          <p className="tagline">{t('Manage your identity, recovery methods, and active sessions.')}</p>
        </div>
        {onNavigate && <button type="button" className="link" onClick={() => onNavigate('/dashboard')}>{t('Dashboard')}</button>}
      </header>

      {notice && (
        <p className={notice.kind === 'error' ? 'error' : 'muted'} role={notice.kind === 'error' ? 'alert' : 'status'}>
          {notice.text}
        </p>
      )}
      {busy && <p className="muted" role="status" aria-live="polite">{t('Saving security-sensitive change…')}</p>}

      <section className="card" aria-labelledby="profile-account-title">
        <h2 id="profile-account-title">{t('Account profile')}</h2>
        {renderProfile()}
      </section>

      <section className="card" aria-labelledby="profile-notifications-title">
        <h2 id="profile-notifications-title">{t('Notifications and privacy')}</h2>
        {renderNotifications()}
      </section>

      <section className="card" aria-labelledby="profile-security-title">
        <h2 id="profile-security-title">{t('Account security')}</h2>
        {renderSecurity()}
      </section>

      <section className="card" aria-labelledby="profile-sessions-title">
        <h2 id="profile-sessions-title">{t('Login sessions')}</h2>
        <ProfileSessionsPanel
          sessions={sessions}
          busy={busy !== null}
          formattedDate={formattedDate}
          onRetry={() => { void loadSessions(); }}
          onSignOutSession={(session) => { void signOutSession(session); }}
          onSignOutOthers={() => { void signOutOtherSessions(); }}
        />
      </section>

      <section className="card" aria-labelledby="profile-bindings-title">
        <h2 id="profile-bindings-title">{t('Connected accounts')}</h2>
        {renderOAuth()}
      </section>

      <section className="card" aria-labelledby="profile-danger-title">
        <h2 id="profile-danger-title">{t('Danger zone')}</h2>
        {renderDangerZone()}
      </section>
    </main>
  );
}
