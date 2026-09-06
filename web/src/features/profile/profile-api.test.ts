import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  beginPasskeyRegistration,
  bindEmail,
  bindWeChat,
  changePassword,
  createOAuthBindingURL,
  deleteAccount,
  deletePasskeys,
  disableTwoFactor,
  enableTwoFactor,
  finishPasskeyRegistration,
  generateAccessToken,
  getOAuthBindings,
  getOAuthCatalog,
  getPasskeys,
  getPasskeyStatus,
  getProfile,
  getSessions,
  getTwoFactorStatus,
  parseAccessTokenResponse,
  parseBackupCodesResponse,
  parseOAuthBindingFlowResponse,
  parseOAuthBindingsResponse,
  parseOAuthCatalogResponse,
  parsePasskeyRegistrationBeginResponse,
  parsePasskeysResponse,
  parseProfileResponse,
  parseSessionsResponse,
  parseTelegramBindingFlowResponse,
  parseTwoFactorSetupResponse,
  ProfileContractError,
  regenerateBackupCodes,
  revokeOtherSessions,
  revokeSession,
  sendEmailVerification,
  startTelegramBinding,
  startTwoFactorSetup,
  unbindOAuth,
  updateDisplayName,
  updateLanguage,
  updateNotificationSettings,
  updateSidebarModules,
  verifyTwoFactor,
  type CustomOAuthProvider,
} from './profile-api';

