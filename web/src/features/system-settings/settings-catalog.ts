import {
  parseConsoleAPIInfo,
  parseConsoleFAQ,
  parseConsoleOptionJSON,
  parseConsoleUptimeKumaGroups,
} from '../../lib/console-content';
import { validateBillingSettingFormat } from './billing-setting-validation';
import { validateModelRequestRateLimitGroups } from './model-request-rate-limit';
import { validateSpecialUsableGroupDirectives } from './registration-setting-validation';
import {
  validateChannelDisableKeywords,
  validateHTTPStatusRanges,
  validateToolPriceMap,
} from './operations-setting-validation';
import { validateUsageRatioMap } from './usage-ratio-validation';

export type SettingInputKind = 'text' | 'textarea' | 'boolean' | 'integer' | 'decimal' | 'json' | 'secret' | 'select';
export type SettingInputFormat = 'hostname' | 'telegram-bot-name' | 'telegram-bot-token'
  | 'console-api-info' | 'console-faq' | 'console-uptime-kuma-groups'
  | 'email-domain-list' | 'oauth-endpoint' | 'passkey-origins'
  | 'gemini-safety-settings' | 'gemini-version-settings' | 'model-id-list'
  | 'claude-header-settings' | 'claude-max-tokens' | 'model-request-rate-limits'
  | 'usage-ratio-map' | 'tool-price-map' | 'http-status-ranges' | 'channel-disable-keywords'
  | 'topup-group-ratios' | 'payment-methods' | 'payment-setting'
  | 'billing-url' | 'billing-endpoint' | 'currency-code' | 'currency-symbol'
  | 'registration-group-directives';

export interface SettingChoice {
  value: string;
  label: string;
}

export interface SettingDefinition {
  key: string;
  label: string;
  description?: string;
  kind: SettingInputKind;
  /** Displays an immutable deployment contract instead of an editable option. */
  fixedValue?: string;
  defaultValue?: string;
  maxLength?: number;
  min?: number;
  max?: number;
  /** The empty/default sentinel is not accepted by this option's backend contract. */
  required?: boolean;
  choices?: readonly SettingChoice[];
  format?: SettingInputFormat;
  /** A confirmation dialog is shown before this option is written. */
  confirmation?: string;
}

export interface SettingGroupDefinition {
  title: string;
  description?: string;
  settings: readonly SettingDefinition[];
}

export interface SettingsSectionDefinition {
  id: string;
  title: string;
  description: string;
  groups: readonly SettingGroupDefinition[];
  /** Explains a reference route that has no writable TokenRouter contract. */
  unavailableReason?: string;
}

export interface SettingsCategoryDefinition {
  id: string;
  label: string;
  sections: readonly SettingsSectionDefinition[];
}

const JSON_LIMIT = 1024 * 1024;
const CONTENT_LIMIT = 1024 * 1024;
const SHORT_TEXT = 4_096;
const SAFE_INTEGER = Number.MAX_SAFE_INTEGER;
const MAX_QUOTA = 2_147_483_647;
const QUOTA_PER_UNIT = 500_000;
const MAX_TOP_UP_REFERENCE_AMOUNT = Math.floor(MAX_QUOTA / QUOTA_PER_UNIT);
const MAX_PAYMENT_PROVIDER_AMOUNT = 999_999.99;
const MAX_GROK_VIOLATION_AMOUNT = MAX_QUOTA / QUOTA_PER_UNIT;
const RISK_AUTH = 'Changing this authentication policy can prevent users from signing in. Continue?';
const RISK_BILLING = 'This change affects billing or account balances. Verify the value before continuing.';
const RISK_ROUTING = 'This change affects live request routing. Continue?';
const RISK_SECURITY = 'This change affects a security control. Continue?';
const DEFAULT_CHANNEL_DISABLE_KEYWORDS = 'Your credit balance is too low\nThis organization has been disabled.\nYou exceeded your current quota\nPermission denied\nThe security token included in the request is invalid\nOperation not allowed\nYour account is not authorized';
const DEFAULT_RETRY_STATUS_CODES = '100-199,300-399,401-407,409-499,500-503,505-523,525-599';

const bool = (
  key: string,
  label: string,
  description?: string,
  confirmation?: string,
  defaultValue?: string,
): SettingDefinition => ({ key, label, description, kind: 'boolean', confirmation, defaultValue });

const text = (
  key: string,
  label: string,
  description?: string,
  maxLength = SHORT_TEXT,
  confirmation?: string,
): SettingDefinition => ({ key, label, description, kind: 'text', maxLength, confirmation });

const secret = (key: string, label: string, description?: string): SettingDefinition => ({
  key,
  label,
  description,
  kind: 'secret',
  maxLength: SHORT_TEXT,
  confirmation: 'This replaces a write-only credential. The existing value cannot be recovered in the dashboard.',
});

const integer = (
  key: string,
  label: string,
  min = 0,
  max = SAFE_INTEGER,
  description?: string,
  confirmation?: string,
  defaultValue?: string,
): SettingDefinition => ({ key, label, description, kind: 'integer', min, max, maxLength: 32, confirmation, defaultValue });

const decimal = (
  key: string,
  label: string,
  min = 0,
  max = 1_000_000_000,
  description?: string,
  confirmation?: string,
  defaultValue?: string,
): SettingDefinition => ({ key, label, description, kind: 'decimal', min, max, maxLength: 64, confirmation, defaultValue });

const json = (
  key: string,
  label: string,
  description?: string,
  confirmation?: string,
  maxLength = JSON_LIMIT,
  defaultValue?: string,
): SettingDefinition => ({ key, label, description, kind: 'json', maxLength, confirmation, defaultValue });

