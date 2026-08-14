import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type User } from './api';
import { supportedLanguages } from './i18n';
import { HomeView } from './views/HomeView';
import { SetupView } from './views/SetupView';
import { LoginView } from './views/LoginView';
import { ConsoleView } from './views/ConsoleView';
import { AdminConsole } from './views/AdminConsole';

type View = 'loading' | 'setup' | 'home' | 'login' | 'console';

function LanguageSwitcher() {
  const { i18n } = useTranslation();
  return (
    <select
      className="lang"
      value={i18n.language}
      onChange={(e) => i18n.changeLanguage(e.target.value)}
      aria-label="Language"
    >
      {supportedLanguages.map((l) => (
        <option key={l.code} value={l.code}>{l.label}</option>
      ))}
    </select>
  );
}

export default function App() {
  const [view, setView] = useState<View>('loading');
  const [user, setUser] = useState<User | null>(null);
  const { t } = useTranslation();

  useEffect(() => {
    (async () => {
      try {
        const setup = await api.get('/setup');
        if (setup.data?.data?.setup_required) {
          setView('setup');
          return;
        }
        const self = await api.get('/user/self');
        if (self.data?.success) {
          setUser(self.data.data);
          setView('console');
          return;
        }
        setView('home');
      } catch {
        setView('login');
      }
    })();
  }, []);

  if (view === 'loading') {
    return (
      <main className="app">
        <div className="langbar"><LanguageSwitcher /></div>
        <p className="muted">{t('Loading TokenRouter…')}</p>
      </main>
    );
  }
  if (view === 'setup') {
    return (
      <>
        <div className="langbar"><LanguageSwitcher /></div>
        <SetupView onDone={() => setView('login')} />
      </>
    );
  }
  if (view === 'login') {
    return (
      <>
        <div className="langbar"><LanguageSwitcher /></div>
        <LoginView onLoggedIn={(u) => { setUser(u); setView('console'); }} />
      </>
    );
  }
  if (view === 'home') {
    return (
      <>
        <div className="langbar"><LanguageSwitcher /></div>
        <HomeView onSignIn={() => setView('login')} />
      </>
    );
  }
  const isAdmin = (user?.role ?? 0) >= 10;
  return (
    <>
      <div className="langbar"><LanguageSwitcher /></div>
      {isAdmin
        ? <AdminConsole user={user!} onLogout={() => { setUser(null); setView('login'); }} />
        : <ConsoleView user={user} onLogout={() => { setUser(null); setView('login'); }} />}
    </>
  );
}
