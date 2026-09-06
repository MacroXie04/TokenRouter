import { useEffect, useMemo, useRef, useState, type MouseEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type ApiResponse, type User } from '../../shared/api/client';
import type { HeaderNavigationModules } from '../../shared/config/nav-modules';
import {
  isNavigationActive,
  visibleNavigationGroups,
  type UserNavigationPermissions,
} from '../../shared/config/sidebar-navigation';
import { SystemBrand } from '../../shared/ui/SystemBrand';

type LayoutUser = User & { permissions?: UserNavigationPermissions };

function shouldNavigateInApp(event: MouseEvent<HTMLAnchorElement>): boolean {
  return event.button === 0
    && !event.defaultPrevented
    && !event.metaKey
    && !event.ctrlKey
    && !event.shiftKey
    && !event.altKey;
}

export function AuthenticatedLayout({
  children,
  user,
  pathname,
  modules,
  sidebarModules,
  defaultCollapseSidebar = false,
  userSidebarModules,
  onNavigate,
  onLogout,
}: {
  children: ReactNode;
  user: User;
  pathname: string;
  modules: HeaderNavigationModules;
  sidebarModules?: unknown;
  defaultCollapseSidebar?: boolean;
  userSidebarModules?: unknown;
  onNavigate: (target: string) => void;
  onLogout: () => void;
}) {
  const { t } = useTranslation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(defaultCollapseSidebar);
  const [logoutBusy, setLogoutBusy] = useState(false);
  const [logoutError, setLogoutError] = useState('');
  const menuButtonRef = useRef<HTMLButtonElement | null>(null);
  const sidebarRef = useRef<HTMLElement | null>(null);
  const shellUser = user as LayoutUser;
  const groups = useMemo(
    () => visibleNavigationGroups(
      (key) => t(key),
      user.role,
      sidebarModules,
      userSidebarModules,
      shellUser.permissions,
    ),
    [shellUser.permissions, sidebarModules, t, user.role, userSidebarModules],
  );
  const displayName = user.display_name?.trim() || user.username;

  useEffect(() => {
    setMobileOpen(false);
  }, [pathname]);

  useEffect(() => {
    setSidebarCollapsed(defaultCollapseSidebar);
  }, [defaultCollapseSidebar]);

  useEffect(() => {
    if (!mobileOpen) return undefined;
    sidebarRef.current?.querySelector<HTMLButtonElement>('button')?.focus();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      setMobileOpen(false);
      menuButtonRef.current?.focus();
    };
    document.addEventListener('keydown', closeOnEscape);
    return () => document.removeEventListener('keydown', closeOnEscape);
  }, [mobileOpen]);

  const navigateFromLink = (event: MouseEvent<HTMLAnchorElement>, target: string) => {
    if (!shouldNavigateInApp(event)) return;
    event.preventDefault();
    setMobileOpen(false);
    onNavigate(target);
  };

  const logout = async () => {
    if (logoutBusy) return;
    setLogoutBusy(true);
    setLogoutError('');
    try {
      const response = await api.post<ApiResponse<unknown>>('/user/auth/logout');
      if (response.data?.success !== true) throw new Error('logout rejected');
      onLogout();
    } catch {
      setLogoutError(t('Unable to sign out that session.'));
    } finally {
      setLogoutBusy(false);
    }
  };

  const topLinks = [
    modules.console !== false
      ? { label: t('Dashboard'), href: '/dashboard/overview', active: '/dashboard/*' }
      : null,
    modules.pricing.enabled ? { label: t('Pricing'), href: '/pricing', active: '/pricing/*' } : null,
    modules.rankings.enabled ? { label: t('Rankings'), href: '/rankings', active: '/rankings' } : null,
    modules.about !== false ? { label: t('About'), href: '/about', active: '/about' } : null,
  ].filter((link): link is { label: string; href: string; active: string } => link !== null);

  return (
    <div
      className="authenticated-shell"
      data-mobile-navigation-open={mobileOpen}
      data-sidebar-collapsed={sidebarCollapsed}
    >
      <a className="skip-to-main" href="#content">{t('Skip to Main')}</a>
      <header className="authenticated-header">
        <button
          ref={menuButtonRef}
          type="button"
          className="shell-menu-toggle"
          aria-controls="authenticated-navigation"
          aria-expanded={mobileOpen}
          aria-label={t('Primary navigation')}
          onClick={() => setMobileOpen((open) => !open)}
        >
          <span aria-hidden="true">☰</span>
        </button>
        <SystemBrand variant="header" onNavigate={onNavigate} />
        <button
          type="button"
          className="shell-sidebar-collapse"
          aria-controls="authenticated-navigation"
          aria-expanded={!sidebarCollapsed}
          aria-label={sidebarCollapsed ? t('Expand sidebar') : t('Collapse sidebar')}
          onClick={() => setSidebarCollapsed((collapsed) => !collapsed)}
        >
          <span aria-hidden="true">{sidebarCollapsed ? '»' : '«'}</span>
        </button>
        <nav className="authenticated-top-navigation" aria-label={t('Primary navigation')}>
          {topLinks.map((link) => (
            <a
              key={link.href}
              href={link.href}
              aria-current={isNavigationActive(pathname, {
                label: link.label,
                href: link.href,
                activePaths: [link.active],
                section: 'header',
                module: 'header',
              }) ? 'page' : undefined}
              onClick={(event) => navigateFromLink(event, link.href)}
            >
              {link.label}
            </a>
          ))}
        </nav>
        <div className="authenticated-account">
          <a
            href="/profile"
            className="authenticated-user"
            aria-current={pathname === '/profile' ? 'page' : undefined}
            onClick={(event) => navigateFromLink(event, '/profile')}
          >
            <span className="authenticated-avatar" aria-hidden="true">
              {Array.from(displayName)[0]?.toLocaleUpperCase() || '?'}
            </span>
            <span>{displayName}</span>
          </a>
          <button type="button" className="link" disabled={logoutBusy} onClick={() => { void logout(); }}>
            {t('Sign out')}
          </button>
        </div>
      </header>

      {logoutError && <p className="shell-session-error" role="alert">{logoutError}</p>}

      <div className="authenticated-body">
        <button
          type="button"
          className="shell-navigation-backdrop"
          aria-label={t('Close')}
          tabIndex={-1}
          onClick={() => setMobileOpen(false)}
        />
        <aside ref={sidebarRef} id="authenticated-navigation" className="authenticated-sidebar">
          <button
            type="button"
            className="shell-sidebar-close"
            aria-label={t('Close')}
            onClick={() => setMobileOpen(false)}
          >
            <span aria-hidden="true">×</span>
          </button>
          <nav aria-label={`${t('Dashboard')} ${t('Primary navigation')}`}>
            {groups.map((group) => (
              <section className="shell-navigation-group" key={group.label}>
                <h2>{group.label}</h2>
                <ul>
                  {group.items.map((item) => {
                    const active = isNavigationActive(pathname, item);
                    return (
                      <li key={item.href}>
                        <a
                          href={item.href}
                          aria-current={active ? 'page' : undefined}
                          onClick={(event) => navigateFromLink(event, item.href)}
                        >
                          {item.label}
                        </a>
                      </li>
                    );
                  })}
                </ul>
              </section>
            ))}
          </nav>
        </aside>
        <div id="content" className="authenticated-content" tabIndex={-1}>
          {children}
        </div>
      </div>
    </div>
  );
}