export const SYSTEM_SETTINGS_CATEGORIES = [
  {
    id: 'site',
    label: 'Site & Branding',
    sections: [
      {
        id: 'system-info',
        title: 'System information',
        description: 'Public identity, canonical address, landing content, and legal documents.',
        groups: [
          {
            title: 'Branding',
            settings: [
              text('SystemName', 'Site name', 'Shown throughout the public and authenticated interface.', 128),
              text('Logo', 'Logo URL', 'Public logo location. The server stores this value verbatim.', 4_096),
              text('ServerAddress', 'Server address', 'Canonical public origin used for callbacks and payment returns.', 2_048),
              { key: 'Footer', label: 'Footer HTML', description: 'Public footer content.', kind: 'textarea', maxLength: CONTENT_LIMIT },
              { key: 'About', label: 'About', description: 'Markdown, HTML, or an absolute URL.', kind: 'textarea', maxLength: CONTENT_LIMIT },
              { key: 'HomePageContent', label: 'Home page content', description: 'Markdown or HTML shown on the public home page.', kind: 'textarea', maxLength: CONTENT_LIMIT },
            ],
          },
          {
            title: 'Legal documents',
            description: 'The public legal endpoints return these values exactly as stored.',
            settings: [
              { key: 'legal.user_agreement', label: 'User agreement (Markdown, HTML, or URL)', kind: 'textarea', maxLength: CONTENT_LIMIT },
              { key: 'legal.privacy_policy', label: 'Privacy policy (Markdown, HTML, or URL)', kind: 'textarea', maxLength: CONTENT_LIMIT },
            ],
          },
        ],
      },
      {
        id: 'notice',
        title: 'Notice',
        description: 'Manage the public system notice.',
        groups: [{ title: 'Notice', settings: [{ key: 'Notice', label: 'Notice', kind: 'textarea', maxLength: CONTENT_LIMIT }] }],
      },
      {
        id: 'header-navigation',
        title: 'Header navigation',
        description: 'Control public header modules using the backend HeaderNavModules contract.',
        groups: [],
      },
      {
        id: 'sidebar-modules',
        title: 'Sidebar modules',
        description: 'Control administrator sidebar modules using the backend SidebarModulesAdmin contract.',
        groups: [],
      },
    ],
  },
  {
    id: 'auth',
    label: 'Authentication',
    sections: [
      {
        id: 'basic-auth',
        title: 'Basic authentication',
        description: 'Registration, password login, and email verification policy.',
        groups: [{
          title: 'Basic authentication',
          settings: [
            bool('RegisterEnabled', 'Registration enabled', undefined, RISK_AUTH, 'true'),
            bool('PasswordLoginEnabled', 'Password login enabled', undefined, RISK_AUTH, 'true'),
            bool('PasswordRegisterEnabled', 'Password registration enabled', undefined, RISK_AUTH, 'true'),
            bool('EmailVerificationEnabled', 'Email verification enabled', undefined, RISK_AUTH, 'false'),
            bool('EmailDomainRestrictionEnabled', 'Restrict verification email domains', 'When enabled, verification and registration accept only exact domains from the whitelist below.', RISK_AUTH, 'false'),
            bool('EmailAliasRestrictionEnabled', 'Reject email aliases', 'Rejects local parts containing + or . during verification, binding, and registration.', RISK_AUTH, 'false'),
            {
              key: 'EmailDomainWhitelist',
              label: 'Allowed email domains',
              description: 'Comma- or line-separated ASCII domain names. Subdomains must be listed explicitly.',
              kind: 'textarea',
              maxLength: 16 * 1024,
              format: 'email-domain-list',
              defaultValue: 'gmail.com,163.com,126.com,qq.com,outlook.com,hotmail.com,icloud.com,yahoo.com,foxmail.com',
              confirmation: RISK_AUTH,
            },
          ],
        }],
      },
      {
        id: 'oauth',
        title: 'OAuth',
        description: 'Configure built-in OAuth providers. A provider-specific deployment environment configuration, when present, overrides its complete option-backed configuration.',
        groups: [
          {
            title: 'GitHub authentication',
            description: 'Callback path: /api/oauth/github/callback. Save both credentials before enabling.',
            settings: [
              bool('GitHubOAuthEnabled', 'GitHub authentication enabled', 'The provider is advertised only when its complete configuration is valid.', RISK_AUTH, 'false'),
              text('GitHubClientId', 'GitHub client ID', undefined, 256, RISK_AUTH),
              secret('GitHubClientSecret', 'GitHub client secret'),
            ],
          },
          {
            title: 'Discord authentication',
            description: 'Callback path: /api/oauth/discord/callback. Save both credentials before enabling.',
            settings: [
              bool('discord.enabled', 'Discord authentication enabled', 'The provider is advertised only when its complete configuration is valid.', RISK_AUTH, 'false'),
              text('discord.client_id', 'Discord client ID', undefined, 256, RISK_AUTH),
              secret('discord.client_secret', 'Discord client secret'),
            ],
          },
          {
            title: 'OpenID Connect authentication',
            description: 'Callback path: /api/oauth/oidc/callback. Enter all three runtime endpoints explicitly; browser-side discovery is intentionally unavailable.',
            settings: [
              bool('oidc.enabled', 'OpenID Connect authentication enabled', 'The provider remains hidden until credentials and all runtime endpoints are valid.', RISK_AUTH, 'false'),
              text('oidc.display_name', 'OpenID Connect display name', 'Public label shown on the sign-in button.', 128, RISK_AUTH),
              text('oidc.client_id', 'OpenID Connect client ID', undefined, 256, RISK_AUTH),
              secret('oidc.client_secret', 'OpenID Connect client secret'),
              {
                ...text('oidc.authorization_endpoint', 'Authorization endpoint', 'Explicit HTTPS endpoint used to start sign-in.', 2_048, RISK_AUTH),
                format: 'oauth-endpoint',
              },
              {
                ...text('oidc.token_endpoint', 'Token endpoint', 'Explicit HTTPS endpoint used by the server.', 2_048, RISK_AUTH),
                format: 'oauth-endpoint',
              },
              {
                ...text('oidc.user_info_endpoint', 'User-info endpoint', 'Explicit HTTPS endpoint used by the server.', 2_048, RISK_AUTH),
                format: 'oauth-endpoint',
              },
            ],
          },
          {
            title: 'Linux DO authentication',
            description: 'Callback path: /api/oauth/linuxdo/callback. Save both credentials before enabling.',
            settings: [
              bool('LinuxDOOAuthEnabled', 'Linux DO authentication enabled', 'The provider is advertised only when its complete configuration is valid.', RISK_AUTH, 'false'),
              text('LinuxDOClientId', 'Linux DO client ID', undefined, 256, RISK_AUTH),
              secret('LinuxDOClientSecret', 'Linux DO client secret'),
              integer('LinuxDOMinimumTrustLevel', 'Minimum Linux DO trust level', 0, 4, 'Users below this provider-asserted trust level are rejected before account lookup or creation.', RISK_AUTH, '0'),
            ],
          },
          {
            title: 'WeChat authentication',
            settings: [
              bool('WeChatAuthEnabled', 'WeChat authentication enabled', undefined, RISK_AUTH, 'false'),
              text('WeChatServerAddress', 'WeChat server address', 'HTTP(S) address for the configured WeChat bridge.', 2_048, RISK_AUTH),
              secret('WeChatServerToken', 'WeChat server token'),
              text('WeChatAccountQRCodeImageURL', 'WeChat QR code URL', undefined, 4_096),
            ],
          },
          {
            title: 'Telegram authentication',
            description: 'A deployment-level TELEGRAM_BOT_NAME or TELEGRAM_BOT_TOKEN overrides the corresponding option.',
            settings: [
              bool('TelegramOAuthEnabled', 'Telegram authentication enabled', 'Login remains disabled until both bot values are valid.', RISK_AUTH, 'false'),
              {
                ...text('TelegramBotName', 'Telegram bot username', 'Five to 32 letters, digits, or underscores, ending in bot. A leading @ is accepted.', 32, RISK_AUTH),
                format: 'telegram-bot-name',
              },
              {
                ...secret('TelegramBotToken', 'Telegram bot token', 'Write-only token issued by BotFather.'),
                maxLength: 512,
                format: 'telegram-bot-token',
              },
            ],
          },
        ],
      },
      {
        id: 'passkey',
        title: 'Passkey',
        description: 'Configure passkey relying-party identity and authenticator preferences. Committed changes apply to new ceremonies without a restart.',
        groups: [{
          title: 'Passkey',
          settings: [
            bool('passkey.enabled', 'Passkey login enabled', undefined, RISK_AUTH, 'false'),
            text('passkey.rp_display_name', 'Relying-party display name', 'Defaults to the site name when empty.', 128, RISK_AUTH),
            {
              ...text('passkey.rp_id', 'Relying-party ID', 'Domain name or IP address without a scheme, port, or path. Empty values derive from the first effective origin.', 253, RISK_AUTH),
              format: 'hostname',
            },
            {
              key: 'passkey.origins',
              label: 'Allowed passkey origins',
              description: 'Comma- or line-separated exact origins. Each hostname must equal or be a subdomain of the relying-party ID.',
              kind: 'textarea',
              maxLength: 16 * 1024,
              format: 'passkey-origins',
              confirmation: RISK_AUTH,
            },
            bool('passkey.allow_insecure_origin', 'Allow insecure passkey origins', 'Permits explicitly configured HTTP origins. Leave disabled outside controlled development environments.', RISK_AUTH, 'false'),
            {
              key: 'passkey.user_verification',
              label: 'Passkey user verification',
              description: 'Required is the strongest policy; preferred matches the WebAuthn default.',
              kind: 'select',
              choices: [
                { value: 'required', label: 'Required' },
                { value: 'preferred', label: 'Preferred' },
                { value: 'discouraged', label: 'Discouraged' },
              ],
              defaultValue: 'preferred',
              confirmation: RISK_AUTH,
            },
            {
              key: 'passkey.attachment_preference',
              label: 'Authenticator attachment preference',
              kind: 'select',
              choices: [
                { value: '', label: 'No preference' },
                { value: 'platform', label: 'Platform authenticator' },
                { value: 'cross-platform', label: 'Cross-platform authenticator' },
              ],
              defaultValue: '',
              confirmation: RISK_AUTH,
            },
          ],
        }],
      },
      {
        id: 'bot-protection',
        title: 'Bot protection',
        description: 'Cloudflare Turnstile policy and credentials.',
        groups: [{
          title: 'Bot protection',
          settings: [
            bool('TurnstileCheckEnabled', 'Turnstile enabled', undefined, RISK_SECURITY, 'false'),
            text('TurnstileSiteKey', 'Turnstile site key', undefined, 4_096, RISK_SECURITY),
            secret('TurnstileSecretKey', 'Turnstile secret key'),
          ],
        }],
      },
      {
        id: 'custom-oauth',
        title: 'Custom OAuth',
        description: 'Manage database-backed OAuth and OpenID Connect providers.',
        groups: [],
      },
    ],
  },
  {
    id: 'billing',
    label: 'Billing & Payment',
    sections: [
      {
        id: 'quota',
        title: 'Quota',
        description: 'Initial balances, pre-consumption, and referral rewards.',
        groups: [{
          title: 'Quota',
          settings: [
            integer(
              'QuotaForNewUser',
              'New-user quota (canonical)',
              0,
              MAX_QUOTA,
              'Exact internal quota granted to newly created accounts. When absent, the server uses legacy InitialQuota only as a compatibility fallback.',
              RISK_BILLING,
              '0',
            ),
            integer('PreConsumedQuota', 'Pre-consumed quota', 0, MAX_QUOTA, undefined, RISK_BILLING),
            integer('QuotaForInviter', 'Inviter bonus quota', 0, MAX_QUOTA, 'Positive referral rewards require payment-compliance confirmation.', RISK_BILLING),
            integer('QuotaForInvitee', 'Invitee bonus quota', 0, MAX_QUOTA, 'Positive referral rewards require payment-compliance confirmation.', RISK_BILLING),
          ],
        }],
      },
      {
        id: 'currency',
        title: 'Currency',
        description: 'Quota display units used by wallet and dashboard billing compatibility endpoints.',
        groups: [{
          title: 'Currency and display',
          settings: [
            {
              key: 'QuotaPerUnit',
              label: 'Quota per unit',
              description: 'Fixed accounting scale: one USD equals 500,000 internal quota. This deployment invariant cannot be changed at runtime.',
              kind: 'integer',
              fixedValue: '500000',
            },
            decimal('USDExchangeRate', 'USD exchange rate', Number.EPSILON, 1_000_000, undefined, RISK_BILLING),
            {
              key: 'QuotaDisplayType',
              label: 'Quota display type',
              description: 'TokenRouter recognizes USD, CNY, tokens, and a custom currency.',
              kind: 'select',
              choices: [
                { value: '', label: 'Currency (USD)' },
                { value: 'CNY', label: 'CNY' },
                { value: 'tokens', label: 'Tokens' },
                { value: 'CUSTOM', label: 'Custom currency' },
              ],
              confirmation: RISK_BILLING,
            },
            {
              ...text('general_setting.custom_currency_symbol', 'Custom currency symbol', 'Short symbol shown next to custom-currency balances.', 32, RISK_BILLING),
              format: 'currency-symbol',
            },
            decimal('general_setting.custom_currency_exchange_rate', 'Custom currency units per USD', Number.EPSILON, 1_000_000, 'One USD equals this many units of the custom currency.', RISK_BILLING, '1'),
            bool('DisplayTokenStatEnabled', 'Display token statistics', undefined, RISK_BILLING, 'true'),
          ],
        }],
      },
      {
        id: 'model-pricing',
        title: 'Model pricing',
        description: 'Live model billing registries. Invalid JSON is rejected before it reaches the server.',
        groups: [{
          title: 'Model pricing',
          settings: [
            json('ModelPrice', 'Model prices (JSON)', 'USD-per-million prompt and completion prices.', RISK_BILLING, JSON_LIMIT, '{}'),
            json('ModelRatio', 'Model ratios (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            json('CompletionRatio', 'Completion ratios (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            {
              ...json('CacheRatio', 'Cache read ratios (JSON)', 'Prompt-cache read token multipliers by exact model name. Missing models use 1.', RISK_BILLING),
              format: 'usage-ratio-map',
            },
            {
              ...json('CreateCacheRatio', 'Cache creation ratios (JSON)', 'Prompt-cache creation token multipliers by exact model name. Missing models use 1.25; Claude one-hour writes cost 1.6 times this value.', RISK_BILLING),
              format: 'usage-ratio-map',
            },
            {
              ...json('ImageRatio', 'Image input ratios (JSON)', 'Image-input token multipliers by exact model name. Missing models use 1.', RISK_BILLING),
              format: 'usage-ratio-map',
            },
            {
              ...json('AudioRatio', 'Audio input ratios (JSON)', 'Audio-input token multipliers by normalized model family. Missing models use 1.', RISK_BILLING),
              format: 'usage-ratio-map',
            },
            {
              ...json('AudioCompletionRatio', 'Audio completion ratios (JSON)', 'Additional audio-output multipliers by normalized model family. Missing models use 1.', RISK_BILLING),
              format: 'usage-ratio-map',
            },
            {
              ...bool(
                'quota_setting.enable_free_model_pre_consume',
                'Pre-consume free models',
                'When disabled, zero-effective-price requests use an explicit durable zero hold instead of consuming wallet or subscription quota.',
                RISK_BILLING,
                'true',
              ),
              required: true,
            },
            json('PerCallModelPrice', 'Per-call model prices (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            {
              ...json(
                'tool_price_setting.prices',
                'Tool call prices (JSON)',
                'USD per 1,000 completed calls, including explicitly priced custom functions. Use a tool name or tool:model-prefix*; the longest matching model prefix wins and an explicit zero disables the charge.',
                RISK_BILLING,
                JSON_LIMIT,
                '{}',
              ),
              format: 'tool-price-map',
              required: true,
            },
            json('ModelBillingMode', 'Billing modes (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            json('ModelBillingExpr', 'Billing expressions (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            bool('ExposeRatioEnabled', 'Expose public ratio configuration', undefined, RISK_BILLING, 'false'),
          ],
        }],
      },
      {
        id: 'group-pricing',
        title: 'Group pricing',
        description: 'User-visible groups, routing order, and billing multipliers.',
        groups: [{
          title: 'Group pricing',
          settings: [
            json('GroupRatio', 'Group ratios (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            json('GroupGroupRatio', 'User-to-routing group ratios (JSON)', undefined, RISK_BILLING, JSON_LIMIT, '{}'),
            {
              ...json('TopupGroupRatio', 'Top-up group ratios (JSON)', 'Top-up-only multipliers by user group. These do not change request-routing prices.', RISK_BILLING, 64 * 1024),
              format: 'topup-group-ratios',
            },
            json('UserUsableGroups', 'User-selectable groups (JSON)', undefined, RISK_ROUTING, 64 * 1024),
            {
              ...json(
                'group_ratio_setting.group_special_usable_group',
                'Special usable groups (JSON)',
                'Map each user group to add or remove directives. Use group or +:group to add and -:group to remove; every resulting relay group still requires a positive configured ratio.',
                RISK_ROUTING,
                64 * 1024,
                '{}',
              ),
              format: 'registration-group-directives',
            },
            json('AutoGroups', 'Automatic group order (JSON)', undefined, RISK_ROUTING, 64 * 1024),
            integer('MaxTokenAutoGroups', 'Maximum automatic groups per token', 1, 64, undefined, RISK_ROUTING, '5'),
          ],
        }],
      },
      {
        id: 'payment',
        title: 'Payment',
        description: 'Payment gateways and top-up rules. Credentials are write-only.',
        groups: [
          {
            title: 'Epay',
            settings: [
              {
                ...text('TopUpLink', 'Top-up link', 'Optional HTTPS link shown in the wallet for buying redemption codes.', 2_048, RISK_BILLING),
                format: 'billing-url',
              },
              { ...text('PayAddress', 'Epay address', undefined, 2_048, RISK_BILLING), format: 'billing-endpoint' },
              text('EpayId', 'Epay partner ID', undefined, 256, RISK_BILLING),
              secret('EpayKey', 'Epay secret key'),
              { ...text('CustomCallbackAddress', 'Custom callback address', undefined, 2_048, RISK_BILLING), format: 'billing-endpoint' },
              decimal('Price', 'Epay unit price', Number.EPSILON, MAX_PAYMENT_PROVIDER_AMOUNT, undefined, RISK_BILLING),
              integer('MinTopUp', 'Epay minimum top-up', 1, MAX_TOP_UP_REFERENCE_AMOUNT, undefined, RISK_BILLING),
              { ...json('PayMethods', 'Payment methods (JSON)', undefined, RISK_BILLING, 128 * 1024), format: 'payment-methods' },
              { ...json('PaymentSetting', 'Amount options and discounts (JSON)', undefined, RISK_BILLING, 128 * 1024), format: 'payment-setting' },
            ],
          },
          {
            title: 'Stripe',
            description: 'Stripe API and webhook secrets are environment-only in TokenRouter.',
            settings: [
              text('StripePriceId', 'Stripe price ID', undefined, 256, RISK_BILLING),
              decimal('StripeUnitPrice', 'Stripe unit price', Number.EPSILON, MAX_PAYMENT_PROVIDER_AMOUNT, undefined, RISK_BILLING),
              integer('StripeMinTopUp', 'Stripe minimum top-up', 1, MAX_TOP_UP_REFERENCE_AMOUNT, undefined, RISK_BILLING),
              { ...text('StripeCurrency', 'Stripe currency', 'Three-letter ISO currency code.', 3, RISK_BILLING), format: 'currency-code' },
              bool('StripePromotionCodesEnabled', 'Stripe promotion codes enabled', undefined, RISK_BILLING, 'false'),
            ],
          },
          {
            title: 'Creem',
            settings: [
              secret('CreemApiKey', 'Creem API key'),
              secret('CreemWebhookSecret', 'Creem webhook secret'),
              bool('CreemTestMode', 'Creem test mode', undefined, RISK_BILLING, 'false'),
              json('CreemProducts', 'Creem products (JSON)', undefined, RISK_BILLING, 128 * 1024, '[]'),
            ],
          },
          {
            title: 'Waffo',
            settings: [
              bool('WaffoEnabled', 'Waffo enabled', undefined, RISK_BILLING, 'false'),
              { ...secret('WaffoApiKey', 'Waffo API key'), maxLength: 16 * 1024 },
              { ...secret('WaffoPrivateKey', 'Waffo private key'), maxLength: 16 * 1024 },
              { key: 'WaffoPublicCert', label: 'Waffo public certificate', kind: 'textarea', maxLength: 16 * 1024, confirmation: RISK_BILLING },
              bool('WaffoSandbox', 'Waffo sandbox mode', undefined, RISK_BILLING, 'false'),
              { ...secret('WaffoSandboxApiKey', 'Waffo sandbox API key', 'Used only while sandbox mode is enabled.'), maxLength: 16 * 1024 },
              { ...secret('WaffoSandboxPrivateKey', 'Waffo sandbox private key', 'Used only while sandbox mode is enabled.'), maxLength: 16 * 1024 },
              { key: 'WaffoSandboxPublicCert', label: 'Waffo sandbox public certificate', description: 'Used only while sandbox mode is enabled.', kind: 'textarea', maxLength: 16 * 1024, confirmation: RISK_BILLING },
              text('WaffoMerchantId', 'Waffo merchant ID', undefined, 512, RISK_BILLING),
              { ...text('WaffoCurrency', 'Waffo currency', 'Three-letter ISO currency code.', 3, RISK_BILLING), format: 'currency-code' },
              decimal('WaffoUnitPrice', 'Waffo unit price', Number.EPSILON, MAX_PAYMENT_PROVIDER_AMOUNT, undefined, RISK_BILLING),
              integer('WaffoMinTopUp', 'Waffo minimum top-up', 1, MAX_TOP_UP_REFERENCE_AMOUNT, undefined, RISK_BILLING),
              { ...text('WaffoNotifyUrl', 'Waffo notification URL', undefined, 2_048, RISK_BILLING), format: 'billing-url' },
              { ...text('WaffoReturnUrl', 'Waffo return URL', undefined, 2_048, RISK_BILLING), format: 'billing-url' },
              json('WaffoPayMethods', 'Waffo payment methods (JSON)', undefined, RISK_BILLING, 64 * 1024, '[]'),
            ],
          },
        ],
      },
      {
        id: 'checkin',
        title: 'Check-in',
        description: 'Daily check-in availability and reward bounds.',
        groups: [{
          title: 'Check-in',
          settings: [
            bool('checkin_setting.enabled', 'Daily check-in enabled', undefined, RISK_BILLING, 'false'),
            integer('checkin_setting.min_quota', 'Minimum check-in reward', 1, MAX_QUOTA, undefined, RISK_BILLING, '1000'),
            integer('checkin_setting.max_quota', 'Maximum check-in reward', 1, MAX_QUOTA, undefined, RISK_BILLING, '10000'),
          ],
        }],
      },
    ],
  },
  {
    id: 'models',
    label: 'Models & Routing',
    sections: [
      {
        id: 'global',
        title: 'Global model settings',
        description: 'Defaults applied to users and model routing.',
        groups: [{
          title: 'Global model settings',
          settings: [
            text('DefaultGroup', 'Default group', 'Bounded group assigned to every newly created account.', 64, RISK_ROUTING),
            bool(
              'DefaultUseAutoGroup',
              'Use automatic group for generated default tokens',
              'When deployment-level default-token generation is enabled, new tokens use the automatic group-routing marker.',
              RISK_ROUTING,
              'false',
            ),
          ],
        }],
      },
      {
        id: 'routing-reliability',
        title: 'Routing reliability',
        description: 'Retry policy used by live relay routing.',
        groups: [
          {
            title: 'Request retries',
            settings: [
              integer('RetryTimes', 'Retry times', 0, 10, 'Maximum additional channel attempts. Zero disables automatic request retry.', RISK_ROUTING, '0'),
              {
                key: 'AutomaticRetryStatusCodes',
                label: 'Retryable HTTP statuses',
                description: 'Comma-separated three-digit codes or inclusive ranges. Gateway timeouts 504 and 524 are never retried.',
                kind: 'text',
                maxLength: 4 * 1024,
                format: 'http-status-ranges',
                defaultValue: DEFAULT_RETRY_STATUS_CODES,
                confirmation: RISK_ROUTING,
              },
            ],
          },
          {
            title: 'Automatic channel state',
            description: 'Automatic state changes are disabled by default and apply only after the configured classifier matches.',
            settings: [
              bool('AutomaticDisableChannelEnabled', 'Automatically disable failing channels', undefined, RISK_ROUTING, 'false'),
              bool('AutomaticEnableChannelEnabled', 'Automatically re-enable recovered channels', undefined, RISK_ROUTING, 'false'),
              decimal('ChannelDisableThreshold', 'Channel disable threshold (seconds)', 0, 15, 'Latency at or above this threshold can classify a health check as failed.', RISK_ROUTING, '5'),
              {
                key: 'AutomaticDisableStatusCodes',
                label: 'Disable-channel HTTP statuses',
                description: 'Comma-separated three-digit codes or inclusive ranges.',
                kind: 'text',
                maxLength: 4 * 1024,
                format: 'http-status-ranges',
                defaultValue: '401',
                confirmation: RISK_ROUTING,
              },
              {
                key: 'AutomaticDisableKeywords',
                label: 'Disable-channel error keywords',
                description: 'One case-insensitive error fragment per line; blank lines and duplicate fragments are ignored.',
                kind: 'textarea',
                maxLength: 16 * 1024,
                format: 'channel-disable-keywords',
                defaultValue: DEFAULT_CHANNEL_DISABLE_KEYWORDS,
                confirmation: RISK_ROUTING,
              },
            ],
          },
          {
            title: 'Scheduled health checks',
            settings: [
              bool('monitor_setting.auto_test_channel_enabled', 'Automatic channel tests enabled', 'Disabled by default; manual channel tests remain available.', RISK_ROUTING, 'false'),
              integer('monitor_setting.auto_test_channel_minutes', 'Automatic test interval (minutes)', 1, 525_600, undefined, RISK_ROUTING, '10'),
              {
                key: 'monitor_setting.channel_test_mode',
                label: 'Scheduled test scope',
                description: 'Choose all channels, only auto-disabled channels, or passive recovery checks that cannot disable channels.',
                kind: 'select',
                required: true,
                defaultValue: 'scheduled_all',
                confirmation: RISK_ROUTING,
                choices: [
                  { value: 'scheduled_all', label: 'All eligible channels' },
                  { value: 'auto_ban_only', label: 'Automatically disabled channels only' },
                  { value: 'passive_recovery', label: 'Passive recovery only' },
                ],
              },
            ],
          },
        ],
      },
      {
        id: 'gemini',
        title: 'Gemini',
        description: 'Reference-compatible Gemini policy route.',
        groups: [
          {
            title: 'Safety and model capabilities',
            settings: [
              {
                ...json('gemini.safety_settings', 'Safety thresholds (JSON)', 'Safety thresholds by category. Supported values include OFF and Gemini block thresholds.', RISK_ROUTING, JSON_LIMIT, '{"default":"OFF"}'),
                format: 'gemini-safety-settings',
              },
              {
                ...json('gemini.version_settings', 'API versions (JSON)', 'API versions by model. Values must be v1 or v1beta.', RISK_ROUTING, JSON_LIMIT, '{"default":"v1beta","gemini-1.0-pro":"v1"}'),
                format: 'gemini-version-settings',
              },
              {
                ...json('gemini.supported_imagine_models', 'Image-capable models (JSON)', 'Models that request text and image output after OpenAI conversion.', RISK_ROUTING, JSON_LIMIT, '["gemini-2.0-flash-exp-image-generation","gemini-2.0-flash-exp","gemini-3-pro-image-preview","gemini-3-pro-image","gemini-2.5-flash-image","gemini-3.1-flash-image","gemini-3.1-flash-image-preview"]'),
                format: 'model-id-list',
              },
            ],
          },
          {
            title: 'Thinking and function calls',
            settings: [
              bool('gemini.thinking_adapter_enabled', 'Thinking suffix adapter enabled', 'Enable virtual -thinking, -thinking-N, and -nothinking model suffixes.', RISK_ROUTING, 'false'),
              decimal('gemini.thinking_adapter_budget_tokens_percentage', 'Thinking budget share', 0.002, 1, 'Share of max output tokens assigned to virtual thinking.', RISK_ROUTING, '0.6'),
              bool('gemini.function_call_thought_signature_enabled', 'Function-call thought signatures', 'Attach a compatibility thought signature to converted function-call history.', RISK_ROUTING, 'true'),
              bool('gemini.remove_function_response_id_enabled', 'Remove function response IDs', 'Remove unsupported function response IDs from native Gemini requests.', RISK_ROUTING, 'true'),
            ],
          },
        ],
      },
      {
        id: 'claude',
        title: 'Claude',
        description: 'Reference-compatible Claude policy route.',
        groups: [
          {
            title: 'Headers and output limits',
            settings: [
              {
                ...json('claude.model_headers_settings', 'Model headers (JSON)', 'Extra safe Anthropic or X- headers by model. Credentials and transport headers are rejected.', RISK_ROUTING, JSON_LIMIT, '{}'),
                format: 'claude-header-settings',
              },
              {
                ...json('claude.default_max_tokens', 'Default max tokens (JSON)', 'Default max output tokens by model when the request omits max_tokens.', RISK_ROUTING, JSON_LIMIT, '{"default":8192}'),
                format: 'claude-max-tokens',
              },
            ],
          },
          {
            title: 'Thinking',
            settings: [
              bool('claude.thinking_adapter_enabled', 'Thinking suffix adapter enabled', 'Enable the virtual -thinking model suffix.', RISK_ROUTING, 'true'),
              decimal('claude.thinking_adapter_budget_tokens_percentage', 'Thinking budget share', 0.1, 1, 'Share of max output tokens assigned to Claude thinking.', RISK_ROUTING, '0.8'),
            ],
          },
        ],
      },
      {
        id: 'grok',
        title: 'Grok',
        description: 'Reference-compatible Grok policy route.',
        groups: [{
          title: 'Violation fees',
          description: 'Configure the atomic post-refund fee for exact xAI safety-policy rejections.',
          settings: [
            {
              ...bool(
                'grok.violation_deduction_enabled',
                'Violation deduction enabled',
                'Charge the configured fee after refunding a request rejected by an exact xAI safety marker.',
                RISK_BILLING,
                'true',
              ),
              required: true,
            },
            {
              ...decimal(
                'grok.violation_deduction_amount',
                'Violation deduction amount (USD)',
                0,
                MAX_GROK_VIOLATION_AMOUNT,
                'Base USD-equivalent fee before applying the effective group ratio.',
                RISK_BILLING,
                '0.05',
              ),
              required: true,
            },
          ],
        }],
      },
      {
        id: 'channel-affinity',
        title: 'Channel affinity',
        description: 'Sticky-channel cache policy and ordered matching rules.',
        groups: [{
          title: 'Channel affinity',
          settings: [
            bool('channel_affinity_setting.enabled', 'Affinity enabled', undefined, RISK_ROUTING, 'true'),
            bool('channel_affinity_setting.switch_on_success', 'Switch affinity after successful retry', undefined, RISK_ROUTING, 'true'),
            bool('channel_affinity_setting.keep_on_channel_disabled', 'Keep entries for disabled channels', undefined, RISK_ROUTING, 'false'),
            integer('channel_affinity_setting.max_entries', 'Maximum cache entries', 0, 1_000_000, undefined, RISK_ROUTING, '100000'),
            integer('channel_affinity_setting.default_ttl_seconds', 'Default TTL (seconds)', 0, 2_592_000, undefined, RISK_ROUTING, '3600'),
            json('channel_affinity_setting.rules', 'Affinity rules (JSON)', undefined, RISK_ROUTING, JSON_LIMIT, '[]'),
          ],
        }],
      },
      {
        id: 'model-deployment',
        title: 'Model deployment',
        description: 'io.net deployment access. The API key is write-only.',
        groups: [{
          title: 'io.net deployment',
          settings: [
            bool('model_deployment.ionet.enabled', 'io.net deployment enabled', undefined, RISK_ROUTING, 'false'),
            secret('model_deployment.ionet.api_key', 'io.net API key'),
          ],
        }],
      },
    ],
  },
  {
    id: 'security',
    label: 'Security & Limits',
    sections: [
      {
        id: 'rate-limit',
        title: 'Rate limiting',
        description: 'Per-user model request limits. Web and dashboard throttles remain deployment configuration.',
        groups: [{
          title: 'Model request limits',
          settings: [
            bool('ModelRequestRateLimitEnabled', 'Model request limiting enabled', 'Applies after relay-token authentication to the OpenAI and Gemini protocol routes.', RISK_SECURITY, 'false'),
            integer('ModelRequestRateLimitDurationMinutes', 'Rate-limit window (minutes)', 1, 43_200, undefined, RISK_SECURITY, '1'),
            integer('ModelRequestRateLimitCount', 'Total requests per window', 0, 100_000_000, 'Zero disables the total-request cap.', RISK_SECURITY, '0'),
            integer('ModelRequestRateLimitSuccessCount', 'Successful requests per window', 1, 100_000_000, undefined, RISK_SECURITY, '1000'),
            {
              ...json('ModelRequestRateLimitGroup', 'Group request limits (JSON)', 'Map each token group to [total requests, successful requests]. A zero total is unlimited.', RISK_SECURITY, 64 * 1024, '{}'),
              format: 'model-request-rate-limits',
            },
          ],
        }],
      },
      {
        id: 'sensitive-words',
        title: 'Sensitive words',
        description: 'Prompt filtering policy and blocked terms.',
        groups: [{
          title: 'Sensitive words',
          settings: [
            bool('CheckSensitiveEnabled', 'Sensitive-word check enabled', undefined, RISK_SECURITY, 'true'),
            bool('CheckSensitiveOnPromptEnabled', 'Check prompts', undefined, RISK_SECURITY, 'true'),
            { key: 'SensitiveWords', label: 'Sensitive words (comma/newline)', kind: 'textarea', maxLength: CONTENT_LIMIT, confirmation: RISK_SECURITY },
          ],
        }],
      },
      {
        id: 'ssrf', title: 'SSRF protection', description: 'Deployment-wide outbound network safety enforced below application features.', groups: [],
        unavailableReason: 'TokenRouter enforces SSRF protection in its safe network transport and exposes no mutable SSRF option contract.',
      },
      {
        id: 'token-limits',
        title: 'Token limits',
        description: 'Maximum access tokens allowed per user.',
        groups: [{
          title: 'Token limits',
          settings: [integer('MaxUserTokens', 'Maximum tokens per user', 1, 1_000_000, undefined, RISK_SECURITY, '1000')],
        }],
      },
    ],
  },
  {
    id: 'content',
    label: 'Console Content',
    sections: [
      {
        id: 'dashboard',
        title: 'Dashboard content',
        description: 'Usage-histogram collection used by dashboards and rankings.',
        groups: [{
          title: 'Dashboard data',
          settings: [
            bool('DataExportEnabled', 'Data export enabled', undefined, 'Disabling this stops new dashboard usage histogram aggregation.', 'true'),
            integer('DataExportInterval', 'Data export interval (minutes)', 1, 1_440, undefined, undefined, '5'),
          ],
        }],
      },
      {
        id: 'announcements',
        title: 'Announcements',
        description: 'Publish a bounded public timeline alongside the system notice.',
        groups: [{
          title: 'Announcements',
          settings: [
            bool(
              'console_setting.announcements_enabled',
              'System announcements enabled',
              undefined,
              'Disabling this hides the public announcement timeline. Continue?',
              'true',
            ),
            json(
              'console_setting.announcements',
              'Announcement timeline (JSON)',
              'Up to 100 newest-first entries with content, an RFC 3339 publishDate, and optional id, type, and extra fields.',
              'This replaces the public announcement timeline. Continue?',
              CONTENT_LIMIT,
              '[]',
            ),
          ],
        }],
      },
      {
        id: 'api-info',
        title: 'API information',
        description: 'Publish a bounded list of API addresses on the authenticated dashboard.',
        groups: [{
          title: 'API information',
          settings: [
            bool(
              'console_setting.api_info_enabled',
              'API information enabled',
              'Show configured API address cards on the dashboard.',
              'Disabling this hides the API information panel. Continue?',
              'true',
            ),
            {
              ...json(
                'console_setting.api_info',
                'API information entries (JSON)',
                'Up to 50 entries with url, route, description, color, and an optional numeric id.',
                'This replaces every public API information entry. Continue?',
                CONTENT_LIMIT,
                '[]',
              ),
              format: 'console-api-info',
            },
          ],
        }],
      },
      {
        id: 'faq',
        title: 'FAQ',
        description: 'Publish bounded questions and answers on the authenticated dashboard.',
        groups: [{
          title: 'FAQ',
          settings: [
            bool(
              'console_setting.faq_enabled',
              'FAQ enabled',
              'Show configured questions and answers on the dashboard.',
              'Disabling this hides the FAQ panel. Continue?',
              'true',
            ),
            {
              ...json(
                'console_setting.faq',
                'FAQ entries (JSON)',
                'Up to 100 entries with question, answer, and an optional numeric id.',
                'This replaces every public FAQ entry. Continue?',
                CONTENT_LIMIT,
                '[]',
              ),
              format: 'console-faq',
            },
          ],
        }],
      },
      {
        id: 'uptime-kuma',
        title: 'Uptime Kuma',
        description: 'Fetch bounded grouped monitor status through the protected server transport.',
        groups: [{
          title: 'Uptime Kuma',
          settings: [
            bool(
              'console_setting.uptime_kuma_enabled',
              'Uptime Kuma enabled',
              'Fetch and show configured status-page groups on the dashboard.',
              'Disabling this stops status-page fetches and hides the panel. Continue?',
              'true',
            ),
            {
              ...json(
                'console_setting.uptime_kuma_groups',
                'Uptime Kuma groups (JSON)',
                'Up to 20 groups with categoryName, HTTPS url, slug, and optional description and numeric id.',
                'This replaces every Uptime Kuma group. Continue?',
                CONTENT_LIMIT,
                '[]',
              ),
              format: 'console-uptime-kuma-groups',
            },
          ],
        }],
      },
      {
        id: 'chat',
        title: 'Chat',
        description: 'Public chat launchers. Each array item must contain exactly one name-to-URL entry.',
        groups: [{ title: 'Chat launchers', settings: [json('Chats', 'Chat launchers (JSON)', undefined, undefined, 64 * 1024, '[]')] }],
      },
      {
        id: 'drawing',
        title: 'Drawing',
        description: 'Image-task callback policy supported by TokenRouter.',
        groups: [{
          title: 'Drawing',
          settings: [bool('MjForwardUrlEnabled', 'Forward image task URLs', undefined, RISK_ROUTING, 'true')],
        }],
      },
    ],
  },
  {
    id: 'operations',
    label: 'Operations',
    sections: [
      {
        id: 'behavior',
        title: 'System behavior',
        description: 'Instance-wide operating mode advertised by the status endpoint.',
        groups: [{
          title: 'System behavior',
          settings: [
            bool('SelfUseModeEnabled', 'Self-use mode enabled', undefined, 'Changing the operating mode affects public onboarding. Continue?', 'false'),
            bool('DemoSiteEnabled', 'Demo site enabled', undefined, 'Changing the operating mode affects public onboarding. Continue?', 'false'),
            bool('DefaultCollapseSidebar', 'Collapse sidebar by default', 'Authenticated users can still expand or collapse it for their current session.', undefined, 'false'),
          ],
        }],
      },
      {
        id: 'alerts',
        title: 'Alerts and metrics',
        description: 'Low-balance alerts and performance-metric retention.',
        groups: [{
          title: 'Alerts and metrics',
          settings: [
            integer('QuotaRemindThreshold', 'Low-quota reminder threshold', 0, MAX_QUOTA),
            bool('perf_metrics_setting.enabled', 'Metrics enabled', undefined, undefined, 'true'),
            integer('perf_metrics_setting.flush_interval', 'Flush interval (minutes)', 1, 1_440, undefined, undefined, '5'),
            {
              key: 'perf_metrics_setting.bucket_time', label: 'Bucket size', kind: 'select', defaultValue: 'hour',
              choices: [{ value: 'minute', label: 'Minute' }, { value: '5min', label: '5 minutes' }, { value: 'hour', label: 'Hour' }],
            },
            integer('perf_metrics_setting.retention_days', 'Retention (days; 0 keeps all)', 0, 36_500, undefined, undefined, '0'),
          ],
        }],
      },
      {
        id: 'email', title: 'Email', description: 'Configure the atomic SMTP delivery profile used for verification and password-reset messages.', groups: [],
      },
      {
        id: 'worker', title: 'Worker', description: 'Reference-compatible worker proxy route.', groups: [],
        unavailableReason: 'TokenRouter uses bounded SSRF-safe direct transports and has no delegated worker trust contract to configure.',
      },
      {
        id: 'logs', title: 'Logs', description: 'Manage durable usage history and bounded local log-file retention.', groups: [],
      },
      {
        id: 'performance',
        title: 'Performance',
        description: 'Disk-cache and host-monitor policy consumed by the performance controller.',
        groups: [{
          title: 'Performance policy',
          settings: [json('performance_setting', 'Performance settings (JSON)', 'Disk-cache thresholds, path, and monitor percentages.', 'This changes host-level cache and monitoring behavior. Continue?', 64 * 1024, '{}')],
        }],
      },
      {
        id: 'update-checker', title: 'Update checker', description: 'Compare this running build with the latest published TokenRouter release.', groups: [],
      },
    ],
  },
] as const satisfies readonly SettingsCategoryDefinition[];

export type SystemSettingsCategory = (typeof SYSTEM_SETTINGS_CATEGORIES)[number]['id'];

export const SYSTEM_SETTINGS_CATEGORY_IDS = SYSTEM_SETTINGS_CATEGORIES.map((category) => category.id) as SystemSettingsCategory[];

const SETTINGS_CATALOG: readonly SettingsCategoryDefinition[] = SYSTEM_SETTINGS_CATEGORIES;

export function resolveSettingsPath(settingsPath?: string): {
  category: SettingsCategoryDefinition;
  section: SettingsSectionDefinition;
} {
  const [categoryId, sectionId, extra] = (settingsPath ?? '').split('/');
  const category = SETTINGS_CATALOG.find((candidate) => candidate.id === categoryId)
    ?? SETTINGS_CATALOG[0];
  const section = extra === undefined
    ? category.sections.find((candidate) => candidate.id === sectionId) ?? category.sections[0]
    : category.sections[0];
  return { category, section };
}

export const SYSTEM_SETTING_KEYS = new Set(
  SETTINGS_CATALOG.flatMap((category) => category.sections)
    .flatMap((section) => section.groups)
    .flatMap((group) => group.settings)
    .map((setting) => setting.key),
);

const GEMINI_SAFETY_THRESHOLDS = new Set([
  'OFF',
  'BLOCK_NONE',
  'BLOCK_ONLY_HIGH',
  'BLOCK_MEDIUM_AND_ABOVE',
  'BLOCK_LOW_AND_ABOVE',
  'HARM_BLOCK_THRESHOLD_UNSPECIFIED',
]);
const FORBIDDEN_CLAUDE_HEADERS = new Set([
  'authorization', 'proxy-authorization', 'x-api-key', 'api-key', 'anthropic-version', 'host',
  'content-length', 'content-type', 'connection', 'keep-alive', 'proxy-authenticate', 'te',
  'trailer', 'transfer-encoding', 'upgrade', 'cookie', 'set-cookie', 'forwarded',
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isValidPolicyIdentifier(value: string): boolean {
  if (value === '' || value.trim() !== value || new TextEncoder().encode(value).byteLength > 256) return false;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 0x20 || code >= 0x7f && code <= 0x9f || code === 0x061c
      || code === 0x200e || code === 0x200f || code >= 0x202a && code <= 0x202e
      || code >= 0x2066 && code <= 0x2069) return false;
  }
  return true;
}

function validatesStringMap(
  value: unknown,
  allowedValue: (candidate: string) => boolean,
): value is Record<string, string> {
  return isRecord(value) && Object.entries(value).length <= 256
    && Object.entries(value).every(([key, candidate]) => isValidPolicyIdentifier(key)
      && typeof candidate === 'string' && allowedValue(candidate));
}

function validClaudeHeaderName(name: string): boolean {
  const lower = name.toLowerCase();
  return /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/u.test(name)
    && !FORBIDDEN_CLAUDE_HEADERS.has(lower)
    && (lower.startsWith('anthropic-') || lower.startsWith('x-'));
}

function validClaudeHeaderValue(value: string): boolean {
  if (value === '' || value.trim() !== value || new TextEncoder().encode(value).byteLength > 2_048) return false;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 0x20 || code === 0x7f || code > 0xff) return false;
  }
  return true;
}

function validateClaudeHeaders(value: unknown): boolean {
  if (!isRecord(value) || Object.keys(value).length > 256) return false;
  let aggregateBytes = 0;
  return Object.entries(value).every(([model, rawHeaders]) => {
    if (!isValidPolicyIdentifier(model) || !isRecord(rawHeaders) || Object.keys(rawHeaders).length > 32) return false;
    const seen = new Set<string>();
    return Object.entries(rawHeaders).every(([name, rawValues]) => {
      const lower = name.toLowerCase();
      if (!validClaudeHeaderName(name) || seen.has(lower) || !Array.isArray(rawValues)
        || rawValues.length === 0 || rawValues.length > 32) return false;
      seen.add(lower);
      return rawValues.every((rawValue) => {
        if (typeof rawValue !== 'string' || !validClaudeHeaderValue(rawValue)) return false;
        aggregateBytes += new TextEncoder().encode(name + rawValue).byteLength;
        return aggregateBytes <= 64 * 1_024;
      });
    });
  });
}

function validateModelPolicyFormat(format: SettingInputFormat | undefined, value: string): string | null {
  if (value.trim() === '') return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(value) as unknown;
  } catch {
    return null;
  }
  switch (format) {
    case 'gemini-safety-settings':
      return validatesStringMap(parsed, (candidate) => candidate === '' || GEMINI_SAFETY_THRESHOLDS.has(candidate))
        ? null : 'Enter valid bounded Gemini safety settings.';
    case 'gemini-version-settings':
      return validatesStringMap(parsed, (candidate) => candidate === 'v1' || candidate === 'v1beta')
        ? null : 'Enter valid bounded Gemini API versions.';
    case 'model-id-list':
      return Array.isArray(parsed) && parsed.length <= 256
        && parsed.every((candidate) => typeof candidate === 'string' && isValidPolicyIdentifier(candidate))
        && new Set(parsed).size === parsed.length
        ? null : 'Enter a valid bounded list of unique model IDs.';
    case 'claude-header-settings':
      return validateClaudeHeaders(parsed) ? null : 'Enter valid bounded Claude model headers.';
    case 'claude-max-tokens':
      return isRecord(parsed) && Object.keys(parsed).length <= 256
        && Object.entries(parsed).every(([model, tokens]) => isValidPolicyIdentifier(model)
          && typeof tokens === 'number' && Number.isSafeInteger(tokens) && tokens >= 0 && tokens <= 1_000_000)
        ? null : 'Enter valid bounded Claude max-token defaults.';
    default:
      return null;
  }
}

function parseIPv4Literal(hostname: string): number[] | null {
  const parts = hostname.split('.');
  if (parts.length !== 4 || parts.some((part) => !/^(?:0|[1-9]\d{0,2})$/u.test(part))) return null;
  const octets = parts.map(Number);
  return octets.every((octet) => octet <= 255) ? octets : null;
}

function isUnsafeOAuthLiteralHost(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/gu, '').replace(/\.$/u, '').toLowerCase();
  const ipv4 = parseIPv4Literal(host);
  if (ipv4) {
    const [first, second] = ipv4;
    return first === 0 || first === 10 || first === 127 || first >= 224
      || first === 100 && second >= 64 && second <= 127
      || first === 169 && second === 254
      || first === 172 && second >= 16 && second <= 31
      || first === 192 && second === 168
      || first === 198 && (second === 18 || second === 19);
  }
  if (!host.includes(':')) return false;
  return host === '::' || host === '::1' || /^f[cd]/u.test(host)
    || /^fe[89ab]/u.test(host);
}

function isLoopbackOAuthHost(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/gu, '').replace(/\.$/u, '').toLowerCase();
  const ipv4 = parseIPv4Literal(host);
  return host === 'localhost' || host.endsWith('.localhost') || host === '::1'
    || ipv4?.[0] === 127;
}

