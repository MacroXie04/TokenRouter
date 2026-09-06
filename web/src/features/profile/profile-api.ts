import { api } from '../../shared/api/client';

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_SAFE_ID = 2_147_483_647;
const MAX_UNIX_SECONDS = 4_102_444_800; // 2100-01-01T00:00:00Z
const MAX_SESSIONS = 128;
const MAX_PASSKEYS = 16;
const MAX_OAUTH_PROVIDERS = 128;
const MAX_SECURITY_PROOF_CHARACTERS = 8_192;
const MAX_TURNSTILE_TOKEN_CHARACTERS = 4_096;
const MAX_PROFILE_SETTINGS_BYTES = 64 * 1024;
const MAX_SIDEBAR_MODULES_BYTES = 16 * 1024;
const MAX_NOTIFICATION_EMAIL_BYTES = 254;
const MAX_NOTIFICATION_URL_BYTES = 2_048;
const MAX_WEBHOOK_SECRET_BYTES = 4_096;
const MAX_GOTIFY_TOKEN_BYTES = 2_048;

export const DEFAULT_QUOTA_WARNING_THRESHOLD = 500_000;

export const PROFILE_LANGUAGES = ['en', 'fr', 'ja', 'ru', 'vi', 'zh', 'zh-TW'] as const;
export type ProfileLanguage = typeof PROFILE_LANGUAGES[number];

export interface SidebarModules {
  chat: { enabled: boolean; playground: boolean; chat: boolean };
  console: {
    enabled: boolean;
    detail: boolean;
    token: boolean;
    log: boolean;
    midjourney: boolean;
    task: boolean;
  };
  personal: { enabled: boolean; topup: boolean; personal: boolean };
}

export type NotificationType = 'email' | 'webhook' | 'bark' | 'gotify';

export interface ProfileNotificationSettings {
  notifyType: NotificationType;
  quotaWarningThreshold: number;
  notificationEmail: string;
  webhookURL: string;
  webhookSecretConfigured: boolean;
  barkURL: string;
  gotifyURL: string;
  gotifyTokenConfigured: boolean;
  gotifyPriority: number;
  acceptUnsetRatioModel: boolean;
  recordIPLog: boolean;
  upstreamModelUpdateNotifyEnabled: boolean;
}

export interface NotificationSettingsUpdate {
  notifyType: NotificationType;
  quotaWarningThreshold: number;
  notificationEmail: string;
  webhookURL: string;
  webhookSecret: string;
  barkURL: string;
  gotifyURL: string;
  gotifyToken: string;
  gotifyPriority: number;
  acceptUnsetRatioModel: boolean;
  recordIPLog: boolean;
  upstreamModelUpdateNotifyEnabled?: boolean;
}

const responseLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

type UnknownRecord = Record<string, unknown>;

export type SecurityProofScope =
  | 'passkey.register'
  | 'passkey.delete'
  | 'twofa.backup_codes.regenerate';

export interface ProfileAccount {
  id: number;
  username: string;
  displayName: string;
  email: string;
  emailVerified: boolean;
  telegramConnected: boolean;
  role: 1 | 10 | 100;
  status: 1 | 2 | 3 | 4;
  group: string;
  quota: number;
  usedQuota: number;
  requestCount: number;
  createdAt: number;
  language: ProfileLanguage | null;
  sidebarModules: SidebarModules;
  notificationSettings: ProfileNotificationSettings;
}

export interface TwoFactorStatus {
  enabled: boolean;
}

export interface TwoFactorSetup {
  secret: string;
  provisioningURL: string;
}

export interface PasskeySummary {
  id: number;
  attachment: '' | 'platform' | 'cross-platform';
  createdAt: string;
  lastUsedAt: string | null;
  backupEligible: boolean;
  backupState: boolean;
  cloneWarning: boolean;
}

export interface LoginSession {
  sid: string;
  current: boolean;
  loginMethod: string;
  ip: string;
  userAgent: string;
  createdAt: number;
  lastActiveAt: number;
  expiresAt: number;
}

export interface CustomOAuthProvider {
  id: number;
  name: string;
  slug: string;
  clientId: string;
  authorizationEndpoint: string;
  scopes: string;
}

export interface CustomOAuthBinding {
  providerId: number;
  providerName: string;
  providerSlug: string;
  providerUserId: string;
}

export interface OAuthCatalog {
  builtIn: Array<{ name: 'GitHub' | 'Discord' | 'OIDC' | 'LinuxDO'; enabled: boolean }>;
  weChatEnabled: boolean;
  weChatQRCode: string | null;
  telegramEnabled: boolean;
  telegramBotName: string | null;
  turnstileRequired: boolean;
  turnstileSiteKey: string | null;
  customProviders: CustomOAuthProvider[];
}

export interface OAuthBindingFlow {
  flowToken: string;
  expiresAt: number;
}

export interface TelegramBindingFlow {
  flowToken: string;
  callbackURL: string;
}

export interface PasskeyRegistrationBegin {
  flowToken: string;
  publicKey: unknown;
}

export class ProfileContractError extends Error {
  constructor() {
    super('Invalid profile API response');
    this.name = 'ProfileContractError';
  }
}

