import { api, type User } from '../../shared/api/client';
import { turnstileParams } from "../security";

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_TEXT = 2_048;
const MAX_QUOTA = Number.MAX_SAFE_INTEGER;
const MAX_HISTORY_ROWS = 100;
const MAX_PAYMENT_METHODS = 32;
const MAX_PRODUCTS = 100;
const MAX_PRESETS = 100;
const MAX_CHECKOUT_FORM_FIELDS = 32;
const MAX_CHECKOUT_FORM_FIELD_NAME_BYTES = 64;
const MAX_CHECKOUT_FORM_FIELD_VALUE_BYTES = 2_048;
const MAX_CHECKOUT_FORM_BYTES = 16 * 1024;

const responseLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

type UnknownRecord = Record<string, unknown>;

export type PaymentKind = 'epay' | 'stripe' | 'waffo' | 'waffo-pancake';
export type TopUpStatus = 'pending' | 'success' | 'failed' | 'cancelled' | 'expired';
export type BillingPreference = 'subscription_first' | 'wallet_first' | 'subscription_only' | 'wallet_only';
export type QuotaDisplayType = 'currency' | 'cny' | 'tokens' | 'custom';
export type SubscriptionDurationUnit = 'year' | 'month' | 'day' | 'hour' | 'custom';
export type SubscriptionResetPeriod = 'never' | 'daily' | 'weekly' | 'monthly' | 'custom';

export interface WalletPaymentMethod {
  name: string;
  type: string;
  minimum: number | null;
}

export interface WaffoPaymentMethod {
  name: string;
  payMethodType: string;
  payMethodName: string;
}

export interface CreemProduct {
  productId: string;
  name: string;
  price: number;
  currency: string;
  quota: number;
}

export interface WalletTopUpInfo {
  onlineEnabled: boolean;
  stripeEnabled: boolean;
  creemEnabled: boolean;
  waffoEnabled: boolean;
  waffoPancakeEnabled: boolean;
  redemptionEnabled: boolean;
  complianceConfirmed: boolean;
  complianceVersion: string;
  minimum: number;
  stripeMinimum: number;
  waffoMinimum: number;
  waffoPancakeMinimum: number;
  presets: number[];
  discounts: Record<number, number>;
  paymentMethods: WalletPaymentMethod[];
  waffoMethods: WaffoPaymentMethod[];
  creemProducts: CreemProduct[];
  topUpLink: string | null;
  quotaDisplayType: QuotaDisplayType;
  quotaPerUnit: number;
  usdExchangeRate: number;
  currencySymbol: string;
  currencyExchangeRate: number;
}

export interface WalletOrder {
  id: number;
  userId: number;
  amount: number;
  money: number;
  tradeNo: string;
  paymentMethod: string;
  paymentProvider: string;
  createdAt: number;
  completedAt: number;
  status: TopUpStatus;
}

export interface WalletOrderPage {
  page: number;
  pageSize: number;
  total: number;
  items: WalletOrder[];
}

export interface AffiliateSummary {
  code: string;
  count: number;
  availableQuota: number;
  lifetimeQuota: number;
}

export interface SubscriptionPlan {
  id: number;
  title: string;
  subtitle: string;
  priceAmount: string;
  currency: string;
  durationUnit: SubscriptionDurationUnit;
  durationValue: number;
  customSeconds: number;
  totalAmount: number;
  allowBalancePay: boolean;
  allowWalletOverflow: boolean;
  maxPurchasePerUser: number;
  upgradeGroup: string;
  downgradeGroup: string;
  quotaResetPeriod: SubscriptionResetPeriod;
  quotaResetCustomSeconds: number;
  stripePriceId: string;
  creemProductId: string;
  waffoPancakeProductId: string;
}

export interface UserSubscription {
  id: number;
  planId: number;
  amountTotal: number;
  amountUsed: number;
  startTime: number;
  endTime: number;
  nextResetTime: number;
  lastResetTime: number;
  upgradeGroup: string;
  downgradeGroup: string;
  source: string;
  status: 'active' | 'expired' | 'cancelled';
  allowWalletOverflow: boolean;
}

export interface SubscriptionSummary {
  billingPreference: BillingPreference;
  active: UserSubscription[];
  history: UserSubscription[];
}

export interface CheckoutFormField {
  name: string;
  value: string;
}

