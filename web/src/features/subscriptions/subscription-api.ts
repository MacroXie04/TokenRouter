import { api } from '../../shared/api/client';

export const SUBSCRIPTION_ADMIN_ROLE = 10;
export const SUBSCRIPTION_ROOT_ROLE = 100;
export const SUBSCRIPTION_PAGE_SIZE = 10;

const MAX_RESPONSE_BYTES = 1024 * 1024;
const MAX_PLANS = 1_000;
const MAX_SUBSCRIPTIONS = 2_000;
const MAX_RESET_COUNT = 10_000_000;
const MAX_ID = 2_147_483_647;
const MAX_QUOTA = 2_147_483_647;
const MAX_SAFE_COUNTER = Number.MAX_SAFE_INTEGER;
const MAX_TIMESTAMP = 253_402_300_799;
const REQUEST_TIMEOUT_MS = 15_000;

const PLAN_KEYS = new Set([
  'id',
  'title',
  'subtitle',
  'price_amount',
  'currency',
  'duration_unit',
  'duration_value',
  'custom_seconds',
  'enabled',
  'sort_order',
  'allow_balance_pay',
  'allow_wallet_overflow',
  'stripe_price_id',
  'creem_product_id',
  'waffo_pancake_product_id',
  'max_purchase_per_user',
  'upgrade_group',
  'downgrade_group',
  'total_amount',
  'quota_reset_period',
  'quota_reset_custom_seconds',
  'created_at',
  'updated_at',
]);

const SUBSCRIPTION_KEYS = new Set([
  'id',
  'user_id',
  'plan_id',
  'amount_total',
  'amount_used',
  'start_time',
  'end_time',
  'status',
  'source',
  'last_reset_time',
  'next_reset_time',
  'upgrade_group',
  'prev_user_group',
  'downgrade_group',
  'allow_wallet_overflow',
  'created_at',
  'updated_at',
]);

export type SubscriptionDurationUnit = 'year' | 'month' | 'day' | 'hour' | 'custom';
export type SubscriptionResetPeriod = 'never' | 'daily' | 'weekly' | 'monthly' | 'custom';
export type UserSubscriptionStatus = 'active' | 'expired' | 'cancelled';

export interface ManagedSubscriptionPlan {
  id: number;
  title: string;
  subtitle: string;
  priceAmount: string;
  currency: string;
  durationUnit: SubscriptionDurationUnit;
  durationValue: number;
  customSeconds: number;
  enabled: boolean;
  sortOrder: number;
  allowBalancePay: boolean;
  allowWalletOverflow: boolean;
  stripePriceId: string;
  creemProductId: string;
  waffoPancakeProductId: string;
  maxPurchasePerUser: number;
  upgradeGroup: string;
  downgradeGroup: string;
  totalAmount: number;
  quotaResetPeriod: SubscriptionResetPeriod;
  quotaResetCustomSeconds: number;
  createdAt: number;
  updatedAt: number;
}

export interface SubscriptionPlanInput {
  title: string;
  subtitle: string;
  priceAmount: string;
  durationUnit: SubscriptionDurationUnit;
  durationValue: number;
  customSeconds: number;
  enabled: boolean;
  sortOrder: number;
  allowBalancePay: boolean;
  allowWalletOverflow: boolean;
  stripePriceId: string;
  creemProductId: string;
  waffoPancakeProductId: string;
  maxPurchasePerUser: number;
  upgradeGroup: string;
  downgradeGroup: string;
  totalAmount: number;
  quotaResetPeriod: SubscriptionResetPeriod;
  quotaResetCustomSeconds: number;
}

export interface ManagedUserSubscription {
  id: number;
  userId: number;
  planId: number;
  amountTotal: number;
  amountUsed: number;
  startTime: number;
  endTime: number;
  status: UserSubscriptionStatus;
  source: string;
  lastResetTime: number;
  nextResetTime: number;
  upgradeGroup: string;
  previousUserGroup: string;
  downgradeGroup: string;
  allowWalletOverflow: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface SubscriptionResetResult {
  planId: number;
  matchedCount: number;
  resetCount: number;
  userCount: number;
  advanceResetTime: boolean;
}

export interface PaymentComplianceState {
  confirmed: boolean;
  termsVersion: string;
}

type UnknownRecord = Record<string, unknown>;

export class SubscriptionContractError extends Error {
  constructor() {
    super('Invalid subscription API contract');
    this.name = 'SubscriptionContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new SubscriptionContractError();
  }
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: Set<string>): void {
  if (Object.keys(value).some((key) => !allowed.has(key))) {
    throw new SubscriptionContractError();
  }
}

function boundedPayload(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new SubscriptionContractError();
  }
  if (new TextEncoder().encode(encoded).byteLength > MAX_RESPONSE_BYTES) {
    throw new SubscriptionContractError();
  }
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new SubscriptionContractError();
  }
  return value as number;
}

