// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { getData, type User } from '../../shared/api/client';
import { authGet, authPost } from './auth-api';
import { oauthCallbackErrorMessage } from './login-validation';
import { LoginView } from './LoginView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, string | number>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? `{{${name}}}`),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('../../shared/api/client', () => ({
  getData: vi.fn(),
}));

vi.mock('./auth-api', () => ({
  authGet: vi.fn(),
  authPost: vi.fn(),
  isCanceledAuthRequest: (error: unknown, signal?: AbortSignal) => {
    if (signal?.aborted) return true;
    if (!error || typeof error !== 'object') return false;
    const candidate = error as { name?: unknown; code?: unknown };
    return candidate.name === 'AbortError'
      || candidate.name === 'CanceledError'
      || candidate.code === 'ERR_CANCELED';
  },
}));

const mockedGetData = vi.mocked(getData);
const mockedAuthGet = vi.mocked(authGet);
const mockedAuthPost = vi.mocked(authPost);
const signedInUser: User = {
  id: 9,
  username: 'legal-user',
  display_name: 'Legal User',
  role: 1,
  group: 'default',
  quota: 500_000,
  used_quota: 0,
  request_count: 0,
  email: 'legal@example.test',
};
const originalPublicKeyCredential = Object.getOwnPropertyDescriptor(globalThis, 'PublicKeyCredential');
const originalCredentials = Object.getOwnPropertyDescriptor(navigator, 'credentials');

function bytes(...values: number[]): ArrayBuffer {
  return Uint8Array.from(values).buffer;
}

function installPasskeyBrowser() {
  const get = vi.fn();
  Object.defineProperty(globalThis, 'PublicKeyCredential', {
    configurable: true,
    value: class MockPublicKeyCredential {},
  });
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { get },
  });
  return get;
}

function restorePasskeyBrowser() {
  if (originalPublicKeyCredential) {
    Object.defineProperty(globalThis, 'PublicKeyCredential', originalPublicKeyCredential);
  } else {
    delete (globalThis as { PublicKeyCredential?: unknown }).PublicKeyCredential;
  }
  if (originalCredentials) {
    Object.defineProperty(navigator, 'credentials', originalCredentials);
  } else {
    delete (navigator as { credentials?: unknown }).credentials;
  }
}

function validPasskeyBegin() {
  return {
    flow_token: 'f'.repeat(64),
    options: {
      publicKey: {
        challenge: 'AAECAwQFBgcICQoLDA0ODw',
        rpId: 'example.test',
        userVerification: 'preferred',
        allowCredentials: [{ id: 'AQID', type: 'public-key', transports: ['internal'] }],
      },
    },
  };
}

function validAssertion() {
  return {
    id: 'credential-id',
    rawId: bytes(1, 2, 3),
    type: 'public-key',
    authenticatorAttachment: 'platform',
    response: {
      clientDataJSON: bytes(4),
      authenticatorData: bytes(5),
      signature: bytes(6),
      userHandle: bytes(7),
    },
    getClientExtensionResults: () => ({ appid: true }),
  };
}

function status(overrides: Record<string, unknown> = {}) {
  const result: Record<string, unknown> = {
    wechat_login: false,
    wechat_qrcode: '',
    github_oauth: true,
    discord_oauth: true,
    oidc_enabled: true,
    linuxdo_oauth: true,
    telegram_oauth: false,
    telegram_bot_name: '',
    ...overrides,
  };
  if (result.github_oauth === true && result.github_client_id === undefined) result.github_client_id = 'github-client';
  if (result.discord_oauth === true && result.discord_client_id === undefined) result.discord_client_id = 'discord-client';
  if (result.oidc_enabled === true) {
    if (result.oidc_client_id === undefined) result.oidc_client_id = 'oidc-client';
    if (result.oidc_authorization_endpoint === undefined) {
      result.oidc_authorization_endpoint = 'https://identity.example.test/authorize';
    }
    if (result.oidc_display_name === undefined) result.oidc_display_name = 'OIDC';
  }
  if (result.linuxdo_oauth === true) {
    if (result.linuxdo_client_id === undefined) result.linuxdo_client_id = 'linuxdo-client';
    if (result.linuxdo_minimum_trust_level === undefined) result.linuxdo_minimum_trust_level = 0;
  }
  if (result.passkey_login === true) {
    if (result.passkey_display_name === undefined) result.passkey_display_name = 'TokenRouter Passkeys';
    if (result.passkey_rp_id === undefined) result.passkey_rp_id = 'example.test';
    if (result.passkey_origins === undefined) result.passkey_origins = 'https://example.test';
    if (result.passkey_allow_insecure === undefined) result.passkey_allow_insecure = false;
    if (result.passkey_user_verification === undefined) result.passkey_user_verification = 'preferred';
    if (result.passkey_attachment === undefined) result.passkey_attachment = '';
  }
  return result;
}

