export interface SidebarModuleSection {
  enabled?: boolean;
  [module: string]: boolean | undefined;
}

export type SidebarModules = Record<string, boolean | SidebarModuleSection>;

export interface NavigationItem {
  label: string;
  href: string;
  activePaths?: string[];
  minimumRole?: number;
  section: string;
  module: string;
}

export interface NavigationGroup {
  label: string;
  items: NavigationItem[];
}

export interface UserNavigationPermissions {
  sidebar_settings?: unknown;
  sidebar_modules?: unknown;
}

type Translate = (key: string) => string;

const MAX_SIDEBAR_CONFIG_BYTES = 64 * 1024;

function normalizeSection(value: unknown): boolean | SidebarModuleSection | undefined {
  if (typeof value === 'boolean') return value;
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined;
  const section: SidebarModuleSection = {};
  for (const [key, enabled] of Object.entries(value as Record<string, unknown>)) {
    if (/^[a-z][a-z0-9_-]{0,63}$/i.test(key) && typeof enabled === 'boolean') {
      section[key] = enabled;
    }
  }
  return section;
}

/**
 * Parse the optional reference-compatible sidebar policy. A missing or invalid
 * policy means "do not narrow" so a bad public setting cannot lock users out.
 */
export function parseSidebarModules(raw: unknown): SidebarModules | null {
  if (raw === undefined || raw === null || raw === '') return null;
  let value = raw;
  if (typeof value === 'string') {
    if (new TextEncoder().encode(value).byteLength > MAX_SIDEBAR_CONFIG_BYTES) return null;
    try {
      value = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const modules: SidebarModules = {};
  for (const [key, sectionValue] of Object.entries(value as Record<string, unknown>)) {
    if (!/^[a-z][a-z0-9_-]{0,63}$/i.test(key)) continue;
    const section = normalizeSection(sectionValue);
    if (section !== undefined) modules[key] = section;
  }
  return modules;
}

function layerAllows(
  modules: SidebarModules | null,
  sectionName: string,
  moduleName: string,
): boolean {
  if (!modules) return true;
  const section = modules[sectionName];
  if (section === undefined) return true;
  if (typeof section === 'boolean') return section;
  return section.enabled !== false && section[moduleName] !== false;
}

export function isNavigationItemVisible(
  item: NavigationItem,
  role: number,
  adminModules: SidebarModules | null,
  userModules: SidebarModules | null,
): boolean {
  return role >= (item.minimumRole ?? 0)
    && layerAllows(adminModules, item.section, item.module)
    && layerAllows(userModules, item.section, item.module);
}

function normalizedPath(pathname: string): string {
  const path = pathname.split(/[?#]/, 1)[0] || '/';
  if (path === '/') return path;
  return path.replace(/\/+$/, '') || '/';
}

function pathMatches(pathname: string, candidate: string): boolean {
  const current = normalizedPath(pathname);
  if (candidate.endsWith('/*')) {
    const prefix = normalizedPath(candidate.slice(0, -2));
    return current === prefix || current.startsWith(`${prefix}/`);
  }
  return current === normalizedPath(candidate);
}

export function isNavigationActive(pathname: string, item: NavigationItem): boolean {
  return [item.href, ...(item.activePaths ?? [])]
    .some((candidate) => pathMatches(pathname, candidate));
}

export function navigationGroups(t: Translate): NavigationGroup[] {
  return [
    {
      label: t('Chat'),
      items: [
        { label: t('Playground'), href: '/playground', section: 'chat', module: 'playground' },
        {
          label: t('Open chat'),
          href: '/chat2link',
          activePaths: ['/chat/*'],
          section: 'chat',
          module: 'chat',
        },
      ],
    },
    {
      label: t('Dashboard'),
      items: [
        {
          label: t('Overview'),
          href: '/dashboard/overview',
          section: 'console',
          module: 'detail',
        },
        {
          label: t('Dashboard'),
          href: '/dashboard/models',
          activePaths: ['/dashboard/flow', '/dashboard/users'],
          section: 'console',
          module: 'detail',
        },
        { label: t('API keys'), href: '/keys', section: 'console', module: 'token' },
        {
          label: t('Usage Logs'),
          href: '/usage-logs/common',
          section: 'console',
          module: 'log',
        },
        {
          label: t('Task history'),
          href: '/usage-logs/task',
          activePaths: ['/usage-logs/drawing'],
          section: 'console',
          module: 'task',
        },
      ],
    },
    {
      label: t('Personal use'),
      items: [
        { label: t('Wallet'), href: '/wallet', section: 'personal', module: 'topup' },
        {
          label: t('Profile and security'),
          href: '/profile',
          section: 'personal',
          module: 'personal',
        },
      ],
    },
    {
      label: t('Administrator'),
      items: [
        {
          label: t('Channels'),
          href: '/channels',
          minimumRole: 10,
          section: 'admin',
          module: 'channel',
        },
        {
          label: t('Models'),
          href: '/models/metadata',
          activePaths: ['/models/*'],
          minimumRole: 10,
          section: 'admin',
          module: 'models',
        },
        {
          label: t('Users'),
          href: '/users',
          minimumRole: 10,
          section: 'admin',
          module: 'user',
        },
        {
          label: t('Redemption codes'),
          href: '/redemption-codes',
          minimumRole: 10,
          section: 'admin',
          module: 'redemption',
        },
        {
          label: t('Subscriptions'),
          href: '/subscriptions',
          minimumRole: 10,
          section: 'admin',
          module: 'subscription',
        },
        {
          label: t('System information'),
          href: '/system-info',
          minimumRole: 100,
          section: 'unconfigured',
          module: 'system-info',
        },
        {
          label: t('System settings'),
          href: '/system-settings/site',
          activePaths: ['/system-settings/*'],
          minimumRole: 100,
          section: 'admin',
          module: 'setting',
        },
      ],
    },
  ];
}

export function visibleNavigationGroups(
  t: Translate,
  role: number,
  adminRaw: unknown,
  userRaw?: unknown,
  permissions?: UserNavigationPermissions,
): NavigationGroup[] {
  const adminModules = parseSidebarModules(adminRaw);
  const userModules = permissions?.sidebar_settings === false
    ? null
    : parseSidebarModules(userRaw);
  return navigationGroups(t)
    .map((group) => ({
      ...group,
      items: group.items.filter((item) => isNavigationItemVisible(
        item,
        role,
        adminModules,
        userModules,
      )),
    }))
    .filter((group) => group.items.length > 0);
}
