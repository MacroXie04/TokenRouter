// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { buildOAuthCallbackURLs, OAuthCallbackGuidance } from './OAuthCallbackGuidance';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, values?: Record<string, string>) => key.replace('{{provider}}', values?.provider ?? ''),
    }),
  };
});

const writeText = vi.fn<() => Promise<void>>();

beforeEach(() => {
  vi.resetAllMocks();
  writeText.mockResolvedValue();
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
});

afterEach(cleanup);

describe('OAuth callback guidance', () => {
  it('builds exact callbacks from the configured safe server address', () => {
    expect(buildOAuthCallbackURLs(
      [{ key: 'ServerAddress', value: 'https://router.example.test/base/' }],
      'http://localhost',
    )).toEqual([
      { id: 'github', label: 'GitHub', url: 'https://router.example.test/base/api/oauth/github/callback' },
      { id: 'discord', label: 'Discord', url: 'https://router.example.test/base/api/oauth/discord/callback' },
      { id: 'oidc', label: 'OpenID Connect', url: 'https://router.example.test/base/api/oauth/oidc/callback' },
      { id: 'linuxdo', label: 'Linux DO', url: 'https://router.example.test/base/api/oauth/linuxdo/callback' },
    ]);
  });

  it('rejects unsafe configured bases instead of showing misleading callbacks', () => {
    expect(buildOAuthCallbackURLs(
      [{ key: 'ServerAddress', value: 'http://identity.example.test?next=callback' }],
      'http://localhost',
    )).toBeNull();
    render(<OAuthCallbackGuidance options={[
      { key: 'ServerAddress', value: 'https://user:password@identity.example.test' },
    ]} />);
    expect(screen.getByRole('alert').textContent).toContain('Set a safe HTTPS server address');
    expect(screen.queryByRole('button', { name: /Copy/u })).toBeNull();
  });

  it('copies one bounded callback and announces the result', async () => {
    render(<OAuthCallbackGuidance options={[
      { key: 'ServerAddress', value: 'https://router.example.test' },
    ]} />);

    fireEvent.click(screen.getByRole('button', { name: 'Copy GitHub callback URL' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://router.example.test/api/oauth/github/callback'));
    expect(await screen.findByRole('status')).toHaveProperty('textContent', 'GitHub callback URL copied.');
  });
});
