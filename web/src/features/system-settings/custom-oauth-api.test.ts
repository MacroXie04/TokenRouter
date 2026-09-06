import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  createCustomOAuthProvider,
  deleteCustomOAuthProvider,
  discoverCustomOAuthProvider,
  loadCustomOAuthProviders,
  parseCustomOAuthProvidersResponse,
  updateCustomOAuthProvider,
  type CustomOAuthInput,
} from './custom-oauth-api';
import { SystemSettingsContractError, SystemSettingsRequestError } from './system-settings-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);
const mockedPut = vi.mocked(api.put);
const mockedDelete = vi.mocked(api.delete);

const provider = (overrides: Record<string, unknown> = {}) => ({
  id: 7,
  name: 'Company SSO',
  slug: 'company-sso',
  icon: 'building',
  enabled: true,
  client_id: 'client-id',
  authorization_endpoint: 'https://identity.example.com/authorize?prompt=login',
  token_endpoint: 'https://identity.example.com/token',
  user_info_endpoint: 'https://identity.example.com/userinfo',
  scopes: 'openid profile email',
  user_id_field: 'sub',
  username_field: 'profile.username',
  display_name_field: 'name',
  email_field: 'email',
  well_known: 'https://identity.example.com/.well-known/openid-configuration',
  auth_style: 2,
  access_policy: '',
  access_denied_message: 'Not authorized',
  ...overrides,
});

const input: CustomOAuthInput = {
  name: 'Company SSO',
  slug: 'company-sso',
  icon: 'building',
  enabled: true,
  clientId: 'client-id',
  clientSecret: 'write-only-secret',
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
  accessPolicy: '{"conditions":[{"field":"team","op":"eq","value":"platform"}]}',
  accessDeniedMessage: 'Team membership required',
};

const ok = (data?: unknown) => data === undefined
  ? { success: true, message: '' }
  : { success: true, message: '', data };

beforeEach(() => vi.resetAllMocks());

describe('custom OAuth response contracts', () => {
  it('projects the exact provider shape without any secret field', () => {
    expect(parseCustomOAuthProvidersResponse(ok([provider()]))).toEqual([{
      id: 7,
      name: 'Company SSO',
      slug: 'company-sso',
      icon: 'building',
      enabled: true,
      clientId: 'client-id',
      authorizationEndpoint: 'https://identity.example.com/authorize?prompt=login',
      tokenEndpoint: 'https://identity.example.com/token',
      userInfoEndpoint: 'https://identity.example.com/userinfo',
      scopes: 'openid profile email',
      userIdField: 'sub',
      usernameField: 'profile.username',
      displayNameField: 'name',
      emailField: 'email',
      wellKnown: 'https://identity.example.com/.well-known/openid-configuration',
      authStyle: 2,
      accessPolicy: '',
      accessDeniedMessage: 'Not authorized',
    }]);
  });

  it('rejects leaked secrets, duplicate identities, unsafe URLs, Unicode, and malformed policies', () => {
    const invalid = [
      ok([{ ...provider(), client_secret: 'leaked' }]),
      ok([provider(), provider({ id: 8 })]),
      ok([provider({ token_endpoint: 'http://identity.example.com/token' })]),
      ok([provider({ name: `unsafe${String.fromCharCode(0xd800)}` })]),
      ok([provider({ access_policy: '{"conditions":[]}' })]),
      ok([provider({ access_policy: '{"conditions":[{"field":"team","op":"xor","value":1}]}' })]),
    ];
    invalid.forEach((value) => {
      expect(() => parseCustomOAuthProvidersResponse(value)).toThrow(SystemSettingsContractError);
    });
    expect(() => parseCustomOAuthProvidersResponse({ success: false, message: 'denied' }))
      .toThrow(SystemSettingsRequestError);
  });
});

