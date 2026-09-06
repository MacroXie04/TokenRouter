import { describe, expect, it } from 'vitest';
import {
  PublicStatusContractError,
  parsePublicStatus,
} from './public-status';

function validStatus(overrides: Record<string, unknown> = {}) {
  return {
    system_name: 'Acme Gateway',
    site_name: 'Legacy Acme',
    app_name: 'TokenRouter',
    logo: 'https://cdn.example/logo.png',
    HeaderNavModules: '{"pricing":{"enabled":true}}',
    SidebarModulesAdmin: '{"chat":{"playground":false}}',
    announcements_enabled: true,
    announcements: [
      { id: 'older', type: 'warning', content: 'Older', publishDate: '2026-01-01T00:00:00Z' },
      { id: 2, type: 'success', content: 'Newer\n\nSecond paragraph', extra: 'Details\ncontinued', publishDate: '2026-02-01T00:00:00Z' },
    ],
    wechat_login: true,
    wechat_qrcode: '/api/wechat/qr',
    password_login_enabled: true,
    turnstile_check: true,
    turnstile_site_key: 'site_key-1',
    telegram_oauth: true,
    telegram_bot_name: 'TokenRouter_bot',
    custom_oauth_providers: [{
      id: 9,
      name: 'Company SSO',
      slug: 'company-sso',
      icon: 'https://identity.example/icon.png',
      client_secret: 'ignored-by-selection',
    }],
    ...overrides,
  };
}

