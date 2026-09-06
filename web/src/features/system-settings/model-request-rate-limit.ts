const MAX_GROUPS = 256;
const MAX_GROUP_BYTES = 64;
const MAX_JSON_BYTES = 64 * 1024;
const MAX_LIMIT = 2_147_483_647n;

const textEncoder = new TextEncoder();

function skipWhitespace(source: string, offset: number): number {
  while (offset < source.length) {
    const code = source.charCodeAt(offset);
    if (code !== 0x09 && code !== 0x0a && code !== 0x0d && code !== 0x20) break;
    offset += 1;
  }
  return offset;
}

function parseJSONString(source: string, offset: number): { value: string; offset: number } | null {
  if (source[offset] !== '"') return null;
  const start = offset;
  offset += 1;
  while (offset < source.length) {
    const code = source.charCodeAt(offset);
    if (code === 0x22) {
      const raw = source.slice(start, offset + 1);
      try {
        const value: unknown = JSON.parse(raw);
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

function parseInteger(source: string, offset: number): { value: bigint; offset: number } | null {
  const match = /^-?(?:0|[1-9][0-9]*)/u.exec(source.slice(offset));
  if (!match) return null;
  const end = offset + match[0].length;
  if (end < source.length && /[.eE0-9]/u.test(source[end])) return null;
  try {
    return { value: BigInt(match[0]), offset: end };
  } catch {
    return null;
  }
}

function validGroupName(group: string): boolean {
  if (group === '' || group.trim() !== group || textEncoder.encode(group).byteLength > MAX_GROUP_BYTES) return false;
  for (let index = 0; index < group.length; index += 1) {
    const code = group.charCodeAt(index);
    if (code <= 0x1f || code >= 0x7f && code <= 0x9f || code === 0x061c
      || code === 0x200e || code === 0x200f || code >= 0x202a && code <= 0x202e
      || code >= 0x2066 && code <= 0x2069) return false;
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = group.charCodeAt(index + 1);
      if (index + 1 >= group.length || next < 0xdc00 || next > 0xdfff) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

/**
 * Validates the exact persisted shape without first collapsing duplicate JSON
 * keys or exponent/decimal number spellings. The server parser applies the
 * same bounds before publishing a live rate-limit snapshot.
 */
export function validateModelRequestRateLimitGroups(source: string): string | null {
  if (source.trim() === '') return null;
  if (textEncoder.encode(source).byteLength > MAX_JSON_BYTES) {
    return 'Enter valid bounded model request group limits.';
  }
  let offset = skipWhitespace(source, 0);
  if (source[offset] !== '{') return 'Enter valid bounded model request group limits.';
  offset = skipWhitespace(source, offset + 1);
  const groups = new Set<string>();
  if (source[offset] === '}') {
    offset = skipWhitespace(source, offset + 1);
    return offset === source.length ? null : 'Enter valid bounded model request group limits.';
  }

  while (offset < source.length) {
    const parsedGroup = parseJSONString(source, offset);
    if (!parsedGroup || !validGroupName(parsedGroup.value) || groups.has(parsedGroup.value)) {
      return 'Enter valid bounded model request group limits.';
    }
    groups.add(parsedGroup.value);
    if (groups.size > MAX_GROUPS) return 'Enter valid bounded model request group limits.';

    offset = skipWhitespace(source, parsedGroup.offset);
    if (source[offset] !== ':') return 'Enter valid bounded model request group limits.';
    offset = skipWhitespace(source, offset + 1);
    if (source[offset] !== '[') return 'Enter valid bounded model request group limits.';
    offset = skipWhitespace(source, offset + 1);

    const total = parseInteger(source, offset);
    if (!total || total.value < 0n || total.value > MAX_LIMIT) {
      return 'Enter valid bounded model request group limits.';
    }
    offset = skipWhitespace(source, total.offset);
    if (source[offset] !== ',') return 'Enter valid bounded model request group limits.';
    offset = skipWhitespace(source, offset + 1);

    const success = parseInteger(source, offset);
    if (!success || success.value < 1n || success.value > MAX_LIMIT) {
      return 'Enter valid bounded model request group limits.';
    }
    offset = skipWhitespace(source, success.offset);
    if (source[offset] !== ']') return 'Enter valid bounded model request group limits.';
    offset = skipWhitespace(source, offset + 1);

    if (source[offset] === '}') {
      offset = skipWhitespace(source, offset + 1);
      return offset === source.length ? null : 'Enter valid bounded model request group limits.';
    }
    if (source[offset] !== ',') return 'Enter valid bounded model request group limits.';
    offset = skipWhitespace(source, offset + 1);
  }
  return 'Enter valid bounded model request group limits.';
}
