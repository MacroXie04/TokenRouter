import { useTranslation } from 'react-i18next';
import type { LoginSession } from './profile-api';
import { compactIdentifier, type Resource } from './profile-view-model';
import { EmptyOrFailure } from './ProfileResourceState';

export function ProfileSessionsPanel({
  sessions, busy, formattedDate, onRetry, onSignOutSession, onSignOutOthers,
}: {
  sessions: Resource<LoginSession[]>;
  busy: boolean;
  formattedDate: (date: Date) => string;
  onRetry: () => void;
  onSignOutSession: (session: LoginSession) => void;
  onSignOutOthers: () => void;
}) {
  const { t } = useTranslation();
  if (sessions.loading || sessions.error || !sessions.data) {
    return (
      <EmptyOrFailure
        loading={sessions.loading}
        error={sessions.error}
        loadingText={t('Loading login sessions…')}
        errorText={t('Unable to load login sessions.')}
        onRetry={onRetry}
      />
    );
  }
  if (sessions.data.length === 0) return <p className="muted">{t('No active login sessions.')}</p>;
  return (
    <>
      <p className="muted">{t('Your current browser is labeled below. Review each session before signing it out.')}</p>
      <button type="button" onClick={onSignOutOthers} disabled={busy || sessions.data.length < 2}>
        {t('Sign out other sessions')}
      </button>
      <ul className="key-list">
        {sessions.data.map((session) => (
          <li key={session.sid}>
            <strong>{session.userAgent || t('Unknown device')}</strong>
            {session.current ? <span className="muted">{t('Current session')}</span> : null}
            <span className="muted">{session.loginMethod} · {session.ip || t('Unknown IP')}</span>
            <span className="muted">
              {t('Last active {{date}}', { date: formattedDate(new Date(session.lastActiveAt * 1_000)) })}
            </span>
            <span className="muted">{compactIdentifier(session.sid)}</span>
            <button type="button" className="link" onClick={() => onSignOutSession(session)} disabled={busy}>
              {t('Sign out this session')}
            </button>
          </li>
        ))}
      </ul>
    </>
  );
}

export function ProfileBackupCodes({ codes, saved, onSavedChange, onHide }: {
  codes: string[];
  saved: boolean;
  onSavedChange: (saved: boolean) => void;
  onHide: () => void;
}) {
  const { t } = useTranslation();
  if (codes.length === 0) return null;
  return (
    <aside className="user-subpanel" aria-labelledby="profile-backup-codes-title">
      <h3 id="profile-backup-codes-title">{t('Save your backup codes')}</h3>
      <p className="muted">{t('Each code works once. Store them offline; they will not be shown again.')}</p>
      <ol aria-label={t('Backup codes')}>
        {codes.map((code) => <li key={code}><code>{code}</code></li>)}
      </ol>
      <label>
        <input type="checkbox" checked={saved} onChange={(event) => onSavedChange(event.target.checked)} />
        {t('I saved these backup codes in a safe place.')}
      </label>
      <button type="button" onClick={onHide} disabled={!saved}>{t('Hide backup codes')}</button>
    </aside>
  );
}
