// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createWaffoPancakeSubscriptionProduct,
  listWaffoPancakeSubscriptionProducts,
} from '../wallet/waffo-pancake-admin-api';
import {
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
} from './subscription-api';
import { SubscriptionAdminView } from './SubscriptionAdminView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./subscription-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./subscription-api')>();
  return {
    ...actual,
    createSubscriptionPlan: vi.fn(),
    createUserSubscription: vi.fn(),
    deleteUserSubscription: vi.fn(),
    getPaymentComplianceState: vi.fn(),
    invalidateUserSubscription: vi.fn(),
    listSubscriptionPlans: vi.fn(),
    listUserSubscriptions: vi.fn(),
    resetPlanSubscriptions: vi.fn(),
    resetUserSubscriptionsByPlan: vi.fn(),
    setSubscriptionPlanStatus: vi.fn(),
    updateSubscriptionPlan: vi.fn(),
  };
});

vi.mock('../wallet/waffo-pancake-admin-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../wallet/waffo-pancake-admin-api')>();
  return {
    ...actual,
    createWaffoPancakeSubscriptionProduct: vi.fn(),
    listWaffoPancakeSubscriptionProducts: vi.fn(),
  };
});

const mockedCreatePlan = vi.mocked(createSubscriptionPlan);
const mockedCreateUserSubscription = vi.mocked(createUserSubscription);
const mockedDeleteSubscription = vi.mocked(deleteUserSubscription);
const mockedGetCompliance = vi.mocked(getPaymentComplianceState);
const mockedInvalidateSubscription = vi.mocked(invalidateUserSubscription);
const mockedListPlans = vi.mocked(listSubscriptionPlans);
const mockedListUserSubscriptions = vi.mocked(listUserSubscriptions);
const mockedResetPlan = vi.mocked(resetPlanSubscriptions);
const mockedResetUserPlan = vi.mocked(resetUserSubscriptionsByPlan);
const mockedSetPlanStatus = vi.mocked(setSubscriptionPlanStatus);
const mockedUpdatePlan = vi.mocked(updateSubscriptionPlan);
const mockedCreatePancakeProduct = vi.mocked(createWaffoPancakeSubscriptionProduct);
const mockedListPancakeProducts = vi.mocked(listWaffoPancakeSubscriptionProducts);

function plan(overrides: Partial<ManagedSubscriptionPlan> = {}): ManagedSubscriptionPlan {
  return {
    id: 7,
    title: 'Pro',
    subtitle: 'Production plan',
    priceAmount: '9.990000',
    currency: 'USD',
    durationUnit: 'month',
    durationValue: 1,
    customSeconds: 0,
    enabled: true,
    sortOrder: 5,
    allowBalancePay: true,
    allowWalletOverflow: false,
    stripePriceId: 'price_pro',
    creemProductId: 'prod_pro',
    waffoPancakeProductId: 'PROD_ABCDEFGHIJKLMNOPQRSTUV',
    maxPurchasePerUser: 2,
    upgradeGroup: 'vip',
    downgradeGroup: 'default',
    totalAmount: 100_000,
    quotaResetPeriod: 'monthly',
    quotaResetCustomSeconds: 0,
    createdAt: 1_700_000_000,
    updatedAt: 1_700_000_100,
    ...overrides,
  };
}

function subscription(overrides: Partial<ManagedUserSubscription> = {}): ManagedUserSubscription {
  return {
    id: 31,
    userId: 11,
    planId: 7,
    amountTotal: 100_000,
    amountUsed: 12_500,
    startTime: 1_700_000_000,
    endTime: 2_000_000_000,
    status: 'active',
    source: 'admin',
    lastResetTime: 1_700_000_000,
    nextResetTime: 1_702_592_000,
    upgradeGroup: 'vip',
    previousUserGroup: 'default',
    downgradeGroup: 'default',
    allowWalletOverflow: false,
    createdAt: 1_700_000_000,
    updatedAt: 1_700_000_100,
    ...overrides,
  };
}

function renderSubscriptions(role = 10) {
  return render(<SubscriptionAdminView operatorRole={role} />);
}

