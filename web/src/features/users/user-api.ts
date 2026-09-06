import { api } from '../../api';

export const USER_PAGE_SIZE = 20;
export const USER_ROLE_COMMON = 1;
export const USER_ROLE_ADMIN = 10;
export const USER_ROLE_ROOT = 100;
export const USER_STATUS_ENABLED = 1;
export const USER_STATUS_DISABLED = 2;

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_USERS = 100;
const MAX_GROUPS = 256;
const MAX_PERMISSION_RESOURCES = 64;
const MAX_PERMISSION_ACTIONS = 64;
const MAX_PERMISSION_ROLES = 32;
const MAX_CUSTOM_BINDINGS = 256;
const MAX_TOTAL = 10_000_000;
const MAX_ID = 2_147_483_647;
const MAX_QUOTA = 2_147_483_647;
const MAX_UNIX_SECONDS = 253_402_300_799;
const REQUEST_TIMEOUT_MS = 15_000;

export type UserRole = typeof USER_ROLE_COMMON | typeof USER_ROLE_ADMIN | typeof USER_ROLE_ROOT;
export type UserStatus = typeof USER_STATUS_ENABLED | typeof USER_STATUS_DISABLED;
export type UserStatusFilter = '' | UserStatus | -1;
export type UserRoleFilter = '' | UserRole;
export type UserSortBy = 'id' | 'username' | 'quota' | 'group' | 'created_at' | 'last_login_at';
export type UserSortOrder = 'asc' | 'desc';
export type UserManageAction = 'enable' | 'disable' | 'promote' | 'demote';
export type QuotaMode = 'add' | 'subtract' | 'override';

export interface ManagedUser {
  id: number;
  username: string;
  displayName: string;
  email: string;
  quota: number;
  usedQuota: number;
  requestCount: number;
  group: string;
  status: UserStatus;
  role: UserRole;
  remark: string;
  createdAt: number;
  lastLoginAt: number;
}

export interface UserQuery {
  keyword: string;
  group: string;
  role: UserRoleFilter;
  status: UserStatusFilter;
  sortBy: UserSortBy;
  sortOrder: UserSortOrder;
  page: number;
  pageSize: number;
}

export interface UserPage {
  items: ManagedUser[];
  total: number;
  page: number;
  pageSize: number;
}

export interface CreateUserInput {
  username: string;
  displayName: string;
  password: string;
  role: typeof USER_ROLE_COMMON | typeof USER_ROLE_ADMIN;
  adminPermissions?: PermissionMatrix;
}

export interface UpdateUserInput {
  id: number;
  displayName: string;
  group: string;
  remark: string;
  password?: string;
}

export type PermissionMatrix = Record<string, Record<string, boolean>>;

export interface PermissionActionDefinition {
  action: string;
  labelKey: string;
  descriptionKey: string;
}

export interface PermissionResourceDefinition {
  resource: string;
  labelKey: string;
  actions: PermissionActionDefinition[];
}

export interface PermissionRoleDefinition {
  key: string;
  name: string;
  builtIn: boolean;
  superuser: boolean;
  grants: PermissionMatrix;
}

export interface PermissionCatalog {
  resources: PermissionResourceDefinition[];
  roles: PermissionRoleDefinition[];
}

export interface UserPermissionState {
  id: number;
  username: string;
  role: UserRole;
  permissions: PermissionMatrix;
}

export type BuiltInBindingType = 'email' | 'github' | 'discord' | 'oidc' | 'wechat' | 'telegram' | 'linuxdo';

export interface BuiltInBindingState {
  type: BuiltInBindingType;
  bound: boolean;
}

export interface UserBindingDetails {
  id: number;
  username: string;
  role: UserRole;
  bindings: BuiltInBindingState[];
}

export interface CustomOAuthBinding {
  providerId: number;
  providerName: string;
  providerSlug: string;
}

type UnknownRecord = Record<string, unknown>;

export class UserContractError extends Error {
  constructor() {
    super('Invalid user API contract');
    this.name = 'UserContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new UserContractError();
  }
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: ReadonlySet<string>): void {
  if (Object.keys(value).some((key) => !allowed.has(key))) throw new UserContractError();
}