export type CheckoutResult =
  | { method: 'GET'; action: string; orderId: string }
  | { method: 'POST'; action: string; fields: CheckoutFormField[]; orderId: string };

export type CheckoutRequest =
  | { kind: 'epay'; amount: number; paymentMethod: string }
  | { kind: 'stripe'; amount: number }
  | { kind: 'waffo'; amount: number; payMethodIndex: number }
  | { kind: 'waffo-pancake'; amount: number }
  | { kind: 'creem'; productId: string };

export type SubscriptionCheckoutRequest =
  | { kind: 'epay'; planId: number; paymentMethod: string }
  | { kind: 'stripe'; planId: number }
  | { kind: 'waffo-pancake'; planId: number }
  | { kind: 'creem'; planId: number };

export class WalletContractError extends Error {
  constructor() {
    super('Invalid wallet response');
    this.name = 'WalletContractError';
  }
}

export class TurnstileRequiredError extends Error {
  constructor() {
    super('Human verification required');
    this.name = 'TurnstileRequiredError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new WalletContractError();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new WalletContractError();
  }
  if (encoded === undefined || encoded.length > MAX_RESPONSE_BYTES) throw new WalletContractError();
}

function own(value: UnknownRecord, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function text(value: unknown, maximum = MAX_TEXT, allowEmpty = true): string {
  if (typeof value !== 'string' || value.length > maximum || (!allowEmpty && value.length === 0)) {
    throw new WalletContractError();
  }
  return value;
}

function safeInteger(value: unknown, minimum = 0, maximum = MAX_QUOTA): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new WalletContractError();
  }
  return value as number;
}

function finite(value: unknown, minimum = 0, maximum = MAX_QUOTA): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new WalletContractError();
  }
  return value;
}

function bool(value: unknown): boolean {
  if (typeof value !== 'boolean') throw new WalletContractError();
  return value;
}

function standardData(value: unknown): unknown {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true || !own(envelope, 'data')) throw new WalletContractError();
  return envelope.data;
}

function standardSuccess(value: unknown): void {
  boundedPayload(value);
  if (record(value).success !== true) throw new WalletContractError();
}

function decimal(value: unknown): string {
  const result = text(value, 64, false);
  if (!/^(?:0|[1-9]\d*)(?:\.\d{1,2})?$/.test(result)) throw new WalletContractError();
  const parsed = Number(result);
  if (!Number.isFinite(parsed) || parsed <= 0 || parsed > MAX_QUOTA) throw new WalletContractError();
  return result;
}

function identifier(value: unknown, maximum = 64): string {
  const result = text(value, maximum, false);
  if (!/^[A-Za-z0-9._-]+$/.test(result)) throw new WalletContractError();
  return result;
}

function optionalNumberString(value: unknown): number | null {
  if (value === undefined || value === null || value === '') return null;
  const parsed = typeof value === 'number' ? value : Number(text(value, 32, false));
  return safeInteger(parsed, 1);
}

function safeURL(value: unknown): string {
  const raw = text(value, 2_048, false);
  if (raw.startsWith('/') && !raw.startsWith('//')) return raw;
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new WalletContractError();
  }
  const loopbackHTTP = parsed.protocol === 'http:'
    && ['localhost', '127.0.0.1', '[::1]'].includes(parsed.hostname);
  if ((parsed.protocol !== 'https:' && !loopbackHTTP) || parsed.username || parsed.password) {
    throw new WalletContractError();
  }
  return parsed.toString();
}

function optionalSafeURL(value: unknown): string | null {
  if (value === undefined || value === null || value === '') return null;
  return safeURL(value);
}

function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function safeFormAction(value: unknown): string {
  const raw = text(value, 2_048, false);
  if (utf8Bytes(raw) > 2_048 || raw !== raw.trim() || raw.includes('\\')
    || !/^https?:\/\//i.test(raw)) throw new WalletContractError();
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new WalletContractError();
  }
  const hostname = parsed.hostname.toLowerCase().replace(/\.$/, '');
  const loopbackHTTP = parsed.protocol === 'http:'
    && (hostname === 'localhost' || hostname.endsWith('.localhost')
      || hostname.startsWith('127.') || hostname === '[::1]');
  if ((parsed.protocol !== 'https:' && !loopbackHTTP) || parsed.username || parsed.password
    || parsed.search || parsed.hash) {
    throw new WalletContractError();
  }
  return parsed.toString();
}

