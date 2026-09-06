// @vitest-environment jsdom

import type { AxiosAdapter, InternalAxiosRequestConfig } from 'axios';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  api,
  isTransientSessionRefreshFailure,
  SESSION_EXPIRED_EVENT,
  withoutSessionRefresh,
} from './client';

const originalAdapter = api.defaults.adapter;

afterEach(async () => {
  api.defaults.adapter = originalAdapter;
  vi.restoreAllMocks();
  await Promise.resolve();
});

function accepted(config: InternalAxiosRequestConfig, data: unknown = { success: true, data: {} }) {
  return Promise.resolve({
    config,
    data,
    headers: {},
    status: 200,
    statusText: 'OK',
  });
}

function rejected(config: InternalAxiosRequestConfig, status: number, data: unknown = undefined) {
  return Promise.reject({
    name: 'AxiosError',
    message: `request failed with ${status}`,
    isAxiosError: true,
    config,
    response: {
      config,
      data,
      headers: {},
      status,
      statusText: 'Error',
    },
    toJSON: () => ({}),
  });
}

function installAdapter(adapter: AxiosAdapter): void {
  api.defaults.adapter = adapter;
}

describe('authenticated API session refresh', () => {
  it('shares one credentialed refresh and replays each waiting request once', async () => {
    let finishRefresh: (() => void) | undefined;
    const refreshGate = new Promise<void>((resolve) => { finishRefresh = resolve; });
    const attempts = new Map<string, number>();
    const requestConfigs = new Map<string, InternalAxiosRequestConfig[]>();
    const refreshRequests: InternalAxiosRequestConfig[] = [];

    installAdapter(async (config) => {
      const url = config.url ?? '';
      if (url === '/user/auth/refresh') {
        refreshRequests.push(config);
        await refreshGate;
        return accepted(config);
      }
      const configs = requestConfigs.get(url) ?? [];
      configs.push(config);
      requestConfigs.set(url, configs);
      const attempt = (attempts.get(url) ?? 0) + 1;
      attempts.set(url, attempt);
      return attempt === 1
        ? rejected(config, 401)
        : accepted(config, { success: true, data: url });
    });

    const controller = new AbortController();
    const first = api.get('/one', { signal: controller.signal });
    const second = api.get('/two');
    await vi.waitFor(() => expect(refreshRequests).toHaveLength(1));
    finishRefresh?.();

    await expect(Promise.all([first, second])).resolves.toMatchObject([
      { data: { success: true, data: '/one' } },
      { data: { success: true, data: '/two' } },
    ]);
    expect(attempts).toEqual(new Map([['/one', 2], ['/two', 2]]));
    expect(requestConfigs.get('/one')?.[1]?.signal).toBe(controller.signal);
    expect(refreshRequests[0]).toMatchObject({
      baseURL: '/api',
      method: 'post',
      url: '/user/auth/refresh',
      withCredentials: true,
    });
  });

  it('replays a late stale 401 without rotating the cookie a second time', async () => {
    let releaseLateResponse: (() => void) | undefined;
    const lateResponse = new Promise<void>((resolve) => { releaseLateResponse = resolve; });
    let refreshCalls = 0;
    let fastAttempts = 0;
    let slowAttempts = 0;

    installAdapter(async (config) => {
      if (config.url === '/user/auth/refresh') {
        refreshCalls += 1;
        return accepted(config);
      }
      if (config.url === '/fast') {
        fastAttempts += 1;
        return fastAttempts === 1 ? rejected(config, 401) : accepted(config);
      }
      if (config.url === '/slow') {
        slowAttempts += 1;
        if (slowAttempts === 1) {
          await lateResponse;
          return rejected(config, 401);
        }
        return accepted(config);
      }
      throw new Error(`unexpected request ${config.url ?? ''}`);
    });

    const slow = api.get('/slow');
    await expect(api.get('/fast')).resolves.toMatchObject({ status: 200 });
    expect(refreshCalls).toBe(1);
    releaseLateResponse?.();

    await expect(slow).resolves.toMatchObject({ status: 200 });
    expect(slowAttempts).toBe(2);
    expect(refreshCalls).toBe(1);
  });

  it('does not recurse and invalidates identity only after a terminal refresh rejection', async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    let protectedCalls = 0;
    let refreshCalls = 0;

    installAdapter((config) => {
      if (config.url === '/user/auth/refresh') {
        refreshCalls += 1;
        return rejected(config, 401, { success: false });
      }
      protectedCalls += 1;
      return rejected(config, 401);
    });

    await expect(api.get('/protected')).rejects.toMatchObject({ response: { status: 401 } });
    expect(protectedCalls).toBe(1);
    expect(refreshCalls).toBe(1);
    expect(expired).toHaveBeenCalledOnce();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  it('preserves identity after service failure or an uncoded refresh race', async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    let protectedCalls = 0;
    let refreshStatus = 503;

    installAdapter((config) => {
      if (config.url === '/user/auth/refresh') {
        return rejected(config, refreshStatus, { success: false });
      }
      protectedCalls += 1;
      return rejected(config, 401);
    });

    const unavailable = await api.get('/temporarily-unavailable').catch((error: unknown) => error);
    expect(unavailable).toMatchObject({ response: { status: 401 } });
    expect(isTransientSessionRefreshFailure(unavailable)).toBe(true);

    refreshStatus = 409;
    const raced = await api.get('/refresh-race').catch((error: unknown) => error);
    expect(raced).toMatchObject({ response: { status: 401 } });
    expect(isTransientSessionRefreshFailure(raced)).toBe(true);
    expect(protectedCalls).toBe(2);
    expect(expired).not.toHaveBeenCalled();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  it('replays a request at most once and expires a still-rejected session', async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    let protectedCalls = 0;
    let refreshCalls = 0;

    installAdapter((config) => {
      if (config.url === '/user/auth/refresh') {
        refreshCalls += 1;
        return accepted(config);
      }
      protectedCalls += 1;
      return rejected(config, 401);
    });

    await expect(api.get('/still-rejected')).rejects.toMatchObject({ response: { status: 401 } });
    expect(protectedCalls).toBe(2);
    expect(refreshCalls).toBe(1);
    expect(expired).toHaveBeenCalledOnce();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  it('keeps caller aborts local while another request completes the shared refresh', async () => {
    let finishRefresh: (() => void) | undefined;
    const refreshGate = new Promise<void>((resolve) => { finishRefresh = resolve; });
    const attempts = new Map<string, number>();
    let refreshCalls = 0;

    installAdapter(async (config) => {
      const url = config.url ?? '';
      if (url === '/user/auth/refresh') {
        refreshCalls += 1;
        await refreshGate;
        return accepted(config);
      }
      const attempt = (attempts.get(url) ?? 0) + 1;
      attempts.set(url, attempt);
      return attempt === 1 ? rejected(config, 401) : accepted(config);
    });

    const controller = new AbortController();
    const canceled = api.get('/canceled', { signal: controller.signal });
    const survivor = api.get('/survivor');
    await vi.waitFor(() => expect(refreshCalls).toBe(1));
    controller.abort();
    const canceledExpectation = expect(canceled).rejects.toMatchObject({ code: 'ERR_CANCELED' });
    finishRefresh?.();

    await canceledExpectation;
    await expect(survivor).resolves.toMatchObject({ status: 200 });
    expect(attempts.get('/canceled')).toBe(1);
    expect(attempts.get('/survivor')).toBe(2);
    expect(refreshCalls).toBe(1);
  });

  it('does not refresh or expire identity for explicitly anonymous requests', async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    const calls: string[] = [];
    installAdapter((config) => {
      calls.push(config.url ?? '');
      return rejected(config, 401);
    });

    await expect(api.post('/user/login', {}, withoutSessionRefresh())).rejects.toMatchObject({
      response: { status: 401 },
    });
    expect(calls).toEqual(['/user/login']);
    expect(expired).not.toHaveBeenCalled();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });
});