function contractError(): never {
  throw new ProfileContractError();
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) contractError();
  return value as UnknownRecord;
}

function payloadBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    contractError();
  }
}

function boundedEnvelope(value: unknown): UnknownRecord {
  if (payloadBytes(value) > MAX_RESPONSE_BYTES) contractError();
  return record(value);
}

function successfulData(value: unknown): unknown {
  const envelope = boundedEnvelope(value);
  if (envelope.success !== true || !Object.prototype.hasOwnProperty.call(envelope, 'data')) contractError();
  return envelope.data;
}

function successfulMutation(value: unknown): void {
  const envelope = boundedEnvelope(value);
  if (envelope.success !== true) contractError();
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) contractError();
  return value as number;
}

function boolean(value: unknown): boolean {
  if (typeof value !== 'boolean') contractError();
  return value;
}

function safeText(value: unknown, maximum: number, allowEmpty = true): string {
  if (typeof value !== 'string' || value.length > maximum || (!allowEmpty && value.length === 0)) contractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) contractError();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) contractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      contractError();
    }
  }
  return value;
}

function optionalText(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : safeText(value, maximum);
}

function isoTimestamp(value: unknown): string {
  const timestamp = safeText(value, 64, false);
  if (!/^\d{4}-\d{2}-\d{2}T/.test(timestamp) || Number.isNaN(Date.parse(timestamp))) contractError();
  return timestamp;
}

function optionalISOTimestamp(value: unknown): string | null {
  if (value === undefined || value === null) return null;
  return isoTimestamp(value);
}

function sessionCreatedAt(value: unknown): number {
  if (typeof value === 'number') return integer(value, 0, MAX_UNIX_SECONDS);
  // Older TokenRouter nodes serialized the GORM timestamp as ISO 8601. Accept
  // it during rolling upgrades, but normalize the application model to the
  // reference-compatible Unix-seconds contract.
  const milliseconds = Date.parse(isoTimestamp(value));
  const seconds = Math.floor(milliseconds / 1_000);
  return integer(seconds, 0, MAX_UNIX_SECONDS);
}

function safeWebURL(value: unknown, maximum = 4_096): string {
  const raw = safeText(value, maximum, false);
  if (raw.trim() !== raw || raw.includes('\\')) contractError();
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    contractError();
  }
  const localHTTP = url.protocol === 'http:'
    && (url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '[::1]');
  if ((url.protocol !== 'https:' && !localHTTP) || !url.hostname || url.username || url.password || url.hash) {
    contractError();
  }
  return url.toString();
}

function role(value: unknown): ProfileAccount['role'] {
  const parsed = integer(value, 1, 100);
  if (parsed !== 1 && parsed !== 10 && parsed !== 100) contractError();
  return parsed;
}

function accountStatus(value: unknown): ProfileAccount['status'] {
  const parsed = integer(value, 1, 4);
  if (parsed !== 1 && parsed !== 2 && parsed !== 3 && parsed !== 4) contractError();
  return parsed;
}

function profileLanguage(value: unknown): ProfileLanguage {
  const language = safeText(value, 5, false);
  if (!(PROFILE_LANGUAGES as readonly string[]).includes(language)) contractError();
  return language as ProfileLanguage;
}

function notificationType(value: unknown): NotificationType {
  const parsed = safeText(value, 64, false);
  if (parsed !== 'email' && parsed !== 'webhook' && parsed !== 'bark' && parsed !== 'gotify') contractError();
  return parsed;
}

function notificationText(value: unknown, maximumBytes: number, allowEmpty = true): string {
  const parsed = safeText(value, maximumBytes, allowEmpty);
  if (new TextEncoder().encode(parsed).byteLength > maximumBytes) contractError();
  for (const character of parsed) {
    const code = character.codePointAt(0) ?? 0;
    if (code === 0x061c || code === 0x200e || code === 0x200f
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      contractError();
    }
  }
  return parsed;
}

function optionalNotificationText(value: unknown, maximumBytes: number): string {
  return value === undefined ? '' : notificationText(value, maximumBytes);
}

function notificationEmail(value: unknown): string {
  const email = notificationText(value, MAX_NOTIFICATION_EMAIL_BYTES);
  if (email === '') return email;
  if (email.trim() !== email
    || !/^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/.test(email)) {
    contractError();
  }
  return email;
}

function loopbackNotificationHost(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/\.$/, '');
  if (host === 'localhost' || host.endsWith('.localhost') || host === '::1' || host === '[::1]') return true;
  const parts = host.split('.');
  return parts.length === 4 && parts[0] === '127'
    && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255);
}

