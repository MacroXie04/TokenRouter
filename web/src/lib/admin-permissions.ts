import type { User } from '../api';
import { isAuthenticatedUser } from './auth-flow';

export type AdminPermissionMatrix = Record<string, Record<string, boolean>>;

export const ADMIN_PERMISSION_RESOURCES = {
  CHANNEL: 'channel',
} as const;

export const ADMIN_PERMISSION_ACTIONS = {
  READ: 'read',
  OPERATE: 'operate',
  WRITE: 'write',
  SENSITIVE_WRITE: 'sensitive_write',
  SECRET_VIEW: 'secret_view',
} as const;

export interface ChannelAdminCapabilities {
  canRead: boolean;
  canOperate: boolean;
  canWrite: boolean;
  canSensitiveWrite: boolean;
}

const MAX_PERMISSION_RESOURCES = 64;
const MAX_PERMISSION_ACTIONS = 64;
const IDENTIFIER_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;
const RESERVED_IDENTIFIERS = new Set(['constructor', 'prototype']);
const REQUIRED_CHANNEL_ACTIONS = Object.values(ADMIN_PERMISSION_ACTIONS);

type UnknownRecord = Record<string, unknown>;

export class AdminPermissionContractError extends Error {
  constructor() {
    super('Invalid admin permission contract');
    this.name = 'AdminPermissionContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new AdminPermissionContractError();
  }
  return value as UnknownRecord;
}

function identifier(value: string): string {
  if (!IDENTIFIER_PATTERN.test(value) || RESERVED_IDENTIFIERS.has(value)) {
    throw new AdminPermissionContractError();
  }
  return value;
}

/**
 * Parse the effective permission matrix returned by /api/user/self. The
 * catalog may grow, so unknown well-formed resources/actions are preserved,
 * while the channel actions used by this client must all be present.
 */
export function parseAdminPermissionMatrix(value: unknown): AdminPermissionMatrix {
  const matrix = record(value);
  const resourceNames = Object.keys(matrix);
  if (resourceNames.length === 0 || resourceNames.length > MAX_PERMISSION_RESOURCES) {
    throw new AdminPermissionContractError();
  }

  const parsed: AdminPermissionMatrix = {};
  for (const rawResourceName of resourceNames) {
    const resourceName = identifier(rawResourceName);
    const rawActions = record(matrix[resourceName]);
    const actionNames = Object.keys(rawActions);
    if (actionNames.length === 0 || actionNames.length > MAX_PERMISSION_ACTIONS) {
      throw new AdminPermissionContractError();
    }
    const actions: Record<string, boolean> = {};
    for (const rawActionName of actionNames) {
      const actionName = identifier(rawActionName);
      const granted = rawActions[actionName];
      if (typeof granted !== 'boolean') throw new AdminPermissionContractError();
      actions[actionName] = granted;
    }
    parsed[resourceName] = actions;
  }

  const channel = parsed[ADMIN_PERMISSION_RESOURCES.CHANNEL];
  if (!channel || REQUIRED_CHANNEL_ACTIONS.some((action) => typeof channel[action] !== 'boolean')) {
    throw new AdminPermissionContractError();
  }
  return parsed;
}

/** Validate the user shell and strictly parse a matrix when /user/self sends it. */
export function parseSelfUser(value: unknown): User {
  if (!isAuthenticatedUser(value)) throw new AdminPermissionContractError();
  const rawUser = value as User & { permissions?: unknown };
  if (rawUser.permissions === undefined) return value;

  const permissions = record(rawUser.permissions);
  if (permissions.admin_permissions === undefined) return value;
  return {
    ...value,
    permissions: {
      ...permissions,
      admin_permissions: parseAdminPermissionMatrix(permissions.admin_permissions),
    },
  } as User;
}

/** Root remains an unconditional superuser; every other runtime value fails closed. */
export function hasAdminPermission(
  user: User | null | undefined,
  resource: string,
  action: string,
): boolean {
  if (!user) return false;
  if (user.role >= 100) return true;
  try {
    return parseAdminPermissionMatrix(user.permissions?.admin_permissions)[resource]?.[action] === true;
  } catch {
    return false;
  }
}

export function channelAdminCapabilities(user: User | null | undefined): ChannelAdminCapabilities {
  const denied = { canRead: false, canOperate: false, canWrite: false, canSensitiveWrite: false };
  if (!user) return denied;
  if (user.role >= 100) {
    return { canRead: true, canOperate: true, canWrite: true, canSensitiveWrite: true };
  }
  try {
    const matrix = parseAdminPermissionMatrix(user.permissions?.admin_permissions);
    const channel = matrix[ADMIN_PERMISSION_RESOURCES.CHANNEL];
    return {
      canRead: channel[ADMIN_PERMISSION_ACTIONS.READ] === true,
      canOperate: channel[ADMIN_PERMISSION_ACTIONS.OPERATE] === true,
      canWrite: channel[ADMIN_PERMISSION_ACTIONS.WRITE] === true,
      canSensitiveWrite: channel[ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE] === true,
    };
  } catch {
    return denied;
  }
}
