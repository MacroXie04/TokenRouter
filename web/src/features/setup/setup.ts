export type SetupUsageMode = 'external' | 'self' | 'demo';

export interface SetupStatus {
  setupRequired: boolean;
  completed: boolean;
  rootInitialized: boolean;
  databaseType: string;
  selfUseModeEnabled: boolean;
  demoSiteEnabled: boolean;
  siteName: string;
  version: string;
}

export interface SetupFormValues {
  username: string;
  password: string;
  confirmPassword: string;
  usageMode: SetupUsageMode;
}

const MAX_SETUP_TEXT_LENGTH = 128;

function boundedString(value: unknown, fallback: string): string {
  if (value === undefined) return fallback;
  if (typeof value !== 'string' || value.length > MAX_SETUP_TEXT_LENGTH) {
    throw new Error('Invalid setup response');
  }
  return value;
}

function optionalBoolean(value: unknown, fallback = false): boolean {
  if (value === undefined) return fallback;
  if (typeof value !== 'boolean') throw new Error('Invalid setup response');
  return value;
}

export function parseSetupStatus(value: unknown): SetupStatus {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('Invalid setup response');
  }
  const source = value as Record<string, unknown>;
  const hasRequired = source.setup_required !== undefined;
  const hasStatus = source.status !== undefined;
  if (!hasRequired && !hasStatus) throw new Error('Invalid setup response');

  const setupRequired = optionalBoolean(source.setup_required, !optionalBoolean(source.status));
  const completed = optionalBoolean(source.status, !setupRequired);
  if (setupRequired === completed) throw new Error('Invalid setup response');

  const selfUseModeEnabled = optionalBoolean(source.SelfUseModeEnabled);
  const demoSiteEnabled = optionalBoolean(source.DemoSiteEnabled);
  if (selfUseModeEnabled && demoSiteEnabled) throw new Error('Invalid setup response');

  return {
    setupRequired,
    completed,
    rootInitialized: optionalBoolean(source.root_init),
    databaseType: boundedString(source.database_type, 'unknown').trim() || 'unknown',
    selfUseModeEnabled,
    demoSiteEnabled,
    siteName: boundedString(source.site_name, 'TokenRouter'),
    version: boundedString(source.version, ''),
  };
}

export function initialSetupUsageMode(status: SetupStatus): SetupUsageMode {
  if (status.selfUseModeEnabled) return 'self';
  if (status.demoSiteEnabled) return 'demo';
  return 'external';
}

export function buildSetupPayload(values: SetupFormValues, rootInitialized: boolean) {
  const modes = {
    SelfUseModeEnabled: values.usageMode === 'self',
    DemoSiteEnabled: values.usageMode === 'demo',
  };
  if (rootInitialized) return modes;
  return {
    username: values.username.trim(),
    password: values.password,
    confirmPassword: values.confirmPassword,
    ...modes,
  };
}

