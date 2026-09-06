// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { HeaderNavigationModules } from '../../shared/config/nav-modules';
import type { PricingCatalog } from '../pricing/catalog';
import { loadPricingCatalog } from '../pricing/pricing-api';
import { loadBasicRankings, loadPerformanceSummary } from './home-api';
import { loadPublicContent } from '../public-documents/public-content-api';
vi.mock('../public-documents/public-content-api', () => ({ loadPublicContent: vi.fn(), MAX_PUBLIC_CONTENT_CHARACTERS: 1_000_000 }));
import { HomeView } from './HomeView';

const i18nState = vi.hoisted(() => ({ language: 'en', resolvedLanguage: 'en' }));

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t, i18n: i18nState }) };
});
vi.mock('./home-api', () => ({
  MAX_PUBLIC_CONTENT_CHARACTERS: 1_000_000,
  loadBasicRankings: vi.fn(),
  loadPerformanceSummary: vi.fn(),
  loadPublicContent: vi.fn(),
}));
vi.mock('../pricing/pricing-api', () => ({ loadPricingCatalog: vi.fn() }));

const mockedContent = vi.mocked(loadPublicContent);
const mockedCatalog = vi.mocked(loadPricingCatalog);
const mockedRankings = vi.mocked(loadBasicRankings);
const mockedPerformance = vi.mocked(loadPerformanceSummary);
const themeListeners = new Set<(event: MediaQueryListEvent) => void>();

const modules: HeaderNavigationModules = {
  pricing: { enabled: true, requireAuth: false },
  rankings: { enabled: true, requireAuth: false },
};

function catalog(): PricingCatalog {
  return {
    items: [
      {
        model_name: 'alpha-model', description: 'Alpha', tags: 'chat', vendor_id: 1, quota_type: 0,
        model_ratio: 0.25, model_price: 0, prompt_price: 0.25, completion_price: 0.75,
        owner_by: 'custom', completion_ratio: 3, enable_groups: ['default'],
        supported_endpoint_types: ['openai'],
      },
      {
        model_name: 'zeta-model', vendor_id: 0, quota_type: 0, model_ratio: 1, model_price: 0,
        prompt_price: 1.25, completion_price: 3.5, owner_by: 'community', completion_ratio: 2.8,
        enable_groups: ['default'], supported_endpoint_types: ['openai'],
      },
    ],
    vendors: [{ id: 1, name: 'Acme' }],
    groupRatio: { default: 1 },
    usableGroup: { default: 'Default' },
    supportedEndpoint: { openai: { path: '/v1/chat/completions', method: 'POST' } },
    autoGroups: ['default'], version: 'a'.repeat(64),
  };
}

const contentByPath: Record<string, string> = {
  '/notice': '', '/home_page_content': '', '/about': '', '/user-agreement': '', '/privacy-policy': '',
};

function useReadyResponses(overrides: Partial<Record<string, string>> = {}) {
  mockedContent.mockImplementation((path) => Promise.resolve({ ...contentByPath, ...overrides }[path] ?? ''));
  mockedCatalog.mockResolvedValue(catalog());
  mockedRankings.mockResolvedValue([{ model_name: 'ranked-model', count: 42, quota: 1_200 }]);
  mockedPerformance.mockResolvedValue([{ model_name: 'fast-model', avg_latency_ms: 187, success_rate: 99.456, avg_tps: 31.239 }]);
}

function renderHome(props: Partial<Parameters<typeof HomeView>[0]> = {}) {
  const onSignIn = props.onSignIn ?? vi.fn();
  render(<HomeView onSignIn={onSignIn} signedIn={props.signedIn ?? false} modules={props.modules ?? modules} />);
  return { onSignIn };
}

