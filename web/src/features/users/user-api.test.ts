import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import {
  USER_ROLE_ADMIN,
  USER_ROLE_COMMON,
  USER_STATUS_DISABLED,
  UserContractError,
  adjustUserQuota,
  clearBuiltInUserBinding,
  createUser,
  deleteUser,
  listUsers,
  loadCustomOAuthBindings,
  loadPermissionCatalog,
  loadUserBindingDetails,
  loadUserDetails,
  loadUserPermissionState,
  loadUserGroups,
  manageUser,
  parseCustomOAuthBindingsResponse,
  parsePermissionCatalogResponse,
  parseUserBindingDetailsResponse,
  parseUserDetailsResponse,
  parseUserListResponse,
  parseUserPermissionResponse,
  parseUserSearchResponse,
  resetUserPasskey,
  resetUserTwoFactor,
  searchUsers,
  unbindCustomOAuth,
  updateUser,
  updateUserPermissions,
} from './user-api';

vi.mock('../../api', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const mockedAPI = vi.mocked(api);

function user(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    username: 'alice',
    display_name: 'Alice',
    email: 'alice@example.test',
    quota: 10_000,
    used_quota: 250,
    request_count: 12,
    group: 'default',
    status: 1,
    role: 1,
    remark: 'reviewed',
    created_at: 1_700_000_000,
    last_login_at: 1_700_000_100,
    ...overrides,
  };
}

function mutationSuccess() {
  return { data: { success: true, message: '' } };
}

const permissionCatalogResponse = {
  success: true,
  message: '',
  data: {
    resources: [{
      resource: 'channel',
      label_key: 'Channel Management',
      actions: [
        { action: 'read', label_key: 'Read channels', description_key: 'View channel lists.' },
        { action: 'sensitive_write', label_key: 'Edit sensitive channels', description_key: 'Change credentials.' },
      ],
    }],
    roles: [
      {
        key: 'root', name: 'Root', built_in: true, superuser: true,
        grants: { channel: { read: true, sensitive_write: true } },
      },
      {
        key: 'admin', name: 'Admin', built_in: true, superuser: false,
        grants: { channel: { read: true, sensitive_write: false } },
      },
    ],
  },
};

const userDetailResponse = {
  success: true,
  data: {
    id: 7,
    username: 'alice',
    display_name: 'Alice',
    role: USER_ROLE_ADMIN,
    status: 1,
    email: 'alice@example.test',
    email_verified: true,
    quota_reminder_at: 0,
    github_id: 'github-subject',
    discord_id: '',
    oidc_id: '',
    wechat_id: '',
    telegram_id: '',
    linuxdo_id: '',
    admin_permissions: { channel: { read: true, sensitive_write: false } },
    quota: 1000,
    used_quota: 25,
    request_count: 2,
    group: 'default',
    created_at: 1_700_000_000,
    last_login_at: 1_700_000_100,
    aff_code: 'ABCDEFGH',
    aff_count: 0,
    aff_quota: 0,
    aff_history_quota: 0,
    inviter_id: 0,
    setting: '{}',
    remark: '',
    stripe_customer: '',
    auth_version: 1,
  },
};

beforeEach(() => {
  vi.resetAllMocks();
});

describe('user response contracts', () => {
  it('accepts both exact list and search pagination envelopes', () => {
    expect(parseUserListResponse({
      success: true,
      data: { items: [user()], pagination: { page: 2, page_size: 20, total: 21, total_pages: 2 } },
    })).toEqual({
      items: [{
        id: 7,
        username: 'alice',
        displayName: 'Alice',
        email: 'alice@example.test',
        quota: 10_000,
        usedQuota: 250,
        requestCount: 12,
        group: 'default',
        status: 1,
        role: 1,
        remark: 'reviewed',
        createdAt: 1_700_000_000,
        lastLoginAt: 1_700_000_100,
      }],
      total: 21,
      page: 2,
      pageSize: 20,
    });
    expect(parseUserSearchResponse({
      success: true,
      data: { items: [user()], page: 3, page_size: 10, total: 27 },
    })).toEqual(expect.objectContaining({ total: 27, page: 3, pageSize: 10 }));
  });

  it('fails closed on credentials, oversized collections, invalid roles, and malformed pagination', () => {
    const envelope = (item: unknown) => ({
      success: true,
      data: { items: [item], pagination: { page: 1, page_size: 20, total: 1 } },
    });
    expect(() => parseUserListResponse(envelope(user({ password: 'hash-must-not-render' })))).toThrow(UserContractError);
    expect(() => parseUserListResponse(envelope(user({ access_token: 'pat-must-not-render' })))).toThrow(UserContractError);
    expect(() => parseUserListResponse(envelope(user({ role: 99 })))).toThrow(UserContractError);
    expect(() => parseUserListResponse(envelope(user({ created_at: -1 })))).toThrow(UserContractError);
    expect(() => parseUserListResponse(envelope(user({ last_login_at: 253_402_300_800 })))).toThrow(UserContractError);
    expect(() => parseUserListResponse({
      success: true,
      data: { items: Array.from({ length: 101 }, () => user()), pagination: { page: 1, page_size: 20, total: 101 } },
    })).toThrow(UserContractError);
    expect(() => parseUserSearchResponse({ success: true, data: { items: [], page: 0, page_size: 20, total: 0 } })).toThrow(UserContractError);
  });
});

