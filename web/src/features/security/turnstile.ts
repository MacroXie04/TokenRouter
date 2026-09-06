const MAX_SITE_KEY_CHARACTERS = 256;
const MAX_TOKEN_CHARACTERS = 4_096;

export interface TurnstileConfig {
  required: boolean;
  siteKey: string;
}

export const TURNSTILE_DISABLED: TurnstileConfig = Object.freeze({
  required: false,
  siteKey: '',
});

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function hasControlCharacters(value: string): boolean {
  for (const character of value) {
    const code = character.charCodeAt(0);
    if (code <= 31 || code === 127) return true;
  }
  return false;
}

export function validSiteKey(value: unknown): string {
  if (typeof value !== 'string') return '';
  const key = value.trim();
  if (
    key.length === 0
    || key.length > MAX_SITE_KEY_CHARACTERS
    || hasControlCharacters(key)
    || !/^[A-Za-z0-9_-]+$/.test(key)
  ) return '';
  return key;
}

export function parseTurnstileConfig(value: unknown): TurnstileConfig {
  const data = record(value);
  if (!data || data.turnstile_check !== true) return TURNSTILE_DISABLED;
  return { required: true, siteKey: validSiteKey(data.turnstile_site_key) };
}

export function normalizeToken(value: unknown): string {
  if (typeof value !== 'string') throw new Error('Invalid human-verification token');
  const token = value.trim();
  if (
    token.length === 0
    || token.length > MAX_TOKEN_CHARACTERS
    || hasControlCharacters(token)
  ) throw new Error('Invalid human-verification token');
  return token;
}

export function turnstileParams(token: string): { turnstile: string } {
  return { turnstile: normalizeToken(token) };
}
