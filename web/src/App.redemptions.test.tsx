// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getData, SESSION_EXPIRED_EVENT } from './api';
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

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>();
  return { ...actual, getData: vi.fn() };
});

vi.mock('./views/AdminConsole', () => ({
  AdminConsole: ({ initialTab, settingsPath, user }: { initialTab: string; settingsPath?: string; user: { role: number } }) => (
    <main aria-label="admin console">
      <h1>Admin route</h1>
      <span>{initialTab}</span>
      {settingsPath && <span>{settingsPath}</span>}
      <span>role {user.role}</span>
    </main>
  ),
}));

vi.mock('./views/SetupView', () => ({
  SetupView: ({ onDone, status }: { onDone: () => void; status: { databaseType: string } }) => (
    <main aria-label="setup test view">
      <span>{status.databaseType}</span>
      <button type="button" onClick={onDone}>Finish setup</button>
    </main>
  ),
}));

vi.mock('./features/system-info/SystemInfoView', () => ({
  SystemInfoView: () => <main aria-label="system information route">Dedicated system information</main>,
}));

vi.mock('./features/system-settings', () => ({
  SystemSettingsView: ({
    role,
    settingsPath,
  }: {
    role: number;
    settingsPath?: string;
  }) => (
    <main aria-label="system settings route">
      <span>{settingsPath}</span>
      <span>role {role}</span>
    </main>
  ),
}));

vi.mock('./features/wallet/WalletView', () => ({
  WalletView: ({
    user,
    turnstileConfig,
    initialShowHistory,
  }: {
    user: { username: string };
    turnstileConfig: { required: boolean; siteKey: string };
    initialShowHistory?: boolean;
  }) => (
    <main aria-label="wallet route">
      <span>Dedicated wallet for {user.username}</span>
      <span>verification {turnstileConfig.required ? turnstileConfig.siteKey : 'disabled'}</span>
      <span>history {initialShowHistory ? 'open' : 'closed'}</span>
    </main>
  ),
}));

vi.mock('./features/chat/ChatView', () => ({
  ChatView: ({ presetId, firstWeb }: { presetId?: string; firstWeb?: boolean }) => (
    <main aria-label="chat route">Chat preset {presetId ?? 'first-web'} {firstWeb ? 'compatibility' : 'direct'}</main>
  ),
}));

vi.mock('./views/ConsoleView', () => ({
  ConsoleView: ({ user }: { user: { username: string } }) => (
    <main aria-label="authenticated console">Protected console for {user.username}</main>
  ),
}));

const mockedGetData = vi.mocked(getData);

