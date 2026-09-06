// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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
  type CustomOAuthBinding,
  type LoginSession,
  type OAuthCatalog,
  type PasskeySummary,
  type ProfileAccount,
} from './profile-api';
import {
  passkeyRegistrationSupported,
  preparePasskeyCreationOptions,
  serializePasskeyRegistrationCredential,
} from './profile-webauthn';
import { ProfileView } from './ProfileView';

const i18nMocks = vi.hoisted(() => ({ changeLanguage: vi.fn() }));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, string | number>) => Object.entries(values ?? {}).reduce(
      (text, [name, value]) => text.replace(`{{${name}}}`, String(value)),
      key,
    ),
    i18n: { language: 'en', changeLanguage: i18nMocks.changeLanguage },
  }),
}));

vi.mock('./profile-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./profile-api')>();
  return {
    ...actual,
    beginPasskeyRegistration: vi.fn(),
    bindEmail: vi.fn(),
    bindWeChat: vi.fn(),
    changePassword: vi.fn(),
    createOAuthBindingURL: vi.fn(),
    deleteAccount: vi.fn(),
    deletePasskeys: vi.fn(),
    disableTwoFactor: vi.fn(),
    enableTwoFactor: vi.fn(),
    finishPasskeyRegistration: vi.fn(),
    generateAccessToken: vi.fn(),
    getOAuthBindings: vi.fn(),
    getOAuthCatalog: vi.fn(),
    getPasskeys: vi.fn(),
    getPasskeyStatus: vi.fn(),
    getProfile: vi.fn(),
    getSessions: vi.fn(),
    getTwoFactorStatus: vi.fn(),
    regenerateBackupCodes: vi.fn(),
    revokeOtherSessions: vi.fn(),
    revokeSession: vi.fn(),
    sendEmailVerification: vi.fn(),
    startTelegramBinding: vi.fn(),
    startTwoFactorSetup: vi.fn(),
    unbindOAuth: vi.fn(),
    updateDisplayName: vi.fn(),
    updateLanguage: vi.fn(),
    updateNotificationSettings: vi.fn(),
    updateSidebarModules: vi.fn(),
    verifyTwoFactor: vi.fn(),
  };
});

vi.mock('./profile-webauthn', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./profile-webauthn')>();
  return {
    ...actual,
    passkeyRegistrationSupported: vi.fn(),
    preparePasskeyCreationOptions: vi.fn(),
    serializePasskeyRegistrationCredential: vi.fn(),
  };
});

const mockedBeginPasskey = vi.mocked(beginPasskeyRegistration);
const mockedBindEmail = vi.mocked(bindEmail);
const mockedBindWeChat = vi.mocked(bindWeChat);
const mockedChangePassword = vi.mocked(changePassword);
const mockedCreateOAuthURL = vi.mocked(createOAuthBindingURL);
const mockedDeleteAccount = vi.mocked(deleteAccount);
const mockedDeletePasskeys = vi.mocked(deletePasskeys);
const mockedDisableTwoFactor = vi.mocked(disableTwoFactor);
const mockedEnableTwoFactor = vi.mocked(enableTwoFactor);
const mockedFinishPasskey = vi.mocked(finishPasskeyRegistration);
const mockedGenerateAccessToken = vi.mocked(generateAccessToken);
const mockedOAuthBindings = vi.mocked(getOAuthBindings);
const mockedOAuthCatalog = vi.mocked(getOAuthCatalog);
const mockedPasskeys = vi.mocked(getPasskeys);
const mockedPasskeyStatus = vi.mocked(getPasskeyStatus);
const mockedProfile = vi.mocked(getProfile);
const mockedSessions = vi.mocked(getSessions);
const mockedTwoFactorStatus = vi.mocked(getTwoFactorStatus);
const mockedRegenerateCodes = vi.mocked(regenerateBackupCodes);
const mockedRevokeOthers = vi.mocked(revokeOtherSessions);
const mockedRevokeSession = vi.mocked(revokeSession);
const mockedSendEmailVerification = vi.mocked(sendEmailVerification);
const mockedStartTelegramBinding = vi.mocked(startTelegramBinding);
const mockedStartTwoFactor = vi.mocked(startTwoFactorSetup);
const mockedUnbindOAuth = vi.mocked(unbindOAuth);
const mockedUpdateDisplayName = vi.mocked(updateDisplayName);
const mockedUpdateLanguage = vi.mocked(updateLanguage);
const mockedUpdateNotificationSettings = vi.mocked(updateNotificationSettings);
const mockedUpdateSidebarModules = vi.mocked(updateSidebarModules);
const mockedVerifyTwoFactor = vi.mocked(verifyTwoFactor);
const mockedPasskeySupported = vi.mocked(passkeyRegistrationSupported);
const mockedPrepareCreation = vi.mocked(preparePasskeyCreationOptions);
const mockedSerializeCredential = vi.mocked(serializePasskeyRegistrationCredential);

