import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getData, postData, type User } from '../api';

interface CustomOAuthProviderInfo {
  id: number;
  name: string;
  slug: string;
  icon: string;
  client_id: string;
  authorization_endpoint: string;
  scopes: string;
}

interface StatusInfo {
  wechat_login: boolean;
  wechat_qrcode: string;
  custom_oauth_providers?: CustomOAuthProviderInfo[];
}

export function LoginView({ onLoggedIn }: { onLoggedIn: (u: User) => void }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  // WeChat login state.
  const [wechat, setWechat] = useState<StatusInfo | null>(null);
  const [wechatOpen, setWechatOpen] = useState(false);
  const [wechatCode, setWechatCode] = useState('');
  const [wechatBusy, setWechatBusy] = useState(false);
  const [wechatError, setWechatError] = useState('');

  // Custom OAuth providers advertised by /api/status.
  const [customProviders, setCustomProviders] = useState<CustomOAuthProviderInfo[]>([]);

  useEffect(() => {
    getData<StatusInfo>('/status')
      .then((s) => {
        setWechat({ wechat_login: s.wechat_login, wechat_qrcode: s.wechat_qrcode });
        setCustomProviders(s.custom_oauth_providers ?? []);
      })
      .catch(() => {});
  }, []);

  // OAuth callbacks land on /login?error=…(&message=…) after a failure; show
  // the provider's denial message verbatim, or a generic fallback.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const oauthError = params.get('error');
    if (!oauthError) return;
    const message = params.get('message');
    setError(oauthError === 'oauth_access_denied' && message
      ? message
      : t('External sign-in failed. Please try again.'));
    window.history.replaceState(null, '', window.location.pathname);
  }, []);

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

  async function submitWeChat(e: React.FormEvent) {
    e.preventDefault();
    setWechatError('');
    setWechatBusy(true);
    try {
      const res = await getData<{ user: User }>('/oauth/wechat', { code: wechatCode });
      onLoggedIn(res.user);
    } catch (err) {
      setWechatError(err instanceof Error ? err.message : 'Error');
    } finally {
      setWechatBusy(false);
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
        {wechat?.wechat_login && (
          <button className="button" type="button" onClick={() => { setWechatOpen(true); setWechatError(''); }}>
            {t('Continue with WeChat')}
          </button>
        )}
        <a className="button" href="/api/oauth/github">GitHub</a>
        <a className="button" href="/api/oauth/discord">Discord</a>
        <a className="button" href="/api/oauth/oidc">OIDC</a>
        {customProviders.map((p) => (
          <a key={p.slug} className="button" href={`/api/oauth/${encodeURIComponent(p.slug)}`}>
            {p.icon && <img src={p.icon} alt="" style={{ width: '1rem', height: '1rem', verticalAlign: '-0.125rem', marginRight: '0.375rem' }} />}
            {p.name}
          </a>
        ))}
      </div>

      {wechatOpen && (
        <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={t('WeChat login')}>
          <form className="modal" onSubmit={submitWeChat}>
            <h2>{t('WeChat login')}</h2>
            {wechat?.wechat_qrcode && <img src={wechat.wechat_qrcode} alt={t('WeChat login')} />}
            <p className="muted">{t('Scan the QR code with WeChat, then enter the code you receive.')}</p>
            <label>
              {t('Authorization code')}
              <input value={wechatCode} onChange={(e) => setWechatCode(e.target.value)} required autoFocus />
            </label>
            {wechatError && <p className="error">{wechatError}</p>}
            <div className="modal-actions">
              <button type="button" className="link" onClick={() => setWechatOpen(false)}>{t('Close')}</button>
              <button type="submit" disabled={wechatBusy}>{wechatBusy ? t('Please wait…') : t('Authorize')}</button>
            </div>
          </form>
        </div>
      )}
    </main>
  );
}
