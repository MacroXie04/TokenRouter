import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import {
  REDEMPTION_STATUS_DISABLED,
  RedemptionContractError,
  createRedemptions,
  deleteInvalidRedemptions,
  deleteRedemption,
  getRedemptionCode,
  getRedemptionForEdit,
  listRedemptions,
  parseCreatedCodesResponse,
  parseRedemptionPageResponse,
  searchRedemptions,
  updateRedemption,
  updateRedemptionStatus,
} from './redemption-api';

vi.mock('../../api', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const mockedAPI = vi.mocked(api);
const secretCode = '0123456789abcdef0123456789abcdef';

function redemption(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    user_id: 2,
    name: 'launch grant',
    key: secretCode,
    status: 1,
    quota: 500,
    created_time: 1_780_000_000,
    redeemed_time: 0,
    expired_time: 0,
    used_user_id: 0,
    ...overrides,
  };
}

function pageResponse(overrides: Record<string, unknown> = {}) {
  return {
    success: true,
    message: '',
    data: { items: [redemption()], total: 21, page: 1, page_size: 20, ...overrides },
  };
}

function itemResponse(overrides: Record<string, unknown> = {}) {
  return { success: true, message: '', data: redemption(overrides) };
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('redemption response contracts', () => {
  it('accepts the exact page envelope while immediately masking list secrets', () => {
    const page = parseRedemptionPageResponse(pageResponse());
    expect(page).toEqual({
      items: [{
        id: 7,
        userId: 2,
        name: 'launch grant',
        maskedCode: '0123••••••••cdef',
        status: 1,
        quota: 500,
        createdTime: 1_780_000_000,
        redeemedTime: 0,
        expiredTime: 0,
        usedUserId: 0,
      }],
      total: 21,
      page: 1,
      pageSize: 20,
    });
    expect(JSON.stringify(page)).not.toContain(secretCode);
  });

  it('fails closed on unexpected credential fields, malformed codes, oversized payloads, and collections', () => {
    expect(() => parseRedemptionPageResponse(pageResponse({
      items: [redemption({ access_token: 'private-token' })],
    }))).toThrow(RedemptionContractError);
    expect(() => parseRedemptionPageResponse(pageResponse({
      items: [redemption({ key: 'not-a-server-code' })],
    }))).toThrow(RedemptionContractError);
    expect(() => parseRedemptionPageResponse(pageResponse({
      items: Array.from({ length: 101 }, () => redemption()),
      page_size: 100,
    }))).toThrow(RedemptionContractError);
    expect(() => parseRedemptionPageResponse({
      ...pageResponse(),
      message: 'x'.repeat(524_289),
    })).toThrow(RedemptionContractError);
    expect(() => parseCreatedCodesResponse({ success: true, data: ['abcd'] })).toThrow(RedemptionContractError);
    expect(() => parseCreatedCodesResponse({ success: true, data: [secretCode, secretCode] }))
      .toThrow(RedemptionContractError);
  });
});

describe('redemption API routes', () => {
  it('uses exact paginated list and filtered search contracts', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: pageResponse() });
    await listRedemptions(2, 20);
    expect(mockedAPI.get).toHaveBeenCalledWith('/redemption/', expect.objectContaining({
      params: { p: 2, page_size: 20 },
      timeout: 15_000,
      maxContentLength: 524_288,
      maxBodyLength: 524_288,
    }));

    mockedAPI.get.mockResolvedValueOnce({ data: pageResponse() });
    await searchRedemptions({
      keyword: ' launch ',
      status: 'expired',
      page: 3,
      pageSize: 20,
    });
    expect(mockedAPI.get).toHaveBeenLastCalledWith('/redemption/search', expect.objectContaining({
      params: { keyword: 'launch', status: 'expired', p: 3, page_size: 20 },
    }));
  });

  it('loads a current row and retrieves the full code only through an explicit single-item request', async () => {
    mockedAPI.get.mockResolvedValue({ data: itemResponse() });
    const safe = await getRedemptionForEdit(7);
    expect(safe.maskedCode).toBe('0123••••••••cdef');
    expect(JSON.stringify(safe)).not.toContain(secretCode);
    await expect(getRedemptionCode(7)).resolves.toBe(secretCode);
    expect(mockedAPI.get).toHaveBeenNthCalledWith(1, '/redemption/7', expect.any(Object));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(2, '/redemption/7', expect.any(Object));
  });

  it('sends allowlisted create, edit, and status-only payloads', async () => {
    mockedAPI.post.mockResolvedValueOnce({ data: { success: true, message: '', data: [secretCode] } });
    await createRedemptions({ name: ' launch ', quota: 500, expiredTime: 0, count: 1 });
    expect(mockedAPI.post).toHaveBeenCalledWith('/redemption/', {
      name: 'launch',
      quota: 500,
      expired_time: 0,
      count: 1,
    }, expect.any(Object));

    mockedAPI.put.mockResolvedValueOnce({ data: itemResponse({ name: 'renewed', quota: 750 }) });
    await updateRedemption(7, { name: ' renewed ', quota: 750, expiredTime: 0 });
    expect(mockedAPI.put).toHaveBeenNthCalledWith(1, '/redemption/', {
      id: 7,
      name: 'renewed',
      quota: 750,
      expired_time: 0,
    }, expect.any(Object));
    expect(mockedAPI.put.mock.calls[0][1]).not.toHaveProperty('key');

    mockedAPI.put.mockResolvedValueOnce({ data: itemResponse({ status: REDEMPTION_STATUS_DISABLED }) });
    await updateRedemptionStatus(7, REDEMPTION_STATUS_DISABLED);
    expect(mockedAPI.put).toHaveBeenNthCalledWith(2, '/redemption/', {
      id: 7,
      status: REDEMPTION_STATUS_DISABLED,
    }, expect.objectContaining({ params: { status_only: 'true' } }));
  });

  it('uses the precise destructive routes and validates the deleted-invalid count', async () => {
    mockedAPI.delete
      .mockResolvedValueOnce({ data: { success: true, message: '' } })
      .mockResolvedValueOnce({ data: { success: true, message: '', data: 4 } });
    await deleteRedemption(7);
    await expect(deleteInvalidRedemptions()).resolves.toBe(4);
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(1, '/redemption/7', expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(2, '/redemption/invalid', expect.any(Object));
  });

  it('rejects invalid inputs and response mismatches before returning unsafe state', async () => {
    await expect(createRedemptions({ name: '', quota: 500, expiredTime: 0, count: 1 }))
      .rejects.toThrow(RedemptionContractError);
    await expect(createRedemptions({ name: 'valid', quota: 0, expiredTime: 0, count: 1 }))
      .rejects.toThrow(RedemptionContractError);
    await expect(searchRedemptions({
      keyword: 'x'.repeat(65), status: '', page: 1, pageSize: 20,
    })).rejects.toThrow(RedemptionContractError);
    expect(mockedAPI.post).not.toHaveBeenCalled();
    expect(mockedAPI.get).not.toHaveBeenCalled();

    mockedAPI.post.mockResolvedValueOnce({ data: { success: true, data: [secretCode, secretCode] } });
    await expect(createRedemptions({ name: 'valid', quota: 500, expiredTime: 0, count: 1 }))
      .rejects.toThrow(RedemptionContractError);
  });
});
