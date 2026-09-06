// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, type User } from '../../api';
import { parseHeaderNavigationModules } from '../../lib/nav-modules';
import { AuthenticatedLayout } from './AuthenticatedLayout';
import { SystemBrandProvider } from './SystemBrand';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

const user: User = {
  id: 7,
  username: 'alice',
  display_name: 'Alice Example',
  role: 1,
  group: 'default',
  quota: 0,
  used_quota: 0,
  request_count: 0,
};

function renderLayout(overrides: Partial<Parameters<typeof AuthenticatedLayout>[0]> = {}) {
  const props = {
    children: <main aria-label="route content">Route content</main>,
    user,
    pathname: '/keys',
    modules: parseHeaderNavigationModules(undefined),
    sidebarModules: '',
    onNavigate: vi.fn(),
    onLogout: vi.fn(),
    ...overrides,
  };
  const result = render(
    <SystemBrandProvider value={{ systemName: 'Acme Gateway', logo: '/brand.png', loading: false }}>
      <AuthenticatedLayout {...props} />
    </SystemBrandProvider>,
  );
  return { ...result, props };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('AuthenticatedLayout', () => {
  it('renders one shared brand, skip target, role-safe navigation, and active route state', () => {
    renderLayout({
      modules: parseHeaderNavigationModules(JSON.stringify({ pricing: false, rankings: true })),
      sidebarModules: JSON.stringify({ chat: { playground: false } }),
      userSidebarModules: JSON.stringify({ personal: { topup: false } }),
    });

    expect(screen.getByRole('link', { name: 'Back to home' }).textContent).toContain('Acme Gateway');
    expect(screen.getByRole('link', { name: 'Skip to Main' }).getAttribute('href')).toBe('#content');
    expect(document.getElementById('content')?.getAttribute('tabindex')).toBe('-1');
    expect(screen.getByRole('link', { name: 'API keys' }).getAttribute('aria-current')).toBe('page');
    expect(screen.queryByRole('link', { name: 'Playground' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Wallet' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Channels' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Pricing' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Rankings' })).toBeTruthy();
  });

  it('uses native links, closes mobile navigation on selection, and restores the toggle on Escape', async () => {
    const onNavigate = vi.fn();
    const interaction = userEvent.setup();
    const { container } = renderLayout({ onNavigate });
    const shell = container.querySelector('.authenticated-shell')!;
    const toggle = screen.getByRole('button', { name: 'Primary navigation' });

    await interaction.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(shell.getAttribute('data-mobile-navigation-open')).toBe('true');
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(toggle);

    await interaction.click(toggle);
    await interaction.click(screen.getByRole('link', { name: 'Wallet' }));
    expect(onNavigate).toHaveBeenCalledWith('/wallet');
    expect(shell.getAttribute('data-mobile-navigation-open')).toBe('false');
  });

  it('applies the public desktop-sidebar default and keeps an accessible session toggle', async () => {
    const { container } = renderLayout({ defaultCollapseSidebar: true });
    const shell = container.querySelector('.authenticated-shell')!;
    const expand = screen.getByRole('button', { name: 'Expand sidebar' });

    expect(shell.getAttribute('data-sidebar-collapsed')).toBe('true');
    expect(expand.getAttribute('aria-controls')).toBe('authenticated-navigation');
    expect(expand.getAttribute('aria-expanded')).toBe('false');
    await userEvent.setup().click(expand);
    expect(shell.getAttribute('data-sidebar-collapsed')).toBe('false');
    expect(screen.getByRole('button', { name: 'Collapse sidebar' }).getAttribute('aria-expanded')).toBe('true');
  });

  it('ends local authentication only after the logout endpoint succeeds', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValueOnce({
      data: { success: true, data: null },
    } as never);
    const onLogout = vi.fn();
    renderLayout({ onLogout });

    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign out' }));

    await waitFor(() => expect(post).toHaveBeenCalledWith('/user/auth/logout'));
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it('keeps the session visible and redacts a failed logout response', async () => {
    vi.spyOn(api, 'post').mockRejectedValueOnce(new Error('private upstream detail'));
    const onLogout = vi.fn();
    renderLayout({ onLogout });

    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign out' }));

    expect((await screen.findByRole('alert')).textContent).toBe('Unable to sign out that session.');
    expect(onLogout).not.toHaveBeenCalled();
    expect(screen.queryByText('private upstream detail')).toBeNull();
  });
});
