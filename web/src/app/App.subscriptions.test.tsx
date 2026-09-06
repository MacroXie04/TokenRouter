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

vi.mock('../features/subscriptions', () => ({
  SubscriptionAdminView: ({ operatorRole }: { operatorRole: number }) => (
    <main aria-label="dedicated subscriptions">operator role {operatorRole}</main>
  ),
}));

vi.mock('../features/admin/AdminConsole', () => ({
  AdminConsole: () => <main aria-label="legacy admin console">Legacy subscriptions</main>,
}));

const mockedGetData = vi.mocked(getData);

beforeEach(() => {
  vi.resetAllMocks();
  window.history.replaceState(null, '', '/subscriptions');
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
  mockedGetData.mockImplementation(async (path: string) => {
    if (path === '/setup') return { setup_required: false };
    if (path === '/status') return {};
    if (path === '/user/self') {
      return {
        id: 7,
        username: 'operator',
        display_name: 'Operator',
        role: 10,
        group: 'default',
        quota: 0,
        used_quota: 0,
        request_count: 0,
      };
    }
    throw new Error(`unexpected ${path}`);
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('/subscriptions integration', () => {
  it('renders the dedicated bounded lifecycle view with the authenticated operator role', async () => {
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated subscriptions' }))
      .toHaveProperty('textContent', 'operator role 10');
    expect(screen.queryByRole('main', { name: 'legacy admin console' })).toBeNull();
  });
});
