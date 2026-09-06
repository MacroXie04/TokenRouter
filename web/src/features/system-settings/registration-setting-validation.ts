const MAX_JSON_BYTES = 64 * 1024;
const MAX_USER_GROUPS = 256;
const MAX_RULES_PER_GROUP = 64;
const MAX_RULES = 4_096;
const MAX_IDENTIFIER_BYTES = 64;
const MAX_DESCRIPTION_BYTES = 256;
const ERROR = 'Enter valid bounded special usable-group directives.';

const encoder = new TextEncoder();

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

function validUnicodeScalarString(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
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

function validPolicyText(value: string, maximumBytes: number, allowEmpty: boolean): boolean {
  return (allowEmpty || value !== '')
    && value.trim() === value
    && encoder.encode(value).byteLength <= maximumBytes
    && validUnicodeScalarString(value)
    && !/[\p{Cc}\p{Cf}]/u.test(value);
}

function targetFromDirective(key: string): string | null {
  const target = key.startsWith('+:') || key.startsWith('-:') ? key.slice(2) : key;
  if (target.startsWith('+:') || target.startsWith('-:')
    || !validPolicyText(target, MAX_IDENTIFIER_BYTES, false)) return null;
  return target;
}

/**
 * Validates the exact backend group_special_usable_group token stream. This
 * intentionally parses before JSON.parse can collapse duplicate keys.
 */
export function validateSpecialUsableGroupDirectives(source: string): string | null {
  if (source.trim() === '') return null;
  if (encoder.encode(source).byteLength > MAX_JSON_BYTES) return ERROR;

  let offset = skipWhitespace(source, 0);
  if (source[offset] !== '{') return ERROR;
  offset = skipWhitespace(source, offset + 1);
  if (source[offset] === '}') {
    return skipWhitespace(source, offset + 1) === source.length ? null : ERROR;
  }

  const userGroups = new Set<string>();
  let totalRules = 0;
  while (offset < source.length) {
    const parsedGroup = parseJSONString(source, offset);
    if (!parsedGroup || !validPolicyText(parsedGroup.value, MAX_IDENTIFIER_BYTES, false)
      || userGroups.has(parsedGroup.value)) return ERROR;
    userGroups.add(parsedGroup.value);
    if (userGroups.size > MAX_USER_GROUPS) return ERROR;

    offset = skipWhitespace(source, parsedGroup.offset);
    if (source[offset] !== ':') return ERROR;
    offset = skipWhitespace(source, offset + 1);
    if (source[offset] !== '{') return ERROR;
    offset = skipWhitespace(source, offset + 1);

    const rawKeys = new Set<string>();
    const targets = new Set<string>();
    let groupRules = 0;
    if (source[offset] !== '}') {
      while (offset < source.length) {
        const parsedRule = parseJSONString(source, offset);
        if (!parsedRule || rawKeys.has(parsedRule.value)) return ERROR;
        rawKeys.add(parsedRule.value);
        const target = targetFromDirective(parsedRule.value);
        if (target === null || targets.has(target)) return ERROR;
        targets.add(target);

        offset = skipWhitespace(source, parsedRule.offset);
        if (source[offset] !== ':') return ERROR;
        const description = parseJSONString(source, skipWhitespace(source, offset + 1));
        if (!description || !validPolicyText(description.value, MAX_DESCRIPTION_BYTES, true)) return ERROR;
        offset = skipWhitespace(source, description.offset);

        groupRules += 1;
        totalRules += 1;
        if (groupRules > MAX_RULES_PER_GROUP || totalRules > MAX_RULES) return ERROR;
        if (source[offset] === '}') break;
        if (source[offset] !== ',') return ERROR;
        offset = skipWhitespace(source, offset + 1);
      }
    }
    if (source[offset] !== '}') return ERROR;
    offset = skipWhitespace(source, offset + 1);
    if (source[offset] === '}') {
      return skipWhitespace(source, offset + 1) === source.length ? null : ERROR;
    }
    if (source[offset] !== ',') return ERROR;
    offset = skipWhitespace(source, offset + 1);
  }
  return ERROR;
}
