import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
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
  parseAffiliateResponse,
  parseAdminWalletOrderPageResponse,
  parseSubscriptionPlansResponse,
  parseSubscriptionSummaryResponse,
  parseTopUpInfoResponse,
  parseWalletOrderPageResponse,
  parseWalletUserResponse,
  purchaseSubscriptionWithBalance,
  redeemTopUpCode,
  signOutWallet,
  transferAffiliateQuota,
  updateBillingPreference,
  WalletContractError,
} from './wallet-api';

vi.mock('../../api', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn() },
}));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);
const mockedPut = vi.mocked(api.put);

const limits = { maxContentLength: 512 * 1024, maxBodyLength: 512 * 1024 };

const infoEnvelope = {
  success: true,
  data: {
    enable_online_topup: true,
    enable_stripe_topup: true,
    enable_creem_topup: true,
    enable_waffo_topup: true,
    enable_waffo_pancake_topup: true,
    enable_redemption: true,
    payment_compliance_confirmed: true,
    payment_compliance_terms_version: 'v1',
    pay_methods: [
      { name: 'Card gateway', type: 'card', min_topup: '20', ignored_secret: 'nope' },
      { name: 'Stripe', type: 'stripe', color: '#635bff' },
    ],
    waffo_pay_methods: [{ name: 'Apple Pay', icon: '/pay.png', payMethodType: 'APPLEPAY', payMethodName: 'APPLEPAY' }],
    creem_products: JSON.stringify([{ productId: 'prod_1', name: 'Starter', price: 9.99, currency: 'USD', quota: 5000 }]),
    min_topup: 10,
    stripe_min_topup: 15,
    waffo_min_topup: 25,
    waffo_pancake_min_topup: 30,
    amount_options: [20, 50],
    discount: { 50: 0.9 },
    topup_link: 'https://billing.example.test/codes',
    quota_display_type: 'currency',
    quota_per_unit: 500_000,
    usd_exchange_rate: 7.3,
    currency_symbol: '$',
    currency_exchange_rate: 1,
  },
};

const userEnvelope = {
  success: true,
  data: {
    id: 7,
    username: 'wallet-user',
    display_name: 'Wallet User',
    role: 1,
    group: 'default',
    quota: 10_000,
    used_quota: 2_000,
    request_count: 3,
    email: 'wallet@example.test',
    password: 'must-not-be-selected',
  },
};

const orderEnvelope = {
  success: true,
  data: {
    page: 1,
    page_size: 10,
    total: 1,
    items: [{
      id: 5,
      user_id: 7,
      amount: 5000,
      money: 9.99,
      trade_no: 'ref_order_5',
      payment_method: 'stripe',
      payment_provider: 'stripe',
      create_time: 1_700_000_000,
      complete_time: 0,
      status: 'pending',
      checkout_request: 'must-not-be-selected',
    }],
  },
};

const plansEnvelope = {
  success: true,
  data: [{ plan: {
    id: 4,
    title: 'Monthly',
    subtitle: 'Starter plan',
    price_amount: '9.99',
    currency: 'USD',
    duration_unit: 'month',
    duration_value: 1,
    custom_seconds: 0,
    total_amount: 10000,
    allow_balance_pay: true,
    allow_wallet_overflow: true,
    max_purchase_per_user: 2,
    upgrade_group: 'pro',
    downgrade_group: 'default',
    quota_reset_period: 'monthly',
    quota_reset_custom_seconds: 0,
    stripe_price_id: 'price_1',
    creem_product_id: 'prod_1',
    waffo_pancake_product_id: 'PROD_AbCdEfGhIjKlMnOpQrStUv',
  } }],
};

const subscriptionEnvelope = {
  success: true,
  data: {
    billing_preference: 'subscription_first',
    subscriptions: [{ subscription: {
      id: 8,
      user_id: 7,
      plan_id: 4,
      amount_total: 10000,
      amount_used: 100,
      start_time: 1_700_000_000,
      end_time: 1_800_000_000,
      next_reset_time: 1_700_086_400,
      last_reset_time: 1_700_000_000,
      upgrade_group: 'pro',
      downgrade_group: 'default',
      source: 'balance',
      status: 'active',
      allow_wallet_overflow: true,
    } }],
    all_subscriptions: [{ subscription: {
      id: 7,
      user_id: 7,
      plan_id: 4,
      amount_total: 10000,
      amount_used: 10000,
      start_time: 1_600_000_000,
      end_time: 1_650_000_000,
      next_reset_time: 0,
      last_reset_time: 1_600_000_000,
      upgrade_group: 'pro',
      downgrade_group: 'default',
      source: 'stripe',
      status: 'expired',
      allow_wallet_overflow: false,
    } }],
  },
};

