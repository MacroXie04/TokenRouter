// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { OAuthCallbackForwarder, oauthCallbackTarget, twoFactorContinuationTarget } from './App';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en', changeLanguage: vi.fn() },
    }),
  };
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('OAuth callback forwarding', () => {
  it('replaces the browser location with the exact bounded provider callback', async () => {
    const replaceLocation = vi.fn();
    render(
      <OAuthCallbackForwarder
        provider="custom_oidc-1"
        search="?code=abc123&state=bounded"
        replaceLocation={replaceLocation}
      />,
    );

    expect(screen.getByText('Completing external sign-in…')).toBeTruthy();
    await waitFor(() => expect(replaceLocation).toHaveBeenCalledOnce());
    expect(replaceLocation).toHaveBeenCalledWith(
      '/api/oauth/custom_oidc-1/callback?code=abc123&state=bounded',
    );
  });

  it('fails closed for malformed providers and unsafe or oversized query strings', () => {
    expect(oauthCallbackTarget('../admin', '?code=1')).toBeNull();
    expect(oauthCallbackTarget('github', 'code=1')).toBeNull();
    expect(oauthCallbackTarget('github', '?code=line\nbreak')).toBeNull();
    expect(oauthCallbackTarget('github', `?code=${'x'.repeat(8 * 1024)}`)).toBeNull();
    expect(oauthCallbackTarget('github', '')).toBe('/api/oauth/github/callback');

    const replaceLocation = vi.fn();
    render(<OAuthCallbackForwarder provider="../admin" search="?code=1" replaceLocation={replaceLocation} />);
    expect(screen.getByRole('heading', { name: 'Page not found' })).toBeTruthy();
    expect(replaceLocation).not.toHaveBeenCalled();
  });
});

describe('two-factor continuation', () => {
  it('carries only a validated same-origin return target into the OTP route', () => {
    expect(twoFactorContinuationTarget('?redirect=%2Fwallet%3Ftab%3Dorders')).toBe(
      '/otp?redirect=%2Fwallet%3Ftab%3Dorders',
    );
    expect(twoFactorContinuationTarget('?redirect=https%3A%2F%2Fevil.example')).toBe('/otp');
    expect(twoFactorContinuationTarget('?redirect=%2F%2Fevil.example')).toBe('/otp');
  });
});