beforeEach(() => {
  i18nState.language = 'en';
  i18nState.resolvedLanguage = 'en';
  themeListeners.clear();
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn((query: string) => ({
      matches: true,
      media: query,
      onchange: null,
      addEventListener: (type: string, listener: (event: MediaQueryListEvent) => void) => {
        if (type === 'change') themeListeners.add(listener);
      },
      removeEventListener: (type: string, listener: (event: MediaQueryListEvent) => void) => {
        if (type === 'change') themeListeners.delete(listener);
      },
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(() => true),
    })),
  });
  const values = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      clear: () => values.clear(),
      getItem: (key: string) => values.get(key) ?? null,
      removeItem: (key: string) => values.delete(key),
      setItem: (key: string, value: string) => values.set(key, String(value)),
    },
  });
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  window.localStorage.clear();
});

describe('HomeView default composition', () => {
  it('renders hero, API-backed stats, features, workflow, live data, CTA, notice, and legal links', async () => {
    useReadyResponses({
      '/notice': '# Maintenance\n\n[Status](https://status.example.test)\n\n<script>steal()</script>',
      '/about': '<strong>Operator supplied About text</strong>',
      '/user-agreement': '# Terms',
      '/privacy-policy': 'https://legal.example.test/privacy',
    });
    renderHome();

    expect(await screen.findByRole('heading', { name: 'One gateway for every AI workflow' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Built around what this gateway publishes' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Three steps from catalog to request' })).toBeTruthy();
    expect(screen.getAllByRole('link', { name: 'Create an account.' })[0]?.getAttribute('href')).toBe('/sign-up');
    expect(screen.getByRole('link', { name: 'Console' }).getAttribute('href')).toBe('/dashboard');
    const stats = screen.getByRole('region', { name: 'Gateway catalog statistics' });
    await waitFor(() => expect(within(stats).getByText('2')).toBeTruthy());
    expect(within(stats).getAllByText('1')).toHaveLength(3);

    const supportedRoutes = screen.getByRole('navigation', { name: 'Supported API routes' });
    const endpointApplication = within(supportedRoutes).getByRole('link', { name: /openai POST \/v1\/chat\/completions/ });
    expect(endpointApplication.getAttribute('href')).toBe('/pricing?endpointType=openai');
    expect(document.querySelector('.home-api-terminal code')?.textContent).toBe('POST /v1/chat/completions\nmodel: alpha-model');

    expect(screen.getByRole('link', { name: 'alpha-model' }).getAttribute('href')).toBe('/pricing/alpha-model');
    expect(screen.getByText('$0.25 input · $0.75 output / 1M')).toBeTruthy();
    expect(screen.getByText('187 ms · 99.46% · 31.24 tokens/s')).toBeTruthy();
    expect(screen.getByText('42 requests · 1200 quota')).toBeTruthy();
    expect(screen.getByText('<strong>Operator supplied About text</strong>')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'User Agreement' }).getAttribute('href')).toBe('/user-agreement');
    expect(screen.getByRole('link', { name: 'Privacy Policy' }).getAttribute('href')).toBe('/privacy-policy');

    const noticeToggle = await screen.findByRole('button', { name: /System notice/ });
    expect(noticeToggle.textContent).toContain('New');
    await userEvent.setup().click(noticeToggle);
    expect(screen.getByRole('heading', { name: 'Maintenance' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Status' }).getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.queryByText('steal()')).toBeNull();
    expect(window.localStorage.getItem('tokenrouter.public_notice_read.v1')).toBeTruthy();

    expect(mockedContent.mock.calls.map(([path]) => path).sort()).toEqual([
      '/about', '/home_page_content', '/notice', '/privacy-policy', '/user-agreement',
    ]);
    expect(mockedCatalog).toHaveBeenCalledTimes(1);
    expect(mockedRankings).toHaveBeenCalledTimes(1);
    expect(mockedPerformance).toHaveBeenCalledTimes(1);
    for (const call of [...mockedContent.mock.calls, ...mockedCatalog.mock.calls, ...mockedRankings.mock.calls, ...mockedPerformance.mock.calls]) {
      expect(call.at(-1)).toBeInstanceOf(AbortSignal);
    }
  });

  it('shows accessible resource-specific loading, empty, and redacted error states', async () => {
    mockedContent.mockImplementation((path) => (
      path === '/notice' ? Promise.resolve('') : new Promise<string>(() => {})
    ));
    mockedCatalog.mockReturnValue(new Promise(() => {}));
    mockedRankings.mockReturnValue(new Promise(() => {}));
    mockedPerformance.mockReturnValue(new Promise(() => {}));
    const pending = render(<HomeView onSignIn={vi.fn()} signedIn={false} modules={modules} />);
    expect(screen.getAllByRole('status').map((node) => node.textContent)).toEqual([
      'Loading pricing…', 'Loading pricing…', 'Loading model performance…', 'Loading top models…',
      'Loading About information…', 'Checking legal documents…',
    ]);
    pending.unmount();

    vi.resetAllMocks();
    useReadyResponses();
    mockedCatalog.mockResolvedValue({ ...catalog(), items: [] });
    mockedRankings.mockResolvedValue([]);
    mockedPerformance.mockResolvedValue([]);
    renderHome();
    expect(await screen.findByText('No custom model prices are published.')).toBeTruthy();
    expect(screen.getByText('No performance data yet.')).toBeTruthy();
    expect(screen.getByText('No usage yet.')).toBeTruthy();
    expect(screen.getByText('No legal documents are currently published.')).toBeTruthy();
    cleanup();

    vi.resetAllMocks();
    mockedContent.mockRejectedValue(new Error('private upstream failure'));
    mockedCatalog.mockRejectedValue(new Error('private catalog failure'));
    mockedRankings.mockRejectedValue(new Error('private ranking failure'));
    mockedPerformance.mockRejectedValue(new Error('private metrics failure'));
    renderHome();
    await waitFor(() => expect(screen.getAllByRole('alert')).toHaveLength(7));
    expect(screen.queryByText(/private upstream|private catalog|private ranking|private metrics/)).toBeNull();
  });

  it('honors module visibility and the sign-in/dashboard action', async () => {
    useReadyResponses();
    const onSignIn = vi.fn();
    renderHome({ onSignIn, signedIn: true, modules: {
      pricing: { enabled: false, requireAuth: false }, rankings: { enabled: false, requireAuth: false },
    } });
    await screen.findByRole('heading', { name: 'One gateway for every AI workflow' });
    expect(screen.queryByRole('link', { name: 'Pricing' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Rankings' })).toBeNull();
    await userEvent.setup().click(screen.getAllByRole('button', { name: 'Open dashboard' })[0]!);
    expect(onSignIn).toHaveBeenCalledTimes(1);
  });

  it('does not render links for disabled header modules', async () => {
    useReadyResponses();
    renderHome({ modules: {
      home: false,
      console: false,
      docs: false,
      about: false,
      pricing: { enabled: false, requireAuth: false },
      rankings: { enabled: false, requireAuth: false },
    } });

    await screen.findByRole('heading', { name: 'One gateway for every AI workflow' });
    const primaryNavigation = screen.getByRole('navigation', { name: 'Primary navigation' });
    expect(within(primaryNavigation).queryByRole('link', { name: 'Home' })).toBeNull();
    expect(within(primaryNavigation).queryByRole('link', { name: 'Console' })).toBeNull();
    expect(within(primaryNavigation).queryByRole('link', { name: 'Pricing' })).toBeNull();
    expect(within(primaryNavigation).queryByRole('link', { name: 'Rankings' })).toBeNull();
    expect(within(primaryNavigation).queryByRole('link', { name: 'Documentation' })).toBeNull();
    expect(within(primaryNavigation).queryByRole('link', { name: 'About' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'About' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Documentation' })).toBeNull();
    expect(screen.getAllByRole('link', { name: 'Create an account.' }).length).toBeGreaterThan(0);
    expect(mockedCatalog).not.toHaveBeenCalled();
    expect(mockedRankings).not.toHaveBeenCalled();
    expect(mockedPerformance).not.toHaveBeenCalled();
  });
});

describe('HomeView custom content', () => {
  it('replaces the default composition with bounded Markdown and caches it', async () => {
    useReadyResponses({ '/home_page_content': '# Operator home\n\n[Docs](https://docs.example.test)' });
    renderHome();
    expect(await screen.findByRole('heading', { name: 'Operator home' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Docs' }).getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.queryByRole('heading', { name: 'One gateway for every AI workflow' })).toBeNull();
    expect(window.localStorage.getItem('tokenrouter.home_page_content.v1')).toContain('# Operator home');
  });

  it('isolates configured HTML and synchronizes external frames to their exact origin', async () => {
    useReadyResponses({ '/home_page_content': '<main><h1>Configured</h1><script>top.secret</script></main>' });
    const html = render(<HomeView onSignIn={vi.fn()} signedIn={false} modules={modules} />);
    const htmlFrame = await screen.findByTitle('Custom home page');
    expect(htmlFrame.getAttribute('sandbox')).toBe('');
    expect(htmlFrame.getAttribute('referrerpolicy')).toBe('no-referrer');
    html.unmount();

    vi.resetAllMocks();
    window.localStorage.clear();
    useReadyResponses({ '/home_page_content': 'https://portal.example.test/home' });
    const external = render(<HomeView onSignIn={vi.fn()} signedIn={false} modules={modules} />);
    const urlFrame = await screen.findByTitle('Custom home page');
    expect(urlFrame.getAttribute('src')).toBe('https://portal.example.test/home');
    expect(urlFrame.getAttribute('sandbox')).not.toContain('allow-same-origin');
    const postMessage = vi.spyOn((urlFrame as HTMLIFrameElement).contentWindow!, 'postMessage');
    fireEvent.load(urlFrame);
    expect(postMessage).toHaveBeenCalledWith({ themeMode: 'dark' }, 'https://portal.example.test');
    expect(postMessage).toHaveBeenCalledWith({ lang: 'en' }, 'https://portal.example.test');
    expect(postMessage.mock.calls.map(([, targetOrigin]) => String(targetOrigin))).toEqual([
      'https://portal.example.test',
      'https://portal.example.test',
    ]);

    i18nState.language = 'fr';
    i18nState.resolvedLanguage = 'fr';
    external.rerender(<HomeView onSignIn={vi.fn()} signedIn={false} modules={modules} />);
    await waitFor(() => expect(postMessage).toHaveBeenCalledWith({ lang: 'fr' }, 'https://portal.example.test'));

    act(() => {
      themeListeners.forEach((listener) => listener({ matches: false } as MediaQueryListEvent));
    });
    await waitFor(() => expect(postMessage).toHaveBeenCalledWith({ themeMode: 'light' }, 'https://portal.example.test'));
  });

  it('uses a valid cached page if refresh fails and aborts every request on unmount', async () => {
    window.localStorage.setItem('tokenrouter.home_page_content.v1', '# Cached home');
    const signals: AbortSignal[] = [];
    mockedContent.mockImplementation((_path, signal) => {
      if (signal) signals.push(signal);
      return new Promise(() => {});
    });
    mockedCatalog.mockImplementation((signal) => {
      if (signal) signals.push(signal);
      return new Promise(() => {});
    });
    mockedRankings.mockImplementation((signal) => {
      if (signal) signals.push(signal);
      return new Promise(() => {});
    });
    mockedPerformance.mockImplementation((signal) => {
      if (signal) signals.push(signal);
      return new Promise(() => {});
    });
    const rendered = render(<HomeView onSignIn={vi.fn()} signedIn={false} modules={modules} />);
    expect(screen.getByRole('heading', { name: 'Cached home' })).toBeTruthy();
    expect(signals).toHaveLength(8);
    rendered.unmount();
    await act(async () => Promise.resolve());
    expect(signals.every((signal) => signal.aborted)).toBe(true);
  });
});