const validEpayFields = (overrides: Record<string, unknown> = {}) => ({
  pid: 'merchant-7',
  type: 'card',
  out_trade_no: 'USR7NOabc123',
  notify_url: 'https://router.example.test/api/user/epay/notify',
  return_url: 'https://router.example.test/usage-logs',
  name: 'TUC20',
  money: '12.34',
  device: 'pc',
  sign_type: 'MD5',
  sign: '0123456789abcdef0123456789abcdef',
  ...overrides,
});

beforeEach(() => {
  vi.resetAllMocks();
});

describe('wallet response contracts', () => {
  it('selects the bounded top-up catalog, products, orders, user, affiliate, and subscriptions', () => {
    expect(parseTopUpInfoResponse(infoEnvelope)).toMatchObject({
      onlineEnabled: true,
      stripeEnabled: true,
      waffoPancakeEnabled: true,
      minimum: 10,
      paymentMethods: [{ name: 'Card gateway', type: 'card', minimum: 20 }, { name: 'Stripe', type: 'stripe', minimum: null }],
      waffoMethods: [{ name: 'Apple Pay', payMethodType: 'APPLEPAY', payMethodName: 'APPLEPAY' }],
      creemProducts: [{ productId: 'prod_1', quota: 5000 }],
      waffoPancakeMinimum: 30,
      discounts: { 50: 0.9 },
      quotaDisplayType: 'currency',
      quotaPerUnit: 500_000,
      usdExchangeRate: 7.3,
      currencySymbol: '$',
      currencyExchangeRate: 1,
    });
    expect(parseTopUpInfoResponse({
      ...infoEnvelope,
      data: { ...infoEnvelope.data, topup_link: 'http://localhost:3000/topup' },
    }).topUpLink).toBe('http://localhost:3000/topup');
    expect(parseWalletOrderPageResponse(orderEnvelope, 7).items[0]).toEqual({
      id: 5,
      userId: 7,
      amount: 5000,
      money: 9.99,
      tradeNo: 'ref_order_5',
      paymentMethod: 'stripe',
      paymentProvider: 'stripe',
      createdAt: 1_700_000_000,
      completedAt: 0,
      status: 'pending',
    });
    expect(parseAdminWalletOrderPageResponse({
      ...orderEnvelope,
      data: {
        ...orderEnvelope.data,
        items: [{ ...orderEnvelope.data.items[0], user_id: 42 }],
      },
    }).items[0].userId).toBe(42);
    expect(parseWalletUserResponse(userEnvelope)).not.toHaveProperty('password');
    expect(parseAffiliateResponse({ success: true, data: { aff_code: 'AFF_1', aff_count: 2, aff_quota: 500, aff_history_quota: 900 } })).toEqual({
      code: 'AFF_1', count: 2, availableQuota: 500, lifetimeQuota: 900,
    });
    expect(parseSubscriptionPlansResponse(plansEnvelope)[0]).toMatchObject({
      title: 'Monthly',
      durationUnit: 'month',
      quotaResetPeriod: 'monthly',
      maxPurchasePerUser: 2,
      upgradeGroup: 'pro',
      allowWalletOverflow: true,
    });
    expect(parseSubscriptionSummaryResponse(subscriptionEnvelope, 7)).toMatchObject({
      active: [{
        id: 8,
        nextResetTime: 1_700_086_400,
        lastResetTime: 1_700_000_000,
        upgradeGroup: 'pro',
        downgradeGroup: 'default',
        source: 'balance',
      }],
      history: [{ id: 7, status: 'expired', nextResetTime: 0, source: 'stripe' }],
    });
  });

  it('rejects oversized, cross-user, invalid URL, unsafe number, and malformed product responses', () => {
    expect(() => parseWalletOrderPageResponse({
      ...orderEnvelope,
      data: { ...orderEnvelope.data, items: [{ ...orderEnvelope.data.items[0], user_id: 8 }] },
    }, 7)).toThrow(WalletContractError);
    expect(() => parseWalletOrderPageResponse({
      ...orderEnvelope,
      data: { ...orderEnvelope.data, total: Number.MAX_SAFE_INTEGER + 1 },
    }, 7)).toThrow(WalletContractError);
    expect(() => parseTopUpInfoResponse({
      ...infoEnvelope,
      data: { ...infoEnvelope.data, topup_link: 'javascript:alert(1)' },
    })).toThrow(WalletContractError);
    expect(() => parseTopUpInfoResponse({
      ...infoEnvelope,
      data: { ...infoEnvelope.data, creem_products: '{not-json' },
    })).toThrow(WalletContractError);
    expect(() => parseTopUpInfoResponse({ success: true, data: { padding: 'x'.repeat(600_000) } }))
      .toThrow(WalletContractError);
    expect(() => parseSubscriptionSummaryResponse({
      ...subscriptionEnvelope,
      data: { ...subscriptionEnvelope.data, subscriptions: [{ subscription: { ...subscriptionEnvelope.data.subscriptions[0].subscription, user_id: 99 } }] },
    }, 7)).toThrow(WalletContractError);
    expect(() => parseSubscriptionPlansResponse({
      ...plansEnvelope,
      data: [{ plan: { ...plansEnvelope.data[0].plan, duration_unit: 'forever' } }],
    })).toThrow(WalletContractError);
    expect(() => parseSubscriptionPlansResponse({
      ...plansEnvelope,
      data: [{ plan: { ...plansEnvelope.data[0].plan, quota_reset_period: 'sometimes' } }],
    })).toThrow(WalletContractError);

    for (const invalidMetadata of [
      { quota_display_type: 'credits' },
      { quota_display_type: null },
      { quota_per_unit: 0 },
      { quota_per_unit: 1.5 },
      { quota_per_unit: '500000' },
      { quota_per_unit: Number.MAX_SAFE_INTEGER + 1 },
      { usd_exchange_rate: 0 },
      { usd_exchange_rate: -1 },
      { usd_exchange_rate: Number.NaN },
      { usd_exchange_rate: '7.3' },
      { usd_exchange_rate: Number.POSITIVE_INFINITY },
      { usd_exchange_rate: 1_000_001 },
      { currency_symbol: '' },
      { currency_symbol: 'x'.repeat(33) },
      { currency_exchange_rate: 0 },
      { currency_exchange_rate: Number.NaN },
      { currency_exchange_rate: Number.POSITIVE_INFINITY },
      { currency_exchange_rate: 1_000_001 },
    ]) {
      expect(() => parseTopUpInfoResponse({
        ...infoEnvelope,
        data: { ...infoEnvelope.data, ...invalidMetadata },
      })).toThrow(WalletContractError);
    }
  });
});

