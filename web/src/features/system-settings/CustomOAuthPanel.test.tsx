// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createCustomOAuthProvider,
  deleteCustomOAuthProvider,
  discoverCustomOAuthProvider,
  loadCustomOAuthProviders,
  updateCustomOAuthProvider,
  type CustomOAuthProvider,
} from './custom-oauth-api';
import { CustomOAuthPanel } from './CustomOAuthPanel';

vi.mock('./custom-oauth-api', () => ({
  createCustomOAuthProvider: vi.fn(),
  deleteCustomOAuthProvider: vi.fn(),
  discoverCustomOAuthProvider: vi.fn(),
  loadCustomOAuthProviders: vi.fn(),
  updateCustomOAuthProvider: vi.fn(),
}));
vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return { ...actual, useTranslation: () => ({ t: (key: string) => key }) };
});

const mockedLoad = vi.mocked(loadCustomOAuthProviders);
const mockedCreate = vi.mocked(createCustomOAuthProvider);
const mockedUpdate = vi.mocked(updateCustomOAuthProvider);
const mockedDelete = vi.mocked(deleteCustomOAuthProvider);
const mockedDiscover = vi.mocked(discoverCustomOAuthProvider);

const savedProvider: CustomOAuthProvider = {
  id: 7,
  name: 'Company SSO',
  slug: 'company-sso',
  icon: 'building',
  enabled: true,
  clientId: 'public-client-id',
  authorizationEndpoint: 'https://identity.example.com/authorize',
  tokenEndpoint: 'https://identity.example.com/token',
  userInfoEndpoint: 'https://identity.example.com/userinfo',
  scopes: 'openid profile email',
  userIdField: 'sub',
  usernameField: 'preferred_username',
  displayNameField: 'name',
  emailField: 'email',
  wellKnown: 'https://identity.example.com/.well-known/openid-configuration',
  authStyle: 0,
  accessPolicy: '',
  accessDeniedMessage: '',
};

beforeEach(() => {
  vi.resetAllMocks();
  mockedLoad.mockResolvedValue([savedProvider]);
  mockedCreate.mockResolvedValue(savedProvider);
  mockedUpdate.mockResolvedValue(savedProvider);
  mockedDelete.mockResolvedValue(undefined);
});

afterEach(cleanup);

describe('CustomOAuthPanel', () => {
  it('renders bounded provider metadata and keeps the client secret write-only when editing', async () => {
    const user = userEvent.setup();
    render(<CustomOAuthPanel />);

    expect(await screen.findByText('Company SSO')).toBeTruthy();
    expect(screen.getByText('public-client-id')).toBeTruthy();
    expect(document.body.textContent).not.toContain('clientSecret');
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const secret = screen.getByLabelText('Client secret') as HTMLInputElement;
    expect(secret.value).toBe('');
    expect(secret.placeholder).toBe('Keep current secret');
    expect(secret.required).toBe(false);
  });

  it('creates a provider with a write-only secret and an abort signal', async () => {
    const user = userEvent.setup();
    mockedLoad.mockResolvedValueOnce([]);
    render(<CustomOAuthPanel />);
    await screen.findByText('No custom OAuth providers configured.');
    await user.click(screen.getByRole('button', { name: 'Add provider' }));

    await user.type(screen.getByLabelText('Provider name'), 'Company SSO');
    await user.type(screen.getByLabelText('Provider slug'), 'company-sso');
    await user.type(screen.getByLabelText('Client ID'), 'client-id');
    await user.type(screen.getByLabelText('Client secret'), 'new-secret');
    await user.type(screen.getByLabelText('Authorization endpoint'), 'https://identity.example.com/authorize');
    await user.type(screen.getByLabelText('Token endpoint'), 'https://identity.example.com/token');
    await user.type(screen.getByLabelText('User information endpoint'), 'https://identity.example.com/userinfo');
    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => expect(mockedCreate).toHaveBeenCalledOnce());
    expect(mockedCreate.mock.calls[0][0]).toMatchObject({
      name: 'Company SSO',
      slug: 'company-sso',
      clientId: 'client-id',
      clientSecret: 'new-secret',
    });
    expect(mockedCreate.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
    expect(screen.queryByRole('dialog', { name: 'Add OAuth provider' })).toBeNull();
  });

  it('prefills only supported fields from discovery metadata', async () => {
    const user = userEvent.setup();
    mockedLoad.mockResolvedValueOnce([]);
    mockedDiscover.mockResolvedValue({
      wellKnownUrl: 'https://identity.example.com/.well-known/openid-configuration',
      issuer: 'https://identity.example.com',
      authorizationEndpoint: 'https://identity.example.com/authorize',
      tokenEndpoint: 'https://identity.example.com/token',
      userInfoEndpoint: 'https://identity.example.com/userinfo',
      scopes: 'openid email',
    });
    render(<CustomOAuthPanel />);
    await screen.findByText('No custom OAuth providers configured.');
    await user.click(screen.getByRole('button', { name: 'Add provider' }));
    await user.type(screen.getByLabelText('Discovery URL'), 'https://identity.example.com/.well-known/openid-configuration');
    await user.click(screen.getByRole('button', { name: 'Discover' }));

    await waitFor(() => expect(mockedDiscover).toHaveBeenCalledOnce());
    expect((screen.getByLabelText('Authorization endpoint') as HTMLInputElement).value)
      .toBe('https://identity.example.com/authorize');
    expect((screen.getByLabelText('Scopes') as HTMLInputElement).value).toBe('openid email');
  });

  it('requires an explicit alert-dialog confirmation before deletion', async () => {
    const user = userEvent.setup();
    render(<CustomOAuthPanel />);
    expect(await screen.findByText('Company SSO')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Delete' }));
    expect(mockedDelete).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Delete OAuth provider?' })).toBeTruthy();
    await user.click(screen.getAllByRole('button', { name: 'Delete' }).at(-1)!);

    await waitFor(() => expect(mockedDelete).toHaveBeenCalledOnce());
    expect(mockedDelete.mock.calls[0][0]).toBe(7);
    expect(mockedDelete.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
    expect(screen.queryByText('Company SSO')).toBeNull();
  });

  it('aborts the active list request on unmount', async () => {
    let signal: AbortSignal | undefined;
    let began!: () => void;
    const started = new Promise<void>((resolve) => { began = resolve; });
    mockedLoad.mockImplementationOnce((requestSignal) => {
      signal = requestSignal;
      began();
      return new Promise<CustomOAuthProvider[]>(() => undefined);
    });
    const view = render(<CustomOAuthPanel />);
    await started;
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });
});
