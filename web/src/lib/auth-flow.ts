import type { User } from '../api';

export interface LoginChallenge {
  twofa_required: true;
  flow_token: string;
}

export const MAX_LOGIN_FLOW_TOKEN_CHARACTERS = 256;
export const MAX_AUTH_CALLBACK_QUERY_CHARACTERS = 8 * 1024;
export const MAX_WECHAT_CODE_CHARACTERS = 2_048;
export const MAX_AUTH_RETURN_TARGET_CHARACTERS = 2_048;

const MAX_AUTH_USER_JSON_BYTES = 64 * 1024;
const MAX_USER_TEXT_CHARACTERS = 255;
const MAX_PASSWORD_RESET_EMAIL_BYTES = 254;
const MAX_PASSWORD_RESET_TOKEN_BYTES = 256;

type UnknownRecord = Record<string, unknown>;

function isRecord(value: unknown): value is UnknownRecord {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

export function hasUnsafeAuthText(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (
      code <= 0x1f
      || (code >= 0x7f && code <= 0x9f)
      || code === 0x061c
      || code === 0x200e
      || code === 0x200f
      || (code >= 0x202a && code <= 0x202e)
      || (code >= 0x2066 && code <= 0x2069)
    ) return true;
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return true;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

export function hasMalformedAuthQueryEncoding(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    if (value[index] !== '%') continue;
    if (!/^[0-9a-f]{2}$/i.test(value.slice(index + 1, index + 3))) return true;
    index += 2;
  }
  return false;
}

function boundedAuthJSON(value: unknown): boolean {
  try {
    const encoded = JSON.stringify(value);
    return typeof encoded === 'string'
      && new TextEncoder().encode(encoded).byteLength <= MAX_AUTH_USER_JSON_BYTES;
  } catch {
    return false;
  }
}

function validRequiredText(value: unknown, maximumCharacters = MAX_USER_TEXT_CHARACTERS): value is string {
  return typeof value === 'string'
    && value.length > 0
    && value.length <= maximumCharacters
    && !hasUnsafeAuthText(value);
}

function validText(value: unknown, maximumCharacters = MAX_USER_TEXT_CHARACTERS): value is string {
  return typeof value === 'string'
    && value.length <= maximumCharacters
    && !hasUnsafeAuthText(value);
}

function validOptionalText(value: unknown, maximumCharacters = MAX_USER_TEXT_CHARACTERS): value is string | undefined {
  return value === undefined
    || (typeof value === 'string'
      && value.length <= maximumCharacters
      && !hasUnsafeAuthText(value));
}

function validSafeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value);
}

export function isLoginChallenge(value: unknown): value is LoginChallenge {
  if (!isRecord(value) || !boundedAuthJSON(value)) return false;
  const challenge = value as Partial<LoginChallenge>;
  return challenge.twofa_required === true
    && validRequiredText(challenge.flow_token, MAX_LOGIN_FLOW_TOKEN_CHARACTERS);
}

export function isAuthenticatedUser(value: unknown): value is User {
  if (!isRecord(value) || !boundedAuthJSON(value)) return false;
  const user = value as Partial<User>;
  return validSafeInteger(user.id)
    && user.id > 0
    && validRequiredText(user.username, 64)
    && validText(user.display_name, 64)
    && validSafeInteger(user.role)
    && user.role >= 0
    && user.role <= 100
    && validText(user.group, 64)
    && validSafeInteger(user.quota)
    && validSafeInteger(user.used_quota)
    && validSafeInteger(user.request_count)
    && validOptionalText(user.email, 50);
}

/** Select the user from the cookie-auth bundle returned by WeChat login. */
export function authenticatedUserFromBundle(value: unknown): User | null {
  if (!isRecord(value) || !boundedAuthJSON(value) || !isAuthenticatedUser(value.user)) return null;
  return value.user;
}

export type WeChatOAuthEntry =
  | { kind: 'not-callback' }
  | { kind: 'invalid' }
  | { kind: 'wechat'; code: string };

