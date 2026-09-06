export const CONSOLE_API_INFO_LIMIT = 50;
export const CONSOLE_FAQ_LIMIT = 100;
export const CONSOLE_UPTIME_GROUP_LIMIT = 20;

const MAX_SAFE_ITEM_ID = Number.MAX_SAFE_INTEGER;
const encoder = new TextEncoder();

export const CONSOLE_API_COLORS = [
  'blue', 'green', 'cyan', 'purple', 'pink', 'red', 'orange', 'amber',
  'yellow', 'lime', 'light-green', 'teal', 'light-blue', 'indigo',
  'violet', 'grey', 'slate',
] as const;

export type ConsoleAPIColor = (typeof CONSOLE_API_COLORS)[number];

export interface ConsoleAPIInfo {
  id?: number;
  url: string;
  route: string;
  description: string;
  color: ConsoleAPIColor;
}

export interface ConsoleFAQ {
  id?: number;
  question: string;
  answer: string;
}

export interface ConsoleUptimeKumaGroup {
  id?: number;
  categoryName: string;
  url: string;
  slug: string;
  description?: string;
}

export class ConsoleContentContractError extends Error {
  constructor() {
    super('Invalid console content contract');
    this.name = 'ConsoleContentContractError';
  }
}

type UnknownRecord = Record<string, unknown>;

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new ConsoleContentContractError();
  }
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: readonly string[]): void {
  const known = new Set(allowed);
  if (Object.keys(value).some((key) => !known.has(key))) throw new ConsoleContentContractError();
}

function safeText(value: unknown, maximumCharacters: number, options: {
  allowEmpty?: boolean;
  multiline?: boolean;
} = {}): string {
  if (typeof value !== 'string' || value.length > maximumCharacters || value !== value.trim()) {
    throw new ConsoleContentContractError();
  }
  if (!options.allowEmpty && value === '') throw new ConsoleContentContractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (options.multiline && (code === 0x09 || code === 0x0a || code === 0x0d)) continue;
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)
      || code === 0x061c || code === 0x200e || code === 0x200f
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      throw new ConsoleContentContractError();
    }
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) throw new ConsoleContentContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new ConsoleContentContractError();
    }
  }
  return value;
}

function itemID(value: unknown): number | undefined {
  if (value === undefined) return undefined;
  if (!Number.isSafeInteger(value) || (value as number) < 0 || (value as number) > MAX_SAFE_ITEM_ID) {
    throw new ConsoleContentContractError();
  }
  return value as number;
}

function ensureUniqueIDs(values: readonly { id?: number }[]): void {
  const seen = new Set<number>();
  values.forEach(({ id }) => {
    if (id === undefined) return;
    if (seen.has(id)) throw new ConsoleContentContractError();
    seen.add(id);
  });
}

function httpURL(value: unknown, maximumCharacters: number, baseOnly = false): string {
  const raw = safeText(value, maximumCharacters);
  if (raw.includes('\\')) throw new ConsoleContentContractError();
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new ConsoleContentContractError();
  }
  if ((parsed.protocol !== 'https:' && parsed.protocol !== 'http:')
    || parsed.username !== '' || parsed.password !== '' || parsed.hostname === ''
    || (baseOnly && (parsed.protocol !== 'https:' || parsed.search !== '' || parsed.hash !== ''))) {
    throw new ConsoleContentContractError();
  }
  const port = parsed.port === '' ? undefined : Number(parsed.port);
  if (port !== undefined && (!Number.isInteger(port) || port < 1 || port > 65_535)) {
    throw new ConsoleContentContractError();
  }
  return raw;
}

function boundedArray(value: unknown, maximum: number): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) throw new ConsoleContentContractError();
  return value;
}

export function parseConsoleAPIInfo(value: unknown): ConsoleAPIInfo[] {
  const result = boundedArray(value, CONSOLE_API_INFO_LIMIT).map((candidate) => {
    const item = record(candidate);
    exactKeys(item, ['id', 'url', 'route', 'description', 'color']);
    const color = safeText(item.color, 32) as ConsoleAPIColor;
    if (!CONSOLE_API_COLORS.includes(color)) throw new ConsoleContentContractError();
    const id = itemID(item.id);
    return {
      ...(id === undefined ? {} : { id }),
      url: httpURL(item.url, 500),
      route: safeText(item.route, 100),
      description: safeText(item.description, 200),
      color,
    };
  });
  ensureUniqueIDs(result);
  return result;
}

export function parseConsoleFAQ(value: unknown): ConsoleFAQ[] {
  const result = boundedArray(value, CONSOLE_FAQ_LIMIT).map((candidate) => {
    const item = record(candidate);
    exactKeys(item, ['id', 'question', 'answer']);
    const id = itemID(item.id);
    return {
      ...(id === undefined ? {} : { id }),
      question: safeText(item.question, 200, { multiline: true }),
      answer: safeText(item.answer, 1_000, { multiline: true }),
    };
  });
  ensureUniqueIDs(result);
  return result;
}

export function parseConsoleUptimeKumaGroups(value: unknown): ConsoleUptimeKumaGroup[] {
  const names = new Set<string>();
  const result = boundedArray(value, CONSOLE_UPTIME_GROUP_LIMIT).map((candidate) => {
    const item = record(candidate);
    exactKeys(item, ['id', 'categoryName', 'url', 'slug', 'description']);
    const id = itemID(item.id);
    const categoryName = safeText(item.categoryName, 50);
    if (names.has(categoryName)) throw new ConsoleContentContractError();
    names.add(categoryName);
    const slug = safeText(item.slug, 100);
    if (!/^[A-Za-z0-9_-]+$/u.test(slug)) throw new ConsoleContentContractError();
    const description = item.description === undefined
      ? undefined : safeText(item.description, 200, { allowEmpty: true });
    return {
      ...(id === undefined ? {} : { id }),
      categoryName,
      url: httpURL(item.url, 500, true),
      slug,
      ...(description === undefined || description === '' ? {} : { description }),
    };
  });
  ensureUniqueIDs(result);
  return result;
}

export function parseConsoleOptionJSON<T>(
  raw: string,
  parser: (value: unknown) => T,
): T {
  if (encoder.encode(raw).byteLength > 1024 * 1024) throw new ConsoleContentContractError();
  let value: unknown;
  try {
    value = JSON.parse(raw || '[]') as unknown;
  } catch {
    throw new ConsoleContentContractError();
  }
  return parser(value);
}
