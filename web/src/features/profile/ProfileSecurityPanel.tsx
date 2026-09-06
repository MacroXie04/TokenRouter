import { EmptyOrFailure } from './ProfileResourceState';
import { ProfileBackupCodes } from './ProfileSessionPanels';
import type { ProfileController } from './useProfileController';

type ProfileSecurityPanelProps = Pick<ProfileController,
  'accessToken' | 'accessTokenConfirmed' | 'backupCodes' | 'backupCodesSaved'
  | 'beginTwoFactorSetup' | 'busy' | 'cancelTwoFactorSetup' | 'completeTwoFactorSetup'
  | 'copyAccessToken' | 'disableCode' | 'disableConfirmed' | 'formattedDate'
  | 'hideAccessToken' | 'hideBackupCodes' | 'loadSecurity' | 'passkeyCode'
  | 'regenerateCode' | 'regenerateConfirmed' | 'registerPasskey' | 'removePasskey'
  | 'replaceAccessToken' | 'replaceBackupCodes' | 'security' | 'setAccessTokenConfirmed'
  | 'setBackupCodesSaved' | 'setDisableCode' | 'setDisableConfirmed' | 'setPasskeyCode'
  | 'setRegenerateCode' | 'setRegenerateConfirmed' | 'setTwoFactorCode' | 't'
  | 'turnOffTwoFactor' | 'twoFactorCode' | 'twoFactorSetup'
>;

export function ProfileSecurityPanel({
  accessToken, accessTokenConfirmed, backupCodes, backupCodesSaved,
  beginTwoFactorSetup, busy, cancelTwoFactorSetup, completeTwoFactorSetup,
  copyAccessToken, disableCode, disableConfirmed, formattedDate,
  hideAccessToken, hideBackupCodes, loadSecurity, passkeyCode,
  regenerateCode, regenerateConfirmed, registerPasskey, removePasskey,
  replaceAccessToken, replaceBackupCodes, security, setAccessTokenConfirmed,
  setBackupCodesSaved, setDisableCode, setDisableConfirmed, setPasskeyCode,
  setRegenerateCode, setRegenerateConfirmed, setTwoFactorCode, t,
  turnOffTwoFactor, twoFactorCode, twoFactorSetup,
}: ProfileSecurityPanelProps) {
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
}
