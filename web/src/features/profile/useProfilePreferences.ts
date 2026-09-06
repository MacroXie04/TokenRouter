import {
  useCallback,
  useState,
  type FormEvent
} from 'react';
import {
  getProfile,
  updateDisplayName,
  updateLanguage,
  updateNotificationSettings,
  updateSidebarModules,
  type ProfileAccount,
  type ProfileNotificationSettings,
  type SidebarModules,
} from './profile-api';
import {
  LANGUAGE_OPTIONS,
  MAX_GOTIFY_TOKEN_BYTES,
  MAX_NOTIFICATION_QUOTA,
  MAX_WEBHOOK_SECRET_BYTES,
  resource,
  validDisplayName,
  validNotificationEmail,
  validNotificationURL,
  validWriteOnlyCredential,
  type Resource,
} from './profile-view-model';
import type { ProfileViewProps } from './profile-view-props';
import type { useProfileMutation } from './useProfileMutation';

type ProfilePreferencesOptions = Pick<ReturnType<typeof useProfileMutation>, 'i18nRef' | 'mounted' | 'runMutation' | 'setNotice' | 't'> & Pick<ProfileViewProps,
  'onProfileChange'
>;

export function useProfilePreferences({
  i18nRef, mounted, onProfileChange, runMutation,
  setNotice, t,
}: ProfilePreferencesOptions) {
  const [profile, setProfile] = useState<Resource<ProfileAccount>>(resource);
  const [displayName, setDisplayName] = useState('');
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
  }, [i18nRef, mounted, onProfileChange]);

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
  return {
    profile, setProfile, displayName, setDisplayName,
    sidebarModules, setSidebarModules, notificationSettings, setNotificationSettings,
    webhookSecret, setWebhookSecret, gotifyToken, setGotifyToken,
    loadProfile, saveDisplayName, saveLanguage, setSidebarPreference,
    saveSidebarPreferences, updateNotificationField, saveNotificationPreferences,
  };
}