describe('custom OAuth transports', () => {
  it('loads, creates, updates, and deletes via the exact root-only endpoints', async () => {
    mockedGet.mockResolvedValueOnce({ data: ok([provider()]) });
    mockedPost.mockResolvedValueOnce({ data: ok(provider()) });
    mockedPut.mockResolvedValueOnce({ data: ok(provider({ name: 'Renamed SSO' })) });
    mockedDelete.mockResolvedValueOnce({ data: ok() });
    const signal = new AbortController().signal;

    await expect(loadCustomOAuthProviders(signal)).resolves.toHaveLength(1);
    await expect(createCustomOAuthProvider(input, signal)).resolves.toMatchObject({ id: 7, slug: 'company-sso' });
    await expect(updateCustomOAuthProvider(7, { ...input, name: 'Renamed SSO', clientSecret: '' }, signal))
      .resolves.toMatchObject({ name: 'Renamed SSO' });
    await expect(deleteCustomOAuthProvider(7, signal)).resolves.toBeUndefined();

    const limits = { signal, timeout: 15_000, maxContentLength: 2 * 1024 * 1024, maxBodyLength: 2 * 1024 * 1024 };
    expect(mockedGet).toHaveBeenCalledWith('/custom-oauth-provider/', limits);
    expect(mockedPost).toHaveBeenCalledWith('/custom-oauth-provider/', expect.objectContaining({
      name: 'Company SSO',
      slug: 'company-sso',
      client_id: 'client-id',
      client_secret: 'write-only-secret',
      auth_style: 0,
    }), limits);
    expect(mockedPut).toHaveBeenCalledWith('/custom-oauth-provider/7', expect.objectContaining({
      name: 'Renamed SSO',
      client_secret: '',
    }), limits);
    expect(mockedDelete).toHaveBeenCalledWith('/custom-oauth-provider/7', limits);
  });

  it('uses discovery input exactly and projects a bounded subset of the document', async () => {
    mockedPost.mockResolvedValueOnce({ data: ok({
      well_known_url: 'https://identity.example.com/.well-known/openid-configuration',
      discovery: {
        issuer: 'https://identity.example.com',
        authorization_endpoint: 'https://identity.example.com/authorize',
        token_endpoint: 'https://identity.example.com/token',
        userinfo_endpoint: 'https://identity.example.com/userinfo',
        scopes_supported: ['openid', 'profile'],
        ignored_extension: { anything: true },
      },
    }) });
    const signal = new AbortController().signal;

    await expect(discoverCustomOAuthProvider(
      'https://identity.example.com/.well-known/openid-configuration',
      signal,
    )).resolves.toEqual({
      wellKnownUrl: 'https://identity.example.com/.well-known/openid-configuration',
      issuer: 'https://identity.example.com',
      authorizationEndpoint: 'https://identity.example.com/authorize',
      tokenEndpoint: 'https://identity.example.com/token',
      userInfoEndpoint: 'https://identity.example.com/userinfo',
      scopes: 'openid profile',
    });
    expect(mockedPost).toHaveBeenCalledWith('/custom-oauth-provider/discovery', {
      well_known_url: 'https://identity.example.com/.well-known/openid-configuration',
      issuer_url: '',
    }, {
      signal,
      timeout: 25_000,
      maxContentLength: 2 * 1024 * 1024,
      maxBodyLength: 2 * 1024 * 1024,
    });
  });

  it('rejects invalid write inputs before making a request', () => {
    expect(() => createCustomOAuthProvider({ ...input, slug: 'github' })).toThrow(SystemSettingsContractError);
    expect(() => createCustomOAuthProvider({ ...input, tokenEndpoint: 'http://identity.example.com/token' }))
      .toThrow(SystemSettingsContractError);
    expect(() => createCustomOAuthProvider({ ...input, accessPolicy: '{"logic":"xor","conditions":[{"field":"x","op":"eq","value":1}]}' }))
      .toThrow(SystemSettingsContractError);
    expect(mockedPost).not.toHaveBeenCalled();
  });
});
