import type { TFunction } from 'i18next';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  createWaffoPancakeSubscriptionProduct,
  listWaffoPancakeSubscriptionProducts,
  type WaffoPancakeCatalogProduct,
} from "../wallet";
import {
  SUBSCRIPTION_ADMIN_ROLE,
  SUBSCRIPTION_PAGE_SIZE,
  SUBSCRIPTION_ROOT_ROLE,
  createSubscriptionPlan,
  createUserSubscription,
  deleteUserSubscription,
  getPaymentComplianceState,
  invalidateUserSubscription,
  listSubscriptionPlans,
  listUserSubscriptions,
  resetPlanSubscriptions,
  resetUserSubscriptionsByPlan,
  setSubscriptionPlanStatus,
  updateSubscriptionPlan,
  type ManagedSubscriptionPlan,
  type ManagedUserSubscription,
  type SubscriptionDurationUnit,
  type SubscriptionPlanInput,
  type SubscriptionResetPeriod,
  type UserSubscriptionStatus,
} from './subscription-api';
import './subscriptions.css';

type Notice = { kind: 'success' | 'error'; text: string } | null;
type StatusFilter = 'all' | 'enabled' | 'disabled';
type UserStatusFilter = 'all' | UserSubscriptionStatus;
type ComplianceState = 'loading' | 'confirmed' | 'required' | 'error';
type PlanDraft = {
  title: string;
  subtitle: string;
  priceAmount: string;
  durationUnit: SubscriptionDurationUnit;
  durationValue: string;
  customSeconds: string;
  enabled: boolean;
  sortOrder: string;
  allowBalancePay: boolean;
  allowWalletOverflow: boolean;
  stripePriceId: string;
  creemProductId: string;
  waffoPancakeProductId: string;
  maxPurchasePerUser: string;
  upgradeGroup: string;
  downgradeGroup: string;
  totalAmount: string;
  quotaResetPeriod: SubscriptionResetPeriod;
  quotaResetCustomSeconds: string;
};

const EMPTY_DRAFT: PlanDraft = {
  title: '',
  subtitle: '',
  priceAmount: '0',
  durationUnit: 'month',
  durationValue: '1',
  customSeconds: '0',
  enabled: true,
  sortOrder: '0',
  allowBalancePay: true,
  allowWalletOverflow: true,
  stripePriceId: '',
  creemProductId: '',
  waffoPancakeProductId: '',
  maxPurchasePerUser: '0',
  upgradeGroup: '',
  downgradeGroup: '',
  totalAmount: '0',
  quotaResetPeriod: 'never',
  quotaResetCustomSeconds: '0',
};

function draftFromPlan(plan: ManagedSubscriptionPlan): PlanDraft {
  return {
    title: plan.title,
    subtitle: plan.subtitle,
    priceAmount: plan.priceAmount,
    durationUnit: plan.durationUnit,
    durationValue: String(plan.durationValue || 1),
    customSeconds: String(plan.customSeconds),
    enabled: plan.enabled,
    sortOrder: String(plan.sortOrder),
    allowBalancePay: plan.allowBalancePay,
    allowWalletOverflow: plan.allowWalletOverflow,
    stripePriceId: plan.stripePriceId,
    creemProductId: plan.creemProductId,
    waffoPancakeProductId: plan.waffoPancakeProductId,
    maxPurchasePerUser: String(plan.maxPurchasePerUser),
    upgradeGroup: plan.upgradeGroup,
    downgradeGroup: plan.downgradeGroup,
    totalAmount: String(plan.totalAmount),
    quotaResetPeriod: plan.quotaResetPeriod,
    quotaResetCustomSeconds: String(plan.quotaResetCustomSeconds),
  };
}

function integerDraft(value: string): number | null {
  if (!/^-?\d+$/.test(value.trim())) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

function planInputFromDraft(draft: PlanDraft): SubscriptionPlanInput | null {
  const durationValue = integerDraft(draft.durationValue);
  const customSeconds = integerDraft(draft.customSeconds);
  const sortOrder = integerDraft(draft.sortOrder);
  const maxPurchasePerUser = integerDraft(draft.maxPurchasePerUser);
  const totalAmount = integerDraft(draft.totalAmount);
  const quotaResetCustomSeconds = integerDraft(draft.quotaResetCustomSeconds);
  if (
    durationValue === null
    || customSeconds === null
    || sortOrder === null
    || maxPurchasePerUser === null
    || totalAmount === null
    || quotaResetCustomSeconds === null
  ) return null;
  return {
    ...draft,
    durationValue,
    customSeconds,
    sortOrder,
    maxPurchasePerUser,
    totalAmount,
    quotaResetCustomSeconds,
  };
}

function formatTimestamp(timestamp: number): string {
  if (timestamp === 0) return '—';
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(timestamp * 1000));
}