function parseCheckoutFormFields(value: unknown): CheckoutFormField[] {
  const data = record(value);
  const entries = Object.entries(data);
  if (entries.length === 0 || entries.length > MAX_CHECKOUT_FORM_FIELDS) {
    throw new WalletContractError();
  }
  const encoded = JSON.stringify(data);
  if (utf8Bytes(encoded) > MAX_CHECKOUT_FORM_BYTES) throw new WalletContractError();
  return entries.map(([name, rawValue]) => {
    if (!/^[A-Za-z][A-Za-z0-9_]*$/.test(name)
      || utf8Bytes(name) > MAX_CHECKOUT_FORM_FIELD_NAME_BYTES
      || typeof rawValue !== 'string'
      || utf8Bytes(rawValue) > MAX_CHECKOUT_FORM_FIELD_VALUE_BYTES) {
      throw new WalletContractError();
    }
    return { name, value: rawValue };
  });
}

function parsePaymentMethod(value: unknown): WalletPaymentMethod {
  const method = record(value);
  return {
    name: text(method.name, 128, false),
    type: identifier(method.type),
    minimum: optionalNumberString(method.min_topup),
  };
}

function parseWaffoMethod(value: unknown): WaffoPaymentMethod {
  const method = record(value);
  return {
    name: text(method.name, 255, false),
    payMethodType: text(method.payMethodType, 128),
    payMethodName: text(method.payMethodName, 128),
  };
}

function parseCreemProduct(value: unknown): CreemProduct {
  const product = record(value);
  const currency = identifier(product.currency, 8).toUpperCase();
  return {
    productId: text(product.productId, 255, false),
    name: text(product.name, 255, false),
    price: finite(product.price, 0.01),
    currency,
    quota: safeInteger(product.quota, 1),
  };
}

function boundedArray(value: unknown, maximum: number): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) throw new WalletContractError();
  return value;
}

function parseCreemProducts(value: unknown): CreemProduct[] {
  if (value === undefined || value === null || value === '') return [];
  let decoded: unknown = value;
  if (typeof value === 'string') {
    if (value.length > 128 * 1024) throw new WalletContractError();
    try {
      decoded = JSON.parse(value) as unknown;
    } catch {
      throw new WalletContractError();
    }
  }
  return boundedArray(decoded, MAX_PRODUCTS).map(parseCreemProduct);
}

function parseDiscounts(value: unknown): Record<number, number> {
  const raw = record(value);
  const entries = Object.entries(raw);
  if (entries.length > MAX_PRESETS) throw new WalletContractError();
  const result: Record<number, number> = {};
  for (const [amountText, discountValue] of entries) {
    if (!/^[1-9]\d*$/.test(amountText)) throw new WalletContractError();
    const amount = safeInteger(Number(amountText), 1);
    result[amount] = finite(discountValue, Number.EPSILON, 100);
  }
  return result;
}

function parseQuotaDisplayType(value: unknown): QuotaDisplayType {
  const result = text(value, 16, false);
  if (!['currency', 'cny', 'tokens', 'custom'].includes(result)) throw new WalletContractError();
  return result as QuotaDisplayType;
}

export function parseTopUpInfoResponse(value: unknown): WalletTopUpInfo {
  const data = record(standardData(value));
  const paymentMethods = boundedArray(data.pay_methods, MAX_PAYMENT_METHODS).map(parsePaymentMethod);
  const rawWaffoMethods = data.waffo_pay_methods === undefined || data.waffo_pay_methods === null
    ? []
    : boundedArray(data.waffo_pay_methods, MAX_PAYMENT_METHODS).map(parseWaffoMethod);
  const presets = boundedArray(data.amount_options, MAX_PRESETS).map((item) => safeInteger(item, 1));
  return {
    onlineEnabled: bool(data.enable_online_topup),
    stripeEnabled: bool(data.enable_stripe_topup),
    creemEnabled: bool(data.enable_creem_topup),
    waffoEnabled: bool(data.enable_waffo_topup),
    waffoPancakeEnabled: bool(data.enable_waffo_pancake_topup),
    redemptionEnabled: bool(data.enable_redemption),
    complianceConfirmed: bool(data.payment_compliance_confirmed),
    complianceVersion: text(data.payment_compliance_terms_version, 64),
    minimum: safeInteger(data.min_topup, 1),
    stripeMinimum: safeInteger(data.stripe_min_topup, 1),
    waffoMinimum: safeInteger(data.waffo_min_topup, 0),
    waffoPancakeMinimum: safeInteger(data.waffo_pancake_min_topup, 0),
    presets,
    discounts: parseDiscounts(data.discount),
    paymentMethods,
    waffoMethods: rawWaffoMethods,
    creemProducts: parseCreemProducts(data.creem_products),
    topUpLink: optionalSafeURL(data.topup_link),
    quotaDisplayType: parseQuotaDisplayType(data.quota_display_type),
    quotaPerUnit: safeInteger(data.quota_per_unit, 1),
    usdExchangeRate: finite(data.usd_exchange_rate, Number.EPSILON, 1_000_000),
    currencySymbol: text(data.currency_symbol, 32, false),
    currencyExchangeRate: finite(data.currency_exchange_rate, Number.EPSILON, 1_000_000),
  };
}

