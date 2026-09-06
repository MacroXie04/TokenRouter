import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  TOKEN_DISABLED,
  TokenContractError,
  createToken,
  deleteToken,
  deleteTokens,
  listTokens,
  loadTokenAutoGroups,
  loadTokenGroups,
  loadTokenModels,
  parseAutoGroupsResponse,
  parseGroupsResponse,
  parseModelsResponse,
  parseTokenPageResponse,
  searchTokens,
  updateToken,
  updateTokenStatus,
  type TokenWriteInput,
} from './token-api';

vi.mock('../../shared/api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const mockedAPI = vi.mocked(api);

function token(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    user_id: 99,
    name: 'Primary key',
    key: 'sk-a**********1234',
    status: 1,
    remain_quota: 5_000,
    used_quota: 100,
    unlimited_quota: false,
    expired_time: -1,
    created_time: 1_700_000_000,
    accessed_time: 1_700_000_100,
    group: 'auto',
    auto_groups: ['vip', 'default'],
    cross_group_retry: true,
    model_limits_enabled: true,
    model_limits: 'gpt-4o,o3-mini',
    allow_ips: '192.0.2.1,2001:db8::/32',
    ...overrides,
  };
}

function pageEnvelope(items: unknown[] = [token()]) {
  return { success: true, data: { items, total: items.length, page: 1, page_size: 20 } };
}

const writeInput: TokenWriteInput = {
  name: ' Relay key ',
  expiredTime: -1,
  remainQuota: 5_000,
  unlimitedQuota: false,
  modelLimitsEnabled: true,
  modelLimits: ['gpt-4o', 'o3-mini'],
  allowIps: '192.0.2.1,2001:db8::/32',
  group: 'auto',
  autoGroups: ['vip', 'default'],
  crossGroupRetry: true,
};

beforeEach(() => vi.resetAllMocks());

describe('relay-token response contracts', () => {
  it('parses a bounded page into a secret-safe UI model', () => {
    expect(parseTokenPageResponse(pageEnvelope())).toEqual({
      items: [{
        id: 7,
        name: 'Primary key',
        maskedKey: 'sk-a**********1234',
        status: 1,
        remainQuota: 5_000,
        usedQuota: 100,
        unlimitedQuota: false,
        expiredTime: -1,
        createdTime: 1_700_000_000,
        accessedTime: 1_700_000_100,
        group: 'auto',
        autoGroups: ['vip', 'default'],
        crossGroupRetry: true,
        modelLimitsEnabled: true,
        modelLimits: ['gpt-4o', 'o3-mini'],
        allowIps: '192.0.2.1,2001:db8::/32',
      }],
      total: 1,
      page: 1,
      pageSize: 20,
    });
  });

  it('rejects unmasked credentials, duplicates, oversized pages, and inconsistent quotas', () => {
    expect(() => parseTokenPageResponse(pageEnvelope([token({ key: 'sk-full-secret-value' })]))).toThrow(TokenContractError);
    expect(() => parseTokenPageResponse(pageEnvelope([token({ auto_groups: ['vip', 'vip'] })]))).toThrow(TokenContractError);
    expect(() => parseTokenPageResponse(pageEnvelope([token({ unlimited_quota: false, remain_quota: -1 })]))).toThrow(TokenContractError);
    expect(() => parseTokenPageResponse(pageEnvelope([token({ remain_quota: 2_147_483_648 })]))).toThrow(TokenContractError);
    expect(() => parseTokenPageResponse(pageEnvelope([token({ used_quota: 2_147_483_648 })]))).toThrow(TokenContractError);
    expect(() => parseTokenPageResponse({ success: true, data: { items: Array.from({ length: 101 }, () => token()), total: 101, page: 1, page_size: 100 } })).toThrow(TokenContractError);
  });

  it('strictly parses group, auto-group, and model catalogs', () => {
    expect(parseGroupsResponse({ success: true, data: {
      vip: { ratio: 2, desc: 'VIP' },
      auto: { ratio: '自动', desc: 'Automatic' },
    } })).toEqual([
      { name: 'auto', ratio: '自动', description: 'Automatic' },
      { name: 'vip', ratio: '2', description: 'VIP' },
    ]);
    expect(parseAutoGroupsResponse({ success: true, data: { groups: ['vip', 'default'], max_count: 2 } })).toEqual({ groups: ['vip', 'default'], maxCount: 2 });
    expect(parseModelsResponse({ success: true, data: ['o3-mini', 'gpt-4o'] })).toEqual(['gpt-4o', 'o3-mini']);
    expect(() => parseModelsResponse({ success: true, data: ['gpt-4o', 'gpt-4o'] })).toThrow(TokenContractError);
    expect(() => parseGroupsResponse({ success: true, data: { vip: { ratio: {}, desc: 'VIP' } } })).toThrow(TokenContractError);
  });
});