function boundedPayload(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new UserContractError();
  }
  if (new TextEncoder().encode(encoded).byteLength > MAX_RESPONSE_BYTES) throw new UserContractError();
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new UserContractError();
  }
  return value as number;
}

function text(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length > maximum || value.includes('\0')) {
    throw new UserContractError();
  }
  return value;
}

function optionalText(value: unknown, maximum: number): string {
  return value === undefined || value === null ? '' : text(value, maximum);
}

function safeText(value: unknown, maximum: number, minimum = 0): string {
  if (typeof value !== 'string' || /[\p{Cc}\p{Cf}]/u.test(value)) throw new UserContractError();
  const length = [...value].length;
  if (length < minimum || length > maximum) throw new UserContractError();
  return value;
}

function identifier(value: unknown): string {
  const parsed = safeText(value, 64, 1);
  if (!/^[a-z][a-z0-9_]{0,63}$/.test(parsed)
    || parsed === 'constructor' || parsed === 'prototype') throw new UserContractError();
  return parsed;
}

function providerSlug(value: unknown): string {
  const parsed = safeText(value, 64, 1);
  if (!/^[a-z0-9-]+$/.test(parsed)) throw new UserContractError();
  return parsed;
}

function successfulEnvelope(value: unknown): UnknownRecord {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true) throw new UserContractError();
  return envelope;
}

function strictEnvelope(value: unknown, requireData: boolean): UnknownRecord {
  boundedPayload(value);
  const envelope = record(value);
  exactKeys(envelope, new Set(['success', 'message', 'data']));
  if (envelope.success !== true || (requireData && !Object.hasOwn(envelope, 'data'))) {
    throw new UserContractError();
  }
  if (envelope.message !== undefined) safeText(envelope.message, 512);
  return envelope;
}

function role(value: unknown): UserRole {
  const parsed = integer(value, USER_ROLE_COMMON, USER_ROLE_ROOT);
  if (parsed !== USER_ROLE_COMMON && parsed !== USER_ROLE_ADMIN && parsed !== USER_ROLE_ROOT) {
    throw new UserContractError();
  }
  return parsed;
}

function status(value: unknown): UserStatus {
  const parsed = integer(value, USER_STATUS_ENABLED, USER_STATUS_DISABLED);
  if (parsed !== USER_STATUS_ENABLED && parsed !== USER_STATUS_DISABLED) throw new UserContractError();
  return parsed;
}

const USER_DETAIL_KEYS = new Set([
  'id', 'username', 'display_name', 'role', 'status', 'email', 'email_verified',
  'quota_reminder_at', 'github_id', 'discord_id', 'oidc_id', 'wechat_id', 'telegram_id',
  'admin_permissions', 'quota', 'used_quota', 'request_count', 'group', 'aff_code',
  'aff_count', 'aff_quota', 'aff_history_quota', 'inviter_id', 'linuxdo_id', 'setting',
  'remark', 'stripe_customer', 'created_at', 'last_login_at', 'auth_version',
]);

const BUILT_IN_BINDINGS: ReadonlyArray<{ type: BuiltInBindingType; field: string; maximum: number }> = [
  { type: 'email', field: 'email', maximum: 50 },
  { type: 'github', field: 'github_id', maximum: 256 },
  { type: 'discord', field: 'discord_id', maximum: 256 },
  { type: 'oidc', field: 'oidc_id', maximum: 256 },
  { type: 'wechat', field: 'wechat_id', maximum: 256 },
  { type: 'telegram', field: 'telegram_id', maximum: 256 },
  { type: 'linuxdo', field: 'linuxdo_id', maximum: 256 },
];

function parseGenericPermissionMatrix(value: unknown): void {
  const matrix = record(value);
  const resources = Object.keys(matrix);
  if (resources.length > MAX_PERMISSION_RESOURCES) throw new UserContractError();
  for (const resourceName of resources) {
    identifier(resourceName);
    const actions = record(matrix[resourceName]);
    const actionNames = Object.keys(actions);
    if (actionNames.length > MAX_PERMISSION_ACTIONS) throw new UserContractError();
    for (const actionName of actionNames) {
      identifier(actionName);
      if (typeof actions[actionName] !== 'boolean') throw new UserContractError();
    }
  }
}