function parseOrder(value: unknown, expectedUserId?: number): WalletOrder {
  const item = record(value);
  const userId = safeInteger(item.user_id, 1);
  if (expectedUserId !== undefined && userId !== expectedUserId) throw new WalletContractError();
  const status = text(item.status, 16, false);
  if (!['pending', 'success', 'failed', 'cancelled', 'expired'].includes(status)) throw new WalletContractError();
  return {
    id: safeInteger(item.id, 1),
    userId,
    amount: safeInteger(item.amount, 1),
    money: finite(item.money),
    tradeNo: text(item.trade_no, 255, false),
    paymentMethod: text(item.payment_method, 50),
    paymentProvider: text(item.payment_provider, 50),
    createdAt: safeInteger(item.create_time),
    completedAt: safeInteger(item.complete_time),
    status: status as TopUpStatus,
  };
}

function parseWalletOrderPage(value: unknown, expectedUserId?: number): WalletOrderPage {
  const data = record(standardData(value));
  const pageSize = safeInteger(data.page_size, 1, MAX_HISTORY_ROWS);
  const items = boundedArray(data.items, pageSize).map((item) => parseOrder(item, expectedUserId));
  return {
    page: safeInteger(data.page, 1, 1_000_000),
    pageSize,
    total: safeInteger(data.total),
    items,
  };
}

export function parseWalletOrderPageResponse(value: unknown, expectedUserId: number): WalletOrderPage {
  return parseWalletOrderPage(value, safeInteger(expectedUserId, 1));
}

export function parseAdminWalletOrderPageResponse(value: unknown): WalletOrderPage {
  return parseWalletOrderPage(value);
}

export function parseWalletUserResponse(value: unknown): User {
  const data = record(standardData(value));
  return {
    id: safeInteger(data.id, 1),
    username: text(data.username, 64, false),
    display_name: text(data.display_name, 128),
    role: safeInteger(data.role, 0, 100),
    group: text(data.group, 64, false),
    quota: safeInteger(data.quota, -MAX_QUOTA),
    used_quota: safeInteger(data.used_quota, 0),
    request_count: safeInteger(data.request_count, 0),
    ...(typeof data.email === 'string' ? { email: text(data.email, 320) } : {}),
  };
}

export function parseAffiliateResponse(value: unknown): AffiliateSummary {
  const data = record(standardData(value));
  return {
    code: text(data.aff_code, 64, false),
    count: safeInteger(data.aff_count),
    availableQuota: safeInteger(data.aff_quota),
    lifetimeQuota: safeInteger(data.aff_history_quota),
  };
}

function parsePlan(value: unknown): SubscriptionPlan {
  const wrapper = record(value);
  const plan = record(wrapper.plan);
  const durationUnit = identifier(plan.duration_unit, 16);
  if (!['year', 'month', 'day', 'hour', 'custom'].includes(durationUnit)) throw new WalletContractError();
  const quotaResetPeriod = identifier(plan.quota_reset_period, 16);
  if (!['never', 'daily', 'weekly', 'monthly', 'custom'].includes(quotaResetPeriod)) {
    throw new WalletContractError();
  }
  return {
    id: safeInteger(plan.id, 1),
    title: text(plan.title, 128, false),
    subtitle: text(plan.subtitle, 255),
    priceAmount: decimal(plan.price_amount),
    currency: identifier(plan.currency, 8).toUpperCase(),
    durationUnit: durationUnit as SubscriptionDurationUnit,
    durationValue: safeInteger(plan.duration_value),
    customSeconds: safeInteger(plan.custom_seconds),
    totalAmount: safeInteger(plan.total_amount),
    allowBalancePay: bool(plan.allow_balance_pay),
    allowWalletOverflow: bool(plan.allow_wallet_overflow),
    maxPurchasePerUser: safeInteger(plan.max_purchase_per_user),
    upgradeGroup: text(plan.upgrade_group, 64),
    downgradeGroup: text(plan.downgrade_group, 64),
    quotaResetPeriod: quotaResetPeriod as SubscriptionResetPeriod,
    quotaResetCustomSeconds: safeInteger(plan.quota_reset_custom_seconds),
    stripePriceId: text(plan.stripe_price_id ?? '', 128),
    creemProductId: text(plan.creem_product_id ?? '', 128),
    waffoPancakeProductId: text(plan.waffo_pancake_product_id ?? '', 128),
  };
}

