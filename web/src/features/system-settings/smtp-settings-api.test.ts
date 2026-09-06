import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import { SystemSettingsContractError, SystemSettingsRequestError } from './system-settings-api';
import {
  parseSMTPSettingsResponse,
  SMTPSettingsValidationError,
  updateSMTPSettings,
  validateSMTPSettingsUpdate,
  type SMTPSettingsUpdate,
} from './smtp-settings-api';

function validUpdate(overrides: Partial<SMTPSettingsUpdate> = {}): SMTPSettingsUpdate {
  return {
    server: 'smtp.example.test',
    port: '587',
    account: 'mailer@example.test',
    from: 'no-reply@example.test',
    token: '',
    currentTokenConfigured: true,
    clearToken: false,
    sslEnabled: false,
    startTLSEnabled: true,
    insecureSkipVerify: false,
    forceAuthLogin: false,
    ...overrides,
  };
}

function successfulResponse(overrides: Record<string, unknown> = {}) {
  return {
    success: true,
    message: '',
    data: {
      SMTPServer: 'smtp.example.test',
      SMTPPort: '587',
      SMTPAccount: 'mailer@example.test',
      SMTPFrom: 'no-reply@example.test',
      SMTPToken: '',
      SMTPTokenRedacted: true,
      SMTPSSLEnabled: false,
      SMTPStartTLSEnabled: true,
      SMTPInsecureSkipVerify: false,
      SMTPForceAuthLogin: false,
      ...overrides,
    },
  };
}

afterEach(() => vi.restoreAllMocks());

describe('SMTP settings API', () => {
  it('sends the complete non-secret domain and omits a preserved password', async () => {
    const put = vi.spyOn(api, 'put').mockResolvedValueOnce({ data: successfulResponse() } as never);
    const result = await updateSMTPSettings(validUpdate());

    expect(result).toMatchObject({ server: 'smtp.example.test', tokenConfigured: true, startTLSEnabled: true });
    expect(put).toHaveBeenCalledOnce();
    expect(put.mock.calls[0][0]).toBe('/option/smtp');
    expect(put.mock.calls[0][1]).toEqual({
      SMTPServer: 'smtp.example.test',
      SMTPPort: '587',
      SMTPAccount: 'mailer@example.test',
      SMTPFrom: 'no-reply@example.test',
      SMTPSSLEnabled: false,
      SMTPStartTLSEnabled: true,
      SMTPInsecureSkipVerify: false,
      SMTPForceAuthLogin: false,
      clear_smtp_token: false,
    });
    expect(put.mock.calls[0][2]).toMatchObject({ timeout: 15_000, maxBodyLength: 16 * 1024 });
  });

  it('supports atomic TLS transition, password replacement, and explicit clearing', async () => {
    const put = vi.spyOn(api, 'put')
      .mockResolvedValueOnce({ data: successfulResponse({
        SMTPPort: '465', SMTPSSLEnabled: true, SMTPStartTLSEnabled: false,
      }) } as never)
      .mockResolvedValueOnce({ data: successfulResponse({
        SMTPServer: '', SMTPAccount: '', SMTPFrom: '', SMTPTokenRedacted: false,
      }) } as never);

    await updateSMTPSettings(validUpdate({
      port: '465', sslEnabled: true, startTLSEnabled: false, token: 'replacement', currentTokenConfigured: true,
    }));
    expect(put.mock.calls[0][1]).toMatchObject({
      SMTPPort: '465', SMTPSSLEnabled: true, SMTPStartTLSEnabled: false, SMTPToken: 'replacement',
    });

    await updateSMTPSettings(validUpdate({
      server: '', account: '', from: '', clearToken: true, currentTokenConfigured: true,
    }));
    expect(put.mock.calls[1][1]).toMatchObject({ SMTPServer: '', clear_smtp_token: true });
    expect(put.mock.calls[1][1]).not.toHaveProperty('SMTPToken');
  });

  it.each([
    ['remote plaintext', { sslEnabled: false, startTLSEnabled: false, port: '25' }],
    ['authenticated loopback plaintext', { server: 'localhost', sslEnabled: false, startTLSEnabled: false, port: '25' }],
    ['partial credentials', { currentTokenConfigured: false }],
    ['force login without credentials', { account: '', from: 'sender@example.test', currentTokenConfigured: false, forceAuthLogin: true }],
    ['insecure without TLS', { server: '', sslEnabled: false, startTLSEnabled: false, port: '25', insecureSkipVerify: true }],
    ['both TLS modes', { sslEnabled: true, startTLSEnabled: true }],
    ['non-loopback localhost suffix', { server: 'mail.localhost', account: '', from: 'sender@example.test', currentTokenConfigured: false, sslEnabled: false, startTLSEnabled: false, port: '25' }],
    ['invalid port', { port: '025' }],
    ['invalid sender', { from: 'Display <no-reply@example.test>' }],
    ['secret conflict', { token: 'new', clearToken: true }],
  ])('rejects %s before transport', (_name, overrides) => {
    const put = vi.spyOn(api, 'put');
    expect(() => updateSMTPSettings(validUpdate(overrides))).toThrow(SMTPSettingsValidationError);
    expect(put).not.toHaveBeenCalled();
  });

  it('allows unauthenticated plaintext only for exact or literal loopback development servers', () => {
    expect(() => validateSMTPSettingsUpdate(validUpdate({
      server: 'localhost', port: '25', account: '', from: 'sender@example.test',
      currentTokenConfigured: false, sslEnabled: false, startTLSEnabled: false,
    }))).not.toThrow();
    expect(() => validateSMTPSettingsUpdate(validUpdate({
      server: '127.0.0.2', port: '25', account: '', from: 'sender@example.test',
      currentTokenConfigured: false, sslEnabled: false, startTLSEnabled: false,
    }))).not.toThrow();
    expect(() => validateSMTPSettingsUpdate(validUpdate({
      server: '::1', port: '25', account: '', from: 'sender@example.test',
      currentTokenConfigured: false, sslEnabled: false, startTLSEnabled: false,
    }))).not.toThrow();
  });

  it.each([
    ['unknown response field', successfulResponse({ unexpected: true })],
    ['returned secret', successfulResponse({ SMTPToken: 'leaked' })],
    ['wrong flag type', successfulResponse({ SMTPTokenRedacted: 'yes' })],
    ['incoherent response', successfulResponse({ SMTPSSLEnabled: true, SMTPStartTLSEnabled: true })],
    ['missing data', { success: true, message: '' }],
  ])('rejects %s', (_name, response) => {
    expect(() => parseSMTPSettingsResponse(response)).toThrow(SystemSettingsContractError);
  });

  it('surfaces a bounded server rejection without accepting it as data', () => {
    expect(() => parseSMTPSettingsResponse({ success: false, message: 'invalid configuration' }))
      .toThrow(SystemSettingsRequestError);
  });
});
