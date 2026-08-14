import { useCallback, useEffect, useState } from 'react';
import { api, getData, postData, putData, type ApiResponse, type User } from '../api';
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

interface PaymentComplianceStatus {
  confirmed: boolean;
  terms_version: string;
  confirmed_at: number;
  confirmed_by: number;
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

interface AffinityCacheStats {
  enabled: boolean;
  total: number;
  unknown: number;
  by_rule_name: Record<string, number>;
  cache_capacity: number;
  cache_algo: string;
}

interface PrefillGroup {
  id: number;
  name: string;
  type: string;
  items: unknown;
  description?: string;
  created_time: number;
  updated_time: number;
}

type Tab = 'overview' | 'channels' | 'abilities' | 'users' | 'tokens' | 'models' | 'prefill' | 'plans' | 'options' | 'logs' | 'redemptions';

export function AdminConsole({ user, onLogout }: { user: User; onLogout: () => void }) {
  const isRoot = user.role >= 100;
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
        getData<DashboardStats>('/dashboard/stats'),
        getData<{ items: Channel[] }>('/channel').then((r) => r.items),
        getData<{ items: User[] }>('/user').then((r) => r.items),
        isRoot ? getData<Option[]>('/option/') : Promise.resolve([] as Option[]),
        getData<{ items: LogRow[] }>('/log', { page: 1, page_size: 50 }).then((r) => r.items),
        getData<Ability[]>('/ability'),
        getData<{ plan: Plan }[]>('/subscription/admin/plans').then((rows) => rows.map((r) => r.plan)),
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
  }, [isRoot]);

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
        {(['overview', 'channels', 'abilities', 'users', 'tokens', 'models', 'prefill', 'plans', 'options', 'logs', 'redemptions'] as Tab[])
          .filter((t) => t !== 'options' || isRoot)
          .map((t) => (
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

      {tab === 'prefill' && <PrefillGroupPanel />}

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

      {tab === 'options' && isRoot && (
        <section className="card">
          <h2>Settings</h2>
          <PaymentCompliancePanel options={options} onConfirmed={refresh} />
          <PricingResetPanel onReset={refresh} />
          <AffinityCachePanel />
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
            .filter((o) => !o.key.startsWith('payment_setting.compliance_'))
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

function PrefillGroupPanel() {
  const [groups, setGroups] = useState<PrefillGroup[]>([]);
  const [filter, setFilter] = useState('');
  const [editingId, setEditingId] = useState<number | null>(null);
  const [name, setName] = useState('');
  const [type, setType] = useState('model');
  const [items, setItems] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');

  const refreshGroups = useCallback(async () => {
    try {
      setGroups(await getData<PrefillGroup[]>('/prefill_group/', filter ? { type: filter } : undefined));
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Could not load prefill groups');
    }
  }, [filter]);

  useEffect(() => { void refreshGroups(); }, [refreshGroups]);

  function resetForm() {
    setEditingId(null);
    setName('');
    setType('model');
    setItems('');
    setDescription('');
  }

  function editGroup(group: PrefillGroup) {
    setEditingId(group.id);
    setName(group.name);
    setType(group.type);
    setDescription(group.description ?? '');
    if (typeof group.items === 'string') setItems(group.items);
    else if (Array.isArray(group.items)) setItems(group.items.join('\n'));
    else setItems(JSON.stringify(group.items ?? {}, null, 2));
    setMessage('');
  }

  async function saveGroup(event: React.FormEvent) {
    event.preventDefault();
    if (busy) return;
    let payloadItems: string | string[];
    if (type === 'endpoint') {
      try {
        JSON.parse(items || '{}');
      } catch {
        setMessage('Endpoint items must contain valid JSON.');
        return;
      }
      payloadItems = items || '{}';
    } else {
      payloadItems = items.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
    }
    setBusy(true);
    setMessage('');
    try {
      const payload = { id: editingId ?? undefined, name, type, items: payloadItems, description };
      if (editingId) await putData<PrefillGroup>('/prefill_group/', payload);
      else await postData<PrefillGroup>('/prefill_group/', payload);
      setMessage(editingId ? 'Prefill group updated.' : 'Prefill group created.');
      resetForm();
      await refreshGroups();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Could not save prefill group');
    } finally {
      setBusy(false);
    }
  }

  async function deleteGroup(group: PrefillGroup) {
    if (busy || !window.confirm(`Delete prefill group “${group.name}”?`)) return;
    setBusy(true);
    setMessage('');
    try {
      const response = await api.delete<ApiResponse<null>>(`/prefill_group/${group.id}`);
      if (!response.data.success) throw new Error(response.data.message || 'Delete failed');
      if (editingId === group.id) resetForm();
      setMessage('Prefill group deleted.');
      await refreshGroups();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Could not delete prefill group');
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card">
      <h2>Prefill groups</h2>
      <p className="muted">Manage reusable model, tag, and endpoint sets used by channel forms.</p>
      <label>
        Filter by type
        <select value={filter} onChange={(event) => setFilter(event.target.value)}>
          <option value="">All types</option>
          <option value="model">Model</option>
          <option value="tag">Tag</option>
          <option value="endpoint">Endpoint</option>
        </select>
      </label>
      <form className="grid-form" onSubmit={saveGroup}>
        <label>Name<input value={name} maxLength={64} onChange={(event) => setName(event.target.value)} required /></label>
        <label>Type
          <select value={type} onChange={(event) => setType(event.target.value)}>
            <option value="model">Model</option>
            <option value="tag">Tag</option>
            <option value="endpoint">Endpoint</option>
          </select>
        </label>
        <label>Description<input value={description} maxLength={255} onChange={(event) => setDescription(event.target.value)} /></label>
        <label>
          {type === 'endpoint' ? 'Endpoint JSON' : 'Items (one per line or comma-separated)'}
          <textarea rows={6} value={items} onChange={(event) => setItems(event.target.value)} />
        </label>
        <div className="inline-form">
          <button type="submit" disabled={busy}>{busy ? 'Saving…' : editingId ? 'Update group' : 'Create group'}</button>
          {editingId && <button type="button" className="link" disabled={busy} onClick={resetForm}>Cancel edit</button>}
        </div>
      </form>
      {message && <p className={message.includes('created') || message.includes('updated') || message.includes('deleted') ? 'success' : 'error'}>{message}</p>}
      {groups.length === 0 ? <p className="muted">No prefill groups.</p> : (
        <ul className="key-list">
          {groups.map((group) => (
            <li key={group.id}>
              <strong>{group.name}</strong>
              <span className="muted">{group.type}</span>
              <span className="muted">{group.description || 'No description'}</span>
              <button type="button" className="link" disabled={busy} onClick={() => editGroup(group)}>Edit</button>
              <button type="button" className="link" disabled={busy} onClick={() => void deleteGroup(group)}>Delete</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function PricingResetPanel({ onReset }: { onReset: () => Promise<void> | void }) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');

  async function resetPricing() {
    if (busy || !window.confirm('Reset all model prices to the built-in TokenRouter defaults?')) return;
    setBusy(true);
    setMessage('');
    try {
      await postData<void>('/option/rest_model_ratio');
      setMessage('Model pricing defaults restored.');
      await onReset();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Pricing reset failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: '0.65rem', padding: '0.8rem', marginBottom: '1rem' }}>
      <h3 style={{ margin: '0 0 0.4rem' }}>Model pricing</h3>
      <p className="muted">Restore the built-in USD-per-million model prices and apply them to live billing immediately.</p>
      <button type="button" disabled={busy} onClick={resetPricing}>{busy ? 'Resetting…' : 'Reset model pricing'}</button>
      {message && <p className={message.startsWith('Model pricing') ? 'success' : 'error'}>{message}</p>}
    </div>
  );
}

function AffinityCachePanel() {
  const [stats, setStats] = useState<AffinityCacheStats | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');

  const refreshStats = useCallback(async () => {
    try {
      setStats(await getData<AffinityCacheStats>('/option/channel_affinity_cache'));
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Could not load affinity cache');
    }
  }, []);

  useEffect(() => { void refreshStats(); }, [refreshStats]);

  async function clearCache(ruleName?: string) {
    const target = ruleName ? `entries for rule “${ruleName}”` : 'all channel-affinity entries';
    if (busy || !window.confirm(`Clear ${target}?`)) return;
    setBusy(true);
    setMessage('');
    try {
      const response = await api.delete<ApiResponse<{ deleted: number }>>('/option/channel_affinity_cache', {
        params: ruleName ? { rule_name: ruleName } : { all: true },
      });
      if (!response.data.success) throw new Error(response.data.message || 'Cache clear failed');
      setMessage(`Cleared ${response.data.data.deleted} affinity cache entries.`);
      await refreshStats();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Cache clear failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: '0.65rem', padding: '0.8rem', marginBottom: '1rem' }}>
      <h3 style={{ margin: '0 0 0.4rem' }}>Channel affinity cache</h3>
      <p className="muted">Inspect live rule-based routing entries. Cache keys use fingerprints; raw affinity values are not exposed.</p>
      {stats && (
        <>
          <p className="muted">
            {stats.enabled ? 'Enabled' : 'Disabled'} · {stats.total} / {stats.cache_capacity} entries · {stats.cache_algo}
            {stats.unknown > 0 ? ` · ${stats.unknown} unknown` : ''}
          </p>
          {Object.entries(stats.by_rule_name).map(([rule, count]) => (
            <div key={rule} className="inline-form" style={{ marginBottom: '0.35rem' }}>
              <span>{rule}: {count}</span>
              <button type="button" disabled={busy || count === 0} onClick={() => void clearCache(rule)}>Clear rule</button>
            </div>
          ))}
        </>
      )}
      <button type="button" disabled={busy || !stats || stats.total === 0} onClick={() => void clearCache()}>
        {busy ? 'Clearing…' : 'Clear all affinity entries'}
      </button>
      {message && <p className={message.startsWith('Cleared') ? 'success' : 'error'}>{message}</p>}
    </div>
  );
}

function PaymentCompliancePanel({ options, onConfirmed }: { options: Option[]; onConfirmed: () => Promise<void> | void }) {
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const confirmed = options.find((o) => o.key === 'payment_setting.compliance_confirmed')?.value === 'true';
  const termsVersion = options.find((o) => o.key === 'payment_setting.compliance_terms_version')?.value ?? '';
  const confirmedAt = Number(options.find((o) => o.key === 'payment_setting.compliance_confirmed_at')?.value ?? 0);
  const current = confirmed && termsVersion === 'v1';

  async function confirmCompliance() {
    if (!accepted || busy) return;
    setBusy(true);
    setMessage('');
    try {
      const status = await postData<PaymentComplianceStatus>('/option/payment_compliance', { confirmed: true });
      setMessage(`Confirmed terms ${status.terms_version}.`);
      setAccepted(false);
      await onConfirmed();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Confirmation failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: '0.65rem', padding: '0.8rem', marginBottom: '1rem' }}>
      <h3 style={{ margin: '0 0 0.4rem' }}>Payment compliance</h3>
      <p className="muted">
        {current
          ? `Current terms ${termsVersion} confirmed${confirmedAt ? ` on ${new Date(confirmedAt * 1000).toLocaleString()}` : ''}.`
          : 'Payment, redemption, subscriptions, and affiliate rewards remain disabled until the current terms are confirmed.'}
      </p>
      <label>
        <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
        I confirm the current payment compliance statement and accept responsibility for enabling payment-related features.
      </label>
      <div><button type="button" disabled={!accepted || busy} onClick={confirmCompliance}>{busy ? 'Confirming…' : 'Confirm compliance'}</button></div>
      {message && <p className={message.startsWith('Confirmed') ? 'success' : 'error'}>{message}</p>}
    </div>
  );
}

function OptionRow({ option, onSaved, label }: { option: Option; onSaved: () => void; label?: string }) {
  const [value, setValue] = useState(option.value);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  useEffect(() => setValue(option.value), [option.value]);

  async function save() {
    setBusy(true);
    setMessage('');
    try {
      await putData('/option/', { key: option.key, value });
      setMessage('Saved.');
      onSaved();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : 'Save failed');
    } finally {
      setBusy(false);
    }
  }
  const isRulesJSON = option.key === 'channel_affinity_setting.rules';
  return (
    <div className="option-row">
      <strong>{label ?? option.key}</strong>
      {isRulesJSON
        ? <textarea rows={8} value={value} onChange={(e) => setValue(e.target.value)} aria-label={label ?? option.key} />
        : <input value={value} onChange={(e) => setValue(e.target.value)} />}
      <button className="link" disabled={busy} onClick={save}>{busy ? 'Saving…' : 'Save'}</button>
      {message && <span className={message === 'Saved.' ? 'success' : 'error'}>{message}</span>}
    </div>
  );
}

function NewOptionForm({ onSaved }: { onSaved: () => void }) {
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    try {
      await putData('/option/', { key, value });
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
