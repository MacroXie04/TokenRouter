import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, getData, postData, putData, type ApiResponse, type User } from '../api';
import { ChannelAdminView } from '../features/channels/ChannelAdminView';
import { RedemptionAdminView } from '../features/redemptions/RedemptionAdminView';
import { UserAdminView } from '../features/users/UserAdminView';
import { WaffoPancakeAdminPanel } from '../features/wallet/WaffoPancakeAdminPanel';
import { SystemSettingsEditor } from '../features/system-settings/SystemSettingsEditor';
import { SMTPSettingsPanel } from '../features/system-settings/SMTPSettingsPanel';
import { channelAdminCapabilities } from '../lib/admin-permissions';
import {
  createWaffoPancakeSubscriptionProduct,
  listWaffoPancakeSubscriptionProducts,
  type WaffoPancakeCatalogProduct,
} from '../features/wallet/waffo-pancake-admin-api';

interface DashboardStats {
  user_count: number;
  token_count: number;
  channel_count: number;
  request_count: number;
}

interface Option {
  key: string;
  value: string;
  redacted?: boolean;
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
  waffo_pancake_product_id: string;
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

const MAX_PREFILL_GROUPS = 1_000;
const MAX_PREFILL_ITEMS = 10_000;
const MAX_PREFILL_ITEM_BYTES = 512;
const MAX_PREFILL_JSON_BYTES = 64 * 1024;
const MAX_AFFINITY_RULES = 1_000;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function boundedInteger(value: unknown, minimum = 0): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum;
}

function parsePrefillGroups(value: unknown): PrefillGroup[] {
  if (!Array.isArray(value) || value.length > MAX_PREFILL_GROUPS) {
    throw new Error('invalid prefill group response');
  }
  return value.map((candidate) => {
    if (!isRecord(candidate) || !boundedInteger(candidate.id, 1) ||
        typeof candidate.name !== 'string' || candidate.name.length === 0 || candidate.name.length > 64 ||
        (candidate.type !== 'model' && candidate.type !== 'tag' && candidate.type !== 'endpoint') ||
        (candidate.description !== undefined && (typeof candidate.description !== 'string' || candidate.description.length > 255)) ||
        !boundedInteger(candidate.created_time) || !boundedInteger(candidate.updated_time)) {
      throw new Error('invalid prefill group response');
    }
    const encodedItems = JSON.stringify(candidate.items ?? null);
    if (encodedItems === undefined || encodedItems.length > MAX_PREFILL_JSON_BYTES ||
        (Array.isArray(candidate.items) && (candidate.items.length > MAX_PREFILL_ITEMS || candidate.items.some((item) => typeof item !== 'string' || item.length > MAX_PREFILL_ITEM_BYTES)))) {
      throw new Error('invalid prefill group response');
    }
    return candidate as unknown as PrefillGroup;
  });
}

function parseAffinityCacheStats(value: unknown): AffinityCacheStats {
  if (!isRecord(value) || typeof value.enabled !== 'boolean' ||
      !boundedInteger(value.total) || !boundedInteger(value.unknown) || !boundedInteger(value.cache_capacity) ||
      value.total > value.cache_capacity || value.unknown > value.total ||
      typeof value.cache_algo !== 'string' || value.cache_algo.length === 0 || value.cache_algo.length > 64 ||
      !isRecord(value.by_rule_name) || Object.keys(value.by_rule_name).length > MAX_AFFINITY_RULES) {
    throw new Error('invalid affinity cache response');
  }
  const byRuleName: Record<string, number> = {};
  let categorized = 0;
  for (const [rule, count] of Object.entries(value.by_rule_name)) {
    if (rule.length === 0 || rule.length > 128 || !boundedInteger(count) || count > value.total) {
      throw new Error('invalid affinity cache response');
    }
    byRuleName[rule] = count;
    categorized += count;
  }
  if (categorized + value.unknown !== value.total) throw new Error('invalid affinity cache response');
  return { ...value, by_rule_name: byRuleName } as AffinityCacheStats;
}

function parseDeletedCount(value: unknown, maximum: number): number {
  if (!isRecord(value) || value.success !== true || !isRecord(value.data) ||
      !boundedInteger(value.data.deleted) || value.data.deleted > maximum) {
    throw new Error('invalid affinity cache clear response');
  }
  return value.data.deleted;
}

