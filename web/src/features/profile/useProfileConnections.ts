import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent
} from 'react';
import {
  bindEmail,
  bindWeChat,
  createOAuthBindingURL,
  getOAuthBindings,
  getOAuthCatalog,
  sendEmailVerification,
  startTelegramBinding,
  unbindOAuth,
  type CustomOAuthBinding,
  type TelegramBindingFlow,
} from './profile-api';
import {
  hasControlCharacters,
  resource,
  validEmailAddress,
  validProofCode,
  type OAuthOverview,
  type Resource,
} from './profile-view-model';
import type { ProfileViewProps } from './profile-view-props';
import type { useProfileMutation } from './useProfileMutation';
import type { useProfilePreferences } from './useProfilePreferences';

type ProfileConnectionsOptions = Pick<ReturnType<typeof useProfileMutation>, 'mounted' | 'runMutation' | 'setNotice' | 't'> & Required<Pick<ProfileViewProps,
  'onExternalNavigate'
>> & Pick<ProfileViewProps,
  'onProfileChange'
> & Pick<ReturnType<typeof useProfilePreferences>, 'profile' | 'setProfile'>;

export function useProfileConnections({
  mounted, onExternalNavigate, onProfileChange, profile,
  runMutation, setNotice, setProfile, t,
}: ProfileConnectionsOptions) {
  const telegramWidget = useRef<HTMLDivElement | null>(null);
  const [oauth, setOAuth] = useState<Resource<OAuthOverview>>(resource);
  const [weChatCode, setWeChatCode] = useState('');
  const [emailAddress, setEmailAddress] = useState('');
  const [emailCode, setEmailCode] = useState('');
  const [emailCodeTarget, setEmailCodeTarget] = useState<string | null>(null);
  const [turnstileToken, setTurnstileToken] = useState('');
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0);
  const [turnstileFailed, setTurnstileFailed] = useState(false);
  const [telegramFlow, setTelegramFlow] = useState<TelegramBindingFlow | null>(null);

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
  }, [mounted]);

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
  return {
    telegramWidget, oauth, setOAuth, weChatCode,
    setWeChatCode, emailAddress, setEmailAddress, emailCode,
    setEmailCode, emailCodeTarget, setEmailCodeTarget, turnstileToken,
    setTurnstileToken, turnstileWidgetKey, setTurnstileWidgetKey, turnstileFailed,
    setTurnstileFailed, telegramFlow, setTelegramFlow, loadOAuth,
    emailVerificationSettingsReady, turnstileRequired, turnstileSiteKey, turnstileUnavailable,
    telegramBotName, sendEmailCode, verifyAndBindEmail, connectOAuth,
    disconnectOAuth, connectWeChat, beginTelegramBinding,
  };
}