function durationLabel(plan: ManagedSubscriptionPlan, t: TFunction): string {
  const unit = plan.durationUnit === 'year'
    ? t('Year')
    : plan.durationUnit === 'month'
      ? t('Month')
      : plan.durationUnit === 'day'
        ? t('Day')
        : plan.durationUnit === 'hour'
          ? t('Hour')
          : t('Seconds');
  return plan.durationUnit === 'custom'
    ? `${plan.customSeconds.toLocaleString()} ${unit}`
    : `${plan.durationValue.toLocaleString()} ${unit}`;
}

function resetLabel(plan: ManagedSubscriptionPlan, t: TFunction): string {
  if (plan.quotaResetPeriod === 'never') return t('Never');
  if (plan.quotaResetPeriod === 'daily') return t('Daily');
  if (plan.quotaResetPeriod === 'weekly') return t('Weekly');
  if (plan.quotaResetPeriod === 'monthly') return t('Monthly');
  return plan.quotaResetPeriod === 'custom'
    ? `${plan.quotaResetCustomSeconds.toLocaleString()} ${t('Seconds')}`
    : t('Never');
}

function isCurrentlyActive(subscription: ManagedUserSubscription): boolean {
  return subscription.status === 'active' && subscription.endTime > Math.floor(Date.now() / 1000);
}

function effectiveSubscriptionStatus(subscription: ManagedUserSubscription): UserSubscriptionStatus {
  if (subscription.status === 'active' && !isCurrentlyActive(subscription)) return 'expired';
  return subscription.status;
}

interface PlanEditorProps {
  editing: ManagedSubscriptionPlan | null;
  draft: PlanDraft;
  canUsePancakeCatalog: boolean;
  busyAction: string;
  onDraft: React.Dispatch<React.SetStateAction<PlanDraft>>;
  onCancel: () => void;
  onSubmit: (event: React.FormEvent) => void;
  onBeginAction: (action: string) => boolean;
  onEndAction: () => void;
  onNotice: (notice: Notice) => void;
  isMounted: () => boolean;
}

