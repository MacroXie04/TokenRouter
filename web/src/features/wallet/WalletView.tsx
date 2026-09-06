import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { User } from '../../api';
import {
  TURNSTILE_DISABLED,
  TurnstileWidget,
  type TurnstileConfig,
} from '../security/TurnstileWidget';
import {
  calculatePaymentAmount,
  checkIn,
  completeWalletOrder,
  createCheckout,
  createSubscriptionCheckout,
  getAdminWalletOrders,
  getAffiliateSummary,
  getCheckInStatus,
  getSubscriptionPlans,
  getSubscriptionSummary,
  getTopUpInfo,
  getWalletOrders,
  getWalletUser,
  purchaseSubscriptionWithBalance,
  redeemTopUpCode,
  signOutWallet,
  transferAffiliateQuota,
  updateBillingPreference,
  TurnstileRequiredError,
  type AffiliateSummary,
  type BillingPreference,
  type CheckoutRequest,
  type CheckoutResult,
  type PaymentKind,
  type SubscriptionPlan,
  type SubscriptionSummary,
  type SubscriptionCheckoutRequest,
  type UserSubscription,
  type WalletOrderPage,
  type WalletTopUpInfo,
} from './wallet-api';

const HISTORY_PAGE_SIZES = [10, 20, 50, 100] as const;

const subscriptionStatusKeys: Record<UserSubscription['status'], string> = {
  active: 'Active',
  expired: 'Expired',
  cancelled: 'Cancelled',
};

const durationUnitKeys: Record<SubscriptionPlan['durationUnit'], string> = {
  year: 'Year',
  month: 'Month',
  day: 'Day',
  hour: 'Hour',
  custom: 'Custom',
};

const resetPeriodKeys: Record<SubscriptionPlan['quotaResetPeriod'], string> = {
  never: 'Never',
  daily: 'Daily',
  weekly: 'Weekly',
  monthly: 'Monthly',
  custom: 'Custom',
};

type Notice = { kind: 'success' | 'error'; text: string } | null;

interface PaymentOption {
  id: string;
  label: string;
  kind: PaymentKind;
  minimum: number;
  paymentMethod?: string;
  payMethodIndex?: number;
}

interface WalletViewProps {
  user: User;
  onNavigate: (target: string) => void;
  onUserChange?: (user: User) => void;
  onLogout: () => void;
  turnstileConfig?: TurnstileConfig;
  initialShowHistory?: boolean;
}

