import { api, withoutSessionRefresh } from '../../api';

const MAX_AUTH_RESPONSE_BYTES = 576 * 1024;
const MAX_AUTH_REQUEST_BYTES = 128 * 1024;

type AuthGetPath =
  | '/oauth/wechat'
  | '/reset_password'
  | '/verification';

type AuthPostPath =
  | '/oauth/state'
  | '/user/login'
  | '/user/login/2fa'
  | '/user/passkey/login/begin'
  | '/user/passkey/login/finish'
  | '/user/register'
  | '/user/reset';

type QueryValue = string | number | boolean;
type Query = Record<string, QueryValue | undefined>;

interface ResponseEnvelope {
  success: boolean;
  data?: unknown;
}

export class AuthResponseContractError extends Error {
  constructor() {
    super('Invalid authentication response');
    this.name = 'AuthResponseContractError';
  }
}

export class AuthRequestFailedError extends Error {
  constructor() {
    super('Authentication request failed');
    this.name = 'AuthRequestFailedError';
  }
}

function jsonSize(value: unknown): number {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new AuthResponseContractError();
  }
  if (encoded === undefined) throw new AuthResponseContractError();
  return new TextEncoder().encode(encoded).byteLength;
}

function parseEnvelope(value: unknown): ResponseEnvelope {
  if (jsonSize(value) > MAX_AUTH_RESPONSE_BYTES
    || !value
    || typeof value !== 'object'
    || Array.isArray(value)) {
    throw new AuthResponseContractError();
  }
  const envelope = value as Partial<ResponseEnvelope>;
  if (typeof envelope.success !== 'boolean') throw new AuthResponseContractError();
  if (!envelope.success) throw new AuthRequestFailedError();
  return envelope as ResponseEnvelope;
}

function validateRequest(value: unknown): void {
  if (value !== undefined && jsonSize(value) > MAX_AUTH_REQUEST_BYTES) {
    throw new AuthResponseContractError();
  }
}

export async function authGet(
  path: AuthGetPath,
  query: Query = {},
  signal?: AbortSignal,
): Promise<unknown> {
  validateRequest(query);
  const response = await api.get<unknown>(path, withoutSessionRefresh({ params: query, signal }));
  return parseEnvelope(response.data).data;
}

export async function authPost(
  path: AuthPostPath,
  body?: unknown,
  query?: Query,
  signal?: AbortSignal,
): Promise<unknown> {
  validateRequest(body);
  validateRequest(query);
  const response = await api.post<unknown>(path, body, withoutSessionRefresh(
    query === undefined && signal === undefined ? {} : { params: query, signal },
  ));
  return parseEnvelope(response.data).data;
}

export function isCanceledAuthRequest(error: unknown, signal?: AbortSignal): boolean {
  if (signal?.aborted) return true;
  if (!error || typeof error !== 'object') return false;
  const candidate = error as { name?: unknown; code?: unknown };
  return candidate.name === 'AbortError'
    || candidate.name === 'CanceledError'
    || candidate.code === 'ERR_CANCELED';
}
