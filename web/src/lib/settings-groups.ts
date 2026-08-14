// Curated settings groups with friendly labels, mirroring the reference's
// dedicated settings pages as one grouped editor.
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
      { key: 'TurnstileEnabled', label: 'Turnstile' },
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
    ],
  },
  {
    name: 'Model & routing',
    settings: [
      { key: 'DefaultGroup', label: 'Default group' },
      { key: 'RetryTimes', label: 'Retry times' },
      { key: 'AutoGroupConsume', label: 'Auto group consume' },
      { key: 'DisplayTokenCount', label: 'Display token count' },
    ],
  },
];
