import { describe, expect, it } from 'vitest';
import type { User } from '../../shared/api/client';
import {
  AdminPermissionContractError,
  channelAdminCapabilities,
  hasAdminPermission,
  parseAdminPermissionMatrix,
  parseSelfUser,
} from './admin-permissions';

const fullChannelMatrix = {
  channel: {
    read: true,
    operate: false,
    write: true,
    sensitive_write: false,
    secret_view: false,
  },
};

const adminUser: User = {
  id: 7,
  username: 'operator',
  display_name: 'Operator',
  role: 10,
  group: 'default',
  quota: 0,
  used_quota: 0,
  request_count: 0,
  permissions: { admin_permissions: fullChannelMatrix },
};

const malformedMatrices: unknown[] = [
  null,
  [],
  {},
  { channel: { read: true } },
  { channel: { ...fullChannelMatrix.channel, operate: 'yes' } },
  { channel: { ...fullChannelMatrix.channel }, 'Bad resource': { read: true } },
  { channel: { ...fullChannelMatrix.channel }, constructor: { read: true } },
];

describe('admin permissions', () => {
  it('strictly parses the complete channel matrix and preserves future bounded entries', () => {
    expect(parseAdminPermissionMatrix({
      ...fullChannelMatrix,
      future_resource: { future_action: true },
    })).toEqual({
      ...fullChannelMatrix,
      future_resource: { future_action: true },
    });
  });

  it.each(malformedMatrices)('rejects a malformed effective matrix', (value) => {
    expect(() => parseAdminPermissionMatrix(value)).toThrow(AdminPermissionContractError);
  });

  it('parses /user/self permissions without trusting a typed cast', () => {
    const parsed = parseSelfUser(adminUser);
    expect(parsed.permissions?.admin_permissions).toEqual(fullChannelMatrix);
    expect(() => parseSelfUser({
      ...adminUser,
      permissions: { admin_permissions: { channel: { ...fullChannelMatrix.channel, write: 1 } } },
    })).toThrow(AdminPermissionContractError);
  });

  it('honors exact administrator grants and denies while malformed or absent data fails closed', () => {
    expect(channelAdminCapabilities(adminUser)).toEqual({
      canRead: true,
      canOperate: false,
      canWrite: true,
      canSensitiveWrite: false,
    });
    expect(hasAdminPermission({ ...adminUser, permissions: undefined }, 'channel', 'read')).toBe(false);
    expect(hasAdminPermission({
      ...adminUser,
      permissions: { admin_permissions: { channel: { read: true } } as never },
    }, 'channel', 'read')).toBe(false);
  });

  it('keeps root users authorized even when the matrix is absent or explicitly false', () => {
    const root = {
      ...adminUser,
      role: 100,
      permissions: {
        admin_permissions: {
          channel: Object.fromEntries(Object.keys(fullChannelMatrix.channel).map((action) => [action, false])),
        },
      },
    } as User;
    expect(channelAdminCapabilities(root)).toEqual({
      canRead: true,
      canOperate: true,
      canWrite: true,
      canSensitiveWrite: true,
    });
    expect(hasAdminPermission({ ...root, permissions: undefined }, 'channel', 'secret_view')).toBe(true);
  });
});
