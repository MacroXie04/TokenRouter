import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  SubscriptionContractError,
  createSubscriptionPlan,
  createUserSubscription,
  deleteUserSubscription,
  getPaymentComplianceState,
  invalidateUserSubscription,
  listSubscriptionPlans,
  listUserSubscriptions,
  parseCreatedSubscriptionPlanResponse,
  parsePaymentComplianceResponse,
  parseSubscriptionPlansResponse,
  parseSubscriptionResetResponse,
  parseUserSubscriptionsResponse,
  resetPlanSubscriptions,
  resetUserSubscriptionsByPlan,
  setSubscriptionPlanStatus,
  updateSubscriptionPlan,
  validateSubscriptionPlanInput,
  type SubscriptionPlanInput,
} from './subscription-api';

vi.mock('../../shared/api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}));

const mockedApi = vi.mocked(api);

function rawPlan(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    title: 'Pro',
    subtitle: 'Production plan',
    price_amount: '9.990000',
    currency: 'USD',
    duration_unit: 'month',
    duration_value: 1,
    custom_seconds: 0,
    enabled: true,
    sort_order: 5,
    allow_balance_pay: true,
    allow_wallet_overflow: false,
    stripe_price_id: 'price_pro',
    creem_product_id: 'prod_pro',
    waffo_pancake_product_id: 'PROD_ABCDEFGHIJKLMNOPQRSTUV',
    max_purchase_per_user: 2,
    upgrade_group: 'vip',
    downgrade_group: 'default',
    total_amount: 100_000,
    quota_reset_period: 'monthly',
    quota_reset_custom_seconds: 0,
    created_at: 1_700_000_000,
    updated_at: 1_700_000_100,
    ...overrides,
  };
}

function rawSubscription(overrides: Record<string, unknown> = {}) {
  return {
    id: 31,
    user_id: 11,
    plan_id: 7,
    amount_total: 100_000,
    amount_used: 12_500,
    start_time: 1_700_000_000,
    end_time: 1_800_000_000,
    status: 'active',
    source: 'admin',
    last_reset_time: 1_700_000_000,
    next_reset_time: 1_702_592_000,
    upgrade_group: 'vip',
    prev_user_group: 'default',
    downgrade_group: 'default',
    allow_wallet_overflow: false,
    created_at: 1_700_000_000,
    updated_at: 1_700_000_100,
    ...overrides,
  };
}

const planInput: SubscriptionPlanInput = {
  title: ' Pro ',
  subtitle: ' Production ',
  priceAmount: '9.99',
  durationUnit: 'month',
  durationValue: 1,
  customSeconds: 99,
  enabled: true,
  sortOrder: 5,
  allowBalancePay: true,
  allowWalletOverflow: false,
  stripePriceId: ' price_pro ',
  creemProductId: ' prod_pro ',
  waffoPancakeProductId: ' PROD_ABCDEFGHIJKLMNOPQRSTUV ',
  maxPurchasePerUser: 2,
  upgradeGroup: ' vip ',
  downgradeGroup: ' default ',
  totalAmount: 100_000,
  quotaResetPeriod: 'monthly',
  quotaResetCustomSeconds: 86_400,
};

beforeEach(() => {
  vi.resetAllMocks();
});

