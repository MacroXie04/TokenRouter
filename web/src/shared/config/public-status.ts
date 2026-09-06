const MAX_STATUS_BYTES = 512 * 1024;
const MAX_NAVIGATION_BYTES = 64 * 1024;
const MAX_CUSTOM_PROVIDERS = 128;
const MAX_PROVIDER_NAME_BYTES = 64;
const MAX_PROVIDER_ICON_BYTES = 2 * 1024;
const MAX_QR_CODE_BYTES = 4 * 1024;
const MAX_ANNOUNCEMENTS = 100;

type UnknownRecord = Record<string, unknown>;

export interface CustomOAuthProviderInfo {
  id: number;
  name: string;
  slug: string;
  icon: string;
}

export type PublicAnnouncementType = 'default' | 'ongoing' | 'success' | 'warning' | 'error';

export interface PublicAnnouncementInfo {
  id?: string | number;
  type?: PublicAnnouncementType;
  content: string;
  extra?: string;
  publishDate: string;
}

export interface PublicStatusInfo {
  system_name: string;
  site_name: string;
  app_name: string;
  logo: string;
  HeaderNavModules: string;
  SidebarModulesAdmin: string;
  announcements_enabled: boolean;
  announcements: PublicAnnouncementInfo[];
  wechat_login: boolean;
  wechat_qrcode: string;
  self_use_mode_enabled: boolean;
  default_collapse_sidebar: boolean;
  register_enabled: boolean;
  password_login_enabled: boolean;
  password_register_enabled: boolean;
  email_verification: boolean;
  user_agreement_enabled: boolean;
  privacy_policy_enabled: boolean;
  passkey_login: boolean;
  passkey_display_name: string;
  passkey_rp_id: string;
  passkey_origins: string;
  passkey_allow_insecure: boolean;
  passkey_user_verification: 'required' | 'preferred' | 'discouraged';
  passkey_attachment: '' | 'platform' | 'cross-platform';
  github_oauth: boolean;
  github_client_id: string;
  discord_oauth: boolean;
  discord_client_id: string;
  oidc_enabled: boolean;
  oidc_client_id: string;
  oidc_authorization_endpoint: string;
  oidc_display_name: string;
  linuxdo_oauth: boolean;
  linuxdo_client_id: string;
  linuxdo_minimum_trust_level: number;
  telegram_oauth: boolean;
  telegram_bot_name: string;
  turnstile_check: boolean;
  turnstile_site_key: string;
  custom_oauth_providers: CustomOAuthProviderInfo[];
}

export class PublicStatusContractError extends Error {
  constructor() {
    super('Invalid public status response');
    this.name = 'PublicStatusContractError';
  }
}

function contractError(): never {
  throw new PublicStatusContractError();
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) contractError();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    contractError();
  }
  if (encoded === undefined || new TextEncoder().encode(encoded).byteLength > MAX_STATUS_BYTES) {
    contractError();
  }
}

function hasUnsafeCharacters(value: string, allowLineBreaks = false): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (allowLineBreaks && (code === 0x09 || code === 0x0a || code === 0x0d)) continue;
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      return true;
    }
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

function boundedText(
  value: unknown,
  maximumBytes: number,
  allowEmpty = true,
  allowLineBreaks = false,
): string {
  if (typeof value !== 'string' || hasUnsafeCharacters(value, allowLineBreaks)
    || new TextEncoder().encode(value).byteLength > maximumBytes
    || (!allowEmpty && value.trim() === '')) {
    contractError();
  }
  return value;
}

function optionalText(value: unknown, maximumBytes: number): string {
  return value === undefined || value === null ? '' : boundedText(value, maximumBytes);
}

function flag(data: UnknownRecord, key: string, fallback: boolean): boolean {
  const value = data[key];
  if (value === undefined || value === null) return fallback;
  if (typeof value !== 'boolean') contractError();
  return value;
}

function boundedInteger(value: unknown, minimum: number, maximum: number, fallback: number): number {
  if (value === undefined || value === null) return fallback;
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) contractError();
  return value as number;
}

function passkeyUserVerification(value: unknown): PublicStatusInfo['passkey_user_verification'] {
  if (value === undefined || value === null || value === '') return 'preferred';
  const parsed = boundedText(value, 16);
  if (parsed !== 'required' && parsed !== 'preferred' && parsed !== 'discouraged') contractError();
  return parsed;
}

function passkeyAttachment(value: unknown): PublicStatusInfo['passkey_attachment'] {
  if (value === undefined || value === null || value === '') return '';
  const parsed = boundedText(value, 16);
  if (parsed !== 'platform' && parsed !== 'cross-platform') contractError();
  return parsed;
}

