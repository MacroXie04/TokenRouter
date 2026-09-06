// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
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

vi.mock('../features/playground', () => ({
  PlaygroundView: ({
    user,
    onNavigate,
  }: {
    user: { username: string; role: number };
    onNavigate: (target: string) => void;
  }) => (
    <main aria-label="dedicated playground">
      <span>{user.username} / role {user.role}</span>
      <button type="button" onClick={() => onNavigate('/dashboard/overview')}>Dashboard</button>
    </main>
  ),
}));

vi.mock('../features/dashboard', () => ({
  DashboardView: () => <main aria-label="dedicated dashboard">Dashboard</main>,
}));

vi.mock('../features/keys/ConsoleView', () => ({
  ConsoleView: () => <main aria-label="legacy console">Legacy console</main>,
}));

const mockedGetData = vi.mocked(getData);

beforeEach(() => {
  vi.resetAllMocks();
  window.history.replaceState(null, '', '/playground');
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

describe('/playground integration', () => {
  it('renders the dedicated authenticated feature and preserves app navigation', async () => {
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated playground' })).toHaveProperty(
      'textContent',
      'operator / role 10Dashboard',
    );
    expect(screen.queryByRole('main', { name: 'legacy console' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Dashboard' }));
    expect(await screen.findByRole('main', { name: 'dedicated dashboard' })).toBeTruthy();
  });
});
