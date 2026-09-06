import type { User } from '../api';
import { SYSTEM_SETTINGS_SECTIONS, type SystemSettingsCategory } from './settings-groups';

export type RouteName =
  | 'home'
  | 'about'
  | 'privacy-policy'
  | 'user-agreement'
  | 'setup'
  | 'sign-in'
  | 'sign-up'
  | 'forgot-password'
  | 'reset-password'
  | 'otp'
  | 'oauth'
  | 'oauth-callback'
  | 'error'
  | 'pricing'
  | 'pricing-detail'
  | 'rankings'
  | 'dashboard'
  | 'channels'
  | 'chat'
  | 'chat2link'
  | 'keys'
  | 'models'
  | 'playground'
  | 'profile'
  | 'redemption-codes'
  | 'subscriptions'
  | 'system-info'
  | 'system-settings'
  | 'usage-logs'
  | 'users'
  | 'wallet'
  | 'authenticated-error'
  | 'not-found';

export interface AppRoute {
  name: RouteName;
  pathname: string;
  parameter?: string;
  authenticated: boolean;
  minimumRole?: number;
}

export interface RouteDecision {
  route: AppRoute;
  redirect?: string;
}

export interface ModuleAccess {
  enabled: boolean;
  requireAuth: boolean;
}

export interface RouteContext {
  setupRequired: boolean;
  user: User | null;
  search?: string;
  modules?: Partial<Record<'pricing' | 'rankings', ModuleAccess>>;
}

const ADMIN_ROLE = 10;
const ROOT_ROLE = 100;
const SECTION_ROUTES = {
  dashboard: { defaultSection: 'overview', sections: ['overview', 'models', 'flow', 'users'] },
  models: { defaultSection: 'metadata', sections: ['metadata', 'deployments'] },
  'usage-logs': { defaultSection: 'common', sections: ['common', 'drawing', 'task'] },
} as const;

type SectionRouteName = keyof typeof SECTION_ROUTES;

function isSystemSettingsCategory(value: string): value is SystemSettingsCategory {
  return Object.prototype.hasOwnProperty.call(SYSTEM_SETTINGS_SECTIONS, value);
}

export function systemSettingsRedirect(parameter?: string): string | null {
  if (!parameter) return '/system-settings/site';
  const parts = parameter.split('/');
  const category = parts[0];
  if (!isSystemSettingsCategory(category)) return '/system-settings/site';
  const defaultSection = SYSTEM_SETTINGS_SECTIONS[category][0];
  if (parts.length === 1 || parts[1] === '') {
    return `/system-settings/${category}/${defaultSection}`;
  }
  if (parts.length !== 2 || !SYSTEM_SETTINGS_SECTIONS[category].some((section) => section === parts[1])) {
    if (category === 'operations' && parts[1] === 'monitoring') {
      return '/system-settings/models/routing-reliability';
    }
    return `/system-settings/${category}/${defaultSection}`;
  }
  return null;
}

export function sectionRouteRedirect(
  name: SectionRouteName,
  parameter?: string,
  search = '',
): string | null {
  const definition = SECTION_ROUTES[name];
  const base = `/${name}`;
  if (!parameter || !definition.sections.some((section) => section === parameter)) {
    return `${base}/${definition.defaultSection}`;
  }
  if (name !== 'usage-logs' || parameter === 'common' || !search.includes('type')) return null;

  // The log type filter is meaningful only for common logs. Preserve the
  // remaining bounded query when canonicalizing drawing/task URLs.
  if (search.length > 16 * 1024 || [...search].some((character) => {
    const code = character.charCodeAt(0);
    return code <= 31 || code === 127;
  })) return `${base}/${parameter}`;
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  if (!params.has('type')) return null;
  params.delete('type');
  const remaining = params.toString();
  return `${base}/${parameter}${remaining ? `?${remaining}` : ''}`;
}

function normalizedPath(pathname: string): string {
  if (!pathname || pathname === '/') return '/';
  const withLeadingSlash = pathname.startsWith('/') ? pathname : `/${pathname}`;
  return withLeadingSlash.replace(/\/{2,}/g, '/').replace(/\/$/, '');
}

function route(
  name: RouteName,
  pathname: string,
  authenticated = false,
  minimumRole?: number,
  parameter?: string,
): AppRoute {
  return { name, pathname, authenticated, minimumRole, parameter };
}

