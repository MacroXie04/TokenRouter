// @vitest-environment jsdom

import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getData } from './api';
import App, { savedUserLanguage } from './App';

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

vi.mock('./features/dashboard', () => ({
  DashboardView: () => <main aria-label="dashboard route">Dashboard route</main>,
}));

vi.mock('./features/home/home-api', () => ({
  MAX_PUBLIC_CONTENT_CHARACTERS: 1_000_000,
  loadBasicRankings: vi.fn(async () => []),
  loadPerformanceSummary: vi.fn(async () => []),
  loadPublicContent: vi.fn(async () => ''),
}));

vi.mock('./features/pricing/pricing-api', () => ({
  loadPricingCatalog: vi.fn(async () => ({
    items: [{
      model_name: 'catalog-model', vendor_id: 0, quota_type: 0, model_ratio: 1,
      model_price: 0, prompt_price: 1, completion_price: 2, owner_by: 'community',
      completion_ratio: 2, enable_groups: ['default'], supported_endpoint_types: ['openai'],
    }],
    vendors: [],
    groupRatio: { default: 1 },
    usableGroup: { default: 'Default' },
    supportedEndpoint: { openai: { path: '/v1/chat/completions', method: 'POST' } },
    autoGroups: ['default'],
    version: 'a'.repeat(64),
  })),
}));

const mockedGetData = vi.mocked(getData);

const publicStatus = {
  system_name: 'Acme Gateway',
  logo: 'https://cdn.example/acme.png',
  HeaderNavModules: JSON.stringify({ pricing: false, rankings: true }),
  SidebarModulesAdmin: JSON.stringify({ chat: { playground: false } }),
};

beforeEach(() => {
  vi.resetAllMocks();
  i18nMocks.changeLanguage.mockResolvedValue(undefined);
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
});

afterEach(() => {
  cleanup();
  window.history.replaceState(null, '', '/');
});

