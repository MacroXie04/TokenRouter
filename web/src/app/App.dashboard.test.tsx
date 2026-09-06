// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getData } from '../shared/api/client';
import App from './App';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en', changeLanguage: vi.fn() },
    }),
  };
});

vi.mock('../shared/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../shared/api/client')>();
  return { ...actual, getData: vi.fn() };
});

vi.mock('../features/dashboard', () => ({
  DashboardView: ({
    section,
    role,
    search,
  }: {
    section: string;
    role: number;
    search: string;
  }) => (
    <main aria-label="dedicated dashboard">
      <span>section {section}</span>
      <span>role {role}</span>
      <span>search {search}</span>
    </main>
  ),
}));

const mockedGetData = vi.mocked(getData);

function bootAs(role: number) {
  mockedGetData.mockImplementation(async (path: string) => {
    if (path === '/setup') return { setup_required: false };
    if (path === '/status') return {};
    if (path === '/user/self') {
      return {
        id: role >= 10 ? 1 : 2,
        username: role >= 10 ? 'operator' : 'alice',
        display_name: 'User',
        role,
        group: 'default',
        quota: 0,
        used_quota: 0,
        request_count: 0,
      };
    }
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('/dashboard integration', () => {
  it('routes an ordinary user to the dedicated overview with self scope', async () => {
    window.history.replaceState(null, '', '/dashboard/overview?start_timestamp=1700000000');
    bootAs(1);
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated dashboard' })).toHaveProperty(
      'textContent',
      'section overviewrole 1search ?start_timestamp=1700000000',
    );
  });

  it('routes an administrator model view to the same dedicated feature', async () => {
    window.history.replaceState(null, '', '/dashboard/models');
    bootAs(10);
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated dashboard' })).toHaveProperty(
      'textContent',
      'section modelsrole 10search ',
    );
  });
});