function normalizedURLHostname(hostname: string): string {
  return hostname.replace(/^\[|\]$/gu, '').replace(/\.$/u, '').toLowerCase();
}

function parsedIPv4(hostname: string): number[] | null {
  const parts = normalizedURLHostname(hostname).split('.');
  if (parts.length !== 4 || parts.some((part) => !/^(?:0|[1-9]\d{0,2})$/u.test(part))) return null;
  const octets = parts.map(Number);
  return octets.every((octet) => octet <= 255) ? octets : null;
}

function loopbackHostname(hostname: string): boolean {
  const host = normalizedURLHostname(hostname);
  return host === 'localhost' || host.endsWith('.localhost') || host === '::1'
    || parsedIPv4(host)?.[0] === 127;
}

function unsafeLiteralHostname(hostname: string): boolean {
  const host = normalizedURLHostname(hostname);
  const ipv4 = parsedIPv4(host);
  if (ipv4) {
    const [first, second] = ipv4;
    return first === 0 || first === 10 || first === 127 || first >= 224
      || first === 100 && second >= 64 && second <= 127
      || first === 169 && second === 254
      || first === 172 && second >= 16 && second <= 31
      || first === 192 && second === 168
      || first === 198 && (second === 18 || second === 19);
  }
  if (!host.includes(':')) return false;
  return host === '::' || host === '::1' || /^f[cd]/u.test(host) || /^fe[89ab]/u.test(host);
}

function ambiguousRawURLPath(value: string): boolean {
  const schemeEnd = value.indexOf('://');
  if (schemeEnd < 0) return true;
  const pathStart = value.indexOf('/', schemeEnd + 3);
  if (pathStart < 0) return false;
  const endings = [value.indexOf('?', pathStart), value.indexOf('#', pathStart)].filter((index) => index >= 0);
  const pathEnd = endings.length > 0 ? Math.min(...endings) : value.length;
  return value.slice(pathStart, pathEnd).split('/').some((segment) => {
    try {
      const decoded = decodeURIComponent(segment);
      return decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\');
    } catch {
      return true;
    }
  });
}

function safeOAuthAuthorizationEndpoint(value: unknown): string {
  const source = optionalText(value, 2 * 1024);
  if (source === '') return '';
  if (source !== source.trim() || source.includes('\\') || source.endsWith('?') || ambiguousRawURLPath(source)) {
    return contractError();
  }
  try {
    const parsed = new URL(source);
    const queryKeys = [...parsed.searchParams.keys()];
    if (parsed.username !== '' || parsed.password !== '' || parsed.hash !== ''
      || queryKeys.length > 16 || new Set(queryKeys).size !== queryKeys.length
      || [...parsed.searchParams].some(([key, candidate]) => key === '' || hasUnsafeCharacters(key)
        || hasUnsafeCharacters(candidate) || new TextEncoder().encode(key).byteLength > 128
        || new TextEncoder().encode(candidate).byteLength > 2_048)) contractError();
    if (parsed.protocol === 'https:' && !unsafeLiteralHostname(parsed.hostname)) return parsed.toString();
    if (parsed.protocol === 'http:' && loopbackHostname(parsed.hostname)) return parsed.toString();
  } catch (error) {
    if (error instanceof PublicStatusContractError) throw error;
  }
  return contractError();
}

function validPasskeyRPID(value: string): boolean {
  if (value === '' || value.length > 253 || value !== value.trim() || value.endsWith('.')) return false;
  if (parsedIPv4(value)) return true;
  if (value.includes(':')) {
    try {
      const parsed = new URL(`http://[${value}]/`);
      return normalizedURLHostname(parsed.hostname) === value.toLowerCase();
    } catch {
      return false;
    }
  }
  return value.split('.').every((label) => label.length > 0 && label.length <= 63
    && /^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/u.test(label));
}

function passkeyHostMatchesRPID(hostname: string, rpID: string): boolean {
  const host = normalizedURLHostname(hostname);
  const relyingParty = normalizedURLHostname(rpID);
  const hostIsIP = parsedIPv4(host) !== null || host.includes(':');
  const relyingPartyIsIP = parsedIPv4(relyingParty) !== null || relyingParty.includes(':');
  if (hostIsIP || relyingPartyIsIP) return hostIsIP && relyingPartyIsIP && host === relyingParty;
  return host === relyingParty || host.endsWith(`.${relyingParty}`);
}