describe('subscription response contracts', () => {
  it('requires the current payment compliance confirmation contract', async () => {
    expect(parsePaymentComplianceResponse({
      success: true,
      data: {
        payment_compliance_confirmed: true,
        payment_compliance_terms_version: 'v1',
        unrelated_public_topup_setting: true,
      },
    })).toEqual({ confirmed: true, termsVersion: 'v1' });
    expect(parsePaymentComplianceResponse({
      success: true,
      data: {
        payment_compliance_confirmed: true,
        payment_compliance_terms_version: 'future',
      },
    })).toEqual({ confirmed: false, termsVersion: 'future' });
    expect(() => parsePaymentComplianceResponse({
      success: true,
      data: { payment_compliance_confirmed: 'true', payment_compliance_terms_version: 'v1' },
    })).toThrow(SubscriptionContractError);
    expect(() => parsePaymentComplianceResponse({
      success: true,
      data: { payment_compliance_confirmed: true, payment_compliance_terms_version: 'x'.repeat(65) },
    })).toThrow(SubscriptionContractError);

    mockedApi.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: { payment_compliance_confirmed: true, payment_compliance_terms_version: 'v1' },
      },
    });
    const controller = new AbortController();
    await expect(getPaymentComplianceState(controller.signal)).resolves.toEqual({
      confirmed: true,
      termsVersion: 'v1',
    });
    expect(mockedApi.get).toHaveBeenCalledWith('/user/topup/info', expect.objectContaining({
      signal: controller.signal,
      timeout: 15_000,
      maxContentLength: 1_048_576,
      maxBodyLength: 1_048_576,
    }));
  });

  it('parses exact bounded plans and preserves decimal prices as strings', () => {
    expect(parseSubscriptionPlansResponse({
      success: true,
      data: [{ plan: rawPlan() }],
    })).toEqual([expect.objectContaining({
      id: 7,
      title: 'Pro',
      priceAmount: '9.990000',
      totalAmount: 100_000,
      waffoPancakeProductId: 'PROD_ABCDEFGHIJKLMNOPQRSTUV',
    })]);
    expect(parseCreatedSubscriptionPlanResponse({ success: true, data: rawPlan() }).id).toBe(7);
  });

  it('fails closed on secret-bearing, duplicate, malformed, and oversized plan payloads', () => {
    expect(() => parseSubscriptionPlansResponse({
      success: true,
      data: [{ plan: rawPlan({ provider_payload: 'private-provider-token' }) }],
    })).toThrow(SubscriptionContractError);
    expect(() => parseSubscriptionPlansResponse({
      success: true,
      data: [{ plan: rawPlan() }, { plan: rawPlan() }],
    })).toThrow(SubscriptionContractError);
    expect(() => parseSubscriptionPlansResponse({
      success: true,
      data: [{ plan: rawPlan({ price_amount: 9.99 }) }],
    })).toThrow(SubscriptionContractError);
    expect(() => parseSubscriptionPlansResponse({
      success: true,
      data: [{ plan: rawPlan({ title: 'x'.repeat(1024 * 1024) }) }],
    })).toThrow(SubscriptionContractError);
  });

  it('parses only subscriptions belonging to the requested user and rejects private lifecycle fields', () => {
    expect(parseUserSubscriptionsResponse({
      success: true,
      data: [{ subscription: rawSubscription() }],
    }, 11)).toEqual([expect.objectContaining({
      id: 31,
      userId: 11,
      planId: 7,
      amountUsed: 12_500,
      status: 'active',
    })]);
    expect(() => parseUserSubscriptionsResponse({
      success: true,
      data: [{ subscription: rawSubscription({ user_id: 12 }) }],
    }, 11)).toThrow(SubscriptionContractError);
    expect(() => parseUserSubscriptionsResponse({
      success: true,
      data: [{ subscription: rawSubscription({ entitlement_snapshot: 'do-not-display' }) }],
    }, 11)).toThrow(SubscriptionContractError);
  });

  it('validates coherent reset results', () => {
    expect(parseSubscriptionResetResponse({
      success: true,
      data: { plan_id: 7, matched_count: 3, reset_count: 2, user_count: 2, advance_reset_time: false },
    }, 7)).toEqual({ planId: 7, matchedCount: 3, resetCount: 2, userCount: 2, advanceResetTime: false });
    expect(() => parseSubscriptionResetResponse({
      success: true,
      data: { plan_id: 8, matched_count: 1, reset_count: 1, user_count: 1, advance_reset_time: true },
    }, 7)).toThrow(SubscriptionContractError);
    expect(() => parseSubscriptionResetResponse({
      success: true,
      data: { plan_id: 7, matched_count: 1, reset_count: 2, user_count: 1, advance_reset_time: true },
    }, 7)).toThrow(SubscriptionContractError);
  });
});

