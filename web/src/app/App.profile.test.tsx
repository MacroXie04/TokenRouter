// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getData } from '../shared/api/client';
import App from './App';

const i18nMocks = vi.hoisted(() => ({ changeLanguage: vi.fn() }));

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en', changeLanguage: i18nMocks.changeLanguage },
    }),
  };
});

vi.mock('../shared/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../shared/api/client')>();
  return { ...actual, getData: vi.fn() };
});

vi.mock('../features/profile/ProfileView', () => ({
  ProfileView: ({
    onNavigate,
    onProfileChange,
  }: {
    onNavigate: (target: string) => void;
    onProfileChange: (profile: Record<string, unknown>) => void;
  }) => (
    <main aria-label="dedicated profile">
      <button type="button" onClick={() => onProfileChange({
        id: 9,
        username: 'alice',
        displayName: 'Alice Updated',
        email: 'alice@example.test',
        role: 10,
        status: 1,
        group: 'premium',
        quota: 900,
        usedQuota: 100,
        requestCount: 7,
        createdAt: 1_700_000_000,
        language: 'ja',
        sidebarModules: {
          chat: { enabled: true, playground: true, chat: true },
          console: {
            enabled: true,
            detail: true,
            token: true,
            log: true,
            midjourney: true,
            task: true,
          },
          personal: { enabled: true, topup: false, personal: true },
        },
      })}>Update profile shell</button>
      <button type="button" onClick={() => onNavigate('/dashboard/overview')}>Dashboard</button>
    </main>
  ),
}));

vi.mock('../features/dashboard', () => ({
  DashboardView: ({ role }: { role: number }) => <main aria-label="dedicated dashboard">role {role}</main>,
}));

vi.mock('../features/keys/ConsoleView', () => ({
  ConsoleView: () => <main aria-label="legacy console">Legacy console</main>,
}));

const mockedGetData = vi.mocked(getData);

beforeEach(() => {
  vi.resetAllMocks();
  i18nMocks.changeLanguage.mockResolvedValue(undefined);
  window.history.replaceState(null, '', '/profile');
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
  mockedGetData.mockImplementation(async (path: string) => {
    if (path === '/setup') return { setup_required: false };
    if (path === '/status') return {};
    if (path === '/user/self') {
      return {
        id: 9,
        username: 'alice',
        display_name: 'Alice',
        role: 1,
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

describe('/profile integration', () => {
  it('renders the dedicated profile and synchronizes its authoritative account update', async () => {
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated profile' })).toBeTruthy();
    expect(screen.queryByRole('main', { name: 'legacy console' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Update profile shell' }));
    expect(screen.queryByRole('link', { name: 'Wallet' })).toBeNull();
    expect(i18nMocks.changeLanguage).toHaveBeenCalledWith('ja');
    fireEvent.click(screen.getByRole('button', { name: 'Dashboard' }));

    expect(await screen.findByRole('main', { name: 'dedicated dashboard' })).toHaveProperty(
      'textContent',
      'role 10',
    );
  });
});