export function parseSubscriptionPlansResponse(value: unknown): SubscriptionPlan[] {
  return boundedArray(standardData(value), 100).map(parsePlan);
}

function parseSubscription(value: unknown, expectedUserId: number): UserSubscription {
  const wrapper = record(value);
  const item = record(wrapper.subscription);
  if (safeInteger(item.user_id, 1) !== expectedUserId) throw new WalletContractError();
  const status = text(item.status, 16, false);
  if (!['active', 'expired', 'cancelled'].includes(status)) throw new WalletContractError();
  return {
    id: safeInteger(item.id, 1),
    planId: safeInteger(item.plan_id, 1),
    amountTotal: safeInteger(item.amount_total),
    amountUsed: safeInteger(item.amount_used),
    startTime: safeInteger(item.start_time),
    endTime: safeInteger(item.end_time),
    nextResetTime: safeInteger(item.next_reset_time),
    lastResetTime: item.last_reset_time === undefined ? 0 : safeInteger(item.last_reset_time),
    upgradeGroup: text(item.upgrade_group, 64),
    downgradeGroup: item.downgrade_group === undefined ? '' : text(item.downgrade_group, 64),
    source: item.source === undefined ? '' : identifier(item.source, 32),
    status: status as UserSubscription['status'],
    allowWalletOverflow: bool(item.allow_wallet_overflow),
  };
}

export function parseSubscriptionSummaryResponse(value: unknown, expectedUserId: number): SubscriptionSummary {
  const data = record(standardData(value));
  const preference = text(data.billing_preference, 32, false);
  if (!['subscription_first', 'wallet_first', 'subscription_only', 'wallet_only'].includes(preference)) {
    throw new WalletContractError();
  }
  return {
    billingPreference: preference as BillingPreference,
    active: boundedArray(data.subscriptions, 100).map((item) => parseSubscription(item, expectedUserId)),
    history: boundedArray(data.all_subscriptions, 100).map((item) => parseSubscription(item, expectedUserId)),
  };
}

export function parseAmountResponse(value: unknown): string {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.message !== 'success') throw new WalletContractError();
  return decimal(envelope.data);
}

function parseLinkCheckoutResponse(value: unknown, key: 'pay_link' | 'checkout_url' | 'payment_url'): CheckoutResult {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.message !== 'success') throw new WalletContractError();
  const data = record(envelope.data);
  const orderId = data.order_id === undefined ? '' : text(data.order_id, 255);
  return { method: 'GET', action: safeURL(data[key]), orderId };
}

function parseEpayCheckoutResponse(value: unknown): CheckoutResult {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.message !== 'success') throw new WalletContractError();
  const action = safeFormAction(envelope.url);
  const fields = parseCheckoutFormFields(envelope.data);
  const tradeNumber = fields.find((field) => field.name === 'out_trade_no')?.value;
  const signature = fields.find((field) => field.name === 'sign')?.value;
  const signatureType = fields.find((field) => field.name === 'sign_type')?.value;
  if (tradeNumber === undefined || !/^[A-Za-z0-9._-]{1,255}$/.test(tradeNumber)
    || signature === undefined || !/^[a-f0-9]{32}$/.test(signature)
    || signatureType !== 'MD5') {
    throw new WalletContractError();
  }
  return {
    method: 'POST',
    action,
    fields,
    orderId: tradeNumber,
  };
}

function validAmount(amount: number): number {
  return safeInteger(amount, 1);
}

export async function getTopUpInfo(signal?: AbortSignal): Promise<WalletTopUpInfo> {
  const response = await api.get<unknown>('/user/topup/info', { signal, ...responseLimits });
  return parseTopUpInfoResponse(response.data);
}

