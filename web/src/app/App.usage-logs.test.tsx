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

vi.mock('../features/usage-logs/UsageLogsView', () => ({
  UsageLogsView: ({ section, user }: { section: string; user: { username: string; role: number } }) => (
    <main aria-label="dedicated usage logs">
      <span>section {section}</span>
      <span>user {user.username}</span>
      <span>role {user.role}</span>
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

describe('/usage-logs integration', () => {
  it('routes an administrator task section directly to the feature-scoped screen', async () => {
    window.history.replaceState(null, '', '/usage-logs/task');
    bootAs(10);
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated usage logs' })).toHaveProperty(
      'textContent',
      'section taskuser operatorrole 10',
    );
  });

  it('routes an ordinary user drawing section to the same scoped feature with self access', async () => {
    window.history.replaceState(null, '', '/usage-logs/drawing');
    bootAs(1);
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated usage logs' })).toHaveProperty(
      'textContent',
      'section drawinguser alicerole 1',
    );
  });
});