function PlanEditor({
  editing,
  draft,
  canUsePancakeCatalog,
  busyAction,
  onDraft,
  onCancel,
  onSubmit,
  onBeginAction,
  onEndAction,
  onNotice,
  isMounted,
}: PlanEditorProps) {
  const { t } = useTranslation();
  const [pancakeProducts, setPancakeProducts] = useState<WaffoPancakeCatalogProduct[]>([]);
  const [pancakeLoading, setPancakeLoading] = useState(false);
  const pancakeRequest = useRef(0);

  useEffect(() => () => {
    pancakeRequest.current += 1;
  }, []);

  async function loadPancakeProducts() {
    if (!canUsePancakeCatalog || pancakeLoading) return;
    const request = ++pancakeRequest.current;
    setPancakeLoading(true);
    onNotice(null);
    try {
      const result = await listWaffoPancakeSubscriptionProducts();
      if (isMounted() && request === pancakeRequest.current) setPancakeProducts(result.products);
    } catch {
      if (isMounted() && request === pancakeRequest.current) {
        setPancakeProducts([]);
        onNotice({ kind: 'error', text: t('Unable to load Waffo Pancake products.') });
      }
    } finally {
      if (isMounted() && request === pancakeRequest.current) setPancakeLoading(false);
    }
  }

  async function createPancakeProduct() {
    if (!canUsePancakeCatalog || !onBeginAction('waffo-product')) return;
    if (!window.confirm(t('Create a Waffo Pancake product for this plan?'))) {
      onEndAction();
      return;
    }
    onNotice(null);
    try {
      const product = await createWaffoPancakeSubscriptionProduct({
        name: draft.title,
        amount: draft.priceAmount,
      });
      if (!isMounted()) return;
      setPancakeProducts((current) => current.some((item) => item.id === product.id)
        ? current
        : [...current, product]);
      onDraft((current) => ({ ...current, waffoPancakeProductId: product.id }));
      onNotice({ kind: 'success', text: t('Waffo Pancake product created and selected.') });
    } catch {
      if (isMounted()) onNotice({ kind: 'error', text: t('Unable to create Waffo Pancake product.') });
    } finally {
      if (isMounted()) onEndAction();
    }
  }

  return (
    <form className="subscription-editor" aria-label={editing
      ? t('Edit subscription plan {{name}}', { name: editing.title })
      : t('Create subscription plan')} onSubmit={onSubmit}>
      <div className="subscription-editor-heading">
        <h3>{editing ? t('Edit subscription plan') : t('Create subscription plan')}</h3>
        <button type="button" className="link" onClick={onCancel}>{t('Cancel')}</button>
      </div>

      <fieldset disabled={busyAction !== ''}>
        <legend>{t('Plan details')}</legend>
        <div className="subscription-form-grid">
          <label>
            {t('Plan title')}
            <input required maxLength={128} value={draft.title}
              onChange={(event) => onDraft((current) => ({ ...current, title: event.target.value }))} />
          </label>
          <label>
            {t('Plan subtitle')}
            <input maxLength={255} value={draft.subtitle}
              onChange={(event) => onDraft((current) => ({ ...current, subtitle: event.target.value }))} />
          </label>
          <label>
            {t('Price (USD)')}
            <input required inputMode="decimal" maxLength={16} value={draft.priceAmount}
              onChange={(event) => onDraft((current) => ({ ...current, priceAmount: event.target.value }))} />
          </label>
          <label>
            {t('Included quota')}
            <input required inputMode="numeric" min={0} max={2_147_483_647} type="number" value={draft.totalAmount}
              onChange={(event) => onDraft((current) => ({ ...current, totalAmount: event.target.value }))} />
          </label>
          <label>
            {t('Duration unit')}
            <select value={draft.durationUnit}
              onChange={(event) => onDraft((current) => ({ ...current, durationUnit: event.target.value as SubscriptionDurationUnit }))}>
              <option value="year">{t('Year')}</option>
              <option value="month">{t('Month')}</option>
              <option value="day">{t('Day')}</option>
              <option value="hour">{t('Hour')}</option>
              <option value="custom">{t('Custom')}</option>
            </select>
          </label>
          {draft.durationUnit === 'custom' ? (
            <label>
              {t('Duration seconds')}
              <input required inputMode="numeric" type="number" min={1} max={Number.MAX_SAFE_INTEGER}
                value={draft.customSeconds}
                onChange={(event) => onDraft((current) => ({ ...current, customSeconds: event.target.value }))} />
            </label>
          ) : (
            <label>
              {t('Duration value')}
              <input required inputMode="numeric" type="number" min={1} max={2_147_483_647}
                value={draft.durationValue}
                onChange={(event) => onDraft((current) => ({ ...current, durationValue: event.target.value }))} />
            </label>
          )}
          <label>
            {t('Quota reset period')}
            <select value={draft.quotaResetPeriod}
              onChange={(event) => onDraft((current) => ({ ...current, quotaResetPeriod: event.target.value as SubscriptionResetPeriod }))}>
              <option value="never">{t('Never')}</option>
              <option value="daily">{t('Daily')}</option>
              <option value="weekly">{t('Weekly')}</option>
              <option value="monthly">{t('Monthly')}</option>
              <option value="custom">{t('Custom')}</option>
            </select>
          </label>
          {draft.quotaResetPeriod === 'custom' && (
            <label>
              {t('Reset interval seconds')}
              <input required inputMode="numeric" type="number" min={1} max={Number.MAX_SAFE_INTEGER}
                value={draft.quotaResetCustomSeconds}
                onChange={(event) => onDraft((current) => ({ ...current, quotaResetCustomSeconds: event.target.value }))} />
            </label>
          )}
          <label>
            {t('Sort priority')}
            <input required inputMode="numeric" type="number" min={-2_147_483_647} max={2_147_483_647}
              value={draft.sortOrder}
              onChange={(event) => onDraft((current) => ({ ...current, sortOrder: event.target.value }))} />
          </label>
          <label>
            {t('Purchase limit per user')}
            <input required inputMode="numeric" type="number" min={0} max={2_147_483_647}
              value={draft.maxPurchasePerUser}
              onChange={(event) => onDraft((current) => ({ ...current, maxPurchasePerUser: event.target.value }))} />
          </label>
          <label>
            {t('Upgrade group')}
            <input maxLength={64} value={draft.upgradeGroup}
              onChange={(event) => onDraft((current) => ({ ...current, upgradeGroup: event.target.value }))} />
          </label>
          <label>
            {t('Downgrade group')}
            <input maxLength={64} value={draft.downgradeGroup}
              onChange={(event) => onDraft((current) => ({ ...current, downgradeGroup: event.target.value }))} />
          </label>
        </div>
      </fieldset>

      <fieldset disabled={busyAction !== ''}>
        <legend>{t('Payment products')}</legend>
        <p className="muted">{t('Product identifiers are references only; provider credentials are managed in Payment settings.')}</p>
        <div className="subscription-form-grid">
          <label>
            {t('Stripe price ID')}
            <input maxLength={128} autoComplete="off" value={draft.stripePriceId}
              onChange={(event) => onDraft((current) => ({ ...current, stripePriceId: event.target.value }))} />
          </label>
          <label>
            {t('Creem product ID')}
            <input maxLength={128} autoComplete="off" value={draft.creemProductId}
              onChange={(event) => onDraft((current) => ({ ...current, creemProductId: event.target.value }))} />
          </label>
          <label htmlFor="subscription-waffo-product-id">
            {t('Waffo Pancake product ID')}
            <input id="subscription-waffo-product-id" maxLength={128} autoComplete="off" list="subscription-pancake-products"
              value={draft.waffoPancakeProductId}
              onChange={(event) => onDraft((current) => ({ ...current, waffoPancakeProductId: event.target.value }))} />
          </label>
          <datalist id="subscription-pancake-products">
            {pancakeProducts.map((product) => <option key={product.id} value={product.id}>{product.name}</option>)}
          </datalist>
        </div>
        {canUsePancakeCatalog && (
          <div className="subscription-inline-actions">
            <button type="button" className="link" disabled={pancakeLoading || busyAction !== ''}
              onClick={() => void loadPancakeProducts()}>
              {pancakeLoading ? t('Loading Waffo Pancake products…') : t('Load Waffo Pancake products')}
            </button>
            <button type="button" className="link" disabled={busyAction !== '' || draft.title.trim() === ''}
              onClick={() => void createPancakeProduct()}>
              {busyAction === 'waffo-product' ? t('Creating product…') : t('Create Waffo Pancake product')}
            </button>
          </div>
        )}
      </fieldset>

      <fieldset className="subscription-checks" disabled={busyAction !== ''}>
        <legend>{t('Plan behavior')}</legend>
        <label><input type="checkbox" checked={draft.enabled}
          onChange={(event) => onDraft((current) => ({ ...current, enabled: event.target.checked }))} /> {t('Enabled')}</label>
        <label><input type="checkbox" checked={draft.allowBalancePay}
          onChange={(event) => onDraft((current) => ({ ...current, allowBalancePay: event.target.checked }))} /> {t('Allow wallet balance payment')}</label>
        <label><input type="checkbox" checked={draft.allowWalletOverflow}
          onChange={(event) => onDraft((current) => ({ ...current, allowWalletOverflow: event.target.checked }))} /> {t('Allow wallet overflow')}</label>
      </fieldset>

      <div className="subscription-inline-actions">
        <button type="submit" disabled={busyAction !== ''}>
          {busyAction === 'save-plan' ? t('Saving…') : editing ? t('Save changes') : t('Create plan')}
        </button>
        <button type="button" className="link" disabled={busyAction !== ''} onClick={onCancel}>{t('Cancel')}</button>
      </div>
    </form>
  );
}