function parsePaymentComplianceStatus(value: unknown): PaymentComplianceStatus {
  if (!isRecord(value) || value.confirmed !== true || value.terms_version !== 'v1' ||
      !boundedInteger(value.confirmed_at) || !boundedInteger(value.confirmed_by, 1)) {
    throw new Error('invalid payment compliance response');
  }
  return value as unknown as PaymentComplianceStatus;
}

export type AdminTab = 'overview' | 'channels' | 'abilities' | 'users' | 'tokens' | 'models' | 'prefill' | 'plans' | 'options' | 'logs' | 'redemptions';

const ADMIN_TAB_ROUTES: Partial<Record<AdminTab, string>> = {
  overview: '/dashboard',
  channels: '/channels',
  users: '/users',
  models: '/models',
  plans: '/subscriptions',
  options: '/system-settings',
  logs: '/usage-logs/common',
  redemptions: '/redemption-codes',
};

export function AdminConsole({
  user,
  initialTab = 'overview',
  settingsPath,
  onNavigate,
  onLogout,
}: {
  user: User;
  initialTab?: AdminTab;
  settingsPath?: string;
  onNavigate?: (path: string) => void;
  onLogout: () => void;
}) {
  const { t } = useTranslation();
  const isRoot = user.role >= 100;
  const channelCapabilities = channelAdminCapabilities(user);
  const [tab, setTab] = useState<AdminTab>(initialTab);
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [options, setOptions] = useState<Option[]>([]);
  const [logs, setLogs] = useState<LogRow[]>([]);
  const [abilities, setAbilities] = useState<Ability[]>([]);
  const [error, setError] = useState('');

  const [abGroup, setAbGroup] = useState('default');
  const [abModel, setAbModel] = useState('');
  const [abChannel, setAbChannel] = useState(0);

  const [plans, setPlans] = useState<Plan[]>([]);
  const [planTitle, setPlanTitle] = useState('');
  const [planPrice, setPlanPrice] = useState('10');
  const [planQuota, setPlanQuota] = useState(1000000);
  const [planPancakeProduct, setPlanPancakeProduct] = useState('');
  const [pancakeProducts, setPancakeProducts] = useState<WaffoPancakeCatalogProduct[]>([]);
  const [pancakeProductBusy, setPancakeProductBusy] = useState(false);
  const [pancakeProductMessage, setPancakeProductMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);
  const pancakeProductLoadError = t('Unable to load Waffo Pancake products.');

  const [tokens, setTokens] = useState<TokenRow[]>([]);
  const [models, setModels] = useState<ModelRow[]>([]);
  const [instances, setInstances] = useState<Instance[]>([]);
  const tabLabels: Record<AdminTab, string> = {
    overview: t('Overview'),
    channels: t('Channels'),
    abilities: t('Abilities'),
    users: t('Users'),
    tokens: t('API keys'),
    models: t('Models'),
    prefill: t('Prefill'),
    plans: t('Plans'),
    options: t('Options'),
    logs: t('Logs'),
    redemptions: t('Redemptions'),
  };

  const refresh = useCallback(async () => {
    const [statsResult, optionsResult, logsResult, abilitiesResult, plansResult, tokensResult, modelsResult, instancesResult] = await Promise.allSettled([
      getData<DashboardStats>('/dashboard/stats'),
      isRoot ? getData<Option[]>('/option/') : Promise.resolve([] as Option[]),
      getData<{ items: LogRow[] }>('/log', { page: 1, page_size: 50 }).then((response) => response.items),
      channelCapabilities.canRead ? getData<Ability[]>('/ability') : Promise.resolve([] as Ability[]),
      getData<{ plan: Plan }[]>('/subscription/admin/plans').then((rows) => rows.map((row) => row.plan)),
      getData<{ items: TokenRow[] }>('/token').then((response) => response.items),
      getData<ModelRow[]>('/models'),
      getData<Instance[]>('/instance'),
    ]);
    if (statsResult.status === 'fulfilled') setStats(statsResult.value);
    if (optionsResult.status === 'fulfilled') setOptions(optionsResult.value);
    if (logsResult.status === 'fulfilled') setLogs(logsResult.value);
    if (abilitiesResult.status === 'fulfilled') setAbilities(abilitiesResult.value);
    if (plansResult.status === 'fulfilled') setPlans(plansResult.value);
    if (tokensResult.status === 'fulfilled') setTokens(tokensResult.value);
    if (modelsResult.status === 'fulfilled') setModels(modelsResult.value);
    if (instancesResult.status === 'fulfilled') setInstances(instancesResult.value);
  }, [channelCapabilities.canRead, isRoot]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  useEffect(() => {
    setTab(initialTab);
  }, [initialTab]);

  useEffect(() => {
    if (tab !== 'plans' || !isRoot) return;
    let active = true;
    setPancakeProductMessage(null);
    listWaffoPancakeSubscriptionProducts()
      .then((result) => {
        if (active) setPancakeProducts(result.products);
      })
      .catch(() => {
        if (active) {
          setPancakeProducts([]);
          setPancakeProductMessage({ kind: 'error', text: pancakeProductLoadError });
        }
      });
    return () => { active = false; };
  }, [isRoot, pancakeProductLoadError, tab]);

  function selectTab(next: AdminTab) {
    setTab(next);
    const target = ADMIN_TAB_ROUTES[next];
    if (target) onNavigate?.(target);
  }

  async function logout() {
    try {
      await api.post('/user/auth/logout');
    } finally {
      onLogout();
    }
  }

  async function createAbility(e: React.FormEvent) {
    e.preventDefault();
    if (!channelCapabilities.canWrite) return;
    setError('');
    try {
      await postData('/ability', { group: abGroup, model: abModel, channel_id: abChannel, weight: 1 });
      setAbModel('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('Creation failed'));
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
        waffo_pancake_product_id: planPancakeProduct,
      });
      setPlanTitle('');
      setPlanPancakeProduct('');
      await refresh();
    } catch {
      setError(t('Creation failed'));
    }
  }

  async function createPlanPancakeProduct() {
    if (pancakeProductBusy) return;
    const title = planTitle.trim();
    const amount = planPrice.trim();
    if (title === '' || !/^(?:0|[1-9]\d{0,9})(?:\.\d{1,6})?$/.test(amount) || Number(amount) <= 0) {
      setPancakeProductMessage({ kind: 'error', text: t('Enter a plan title and positive price first.') });
      return;
    }
    setPancakeProductBusy(true);
    setPancakeProductMessage(null);
    try {
      const product = await createWaffoPancakeSubscriptionProduct({ name: title, amount });
      setPancakeProducts((current) => [product, ...current.filter((item) => item.id !== product.id)]);
      setPlanPancakeProduct(product.id);
      setPancakeProductMessage({ kind: 'success', text: t('Waffo Pancake plan product created.') });
    } catch {
      setPancakeProductMessage({ kind: 'error', text: t('Unable to create the Waffo Pancake plan product.') });
    } finally {
      setPancakeProductBusy(false);
    }
  }

  return (
    <main className="app">
      <header className="header row">
        <div>
          <h1>{t('TokenRouter Admin')}</h1>
          <p className="tagline">{t('Manage channels, users, and settings.')}</p>
        </div>
        <button className="link" onClick={logout}>{t('Sign out')}</button>
      </header>

      <nav className="tabs">
        {(['overview', 'channels', 'abilities', 'users', 'tokens', 'models', 'prefill', 'plans', 'options', 'logs', 'redemptions'] as AdminTab[])
          .filter((tabName) => tabName !== 'options' || isRoot)
          .filter((tabName) => tabName !== 'channels' || channelCapabilities.canRead)
          .filter((tabName) => tabName !== 'abilities' || channelCapabilities.canRead)
          .map((tabName) => (
          <button key={tabName} className={tab === tabName ? 'tab active' : 'tab'} onClick={() => selectTab(tabName)}>
            {tabLabels[tabName]}
          </button>
        ))}
      </nav>

      {error && <p className="error">{error}</p>}

      {tab === 'overview' && (
        <section className="card">
          <h2>{t('Overview')}</h2>
          <dl className="kv">
            <dt>{t('Users')}</dt><dd>{stats?.user_count ?? 0}</dd>
            <dt>{t('API keys')}</dt><dd>{stats?.token_count ?? 0}</dd>
            <dt>{t('Channels')}</dt><dd>{stats?.channel_count ?? 0}</dd>
            <dt>{t('Requests')}</dt><dd>{stats?.request_count ?? 0}</dd>
          </dl>
          {instances.length > 0 && (
            <>
              <h3 style={{ fontSize: '0.95rem', margin: '1rem 0 0.4rem' }}>{t('Nodes')}</h3>
              <ul className="key-list">
                {instances.map((i) => (
                  <li key={i.node_name}>
                    <strong>{i.node_name}</strong>
                    <span className="muted">{t('Seen {{time}}', { time: new Date(i.last_seen_at * 1000).toLocaleString() })}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
      )}

      {tab === 'channels' && <ChannelAdminView {...channelCapabilities} isRoot={isRoot} />}

      {tab === 'abilities' && channelCapabilities.canRead && (
        <section className="card">
          <h2>{t('Routing abilities')}</h2>
          {channelCapabilities.canWrite && <form className="grid-form" onSubmit={createAbility}>
            <label>{t('Group')}<input value={abGroup} onChange={(e) => setAbGroup(e.target.value)} /></label>
            <label>{t('Model')}<input value={abModel} onChange={(e) => setAbModel(e.target.value)} required /></label>
            <label>{t('Channel ID')}<input type="number" value={abChannel} onChange={(e) => setAbChannel(Number(e.target.value))} required /></label>
            <button type="submit">{t('Add ability')}</button>
          </form>}
          <ul className="key-list">
            {abilities.map((a) => (
              <li key={`${a.group}:${a.model}:${a.channel_id}`}>
                <strong>{a.group} / {a.model}</strong>
                <span className="muted">{t('Channel {{id}}', { id: a.channel_id })}</span>
                <span className="muted">{t('Weight {{weight}}', { weight: a.weight })}</span>
                <span className="muted">{a.enabled ? t('Enabled') : t('Disabled')}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'users' && <UserAdminView operatorId={user.id} operatorRole={user.role} />}

      {tab === 'tokens' && (
        <section className="card">
          <h2>{t('API keys')}</h2>
          <ul className="key-list">
            {tokens.map((tk) => (
              <li key={tk.id}>
                <strong>{tk.name}</strong>
                <code>{tk.key.slice(0, 8)}…</code>
                <span className="muted">{t('User {{id}}', { id: tk.user_id })}</span>
                <span className="muted">{tk.unlimited_quota ? t('Unlimited') : t('Remaining {{quota}}', { quota: tk.remain_quota })}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'models' && (
        <section className="card">
          <h2>{t('Model registry')}</h2>
          {models.length === 0 ? (
            <p className="muted">{t('No model metadata yet.')}</p>
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
          <h2>{t('Subscription plans')}</h2>
          <form className="grid-form" onSubmit={createPlan}>
            <label>{t('Title')}<input value={planTitle} onChange={(e) => setPlanTitle(e.target.value)} required /></label>
            <label>{t('Price (USD)')}<input value={planPrice} onChange={(e) => setPlanPrice(e.target.value)} /></label>
            <label>{t('Quota')}<input type="number" value={planQuota} onChange={(e) => setPlanQuota(Number(e.target.value))} /></label>
            {isRoot && (
              <label>
                {t('Waffo Pancake product')}
                <select value={planPancakeProduct} onChange={(event) => setPlanPancakeProduct(event.target.value)}>
                  <option value="">{t('No Waffo Pancake checkout')}</option>
                  {pancakeProducts.map((product) => (
                    <option key={product.id} value={product.id}>{product.name} ({product.id})</option>
                  ))}
                </select>
              </label>
            )}
            {isRoot && (
              <button type="button" disabled={pancakeProductBusy} onClick={() => void createPlanPancakeProduct()}>
                {pancakeProductBusy ? t('Creating…') : t('Create Pancake product from this plan')}
              </button>
            )}
            <button type="submit">{t('Add plan')}</button>
          </form>
          {pancakeProductMessage && <p role="status" className={pancakeProductMessage.kind}>{pancakeProductMessage.text}</p>}
          <ul className="key-list">
            {plans.map((p) => (
              <li key={p.id}>
                <strong>{p.title}</strong>
                <span className="muted">${p.price_amount}</span>
                <span className="muted">{t('+{{quota}} quota', { quota: p.total_amount })}</span>
                <span className="muted">{t('{{count}} {{unit}}', { count: p.duration_value, unit: p.duration_unit })}</span>
                {p.waffo_pancake_product_id && <span className="muted">{t('Waffo Pancake: {{id}}', { id: p.waffo_pancake_product_id })}</span>}
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'options' && isRoot && (
        <SystemSettingsEditor
          options={options}
          settingsPath={settingsPath}
          onNavigate={(target) => onNavigate?.(target)}
          onSaved={refresh}
          supplements={{
            'billing/payment': (
              <>
                <PaymentCompliancePanel options={options} onConfirmed={refresh} />
                <WaffoPancakeAdminPanel options={options} onSaved={refresh} />
              </>
            ),
            'billing/model-pricing': <PricingResetPanel onReset={refresh} />,
            'models/channel-affinity': <AffinityCachePanel />,
            'operations/email': <SMTPSettingsPanel options={options} onSaved={refresh} />,
          }}
        />
      )}

      {tab === 'logs' && (
        <section className="card">
          <h2>{t('Recent logs')}</h2>
          <ul className="key-list">
            {logs.map((l) => (
              <li key={l.id}>
                <strong>{l.username || '—'}</strong>
                <span className="muted">{l.model_name}</span>
                <span className="muted">{t('Quota {{quota}}', { quota: l.quota })}</span>
                <span className="muted">{new Date(l.created_at * 1000).toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {tab === 'redemptions' && <RedemptionAdminView operatorRole={user.role} />}
    </main>
  );
}

function PrefillGroupPanel() {
  const { t } = useTranslation();
  const [groups, setGroups] = useState<PrefillGroup[]>([]);
  const [filter, setFilter] = useState('');
  const [editingId, setEditingId] = useState<number | null>(null);
  const [name, setName] = useState('');
  const [type, setType] = useState('model');
  const [items, setItems] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);

  const refreshGroups = useCallback(async () => {
    try {
      const response = await getData<unknown>('/prefill_group/', filter ? { type: filter } : undefined);
      setGroups(parsePrefillGroups(response));
    } catch {
      setGroups([]);
      setMessage({ kind: 'error', text: t('Could not load prefill groups') });
    }
  }, [filter, t]);

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
    setMessage(null);
  }

  async function saveGroup(event: React.FormEvent) {
    event.preventDefault();
    if (busy) return;
    const normalizedName = name.trim();
    if (normalizedName.length === 0 || normalizedName.length > 64 || description.length > 255 || items.length > MAX_PREFILL_JSON_BYTES) {
      setMessage({ kind: 'error', text: t('Prefill group values exceed safe limits.') });
      return;
    }
    let payloadItems: string | string[];
    if (type === 'endpoint') {
      try {
        JSON.parse(items || '{}');
      } catch {
        setMessage({ kind: 'error', text: t('Endpoint items must contain valid JSON.') });
        return;
      }
      payloadItems = items || '{}';
    } else {
      payloadItems = items.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
      if (payloadItems.length > MAX_PREFILL_ITEMS || payloadItems.some((item) => item.length > MAX_PREFILL_ITEM_BYTES)) {
        setMessage({ kind: 'error', text: t('Prefill group items exceed safe limits.') });
        return;
      }
    }
    setBusy(true);
    setMessage(null);
    try {
      const payload = { id: editingId ?? undefined, name: normalizedName, type, items: payloadItems, description };
      if (editingId) await putData<PrefillGroup>('/prefill_group/', payload);
      else await postData<PrefillGroup>('/prefill_group/', payload);
      setMessage({ kind: 'success', text: editingId ? t('Prefill group updated.') : t('Prefill group created.') });
      resetForm();
      await refreshGroups();
    } catch {
      setMessage({ kind: 'error', text: t('Could not save prefill group') });
    } finally {
      setBusy(false);
    }
  }

  async function deleteGroup(group: PrefillGroup) {
    if (busy || !window.confirm(t('Delete prefill group “{{name}}”?', { name: group.name }))) return;
    setBusy(true);
    setMessage(null);
    try {
      const response = await api.delete<ApiResponse<null>>(`/prefill_group/${group.id}`);
      if (!response.data.success) throw new Error(response.data.message || 'Delete failed');
      if (editingId === group.id) resetForm();
      setMessage({ kind: 'success', text: t('Prefill group deleted.') });
      await refreshGroups();
    } catch {
      setMessage({ kind: 'error', text: t('Could not delete prefill group') });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card">
      <h2>{t('Prefill groups')}</h2>
      <p className="muted">{t('Manage reusable model, tag, and endpoint sets used by channel forms.')}</p>
      <label>
        {t('Filter by type')}
        <select value={filter} onChange={(event) => setFilter(event.target.value)}>
          <option value="">{t('All types')}</option>
          <option value="model">{t('Model')}</option>
          <option value="tag">{t('Tag')}</option>
          <option value="endpoint">{t('Endpoint')}</option>
        </select>
      </label>
      <form className="grid-form" onSubmit={saveGroup}>
        <label>{t('Name')}<input value={name} maxLength={64} onChange={(event) => setName(event.target.value)} required /></label>
        <label>{t('Type')}
          <select value={type} onChange={(event) => setType(event.target.value)}>
            <option value="model">{t('Model')}</option>
            <option value="tag">{t('Tag')}</option>
            <option value="endpoint">{t('Endpoint')}</option>
          </select>
        </label>
        <label>{t('Description')}<input value={description} maxLength={255} onChange={(event) => setDescription(event.target.value)} /></label>
        <label>
          {type === 'endpoint' ? t('Endpoint JSON') : t('Items (one per line or comma-separated)')}
          <textarea rows={6} maxLength={MAX_PREFILL_JSON_BYTES} value={items} onChange={(event) => setItems(event.target.value)} />
        </label>
        <div className="inline-form">
          <button type="submit" disabled={busy}>{busy ? t('Saving…') : editingId ? t('Update group') : t('Create group')}</button>
          {editingId && <button type="button" className="link" disabled={busy} onClick={resetForm}>{t('Cancel edit')}</button>}
        </div>
      </form>
      {message && <p className={message.kind}>{message.text}</p>}
      {groups.length === 0 ? <p className="muted">{t('No prefill groups.')}</p> : (
        <ul className="key-list">
          {groups.map((group) => (
            <li key={group.id}>
              <strong>{group.name}</strong>
              <span className="muted">{group.type}</span>
              <span className="muted">{group.description || t('No description')}</span>
              <button type="button" className="link" disabled={busy} onClick={() => editGroup(group)}>{t('Edit')}</button>
              <button type="button" className="link" disabled={busy} onClick={() => void deleteGroup(group)}>{t('Delete')}</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function PricingResetPanel({ onReset }: { onReset: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);

  async function resetPricing() {
    if (busy || !window.confirm(t('Reset all model prices to the built-in TokenRouter defaults?'))) return;
    setBusy(true);
    setMessage(null);
    try {
      await postData<void>('/option/rest_model_ratio');
      setMessage({ kind: 'success', text: t('Model pricing defaults restored.') });
      await onReset();
    } catch {
      setMessage({ kind: 'error', text: t('Pricing reset failed') });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: '0.65rem', padding: '0.8rem', marginBottom: '1rem' }}>
      <h3 style={{ margin: '0 0 0.4rem' }}>{t('Model pricing')}</h3>
      <p className="muted">{t('Restore the built-in USD-per-million model prices and apply them to live billing immediately.')}</p>
      <button type="button" disabled={busy} onClick={resetPricing}>{busy ? t('Resetting…') : t('Reset model pricing')}</button>
      {message && <p className={message.kind}>{message.text}</p>}
    </div>
  );
}

function AffinityCachePanel() {
  const { t } = useTranslation();
  const [stats, setStats] = useState<AffinityCacheStats | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);

  const refreshStats = useCallback(async () => {
    try {
      const response = await getData<unknown>('/option/channel_affinity_cache');
      setStats(parseAffinityCacheStats(response));
    } catch {
      setStats(null);
      setMessage({ kind: 'error', text: t('Could not load affinity cache') });
    }
  }, [t]);

  useEffect(() => { void refreshStats(); }, [refreshStats]);

  async function clearCache(ruleName?: string) {
    const confirmation = ruleName
      ? t('Clear entries for rule “{{rule}}”?', { rule: ruleName })
      : t('Clear all channel-affinity entries?');
    if (busy || !window.confirm(confirmation)) return;
    setBusy(true);
    setMessage(null);
    try {
      const response = await api.delete<ApiResponse<{ deleted: number }>>('/option/channel_affinity_cache', {
        params: ruleName ? { rule_name: ruleName } : { all: true },
      });
      const deleted = parseDeletedCount(response.data, stats?.total ?? 0);
      setMessage({ kind: 'success', text: t('Cleared {{count}} affinity cache entries.', { count: deleted }) });
      await refreshStats();
    } catch {
      setMessage({ kind: 'error', text: t('Cache clear failed') });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: '0.65rem', padding: '0.8rem', marginBottom: '1rem' }}>
      <h3 style={{ margin: '0 0 0.4rem' }}>{t('Channel affinity cache')}</h3>
      <p className="muted">{t('Inspect live rule-based routing entries. Cache keys use fingerprints; raw affinity values are not exposed.')}</p>
      {stats && (
        <>
          <p className="muted">
            {stats.enabled ? t('Enabled') : t('Disabled')} · {t('{{total}} / {{capacity}} entries', { total: stats.total, capacity: stats.cache_capacity })} · {stats.cache_algo}
            {stats.unknown > 0 ? ` · ${t('{{count}} unknown', { count: stats.unknown })}` : ''}
          </p>
          {Object.entries(stats.by_rule_name).map(([rule, count]) => (
            <div key={rule} className="inline-form" style={{ marginBottom: '0.35rem' }}>
              <span>{rule}: {count}</span>
              <button type="button" disabled={busy || count === 0} onClick={() => void clearCache(rule)}>{t('Clear rule')}</button>
            </div>
          ))}
        </>
      )}
      <button type="button" disabled={busy || !stats || stats.total === 0} onClick={() => void clearCache()}>
        {busy ? t('Clearing…') : t('Clear all affinity entries')}
      </button>
      {message && <p className={message.kind}>{message.text}</p>}
    </div>
  );
}

function PaymentCompliancePanel({ options, onConfirmed }: { options: Option[]; onConfirmed: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);
  const confirmed = options.find((o) => o.key === 'payment_setting.compliance_confirmed')?.value === 'true';
  const termsVersion = options.find((o) => o.key === 'payment_setting.compliance_terms_version')?.value ?? '';
  const confirmedAt = Number(options.find((o) => o.key === 'payment_setting.compliance_confirmed_at')?.value ?? 0);
  const current = confirmed && termsVersion === 'v1';

  async function confirmCompliance() {
    if (!accepted || busy) return;
    setBusy(true);
    setMessage(null);
    try {
      const response = await postData<unknown>('/option/payment_compliance', { confirmed: true });
      const status = parsePaymentComplianceStatus(response);
      setMessage({ kind: 'success', text: t('Confirmed terms {{version}}.', { version: status.terms_version }) });
      setAccepted(false);
      await onConfirmed();
    } catch {
      setMessage({ kind: 'error', text: t('Confirmation failed') });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: '0.65rem', padding: '0.8rem', marginBottom: '1rem' }}>
      <h3 style={{ margin: '0 0 0.4rem' }}>{t('Payment compliance')}</h3>
      <p className="muted">
        {current
          ? confirmedAt
            ? t('Current terms {{version}} confirmed on {{time}}.', { version: termsVersion, time: new Date(confirmedAt * 1000).toLocaleString() })
            : t('Current terms {{version}} confirmed.', { version: termsVersion })
          : t('Payment, redemption, subscriptions, and affiliate rewards remain disabled until the current terms are confirmed.')}
      </p>
      <label>
        <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
        {t('I confirm the current payment compliance statement and accept responsibility for enabling payment-related features.')}
      </label>
      <div><button type="button" disabled={!accepted || busy} onClick={confirmCompliance}>{busy ? t('Confirming…') : t('Confirm compliance')}</button></div>
      {message && <p className={message.kind}>{message.text}</p>}
    </div>
  );
}
