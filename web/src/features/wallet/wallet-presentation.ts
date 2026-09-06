import {
  type PaymentKind,
  type SubscriptionPlan,
  type UserSubscription,
  type WalletOrderPage,
  type WalletTopUpInfo
} from './wallet-api';

export const HISTORY_PAGE_SIZES = [10, 20, 50, 100] as const;

export const subscriptionStatusKeys: Record<UserSubscription['status'], string> = {
  active: 'Active',
  expired: 'Expired',
  cancelled: 'Cancelled',
};

export const durationUnitKeys: Record<SubscriptionPlan['durationUnit'], string> = {
  year: 'Year',
  month: 'Month',
  day: 'Day',
  hour: 'Hour',
  custom: 'Custom',
};

export const resetPeriodKeys: Record<SubscriptionPlan['quotaResetPeriod'], string> = {
  never: 'Never',
  daily: 'Daily',
  weekly: 'Weekly',
  monthly: 'Monthly',
  custom: 'Custom',
};

export type Notice = { kind: 'success' | 'error'; text: string } | null;

export interface PaymentOption {
  id: string;
  label: string;
  kind: PaymentKind;
  minimum: number;
  paymentMethod?: string;
  payMethodIndex?: number;
}

export function integerInput(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

export function dateTime(timestamp: number, language: string): string {
  if (timestamp === 0) return '—';
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' })
    .format(new Date(timestamp * 1_000));
}

export function number(value: number, language: string): string {
  return new Intl.NumberFormat(language).format(value);
}

export function decimalNumber(value: number, language: string): string {
  return new Intl.NumberFormat(language, { maximumFractionDigits: 6 }).format(value);
}

export function money(value: number, language: string): string {
  return new Intl.NumberFormat(language, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value);
}

export function quotaDisplayUnit(info: WalletTopUpInfo): string {
  if (info.quotaDisplayType === 'currency') return 'USD';
  if (info.quotaDisplayType === 'cny') return 'CNY';
  return info.currencySymbol;
}

export function quotaToDisplayAmount(quota: number, info: WalletTopUpInfo): number {
  if (info.quotaDisplayType === 'tokens') return quota;
  const usd = quota / info.quotaPerUnit;
  return usd * info.currencyExchangeRate;
}

export function formatQuotaForDisplay(quota: number, info: WalletTopUpInfo, language: string): string {
  const amount = quotaToDisplayAmount(quota, info);
  if (!Number.isFinite(amount)) return '—';
  return `${decimalNumber(amount, language)} ${quotaDisplayUnit(info)}`;
}

export function formatCreditForDisplay(amount: number, info: WalletTopUpInfo, language: string): string {
  const displayed = info.quotaDisplayType === 'tokens'
    ? amount * info.quotaPerUnit
    : amount * info.currencyExchangeRate;
  if (!Number.isFinite(displayed) || !Number.isSafeInteger(displayed) && info.quotaDisplayType === 'tokens') return '—';
  return `${decimalNumber(displayed, language)} ${quotaDisplayUnit(info)}`;
}

export function positiveDecimalFraction(value: string): { numerator: bigint; denominator: bigint } | null {
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

export function transferInputToQuota(value: string, info: WalletTopUpInfo): number | null {
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

export function subscriptionBalanceCost(plan: SubscriptionPlan, info: WalletTopUpInfo): number | null {
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

export function knownProviderName(value: string): string {
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

export function orderProviderText(
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

export function paymentOptions(info: WalletTopUpInfo | null): PaymentOption[] {
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