function notificationURL(value: unknown, kind: 'webhook' | 'bark' | 'gotify'): string {
  const raw = notificationText(value, MAX_NOTIFICATION_URL_BYTES);
  if (raw === '') return raw;
  if (raw.trim() !== raw || raw.includes('\\')) contractError();
  const candidate = kind === 'bark'
    ? raw.replaceAll('{{title}}', 'title').replaceAll('{{content}}', 'content')
    : raw;
  if (kind === 'bark' && (candidate.includes('{') || candidate.includes('}'))) contractError();
  let parsed: URL;
  try {
    parsed = new URL(candidate);
  } catch {
    contractError();
  }
  if (!parsed.hostname || parsed.username || parsed.password || parsed.hash
    || (parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && loopbackNotificationHost(parsed.hostname)))) {
    contractError();
  }
  if (kind === 'gotify' && (parsed.search !== '' || parsed.pathname.endsWith('/message'))) contractError();
  return raw;
}

function optionalSettingBoolean(value: unknown): boolean {
  return value === undefined ? false : boolean(value);
}

function parseNotificationSettings(settings: UnknownRecord): ProfileNotificationSettings {
  if (Object.prototype.hasOwnProperty.call(settings, 'webhook_secret')
    || Object.prototype.hasOwnProperty.call(settings, 'gotify_token')) {
    contractError();
  }
  const notifyType = settings.notify_type === undefined ? 'email' : notificationType(settings.notify_type);
  const quotaWarningThreshold = settings.quota_warning_threshold === undefined
    ? DEFAULT_QUOTA_WARNING_THRESHOLD
    : integer(settings.quota_warning_threshold, 1, MAX_SAFE_ID);
  const notificationEmailValue = settings.notification_email === undefined
    ? ''
    : notificationEmail(settings.notification_email);
  const webhookURL = notificationURL(optionalNotificationText(settings.webhook_url, MAX_NOTIFICATION_URL_BYTES), 'webhook');
  const barkURL = notificationURL(optionalNotificationText(settings.bark_url, MAX_NOTIFICATION_URL_BYTES), 'bark');
  const gotifyURL = notificationURL(optionalNotificationText(settings.gotify_url, MAX_NOTIFICATION_URL_BYTES), 'gotify');
  const webhookSecretConfigured = boolean(settings.webhook_secret_configured);
  const gotifyTokenConfigured = boolean(settings.gotify_token_configured);
  const gotifyPriority = settings.gotify_priority === undefined ? 5 : integer(settings.gotify_priority, 0, 10);
  if ((notifyType === 'webhook' && webhookURL === '')
    || (notifyType === 'bark' && barkURL === '')
    || (notifyType === 'gotify' && (gotifyURL === '' || !gotifyTokenConfigured))
    || (webhookSecretConfigured && webhookURL === '')
    || (gotifyTokenConfigured && gotifyURL === '')) {
    contractError();
  }
  return {
    notifyType,
    quotaWarningThreshold,
    notificationEmail: notificationEmailValue,
    webhookURL,
    webhookSecretConfigured,
    barkURL,
    gotifyURL,
    gotifyTokenConfigured,
    gotifyPriority,
    acceptUnsetRatioModel: optionalSettingBoolean(settings.accept_unset_model_ratio_model),
    recordIPLog: optionalSettingBoolean(settings.record_ip_log),
    upstreamModelUpdateNotifyEnabled: optionalSettingBoolean(settings.upstream_model_update_notify_enabled),
  };
}

function defaultSidebarModules(): SidebarModules {
  return {
    chat: { enabled: true, playground: true, chat: true },
    console: { enabled: true, detail: true, token: true, log: true, midjourney: true, task: true },
    personal: { enabled: true, topup: true, personal: true },
  };
}

function parseSidebarModulesText(value: unknown): SidebarModules {
  const raw = safeText(value, MAX_SIDEBAR_MODULES_BYTES);
  if (raw === '') return defaultSidebarModules();
  if (new TextEncoder().encode(raw).byteLength > MAX_SIDEBAR_MODULES_BYTES) contractError();
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    contractError();
  }
  const modules = record(parsed);
  const allowed: Record<string, ReadonlySet<string>> = {
    chat: new Set(['enabled', 'playground', 'chat']),
    console: new Set(['enabled', 'detail', 'token', 'log', 'midjourney', 'task']),
    personal: new Set(['enabled', 'topup', 'personal']),
  };
  for (const [sectionName, sectionValue] of Object.entries(modules)) {
    const allowedKeys = allowed[sectionName];
    if (!allowedKeys) contractError();
    const section = record(sectionValue);
    for (const [key, enabled] of Object.entries(section)) {
      if (!allowedKeys.has(key) || typeof enabled !== 'boolean') contractError();
    }
  }
  const configured = defaultSidebarModules();
  for (const sectionName of ['chat', 'console', 'personal'] as const) {
    const section = modules[sectionName];
    if (section === undefined) continue;
    const values = record(section);
    for (const key of Object.keys(configured[sectionName])) {
      if (values[key] !== undefined) {
        (configured[sectionName] as unknown as Record<string, boolean>)[key] = boolean(values[key]);
      }
    }
  }
  return configured;
}

