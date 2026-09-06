export type SettingInputKind = 'text' | 'textarea' | 'boolean' | 'number' | 'json' | 'secret';

export interface SettingDef {
  key: string;
  label: string;
  kind?: SettingInputKind;
}

export const SYSTEM_SETTINGS_SECTIONS = {
  site: ['system-info', 'notice', 'header-navigation', 'sidebar-modules'],
  auth: ['basic-auth', 'oauth', 'passkey', 'bot-protection', 'custom-oauth'],
  billing: ['quota', 'currency', 'model-pricing', 'group-pricing', 'payment', 'checkin'],
  models: ['global', 'routing-reliability', 'gemini', 'claude', 'grok', 'channel-affinity', 'model-deployment'],
  security: ['rate-limit', 'sensitive-words', 'ssrf', 'token-limits'],
  content: ['dashboard', 'announcements', 'api-info', 'faq', 'uptime-kuma', 'chat', 'drawing'],
  operations: ['behavior', 'alerts', 'email', 'worker', 'logs', 'performance', 'update-checker'],
} as const;

export type SystemSettingsCategory = keyof typeof SYSTEM_SETTINGS_SECTIONS;

export const SYSTEM_SETTINGS_CATEGORY_LABELS: Record<SystemSettingsCategory, string> = {
  site: 'Site & Branding',
  auth: 'Authentication',
  billing: 'Billing & Payment',
  models: 'Models & Routing',
  security: 'Security & Limits',
  content: 'Console Content',
  operations: 'Operations',
};

export interface SettingGroup {
  name: string;
  category: SystemSettingsCategory;
  section: string;
  settings: SettingDef[];
}

