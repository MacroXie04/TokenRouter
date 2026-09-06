import { api } from '../../api';
import { serializeSystemSettingsMutation, SystemSettingsContractError, SystemSettingsRequestError } from './system-settings-api';

const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_PROVIDERS = 1_000;
const MAX_ID = 2_147_483_647;
const REQUEST_TIMEOUT_MS = 15_000;
const MAX_POLICY_BYTES = 64 * 1024;
const BUILT_IN_SLUGS = new Set(['github', 'discord', 'oidc', 'linuxdo', 'telegram', 'wechat']);
const REQUEST_LIMITS = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
} as const;
const FIELD_PATH = /^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$/u;
const POLICY_OPERATIONS = new Set([
  'eq', 'ne', 'gt', 'gte', 'lt', 'lte', 'in', 'not_in', 'contains', 'not_contains', 'exists', 'not_exists',
]);
const PROVIDER_KEYS = new Set([
  'id', 'name', 'slug', 'icon', 'enabled', 'client_id', 'authorization_endpoint', 'token_endpoint',
  'user_info_endpoint', 'scopes', 'user_id_field', 'username_field', 'display_name_field', 'email_field',
  'well_known', 'auth_style', 'access_policy', 'access_denied_message',
]);

type UnknownRecord = Record<string, unknown>;

export interface CustomOAuthProvider {
  id: number;
  name: string;
  slug: string;
  icon: string;
  enabled: boolean;
  clientId: string;
  authorizationEndpoint: string;
  tokenEndpoint: string;
  userInfoEndpoint: string;
  scopes: string;
  userIdField: string;
  usernameField: string;
  displayNameField: string;
  emailField: string;
  wellKnown: string;
  authStyle: 0 | 1 | 2;
  accessPolicy: string;
  accessDeniedMessage: string;
}

export interface CustomOAuthInput extends Omit<CustomOAuthProvider, 'id'> {
  clientSecret: string;
}

export interface OAuthDiscoveryResult {
  wellKnownUrl: string;
  issuer: string;
  authorizationEndpoint: string;
  tokenEndpoint: string;
  userInfoEndpoint: string;
  scopes: string;
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new SystemSettingsContractError();
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: Set<string>): void {
  if (Object.keys(value).some((key) => !allowed.has(key))) throw new SystemSettingsContractError();
}

function payloadBytes(value: unknown): number {
  try {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) throw new Error('not serializable');
    return new TextEncoder().encode(encoded).byteLength;
  } catch {
    throw new SystemSettingsContractError();
  }
}

function safeText(value: unknown, maximumBytes: number, allowNewlines = false): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximumBytes) {
    throw new SystemSettingsContractError();
  }
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    const whitespace = allowNewlines && (code === 9 || code === 10 || code === 13);
    if ((!whitespace && code <= 31) || (code >= 127 && code <= 159)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      throw new SystemSettingsContractError();
    }
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) throw new SystemSettingsContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new SystemSettingsContractError();
    }
  }
  return value;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new SystemSettingsContractError();
  }
  return value as number;
}

function throwIfAborted(signal?: AbortSignal): void {
  if (!signal?.aborted) return;
  if (typeof DOMException !== 'undefined') throw new DOMException('The operation was aborted', 'AbortError');
  const error = new Error('The operation was aborted');
  error.name = 'AbortError';
  throw error;
}

function envelope(value: unknown, requireData: boolean): UnknownRecord {
  if (payloadBytes(value) > MAX_RESPONSE_BYTES) throw new SystemSettingsContractError();
  const raw = record(value);
  exactKeys(raw, new Set(['success', 'message', 'data']));
  const message = raw.message === undefined ? '' : safeText(raw.message, 512, true);
  if (raw.success !== true) throw new SystemSettingsRequestError(message || 'Request failed');
  if (requireData && !Object.hasOwn(raw, 'data')) throw new SystemSettingsContractError();
  return raw;
}