function parseProfilePreferences(settingValue: unknown, sidebarValue: unknown): {
  language: ProfileLanguage | null;
  sidebarModules: SidebarModules;
  notificationSettings: ProfileNotificationSettings;
} {
  const setting = safeText(settingValue, MAX_PROFILE_SETTINGS_BYTES);
  if (new TextEncoder().encode(setting).byteLength > MAX_PROFILE_SETTINGS_BYTES) contractError();
  let settings: UnknownRecord = {};
  if (setting !== '') {
    try {
      settings = record(JSON.parse(setting));
    } catch {
      contractError();
    }
  }
  const sidebar = safeText(sidebarValue, MAX_SIDEBAR_MODULES_BYTES);
  const embeddedSidebar = settings.sidebar_modules === undefined ? '' : settings.sidebar_modules;
  if (embeddedSidebar !== sidebar) contractError();
  return {
    language: settings.language === undefined ? null : profileLanguage(settings.language),
    sidebarModules: parseSidebarModulesText(sidebar),
    notificationSettings: parseNotificationSettings(settings),
  };
}

export function parseProfileResponse(value: unknown): ProfileAccount {
  const item = record(successfulData(value));
  for (const secretField of ['password', 'access_token', 'verified_email_key', 'auth_version']) {
    if (item[secretField] !== undefined && item[secretField] !== null && item[secretField] !== '') contractError();
  }
  const email = optionalText(item.email, 50);
  const emailVerified = boolean(item.email_verified);
  if (emailVerified && email === '') contractError();
  const telegramId = optionalText(item.telegram_id, 64);
  const preferences = parseProfilePreferences(item.setting, item.sidebar_modules);
  return {
    id: integer(item.id, 1, MAX_SAFE_ID),
    username: safeText(item.username, 64, false),
    displayName: safeText(item.display_name, 64),
    email,
    emailVerified,
    telegramConnected: telegramId !== '',
    role: role(item.role),
    status: accountStatus(item.status),
    group: safeText(item.group, 64, false),
    quota: integer(item.quota, 0, Number.MAX_SAFE_INTEGER),
    usedQuota: integer(item.used_quota, 0, Number.MAX_SAFE_INTEGER),
    requestCount: integer(item.request_count, 0, Number.MAX_SAFE_INTEGER),
    createdAt: integer(item.created_at, 0, MAX_UNIX_SECONDS),
    ...preferences,
  };
}

export function parseTwoFactorStatusResponse(value: unknown): TwoFactorStatus {
  const data = record(successfulData(value));
  return { enabled: boolean(data.enabled) };
}

export function parseTwoFactorSetupResponse(value: unknown): TwoFactorSetup {
  const data = record(successfulData(value));
  const secret = safeText(data.secret, 256, false);
  if (!/^[A-Z2-7]+=*$/.test(secret)) contractError();
  const provisioningURL = safeText(data.otpauth_url, 4_096, false);
  let parsed: URL;
  try {
    parsed = new URL(provisioningURL);
  } catch {
    contractError();
  }
  if (parsed.protocol !== 'otpauth:' || parsed.hostname !== 'totp' || !parsed.pathname
    || parsed.username || parsed.password || parsed.hash || parsed.searchParams.get('secret') !== secret) {
    contractError();
  }
  return { secret, provisioningURL };
}

function parseBackupCodesData(value: unknown): string[] {
  const data = record(value);
  if (!Array.isArray(data.backup_codes) || data.backup_codes.length !== 8) contractError();
  const codes = data.backup_codes.map((code) => safeText(code, 16, false));
  if (codes.some((code) => !/^\d{8}$/.test(code)) || new Set(codes).size !== codes.length) contractError();
  return codes;
}

export function parseBackupCodesResponse(value: unknown): string[] {
  return parseBackupCodesData(successfulData(value));
}

export function parsePasskeyStatusResponse(value: unknown): TwoFactorStatus {
  const data = record(successfulData(value));
  return { enabled: boolean(data.enabled) };
}

export function parsePasskeysResponse(value: unknown): PasskeySummary[] {
  const data = successfulData(value);
  if (!Array.isArray(data) || data.length > MAX_PASSKEYS) contractError();
  const identifiers = new Set<number>();
  return data.map((raw): PasskeySummary => {
    const item = record(raw);
    if (item.public_key !== undefined && item.public_key !== null && item.public_key !== '') contractError();
    const id = integer(item.id, 1, MAX_SAFE_ID);
    if (identifiers.has(id)) contractError();
    identifiers.add(id);
    safeText(item.credential_id, 1_024, false);
    const rawAttachment = optionalText(item.attachment, 32);
    if (rawAttachment !== '' && rawAttachment !== 'platform' && rawAttachment !== 'cross-platform') contractError();
    return {
      id,
      attachment: rawAttachment,
      createdAt: isoTimestamp(item.created_at),
      lastUsedAt: optionalISOTimestamp(item.last_used_at),
      backupEligible: boolean(item.backup_eligible),
      backupState: boolean(item.backup_state),
      cloneWarning: boolean(item.clone_warning),
    };
  });
}

