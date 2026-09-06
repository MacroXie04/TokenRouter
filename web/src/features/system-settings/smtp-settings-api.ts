import { api } from '../../api';
import {
  serializeSystemSettingsMutation,
  SystemSettingsContractError,
  SystemSettingsRequestError,
} from './system-settings-api';

const REQUEST_TIMEOUT_MS = 15_000;
const MAX_REQUEST_BYTES = 16 * 1024;
const MAX_RESPONSE_BYTES = 16 * 1024;
const MAX_MESSAGE_BYTES = 512;
const encoder = new TextEncoder();

type UnknownRecord = Record<string, unknown>;

export interface SMTPSettingsData {
  server: string;
  port: string;
  account: string;
  from: string;
  tokenConfigured: boolean;
  sslEnabled: boolean;
  startTLSEnabled: boolean;
  insecureSkipVerify: boolean;
  forceAuthLogin: boolean;
}

export interface SMTPSettingsUpdate extends Omit<SMTPSettingsData, 'tokenConfigured'> {
  token?: string;
  currentTokenConfigured: boolean;
  clearToken: boolean;
}

export class SMTPSettingsValidationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SMTPSettingsValidationError';
  }
}

function contractError(): never {
  throw new SystemSettingsContractError();
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) contractError();
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: readonly string[]): void {
  const expected = new Set(allowed);
  if (Object.keys(value).some((key) => !expected.has(key))) contractError();
}

function safeText(value: unknown, maximumBytes: number, requireTrimmed = false): string {
  if (typeof value !== 'string' || encoder.encode(value).byteLength > maximumBytes
    || requireTrimmed && value.trim() !== value) contractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || code >= 0x7f && code <= 0x9f || code === 0x061c
      || code === 0x200e || code === 0x200f || code >= 0x202a && code <= 0x202e
      || code >= 0x2066 && code <= 0x2069) contractError();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc00 || next > 0xdfff) contractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      contractError();
    }
  }
  return value;
}

function flag(value: unknown): boolean {
  if (typeof value !== 'boolean') contractError();
  return value;
}

function canonicalPort(value: unknown): string {
  const port = safeText(value, 5, true);
  if (!/^[1-9]\d{0,4}$/u.test(port) || Number(port) > 65_535) contractError();
  return port;
}

function responseData(value: unknown): SMTPSettingsData {
  const data = record(value);
  exactKeys(data, [
    'SMTPServer', 'SMTPPort', 'SMTPAccount', 'SMTPFrom', 'SMTPToken', 'SMTPTokenRedacted',
    'SMTPSSLEnabled', 'SMTPStartTLSEnabled', 'SMTPInsecureSkipVerify', 'SMTPForceAuthLogin',
  ]);
  if (Object.keys(data).length !== 10 || data.SMTPToken !== '') contractError();
  const parsed = {
    server: safeText(data.SMTPServer, 253, true),
    port: canonicalPort(data.SMTPPort),
    account: safeText(data.SMTPAccount, 512, true),
    from: safeText(data.SMTPFrom, 320, true),
    tokenConfigured: flag(data.SMTPTokenRedacted),
    sslEnabled: flag(data.SMTPSSLEnabled),
    startTLSEnabled: flag(data.SMTPStartTLSEnabled),
    insecureSkipVerify: flag(data.SMTPInsecureSkipVerify),
    forceAuthLogin: flag(data.SMTPForceAuthLogin),
  };
  try {
    validateSMTPSettingsUpdate({
      ...parsed,
      currentTokenConfigured: parsed.tokenConfigured,
      clearToken: false,
      token: '',
    });
  } catch {
    contractError();
  }
  return parsed;
}

export function parseSMTPSettingsResponse(value: unknown): SMTPSettingsData {
  let bytes: number;
  try {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) contractError();
    bytes = encoder.encode(encoded).byteLength;
  } catch (error) {
    if (error instanceof SystemSettingsContractError) throw error;
    return contractError();
  }
  if (bytes > MAX_RESPONSE_BYTES) contractError();
  const envelope = record(value);
  exactKeys(envelope, ['success', 'message', 'data']);
  const message = envelope.message === undefined ? '' : safeText(envelope.message, MAX_MESSAGE_BYTES);
  if (envelope.success === false) throw new SystemSettingsRequestError(message || 'Request failed');
  if (envelope.success !== true || !Object.hasOwn(envelope, 'data')) contractError();
  return responseData(envelope.data);
}

function validHostname(value: string): boolean {
  if (value === '' || value.length > 253 || value.trim() !== value || value.endsWith('.')) return false;
  if (value.includes(':')) {
    try {
      const parsed = new URL(`http://[${value}]/`);
      return parsed.hostname.startsWith('[') && parsed.hostname.endsWith(']');
    } catch {
      return false;
    }
  }
  const parts = value.split('.');
  const ipv4 = parts.length === 4 && parts.every((part) => /^(?:0|[1-9]\d{0,2})$/u.test(part)
    && Number(part) <= 255);
  return ipv4 || parts.every((part) => part.length > 0 && part.length <= 63
    && /^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/u.test(part));
}