describe('wallet read transports', () => {
  it('uses exact bounded user routes, pagination, search, and abort signals', async () => {
    mockedGet.mockResolvedValueOnce({ data: infoEnvelope });
    mockedGet.mockResolvedValueOnce({ data: userEnvelope });
    mockedGet.mockResolvedValueOnce({ data: { success: true, data: { checked_in: false } } });
    mockedGet.mockResolvedValueOnce({ data: orderEnvelope });
    mockedGet.mockResolvedValueOnce({ data: { success: true, data: { aff_code: 'A', aff_count: 0, aff_quota: 0, aff_history_quota: 0 } } });
    mockedGet.mockResolvedValueOnce({ data: plansEnvelope });
    mockedGet.mockResolvedValueOnce({ data: subscriptionEnvelope });
    const signal = new AbortController().signal;

    await getTopUpInfo(signal);
    await getWalletUser(signal);
    await expect(getCheckInStatus(signal)).resolves.toBe(false);
    await getWalletOrders(7, 1, 10, 'ref_%', signal);
    await getAffiliateSummary(signal);
    await getSubscriptionPlans(signal);
    await getSubscriptionSummary(7, signal);

    expect(mockedGet).toHaveBeenNthCalledWith(1, '/user/topup/info', { signal, ...limits });
    expect(mockedGet).toHaveBeenNthCalledWith(2, '/user/self', { signal, ...limits });
    expect(mockedGet).toHaveBeenNthCalledWith(3, '/user/checkin/status', { signal, ...limits });
    expect(mockedGet).toHaveBeenNthCalledWith(4, '/user/topup/self', {
      params: { p: 1, page_size: 10, keyword: 'ref_%' }, signal, ...limits,
    });
    expect(mockedGet).toHaveBeenNthCalledWith(5, '/user/aff', { signal, ...limits });
    expect(mockedGet).toHaveBeenNthCalledWith(6, '/subscription/plans', { signal, ...limits });
    expect(mockedGet).toHaveBeenNthCalledWith(7, '/subscription/self', { signal, ...limits });
  });

  it('loads bounded cross-user top-up history only through the admin route', async () => {
    const adminEnvelope = {
      ...orderEnvelope,
      data: {
        ...orderEnvelope.data,
        items: [{ ...orderEnvelope.data.items[0], user_id: 42 }],
      },
    };
    mockedGet.mockResolvedValueOnce({ data: adminEnvelope });
    const signal = new AbortController().signal;

    await expect(getAdminWalletOrders(2, 20, 'ref_42', signal)).resolves.toMatchObject({
      items: [{ userId: 42, tradeNo: 'ref_order_5' }],
    });
    expect(mockedGet).toHaveBeenCalledWith('/user/topup', {
      params: { p: 2, page_size: 20, keyword: 'ref_42' }, signal, ...limits,
    });
  });
});

