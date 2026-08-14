import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { postData, type User } from '../api';

export function LoginView({ onLoggedIn }: { onLoggedIn: (u: User) => void }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      if (mode === 'register') {
        await postData('/user/register', { username, password });
        setMode('login');
        setPassword('');
        return;
      }
      const res = await postData<User>('/user/login', { username, password });
      onLoggedIn(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="app app-narrow">
      <header className="header">
        <h1>TokenRouter</h1>
        <p className="tagline">{mode === 'login' ? t('Sign in to your console.') : t('Create an account.')}</p>
      </header>
      <form className="card" onSubmit={submit}>
        <label>
          {t('Username')}
          <input value={username} onChange={(e) => setUsername(e.target.value)} required minLength={3} autoFocus />
        </label>
        <label>
          {t('Password')}
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={8} />
        </label>
        {error && <p className="error">{error}</p>}
        <button type="submit" disabled={busy}>{busy ? t('Please wait…') : mode === 'login' ? t('Sign in') : t('Register')}</button>
        <button type="button" className="link" onClick={() => setMode(mode === 'login' ? 'register' : 'login')}>
          {mode === 'login' ? t('No account? Register') : t('Have an account? Sign in')}
        </button>
      </form>
      <div className="oauth-buttons">
        <a className="button" href="/api/oauth/github">GitHub</a>
        <a className="button" href="/api/oauth/discord">Discord</a>
        <a className="button" href="/api/oauth/oidc">OIDC</a>
      </div>
    </main>
  );
}