beforeEach(() => {
  vi.resetAllMocks();
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
  window.history.replaceState(null, '', '/redemption-codes');
  mockedGetData.mockImplementation(async (path: string) => {
    if (path === '/setup') return { setup_required: false };
    if (path === '/status') return { turnstile_check: true, turnstile_site_key: 'public-site-key' };
    if (path === '/user/self') {
      return {
        id: 2,
        username: 'operator',
        display_name: 'Operator',
        role: 10,
        group: 'default',
        quota: 0,
        used_quota: 0,
        request_count: 0,
      };
    }
    throw new Error(`unexpected test endpoint ${path}`);
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('/redemption-codes integration', () => {
  it('renders the root language control with every supported locale', async () => {
    render(<App />);
    expect(await screen.findByRole('main', { name: 'admin console' })).toBeTruthy();

    const language = screen.getByRole('combobox', { name: 'Language' });
    expect(language.querySelectorAll('option')).toHaveLength(7);
    await userEvent.setup().selectOptions(language, 'ja');
    expect(i18nMocks.changeLanguage).toHaveBeenCalledWith('ja');
  });

  it('boots the guarded admin route into the dedicated redemption tab', async () => {
    render(<App />);
    expect(await screen.findByRole('main', { name: 'admin console' })).toBeTruthy();
    expect(screen.getByText('redemptions')).toBeTruthy();
    expect(screen.getByText('role 10')).toBeTruthy();
    await waitFor(() => expect(mockedGetData).toHaveBeenCalledWith('/user/self'));
  });

  it('clears the setup guard before navigating away from a completed wizard', async () => {
    window.history.replaceState(null, '', '/setup');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') {
        return {
          setup_required: true,
          status: false,
          root_init: false,
          database_type: 'postgres',
        };
      }
      if (path === '/status') return { wechat_login: false, wechat_qrcode: '' };
      throw new Error(`unexpected test endpoint ${path}`);
    });

    render(<App />);
    expect(await screen.findByRole('main', { name: 'setup test view' })).toBeTruthy();
    expect(screen.getByText('postgres')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Finish setup' }));

    await waitFor(() => expect(window.location.pathname).toBe('/sign-in'));
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy();
    expect(screen.queryByRole('main', { name: 'setup test view' })).toBeNull();
  });

  it('boots the root-only system-information route into its dedicated feature view', async () => {
    window.history.replaceState(null, '', '/system-info');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') return { setup_required: false };
      if (path === '/status') return {};
      if (path === '/user/self') {
        return {
          id: 1,
          username: 'root',
          display_name: 'Root',
          role: 100,
          group: 'default',
          quota: 0,
          used_quota: 0,
          request_count: 0,
        };
      }
      throw new Error(`unexpected test endpoint ${path}`);
    });

    render(<App />);
    expect(await screen.findByRole('main', { name: 'system information route' })).toBeTruthy();
    expect(screen.queryByRole('main', { name: 'admin console' })).toBeNull();
  });

  it('boots the authenticated wallet route into its dedicated feature view', async () => {
    window.history.replaceState(null, '', '/wallet?show_history=true');
    render(<App />);

    expect(await screen.findByRole('main', { name: 'wallet route' })).toBeTruthy();
    expect(screen.getByText('Dedicated wallet for operator')).toBeTruthy();
    expect(screen.getByText(/verification public-site-key/)).toBeTruthy();
    expect(screen.getByText('history open')).toBeTruthy();
    expect(screen.queryByRole('main', { name: 'admin console' })).toBeNull();
  });

  it('boots direct and compatibility chat routes into the dedicated feature', async () => {
    window.history.replaceState(null, '', '/chat/7');
    const first = render(<App />);
    expect(await screen.findByRole('main', { name: 'chat route' })).toHaveProperty('textContent', 'Chat preset 7 direct');

    first.unmount();
    window.history.replaceState(null, '', '/chat2link');
    render(<App />);
    expect(await screen.findByRole('main', { name: 'chat route' })).toHaveProperty('textContent', 'Chat preset first-web compatibility');
  });

  it('invalidates an expired live session and preserves the protected return target', async () => {
    window.history.replaceState(null, '', '/keys?page=2');
    render(<App />);
    expect(await screen.findByRole('main', { name: 'authenticated console' })).toBeTruthy();

    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));

    await waitFor(() => expect(window.location.pathname).toBe('/sign-in'));
    expect(new URLSearchParams(window.location.search).get('redirect')).toBe('/keys?page=2');
    expect(screen.queryByRole('main', { name: 'authenticated console' })).toBeNull();
  });

  it('renders the root guard before entering the nested system-settings routes', async () => {
    window.history.replaceState(null, '', '/system-settings/auth');
    render(<App />);

    await waitFor(() => expect(window.location.pathname).toBe('/403'));
    expect(await screen.findByRole('heading', { name: 'Access denied' })).toBeTruthy();
    expect(screen.queryByRole('main', { name: 'admin console' })).toBeNull();
  });

  it('normalizes a root system-settings category to its exact default section', async () => {
    window.history.replaceState(null, '', '/system-settings/billing');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') return { setup_required: false };
      if (path === '/status') return {};
      if (path === '/user/self') {
        return {
          id: 1,
          username: 'root',
          display_name: 'Root',
          role: 100,
          group: 'default',
          quota: 0,
          used_quota: 0,
          request_count: 0,
        };
      }
      throw new Error(`unexpected test endpoint ${path}`);
    });

    render(<App />);
    await waitFor(() => expect(window.location.pathname).toBe('/system-settings/billing/quota'));
    expect(await screen.findByRole('main', { name: 'system settings route' })).toBeTruthy();
    expect(screen.getByText('billing/quota')).toBeTruthy();
    expect(screen.getByText('role 100')).toBeTruthy();
  });
});