vi.mock('../../shared/api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const mockedAPI = vi.mocked(api);
const limits = { maxContentLength: 512 * 1024, maxBodyLength: 512 * 1024 };
const backupCodes = ['10000001', '10000002', '10000003', '10000004', '10000005', '10000006', '10000007', '10000008'];
const sidebarModules = {
  chat: { enabled: true, playground: false, chat: true },
  console: { enabled: true, detail: true, token: true, log: false, midjourney: true, task: true },
  personal: { enabled: true, topup: true, personal: true },
};
const serializedSidebarModules = JSON.stringify(sidebarModules);
const profileSettings = {
  language: 'en',
  sidebar_modules: serializedSidebarModules,
  billing_preference: 'wallet_only',
  future_setting: { preservedByServer: true },
  notify_type: 'webhook',
  quota_warning_threshold: 125_000,
  notification_email: 'alerts@example.test',
  webhook_url: 'https://hooks.example.test/quota',
  bark_url: 'https://bark.example.test/device/{{title}}/{{content}}',
  gotify_url: 'https://gotify.example.test',
  gotify_priority: 7,
  accept_unset_model_ratio_model: true,
  record_ip_log: false,
  upstream_model_update_notify_enabled: true,
  webhook_secret_configured: true,
  gotify_token_configured: true,
};

const profileEnvelope = {
  success: true,
  data: {
    id: 7,
    username: 'profile-user',
    display_name: 'Profile User',
    role: 1,
    status: 1,
    email: 'profile@example.test',
    email_verified: true,
    telegram_id: '99887766',
    group: 'default',
    quota: 10_000,
    used_quota: 500,
    request_count: 12,
    created_at: 1_700_000_000,
    setting: JSON.stringify(profileSettings),
    sidebar_modules: serializedSidebarModules,
  },
};

const session = {
  sid: 'session_abcdef123456',
  current: true,
  login_method: 'password',
  ip: '192.0.2.1',
  user_agent: 'Example Browser',
  created_at: 1_788_544_800,
  last_active_at: 1_788_600_000,
  expires_at: 1_789_000_000,
};

const passkey = {
  id: 3,
  user_id: 7,
  credential_id: 'public-credential-id',
  attestation_type: 'none',
  aaguid: '00000000-0000-0000-0000-000000000000',
  sign_count: 1,
  clone_warning: false,
  user_present: true,
  user_verified: true,
  backup_eligible: true,
  backup_state: true,
  transports: '["internal"]',
  attachment: 'platform',
  last_used_at: '2026-09-05T12:01:00Z',
  created_at: '2026-09-01T12:00:00Z',
  updated_at: '2026-09-05T12:01:00Z',
};

const provider: CustomOAuthProvider = {
  id: 9,
  name: 'Company SSO',
  slug: 'company-sso',
  clientId: 'public-client-id',
  authorizationEndpoint: 'https://identity.example.test/oauth/authorize',
  scopes: 'openid profile',
};

const statusEnvelope = {
  success: true,
  data: {
    github_oauth: true,
    discord_oauth: false,
    oidc_enabled: true,
    linuxdo_oauth: false,
    wechat_login: true,
    wechat_qrcode: 'https://wechat.example.test/qr.png',
    telegram_oauth: true,
    telegram_bot_name: 'TokenRouterBot',
    turnstile_check: true,
    turnstile_site_key: 'profile-site-key',
    custom_oauth_providers: [{
      id: provider.id,
      name: provider.name,
      slug: provider.slug,
      client_id: provider.clientId,
      authorization_endpoint: provider.authorizationEndpoint,
      scopes: provider.scopes,
      icon: 'ignored-public-icon',
    }],
  },
};

const bindingEnvelope = {
  success: true,
  data: [{
    provider_id: 9,
    provider_name: 'Company SSO',
    provider_slug: 'company-sso',
    provider_icon: 'ignored',
    provider_user_id: 'external-user-42',
  }],
};

beforeEach(() => vi.resetAllMocks());

describe('profile response contracts', () => {
  it('selects bounded account, session, passkey, and OAuth metadata without returning credentials', () => {
    expect(parseProfileResponse(profileEnvelope)).toEqual({
      id: 7,
      username: 'profile-user',
      displayName: 'Profile User',
      role: 1,
      status: 1,
      email: 'profile@example.test',
      emailVerified: true,
      telegramConnected: true,
      group: 'default',
      quota: 10_000,
      usedQuota: 500,
      requestCount: 12,
      createdAt: 1_700_000_000,
      language: 'en',
      sidebarModules,
      notificationSettings: {
        notifyType: 'webhook',
        quotaWarningThreshold: 125_000,
        notificationEmail: 'alerts@example.test',
        webhookURL: 'https://hooks.example.test/quota',
        webhookSecretConfigured: true,
        barkURL: 'https://bark.example.test/device/{{title}}/{{content}}',
        gotifyURL: 'https://gotify.example.test',
        gotifyTokenConfigured: true,
        gotifyPriority: 7,
        acceptUnsetRatioModel: true,
        recordIPLog: false,
        upstreamModelUpdateNotifyEnabled: true,
      },
    });
    expect(parseSessionsResponse({ success: true, data: [session] })).toEqual([{
      sid: 'session_abcdef123456',
      current: true,
      loginMethod: 'password',
      ip: '192.0.2.1',
      userAgent: 'Example Browser',
      createdAt: 1_788_544_800,
      lastActiveAt: 1_788_600_000,
      expiresAt: 1_789_000_000,
    }]);
    expect(parseSessionsResponse({
      success: true,
      data: [{ ...session, created_at: '2026-09-01T12:00:00Z' }],
    })[0]?.createdAt).toBe(1_788_264_000);
    expect(parsePasskeysResponse({ success: true, data: [passkey] })).toEqual([{
      id: 3,
      attachment: 'platform',
      createdAt: '2026-09-01T12:00:00Z',
      lastUsedAt: '2026-09-05T12:01:00Z',
      backupEligible: true,
      backupState: true,
      cloneWarning: false,
    }]);
    expect(parseOAuthCatalogResponse(statusEnvelope)).toMatchObject({
      weChatEnabled: true,
      weChatQRCode: 'https://wechat.example.test/qr.png',
      telegramEnabled: true,
      telegramBotName: 'TokenRouterBot',
      turnstileRequired: true,
      turnstileSiteKey: 'profile-site-key',
      customProviders: [provider],
    });
    expect(parseOAuthBindingsResponse(bindingEnvelope)[0]).toEqual({
      providerId: 9,
      providerName: 'Company SSO',
      providerSlug: 'company-sso',
      providerUserId: 'external-user-42',
    });
    expect(parseProfileResponse({
      success: true,
      data: {
        ...profileEnvelope.data,
        quota: Number.MAX_SAFE_INTEGER,
        used_quota: Number.MAX_SAFE_INTEGER - 1,
        request_count: Number.MAX_SAFE_INTEGER - 2,
      },
    })).toMatchObject({
      quota: Number.MAX_SAFE_INTEGER,
      usedQuota: Number.MAX_SAFE_INTEGER - 1,
      requestCount: Number.MAX_SAFE_INTEGER - 2,
    });
  });

  it('uses safe notification defaults when a new account has no saved delivery preferences', () => {
    expect(parseProfileResponse({
      success: true,
      data: {
        ...profileEnvelope.data,
        setting: JSON.stringify({
          webhook_secret_configured: false,
          gotify_token_configured: false,
        }),
        sidebar_modules: '',
      },
    }).notificationSettings).toEqual({
      notifyType: 'email',
      quotaWarningThreshold: 500_000,
      notificationEmail: '',
      webhookURL: '',
      webhookSecretConfigured: false,
      barkURL: '',
      gotifyURL: '',
      gotifyTokenConfigured: false,
      gotifyPriority: 5,
      acceptUnsetRatioModel: false,
      recordIPLog: false,
      upstreamModelUpdateNotifyEnabled: false,
    });
  });

  it('parses provisioning material and exact one-time recovery-code sets', () => {
    expect(parseTwoFactorSetupResponse({
      success: true,
      data: {
        secret: 'JBSWY3DPEHPK3PXP',
        otpauth_url: 'otpauth://totp/TokenRouter:user?secret=JBSWY3DPEHPK3PXP&issuer=TokenRouter',
      },
    })).toEqual({
      secret: 'JBSWY3DPEHPK3PXP',
      provisioningURL: 'otpauth://totp/TokenRouter:user?secret=JBSWY3DPEHPK3PXP&issuer=TokenRouter',
    });
    expect(parseBackupCodesResponse({ success: true, data: { backup_codes: backupCodes } })).toEqual(backupCodes);
    expect(parseAccessTokenResponse({ success: true, data: 'AbCdEfGhIjKlMnOpQrStUvWxYz01' }))
      .toBe('AbCdEfGhIjKlMnOpQrStUvWxYz01');
    expect(parseOAuthBindingFlowResponse({
      success: true,
      data: { flow_token: 'flow_token_123', expires_at: 1_900_000_000 },
    })).toEqual({ flowToken: 'flow_token_123', expiresAt: 1_900_000_000 });
    expect(parsePasskeyRegistrationBeginResponse({
      success: true,
      data: { flow_token: 'flow123', options: { publicKey: { challenge: 'AAECAw' } } },
    })).toEqual({ flowToken: 'flow123', publicKey: { challenge: 'AAECAw' } });
    expect(parseTelegramBindingFlowResponse({
      success: true,
      data: { flow_token: 'telegram_flow_123' },
    }, 'https://router.example.test')).toEqual({
      flowToken: 'telegram_flow_123',
      callbackURL: 'https://router.example.test/api/oauth/telegram/bind/telegram_flow_123',
    });
  });

  it('rejects disclosed secrets, malformed states, unsafe URLs, duplicates, and oversized responses', () => {
    const withNotificationSettings = (overrides: Record<string, unknown>) => ({
      success: true,
      data: {
        ...profileEnvelope.data,
        setting: JSON.stringify({ ...profileSettings, ...overrides }),
      },
    });
    const invalid = [
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, password: 'hash' } }),
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, access_token: 'secret' } }),
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, role: 11 } }),
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, quota: Number.MAX_SAFE_INTEGER + 1 } }),
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, email: '', email_verified: true } }),
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, setting: '{bad json' } }),
      () => parseProfileResponse(withNotificationSettings({ webhook_secret: 'reflected-secret' })),
      () => parseProfileResponse(withNotificationSettings({ gotify_token: '' })),
      () => parseProfileResponse(withNotificationSettings({ notify_type: 'sms' })),
      () => parseProfileResponse(withNotificationSettings({ quota_warning_threshold: 2_147_483_648 })),
      () => parseProfileResponse(withNotificationSettings({ webhook_url: 'http://hooks.example.test/quota' })),
      () => parseProfileResponse(withNotificationSettings({ bark_url: 'https://bark.example.test/{unknown}' })),
      () => parseProfileResponse(withNotificationSettings({ gotify_url: 'https://gotify.example.test?tenant=one' })),
      () => parseProfileResponse(withNotificationSettings({ gotify_priority: 11 })),
      () => parseProfileResponse(withNotificationSettings({ webhook_secret_configured: 'true' })),
      () => parseProfileResponse(withNotificationSettings({
        notify_type: 'gotify',
        gotify_token_configured: false,
      })),
      () => parseProfileResponse({
        success: true,
        data: {
          ...profileEnvelope.data,
          setting: JSON.stringify({ language: 'de', sidebar_modules: serializedSidebarModules }),
        },
      }),
      () => parseProfileResponse({
        success: true,
        data: {
          ...profileEnvelope.data,
          setting: JSON.stringify({ language: 'en', sidebar_modules: '{"admin":{"setting":true}}' }),
          sidebar_modules: '{"admin":{"setting":true}}',
        },
      }),
      () => parseProfileResponse({
        success: true,
        data: { ...profileEnvelope.data, sidebar_modules: '{}', setting: JSON.stringify({ language: 'en' }) },
      }),
		() => parseSessionsResponse({ success: true, data: [{ ...session, current: 'yes' }] }),
		() => parseSessionsResponse({ success: true, data: [{ ...session, status: 'revoked' }] }),
      () => parseSessionsResponse({ success: true, data: [session, session] }),
      () => parseSessionsResponse({ success: true, data: [{ ...session, refresh_hash: 'secret' }] }),
      () => parsePasskeysResponse({ success: true, data: [{ ...passkey, public_key: 'secret' }] }),
      () => parsePasskeysResponse({ success: true, data: [{ ...passkey, created_at: 'not-a-date' }] }),
      () => parseBackupCodesResponse({ success: true, data: { backup_codes: [...backupCodes.slice(0, 7), backupCodes[0]] } }),
      () => parseTwoFactorSetupResponse({
        success: true,
        data: { secret: 'JBSWY3DPEHPK3PXP', otpauth_url: 'otpauth://totp/user?secret=DIFFERENT' },
      }),
      () => parseOAuthCatalogResponse({
        ...statusEnvelope,
        data: { ...statusEnvelope.data, custom_oauth_providers: [{
          ...statusEnvelope.data.custom_oauth_providers[0],
          authorization_endpoint: 'javascript:alert(1)',
        }] },
      }),
      () => parseOAuthBindingsResponse({ success: true, data: [bindingEnvelope.data[0], bindingEnvelope.data[0]] }),
      () => parseOAuthBindingsResponse({
        success: true,
        data: [{ ...bindingEnvelope.data[0], provider_slug: 'Upper_or_underscore' }],
      }),
      () => parseOAuthCatalogResponse({
        ...statusEnvelope,
        data: {
          ...statusEnvelope.data,
          custom_oauth_providers: [{ ...statusEnvelope.data.custom_oauth_providers[0], slug: 'company_sso' }],
        },
      }),
      () => parseOAuthBindingFlowResponse({ success: true, data: { flow_token: '../unsafe', expires_at: 1_900_000_000 } }),
      () => parseAccessTokenResponse({ success: true, data: 'short' }),
      () => parseTelegramBindingFlowResponse({ success: true, data: { flow_token: '../unsafe' } }, 'https://router.example.test'),
      () => parseTelegramBindingFlowResponse({ success: true, data: { flow_token: 'safe_flow' } }, 'javascript:alert(1)'),
      () => parseOAuthCatalogResponse({
        ...statusEnvelope,
        data: { ...statusEnvelope.data, telegram_oauth: true, telegram_bot_name: 'not-a-bot!' },
      }),
      () => parseProfileResponse({ success: true, data: { ...profileEnvelope.data, padding: 'x'.repeat(600_000) } }),
    ];
    for (const parse of invalid) expect(parse).toThrow(ProfileContractError);
  });
});