export function parseSessionsResponse(value: unknown): LoginSession[] {
  const data = successfulData(value);
  if (!Array.isArray(data) || data.length > MAX_SESSIONS) contractError();
  const seen = new Set<string>();
  return data.map((raw): LoginSession => {
    const item = record(raw);
    for (const secretField of ['refresh_hash', 'previous_refresh_hash']) {
      if (item[secretField] !== undefined && item[secretField] !== null && item[secretField] !== '') contractError();
    }
    const sid = safeText(item.sid, 64, false);
    if (!/^[A-Za-z0-9_-]+$/.test(sid) || seen.has(sid)) contractError();
    seen.add(sid);
    if (item.status !== undefined && item.status !== 'active') contractError();
    return {
      sid,
      // Tolerate the former session-row response during rolling upgrades. A
      // new backend always identifies the current browser explicitly.
      current: item.current === undefined ? false : boolean(item.current),
      loginMethod: safeText(item.login_method, 32, false),
      ip: optionalText(item.ip, 64),
      userAgent: optionalText(item.user_agent, 2_048),
      createdAt: sessionCreatedAt(item.created_at),
      lastActiveAt: integer(item.last_active_at, 0, MAX_UNIX_SECONDS),
      expiresAt: integer(item.expires_at, 0, MAX_UNIX_SECONDS),
    };
  });
}

function parseOAuthProvider(value: unknown): CustomOAuthProvider {
  const item = record(value);
  const slug = safeText(item.slug, 64, false);
  if (!/^[a-z0-9-]+$/.test(slug)) contractError();
  return {
    id: integer(item.id, 1, MAX_SAFE_ID),
    name: safeText(item.name, 128, false),
    slug,
    clientId: safeText(item.client_id, 512, false),
    authorizationEndpoint: safeWebURL(item.authorization_endpoint),
    scopes: optionalText(item.scopes, 1_024),
  };
}

export function parseOAuthCatalogResponse(value: unknown): OAuthCatalog {
  const data = record(successfulData(value));
  const providersRaw = data.custom_oauth_providers ?? [];
  if (!Array.isArray(providersRaw) || providersRaw.length > MAX_OAUTH_PROVIDERS) contractError();
  const ids = new Set<number>();
  const slugs = new Set<string>();
  const customProviders = providersRaw.map((raw) => {
    const provider = parseOAuthProvider(raw);
    if (ids.has(provider.id) || slugs.has(provider.slug)) contractError();
    ids.add(provider.id);
    slugs.add(provider.slug);
    return provider;
  });
  const rawQRCode = optionalText(data.wechat_qrcode, 4_096);
  const telegramEnabled = boolean(data.telegram_oauth);
  const rawTelegramBotName = optionalText(data.telegram_bot_name, 32);
  if ((rawTelegramBotName !== '') !== telegramEnabled
    || (rawTelegramBotName !== ''
      && (!/^[A-Za-z0-9_]{5,32}$/.test(rawTelegramBotName) || !/bot$/i.test(rawTelegramBotName)))) {
    contractError();
  }
  const turnstileRequired = boolean(data.turnstile_check);
  const rawTurnstileSiteKey = optionalText(data.turnstile_site_key, 256);
  if (rawTurnstileSiteKey.trim() !== rawTurnstileSiteKey
    || (rawTurnstileSiteKey !== '' && !/^[A-Za-z0-9_-]+$/.test(rawTurnstileSiteKey))) {
    contractError();
  }
  return {
    builtIn: [
      { name: 'GitHub', enabled: boolean(data.github_oauth) },
      { name: 'Discord', enabled: boolean(data.discord_oauth) },
      { name: 'OIDC', enabled: boolean(data.oidc_enabled) },
      { name: 'LinuxDO', enabled: boolean(data.linuxdo_oauth) },
    ],
    weChatEnabled: boolean(data.wechat_login),
    weChatQRCode: rawQRCode === '' ? null : safeWebURL(rawQRCode),
    telegramEnabled,
    telegramBotName: rawTelegramBotName || null,
    turnstileRequired,
    turnstileSiteKey: rawTurnstileSiteKey || null,
    customProviders,
  };
}

export function parseAccessTokenResponse(value: unknown): string {
  const token = safeText(successfulData(value), 64, false);
  if ((token.length !== 28 && token.length !== 32)
    || !/^[A-Za-z0-9+/]+={0,2}$/.test(token)
    || token.length % 4 !== 0
    || (token.indexOf('=') >= 0 && token.indexOf('=') < token.length - 2)) {
    contractError();
  }
  return token;
}

export function parseTelegramBindingFlowResponse(value: unknown, rawOrigin: string): TelegramBindingFlow {
  const data = record(successfulData(value));
  const flowToken = safeText(data.flow_token, 256, false);
  if (!/^[A-Za-z0-9_-]+$/.test(flowToken)) contractError();
  const origin = browserOrigin(rawOrigin);
  return {
    flowToken,
    callbackURL: new URL(`/api/oauth/telegram/bind/${encodeURIComponent(flowToken)}`, origin).toString(),
  };
}