/** Parse the legacy/reference `/oauth` WeChat completion route without accepting ambiguous input. */
export function parseWeChatOAuthEntry(pathname: string, search: string): WeChatOAuthEntry {
  if (pathname !== '/oauth') return { kind: 'not-callback' };
  if (
    search.length === 0
    || search.length > MAX_AUTH_CALLBACK_QUERY_CHARACTERS
    || !search.startsWith('?')
    || hasUnsafeAuthText(search)
    || hasMalformedAuthQueryEncoding(search)
  ) return { kind: 'invalid' };

  const parameters = new URLSearchParams(search.slice(1));
  const allowedKeys = new Set(['provider', 'code', 'redirect', 'state']);
  for (const key of parameters.keys()) {
    if (!allowedKeys.has(key) || parameters.getAll(key).length !== 1) return { kind: 'invalid' };
  }
  if (parameters.getAll('provider').length !== 1 || parameters.getAll('code').length !== 1) {
    return { kind: 'invalid' };
  }
  if (parameters.get('provider') !== 'wechat') return { kind: 'invalid' };
  const code = parameters.get('code') ?? '';
  if (
    code.length === 0
    || code.length > MAX_WECHAT_CODE_CHARACTERS
    || code !== code.trim()
    || hasUnsafeAuthText(code)
  ) return { kind: 'invalid' };
  return { kind: 'wechat', code };
}

export interface TelegramAuthorization {
  id: string;
  auth_date: string;
  hash: string;
  first_name?: string;
  last_name?: string;
  username?: string;
  photo_url?: string;
  lang?: string;
}

export interface OAuthFlowState {
  flow_token: string;
  expires_at: number;
}

/** Select the exact one-time state response used by browser OAuth ceremonies. */
export function parseOAuthFlowState(value: unknown): OAuthFlowState | null {
  if (!isRecord(value) || !boundedAuthJSON(value)) return null;
  if (Object.keys(value).some((key) => key !== 'flow_token' && key !== 'expires_at')) return null;
  const flowToken = value.flow_token;
  const expiresAt = value.expires_at;
  if (
    typeof flowToken !== 'string'
    || !/^[A-Za-z0-9]{64}$/.test(flowToken)
    || !Number.isSafeInteger(expiresAt)
    || (expiresAt as number) <= 0
  ) return null;
  return { flow_token: flowToken, expires_at: expiresAt as number };
}

function decimalTelegramField(value: unknown, maximumDigits: number): string | null {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value <= 0) return null;
    return String(value);
  }
  if (typeof value !== 'string') return null;
  return value === value.trim()
    && new RegExp(`^[0-9]{1,${maximumDigits}}$`).test(value)
    && value !== '0'
    ? value
    : null;
}

function optionalTelegramText(value: unknown, maximumCharacters: number): string | null | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== 'string') return null;
  if (
    value.length > maximumCharacters
    || new TextEncoder().encode(value).byteLength > maximumCharacters
    || hasUnsafeAuthText(value)
  ) return null;
  return value;
}

/** Keep only Telegram-signed login fields and apply limits stricter than the server query ceiling. */
export function parseTelegramAuthorization(value: unknown): TelegramAuthorization | null {
  if (!isRecord(value) || !boundedAuthJSON(value)) return null;
  const id = decimalTelegramField(value.id, 64);
  const authDate = decimalTelegramField(value.auth_date, 20);
  const hash = typeof value.hash === 'string' ? value.hash : '';
  if (!id || !authDate || hash !== hash.trim() || !/^[a-f0-9]{64}$/i.test(hash)) return null;

  const limits = {
    first_name: 64,
    last_name: 64,
    username: 64,
    photo_url: 2_048,
    lang: 16,
  } as const;
  const result: TelegramAuthorization = { id, auth_date: authDate, hash };
  for (const key of Object.keys(limits) as Array<keyof typeof limits>) {
    const parsed = optionalTelegramText(value[key], limits[key]);
    if (parsed === null) return null;
    if (parsed !== undefined) result[key] = parsed;
  }
  return result;
}

export function telegramLoginTarget(value: TelegramAuthorization, flowToken: string): string | null {
  if (!/^[A-Za-z0-9]{64}$/.test(flowToken)) return null;
  const parameters = new URLSearchParams();
  parameters.set('flow_token', flowToken);
  const keys: Array<keyof TelegramAuthorization> = [
    'id', 'first_name', 'last_name', 'username', 'photo_url', 'auth_date', 'hash', 'lang',
  ];
  for (const key of keys) {
    const item = value[key];
    if (item !== undefined) parameters.set(key, item);
  }
  return `/api/oauth/telegram/login?${parameters.toString()}`;
}

