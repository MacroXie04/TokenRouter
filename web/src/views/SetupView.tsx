import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { postData } from '../api';

export function SetupView({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await postData('/setup', { username, password });
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('Initialize'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="app app-narrow">
      <header className="header">
        <h1>TokenRouter</h1>
        <p className="tagline">{t('Welcome. Create your root administrator account.')}</p>
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
        <button type="submit" disabled={busy}>{busy ? t('Creating…') : t('Initialize')}</button>
      </form>
    </main>
  );
}