describe('subscription write validation and exact transport', () => {
  it('normalizes the allowlisted plan input without floating-point conversion', () => {
    expect(validateSubscriptionPlanInput(planInput)).toEqual({
      ...planInput,
      title: 'Pro',
      subtitle: 'Production',
      customSeconds: 0,
      stripePriceId: 'price_pro',
      creemProductId: 'prod_pro',
      waffoPancakeProductId: 'PROD_ABCDEFGHIJKLMNOPQRSTUV',
      upgradeGroup: 'vip',
      downgradeGroup: 'default',
      quotaResetCustomSeconds: 0,
    });
    expect(() => validateSubscriptionPlanInput({ ...planInput, priceAmount: '1.0000001' })).toThrow(SubscriptionContractError);
    expect(() => validateSubscriptionPlanInput({ ...planInput, title: 'bad\nname' })).toThrow(SubscriptionContractError);
    expect(() => validateSubscriptionPlanInput({ ...planInput, totalAmount: 2_147_483_648 })).toThrow(SubscriptionContractError);
    expect(() => validateSubscriptionPlanInput({ ...planInput, waffoPancakeProductId: 'private-key' })).toThrow(SubscriptionContractError);
  });

  it('uses the exact list/create/update/status routes and an allowlisted plan body', async () => {
    const envelope = { success: true, data: rawPlan() };
    mockedApi.get.mockResolvedValueOnce({ data: { success: true, data: [{ plan: rawPlan() }] } });
    mockedApi.post.mockResolvedValueOnce({ data: envelope });
    mockedApi.put.mockResolvedValueOnce({ data: { success: true } });
    mockedApi.patch.mockResolvedValueOnce({ data: { success: true } });
    const controller = new AbortController();

    await listSubscriptionPlans(controller.signal);
    await createSubscriptionPlan(planInput);
    await updateSubscriptionPlan(7, planInput);
    await setSubscriptionPlanStatus(7, false);

    expect(mockedApi.get).toHaveBeenCalledWith('/subscription/admin/plans', expect.objectContaining({
      signal: controller.signal,
      timeout: 15_000,
      maxContentLength: 1_048_576,
      maxBodyLength: 1_048_576,
    }));
    const expectedPlan = {
      title: 'Pro', subtitle: 'Production', price_amount: '9.99', currency: 'USD',
      duration_unit: 'month', duration_value: 1, custom_seconds: 0, enabled: true,
      sort_order: 5, allow_balance_pay: true, allow_wallet_overflow: false,
      stripe_price_id: 'price_pro', creem_product_id: 'prod_pro',
      waffo_pancake_product_id: 'PROD_ABCDEFGHIJKLMNOPQRSTUV', max_purchase_per_user: 2,
      upgrade_group: 'vip', downgrade_group: 'default', total_amount: 100_000,
      quota_reset_period: 'monthly', quota_reset_custom_seconds: 0,
    };
    expect(mockedApi.post).toHaveBeenCalledWith('/subscription/admin/plans', { plan: expectedPlan }, expect.any(Object));
    expect(mockedApi.put).toHaveBeenCalledWith('/subscription/admin/plans/7', { plan: expectedPlan }, expect.any(Object));
    expect(mockedApi.patch).toHaveBeenCalledWith('/subscription/admin/plans/7', { enabled: false }, expect.any(Object));
    expect(JSON.stringify(mockedApi.post.mock.calls[0][1])).not.toMatch(/secret|credential|provider_payload|private_key/i);
  });

  it('uses exact user lifecycle and plan reset routes with explicit reset semantics', async () => {
    mockedApi.get.mockResolvedValueOnce({ data: { success: true, data: [{ subscription: rawSubscription() }] } });
    mockedApi.post
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({ data: { success: true, data: { plan_id: 7, matched_count: 2, reset_count: 2, user_count: 1, advance_reset_time: false } } })
      .mockResolvedValueOnce({ data: { success: true, data: { plan_id: 7, matched_count: 3, reset_count: 3, user_count: 2, advance_reset_time: true } } })
      .mockResolvedValueOnce({ data: { success: true, data: { message: 'group changed' } } });
    mockedApi.delete.mockResolvedValueOnce({ data: { success: true } });

    await listUserSubscriptions(11);
    await createUserSubscription(11, 7);
    await resetUserSubscriptionsByPlan(11, 7, false);
    await resetPlanSubscriptions(7, true);
    await invalidateUserSubscription(31);
    await deleteUserSubscription(31);

    expect(mockedApi.get).toHaveBeenCalledWith('/subscription/admin/users/11/subscriptions', expect.any(Object));
    expect(mockedApi.post).toHaveBeenNthCalledWith(1, '/subscription/admin/users/11/subscriptions', { plan_id: 7 }, expect.any(Object));
    expect(mockedApi.post).toHaveBeenNthCalledWith(2, '/subscription/admin/users/11/subscriptions/reset', { plan_id: 7, advance_reset_time: false }, expect.any(Object));
    expect(mockedApi.post).toHaveBeenNthCalledWith(3, '/subscription/admin/plans/7/subscriptions/reset', { advance_reset_time: true }, expect.any(Object));
    expect(mockedApi.post).toHaveBeenNthCalledWith(4, '/subscription/admin/user_subscriptions/31/invalidate', {}, expect.any(Object));
    expect(mockedApi.delete).toHaveBeenCalledWith('/subscription/admin/user_subscriptions/31', expect.any(Object));
  });
});