describe('user authorization and binding contracts', () => {
  it('parses a complete permission catalog and exact effective matrix', () => {
    const catalog = parsePermissionCatalogResponse(permissionCatalogResponse);
    expect(catalog.resources[0]).toEqual({
      resource: 'channel',
      labelKey: 'Channel Management',
      actions: [
        { action: 'read', labelKey: 'Read channels', descriptionKey: 'View channel lists.' },
        { action: 'sensitive_write', labelKey: 'Edit sensitive channels', descriptionKey: 'Change credentials.' },
      ],
    });
    expect(parseUserPermissionResponse(userDetailResponse, 7, catalog)).toEqual({
      id: 7,
      username: 'alice',
      role: USER_ROLE_ADMIN,
      permissions: { channel: { read: true, sensitive_write: false } },
    });
  });

  it('parses a fresh credential-free user detail for editing', async () => {
    expect(parseUserDetailsResponse(userDetailResponse, 7)).toEqual({
      id: 7,
      username: 'alice',
      displayName: 'Alice',
      email: 'alice@example.test',
      quota: 1000,
      usedQuota: 25,
      requestCount: 2,
      group: 'default',
      status: 1,
      role: USER_ROLE_ADMIN,
      remark: '',
      createdAt: 1_700_000_000,
      lastLoginAt: 1_700_000_100,
    });
    mockedAPI.get.mockResolvedValueOnce({ data: userDetailResponse });
    await expect(loadUserDetails(7)).resolves.toEqual(expect.objectContaining({ id: 7, displayName: 'Alice' }));
    expect(mockedAPI.get).toHaveBeenCalledWith('/user/7', expect.objectContaining({ maxContentLength: 524_288 }));
  });

  it('returns binding presence without retaining provider-side identifiers', () => {
    const details = parseUserBindingDetailsResponse(userDetailResponse, 7);
    expect(details.bindings).toEqual([
      { type: 'email', bound: true },
      { type: 'github', bound: true },
      { type: 'discord', bound: false },
      { type: 'oidc', bound: false },
      { type: 'wechat', bound: false },
      { type: 'telegram', bound: false },
      { type: 'linuxdo', bound: false },
    ]);
    expect(JSON.stringify(details)).not.toMatch(/alice@example|github-subject/);
    const parsed = parseCustomOAuthBindingsResponse({
      success: true,
      data: [{
        provider_id: 42,
        provider_name: 'Corporate SSO',
        provider_slug: 'corp-sso',
        provider_icon: 'https://cdn.example.test/icon.svg',
        provider_user_id: 'private-provider-subject',
      }],
    });
    expect(parsed).toEqual([{ providerId: 42, providerName: 'Corporate SSO', providerSlug: 'corp-sso' }]);
    expect(JSON.stringify(parsed)).not.toContain('private-provider-subject');
  });

  it('fails closed on unknown permission entries, missing grants, secrets, duplicates, and unsafe text', () => {
    expect(() => parsePermissionCatalogResponse({
      ...permissionCatalogResponse,
      data: {
        ...permissionCatalogResponse.data,
        resources: [{
          ...permissionCatalogResponse.data.resources[0],
          actions: [{ action: '__proto__', label_key: 'Bad', description_key: 'Bad.' }],
        }],
      },
    })).toThrow(UserContractError);

    const catalog = parsePermissionCatalogResponse(permissionCatalogResponse);
    expect(() => parseUserPermissionResponse({
      ...userDetailResponse,
      data: { ...userDetailResponse.data, admin_permissions: { channel: { read: true } } },
    }, 7, catalog)).toThrow(UserContractError);
    expect(() => parseUserPermissionResponse({
      ...userDetailResponse,
      data: { ...userDetailResponse.data, password: 'hash' },
    }, 7, catalog)).toThrow(UserContractError);
    expect(() => parseCustomOAuthBindingsResponse({
      success: true,
      data: [
        { provider_id: 2, provider_name: 'One', provider_slug: 'one', provider_icon: '', provider_user_id: 'a' },
        { provider_id: 2, provider_name: 'Two', provider_slug: 'two', provider_icon: '', provider_user_id: 'b' },
      ],
    })).toThrow(UserContractError);
    expect(() => parseCustomOAuthBindingsResponse({
      success: true,
      data: [{
        provider_id: 2, provider_name: 'Unsafe\u202ename', provider_slug: 'safe', provider_icon: '', provider_user_id: 'a',
      }],
    })).toThrow(UserContractError);
    expect(() => parseCustomOAuthBindingsResponse({
      success: true,
      data: Array.from({ length: 257 }, (_, index) => ({
        provider_id: index + 1,
        provider_name: `Provider ${index + 1}`,
        provider_slug: `provider-${index + 1}`,
        provider_icon: '',
        provider_user_id: `subject-${index + 1}`,
      })),
    })).toThrow(UserContractError);
    expect(() => parsePermissionCatalogResponse({
      ...permissionCatalogResponse,
      message: 'x'.repeat(600_000),
    })).toThrow(UserContractError);
  });
});

