// Curated settings groups with friendly labels, mirroring the reference's
// dedicated settings pages as one grouped editor. Every key listed here is
// read by the backend (service/controller layers); the root-only editor writes
// one key/value pair through PUT /api/option/.
export interface SettingDef {
  key: string;
  label: string;
}

export interface SettingGroup {
  name: string;
  settings: SettingDef[];
}

export const SETTINGS_GROUPS: SettingGroup[] = [
  {
    name: 'Site',
    settings: [
      { key: 'SystemName', label: 'Site name' },
      { key: 'Logo', label: 'Logo URL' },
      { key: 'Footer', label: 'Footer HTML' },
      { key: 'ServerAddress', label: 'Server address' },
      { key: 'About', label: 'About' },
      { key: 'HomePageContent', label: 'Home page content' },
      { key: 'Notice', label: 'Notice' },
      { key: 'Pricing', label: 'Pricing JSON' },
    ],
  },
  {
    name: 'Authentication',
    settings: [
      { key: 'RegisterEnabled', label: 'Registration enabled' },
      { key: 'PasswordLoginEnabled', label: 'Password login enabled' },
      { key: 'PasswordRegisterEnabled', label: 'Password register enabled' },
      { key: 'EmailVerificationEnabled', label: 'Email verification enabled' },
      { key: 'GitHubOAuthEnabled', label: 'GitHub OAuth' },
      { key: 'DiscordOAuthEnabled', label: 'Discord OAuth' },
      { key: 'WeChatAuthEnabled', label: 'WeChat auth' },
      { key: 'TelegramOAuthEnabled', label: 'Telegram auth' },
      { key: 'LinuxDOOAuthEnabled', label: 'LinuxDO OAuth' },
      { key: 'TurnstileCheckEnabled', label: 'Turnstile' },
      { key: 'TurnstileSiteKey', label: 'Turnstile site key' },
      { key: 'TurnstileSecretKey', label: 'Turnstile secret key' },
      { key: 'WeChatServerAddress', label: 'WeChat server address' },
      { key: 'WeChatServerToken', label: 'WeChat server token' },
      { key: 'WeChatAccountQRCodeImageURL', label: 'WeChat QR code URL' },
    ],
  },
  {
    name: 'Moderation',
    settings: [
      { key: 'CheckSensitiveEnabled', label: 'Sensitive-word check enabled' },
      { key: 'CheckSensitiveOnPromptEnabled', label: 'Check prompts' },
      { key: 'SensitiveWords', label: 'Sensitive words (comma/newline)' },
    ],
  },
  {
    name: 'Billing',
    settings: [
      { key: 'InitialQuota', label: 'Initial quota' },
      { key: 'QuotaPerUnit', label: 'Quota per unit' },
      { key: 'TopUpMinimum', label: 'Top-up minimum' },
      { key: 'CheckInQuota', label: 'Check-in reward' },
      { key: 'ModelPrice', label: 'Model prices (JSON)' },
      { key: 'GroupRatio', label: 'Group ratios (JSON)' },
      { key: 'ModelBillingMode', label: 'Billing modes (JSON)' },
      { key: 'ModelBillingExpr', label: 'Billing expressions (JSON)' },
      { key: 'QuotaForInviter', label: 'Inviter bonus quota' },
      { key: 'QuotaForInvitee', label: 'Invitee bonus quota' },
      { key: 'QuotaRemindThreshold', label: 'Low-quota reminder threshold' },
    ],
  },
  {
    name: 'Performance metrics',
    settings: [
      { key: 'perf_metrics_setting.enabled', label: 'Metrics enabled' },
      { key: 'perf_metrics_setting.flush_interval', label: 'Flush interval (minutes)' },
      { key: 'perf_metrics_setting.bucket_time', label: 'Bucket size (minute, 5min, or hour)' },
      { key: 'perf_metrics_setting.retention_days', label: 'Retention (days; 0 keeps all)' },
    ],
  },
  {
    name: 'Model & routing',
    settings: [
      { key: 'DefaultGroup', label: 'Default group' },
      { key: 'RetryTimes', label: 'Retry times' },
      { key: 'AutoGroupConsume', label: 'Auto group consume' },
      { key: 'DisplayTokenCount', label: 'Display token count' },
      { key: 'DefaultStreamingTimeout', label: 'Streaming timeout (seconds)' },
    ],
  },
  {
    name: 'Channel affinity',
    settings: [
      { key: 'channel_affinity_setting.enabled', label: 'Affinity enabled' },
      { key: 'channel_affinity_setting.switch_on_success', label: 'Switch affinity after successful retry' },
      { key: 'channel_affinity_setting.keep_on_channel_disabled', label: 'Keep entries for disabled channels' },
      { key: 'channel_affinity_setting.max_entries', label: 'Maximum cache entries' },
      { key: 'channel_affinity_setting.default_ttl_seconds', label: 'Default TTL (seconds)' },
      { key: 'channel_affinity_setting.rules', label: 'Affinity rules (JSON)' },
    ],
  },
];
