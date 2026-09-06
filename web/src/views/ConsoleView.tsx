import { useTranslation } from 'react-i18next';
import { api, type User } from '../api';
import { RelayTokenManager } from '../features/keys/RelayTokenManager';

/**
 * The authenticated router sends only the `/keys` route here. Profile,
 * wallet, subscription, security, session, and playground workflows live in
 * their focused feature views; keeping duplicate implementations here made
 * stale code part of the production bundle and risked accidental divergence.
 */
export function ConsoleView({ user, onLogout }: { user: User | null; onLogout: () => void; keysOnly?: boolean }) {
  const { t } = useTranslation();

  async function logout() {
    try {
      await api.post('/user/auth/logout');
    } finally {
      onLogout();
    }
  }

  return (
    <main className="app">
      <header className="header row">
        <div>
          <h1>{t('TokenRouter Console')}</h1>
          <p className="tagline">{t('Welcome, {{name}}.', { name: user?.display_name ?? user?.username ?? '' })}</p>
        </div>
        <button className="link" onClick={logout}>{t('Sign out')}</button>
      </header>
      <RelayTokenManager />
      <footer className="footer">{t('TokenRouter — independent AI API gateway.')}</footer>
    </main>
  );
}
