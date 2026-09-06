import { describe, expect, it } from 'vitest';

import { SETTINGS_GROUPS, SYSTEM_SETTINGS_SECTIONS, settingsGroupsFor } from './settings-groups';

describe('SETTINGS_GROUPS', () => {
  it('keeps group names and setting keys unique', () => {
    const groupNames = SETTINGS_GROUPS.map((group) => group.name);
    const settingKeys = SETTINGS_GROUPS.flatMap((group) =>
      group.settings.map((setting) => setting.key),
    );

    expect(new Set(groupNames).size).toBe(groupNames.length);
    expect(new Set(settingKeys).size).toBe(settingKeys.length);
  });

  it('retains the operational settings surfaced by the root console', () => {
    const keys = new Set(
      SETTINGS_GROUPS.flatMap((group) => group.settings.map((setting) => setting.key)),
    );

    expect(keys.has('SystemName')).toBe(true);
    expect(keys.has('HeaderNavModules')).toBe(true);
    expect(keys.has('SidebarModulesAdmin')).toBe(true);
    expect(keys.has('legal.user_agreement')).toBe(true);
    expect(keys.has('legal.privacy_policy')).toBe(true);
    expect(keys.has('ModelPrice')).toBe(true);
    expect(keys.has('GroupGroupRatio')).toBe(true);
    expect(keys.has('RetryTimes')).toBe(true);
    expect(keys.has('UserUsableGroups')).toBe(true);
    expect(keys.has('AutoGroups')).toBe(true);
    expect(keys.has('MaxTokenAutoGroups')).toBe(true);
    expect(keys.has('perf_metrics_setting.enabled')).toBe(true);
    expect(keys.has('perf_metrics_setting.flush_interval')).toBe(true);
    expect(keys.has('perf_metrics_setting.bucket_time')).toBe(true);
    expect(keys.has('perf_metrics_setting.retention_days')).toBe(true);
    for (const key of [
      'channel_affinity_setting.enabled',
      'channel_affinity_setting.switch_on_success',
      'channel_affinity_setting.keep_on_channel_disabled',
      'channel_affinity_setting.max_entries',
      'channel_affinity_setting.default_ttl_seconds',
      'channel_affinity_setting.rules',
    ]) {
      expect(keys.has(key), `missing ${key}`).toBe(true);
    }
  });

  it('pins the reference category and section registry and locates groups by route', () => {
    expect(SYSTEM_SETTINGS_SECTIONS).toEqual({
      site: ['system-info', 'notice', 'header-navigation', 'sidebar-modules'],
      auth: ['basic-auth', 'oauth', 'passkey', 'bot-protection', 'custom-oauth'],
      billing: ['quota', 'currency', 'model-pricing', 'group-pricing', 'payment', 'checkin'],
      models: ['global', 'routing-reliability', 'gemini', 'claude', 'grok', 'channel-affinity', 'model-deployment'],
      security: ['rate-limit', 'sensitive-words', 'ssrf', 'token-limits'],
      content: ['dashboard', 'announcements', 'api-info', 'faq', 'uptime-kuma', 'chat', 'drawing'],
      operations: ['behavior', 'alerts', 'email', 'worker', 'logs', 'performance', 'update-checker'],
    });
    expect(settingsGroupsFor('auth', 'passkey').flatMap((group) => group.settings.map((setting) => setting.key)))
      .toEqual(['passkey.enabled']);
    expect(settingsGroupsFor('security', 'ssrf')).toEqual([]);
  });
});