function validatePasskeyOrigins(value: string, rpID: string, allowInsecure: boolean): void {
  const origins = value.split(',');
  if (origins.length === 0 || origins.length > 32 || new Set(origins).size !== origins.length) contractError();
  for (const origin of origins) {
    if (origin === '' || origin !== origin.trim() || origin.includes('\\')) contractError();
    try {
      const parsed = new URL(origin);
      if (parsed.origin !== origin || parsed.username !== '' || parsed.password !== '' || parsed.hash !== ''
        || parsed.search !== '' || parsed.pathname !== '/'
        || parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && (allowInsecure || loopbackHostname(parsed.hostname)))
        || !passkeyHostMatchesRPID(parsed.hostname, rpID)) contractError();
    } catch (error) {
      if (error instanceof PublicStatusContractError) throw error;
      contractError();
    }
  }
}

function safeImageSource(value: unknown, maximumBytes: number): string {
  const source = optionalText(value, maximumBytes).trim();
  if (source === '') return '';
  if (source.startsWith('/') && !source.startsWith('//') && !source.includes('\\')) return source;
  try {
    const parsed = new URL(source);
    if (parsed.username !== '' || parsed.password !== '') return '';
    if (parsed.protocol === 'https:') return parsed.toString();
    if (parsed.protocol !== 'http:') return '';
    const hostname = parsed.hostname.replace(/\.$/, '').toLowerCase();
    if (hostname === 'localhost' || hostname.endsWith('.localhost')
      || hostname === '127.0.0.1' || hostname === '[::1]') {
      return parsed.toString();
    }
  } catch {
    // Invalid optional artwork is omitted without hiding an otherwise usable provider.
  }
  return '';
}

function customProviders(value: unknown): CustomOAuthProviderInfo[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || value.length > MAX_CUSTOM_PROVIDERS) contractError();
  const ids = new Set<number>();
  const slugs = new Set<string>();
  return value.map((entry) => {
    const provider = record(entry);
    if (!Number.isSafeInteger(provider.id) || (provider.id as number) <= 0) contractError();
    const id = provider.id as number;
    const name = boundedText(provider.name, MAX_PROVIDER_NAME_BYTES, false).trim();
    const slug = boundedText(provider.slug, 64, false).trim();
    if (!/^[a-z0-9-]{1,64}$/.test(slug) || ids.has(id) || slugs.has(slug)) contractError();
    ids.add(id);
    slugs.add(slug);
    return {
      id,
      name,
      slug,
      icon: safeImageSource(provider.icon, MAX_PROVIDER_ICON_BYTES),
    };
  });
}

const announcementTypes = new Set<PublicAnnouncementType>([
  'default', 'ongoing', 'success', 'warning', 'error',
]);
const rfc3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/u;

function announcements(value: unknown): PublicAnnouncementInfo[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || value.length > MAX_ANNOUNCEMENTS) contractError();
  const ids = new Set<string>();
  const parsed = value.map((entry) => {
    const item = record(entry);
    const content = boundedText(item.content, 4 * 1024, false, true);
    if (Array.from(content).length > 500) contractError();
    const publishDate = boundedText(item.publishDate, 64, false);
    if (!rfc3339.test(publishDate) || !Number.isFinite(Date.parse(publishDate))) contractError();
    const result: PublicAnnouncementInfo = { content, publishDate };
    if (item.extra !== undefined && item.extra !== null && item.extra !== '') {
      const extra = boundedText(item.extra, 2 * 1024, true, true);
      if (Array.from(extra).length > 100) contractError();
      result.extra = extra;
    }
    if (item.type !== undefined && item.type !== null && item.type !== '') {
      const type = boundedText(item.type, 16) as PublicAnnouncementType;
      if (!announcementTypes.has(type)) contractError();
      result.type = type;
    }
    if (item.id !== undefined && item.id !== null) {
      if (typeof item.id === 'string') {
        const id = boundedText(item.id, 128, false);
        if (ids.has(`s:${id}`)) contractError();
        ids.add(`s:${id}`);
        result.id = id;
      } else if (Number.isSafeInteger(item.id) && (item.id as number) >= 0) {
        const id = item.id as number;
        if (ids.has(`n:${id}`)) contractError();
        ids.add(`n:${id}`);
        result.id = id;
      } else {
        contractError();
      }
    }
    return result;
  });
  return parsed.sort((left, right) => Date.parse(right.publishDate) - Date.parse(left.publishDate));
}

