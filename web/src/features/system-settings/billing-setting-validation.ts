const MAX_JSON_DEPTH = 64;
const MAX_QUOTA = 2_147_483_647;
const MAX_TOP_UP_GROUPS = 256;
const MAX_TOP_UP_GROUP_RATIO = 1_000_000;
const MAX_PAYMENT_METHODS = 64;
const MAX_PAYMENT_METHOD_FIELDS = 16;
const MAX_PAYMENT_PRESETS = 100;

const encoder = new TextEncoder();

interface JSONScanResult {
  offset: number;
  duplicate: boolean;
}

interface JSONStringResult {
  offset: number;
  value: string;
}

function skipWhitespace(source: string, offset: number): number {
  while (offset < source.length && /[\t\n\r ]/u.test(source[offset])) offset += 1;
  return offset;
}

function scanString(source: string, offset: number): JSONStringResult | null {
  if (source[offset] !== '"') return null;
  const start = offset;
  offset += 1;
  while (offset < source.length) {
    const code = source.charCodeAt(offset);
    if (code === 0x22) {
      try {
        const value: unknown = JSON.parse(source.slice(start, offset + 1));
        return typeof value === 'string' ? { offset: offset + 1, value } : null;
      } catch {
        return null;
      }
    }
    if (code < 0x20) return null;
    if (code === 0x5c) {
      offset += 1;
      if (offset >= source.length) return null;
      if (source[offset] === 'u') {
        if (!/^[0-9a-fA-F]{4}$/u.test(source.slice(offset + 1, offset + 5))) return null;
        offset += 5;
        continue;
      }
      if (!/["\\/bfnrt]/u.test(source[offset])) return null;
    }
    offset += 1;
  }
  return null;
}

function scanValue(source: string, start: number, depth: number): JSONScanResult | null {
  if (depth > MAX_JSON_DEPTH) return null;
  let offset = skipWhitespace(source, start);
  if (source[offset] === '"') {
    const parsed = scanString(source, offset);
    return parsed ? { offset: parsed.offset, duplicate: false } : null;
  }
  if (source[offset] === '{') {
    offset = skipWhitespace(source, offset + 1);
    const keys = new Set<string>();
    let duplicate = false;
    if (source[offset] === '}') return { offset: offset + 1, duplicate };
    while (offset < source.length) {
      const key = scanString(source, offset);
      if (!key) return null;
      if (keys.has(key.value)) duplicate = true;
      keys.add(key.value);
      offset = skipWhitespace(source, key.offset);
      if (source[offset] !== ':') return null;
      const child = scanValue(source, offset + 1, depth + 1);
      if (!child) return null;
      duplicate ||= child.duplicate;
      offset = skipWhitespace(source, child.offset);
      if (source[offset] === '}') return { offset: offset + 1, duplicate };
      if (source[offset] !== ',') return null;
      offset = skipWhitespace(source, offset + 1);
    }
    return null;
  }
  if (source[offset] === '[') {
    offset = skipWhitespace(source, offset + 1);
    let duplicate = false;
    if (source[offset] === ']') return { offset: offset + 1, duplicate };
    while (offset < source.length) {
      const child = scanValue(source, offset, depth + 1);
      if (!child) return null;
      duplicate ||= child.duplicate;
      offset = skipWhitespace(source, child.offset);
      if (source[offset] === ']') return { offset: offset + 1, duplicate };
      if (source[offset] !== ',') return null;
      offset = skipWhitespace(source, offset + 1);
    }
    return null;
  }
  for (const literal of ['true', 'false', 'null']) {
    if (source.startsWith(literal, offset)) return { offset: offset + literal.length, duplicate: false };
  }
  const number = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(source.slice(offset));
  return number ? { offset: offset + number[0].length, duplicate: false } : null;
}

function parseUniqueJSON(source: string): unknown | undefined {
  const scanned = scanValue(source, 0, 0);
  if (!scanned || scanned.duplicate || skipWhitespace(source, scanned.offset) !== source.length) return undefined;
  try {
    return JSON.parse(source) as unknown;
  } catch {
    return undefined;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function validDecodedText(value: string, maximumBytes: number, allowEmpty = true): boolean {
  if (!allowEmpty && value === '' || value !== value.trim() || encoder.encode(value).byteLength > maximumBytes) return false;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 0x20 || code >= 0x7f && code <= 0x9f || code === 0x061c
      || code === 0x200e || code === 0x200f || code >= 0x202a && code <= 0x202e
      || code >= 0x2066 && code <= 0x2069) return false;
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function validateTopUpGroupRatios(source: string): boolean {
  const parsed = parseUniqueJSON(source);
  if (!isRecord(parsed)) return false;
  const ratios = Object.entries(parsed);
  return ratios.length > 0 && ratios.length <= MAX_TOP_UP_GROUPS
    && ratios.every(([group, ratio]) => validDecodedText(group, 128, false)
      && typeof ratio === 'number' && Number.isFinite(ratio)
      && ratio > 0 && ratio <= MAX_TOP_UP_GROUP_RATIO);
}

function validPaymentMethodKey(value: string): boolean {
  return encoder.encode(value).byteLength <= 64 && /^[A-Za-z0-9_-]+$/u.test(value);
}

function validPaymentMethodType(value: string): boolean {
  return encoder.encode(value).byteLength <= 64 && /^[A-Za-z0-9_.-]+$/u.test(value);
}

function validatePaymentMethods(source: string): boolean {
  const parsed = parseUniqueJSON(source);
  if (!Array.isArray(parsed) || parsed.length > MAX_PAYMENT_METHODS) return false;
  const types = new Set<string>();
  return parsed.every((candidate) => {
    if (!isRecord(candidate)) return false;
    const fields = Object.entries(candidate);
    if (fields.length === 0 || fields.length > MAX_PAYMENT_METHOD_FIELDS
      || !fields.every(([key, value]) => validPaymentMethodKey(key)
        && typeof value === 'string' && validDecodedText(value, 4_096))) return false;
    const { name, type } = candidate;
    if (typeof name !== 'string' || !validDecodedText(name, 128, false)
      || typeof type !== 'string' || !validPaymentMethodType(type) || types.has(type)) return false;
    types.add(type);
    return true;
  });
}

function validatePaymentSetting(source: string): boolean {
  const parsed = parseUniqueJSON(source);
  if (!isRecord(parsed)) return false;
  const options = parsed.amount_options;
  if (options !== undefined && options !== null) {
    if (!Array.isArray(options) || options.length > MAX_PAYMENT_PRESETS) return false;
    const amounts = new Set<number>();
    for (const amount of options) {
      if (typeof amount !== 'number' || !Number.isInteger(amount) || amount <= 0
        || amount > MAX_QUOTA || amounts.has(amount)) return false;
      amounts.add(amount);
    }
  }
  const discounts = parsed.amount_discount;
  if (discounts !== undefined && discounts !== null) {
    if (!isRecord(discounts) || Object.keys(discounts).length > MAX_PAYMENT_PRESETS) return false;
    for (const [amount, discount] of Object.entries(discounts)) {
      if (!/^[1-9]\d*$/u.test(amount) || BigInt(amount) > BigInt(MAX_QUOTA)
        || typeof discount !== 'number' || !Number.isFinite(discount) || discount <= 0 || discount > 1) return false;
    }
  }
  return true;
}

function isLoopbackHost(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/gu, '').replace(/\.$/u, '').toLowerCase();
  if (host === 'localhost' || host.endsWith('.localhost') || host === '::1') return true;
  const parts = host.split('.');
  return parts.length === 4 && parts.every((part) => /^(?:0|[1-9]\d{0,2})$/u.test(part) && Number(part) <= 255)
    && Number(parts[0]) === 127;
}

export function validateBillingURL(value: string, allowQuery: boolean): boolean {
  if (value === '') return true;
  if (value !== value.trim() || value.includes('\\')) return false;
  try {
    const parsed = new URL(value);
    return parsed.hostname !== '' && parsed.username === '' && parsed.password === '' && parsed.hash === ''
      && (allowQuery || parsed.search === '')
      && (parsed.protocol === 'https:' || parsed.protocol === 'http:' && isLoopbackHost(parsed.hostname));
  } catch {
    return false;
  }
}

export function validateBillingSettingFormat(format: string | undefined, value: string): string | null {
  if (value.trim() === '') return null;
  switch (format) {
    case 'topup-group-ratios':
      return validateTopUpGroupRatios(value) ? null : 'Enter valid bounded top-up group ratios.';
    case 'payment-methods':
      return validatePaymentMethods(value) ? null : 'Enter valid bounded payment methods.';
    case 'payment-setting':
      return validatePaymentSetting(value) ? null : 'Enter valid bounded payment settings.';
    case 'billing-url':
      return validateBillingURL(value, true) ? null : 'Enter a valid billing URL.';
    case 'billing-endpoint':
      return validateBillingURL(value, false) ? null : 'Enter a valid billing URL.';
    case 'currency-code':
      return /^[A-Za-z]{3}$/u.test(value) ? null : 'Three-letter ISO currency code.';
    case 'currency-symbol':
      return value === value.trim() && [...value].length <= 8 && encoder.encode(value).byteLength <= 32
        ? null : 'Enter a short currency symbol.';
    default:
      return null;
  }
}