function renderLogin(
  initialMode: 'login' | 'register' = 'login',
  replaceLocation = vi.fn(),
) {
  const onLoggedIn = vi.fn();
  const onTwoFARequired = vi.fn();
  const rendered = render(
    <LoginView
      initialMode={initialMode}
      onLoggedIn={onLoggedIn}
      onTwoFARequired={onTwoFARequired}
      replaceLocation={replaceLocation}
    />,
  );
  return { onLoggedIn, onTwoFARequired, replaceLocation, unmount: rendered.unmount };
}

afterEach(() => {
  cleanup();
  mockedGetData.mockReset();
  mockedAuthGet.mockReset();
  mockedAuthPost.mockReset();
  restorePasskeyBrowser();
  delete window.turnstile;
  window.history.replaceState(null, '', '/sign-in');
});

describe('OAuth callback failure text', () => {
  const fallback = 'External sign-in failed. Please try again.';

  it('accepts one bounded, display-safe provider denial and ignores unrelated queries', () => {
    expect(oauthCallbackErrorMessage('', fallback)).toBeNull();
    expect(oauthCallbackErrorMessage('?code=authorization-code', fallback)).toBeNull();
    expect(oauthCallbackErrorMessage(
      '?error=oauth_access_denied&message=Permission%20declined',
      fallback,
    )).toBe('Permission declined');
  });

  it('replaces oversized, ambiguous, or control-bearing callback text', () => {
    expect(oauthCallbackErrorMessage(
      `?error=oauth_access_denied&message=${'x'.repeat(513)}`,
      fallback,
    )).toBe(fallback);
    expect(oauthCallbackErrorMessage(
      '?error=oauth_access_denied&error=other&message=denied',
      fallback,
    )).toBe(fallback);
    expect(oauthCallbackErrorMessage(
      '?error=oauth_access_denied&message=line%0Abreak',
      fallback,
    )).toBe(fallback);
    expect(oauthCallbackErrorMessage(
      '?error=oauth_access_denied&message=spoof%E2%80%AEtext',
      fallback,
    )).toBe(fallback);
    expect(oauthCallbackErrorMessage(`?${'x'.repeat(8 * 1024)}`, fallback)).toBe(fallback);
  });
});