export function parseOAuthBindingsResponse(value: unknown): CustomOAuthBinding[] {
  const data = successfulData(value);
  if (!Array.isArray(data) || data.length > MAX_OAUTH_PROVIDERS) contractError();
  const ids = new Set<number>();
  return data.map((raw): CustomOAuthBinding => {
    const item = record(raw);
    const providerId = integer(item.provider_id, 1, MAX_SAFE_ID);
    if (ids.has(providerId)) contractError();
    ids.add(providerId);
    const providerSlug = safeText(item.provider_slug, 64, false);
    if (!/^[a-z0-9-]+$/.test(providerSlug)) contractError();
    return {
      providerId,
      providerName: safeText(item.provider_name, 128, false),
      providerSlug,
      providerUserId: safeText(item.provider_user_id, 512, false),
    };
  });
}

export function parseOAuthBindingFlowResponse(value: unknown): OAuthBindingFlow {
  const data = record(successfulData(value));
  const flowToken = safeText(data.flow_token, 256, false);
  if (!/^[A-Za-z0-9_-]+$/.test(flowToken)) contractError();
  return {
    flowToken,
    expiresAt: integer(data.expires_at, 1, MAX_UNIX_SECONDS),
  };
}

export function parsePasskeyRegistrationBeginResponse(value: unknown): PasskeyRegistrationBegin {
  const data = record(successfulData(value));
  const flowToken = safeText(data.flow_token, 256, false);
  if (!/^[A-Za-z0-9_-]+$/.test(flowToken)) contractError();
  const options = record(data.options);
  return { flowToken, publicKey: record(options.publicKey) };
}

function parseSecurityProof(value: unknown, expectedScope: SecurityProofScope): string {
  const data = record(successfulData(value));
  const proof = safeText(data.proof_token, MAX_SECURITY_PROOF_CHARACTERS, false);
  if (!/^[A-Za-z0-9._-]+$/.test(proof) || data.method !== '2fa' || data.scope !== expectedScope) contractError();
  integer(data.expires_at, 1, MAX_UNIX_SECONDS);
  return proof;
}

function cleanDisplayName(value: string): string {
  const cleaned = safeText(value, 64, false).trim();
  if (!cleaned) contractError();
  return cleaned;
}

function cleanPassword(value: string, minimum: number, maximum: number): string {
  if (value.length < minimum || value.length > maximum || value.includes('\0')) contractError();
  return value;
}

function cleanVerificationCode(value: string, proofOnly = false): string {
  const code = safeText(value, 32, false).trim();
  if (proofOnly ? !/^\d{6}$/.test(code) : !/^[A-Za-z0-9-]{6,32}$/.test(code)) contractError();
  return code;
}

function cleanEmail(value: string): string {
  const email = safeText(value, 50, false).trim().toLowerCase();
  if (!/^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$/.test(email)) {
    contractError();
  }
  return email;
}

function cleanTurnstileToken(value: string): string {
  const token = safeText(value, MAX_TURNSTILE_TOKEN_CHARACTERS, false).trim();
  if (token === '' || new TextEncoder().encode(token).byteLength > MAX_TURNSTILE_TOKEN_CHARACTERS) contractError();
  return token;
}

function browserOrigin(value: string): URL {
  let origin: URL;
  try {
    origin = new URL(value);
  } catch {
    contractError();
  }
  if (origin.origin !== value || (origin.protocol !== 'https:' && origin.protocol !== 'http:')
    || origin.username || origin.password) contractError();
  return origin;
}

function cleanProofToken(value: string): string {
  const token = safeText(value, MAX_SECURITY_PROOF_CHARACTERS, false);
  if (!/^[A-Za-z0-9._-]+$/.test(token)) contractError();
  return token;
}

function signalConfig(signal?: AbortSignal) {
  return { signal, ...responseLimits };
}

export async function getProfile(signal?: AbortSignal): Promise<ProfileAccount> {
  const response = await api.get<unknown>('/user/self', signalConfig(signal));
  return parseProfileResponse(response.data);
}