function parsePermissionMatrix(value: unknown, catalog: PermissionCatalog): PermissionMatrix {
  const matrix = record(value);
  const resourceNames = catalog.resources.map((resource) => resource.resource);
  if (Object.keys(matrix).length !== resourceNames.length) throw new UserContractError();
  const parsed: PermissionMatrix = {};
  for (const resource of catalog.resources) {
    if (!Object.hasOwn(matrix, resource.resource)) throw new UserContractError();
    const rawActions = record(matrix[resource.resource]);
    if (Object.keys(rawActions).length !== resource.actions.length) throw new UserContractError();
    const actions: Record<string, boolean> = {};
    for (const action of resource.actions) {
      if (!Object.hasOwn(rawActions, action.action) || typeof rawActions[action.action] !== 'boolean') {
        throw new UserContractError();
      }
      actions[action.action] = rawActions[action.action] as boolean;
    }
    parsed[resource.resource] = actions;
  }
  return parsed;
}

function parsePermissionResources(value: unknown): PermissionResourceDefinition[] {
  if (!Array.isArray(value) || value.length > MAX_PERMISSION_RESOURCES) throw new UserContractError();
  const seenResources = new Set<string>();
  return value.map((candidate) => {
    const raw = record(candidate);
    exactKeys(raw, new Set(['resource', 'label_key', 'actions']));
    const resource = identifier(raw.resource);
    if (seenResources.has(resource)) throw new UserContractError();
    seenResources.add(resource);
    if (!Array.isArray(raw.actions) || raw.actions.length > MAX_PERMISSION_ACTIONS) throw new UserContractError();
    const seenActions = new Set<string>();
    const actions = raw.actions.map((actionCandidate) => {
      const actionRaw = record(actionCandidate);
      exactKeys(actionRaw, new Set(['action', 'label_key', 'description_key']));
      const action = identifier(actionRaw.action);
      if (seenActions.has(action)) throw new UserContractError();
      seenActions.add(action);
      return {
        action,
        labelKey: safeText(actionRaw.label_key, 128, 1),
        descriptionKey: safeText(actionRaw.description_key, 512, 1),
      };
    });
    return { resource, labelKey: safeText(raw.label_key, 128, 1), actions };
  });
}

export function parsePermissionCatalogResponse(value: unknown): PermissionCatalog {
  const data = record(strictEnvelope(value, true).data);
  exactKeys(data, new Set(['resources', 'roles']));
  const resources = parsePermissionResources(data.resources);
  const partialCatalog: PermissionCatalog = { resources, roles: [] };
  if (!Array.isArray(data.roles) || data.roles.length > MAX_PERMISSION_ROLES) throw new UserContractError();
  const seenRoles = new Set<string>();
  const roles = data.roles.map((candidate) => {
    const raw = record(candidate);
    exactKeys(raw, new Set(['key', 'name', 'built_in', 'superuser', 'grants']));
    const key = identifier(raw.key);
    if (seenRoles.has(key) || typeof raw.built_in !== 'boolean' || typeof raw.superuser !== 'boolean') {
      throw new UserContractError();
    }
    seenRoles.add(key);
    return {
      key,
      name: safeText(raw.name, 64, 1),
      builtIn: raw.built_in,
      superuser: raw.superuser,
      grants: parsePermissionMatrix(raw.grants, partialCatalog),
    };
  });
  if (!seenRoles.has('admin')) throw new UserContractError();
  return { resources, roles };
}

function parseUserDetail(value: unknown, expectedId: number): UnknownRecord {
  const data = record(strictEnvelope(value, true).data);
  exactKeys(data, USER_DETAIL_KEYS);
  if (integer(data.id, 1, MAX_ID) !== validID(expectedId)) throw new UserContractError();
  safeText(data.username, 64, 1);
  role(data.role);
  if (data.admin_permissions !== undefined) parseGenericPermissionMatrix(data.admin_permissions);
  return data;
}

