import { supportedLanguages } from '../../i18n';
import type { User } from '../../shared/api/contracts';

const MAX_USER_SETTINGS_BYTES = 64 * 1024;
const SUPPORTED_LANGUAGE_CODES = new Set(supportedLanguages.map(({ code }) => code));

function supportedUserLanguage(value: unknown): string | null {
  return typeof value === 'string' && SUPPORTED_LANGUAGE_CODES.has(value) ? value : null;
}

/** Ignore malformed or oversized legacy settings during session startup. */
export function savedUserLanguage(user: User | null | undefined): string | null {
  if (!user) return null;
  const direct = supportedUserLanguage(user.language);
  if (direct) return direct;
  let settings = user.setting;
  if (typeof settings === 'string') {
    if (new TextEncoder().encode(settings).byteLength > MAX_USER_SETTINGS_BYTES) return null;
    try {
      settings = JSON.parse(settings);
    } catch {
      return null;
    }
  }
  if (!settings || typeof settings !== 'object' || Array.isArray(settings)) return null;
  return supportedUserLanguage((settings as Record<string, unknown>).language);
}
