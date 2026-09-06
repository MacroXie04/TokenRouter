const MAX_JSON_BYTES = 1024 * 1024;
const MAX_ENTRIES = 20_000;
const MAX_MODEL_BYTES = 256;
const MAX_RATIO = 1_000_000_000_000;

const encoder = new TextEncoder();

function skipWhitespace(source: string, offset: number): number {
  while (offset < source.length && /[\t\n\r ]/u.test(source[offset])) offset += 1;
  return offset;
}

function parseString(source: string, offset: number): { value: string; offset: number } | null {
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

function validModelIdentifier(value: string): boolean {
  if (value === '' || value.trim() !== value || encoder.encode(value).byteLength > MAX_MODEL_BYTES) return false;
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

/**
 * Validates the persisted model-to-ratio object without first collapsing
 * duplicate JSON keys. This mirrors the server's finite, non-negative bounds.
 */
export function validateUsageRatioMap(source: string): string | null {
  if (source.trim() === '') return null;
  if (encoder.encode(source).byteLength > MAX_JSON_BYTES) return 'Enter a valid bounded non-negative ratio map.';

  let offset = skipWhitespace(source, 0);
  if (source[offset] !== '{') return 'Enter a valid bounded non-negative ratio map.';
  offset = skipWhitespace(source, offset + 1);
  const models = new Set<string>();
  if (source[offset] === '}') {
    offset = skipWhitespace(source, offset + 1);
    return offset === source.length ? null : 'Enter a valid bounded non-negative ratio map.';
  }

  while (offset < source.length) {
    const model = parseString(source, offset);
    if (!model || !validModelIdentifier(model.value) || models.has(model.value)) {
      return 'Enter a valid bounded non-negative ratio map.';
    }
    models.add(model.value);
    if (models.size > MAX_ENTRIES) return 'Enter a valid bounded non-negative ratio map.';

    offset = skipWhitespace(source, model.offset);
    if (source[offset] !== ':') return 'Enter a valid bounded non-negative ratio map.';
    offset = skipWhitespace(source, offset + 1);
    const number = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/u.exec(source.slice(offset));
    if (!number) return 'Enter a valid bounded non-negative ratio map.';
    const end = offset + number[0].length;
    if (end < source.length && /[.eE+\-0-9]/u.test(source[end])) {
      return 'Enter a valid bounded non-negative ratio map.';
    }
    const ratio = Number(number[0]);
    if (!Number.isFinite(ratio) || ratio < 0 || ratio > MAX_RATIO) {
      return 'Enter a valid bounded non-negative ratio map.';
    }
    offset = skipWhitespace(source, end);

    if (source[offset] === '}') {
      offset = skipWhitespace(source, offset + 1);
      return offset === source.length ? null : 'Enter a valid bounded non-negative ratio map.';
    }
    if (source[offset] !== ',') return 'Enter a valid bounded non-negative ratio map.';
    offset = skipWhitespace(source, offset + 1);
  }
  return 'Enter a valid bounded non-negative ratio map.';
}