describe('public status contract', () => {
  it('selects bounded browser metadata and conservative defaults', () => {
    expect(parsePublicStatus(validStatus())).toMatchObject({
      system_name: 'Acme Gateway',
      site_name: 'Legacy Acme',
      logo: 'https://cdn.example/logo.png',
      SidebarModulesAdmin: '{"chat":{"playground":false}}',
      announcements_enabled: true,
      announcements: [
        { id: 2, type: 'success', content: 'Newer\n\nSecond paragraph', extra: 'Details\ncontinued', publishDate: '2026-02-01T00:00:00Z' },
        { id: 'older', type: 'warning', content: 'Older', publishDate: '2026-01-01T00:00:00Z' },
      ],
      wechat_login: true,
      wechat_qrcode: '/api/wechat/qr',
      default_collapse_sidebar: false,
      register_enabled: true,
      email_verification: false,
      turnstile_check: true,
      turnstile_site_key: 'site_key-1',
      telegram_oauth: true,
      telegram_bot_name: 'TokenRouter_bot',
      custom_oauth_providers: [{
        id: 9,
        name: 'Company SSO',
        slug: 'company-sso',
        icon: 'https://identity.example/icon.png',
      }],
    });
    expect(parsePublicStatus(validStatus({ default_collapse_sidebar: true })).default_collapse_sidebar).toBe(true);
  });

  it('omits unsafe optional artwork without exposing it to the DOM', () => {
    const parsed = parsePublicStatus(validStatus({
      logo: 'data:image/svg+xml,<svg/>',
      wechat_qrcode: 'data:image/svg+xml,<svg/>',
      custom_oauth_providers: [{
        id: 9, name: 'Company SSO', slug: 'company-sso', icon: 'javascript:alert(1)',
      }],
    }));
    expect(parsed.logo).toBe('');
    expect(parsed.wechat_qrcode).toBe('');
    expect(parsed.custom_oauth_providers[0].icon).toBe('');
  });

  it('accepts only coherent advertised built-in OAuth and passkey metadata', () => {
    const parsed = parsePublicStatus(validStatus({
      github_oauth: true,
      github_client_id: 'github-client',
      discord_oauth: true,
      discord_client_id: 'discord-client',
      oidc_enabled: true,
      oidc_client_id: 'oidc-client',
      oidc_authorization_endpoint: 'https://identity.example.test/authorize?tenant=one',
      oidc_display_name: 'Workforce SSO',
      linuxdo_oauth: true,
      linuxdo_client_id: 'linuxdo-client',
      linuxdo_minimum_trust_level: 3,
      passkey_login: true,
      passkey_display_name: 'Acme Passkeys',
      passkey_rp_id: 'example.test',
      passkey_origins: 'https://example.test,https://login.example.test:8443',
      passkey_allow_insecure: false,
      passkey_user_verification: 'required',
      passkey_attachment: 'platform',
    }));
    expect(parsed).toMatchObject({
      github_client_id: 'github-client',
      oidc_display_name: 'Workforce SSO',
      linuxdo_minimum_trust_level: 3,
      passkey_rp_id: 'example.test',
      passkey_user_verification: 'required',
      passkey_attachment: 'platform',
    });
  });

  it.each([
    ['non-object', []],
    ['wrong boolean type', validStatus({ github_oauth: 1 })],
    ['wrong sidebar-collapse type', validStatus({ default_collapse_sidebar: 'yes' })],
    ['wrong Telegram flag type', validStatus({ telegram_oauth: 'yes' })],
    ['enabled GitHub without metadata', validStatus({ github_oauth: true })],
    ['disabled GitHub with stale metadata', validStatus({ github_oauth: false, github_client_id: 'stale' })],
    ['unsafe OIDC authorization endpoint', validStatus({
      oidc_enabled: true,
      oidc_client_id: 'client',
      oidc_authorization_endpoint: 'https://127.0.0.1/authorize',
      oidc_display_name: 'SSO',
    })],
    ['duplicate OIDC query parameter', validStatus({
      oidc_enabled: true,
      oidc_client_id: 'client',
      oidc_authorization_endpoint: 'https://identity.example/authorize?tenant=one&tenant=two',
      oidc_display_name: 'SSO',
    })],
    ['invalid Linux DO trust level', validStatus({
      linuxdo_oauth: true, linuxdo_client_id: 'client', linuxdo_minimum_trust_level: 5,
    })],
    ['passkey origin outside RP ID', validStatus({
      passkey_login: true,
      passkey_display_name: 'Passkeys',
      passkey_rp_id: 'example.test',
      passkey_origins: 'https://outside.test',
      passkey_allow_insecure: false,
      passkey_user_verification: 'preferred',
      passkey_attachment: '',
    })],
    ['public insecure passkey origin without opt-in', validStatus({
      passkey_login: true,
      passkey_display_name: 'Passkeys',
      passkey_rp_id: 'example.test',
      passkey_origins: 'http://example.test',
      passkey_allow_insecure: false,
      passkey_user_verification: 'preferred',
      passkey_attachment: '',
    })],
    ['invalid passkey preference', validStatus({
      passkey_login: true,
      passkey_display_name: 'Passkeys',
      passkey_rp_id: 'example.test',
      passkey_origins: 'https://example.test',
      passkey_allow_insecure: false,
      passkey_user_verification: 'optional',
      passkey_attachment: '',
    })],
    ['control-bearing Telegram bot', validStatus({ telegram_bot_name: 'router\nbot' })],
    ['oversized Telegram bot', validStatus({ telegram_bot_name: 'x'.repeat(65) })],
    ['control-bearing provider name', validStatus({ custom_oauth_providers: [{ id: 1, name: 'bad\nname', slug: 'bad', icon: '' }] })],
    ['invalid provider slug', validStatus({ custom_oauth_providers: [{ id: 1, name: 'Bad', slug: '../bad', icon: '' }] })],
    ['duplicate provider slug', validStatus({ custom_oauth_providers: [
      { id: 1, name: 'First', slug: 'same', icon: '' },
      { id: 2, name: 'Second', slug: 'same', icon: '' },
    ] })],
    ['too many providers', validStatus({ custom_oauth_providers: Array.from(
      { length: 129 },
      (_, index) => ({ id: index + 1, name: `Provider ${index}`, slug: `provider-${index}`, icon: '' }),
    ) })],
    ['wrong announcement flag type', validStatus({ announcements_enabled: 'yes' })],
    ['non-array announcements', validStatus({ announcements: {} })],
    ['too many announcements', validStatus({ announcements: Array.from(
      { length: 101 },
      (_, index) => ({ id: index, content: `Item ${index}`, publishDate: '2026-01-01T00:00:00Z' }),
    ) })],
    ['invalid announcement date', validStatus({ announcements: [{ content: 'Update', publishDate: 'tomorrow' }] })],
    ['invalid announcement type', validStatus({ announcements: [{ content: 'Update', type: 'critical', publishDate: '2026-01-01T00:00:00Z' }] })],
    ['oversized announcement content', validStatus({ announcements: [{ content: 'x'.repeat(501), publishDate: '2026-01-01T00:00:00Z' }] })],
    ['duplicate announcement id', validStatus({ announcements: [
      { id: 'same', content: 'First', publishDate: '2026-01-01T00:00:00Z' },
      { id: 'same', content: 'Second', publishDate: '2026-01-02T00:00:00Z' },
    ] })],
  ])('rejects %s', (_name, value) => {
    expect(() => parsePublicStatus(value)).toThrow(PublicStatusContractError);
  });

  it('rejects oversized aggregate payloads before walking nested data', () => {
    expect(() => parsePublicStatus(validStatus({ ignored: 'x'.repeat(512 * 1024) })))
      .toThrow(PublicStatusContractError);
  });
});