describe('profile API transport and validation', () => {
  it('uses exact bounded query routes with abort propagation', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: profileEnvelope })
      .mockResolvedValueOnce({ data: { success: true, data: { enabled: true } } })
      .mockResolvedValueOnce({ data: { success: true, data: { enabled: true } } })
      .mockResolvedValueOnce({ data: { success: true, data: [passkey] } })
      .mockResolvedValueOnce({ data: { success: true, data: [session] } })
      .mockResolvedValueOnce({ data: statusEnvelope })
      .mockResolvedValueOnce({ data: bindingEnvelope });
    const signal = new AbortController().signal;

    await expect(getProfile(signal)).resolves.toMatchObject({ id: 7 });
    await expect(getTwoFactorStatus(signal)).resolves.toEqual({ enabled: true });
    await expect(getPasskeyStatus(signal)).resolves.toEqual({ enabled: true });
    await expect(getPasskeys(signal)).resolves.toHaveLength(1);
    await expect(getSessions(signal)).resolves.toHaveLength(1);
    await expect(getOAuthCatalog(signal)).resolves.toMatchObject({ weChatEnabled: true });
    await expect(getOAuthBindings(signal)).resolves.toHaveLength(1);

    const routes = ['/user/self', '/user/2fa/status', '/user/passkey/status', '/user/passkey', '/user/sessions', '/status', '/user/oauth/bindings'];
    routes.forEach((route, index) => {
      expect(mockedAPI.get).toHaveBeenNthCalledWith(index + 1, route, { signal, ...limits });
    });
  });

  it('sends write-only profile and 2FA inputs to their exact routes and consumes proofs only in headers', async () => {
    mockedAPI.put.mockResolvedValue({ data: { success: true } });
    mockedAPI.post
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            secret: 'JBSWY3DPEHPK3PXP',
            otpauth_url: 'otpauth://totp/TokenRouter:user?secret=JBSWY3DPEHPK3PXP',
          },
        },
      })
      .mockResolvedValueOnce({ data: { success: true, data: { backup_codes: backupCodes } } })
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            proof_token: 'header.only.proof',
            expires_at: 1_900_000_000,
            method: '2fa',
            scope: 'twofa.backup_codes.regenerate',
          },
        },
      })
      .mockResolvedValueOnce({ data: { success: true, data: { backup_codes: backupCodes } } });
    const signal = new AbortController().signal;

    await updateDisplayName('  New Name  ', signal);
    await changePassword('old-password', 'new-password', signal);
    await startTwoFactorSetup(signal);
    await enableTwoFactor('123456', signal);
    await disableTwoFactor('10000001', signal);
    const proof = await verifyTwoFactor('twofa.backup_codes.regenerate', '654321', signal);
    await regenerateBackupCodes(proof, signal);

    expect(mockedAPI.put).toHaveBeenNthCalledWith(1, '/user/self', { display_name: 'New Name' }, { signal, ...limits });
    expect(mockedAPI.put).toHaveBeenNthCalledWith(2, '/user/self', {
      old_password: 'old-password',
      password: 'new-password',
    }, { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/user/2fa/start', {}, { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/user/2fa/enable', { code: '123456' }, { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(3, '/user/2fa/disable', { code: '10000001' }, { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(4, '/verify', {
      method: '2fa', code: '654321', scope: 'twofa.backup_codes.regenerate',
    }, { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(5, '/user/2fa/backup_codes', {}, {
      signal,
      ...limits,
      headers: { 'X-Security-Proof': 'header.only.proof' },
    });
    expect(mockedAPI.post.mock.calls[4][1]).not.toHaveProperty('proof_token');
  });

  it('runs the target passkey and session contracts with bounded paths and proof headers', async () => {
    mockedAPI.post
      .mockResolvedValueOnce({
        data: { success: true, data: { flow_token: 'registration_flow', options: { publicKey: { challenge: 'AAECAw' } } } },
      })
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({ data: { success: true } });
    mockedAPI.delete
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({ data: { success: true } });
    const signal = new AbortController().signal;

    await beginPasskeyRegistration('register.proof', signal);
    await finishPasskeyRegistration('registration_flow', {
      id: 'credential', type: 'public-key', response: { clientDataJSON: 'AA' },
    }, signal);
    await deletePasskeys('delete.proof', signal);
    await revokeSession('session_abcdef123456', signal);
    await revokeOtherSessions(signal);

    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/user/passkey/register/begin', {}, {
      signal, ...limits, headers: { 'X-Security-Proof': 'register.proof' },
    });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/user/passkey/register/finish', {
      flow_token: 'registration_flow',
      id: 'credential',
      type: 'public-key',
      response: { clientDataJSON: 'AA' },
    }, { signal, ...limits });
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(1, '/user/passkey', {
      signal, ...limits, headers: { 'X-Security-Proof': 'delete.proof' },
    });
    expect(mockedAPI.delete).toHaveBeenNthCalledWith(2, '/user/sessions/session_abcdef123456', { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(3, '/user/sessions/revoke-others', {}, { signal, ...limits });
  });

  it('builds a same-callback OAuth bind URL and performs only supported connection mutations', async () => {
    mockedAPI.post
      .mockResolvedValueOnce({ data: { success: true, data: { flow_token: 'state_123', expires_at: 1_900_000_000 } } })
      .mockResolvedValueOnce({ data: { success: true } });
    mockedAPI.delete.mockResolvedValueOnce({ data: { success: true } });

    const target = await createOAuthBindingURL(provider, 'https://router.example.test');
    const url = new URL(target);
    expect(url.origin + url.pathname).toBe('https://identity.example.test/oauth/authorize');
    expect(Object.fromEntries(url.searchParams)).toEqual({
      client_id: 'public-client-id',
      redirect_uri: 'https://router.example.test/api/oauth/company-sso/callback',
      response_type: 'code',
      state: 'state_123',
      scope: 'openid profile',
    });
    await bindWeChat('  wechat-code  ');
    await unbindOAuth(9);

    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/oauth/state', {
      provider: 'company-sso', intent: 'bind', redirect: '/profile',
    }, { signal: undefined, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/oauth/wechat/bind', { code: 'wechat-code' }, { signal: undefined, ...limits });
    expect(mockedAPI.delete).toHaveBeenCalledWith('/user/oauth/bindings/9', { signal: undefined, ...limits });
  });

  it('uses exact one-time token, verified-email, Telegram-start, and deletion contracts', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: { success: true, data: 'AbCdEfGhIjKlMnOpQrStUvWxYz01' } })
      .mockResolvedValueOnce({ data: { success: true } });
    mockedAPI.post
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({ data: { success: true, data: { flow_token: 'telegram_flow_123' } } });
    mockedAPI.delete.mockResolvedValueOnce({ data: { success: true } });
    const signal = new AbortController().signal;

    await expect(generateAccessToken(signal)).resolves.toBe('AbCdEfGhIjKlMnOpQrStUvWxYz01');
    await sendEmailVerification(' Next.User@Example.Test ', 'turnstile-proof', signal);
    await bindEmail(' Next.User@Example.Test ', '123456', signal);
    await expect(startTelegramBinding('https://router.example.test', signal)).resolves.toEqual({
      flowToken: 'telegram_flow_123',
      callbackURL: 'https://router.example.test/api/oauth/telegram/bind/telegram_flow_123',
    });
    await deleteAccount(signal);

    expect(mockedAPI.get).toHaveBeenNthCalledWith(1, '/user/token', { signal, ...limits });
    expect(mockedAPI.get).toHaveBeenNthCalledWith(
      2,
      '/verification?email=next.user%40example.test&turnstile=turnstile-proof',
      { signal, ...limits },
    );
    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/user/email/bind', {
      email: 'next.user@example.test',
      code: '123456',
    }, { signal, ...limits });
    expect(mockedAPI.post).toHaveBeenNthCalledWith(
      2,
      '/oauth/telegram/bind/start',
      {},
      { signal, ...limits },
    );
    expect(mockedAPI.delete).toHaveBeenCalledWith('/user/self', { signal, ...limits });
  });

  it('persists only allowlisted language and sidebar preferences in isolated updates', async () => {
    mockedAPI.put.mockResolvedValue({ data: { success: true } });
    const signal = new AbortController().signal;

    await updateLanguage('zh-TW', signal);
    await updateSidebarModules(sidebarModules, signal);

    expect(mockedAPI.put).toHaveBeenNthCalledWith(1, '/user/self', { language: 'zh-TW' }, { signal, ...limits });
    expect(mockedAPI.put).toHaveBeenNthCalledWith(2, '/user/self', {
      sidebar_modules: serializedSidebarModules,
    }, { signal, ...limits });

    await expect(updateLanguage('de' as 'en')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(updateSidebarModules({
      ...sidebarModules,
      admin: { enabled: true },
    } as never)).rejects.toBeInstanceOf(ProfileContractError);
    expect(mockedAPI.put).toHaveBeenCalledTimes(2);
  });

  it('sends the complete bounded notification payload and gates the admin-only preference', async () => {
    mockedAPI.put.mockResolvedValue({ data: { success: true } });
    const signal = new AbortController().signal;
    const settings = {
      notifyType: 'webhook' as const,
      quotaWarningThreshold: 125_000,
      notificationEmail: 'alerts@example.test',
      webhookURL: 'https://hooks.example.test/quota?tenant=one',
      webhookSecret: 'one-time-signing-secret',
      barkURL: 'https://bark.example.test/device/{{title}}/{{content}}',
      gotifyURL: 'https://gotify.example.test',
      gotifyToken: '',
      gotifyPriority: 7,
      acceptUnsetRatioModel: true,
      recordIPLog: true,
      upstreamModelUpdateNotifyEnabled: true,
    };

    await updateNotificationSettings(settings, 10, signal);
    expect(mockedAPI.put).toHaveBeenCalledWith('/user/setting', {
      quota_warning_threshold: 125_000,
      notify_type: 'webhook',
      notification_email: 'alerts@example.test',
      webhook_url: 'https://hooks.example.test/quota?tenant=one',
      webhook_secret: 'one-time-signing-secret',
      bark_url: 'https://bark.example.test/device/{{title}}/{{content}}',
      gotify_url: 'https://gotify.example.test',
      gotify_token: '',
      gotify_priority: 7,
      accept_unset_model_ratio_model: true,
      record_ip_log: true,
      upstream_model_update_notify_enabled: true,
    }, { signal, ...limits });

    await updateNotificationSettings({
      ...settings,
      webhookSecret: '',
      upstreamModelUpdateNotifyEnabled: undefined,
    }, 1, signal);
    const commonPayload = vi.mocked(mockedAPI.put).mock.calls[1]?.[1] as Record<string, unknown>;
    expect(commonPayload).not.toHaveProperty('upstream_model_update_notify_enabled');

    await expect(updateNotificationSettings({ ...settings, gotifyPriority: 11 }, 10))
      .rejects.toBeInstanceOf(ProfileContractError);
    await expect(updateNotificationSettings(settings, 1))
      .rejects.toBeInstanceOf(ProfileContractError);
    await expect(updateNotificationSettings({
      ...settings,
      webhookURL: 'http://public.example.test/hook',
    }, 10)).rejects.toBeInstanceOf(ProfileContractError);
    expect(mockedAPI.put).toHaveBeenCalledTimes(2);
  });

  it('rejects unsafe client input before issuing a request and rejects mismatched proof contracts', async () => {
    await expect(updateDisplayName('   ')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(changePassword('', 'short')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(revokeSession('../foreign')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(bindWeChat('line\nbreak')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(sendEmailVerification('not-an-email')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(sendEmailVerification('person@example.test', 'bad\u0000proof'))
      .rejects.toBeInstanceOf(ProfileContractError);
    await expect(bindEmail('person@example.test', '12345x')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(startTelegramBinding('https://router.example.test/path')).rejects.toBeInstanceOf(ProfileContractError);
    await expect(createOAuthBindingURL({ ...provider, authorizationEndpoint: 'http://identity.example.test' }, 'https://router.example.test'))
      .rejects.toBeInstanceOf(ProfileContractError);
    expect(mockedAPI.put).not.toHaveBeenCalled();
    expect(mockedAPI.delete).not.toHaveBeenCalled();
    expect(mockedAPI.post).not.toHaveBeenCalled();

    mockedAPI.post.mockResolvedValueOnce({
      data: {
        success: true,
        data: { proof_token: 'proof', expires_at: 1_900_000_000, method: '2fa', scope: 'passkey.delete' },
      },
    });
    await expect(verifyTwoFactor('passkey.register', '123456')).rejects.toBeInstanceOf(ProfileContractError);
  });
});
