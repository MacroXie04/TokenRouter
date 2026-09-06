import type { ProfileViewProps } from './profile-view-props';
import { ProfileConnectionsPanel } from './ProfileConnectionsPanel';
import { ProfileDangerPanel } from './ProfileDangerPanel';
import { ProfileNotificationsPanel } from './ProfileNotificationsPanel';
import { ProfilePreferencesPanel } from './ProfilePreferencesPanel';
import { ProfileSecurityPanel } from './ProfileSecurityPanel';
import { ProfileSessionsPanel } from './ProfileSessionPanels';
import { useProfileController } from './useProfileController';
export type { ProfileViewProps } from './profile-view-props';

export function ProfileView(props: ProfileViewProps) {
  const controller = useProfileController(props);
  const { busy, formattedDate, loadSessions, notice, onNavigate, sessions, signOutOtherSessions, signOutSession, t } = controller;
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
        <ProfilePreferencesPanel {...controller} />
      </section>

      <section className="card" aria-labelledby="profile-notifications-title">
        <h2 id="profile-notifications-title">{t('Notifications and privacy')}</h2>
        <ProfileNotificationsPanel {...controller} />
      </section>

      <section className="card" aria-labelledby="profile-security-title">
        <h2 id="profile-security-title">{t('Account security')}</h2>
        <ProfileSecurityPanel {...controller} />
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
        <ProfileConnectionsPanel {...controller} />
      </section>

      <section className="card" aria-labelledby="profile-danger-title">
        <h2 id="profile-danger-title">{t('Danger zone')}</h2>
        <ProfileDangerPanel {...controller} />
      </section>
    </main>
  );
}