function loopbackHost(value: string): boolean {
  if (value.toLowerCase() === 'localhost') return true;
  const parts = value.split('.');
  if (parts.length === 4 && parts.every((part) => /^(?:0|[1-9]\d{0,2})$/u.test(part)
    && Number(part) <= 255)) return Number(parts[0]) === 127;
  if (!value.includes(':')) return false;
  try {
    return new URL(`http://[${value}]/`).hostname.toLowerCase() === '[::1]';
  } catch {
    return false;
  }
}

function bareMailbox(value: string): boolean {
  if (value === '' || value.trim() !== value || /[\s<>(),;:\\"]/u.test(value)
    || value.includes('[') || value.includes(']')) return false;
  const at = value.indexOf('@');
  return at > 0 && at === value.lastIndexOf('@') && at < value.length - 1;
}

function mutationText(value: unknown, maximumBytes: number, requireTrimmed: boolean): string {
  try {
    return safeText(value, maximumBytes, requireTrimmed);
  } catch {
    throw new SMTPSettingsValidationError('Enter valid SMTP settings.');
  }
}

export function validateSMTPSettingsUpdate(input: SMTPSettingsUpdate): void {
  const server = mutationText(input.server, 253, true);
  const account = mutationText(input.account, 512, true);
  const from = mutationText(input.from, 320, true);
  const token = mutationText(input.token ?? '', 4_096, false);
  let port: string;
  try {
    port = canonicalPort(input.port);
  } catch {
    throw new SMTPSettingsValidationError('Enter a port from 1 to 65535.');
  }
  const booleans = [
    input.currentTokenConfigured, input.clearToken, input.sslEnabled, input.startTLSEnabled,
    input.insecureSkipVerify, input.forceAuthLogin,
  ];
  if (booleans.some((value) => typeof value !== 'boolean') || !validHostname(server) && server !== '') {
    throw new SMTPSettingsValidationError('Enter valid SMTP settings.');
  }
  if (input.clearToken && token !== '') {
    throw new SMTPSettingsValidationError('Choose either a replacement password or clear the stored password.');
  }
  if (input.sslEnabled && input.startTLSEnabled) {
    throw new SMTPSettingsValidationError('Choose exactly one SMTP transport mode.');
  }
  const usesTLS = input.sslEnabled || input.startTLSEnabled || port === '465';
  if (input.insecureSkipVerify && !usesTLS) {
    throw new SMTPSettingsValidationError('Certificate verification can be disabled only for a TLS connection.');
  }
  if (server === '') return;
  const tokenConfigured = token !== '' || input.currentTokenConfigured && !input.clearToken;
  if ((account === '') !== !tokenConfigured) {
    throw new SMTPSettingsValidationError('SMTP account and password must be configured together.');
  }
  if (input.forceAuthLogin && account === '') {
    throw new SMTPSettingsValidationError('LOGIN authentication requires an SMTP account and password.');
  }
  if (!usesTLS && !loopbackHost(server)) {
    throw new SMTPSettingsValidationError('Remote SMTP servers require implicit TLS or STARTTLS.');
  }
  if (account !== '' && !usesTLS) {
    throw new SMTPSettingsValidationError('SMTP authentication requires implicit TLS or STARTTLS.');
  }
  if (!bareMailbox(from || account)) {
    throw new SMTPSettingsValidationError('Enter a bare sender email address.');
  }
}

export function updateSMTPSettings(input: SMTPSettingsUpdate, signal?: AbortSignal): Promise<SMTPSettingsData> {
  validateSMTPSettingsUpdate(input);
  const payload: Record<string, string | boolean> = {
    SMTPServer: input.server,
    SMTPPort: input.port,
    SMTPAccount: input.account,
    SMTPFrom: input.from,
    SMTPSSLEnabled: input.sslEnabled,
    SMTPStartTLSEnabled: input.startTLSEnabled,
    SMTPInsecureSkipVerify: input.insecureSkipVerify,
    SMTPForceAuthLogin: input.forceAuthLogin,
    clear_smtp_token: input.clearToken,
  };
  if (input.token) payload.SMTPToken = input.token;
  if (encoder.encode(JSON.stringify(payload)).byteLength > MAX_REQUEST_BYTES) {
    throw new SMTPSettingsValidationError('Enter valid SMTP settings.');
  }
  return serializeSystemSettingsMutation(async () => {
    const response = await api.put<unknown>('/option/smtp', payload, {
      signal,
      timeout: REQUEST_TIMEOUT_MS,
      maxContentLength: MAX_RESPONSE_BYTES,
      maxBodyLength: MAX_REQUEST_BYTES,
    });
    if (signal?.aborted) {
      const error = new Error('The operation was aborted');
      error.name = 'AbortError';
      throw error;
    }
    return parseSMTPSettingsResponse(response.data);
  }, signal);
}