export async function getWalletUser(signal?: AbortSignal): Promise<User> {
  const response = await api.get<unknown>('/user/self', { signal, ...responseLimits });
  return parseWalletUserResponse(response.data);
}

export async function getCheckInStatus(signal?: AbortSignal): Promise<boolean> {
  const response = await api.get<unknown>('/user/checkin/status', { signal, ...responseLimits });
  return bool(record(standardData(response.data)).checked_in);
}

export async function checkIn(token?: string): Promise<{ quotaAwarded: number; date: string }> {
  const config = token === undefined
    ? responseLimits
    : { ...responseLimits, params: turnstileParams(token) };
  const response = await api.post<unknown>('/user/checkin', {}, config);
  boundedPayload(response.data);
  const envelope = record(response.data);
  if (envelope.success !== true) {
    const message = typeof envelope.message === 'string' && envelope.message.length <= MAX_TEXT
      ? envelope.message
      : '';
    if (/turnstile/i.test(message)) throw new TurnstileRequiredError();
    throw new WalletContractError();
  }
  if (!own(envelope, 'data')) throw new WalletContractError();
  const data = record(envelope.data);
  return {
    quotaAwarded: safeInteger(data.quota_awarded, 1),
    date: text(data.checkin_date, 10, false),
  };
}

export async function redeemTopUpCode(code: string): Promise<number> {
  const key = text(code, 128, false);
  const response = await api.post<unknown>('/user/topup', { key }, responseLimits);
  return safeInteger(standardData(response.data), 1);
}

export async function getWalletOrders(
  userId: number,
  page: number,
  pageSize: number,
  keyword: string,
  signal?: AbortSignal,
): Promise<WalletOrderPage> {
  const safeUserId = safeInteger(userId, 1);
  const safePage = safeInteger(page, 1, 1_000_000);
  const safePageSize = safeInteger(pageSize, 1, MAX_HISTORY_ROWS);
  const safeKeyword = text(keyword, 255);
  const params: Record<string, string | number> = { p: safePage, page_size: safePageSize };
  if (safeKeyword !== '') params.keyword = safeKeyword;
  const response = await api.get<unknown>('/user/topup/self', { params, signal, ...responseLimits });
  return parseWalletOrderPageResponse(response.data, safeUserId);
}

export async function getAdminWalletOrders(
  page: number,
  pageSize: number,
  keyword: string,
  signal?: AbortSignal,
): Promise<WalletOrderPage> {
  const safePage = safeInteger(page, 1, 1_000_000);
  const safePageSize = safeInteger(pageSize, 1, MAX_HISTORY_ROWS);
  const safeKeyword = text(keyword, 255);
  const params: Record<string, string | number> = { p: safePage, page_size: safePageSize };
  if (safeKeyword !== '') params.keyword = safeKeyword;
  const response = await api.get<unknown>('/user/topup', { params, signal, ...responseLimits });
  return parseAdminWalletOrderPageResponse(response.data);
}

export async function completeWalletOrder(tradeNo: string): Promise<void> {
  const boundedTradeNo = text(tradeNo, 255, false);
  const safeTradeNo = boundedTradeNo.trim();
  if (safeTradeNo === '') throw new WalletContractError();
  const response = await api.post<unknown>('/user/topup/complete', { trade_no: safeTradeNo }, responseLimits);
  standardSuccess(response.data);
}

export async function getAffiliateSummary(signal?: AbortSignal): Promise<AffiliateSummary> {
  const response = await api.get<unknown>('/user/aff', { signal, ...responseLimits });
  return parseAffiliateResponse(response.data);
}

export async function transferAffiliateQuota(quota: number): Promise<void> {
  const response = await api.post<unknown>('/user/aff_transfer', { quota: validAmount(quota) }, responseLimits);
  standardSuccess(response.data);
}

export async function getSubscriptionPlans(signal?: AbortSignal): Promise<SubscriptionPlan[]> {
  const response = await api.get<unknown>('/subscription/plans', { signal, ...responseLimits });
  return parseSubscriptionPlansResponse(response.data);
}

export async function getSubscriptionSummary(userId: number, signal?: AbortSignal): Promise<SubscriptionSummary> {
  const safeUserId = safeInteger(userId, 1);
  const response = await api.get<unknown>('/subscription/self', { signal, ...responseLimits });
  return parseSubscriptionSummaryResponse(response.data, safeUserId);
}