function endpoint(value: unknown): string {
  const parsed = safeText(value, 512);
  if (parsed === '') return parsed;
  if (parsed !== parsed.trim() || parsed.includes('\\')) throw new SystemSettingsContractError();
  let url: URL;
  try {
    url = new URL(parsed);
  } catch {
    throw new SystemSettingsContractError();
  }
  if ((url.protocol !== 'https:' && url.protocol !== 'http:') || url.hostname === ''
    || url.username !== '' || url.password !== '' || url.hash !== '') {
    throw new SystemSettingsContractError();
  }
  const queryNames = new Set<string>();
  let queryPairs = 0;
  for (const [name, queryValue] of url.searchParams) {
    queryPairs += 1;
    if (queryPairs > 16 || name === '' || queryNames.has(name)) throw new SystemSettingsContractError();
    safeText(name, 128);
    safeText(queryValue, 2_048);
    queryNames.add(name);
  }
  const hostname = url.hostname.toLowerCase().replace(/\.$/u, '');
  const loopback = hostname === 'localhost' || hostname.endsWith('.localhost')
    || hostname === '[::1]' || hostname === '::1' || /^127(?:\.\d{1,3}){3}$/u.test(hostname);
  if (url.protocol === 'http:' && !loopback) throw new SystemSettingsContractError();
  return parsed;
}

function validatePolicyValue(value: unknown, depth: number, count: { values: number }): void {
  if (depth > 16 || count.values >= 4_096) throw new SystemSettingsContractError();
  count.values += 1;
  if (value === null || typeof value === 'boolean') return;
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new SystemSettingsContractError();
    return;
  }
  if (typeof value === 'string') {
    safeText(value, 4_096);
    return;
  }
  if (Array.isArray(value)) {
    if (value.length > 256) throw new SystemSettingsContractError();
    value.forEach((item) => validatePolicyValue(item, depth + 1, count));
    return;
  }
  const object = record(value);
  if (Object.keys(object).length > 256) throw new SystemSettingsContractError();
  Object.entries(object).forEach(([key, item]) => {
    safeText(key, 128);
    validatePolicyValue(item, depth + 1, count);
  });
}

function validateAccessPolicy(value: unknown): string {
  const policy = safeText(value, MAX_POLICY_BYTES, true);
  if (policy.trim() === '') return policy;
  let parsed: unknown;
  try {
    parsed = JSON.parse(policy);
  } catch {
    throw new SystemSettingsContractError();
  }
  const entries = { count: 0 };
  const values = { values: 0 };
  const visit = (candidate: unknown, depth: number): void => {
    if (depth > 16) throw new SystemSettingsContractError();
    const document = record(candidate);
    exactKeys(document, new Set(['logic', 'conditions', 'groups']));
    const conditions = document.conditions === undefined ? [] : document.conditions;
    const groups = document.groups === undefined ? [] : document.groups;
    if (!Array.isArray(conditions) || !Array.isArray(groups) || conditions.length + groups.length === 0
      || conditions.length > 1_024 || groups.length > 1_024
      || entries.count > 1_024 - conditions.length - groups.length) {
      throw new SystemSettingsContractError();
    }
    entries.count += conditions.length + groups.length;
    const logic = document.logic === undefined ? 'and' : safeText(document.logic, 8).trim().toLowerCase();
    if (logic !== 'and' && logic !== 'or') throw new SystemSettingsContractError();
    conditions.forEach((condition) => {
      const item = record(condition);
      exactKeys(item, new Set(['field', 'op', 'value']));
      const field = safeText(item.field, 128).trim();
      const operation = safeText(item.op, 32).trim().toLowerCase();
      if (field === '' || !POLICY_OPERATIONS.has(operation)) throw new SystemSettingsContractError();
      if ((operation === 'in' || operation === 'not_in') && !Array.isArray(item.value)) {
        throw new SystemSettingsContractError();
      }
      validatePolicyValue(item.value, 1, values);
    });
    groups.forEach((group) => visit(group, depth + 1));
  };
  visit(parsed, 1);
  return policy;
}

