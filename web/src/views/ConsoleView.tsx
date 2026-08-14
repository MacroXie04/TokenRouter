import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, getData, postData, putData, type Token, type User } from '../api';
import { prepareCreationOptions, serializeCredential } from '../lib/webauthn';

interface Plan {
  id: number;
  title: string;
  subtitle: string;
  total_amount: number;
  price_amount: string;
  duration_unit: string;
  duration_value: number;
}

interface SubscriptionRow {
  subscription: {
    id: number;
    plan_id: number;
    amount_total: number;
    amount_used: number;
    end_time: number;
    status: string;
    allow_wallet_overflow: boolean;
  };
}

interface SubscriptionSelf {
  billing_preference: string;
  subscriptions: SubscriptionRow[];
  all_subscriptions: SubscriptionRow[];
}

const BILLING_PREFERENCES = ['subscription_first', 'wallet_first', 'subscription_only', 'wallet_only'];

interface OAuthBinding {
  provider_id: number;
  provider_name: string;
  provider_slug: string;
  provider_icon: string;
  provider_user_id: string;
}

export function ConsoleView({ user, onLogout }: { user: User | null; onLogout: () => void }) {
  const { t } = useTranslation();
  const [tokens, setTokens] = useState<Token[]>([]);
  const [tokenName, setTokenName] = useState('');
  const [topUpAmount, setTopUpAmount] = useState(1000);
  const [checkinMsg, setCheckinMsg] = useState('');
  const [redeemKey, setRedeemKey] = useState('');
  const [plans, setPlans] = useState<Plan[]>([]);
  const [subSelf, setSubSelf] = useState<SubscriptionSelf | null>(null);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  // 2FA state.
  const [twofaEnabled, setTwofaEnabled] = useState(false);
  const [twofaSecret, setTwofaSecret] = useState('');
  const [twofaURL, setTwofaURL] = useState('');
  const [twofaCode, setTwofaCode] = useState('');

  // Profile state.
  const [profileName, setProfileName] = useState(user?.display_name ?? '');
  const [profileEmail, setProfileEmail] = useState(user?.email ?? '');
  const [profileOld, setProfileOld] = useState('');
  const [profilePass, setProfilePass] = useState('');

  // WeChat bind state.
  const [wechatEnabled, setWechatEnabled] = useState(false);
  const [wechatQrcode, setWechatQrcode] = useState('');
  const [wechatCode, setWechatCode] = useState('');

  // Login sessions.
  const [sessions, setSessions] = useState<{ sid: string; ip: string; user_agent: string; created_at: string; status: string }[]>([]);

  // Passkey state.
  const [passkeyEnabled, setPasskeyEnabled] = useState(false);

  // Custom OAuth bindings.
  const [oauthBindings, setOauthBindings] = useState<OAuthBinding[]>([]);

  // Playground state.
  const [playModel, setPlayModel] = useState('gpt-4');
  const [playMessage, setPlayMessage] = useState('');
  const [playResponse, setPlayResponse] = useState('');
  const [playing, setPlaying] = useState(false);

  const refreshTokens = useCallback(async () => {
    try {
      const page = await getData<{ items: Token[] }>('/token/');
      setTokens(page.items);
    } catch {
      /* ignore */
    }
  }, []);

  const refreshSubscriptions = useCallback(async () => {
    try {
      setSubSelf(await getData<SubscriptionSelf>('/subscription/self'));
    } catch {
      /* ignore */
    }
  }, []);

  useEffect(() => {
    refreshTokens();
    refreshSubscriptions();
    getData<{ plan: Plan }[]>('/subscription/plans')
      .then((rows) => setPlans(rows.map((r) => r.plan)))
      .catch(() => {});
    getData<{ enabled: boolean }>('/user/2fa/status').then((r) => setTwofaEnabled(r.enabled)).catch(() => {});
    getData<{ sid: string; ip: string; user_agent: string; created_at: string; status: string }[]>('/user/sessions').then(setSessions).catch(() => {});
    getData<{ enabled: boolean }>('/user/passkey/status').then((r) => setPasskeyEnabled(r.enabled)).catch(() => {});
    getData<OAuthBinding[]>('/user/oauth/bindings').then(setOauthBindings).catch(() => {});
    getData<{ wechat_login: boolean; wechat_qrcode: string }>('/status')
      .then((s) => { setWechatEnabled(s.wechat_login); setWechatQrcode(s.wechat_qrcode); })
      .catch(() => {});
  }, [refreshTokens, refreshSubscriptions]);

  async function createToken(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData<Token>('/token/', { name: tokenName || 'default' });
      setTokenName('');
      await refreshTokens();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function doCheckin() {
    setError('');
    try {
      const res = await postData<{ reward: number }>('/user/checkin', {});
      setMessage(`${t('Check in today')} ✓ +${res.reward}`);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Error');
    }
  }

  async function doTopUp(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/user/topup', { amount: topUpAmount, payment_method: 'balance' });
      setMessage(t('Top up') + ' ✓');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function doRedeem(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      const res = await postData<{ quota: number }>('/user/redemption/redeem', { key: redeemKey });
      setMessage(`✓ +${res.quota}`);
      setRedeemKey('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function purchasePlan(planId: number) {
    setError('');
    try {
      await postData('/subscription/balance/pay', { plan_id: planId });
      setMessage('订阅成功 ✓');
      await refreshSubscriptions();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function changeBillingPreference(pref: string) {
    setError('');
    try {
      const res = await putData<{ billing_preference: string }>('/subscription/self/preference', { billing_preference: pref });
      setSubSelf((prev) => (prev ? { ...prev, billing_preference: res.billing_preference } : prev));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function start2FA() {
    setError('');
    try {
      const res = await postData<{ secret: string; otpauth_url: string }>('/user/2fa/start', {});
      setTwofaSecret(res.secret);
      setTwofaURL(res.otpauth_url);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function enable2FA(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/user/2fa/enable', { code: twofaCode });
      setTwofaEnabled(true);
      setTwofaSecret(''); setTwofaURL(''); setTwofaCode('');
      setMessage('2FA 已启用');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function disable2FA() {
    setError('');
    try {
      await postData('/user/2fa/disable', { code: twofaCode });
      setTwofaEnabled(false);
      setMessage('2FA 已禁用');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function updateProfile(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      const payload: Record<string, string> = {};
      if (profileName) payload.display_name = profileName;
      if (profileEmail) payload.email = profileEmail;
      if (profilePass) {
        payload.password = profilePass;
        payload.old_password = profileOld;
      }
      await api.put('/user/self', payload);
      setProfilePass(''); setProfileOld('');
      setMessage('Profile updated ✓');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function bindWeChat(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/oauth/wechat/bind', { code: wechatCode });
      setWechatCode('');
      setMessage(t('WeChat bound ✓'));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function unbindOAuth(providerId: number) {
    setError('');
    try {
      await api.delete(`/user/oauth/bindings/${providerId}`);
      setOauthBindings((prev) => prev.filter((b) => b.provider_id !== providerId));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function revokeSession(sid: string) {
    setError('');
    try {
      await api.delete(`/user/sessions/${sid}`);
      setSessions((prev) => prev.filter((s) => s.sid !== sid));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    }
  }

  async function registerPasskey() {
    setError('');
    try {
      const begin = await postData<{ options: { publicKey: any }; flow_token: string }>('/user/passkey/register/begin', {});
      const publicKey = prepareCreationOptions(begin.options.publicKey);
      const cred = await navigator.credentials.create({ publicKey } as any);
      const payload = { flow_token: begin.flow_token, ...serializeCredential(cred) };
      await postData('/user/passkey/register/finish', payload);
      setPasskeyEnabled(true);
      setMessage('Passkey registered ✓');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Registration failed');
    }
  }

  async function runPlayground() {
    if (!tokens[0]) {
      setError('Create an API key first');
      return;
    }
    setPlaying(true);
    setPlayResponse('');
    setError('');
    try {
      const res = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${tokens[0].key}` },
        body: JSON.stringify({ model: playModel, messages: [{ role: 'user', content: playMessage }], stream: true }),
      });
      const reader = res.body?.getReader();
      if (!reader) throw new Error('no stream');
      const decoder = new TextDecoder();
      let buffer = '';
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        for (const line of buffer.split('\n')) {
          if (!line.startsWith('data: ')) continue;
          const data = line.slice(6).trim();
          if (data === '[DONE]' || data === '') continue;
          try {
            const json = JSON.parse(data);
            const delta = json.choices?.[0]?.delta?.content ?? '';
            if (delta) setPlayResponse((prev) => prev + delta);
          } catch {
            /* ignore malformed chunk */
          }
        }
        buffer = buffer.slice(buffer.lastIndexOf('\n') + 1);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Error');
    } finally {
      setPlaying(false);
    }
  }

  async function logout() {
    try {
      await api.post('/user/auth/logout');
    } finally {
      onLogout();
    }
  }

  const remaining = (user?.quota ?? 0) - (user?.used_quota ?? 0);

  return (
    <main className="app">
      <header className="header row">
        <div>
          <h1>{t('TokenRouter Console')}</h1>
          <p className="tagline">{t('Welcome, {{name}}.', { name: user?.display_name ?? user?.username })}</p>
        </div>
        <button className="link" onClick={logout}>{t('Sign out')}</button>
      </header>

      <section className="card">
        <h2>Profile</h2>
        <form className="grid-form" onSubmit={updateProfile}>
          <label>Display name<input value={profileName} onChange={(e) => setProfileName(e.target.value)} /></label>
          <label>Email<input value={profileEmail} onChange={(e) => setProfileEmail(e.target.value)} /></label>
          <label>Old password<input type="password" value={profileOld} onChange={(e) => setProfileOld(e.target.value)} /></label>
          <label>New password<input type="password" value={profilePass} onChange={(e) => setProfilePass(e.target.value)} /></label>
          <button type="submit">Save</button>
        </form>
        {wechatEnabled && (
          <form className="inline-form" onSubmit={bindWeChat} style={{ marginTop: '0.75rem' }}>
            {wechatQrcode && <img src={wechatQrcode} alt={t('Bind WeChat')} style={{ width: '7rem', height: '7rem', borderRadius: 8 }} />}
            <label>
              {t('Bind WeChat')}
              <input value={wechatCode} onChange={(e) => setWechatCode(e.target.value)} placeholder={t('Authorization code')} required />
            </label>
            <button type="submit">{t('Bind')}</button>
          </form>
        )}
      </section>

      <section className="card">
        <h2>{t('Quota')}</h2>
        <dl className="kv">
          <dt>{t('Total')}</dt><dd>{user?.quota ?? 0}</dd>
          <dt>{t('Used')}</dt><dd>{user?.used_quota ?? 0}</dd>
          <dt>{t('Remaining')}</dt><dd>{remaining}</dd>
          <dt>{t('Requests')}</dt><dd>{user?.request_count ?? 0}</dd>
          <dt>{t('Group')}</dt><dd>{user?.group ?? 'default'}</dd>
        </dl>
      </section>

      <section className="card">
        <h2>{t('Wallet')}</h2>
        <form className="inline-form" onSubmit={doTopUp}>
          <label>
            {t('Amount')}
            <input type="number" value={topUpAmount} onChange={(e) => setTopUpAmount(Number(e.target.value))} min={1} />
          </label>
          <button type="submit">{t('Top up')}</button>
        </form>
        <button className="link" onClick={doCheckin}>{t('Check in today')}</button>
        <form className="inline-form" onSubmit={doRedeem} style={{ marginTop: '0.75rem' }}>
          <label>
            Redeem code
            <input value={redeemKey} onChange={(e) => setRedeemKey(e.target.value)} placeholder="code" />
          </label>
          <button type="submit">Redeem</button>
        </form>
        {message && <p className="muted">{message}</p>}
      </section>

      {(plans.length > 0 || (subSelf?.subscriptions.length ?? 0) > 0) && (
        <section className="card">
          <h2>Subscriptions</h2>
          {(subSelf?.subscriptions.length ?? 0) > 0 && (
            <ul className="key-list">
              {subSelf!.subscriptions.map((row) => (
                <li key={row.subscription.id}>
                  <strong>#{row.subscription.id}</strong>
                  <span className="muted">
                    {row.subscription.amount_total > 0
                      ? `${row.subscription.amount_used} / ${row.subscription.amount_total} quota`
                      : 'unlimited quota'}
                  </span>
                  <span className="muted">until {new Date(row.subscription.end_time * 1000).toLocaleDateString()}</span>
                </li>
              ))}
            </ul>
          )}
          {subSelf && (
            <label>
              Billing preference
              <select
                value={subSelf.billing_preference}
                onChange={(e) => changeBillingPreference(e.target.value)}
              >
                {BILLING_PREFERENCES.map((p) => (
                  <option key={p} value={p}>{p}</option>
                ))}
              </select>
            </label>
          )}
          <ul className="key-list">
            {plans.map((p) => (
              <li key={p.id}>
                <strong>{p.title}</strong>
                <span className="muted">{p.price_amount}</span>
                <span className="muted">+{p.total_amount} quota</span>
                <button className="link" onClick={() => purchasePlan(p.id)}>Subscribe</button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section className="card">
        <h2>Security</h2>
        <p className="muted">Two-factor authentication: {twofaEnabled ? 'enabled' : 'disabled'}</p>
        {!twofaEnabled && !twofaSecret && (
          <button className="link" onClick={start2FA}>Enable 2FA</button>
        )}
        {twofaSecret && (
          <div>
            <p className="muted">Secret: <code>{twofaSecret}</code></p>
            {twofaURL && <p className="muted">Scan or enter this in your authenticator app.</p>}
            <form className="inline-form" onSubmit={enable2FA}>
              <label>
                Code
                <input value={twofaCode} onChange={(e) => setTwofaCode(e.target.value)} required />
              </label>
              <button type="submit">Enable</button>
            </form>
          </div>
        )}
        {twofaEnabled && (
          <form className="inline-form" onSubmit={(e) => { e.preventDefault(); disable2FA(); }}>
            <label>
              Code
              <input value={twofaCode} onChange={(e) => setTwofaCode(e.target.value)} required />
            </label>
            <button type="submit">Disable 2FA</button>
          </form>
        )}
        <p className="muted" style={{ marginTop: '0.75rem' }}>
          Passkey: {passkeyEnabled ? 'registered' : 'not registered'}
        </p>
        {!passkeyEnabled && <button className="link" onClick={registerPasskey}>Register passkey</button>}
      </section>

      {oauthBindings.length > 0 && (
        <section className="card">
          <h2>{t('Linked accounts')}</h2>
          <ul className="key-list">
            {oauthBindings.map((b) => (
              <li key={b.provider_id}>
                {b.provider_icon && <img src={b.provider_icon} alt="" style={{ width: '1rem', height: '1rem' }} />}
                <strong>{b.provider_name}</strong>
                <span className="muted">{b.provider_user_id}</span>
                <button className="link" onClick={() => unbindOAuth(b.provider_id)}>{t('Unbind')}</button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section className="card">
        <h2>Login sessions</h2>
        <ul className="key-list">
          {sessions.map((s) => (
            <li key={s.sid}>
              <strong>{s.ip || '—'}</strong>
              <span className="muted">{s.user_agent?.slice(0, 40)}</span>
              <span className="muted">{s.status}</span>
              {s.status === 'active' && <button className="link" onClick={() => revokeSession(s.sid)}>Revoke</button>}
            </li>
          ))}
        </ul>
      </section>

      <section className="card">
        <h2>Playground</h2>
        <form className="inline-form" onSubmit={(e) => { e.preventDefault(); runPlayground(); }}>
          <label>
            Model
            <input value={playModel} onChange={(e) => setPlayModel(e.target.value)} />
          </label>
          <label>
            Message
            <input value={playMessage} onChange={(e) => setPlayMessage(e.target.value)} required />
          </label>
          <button type="submit" disabled={playing}>{playing ? '…' : 'Send'}</button>
        </form>
        {playResponse && <pre className="play-response">{playResponse}</pre>}
      </section>

      <section className="card">
        <h2>{t('API keys')}</h2>
        <form className="inline-form" onSubmit={createToken}>
          <label>
            {t('Name')}
            <input value={tokenName} onChange={(e) => setTokenName(e.target.value)} placeholder="key name" />
          </label>
          <button type="submit">{t('Create key')}</button>
        </form>
        {error && <p className="error">{error}</p>}
        {tokens.length === 0 ? (
          <p className="muted">{t('No API keys yet.')}</p>
        ) : (
          <ul className="key-list">
            {tokens.map((tk) => (
              <li key={tk.id}>
                <code>{tk.key.slice(0, 8)}…</code>
                <span className="muted">{tk.name}</span>
                <span className="muted">{tk.unlimited_quota ? t('unlimited') : t('remaining {{quota}}', { quota: tk.remain_quota })}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <footer className="footer">TokenRouter — independent AI API gateway.</footer>
    </main>
  );
}
