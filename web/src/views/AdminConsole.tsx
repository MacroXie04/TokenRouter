import { useCallback, useEffect, useState } from 'react';
import { api, getData, postData, type User } from '../api';
import { SETTINGS_GROUPS } from '../lib/settings-groups';

interface Channel {
  id: number;
  name: string;
  type: number;
  status: number;
  models: string;
  group: string;
  base_url: string;
}

interface DashboardStats {
  user_count: number;
  token_count: number;
  channel_count: number;
  request_count: number;
}

interface Option {
  key: string;
  value: string;
}

interface LogRow {
  id: number;
  username: string;
  model_name: string;
  quota: number;
  created_at: number;
}

interface Ability {
  group: string;
  model: string;
  channel_id: number;
  enabled: boolean;
  weight: number;
}

interface Plan {
  id: number;
  title: string;
  price_amount: string;
  total_amount: number;
  duration_unit: string;
  duration_value: number;
  enabled: boolean;
}

interface TokenRow {
  id: number;
  user_id: number;
  name: string;
  key: string;
  status: number;
  remain_quota: number;
  unlimited_quota: boolean;
}

interface ModelRow {
  id: number;
  model_name: string;
  description: string;
  tags: string;
}

interface Instance {
  node_name: string;
  started_at: number;
  last_seen_at: number;
}

type Tab = 'overview' | 'channels' | 'abilities' | 'users' | 'tokens' | 'models' | 'plans' | 'options' | 'logs' | 'redemptions';