beforeEach(() => {
  vi.resetAllMocks();
  mockedListPlans.mockResolvedValue([plan()]);
  mockedGetCompliance.mockResolvedValue({ confirmed: true, termsVersion: 'v1' });
  mockedListUserSubscriptions.mockResolvedValue([subscription()]);
  mockedCreatePlan.mockResolvedValue(plan());
  mockedCreateUserSubscription.mockResolvedValue();
  mockedDeleteSubscription.mockResolvedValue();
  mockedInvalidateSubscription.mockResolvedValue();
  mockedResetPlan.mockResolvedValue({
    planId: 7, matchedCount: 2, resetCount: 2, userCount: 1, advanceResetTime: true,
  });
  mockedResetUserPlan.mockResolvedValue({
    planId: 7, matchedCount: 1, resetCount: 1, userCount: 1, advanceResetTime: true,
  });
  mockedSetPlanStatus.mockResolvedValue();
  mockedUpdatePlan.mockResolvedValue();
  mockedListPancakeProducts.mockResolvedValue({
    storeId: 'STO_ABCDEFGHIJKLMNOPQRSTUV',
    products: [{ id: 'PROD_ZYXWVUTSRQPONMLKJIHGFE', name: 'Existing Pro', status: 'ACTIVE' }],
  });
  mockedCreatePancakeProduct.mockResolvedValue({
    id: 'PROD_ZYXWVUTSRQPONMLKJIHGFE', name: 'New Pro', status: '',
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('SubscriptionAdminView access and plan browsing', () => {
  it('fails closed for non-admin roles without issuing subscription requests', () => {
    renderSubscriptions(1);
    expect(screen.getByRole('alert').textContent).toContain('Administrator access required');
    expect(mockedListPlans).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Create plan' })).toBeNull();
  });

  it('renders plans, applies client-side search/status filters, and paginates bounded results', async () => {
    mockedListPlans.mockResolvedValue(Array.from({ length: 12 }, (_, index) => plan({
      id: index + 1,
      title: index === 11 ? 'Disabled Archive' : `Plan ${index + 1}`,
      enabled: index !== 11,
    })));
    renderSubscriptions();
    const user = userEvent.setup();

    expect(await screen.findByText('Plan 1')).toBeTruthy();
    expect(screen.getByRole('table', { name: 'Subscription plans' }).getAttribute('aria-busy')).toBe('false');
    expect(screen.getByText('Page 1 of 2')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('Disabled Archive')).toBeTruthy();

    const filters = screen.getByRole('search', { name: 'Search subscription plans' });
    await user.type(within(filters).getByLabelText('Search plans'), 'archive');
    await user.selectOptions(within(filters).getByLabelText('Plan status'), 'disabled');
    await user.click(within(filters).getByRole('button', { name: 'Apply filters' }));
    expect(screen.getByText('Disabled Archive')).toBeTruthy();
    expect(screen.queryByText('Plan 1')).toBeNull();
    expect(screen.getByText('Page 1 of 1')).toBeTruthy();

    expect(screen.getByText(/Plans are retained/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: /delete plan/i })).toBeNull();
  });

  it('shows redacted loading, empty, failure, and retry states', async () => {
    let resolvePlans: ((plans: ManagedSubscriptionPlan[]) => void) | undefined;
    mockedListPlans.mockImplementationOnce(() => new Promise((resolve) => { resolvePlans = resolve; }));
    const first = renderSubscriptions();
    expect(screen.getByRole('status').textContent).toBe('Loading subscription plans…');
    await act(async () => resolvePlans?.([]));
    expect(await screen.findByText('No subscription plans match these filters.')).toBeTruthy();
    first.unmount();

    mockedListPlans.mockRejectedValueOnce(new Error('database password and provider secret'));
    mockedListPlans.mockResolvedValueOnce([plan()]);
    renderSubscriptions();
    expect((await screen.findByRole('alert')).textContent).toContain('Unable to load subscription plans.');
    expect(screen.queryByText(/database password|provider secret/)).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('Pro')).toBeTruthy();
  });
});

describe('SubscriptionAdminView payment compliance gate', () => {
  it('locks plan changes and grants while retaining safe lifecycle cleanup actions', async () => {
    mockedGetCompliance.mockResolvedValueOnce({ confirmed: false, termsVersion: 'future' });
    renderSubscriptions();
    const user = userEvent.setup();

    expect(await screen.findByText(/Subscription plan creation and changes are locked/)).toBeTruthy();
    const plansTable = await screen.findByRole('table', { name: 'Subscription plans' });
    const planRow = within(plansTable).getByText('Pro').closest('tr') as HTMLTableRowElement;
    expect(screen.getByRole('button', { name: 'Create plan' }).hasAttribute('disabled')).toBe(true);
    expect(within(planRow).getByRole('button', { name: 'Edit' }).hasAttribute('disabled')).toBe(true);
    expect(within(planRow).getByRole('button', { name: 'Disable' }).hasAttribute('disabled')).toBe(true);
    expect(within(planRow).getByRole('button', { name: 'Reset quota' }).hasAttribute('disabled')).toBe(false);

    const lookup = screen.getByRole('search', { name: 'Find user subscriptions' });
    await user.type(within(lookup).getByLabelText('User ID'), '11');
    await user.click(within(lookup).getByRole('button', { name: 'Load subscriptions' }));
    await screen.findByRole('table', { name: 'Subscriptions for user 11' });
    expect(screen.getByLabelText('Plan to grant').hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: 'Add subscription' }).hasAttribute('disabled')).toBe(true);
    expect(mockedCreatePlan).not.toHaveBeenCalled();
    expect(mockedSetPlanStatus).not.toHaveBeenCalled();
    expect(mockedCreateUserSubscription).not.toHaveBeenCalled();
  });

  it('fails closed when compliance cannot be verified and unlocks after a successful retry', async () => {
    mockedGetCompliance
      .mockRejectedValueOnce(new Error('private payment provider details'))
      .mockResolvedValueOnce({ confirmed: true, termsVersion: 'v1' });
    renderSubscriptions();

    expect(await screen.findByText('Unable to verify payment compliance. Plan changes remain locked.')).toBeTruthy();
    expect(screen.queryByText(/private payment provider details/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Create plan' }).hasAttribute('disabled')).toBe(true);
    await userEvent.click(screen.getByRole('button', { name: 'Retry compliance check' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create plan' }).hasAttribute('disabled')).toBe(false));
    expect(mockedGetCompliance).toHaveBeenCalledTimes(2);
  });
});

describe('SubscriptionAdminView plan workflows', () => {
  it('creates and edits a complete allowlisted plan configuration', async () => {
    renderSubscriptions();
    const user = userEvent.setup();
    await screen.findByText('Pro');

    await user.click(screen.getByRole('button', { name: 'Create plan' }));
    const createForm = screen.getByRole('form', { name: 'Create subscription plan' });
    await user.type(within(createForm).getByLabelText('Plan title'), 'Starter');
    await user.clear(within(createForm).getByLabelText('Price (USD)'));
    await user.type(within(createForm).getByLabelText('Price (USD)'), '4.5');
    await user.clear(within(createForm).getByLabelText('Included quota'));
    await user.type(within(createForm).getByLabelText('Included quota'), '5000');
    await user.selectOptions(within(createForm).getByLabelText('Quota reset period'), 'custom');
    await user.clear(within(createForm).getByLabelText('Reset interval seconds'));
    await user.type(within(createForm).getByLabelText('Reset interval seconds'), '86400');
    await user.click(within(createForm).getByRole('button', { name: 'Create plan' }));

    await waitFor(() => expect(mockedCreatePlan).toHaveBeenCalledWith(expect.objectContaining({
      title: 'Starter',
      priceAmount: '4.5',
      totalAmount: 5000,
      quotaResetPeriod: 'custom',
      quotaResetCustomSeconds: 86_400,
    })));
    expect(screen.getByRole('status').textContent).toBe('Subscription plan created.');

    await user.click(within(screen.getByRole('table', { name: 'Subscription plans' })).getByRole('button', { name: 'Edit' }));
    const editForm = screen.getByRole('form', { name: 'Edit subscription plan Pro' });
    const subtitle = within(editForm).getByLabelText('Plan subtitle');
    await user.clear(subtitle);
    await user.type(subtitle, 'Updated plan');
    await user.click(within(editForm).getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(mockedUpdatePlan).toHaveBeenCalledWith(7, expect.objectContaining({
      title: 'Pro', subtitle: 'Updated plan', priceAmount: '9.990000',
    })));
  });

  it('confirms status and quota-reset mutations and serializes in-flight writes', async () => {
    let resolveStatus: (() => void) | undefined;
    mockedSetPlanStatus.mockImplementationOnce(() => new Promise((resolve) => { resolveStatus = resolve; }));
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderSubscriptions();
    const user = userEvent.setup();
    const table = await screen.findByRole('table', { name: 'Subscription plans' });
    const row = within(table).getByText('Pro').closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Disable' }));
    expect(confirmation).toHaveBeenCalledWith('Disable plan “Pro”? Existing subscription records will remain.');
    expect(mockedSetPlanStatus).toHaveBeenCalledWith(7, false);
    expect(within(row).getByRole('button', { name: 'Edit' }).hasAttribute('disabled')).toBe(true);
    expect(within(row).getByRole('button', { name: 'Reset quota' }).hasAttribute('disabled')).toBe(true);
    await act(async () => resolveStatus?.());
    await waitFor(() => expect(within(row).getByRole('button', { name: 'Reset quota' }).hasAttribute('disabled')).toBe(false));

    await user.click(within(row).getByRole('button', { name: 'Reset quota' }));
    expect(confirmation).toHaveBeenLastCalledWith('Reset all active subscriptions for plan “Pro”?');
    await waitFor(() => expect(mockedResetPlan).toHaveBeenCalledWith(7, true));
    expect(screen.getByRole('status').textContent).toBe('2 active subscriptions reset.');
  });

  it('keeps Waffo Pancake catalog and product creation root-only and never asks for credentials', async () => {
    const user = userEvent.setup();
    const admin = renderSubscriptions(10);
    await screen.findByText('Pro');
    await user.click(screen.getByRole('button', { name: 'Create plan' }));
    expect(screen.queryByRole('button', { name: 'Load Waffo Pancake products' })).toBeNull();
    expect(screen.queryByLabelText(/private key|merchant/i)).toBeNull();
    admin.unmount();

    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderSubscriptions(100);
    await screen.findByText('Pro');
    await user.click(screen.getByRole('button', { name: 'Create plan' }));
    await user.click(screen.getByRole('button', { name: 'Load Waffo Pancake products' }));
    await waitFor(() => expect(mockedListPancakeProducts).toHaveBeenCalledTimes(1));
    await user.type(screen.getByLabelText('Plan title'), 'Pancake Plan');
    await user.clear(screen.getByLabelText('Price (USD)'));
    await user.type(screen.getByLabelText('Price (USD)'), '12.5');
    await user.click(screen.getByRole('button', { name: 'Create Waffo Pancake product' }));
    await waitFor(() => expect(mockedCreatePancakeProduct).toHaveBeenCalledWith({ name: 'Pancake Plan', amount: '12.5' }));
    const pancakeField = screen.getByRole('form', { name: 'Create subscription plan' })
      .querySelector<HTMLInputElement>('input[list="subscription-pancake-products"]');
    expect(pancakeField?.value)
      .toBe('PROD_ZYXWVUTSRQPONMLKJIHGFE');
  });

  it('redacts rejected plan mutations and keeps the editor available for correction', async () => {
    mockedCreatePlan.mockRejectedValueOnce(new Error('Stripe secret sk-private and database DSN'));
    renderSubscriptions();
    const user = userEvent.setup();
    await screen.findByText('Pro');
    await user.click(screen.getByRole('button', { name: 'Create plan' }));
    const form = screen.getByRole('form', { name: 'Create subscription plan' });
    await user.type(within(form).getByLabelText('Plan title'), 'Starter');
    await user.click(within(form).getByRole('button', { name: 'Create plan' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to create subscription plan.');
    expect(screen.queryByText(/sk-private|database DSN/)).toBeNull();
    expect(screen.getByRole('form', { name: 'Create subscription plan' })).toBeTruthy();
  });
});

describe('SubscriptionAdminView user lifecycle', () => {
  it('loads a user and completes grant, reset, invalidate, and hard-delete workflows with confirmations', async () => {
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderSubscriptions();
    const user = userEvent.setup();
    await screen.findByText('Pro');

    const lookup = screen.getByRole('search', { name: 'Find user subscriptions' });
    await user.type(within(lookup).getByLabelText('User ID'), '11');
    await user.click(within(lookup).getByRole('button', { name: 'Load subscriptions' }));
    const table = await screen.findByRole('table', { name: 'Subscriptions for user 11' });
    expect(within(table).getByText(/#31/)).toBeTruthy();
    expect(mockedListUserSubscriptions).toHaveBeenCalledWith(11, expect.any(AbortSignal));

    await user.selectOptions(screen.getByLabelText('Plan to grant'), '7');
    await user.click(screen.getByRole('button', { name: 'Add subscription' }));
    await waitFor(() => expect(mockedCreateUserSubscription).toHaveBeenCalledWith(11, 7));

    await user.click(within(table).getByRole('button', { name: 'Reset quota' }));
    expect(confirmation).toHaveBeenLastCalledWith('Reset active “Pro” subscriptions for user 11?');
    await waitFor(() => expect(mockedResetUserPlan).toHaveBeenCalledWith(11, 7, true));

    await user.click(within(table).getByRole('button', { name: 'Invalidate' }));
    expect(confirmation).toHaveBeenLastCalledWith('Invalidate subscription 31 now?');
    await waitFor(() => expect(mockedInvalidateSubscription).toHaveBeenCalledWith(31));

    await user.click(within(table).getByRole('button', { name: 'Delete' }));
    expect(confirmation).toHaveBeenLastCalledWith('Permanently delete subscription 31? This cannot be undone.');
    await waitFor(() => expect(mockedDeleteSubscription).toHaveBeenCalledWith(31));
  });

  it('shows stable user-load and mutation errors without reflecting backend details', async () => {
    mockedListUserSubscriptions.mockRejectedValueOnce(new Error('user table DSN secret'));
    renderSubscriptions();
    const user = userEvent.setup();
    await screen.findByText('Pro');
    await user.type(screen.getByLabelText('User ID'), '11');
    await user.click(screen.getByRole('button', { name: 'Load subscriptions' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Unable to load user subscriptions.');
    expect(screen.queryByText(/DSN secret/)).toBeNull();

    mockedListUserSubscriptions.mockResolvedValueOnce([subscription()]);
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    const table = await screen.findByRole('table', { name: 'Subscriptions for user 11' });
    mockedDeleteSubscription.mockRejectedValueOnce(new Error('provider payload private'));
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await user.click(within(table).getByRole('button', { name: 'Delete' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to delete subscription.');
    expect(screen.queryByText(/provider payload private/)).toBeNull();
  });

  it('ignores an obsolete user response when a newer lookup supersedes it', async () => {
    let resolveFirst: ((items: ManagedUserSubscription[]) => void) | undefined;
    mockedListUserSubscriptions
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
      .mockResolvedValueOnce([subscription({ id: 52, userId: 12, source: 'newer-user' })]);
    renderSubscriptions();
    const user = userEvent.setup();
    await screen.findByText('Pro');
    const input = screen.getByLabelText('User ID');
    await user.type(input, '11');
    await user.click(screen.getByRole('button', { name: 'Load subscriptions' }));
    await user.clear(input);
    await user.type(input, '12');
    await user.click(screen.getByRole('button', { name: 'Load subscriptions' }));
    expect(await screen.findByRole('table', { name: 'Subscriptions for user 12' })).toBeTruthy();
    expect(screen.getByText(/#52/)).toBeTruthy();

    await act(async () => {
      resolveFirst?.([subscription({ id: 99, source: 'obsolete-user' })]);
      await Promise.resolve();
    });
    expect(screen.queryByText(/#99/)).toBeNull();
    expect(screen.queryByText(/obsolete-user/)).toBeNull();
  });

  it('aborts initial loads and ignores late responses after unmount', async () => {
    let resolvePlans: ((plans: ManagedSubscriptionPlan[]) => void) | undefined;
    mockedListPlans.mockImplementationOnce(() => new Promise((resolve) => { resolvePlans = resolve; }));
    const rendered = renderSubscriptions();
    const signal = mockedListPlans.mock.calls[0][0];
    rendered.unmount();
    expect(signal?.aborted).toBe(true);

    await act(async () => {
      resolvePlans?.([plan({ title: 'Late private plan' })]);
      await Promise.resolve();
    });
    expect(screen.queryByText('Late private plan')).toBeNull();
  });
});