describe('LoginView', () => {
  it('loads public status once and remains usable through the production StrictMode replay', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    render(
      <StrictMode>
        <LoginView initialMode="login" onLoggedIn={vi.fn()} onTwoFARequired={vi.fn()} />
      </StrictMode>,
    );

    expect(await screen.findByLabelText('Username or Email')).toBeTruthy();
    expect(mockedGetData).toHaveBeenCalledTimes(1);
  });

  it('keeps authentication closed until status loads and recovers through an explicit retry', async () => {
    mockedGetData
      .mockRejectedValueOnce(new Error('private status failure'))
      .mockResolvedValueOnce(status());
    const rendered = renderLogin();

    expect(rendered).toBeTruthy();
    expect(screen.queryByLabelText('Username or Email')).toBeNull();
    expect((await screen.findByRole('alert')).textContent).toBe('Service unavailableRetry');
    expect(screen.queryByText('private status failure')).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));

    expect(await screen.findByLabelText('Username or Email')).toBeTruthy();
    expect(mockedGetData).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[data-auth-layout="true"]')).toBeTruthy();
  });

  it('renders a safe OAuth denial once and removes it from browser history', async () => {
    window.history.replaceState(
      null,
      '',
      '/sign-in?error=oauth_access_denied&message=Permission%20declined',
    );
    mockedGetData.mockResolvedValueOnce(status());

    renderLogin();

    expect((await screen.findByRole('alert')).textContent).toBe('Permission declined');
    expect(window.location.pathname).toBe('/sign-in');
    expect(window.location.search).toBe('');
  });

  it('removes OAuth error fields without discarding a safe return, affiliate, or fragment', async () => {
    window.history.replaceState(
      null,
      '',
      '/sign-in?redirect=%2Fwallet&aff=partner.code&error=oauth_access_denied&message=Denied#section',
    );
    mockedGetData.mockResolvedValueOnce(status());

    renderLogin();

    expect((await screen.findByRole('alert')).textContent).toBe('Denied');
    expect(window.location.search).toBe('?redirect=%2Fwallet&aff=partner.code');
    expect(window.location.hash).toBe('#section');
  });

  it('submits a username or email identifier to the exact password-login contract', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    mockedAuthPost.mockResolvedValueOnce(signedInUser);
    const { onLoggedIn } = renderLogin();
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText('Username or Email'), 'person@example.test');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith(
      '/user/login',
      { username: 'person@example.test', password: 'password1' },
      undefined,
      expect.any(AbortSignal),
    ));
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
  });

  it('rejects a login identifier beyond the backend UTF-8 byte ceiling', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    renderLogin();
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText('Username or Email'), '你'.repeat(17));
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(screen.getByRole('alert').textContent).toBe('Authentication failed.');
    expect(mockedAuthPost).not.toHaveBeenCalled();
  });

  it('requires and consumes a configured Turnstile token on password sign-in', async () => {
    let options: Record<string, unknown> = {};
    window.turnstile = {
      render: vi.fn((_element, value) => {
        options = value;
        return 'login-widget';
      }),
      remove: vi.fn(),
    };
    mockedGetData.mockResolvedValueOnce(status({
      turnstile_check: true,
      turnstile_site_key: 'public-site-key',
    }));
    mockedAuthPost.mockResolvedValueOnce(signedInUser);
    const { onLoggedIn } = renderLogin();
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText('Username or Email'), 'secured-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(mockedAuthPost).not.toHaveBeenCalled();
    expect(screen.getByRole('alert').textContent).toBe('Complete the human verification challenge.');

    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    (options.callback as (token: string) => void)('login-challenge');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith(
      '/user/login',
      { username: 'secured-user', password: 'password1' },
      { turnstile: 'login-challenge' },
      expect.any(AbortSignal),
    ));
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
  });

  it('fails closed when Turnstile is required without a usable site key', async () => {
    mockedGetData.mockResolvedValueOnce(status({ turnstile_check: true, turnstile_site_key: '' }));
    renderLogin();
    await screen.findByText('Human verification is unavailable.');
    expect(screen.getByRole('button', { name: 'Sign in' }).hasAttribute('disabled')).toBe(true);
  });

  it('keeps provider load failures closed until the user retries the widget', async () => {
    let options: Record<string, unknown> = {};
    window.turnstile = {
      render: vi.fn((_element, value) => {
        options = value;
        return `login-widget-${String(window.turnstile?.render && vi.mocked(window.turnstile.render).mock.calls.length)}`;
      }),
      remove: vi.fn(),
    };
    mockedGetData.mockResolvedValueOnce(status({
      turnstile_check: true,
      turnstile_site_key: 'public-site-key',
    }));
    renderLogin();

    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    act(() => { (options['error-callback'] as () => void)(); });
    expect(await screen.findByText('Human verification is unavailable.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Sign in' }).hasAttribute('disabled')).toBe(true);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry human verification' }));
    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(2));
    expect(screen.getByRole('button', { name: 'Sign in' }).hasAttribute('disabled')).toBe(false);
  });


  it('requires configured legal consent before password or provider sign-in', async () => {
    mockedGetData.mockResolvedValueOnce(status({
      wechat_login: true,
      user_agreement_enabled: true,
      privacy_policy_enabled: true,
      custom_oauth_providers: [{ id: 2, name: 'Example ID', slug: 'example', icon: '' }],
    }));
    mockedAuthPost.mockResolvedValueOnce(signedInUser);
    const { onLoggedIn } = renderLogin();
    const user = userEvent.setup();

    const consent = await screen.findByRole('checkbox', { name: /I have read and agree to the/ });
    const submit = screen.getByRole('button', { name: 'Sign in' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: 'Continue with WeChat' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('link', { name: 'Continue with GitHub' }).getAttribute('aria-disabled')).toBe('true');
    expect(screen.getByRole('link', { name: 'Continue with GitHub' }).getAttribute('href')).toBeNull();
    expect(screen.getByRole('link', { name: 'User Agreement' }).getAttribute('href')).toBe('/user-agreement');
    expect(screen.getByRole('link', { name: 'Privacy Policy' }).getAttribute('href')).toBe('/privacy-policy');

    await user.click(screen.getByRole('link', { name: 'Continue with Example ID' }));
    expect(screen.getByRole('alert').textContent).toBe('Accept the configured terms to continue.');
    await user.click(consent);
    expect(submit.hasAttribute('disabled')).toBe(false);
    expect(screen.getByRole('button', { name: 'Continue with WeChat' }).hasAttribute('disabled')).toBe(false);
    expect(screen.getByRole('link', { name: 'Continue with GitHub' }).getAttribute('href')).toBe('/api/oauth/github');

    await user.type(screen.getByLabelText('Username or Email'), 'legal-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.click(submit);
    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith('/user/login', {
      username: 'legal-user', password: 'password1',
    }, undefined, expect.any(AbortSignal)));
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
  });

  it('does not render a consent gate when no legal document is enabled', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    renderLogin();

    await waitFor(() => expect(mockedGetData).toHaveBeenCalledWith('/status'));
    expect(screen.queryByRole('checkbox')).toBeNull();
    expect(screen.getByRole('button', { name: 'Sign in' }).hasAttribute('disabled')).toBe(false);
  });

  it('renders only OAuth methods advertised by public status', async () => {
    mockedGetData.mockResolvedValueOnce(status({
      github_oauth: false,
      discord_oauth: true,
      oidc_enabled: false,
      linuxdo_oauth: true,
      custom_oauth_providers: [{ id: 2, name: 'Company SSO', slug: 'company', icon: '' }],
    }));
    renderLogin();

    expect((await screen.findByRole('link', { name: 'Continue with Discord' })).getAttribute('href')).toBe('/api/oauth/discord');
    expect(screen.getByRole('link', { name: 'Continue with LinuxDO' }).getAttribute('href')).toBe('/api/oauth/linuxdo');
    expect(screen.getByRole('link', { name: 'Continue with Company SSO' }).getAttribute('href')).toBe('/api/oauth/company');
    expect(screen.queryByRole('link', { name: 'Continue with GitHub' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Continue with OIDC' })).toBeNull();
  });

  it('uses the bounded OpenID Connect display name advertised by status', async () => {
    mockedGetData.mockResolvedValueOnce(status({ oidc_display_name: 'Workforce SSO' }));
    renderLogin();

    expect((await screen.findByRole('link', { name: 'Continue with Workforce SSO' })).getAttribute('href'))
      .toBe('/api/oauth/oidc');
    expect(screen.queryByRole('link', { name: 'Continue with OIDC' })).toBeNull();
  });

  it('carries one backend-valid affiliate value through every server-owned OAuth start', async () => {
    window.history.replaceState(null, '', '/sign-up?aff=partner.code');
    mockedGetData.mockResolvedValueOnce(status({
      custom_oauth_providers: [{ id: 2, name: 'Company SSO', slug: 'company-sso', icon: '' }],
    }));
    renderLogin('register');

    expect((await screen.findByRole('link', { name: 'Continue with GitHub' })).getAttribute('href'))
      .toBe('/api/oauth/github?aff=partner.code');
    expect(screen.getByRole('link', { name: 'Continue with Company SSO' }).getAttribute('href'))
      .toBe('/api/oauth/company-sso?aff=partner.code');
  });

  it('rejects unsafe WeChat authorization codes before transport', async () => {
    mockedGetData.mockResolvedValueOnce(status({ wechat_login: true }));
    renderLogin();
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Continue with WeChat' }));
    fireEvent.change(screen.getByLabelText('WeChat authorization code'), {
      target: { value: 'provider\u202espoof' },
    });
    await user.click(screen.getByRole('button', { name: 'Authorize' }));

    expect(screen.getByRole('alert').textContent).toBe('Enter a valid WeChat authorization code.');
    expect(mockedAuthGet).not.toHaveBeenCalled();
  });

  it('traps focus in the WeChat dialog, closes it with Escape, and returns focus', async () => {
    mockedGetData.mockResolvedValueOnce(status({ wechat_login: true }));
    renderLogin();
    const user = userEvent.setup();
    const trigger = await screen.findByRole('button', { name: 'Continue with WeChat' });

    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'WeChat login' });
    const code = screen.getByLabelText('WeChat authorization code');
    await waitFor(() => expect(document.activeElement).toBe(code));
    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'WeChat login' })).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    expect(dialog.getAttribute('aria-modal')).toBe('true');
  });

  it('completes WeChat sign-in from a strictly selected bundle response', async () => {
    mockedGetData.mockResolvedValueOnce(status({ wechat_login: true }));
    mockedAuthGet.mockResolvedValueOnce({
      user: signedInUser,
      access_token: 'cookie-bound-response-field',
    });
    const { onLoggedIn } = renderLogin();
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Continue with WeChat' }));
    await user.type(screen.getByLabelText('WeChat authorization code'), 'provider-code');
    await user.click(screen.getByRole('button', { name: 'Authorize' }));

    await waitFor(() => expect(mockedAuthGet).toHaveBeenCalledWith(
      '/oauth/wechat', { code: 'provider-code' }, expect.any(AbortSignal),
    ));
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
  });

  it('aborts a pending WeChat exchange when its dialog closes and ignores the late result', async () => {
    mockedGetData.mockResolvedValueOnce(status({ wechat_login: true }));
    let resolveRequest: (value: unknown) => void = () => {};
    const request = new Promise<unknown>((resolve) => { resolveRequest = resolve; });
    mockedAuthGet.mockReturnValueOnce(request);
    const { onLoggedIn } = renderLogin();
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Continue with WeChat' }));
    await user.type(screen.getByLabelText('WeChat authorization code'), 'provider-code');
    await user.click(screen.getByRole('button', { name: 'Authorize' }));
    const signal = mockedAuthGet.mock.calls[0][2] as AbortSignal;
    await user.click(screen.getByRole('button', { name: 'Close' }));
    expect(signal.aborted).toBe(true);
    await act(async () => {
      resolveRequest({ user: signedInUser });
      await request;
    });
    expect(onLoggedIn).not.toHaveBeenCalled();
  });

  it('completes the bounded /oauth WeChat callback without loading public status', async () => {
    window.history.replaceState(
      null,
      '',
      '/oauth?provider=wechat&code=callback-code&redirect=%2Fwallet',
    );
    mockedAuthGet.mockResolvedValueOnce({ user: signedInUser });
    const { onLoggedIn } = renderLogin();

    await waitFor(() => expect(mockedAuthGet).toHaveBeenCalledWith(
      '/oauth/wechat', { code: 'callback-code' }, expect.any(AbortSignal),
    ));
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
    expect(mockedGetData).not.toHaveBeenCalled();
  });

  it('does not restart a one-time WeChat callback when parent handlers change', async () => {
    window.history.replaceState(null, '', '/oauth?provider=wechat&code=callback-code');
    let resolveRequest: (value: unknown) => void = () => {};
    const request = new Promise<unknown>((resolve) => { resolveRequest = resolve; });
    mockedAuthGet.mockReturnValueOnce(request);
    const firstHandler = vi.fn();
    const currentHandler = vi.fn();
    const rendered = render(
      <LoginView initialMode="login" onLoggedIn={firstHandler} onTwoFARequired={vi.fn()} />,
    );

    await waitFor(() => expect(mockedAuthGet).toHaveBeenCalledTimes(1));
    rendered.rerender(
      <LoginView initialMode="login" onLoggedIn={currentHandler} onTwoFARequired={vi.fn()} />,
    );
    expect(mockedAuthGet).toHaveBeenCalledTimes(1);
    await act(async () => {
      resolveRequest({ user: signedInUser });
      await request;
    });

    expect(mockedAuthGet).toHaveBeenCalledTimes(1);
    expect(firstHandler).not.toHaveBeenCalled();
    expect(currentHandler).toHaveBeenCalledWith(signedInUser);
  });

  it('rejects ambiguous /oauth callbacks without sending a provider credential', async () => {
    window.history.replaceState(
      null,
      '',
      '/oauth?provider=wechat&code=one&code=two',
    );
    renderLogin();

    expect((await screen.findByRole('alert')).textContent)
      .toBe('External sign-in failed. Please try again.');
    expect(mockedAuthGet).not.toHaveBeenCalled();
    expect(mockedGetData).not.toHaveBeenCalled();
  });

  it('hands a selected Telegram authorization to the exact same-origin login endpoint', async () => {
    mockedGetData.mockResolvedValueOnce(status({
      telegram_oauth: true,
      telegram_bot_name: '@TokenRouter_bot',
    }));
    const flowToken = 'T'.repeat(64);
    mockedAuthPost.mockResolvedValueOnce({ flow_token: flowToken, expires_at: 1_788_566_999 });
    const replaceLocation = vi.fn();
    const onLoggedIn = vi.fn();
    render(
      <LoginView
        initialMode="login"
        returnTarget="/wallet?section=topup"
        onLoggedIn={onLoggedIn}
        onTwoFARequired={vi.fn()}
        replaceLocation={replaceLocation}
      />,
    );
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Continue with Telegram' }));
    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith(
      '/oauth/state',
      { provider: 'telegram', intent: 'login', redirect: '/wallet?section=topup' },
      undefined,
      expect.any(AbortSignal),
    ));
    const script = await waitFor(() => {
      const candidate = document.querySelector<HTMLScriptElement>('script[data-telegram-login]');
      expect(candidate).toBeTruthy();
      return candidate!;
    });
    expect(script?.dataset.telegramLogin).toBe('TokenRouter_bot');
    const callbackName = script?.dataset.onauth?.replace('(user)', '') ?? '';
    const callback = (window as unknown as Record<string, unknown>)[callbackName];
    act(() => {
      (callback as (value: unknown) => void)({
        id: 42,
        first_name: 'Alice',
        auth_date: '1788566400',
        hash: 'a'.repeat(64),
        ignored: 'not-forwarded',
      });
      (callback as (value: unknown) => void)({
        id: 99,
        auth_date: '1788566400',
        hash: 'b'.repeat(64),
      });
    });

    expect(replaceLocation).toHaveBeenCalledTimes(1);
    expect(replaceLocation).toHaveBeenCalledWith(
      `/api/oauth/telegram/login?flow_token=${flowToken}&id=42&first_name=Alice&auth_date=1788566400&hash=${'a'.repeat(64)}`,
    );
  });

  it('fails closed when Telegram state allocation is malformed', async () => {
    mockedGetData.mockResolvedValueOnce(status({
      telegram_oauth: true,
      telegram_bot_name: '@TokenRouter_bot',
    }));
    mockedAuthPost.mockResolvedValueOnce({ flow_token: 'short', expires_at: 1 });
    renderLogin();

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Continue with Telegram' }));
    expect((await screen.findByRole('alert')).textContent)
      .toContain('External sign-in failed. Please try again.');
    expect(document.querySelector('script[data-telegram-login]')).toBeNull();
  });

  it('fails closed when Telegram is advertised without a usable bot identity', async () => {
    mockedGetData.mockResolvedValueOnce(status({
      telegram_oauth: true,
      telegram_bot_name: 'not a bot',
    }));
    renderLogin();

    const button = await screen.findByRole('button', { name: 'Continue with Telegram' });
    expect(button.hasAttribute('disabled')).toBe(true);
    expect(button.getAttribute('aria-describedby')).toBe('telegram-unavailable');
  });

  it('hides registration and leaves the sign-in form active in personal-use mode', async () => {
    mockedGetData.mockResolvedValueOnce(status({ self_use_mode_enabled: true }));
    renderLogin('register');

    await waitFor(() => expect(mockedGetData).toHaveBeenCalledWith('/status'));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy());
    expect(screen.queryByRole('button', { name: 'No account? Register' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Register' })).toBeNull();
  });

  it('hides registration whenever the global registration gate is closed', async () => {
    mockedGetData.mockResolvedValueOnce(status({ register_enabled: false }));
    renderLogin('register');

    expect(await screen.findByLabelText('Username or Email')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Register' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'No account? Register' })).toBeNull();
  });

  it('requires matching registration passwords before any transport occurs', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    renderLogin('register');
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText('Username'), 'new-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.type(screen.getByLabelText('Confirm password'), 'password2');
    await user.click(screen.getByRole('button', { name: 'Register' }));

    expect(screen.getByRole('alert').textContent).toBe('Passwords do not match.');
    expect(mockedAuthPost).not.toHaveBeenCalled();
  });

  it('applies the same consent gate to registration and submits the exact payload', async () => {
    mockedGetData.mockResolvedValueOnce(status({ privacy_policy_enabled: true }));
    mockedAuthPost.mockResolvedValueOnce({});
    renderLogin('register');
    const user = userEvent.setup();

    const consent = await screen.findByRole('checkbox', { name: /I have read and agree to the/ });
    const submit = screen.getByRole('button', { name: 'Register' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    expect(screen.queryByRole('link', { name: 'User Agreement' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Privacy Policy' })).toBeTruthy();

    await user.type(screen.getByLabelText('Username'), 'new-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.type(screen.getByLabelText('Confirm password'), 'password1');
    await user.click(consent);
    await user.click(submit);
    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith('/user/register', {
      username: 'new-user', password: 'password1',
    }, undefined, expect.any(AbortSignal)));
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy();
  });

  it('sends a bounded email code and preserves an editable invitation for verified registration', async () => {
    window.history.replaceState(null, '', '/sign-up?aff=INVITE_7');
    mockedGetData.mockResolvedValueOnce(status({ email_verification: true }));
    mockedAuthGet.mockResolvedValueOnce(undefined);
    mockedAuthPost.mockResolvedValueOnce({});
    renderLogin('register');
    const user = userEvent.setup();

    expect((await screen.findByLabelText('Invitation code (optional)')).getAttribute('value')).toBe('INVITE_7');
    await user.type(screen.getByLabelText('Username'), 'verified-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.type(screen.getByLabelText('Confirm password'), 'password1');
    await user.type(screen.getByLabelText('Email'), 'person@example.com');
    await user.click(screen.getByRole('button', { name: 'Send verification code' }));
    await waitFor(() => expect(mockedAuthGet).toHaveBeenCalledWith('/verification', {
      email: 'person@example.com',
    }, expect.any(AbortSignal)));
    expect(screen.getByRole('button', { name: 'Resend in 30 seconds' }).hasAttribute('disabled')).toBe(true);

    await user.type(screen.getByLabelText('Verification code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Register' }));
    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith('/user/register', {
      username: 'verified-user',
      password: 'password1',
      email: 'person@example.com',
      verification_code: '123456',
      aff_code: 'INVITE_7',
    }, undefined, expect.any(AbortSignal)));
  });

  it('consumes separate Turnstile proofs for the email and registration requests', async () => {
    const options: Array<Record<string, unknown>> = [];
    window.turnstile = {
      render: vi.fn((_element, value) => {
        options.push(value);
        return `registration-widget-${options.length}`;
      }),
      remove: vi.fn(),
    };
    mockedGetData.mockResolvedValueOnce(status({
      email_verification: true,
      turnstile_check: true,
      turnstile_site_key: 'registration-site-key',
    }));
    mockedAuthGet.mockResolvedValueOnce(undefined);
    mockedAuthPost.mockResolvedValueOnce({});
    renderLogin('register');
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText('Username'), 'proof-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.type(screen.getByLabelText('Confirm password'), 'password1');
    await user.type(screen.getByLabelText('Email'), 'proof@example.com');
    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    act(() => { (options[0].callback as (token: string) => void)('email-proof'); });
    await user.click(screen.getByRole('button', { name: 'Send verification code' }));
    await waitFor(() => expect(mockedAuthGet).toHaveBeenCalledWith('/verification', {
      email: 'proof@example.com',
      turnstile: 'email-proof',
    }, expect.any(AbortSignal)));
    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(2));

    await user.type(screen.getByLabelText('Verification code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Register' }));
    expect(screen.getByRole('alert').textContent).toBe('Complete the human verification challenge.');
    expect(mockedAuthPost).not.toHaveBeenCalled();

    act(() => { (options[1].callback as (token: string) => void)('registration-proof'); });
    await user.click(screen.getByRole('button', { name: 'Register' }));
    await waitFor(() => expect(mockedAuthPost).toHaveBeenCalledWith('/user/register', {
      username: 'proof-user',
      password: 'password1',
      email: 'proof@example.com',
      verification_code: '123456',
    }, { turnstile: 'registration-proof' }, expect.any(AbortSignal)));
  });

  it('honors password login and registration availability from status', async () => {
    mockedGetData.mockResolvedValueOnce(status({
      password_login_enabled: false,
      password_register_enabled: false,
    }));
    renderLogin('register');

    expect(await screen.findByText('Password registration is disabled.')).toBeTruthy();
    expect(screen.queryByLabelText('Username')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Register' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'No account? Register' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Have an account? Sign in' })).toBeTruthy();
  });

  it('hands a bounded server-issued two-factor flow to the OTP route', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    mockedAuthPost.mockResolvedValueOnce({ twofa_required: true, flow_token: 'flow-123' });
    const { onLoggedIn, onTwoFARequired } = renderLogin();
    const user = userEvent.setup();

    await waitFor(() => expect(mockedGetData).toHaveBeenCalledWith('/status'));
    await user.type(await screen.findByLabelText('Username or Email'), 'secured-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await waitFor(() => expect(onTwoFARequired).toHaveBeenCalledWith('flow-123'));
    expect(onLoggedIn).not.toHaveBeenCalled();
  });

  it('rejects malformed and oversized authentication responses', async () => {
    mockedGetData.mockResolvedValueOnce(status());
    mockedAuthPost.mockResolvedValueOnce({ twofa_required: true, flow_token: 'x'.repeat(257) });
    const { onLoggedIn, onTwoFARequired } = renderLogin();
    const user = userEvent.setup();

    await waitFor(() => expect(mockedGetData).toHaveBeenCalledWith('/status'));
    await user.type(await screen.findByLabelText('Username or Email'), 'secured-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect((await screen.findByRole('alert')).textContent).toBe('Authentication failed.');
    expect(onLoggedIn).not.toHaveBeenCalled();
    expect(onTwoFARequired).not.toHaveBeenCalled();
  });

  it('completes passkey sign-in with decoded options and a bounded assertion payload', async () => {
    const credentialGet = installPasskeyBrowser();
    credentialGet.mockResolvedValueOnce(validAssertion());
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    mockedAuthPost
      .mockResolvedValueOnce(validPasskeyBegin())
      .mockResolvedValueOnce(signedInUser);
    const { onLoggedIn } = renderLogin();

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign in with passkey' }));

    await waitFor(() => expect(credentialGet).toHaveBeenCalledTimes(1));
    const request = credentialGet.mock.calls[0][0] as CredentialRequestOptions;
    expect(Array.from(new Uint8Array(request.publicKey?.challenge as ArrayBuffer))).toEqual([
      0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
    ]);
    expect(Array.from(new Uint8Array(request.publicKey?.allowCredentials?.[0].id as ArrayBuffer))).toEqual([1, 2, 3]);
    await waitFor(() => expect(mockedAuthPost).toHaveBeenNthCalledWith(2, '/user/passkey/login/finish', {
      flow_token: 'f'.repeat(64),
      id: 'credential-id',
      rawId: 'AQID',
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response: {
        clientDataJSON: 'BA',
        authenticatorData: 'BQ',
        signature: 'Bg',
        userHandle: 'Bw',
      },
      clientExtensionResults: { appid: true },
    }, undefined, expect.any(AbortSignal)));
    expect(mockedAuthPost).toHaveBeenNthCalledWith(
      1, '/user/passkey/login/begin', undefined, undefined, expect.any(AbortSignal),
    );
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
  });

  it('shows the advertised passkey feature as unavailable without browser support', async () => {
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    renderLogin();

    const button = await screen.findByRole('button', { name: 'Sign in with passkey' });
    expect(button.hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('status').textContent).toBe('Passkey sign-in is unavailable in this browser.');
    expect(mockedAuthPost).not.toHaveBeenCalled();
  });

  it('reports passkey cancellation without exposing browser details', async () => {
    const credentialGet = installPasskeyBrowser();
    credentialGet.mockRejectedValueOnce(new DOMException('private authenticator detail', 'NotAllowedError'));
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    mockedAuthPost.mockResolvedValueOnce(validPasskeyBegin());
    renderLogin();

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign in with passkey' }));

    expect((await screen.findByRole('alert')).textContent).toBe('Passkey sign-in was cancelled or timed out.');
    expect(screen.queryByText(/private authenticator detail/)).toBeNull();
    expect(mockedAuthPost).toHaveBeenCalledTimes(1);
  });

  it('rejects malformed passkey begin data with a stable redacted error', async () => {
    const credentialGet = installPasskeyBrowser();
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    mockedAuthPost.mockResolvedValueOnce({
      ...validPasskeyBegin(),
      flow_token: 'x'.repeat(257),
    });
    renderLogin();

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign in with passkey' }));

    expect((await screen.findByRole('alert')).textContent).toBe('Passkey sign-in failed. Please try again.');
    expect(credentialGet).not.toHaveBeenCalled();
    expect(mockedAuthPost).toHaveBeenCalledTimes(1);
  });

  it('rejects a passkey finish response without a valid authenticated user', async () => {
    const credentialGet = installPasskeyBrowser();
    credentialGet.mockResolvedValueOnce(validAssertion());
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    mockedAuthPost
      .mockResolvedValueOnce(validPasskeyBegin())
      .mockResolvedValueOnce({ id: 0, username: 'invalid-user' });
    const { onLoggedIn } = renderLogin();

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign in with passkey' }));

    expect((await screen.findByRole('alert')).textContent).toBe('Passkey sign-in failed. Please try again.');
    expect(onLoggedIn).not.toHaveBeenCalled();
  });

  it('never renders a server error from a failed passkey ceremony', async () => {
    installPasskeyBrowser();
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    mockedAuthPost.mockRejectedValueOnce(new Error('upstream secret trace id 123'));
    renderLogin();

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign in with passkey' }));

    expect((await screen.findByRole('alert')).textContent).toBe('Passkey sign-in failed. Please try again.');
    expect(screen.queryByText(/upstream secret trace/)).toBeNull();
  });

  it('applies the legal-consent gate to passkey sign-in', async () => {
    const credentialGet = installPasskeyBrowser();
    credentialGet.mockResolvedValueOnce(null);
    mockedGetData.mockResolvedValueOnce(status({
      passkey_login: true,
      password_login_enabled: false,
      user_agreement_enabled: true,
    }));
    mockedAuthPost.mockResolvedValueOnce(validPasskeyBegin());
    renderLogin();
    const user = userEvent.setup();

    const passkey = await screen.findByRole('button', { name: 'Sign in with passkey' });
    expect(passkey.hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('checkbox', { name: /I have read and agree to the/ })).toBeTruthy();
    expect(mockedAuthPost).not.toHaveBeenCalled();

    await user.click(screen.getByRole('checkbox'));
    expect(passkey.hasAttribute('disabled')).toBe(false);
    await user.click(passkey);
    expect((await screen.findByRole('alert')).textContent).toBe('Passkey sign-in was cancelled or timed out.');
    expect(mockedAuthPost).toHaveBeenCalledWith(
      '/user/passkey/login/begin', undefined, undefined, expect.any(AbortSignal),
    );
  });

  it('does not expose passkey sign-in when status disables it', async () => {
    installPasskeyBrowser();
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: false }));
    renderLogin();

    await waitFor(() => expect(mockedGetData).toHaveBeenCalledWith('/status'));
    expect(screen.queryByRole('button', { name: 'Sign in with passkey' })).toBeNull();
  });

  it('deduplicates passkey submits and ignores a late begin response after unmount', async () => {
    const credentialGet = installPasskeyBrowser();
    let resolveBegin: (value: unknown) => void = () => {};
    const beginPromise = new Promise<unknown>((resolve) => {
      resolveBegin = resolve;
    });
    mockedGetData.mockResolvedValueOnce(status({ passkey_login: true }));
    mockedAuthPost.mockReturnValueOnce(beginPromise);
    const { unmount } = renderLogin();
    const passkey = await screen.findByRole('button', { name: 'Sign in with passkey' });

    act(() => {
      passkey.click();
      passkey.click();
    });
    expect(mockedAuthPost).toHaveBeenCalledTimes(1);
    unmount();
    await act(async () => {
      resolveBegin(validPasskeyBegin());
      await beginPromise;
    });
    expect(credentialGet).not.toHaveBeenCalled();
  });
});