export function AdminConsole({ onLogout }: { onLogout: () => void }) {
  const [tab, setTab] = useState<Tab>('overview');
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [options, setOptions] = useState<Option[]>([]);
  const [logs, setLogs] = useState<LogRow[]>([]);
  const [abilities, setAbilities] = useState<Ability[]>([]);
  const [error, setError] = useState('');

  const [chName, setChName] = useState('');
  const [chType, setChType] = useState(1);
  const [chKey, setChKey] = useState('');
  const [chBase, setChBase] = useState('');
  const [chModels, setChModels] = useState('');
  const [chGroup, setChGroup] = useState('default');

  const [abGroup, setAbGroup] = useState('default');
  const [abModel, setAbModel] = useState('');
  const [abChannel, setAbChannel] = useState(0);

  const [plans, setPlans] = useState<Plan[]>([]);
  const [planTitle, setPlanTitle] = useState('');
  const [planPrice, setPlanPrice] = useState('10');
  const [planQuota, setPlanQuota] = useState(1000000);

  const [tokens, setTokens] = useState<TokenRow[]>([]);
  const [models, setModels] = useState<ModelRow[]>([]);
  const [instances, setInstances] = useState<Instance[]>([]);

  const [redName, setRedName] = useState('');
  const [redQuota, setRedQuota] = useState(500);

  // Editable site-name option (removed; grouped editor supersedes it).


  const refresh = useCallback(async () => {
    try {
      const [s, c, u, o, l, a, pl, tk, md, ins] = await Promise.all([
        getData<DashboardStats>('/data'),
        getData<{ items: Channel[] }>('/channel').then((r) => r.items),
        getData<{ items: User[] }>('/user').then((r) => r.items),
        getData<Option[]>('/option'),
        getData<{ items: LogRow[] }>('/log', { page: 1, page_size: 50 }).then((r) => r.items),
        getData<Ability[]>('/ability'),
        getData<Plan[]>('/subscription/plans'),
        getData<{ items: TokenRow[] }>('/token').then((r) => r.items),
        getData<ModelRow[]>('/models'),
        getData<Instance[]>('/instance'),
      ]);
      setStats(s);
      setChannels(c);
      setUsers(u);
      setOptions(o);
      setLogs(l);
      setAbilities(a);
      setPlans(pl);
      setTokens(tk);
      setModels(md);
      setInstances(ins);
    } catch {
      /* ignore */
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function createChannel(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/channel', { name: chName, type: chType, key: chKey, base_url: chBase, models: chModels, group: chGroup });
      setChName(''); setChKey(''); setChBase(''); setChModels('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建失败');
    }
  }

  async function deleteChannel(id: number) {
    try {
      await api.delete(`/channel/${id}`);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败');
    }
  }

  async function createAbility(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/ability', { group: abGroup, model: abModel, channel_id: abChannel, weight: 1 });
      setAbModel('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建失败');
    }
  }

  async function createPlan(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/subscription/plan', {
        title: planTitle,
        price_amount: planPrice,
        total_amount: planQuota,
        duration_unit: 'month',
        duration_value: 1,
        enabled: true,
      });
      setPlanTitle('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建失败');
    }
  }

  async function createRedemption(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await postData('/redemption', { name: redName, quota: redQuota });
      setRedName('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建失败');
    }
  }

  return (
    <main className="app">
      <header className="header row">
        <div>
          <h1>TokenRouter Admin</h1>
          <p className="tagline">Manage channels, users, and settings.</p>
        </div>
        <button className="link" onClick={onLogout}>Sign out</button>
      </header>

      <nav className="tabs">
        {(['overview', 'channels', 'abilities', 'users', 'tokens', 'models', 'plans', 'options', 'logs', 'redemptions'] as Tab[]).map((t) => (
          <button key={t} className={tab === t ? 'tab active' : 'tab'} onClick={() => setTab(t)}>
            {t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
      </nav>

      {error && <p className="error">{error}</p>}

      {tab === 'overview' && (
        <section className="card">
          <h2>Overview</h2>
          <dl className="kv">
            <dt>Users</dt><dd>{stats?.user_count ?? 0}</dd>
            <dt>API keys</dt><dd>{stats?.token_count ?? 0}</dd>
            <dt>Channels</dt><dd>{stats?.channel_count ?? 0}</dd>
            <dt>Requests</dt><dd>{stats?.request_count ?? 0}</dd>
          </dl>
          {instances.length > 0 && (
            <>
              <h3 style={{ fontSize: '0.95rem', margin: '1rem 0 0.4rem' }}>Nodes</h3>
              <ul className="key-list">
                {instances.map((i) => (
                  <li key={i.node_name}>
                    <strong>{i.node_name}</strong>
                    <span className="muted">seen {new Date(i.last_seen_at * 1000).toLocaleString()}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
      )}

      {tab === 'channels' && (
        <section className="card">
          <h2>Channels</h2>
          <form className="grid-form" onSubmit={createChannel}>
            <label>Name<input value={chName} onChange={(e) => setChName(e.target.value)} required /></label>
            <label>Type<input type="number" value={chType} onChange={(e) => setChType(Number(e.target.value))} /></label>
            <label>API key<input value={chKey} onChange={(e) => setChKey(e.target.value)} /></label>
            <label>Base URL<input value={chBase} onChange={(e) => setChBase(e.target.value)} placeholder="https://api.openai.com" /></label>
            <label>Models<input value={chModels} onChange={(e) => setChModels(e.target.value)} placeholder="gpt-4,gpt-3.5-turbo" /></label>
            <label>Group<input value={chGroup} onChange={(e) => setChGroup(e.target.value)} /></label>
            <button type="submit">Add channel</button>
          </form>
          <ul className="key-list">
            {channels.map((c) => (
              <li key={c.id}>
                <strong>{c.name}</strong>
                <span className="muted">type {c.type}</span>
                <span className="muted">{c.models}</span>
                <button className="link" onClick={() => deleteChannel(c.id)}>Delete</button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'abilities' && (
        <section className="card">
          <h2>Routing abilities</h2>
          <form className="grid-form" onSubmit={createAbility}>
            <label>Group<input value={abGroup} onChange={(e) => setAbGroup(e.target.value)} /></label>
            <label>Model<input value={abModel} onChange={(e) => setAbModel(e.target.value)} required /></label>
            <label>Channel ID<input type="number" value={abChannel} onChange={(e) => setAbChannel(Number(e.target.value))} required /></label>
            <button type="submit">Add ability</button>
          </form>
          <ul className="key-list">
            {abilities.map((a) => (
              <li key={`${a.group}:${a.model}:${a.channel_id}`}>
                <strong>{a.group} / {a.model}</strong>
                <span className="muted">channel {a.channel_id}</span>
                <span className="muted">weight {a.weight}</span>
                <span className="muted">{a.enabled ? 'enabled' : 'disabled'}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'users' && (
        <section className="card">
          <h2>Users</h2>
          <ul className="key-list">
            {users.map((u) => (
              <li key={u.id}>
                <strong>{u.username}</strong>
                <span className="muted">role {u.role}</span>
                <span className="muted">quota {u.quota}</span>
                <span className="muted">group {u.group}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'tokens' && (
        <section className="card">
          <h2>API keys</h2>
          <ul className="key-list">
            {tokens.map((tk) => (
              <li key={tk.id}>
                <strong>{tk.name}</strong>
                <code>{tk.key.slice(0, 8)}…</code>
                <span className="muted">user {tk.user_id}</span>
                <span className="muted">{tk.unlimited_quota ? 'unlimited' : `remaining ${tk.remain_quota}`}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'models' && (
        <section className="card">
          <h2>Model registry</h2>
          {models.length === 0 ? (
            <p className="muted">No model metadata yet.</p>
          ) : (
            <ul className="key-list">
              {models.map((m) => (
                <li key={m.id}>
                  <strong>{m.model_name}</strong>
                  <span className="muted">{m.tags}</span>
                  <span className="muted">{m.description?.slice(0, 60)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}

      {tab === 'plans' && (
        <section className="card">
          <h2>Subscription plans</h2>
          <form className="grid-form" onSubmit={createPlan}>
            <label>Title<input value={planTitle} onChange={(e) => setPlanTitle(e.target.value)} required /></label>
            <label>Price (USD)<input value={planPrice} onChange={(e) => setPlanPrice(e.target.value)} /></label>
            <label>Quota<input type="number" value={planQuota} onChange={(e) => setPlanQuota(Number(e.target.value))} /></label>
            <button type="submit">Add plan</button>
          </form>
          <ul className="key-list">
            {plans.map((p) => (
              <li key={p.id}>
                <strong>{p.title}</strong>
                <span className="muted">${p.price_amount}</span>
                <span className="muted">+{p.total_amount} quota</span>
                <span className="muted">{p.duration_value} {p.duration_unit}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'options' && (
        <section className="card">
          <h2>Settings</h2>
          <NewOptionForm onSaved={refresh} />
          {SETTINGS_GROUPS.map((g) => (
            <div key={g.name} style={{ marginBottom: '0.9rem' }}>
              <h3 style={{ fontSize: '0.95rem', margin: '0 0 0.4rem' }}>{g.name}</h3>
              {g.settings.map((s) => {
                const opt = options.find((o) => o.key === s.key);
                return <OptionRow key={s.key} option={opt ?? { key: s.key, value: '' }} label={s.label} onSaved={refresh} />;
              })}
            </div>
          ))}
          <h3 style={{ fontSize: '0.95rem', margin: '0 0 0.4rem' }}>Other</h3>
          {options
            .filter((o) => !SETTINGS_GROUPS.some((g) => g.settings.some((s) => s.key === o.key)))
            .map((o) => <OptionRow key={o.key} option={o} onSaved={refresh} />)}
        </section>
      )}

      {tab === 'logs' && (
        <section className="card">
          <h2>Recent logs</h2>
          <ul className="key-list">
            {logs.map((l) => (
              <li key={l.id}>
                <strong>{l.username || '—'}</strong>
                <span className="muted">{l.model_name}</span>
                <span className="muted">quota {l.quota}</span>
                <span className="muted">{new Date(l.created_at * 1000).toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'redemptions' && (
        <section className="card">
          <h2>Redemption codes</h2>
          <form className="inline-form" onSubmit={createRedemption}>
            <label>Name<input value={redName} onChange={(e) => setRedName(e.target.value)} required /></label>
            <label>Quota<input type="number" value={redQuota} onChange={(e) => setRedQuota(Number(e.target.value))} /></label>
            <button type="submit">Create</button>
          </form>
        </section>
      )}
    </main>
  );
}

function OptionRow({ option, onSaved, label }: { option: Option; onSaved: () => void; label?: string }) {
  const [value, setValue] = useState(option.value);
  async function save() {
    try {
      await api.put('/option', { [option.key]: value });
      onSaved();
    } catch {
      /* ignore */
    }
  }
  return (
    <div className="option-row">
      <strong>{label ?? option.key}</strong>
      <input value={value} onChange={(e) => setValue(e.target.value)} />
      <button className="link" onClick={save}>Save</button>
    </div>
  );
}

function NewOptionForm({ onSaved }: { onSaved: () => void }) {
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.put('/option', { [key]: value });
      setKey(''); setValue('');
      onSaved();
    } catch {
      /* ignore */
    }
  }
  return (
    <form className="inline-form" onSubmit={submit} style={{ marginBottom: '0.75rem' }}>
      <label>Key<input value={key} onChange={(e) => setKey(e.target.value)} required /></label>
      <label>Value<input value={value} onChange={(e) => setValue(e.target.value)} /></label>
      <button type="submit">Add</button>
    </form>
  );
}