export function parseUserPermissionResponse(
  value: unknown,
  expectedId: number,
  catalog: PermissionCatalog,
): UserPermissionState {
  const data = parseUserDetail(value, expectedId);
  if (data.admin_permissions === undefined) throw new UserContractError();
  return {
    id: data.id as number,
    username: data.username as string,
    role: role(data.role),
    permissions: parsePermissionMatrix(data.admin_permissions, catalog),
  };
}

export function parseUserBindingDetailsResponse(value: unknown, expectedId: number): UserBindingDetails {
  const data = parseUserDetail(value, expectedId);
  const bindings = BUILT_IN_BINDINGS.map(({ type, field, maximum }) => ({
    type,
    bound: safeText(data[field], maximum).trim().length > 0,
  }));
  return {
    id: data.id as number,
    username: data.username as string,
    role: role(data.role),
    bindings,
  };
}

export function parseUserDetailsResponse(value: unknown, expectedId: number): ManagedUser {
  return parseUser(parseUserDetail(value, expectedId));
}

export function parseCustomOAuthBindingsResponse(value: unknown): CustomOAuthBinding[] {
  const data = strictEnvelope(value, true).data;
  if (!Array.isArray(data) || data.length > MAX_CUSTOM_BINDINGS) throw new UserContractError();
  const seen = new Set<number>();
  return data.map((candidate) => {
    const raw = record(candidate);
    exactKeys(raw, new Set([
      'provider_id', 'provider_name', 'provider_slug', 'provider_icon', 'provider_user_id',
    ]));
    const providerId = integer(raw.provider_id, 1, MAX_ID);
    if (seen.has(providerId)) throw new UserContractError();
    seen.add(providerId);
    // Provider-side identifiers and icons are validated but intentionally not
    // returned: the admin UI only needs the binding's existence and name.
    safeText(raw.provider_icon, 128);
    safeText(raw.provider_user_id, 256, 1);
    return {
      providerId,
      providerName: safeText(raw.provider_name, 64, 1),
      providerSlug: providerSlug(raw.provider_slug),
    };
  });
}

function parseUser(value: unknown): ManagedUser {
  const item = record(value);
  // An administrator list must never become a credential-disclosure surface,
  // even if a future backend regression serializes these fields.
  if ('password' in item && item.password !== '' && item.password !== null && item.password !== undefined) {
    throw new UserContractError();
  }
  if ('access_token' in item && item.access_token !== '' && item.access_token !== null && item.access_token !== undefined) {
    throw new UserContractError();
  }
  return {
    id: integer(item.id, 1, MAX_ID),
    // Public registration permits 64 characters even though the stricter
    // administrator-create contract is capped at 20.
    username: text(item.username, 64),
    displayName: text(item.display_name, 64),
    email: optionalText(item.email, 50),
    quota: integer(item.quota, 0, MAX_QUOTA),
    usedQuota: integer(item.used_quota, 0, MAX_QUOTA),
    requestCount: integer(item.request_count, 0, MAX_QUOTA),
    group: text(item.group, 64),
    status: status(item.status),
    role: role(item.role),
    remark: optionalText(item.remark, 255),
    createdAt: integer(item.created_at, 0, MAX_UNIX_SECONDS),
    lastLoginAt: integer(item.last_login_at, 0, MAX_UNIX_SECONDS),
  };
}

function parseItems(value: unknown): ManagedUser[] {
  if (!Array.isArray(value) || value.length > MAX_USERS) throw new UserContractError();
  return value.map(parseUser);
}

export function parseUserListResponse(value: unknown): UserPage {
  const data = record(successfulEnvelope(value).data);
  const pagination = record(data.pagination);
  return {
    items: parseItems(data.items),
    total: integer(pagination.total, 0, MAX_TOTAL),
    page: integer(pagination.page, 1, 1_000_000),
    pageSize: integer(pagination.page_size, 1, MAX_USERS),
  };
}

export function parseUserSearchResponse(value: unknown): UserPage {
  const data = record(successfulEnvelope(value).data);
  return {
    items: parseItems(data.items),
    total: integer(data.total, 0, MAX_TOTAL),
    page: integer(data.page, 1, 1_000_000),
    pageSize: integer(data.page_size, 1, MAX_USERS),
  };
}