function boundedText(value: unknown, maximumCodePoints: number, minimumCodePoints = 0): string {
  if (typeof value !== 'string' || value.includes('\0')) throw new SubscriptionContractError();
  const size = [...value].length;
  if (size < minimumCodePoints || size > maximumCodePoints) throw new SubscriptionContractError();
  return value;
}

function formText(value: unknown, maximumCodePoints: number, minimumCodePoints = 0): string {
  const parsed = boundedText(value, maximumCodePoints, minimumCodePoints);
  if (/\p{Cc}/u.test(parsed)) throw new SubscriptionContractError();
  return parsed;
}

function durationUnit(value: unknown): SubscriptionDurationUnit {
  if (value !== 'year' && value !== 'month' && value !== 'day' && value !== 'hour' && value !== 'custom') {
    throw new SubscriptionContractError();
  }
  return value;
}

function resetPeriod(value: unknown): SubscriptionResetPeriod {
  if (value !== 'never' && value !== 'daily' && value !== 'weekly' && value !== 'monthly' && value !== 'custom') {
    throw new SubscriptionContractError();
  }
  return value;
}

function subscriptionStatus(value: unknown): UserSubscriptionStatus {
  if (value !== 'active' && value !== 'expired' && value !== 'cancelled') {
    throw new SubscriptionContractError();
  }
  return value;
}

function decimalPrice(value: unknown): string {
  const parsed = boundedText(value, 16, 1);
  if (!/^(?:0|[1-9]\d{0,3})(?:\.\d{1,6})?$/.test(parsed) || Number(parsed) > 9999) {
    throw new SubscriptionContractError();
  }
  return parsed;
}

function currency(value: unknown): string {
  const parsed = boundedText(value, 8, 3);
  if (!/^[A-Z]{3,8}$/.test(parsed)) throw new SubscriptionContractError();
  return parsed;
}

function optionalProductId(value: unknown, kind: 'generic' | 'waffo' = 'generic'): string {
  const parsed = boundedText(value, 128);
  if (/\p{Cc}|\s/u.test(parsed)) throw new SubscriptionContractError();
  if (kind === 'waffo' && parsed !== '' && !/^PROD_[A-Za-z0-9]{22}$/.test(parsed)) {
    throw new SubscriptionContractError();
  }
  return parsed;
}

function successfulEnvelope(value: unknown, requireData: boolean): UnknownRecord {
  boundedPayload(value);
  const envelope = record(value);
  exactKeys(envelope, new Set(['success', 'message', 'data']));
  if (envelope.success !== true || (requireData && !Object.hasOwn(envelope, 'data'))) {
    throw new SubscriptionContractError();
  }
  if (envelope.message !== undefined) boundedText(envelope.message, 512);
  return envelope;
}

function parsePlan(value: unknown): ManagedSubscriptionPlan {
  const raw = record(value);
  exactKeys(raw, PLAN_KEYS);
  if (typeof raw.enabled !== 'boolean' || typeof raw.allow_balance_pay !== 'boolean'
    || typeof raw.allow_wallet_overflow !== 'boolean') {
    throw new SubscriptionContractError();
  }
  return {
    id: integer(raw.id, 1, MAX_ID),
    title: boundedText(raw.title, 128, 1),
    subtitle: boundedText(raw.subtitle, 255),
    priceAmount: decimalPrice(raw.price_amount),
    currency: currency(raw.currency),
    durationUnit: durationUnit(raw.duration_unit),
    durationValue: integer(raw.duration_value, 0, MAX_ID),
    customSeconds: integer(raw.custom_seconds, 0, MAX_SAFE_COUNTER),
    enabled: raw.enabled,
    sortOrder: integer(raw.sort_order, -MAX_ID, MAX_ID),
    allowBalancePay: raw.allow_balance_pay,
    allowWalletOverflow: raw.allow_wallet_overflow,
    stripePriceId: optionalProductId(raw.stripe_price_id),
    creemProductId: optionalProductId(raw.creem_product_id),
    waffoPancakeProductId: optionalProductId(raw.waffo_pancake_product_id, 'waffo'),
    maxPurchasePerUser: integer(raw.max_purchase_per_user, 0, MAX_ID),
    upgradeGroup: boundedText(raw.upgrade_group, 64),
    downgradeGroup: boundedText(raw.downgrade_group, 64),
    totalAmount: integer(raw.total_amount, 0, MAX_QUOTA),
    quotaResetPeriod: resetPeriod(raw.quota_reset_period),
    quotaResetCustomSeconds: integer(raw.quota_reset_custom_seconds, 0, MAX_SAFE_COUNTER),
    createdAt: integer(raw.created_at, 0, MAX_TIMESTAMP),
    updatedAt: integer(raw.updated_at, 0, MAX_TIMESTAMP),
  };
}

