import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  AuthRequestFailedError,
  AuthResponseContractError,
  authGet,
  authPost,
  isCanceledAuthRequest,
} from './auth-api';

vi.mock('../../shared/api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
  },
  withoutSessionRefresh: vi.fn((config = {}) => ({
    ...config,
    __tokenRouterSessionRefresh: 'skip',
  })),
}));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);

afterEach(() => {
  mockedGet.mockReset();
  mockedPost.mockReset();
});

describe('auth API boundary', () => {
  it('uses the exact authentication paths, query contracts, and cancellation signal', async () => {
    const controller = new AbortController();
    mockedGet.mockResolvedValueOnce({ data: { success: true, data: { delivered: true } } });
    mockedPost.mockResolvedValueOnce({ data: { success: true, data: { id: 7 } } });

    await expect(authGet('/verification', {
      email: 'person@example.test', turnstile: 'proof',
    }, controller.signal)).resolves.toEqual({ delivered: true });
    expect(mockedGet).toHaveBeenCalledWith('/verification', {
      params: { email: 'person@example.test', turnstile: 'proof' },
      signal: controller.signal,
      __tokenRouterSessionRefresh: 'skip',
    });

    await expect(authPost('/user/login', {
      username: 'alice', password: 'password1',
    }, { turnstile: 'proof' }, controller.signal)).resolves.toEqual({ id: 7 });
    expect(mockedPost).toHaveBeenCalledWith('/user/login', {
      username: 'alice', password: 'password1',
    }, {
      params: { turnstile: 'proof' },
      signal: controller.signal,
      __tokenRouterSessionRefresh: 'skip',
    });
  });

  it('rejects failed, malformed, and oversized envelopes without exposing server messages', async () => {
    mockedGet
      .mockResolvedValueOnce({ data: { success: false, message: 'private trace' } })
      .mockResolvedValueOnce({ data: { success: 'yes', data: {} } })
      .mockResolvedValueOnce({ data: { success: true, data: 'x'.repeat(576 * 1024) } });

    await expect(authGet('/reset_password')).rejects.toBeInstanceOf(AuthRequestFailedError);
    await expect(authGet('/reset_password')).rejects.toBeInstanceOf(AuthResponseContractError);
    await expect(authGet('/reset_password')).rejects.toBeInstanceOf(AuthResponseContractError);
  });

  it('bounds outgoing bodies before transport', async () => {
    await expect(authPost('/user/reset', { token: 'x'.repeat(128 * 1024) }))
      .rejects.toBeInstanceOf(AuthResponseContractError);
    expect(mockedPost).not.toHaveBeenCalled();
  });

  it('recognizes local and Axios-style cancellation without classifying arbitrary failures', () => {
    const controller = new AbortController();
    controller.abort();
    expect(isCanceledAuthRequest(new Error('anything'), controller.signal)).toBe(true);
    expect(isCanceledAuthRequest({ name: 'CanceledError' })).toBe(true);
    expect(isCanceledAuthRequest({ code: 'ERR_CANCELED' })).toBe(true);
    expect(isCanceledAuthRequest(new Error('network failed'))).toBe(false);
  });
});