export function parseUserGroupsResponse(value: unknown): string[] {
  const data = successfulEnvelope(value).data;
  if (!Array.isArray(data) || data.length > MAX_GROUPS) throw new UserContractError();
  const groups = new Set<string>();
  for (const value of data) {
    const group = text(value, 64).trim();
    if (group) groups.add(group);
  }
  return [...groups].sort((left, right) => left.localeCompare(right));
}

function parseMutationResponse(value: unknown): void {
  successfulEnvelope(value);
}

function parseStrictMutationResponse(value: unknown): void {
  const envelope = strictEnvelope(value, false);
  if (envelope.data !== undefined && envelope.data !== null) throw new UserContractError();
}

function validID(value: number): number {
  return integer(value, 1, MAX_ID);
}

function validPage(value: number): number {
  return integer(value, 1, 1_000_000);
}

function validPageSize(value: number): number {
  return integer(value, 1, MAX_USERS);
}

function validSortBy(value: UserSortBy): UserSortBy {
  if (!['id', 'username', 'quota', 'group', 'created_at', 'last_login_at'].includes(value)) {
    throw new UserContractError();
  }
  return value;
}

function validSortOrder(value: UserSortOrder): UserSortOrder {
  if (value !== 'asc' && value !== 'desc') throw new UserContractError();
  return value;
}