/** Select and normalize only the bounded public status fields used by the browser. */
export function parsePublicStatus(value: unknown): PublicStatusInfo {
  boundedPayload(value);
  const data = record(value);
  const result: PublicStatusInfo = {
    system_name: optionalText(data.system_name, 128).trim(),
    site_name: optionalText(data.site_name, 128).trim(),
    app_name: optionalText(data.app_name, 128).trim(),
    logo: safeImageSource(data.logo, 4 * 1024),
    HeaderNavModules: optionalText(data.HeaderNavModules, MAX_NAVIGATION_BYTES),
    SidebarModulesAdmin: optionalText(data.SidebarModulesAdmin, MAX_NAVIGATION_BYTES),
    announcements_enabled: flag(data, 'announcements_enabled', false),
    announcements: announcements(data.announcements),
    wechat_login: flag(data, 'wechat_login', false),
    wechat_qrcode: safeImageSource(data.wechat_qrcode, MAX_QR_CODE_BYTES),
    self_use_mode_enabled: flag(data, 'self_use_mode_enabled', false),
    default_collapse_sidebar: flag(data, 'default_collapse_sidebar', false),
    register_enabled: flag(data, 'register_enabled', true),
    password_login_enabled: flag(data, 'password_login_enabled', true),
    password_register_enabled: flag(data, 'password_register_enabled', true),
    email_verification: flag(data, 'email_verification', false),
    user_agreement_enabled: flag(data, 'user_agreement_enabled', false),
    privacy_policy_enabled: flag(data, 'privacy_policy_enabled', false),
    passkey_login: flag(data, 'passkey_login', false),
    passkey_display_name: optionalText(data.passkey_display_name, 128).trim(),
    passkey_rp_id: optionalText(data.passkey_rp_id, 253).trim(),
    passkey_origins: optionalText(data.passkey_origins, 16 * 1024).trim(),
    passkey_allow_insecure: flag(data, 'passkey_allow_insecure', false),
    passkey_user_verification: passkeyUserVerification(data.passkey_user_verification),
    passkey_attachment: passkeyAttachment(data.passkey_attachment),
    github_oauth: flag(data, 'github_oauth', false),
    github_client_id: optionalText(data.github_client_id, 256).trim(),
    discord_oauth: flag(data, 'discord_oauth', false),
    discord_client_id: optionalText(data.discord_client_id, 256).trim(),
    oidc_enabled: flag(data, 'oidc_enabled', false),
    oidc_client_id: optionalText(data.oidc_client_id, 256).trim(),
    oidc_authorization_endpoint: safeOAuthAuthorizationEndpoint(data.oidc_authorization_endpoint),
    oidc_display_name: optionalText(data.oidc_display_name, 128).trim() || 'OIDC',
    linuxdo_oauth: flag(data, 'linuxdo_oauth', false),
    linuxdo_client_id: optionalText(data.linuxdo_client_id, 256).trim(),
    linuxdo_minimum_trust_level: boundedInteger(data.linuxdo_minimum_trust_level, 0, 4, 0),
    telegram_oauth: flag(data, 'telegram_oauth', false),
    telegram_bot_name: optionalText(data.telegram_bot_name, 64).trim(),
    turnstile_check: flag(data, 'turnstile_check', false),
    turnstile_site_key: optionalText(data.turnstile_site_key, 256).trim(),
    custom_oauth_providers: customProviders(data.custom_oauth_providers),
  };

  const conditionalMetadata: Array<[boolean, string[]]> = [
    [result.github_oauth, ['github_client_id']],
    [result.discord_oauth, ['discord_client_id']],
    [result.oidc_enabled, ['oidc_client_id', 'oidc_authorization_endpoint', 'oidc_display_name']],
    [result.linuxdo_oauth, ['linuxdo_client_id', 'linuxdo_minimum_trust_level']],
    [result.passkey_login, [
      'passkey_display_name', 'passkey_rp_id', 'passkey_origins', 'passkey_allow_insecure',
      'passkey_user_verification', 'passkey_attachment',
    ]],
  ];
  for (const [enabled, keys] of conditionalMetadata) {
    if (!enabled && keys.some((key) => data[key] !== undefined && data[key] !== null)) contractError();
  }
  if (result.github_oauth && result.github_client_id === '') contractError();
  if (result.discord_oauth && result.discord_client_id === '') contractError();
  if (result.linuxdo_oauth && result.linuxdo_client_id === '') contractError();
  if (result.oidc_enabled && (result.oidc_client_id === '' || result.oidc_authorization_endpoint === '')) contractError();
  if (result.passkey_login) {
    if (result.passkey_display_name === '' || !validPasskeyRPID(result.passkey_rp_id)
      || result.passkey_origins === '') contractError();
    validatePasskeyOrigins(result.passkey_origins, result.passkey_rp_id, result.passkey_allow_insecure);
  }
  return result;
}