const account: ProfileAccount = {
  id: 7,
  username: 'profile-user',
  displayName: 'Profile User',
  email: 'profile@example.test',
  emailVerified: true,
  telegramConnected: false,
  role: 1,
  status: 1,
  group: 'default',
  quota: 10_000,
  usedQuota: 500,
  requestCount: 12,
  createdAt: 1_700_000_000,
  language: 'en',
  sidebarModules: {
    chat: { enabled: true, playground: true, chat: true },
    console: { enabled: true, detail: true, token: true, log: true, midjourney: true, task: true },
    personal: { enabled: true, topup: true, personal: true },
  },
  notificationSettings: {
    notifyType: 'email',
    quotaWarningThreshold: 500_000,
    notificationEmail: '',
    webhookURL: 'https://hooks.example.test/quota',
    webhookSecretConfigured: true,
    barkURL: 'https://bark.example.test/device/{{title}}/{{content}}',
    gotifyURL: 'https://gotify.example.test',
    gotifyTokenConfigured: true,
    gotifyPriority: 5,
    acceptUnsetRatioModel: false,
    recordIPLog: false,
    upstreamModelUpdateNotifyEnabled: false,
  },
};

const firstSession: LoginSession = {
  sid: 'session_first_123456',
  current: true,
  loginMethod: 'password',
  ip: '192.0.2.1',
  userAgent: 'Desktop Browser',
  createdAt: 1_788_199_200,
  lastActiveAt: 1_788_600_000,
  expiresAt: 1_789_000_000,
};

const secondSession: LoginSession = {
  ...firstSession,
  sid: 'session_second_abcdef',
  current: false,
  loginMethod: 'passkey',
  ip: '198.51.100.2',
  userAgent: 'Mobile Browser',
};

const passkey: PasskeySummary = {
  id: 3,
  attachment: 'platform',
  createdAt: '2026-09-01T12:00:00Z',
  lastUsedAt: '2026-09-05T12:01:00Z',
  backupEligible: true,
  backupState: true,
  cloneWarning: false,
};

const boundOAuth: CustomOAuthBinding = {
  providerId: 9,
  providerName: 'Company SSO',
  providerSlug: 'company-sso',
  providerUserId: 'external-user-42',
};

const oauthCatalog: OAuthCatalog = {
  builtIn: [
    { name: 'GitHub', enabled: true },
    { name: 'Discord', enabled: false },
    { name: 'OIDC', enabled: false },
    { name: 'LinuxDO', enabled: false },
  ],
  weChatEnabled: true,
  weChatQRCode: 'https://wechat.example.test/qr.png',
  telegramEnabled: true,
  telegramBotName: 'TokenRouterBot',
  turnstileRequired: false,
  turnstileSiteKey: null,
  customProviders: [
    {
      id: 9,
      name: 'Company SSO',
      slug: 'company-sso',
      clientId: 'client-9',
      authorizationEndpoint: 'https://identity.example.test/authorize',
      scopes: 'openid profile',
    },
    {
      id: 10,
      name: 'Partner ID',
      slug: 'partner',
      clientId: 'client-10',
      authorizationEndpoint: 'https://partner.example.test/authorize',
      scopes: '',
    },
  ],
};

const backupCodes = ['10000001', '10000002', '10000003', '10000004', '10000005', '10000006', '10000007', '10000008'];

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function mockSecurity(twoFactorEnabled = true, passkeyEnabled = false, items: PasskeySummary[] = []) {
  mockedTwoFactorStatus.mockResolvedValue({ enabled: twoFactorEnabled });
  mockedPasskeyStatus.mockResolvedValue({ enabled: passkeyEnabled });
  mockedPasskeys.mockResolvedValue(items);
}

beforeEach(() => {
  vi.resetAllMocks();
  i18nMocks.changeLanguage.mockResolvedValue(undefined);
  mockedProfile.mockResolvedValue(account);
  mockSecurity();
  mockedSessions.mockResolvedValue([firstSession, secondSession]);
  mockedOAuthCatalog.mockResolvedValue(oauthCatalog);
  mockedOAuthBindings.mockResolvedValue([boundOAuth]);
  mockedBindEmail.mockResolvedValue();
  mockedUpdateDisplayName.mockResolvedValue();
  mockedUpdateLanguage.mockResolvedValue();
  mockedUpdateNotificationSettings.mockResolvedValue();
  mockedUpdateSidebarModules.mockResolvedValue();
  mockedChangePassword.mockResolvedValue();
  mockedDeleteAccount.mockResolvedValue();
  mockedGenerateAccessToken.mockResolvedValue('AbCdEfGhIjKlMnOpQrStUvWxYz01');
  mockedStartTwoFactor.mockResolvedValue({
    secret: 'JBSWY3DPEHPK3PXP',
    provisioningURL: 'otpauth://totp/TokenRouter:user?secret=JBSWY3DPEHPK3PXP',
  });
  mockedEnableTwoFactor.mockResolvedValue(backupCodes);
  mockedDisableTwoFactor.mockResolvedValue();
  mockedVerifyTwoFactor.mockResolvedValue('one.time.proof');
  mockedRegenerateCodes.mockResolvedValue(backupCodes);
  mockedPasskeySupported.mockReturnValue(true);
  mockedPrepareCreation.mockReturnValue({ challenge: new ArrayBuffer(16), user: { id: new ArrayBuffer(4) } });
  mockedSerializeCredential.mockReturnValue({ id: 'credential-id', type: 'public-key', response: {} });
  mockedBeginPasskey.mockResolvedValue({ flowToken: 'flow-token', publicKey: { server: 'options' } });
  mockedFinishPasskey.mockResolvedValue();
  mockedDeletePasskeys.mockResolvedValue();
  mockedRevokeSession.mockResolvedValue();
  mockedRevokeOthers.mockResolvedValue();
  mockedSendEmailVerification.mockResolvedValue();
  mockedStartTelegramBinding.mockResolvedValue({
    flowToken: 'telegram_flow_123',
    callbackURL: 'https://router.example.test/api/oauth/telegram/bind/telegram_flow_123',
  });
  mockedCreateOAuthURL.mockResolvedValue('https://identity.example.test/authorize?state=one-time');
  mockedUnbindOAuth.mockResolvedValue();
  mockedBindWeChat.mockResolvedValue();
});

