import {
  MAX_GOTIFY_TOKEN_BYTES,
  MAX_NOTIFICATION_EMAIL_BYTES,
  MAX_NOTIFICATION_QUOTA,
  MAX_NOTIFICATION_URL_BYTES,
  MAX_WEBHOOK_SECRET_BYTES,
  NOTIFICATION_OPTIONS,
} from './profile-view-model';
import { EmptyOrFailure } from './ProfileResourceState';
import type { ProfileController } from './useProfileController';

type ProfileNotificationsPanelProps = Pick<ProfileController,
  'busy' | 'gotifyToken' | 'loadProfile' | 'notificationSettings'
  | 'profile' | 'saveNotificationPreferences' | 'setGotifyToken' | 'setWebhookSecret'
  | 't' | 'updateNotificationField' | 'webhookSecret'
>;

export function ProfileNotificationsPanel({
  busy, gotifyToken, loadProfile, notificationSettings,
  profile, saveNotificationPreferences, setGotifyToken, setWebhookSecret,
  t, updateNotificationField, webhookSecret,
}: ProfileNotificationsPanelProps) {
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
}