// Every key below is consumed by the backend. Section metadata lets the
// compact editor honor the reference route hierarchy without coupling option
// storage to a particular React router implementation.
export const SETTINGS_GROUPS: SettingGroup[] = [
  {
    name: 'System information', category: 'site', section: 'system-info',
    settings: [
      { key: 'SystemName', label: 'Site name' },
      { key: 'Logo', label: 'Logo URL' },
      { key: 'Footer', label: 'Footer HTML', kind: 'textarea' },
      { key: 'ServerAddress', label: 'Server address' },
      { key: 'About', label: 'About', kind: 'textarea' },
      { key: 'legal.user_agreement', label: 'User agreement (Markdown, HTML, or URL)', kind: 'textarea' },
      { key: 'legal.privacy_policy', label: 'Privacy policy (Markdown, HTML, or URL)', kind: 'textarea' },
    ],
  },
  {
    name: 'Notice', category: 'site', section: 'notice',
    settings: [{ key: 'Notice', label: 'Notice', kind: 'textarea' }],
  },
  {
    name: 'Header navigation', category: 'site', section: 'header-navigation',
    settings: [{ key: 'HeaderNavModules', label: 'Public module access (JSON)', kind: 'json' }],
  },
  {
    name: 'Administrator sidebar', category: 'site', section: 'sidebar-modules',
    settings: [{ key: 'SidebarModulesAdmin', label: 'Administrator module access (JSON)', kind: 'json' }],
  },
  {
    name: 'Basic authentication', category: 'auth', section: 'basic-auth',
    settings: [
      { key: 'RegisterEnabled', label: 'Registration enabled', kind: 'boolean' },
      { key: 'PasswordLoginEnabled', label: 'Password login enabled', kind: 'boolean' },
      { key: 'PasswordRegisterEnabled', label: 'Password register enabled', kind: 'boolean' },
      { key: 'EmailVerificationEnabled', label: 'Email verification enabled', kind: 'boolean' },
    ],
  },
  {
    name: 'OAuth and WeChat', category: 'auth', section: 'oauth',
    settings: [
      { key: 'GitHubOAuthEnabled', label: 'GitHub OAuth', kind: 'boolean' },
      { key: 'DiscordOAuthEnabled', label: 'Discord OAuth', kind: 'boolean' },
      { key: 'WeChatAuthEnabled', label: 'WeChat auth', kind: 'boolean' },
      { key: 'TelegramOAuthEnabled', label: 'Telegram OAuth', kind: 'boolean' },
      { key: 'LinuxDOOAuthEnabled', label: 'LinuxDO OAuth', kind: 'boolean' },
      { key: 'WeChatServerAddress', label: 'WeChat server address' },
      { key: 'WeChatServerToken', label: 'WeChat server token', kind: 'secret' },
      { key: 'WeChatAccountQRCodeImageURL', label: 'WeChat QR code URL' },
    ],
  },
  {
    name: 'Passkey', category: 'auth', section: 'passkey',
    settings: [{ key: 'passkey.enabled', label: 'Passkey login enabled', kind: 'boolean' }],
  },
  {
    name: 'Bot protection', category: 'auth', section: 'bot-protection',
    settings: [
      { key: 'TurnstileCheckEnabled', label: 'Turnstile', kind: 'boolean' },
      { key: 'TurnstileSiteKey', label: 'Turnstile site key' },
      { key: 'TurnstileSecretKey', label: 'Turnstile secret key', kind: 'secret' },
    ],
  },
  {
    name: 'Quota', category: 'billing', section: 'quota',
    settings: [
      { key: 'InitialQuota', label: 'Initial quota', kind: 'number' },
      { key: 'TopUpMinimum', label: 'Top-up minimum', kind: 'number' },
      { key: 'QuotaForInviter', label: 'Inviter bonus quota', kind: 'number' },
      { key: 'QuotaForInvitee', label: 'Invitee bonus quota', kind: 'number' },
      { key: 'QuotaRemindThreshold', label: 'Low-quota reminder threshold', kind: 'number' },
    ],
  },
  {
    name: 'Model pricing', category: 'billing', section: 'model-pricing',
    settings: [
      { key: 'ModelPrice', label: 'Model prices (JSON)', kind: 'json' },
      { key: 'ModelBillingMode', label: 'Billing modes (JSON)', kind: 'json' },
      { key: 'ModelBillingExpr', label: 'Billing expressions (JSON)', kind: 'json' },
      { key: 'ExposeRatioEnabled', label: 'Expose public ratio configuration', kind: 'boolean' },
    ],
  },
  {
    name: 'Group pricing', category: 'billing', section: 'group-pricing',
    settings: [
      { key: 'GroupRatio', label: 'Group ratios (JSON)', kind: 'json' },
      { key: 'GroupGroupRatio', label: 'User-to-routing group ratios (JSON)', kind: 'json' },
    ],
  },
  {
    name: 'Check-in', category: 'billing', section: 'checkin',
    settings: [
      { key: 'checkin_setting.enabled', label: 'Daily check-in enabled', kind: 'boolean' },
      { key: 'checkin_setting.min_quota', label: 'Minimum check-in reward', kind: 'number' },
      { key: 'checkin_setting.max_quota', label: 'Maximum check-in reward', kind: 'number' },
    ],
  },
  {
    name: 'Global model settings', category: 'models', section: 'global',
    settings: [
      { key: 'DefaultGroup', label: 'Default group' },
      { key: 'UserUsableGroups', label: 'User-selectable groups (JSON)', kind: 'json' },
      { key: 'DisplayTokenCount', label: 'Display token count', kind: 'boolean' },
    ],
  },
  {
    name: 'Routing reliability', category: 'models', section: 'routing-reliability',
    settings: [
      { key: 'RetryTimes', label: 'Retry times', kind: 'number' },
      { key: 'AutoGroups', label: 'Automatic group order (JSON)', kind: 'json' },
      { key: 'MaxTokenAutoGroups', label: 'Maximum auto-groups per token', kind: 'number' },
      { key: 'DefaultStreamingTimeout', label: 'Streaming timeout (seconds)', kind: 'number' },
    ],
  },
  {
    name: 'Channel affinity', category: 'models', section: 'channel-affinity',
    settings: [
      { key: 'channel_affinity_setting.enabled', label: 'Affinity enabled', kind: 'boolean' },
      { key: 'channel_affinity_setting.switch_on_success', label: 'Switch affinity after successful retry', kind: 'boolean' },
      { key: 'channel_affinity_setting.keep_on_channel_disabled', label: 'Keep entries for disabled channels', kind: 'boolean' },
      { key: 'channel_affinity_setting.max_entries', label: 'Maximum cache entries', kind: 'number' },
      { key: 'channel_affinity_setting.default_ttl_seconds', label: 'Default TTL (seconds)', kind: 'number' },
      { key: 'channel_affinity_setting.rules', label: 'Affinity rules (JSON)', kind: 'json' },
    ],
  },
  {
    name: 'Sensitive words', category: 'security', section: 'sensitive-words',
    settings: [
      { key: 'CheckSensitiveEnabled', label: 'Sensitive-word check enabled', kind: 'boolean' },
      { key: 'CheckSensitiveOnPromptEnabled', label: 'Check prompts', kind: 'boolean' },
      { key: 'SensitiveWords', label: 'Sensitive words (comma/newline)', kind: 'textarea' },
    ],
  },
  {
    name: 'Dashboard content', category: 'content', section: 'dashboard',
    settings: [
      { key: 'HomePageContent', label: 'Home page content', kind: 'textarea' },
      { key: 'Pricing', label: 'Pricing JSON', kind: 'json' },
    ],
  },
  {
    name: 'Chat launchers', category: 'content', section: 'chat',
    settings: [{ key: 'Chats', label: 'Chat launchers (JSON)', kind: 'json' }],
  },
  {
    name: 'Performance metrics', category: 'operations', section: 'performance',
    settings: [
      { key: 'perf_metrics_setting.enabled', label: 'Metrics enabled', kind: 'boolean' },
      { key: 'perf_metrics_setting.flush_interval', label: 'Flush interval (minutes)', kind: 'number' },
      { key: 'perf_metrics_setting.bucket_time', label: 'Bucket size (minute, 5min, or hour)' },
      { key: 'perf_metrics_setting.retention_days', label: 'Retention (days; 0 keeps all)', kind: 'number' },
    ],
  },
];

export function settingsGroupsFor(category: SystemSettingsCategory, section: string): SettingGroup[] {
  return SETTINGS_GROUPS.filter((group) => group.category === category && group.section === section);
}