describe('shared layout integration', () => {
  it('shows the public auth brand loading state while bootstrap is pending', () => {
    window.history.replaceState(null, '', '/sign-in');
    mockedGetData.mockImplementation(() => new Promise(() => {}));

    render(<App />);

    expect(document.querySelector('[data-auth-layout="true"]')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Back to home' }).getAttribute('aria-busy')).toBe('true');
  });

  it('renders status-driven auth branding once bootstrap completes', async () => {
    window.history.replaceState(null, '', '/sign-in');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') return { setup_required: false };
      if (path === '/status') return publicStatus;
      if (path === '/user/self') throw new Error('anonymous');
      throw new Error(`unexpected ${path}`);
    });

    render(<App />);

    expect(await screen.findByLabelText('Username or Email')).toBeTruthy();
    const brand = screen.getByRole('link', { name: 'Back to home' });
    expect(brand.textContent).toContain('Acme Gateway');
    expect(brand.querySelector('img')?.getAttribute('src')).toBe('https://cdn.example/acme.png');
  });

  it('renders status branding and only enabled public navigation on the home route', async () => {
    window.history.replaceState(null, '', '/');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') return { setup_required: false };
      if (path === '/status') return {
        ...publicStatus,
        announcements_enabled: true,
        announcements: [{
          id: 3,
          type: 'success',
          content: 'Status announcement\n\nMore detail',
          publishDate: '2026-09-06T12:00:00Z',
        }],
        HeaderNavModules: JSON.stringify({
          home: false,
          console: false,
          docs: false,
          about: false,
          pricing: false,
          rankings: true,
        }),
      };
      if (path === '/user/self') throw new Error('anonymous');
      throw new Error(`unexpected ${path}`);
    });

    render(<App />);

    const navigation = await screen.findByRole('navigation', { name: 'Primary navigation' });
    const brand = screen.getByRole('link', { name: 'Back to home' });
    expect(brand.textContent).toContain('Acme Gateway');
    expect(brand.querySelector('img')?.getAttribute('src')).toBe('https://cdn.example/acme.png');
    expect(screen.getByRole('img', { name: 'Gateway request flow' }).textContent).toContain('Acme Gateway');
    expect(within(navigation).queryByRole('link', { name: 'Home' })).toBeNull();
    expect(within(navigation).queryByRole('link', { name: 'Console' })).toBeNull();
    expect(within(navigation).queryByRole('link', { name: 'Pricing' })).toBeNull();
    expect(within(navigation).getByRole('link', { name: 'Rankings' })).toBeTruthy();
    expect(within(navigation).queryByRole('link', { name: 'Documentation' })).toBeNull();
    expect(within(navigation).queryByRole('link', { name: 'About' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'About' })).toBeNull();
    expect(screen.getAllByRole('link', { name: 'Create an account.' })[0]?.getAttribute('href')).toBe('/sign-up');
    expect(await screen.findByText('Acme Gateway — independent AI API gateway.')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: /System notice/ }));
    await userEvent.setup().click(screen.getByRole('tab', { name: /Announcements/ }));
    expect(screen.getByText('Status announcement')).toBeTruthy();
    expect(screen.getByText('More detail')).toBeTruthy();
  });

  it('keeps the TokenRouter fallback and catalog-backed API demonstration on home', async () => {
    window.history.replaceState(null, '', '/');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') return { setup_required: false };
      if (path === '/status') return { system_name: '', site_name: '', app_name: '', logo: '' };
      if (path === '/user/self') throw new Error('anonymous');
      throw new Error(`unexpected ${path}`);
    });

    render(<App />);

    const navigation = await screen.findByRole('navigation', { name: 'Primary navigation' });
    expect(screen.getByRole('link', { name: 'Back to home' }).textContent).toContain('TokenRouter');
    expect(within(navigation).getByRole('link', { name: 'Home' })).toBeTruthy();
    expect(within(navigation).getByRole('link', { name: 'Console' }).getAttribute('href')).toBe('/dashboard');
    expect(within(navigation).getByRole('link', { name: 'Pricing' })).toBeTruthy();
    expect(within(navigation).getByRole('link', { name: 'Rankings' })).toBeTruthy();
    expect(within(navigation).getByRole('link', { name: 'Documentation' }).getAttribute('href')).toBe('#api-quickstart');
    expect(within(navigation).getByRole('link', { name: 'About' })).toBeTruthy();
    await screen.findByRole('navigation', { name: 'Supported API routes' });
    expect(document.querySelector('.home-api-terminal code')?.textContent).toBe('POST /v1/chat/completions\nmodel: catalog-model');
    expect(screen.getByRole('link', { name: /openai POST \/v1\/chat\/completions/ }).getAttribute('href')).toBe('/pricing?endpointType=openai');
  });

  it('wraps protected routes in the configured, role-filtered application shell', async () => {
    window.history.replaceState(null, '', '/dashboard/overview');
    mockedGetData.mockImplementation(async (path: string) => {
      if (path === '/setup') return { setup_required: false };
      if (path === '/status') return { ...publicStatus, default_collapse_sidebar: true };
      if (path === '/user/self') {
        return {
          id: 4,
          username: 'alice',
          display_name: 'Alice',
          role: 1,
          group: 'default',
          quota: 0,
          used_quota: 0,
          request_count: 0,
          setting: JSON.stringify({ language: 'ja' }),
          sidebar_modules: JSON.stringify({ personal: { topup: false } }),
          permissions: { sidebar_settings: true, sidebar_modules: { personal: true } },
        };
      }
      throw new Error(`unexpected ${path}`);
    });

    render(<App />);

    expect(await screen.findByRole('main', { name: 'dashboard route' })).toBeTruthy();
    expect(document.querySelector('.authenticated-shell')?.getAttribute('data-sidebar-collapsed')).toBe('true');
    expect(screen.getByRole('link', { name: 'Overview' }).getAttribute('aria-current')).toBe('page');
    expect(screen.queryByRole('link', { name: 'Playground' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Wallet' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Channels' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Pricing' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Rankings' })).toBeTruthy();
    expect(i18nMocks.changeLanguage).toHaveBeenCalledWith('ja');
  });

  it('ignores malformed, oversized, and unsupported saved languages', () => {
    const base = {
      id: 4,
      username: 'alice',
      display_name: 'Alice',
      role: 1,
      group: 'default',
      quota: 0,
      used_quota: 0,
      request_count: 0,
    };

    expect(savedUserLanguage({ ...base, setting: '{bad json' })).toBeNull();
    expect(savedUserLanguage({ ...base, setting: JSON.stringify({ language: 'xx' }) })).toBeNull();
    expect(savedUserLanguage({ ...base, setting: ' '.repeat((64 * 1024) + 1) })).toBeNull();
    expect(savedUserLanguage({ ...base, language: 'zh-TW', setting: '{bad json' })).toBe('zh-TW');
  });
});