export async function purchaseSubscriptionWithBalance(planId: number): Promise<void> {
  const response = await api.post<unknown>(
    '/subscription/balance/pay',
    { plan_id: safeInteger(planId, 1) },
    responseLimits,
  );
  standardSuccess(response.data);
}

export async function updateBillingPreference(preference: BillingPreference): Promise<BillingPreference> {
  if (!['subscription_first', 'wallet_first', 'subscription_only', 'wallet_only'].includes(preference)) {
    throw new WalletContractError();
  }
  const response = await api.put<unknown>('/subscription/self/preference', {
    billing_preference: preference,
  }, responseLimits);
  const data = record(standardData(response.data));
  const result = text(data.billing_preference, 32, false);
  if (!['subscription_first', 'wallet_first', 'subscription_only', 'wallet_only'].includes(result)) {
    throw new WalletContractError();
  }
  return result as BillingPreference;
}

export async function signOutWallet(): Promise<void> {
  const response = await api.post<unknown>('/user/auth/logout', {}, responseLimits);
  standardSuccess(response.data);
}

export async function calculatePaymentAmount(
  kind: PaymentKind,
  amount: number,
  payMethodIndex?: number,
): Promise<string> {
  const path = kind === 'epay' ? '/user/amount' : `/user/${kind}/amount`;
  const payload: Record<string, number> = { amount: validAmount(amount) };
  if (kind === 'waffo' && payMethodIndex !== undefined) {
    payload.pay_method_index = safeInteger(payMethodIndex, 0, MAX_PAYMENT_METHODS - 1);
  }
  const response = await api.post<unknown>(path, payload, responseLimits);
  return parseAmountResponse(response.data);
}

export async function createCheckout(request: CheckoutRequest): Promise<CheckoutResult> {
  if (request.kind === 'epay') {
    const response = await api.post<unknown>('/user/pay', {
      amount: validAmount(request.amount),
      payment_method: identifier(request.paymentMethod),
    }, responseLimits);
    return parseEpayCheckoutResponse(response.data);
  }
  if (request.kind === 'stripe') {
    const response = await api.post<unknown>('/user/stripe/pay', {
      amount: validAmount(request.amount),
      payment_method: 'stripe',
    }, responseLimits);
    return parseLinkCheckoutResponse(response.data, 'pay_link');
  }
  if (request.kind === 'waffo') {
    const response = await api.post<unknown>('/user/waffo/pay', {
      amount: validAmount(request.amount),
      pay_method_index: safeInteger(request.payMethodIndex, 0, MAX_PAYMENT_METHODS - 1),
    }, responseLimits);
    return parseLinkCheckoutResponse(response.data, 'payment_url');
  }
  if (request.kind === 'waffo-pancake') {
    const response = await api.post<unknown>('/user/waffo-pancake/pay', {
      amount: validAmount(request.amount),
    }, responseLimits);
    return parseLinkCheckoutResponse(response.data, 'checkout_url');
  }
  const response = await api.post<unknown>('/user/creem/pay', {
    product_id: text(request.productId, 255, false),
    payment_method: 'creem',
  }, responseLimits);
  return parseLinkCheckoutResponse(response.data, 'checkout_url');
}

export async function createSubscriptionCheckout(request: SubscriptionCheckoutRequest): Promise<CheckoutResult> {
  const planId = safeInteger(request.planId, 1);
  if (request.kind === 'epay') {
    const response = await api.post<unknown>('/subscription/epay/pay', {
      plan_id: planId,
      payment_method: identifier(request.paymentMethod),
    }, responseLimits);
    return parseEpayCheckoutResponse(response.data);
  }
  if (request.kind === 'stripe') {
    const response = await api.post<unknown>('/subscription/stripe/pay', { plan_id: planId }, responseLimits);
    return parseLinkCheckoutResponse(response.data, 'pay_link');
  }
  if (request.kind === 'waffo-pancake') {
    const response = await api.post<unknown>('/subscription/waffo-pancake/pay', { plan_id: planId }, responseLimits);
    return parseLinkCheckoutResponse(response.data, 'checkout_url');
  }
  const response = await api.post<unknown>('/subscription/creem/pay', { plan_id: planId }, responseLimits);
  return parseLinkCheckoutResponse(response.data, 'checkout_url');
}