function parseProvider(value: unknown): CustomOAuthProvider {
  const raw = record(value);
  exactKeys(raw, PROVIDER_KEYS);
  if (typeof raw.enabled !== 'boolean') throw new SystemSettingsContractError();
  const slug = safeText(raw.slug, 64);
  if (!/^[a-z0-9-]{1,64}$/u.test(slug) || BUILT_IN_SLUGS.has(slug)) throw new SystemSettingsContractError();
  const authStyle = integer(raw.auth_style, 0, 2) as 0 | 1 | 2;
  const accessPolicy = validateAccessPolicy(raw.access_policy);
  const name = safeText(raw.name, 64);
  const clientId = safeText(raw.client_id, 256);
  const authorizationEndpoint = endpoint(raw.authorization_endpoint);
  const tokenEndpoint = endpoint(raw.token_endpoint);
  const userInfoEndpoint = endpoint(raw.user_info_endpoint);
  if (name.trim() === '' || clientId === '' || authorizationEndpoint === '' || tokenEndpoint === '' || userInfoEndpoint === '') {
    throw new SystemSettingsContractError();
  }
  const mappings = [raw.user_id_field, raw.username_field, raw.display_name_field, raw.email_field]
    .map((field) => safeText(field, 128));
  if (mappings.some((field) => !FIELD_PATH.test(field))) throw new SystemSettingsContractError();
  return {
    id: integer(raw.id, 1, MAX_ID),
    name,
    slug,
    icon: safeText(raw.icon, 128),
    enabled: raw.enabled,
    clientId,
    authorizationEndpoint,
    tokenEndpoint,
    userInfoEndpoint,
    scopes: safeText(raw.scopes, 256),
    userIdField: mappings[0],
    usernameField: mappings[1],
    displayNameField: mappings[2],
    emailField: mappings[3],
    wellKnown: endpoint(raw.well_known),
    authStyle,
    accessPolicy,
    accessDeniedMessage: safeText(raw.access_denied_message, 512),
  };
}

export function parseCustomOAuthProvidersResponse(value: unknown): CustomOAuthProvider[] {
  const data = envelope(value, true).data;
  if (!Array.isArray(data) || data.length > MAX_PROVIDERS) throw new SystemSettingsContractError();
  const ids = new Set<number>();
  const slugs = new Set<string>();
  return data.map((candidate) => {
    const provider = parseProvider(candidate);
    if (ids.has(provider.id) || slugs.has(provider.slug)) throw new SystemSettingsContractError();
    ids.add(provider.id);
    slugs.add(provider.slug);
    return provider;
  });
}

function body(input: CustomOAuthInput, editing: boolean): UnknownRecord {
  const name = safeText(input.name.trim(), 64);
  const slug = safeText(input.slug.trim().toLowerCase(), 64);
  if (name === '' || !/^[a-z0-9-]{1,64}$/u.test(slug) || BUILT_IN_SLUGS.has(slug)) {
    throw new SystemSettingsContractError();
  }
  const clientId = safeText(input.clientId, 256);
  const clientSecret = safeText(input.clientSecret, 512);
  if (clientId === '' || clientId !== clientId.trim() || (!editing && clientSecret === '')) {
    throw new SystemSettingsContractError();
  }
  const authorizationEndpoint = endpoint(input.authorizationEndpoint.trim());
  const tokenEndpoint = endpoint(input.tokenEndpoint.trim());
  const userInfoEndpoint = endpoint(input.userInfoEndpoint.trim());
  if (!authorizationEndpoint || !tokenEndpoint || !userInfoEndpoint) throw new SystemSettingsContractError();
  if (typeof input.enabled !== 'boolean') throw new SystemSettingsContractError();
  const accessPolicy = validateAccessPolicy(input.accessPolicy);
  const mappings = [input.userIdField, input.usernameField, input.displayNameField, input.emailField]
    .map((field) => safeText(field.trim(), 128));
  if (mappings.some((field) => !FIELD_PATH.test(field))) throw new SystemSettingsContractError();
  return {
    name,
    slug,
    icon: safeText(input.icon, 128),
    enabled: input.enabled,
    client_id: clientId,
    client_secret: clientSecret,
    authorization_endpoint: authorizationEndpoint,
    token_endpoint: tokenEndpoint,
    user_info_endpoint: userInfoEndpoint,
    scopes: safeText(input.scopes, 256),
    user_id_field: mappings[0],
    username_field: mappings[1],
    display_name_field: mappings[2],
    email_field: mappings[3],
    well_known: endpoint(input.wellKnown.trim()),
    auth_style: integer(input.authStyle, 0, 2),
    access_policy: accessPolicy,
    access_denied_message: safeText(input.accessDeniedMessage, 512),
  };
}