export function isValidAffiliateCode(value: string): boolean {
  return value === value.trim()
    && new TextEncoder().encode(value).byteLength <= 32
    && !hasUnsafeAuthText(value);
}

function isSafeAuthReturnTarget(value: string): boolean {
  if (
    value === ''
    || value !== value.trim()
    || value.length > MAX_AUTH_RETURN_TARGET_CHARACTERS
    || !value.startsWith('/')
    || value.startsWith('//')
    || value.includes('\\')
    || hasUnsafeAuthText(value)
    || hasMalformedAuthQueryEncoding(value)
  ) return false;
  const rawPathEnd = value.search(/[?#]/);
  const rawPath = rawPathEnd === -1 ? value : value.slice(0, rawPathEnd);
  for (const rawSegment of rawPath.split('/')) {
    let segment: string;
    try {
      segment = decodeURIComponent(rawSegment);
    } catch {
      return false;
    }
    if (segment === '.' || segment === '..' || segment.includes('/') || segment.includes('\\')) {
      return false;
    }
  }
  let parsed: URL;
  try {
    parsed = new URL(value, 'https://tokenrouter.invalid');
  } catch {
    return false;
  }
  if (parsed.origin !== 'https://tokenrouter.invalid') return false;
  const pathname = parsed.pathname.replace(/\/$/, '') || '/';
  if (
    pathname === '/login'
    || pathname === '/sign-in'
    || pathname === '/sign-up'
    || pathname === '/register'
    || pathname === '/forgot-password'
    || pathname === '/reset'
    || pathname === '/user/reset'
    || pathname === '/otp'
    || pathname === '/oauth'
    || pathname.startsWith('/oauth/')
  ) return false;
  return true;
}

export function oauthStartTarget(
  provider: string,
  affiliateCode = '',
  returnTarget = '',
): string | null {
  if (!/^[a-z0-9-]{1,64}$/.test(provider)) return null;
  if (!isValidAffiliateCode(affiliateCode)) return null;
  if (returnTarget !== '' && !isSafeAuthReturnTarget(returnTarget)) return null;
  const base = `/api/oauth/${encodeURIComponent(provider)}`;
  const parameters = new URLSearchParams();
  if (affiliateCode !== '') parameters.set('aff', affiliateCode);
  if (returnTarget !== '') parameters.set('redirect', returnTarget);
  const query = parameters.toString();
  return query === '' ? base : `${base}?${query}`;
}

export interface PasswordResetLocation {
  valid: boolean;
  email: string;
  credential: string;
  generatedMode: boolean;
}

/** Parse reset-link state once, rejecting unknown, duplicated, conflicting, or oversized input. */
export function parsePasswordResetLocation(search: string): PasswordResetLocation {
  const invalid: PasswordResetLocation = {
    valid: false, email: '', credential: '', generatedMode: false,
  };
  if (
    search.length > MAX_AUTH_CALLBACK_QUERY_CHARACTERS
    || (search !== '' && !search.startsWith('?'))
    || hasUnsafeAuthText(search)
    || hasMalformedAuthQueryEncoding(search)
  ) return invalid;

  const parameters = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  const allowedKeys = new Set(['email', 'token', 'code']);
  for (const key of parameters.keys()) {
    if (!allowedKeys.has(key) || parameters.getAll(key).length !== 1) return invalid;
  }

  const email = parameters.get('email')?.trim() ?? '';
  const token = parameters.get('token') ?? '';
  const code = parameters.get('code') ?? '';
  if (
    new TextEncoder().encode(email).byteLength > MAX_PASSWORD_RESET_EMAIL_BYTES
    || new TextEncoder().encode(token).byteLength > MAX_PASSWORD_RESET_TOKEN_BYTES
    || new TextEncoder().encode(code).byteLength > MAX_PASSWORD_RESET_TOKEN_BYTES
    || hasUnsafeAuthText(email)
    || hasUnsafeAuthText(token)
    || hasUnsafeAuthText(code)
    || (token !== '' && code !== '' && token !== code)
  ) return invalid;

  return {
    valid: true,
    email,
    credential: token || code,
    generatedMode: token !== '',
  };
}