export async function updateDisplayName(displayName: string, signal?: AbortSignal): Promise<void> {
  const response = await api.put<unknown>('/user/self', { display_name: cleanDisplayName(displayName) }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function updateLanguage(language: ProfileLanguage, signal?: AbortSignal): Promise<void> {
  const response = await api.put<unknown>('/user/self', { language: profileLanguage(language) }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function updateSidebarModules(modules: SidebarModules, signal?: AbortSignal): Promise<void> {
  let serialized: string;
  try {
    serialized = JSON.stringify(modules);
  } catch {
    contractError();
  }
  const normalized = JSON.stringify(parseSidebarModulesText(serialized));
  const response = await api.put<unknown>('/user/self', { sidebar_modules: normalized }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function updateNotificationSettings(
  settings: NotificationSettingsUpdate,
  accountRole: ProfileAccount['role'],
  signal?: AbortSignal,
): Promise<void> {
  const input = record(settings);
  const notifyType = notificationType(input.notifyType);
  const quotaWarningThreshold = integer(input.quotaWarningThreshold, 1, MAX_SAFE_ID);
  const notificationEmailValue = notificationEmail(input.notificationEmail);
  const webhookURL = notificationURL(input.webhookURL, 'webhook');
  const webhookSecret = notificationText(input.webhookSecret, MAX_WEBHOOK_SECRET_BYTES);
  const barkURL = notificationURL(input.barkURL, 'bark');
  const gotifyURL = notificationURL(input.gotifyURL, 'gotify');
  const gotifyToken = notificationText(input.gotifyToken, MAX_GOTIFY_TOKEN_BYTES);
  const gotifyPriority = integer(input.gotifyPriority, 0, 10);
  const acceptUnsetRatioModel = boolean(input.acceptUnsetRatioModel);
  const recordIPLog = boolean(input.recordIPLog);
  if ((notifyType === 'webhook' && webhookURL === '')
    || (notifyType === 'bark' && barkURL === '')
    || (notifyType === 'gotify' && gotifyURL === '')
    || (webhookSecret !== '' && webhookURL === '')
    || (gotifyToken !== '' && gotifyURL === '')) {
    contractError();
  }
  const parsedRole = role(accountRole);
  let upstreamModelUpdateNotifyEnabled: boolean | undefined;
  if (parsedRole >= 10) {
    upstreamModelUpdateNotifyEnabled = boolean(input.upstreamModelUpdateNotifyEnabled);
  } else if (input.upstreamModelUpdateNotifyEnabled !== undefined) {
    contractError();
  }
  const response = await api.put<unknown>('/user/setting', {
    quota_warning_threshold: quotaWarningThreshold,
    notify_type: notifyType,
    notification_email: notificationEmailValue,
    webhook_url: webhookURL,
    webhook_secret: webhookSecret,
    bark_url: barkURL,
    gotify_url: gotifyURL,
    gotify_token: gotifyToken,
    gotify_priority: gotifyPriority,
    accept_unset_model_ratio_model: acceptUnsetRatioModel,
    record_ip_log: recordIPLog,
    ...(upstreamModelUpdateNotifyEnabled === undefined
      ? {}
      : { upstream_model_update_notify_enabled: upstreamModelUpdateNotifyEnabled }),
  }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function changePassword(oldPassword: string, password: string, signal?: AbortSignal): Promise<void> {
  const response = await api.put<unknown>('/user/self', {
    old_password: cleanPassword(oldPassword, 1, 128),
    password: cleanPassword(password, 8, 64),
  }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function generateAccessToken(signal?: AbortSignal): Promise<string> {
  const response = await api.get<unknown>('/user/token', signalConfig(signal));
  return parseAccessTokenResponse(response.data);
}

export async function sendEmailVerification(
  email: string,
  turnstileToken?: string,
  signal?: AbortSignal,
): Promise<void> {
  const parameters = new URLSearchParams({ email: cleanEmail(email) });
  if (turnstileToken !== undefined) parameters.set('turnstile', cleanTurnstileToken(turnstileToken));
  const response = await api.get<unknown>(`/verification?${parameters.toString()}`, signalConfig(signal));
  successfulMutation(response.data);
}

export async function bindEmail(email: string, code: string, signal?: AbortSignal): Promise<void> {
  const response = await api.post<unknown>('/user/email/bind', {
    email: cleanEmail(email),
    code: cleanVerificationCode(code, true),
  }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function deleteAccount(signal?: AbortSignal): Promise<void> {
  const response = await api.delete<unknown>('/user/self', signalConfig(signal));
  successfulMutation(response.data);
}

export async function getTwoFactorStatus(signal?: AbortSignal): Promise<TwoFactorStatus> {
  const response = await api.get<unknown>('/user/2fa/status', signalConfig(signal));
  return parseTwoFactorStatusResponse(response.data);
}

export async function startTwoFactorSetup(signal?: AbortSignal): Promise<TwoFactorSetup> {
  const response = await api.post<unknown>('/user/2fa/start', {}, signalConfig(signal));
  return parseTwoFactorSetupResponse(response.data);
}

export async function enableTwoFactor(code: string, signal?: AbortSignal): Promise<string[]> {
  const response = await api.post<unknown>('/user/2fa/enable', { code: cleanVerificationCode(code, true) }, signalConfig(signal));
  return parseBackupCodesResponse(response.data);
}

export async function disableTwoFactor(code: string, signal?: AbortSignal): Promise<void> {
  const response = await api.post<unknown>('/user/2fa/disable', { code: cleanVerificationCode(code) }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function verifyTwoFactor(
  scope: SecurityProofScope,
  code: string,
  signal?: AbortSignal,
): Promise<string> {
  if (!['passkey.register', 'passkey.delete', 'twofa.backup_codes.regenerate'].includes(scope)) contractError();
  const response = await api.post<unknown>('/verify', {
    method: '2fa',
    code: cleanVerificationCode(code, true),
    scope,
  }, signalConfig(signal));
  return parseSecurityProof(response.data, scope);
}

export async function regenerateBackupCodes(proofToken: string, signal?: AbortSignal): Promise<string[]> {
  const response = await api.post<unknown>('/user/2fa/backup_codes', {}, {
    ...signalConfig(signal),
    headers: { 'X-Security-Proof': cleanProofToken(proofToken) },
  });
  return parseBackupCodesResponse(response.data);
}

export async function getPasskeyStatus(signal?: AbortSignal): Promise<TwoFactorStatus> {
  const response = await api.get<unknown>('/user/passkey/status', signalConfig(signal));
  return parsePasskeyStatusResponse(response.data);
}

export async function getPasskeys(signal?: AbortSignal): Promise<PasskeySummary[]> {
  const response = await api.get<unknown>('/user/passkey', signalConfig(signal));
  return parsePasskeysResponse(response.data);
}

export async function beginPasskeyRegistration(
  proofToken?: string,
  signal?: AbortSignal,
): Promise<PasskeyRegistrationBegin> {
  const response = await api.post<unknown>('/user/passkey/register/begin', {}, {
    ...signalConfig(signal),
    ...(proofToken ? { headers: { 'X-Security-Proof': cleanProofToken(proofToken) } } : {}),
  });
  return parsePasskeyRegistrationBeginResponse(response.data);
}

export async function finishPasskeyRegistration(
  flowToken: string,
  credential: Record<string, unknown>,
  signal?: AbortSignal,
): Promise<void> {
  const token = safeText(flowToken, 256, false);
  if (!/^[A-Za-z0-9_-]+$/.test(token) || payloadBytes(credential) > 128 * 1024) contractError();
  const response = await api.post<unknown>('/user/passkey/register/finish', {
    flow_token: token,
    ...record(credential),
  }, signalConfig(signal));
  successfulMutation(response.data);
}

export async function deletePasskeys(proofToken?: string, signal?: AbortSignal): Promise<void> {
  const response = await api.delete<unknown>('/user/passkey', {
    ...signalConfig(signal),
    ...(proofToken ? { headers: { 'X-Security-Proof': cleanProofToken(proofToken) } } : {}),
  });
  successfulMutation(response.data);
}

export async function getSessions(signal?: AbortSignal): Promise<LoginSession[]> {
  const response = await api.get<unknown>('/user/sessions', signalConfig(signal));
  return parseSessionsResponse(response.data);
}

export async function revokeSession(sid: string, signal?: AbortSignal): Promise<void> {
  const safeSID = safeText(sid, 64, false);
  if (!/^[A-Za-z0-9_-]+$/.test(safeSID)) contractError();
  const response = await api.delete<unknown>(`/user/sessions/${encodeURIComponent(safeSID)}`, signalConfig(signal));
  successfulMutation(response.data);
}

export async function revokeOtherSessions(signal?: AbortSignal): Promise<void> {
  const response = await api.post<unknown>('/user/sessions/revoke-others', {}, signalConfig(signal));
  successfulMutation(response.data);
}

export async function getOAuthCatalog(signal?: AbortSignal): Promise<OAuthCatalog> {
  const response = await api.get<unknown>('/status', signalConfig(signal));
  return parseOAuthCatalogResponse(response.data);
}

export async function getOAuthBindings(signal?: AbortSignal): Promise<CustomOAuthBinding[]> {
  const response = await api.get<unknown>('/user/oauth/bindings', signalConfig(signal));
  return parseOAuthBindingsResponse(response.data);
}

export async function createOAuthBindingURL(
  provider: CustomOAuthProvider,
  rawOrigin: string,
  signal?: AbortSignal,
): Promise<string> {
  const safeProvider = parseOAuthProvider({
    id: provider.id,
    name: provider.name,
    slug: provider.slug,
    client_id: provider.clientId,
    authorization_endpoint: provider.authorizationEndpoint,
    scopes: provider.scopes,
  });
  const origin = browserOrigin(rawOrigin);

  const response = await api.post<unknown>('/oauth/state', {
    provider: safeProvider.slug,
    intent: 'bind',
    redirect: '/profile',
  }, signalConfig(signal));
  const flow = parseOAuthBindingFlowResponse(response.data);
  const target = new URL(safeProvider.authorizationEndpoint);
  target.searchParams.set('client_id', safeProvider.clientId);
  target.searchParams.set('redirect_uri', `${origin.origin}/api/oauth/${encodeURIComponent(safeProvider.slug)}/callback`);
  target.searchParams.set('response_type', 'code');
  target.searchParams.set('state', flow.flowToken);
  if (safeProvider.scopes) target.searchParams.set('scope', safeProvider.scopes);
  return target.toString();
}

export async function startTelegramBinding(
  rawOrigin: string,
  signal?: AbortSignal,
): Promise<TelegramBindingFlow> {
  // Validate the origin before creating a one-time server flow.
  browserOrigin(rawOrigin);
  const response = await api.post<unknown>('/oauth/telegram/bind/start', {}, signalConfig(signal));
  return parseTelegramBindingFlowResponse(response.data, rawOrigin);
}

export async function unbindOAuth(providerId: number, signal?: AbortSignal): Promise<void> {
  const id = integer(providerId, 1, MAX_SAFE_ID);
  const response = await api.delete<unknown>(`/user/oauth/bindings/${id}`, signalConfig(signal));
  successfulMutation(response.data);
}

export async function bindWeChat(code: string, signal?: AbortSignal): Promise<void> {
  const normalized = safeText(code, 512, false).trim();
  if (!normalized) contractError();
  const response = await api.post<unknown>('/oauth/wechat/bind', { code: normalized }, signalConfig(signal));
  successfulMutation(response.data);
}
