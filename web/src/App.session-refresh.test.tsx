// @vitest-environment jsdom

import type { AxiosAdapter, InternalAxiosRequestConfig } from 'axios';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import { api } from './api';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en', changeLanguage: vi.fn(async () => undefined) },
    }),
  };
});

vi.mock('./features/dashboard', () => ({
  DashboardView: ({ section }: { section: string }) => (
    <main aria-label="refreshed dashboard">Recovered {section}</main>
  ),
}));

const originalAdapter = api.defaults.adapter;

function accepted(config: InternalAxiosRequestConfig, data: unknown) {
  return Promise.resolve({
    config,
    data: { success: true, data },
    headers: {},
    status: 200,
    statusText: 'OK',
  });
}

function rejected(config: InternalAxiosRequestConfig, status: number) {
  return Promise.reject({
    name: 'AxiosError',
    message: `request failed with ${status}`,
    isAxiosError: true,
    config,
    response: {
      config,
      data: { success: false },
      headers: {},
      status,
      statusText: 'Error',
    },
    toJSON: () => ({}),
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  window.history.replaceState(null, '', '/dashboard/overview');
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
});

afterEach(async () => {
  cleanup();
  api.defaults.adapter = originalAdapter;
  window.history.replaceState(null, '', '/');
  await Promise.resolve();
});

describe('application session bootstrap', () => {
  it('refreshes an expired access cookie before deciding the user is signed out', async () => {
    const calls: string[] = [];
    let selfAttempts = 0;
    const adapter: AxiosAdapter = (config) => {
      const url = config.url ?? '';
      calls.push(url);
      if (url === '/setup') return accepted(config, { setup_required: false });
      if (url === '/status') return accepted(config, {});
      if (url === '/user/auth/refresh') return accepted(config, { id: 7 });
      if (url === '/user/self') {
        selfAttempts += 1;
        if (selfAttempts === 1) return rejected(config, 401);
        return accepted(config, {
          id: 7,
          username: 'alice',
          display_name: 'Alice',
          role: 1,
          group: 'default',
          quota: 0,
          used_quota: 0,
          request_count: 0,
        });
      }
      throw new Error(`unexpected request ${url}`);
    };
    api.defaults.adapter = adapter;

    render(<App />);

    expect(await screen.findByRole('main', { name: 'refreshed dashboard' })).toBeTruthy();
    expect(window.location.pathname).toBe('/dashboard/overview');
    expect(calls).toEqual([
      '/setup',
      '/status',
      '/user/self',
      '/user/auth/refresh',
      '/user/self',
    ]);
  });

  it('shows a service failure instead of signing out on a transient bootstrap refresh error', async () => {
    const calls: string[] = [];
    api.defaults.adapter = ((config) => {
      const url = config.url ?? '';
      calls.push(url);
      if (url === '/setup') return accepted(config, { setup_required: false });
      if (url === '/status') return accepted(config, {});
      if (url === '/user/self') return rejected(config, 401);
      if (url === '/user/auth/refresh') return rejected(config, 503);
      throw new Error(`unexpected request ${url}`);
    }) satisfies AxiosAdapter;

    render(<App />);

    expect(await screen.findByRole('heading', { name: 'Service unavailable' })).toBeTruthy();
    expect(window.location.pathname).toBe('/dashboard/overview');
    expect(calls).toEqual(['/setup', '/status', '/user/self', '/user/auth/refresh']);
  });
});