afterEach(() => {
  cleanup();
  document.getElementById('tokenrouter-turnstile-script')?.remove();
  delete window.turnstile;
  vi.restoreAllMocks();
});

describe('ProfileView', () => {
  it('renders independent, secret-safe profile, security, session, and connection states', async () => {
    const onNavigate = vi.fn();
    const onProfileChange = vi.fn();
    render(<ProfileView onNavigate={onNavigate} onProfileChange={onProfileChange} />);

    expect(screen.getByRole('heading', { name: 'Profile and security' })).toBeTruthy();
    expect(await screen.findByText('profile@example.test')).toBeTruthy();
    expect((screen.getByLabelText('Display name') as HTMLInputElement).value).toBe('Profile User');
    expect(screen.getByText('Desktop Browser')).toBeTruthy();
    expect(screen.getByText('Mobile Browser')).toBeTruthy();
    expect(screen.getByText('••••123456')).toBeTruthy();
    expect(screen.getByText('••••abcdef')).toBeTruthy();
    expect(screen.getByText('Company SSO')).toBeTruthy();
    expect(screen.getByText('Partner ID')).toBeTruthy();
    expect(screen.getByText('Connected as ••••ser-42')).toBeTruthy();
    expect(screen.getByText('GitHub · Available')).toBeTruthy();
    expect(screen.queryByText('external-user-42')).toBeNull();
    expect(screen.queryByText('session_first_123456')).toBeNull();
    expect(screen.queryByText(/access_token|refresh_hash|password hash/i)).toBeNull();
    expect(onProfileChange).toHaveBeenCalledWith(account);

    await userEvent.setup().click(screen.getByRole('button', { name: 'Dashboard' }));
    expect(onNavigate).toHaveBeenCalledWith('/dashboard');
  });

  it('updates profile fields, validates password confirmation, and clears rejected password secrets', async () => {
    const onProfileChange = vi.fn();
    render(<ProfileView onProfileChange={onProfileChange} />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');

    const displayForm = screen.getByRole('form', { name: 'Update display name' });
    const displayInput = within(displayForm).getByLabelText('Display name');
    await user.clear(displayInput);
    await user.type(displayInput, '  New Display  ');
    await user.click(within(displayForm).getByRole('button', { name: 'Save display name' }));
    await waitFor(() => expect(mockedUpdateDisplayName).toHaveBeenCalledWith('New Display', expect.any(AbortSignal)));
    expect(await screen.findByText('Display name updated.')).toBeTruthy();
    expect(onProfileChange).toHaveBeenLastCalledWith({ ...account, displayName: 'New Display' });

    const passwordForm = screen.getByRole('form', { name: 'Change password' });
    const oldInput = within(passwordForm).getByLabelText('Current password') as HTMLInputElement;
    const newInput = within(passwordForm).getByLabelText('New password') as HTMLInputElement;
    const confirmation = within(passwordForm).getByLabelText('Confirm new password') as HTMLInputElement;
    await user.type(oldInput, 'old-password');
    await user.type(newInput, 'new-password');
    await user.type(confirmation, 'different-password');
    await user.click(within(passwordForm).getByRole('button', { name: 'Change password' }));
    expect(screen.getByRole('alert').textContent).toBe('New passwords do not match.');
    expect(mockedChangePassword).not.toHaveBeenCalled();

    await user.clear(confirmation);
    await user.type(confirmation, 'new-password');
    mockedChangePassword.mockRejectedValueOnce(new Error('database password hash: should-not-render'));
    await user.click(within(passwordForm).getByRole('button', { name: 'Change password' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to change your password.');
    expect(screen.queryByText(/database password hash|should-not-render/)).toBeNull();
    expect(oldInput.value).toBe('');
    expect(newInput.value).toBe('');
    expect(confirmation.value).toBe('');
  });

  it('persists language and bounded personal navigation choices', async () => {
    const onProfileChange = vi.fn();
    mockedProfile.mockResolvedValueOnce({ ...account, language: 'ja' });
    render(<ProfileView onProfileChange={onProfileChange} />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    expect(i18nMocks.changeLanguage).toHaveBeenCalledWith('ja');

    await user.selectOptions(screen.getByLabelText('Interface language'), 'zh-TW');
    await waitFor(() => expect(mockedUpdateLanguage).toHaveBeenCalledWith('zh-TW', expect.any(AbortSignal)));
    expect(i18nMocks.changeLanguage).toHaveBeenCalledWith('zh-TW');
    expect(await screen.findByText('Language preference saved.')).toBeTruthy();

    const navigation = screen.getByRole('form', { name: 'Personal navigation preferences' });
    await user.click(within(navigation).getByLabelText('Playground'));
    await user.click(within(navigation).getByRole('button', { name: 'Save navigation preferences' }));
    await waitFor(() => expect(mockedUpdateSidebarModules).toHaveBeenCalledWith({
      ...account.sidebarModules,
      chat: { ...account.sidebarModules.chat, playground: false },
    }, expect.any(AbortSignal)));
    expect(await screen.findByText('Navigation preferences saved.')).toBeTruthy();
    expect(onProfileChange).toHaveBeenLastCalledWith(expect.objectContaining({
      language: 'zh-TW',
      sidebarModules: expect.objectContaining({
        chat: expect.objectContaining({ playground: false }),
      }),
    }));
  });

  it('saves conditional webhook settings without retaining the submitted secret', async () => {
    const onProfileChange = vi.fn();
    render(<ProfileView onProfileChange={onProfileChange} />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Notification and privacy settings' });
    expect(within(form).queryByLabelText('Webhook URL')).toBeNull();
    expect(within(form).queryByLabelText('Notify me when upstream model checks find changes or failures.')).toBeNull();

    await user.selectOptions(within(form).getByLabelText('Notification method'), 'bark');
    expect((within(form).getByLabelText('Bark push URL') as HTMLInputElement).value)
      .toBe('https://bark.example.test/device/{{title}}/{{content}}');
    expect(form.querySelector('#profile-bark-url-help')?.textContent)
      .toContain('Bark URLs may use these template placeholders:');
    await user.selectOptions(within(form).getByLabelText('Notification method'), 'webhook');
    const webhookURL = within(form).getByLabelText('Webhook URL');
    const webhookSecretInput = within(form).getByLabelText('Webhook signing secret') as HTMLInputElement;
    expect((webhookURL as HTMLInputElement).value).toBe('https://hooks.example.test/quota');
    expect(within(form).getByText('A webhook secret is configured for this saved endpoint. Leave this blank to keep it.')).toBeTruthy();
    await user.clear(webhookURL);
    await user.type(webhookURL, 'https://hooks.example.test/replacement');
    expect(within(form).getByText('The configured webhook secret belongs to the previous endpoint. Leaving this blank removes it.')).toBeTruthy();
    await user.type(webhookSecretInput, 'replacement-signing-secret');
    await user.click(within(form).getByLabelText('Allow models that do not have a configured price ratio.'));
    await user.click(within(form).getByLabelText('Record my IP address in usage and error logs.'));
    await user.click(within(form).getByRole('button', { name: 'Save notification and privacy settings' }));

    await waitFor(() => expect(mockedUpdateNotificationSettings).toHaveBeenCalledTimes(1));
    const [payload, role, signal] = mockedUpdateNotificationSettings.mock.calls[0] ?? [];
    expect(payload).toEqual({
      notifyType: 'webhook',
      quotaWarningThreshold: 500_000,
      notificationEmail: '',
      webhookURL: 'https://hooks.example.test/replacement',
      webhookSecret: 'replacement-signing-secret',
      barkURL: 'https://bark.example.test/device/{{title}}/{{content}}',
      gotifyURL: 'https://gotify.example.test',
      gotifyToken: '',
      gotifyPriority: 5,
      acceptUnsetRatioModel: true,
      recordIPLog: true,
    });
    expect(role).toBe(1);
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(webhookSecretInput.value).toBe('');
    expect(await screen.findByText('Notification and privacy settings saved.')).toBeTruthy();
    expect(within(form).getByText('A webhook secret is configured for this saved endpoint. Leave this blank to keep it.')).toBeTruthy();
    expect(onProfileChange).toHaveBeenLastCalledWith(expect.objectContaining({
      notificationSettings: expect.objectContaining({
        notifyType: 'webhook',
        webhookURL: 'https://hooks.example.test/replacement',
        webhookSecretConfigured: true,
        acceptUnsetRatioModel: true,
        recordIPLog: true,
      }),
    }));
  });

  it('serializes an in-flight notification save, clears its secret, and aborts it on unmount', async () => {
    const pending = deferred<void>();
    mockedUpdateNotificationSettings.mockReturnValueOnce(pending.promise);
    const view = render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Notification and privacy settings' });
    await user.selectOptions(within(form).getByLabelText('Notification method'), 'webhook');
    const secret = within(form).getByLabelText('Webhook signing secret') as HTMLInputElement;
    await user.type(secret, 'ephemeral-secret');
    const save = within(form).getByRole('button', { name: 'Save notification and privacy settings' });
    act(() => {
      save.click();
      save.click();
    });
    expect(mockedUpdateNotificationSettings).toHaveBeenCalledTimes(1);
    expect(secret.value).toBe('');
    const signal = mockedUpdateNotificationSettings.mock.calls[0]?.[2] as AbortSignal;
    expect(signal.aborted).toBe(false);
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => pending.resolve());
    expect(document.body.textContent).not.toContain('ephemeral-secret');
  });

  it('preserves a Gotify token only for the saved server and requires a replacement after an endpoint change', async () => {
    render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Notification and privacy settings' });
    await user.selectOptions(within(form).getByLabelText('Notification method'), 'gotify');
    const serverURL = within(form).getByLabelText('Gotify server URL');
    let tokenInput = within(form).getByLabelText('Gotify application token') as HTMLInputElement;
    expect(tokenInput.required).toBe(false);
    expect(within(form).getByText('A Gotify token is configured for this saved server. Leave this blank to keep it.')).toBeTruthy();
    await user.click(within(form).getByRole('button', { name: 'Save notification and privacy settings' }));
    await waitFor(() => expect(mockedUpdateNotificationSettings).toHaveBeenCalledTimes(1));
    expect(mockedUpdateNotificationSettings.mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      notifyType: 'gotify',
      gotifyURL: 'https://gotify.example.test',
      gotifyToken: '',
    }));

    await user.clear(serverURL);
    await user.type(serverURL, 'https://push.example.test');
    tokenInput = within(form).getByLabelText('Gotify application token') as HTMLInputElement;
    expect(tokenInput.required).toBe(true);
    expect(within(form).getByText('The configured Gotify token belongs to the previous server. Enter a replacement before saving.')).toBeTruthy();
    await user.type(tokenInput, 'replacement-gotify-token');
    await user.clear(within(form).getByLabelText('Gotify priority'));
    await user.type(within(form).getByLabelText('Gotify priority'), '8');
    await user.click(within(form).getByRole('button', { name: 'Save notification and privacy settings' }));
    await waitFor(() => expect(mockedUpdateNotificationSettings).toHaveBeenCalledTimes(2));
    expect(mockedUpdateNotificationSettings.mock.calls[1]?.[0]).toEqual(expect.objectContaining({
      notifyType: 'gotify',
      gotifyURL: 'https://push.example.test',
      gotifyToken: 'replacement-gotify-token',
      gotifyPriority: 8,
    }));
    expect(tokenInput.value).toBe('');
  });

  it('role-gates upstream notifications and blocks an unusable email fallback', async () => {
    mockedProfile.mockResolvedValueOnce({
      ...account,
      role: 10,
      email: '',
      emailVerified: false,
    });
    render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('No verified email is connected.');
    const form = screen.getByRole('form', { name: 'Notification and privacy settings' });
    await user.click(within(form).getByRole('button', { name: 'Save notification and privacy settings' }));
    expect(screen.getByRole('alert').textContent)
      .toBe('Enter a notification email because this account has no verified email.');
    expect(mockedUpdateNotificationSettings).not.toHaveBeenCalled();

    await user.type(within(form).getByLabelText('Notification email'), 'notify@example.test');
    await user.click(within(form).getByLabelText('Notify me when upstream model checks find changes or failures.'));
    await user.click(within(form).getByRole('button', { name: 'Save notification and privacy settings' }));
    await waitFor(() => expect(mockedUpdateNotificationSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        notificationEmail: 'notify@example.test',
        upstreamModelUpdateNotifyEnabled: true,
      }),
      10,
      expect.any(AbortSignal),
    ));
  });

  it('fails notification settings closed on a profile contract error and supports a bounded retry', async () => {
    mockedProfile
      .mockRejectedValueOnce(new Error('webhook_secret=must-not-render'))
      .mockResolvedValueOnce(account);
    render(<ProfileView />);
    const user = userEvent.setup();
    const section = screen.getByRole('heading', { name: 'Notifications and privacy' }).closest('section') as HTMLElement;
    expect(await within(section).findByText('Unable to load notification and privacy settings. Changes are disabled.')).toBeTruthy();
    expect(within(section).queryByRole('form', { name: 'Notification and privacy settings' })).toBeNull();
    expect(document.body.textContent).not.toContain('must-not-render');
    await user.click(within(section).getByRole('button', { name: 'Try again' }));
    expect(await within(section).findByRole('form', { name: 'Notification and privacy settings' })).toBeTruthy();
    expect(mockedProfile).toHaveBeenCalledTimes(2);
  });

  it('sends an address-bound code and updates only after successful email verification', async () => {
    const onProfileChange = vi.fn();
    render(<ProfileView onProfileChange={onProfileChange} />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Verify and bind email' });
    const email = within(form).getByLabelText('Email address');

    await user.type(email, 'not-an-email');
    await user.click(within(form).getByRole('button', { name: 'Send verification code' }));
    expect(screen.getByRole('alert').textContent).toBe('Enter a valid email address of at most 50 characters.');
    expect(mockedSendEmailVerification).not.toHaveBeenCalled();

    await user.clear(email);
    await user.type(email, ' Next.User@Example.Test ');
    await user.click(within(form).getByRole('button', { name: 'Send verification code' }));
    await waitFor(() => expect(mockedSendEmailVerification).toHaveBeenCalledWith(
      'next.user@example.test', undefined, expect.any(AbortSignal),
    ));
    expect(await within(form).findByText('A 6-digit code was sent to next.user@example.test.')).toBeTruthy();
    await user.type(within(form).getByLabelText('Email verification code'), '123456');
    await user.click(within(form).getByRole('button', { name: 'Verify and bind email' }));
    await waitFor(() => expect(mockedBindEmail).toHaveBeenCalledWith(
      'next.user@example.test', '123456', expect.any(AbortSignal),
    ));
    expect(await screen.findByText('Email address verified and bound.')).toBeTruthy();
    expect(onProfileChange).toHaveBeenLastCalledWith({
      ...account,
      email: 'next.user@example.test',
      emailVerified: true,
    });
    expect(within(form).queryByLabelText('Email verification code')).toBeNull();
  });

  it('fails closed and consumes a configured Turnstile proof before sending email', async () => {
    let widgetOptions: Record<string, unknown> = {};
    window.turnstile = {
      render: vi.fn((_element, options) => {
        widgetOptions = options;
        return 'profile-email-widget';
      }),
      remove: vi.fn(),
    };
    mockedOAuthCatalog.mockResolvedValueOnce({
      ...oauthCatalog,
      turnstileRequired: true,
      turnstileSiteKey: 'profile-site-key',
    });
    render(<ProfileView />);
    const user = userEvent.setup();
    const form = await screen.findByRole('form', { name: 'Verify and bind email' });
    const send = within(form).getByRole('button', { name: 'Send verification code' });
    await user.type(within(form).getByLabelText('Email address'), 'person@example.test');
    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    expect(send.hasAttribute('disabled')).toBe(true);

    act(() => { (widgetOptions.callback as (token: string) => void)('one-time-human-proof'); });
    expect(send.hasAttribute('disabled')).toBe(false);
    await user.click(send);
    await waitFor(() => expect(mockedSendEmailVerification).toHaveBeenCalledWith(
      'person@example.test', 'one-time-human-proof', expect.any(AbortSignal),
    ));
    expect(send.hasAttribute('disabled')).toBe(true);
  });

  it('shows a generated access token once, copies it, and clears stale material before regeneration', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    render(<ProfileView />);
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Regenerate access token' });
    const generate = within(form).getByRole('button', { name: 'Generate new access token' });
    expect(generate.hasAttribute('disabled')).toBe(true);

    await user.click(within(form).getByLabelText('Invalidate my existing access token and create a replacement.'));
    await user.click(generate);
    await waitFor(() => expect(mockedGenerateAccessToken).toHaveBeenCalledWith(expect.any(AbortSignal)));
    expect((await screen.findByLabelText('Access token value') as HTMLInputElement).value)
      .toBe('AbCdEfGhIjKlMnOpQrStUvWxYz01');
    const copy = screen.getByRole('button', { name: 'Copy access token' });
    await waitFor(() => expect(copy.hasAttribute('disabled')).toBe(false));
    await user.click(copy);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('AbCdEfGhIjKlMnOpQrStUvWxYz01'));
    await user.click(screen.getByRole('button', { name: 'Hide access token' }));
    expect(screen.queryByLabelText('Access token value')).toBeNull();

    mockedGenerateAccessToken.mockRejectedValueOnce(new Error('token=server-secret'));
    await user.click(within(form).getByLabelText('Invalidate my existing access token and create a replacement.'));
    await user.click(generate);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to generate a new access token.');
    expect(screen.queryByText(/server-secret|token=/)).toBeNull();
    expect(screen.queryByLabelText('Access token value')).toBeNull();
  });

  it('serializes mutations before React can disable the controls', async () => {
    const pending = deferred<void>();
    mockedUpdateDisplayName.mockReturnValueOnce(pending.promise);
    render(<ProfileView />);
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Update display name' });
    const input = within(form).getByLabelText('Display name');
    await userEvent.setup().clear(input);
    await userEvent.setup().type(input, 'Serialized Name');
    const save = within(form).getByRole('button', { name: 'Save display name' });
    act(() => {
      save.click();
      save.click();
    });
    expect(mockedUpdateDisplayName).toHaveBeenCalledTimes(1);
    expect(screen.getByText('Saving security-sensitive change…')).toBeTruthy();
    await act(async () => pending.resolve());
    expect(await screen.findByText('Display name updated.')).toBeTruthy();
  });

  it('completes first-time 2FA setup and requires acknowledgement before hiding one-time recovery codes', async () => {
    mockSecurity(false, false, []);
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');

    await user.click(screen.getByRole('button', { name: 'Set up two-factor authentication' }));
    expect(await screen.findByText('JBSWY3DPEHPK3PXP')).toBeTruthy();
    expect(mockedStartTwoFactor).toHaveBeenCalledWith(expect.any(AbortSignal));
    const form = screen.getByRole('form', { name: 'Complete two-factor setup' });
    await user.type(within(form).getByLabelText('Authenticator code'), '123456');
    await user.click(within(form).getByRole('button', { name: 'Enable two-factor authentication' }));

    await waitFor(() => expect(mockedEnableTwoFactor).toHaveBeenCalledWith('123456', expect.any(AbortSignal)));
    const codes = await screen.findByRole('list', { name: 'Backup codes' });
    expect(within(codes).getAllByRole('listitem')).toHaveLength(8);
    expect(screen.queryByText('JBSWY3DPEHPK3PXP')).toBeNull();
    const hide = screen.getByRole('button', { name: 'Hide backup codes' });
    expect(hide.hasAttribute('disabled')).toBe(true);
    await user.click(screen.getByLabelText('I saved these backup codes in a safe place.'));
    await user.click(hide);
    expect(screen.queryByRole('list', { name: 'Backup codes' })).toBeNull();
  });

  it('uses one-time scoped proofs for backup regeneration and confirms disabling 2FA', async () => {
    render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');

    const regenerate = screen.getByRole('form', { name: 'Regenerate backup codes' });
    await user.type(within(regenerate).getByLabelText('Authenticator code'), '654321');
    await user.click(within(regenerate).getByLabelText('Invalidate my existing backup codes.'));
    await user.click(within(regenerate).getByRole('button', { name: 'Generate new backup codes' }));
    await waitFor(() => expect(mockedVerifyTwoFactor).toHaveBeenCalledWith(
      'twofa.backup_codes.regenerate', '654321', expect.any(AbortSignal),
    ));
    expect(mockedRegenerateCodes).toHaveBeenCalledWith('one.time.proof', expect.any(AbortSignal));
    expect(await screen.findByRole('list', { name: 'Backup codes' })).toBeTruthy();

    const disable = screen.getByRole('form', { name: 'Disable two-factor authentication' });
    await user.type(within(disable).getByLabelText('Authenticator or backup code'), '10000001');
    await user.click(within(disable).getByLabelText('I understand this removes two-factor protection and all backup codes.'));
    await user.click(within(disable).getByRole('button', { name: 'Disable two-factor authentication' }));
    await waitFor(() => expect(mockedDisableTwoFactor).toHaveBeenCalledWith('10000001', expect.any(AbortSignal)));
    expect(await screen.findByText('Two-factor authentication disabled.')).toBeTruthy();
    expect(screen.queryByRole('form', { name: 'Disable two-factor authentication' })).toBeNull();
  });

  it('registers and removes a passkey only after the required scoped 2FA proof', async () => {
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { create: vi.fn().mockResolvedValue({ browser: 'credential' }) },
    });
    mockedPasskeyStatus
      .mockResolvedValueOnce({ enabled: false })
      .mockResolvedValueOnce({ enabled: true });
    mockedPasskeys
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([passkey]);
    render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('No passkey is registered.');

    await user.type(screen.getByLabelText('Authenticator code for passkey changes'), '123456');
    await user.click(screen.getByRole('button', { name: 'Register a passkey' }));
    await waitFor(() => expect(mockedVerifyTwoFactor).toHaveBeenCalledWith('passkey.register', '123456', expect.any(AbortSignal)));
    expect(mockedBeginPasskey).toHaveBeenCalledWith('one.time.proof', expect.any(AbortSignal));
    expect(mockedPrepareCreation).toHaveBeenCalledWith({ server: 'options' });
    expect(navigator.credentials.create).toHaveBeenCalled();
    expect(mockedSerializeCredential).toHaveBeenCalledWith({ browser: 'credential' });
    expect(mockedFinishPasskey).toHaveBeenCalledWith(
      'flow-token', { id: 'credential-id', type: 'public-key', response: {} }, expect.any(AbortSignal),
    );
    expect(await screen.findByText('This-device passkey')).toBeTruthy();

    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await user.type(screen.getByLabelText('Authenticator code for passkey changes'), '654321');
    await user.click(screen.getByRole('button', { name: 'Remove passkey' }));
    await waitFor(() => expect(mockedVerifyTwoFactor).toHaveBeenLastCalledWith('passkey.delete', '654321', expect.any(AbortSignal)));
    expect(mockedDeletePasskeys).toHaveBeenCalledWith('one.time.proof', expect.any(AbortSignal));
    expect(await screen.findByText('No passkey is registered.')).toBeTruthy();
  });

  it('redacts passkey cancellation details and never starts an unsupported ceremony', async () => {
    mockedPasskeySupported.mockReturnValue(false);
    render(<ProfileView />);
    await screen.findByText('No passkey is registered.');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Register a passkey' }));
    expect(screen.getByRole('alert').textContent).toBe('Passkey registration is not supported on this device.');
    expect(mockedVerifyTwoFactor).not.toHaveBeenCalled();
    expect(mockedBeginPasskey).not.toHaveBeenCalled();
  });

  it('confirms session revocation, refreshes revoke-others, and never renders full session identifiers', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockedSessions
      .mockResolvedValueOnce([firstSession, secondSession])
      .mockResolvedValueOnce([firstSession]);
    render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('Desktop Browser');

    await user.click(screen.getByRole('button', { name: 'Sign out other sessions' }));
    await waitFor(() => expect(mockedRevokeOthers).toHaveBeenCalledWith(expect.any(AbortSignal)));
    expect(mockedSessions).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Mobile Browser')).toBeNull();
    expect(screen.queryByText(firstSession.sid)).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Sign out this session' }));
    await waitFor(() => expect(mockedRevokeSession).toHaveBeenCalledWith(firstSession.sid, expect.any(AbortSignal)));
    expect(await screen.findByText('No active login sessions.')).toBeTruthy();
    expect(mockedProfile).toHaveBeenCalledTimes(2);
  });

  it('shows independent redacted errors and retries only the affected empty session panel', async () => {
    mockedSessions
      .mockRejectedValueOnce(new Error('postgres password=private'))
      .mockResolvedValueOnce([]);
    render(<ProfileView />);
    const user = userEvent.setup();

    expect(await screen.findByText('Unable to load login sessions.')).toBeTruthy();
    expect(screen.getByText('profile@example.test')).toBeTruthy();
    expect(screen.queryByText(/postgres password|private/)).toBeNull();
    const sessionsSection = screen.getByRole('heading', { name: 'Login sessions' }).closest('section') as HTMLElement;
    await user.click(within(sessionsSection).getByRole('button', { name: 'Try again' }));
    expect(await within(sessionsSection).findByText('No active login sessions.')).toBeTruthy();
    expect(mockedSessions).toHaveBeenCalledTimes(2);
    expect(mockedProfile).toHaveBeenCalledTimes(1);
  });

  it('initiates safe custom OAuth, confirms unbinding, and connects WeChat without claiming unsupported status', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const navigateExternal = vi.fn();
    render(<ProfileView onExternalNavigate={navigateExternal} />);
    const user = userEvent.setup();
    await screen.findByText('Company SSO');

    const partner = screen.getByText('Partner ID').closest('li') as HTMLLIElement;
    await user.click(within(partner).getByRole('button', { name: 'Connect' }));
    await waitFor(() => expect(mockedCreateOAuthURL).toHaveBeenCalledWith(
      oauthCatalog.customProviders[1], window.location.origin, expect.any(AbortSignal),
    ));
    expect(navigateExternal).toHaveBeenCalledWith('https://identity.example.test/authorize?state=one-time');

    const company = screen.getByText('Company SSO').closest('li') as HTMLLIElement;
    await user.click(within(company).getByRole('button', { name: 'Disconnect' }));
    await waitFor(() => expect(mockedUnbindOAuth).toHaveBeenCalledWith(9, expect.any(AbortSignal)));
    expect(within(company).queryByRole('button', { name: 'Disconnect' })).toBeNull();

    const weChat = screen.getByRole('form', { name: 'Connect WeChat' });
    await user.type(within(weChat).getByLabelText('WeChat authorization code'), 'wechat-code');
    await user.click(within(weChat).getByRole('button', { name: 'Connect WeChat' }));
    await waitFor(() => expect(mockedBindWeChat).toHaveBeenCalledWith('wechat-code', expect.any(AbortSignal)));
    expect(screen.getByText('This endpoint can connect WeChat, but it does not report whether WeChat is already connected.')).toBeTruthy();
  });

  it('starts Telegram binding only when advertised and never renders its one-time flow token as text', async () => {
    render(<ProfileView />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Start Telegram binding' }));
    await waitFor(() => expect(mockedStartTelegramBinding).toHaveBeenCalledWith(
      window.location.origin, expect.any(AbortSignal),
    ));
    const widget = await screen.findByRole('group', { name: 'Telegram sign-in control' });
    const script = widget.querySelector('script');
    expect(script?.src).toBe('https://telegram.org/js/telegram-widget.js?22');
    expect(script?.getAttribute('data-telegram-login')).toBe('TokenRouterBot');
    expect(script?.getAttribute('data-auth-url'))
      .toBe('https://router.example.test/api/oauth/telegram/bind/telegram_flow_123');
    expect(document.body.textContent).not.toContain('telegram_flow_123');
  });

  it('requires exact, acknowledged account deletion and invalidates the authenticated shell on success', async () => {
    const onNavigate = vi.fn();
    const expired = vi.fn();
    window.addEventListener('tokenrouter:session-expired', expired);
    render(<ProfileView onNavigate={onNavigate} />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Delete account' });
    const confirmation = within(form).getByLabelText('Type profile-user to confirm');
    const submit = within(form).getByRole('button', { name: 'Delete account' });

    await user.type(confirmation, 'profile-user');
    expect(submit.hasAttribute('disabled')).toBe(true);
    await user.click(within(form).getByLabelText('I understand this account action cannot be undone here.'));
    expect(submit.hasAttribute('disabled')).toBe(false);
    await user.click(submit);
    await waitFor(() => expect(mockedDeleteAccount).toHaveBeenCalledWith(expect.any(AbortSignal)));
    expect(expired).toHaveBeenCalledTimes(1);
    expect(onNavigate).toHaveBeenCalledWith('/sign-in');
    window.removeEventListener('tokenrouter:session-expired', expired);
  });

  it('does not offer deletion or personal navigation controls for the root administrator', async () => {
    mockedProfile.mockResolvedValueOnce({ ...account, role: 100 });
    render(<ProfileView />);
    expect(await screen.findByText('The root administrator account cannot be deleted.')).toBeTruthy();
    expect(screen.queryByRole('form', { name: 'Delete account' })).toBeNull();
    expect(screen.queryByRole('form', { name: 'Personal navigation preferences' })).toBeNull();
  });

  it('aborts an in-flight one-time token request and ignores its late credential after unmount', async () => {
    const pending = deferred<string>();
    mockedGenerateAccessToken.mockReturnValueOnce(pending.promise);
    const view = render(<ProfileView />);
    const user = userEvent.setup();
    await screen.findByText('profile@example.test');
    const form = screen.getByRole('form', { name: 'Regenerate access token' });
    await user.click(within(form).getByLabelText('Invalidate my existing access token and create a replacement.'));
    await user.click(within(form).getByRole('button', { name: 'Generate new access token' }));
    const signal = mockedGenerateAccessToken.mock.calls[0][0] as AbortSignal;
    expect(signal.aborted).toBe(false);
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => pending.resolve('AbCdEfGhIjKlMnOpQrStUvWxYz01'));
    expect(document.body.textContent).not.toContain('AbCdEfGhIjKlMnOpQrStUvWxYz01');
  });

  it('aborts initial work and ignores late responses after unmount', async () => {
    const pendingProfile = deferred<ProfileAccount>();
    mockedProfile.mockReturnValueOnce(pendingProfile.promise);
    const onProfileChange = vi.fn();
    const view = render(<ProfileView onProfileChange={onProfileChange} />);
    const initialSignal = mockedProfile.mock.calls[0][0] as AbortSignal;
    expect(initialSignal.aborted).toBe(false);
    view.unmount();
    expect(initialSignal.aborted).toBe(true);
    await act(async () => pendingProfile.resolve(account));
    expect(onProfileChange).not.toHaveBeenCalled();
  });
});
