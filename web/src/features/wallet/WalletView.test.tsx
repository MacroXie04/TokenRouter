// @vitest-environment jsdom

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { User } from '../../shared/api/client';
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
  type SubscriptionPlan,
  type SubscriptionSummary,
  type WalletOrderPage,
  type WalletTopUpInfo,
} from './wallet-api';
import { WalletView } from './WalletView';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, string | number>) => Object.entries(values ?? {}).reduce(
      (text, [name, value]) => text.replace(`{{${name}}}`, String(value)),
      key,
    ),
    i18n: { language: 'en' },
  }),
}));

vi.mock('./wallet-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./wallet-api')>();
  return {
    ...actual,
    getTopUpInfo: vi.fn(),
    getWalletUser: vi.fn(),
    getCheckInStatus: vi.fn(),
    checkIn: vi.fn(),
    redeemTopUpCode: vi.fn(),
    getAdminWalletOrders: vi.fn(),
    getWalletOrders: vi.fn(),
    completeWalletOrder: vi.fn(),
    calculatePaymentAmount: vi.fn(),
    createCheckout: vi.fn(),
    createSubscriptionCheckout: vi.fn(),
    getAffiliateSummary: vi.fn(),
    transferAffiliateQuota: vi.fn(),
    getSubscriptionPlans: vi.fn(),
    getSubscriptionSummary: vi.fn(),
    purchaseSubscriptionWithBalance: vi.fn(),
    updateBillingPreference: vi.fn(),
    signOutWallet: vi.fn(),
  };
});

const mockedInfo = vi.mocked(getTopUpInfo);
const mockedUser = vi.mocked(getWalletUser);
const mockedCheckInStatus = vi.mocked(getCheckInStatus);
const mockedCheckIn = vi.mocked(checkIn);
const mockedRedeem = vi.mocked(redeemTopUpCode);
const mockedAdminOrders = vi.mocked(getAdminWalletOrders);
const mockedOrders = vi.mocked(getWalletOrders);
const mockedCompleteOrder = vi.mocked(completeWalletOrder);
const mockedAmount = vi.mocked(calculatePaymentAmount);
const mockedCheckout = vi.mocked(createCheckout);
const mockedSubscriptionCheckout = vi.mocked(createSubscriptionCheckout);
const mockedAffiliate = vi.mocked(getAffiliateSummary);
const mockedTransfer = vi.mocked(transferAffiliateQuota);
const mockedPlans = vi.mocked(getSubscriptionPlans);
const mockedSubscriptions = vi.mocked(getSubscriptionSummary);
const mockedPurchase = vi.mocked(purchaseSubscriptionWithBalance);
const mockedPreference = vi.mocked(updateBillingPreference);
const mockedSignOut = vi.mocked(signOutWallet);

const user: User = {
  id: 7,
  username: 'wallet-user',
  display_name: 'Wallet User',
  role: 1,
  group: 'default',
  quota: 10_000_000,
  used_quota: 2_000_000,
  request_count: 3,
};

const info: WalletTopUpInfo = {
  onlineEnabled: true,
  stripeEnabled: true,
  creemEnabled: true,
  waffoEnabled: true,
  waffoPancakeEnabled: true,
  redemptionEnabled: true,
  complianceConfirmed: true,
  complianceVersion: 'v1',
  minimum: 10,
  stripeMinimum: 15,
  waffoMinimum: 25,
  waffoPancakeMinimum: 30,
  presets: [20, 50],
  discounts: { 50: 0.9 },
  paymentMethods: [{ name: 'Card gateway', type: 'card', minimum: 20 }],
  waffoMethods: [{ name: 'Apple Pay', payMethodType: 'APPLEPAY', payMethodName: 'APPLEPAY' }],
  creemProducts: [{ productId: 'prod_1', name: 'Starter', price: 9.99, currency: 'USD', quota: 5000 }],
  topUpLink: 'https://billing.example.test/codes',
  quotaDisplayType: 'currency',
  quotaPerUnit: 500_000,
  usdExchangeRate: 7.3,
  currencySymbol: '$',
  currencyExchangeRate: 1,
};