export function matchRoute(pathname: string): AppRoute {
  const path = normalizedPath(pathname);
  const publicExact: Record<string, RouteName> = {
    '/': 'home',
    '/about': 'about',
    '/privacy-policy': 'privacy-policy',
    '/user-agreement': 'user-agreement',
    '/setup': 'setup',
    '/sign-in': 'sign-in',
    '/sign-up': 'sign-up',
    '/forgot-password': 'forgot-password',
    '/reset': 'reset-password',
    '/user/reset': 'reset-password',
    '/otp': 'otp',
    '/oauth': 'oauth',
    '/pricing': 'pricing',
    '/rankings': 'rankings',
    '/401': 'error',
    '/403': 'error',
    '/404': 'error',
    '/500': 'error',
    '/503': 'error',
  };
  const publicName = publicExact[path];
  if (publicName) {
    const parameter = publicName === 'error' ? path.slice(1) : undefined;
    return route(publicName, path, false, undefined, parameter);
  }
  if (path === '/register') {
    return route('sign-up', path, false, undefined, 'legacy-register');
  }
  if (path === '/login') {
    return route('sign-in', path, false, undefined, 'legacy-login');
  }
  if (path.startsWith('/pricing/')) {
    return route('pricing-detail', path, false, undefined, decodePathPart(path.slice('/pricing/'.length)));
  }
  if (path.startsWith('/oauth/')) {
    return route('oauth-callback', path, false, undefined, decodePathPart(path.slice('/oauth/'.length)));
  }

  const authenticatedExact: Record<string, { name: RouteName; role?: number }> = {
    '/dashboard': { name: 'dashboard' },
    '/channels': { name: 'channels', role: ADMIN_ROLE },
    '/chat2link': { name: 'chat2link' },
    '/keys': { name: 'keys' },
    '/models': { name: 'models', role: ADMIN_ROLE },
    '/playground': { name: 'playground' },
    '/profile': { name: 'profile' },
    '/redemption-codes': { name: 'redemption-codes', role: ADMIN_ROLE },
    '/subscriptions': { name: 'subscriptions', role: ADMIN_ROLE },
    '/system-info': { name: 'system-info', role: ROOT_ROLE },
    '/system-settings': { name: 'system-settings', role: ROOT_ROLE },
    '/usage-logs': { name: 'usage-logs' },
    '/users': { name: 'users', role: ADMIN_ROLE },
    '/wallet': { name: 'wallet' },
  };
  const exact = authenticatedExact[path];
  if (exact) return route(exact.name, path, true, exact.role);

  if (path.startsWith('/chat/')) {
    return route('chat', path, true, undefined, decodePathPart(path.slice('/chat/'.length)));
  }
  if (path.startsWith('/dashboard/')) {
    return route('dashboard', path, true, undefined, decodePathPart(path.slice('/dashboard/'.length)));
  }
  if (path.startsWith('/models/')) {
    return route('models', path, true, ADMIN_ROLE, decodePathPart(path.slice('/models/'.length)));
  }
  if (path.startsWith('/system-settings/')) {
    return route('system-settings', path, true, ROOT_ROLE, path.slice('/system-settings/'.length));
  }
  if (path.startsWith('/usage-logs/')) {
    return route('usage-logs', path, true, undefined, decodePathPart(path.slice('/usage-logs/'.length)));
  }
  if (path.startsWith('/errors/')) {
    return route('authenticated-error', path, true, undefined, decodePathPart(path.slice('/errors/'.length)));
  }
  return route('not-found', path);
}

function decodePathPart(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

const AUTH_PAGES = new Set<RouteName>([
  'sign-in',
  'sign-up',
  'forgot-password',
  'reset-password',
  'otp',
  'oauth',
]);

export function decideRoute(pathname: string, context: RouteContext): RouteDecision {
  const matched = matchRoute(pathname);
  const search = context.search ?? '';

  if (context.setupRequired) {
    if (matched.name !== 'setup') return { route: matched, redirect: '/setup' };
    return { route: matched };
  }
  if (matched.name === 'setup') return { route: matched, redirect: '/' };
  if (matched.parameter === 'legacy-register') return { route: matched, redirect: `/sign-up${search}` };
  if (matched.parameter === 'legacy-login') return { route: matched, redirect: `/sign-in${search}` };

  const moduleName = matched.name === 'pricing' || matched.name === 'pricing-detail'
    ? 'pricing'
    : matched.name === 'rankings'
      ? 'rankings'
      : undefined;
  if (moduleName) {
    const access = context.modules?.[moduleName] ?? { enabled: true, requireAuth: false };
    if (!access.enabled) return { route: matched, redirect: '/' };
    if (access.requireAuth && !context.user) {
      return { route: matched, redirect: signInTarget(matched.pathname, search) };
    }
  }

  if (AUTH_PAGES.has(matched.name) && context.user) {
    return { route: matched, redirect: safeReturnTarget(search) ?? '/dashboard' };
  }
  if (matched.authenticated && !context.user) {
    return { route: matched, redirect: signInTarget(matched.pathname, search) };
  }
  if (matched.minimumRole && (context.user?.role ?? 0) < matched.minimumRole) {
    return { route: matched, redirect: '/403' };
  }
  if (matched.name === 'system-settings') {
    const redirect = systemSettingsRedirect(matched.parameter);
    if (redirect) return { route: matched, redirect };
  }
  if (matched.name === 'dashboard' || matched.name === 'models' || matched.name === 'usage-logs') {
    const redirect = sectionRouteRedirect(matched.name, matched.parameter, search);
    if (redirect) return { route: matched, redirect };
  }
  return { route: matched };
}

export function signInTarget(pathname: string, search = ''): string {
  const target = `${normalizedPath(pathname)}${search.startsWith('?') ? search : ''}`;
  return `/sign-in?redirect=${encodeURIComponent(target)}`;
}

export function safeReturnTarget(search: string): string | null {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  const target = params.get('redirect');
  if (!target || !target.startsWith('/') || target.startsWith('//')) return null;
  try {
    const parsed = new URL(target, 'https://tokenrouter.invalid');
    if (parsed.origin !== 'https://tokenrouter.invalid') return null;
    const pathname = normalizedPath(parsed.pathname);
    if (pathname === '/sign-in' || pathname === '/sign-up' || pathname === '/register' ||
        pathname === '/login' || pathname === '/forgot-password' || pathname === '/reset' ||
        pathname === '/user/reset' || pathname === '/otp' || pathname === '/oauth' ||
        pathname.startsWith('/oauth/')) return null;
    return `${pathname}${parsed.search}${parsed.hash}`;
  } catch {
    return null;
  }
}
