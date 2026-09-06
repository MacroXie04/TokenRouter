import { TurnstileWidget } from "../security";
import {
  enabledSidebarModules,
  LANGUAGE_OPTIONS,
  SIDEBAR_SECTIONS,
  sidebarPreferenceEnabled,
} from './profile-view-model';
import { EmptyOrFailure } from './ProfileResourceState';
import type { ProfileController } from './useProfileController';

type ProfilePreferencesPanelProps = Pick<ProfileController,
  'busy' | 'confirmPassword' | 'displayName' | 'emailAddress'
  | 'emailCode' | 'emailCodeTarget' | 'emailVerificationSettingsReady' | 'i18n'
  | 'loadProfile' | 'newPassword' | 'oauth' | 'oldPassword'
  | 'profile' | 'saveDisplayName' | 'saveLanguage' | 'savePassword'
  | 'saveSidebarPreferences' | 'sendEmailCode' | 'setConfirmPassword' | 'setDisplayName'
  | 'setEmailAddress' | 'setEmailCode' | 'setEmailCodeTarget' | 'setNewPassword'
  | 'setNotice' | 'setOldPassword' | 'setSidebarModules' | 'setSidebarPreference'
  | 'setTurnstileFailed' | 'setTurnstileToken' | 'setTurnstileWidgetKey' | 'sidebarModules'
  | 't' | 'turnstileFailed' | 'turnstileRequired' | 'turnstileSiteKey'
  | 'turnstileToken' | 'turnstileUnavailable' | 'turnstileWidgetKey' | 'verifyAndBindEmail'
>;

export function ProfilePreferencesPanel({
  busy, confirmPassword, displayName, emailAddress,
  emailCode, emailCodeTarget, emailVerificationSettingsReady, i18n,
  loadProfile, newPassword, oauth, oldPassword,
  profile, saveDisplayName, saveLanguage, savePassword,
  saveSidebarPreferences, sendEmailCode, setConfirmPassword, setDisplayName,
  setEmailAddress, setEmailCode, setEmailCodeTarget, setNewPassword,
  setNotice, setOldPassword, setSidebarModules, setSidebarPreference,
  setTurnstileFailed, setTurnstileToken, setTurnstileWidgetKey, sidebarModules,
  t, turnstileFailed, turnstileRequired, turnstileSiteKey,
  turnstileToken, turnstileUnavailable, turnstileWidgetKey, verifyAndBindEmail,
}: ProfilePreferencesPanelProps) {
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
}