const orderPage: WalletOrderPage = {
  page: 1,
  pageSize: 10,
  total: 1,
  items: [{
    id: 1,
    userId: 7,
    amount: 5000,
    money: 9.99,
    tradeNo: 'ref_order_1',
    paymentMethod: 'stripe',
    paymentProvider: 'stripe',
    createdAt: 1_700_000_000,
    completedAt: 0,
    status: 'pending',
  }],
};

const affiliate: AffiliateSummary = {
  code: 'AFF_7',
  count: 2,
  availableQuota: 1_000_000,
  lifetimeQuota: 1_500_000,
};

const plans: SubscriptionPlan[] = [{
  id: 4,
  title: 'Monthly',
  subtitle: 'Starter plan',
  priceAmount: '9.99',
  currency: 'USD',
  durationUnit: 'month',
  durationValue: 1,
  customSeconds: 0,
  totalAmount: 10_000,
  allowBalancePay: true,
  allowWalletOverflow: true,
  maxPurchasePerUser: 3,
  upgradeGroup: 'pro',
  downgradeGroup: 'default',
  quotaResetPeriod: 'monthly',
  quotaResetCustomSeconds: 0,
  stripePriceId: 'price_1',
  creemProductId: 'prod_1',
  waffoPancakeProductId: 'PROD_AbCdEfGhIjKlMnOpQrStUv',
}];

const subscriptions: SubscriptionSummary = {
  billingPreference: 'subscription_first',
  active: [{
    id: 8,
    planId: 4,
    amountTotal: 10_000,
    amountUsed: 100,
    startTime: 1_700_000_000,
    endTime: 1_800_000_000,
    nextResetTime: 1_700_086_400,
    lastResetTime: 1_700_000_000,
    upgradeGroup: 'pro',
    downgradeGroup: 'default',
    source: 'balance',
    status: 'active',
    allowWalletOverflow: true,
  }],
  history: [{
    id: 7,
    planId: 4,
    amountTotal: 10_000,
    amountUsed: 10_000,
    startTime: 1_600_000_000,
    endTime: 1_650_000_000,
    nextResetTime: 0,
    lastResetTime: 1_600_000_000,
    upgradeGroup: 'pro',
    downgradeGroup: 'default',
    source: 'stripe',
    status: 'expired',
    allowWalletOverflow: false,
  }],
};

