import { describe, expect, it } from 'vitest';
import {
  SYSTEM_SETTINGS_CATEGORIES,
  SYSTEM_SETTING_KEYS,
  resolveSettingsPath,
  validateSettingValue,
  type SettingDefinition,
  type SettingsCategoryDefinition,
} from './settings-catalog';

const EXPECTED_ROUTES: Record<string, string[]> = {
  site: ['system-info', 'notice', 'header-navigation', 'sidebar-modules'],
  auth: ['basic-auth', 'oauth', 'passkey', 'bot-protection', 'custom-oauth'],
  billing: ['quota', 'currency', 'model-pricing', 'group-pricing', 'payment', 'checkin'],
  models: ['global', 'routing-reliability', 'gemini', 'claude', 'grok', 'channel-affinity', 'model-deployment'],
  security: ['rate-limit', 'sensitive-words', 'ssrf', 'token-limits'],
  content: ['dashboard', 'announcements', 'api-info', 'faq', 'uptime-kuma', 'chat', 'drawing'],
  operations: ['behavior', 'alerts', 'email', 'worker', 'logs', 'performance', 'update-checker'],
};

describe('system settings catalog', () => {
  const catalog: readonly SettingsCategoryDefinition[] = SYSTEM_SETTINGS_CATEGORIES;

  it('matches every reference category and section in stable order', () => {
    expect(Object.fromEntries(catalog.map((category) => [
      category.id,
      category.sections.map((section) => section.id),
    ]))).toEqual(EXPECTED_ROUTES);
  });

  it('contains no duplicate option writers and marks contract gaps unavailable', () => {
    const keys = catalog.flatMap((category) => category.sections)
      .flatMap((section) => section.groups)
      .flatMap((group) => group.settings)
      .map((setting) => setting.key);
    expect(SYSTEM_SETTING_KEYS.size).toBe(keys.length);
    expect([...SYSTEM_SETTING_KEYS]).toEqual(keys);
    expect(keys.filter((key) => key === 'QuotaForInviter')).toHaveLength(1);

    const unavailable = catalog.flatMap((category) => category.sections)
      .filter((section) => section.unavailableReason);
    expect(unavailable.length).toBeGreaterThan(0);
    unavailable.forEach((section) => expect(section.groups).toHaveLength(0));

    const operations = catalog.find((category) => category.id === 'operations')!;
    expect(operations.sections.find((section) => section.id === 'logs')?.unavailableReason).toBeUndefined();
    expect(operations.sections.find((section) => section.id === 'update-checker')?.unavailableReason).toBeUndefined();
    expect(operations.sections.find((section) => section.id === 'worker')?.unavailableReason).toBeTruthy();
    expect(catalog.find((category) => category.id === 'security')
      ?.sections.find((section) => section.id === 'ssrf')?.unavailableReason).toBeTruthy();
  });

  it('fails closed to a known default for malformed paths', () => {
    expect(resolveSettingsPath('auth/passkey').section.id).toBe('passkey');
    expect(resolveSettingsPath('auth/not-real').section.id).toBe('basic-auth');
    expect(resolveSettingsPath('not-real/anything').category.id).toBe('site');
    expect(resolveSettingsPath('security/ssrf/extra').section.id).toBe('rate-limit');
  });

  it('enforces byte, numeric, JSON, select, boolean, and Unicode bounds', () => {
    const base = { key: 'test', label: 'Test' };
    const definitions: Record<string, SettingDefinition> = {
      text: { ...base, kind: 'text', maxLength: 4 },
      integer: { ...base, kind: 'integer', min: 1, max: 3 },
      decimal: { ...base, kind: 'decimal', min: 0.1, max: 2 },
      json: { ...base, kind: 'json' },
      select: { ...base, kind: 'select', choices: [{ value: 'one', label: 'One' }] },
      boolean: { ...base, kind: 'boolean' },
      secret: { ...base, kind: 'secret' },
    };

    expect(validateSettingValue(definitions.text, '🙂')).toBeNull();
    expect(validateSettingValue(definitions.text, '🙂a')).toMatch(/too long/u);
    expect(validateSettingValue(definitions.text, '\u2066unsafe')).toMatch(/invalid/u);
    expect(validateSettingValue(definitions.text, String.fromCharCode(0xd800))).toMatch(/invalid/u);
    expect(validateSettingValue(definitions.integer, '2')).toBeNull();
    expect(validateSettingValue(definitions.integer, '2.5')).toBe('Enter a whole number.');
    expect(validateSettingValue(definitions.integer, '4')).toMatch(/allowed range/u);
    expect(validateSettingValue(definitions.decimal, '1.25')).toBeNull();
    expect(validateSettingValue(definitions.json, '{bad')).toBe('Enter valid JSON.');
    expect(validateSettingValue(definitions.select, 'two')).toBe('Choose a valid option.');
    expect(validateSettingValue(definitions.boolean, 'yes')).toBe('Choose a valid option.');
    expect(validateSettingValue(definitions.secret, '')).toBe('Enter a replacement value.');
  });

  it('exposes backend-backed built-in authentication, wallet, and console-content controls', () => {
    [
      'EmailDomainRestrictionEnabled',
      'EmailAliasRestrictionEnabled',
      'EmailDomainWhitelist',
      'GitHubOAuthEnabled',
      'GitHubClientId',
      'GitHubClientSecret',
      'discord.enabled',
      'discord.client_id',
      'discord.client_secret',
      'oidc.enabled',
      'oidc.display_name',
      'oidc.client_id',
      'oidc.client_secret',
      'oidc.authorization_endpoint',
      'oidc.token_endpoint',
      'oidc.user_info_endpoint',
      'LinuxDOOAuthEnabled',
      'LinuxDOClientId',
      'LinuxDOClientSecret',
      'LinuxDOMinimumTrustLevel',
      'TelegramOAuthEnabled',
      'TelegramBotName',
      'TelegramBotToken',
      'passkey.enabled',
      'passkey.rp_display_name',
      'passkey.rp_id',
      'passkey.origins',
      'passkey.allow_insecure_origin',
      'passkey.user_verification',
      'passkey.attachment_preference',
      'WaffoSandboxApiKey',
      'WaffoSandboxPrivateKey',
      'WaffoSandboxPublicCert',
      'console_setting.announcements_enabled',
      'console_setting.announcements',
      'console_setting.api_info_enabled',
      'console_setting.api_info',
      'console_setting.faq_enabled',
      'console_setting.faq',
      'console_setting.uptime_kuma_enabled',
      'console_setting.uptime_kuma_groups',
      'tool_price_setting.prices',
      'DefaultCollapseSidebar',
      'AutomaticDisableChannelEnabled',
      'AutomaticEnableChannelEnabled',
      'ChannelDisableThreshold',
      'AutomaticDisableKeywords',
      'AutomaticDisableStatusCodes',
      'AutomaticRetryStatusCodes',
      'monitor_setting.auto_test_channel_enabled',
      'monitor_setting.auto_test_channel_minutes',
      'monitor_setting.channel_test_mode',
      'QuotaForNewUser',
      'DefaultUseAutoGroup',
      'group_ratio_setting.group_special_usable_group',
    ].forEach((key) => expect(SYSTEM_SETTING_KEYS.has(key)).toBe(true));

    expect(SYSTEM_SETTING_KEYS.has('WebAuthnRPDisplayName')).toBe(false);
    expect(SYSTEM_SETTING_KEYS.has('WebAuthnRPID')).toBe(false);
    expect(SYSTEM_SETTING_KEYS.has('DiscordOAuthEnabled')).toBe(false);
    expect(SYSTEM_SETTING_KEYS.has('OIDCEnabled')).toBe(false);
    expect(SYSTEM_SETTING_KEYS.has('InitialQuota')).toBe(false);
  });

  it('validates authentication lists and endpoints before transport', () => {
    const definitions = catalog.flatMap((category) => category.sections)
      .flatMap((section) => section.groups)
      .flatMap((group) => group.settings);
    const setting = (key: string) => definitions.find((candidate) => candidate.key === key)!;

    expect(validateSettingValue(setting('EmailDomainWhitelist'), 'example.com\nlogin.example.com')).toBeNull();
    expect(validateSettingValue(setting('EmailDomainWhitelist'), 'Example.com,example.com'))
      .toBe('Enter a valid list of unique email domains.');
    expect(validateSettingValue(setting('EmailDomainWhitelist'), '127.0.0.1'))
      .toBe('Enter a valid list of unique email domains.');

    expect(validateSettingValue(setting('oidc.authorization_endpoint'), 'https://identity.example.test/authorize?tenant=one')).toBeNull();
    for (const endpoint of [
      'http://identity.example.test/authorize',
      'https://127.0.0.1/authorize',
      'https://identity.example.test/a/../authorize',
      'https://identity.example.test/%2e%2e/authorize',
      'https://identity.example.test/authorize?tenant=one&tenant=two',
    ]) {
      expect(validateSettingValue(setting('oidc.authorization_endpoint'), endpoint), endpoint)
        .toBe('Enter a safe HTTPS OAuth endpoint.');
    }

    expect(validateSettingValue(setting('passkey.origins'), 'https://example.test\nhttps://login.example.test:8443')).toBeNull();
    expect(validateSettingValue(setting('passkey.origins'), 'https://example.test/path'))
      .toBe('Enter a valid list of unique passkey origins.');
  });

  it('exposes bounded billing controls and rejects ambiguous payment configuration', () => {
    const billing = catalog.find((category) => category.id === 'billing')!;
    const definitions = billing.sections.flatMap((section) => section.groups)
      .flatMap((group) => group.settings);
    const setting = (key: string) => definitions.find((candidate) => candidate.key === key)!;

    for (const key of [
      'QuotaForNewUser', 'PreConsumedQuota', 'QuotaForInviter', 'QuotaForInvitee',
      'checkin_setting.min_quota', 'checkin_setting.max_quota',
    ]) {
      expect(setting(key)).toMatchObject({ kind: 'integer', max: 2_147_483_647 });
      expect(validateSettingValue(setting(key), '2147483647')).toBeNull();
      expect(validateSettingValue(setting(key), '2147483648')).toBe('Enter a value within the allowed range.');
    }

    for (const key of ['MinTopUp', 'StripeMinTopUp', 'WaffoMinTopUp']) {
      expect(setting(key)).toMatchObject({ kind: 'integer', max: 4_294 });
      expect(validateSettingValue(setting(key), '4294')).toBeNull();
      expect(validateSettingValue(setting(key), '4295')).toBe('Enter a value within the allowed range.');
    }

    for (const key of ['Price', 'StripeUnitPrice', 'WaffoUnitPrice']) {
      expect(setting(key)).toMatchObject({ kind: 'decimal', max: 999_999.99 });
      expect(validateSettingValue(setting(key), '999999.99')).toBeNull();
      expect(validateSettingValue(setting(key), '1000000')).toBe('Enter a value within the allowed range.');
    }

    expect(setting('QuotaPerUnit')).toMatchObject({ fixedValue: '500000' });
    expect(setting('QuotaDisplayType').choices).toContainEqual({ value: 'CUSTOM', label: 'Custom currency' });
    expect(validateSettingValue(setting('general_setting.custom_currency_symbol'), 'HK$')).toBeNull();
    expect(validateSettingValue(setting('general_setting.custom_currency_symbol'), '123456789'))
      .toBe('Enter a short currency symbol.');

    const ratios = setting('TopupGroupRatio');
    expect(ratios).toMatchObject({ kind: 'json', format: 'topup-group-ratios', maxLength: 64 * 1024 });
    expect(validateSettingValue(ratios, '{"default":1,"vip":1.25}')).toBeNull();
    for (const candidate of [
      '{}', '{"default":0}', '{"default":1e309}',
      '{"vip":1,"vip":2}', String.raw`{"vip":1,"\u0076ip":2}`,
    ]) {
      expect(validateSettingValue(ratios, candidate), candidate)
        .toBe('Enter valid bounded top-up group ratios.');
    }

    const paymentSetting = setting('PaymentSetting');
    expect(validateSettingValue(paymentSetting, '{"amount_options":[10,20],"amount_discount":{"10":0.9}}')).toBeNull();
    for (const candidate of [
      '{"amount_options":[10,10]}',
      '{"amount_options":[2147483648]}',
      '{"amount_options":[10],"amount_discount":{"10":0.9,"10":0.8}}',
      '{"amount_discount":{"10":0}}',
      '{"amount_discount":{"10":1.01}}',
    ]) {
      expect(validateSettingValue(paymentSetting, candidate), candidate)
        .toBe('Enter valid bounded payment settings.');
    }

    const methods = setting('PayMethods');
    expect(validateSettingValue(methods, '[{"name":"Card","type":"card"}]')).toBeNull();
    expect(validateSettingValue(methods, '[{"name":"Card","type":"card"},{"name":"Other","type":"card"}]'))
      .toBe('Enter valid bounded payment methods.');

    expect(validateSettingValue(setting('TopUpLink'), 'https://billing.example.test/codes?from=wallet')).toBeNull();
    expect(validateSettingValue(setting('TopUpLink'), 'javascript:alert(1)')).toBe('Enter a valid billing URL.');
    expect(validateSettingValue(setting('PayAddress'), 'http://localhost:8080/epay')).toBeNull();
    expect(validateSettingValue(setting('PayAddress'), 'https://pay.example.test/epay?token=secret'))
      .toBe('Enter a valid billing URL.');
    expect(validateSettingValue(setting('StripeCurrency'), 'USD')).toBeNull();
    expect(validateSettingValue(setting('StripeCurrency'), 'US1')).toBe('Three-letter ISO currency code.');
  });

  it('exposes canonical registration defaults and validates special group directives', () => {
    const billing = catalog.find((category) => category.id === 'billing')!;
    const quotaSettings = billing.sections.find((section) => section.id === 'quota')!.groups
      .flatMap((group) => group.settings);
    expect(quotaSettings.find((setting) => setting.key === 'QuotaForNewUser')).toMatchObject({
      kind: 'integer', min: 0, max: 2_147_483_647, defaultValue: '0',
    });
    expect(quotaSettings.some((setting) => setting.key === 'InitialQuota')).toBe(false);

    const globalSettings = catalog.find((category) => category.id === 'models')!.sections
      .find((section) => section.id === 'global')!.groups.flatMap((group) => group.settings);
    expect(globalSettings.find((setting) => setting.key === 'DefaultUseAutoGroup')).toMatchObject({
      kind: 'boolean', defaultValue: 'false',
    });

    const groupSettings = billing.sections.find((section) => section.id === 'group-pricing')!.groups
      .flatMap((group) => group.settings);
    const directives = groupSettings.find((setting) => setting.key === 'group_ratio_setting.group_special_usable_group')!;
    expect(directives).toMatchObject({
      kind: 'json', format: 'registration-group-directives', maxLength: 64 * 1024, defaultValue: '{}',
    });
    expect(validateSettingValue(directives, '{"default":{"+:vip":"VIP","-:blocked":""}}')).toBeNull();
    expect(validateSettingValue(directives, '{"default":{"vip":"one","vip":"two"}}'))
      .toBe('Enter valid bounded special usable-group directives.');
    expect(validateSettingValue(directives, '{"default":{"vip":"one","+:vip":"two"}}'))
      .toBe('Enter valid bounded special usable-group directives.');
  });

  it('exposes backed auxiliary usage ratios and free-model funding policy', () => {
    const section = catalog.find((category) => category.id === 'billing')!.sections
      .find((candidate) => candidate.id === 'model-pricing')!;
    const settings = section.groups.flatMap((group) => group.settings);
    const setting = (key: string) => settings.find((candidate) => candidate.key === key)!;

    for (const key of [
      'CacheRatio',
      'CreateCacheRatio',
      'ImageRatio',
      'AudioRatio',
      'AudioCompletionRatio',
    ]) {
      expect(setting(key)).toMatchObject({
        kind: 'json', format: 'usage-ratio-map', maxLength: 1024 * 1024,
      });
      expect(validateSettingValue(setting(key), '{"model":0,"model-*":1.25}')).toBeNull();
      expect(validateSettingValue(setting(key), '{"model":1,"model":2}'))
        .toBe('Enter a valid bounded non-negative ratio map.');
      expect(validateSettingValue(setting(key), String.raw`{"model":1,"\u006dodel":2}`))
        .toBe('Enter a valid bounded non-negative ratio map.');
      expect(validateSettingValue(setting(key), '{"model":-1}'))
        .toBe('Enter a valid bounded non-negative ratio map.');
      expect(validateSettingValue(setting(key), '{"model":1e309}'))
        .toBe('Enter a valid bounded non-negative ratio map.');
    }

    expect(setting('quota_setting.enable_free_model_pre_consume')).toMatchObject({
      kind: 'boolean', defaultValue: 'true', required: true,
    });
    expect(validateSettingValue(setting('quota_setting.enable_free_model_pre_consume'), 'true')).toBeNull();
    expect(validateSettingValue(setting('quota_setting.enable_free_model_pre_consume'), 'false')).toBeNull();
    expect(validateSettingValue(setting('quota_setting.enable_free_model_pre_consume'), ''))
      .toBe('Choose a valid option.');

    const toolPrices = setting('tool_price_setting.prices');
    expect(toolPrices).toMatchObject({ kind: 'json', format: 'tool-price-map', required: true });
    expect(validateSettingValue(toolPrices, '{"web_search":10,"web_search:gpt-4o-*":0}')).toBeNull();
    expect(validateSettingValue(toolPrices, '{"web_search":1,"web_search":2}'))
      .toBe('Enter valid bounded tool prices.');
    expect(validateSettingValue(toolPrices, '{"web_search":1e2147483647}'))
      .toBe('Enter valid bounded tool prices.');
  });

  it('exposes fail-safe channel reliability and operations settings', () => {
    const models = catalog.find((category) => category.id === 'models')!;
    const reliability = models.sections.find((section) => section.id === 'routing-reliability')!;
    const settings = reliability.groups.flatMap((group) => group.settings);
    const setting = (key: string) => settings.find((candidate) => candidate.key === key)!;

    expect(setting('RetryTimes')).toMatchObject({ min: 0, max: 10, defaultValue: '0' });
    expect(setting('AutomaticDisableChannelEnabled')).toMatchObject({ kind: 'boolean', defaultValue: 'false' });
    expect(setting('AutomaticEnableChannelEnabled')).toMatchObject({ kind: 'boolean', defaultValue: 'false' });
    expect(setting('monitor_setting.auto_test_channel_enabled')).toMatchObject({ kind: 'boolean', defaultValue: 'false' });
    expect(setting('monitor_setting.auto_test_channel_minutes')).toMatchObject({ min: 1, max: 525_600, defaultValue: '10' });
    expect(setting('monitor_setting.channel_test_mode').choices?.map((choice) => choice.value)).toEqual([
      'scheduled_all', 'auto_ban_only', 'passive_recovery',
    ]);
    expect(validateSettingValue(setting('AutomaticRetryStatusCodes'), '401,409-499,500-503')).toBeNull();
    expect(validateSettingValue(setting('AutomaticRetryStatusCodes'), '99,500-400'))
      .toBe('Enter valid HTTP status codes or ranges.');
    expect(validateSettingValue(setting('AutomaticDisableKeywords'), 'Permission denied\nQuota exceeded')).toBeNull();
    expect(validateSettingValue(setting('AutomaticDisableKeywords'), 'bad\tkeyword'))
      .toBe('Enter valid bounded channel-disable keywords.');

    const operations = catalog.find((category) => category.id === 'operations')!;
    expect(operations.sections.find((section) => section.id === 'email')?.unavailableReason).toBeUndefined();
    expect(operations.sections.find((section) => section.id === 'behavior')?.groups[0].settings)
      .toContainEqual(expect.objectContaining({ key: 'DefaultCollapseSidebar', defaultValue: 'false' }));
  });

  it('exposes bounded model request-rate controls and rejects ambiguous group JSON', () => {
    const section = catalog.find((category) => category.id === 'security')!.sections
      .find((candidate) => candidate.id === 'rate-limit')!;
    const settings = section.groups.flatMap((group) => group.settings);

    expect(section.unavailableReason).toBeUndefined();
    expect(settings.map((setting) => setting.key)).toEqual([
      'ModelRequestRateLimitEnabled',
      'ModelRequestRateLimitDurationMinutes',
      'ModelRequestRateLimitCount',
      'ModelRequestRateLimitSuccessCount',
      'ModelRequestRateLimitGroup',
    ]);
    expect(settings.find((setting) => setting.key === 'ModelRequestRateLimitDurationMinutes')).toMatchObject({
      kind: 'integer', min: 1, max: 43_200, defaultValue: '1',
    });
    const groups = settings.find((setting) => setting.key === 'ModelRequestRateLimitGroup')!;
    expect(validateSettingValue(groups, '{"default":[0,1000],"vip":[50,40]}')).toBeNull();
    expect(validateSettingValue(groups, '{"vip":[1,2],"vip":[3,4]}'))
      .toBe('Enter valid bounded model request group limits.');
    expect(validateSettingValue(groups, '{"vip":[1e2,2]}'))
      .toBe('Enter valid bounded model request group limits.');
  });

  it('validates each structured console-content option before transport', () => {
    const definitions = catalog.flatMap((category) => category.sections)
      .flatMap((section) => section.groups)
      .flatMap((group) => group.settings);
    const setting = (key: string) => definitions.find((candidate) => candidate.key === key)!;

    expect(validateSettingValue(setting('console_setting.api_info'), JSON.stringify([{
      url: 'https://api.example.test/v1', route: 'Primary', description: 'Main route', color: 'green',
    }]))).toBeNull();
    expect(validateSettingValue(setting('console_setting.api_info'), JSON.stringify([{
      url: 'javascript:alert(1)', route: 'Primary', description: 'Main route', color: 'green',
    }]))).toBe('Enter API information as a valid bounded JSON array.');

    expect(validateSettingValue(setting('console_setting.faq'), JSON.stringify([{
      question: 'How?', answer: 'Create a token.',
    }]))).toBeNull();
    expect(validateSettingValue(setting('console_setting.faq'), '[{"question":"Missing answer"}]'))
      .toBe('Enter FAQ entries as a valid bounded JSON array.');

    expect(validateSettingValue(setting('console_setting.uptime_kuma_groups'), JSON.stringify([{
      categoryName: 'Core', url: 'https://status.example.test', slug: 'public-status',
    }]))).toBeNull();
    expect(validateSettingValue(setting('console_setting.uptime_kuma_groups'), JSON.stringify([{
      categoryName: 'Core', url: 'https://status.example.test?token=x', slug: 'bad/slug',
    }]))).toBe('Enter Uptime Kuma groups as a valid bounded JSON array.');
  });

  it('exposes backed Gemini, Claude, and Grok policy controls with strict local validation', () => {
    const modelSections = catalog.find((category) => category.id === 'models')!.sections;
    const settingsFor = (sectionId: string) => modelSections.find((section) => section.id === sectionId)!.groups
      .flatMap((group) => group.settings);
    const gemini = settingsFor('gemini');
    const claude = settingsFor('claude');
    const grok = settingsFor('grok');
    expect(gemini.map((setting) => setting.key)).toEqual([
      'gemini.safety_settings',
      'gemini.version_settings',
      'gemini.supported_imagine_models',
      'gemini.thinking_adapter_enabled',
      'gemini.thinking_adapter_budget_tokens_percentage',
      'gemini.function_call_thought_signature_enabled',
      'gemini.remove_function_response_id_enabled',
    ]);
    expect(claude.map((setting) => setting.key)).toEqual([
      'claude.model_headers_settings',
      'claude.default_max_tokens',
      'claude.thinking_adapter_enabled',
      'claude.thinking_adapter_budget_tokens_percentage',
    ]);
    expect(grok.map((setting) => setting.key)).toEqual([
      'grok.violation_deduction_enabled',
      'grok.violation_deduction_amount',
    ]);
    expect(modelSections.find((section) => section.id === 'gemini')!.unavailableReason).toBeUndefined();
    expect(modelSections.find((section) => section.id === 'claude')!.unavailableReason).toBeUndefined();
    expect(modelSections.find((section) => section.id === 'grok')!.unavailableReason).toBeUndefined();

    const setting = (key: string) => [...gemini, ...claude, ...grok].find((candidate) => candidate.key === key)!;
    expect(setting('gemini.thinking_adapter_budget_tokens_percentage')).toMatchObject({
      kind: 'decimal', min: 0.002, max: 1, defaultValue: '0.6',
    });
    expect(setting('claude.thinking_adapter_budget_tokens_percentage')).toMatchObject({
      kind: 'decimal', min: 0.1, max: 1, defaultValue: '0.8',
    });
    expect(setting('grok.violation_deduction_enabled')).toMatchObject({
      kind: 'boolean', defaultValue: 'true', required: true,
    });
    expect(setting('grok.violation_deduction_amount')).toMatchObject({
      kind: 'decimal', min: 0, max: 4294.967294, defaultValue: '0.05', required: true,
    });
    expect(validateSettingValue(setting('gemini.safety_settings'), '{"default":"BLOCK_ONLY_HIGH"}')).toBeNull();
    expect(validateSettingValue(setting('gemini.safety_settings'), '{"default":"BLOCK_SOME"}'))
      .toBe('Enter valid bounded Gemini safety settings.');
    expect(validateSettingValue(setting('gemini.version_settings'), '{"default":"v2"}'))
      .toBe('Enter valid bounded Gemini API versions.');
    expect(validateSettingValue(setting('gemini.supported_imagine_models'), '["gemini-a","gemini-a"]'))
      .toBe('Enter a valid bounded list of unique model IDs.');
    expect(validateSettingValue(setting('claude.model_headers_settings'), '{"claude-a":{"Anthropic-Beta":["tools"]}}'))
      .toBeNull();
    expect(validateSettingValue(setting('claude.model_headers_settings'), '{"claude-a":{"Authorization":["secret"]}}'))
      .toBe('Enter valid bounded Claude model headers.');
    expect(validateSettingValue(setting('claude.model_headers_settings'), JSON.stringify({
      'claude-a': { 'Anthropic-Beta': ['line\r\nbreak'] },
    }))).toBe('Enter valid bounded Claude model headers.');
    expect(validateSettingValue(setting('claude.default_max_tokens'), '{"default":8192,"claude-a":4096}')).toBeNull();
    expect(validateSettingValue(setting('claude.default_max_tokens'), '{"default":1.5}'))
      .toBe('Enter valid bounded Claude max-token defaults.');
    expect(validateSettingValue(setting('grok.violation_deduction_enabled'), '')).toBe('Choose a valid option.');
    expect(validateSettingValue(setting('grok.violation_deduction_enabled'), 'yes')).toBe('Choose a valid option.');
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), '0')).toBeNull();
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), '4294.967294')).toBeNull();
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), '')).toBe('Enter a value within the allowed range.');
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), '-0.01')).toBe('Enter a value within the allowed range.');
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), '4294.967295')).toBe('Enter a value within the allowed range.');
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), 'NaN')).toBe('Enter a decimal number.');
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), 'Infinity')).toBe('Enter a decimal number.');
    expect(validateSettingValue(setting('grok.violation_deduction_amount'), '5e-2')).toBe('Enter a decimal number.');
  });

  it('validates Telegram and WebAuthn identifiers before writing options', () => {
    const telegramName: SettingDefinition = {
      key: 'TelegramBotName',
      label: 'Telegram bot username',
      kind: 'text',
      format: 'telegram-bot-name',
    };
    const telegramToken: SettingDefinition = {
      key: 'TelegramBotToken',
      label: 'Telegram bot token',
      kind: 'secret',
      format: 'telegram-bot-token',
    };
    const relyingPartyId: SettingDefinition = {
      key: 'WebAuthnRPID',
      label: 'Relying-party ID',
      kind: 'text',
      format: 'hostname',
    };

    expect(validateSettingValue(telegramName, '@Example_bot')).toBeNull();
    expect(validateSettingValue(telegramName, 'not-a-bot')).toBe('Enter a valid Telegram bot username.');
    expect(validateSettingValue(telegramToken, `12345:${'A'.repeat(20)}`)).toBeNull();
    expect(validateSettingValue(telegramToken, `0:${'A'.repeat(20)}`)).toBe('Enter a valid Telegram bot token.');
    expect(validateSettingValue(relyingPartyId, 'login.example.com')).toBeNull();
    expect(validateSettingValue(relyingPartyId, 'localhost')).toBeNull();
    expect(validateSettingValue(relyingPartyId, '2001:db8::1')).toBeNull();
    expect(validateSettingValue(relyingPartyId, 'internal')).toBe('Enter a valid hostname.');
    expect(validateSettingValue(relyingPartyId, 'https://example.com')).toBe('Enter a valid hostname.');
    expect(validateSettingValue(relyingPartyId, '-bad.example')).toBe('Enter a valid hostname.');
  });
});
