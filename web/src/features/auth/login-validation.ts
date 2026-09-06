import {
  hasMalformedAuthQueryEncoding,
  hasUnsafeAuthText,
  isValidAffiliateCode,
  MAX_AUTH_CALLBACK_QUERY_CHARACTERS
} from './auth-flow';

export const MAX_OAUTH_ERROR_MESSAGE_CHARACTERS = 512;

export const MAX_USERNAME_CHARACTERS = 64;

export const MAX_PASSWORD_CHARACTERS = 64;

export const MAX_EMAIL_CHARACTERS = 50;

export const MAX_LOGIN_IDENTIFIER_BYTES = 50;

export function safeOAuthDisplayText(
  value: string,
  maximumCharacters = MAX_OAUTH_ERROR_MESSAGE_CHARACTERS,
): string | null {
  const normalized = value.trim();
  return normalized.length > 0
    && normalized.length <= maximumCharacters
    && new TextEncoder().encode(normalized).byteLength <= maximumCharacters
    && !hasUnsafeAuthText(normalized)
    ? normalized
    : null;
}

export function safeOAuthCredentialText(value: string, maximumCharacters: number): string | null {
  return value.length > 0
    && value === value.trim()
    && value.length <= maximumCharacters
    && new TextEncoder().encode(value).byteLength <= maximumCharacters
    && !hasUnsafeAuthText(value)
    ? value
    : null;
}

export function oauthCallbackErrorMessage(search: string, fallback: string): string | null {
  if (
    search.length > MAX_AUTH_CALLBACK_QUERY_CHARACTERS
    || (search !== '' && !search.startsWith('?'))
    || hasUnsafeAuthText(search)
    || hasMalformedAuthQueryEncoding(search)
  ) return fallback;
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  if (Array.from(params).length > 32) return fallback;
  const errors = params.getAll('error');
  if (errors.length === 0) return null;
  if (errors.length !== 1 || errors[0].length > 64) return fallback;
  const messages = params.getAll('message');
  if (errors[0] !== 'oauth_access_denied' || messages.length !== 1) return fallback;
  return safeOAuthDisplayText(messages[0]) ?? fallback;
}

export function oauthErrorClearedLocation(pathname: string, search: string, hash: string): string {
  if (
    search.length > MAX_AUTH_CALLBACK_QUERY_CHARACTERS
    || hasUnsafeAuthText(search)
    || hasMalformedAuthQueryEncoding(search)
    || (search !== '' && !search.startsWith('?'))
  ) return pathname;
  const params = new URLSearchParams(search.slice(1));
  params.delete('error');
  params.delete('message');
  const remaining = params.toString();
  return `${pathname}${remaining ? `?${remaining}` : ''}${hash}`;
}

export function initialAffiliateCode(): string {
  if (
    window.location.search.length > MAX_AUTH_CALLBACK_QUERY_CHARACTERS
    || hasUnsafeAuthText(window.location.search)
    || hasMalformedAuthQueryEncoding(window.location.search)
  ) return '';
  const params = new URLSearchParams(window.location.search);
  if (Array.from(params).length > 32) return '';
  const values = params.getAll('aff');
  if (values.length !== 1) return '';
  const value = values[0];
  return value !== '' && isValidAffiliateCode(value) ? value : '';
}