function parsePlanRecord(value: unknown): ManagedSubscriptionPlan {
  const raw = record(value);
  exactKeys(raw, new Set(['plan']));
  return parsePlan(raw.plan);
}

export function parseSubscriptionPlansResponse(value: unknown): ManagedSubscriptionPlan[] {
  const data = successfulEnvelope(value, true).data;
  if (!Array.isArray(data) || data.length > MAX_PLANS) throw new SubscriptionContractError();
  const plans = data.map(parsePlanRecord);
  if (new Set(plans.map((plan) => plan.id)).size !== plans.length) throw new SubscriptionContractError();
  return plans;
}

export function parseCreatedSubscriptionPlanResponse(value: unknown): ManagedSubscriptionPlan {
  return parsePlan(successfulEnvelope(value, true).data);
}

function parseUserSubscription(value: unknown): ManagedUserSubscription {
  const raw = record(value);
  exactKeys(raw, SUBSCRIPTION_KEYS);
  if (typeof raw.allow_wallet_overflow !== 'boolean') throw new SubscriptionContractError();
  return {
    id: integer(raw.id, 1, MAX_ID),
    userId: integer(raw.user_id, 1, MAX_ID),
    planId: integer(raw.plan_id, 1, MAX_ID),
    amountTotal: integer(raw.amount_total, 0, MAX_QUOTA),
    amountUsed: integer(raw.amount_used, 0, MAX_QUOTA),
    startTime: integer(raw.start_time, 0, MAX_TIMESTAMP),
    endTime: integer(raw.end_time, 0, MAX_TIMESTAMP),
    status: subscriptionStatus(raw.status),
    source: boundedText(raw.source, 32),
    lastResetTime: integer(raw.last_reset_time, 0, MAX_TIMESTAMP),
    nextResetTime: integer(raw.next_reset_time, 0, MAX_TIMESTAMP),
    upgradeGroup: boundedText(raw.upgrade_group, 64),
    previousUserGroup: boundedText(raw.prev_user_group, 64),
    downgradeGroup: boundedText(raw.downgrade_group, 64),
    allowWalletOverflow: raw.allow_wallet_overflow,
    createdAt: integer(raw.created_at, 0, MAX_TIMESTAMP),
    updatedAt: integer(raw.updated_at, 0, MAX_TIMESTAMP),
  };
}

export function parseUserSubscriptionsResponse(value: unknown, expectedUserId: number): ManagedUserSubscription[] {
  const safeUserId = integer(expectedUserId, 1, MAX_ID);
  const data = successfulEnvelope(value, true).data;
  if (!Array.isArray(data) || data.length > MAX_SUBSCRIPTIONS) throw new SubscriptionContractError();
  const subscriptions = data.map((candidate) => {
    const wrapper = record(candidate);
    exactKeys(wrapper, new Set(['subscription']));
    const subscription = parseUserSubscription(wrapper.subscription);
    if (subscription.userId !== safeUserId) throw new SubscriptionContractError();
    return subscription;
  });
  if (new Set(subscriptions.map((subscription) => subscription.id)).size !== subscriptions.length) {
    throw new SubscriptionContractError();
  }
  return subscriptions;
}

