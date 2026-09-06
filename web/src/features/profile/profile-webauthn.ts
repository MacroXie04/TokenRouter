import { bufferToBase64Url, prepareCreationOptions, type PreparedCreationOptions } from '../../lib/webauthn';

const MAX_OPTIONS_BYTES = 64 * 1024;
const MAX_RESULT_BYTES = 128 * 1024;
const MAX_CREDENTIAL_ID_BYTES = 4 * 1024;
const MAX_RESPONSE_FIELD_BYTES = 64 * 1024;
const MAX_EXTENSION_BYTES = 16 * 1024;
const MAX_TIMEOUT_MS = 10 * 60 * 1_000;
const ALLOWED_TRANSPORTS = ['usb', 'nfc', 'ble', 'internal', 'hybrid', 'smart-card'];

type UnknownRecord = Record<string, unknown>;

export class ProfileWebAuthnError extends Error {
  constructor() {
    super('Invalid passkey registration data');
    this.name = 'ProfileWebAuthnError';
  }
}

function fail(): never {
  throw new ProfileWebAuthnError();
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) fail();
  return value as UnknownRecord;
}

function boundedJSON(value: unknown, maximum: number): unknown {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    fail();
  }
  if (typeof encoded !== 'string' || new TextEncoder().encode(encoded).byteLength > maximum) fail();
  return JSON.parse(encoded) as unknown;
}

function text(value: unknown, maximum: number, allowEmpty = false): string {
  if (typeof value !== 'string' || value.length > maximum || (!allowEmpty && value.length === 0)) fail();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) fail();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) fail();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      fail();
    }
  }
  return value;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) fail();
  return value as number;
}

function arrayBuffer(value: unknown, maximum: number): ArrayBuffer {
  if (!(value instanceof ArrayBuffer) || value.byteLength === 0 || value.byteLength > maximum) fail();
  return value;
}

function validateCreationOptions(value: unknown): UnknownRecord {
  boundedJSON(value, MAX_OPTIONS_BYTES);
  const source = record(value);
  const relyingParty = record(source.rp);
  text(relyingParty.name, 128);
  if (relyingParty.id !== undefined) {
    const id = text(relyingParty.id, 253);
    if (/\s|:\/\//.test(id)) fail();
  }

  const user = record(source.user);
  text(user.name, 128);
  text(user.displayName, 128);

  if (!Array.isArray(source.pubKeyCredParams)
    || source.pubKeyCredParams.length === 0
    || source.pubKeyCredParams.length > 64) fail();
  const algorithms = new Set<number>();
  for (const raw of source.pubKeyCredParams) {
    const parameter = record(raw);
    if (parameter.type !== 'public-key') fail();
    const algorithm = integer(parameter.alg, -65_536, 65_536);
    if (algorithms.has(algorithm)) fail();
    algorithms.add(algorithm);
  }

  if (source.timeout !== undefined) integer(source.timeout, 1, MAX_TIMEOUT_MS);
  if (source.attestation !== undefined
    && !['none', 'indirect', 'direct', 'enterprise'].includes(String(source.attestation))) fail();

  if (source.authenticatorSelection !== undefined) {
    const selection = record(source.authenticatorSelection);
    if (selection.authenticatorAttachment !== undefined
      && !['platform', 'cross-platform'].includes(String(selection.authenticatorAttachment))) fail();
    if (selection.residentKey !== undefined
      && !['discouraged', 'preferred', 'required'].includes(String(selection.residentKey))) fail();
    if (selection.requireResidentKey !== undefined && typeof selection.requireResidentKey !== 'boolean') fail();
    if (selection.userVerification !== undefined
      && !['required', 'preferred', 'discouraged'].includes(String(selection.userVerification))) fail();
  }

  if (source.excludeCredentials !== undefined) {
    if (!Array.isArray(source.excludeCredentials) || source.excludeCredentials.length > 64) fail();
    for (const raw of source.excludeCredentials) {
      const descriptor = record(raw);
      if (descriptor.type !== 'public-key') fail();
      if (descriptor.transports !== undefined) {
        if (!Array.isArray(descriptor.transports) || descriptor.transports.length > 8) fail();
        for (const transport of descriptor.transports) {
          if (!ALLOWED_TRANSPORTS.includes(text(transport, 32))) fail();
        }
      }
    }
  }

  if (source.extensions !== undefined) record(source.extensions);
  return source;
}

export function preparePasskeyCreationOptions(value: unknown): PreparedCreationOptions {
  const validated = validateCreationOptions(value);
  let prepared: PreparedCreationOptions;
  try {
    prepared = prepareCreationOptions(validated);
  } catch {
    fail();
  }
  if (prepared.challenge.byteLength < 16 || prepared.challenge.byteLength > 1_024
    || prepared.user.id.byteLength === 0 || prepared.user.id.byteLength > MAX_CREDENTIAL_ID_BYTES) fail();
  return prepared;
}

export function passkeyRegistrationSupported(): boolean {
  return typeof globalThis.PublicKeyCredential === 'function'
    && typeof globalThis.navigator !== 'undefined'
    && typeof globalThis.navigator.credentials?.create === 'function';
}

export function serializePasskeyRegistrationCredential(value: unknown): Record<string, unknown> {
  const credential = record(value);
  const response = record(credential.response);
  const id = text(credential.id, 8_192);
  if (credential.type !== 'public-key') fail();
  const rawId = arrayBuffer(credential.rawId, MAX_CREDENTIAL_ID_BYTES);
  const clientDataJSON = arrayBuffer(response.clientDataJSON, MAX_RESPONSE_FIELD_BYTES);
  const attestationObject = arrayBuffer(response.attestationObject, MAX_RESPONSE_FIELD_BYTES);

  let transports: string[] | undefined;
  if (response.getTransports !== undefined) {
    if (typeof response.getTransports !== 'function') fail();
    const rawTransports = response.getTransports.call(response) as unknown;
    if (!Array.isArray(rawTransports) || rawTransports.length > 8) fail();
    transports = rawTransports.map((transport) => text(transport, 32));
    if (transports.some((transport) => !ALLOWED_TRANSPORTS.includes(transport))
      || new Set(transports).size !== transports.length) fail();
  }

  let clientExtensionResults: unknown = {};
  if (credential.getClientExtensionResults !== undefined) {
    if (typeof credential.getClientExtensionResults !== 'function') fail();
    clientExtensionResults = credential.getClientExtensionResults.call(credential);
  }
  record(clientExtensionResults);
  clientExtensionResults = boundedJSON(clientExtensionResults, MAX_EXTENSION_BYTES);

  const result: UnknownRecord = {
    id,
    rawId: bufferToBase64Url(rawId),
    type: 'public-key',
    response: {
      clientDataJSON: bufferToBase64Url(clientDataJSON),
      attestationObject: bufferToBase64Url(attestationObject),
      ...(transports ? { transports } : {}),
    },
    clientExtensionResults,
  };
  if (credential.authenticatorAttachment !== undefined && credential.authenticatorAttachment !== null) {
    if (!['platform', 'cross-platform'].includes(String(credential.authenticatorAttachment))) fail();
    result.authenticatorAttachment = credential.authenticatorAttachment;
  }
  return boundedJSON(result, MAX_RESULT_BYTES) as Record<string, unknown>;
}