function integerInput(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

function dateTime(timestamp: number, language: string): string {
  if (timestamp === 0) return '—';
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' })
    .format(new Date(timestamp * 1_000));
}

function number(value: number, language: string): string {
  return new Intl.NumberFormat(language).format(value);
}

function decimalNumber(value: number, language: string): string {
  return new Intl.NumberFormat(language, { maximumFractionDigits: 6 }).format(value);
}

function money(value: number, language: string): string {
  return new Intl.NumberFormat(language, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value);
}

function quotaDisplayUnit(info: WalletTopUpInfo): string {
  if (info.quotaDisplayType === 'currency') return 'USD';
  if (info.quotaDisplayType === 'cny') return 'CNY';
  return info.currencySymbol;
}

function quotaToDisplayAmount(quota: number, info: WalletTopUpInfo): number {
  if (info.quotaDisplayType === 'tokens') return quota;
  const usd = quota / info.quotaPerUnit;
  return usd * info.currencyExchangeRate;
}

function formatQuotaForDisplay(quota: number, info: WalletTopUpInfo, language: string): string {
  const amount = quotaToDisplayAmount(quota, info);
  if (!Number.isFinite(amount)) return '—';
  return `${decimalNumber(amount, language)} ${quotaDisplayUnit(info)}`;
}

function formatCreditForDisplay(amount: number, info: WalletTopUpInfo, language: string): string {
  const displayed = info.quotaDisplayType === 'tokens'
    ? amount * info.quotaPerUnit
    : amount * info.currencyExchangeRate;
  if (!Number.isFinite(displayed) || !Number.isSafeInteger(displayed) && info.quotaDisplayType === 'tokens') return '—';
  return `${decimalNumber(displayed, language)} ${quotaDisplayUnit(info)}`;
}

function positiveDecimalFraction(value: string): { numerator: bigint; denominator: bigint } | null {
  const match = /^(\d+)(?:\.(\d+))?(?:e([+-]?\d+))?$/i.exec(value);
  if (!match) return null;
  const fraction = match[2] ?? '';
  const exponent = Number(match[3] ?? '0');
  if (!Number.isSafeInteger(exponent) || Math.abs(exponent) > 1_000) return null;
  const digits = `${match[1]}${fraction}`.replace(/^0+(?=\d)/, '');
  let numerator = BigInt(digits);
  let denominator = 1n;
  const decimalPlaces = fraction.length - exponent;
  if (decimalPlaces > 0) denominator = 10n ** BigInt(decimalPlaces);
  if (decimalPlaces < 0) numerator *= 10n ** BigInt(-decimalPlaces);
  return numerator > 0n ? { numerator, denominator } : null;
}

function transferInputToQuota(value: string, info: WalletTopUpInfo): number | null {
  if (info.quotaDisplayType === 'tokens') return integerInput(value);
  if (!/^(?:0|[1-9]\d*)(?:\.\d{1,6})?$/.test(value)) return null;
  const amount = positiveDecimalFraction(value);
  const exchangeRate = positiveDecimalFraction(
    info.currencyExchangeRate.toString(),
  );
  if (!amount || !exchangeRate) return null;
  const numerator = amount.numerator * exchangeRate.denominator * BigInt(info.quotaPerUnit);
  const denominator = amount.denominator * exchangeRate.numerator;
  const rounded = (2n * numerator + denominator) / (2n * denominator);
  return rounded > 0n && rounded <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(rounded) : null;
}

function subscriptionBalanceCost(plan: SubscriptionPlan, info: WalletTopUpInfo): number | null {
  const [whole, fraction = ''] = plan.priceAmount.split('.');
  try {
    const scale = 10n ** BigInt(fraction.length);
    const numerator = BigInt(whole) * scale + BigInt(fraction || '0');
    const exact = numerator * BigInt(info.quotaPerUnit);
    const rounded = (exact + scale - 1n) / scale;
    return rounded <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(rounded) : null;
  } catch {
    return null;
  }
}

function knownProviderName(value: string): string {
  switch (value.toLowerCase()) {
    case 'stripe': return 'Stripe';
    case 'creem': return 'Creem';
    case 'waffo': return 'Waffo';
    case 'waffo_pancake':
    case 'waffo-pancake': return 'Waffo Pancake';
    case 'epay': return 'Epay';
    case 'balance': return 'Balance';
    default: return value;
  }
}

function orderProviderText(
  order: WalletOrderPage['items'][number],
  info: WalletTopUpInfo | null,
): string {
  const provider = order.paymentProvider === '' ? '' : knownProviderName(order.paymentProvider);
  const configuredMethod = info?.paymentMethods.find((method) => method.type === order.paymentMethod)?.name
    ?? info?.waffoMethods.find((method) => (
      method.payMethodType === order.paymentMethod || method.payMethodName === order.paymentMethod
    ))?.name;
  const method = configuredMethod
    ?? (order.paymentMethod === '' ? '' : knownProviderName(order.paymentMethod));
  if (provider !== '' && method !== '' && provider.toLowerCase() !== method.toLowerCase()) {
    return `${provider} · ${method}`;
  }
  return provider || method || '—';
}

function SubscriptionRecordItem({
  subscription,
  plan,
  info,
}: {
  subscription: UserSubscription;
  plan: SubscriptionPlan | undefined;
  info: WalletTopUpInfo | null;
}) {
  const { t, i18n } = useTranslation();
  const now = Date.now() / 1_000;
  const effectiveStatus = subscription.status === 'active' && subscription.endTime <= now
    ? 'expired'
    : subscription.status;
  const finiteQuota = subscription.amountTotal > 0;
  const remainingQuota = finiteQuota
    ? Math.max(0, subscription.amountTotal - subscription.amountUsed)
    : 0;
  const usagePercent = finiteQuota
    ? Math.min(100, Math.max(0, Math.round((subscription.amountUsed / subscription.amountTotal) * 100)))
    : 0;
  const remainingDays = effectiveStatus === 'active'
    ? Math.max(0, Math.ceil((subscription.endTime - now) / 86_400))
    : 0;
  const endLabel = effectiveStatus === 'active'
    ? t('Until')
    : effectiveStatus === 'cancelled' ? t('Cancelled at') : t('Expired at');
  const quotaText = (value: number) => info
    ? formatQuotaForDisplay(value, info, i18n.language)
    : number(value, i18n.language);
  return (
    <li>
      <div className="wallet-subscription-heading">
        <strong>
          {plan?.title && <span>{plan.title} · </span>}
          <span>{t('Subscription {{id}}', { id: subscription.id })}</span>
        </strong>
        <span className={`status status-${effectiveStatus}`}>{t(subscriptionStatusKeys[effectiveStatus])}</span>
      </div>
      {effectiveStatus === 'active' && (
        <span className="muted">{t('{{count}} days remaining', { count: remainingDays })}</span>
      )}
      <span>{!finiteQuota
        ? t('Unlimited quota')
        : t('{{used}} of {{total}} used', {
          used: quotaText(subscription.amountUsed),
          total: quotaText(subscription.amountTotal),
        })}</span>
      {finiteQuota && (
        <>
          <span className="muted">{t('Remaining quota')}: {quotaText(remainingQuota)} · {t('Used {{percent}}%', { percent: usagePercent })}</span>
          {effectiveStatus === 'active' && (
            <progress
              className="wallet-subscription-progress"
              aria-label={t('Quota usage for subscription {{id}}', { id: subscription.id })}
              max={100}
              value={usagePercent}
            />
          )}
        </>
      )}
      <span className="muted">{t('Started')}: {dateTime(subscription.startTime, i18n.language)}</span>
      <span className="muted">{endLabel}: {dateTime(subscription.endTime, i18n.language)}</span>
      {effectiveStatus === 'active' && subscription.nextResetTime > 0 && (
        <span className="muted">{t('Next reset')}: {dateTime(subscription.nextResetTime, i18n.language)}</span>
      )}
      {subscription.lastResetTime > 0 && (
        <span className="muted">{t('Last reset')}: {dateTime(subscription.lastResetTime, i18n.language)}</span>
      )}
      {subscription.upgradeGroup !== '' && (
        <span className="muted">{t('Upgrade group')}: {subscription.upgradeGroup}</span>
      )}
      {subscription.downgradeGroup !== '' && (
        <span className="muted">{t('Downgrade group')}: {subscription.downgradeGroup}</span>
      )}
      <span className="muted">{t('Wallet overflow')}: {subscription.allowWalletOverflow ? t('Enabled') : t('Disabled')}</span>
      {subscription.source !== '' && <span className="muted">{t('Source')}: {knownProviderName(subscription.source)}</span>}
    </li>
  );
}

function CheckoutControl({ checkout, label }: { checkout: CheckoutResult; label: string }) {
  if (checkout.method === 'POST') {
    return (
      <form
        aria-label={label}
        action={checkout.action}
        method="post"
        acceptCharset="UTF-8"
      >
        {checkout.fields.map((field) => (
          <input key={field.name} type="hidden" name={field.name} value={field.value} />
        ))}
        <button type="submit" className="button">{label}</button>
      </form>
    );
  }
  return (
    <a className="button" href={checkout.action} target="_blank" rel="noopener noreferrer">{label}</a>
  );
}

function paymentOptions(info: WalletTopUpInfo | null): PaymentOption[] {
  if (!info || !info.complianceConfirmed || info.complianceVersion !== 'v1') return [];
  const result: PaymentOption[] = [];
  if (info.onlineEnabled) {
    for (const method of info.paymentMethods) {
      if (method.type === 'stripe' || method.type === 'waffo' || method.type === 'waffo_pancake') continue;
      result.push({
        id: `epay:${method.type}`,
        label: method.name,
        kind: 'epay',
        minimum: Math.max(method.minimum ?? 0, info.minimum),
        paymentMethod: method.type,
      });
    }
  }
  if (info.stripeEnabled) {
    result.push({ id: 'stripe', label: 'Stripe', kind: 'stripe', minimum: info.stripeMinimum });
  }
  if (info.waffoPancakeEnabled) {
    result.push({
      id: 'waffo-pancake',
      label: 'Waffo Pancake',
      kind: 'waffo-pancake',
      minimum: info.waffoPancakeMinimum,
    });
  }
  if (info.waffoEnabled) {
    info.waffoMethods.forEach((method, index) => {
      result.push({
        id: `waffo:${index}`,
        label: method.name,
        kind: 'waffo',
        minimum: info.waffoMinimum,
        payMethodIndex: index,
      });
    });
  }
  return result;
}

export function WalletView({
  user,
  onNavigate,
  onUserChange,
  onLogout,
  turnstileConfig = TURNSTILE_DISABLED,
  initialShowHistory = false,
}: WalletViewProps) {
  const { t, i18n } = useTranslation();
  const [walletUser, setWalletUser] = useState(user);
  const [info, setInfo] = useState<WalletTopUpInfo | null>(null);
  const [checkedIn, setCheckedIn] = useState<boolean | null>(null);
  const [orders, setOrders] = useState<WalletOrderPage | null>(null);
  const [affiliate, setAffiliate] = useState<AffiliateSummary | null>(null);
  const [plans, setPlans] = useState<SubscriptionPlan[]>([]);
  const [subscriptions, setSubscriptions] = useState<SubscriptionSummary | null>(null);

  const [infoLoading, setInfoLoading] = useState(true);
  const [ordersLoading, setOrdersLoading] = useState(true);
  const [affiliateLoading, setAffiliateLoading] = useState(true);
  const [subscriptionsLoading, setSubscriptionsLoading] = useState(true);
  const [infoError, setInfoError] = useState(false);
  const [ordersError, setOrdersError] = useState(false);
  const [affiliateError, setAffiliateError] = useState(false);
  const [subscriptionsError, setSubscriptionsError] = useState(false);

  const [amount, setAmount] = useState('');
  const [selectedPayment, setSelectedPayment] = useState('');
  const [preview, setPreview] = useState<{ signature: string; payable: string } | null>(null);
  const [checkout, setCheckout] = useState<CheckoutResult | null>(null);
  const [subscriptionCheckout, setSubscriptionCheckout] = useState<CheckoutResult | null>(null);
  const [redeemCode, setRedeemCode] = useState('');
  const [affiliateAmount, setAffiliateAmount] = useState('');
  const [historyKeyword, setHistoryKeyword] = useState('');
  const [historyPage, setHistoryPage] = useState(1);
  const [historyPageSize, setHistoryPageSize] = useState<number>(HISTORY_PAGE_SIZES[0]);
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [copiedKey, setCopiedKey] = useState('');
  const [turnstileOpen, setTurnstileOpen] = useState(false);
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0);
  const historySectionRef = useRef<HTMLElement>(null);

  const options = useMemo(() => paymentOptions(info), [info]);
  const plansById = useMemo(() => new Map(plans.map((plan) => [plan.id, plan])), [plans]);
  const allSubscriptions = useMemo(() => {
    if (!subscriptions) return [];
    const unique = new Map<number, UserSubscription>();
    for (const subscription of subscriptions.history) unique.set(subscription.id, subscription);
    for (const subscription of subscriptions.active) unique.set(subscription.id, subscription);
    return Array.from(unique.values());
  }, [subscriptions]);
  const planPurchaseCounts = useMemo(() => {
    const counts = new Map<number, number>();
    for (const subscription of allSubscriptions) {
      counts.set(subscription.planId, (counts.get(subscription.planId) ?? 0) + 1);
    }
    return counts;
  }, [allSubscriptions]);
  const historicalSubscriptions = useMemo(() => {
    if (!subscriptions) return [];
    const activeIds = new Set(subscriptions.active.map((subscription) => subscription.id));
    return allSubscriptions.filter((subscription) => !activeIds.has(subscription.id));
  }, [allSubscriptions, subscriptions]);
  const isAdmin = Number.isSafeInteger(user.role) && user.role >= 10;
  const complianceReady = !infoLoading
    && !infoError
    && info?.complianceConfirmed === true
    && info.complianceVersion === 'v1';
  const redemptionReady = complianceReady && info?.redemptionEnabled === true;
  const selectedOption = options.find((option) => option.id === selectedPayment) ?? null;
  const enteredPaymentAmount = integerInput(amount);
  const selectedPaymentBelowMinimum = selectedOption !== null
    && (enteredPaymentAmount === null || enteredPaymentAmount < selectedOption.minimum);
  const previewSignature = `${selectedPayment}:${amount}`;
  const affiliateTransferQuota = info ? transferInputToQuota(affiliateAmount, info) : null;
  const affiliateTransferReady = affiliate !== null
    && info !== null
    && affiliateTransferQuota !== null
    && affiliateTransferQuota >= info.quotaPerUnit
    && affiliateTransferQuota <= affiliate.availableQuota;
  const referralLink = useMemo(() => {
    if (!affiliate || typeof window === 'undefined') return '';
    const link = new URL('/sign-up', window.location.origin);
    link.searchParams.set('aff', affiliate.code);
    return link.toString();
  }, [affiliate]);
  const effectiveBillingPreference: BillingPreference | null = useMemo(() => {
    if (!subscriptions) return null;
    const subscriptionPreference = subscriptions.billingPreference === 'subscription_first'
      || subscriptions.billingPreference === 'subscription_only';
    return subscriptions.active.length === 0 && subscriptionPreference
      ? 'wallet_first'
      : subscriptions.billingPreference;
  }, [subscriptions]);
  const balanceRefreshFailure = t('Unable to refresh wallet balance.');
  const complianceUnavailable = t('Payments are unavailable until the operator confirms the payment terms.');

  const refreshUser = useCallback(async (signal?: AbortSignal) => {
    try {
      const fresh = await getWalletUser(signal);
      if (signal?.aborted) return;
      setWalletUser(fresh);
      onUserChange?.(fresh);
    } catch {
      if (!signal?.aborted) setNotice({ kind: 'error', text: balanceRefreshFailure });
    }
  }, [balanceRefreshFailure, onUserChange]);

  const loadInfo = useCallback(async (signal?: AbortSignal) => {
    setInfoLoading(true);
    setInfoError(false);
    try {
      const next = await getTopUpInfo(signal);
      if (!signal?.aborted) {
        const nextOptions = paymentOptions(next);
        const firstMinimum = nextOptions[0]?.minimum ?? next.minimum;
        const firstPreset = next.presets.find((preset) => preset >= firstMinimum);
        setAmount((current) => current === '' ? String(firstPreset ?? firstMinimum) : current);
        setInfo(next);
      }
    } catch {
      if (!signal?.aborted) setInfoError(true);
    } finally {
      if (!signal?.aborted) setInfoLoading(false);
    }
  }, []);

  const loadOrders = useCallback(async (
    page: number,
    pageSize: number,
    keyword: string,
    signal?: AbortSignal,
  ) => {
    setOrdersLoading(true);
    setOrdersError(false);
    try {
      const next = isAdmin
        ? await getAdminWalletOrders(page, pageSize, keyword, signal)
        : await getWalletOrders(user.id, page, pageSize, keyword, signal);
      if (!signal?.aborted) {
        setOrders(next);
        setHistoryPage(next.page);
        setHistoryPageSize(next.pageSize);
      }
    } catch {
      if (!signal?.aborted) setOrdersError(true);
    } finally {
      if (!signal?.aborted) setOrdersLoading(false);
    }
  }, [isAdmin, user.id]);

  const loadAffiliate = useCallback(async (signal?: AbortSignal) => {
    setAffiliateLoading(true);
    setAffiliateError(false);
    try {
      const next = await getAffiliateSummary(signal);
      if (!signal?.aborted) setAffiliate(next);
    } catch {
      if (!signal?.aborted) setAffiliateError(true);
    } finally {
      if (!signal?.aborted) setAffiliateLoading(false);
    }
  }, []);

  const loadSubscriptions = useCallback(async (signal?: AbortSignal) => {
    setSubscriptionsLoading(true);
    setSubscriptionsError(false);
    try {
      const [nextPlans, nextSummary] = await Promise.all([
        getSubscriptionPlans(signal),
        getSubscriptionSummary(user.id, signal),
      ]);
      if (!signal?.aborted) {
        setPlans(nextPlans);
        setSubscriptions(nextSummary);
      }
    } catch {
      if (!signal?.aborted) setSubscriptionsError(true);
    } finally {
      if (!signal?.aborted) setSubscriptionsLoading(false);
    }
  }, [user.id]);

  useEffect(() => {
    const controller = new AbortController();
    void loadInfo(controller.signal);
    void loadOrders(1, HISTORY_PAGE_SIZES[0], '', controller.signal);
    void loadAffiliate(controller.signal);
    void loadSubscriptions(controller.signal);
    void refreshUser(controller.signal);
    getCheckInStatus(controller.signal)
      .then((value) => { if (!controller.signal.aborted) setCheckedIn(value); })
      .catch(() => { if (!controller.signal.aborted) setCheckedIn(null); });
    return () => controller.abort();
  }, [loadAffiliate, loadInfo, loadOrders, loadSubscriptions, refreshUser]);

  useEffect(() => {
    if (!info) return;
    if (options.length > 0 && !options.some((option) => option.id === selectedPayment)) {
      setSelectedPayment(options[0].id);
    }
  }, [info, options, selectedPayment]);

  const showHistory = useCallback(() => {
    historySectionRef.current?.focus({ preventScroll: true });
    historySectionRef.current?.scrollIntoView?.({ block: 'start' });
  }, []);

  useEffect(() => {
    if (!initialShowHistory) return;
    window.history.replaceState({}, '', window.location.pathname);
    showHistory();
  }, [initialShowHistory, showHistory]);

  const walletQuotaText = (value: number) => info
    ? formatQuotaForDisplay(value, info, i18n.language)
    : number(value, i18n.language);

  function planPurchaseState(plan: SubscriptionPlan) {
    const purchaseCount = planPurchaseCounts.get(plan.id) ?? 0;
    const limitReached = plan.maxPurchasePerUser > 0 && purchaseCount >= plan.maxPurchasePerUser;
    const balanceCost = info ? subscriptionBalanceCost(plan, info) : null;
    const insufficientBalance = balanceCost !== null && walletUser.quota < balanceCost;
    const hostedPaymentAvailable = Boolean(
      info?.stripeEnabled && plan.stripePriceId !== ''
      || info?.creemEnabled && plan.creemProductId !== ''
      || info?.waffoPancakeEnabled && plan.waffoPancakeProductId !== ''
      || info?.onlineEnabled && info.paymentMethods.some(
        (method) => method.type !== 'stripe' && method.type !== 'waffo' && method.type !== 'waffo_pancake',
      ),
    );
    return {
      purchaseCount,
      limitReached,
      balanceCost,
      insufficientBalance,
      hostedPaymentAvailable,
      balancePaymentAvailable: plan.allowBalancePay && balanceCost !== null && !insufficientBalance,
    };
  }

  function requirePaymentCompliance(): boolean {
    if (complianceReady) return true;
    setNotice({ kind: 'error', text: complianceUnavailable });
    return false;
  }

  async function doCheckIn(token?: string) {
    if (busy || checkedIn) return;
    setBusy('checkin');
    setNotice(null);
    try {
      const result = token === undefined ? await checkIn() : await checkIn(token);
      setCheckedIn(true);
      setTurnstileOpen(false);
      await refreshUser();
      setNotice({ kind: 'success', text: t('Check-in added {{quota}} quota.', { quota: number(result.quotaAwarded, i18n.language) }) });
    } catch (error) {
      if (error instanceof TurnstileRequiredError && token === undefined) {
        if (turnstileConfig.required && turnstileConfig.siteKey !== '') {
          setTurnstileOpen(true);
          return;
        }
        setNotice({ kind: 'error', text: t('Human verification is unavailable.') });
        return;
      }
      if (token !== undefined) setTurnstileWidgetKey((value) => value + 1);
      setNotice({ kind: 'error', text: t('Unable to check in today.') });
    } finally {
      setBusy('');
    }
  }

  async function doRedeem(event: FormEvent) {
    event.preventDefault();
    const code = redeemCode.trim();
    if (busy || !requirePaymentCompliance()) return;
    if (!info?.redemptionEnabled) {
      setNotice({ kind: 'error', text: t('Code redemption is currently unavailable.') });
      return;
    }
    if (code === '' || code.length > 128) return;
    setBusy('redeem');
    setNotice(null);
    try {
      const granted = await redeemTopUpCode(code);
      setRedeemCode('');
      await Promise.all([refreshUser(), loadOrders(1, historyPageSize, historyKeyword)]);
      setNotice({ kind: 'success', text: t('Code redeemed for {{quota}} quota.', { quota: number(granted, i18n.language) }) });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to redeem this code.') });
    } finally {
      setBusy('');
    }
  }

  function validatePayment(): { option: PaymentOption; requested: number } | null {
    const requested = integerInput(amount);
    if (!selectedOption || requested === null || requested < selectedOption.minimum) {
      setNotice({ kind: 'error', text: t('Enter an amount that meets the selected payment minimum.') });
      return null;
    }
    return { option: selectedOption, requested };
  }

  async function reviewPayment(event: FormEvent) {
    event.preventDefault();
    if (busy || !requirePaymentCompliance()) return;
    const validated = validatePayment();
    if (!validated) return;
    setBusy('preview');
    setNotice(null);
    setCheckout(null);
    try {
      const payable = await calculatePaymentAmount(
        validated.option.kind,
        validated.requested,
        validated.option.payMethodIndex,
      );
      setPreview({ signature: previewSignature, payable });
    } catch {
      setPreview(null);
      setNotice({ kind: 'error', text: t('Unable to calculate the payment amount.') });
    } finally {
      setBusy('');
    }
  }

  async function startSelectedCheckout() {
    if (busy || !requirePaymentCompliance() || preview?.signature !== previewSignature) return;
    const validated = validatePayment();
    if (!validated) return;
    if (!window.confirm(t('Create a checkout for {{amount}} quota costing {{money}}?', {
      amount: number(validated.requested, i18n.language),
      money: preview.payable,
    }))) return;
    let request: CheckoutRequest;
    if (validated.option.kind === 'epay') {
      request = {
        kind: 'epay',
        amount: validated.requested,
        paymentMethod: validated.option.paymentMethod ?? '',
      };
    } else if (validated.option.kind === 'waffo') {
      request = {
        kind: 'waffo',
        amount: validated.requested,
        payMethodIndex: validated.option.payMethodIndex ?? -1,
      };
    } else if (validated.option.kind === 'waffo-pancake') {
      request = { kind: 'waffo-pancake', amount: validated.requested };
    } else {
      request = { kind: 'stripe', amount: validated.requested };
    }
    await runCheckout(request);
  }

  async function runCheckout(request: CheckoutRequest) {
    if (busy || !requirePaymentCompliance()) return;
    setBusy('checkout');
    setNotice(null);
    setCheckout(null);
    try {
      const result = await createCheckout(request);
      setCheckout(result);
      await loadOrders(1, historyPageSize, historyKeyword);
      setNotice({ kind: 'success', text: t('Checkout created. Open it when you are ready to pay.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to create the checkout.') });
    } finally {
      setBusy('');
    }
  }

  async function buyCreemProduct(productId: string, label: string) {
    if (busy || !requirePaymentCompliance()
      || !window.confirm(t('Create a checkout for {{product}}?', { product: label }))) return;
    await runCheckout({ kind: 'creem', productId });
  }

  async function doAffiliateTransfer(event: FormEvent) {
    event.preventDefault();
    if (busy || !requirePaymentCompliance()) return;
    const requested = info ? transferInputToQuota(affiliateAmount, info) : null;
    if (!affiliate || !info || requested === null || requested < info.quotaPerUnit
      || requested > affiliate.availableQuota) {
      setNotice({ kind: 'error', text: t('Enter a valid affiliate quota amount.') });
      return;
    }
    setBusy('affiliate');
    setNotice(null);
    try {
      await transferAffiliateQuota(requested);
      setAffiliateAmount('');
      await Promise.all([refreshUser(), loadAffiliate()]);
      setNotice({ kind: 'success', text: t('Affiliate quota transferred to your wallet.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to transfer affiliate quota.') });
    } finally {
      setBusy('');
    }
  }

  async function buySubscription(plan: SubscriptionPlan) {
    if (busy || !requirePaymentCompliance()) return;
    const state = planPurchaseState(plan);
    if (state.limitReached) {
      setNotice({ kind: 'error', text: t('Purchase limit reached.') });
      return;
    }
    if (!plan.allowBalancePay) {
      setNotice({ kind: 'error', text: t('Wallet purchase is disabled for this plan.') });
      return;
    }
    if (state.balanceCost === null) {
      setNotice({ kind: 'error', text: t('Unable to calculate the required wallet balance.') });
      return;
    }
    if (state.insufficientBalance) {
      setNotice({ kind: 'error', text: t('Insufficient wallet balance.') });
      return;
    }
    if (!window.confirm(t('Buy {{plan}} from your wallet balance?', { plan: plan.title }))) return;
    setBusy(`plan:${plan.id}`);
    setNotice(null);
    try {
      await purchaseSubscriptionWithBalance(plan.id);
      await Promise.all([refreshUser(), loadSubscriptions()]);
      setNotice({ kind: 'success', text: t('Subscription purchased.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to purchase this subscription.') });
    } finally {
      setBusy('');
    }
  }

  async function startSubscriptionCheckout(
    plan: SubscriptionPlan,
    kind: SubscriptionCheckoutRequest['kind'],
    paymentMethod?: string,
  ) {
    if (busy || !requirePaymentCompliance()) return;
    if (planPurchaseState(plan).limitReached) {
      setNotice({ kind: 'error', text: t('Purchase limit reached.') });
      return;
    }
    if (!window.confirm(t('Create a payment checkout for {{plan}}?', { plan: plan.title }))) return;
    let request: SubscriptionCheckoutRequest;
    if (kind === 'epay') {
      request = { kind, planId: plan.id, paymentMethod: paymentMethod ?? '' };
    } else {
      request = { kind, planId: plan.id };
    }
    setBusy(`subscription-checkout:${plan.id}:${kind}`);
    setNotice(null);
    setSubscriptionCheckout(null);
    try {
      const result = await createSubscriptionCheckout(request);
      setSubscriptionCheckout(result);
      setNotice({ kind: 'success', text: t('Subscription checkout created.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to create the subscription checkout.') });
    } finally {
      setBusy('');
    }
  }

  async function changeBillingPreference(preference: BillingPreference) {
    if (busy || !subscriptions) return;
    setBusy('preference');
    setNotice(null);
    try {
      const saved = await updateBillingPreference(preference);
      setSubscriptions({ ...subscriptions, billingPreference: saved });
      setNotice({ kind: 'success', text: t('Billing preference saved.') });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to save billing preference.') });
    } finally {
      setBusy('');
    }
  }

  function searchHistory(event: FormEvent) {
    event.preventDefault();
    if (historyKeyword.length > 255) return;
    setHistoryPage(1);
    void loadOrders(1, historyPageSize, historyKeyword);
  }

  function changeHistoryPageSize(value: string) {
    const size = Number(value);
    if (!HISTORY_PAGE_SIZES.includes(size as (typeof HISTORY_PAGE_SIZES)[number]) || ordersLoading) return;
    setHistoryPageSize(size);
    setHistoryPage(1);
    void loadOrders(1, size, historyKeyword);
  }

  async function copyValue(value: string, key: string, successText: string) {
    if (!navigator.clipboard?.writeText) {
      setNotice({ kind: 'error', text: t('Unable to copy.') });
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
      setCopiedKey(key);
      setNotice({ kind: 'success', text: successText });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to copy.') });
    }
  }

  async function completeOrder(order: WalletOrderPage['items'][number]) {
    if (busy || !isAdmin || order.status !== 'pending') return;
    if (!window.confirm(t(
      'Are you sure you want to manually complete this order? The user will be credited with the corresponding quota.',
    ))) return;
    setBusy(`complete-order:${order.id}`);
    setNotice(null);
    try {
      await completeWalletOrder(order.tradeNo);
      await loadOrders(historyPage, historyPageSize, historyKeyword);
      setNotice({ kind: 'success', text: t('Order completed successfully') });
    } catch {
      setNotice({ kind: 'error', text: t('Failed to complete order') });
    } finally {
      setBusy('');
    }
  }

  async function signOut() {
    if (busy) return;
    setBusy('logout');
    try {
      await signOutWallet();
    } catch {
      // Local sign-out still fails closed if the server cannot be reached.
    } finally {
      onLogout();
    }
  }

  return (
    <main className="app wallet-app" aria-label={t('Wallet')}>
      {turnstileOpen && (
        <div className="modal-overlay">
          <section className="modal" role="dialog" aria-modal="true" aria-label={t('Security check')}>
            <h2>{t('Security check')}</h2>
            <p className="muted">{t('Complete the security check to continue.')}</p>
            <TurnstileWidget
              key={turnstileWidgetKey}
              className="turnstile-widget"
              siteKey={turnstileConfig.siteKey}
              label={t('Human verification')}
              onVerify={(challenge) => { void doCheckIn(challenge); }}
              onExpire={() => setTurnstileWidgetKey((value) => value + 1)}
              onError={() => {
                setTurnstileOpen(false);
                setNotice({ kind: 'error', text: t('Human verification is unavailable.') });
              }}
            />
            <div className="modal-actions">
              <button
                type="button"
                className="link"
                disabled={busy === 'checkin'}
                onClick={() => {
                  setTurnstileOpen(false);
                  setTurnstileWidgetKey((value) => value + 1);
                }}
              >
                {t('Close')}
              </button>
            </div>
          </section>
        </div>
      )}
      <header className="header row">
        <div>
          <h1>{t('Wallet')}</h1>
          <p className="tagline">{t('Manage balance, payments, rewards, and subscriptions.')}</p>
        </div>
        <nav className="wallet-header-actions" aria-label={t('Wallet navigation')}>
          <button type="button" className="link" onClick={() => onNavigate('/dashboard')}>{t('Dashboard')}</button>
          <button type="button" className="link" onClick={signOut} disabled={Boolean(busy)}>{t('Sign out')}</button>
        </nav>
      </header>

      {notice && <p className={notice.kind === 'error' ? 'error' : 'success'} role="status" aria-live="polite">{notice.text}</p>}

      <section className="wallet-stat-grid" aria-label={t('Wallet balance')}>
        <article className="card wallet-stat"><span>{t('Remaining')}</span><strong>{walletQuotaText(walletUser.quota)}</strong></article>
        <article className="card wallet-stat"><span>{t('Used')}</span><strong>{walletQuotaText(walletUser.used_quota)}</strong></article>
        <article className="card wallet-stat"><span>{t('Requests')}</span><strong>{number(walletUser.request_count, i18n.language)}</strong></article>
      </section>

      <section className="card wallet-checkin">
        <div>
          <h2>{t('Daily check-in')}</h2>
          <p className="muted">{checkedIn === true ? t('You have checked in today.') : t('Claim today’s available quota reward.')}</p>
        </div>
        <button type="button" onClick={() => { void doCheckIn(); }} disabled={Boolean(busy) || checkedIn === true}>
          {busy === 'checkin' ? t('Checking in…') : checkedIn === true ? t('Checked in') : t('Check in today')}
        </button>
      </section>

      <div className="wallet-columns">
        <section className="card">
          <div className="wallet-section-heading">
            <h2>{t('Add wallet credit')}</h2>
            <button type="button" className="link" onClick={showHistory}>{t('Payment history')}</button>
          </div>
          {infoLoading && <p className="muted" role="status">{t('Loading payment options…')}</p>}
          {infoError && (
            <div className="error-panel" role="alert">
              <p>{t('Unable to load payment options.')}</p>
              <button type="button" className="link" onClick={() => loadInfo()}>{t('Try again')}</button>
            </div>
          )}
          {!infoLoading && !infoError && !complianceReady && (
            <p className="muted">{complianceUnavailable}</p>
          )}
          {complianceReady && info && options.length === 0 && info.creemProducts.length === 0 && (
            <p className="muted">{t('No online payment methods are configured.')}</p>
          )}
          {!infoLoading && !infoError && options.length > 0 && (
            <form onSubmit={reviewPayment}>
              <fieldset className="wallet-payment-options">
                <legend>{t('Payment method')}</legend>
                {options.map((option) => {
                  const belowMinimum = enteredPaymentAmount === null || enteredPaymentAmount < option.minimum;
                  const minimumText = number(option.minimum, i18n.language);
                  return (
                    <label
                      key={option.id}
                      data-selected={selectedPayment === option.id}
                      data-disabled={belowMinimum}
                      title={belowMinimum ? t('Requires at least {{amount}}', { amount: minimumText }) : undefined}
                    >
                      <input
                        type="radio"
                        name="wallet-payment"
                        value={option.id}
                        checked={selectedPayment === option.id}
                        disabled={Boolean(busy) || belowMinimum}
                        aria-label={belowMinimum
                          ? `${option.label}. ${t('Requires at least {{amount}}', { amount: minimumText })}`
                          : option.label}
                        onChange={() => { setSelectedPayment(option.id); setPreview(null); setCheckout(null); }}
                      />
                      <span>{option.label}</span>
                      <small>{belowMinimum
                        ? t('Requires at least {{amount}}', { amount: minimumText })
                        : t('Minimum {{amount}}', { amount: minimumText })}</small>
                    </label>
                  );
                })}
              </fieldset>
              {info && info.presets.length > 0 && (
                <div className="wallet-presets" aria-label={t('Preset amounts')}>
                  {info.presets.map((preset) => (
                    <button
                      type="button"
                      className="button"
                      key={preset}
                      onClick={() => { setAmount(String(preset)); setPreview(null); setCheckout(null); }}
                    >
                      {number(preset, i18n.language)}
                      {info.discounts[preset] !== undefined && info.discounts[preset] !== 1
                        ? ` × ${info.discounts[preset]}`
                        : ''}
                    </button>
                  ))}
                </div>
              )}
              <label>
                {t('Top-up amount')}
                <input
                  type="text"
                  inputMode="numeric"
                  pattern="[1-9][0-9]*"
                  maxLength={16}
                  value={amount}
                  onChange={(event) => { setAmount(event.target.value); setPreview(null); setCheckout(null); }}
                  required
                />
              </label>
              {selectedOption && selectedPaymentBelowMinimum && (
                <div className="wallet-minimum-help" role="status">
                  <span>{t('Requires at least {{amount}}', { amount: number(selectedOption.minimum, i18n.language) })}</span>
                  <button
                    type="button"
                    className="link"
                    disabled={Boolean(busy)}
                    onClick={() => { setAmount(String(selectedOption.minimum)); setPreview(null); setCheckout(null); }}
                  >
                    {t('Use provider minimum')}
                  </button>
                </div>
              )}
              <button type="submit" disabled={Boolean(busy) || selectedPaymentBelowMinimum}>{busy === 'preview' ? t('Calculating…') : t('Review payment')}</button>
            </form>
          )}
          {complianceReady && preview?.signature === previewSignature && selectedOption && (
            <div className="wallet-review" aria-live="polite">
              <p>{t('Payable amount')}: <strong>{preview.payable}</strong></p>
              <button type="button" onClick={startSelectedCheckout} disabled={Boolean(busy)}>
                {busy === 'checkout' ? t('Creating checkout…') : t('Create checkout')}
              </button>
            </div>
          )}
          {complianceReady && checkout && (
            <div className="wallet-checkout" role="status">
              <strong>{t('Checkout ready')}</strong>
              {checkout.orderId && <span className="muted">{t('Order {{id}}', { id: checkout.orderId })}</span>}
              <CheckoutControl checkout={checkout} label={t('Open secure checkout')} />
            </div>
          )}
          {complianceReady && info?.creemEnabled && info.creemProducts.length > 0 && (
            <div className="wallet-products">
              <h3>{t('Fixed-price products')}</h3>
              {info.creemProducts.map((product) => (
                <article key={product.productId}>
                  <div>
                    <strong>{product.name}</strong>
                    <span className="muted">{formatQuotaForDisplay(product.quota, info, i18n.language)} · {product.price.toFixed(2)} {product.currency}</span>
                  </div>
                  <button type="button" className="link" onClick={() => buyCreemProduct(product.productId, product.name)} disabled={Boolean(busy)}>
                    {t('Buy')}
                  </button>
                </article>
              ))}
            </div>
          )}
        </section>

        <section className="card">
          <h2>{t('Redeem a code')}</h2>
          {infoLoading ? (
            <p className="muted" role="status">{t('Loading payment options…')}</p>
          ) : !redemptionReady ? (
            <p className="muted">{t('Code redemption is currently unavailable.')}</p>
          ) : (
            <form onSubmit={doRedeem}>
              <label>
                {t('Redemption code')}
                <input
                  value={redeemCode}
                  onChange={(event) => setRedeemCode(event.target.value)}
                  maxLength={128}
                  autoComplete="off"
                  required
                />
              </label>
              <button type="submit" disabled={Boolean(busy) || redeemCode.trim() === ''}>
                {busy === 'redeem' ? t('Redeeming…') : t('Redeem code')}
              </button>
            </form>
          )}
          {redemptionReady && info?.topUpLink && (
            <p><a href={info.topUpLink} target="_blank" rel="noopener noreferrer">{t('Get a redemption code')}</a></p>
          )}
        </section>
      </div>

      <section
        ref={historySectionRef}
        id="wallet-payment-history"
        className="card wallet-history-target"
        aria-labelledby="wallet-payment-history-heading"
        tabIndex={-1}
      >
        <div className="wallet-section-heading">
          <div>
            <h2 id="wallet-payment-history-heading">{t('Payment history')}</h2>
            <p className="muted">{t('Recent wallet orders from the last 30 days.')}</p>
          </div>
          <button type="button" className="link" onClick={() => loadOrders(historyPage, historyPageSize, historyKeyword)} disabled={ordersLoading}>{t('Refresh')}</button>
        </div>
        <form className="inline-form wallet-search wallet-history-controls" onSubmit={searchHistory}>
          <label>{t('Search orders')}<input value={historyKeyword} onChange={(event) => setHistoryKeyword(event.target.value)} maxLength={255} /></label>
          <label>
            {t('Rows per page')}
            <select
              aria-label={t('Rows per page')}
              value={historyPageSize}
              disabled={ordersLoading}
              onChange={(event) => changeHistoryPageSize(event.target.value)}
            >
              {HISTORY_PAGE_SIZES.map((size) => (
                <option key={size} value={size}>{t('{{count}} per page', { count: size })}</option>
              ))}
            </select>
          </label>
          <button type="submit" disabled={ordersLoading}>{t('Search')}</button>
        </form>
        {ordersLoading && <p className="muted" role="status">{t('Loading payment history…')}</p>}
        {ordersError && (
          <div className="error-panel" role="alert">
            <p>{t('Unable to load payment history.')}</p>
            <button type="button" className="link" onClick={() => loadOrders(historyPage, historyPageSize, historyKeyword)}>{t('Try again')}</button>
          </div>
        )}
        {!ordersLoading && !ordersError && orders?.items.length === 0 && <p className="muted">{t('No wallet orders match this search.')}</p>}
        {!ordersLoading && !ordersError && orders && orders.items.length > 0 && (
          <div className="wallet-table-wrap">
            <table className="wallet-table">
              <caption className="sr-only">{t('Payment history')}</caption>
              <thead><tr><th>{t('Order')}</th>{isAdmin && <th>{t('User ID')}</th>}<th>{t('Provider')}</th><th>{t('Amount')}</th><th>{t('Paid amount')}</th><th>{t('Status')}</th><th>{t('Created')}</th>{isAdmin && <th>{t('Actions')}</th>}</tr></thead>
              <tbody>
                {orders.items.map((order) => (
                  <tr key={order.id}>
                    <td data-label={t('Order')}>
                      <div className="wallet-copy-value">
                        <code>{order.tradeNo}</code>
                        <button
                          type="button"
                          className="link"
                          aria-label={t('Copy order number')}
                          onClick={() => { void copyValue(order.tradeNo, `order:${order.id}`, t('Order number copied.')); }}
                        >
                          {copiedKey === `order:${order.id}` ? t('Copied') : t('Copy')}
                        </button>
                      </div>
                    </td>
                    {isAdmin && <td data-label={t('User ID')}>
                      <div className="wallet-copy-value">
                        <span>{number(order.userId, i18n.language)}</span>
                        <button
                          type="button"
                          className="link"
                          aria-label={t('Copy user ID')}
                          onClick={() => { void copyValue(String(order.userId), `user:${order.id}`, t('User ID copied.')); }}
                        >
                          {copiedKey === `user:${order.id}` ? t('Copied') : t('Copy')}
                        </button>
                      </div>
                    </td>}
                    <td data-label={t('Provider')}>{orderProviderText(order, info)}</td>
                    <td data-label={t('Amount')}>{info
                      ? formatCreditForDisplay(order.amount, info, i18n.language)
                      : number(order.amount, i18n.language)}</td>
                    <td data-label={t('Paid amount')}>{money(order.money, i18n.language)}</td>
                    <td data-label={t('Status')}><span className={`status status-${order.status}`}>{t(order.status)}</span></td>
                    <td data-label={t('Created')}>
                      <span>{dateTime(order.createdAt, i18n.language)}</span>
                      {order.completedAt > 0 && (
                        <small className="muted">{t('Completed')}: {dateTime(order.completedAt, i18n.language)}</small>
                      )}
                    </td>
                    {isAdmin && (
                      <td data-label={t('Actions')}>
                        {order.status === 'pending' && (
                          <button
                            type="button"
                            className="link"
                            disabled={Boolean(busy)}
                            onClick={() => { void completeOrder(order); }}
                          >
                            {busy === `complete-order:${order.id}` ? t('Processing...') : t('Complete Order')}
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
            <nav className="pagination" aria-label={t('Payment history pages')}>
              <button type="button" className="link" disabled={orders.page <= 1 || ordersLoading} onClick={() => loadOrders(orders.page - 1, historyPageSize, historyKeyword)}>{t('Previous')}</button>
              <span>
                {t('Showing {{start}}–{{end}} of {{total}}', {
                  start: (orders.page - 1) * orders.pageSize + 1,
                  end: Math.min(orders.page * orders.pageSize, orders.total),
                  total: orders.total,
                })} · {t('Page {{page}} of {{pages}}', { page: orders.page, pages: Math.max(1, Math.ceil(orders.total / orders.pageSize)) })}
              </span>
              <button type="button" className="link" disabled={orders.page * orders.pageSize >= orders.total || ordersLoading} onClick={() => loadOrders(orders.page + 1, historyPageSize, historyKeyword)}>{t('Next')}</button>
            </nav>
          </div>
        )}
      </section>

      <div className="wallet-columns">
        <section className="card">
          <h2>{t('Affiliate rewards')}</h2>
          {affiliateLoading && <p className="muted" role="status">{t('Loading affiliate rewards…')}</p>}
          {affiliateError && (
            <div className="error-panel" role="alert"><p>{t('Unable to load affiliate rewards.')}</p><button type="button" className="link" onClick={() => loadAffiliate()}>{t('Try again')}</button></div>
          )}
          {!affiliateLoading && !affiliateError && affiliate && (
            <>
              <dl className="kv">
                <dt>{t('Referral code')}</dt><dd><code>{affiliate.code}</code></dd>
                <dt>{t('Referred users')}</dt><dd>{number(affiliate.count, i18n.language)}</dd>
                <dt>{t('Available rewards')}</dt><dd>{info
                  ? formatQuotaForDisplay(affiliate.availableQuota, info, i18n.language)
                  : number(affiliate.availableQuota, i18n.language)}</dd>
                <dt>{t('Lifetime rewards')}</dt><dd>{info
                  ? formatQuotaForDisplay(affiliate.lifetimeQuota, info, i18n.language)
                  : number(affiliate.lifetimeQuota, i18n.language)}</dd>
              </dl>
              <label>
                {t('Referral link')}
                <span className="wallet-copy-value wallet-referral-link">
                  <input value={referralLink} readOnly aria-label={t('Referral link')} />
                  <button
                    type="button"
                    className="button"
                    aria-label={t('Copy referral link')}
                    onClick={() => { void copyValue(referralLink, 'referral', t('Referral link copied.')); }}
                  >
                    {copiedKey === 'referral' ? t('Copied') : t('Copy')}
                  </button>
                </span>
              </label>
              {complianceReady ? (
                <form onSubmit={doAffiliateTransfer}>
                  <label>
                    {info
                      ? t('Amount to transfer ({{unit}})', { unit: quotaDisplayUnit(info) })
                      : t('Transfer amount')}
                    <input
                      type="text"
                      inputMode={info?.quotaDisplayType === 'tokens' ? 'numeric' : 'decimal'}
                      pattern={info?.quotaDisplayType === 'tokens' ? '[1-9][0-9]*' : '(?:0|[1-9][0-9]*)(?:\\.[0-9]{1,6})?'}
                      maxLength={32}
                      value={affiliateAmount}
                      onChange={(event) => setAffiliateAmount(event.target.value)}
                      required
                    />
                  </label>
                  {info && (
                    <div className="wallet-transfer-summary">
                      <span className="muted">{t('Minimum transfer')}: {formatQuotaForDisplay(info.quotaPerUnit, info, i18n.language)}</span>
                      <span className="muted">{t('Available')}: {formatQuotaForDisplay(affiliate.availableQuota, info, i18n.language)}</span>
                    </div>
                  )}
                  {info && affiliate.availableQuota < info.quotaPerUnit && (
                    <p className="muted">{t('At least {{minimum}} is required before rewards can be transferred.', {
                      minimum: formatQuotaForDisplay(info.quotaPerUnit, info, i18n.language),
                    })}</p>
                  )}
                  <button type="submit" disabled={Boolean(busy) || !affiliateTransferReady}>{busy === 'affiliate' ? t('Transferring…') : t('Transfer to wallet')}</button>
                </form>
              ) : <p className="muted">{complianceUnavailable}</p>}
            </>
          )}
        </section>

        <section className="card">
          <h2>{t('Subscriptions')}</h2>
          {subscriptionsLoading && <p className="muted" role="status">{t('Loading subscriptions…')}</p>}
          {subscriptionsError && (
            <div className="error-panel" role="alert"><p>{t('Unable to load subscriptions.')}</p><button type="button" className="link" onClick={() => loadSubscriptions()}>{t('Try again')}</button></div>
          )}
          {!subscriptionsLoading && !subscriptionsError && subscriptions && (
            <>
              <label>
                {t('Billing preference')}
                <select value={effectiveBillingPreference ?? 'wallet_first'} disabled={Boolean(busy)} onChange={(event) => changeBillingPreference(event.target.value as BillingPreference)}>
                  <option value="subscription_first" disabled={subscriptions.active.length === 0}>{t('Use subscription first')}</option>
                  <option value="wallet_first">{t('Use wallet first')}</option>
                  <option value="subscription_only" disabled={subscriptions.active.length === 0}>{t('Subscription only')}</option>
                  <option value="wallet_only">{t('Wallet only')}</option>
                </select>
              </label>
              {subscriptions.active.length === 0 && (
                <>
                  <p className="muted">{t('No active subscriptions.')}</p>
                  {(subscriptions.billingPreference === 'subscription_first'
                    || subscriptions.billingPreference === 'subscription_only') && (
                    <p className="muted" role="status">{t(
                      'Preference saved as {{preference}}, but no active subscription. Wallet will be used automatically.',
                      {
                        preference: subscriptions.billingPreference === 'subscription_only'
                          ? t('Subscription only')
                          : t('Use subscription first'),
                      },
                    )}</p>
                  )}
                </>
              )}
              {subscriptions.active.length > 0 && (
                <ul className="wallet-subscriptions">
                  {subscriptions.active.map((subscription) => (
                    <SubscriptionRecordItem
                      key={subscription.id}
                      subscription={subscription}
                      plan={plansById.get(subscription.planId)}
                      info={info}
                    />
                  ))}
                </ul>
              )}
              <h3>{t('Subscription history')}</h3>
              {historicalSubscriptions.length === 0 ? (
                <p className="muted">{t('No previous subscriptions.')}</p>
              ) : (
                <ul className="wallet-subscriptions wallet-subscription-history">
                  {historicalSubscriptions.map((subscription) => (
                    <SubscriptionRecordItem
                      key={subscription.id}
                      subscription={subscription}
                      plan={plansById.get(subscription.planId)}
                      info={info}
                    />
                  ))}
                </ul>
              )}
              {plans.length === 0 ? <p className="muted">{t('No subscription plans are available.')}</p> : (
                <div className="wallet-products">
                  <h3>{t('Available plans')}</h3>
                  {!complianceReady && <p className="muted">{complianceUnavailable}</p>}
                  {plans.map((plan) => {
                    const state = planPurchaseState(plan);
                    const purchasesRemaining = plan.maxPurchasePerUser > 0
                      ? Math.max(0, plan.maxPurchasePerUser - state.purchaseCount)
                      : null;
                    let walletUnavailableReason = '';
                    if (state.limitReached) walletUnavailableReason = t('Purchase limit reached.');
                    else if (!plan.allowBalancePay) walletUnavailableReason = t('Wallet purchase is disabled for this plan.');
                    else if (state.balanceCost === null) walletUnavailableReason = t('Unable to calculate the required wallet balance.');
                    else if (state.insufficientBalance) walletUnavailableReason = t('Insufficient wallet balance.');
                    return (
                      <article key={plan.id}>
                        <div>
                          <strong>{plan.title}</strong>
                          <span className="muted">{plan.subtitle}</span>
                          <span>{plan.priceAmount} {plan.currency}</span>
                          <span className="muted">
                            {t('Quota')}: {plan.totalAmount === 0
                              ? t('Unlimited')
                              : info
                                ? formatQuotaForDisplay(plan.totalAmount, info, i18n.language)
                                : number(plan.totalAmount, i18n.language)}
                          </span>
                          <span className="muted">
                            {t('Validity')}: {plan.durationUnit === 'custom'
                              ? `${number(plan.customSeconds, i18n.language)} ${t('Seconds')}`
                              : `${number(plan.durationValue, i18n.language)} ${t(durationUnitKeys[plan.durationUnit])}`}
                          </span>
                          <span className="muted">
                            {t('Quota reset period')}: {plan.quotaResetPeriod === 'custom'
                              ? `${t('Custom')} · ${number(plan.quotaResetCustomSeconds, i18n.language)} ${t('Seconds')}`
                              : t(resetPeriodKeys[plan.quotaResetPeriod])}
                          </span>
                          <span className="muted">
                            {t('Purchase limit per user')}: {plan.maxPurchasePerUser === 0
                              ? t('Unlimited')
                              : number(plan.maxPurchasePerUser, i18n.language)}
                          </span>
                          <span className="muted">{t('Purchases used')}: {plan.maxPurchasePerUser === 0
                            ? number(state.purchaseCount, i18n.language)
                            : t('{{count}} of {{limit}}', {
                              count: state.purchaseCount,
                              limit: plan.maxPurchasePerUser,
                            })}</span>
                          {purchasesRemaining !== null && !state.limitReached && (
                            <span className="muted">{t('{{count}} purchases remaining', { count: purchasesRemaining })}</span>
                          )}
                          {plan.upgradeGroup !== '' && (
                            <span className="muted">{t('Upgrade group')}: {plan.upgradeGroup}</span>
                          )}
                          {plan.downgradeGroup !== '' && (
                            <span className="muted">{t('Downgrade group')}: {plan.downgradeGroup}</span>
                          )}
                          <span className="muted">{t('Wallet overflow')}: {plan.allowWalletOverflow ? t('Enabled') : t('Disabled')}</span>
                          {info && state.balanceCost !== null && (
                            <div className="wallet-plan-balance">
                              <span>{t('Required wallet balance')}: {formatQuotaForDisplay(state.balanceCost, info, i18n.language)}</span>
                              <span>{t('Available wallet balance')}: {formatQuotaForDisplay(walletUser.quota, info, i18n.language)}</span>
                            </div>
                          )}
                          {state.limitReached && (
                            <span className="error" role="status">{t('Purchase limit reached.')} ({state.purchaseCount}/{plan.maxPurchasePerUser})</span>
                          )}
                          {!state.limitReached && walletUnavailableReason !== '' && (
                            <span className="muted">{walletUnavailableReason}</span>
                          )}
                        </div>
                        {complianceReady && <div className="wallet-plan-actions">
                          <span className="muted wallet-plan-action-label">{t('Payment options')}</span>
                          <button
                            type="button"
                            className="link"
                            disabled={Boolean(busy) || state.limitReached || !state.balancePaymentAvailable}
                            title={walletUnavailableReason || undefined}
                            onClick={() => buySubscription(plan)}
                          >
                            {busy === `plan:${plan.id}` ? t('Buying…') : t('Buy with wallet')}
                          </button>
                          {info?.stripeEnabled && plan.stripePriceId !== '' && (
                            <button type="button" className="link" disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'stripe')}>{t('Pay with {{method}}', { method: 'Stripe' })}</button>
                          )}
                          {info?.creemEnabled && plan.creemProductId !== '' && (
                            <button type="button" className="link" disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'creem')}>{t('Pay with {{method}}', { method: 'Creem' })}</button>
                          )}
                          {info?.waffoPancakeEnabled && plan.waffoPancakeProductId !== '' && (
                            <button type="button" className="link" disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'waffo-pancake')}>{t('Pay with {{method}}', { method: 'Waffo Pancake' })}</button>
                          )}
                          {info?.onlineEnabled && info.paymentMethods.filter((method) => method.type !== 'stripe' && method.type !== 'waffo' && method.type !== 'waffo_pancake').map((method) => (
                            <button type="button" className="link" key={method.type} disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'epay', method.type)}>{t('Pay with {{method}}', { method: method.name })}</button>
                          ))}
                          {!plan.allowBalancePay && !state.hostedPaymentAvailable && (
                            <span className="muted">{t('No purchase method is currently available for this plan.')}</span>
                          )}
                        </div>}
                      </article>
                    );
                  })}
                </div>
              )}
              {complianceReady && subscriptionCheckout && (
                <div className="wallet-checkout" role="status">
                  <strong>{t('Subscription checkout ready')}</strong>
                  {subscriptionCheckout.orderId && <span className="muted">{t('Order {{id}}', { id: subscriptionCheckout.orderId })}</span>}
                  <CheckoutControl checkout={subscriptionCheckout} label={t('Open secure checkout')} />
                </div>
              )}
            </>
          )}
        </section>
      </div>

      <footer className="footer">TokenRouter — {t('independent AI API gateway.')}</footer>
    </main>
  );
}