const responseLimits = {
  timeout: REQUEST_TIMEOUT_MS,
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

export async function loadPermissionCatalog(signal?: AbortSignal): Promise<PermissionCatalog> {
  const response = await api.get<unknown>('/authz/catalog', { signal, ...responseLimits });
  return parsePermissionCatalogResponse(response.data);
}

export async function loadUserPermissionState(
  id: number,
  catalog: PermissionCatalog,
  signal?: AbortSignal,
): Promise<UserPermissionState> {
  const safeId = validID(id);
  const response = await api.get<unknown>(`/user/${safeId}`, { signal, ...responseLimits });
  return parseUserPermissionResponse(response.data, safeId, catalog);
}

export async function loadUserDetails(id: number, signal?: AbortSignal): Promise<ManagedUser> {
  const safeId = validID(id);
  const response = await api.get<unknown>(`/user/${safeId}`, { signal, ...responseLimits });
  return parseUserDetailsResponse(response.data, safeId);
}

export async function updateUserPermissions(
  id: number,
  catalog: PermissionCatalog,
  permissions: PermissionMatrix,
): Promise<void> {
  const cleanPermissions = parsePermissionMatrix(permissions, catalog);
  const response = await api.put<unknown>('/user/', {
    id: validID(id),
    admin_permissions: cleanPermissions,
  }, responseLimits);
  parseStrictMutationResponse(response.data);
}

export async function loadUserBindingDetails(id: number, signal?: AbortSignal): Promise<UserBindingDetails> {
  const safeId = validID(id);
  const response = await api.get<unknown>(`/user/${safeId}`, { signal, ...responseLimits });
  return parseUserBindingDetailsResponse(response.data, safeId);
}

export async function loadCustomOAuthBindings(id: number, signal?: AbortSignal): Promise<CustomOAuthBinding[]> {
  const safeId = validID(id);
  const response = await api.get<unknown>(`/user/${safeId}/oauth/bindings`, { signal, ...responseLimits });
  return parseCustomOAuthBindingsResponse(response.data);
}

export async function clearBuiltInUserBinding(id: number, bindingType: BuiltInBindingType): Promise<void> {
  if (!BUILT_IN_BINDINGS.some((binding) => binding.type === bindingType)) throw new UserContractError();
  const response = await api.delete<unknown>(
    `/user/${validID(id)}/bindings/${bindingType}`,
    responseLimits,
  );
  parseStrictMutationResponse(response.data);
}

export async function unbindCustomOAuth(id: number, providerId: number): Promise<void> {
  const response = await api.delete<unknown>(
    `/user/${validID(id)}/oauth/bindings/${validID(providerId)}`,
    responseLimits,
  );
  parseStrictMutationResponse(response.data);
}

export async function listUsers(page: number, pageSize = USER_PAGE_SIZE, signal?: AbortSignal): Promise<UserPage> {
  const safePage = validPage(page);
  const response = await api.get<unknown>('/user', {
    // TokenRouter consumes `page`; `p` preserves reference-client compatibility.
    params: { page: safePage, p: safePage, page_size: validPageSize(pageSize) },
    signal,
    ...responseLimits,
  });
  return parseUserListResponse(response.data);
}

export async function searchUsers(query: UserQuery, signal?: AbortSignal): Promise<UserPage> {
  const params: Record<string, string | number> = {
    p: validPage(query.page),
    page_size: validPageSize(query.pageSize),
    sort_by: validSortBy(query.sortBy),
    sort_order: validSortOrder(query.sortOrder),
  };
  const keyword = text(query.keyword.trim(), 128);
  const group = text(query.group.trim(), 64);
  if (keyword) params.keyword = keyword;
  if (group) params.group = group;
  if (query.role !== '') params.role = role(query.role);
  if (query.status !== '') {
    if (query.status !== -1) status(query.status);
    params.status = query.status;
  }
  const response = await api.get<unknown>('/user/search', { params, signal, ...responseLimits });
  return parseUserSearchResponse(response.data);
}

export async function loadUserGroups(signal?: AbortSignal): Promise<string[]> {
  const response = await api.get<unknown>('/group/', { signal, ...responseLimits });
  return parseUserGroupsResponse(response.data);
}

export async function createUser(input: CreateUserInput): Promise<void> {
  const username = text(input.username.trim(), 20);
  const displayName = text(input.displayName.trim(), 20) || username;
  const password = text(input.password, 20);
  if (input.role !== USER_ROLE_COMMON && input.role !== USER_ROLE_ADMIN) throw new UserContractError();
  const safeRole = input.role;
  if (!username || password.length < 8) throw new UserContractError();
  if (input.adminPermissions !== undefined) {
    if (safeRole !== USER_ROLE_ADMIN) throw new UserContractError();
    parseGenericPermissionMatrix(input.adminPermissions);
  }
  const response = await api.post<unknown>('/user/', {
    username,
    display_name: displayName,
    password,
    role: safeRole,
    ...(input.adminPermissions === undefined ? {} : { admin_permissions: input.adminPermissions }),
  }, responseLimits);
  parseMutationResponse(response.data);
}

export async function updateUser(input: UpdateUserInput): Promise<void> {
  const displayName = text(input.displayName.trim(), 64);
  const group = text(input.group.trim(), 64);
  const remark = text(input.remark.trim(), 255);
  const password = input.password === undefined ? '' : text(input.password, 64);
  if (!displayName || !group || (password !== '' && password.length < 8)) throw new UserContractError();
  const response = await api.put<unknown>('/user/', {
    id: validID(input.id),
    display_name: displayName,
    group,
    remark,
    ...(password ? { password } : {}),
  }, responseLimits);
  parseMutationResponse(response.data);
}

export async function manageUser(id: number, action: UserManageAction): Promise<void> {
  if (!['enable', 'disable', 'promote', 'demote'].includes(action)) throw new UserContractError();
  const response = await api.post<unknown>('/user/manage', { id: validID(id), action }, responseLimits);
  parseMutationResponse(response.data);
}

export async function adjustUserQuota(id: number, mode: QuotaMode, value: number): Promise<void> {
  if (!['add', 'subtract', 'override'].includes(mode)) throw new UserContractError();
  const safeValue = integer(value, mode === 'override' ? 0 : 1, MAX_QUOTA);
  const response = await api.post<unknown>('/user/manage', {
    id: validID(id),
    action: 'add_quota',
    mode,
    value: safeValue,
  }, responseLimits);
  parseMutationResponse(response.data);
}

export async function deleteUser(id: number): Promise<void> {
  const response = await api.delete<unknown>(`/user/${validID(id)}`, responseLimits);
  parseMutationResponse(response.data);
}

export async function resetUserPasskey(id: number): Promise<void> {
  const response = await api.delete<unknown>(`/user/${validID(id)}/reset_passkey`, responseLimits);
  parseMutationResponse(response.data);
}

export async function resetUserTwoFactor(id: number): Promise<void> {
  const response = await api.delete<unknown>(`/user/${validID(id)}/2fa`, responseLimits);
  parseMutationResponse(response.data);
}
