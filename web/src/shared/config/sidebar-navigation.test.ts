import { describe, expect, it } from 'vitest';
import {
  isNavigationActive,
  navigationGroups,
  parseSidebarModules,
  visibleNavigationGroups,
} from './sidebar-navigation';

const t = (key: string) => key;

describe('authenticated navigation policy', () => {
  it('matches exact routes and declared route families without prefix collisions', () => {
    const items = navigationGroups(t).flatMap((group) => group.items);
    const models = items.find((item) => item.href === '/models/metadata')!;
    const keys = items.find((item) => item.href === '/keys')!;

    expect(isNavigationActive('/models/deployments?tab=active', models)).toBe(true);
    expect(isNavigationActive('/models-other', models)).toBe(false);
    expect(isNavigationActive('/keys/', keys)).toBe(true);
    expect(isNavigationActive('/keys/extra', keys)).toBe(false);
  });

  it('fails open for absent or malformed optional sidebar settings', () => {
    expect(parseSidebarModules(undefined)).toBeNull();
    expect(parseSidebarModules('{bad json')).toBeNull();
    expect(parseSidebarModules('true')).toBeNull();
    expect(parseSidebarModules(`{"chat":{"enabled":true,"playground":false}}`)).toEqual({
      chat: { enabled: true, playground: false },
    });
  });

  it('combines role guards with the administrator and user visibility layers', () => {
    const groups = visibleNavigationGroups(
      t,
      10,
      JSON.stringify({ chat: { playground: false }, admin: { channel: false } }),
      {
        personal: { enabled: true, topup: false },
        console: false,
      },
      {
        sidebar_settings: true,
        sidebar_modules: { personal: true, console: true },
      },
    );
    const labels = groups.flatMap((group) => group.items.map((item) => item.label));

    expect(labels).not.toContain('Playground');
    expect(labels).not.toContain('Channels');
    expect(labels).not.toContain('Wallet');
    expect(labels).not.toContain('Overview');
    expect(labels).toContain('Models');
    expect(labels).not.toContain('System information');
  });

  it('ignores a stale user overlay when sidebar settings are unavailable', () => {
    const groups = visibleNavigationGroups(
      t,
      100,
      '',
      { admin: false, personal: false },
      { sidebar_settings: false },
    );
    const labels = groups.flatMap((group) => group.items.map((item) => item.label));

    expect(labels).toContain('Wallet');
    expect(labels).toContain('System settings');
  });
});