export function parseSubscriptionResetResponse(value: unknown, expectedPlanId: number): SubscriptionResetResult {
  const data = record(successfulEnvelope(value, true).data);
  exactKeys(data, new Set(['plan_id', 'matched_count', 'reset_count', 'user_count', 'advance_reset_time']));
  if (typeof data.advance_reset_time !== 'boolean') throw new SubscriptionContractError();
  const result = {
    planId: integer(data.plan_id, 1, MAX_ID),
    matchedCount: integer(data.matched_count, 0, MAX_RESET_COUNT),
    resetCount: integer(data.reset_count, 0, MAX_RESET_COUNT),
    userCount: integer(data.user_count, 0, MAX_RESET_COUNT),
    advanceResetTime: data.advance_reset_time,
  };
  if (result.planId !== integer(expectedPlanId, 1, MAX_ID)
    || result.resetCount > result.matchedCount || result.userCount > result.matchedCount) {
    throw new SubscriptionContractError();
  }
  return result;
}

export function parsePaymentComplianceResponse(value: unknown): PaymentComplianceState {
  const data = record(successfulEnvelope(value, true).data);
  if (typeof data.payment_compliance_confirmed !== 'boolean') {
    throw new SubscriptionContractError();
  }
  const termsVersion = boundedText(data.payment_compliance_terms_version, 64);
  return {
    confirmed: data.payment_compliance_confirmed === true && termsVersion === 'v1',
    termsVersion,
  };
}

function parseMutationResponse(value: unknown): void {
  const envelope = successfulEnvelope(value, false);
  if (envelope.data === undefined || envelope.data === null) return;
  const data = record(envelope.data);
  exactKeys(data, new Set(['message']));
  if (data.message !== undefined) boundedText(data.message, 512);
}

function validId(value: number): number {
  return integer(value, 1, MAX_ID);
}

export function validateSubscriptionPlanInput(input: SubscriptionPlanInput): SubscriptionPlanInput {
  const title = formText(input.title.trim(), 128, 1);
  const subtitle = formText(input.subtitle.trim(), 255);
  const unit = durationUnit(input.durationUnit);
  const durationValue = integer(input.durationValue, 1, MAX_ID);
  const customSeconds = unit === 'custom'
    ? integer(input.customSeconds, 1, MAX_SAFE_COUNTER)
    : 0;
  const period = resetPeriod(input.quotaResetPeriod);
  const quotaResetCustomSeconds = period === 'custom'
    ? integer(input.quotaResetCustomSeconds, 1, MAX_SAFE_COUNTER)
    : 0;
  if (typeof input.enabled !== 'boolean' || typeof input.allowBalancePay !== 'boolean'
    || typeof input.allowWalletOverflow !== 'boolean') {
    throw new SubscriptionContractError();
  }
  return {
    title,
    subtitle,
    priceAmount: decimalPrice(input.priceAmount.trim()),
    durationUnit: unit,
    durationValue,
    customSeconds,
    enabled: input.enabled,
    sortOrder: integer(input.sortOrder, -MAX_ID, MAX_ID),
    allowBalancePay: input.allowBalancePay,
    allowWalletOverflow: input.allowWalletOverflow,
    stripePriceId: optionalProductId(input.stripePriceId.trim()),
    creemProductId: optionalProductId(input.creemProductId.trim()),
    waffoPancakeProductId: optionalProductId(input.waffoPancakeProductId.trim(), 'waffo'),
    maxPurchasePerUser: integer(input.maxPurchasePerUser, 0, MAX_ID),
    upgradeGroup: formText(input.upgradeGroup.trim(), 64),
    downgradeGroup: formText(input.downgradeGroup.trim(), 64),
    totalAmount: integer(input.totalAmount, 0, MAX_QUOTA),
    quotaResetPeriod: period,
    quotaResetCustomSeconds,
  };
}

function planPayload(input: SubscriptionPlanInput): { plan: Record<string, unknown> } {
  const safe = validateSubscriptionPlanInput(input);
  return {
    plan: {
      title: safe.title,
      subtitle: safe.subtitle,
      price_amount: safe.priceAmount,
      currency: 'USD',
      duration_unit: safe.durationUnit,
      duration_value: safe.durationValue,
      custom_seconds: safe.customSeconds,
      enabled: safe.enabled,
      sort_order: safe.sortOrder,
      allow_balance_pay: safe.allowBalancePay,
      allow_wallet_overflow: safe.allowWalletOverflow,
      stripe_price_id: safe.stripePriceId,
      creem_product_id: safe.creemProductId,
      waffo_pancake_product_id: safe.waffoPancakeProductId,
      max_purchase_per_user: safe.maxPurchasePerUser,
      upgrade_group: safe.upgradeGroup,
      downgrade_group: safe.downgradeGroup,
      total_amount: safe.totalAmount,
      quota_reset_period: safe.quotaResetPeriod,
      quota_reset_custom_seconds: safe.quotaResetCustomSeconds,
    },
  };
}

