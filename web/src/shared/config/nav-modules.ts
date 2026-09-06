export interface ModuleAccess {
  enabled: boolean;
  requireAuth: boolean;
}

export interface HeaderNavigationModules {
  home?: boolean;
  console?: boolean;
  docs?: boolean;
  about?: boolean;
  pricing: ModuleAccess;
  rankings: ModuleAccess;
}

const DEFAULT_ACCESS: ModuleAccess = { enabled: true, requireAuth: false };

function parseBoolean(value: unknown): boolean | undefined {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number' && (value === 0 || value === 1)) return value === 1;
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase();
    if (normalized === 'true' || normalized === '1') return true;
    if (normalized === 'false' || normalized === '0') return false;
  }
  return undefined;
}

function moduleAccess(value: unknown): ModuleAccess {
  const scalar = parseBoolean(value);
  if (scalar !== undefined) return { enabled: scalar, requireAuth: false };
  if (!value || typeof value !== 'object' || Array.isArray(value)) return { ...DEFAULT_ACCESS };
  const object = value as Record<string, unknown>;
  return {
    enabled: parseBoolean(object.enabled) ?? true,
    requireAuth: parseBoolean(object.requireAuth) ?? false,
  };
}

export function parseHeaderNavigationModules(raw: unknown): HeaderNavigationModules {
  let parsed = raw;
  if (typeof parsed === 'string') {
    if (!parsed.trim()) parsed = {};
    else {
      try {
        parsed = JSON.parse(parsed);
      } catch {
        parsed = {};
      }
    }
  }
  const object = parsed && typeof parsed === 'object' && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : {};
  return {
    home: moduleAccess(object.home).enabled,
    console: moduleAccess(object.console).enabled,
    docs: moduleAccess(object.docs).enabled,
    about: moduleAccess(object.about).enabled,
    pricing: moduleAccess(object.pricing),
    rankings: moduleAccess(object.rankings),
  };
}