export async function loadCustomOAuthProviders(signal?: AbortSignal): Promise<CustomOAuthProvider[]> {
  throwIfAborted(signal);
  const response = await api.get<unknown>('/custom-oauth-provider/', {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    ...REQUEST_LIMITS,
  });
  throwIfAborted(signal);
  return parseCustomOAuthProvidersResponse(response.data);
}

export function createCustomOAuthProvider(input: CustomOAuthInput, signal?: AbortSignal): Promise<CustomOAuthProvider> {
  const request = body(input, false);
  return serializeSystemSettingsMutation(async () => {
    const response = await api.post<unknown>('/custom-oauth-provider/', request, {
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    return parseProvider(envelope(response.data, true).data);
  }, signal);
}

export function updateCustomOAuthProvider(
  id: number,
  input: CustomOAuthInput,
  signal?: AbortSignal,
): Promise<CustomOAuthProvider> {
  const safeId = integer(id, 1, MAX_ID);
  const request = body(input, true);
  return serializeSystemSettingsMutation(async () => {
    const response = await api.put<unknown>(`/custom-oauth-provider/${safeId}`, request, {
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    return parseProvider(envelope(response.data, true).data);
  }, signal);
}

export function deleteCustomOAuthProvider(id: number, signal?: AbortSignal): Promise<void> {
  const safeId = integer(id, 1, MAX_ID);
  return serializeSystemSettingsMutation(async () => {
    const response = await api.delete<unknown>(`/custom-oauth-provider/${safeId}`, {
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      ...REQUEST_LIMITS,
    });
    throwIfAborted(signal);
    envelope(response.data, false);
  }, signal);
}

function optionalDiscoveryText(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : safeText(value, maximum);
}

export async function discoverCustomOAuthProvider(url: string, signal?: AbortSignal): Promise<OAuthDiscoveryResult> {
  throwIfAborted(signal);
  const wellKnownUrl = endpoint(url.trim());
  if (!wellKnownUrl) throw new SystemSettingsContractError();
  const response = await api.post<unknown>(
    '/custom-oauth-provider/discovery',
    { well_known_url: wellKnownUrl, issuer_url: '' },
    { signal, timeout: 25_000, ...REQUEST_LIMITS },
  );
  throwIfAborted(signal);
  const data = record(envelope(response.data, true).data);
  exactKeys(data, new Set(['well_known_url', 'discovery']));
  const discovery = record(data.discovery);
  // The discovery document is intentionally projected onto the only fields the form consumes.
  const scopesValue = discovery.scopes_supported;
  let scopes = '';
  if (scopesValue !== undefined) {
    if (!Array.isArray(scopesValue) || scopesValue.length > 128) throw new SystemSettingsContractError();
    scopes = scopesValue.map((scope) => safeText(scope, 128)).join(' ');
    if (new TextEncoder().encode(scopes).byteLength > 256) throw new SystemSettingsContractError();
  }
  return {
    wellKnownUrl: endpoint(data.well_known_url),
    issuer: optionalDiscoveryText(discovery.issuer, 512),
    authorizationEndpoint: endpoint(optionalDiscoveryText(discovery.authorization_endpoint, 512)),
    tokenEndpoint: endpoint(optionalDiscoveryText(discovery.token_endpoint, 512)),
    userInfoEndpoint: endpoint(optionalDiscoveryText(discovery.userinfo_endpoint, 512)),
    scopes,
  };
}