const responseLimits = {
  timeout: REQUEST_TIMEOUT_MS,
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

export async function listSubscriptionPlans(signal?: AbortSignal): Promise<ManagedSubscriptionPlan[]> {
  const response = await api.get<unknown>('/subscription/admin/plans', { signal, ...responseLimits });
  return parseSubscriptionPlansResponse(response.data);
}

export async function getPaymentComplianceState(signal?: AbortSignal): Promise<PaymentComplianceState> {
  const response = await api.get<unknown>('/user/topup/info', { signal, ...responseLimits });
  return parsePaymentComplianceResponse(response.data);
}

export async function createSubscriptionPlan(input: SubscriptionPlanInput): Promise<ManagedSubscriptionPlan> {
  const response = await api.post<unknown>('/subscription/admin/plans', planPayload(input), responseLimits);
  return parseCreatedSubscriptionPlanResponse(response.data);
}

export async function updateSubscriptionPlan(id: number, input: SubscriptionPlanInput): Promise<void> {
  const response = await api.put<unknown>(`/subscription/admin/plans/${validId(id)}`, planPayload(input), responseLimits);
  parseMutationResponse(response.data);
}

export async function setSubscriptionPlanStatus(id: number, enabled: boolean): Promise<void> {
  if (typeof enabled !== 'boolean') throw new SubscriptionContractError();
  const response = await api.patch<unknown>(
    `/subscription/admin/plans/${validId(id)}`,
    { enabled },
    responseLimits,
  );
  parseMutationResponse(response.data);
}

export async function resetPlanSubscriptions(
  id: number,
  advanceResetTime: boolean,
): Promise<SubscriptionResetResult> {
  if (typeof advanceResetTime !== 'boolean') throw new SubscriptionContractError();
  const safeId = validId(id);
  const response = await api.post<unknown>(
    `/subscription/admin/plans/${safeId}/subscriptions/reset`,
    { advance_reset_time: advanceResetTime },
    responseLimits,
  );
  return parseSubscriptionResetResponse(response.data, safeId);
}

export async function listUserSubscriptions(
  userId: number,
  signal?: AbortSignal,
): Promise<ManagedUserSubscription[]> {
  const safeUserId = validId(userId);
  const response = await api.get<unknown>(
    `/subscription/admin/users/${safeUserId}/subscriptions`,
    { signal, ...responseLimits },
  );
  return parseUserSubscriptionsResponse(response.data, safeUserId);
}

export async function createUserSubscription(userId: number, planId: number): Promise<void> {
  const response = await api.post<unknown>(
    `/subscription/admin/users/${validId(userId)}/subscriptions`,
    { plan_id: validId(planId) },
    responseLimits,
  );
  parseMutationResponse(response.data);
}

export async function resetUserSubscriptionsByPlan(
  userId: number,
  planId: number,
  advanceResetTime: boolean,
): Promise<SubscriptionResetResult> {
  if (typeof advanceResetTime !== 'boolean') throw new SubscriptionContractError();
  const safePlanId = validId(planId);
  const response = await api.post<unknown>(
    `/subscription/admin/users/${validId(userId)}/subscriptions/reset`,
    { plan_id: safePlanId, advance_reset_time: advanceResetTime },
    responseLimits,
  );
  return parseSubscriptionResetResponse(response.data, safePlanId);
}

export async function invalidateUserSubscription(subscriptionId: number): Promise<void> {
  const response = await api.post<unknown>(
    `/subscription/admin/user_subscriptions/${validId(subscriptionId)}/invalidate`,
    {},
    responseLimits,
  );
  parseMutationResponse(response.data);
}

export async function deleteUserSubscription(subscriptionId: number): Promise<void> {
  const response = await api.delete<unknown>(
    `/subscription/admin/user_subscriptions/${validId(subscriptionId)}`,
    responseLimits,
  );
  parseMutationResponse(response.data);
}