describe('user API routes', () => {
  it('uses the local/reference-compatible list route and exact filtered search query', async () => {
    mockedAPI.get.mockResolvedValueOnce({
      data: { success: true, data: { items: [user()], pagination: { page: 2, page_size: 20, total: 21 } } },
    });
    await listUsers(2);
    expect(mockedAPI.get).toHaveBeenCalledWith('/user', expect.objectContaining({
      params: { page: 2, p: 2, page_size: 20 },
      maxContentLength: 524_288,
      maxBodyLength: 524_288,
    }));

    mockedAPI.get.mockResolvedValueOnce({
      data: { success: true, data: { items: [user()], page: 1, page_size: 20, total: 1 } },
    });
    await searchUsers({
      keyword: ' alice ',
      group: ' vip ',
      role: USER_ROLE_ADMIN,
      status: USER_STATUS_DISABLED,
      sortBy: 'username',
      sortOrder: 'asc',
      page: 1,
      pageSize: 20,
    });
    expect(mockedAPI.get).toHaveBeenLastCalledWith('/user/search', expect.objectContaining({
      params: {
        p: 1,
        page_size: 20,
        sort_by: 'username',
        sort_order: 'asc',
        keyword: 'alice',
        group: 'vip',
        role: 10,
        status: 2,
      },
    }));
  });

  it('loads a bounded, normalized group catalog', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: { success: true, data: ['vip', 'default', 'vip', ' '] } });
    await expect(loadUserGroups()).resolves.toEqual(['default', 'vip']);
    expect(mockedAPI.get).toHaveBeenCalledWith('/group/', expect.objectContaining({ maxContentLength: 524_288 }));
  });

  it('sends only allowlisted create/edit fields and never returns or reuses credentials', async () => {
    mockedAPI.post.mockResolvedValueOnce(mutationSuccess());
    await createUser({ username: ' alice ', displayName: '', password: 'password8', role: USER_ROLE_COMMON });
    expect(mockedAPI.post).toHaveBeenCalledWith('/user/', {
      username: 'alice',
      display_name: 'alice',
      password: 'password8',
      role: 1,
    }, expect.objectContaining({ maxContentLength: 524_288 }));

    mockedAPI.put.mockResolvedValueOnce(mutationSuccess());
    await updateUser({ id: 7, displayName: ' Alice Admin ', group: ' vip ', remark: '', password: 'replacement8' });
    expect(mockedAPI.put).toHaveBeenCalledWith('/user/', {
      id: 7,
      display_name: 'Alice Admin',
      group: 'vip',
      remark: '',
      password: 'replacement8',
    }, expect.any(Object));
    const editBody = mockedAPI.put.mock.calls[0][1] as Record<string, unknown>;
    expect(editBody).not.toHaveProperty('role');
    expect(editBody).not.toHaveProperty('quota');
    expect(editBody).not.toHaveProperty('admin_permissions');
  });

  it('provisions a bounded permission matrix only for a new administrator', async () => {
    mockedAPI.post.mockResolvedValueOnce(mutationSuccess());
    await createUser({
      username: 'new-admin',
      displayName: 'New Admin',
      password: 'password8',
      role: USER_ROLE_ADMIN,
      adminPermissions: { channel: { read: true, sensitive_write: false } },
    });
    expect(mockedAPI.post).toHaveBeenCalledWith('/user/', {
      username: 'new-admin',
      display_name: 'New Admin',
      password: 'password8',
      role: USER_ROLE_ADMIN,
      admin_permissions: { channel: { read: true, sensitive_write: false } },
    }, expect.any(Object));
    await expect(createUser({
      username: 'plain-user',
      displayName: 'Plain User',
      password: 'password8',
      role: USER_ROLE_COMMON,
      adminPermissions: { channel: { read: true } },
    })).rejects.toBeInstanceOf(UserContractError);
  });

  it('uses exact management, quota, deletion, and factor-reset contracts', async () => {
    mockedAPI.post.mockResolvedValue(mutationSuccess());
    mockedAPI.delete.mockResolvedValue(mutationSuccess());
    await manageUser(7, 'disable');
    await adjustUserQuota(7, 'subtract', 25);
    await deleteUser(7);
    await resetUserPasskey(7);
    await resetUserTwoFactor(7);

    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/user/manage', { id: 7, action: 'disable' }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/user/manage', {
      id: 7,
      action: 'add_quota',
      mode: 'subtract',
      value: 25,
    }, expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(1, '/user/7', expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(2, '/user/7/reset_passkey', expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(3, '/user/7/2fa', expect.any(Object));
  });

  it('uses exact permission and account-binding routes with allowlisted bodies', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: permissionCatalogResponse })
      .mockResolvedValueOnce({ data: userDetailResponse })
      .mockResolvedValueOnce({ data: userDetailResponse })
      .mockResolvedValueOnce({ data: {
        success: true,
        data: [{
          provider_id: 42,
          provider_name: 'Corporate SSO',
          provider_slug: 'corp-sso',
          provider_icon: '',
          provider_user_id: 'subject-never-returned',
        }],
      } });
    mockedAPI.put.mockResolvedValueOnce(mutationSuccess());
    mockedAPI.delete.mockResolvedValue(mutationSuccess());

    const catalog = await loadPermissionCatalog();
    await expect(loadUserPermissionState(7, catalog)).resolves.toEqual(expect.objectContaining({ id: 7 }));
    await updateUserPermissions(7, catalog, { channel: { read: false, sensitive_write: true } });
    await expect(loadUserBindingDetails(7)).resolves.toEqual(expect.objectContaining({ id: 7 }));
    await expect(loadCustomOAuthBindings(7)).resolves.toEqual([{
      providerId: 42,
      providerName: 'Corporate SSO',
      providerSlug: 'corp-sso',
    }]);
    await clearBuiltInUserBinding(7, 'github');
    await unbindCustomOAuth(7, 42);

    expect(mockedAPI.get).toHaveBeenNthCalledWith(1, '/authz/catalog', expect.objectContaining({
      maxContentLength: 524_288,
    }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(2, '/user/7', expect.any(Object));
    expect(mockedAPI.put).toHaveBeenCalledWith('/user/', {
      id: 7,
      admin_permissions: { channel: { read: false, sensitive_write: true } },
    }, expect.objectContaining({ maxBodyLength: 524_288 }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(3, '/user/7', expect.any(Object));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(4, '/user/7/oauth/bindings', expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(1, '/user/7/bindings/github', expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(2, '/user/7/oauth/bindings/42', expect.any(Object));
  });

  it('rejects invalid local inputs before transport', async () => {
    await expect(createUser({ username: 'alice', displayName: '', password: 'short', role: USER_ROLE_COMMON })).rejects.toThrow(UserContractError);
    await expect(createUser({
      username: 'alice', displayName: '', password: 'password8', role: 100 as unknown as typeof USER_ROLE_COMMON,
    })).rejects.toThrow(UserContractError);
    await expect(adjustUserQuota(7, 'add', 0)).rejects.toThrow(UserContractError);
    await expect(searchUsers({
      keyword: 'x'.repeat(129),
      group: '',
      role: '',
      status: '',
      sortBy: 'id',
      sortOrder: 'desc',
      page: 1,
      pageSize: 20,
    })).rejects.toThrow(UserContractError);
    const catalog = parsePermissionCatalogResponse(permissionCatalogResponse);
    await expect(updateUserPermissions(7, catalog, { channel: { read: true } })).rejects.toThrow(UserContractError);
    await expect(clearBuiltInUserBinding(7, 'password' as never)).rejects.toThrow(UserContractError);
    await expect(unbindCustomOAuth(7, 0)).rejects.toThrow(UserContractError);
    expect(mockedAPI.post).not.toHaveBeenCalled();
    expect(mockedAPI.get).not.toHaveBeenCalled();
  });
});