describe('wallet mutation transports', () => {
  it('calculates all supported variable-price providers and creates user-clickable checkouts', async () => {
    const walletEpayFields = validEpayFields();
    const subscriptionEpayFields = validEpayFields({
      out_trade_no: 'SUB7NOdef456',
      name: 'SUB:Monthly',
      sign: 'abcdef0123456789abcdef0123456789',
    });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: '12.34' } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: '13.00' } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: '14.00' } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: '15.00' } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: walletEpayFields, url: 'https://epay.example.test/submit.php' } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { pay_link: 'https://stripe.example.test/pay' } } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { payment_url: 'https://waffo.example.test/pay', order_id: 'WAFFO-1' } } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { checkout_url: 'https://checkout.waffo.ai/wallet#token=signed', order_id: 'WAFFO_PANCAKE-1' } } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { checkout_url: 'https://creem.example.test/pay', order_id: 'ref_1' } } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: subscriptionEpayFields, url: 'https://epay.example.test/submit.php' } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { pay_link: 'https://stripe.example.test/subscription' } } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { checkout_url: 'https://checkout.waffo.ai/subscription#token=signed', order_id: 'WAFFO_PANCAKE_SUB-1' } } });
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { checkout_url: 'https://creem.example.test/subscription', order_id: 'sub_ref_1' } } });

    await expect(calculatePaymentAmount('epay', 20)).resolves.toBe('12.34');
    await expect(calculatePaymentAmount('stripe', 20)).resolves.toBe('13.00');
    await expect(calculatePaymentAmount('waffo', 20, 1)).resolves.toBe('14.00');
    await expect(calculatePaymentAmount('waffo-pancake', 20)).resolves.toBe('15.00');
    const walletEpay = await createCheckout({ kind: 'epay', amount: 20, paymentMethod: 'card' });
    const stripe = await createCheckout({ kind: 'stripe', amount: 20 });
    await createCheckout({ kind: 'waffo', amount: 20, payMethodIndex: 1 });
    await createCheckout({ kind: 'waffo-pancake', amount: 20 });
    await createCheckout({ kind: 'creem', productId: 'prod_1' });
    const subscriptionEpay = await createSubscriptionCheckout({ kind: 'epay', planId: 4, paymentMethod: 'card' });
    await createSubscriptionCheckout({ kind: 'stripe', planId: 4 });
    await createSubscriptionCheckout({ kind: 'waffo-pancake', planId: 4 });
    await createSubscriptionCheckout({ kind: 'creem', planId: 4 });

    expect(walletEpay).toEqual({
      method: 'POST',
      action: 'https://epay.example.test/submit.php',
      orderId: 'USR7NOabc123',
      fields: Object.entries(walletEpayFields).map(([name, value]) => ({ name, value })),
    });
    expect(subscriptionEpay).toEqual({
      method: 'POST',
      action: 'https://epay.example.test/submit.php',
      orderId: 'SUB7NOdef456',
      fields: Object.entries(subscriptionEpayFields).map(([name, value]) => ({ name, value })),
    });
    expect(stripe).toEqual({
      method: 'GET',
      action: 'https://stripe.example.test/pay',
      orderId: '',
    });

    expect(mockedPost).toHaveBeenNthCalledWith(1, '/user/amount', { amount: 20 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(2, '/user/stripe/amount', { amount: 20 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(3, '/user/waffo/amount', { amount: 20, pay_method_index: 1 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(4, '/user/waffo-pancake/amount', { amount: 20 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(5, '/user/pay', { amount: 20, payment_method: 'card' }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(6, '/user/stripe/pay', { amount: 20, payment_method: 'stripe' }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(7, '/user/waffo/pay', { amount: 20, pay_method_index: 1 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(8, '/user/waffo-pancake/pay', { amount: 20 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(9, '/user/creem/pay', { product_id: 'prod_1', payment_method: 'creem' }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(10, '/subscription/epay/pay', { plan_id: 4, payment_method: 'card' }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(11, '/subscription/stripe/pay', { plan_id: 4 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(12, '/subscription/waffo-pancake/pay', { plan_id: 4 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(13, '/subscription/creem/pay', { plan_id: 4 }, limits);
  });

  it('rejects business failures and unsafe checkout URLs without exposing server messages', async () => {
    mockedPost.mockResolvedValueOnce({ data: { message: 'error', data: 'database password=hidden' } });
    await expect(calculatePaymentAmount('stripe', 20)).rejects.toBeInstanceOf(WalletContractError);
    mockedPost.mockResolvedValueOnce({ data: { message: 'success', data: { pay_link: 'javascript:alert(1)' } } });
    await expect(createCheckout({ kind: 'stripe', amount: 20 })).rejects.toBeInstanceOf(WalletContractError);
    await expect(calculatePaymentAmount('epay', 0)).rejects.toBeInstanceOf(WalletContractError);

    mockedPost.mockResolvedValueOnce({ data: {
      message: 'success',
      url: 'javascript:alert(1)',
      data: validEpayFields({ sign: 'provider-secret-must-not-escape' }),
    } });
    const unsafeAction = await createCheckout({ kind: 'epay', amount: 20, paymentMethod: 'card' })
      .catch((error: unknown) => error);
    expect(unsafeAction).toBeInstanceOf(WalletContractError);
    expect(String(unsafeAction)).not.toContain('provider-secret-must-not-escape');

    mockedPost.mockResolvedValueOnce({ data: {
      message: 'success',
      url: 'https://epay.example.test/submit.php',
      data: validEpayFields(Object.fromEntries(
        Array.from({ length: 23 }, (_, index) => [`field_${index}`, 'value']),
      )),
    } });
    await expect(createSubscriptionCheckout({ kind: 'epay', planId: 4, paymentMethod: 'card' }))
      .rejects.toBeInstanceOf(WalletContractError);

    mockedPost.mockResolvedValueOnce({ data: {
      message: 'error',
      data: 'database password=hidden',
      url: 'https://epay.example.test/submit.php',
    } });
    const businessFailure = await createCheckout({ kind: 'epay', amount: 20, paymentMethod: 'card' })
      .catch((error: unknown) => error);
    expect(businessFailure).toBeInstanceOf(WalletContractError);
    expect(String(businessFailure)).not.toContain('password=hidden');

    for (const action of [
      'data:text/html,unsafe',
      'http://epay.example.test/submit.php',
      'https://user:password@epay.example.test/submit.php',
      'https://epay.example.test/submit.php?secret=signed',
      'https://epay.example.test/submit.php#signed',
      ' https://epay.example.test/submit.php',
      'https://epay.example.test\\submit.php',
    ]) {
      mockedPost.mockResolvedValueOnce({ data: {
        message: 'success',
        url: action,
        data: validEpayFields(),
      } });
      await expect(createCheckout({ kind: 'epay', amount: 20, paymentMethod: 'card' }))
        .rejects.toBeInstanceOf(WalletContractError);
    }
  });

  it('strictly bounds Epay form fields and validates the signed identity fields', async () => {
    const response = (data: Record<string, unknown>) => ({ data: {
      message: 'success',
      url: 'https://epay.example.test/submit.php',
      data,
    } });
    const checkout = () => createCheckout({ kind: 'epay' as const, amount: 20, paymentMethod: 'card' });
    const without = (key: string) => Object.fromEntries(
      Object.entries(validEpayFields()).filter(([name]) => name !== key),
    );

    const invalidFields: Record<string, unknown>[] = [
      {},
      validEpayFields({ 'bad-name': 'value' }),
      validEpayFields({ [`A${'x'.repeat(64)}`]: 'value' }),
      validEpayFields({ provider_note: 'x'.repeat(2_049) }),
      validEpayFields({
        ...Object.fromEntries(Array.from({ length: 9 }, (_, index) => [`field_${index}`, 'x'.repeat(2_000)])),
      }),
      validEpayFields({ provider_sequence: 42 }),
      without('out_trade_no'),
      validEpayFields({ out_trade_no: '' }),
      validEpayFields({ out_trade_no: 'trade number with spaces' }),
      without('sign'),
      validEpayFields({ sign: '' }),
      validEpayFields({ sign: 'ABCDEF0123456789ABCDEF0123456789' }),
      without('sign_type'),
      validEpayFields({ sign_type: 'SHA256' }),
    ];

    for (const fields of invalidFields) {
      mockedPost.mockResolvedValueOnce(response(fields));
      const error = await checkout().catch((reason: unknown) => reason);
      expect(error).toBeInstanceOf(WalletContractError);
      expect(String(error)).toBe('WalletContractError: Invalid wallet response');
    }
  });

  it('uses exact check-in, historical redemption, affiliate, subscription, preference, and logout routes', async () => {
    mockedPost.mockResolvedValueOnce({ data: { success: true, data: { quota_awarded: 100, checkin_date: '2026-05-04' } } });
    mockedPost.mockResolvedValueOnce({ data: { success: true, data: 500 } });
    mockedPost.mockResolvedValueOnce({ data: { success: true } });
    mockedPost.mockResolvedValueOnce({ data: { success: true } });
    mockedPut.mockResolvedValueOnce({ data: { success: true, data: { billing_preference: 'wallet_only' } } });
    mockedPost.mockResolvedValueOnce({ data: { success: true } });

    await expect(checkIn('challenge-token')).resolves.toEqual({ quotaAwarded: 100, date: '2026-05-04' });
    await expect(redeemTopUpCode('CODE_1')).resolves.toBe(500);
    await transferAffiliateQuota(100);
    await purchaseSubscriptionWithBalance(4);
    await expect(updateBillingPreference('wallet_only')).resolves.toBe('wallet_only');
    await signOutWallet();

    expect(mockedPost).toHaveBeenNthCalledWith(1, '/user/checkin', {}, {
      ...limits, params: { turnstile: 'challenge-token' },
    });
    expect(mockedPost).toHaveBeenNthCalledWith(2, '/user/topup', { key: 'CODE_1' }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(3, '/user/aff_transfer', { quota: 100 }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(4, '/subscription/balance/pay', { plan_id: 4 }, limits);
    expect(mockedPut).toHaveBeenCalledWith('/subscription/self/preference', { billing_preference: 'wallet_only' }, limits);
    expect(mockedPost).toHaveBeenNthCalledWith(5, '/user/auth/logout', {}, limits);
  });

  it('manually completes only a bounded non-empty top-up order identifier', async () => {
    mockedPost.mockResolvedValueOnce({ data: { success: true } });

    await completeWalletOrder('  ref_order_5  ');

    expect(mockedPost).toHaveBeenCalledWith(
      '/user/topup/complete',
      { trade_no: 'ref_order_5' },
      limits,
    );
    await expect(completeWalletOrder('   ')).rejects.toBeInstanceOf(WalletContractError);
    expect(mockedPost).toHaveBeenCalledTimes(1);

    mockedPost.mockResolvedValueOnce({ data: { success: false, message: 'database password=hidden' } });
    const failure = await completeWalletOrder('ref_order_6').catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(WalletContractError);
    expect(String(failure)).not.toContain('password=hidden');
  });

  it('classifies a bounded Turnstile challenge without exposing server details', async () => {
    mockedPost.mockResolvedValueOnce({ data: {
      success: false,
      message: 'Turnstile token \u4e3a\u7a7a secret=must-not-escape',
    } });
    await expect(checkIn()).rejects.toMatchObject({ name: 'TurnstileRequiredError' });
  });
});
