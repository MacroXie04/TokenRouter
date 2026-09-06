import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { TURNSTILE_DISABLED } from "../security";
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
  TurnstileRequiredError,
  updateBillingPreference,
  type AffiliateSummary,
  type BillingPreference,
  type CheckoutRequest,
  type CheckoutResult,
  type SubscriptionCheckoutRequest,
  type SubscriptionPlan,
  type SubscriptionSummary,
  type UserSubscription,
  type WalletOrderPage,
  type WalletTopUpInfo,
} from './wallet-api';
import {
  formatQuotaForDisplay,
  HISTORY_PAGE_SIZES,
  integerInput,
  number,
  paymentOptions,
  subscriptionBalanceCost,
  transferInputToQuota,
  type Notice,
  type PaymentOption,
} from './wallet-presentation';
import type { WalletViewProps } from './wallet-view-props';

export function useWalletController({
  user, onNavigate, onUserChange, onLogout,
  turnstileConfig = TURNSTILE_DISABLED, initialShowHistory = false,
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

  return {
    affiliate, affiliateAmount, affiliateError, affiliateLoading,
    affiliateTransferReady, amount, busy, buyCreemProduct,
    buySubscription, changeBillingPreference, changeHistoryPageSize, checkedIn,
    checkout, completeOrder, complianceReady, complianceUnavailable,
    copiedKey, copyValue, doAffiliateTransfer, doCheckIn,
    doRedeem, effectiveBillingPreference, enteredPaymentAmount, historicalSubscriptions,
    historyKeyword, historyPage, historyPageSize, historySectionRef,
    i18n, info, infoError, infoLoading,
    isAdmin, loadAffiliate, loadInfo, loadOrders,
    loadSubscriptions, notice, onNavigate, options,
    orders, ordersError, ordersLoading, planPurchaseState,
    plans, plansById, preview, previewSignature,
    redeemCode, redemptionReady, referralLink, reviewPayment,
    searchHistory, selectedOption, selectedPayment, selectedPaymentBelowMinimum,
    setAffiliateAmount, setAmount, setCheckout, setHistoryKeyword,
    setNotice, setPreview, setRedeemCode, setSelectedPayment,
    setTurnstileOpen, setTurnstileWidgetKey, showHistory, signOut,
    startSelectedCheckout, startSubscriptionCheckout, subscriptionCheckout, subscriptions,
    subscriptionsError, subscriptionsLoading, t, turnstileConfig,
    turnstileOpen, turnstileWidgetKey, walletQuotaText, walletUser,
  };
}

export type WalletViewController = ReturnType<typeof useWalletController>;