function hasAmbiguousRawURLPath(value: string): boolean {
  const schemeEnd = value.indexOf('://');
  if (schemeEnd < 0) return true;
  const pathStart = value.indexOf('/', schemeEnd + 3);
  if (pathStart < 0) return false;
  const queryStart = value.indexOf('?', pathStart);
  const fragmentStart = value.indexOf('#', pathStart);
  const candidates = [queryStart, fragmentStart].filter((index) => index >= 0);
  const pathEnd = candidates.length > 0 ? Math.min(...candidates) : value.length;
  return value.slice(pathStart, pathEnd).split('/').some((segment) => {
    try {
      const decoded = decodeURIComponent(segment);
      return decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\');
    } catch {
      return true;
    }
  });
}

export function validateSettingValue(setting: SettingDefinition, value: string): string | null {
  const maximum = setting.maxLength ?? CONTENT_LIMIT;
  if (new TextEncoder().encode(value).byteLength > maximum) return 'Value is too long or contains an invalid character.';
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    const permittedWhitespace = code === 0x09 || code === 0x0a || code === 0x0d;
    if ((!permittedWhitespace && code <= 0x1f) || (code >= 0x7f && code <= 0x9f)
      || code === 0x061c || code === 0x200e || code === 0x200f
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) {
      return 'Value is too long or contains an invalid character.';
    }
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) return 'Value is too long or contains an invalid character.';
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return 'Value is too long or contains an invalid character.';
    }
  }
  if (setting.required && value.trim() === '') {
    return setting.kind === 'boolean' || setting.kind === 'select'
      ? 'Choose a valid option.'
      : 'Enter a value within the allowed range.';
  }
  if (setting.kind === 'secret' && value === '') return 'Enter a replacement value.';
  if (setting.kind === 'json' && value.trim() !== '') {
    try {
      JSON.parse(value);
    } catch {
      return 'Enter valid JSON.';
    }
  }
  const billingError = validateBillingSettingFormat(setting.format, value);
  if (billingError) return billingError;
  if (setting.format === 'usage-ratio-map') {
    const ratioError = validateUsageRatioMap(value);
    if (ratioError) return ratioError;
  }
  const modelPolicyError = validateModelPolicyFormat(setting.format, value);
  if (modelPolicyError) return modelPolicyError;
  if (setting.format === 'model-request-rate-limits') {
    const rateLimitError = validateModelRequestRateLimitGroups(value);
    if (rateLimitError) return rateLimitError;
  }
  if (setting.format === 'registration-group-directives') {
    const groupDirectiveError = validateSpecialUsableGroupDirectives(value);
    if (groupDirectiveError) return groupDirectiveError;
  }
  if (setting.format === 'tool-price-map') {
    const toolPriceError = validateToolPriceMap(value);
    if (toolPriceError) return toolPriceError;
  }
  if (setting.format === 'http-status-ranges') {
    const statusRangeError = validateHTTPStatusRanges(value);
    if (statusRangeError) return statusRangeError;
  }
  if (setting.format === 'channel-disable-keywords') {
    const keywordError = validateChannelDisableKeywords(value);
    if (keywordError) return keywordError;
  }
  if (setting.format === 'console-api-info') {
    try {
      parseConsoleOptionJSON(value, parseConsoleAPIInfo);
    } catch {
      return 'Enter API information as a valid bounded JSON array.';
    }
  }
  if (setting.format === 'console-faq') {
    try {
      parseConsoleOptionJSON(value, parseConsoleFAQ);
    } catch {
      return 'Enter FAQ entries as a valid bounded JSON array.';
    }
  }
  if (setting.format === 'console-uptime-kuma-groups') {
    try {
      parseConsoleOptionJSON(value, parseConsoleUptimeKumaGroups);
    } catch {
      return 'Enter Uptime Kuma groups as a valid bounded JSON array.';
    }
  }
  if ((setting.kind === 'integer' || setting.kind === 'decimal') && value.trim() !== '') {
    const pattern = setting.kind === 'integer' ? /^-?(?:0|[1-9]\d*)$/u : /^-?(?:0|[1-9]\d*)(?:\.\d+)?$/u;
    if (!pattern.test(value.trim())) return setting.kind === 'integer' ? 'Enter a whole number.' : 'Enter a decimal number.';
    const parsed = Number(value.trim());
    if (!Number.isFinite(parsed) || !Number.isSafeInteger(parsed) && setting.kind === 'integer'
      || setting.min !== undefined && parsed < setting.min || setting.max !== undefined && parsed > setting.max) {
      return 'Enter a value within the allowed range.';
    }
  }
  if (setting.kind === 'select' && !setting.choices?.some((choice) => choice.value === value)) {
    return 'Choose a valid option.';
  }
  if (setting.kind === 'boolean' && value !== '' && value !== 'true' && value !== 'false') {
    return 'Choose a valid option.';
  }
  if (setting.format === 'telegram-bot-name' && value !== '') {
    const normalized = value.trim().replace(/^@/u, '');
    if (!/^[A-Za-z0-9_]{5,32}$/u.test(normalized)
      || !/bot$/iu.test(normalized)) {
      return 'Enter a valid Telegram bot username.';
    }
  }
  if (setting.format === 'telegram-bot-token' && value !== '') {
    const [identifier, token, extra] = value.split(':');
    if (value !== value.trim() || extra !== undefined
      || !/^\d{5,20}$/u.test(identifier ?? '')
      || identifier === undefined || BigInt(identifier) === 0n
      || !/^[A-Za-z0-9_-]{20,128}$/u.test(token ?? '')) {
      return 'Enter a valid Telegram bot token.';
    }
  }
  if (setting.format === 'hostname' && value !== '') {
    const hostname = value.trim();
    const labels = hostname.split('.');
    const ipv4 = labels.length === 4 && labels.every((label) => /^\d{1,3}$/u.test(label) && Number(label) <= 255);
    let ipv6 = false;
    if (hostname.includes(':')) {
      try {
        const parsed = new URL(`http://[${hostname}]/`);
        ipv6 = parsed.hostname.startsWith('[') && parsed.hostname.endsWith(']');
      } catch {
        ipv6 = false;
      }
    }
    const domain = (hostname === 'localhost' || hostname.includes('.')) && labels.every((label) => (
      label.length > 0 && label.length <= 63
      && /^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/u.test(label)
    ));
    if (hostname !== value || hostname.length > 253 || !ipv4 && !ipv6 && !domain) {
      return 'Enter a valid hostname.';
    }
  }
  if (setting.format === 'email-domain-list' && value.trim() !== '') {
    const domains = value.split(/[,\r\n]+/u).map((domain) => domain.trim().toLowerCase()).filter(Boolean);
    const validDomain = (domain: string) => domain.length <= 253 && !/^\d+(?:\.\d+){3}$/u.test(domain)
      && domain.split('.').every((label) => label.length > 0 && label.length <= 63
        && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/u.test(label));
    if (domains.length === 0 || domains.length > 100 || !domains.every(validDomain)
      || new Set(domains).size !== domains.length) {
      return 'Enter a valid list of unique email domains.';
    }
  }
  if (setting.format === 'oauth-endpoint' && value !== '') {
    if (value !== value.trim() || value.includes('\\') || value.endsWith('?') || hasAmbiguousRawURLPath(value)) {
      return 'Enter a safe HTTPS OAuth endpoint.';
    }
    try {
      const parsed = new URL(value);
      const loopback = isLoopbackOAuthHost(parsed.hostname);
      const queryKeys = [...parsed.searchParams.keys()];
      if (parsed.username !== '' || parsed.password !== '' || parsed.hash !== ''
        || parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && loopback)
        || parsed.protocol === 'https:' && isUnsafeOAuthLiteralHost(parsed.hostname)
        || queryKeys.length > 16 || new Set(queryKeys).size !== queryKeys.length
        || [...parsed.searchParams].some(([key, candidate]) => !isValidPolicyIdentifier(key)
          || new TextEncoder().encode(candidate).byteLength > 2_048
          || candidate !== '' && !isValidPolicyIdentifier(candidate))) {
        return 'Enter a safe HTTPS OAuth endpoint.';
      }
    } catch {
      return 'Enter a safe HTTPS OAuth endpoint.';
    }
  }
  if (setting.format === 'passkey-origins' && value.trim() !== '') {
    const origins = value.split(/[,\r\n]+/u).map((origin) => origin.trim()).filter(Boolean);
    if (origins.length === 0 || origins.length > 32 || new Set(origins).size !== origins.length) {
      return 'Enter a valid list of unique passkey origins.';
    }
    for (const origin of origins) {
      try {
        const parsed = new URL(origin);
        if (origin !== origin.trim() || parsed.username !== '' || parsed.password !== '' || parsed.hash !== ''
          || parsed.search !== '' || parsed.pathname !== '/' || parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
          return 'Enter a valid list of unique passkey origins.';
        }
      } catch {
        return 'Enter a valid list of unique passkey origins.';
      }
    }
  }
  return null;
}