describe('relay-token API routes', () => {
  it('rejects writes outside the persisted quota domain', async () => {
    await expect(createToken({ ...writeInput, remainQuota: 2_147_483_648 })).rejects.toThrow(TokenContractError);
    expect(mockedAPI.post).not.toHaveBeenCalled();
  });

  it('uses exact bounded list/search and metadata routes without credential-query search', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: pageEnvelope() })
      .mockResolvedValueOnce({ data: pageEnvelope() })
      .mockResolvedValueOnce({ data: { success: true, data: { default: { ratio: 1, desc: 'Default' } } } })
      .mockResolvedValueOnce({ data: { success: true, data: { groups: ['default'], max_count: 1 } } })
      .mockResolvedValueOnce({ data: { success: true, data: ['gpt-4o'] } });

    await listTokens(2);
    await searchTokens(' Primary ', 3);
    await loadTokenGroups();
    await loadTokenAutoGroups();
    await loadTokenModels('default');

    expect(mockedAPI.get).toHaveBeenNthCalledWith(1, '/token/', expect.objectContaining({ params: { p: 2, page_size: 20 }, maxContentLength: 524_288 }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(2, '/token/search', expect.objectContaining({ params: { keyword: 'Primary', p: 3, page_size: 20 } }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(3, '/user/self/groups', expect.objectContaining({ maxContentLength: 524_288 }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(4, '/token/auto-groups', expect.any(Object));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(5, '/user/models', expect.objectContaining({ params: { group: 'default' } }));
    expect(mockedAPI.get.mock.calls[1][1]?.params).not.toHaveProperty('token');
    await expect(searchTokens('unsafe%', 1)).rejects.toThrow(TokenContractError);
  });

  it('sends the complete policy on create/edit and the isolated status-only contract', async () => {
    mockedAPI.post.mockResolvedValue({ data: { success: true, message: '' } });
    mockedAPI.put.mockResolvedValue({ data: { success: true, data: token() } });

    await createToken(writeInput);
    await updateToken(7, writeInput);
    await updateTokenStatus(7, TOKEN_DISABLED);

    const normalized = {
      name: 'Relay key',
      expired_time: -1,
      remain_quota: 5_000,
      unlimited_quota: false,
      model_limits_enabled: true,
      model_limits: 'gpt-4o,o3-mini',
      allow_ips: '192.0.2.1,2001:db8::/32',
      cross_group_retry: true,
      group: 'auto',
      auto_groups: ['vip', 'default'],
    };
    expect(mockedAPI.post).toHaveBeenCalledWith('/token/', normalized, expect.objectContaining({ maxContentLength: 524_288 }));
    expect(mockedAPI.put).toHaveBeenNthCalledWith(1, '/token/', { id: 7, ...normalized }, expect.any(Object));
    expect(mockedAPI.put).toHaveBeenNthCalledWith(2, '/token/', { id: 7, status: 2 }, expect.objectContaining({ params: { status_only: 1 } }));
    expect(mockedAPI.post.mock.calls[0][1]).not.toHaveProperty('key');
  });

  it('uses ownership-scoped single and bounded batch deletion routes', async () => {
    mockedAPI.delete.mockResolvedValueOnce({ data: { success: true } });
    mockedAPI.post.mockResolvedValueOnce({ data: { success: true, data: 2 } });

    await deleteToken(7);
    expect(await deleteTokens([7, 8])).toBe(2);

    expect(mockedAPI.delete).toHaveBeenCalledWith('/token/7', expect.objectContaining({ maxContentLength: 524_288 }));
    expect(mockedAPI.post).toHaveBeenCalledWith('/token/batch', { ids: [7, 8] }, expect.any(Object));
    await expect(deleteTokens([7, 7])).rejects.toThrow(TokenContractError);
    await expect(deleteTokens([])).rejects.toThrow(TokenContractError);
  });
});
