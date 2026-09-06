import {
  type CustomOAuthBinding,
  type OAuthCatalog,
  type PasskeySummary,
  type ProfileLanguage,
  type ProfileNotificationSettings,
  type SidebarModules
} from './profile-api';

export type Notice = { kind: 'success' | 'error'; text: string } | null;

export type Resource<T> = { data: T | null; loading: boolean; error: boolean };

export type MutationResult<T> = { ok: true; value: T } | { ok: false };

export interface SecurityOverview {
  twoFactorEnabled: boolean;
  passkeyEnabled: boolean;
  passkeys: PasskeySummary[];
}

export interface OAuthOverview {
  catalog: OAuthCatalog;
  bindings: CustomOAuthBinding[];
}

export const LANGUAGE_OPTIONS: Array<{ code: ProfileLanguage; label: string }> = [
  { code: 'en', label: 'English' },
  { code: 'fr', label: 'Français' },
  { code: 'ja', label: '日本語' },
  { code: 'ru', label: 'Русский' },
  { code: 'vi', label: 'Tiếng Việt' },
  { code: 'zh', label: '简体中文' },
  { code: 'zh-TW', label: '繁體中文' },
];

export const NOTIFICATION_OPTIONS: Array<{ value: ProfileNotificationSettings['notifyType']; label: string }> = [
  { value: 'email', label: 'Email' },
  { value: 'webhook', label: 'Webhook' },
  { value: 'bark', label: 'Bark' },
  { value: 'gotify', label: 'Gotify' },
];

export const MAX_NOTIFICATION_QUOTA = 2_147_483_647;

export const MAX_NOTIFICATION_EMAIL_BYTES = 254;

export const MAX_NOTIFICATION_URL_BYTES = 2_048;

export const MAX_WEBHOOK_SECRET_BYTES = 4_096;

export const MAX_GOTIFY_TOKEN_BYTES = 2_048;

export const SIDEBAR_SECTIONS = [
  {
    key: 'chat',
    label: 'Chat area',
    modules: [
      { key: 'playground', label: 'Playground' },
      { key: 'chat', label: 'Chat' },
    ],
  },
  {
    key: 'console',
    label: 'Console area',
    modules: [
      { key: 'detail', label: 'Dashboard' },
      { key: 'token', label: 'API keys' },
      { key: 'log', label: 'Usage logs' },
      { key: 'midjourney', label: 'Drawing logs' },
      { key: 'task', label: 'Task history' },
    ],
  },
  {
    key: 'personal',
    label: 'Personal area',
    modules: [
      { key: 'topup', label: 'Wallet' },
      { key: 'personal', label: 'Profile and security' },
    ],
  },
] as const;

export function enabledSidebarModules(): SidebarModules {
  return {
    chat: { enabled: true, playground: true, chat: true },
    console: { enabled: true, detail: true, token: true, log: true, midjourney: true, task: true },
    personal: { enabled: true, topup: true, personal: true },
  };
}

export function sidebarPreferenceEnabled(modules: SidebarModules, section: keyof SidebarModules, key: string): boolean {
  return (modules[section] as unknown as Record<string, boolean>)[key] === true;
}

export function resource<T>(): Resource<T> {
  return { data: null, loading: true, error: false };
}

export function defaultExternalNavigate(target: string): void {
  window.location.assign(target);
}

export function compactIdentifier(value: string): string {
  const suffix = value.slice(-6);
  return suffix ? `••••${suffix}` : '••••';
}

export function hasControlCharacters(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return true;
  }
  return false;
}

export function validDisplayName(value: string): string | null {
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > 64 || hasControlCharacters(trimmed)) return null;
  return trimmed;
}

export function validEmailAddress(value: string): string | null {
  const normalized = value.trim().toLowerCase();
  if (!normalized || normalized.length > 50 || hasControlCharacters(normalized)
    || !/^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$/.test(normalized)) {
    return null;
  }
  return normalized;
}

export function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

export function hasUnsafeNotificationText(value: string): boolean {
  if (hasControlCharacters(value)) return true;
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    if (code === 0x061c || code === 0x200e || code === 0x200f
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      return true;
    }
  }
  return false;
}

export function validNotificationEmail(value: string): boolean {
  if (value === '') return true;
  return value.trim() === value
    && utf8Bytes(value) <= MAX_NOTIFICATION_EMAIL_BYTES
    && !hasUnsafeNotificationText(value)
    && /^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/.test(value);
}

export function notificationLoopbackHost(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/\.$/, '');
  if (host === 'localhost' || host.endsWith('.localhost') || host === '::1' || host === '[::1]') return true;
  const parts = host.split('.');
  return parts.length === 4 && parts[0] === '127'
    && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255);
}

export function validNotificationURL(value: string, kind: 'webhook' | 'bark' | 'gotify'): boolean {
  if (value === '') return true;
  if (value.trim() !== value || value.includes('\\') || utf8Bytes(value) > MAX_NOTIFICATION_URL_BYTES
    || hasUnsafeNotificationText(value)) return false;
  const candidate = kind === 'bark'
    ? value.replaceAll('{{title}}', 'title').replaceAll('{{content}}', 'content')
    : value;
  if (kind === 'bark' && (candidate.includes('{') || candidate.includes('}'))) return false;
  try {
    const parsed = new URL(candidate);
    if (!parsed.hostname || parsed.username || parsed.password || parsed.hash
      || (parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && notificationLoopbackHost(parsed.hostname)))) {
      return false;
    }
    return kind !== 'gotify' || (parsed.search === '' && !parsed.pathname.endsWith('/message'));
  } catch {
    return false;
  }
}

export function validWriteOnlyCredential(value: string, maximumBytes: number): boolean {
  return utf8Bytes(value) <= maximumBytes && !hasUnsafeNotificationText(value);
}

export function validProofCode(value: string): boolean {
  return /^\d{6}$/.test(value.trim());
}

export function validFactorCode(value: string): boolean {
  return /^[A-Za-z0-9-]{6,32}$/.test(value.trim());
}