export function SubscriptionAdminView({ operatorRole }: { operatorRole: number }) {
  const { t } = useTranslation();
  const authorized = Number.isSafeInteger(operatorRole) && operatorRole >= SUBSCRIPTION_ADMIN_ROLE;
  const rootOperator = Number.isSafeInteger(operatorRole) && operatorRole >= SUBSCRIPTION_ROOT_ROLE;
  const mounted = useRef(true);
  const busyActionRef = useRef('');
  const [plans, setPlans] = useState<ManagedSubscriptionPlan[]>([]);
  const [loading, setLoading] = useState(authorized);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [complianceReload, setComplianceReload] = useState(0);
  const [complianceState, setComplianceState] = useState<ComplianceState>(authorized ? 'loading' : 'required');
  const [busyAction, setBusyAction] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [draftKeyword, setDraftKeyword] = useState('');
  const [draftStatus, setDraftStatus] = useState<StatusFilter>('all');
  const [keyword, setKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [planPage, setPlanPage] = useState(1);
  const [editing, setEditing] = useState<ManagedSubscriptionPlan | null | undefined>(undefined);
  const [planDraft, setPlanDraft] = useState<PlanDraft>(EMPTY_DRAFT);
  const [advanceResetTime, setAdvanceResetTime] = useState(true);

  const [userIdDraft, setUserIdDraft] = useState('');
  const [selectedUserId, setSelectedUserId] = useState<number | null>(null);
  const [userSubscriptions, setUserSubscriptions] = useState<ManagedUserSubscription[]>([]);
  const [userLoading, setUserLoading] = useState(false);
  const [userLoadError, setUserLoadError] = useState(false);
  const [userReload, setUserReload] = useState(0);
  const [selectedPlanId, setSelectedPlanId] = useState('');
  const [userStatusFilter, setUserStatusFilter] = useState<UserStatusFilter>('all');
  const [userPage, setUserPage] = useState(1);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    if (!authorized) return undefined;
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void listSubscriptionPlans(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setPlans(result);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setPlans([]);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [authorized, reload]);

  useEffect(() => {
    if (!authorized) return undefined;
    const controller = new AbortController();
    setComplianceState('loading');
    void getPaymentComplianceState(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) {
          setComplianceState(result.confirmed ? 'confirmed' : 'required');
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setComplianceState('error');
      });
    return () => controller.abort();
  }, [authorized, complianceReload]);

  const canChangePlans = complianceState === 'confirmed';

  useEffect(() => {
    if (!canChangePlans) setEditing(undefined);
  }, [canChangePlans]);

  useEffect(() => {
    if (!authorized || selectedUserId === null) return undefined;
    const controller = new AbortController();
    setUserLoading(true);
    setUserLoadError(false);
    void listUserSubscriptions(selectedUserId, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setUserSubscriptions(result);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setUserSubscriptions([]);
          setUserLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setUserLoading(false);
      });
    return () => controller.abort();
  }, [authorized, selectedUserId, userReload]);

  const filteredPlans = useMemo(() => plans.filter((plan) => {
    const normalized = keyword.toLocaleLowerCase();
    const matchesKeyword = normalized === ''
      || plan.title.toLocaleLowerCase().includes(normalized)
      || plan.subtitle.toLocaleLowerCase().includes(normalized)
      || String(plan.id) === normalized;
    const matchesStatus = statusFilter === 'all'
      || (statusFilter === 'enabled' ? plan.enabled : !plan.enabled);
    return matchesKeyword && matchesStatus;
  }), [keyword, plans, statusFilter]);
  const planPageCount = Math.max(1, Math.ceil(filteredPlans.length / SUBSCRIPTION_PAGE_SIZE));
  const visiblePlans = filteredPlans.slice(
    (Math.min(planPage, planPageCount) - 1) * SUBSCRIPTION_PAGE_SIZE,
    Math.min(planPage, planPageCount) * SUBSCRIPTION_PAGE_SIZE,
  );

  const filteredUserSubscriptions = useMemo(() => userSubscriptions.filter((subscription) => (
    userStatusFilter === 'all' || effectiveSubscriptionStatus(subscription) === userStatusFilter
  )), [userStatusFilter, userSubscriptions]);
  const userPageCount = Math.max(1, Math.ceil(filteredUserSubscriptions.length / SUBSCRIPTION_PAGE_SIZE));
  const visibleUserSubscriptions = filteredUserSubscriptions.slice(
    (Math.min(userPage, userPageCount) - 1) * SUBSCRIPTION_PAGE_SIZE,
    Math.min(userPage, userPageCount) * SUBSCRIPTION_PAGE_SIZE,
  );
  const planNames = useMemo(() => new Map(plans.map((plan) => [plan.id, plan.title])), [plans]);

  useEffect(() => {
    setPlanPage((current) => Math.min(current, planPageCount));
  }, [planPageCount]);

  useEffect(() => {
    setUserPage((current) => Math.min(current, userPageCount));
  }, [userPageCount]);

  const refreshPlans = useCallback(() => setReload((value) => value + 1), []);
  const refreshUserSubscriptions = useCallback(() => setUserReload((value) => value + 1), []);
  const isMounted = useCallback(() => mounted.current, []);

  function beginAction(action: string): boolean {
    if (busyActionRef.current !== '') return false;
    busyActionRef.current = action;
    setBusyAction(action);
    return true;
  }

  function endAction() {
    busyActionRef.current = '';
    setBusyAction('');
  }

  function applyPlanFilters(event: React.FormEvent) {
    event.preventDefault();
    setKeyword(draftKeyword.trim());
    setStatusFilter(draftStatus);
    setPlanPage(1);
    setNotice(null);
  }

  function clearPlanFilters() {
    setDraftKeyword('');
    setDraftStatus('all');
    setKeyword('');
    setStatusFilter('all');
    setPlanPage(1);
  }

  function openCreate() {
    if (busyActionRef.current || !canChangePlans) return;
    setEditing(null);
    setPlanDraft(EMPTY_DRAFT);
    setNotice(null);
  }

  function openEdit(plan: ManagedSubscriptionPlan) {
    if (busyActionRef.current || !canChangePlans) return;
    setEditing(plan);
    setPlanDraft(draftFromPlan(plan));
    setNotice(null);
  }

  async function savePlan(event: React.FormEvent) {
    event.preventDefault();
    if (!canChangePlans) return;
    const input = planInputFromDraft(planDraft);
    if (!input) {
      setNotice({ kind: 'error', text: t('Enter a valid subscription plan configuration.') });
      return;
    }
    if (!beginAction('save-plan')) return;
    setNotice(null);
    try {
      if (editing) await updateSubscriptionPlan(editing.id, input);
      else await createSubscriptionPlan(input);
      if (!mounted.current) return;
      setEditing(undefined);
      setNotice({ kind: 'success', text: editing ? t('Subscription plan updated.') : t('Subscription plan created.') });
      refreshPlans();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: editing
        ? t('Unable to update subscription plan.')
        : t('Unable to create subscription plan.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  async function togglePlan(plan: ManagedSubscriptionPlan) {
    if (!canChangePlans || busyActionRef.current || !window.confirm(plan.enabled
      ? t('Disable plan “{{name}}”? Existing subscription records will remain.', { name: plan.title })
      : t('Enable plan “{{name}}”? It will become available for purchase.', { name: plan.title }))) return;
    if (!beginAction(`status:${plan.id}`)) return;
    setNotice(null);
    try {
      await setSubscriptionPlanStatus(plan.id, !plan.enabled);
      if (!mounted.current) return;
      setNotice({ kind: 'success', text: plan.enabled ? t('Subscription plan disabled.') : t('Subscription plan enabled.') });
      refreshPlans();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to update subscription plan status.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  async function resetPlan(plan: ManagedSubscriptionPlan) {
    if (busyActionRef.current || !window.confirm(t('Reset all active subscriptions for plan “{{name}}”?', { name: plan.title }))) return;
    if (!beginAction(`reset-plan:${plan.id}`)) return;
    setNotice(null);
    try {
      const result = await resetPlanSubscriptions(plan.id, advanceResetTime);
      if (!mounted.current) return;
      setNotice({ kind: 'success', text: t('{{count}} active subscriptions reset.', { count: result.resetCount }) });
      if (selectedUserId !== null) refreshUserSubscriptions();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to reset plan subscriptions.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  function loadUser(event: React.FormEvent) {
    event.preventDefault();
    const userId = integerDraft(userIdDraft);
    if (userId === null || userId < 1 || userId > 2_147_483_647) {
      setNotice({ kind: 'error', text: t('Enter a valid user ID.') });
      return;
    }
    setSelectedUserId(userId);
    setUserSubscriptions([]);
    setUserStatusFilter('all');
    setUserPage(1);
    setNotice(null);
  }

  async function addUserSubscription() {
    if (!canChangePlans || selectedUserId === null || busyActionRef.current) return;
    const planId = integerDraft(selectedPlanId);
    if (planId === null || !plans.some((plan) => plan.id === planId)) {
      setNotice({ kind: 'error', text: t('Select a valid subscription plan.') });
      return;
    }
    if (!beginAction('add-user-subscription')) return;
    setNotice(null);
    try {
      await createUserSubscription(selectedUserId, planId);
      if (!mounted.current) return;
      setSelectedPlanId('');
      setNotice({ kind: 'success', text: t('Subscription added to user.') });
      refreshUserSubscriptions();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to add subscription to user.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  async function resetUserPlan(subscription: ManagedUserSubscription) {
    if (selectedUserId === null || busyActionRef.current || !window.confirm(t(
      'Reset active “{{plan}}” subscriptions for user {{userId}}?',
      { plan: planNames.get(subscription.planId) ?? `#${subscription.planId}`, userId: selectedUserId },
    ))) return;
    if (!beginAction(`reset-user:${subscription.planId}`)) return;
    setNotice(null);
    try {
      const result = await resetUserSubscriptionsByPlan(
        selectedUserId,
        subscription.planId,
        advanceResetTime,
      );
      if (!mounted.current) return;
      setNotice({ kind: 'success', text: t('{{count}} active subscriptions reset.', { count: result.resetCount }) });
      refreshUserSubscriptions();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to reset user subscriptions.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  async function invalidateSubscription(subscription: ManagedUserSubscription) {
    if (busyActionRef.current || !window.confirm(t('Invalidate subscription {{id}} now?', { id: subscription.id }))) return;
    if (!beginAction(`invalidate:${subscription.id}`)) return;
    setNotice(null);
    try {
      await invalidateUserSubscription(subscription.id);
      if (!mounted.current) return;
      setNotice({ kind: 'success', text: t('Subscription invalidated.') });
      refreshUserSubscriptions();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to invalidate subscription.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  async function removeSubscription(subscription: ManagedUserSubscription) {
    if (busyActionRef.current || !window.confirm(t(
      'Permanently delete subscription {{id}}? This cannot be undone.',
      { id: subscription.id },
    ))) return;
    if (!beginAction(`delete:${subscription.id}`)) return;
    setNotice(null);
    try {
      await deleteUserSubscription(subscription.id);
      if (!mounted.current) return;
      setNotice({ kind: 'success', text: t('Subscription deleted.') });
      refreshUserSubscriptions();
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to delete subscription.') });
    } finally {
      if (mounted.current) endAction();
    }
  }

  if (!authorized) {
    return (
      <section className="card subscription-admin" aria-labelledby="subscription-admin-title">
        <h2 id="subscription-admin-title">{t('Subscription management')}</h2>
        <div className="subscription-state" role="alert">
          <h3>{t('Administrator access required')}</h3>
          <p className="muted">{t('You do not have permission to manage subscriptions.')}</p>
        </div>
      </section>
    );
  }

  return (
    <div className="subscription-admin">
      <section className="card" aria-labelledby="subscription-admin-title">
        <div className="subscription-heading">
          <div>
            <h2 id="subscription-admin-title">{t('Subscription management')}</h2>
            <p className="muted">{t('Manage plans and user subscription entitlements.')}</p>
          </div>
          <div className="subscription-inline-actions">
            <button type="button" onClick={openCreate} disabled={busyAction !== '' || !canChangePlans}>{t('Create plan')}</button>
            <button type="button" className="link" onClick={refreshPlans} disabled={loading || busyAction !== ''}>
              {loading ? t('Refreshing…') : t('Refresh')}
            </button>
          </div>
        </div>

        <p className="muted subscription-retention-note">
          {t('Plans are retained for entitlement history. Disable a plan to retire it from purchase.')}
        </p>
        {complianceState === 'loading' && (
          <p className="muted">{t('Checking payment compliance…')}</p>
        )}
        {complianceState === 'required' && (
          <div className="subscription-state" role="alert">
            <p className="error">{t('Subscription plan creation and changes are locked until the administrator confirms compliance terms in Payment Gateway settings.')}</p>
          </div>
        )}
        {complianceState === 'error' && (
          <div className="subscription-state" role="alert">
            <p className="error">{t('Unable to verify payment compliance. Plan changes remain locked.')}</p>
            <button type="button" onClick={() => setComplianceReload((value) => value + 1)}>
              {t('Retry compliance check')}
            </button>
          </div>
        )}
        <label className="subscription-advance-reset">
          <input type="checkbox" checked={advanceResetTime}
            onChange={(event) => setAdvanceResetTime(event.target.checked)} />
          {t('Advance the next reset time when resetting quota')}
        </label>

        <form className="subscription-filters" role="search" aria-label={t('Search subscription plans')} onSubmit={applyPlanFilters}>
          <label>
            {t('Search plans')}
            <input maxLength={128} value={draftKeyword} placeholder={t('Title, subtitle, or ID')}
              onChange={(event) => setDraftKeyword(event.target.value)} />
          </label>
          <label>
            {t('Plan status')}
            <select value={draftStatus} onChange={(event) => setDraftStatus(event.target.value as StatusFilter)}>
              <option value="all">{t('All statuses')}</option>
              <option value="enabled">{t('Enabled')}</option>
              <option value="disabled">{t('Disabled')}</option>
            </select>
          </label>
          <div className="subscription-inline-actions">
            <button type="submit">{t('Apply filters')}</button>
            <button type="button" className="link" onClick={clearPlanFilters}>{t('Clear filters')}</button>
          </div>
        </form>

        {notice && <p className={notice.kind} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}

        {editing !== undefined && canChangePlans && (
          <PlanEditor
            editing={editing}
            draft={planDraft}
            canUsePancakeCatalog={rootOperator && canChangePlans}
            busyAction={busyAction}
            onDraft={setPlanDraft}
            onCancel={() => setEditing(undefined)}
            onSubmit={(event) => void savePlan(event)}
            onBeginAction={beginAction}
            onEndAction={endAction}
            onNotice={setNotice}
            isMounted={isMounted}
          />
        )}

        {loadError ? (
          <div className="subscription-state" role="alert">
            <p className="error">{t('Unable to load subscription plans.')}</p>
            <button type="button" onClick={refreshPlans}>{t('Try again')}</button>
          </div>
        ) : (
          <div className="table-scroll subscription-table-wrap" aria-busy={loading}>
            <table aria-busy={loading}>
              <caption className="sr-only">{t('Subscription plans')}</caption>
              <thead><tr>
                <th scope="col">{t('Plan')}</th>
                <th scope="col">{t('Price and quota')}</th>
                <th scope="col">{t('Duration and reset')}</th>
                <th scope="col">{t('Status')}</th>
                <th scope="col">{t('Actions')}</th>
              </tr></thead>
              <tbody>
                {loading && plans.length === 0 && <tr><td colSpan={5} role="status">{t('Loading subscription plans…')}</td></tr>}
                {!loading && visiblePlans.length === 0 && <tr><td colSpan={5}>{t('No subscription plans match these filters.')}</td></tr>}
                {visiblePlans.map((plan) => {
                  const rowResetting = busyAction === `reset-plan:${plan.id}`;
                  return (
                    <tr key={plan.id}>
                      <th scope="row"><strong>{plan.title}</strong><span className="muted">#{plan.id}{plan.subtitle ? ` · ${plan.subtitle}` : ''}</span></th>
                      <td><strong>{plan.priceAmount} {plan.currency}</strong><span className="muted">{plan.totalAmount === 0 ? t('Unlimited quota') : `${plan.totalAmount.toLocaleString()} ${t('quota')}`}</span></td>
                      <td>{durationLabel(plan, t)}<span className="muted">{t('Reset')}: {resetLabel(plan, t)}</span></td>
                      <td><span className={plan.enabled ? 'status-pill success' : 'status-pill'}>{plan.enabled ? t('Enabled') : t('Disabled')}</span></td>
                      <td><div className="subscription-row-actions">
                        <button type="button" className="link" disabled={busyAction !== '' || !canChangePlans} onClick={() => openEdit(plan)}>{t('Edit')}</button>
                        <button type="button" className="link" disabled={busyAction !== ''} onClick={() => void resetPlan(plan)}>{rowResetting ? t('Working…') : t('Reset quota')}</button>
                        <button type="button" className="link" disabled={busyAction !== '' || !canChangePlans} onClick={() => void togglePlan(plan)}>{plan.enabled ? t('Disable') : t('Enable')}</button>
                      </div></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {!loadError && !loading && filteredPlans.length > 0 && (
          <nav className="pagination" aria-label={t('Subscription plan pages')}>
            <button type="button" className="link" disabled={planPage <= 1} onClick={() => setPlanPage((value) => Math.max(1, value - 1))}>{t('Previous')}</button>
            <span>{t('Page {{page}} of {{pages}}', { page: Math.min(planPage, planPageCount), pages: planPageCount })}</span>
            <button type="button" className="link" disabled={planPage >= planPageCount} onClick={() => setPlanPage((value) => Math.min(planPageCount, value + 1))}>{t('Next')}</button>
          </nav>
        )}
      </section>

      <section className="card" aria-labelledby="user-subscriptions-title">
        <div className="subscription-heading">
          <div>
            <h2 id="user-subscriptions-title">{t('User subscription lifecycle')}</h2>
            <p className="muted">{t('Grant, inspect, reset, invalidate, or permanently delete a user entitlement.')}</p>
          </div>
        </div>

        <form className="subscription-user-lookup" role="search" aria-label={t('Find user subscriptions')} onSubmit={loadUser}>
          <label>
            {t('User ID')}
            <input required inputMode="numeric" type="number" min={1} max={2_147_483_647}
              value={userIdDraft} onChange={(event) => setUserIdDraft(event.target.value)} />
          </label>
          <button type="submit" disabled={busyAction !== ''}>{t('Load subscriptions')}</button>
        </form>

        {selectedUserId !== null && (
          <>
            <div className="subscription-user-controls">
              <label>
                {t('Subscription status')}
                <select value={userStatusFilter} onChange={(event) => {
                  setUserStatusFilter(event.target.value as UserStatusFilter);
                  setUserPage(1);
                }}>
                  <option value="all">{t('All statuses')}</option>
                  <option value="active">{t('Active')}</option>
                  <option value="expired">{t('Expired')}</option>
                  <option value="cancelled">{t('Invalidated')}</option>
                </select>
              </label>
              <label>
                {t('Plan to grant')}
                <select disabled={!canChangePlans} value={selectedPlanId} onChange={(event) => setSelectedPlanId(event.target.value)}>
                  <option value="">{t('Select a plan')}</option>
                  {plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.title} (#{plan.id})</option>)}
                </select>
              </label>
              <button type="button" disabled={busyAction !== '' || selectedPlanId === '' || !canChangePlans}
                onClick={() => void addUserSubscription()}>
                {busyAction === 'add-user-subscription' ? t('Adding…') : t('Add subscription')}
              </button>
              <button type="button" className="link" disabled={userLoading || busyAction !== ''}
                onClick={refreshUserSubscriptions}>{userLoading ? t('Refreshing…') : t('Refresh')}</button>
            </div>

            {userLoadError ? (
              <div className="subscription-state" role="alert">
                <p className="error">{t('Unable to load user subscriptions.')}</p>
                <button type="button" onClick={refreshUserSubscriptions}>{t('Try again')}</button>
              </div>
            ) : (
              <div className="table-scroll subscription-table-wrap" aria-busy={userLoading}>
                <table aria-busy={userLoading}>
                  <caption className="sr-only">{t('Subscriptions for user {{id}}', { id: selectedUserId })}</caption>
                  <thead><tr>
                    <th scope="col">{t('Subscription')}</th>
                    <th scope="col">{t('Status')}</th>
                    <th scope="col">{t('Usage')}</th>
                    <th scope="col">{t('Validity')}</th>
                    <th scope="col">{t('Actions')}</th>
                  </tr></thead>
                  <tbody>
                    {userLoading && userSubscriptions.length === 0 && <tr><td colSpan={5} role="status">{t('Loading user subscriptions…')}</td></tr>}
                    {!userLoading && visibleUserSubscriptions.length === 0 && <tr><td colSpan={5}>{t('No subscription records match this filter.')}</td></tr>}
                    {visibleUserSubscriptions.map((subscription) => {
                      const active = isCurrentlyActive(subscription);
                      const effectiveStatus = effectiveSubscriptionStatus(subscription);
                      return (
                        <tr key={subscription.id}>
                          <th scope="row"><strong>{planNames.get(subscription.planId) ?? `#${subscription.planId}`}</strong><span className="muted">#{subscription.id} · {t('Source')}: {subscription.source || '—'}</span></th>
                          <td>{effectiveStatus === 'active' ? t('Active') : effectiveStatus === 'cancelled' ? t('Invalidated') : t('Expired')}</td>
                          <td>{subscription.amountUsed.toLocaleString()} / {subscription.amountTotal === 0 ? t('Unlimited') : subscription.amountTotal.toLocaleString()}</td>
                          <td>{formatTimestamp(subscription.startTime)}<span className="muted">{t('to')} {formatTimestamp(subscription.endTime)}</span></td>
                          <td><div className="subscription-row-actions">
                            {active && <button type="button" className="link" disabled={busyAction !== ''} onClick={() => void resetUserPlan(subscription)}>{t('Reset quota')}</button>}
                            {active && <button type="button" className="link" disabled={busyAction !== ''} onClick={() => void invalidateSubscription(subscription)}>{t('Invalidate')}</button>}
                            <button type="button" className="link danger" disabled={busyAction !== ''} onClick={() => void removeSubscription(subscription)}>{t('Delete')}</button>
                          </div></td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
            {!userLoadError && !userLoading && filteredUserSubscriptions.length > 0 && (
              <nav className="pagination" aria-label={t('User subscription pages')}>
                <button type="button" className="link" disabled={userPage <= 1} onClick={() => setUserPage((value) => Math.max(1, value - 1))}>{t('Previous')}</button>
                <span>{t('Page {{page}} of {{pages}}', { page: Math.min(userPage, userPageCount), pages: userPageCount })}</span>
                <button type="button" className="link" disabled={userPage >= userPageCount} onClick={() => setUserPage((value) => Math.min(userPageCount, value + 1))}>{t('Next')}</button>
              </nav>
            )}
          </>
        )}
      </section>
    </div>
  );
}
