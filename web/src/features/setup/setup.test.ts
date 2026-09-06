import { describe, expect, it } from 'vitest';
import { buildSetupPayload, initialSetupUsageMode, parseSetupStatus } from './setup';

describe('setup contract helpers', () => {
  it('accepts both the reference status fields and the established setup_required alias', () => {
    expect(parseSetupStatus({
      setup_required: true,
      status: false,
      root_init: true,
      database_type: 'postgres',
      SelfUseModeEnabled: true,
      DemoSiteEnabled: false,
      site_name: 'Example',
      version: '1.2.3',
    })).toEqual({
      setupRequired: true,
      completed: false,
      rootInitialized: true,
      databaseType: 'postgres',
      selfUseModeEnabled: true,
      demoSiteEnabled: false,
      siteName: 'Example',
      version: '1.2.3',
    });
    expect(parseSetupStatus({ setup_required: false }).completed).toBe(true);
    expect(parseSetupStatus({ status: false }).setupRequired).toBe(true);
  });

  it('rejects contradictory, unbounded, and type-confused public status data', () => {
    for (const value of [
      null,
      {},
      { setup_required: 'true' },
      { setup_required: true, status: true },
      { status: false, root_init: 1 },
      { status: false, database_type: 'x'.repeat(129) },
      { status: false, SelfUseModeEnabled: true, DemoSiteEnabled: true },
    ]) {
      expect(() => parseSetupStatus(value)).toThrow('Invalid setup response');
    }
  });

  it('builds the exact conditional request payload without retaining credentials for an existing root', () => {
    const values = {
      username: '  root-user  ',
      password: 'password1',
      confirmPassword: 'password1',
      usageMode: 'demo' as const,
    };
    expect(buildSetupPayload(values, false)).toEqual({
      username: 'root-user',
      password: 'password1',
      confirmPassword: 'password1',
      SelfUseModeEnabled: false,
      DemoSiteEnabled: true,
    });
    expect(buildSetupPayload(values, true)).toEqual({
      SelfUseModeEnabled: false,
      DemoSiteEnabled: true,
    });
    expect(initialSetupUsageMode(parseSetupStatus({ status: false, DemoSiteEnabled: true }))).toBe('demo');
  });
});

