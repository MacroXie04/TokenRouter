import { api } from '../../api';
import {
  MAX_PASSKEY_FLOW_TOKEN_CHARACTERS,
  parsePasskeyLoginBegin,
  type PasskeyLoginBegin,
} from '../../lib/webauthn';

const CHANNEL_KEY_SCOPE = 'channel.key.read';
const MAX_RESPONSE_BYTES = 1024 * 1024;
const MAX_CHANNEL_KEY_BYTES = 512 * 1024;
const MAX_PROOF_CHARACTERS = 8_192;
const MAX_ASSERTION_BYTES = 128 * 1024;
const MAX_UNIX_SECONDS = 8_640_000_000;

type UnknownRecord = Record<string, unknown>;
type ProofMethod = '2fa' | 'passkey';

export class ChannelKeyContractError extends Error {
  constructor() {
    super('Invalid channel key API response');
    this.name = 'ChannelKeyContractError';
  }
}

function contractError(): never {
  throw new ChannelKeyContractError();
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) contractError();
  return value as UnknownRecord;
}

function payloadBytes(value: unknown): number {
  let serialized: string;
  try {
    serialized = JSON.stringify(value);
  } catch {
    contractError();
  }
  if (typeof serialized !== 'string') contractError();
  return new TextEncoder().encode(serialized).byteLength;
}

function successfulData(value: unknown): unknown {
  if (payloadBytes(value) > MAX_RESPONSE_BYTES) contractError();
  const envelope = record(value);
  if (envelope.success !== true || !Object.prototype.hasOwnProperty.call(envelope, 'data')) contractError();
  return envelope.data;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) contractError();
  return value as number;
}

function proofToken(value: unknown): string {
  if (
    typeof value !== 'string'
    || value.length === 0
    || value.length > MAX_PROOF_CHARACTERS
    || !/^[A-Za-z0-9._-]+$/.test(value)
  ) contractError();
  return value;
}

function flowToken(value: string): string {
  if (
    typeof value !== 'string'
    || value.length === 0
    || value.length > MAX_PASSKEY_FLOW_TOKEN_CHARACTERS
    || !/^[A-Za-z0-9_-]+$/.test(value)
  ) contractError();
  return value;
}

function verificationCode(value: string): string {
  const code = value.trim();
  if (!/^\d{6}$/.test(code)) contractError();
  return code;
}

function parseProofResponse(value: unknown, expectedMethod: ProofMethod): string {
  const data = record(successfulData(value));
  if (data.method !== expectedMethod || data.scope !== CHANNEL_KEY_SCOPE) contractError();
  integer(data.expires_at, 1, MAX_UNIX_SECONDS);
  return proofToken(data.proof_token);
}

function safeChannelKey(value: unknown): string {
  if (typeof value !== 'string' || value.trim().length === 0) contractError();
  if (new TextEncoder().encode(value).byteLength > MAX_CHANNEL_KEY_BYTES) contractError();
  for (const character of value) {
    const codePoint = character.codePointAt(0) ?? 0;
    if (character === '\n' || character === '\r' || character === '\t') continue;
    if (
      codePoint < 0x20
      || (codePoint >= 0x7f && codePoint <= 0x9f)
      || codePoint === 0x061c
      || codePoint === 0x200e
      || codePoint === 0x200f
      || (codePoint >= 0x202a && codePoint <= 0x202e)
      || (codePoint >= 0x2066 && codePoint <= 0x2069)
    ) contractError();
  }
  return value;
}

function requestConfig(signal?: AbortSignal, securityProof?: string) {
  return {
    signal,
    maxContentLength: MAX_RESPONSE_BYTES,
    maxBodyLength: MAX_RESPONSE_BYTES,
    headers: {
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
      ...(securityProof ? { 'X-Security-Proof': proofToken(securityProof) } : {}),
    },
  };
}

export function parseChannelKeyRevealResponse(value: unknown): string {
  const data = record(successfulData(value));
  return safeChannelKey(data.key);
}

export async function verifyChannelKeyTwoFactor(code: string, signal?: AbortSignal): Promise<string> {
  const response = await api.post<unknown>('/verify', {
    method: '2fa',
    code: verificationCode(code),
    scope: CHANNEL_KEY_SCOPE,
  }, requestConfig(signal));
  return parseProofResponse(response.data, '2fa');
}

export async function beginChannelKeyPasskey(signal?: AbortSignal): Promise<PasskeyLoginBegin> {
  const response = await api.post<unknown>(
    '/user/passkey/verify/begin',
    { scope: CHANNEL_KEY_SCOPE },
    requestConfig(signal),
  );
  const data = successfulData(response.data);
  try {
    return parsePasskeyLoginBegin(data);
  } catch {
    contractError();
  }
}

export async function finishChannelKeyPasskey(
  token: string,
  credential: Record<string, unknown>,
  signal?: AbortSignal,
): Promise<string> {
  const assertion = record(credential);
  if (payloadBytes(assertion) > MAX_ASSERTION_BYTES) contractError();
  const response = await api.post<unknown>('/user/passkey/verify/finish', {
    ...assertion,
    flow_token: flowToken(token),
  }, requestConfig(signal));
  return parseProofResponse(response.data, 'passkey');
}

export async function revealChannelKey(
  channelId: number,
  securityProof: string,
  signal?: AbortSignal,
): Promise<string> {
  if (!Number.isSafeInteger(channelId) || channelId <= 0 || channelId > 2_147_483_647) contractError();
  const response = await api.post<unknown>(
    `/channel/${channelId}/key`,
    {},
    requestConfig(signal, securityProof),
  );
  return parseChannelKeyRevealResponse(response.data);
}
