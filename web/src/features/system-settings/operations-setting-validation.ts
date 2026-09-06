const encoder = new TextEncoder();

const MAX_TOOL_PRICE_BYTES = 1024 * 1024;
const MAX_TOOL_PRICE_RULES = 20_000;
const MAX_TOOL_PRICE_RULE_BYTES = 512;
const MAX_TOOL_PRICE_NUMBER_BYTES = 64;
const MAX_TOOL_PRICE_EXPONENT = 24;
const MAX_TOOL_PRICE_SCALE = 12;
const MAX_TOOL_PRICE_DIGITS = 25;
const MAX_TOOL_PRICE_USD = 1_000_000_000_000n;
const MAX_STATUS_RULE_BYTES = 4 * 1024;
const MAX_STATUS_RULES = 128;
const MAX_DISABLE_KEYWORDS_BYTES = 16 * 1024;
const MAX_DISABLE_KEYWORD_BYTES = 256;
const MAX_DISABLE_KEYWORDS = 128;

function skipWhitespace(source: string, offset: number): number {
  while (offset < source.length && /[\t\n\r ]/u.test(source[offset])) offset += 1;
  return offset;
}

function parseJSONString(source: string, offset: number): { value: string; offset: number } | null {
  if (source[offset] !== '"') return null;
  const start = offset;
  offset += 1;
  while (offset < source.length) {
    const code = source.charCodeAt(offset);
    if (code === 0x22) {
      try {
        const value: unknown = JSON.parse(source.slice(start, offset + 1));
        return typeof value === 'string' ? { value, offset: offset + 1 } : null;
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

function containsUnsafeText(value: string, allowNewline: boolean): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (allowNewline && code === 0x0a) continue;
    if (code <= 0x1f || code >= 0x7f && code <= 0x9f || code === 0x061c
      || code === 0x200e || code === 0x200f || code >= 0x202a && code <= 0x202e
      || code >= 0x2066 && code <= 0x2069) return true;
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) return true;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function validToolPriceRule(rule: string): boolean {
  if (rule === '' || rule.trim() !== rule || encoder.encode(rule).byteLength > MAX_TOOL_PRICE_RULE_BYTES
    || containsUnsafeText(rule, false)) return false;
  const colons = [...rule].filter((character) => character === ':').length;
  if (colons === 0) return !rule.includes('*');
  if (colons !== 1) return false;
  const colon = rule.indexOf(':');
  const tool = rule.slice(0, colon);
  const model = rule.slice(colon + 1);
  if (tool === '' || tool.trim() !== tool || tool.includes('*') || !model.endsWith('*')) return false;
  const prefix = model.slice(0, -1);
  return prefix !== '' && prefix.trim() === prefix && !prefix.includes('*');
}

function boundedExponent(source: string): number | null {
  if (source === '') return 0;
  let negative = false;
  let offset = 0;
  if (source[0] === '+' || source[0] === '-') {
    negative = source[0] === '-';
    offset = 1;
  }
  if (offset === source.length) return null;
  let result = 0;
  for (; offset < source.length; offset += 1) {
    const code = source.charCodeAt(offset) - 0x30;
    if (code < 0 || code > 9 || result > Math.floor(MAX_TOOL_PRICE_EXPONENT / 10)
      || result * 10 + code > MAX_TOOL_PRICE_EXPONENT) return null;
    result = result * 10 + code;
  }
  return negative ? -result : result;
}

function validToolPriceNumber(literal: string): boolean {
  if (literal === '' || encoder.encode(literal).byteLength > MAX_TOOL_PRICE_NUMBER_BYTES
    || !/^(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/u.test(literal)) return false;
  const exponentOffset = literal.search(/[eE]/u);
  const mantissa = exponentOffset < 0 ? literal : literal.slice(0, exponentOffset);
  const exponent = boundedExponent(exponentOffset < 0 ? '' : literal.slice(exponentOffset + 1));
  if (exponent === null) return false;
  const point = mantissa.indexOf('.');
  const fractionDigits = point < 0 ? 0 : mantissa.length - point - 1;
  const coefficientText = mantissa.replace('.', '').replace(/^0+/u, '');
  if (coefficientText.length > MAX_TOOL_PRICE_DIGITS) return false;
  const effectiveExponent = exponent - fractionDigits;
  if (effectiveExponent < -MAX_TOOL_PRICE_SCALE || effectiveExponent > MAX_TOOL_PRICE_SCALE) return false;
  if (coefficientText === '') return true;
  const coefficient = BigInt(coefficientText);
  return effectiveExponent >= 0
    ? coefficient * (10n ** BigInt(effectiveExponent)) <= MAX_TOOL_PRICE_USD
    : coefficient <= MAX_TOOL_PRICE_USD * (10n ** BigInt(-effectiveExponent));
}

/** Validate the exact JSON token stream so duplicate keys and hostile exponents are rejected. */
export function validateToolPriceMap(source: string): string | null {
  if (source.trim() === '') return null;
  if (encoder.encode(source).byteLength > MAX_TOOL_PRICE_BYTES) return 'Enter valid bounded tool prices.';
  let offset = skipWhitespace(source, 0);
  if (source[offset] !== '{') return 'Enter valid bounded tool prices.';
  offset = skipWhitespace(source, offset + 1);
  const rules = new Set<string>();
  if (source[offset] === '}') {
    return skipWhitespace(source, offset + 1) === source.length ? null : 'Enter valid bounded tool prices.';
  }
  while (offset < source.length) {
    const rule = parseJSONString(source, offset);
    if (!rule || !validToolPriceRule(rule.value) || rules.has(rule.value)) return 'Enter valid bounded tool prices.';
    rules.add(rule.value);
    if (rules.size > MAX_TOOL_PRICE_RULES) return 'Enter valid bounded tool prices.';
    offset = skipWhitespace(source, rule.offset);
    if (source[offset] !== ':') return 'Enter valid bounded tool prices.';
    offset = skipWhitespace(source, offset + 1);
    const number = /^(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(source.slice(offset));
    if (!number || !validToolPriceNumber(number[0])) return 'Enter valid bounded tool prices.';
    offset = skipWhitespace(source, offset + number[0].length);
    if (source[offset] === '}') {
      return skipWhitespace(source, offset + 1) === source.length ? null : 'Enter valid bounded tool prices.';
    }
    if (source[offset] !== ',') return 'Enter valid bounded tool prices.';
    offset = skipWhitespace(source, offset + 1);
  }
  return 'Enter valid bounded tool prices.';
}

export function validateHTTPStatusRanges(source: string): string | null {
  if (encoder.encode(source).byteLength > MAX_STATUS_RULE_BYTES || containsUnsafeText(source, false)) {
    return 'Enter valid HTTP status codes or ranges.';
  }
  const normalized = source.replaceAll('，', ',').trim();
  if (normalized === '') return null;
  let count = 0;
  for (const rawSegment of normalized.split(',')) {
    const segment = rawSegment.trim().replaceAll(' ', '');
    if (segment === '') continue;
    count += 1;
    if (count > MAX_STATUS_RULES || !/^\d{3}(?:-\d{3})?$/u.test(segment)) {
      return 'Enter valid HTTP status codes or ranges.';
    }
    const [rawStart, rawEnd = rawStart] = segment.split('-');
    const start = Number(rawStart);
    const end = Number(rawEnd);
    if (start < 100 || end > 599 || start > end) return 'Enter valid HTTP status codes or ranges.';
  }
  return null;
}

export function validateChannelDisableKeywords(source: string): string | null {
  if (encoder.encode(source).byteLength > MAX_DISABLE_KEYWORDS_BYTES) {
    return 'Enter valid bounded channel-disable keywords.';
  }
  const normalized = source.replaceAll('\r\n', '\n').replaceAll('\r', '\n');
  if (containsUnsafeText(normalized, true)) return 'Enter valid bounded channel-disable keywords.';
  const unique = new Set<string>();
  for (const rawLine of normalized.split('\n')) {
    const line = rawLine.trim();
    if (line === '') continue;
    const folded = line.toLocaleLowerCase('en-US');
    if (encoder.encode(line).byteLength > MAX_DISABLE_KEYWORD_BYTES
      || encoder.encode(folded).byteLength > MAX_DISABLE_KEYWORD_BYTES) {
      return 'Enter valid bounded channel-disable keywords.';
    }
    unique.add(folded);
    if (unique.size > MAX_DISABLE_KEYWORDS) return 'Enter valid bounded channel-disable keywords.';
  }
  return null;
}