beforeEach(() => {
  vi.resetAllMocks();
  mockedInfo.mockResolvedValue(info);
  mockedUser.mockResolvedValue(user);
  mockedCheckInStatus.mockResolvedValue(false);
  mockedCheckIn.mockResolvedValue({ quotaAwarded: 100, date: '2026-05-04' });
  mockedRedeem.mockResolvedValue(500);
  mockedAdminOrders.mockResolvedValue(orderPage);
  mockedOrders.mockResolvedValue(orderPage);
  mockedCompleteOrder.mockResolvedValue(undefined);
  mockedAmount.mockResolvedValue('12.34');
  mockedCheckout.mockResolvedValue({ method: 'GET', action: 'https://pay.example.test/checkout', orderId: 'ref_new' });
  mockedSubscriptionCheckout.mockResolvedValue({ method: 'GET', action: 'https://pay.example.test/subscription', orderId: 'sub_ref_new' });
  mockedAffiliate.mockResolvedValue(affiliate);
  mockedTransfer.mockResolvedValue(undefined);
  mockedPlans.mockResolvedValue(plans);
  mockedSubscriptions.mockResolvedValue(subscriptions);
  mockedPurchase.mockResolvedValue(undefined);
  mockedPreference.mockResolvedValue('wallet_only');
  mockedSignOut.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('WalletView', () => {
  it('opens a configured Turnstile challenge and submits its one-time token for check-in', async () => {
    let options: Record<string, unknown> = {};
    window.turnstile = {
      render: vi.fn((_element, value) => {
        options = value;
        return 'wallet-widget';
      }),
      remove: vi.fn(),
    };
    mockedCheckIn
      .mockRejectedValueOnce(new TurnstileRequiredError())
      .mockResolvedValueOnce({ quotaAwarded: 100, date: '2026-05-04' });
    render(<WalletView
      user={user}
      onNavigate={vi.fn()}
      onLogout={vi.fn()}
      turnstileConfig={{ required: true, siteKey: 'public-site-key' }}
    />);
    const interaction = userEvent.setup();

    await interaction.click(await screen.findByRole('button', { name: 'Check in today' }));
    expect(mockedCheckIn).toHaveBeenNthCalledWith(1);
    expect(await screen.findByRole('dialog', { name: 'Security check' })).toBeTruthy();
    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    (options.callback as (token: string) => void)('wallet-challenge');
    await waitFor(() => expect(mockedCheckIn).toHaveBeenNthCalledWith(2, 'wallet-challenge'));
    expect(await screen.findByText('Check-in added 100 quota.')).toBeTruthy();
    expect(screen.queryByRole('dialog', { name: 'Security check' })).toBeNull();
  });

  it('renders bounded wallet, payment, order, affiliate, and subscription state accessibly', async () => {
    const onNavigate = vi.fn();
    render(<WalletView user={user} onNavigate={onNavigate} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    expect(screen.getByRole('main', { name: 'Wallet' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Wallet' })).toBeTruthy();
    expect(await screen.findByText('Card gateway')).toBeTruthy();
    expect(screen.getByText('Apple Pay')).toBeTruthy();
    expect(screen.getByText('Waffo Pancake')).toBeTruthy();
    expect(screen.getByText('Starter')).toBeTruthy();
    expect(screen.getByText('ref_order_1')).toBeTruthy();
    expect(screen.getByText('AFF_7')).toBeTruthy();
    expect(screen.getByText('Monthly')).toBeTruthy();
    expect(screen.getByText('Subscription 8')).toBeTruthy();
    expect(screen.getByText('Subscription 7')).toBeTruthy();
    expect(screen.getByText('Subscription history')).toBeTruthy();
    expect(screen.getByText('Validity: 1 Month')).toBeTruthy();
    expect(screen.getByText('Quota reset period: Monthly')).toBeTruthy();
    expect(screen.getByText('Purchase limit per user: 3')).toBeTruthy();
    expect(screen.getAllByText('Upgrade group: pro').length).toBeGreaterThan(0);
    expect(screen.getAllByText('20 USD').length).toBeGreaterThan(0);
    expect(screen.getByText(/days remaining/)).toBeTruthy();
    expect(screen.getByText('Remaining quota: 0.0198 USD · Used 1%')).toBeTruthy();
    expect(screen.getByRole('progressbar', { name: 'Quota usage for subscription 8' })).toBeTruthy();
    expect(screen.getAllByText(/Last reset:/).length).toBeGreaterThan(0);
    expect(screen.getByText('Source: Balance')).toBeTruthy();
    expect(screen.getByText(/Expired at:/)).toBeTruthy();

    await interaction.click(screen.getByRole('button', { name: 'Dashboard' }));
    expect(onNavigate).toHaveBeenCalledWith('/dashboard');
  });

  it('copies absolute referral and order identifiers, labels providers, and reloads history at the selected page size', async () => {
    const completedPage: WalletOrderPage = {
      page: 1,
      pageSize: 10,
      total: 51,
      items: [{
        ...orderPage.items[0],
        paymentProvider: 'epay',
        paymentMethod: 'card',
        status: 'success',
        completedAt: 1_700_000_300,
      }],
    };
    mockedOrders
      .mockResolvedValueOnce(completedPage)
      .mockResolvedValueOnce({ ...completedPage, pageSize: 50 });
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    expect(await screen.findByText('Epay · Card gateway')).toBeTruthy();
    expect(screen.getByText(/Completed:/)).toBeTruthy();
    const referralInput = screen.getByLabelText<HTMLInputElement>('Referral link');
    expect(referralInput.readOnly).toBe(true);
    expect(referralInput.value).toBe(`${window.location.origin}/sign-up?aff=AFF_7`);

    await interaction.click(screen.getByRole('button', { name: 'Copy referral link' }));
    await waitFor(async () => expect(await navigator.clipboard.readText()).toBe(referralInput.value));
    expect(await screen.findByText('Referral link copied.')).toBeTruthy();
    await interaction.click(screen.getByRole('button', { name: 'Copy order number' }));
    await waitFor(async () => expect(await navigator.clipboard.readText()).toBe('ref_order_1'));

    await interaction.selectOptions(screen.getByLabelText('Rows per page'), '50');
    await waitFor(() => expect(mockedOrders).toHaveBeenCalledWith(7, 1, 50, '', undefined));
    expect(await screen.findByText(/Showing 1–50 of 51/)).toBeTruthy();
  });

  it.each([
    ['currency', 1, '$', 'USD', '1', 500_000],
    ['cny', 6.8, '¥', 'CNY', '6.8', 500_000],
    ['custom', 7.8, 'HK$', 'HK$', '7.8', 500_000],
    ['tokens', 1, 'tokens', 'tokens', '500000', 500_000],
  ] as const)(
    'converts %s affiliate input from server display metadata to exact internal quota',
    async (quotaDisplayType, currencyExchangeRate, currencySymbol, unit, input, expectedQuota) => {
      mockedInfo.mockResolvedValueOnce({
        ...info, quotaDisplayType, currencyExchangeRate, currencySymbol,
      });
      render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
      const interaction = userEvent.setup();

      const transferInput = await screen.findByLabelText<HTMLInputElement>(`Amount to transfer (${unit})`);
      await interaction.type(transferInput, input);
      const transferButton = screen.getByRole('button', { name: 'Transfer to wallet' }) as HTMLButtonElement;
      expect(transferButton.disabled).toBe(false);
      await interaction.click(transferButton);
      expect(mockedTransfer).toHaveBeenCalledWith(expectedQuota);
    },
  );

  it('falls back to wallet-first presentation without overwriting a saved subscription preference', async () => {
    mockedSubscriptions.mockResolvedValueOnce({
      billingPreference: 'subscription_only',
      active: [],
      history: subscriptions.history,
    });
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    await screen.findByText('No active subscriptions.');
    const preference = screen.getByLabelText<HTMLSelectElement>('Billing preference');
    expect(preference.value).toBe('wallet_first');
    expect((within(preference).getByRole('option', { name: 'Use subscription first' }) as HTMLOptionElement).disabled).toBe(true);
    expect((within(preference).getByRole('option', { name: 'Subscription only' }) as HTMLOptionElement).disabled).toBe(true);
    expect(screen.getByText(
      'Preference saved as Subscription only, but no active subscription. Wallet will be used automatically.',
    )).toBeTruthy();

    await interaction.selectOptions(preference, 'wallet_only');
    expect(mockedPreference).toHaveBeenCalledWith('wallet_only');
  });

  it('enforces plan purchase caps and reports exact affordability without disabling hosted alternatives', async () => {
    mockedPlans.mockResolvedValueOnce([{ ...plans[0], maxPurchasePerUser: 2 }]);
    const firstRender = render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);

    expect(await screen.findByText('Purchase limit reached. (2/2)')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Buy with wallet' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Pay with Stripe' }) as HTMLButtonElement).disabled).toBe(true);
    expect(mockedPurchase).not.toHaveBeenCalled();

    firstRender.unmount();
    cleanup();
    vi.clearAllMocks();
    mockedInfo.mockResolvedValue(info);
    const lowBalanceUser = { ...user, quota: 100 };
    mockedUser.mockResolvedValue(lowBalanceUser);
    mockedCheckInStatus.mockResolvedValue(false);
    mockedOrders.mockResolvedValue(orderPage);
    mockedAffiliate.mockResolvedValue(affiliate);
    mockedPlans.mockResolvedValue(plans);
    mockedSubscriptions.mockResolvedValue(subscriptions);
    render(<WalletView user={lowBalanceUser} onNavigate={vi.fn()} onLogout={vi.fn()} />);

    expect(await screen.findByText('Required wallet balance: 9.99 USD')).toBeTruthy();
    expect(screen.getByText('Available wallet balance: 0.0002 USD')).toBeTruthy();
    expect(screen.getByText('Insufficient wallet balance.')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Buy with wallet' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Pay with Stripe' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('explains provider-specific minimums and offers a one-click valid amount', async () => {
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    const waffo = await screen.findByRole('radio', { name: 'Apple Pay. Requires at least 25' }) as HTMLInputElement;
    expect(waffo.disabled).toBe(true);
    const amountInput = screen.getByLabelText<HTMLInputElement>('Top-up amount');
    await interaction.clear(amountInput);
    await interaction.type(amountInput, '10');
    expect(amountInput.value).toBe('10');
    await waitFor(() => expect(
      (screen.getByRole('button', { name: 'Review payment' }) as HTMLButtonElement).disabled,
    ).toBe(true));
    await interaction.click(screen.getByRole('button', { name: 'Use provider minimum' }));
    expect(amountInput.value).toBe('20');
    expect((screen.getByRole('button', { name: 'Review payment' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('requires a reviewed amount and confirmation, then renders Epay as an explicit signed POST form', async () => {
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockedCheckout.mockResolvedValueOnce({
      method: 'POST',
      action: 'https://epay.example.test/submit.php',
      orderId: 'USR7NOabc123',
      fields: [
        { name: 'pid', value: 'merchant-7' },
        { name: 'out_trade_no', value: 'USR7NOabc123' },
        { name: 'sign', value: '0123456789abcdef0123456789abcdef' },
      ],
    });
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    await screen.findByText('Card gateway');
    await interaction.click(screen.getByRole('button', { name: '50 × 0.9' }));
    await interaction.click(screen.getByRole('button', { name: 'Review payment' }));
    expect(mockedAmount).toHaveBeenCalledWith('epay', 50, undefined);
    expect(await screen.findByText('12.34')).toBeTruthy();

    await interaction.click(screen.getByRole('button', { name: 'Create checkout' }));
    expect(confirmation).toHaveBeenCalledWith('Create a checkout for 50 quota costing 12.34?');
    expect(mockedCheckout).toHaveBeenCalledWith({ kind: 'epay', amount: 50, paymentMethod: 'card' });
    const form = await screen.findByRole('form', { name: 'Open secure checkout' });
    expect(form.getAttribute('action')).toBe('https://epay.example.test/submit.php');
    expect(form.getAttribute('method')).toBe('post');
    expect(Array.from(form.querySelectorAll<HTMLInputElement>('input[type="hidden"]')).map((input) => ({
      name: input.name,
      value: input.value,
    }))).toEqual([
      { name: 'pid', value: 'merchant-7' },
      { name: 'out_trade_no', value: 'USR7NOabc123' },
      { name: 'sign', value: '0123456789abcdef0123456789abcdef' },
    ]);
    const submit = vi.fn((event: Event) => event.preventDefault());
    form.addEventListener('submit', submit);
    expect(submit).not.toHaveBeenCalled();
    expect(form.textContent).not.toContain('0123456789abcdef0123456789abcdef');
    const submitButton = within(form).getByRole('button', { name: 'Open secure checkout' });
    expect(submitButton.getAttribute('type')).toBe('submit');
    await interaction.click(submitButton);
    expect(submit).toHaveBeenCalledOnce();
    expect(screen.queryByRole('link', { name: 'Open secure checkout' })).toBeNull();
  });

  it('keeps GET-style provider checkout destinations as explicit safe links', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    await screen.findByRole('radio', { name: 'Stripe' });
    await interaction.click(screen.getByLabelText(/Stripe/));
    await interaction.click(screen.getByRole('button', { name: 'Review payment' }));
    await interaction.click(await screen.findByRole('button', { name: 'Create checkout' }));

    expect(mockedCheckout).toHaveBeenCalledWith({ kind: 'stripe', amount: 20 });
    const link = await screen.findByRole('link', { name: 'Open secure checkout' });
    expect(link.getAttribute('href')).toBe('https://pay.example.test/checkout');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.queryByRole('form', { name: 'Open secure checkout' })).toBeNull();
  });

  it('serializes wallet mutations and refreshes balance, history, rewards, and subscriptions', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const onUserChange = vi.fn();
    render(<WalletView user={user} onNavigate={vi.fn()} onUserChange={onUserChange} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    await screen.findByText('Card gateway');
    await interaction.click(screen.getByRole('button', { name: 'Check in today' }));
    expect(mockedCheckIn).toHaveBeenCalledTimes(1);
    expect(await screen.findByText('Check-in added 100 quota.')).toBeTruthy();

    await interaction.type(screen.getByLabelText('Redemption code'), 'CODE_1');
    await interaction.click(screen.getByRole('button', { name: 'Redeem code' }));
    expect(mockedRedeem).toHaveBeenCalledWith('CODE_1');
    expect(await screen.findByText('Code redeemed for 500 quota.')).toBeTruthy();

    const transferInput = screen.getByLabelText<HTMLInputElement>('Amount to transfer (USD)');
    await interaction.type(transferInput, '1');
    expect(transferInput.value).toBe('1');
    await interaction.click(screen.getByRole('button', { name: 'Transfer to wallet' }));
    expect(mockedTransfer).toHaveBeenCalledWith(500_000);
    expect(await screen.findByText('Affiliate quota transferred to your wallet.')).toBeTruthy();

    await interaction.selectOptions(screen.getByLabelText('Billing preference'), 'wallet_only');
    expect(mockedPreference).toHaveBeenCalledWith('wallet_only');
    expect(await screen.findByText('Billing preference saved.')).toBeTruthy();

    await interaction.click(screen.getByRole('button', { name: 'Buy with wallet' }));
    expect(mockedPurchase).toHaveBeenCalledWith(4);
    expect(await screen.findByText('Subscription purchased.')).toBeTruthy();

    await interaction.click(screen.getByRole('button', { name: 'Pay with Stripe' }));
    expect(mockedSubscriptionCheckout).toHaveBeenCalledWith({ kind: 'stripe', planId: 4 });
    expect(await screen.findByText('Subscription checkout created.')).toBeTruthy();
    expect(screen.getByText('Subscription checkout ready')).toBeTruthy();
    await interaction.click(screen.getByRole('button', { name: 'Pay with Waffo Pancake' }));
    expect(mockedSubscriptionCheckout).toHaveBeenCalledWith({ kind: 'waffo-pancake', planId: 4 });
    expect(onUserChange).toHaveBeenCalled();
  });

  it('keeps classic Waffo, Creem products, and Epay subscriptions as distinct confirmed provider flows', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockedSubscriptionCheckout.mockResolvedValueOnce({
      method: 'POST',
      action: 'https://epay.example.test/submit.php',
      orderId: 'SUB7NOabc123',
      fields: [
        { name: 'out_trade_no', value: 'SUB7NOabc123' },
        { name: 'sign', value: 'subscription-signature' },
      ],
    });
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    await screen.findByText('Apple Pay');
    const amountInput = screen.getByLabelText<HTMLInputElement>('Top-up amount');
    await interaction.clear(amountInput);
    await interaction.type(amountInput, '30');
    await interaction.click(screen.getByLabelText(/Apple Pay/));
    await interaction.click(screen.getByRole('button', { name: 'Review payment' }));
    expect(mockedAmount).toHaveBeenCalledWith('waffo', 30, 0);
    await interaction.click(await screen.findByRole('button', { name: 'Create checkout' }));
    expect(mockedCheckout).toHaveBeenCalledWith({ kind: 'waffo', amount: 30, payMethodIndex: 0 });

    await interaction.click(screen.getByRole('button', { name: 'Buy' }));
    expect(mockedCheckout).toHaveBeenCalledWith({ kind: 'creem', productId: 'prod_1' });

    await interaction.click(screen.getByRole('button', { name: 'Pay with Card gateway' }));
    expect(mockedSubscriptionCheckout).toHaveBeenCalledWith({
      kind: 'epay',
      planId: 4,
      paymentMethod: 'card',
    });
    const form = await screen.findByRole('form', { name: 'Open secure checkout' });
    expect(form.getAttribute('action')).toBe('https://epay.example.test/submit.php');
    expect(form.getAttribute('method')).toBe('post');
    expect(Array.from(form.querySelectorAll<HTMLInputElement>('input[type="hidden"]')).map((input) => ({
      name: input.name,
      value: input.value,
    }))).toEqual([
      { name: 'out_trade_no', value: 'SUB7NOabc123' },
      { name: 'sign', value: 'subscription-signature' },
    ]);
  });

  it('redacts an Epay checkout failure instead of rendering signed or server details', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockedCheckout.mockRejectedValueOnce(new Error('sign=provider-secret database password=hidden'));
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    await screen.findByText('Card gateway');
    await interaction.click(screen.getByRole('button', { name: 'Review payment' }));
    await interaction.click(await screen.findByRole('button', { name: 'Create checkout' }));

    expect(await screen.findByText('Unable to create the checkout.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('provider-secret');
    expect(document.body.textContent).not.toContain('password=hidden');
    expect(screen.queryByRole('form', { name: 'Open secure checkout' })).toBeNull();
  });

  it('fails closed for every payment mutation while compliance is loading or unconfirmed', async () => {
    let resolveInfo!: (value: WalletTopUpInfo) => void;
    mockedInfo.mockReturnValueOnce(new Promise((resolve) => { resolveInfo = resolve; }));
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);

    await screen.findByText('AFF_7');
    expect(screen.queryByLabelText('Redemption code')).toBeNull();
    expect(screen.queryByLabelText('Amount to transfer (USD)')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Review payment' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Buy with wallet' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Pay with Stripe' })).toBeNull();

    resolveInfo({ ...info, complianceConfirmed: false });
    await screen.findByText('Code redemption is currently unavailable.');
    expect(screen.queryByLabelText('Redemption code')).toBeNull();
    expect(screen.queryByLabelText('Amount to transfer (USD)')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Buy with wallet' })).toBeNull();
    expect(mockedRedeem).not.toHaveBeenCalled();
    expect(mockedTransfer).not.toHaveBeenCalled();
    expect(mockedPurchase).not.toHaveBeenCalled();
    expect(mockedCheckout).not.toHaveBeenCalled();
    expect(mockedSubscriptionCheckout).not.toHaveBeenCalled();
  });

  it('treats an unknown compliance terms version as unconfirmed', async () => {
    mockedInfo.mockResolvedValueOnce({ ...info, complianceVersion: 'future' });
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);

    await screen.findByText('Code redemption is currently unavailable.');
    expect(screen.queryByLabelText('Redemption code')).toBeNull();
    expect(screen.queryByLabelText('Amount to transfer (USD)')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Review payment' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Buy with wallet' })).toBeNull();
  });

  it('keeps mutations unavailable when compliance state cannot be loaded', async () => {
    mockedInfo.mockRejectedValueOnce(new Error('payment service secret=hidden'));
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={vi.fn()} />);

    expect(await screen.findByText('Unable to load payment options.')).toBeTruthy();
    await screen.findByText('AFF_7');
    expect(screen.queryByLabelText('Redemption code')).toBeNull();
    expect(screen.queryByLabelText('Amount to transfer (USD)')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Buy with wallet' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Pay with Stripe' })).toBeNull();
    expect(document.body.textContent).not.toContain('secret=hidden');
  });

  it('uses the admin history route and confirms pending orders without exposing failures', async () => {
    const admin = { ...user, role: 10 };
    const adminOrders: WalletOrderPage = {
      ...orderPage,
      items: [{ ...orderPage.items[0], userId: 42 }],
    };
    mockedUser.mockResolvedValueOnce(admin);
    mockedInfo.mockResolvedValueOnce({ ...info, complianceConfirmed: false });
    mockedAdminOrders.mockResolvedValue(adminOrders);
    mockedCompleteOrder
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('database password=hidden'));
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<WalletView user={admin} onNavigate={vi.fn()} onLogout={vi.fn()} />);
    const interaction = userEvent.setup();

    expect(await screen.findByText('ref_order_1')).toBeTruthy();
    expect(mockedAdminOrders).toHaveBeenCalledWith(1, 10, '', expect.any(AbortSignal));
    expect(mockedOrders).not.toHaveBeenCalled();
    expect(screen.getByRole('cell', { name: /42/ })).toBeTruthy();
    await interaction.click(screen.getByRole('button', { name: 'Copy user ID' }));
    await waitFor(async () => expect(await navigator.clipboard.readText()).toBe('42'));

    const completeButton = screen.getByRole('button', { name: 'Complete Order' }) as HTMLButtonElement;
    expect(completeButton.disabled).toBe(false);
    await interaction.click(completeButton);
    expect(confirmation).toHaveBeenCalledWith(
      'Are you sure you want to manually complete this order? The user will be credited with the corresponding quota.',
    );
    expect(mockedCompleteOrder).toHaveBeenCalledWith('ref_order_1');
    expect(await screen.findByText('Order completed successfully')).toBeTruthy();

    await interaction.click(screen.getByRole('button', { name: 'Complete Order' }));
    expect(await screen.findByText('Failed to complete order')).toBeTruthy();
    expect(document.body.textContent).not.toContain('password=hidden');
  });

  it('opens the inline history target and clears the one-shot history query', async () => {
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
      configurable: true,
      value: scrollIntoView,
    });
    window.history.replaceState(null, '', '/wallet?show_history=true');

    render(<WalletView
      user={user}
      onNavigate={vi.fn()}
      onLogout={vi.fn()}
      initialShowHistory
    />);

    const history = document.getElementById('wallet-payment-history');
    expect(history).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(history));
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start' });
    expect(window.location.pathname).toBe('/wallet');
    expect(window.location.search).toBe('');
  });

  it('keeps panel failures independent, redacts error details, retries locally, and invalidates server logout', async () => {
    mockedInfo.mockRejectedValueOnce(new Error('stripe_secret=hidden'));
    mockedOrders.mockRejectedValueOnce(new Error('postgres password=hidden'));
    mockedAffiliate.mockRejectedValueOnce(new Error('affiliate token=hidden'));
    mockedSubscriptions.mockRejectedValueOnce(new Error('subscription key=hidden'));
    const onLogout = vi.fn();
    render(<WalletView user={user} onNavigate={vi.fn()} onLogout={onLogout} />);
    const interaction = userEvent.setup();

    expect(await screen.findByText('Unable to load payment options.')).toBeTruthy();
    expect(screen.getByText('Unable to load payment history.')).toBeTruthy();
    expect(screen.getByText('Unable to load affiliate rewards.')).toBeTruthy();
    expect(screen.getByText('Unable to load subscriptions.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('password=hidden');
    expect(document.body.textContent).not.toContain('stripe_secret');

    const topUpPanel = screen.getByRole('heading', { name: 'Add wallet credit' }).closest('section');
    expect(topUpPanel).toBeTruthy();
    await interaction.click(within(topUpPanel!).getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('Card gateway')).toBeTruthy();
    expect(mockedInfo).toHaveBeenCalledTimes(2);
    expect(mockedOrders).toHaveBeenCalledTimes(1);

    await interaction.click(screen.getByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(mockedSignOut).toHaveBeenCalledTimes(1));
    expect(onLogout).toHaveBeenCalledTimes(1);
  });
});
