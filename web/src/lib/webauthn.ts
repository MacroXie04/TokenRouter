// WebAuthn client helpers for passkey registration/login. These bridge the
// base64url-encoded values from the server to the ArrayBuffers required by the
// browser API, and serialize browser credentials back to bounded JSON.

const MAX_ASSERTION_OPTIONS_JSON_BYTES = 64 * 1024;
const MAX_ASSERTION_JSON_BYTES = 64 * 1024;
const MAX_CHALLENGE_BYTES = 1024;
const MAX_CREDENTIAL_ID_BYTES = 4096;
const MAX_ASSERTION_FIELD_BYTES = 16 * 1024;
const MAX_USER_HANDLE_BYTES = 1024;
const MAX_EXTENSION_JSON_BYTES = 16 * 1024;
const MAX_ALLOWED_CREDENTIALS = 64;
const MAX_WEBAUTHN_TIMEOUT_MS = 10 * 60 * 1000;

export const MAX_PASSKEY_FLOW_TOKEN_CHARACTERS = 256;

type UnknownRecord = Record<string, unknown>;

export interface PasskeyLoginBegin {
  flowToken: string;
  requestOptions: CredentialRequestOptions;
}

export interface PreparedCreationOptions extends UnknownRecord {
  challenge: ArrayBuffer;
  user: UnknownRecord & { id: ArrayBuffer };
  excludeCredentials?: Array<UnknownRecord & { id: ArrayBuffer }>;
}

function isRecord(value: unknown): value is UnknownRecord {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function jsonCloneWithinLimit(value: unknown, maxBytes: number): unknown {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new Error('Invalid WebAuthn JSON.');
  }
  if (typeof encoded !== 'string' || new TextEncoder().encode(encoded).byteLength > maxBytes) {
    throw new Error('WebAuthn JSON is too large.');
  }
  return JSON.parse(encoded) as unknown;
}

function requireString(value: unknown, maxCharacters: number): string {
  if (
    typeof value !== 'string'
    || value.length === 0
    || value.length > maxCharacters
    || Array.from(value).some((character) => {
      const codePoint = character.codePointAt(0) ?? 0;
      return codePoint < 32 || codePoint === 127;
    })
  ) {
    throw new Error('Invalid WebAuthn string.');
  }
  return value;
}

function requireBase64Url(value: unknown, maxBytes: number, allowEmpty = false): ArrayBuffer {
  if (typeof value !== 'string' || (!allowEmpty && value.length === 0)) {
    throw new Error('Invalid WebAuthn binary value.');
  }
  if (
    value.length > Math.ceil(maxBytes * 4 / 3) + 2
    || !/^[A-Za-z0-9_-]*={0,2}$/.test(value)
    || value.length % 4 === 1
  ) {
    throw new Error('Invalid WebAuthn binary value.');
  }
  const result = decodeBase64Url(value);
  if ((!allowEmpty && result.byteLength === 0) || result.byteLength > maxBytes) {
    throw new Error('Invalid WebAuthn binary value.');
  }
  return result;
}

function decodeBase64Url(value: string): ArrayBuffer {
  let padded = value.replace(/-/g, '+').replace(/_/g, '/');
  while (padded.length % 4) padded += '=';
  let binary: string;
  try {
    binary = atob(padded);
  } catch {
    throw new Error('Invalid WebAuthn binary value.');
  }
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}

function requireArrayBuffer(value: unknown, maxBytes: number): ArrayBuffer {
  if (!(value instanceof ArrayBuffer) || value.byteLength === 0 || value.byteLength > maxBytes) {
    throw new Error('Invalid WebAuthn credential.');
  }
  return value;
}

export function bufferToBase64Url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let bin = '';
  for (let i = 0; i < bytes.length; i += 1) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function base64UrlToBuffer(value: string): ArrayBuffer {
  return requireBase64Url(value, MAX_ASSERTION_FIELD_BYTES, true);
}

// prepareCreationOptions converts server-sent creation options for
// navigator.credentials.create. Registration currently uses the same server
// contract, so retain this small compatibility helper alongside the stricter
// assertion parser below.
export function prepareCreationOptions(publicKey: unknown): PreparedCreationOptions {
  if (!isRecord(publicKey) || !isRecord(publicKey.user)) {
    throw new Error('Invalid WebAuthn creation options.');
  }
  const prepared: UnknownRecord = {
    ...publicKey,
    challenge: requireBase64Url(publicKey.challenge, MAX_CHALLENGE_BYTES),
    user: {
      ...publicKey.user,
      id: requireBase64Url(publicKey.user.id, MAX_CREDENTIAL_ID_BYTES),
    },
  };
  if (publicKey.excludeCredentials !== undefined) {
    if (!Array.isArray(publicKey.excludeCredentials) || publicKey.excludeCredentials.length > MAX_ALLOWED_CREDENTIALS) {
      throw new Error('Invalid WebAuthn creation options.');
    }
    prepared.excludeCredentials = publicKey.excludeCredentials.map((entry) => {
      if (!isRecord(entry)) throw new Error('Invalid WebAuthn creation options.');
      return { ...entry, id: requireBase64Url(entry.id, MAX_CREDENTIAL_ID_BYTES) };
    });
  }
  return prepared as PreparedCreationOptions;
}

function prepareRequestOptions(value: unknown): CredentialRequestOptions {
  if (!isRecord(value)) throw new Error('Invalid WebAuthn request options.');
  jsonCloneWithinLimit(value, MAX_ASSERTION_OPTIONS_JSON_BYTES);

  const publicKeyValue = value.publicKey;
  if (!isRecord(publicKeyValue)) throw new Error('Invalid WebAuthn request options.');

  const challenge = requireBase64Url(publicKeyValue.challenge, MAX_CHALLENGE_BYTES);
  if (challenge.byteLength < 16) throw new Error('Invalid WebAuthn challenge.');
  const publicKey: UnknownRecord = { challenge };

  if (publicKeyValue.timeout !== undefined) {
    if (
      typeof publicKeyValue.timeout !== 'number'
      || !Number.isInteger(publicKeyValue.timeout)
      || publicKeyValue.timeout <= 0
      || publicKeyValue.timeout > MAX_WEBAUTHN_TIMEOUT_MS
    ) {
      throw new Error('Invalid WebAuthn timeout.');
    }
    publicKey.timeout = publicKeyValue.timeout;
  }

  if (publicKeyValue.rpId !== undefined) {
    const rpId = requireString(publicKeyValue.rpId, 253);
    if (/\s/.test(rpId)) throw new Error('Invalid WebAuthn relying party.');
    publicKey.rpId = rpId;
  }

  if (publicKeyValue.userVerification !== undefined) {
    if (!['required', 'preferred', 'discouraged'].includes(String(publicKeyValue.userVerification))) {
      throw new Error('Invalid WebAuthn user verification preference.');
    }
    publicKey.userVerification = publicKeyValue.userVerification;
  }

  if (publicKeyValue.allowCredentials !== undefined) {
    if (
      !Array.isArray(publicKeyValue.allowCredentials)
      || publicKeyValue.allowCredentials.length > MAX_ALLOWED_CREDENTIALS
    ) {
      throw new Error('Invalid WebAuthn credential list.');
    }
    publicKey.allowCredentials = publicKeyValue.allowCredentials.map((entry) => {
      if (!isRecord(entry) || entry.type !== 'public-key') {
        throw new Error('Invalid WebAuthn credential descriptor.');
      }
      const descriptor: UnknownRecord = {
        type: 'public-key',
        id: requireBase64Url(entry.id, MAX_CREDENTIAL_ID_BYTES),
      };
      if (entry.transports !== undefined) {
        if (!Array.isArray(entry.transports) || entry.transports.length > 8) {
          throw new Error('Invalid WebAuthn transports.');
        }
        const transports = entry.transports.map((transport) => requireString(transport, 32));
        if (transports.some((transport) => !['usb', 'nfc', 'ble', 'internal', 'hybrid', 'smart-card'].includes(transport))) {
          throw new Error('Invalid WebAuthn transport.');
        }
        descriptor.transports = transports;
      }
      return descriptor;
    });
  }

  if (publicKeyValue.hints !== undefined) {
    if (!Array.isArray(publicKeyValue.hints) || publicKeyValue.hints.length > 8) {
      throw new Error('Invalid WebAuthn hints.');
    }
    const hints = publicKeyValue.hints.map((hint) => requireString(hint, 64));
    if (hints.some((hint) => !['security-key', 'client-device', 'hybrid'].includes(hint))) {
      throw new Error('Invalid WebAuthn hint.');
    }
    publicKey.hints = hints;
  }

  if (publicKeyValue.extensions !== undefined) {
    if (!isRecord(publicKeyValue.extensions)) throw new Error('Invalid WebAuthn extensions.');
    publicKey.extensions = jsonCloneWithinLimit(publicKeyValue.extensions, MAX_EXTENSION_JSON_BYTES);
  }

  const result: UnknownRecord = { publicKey };
  if (value.mediation !== undefined) {
    if (!['silent', 'optional', 'required', 'conditional'].includes(String(value.mediation))) {
      throw new Error('Invalid WebAuthn mediation.');
    }
    result.mediation = value.mediation;
  }
  return result as unknown as CredentialRequestOptions;
}

export function parsePasskeyLoginBegin(payload: unknown): PasskeyLoginBegin {
  if (!isRecord(payload) || !isRecord(payload.options)) {
    throw new Error('Invalid passkey login response.');
  }
  const flowToken = requireString(payload.flow_token, MAX_PASSKEY_FLOW_TOKEN_CHARACTERS);
  return {
    flowToken,
    requestOptions: prepareRequestOptions(payload.options),
  };
}

export function isPasskeyLoginSupported(): boolean {
  if (typeof globalThis.PublicKeyCredential !== 'function') return false;
  if (typeof globalThis.navigator === 'undefined') return false;
  return typeof globalThis.navigator.credentials?.get === 'function';
}

// serializeCredential converts a PublicKeyCredential to the server's JSON
// shape. It remains shared by passkey registration; login uses the stricter
// assertion-only serializer below.
export function serializeCredential(value: unknown): UnknownRecord {
  if (!isRecord(value) || !isRecord(value.response)) throw new Error('Invalid WebAuthn credential.');
  const getTransports = value.response.getTransports;
  const getExtensions = value.getClientExtensionResults;
  return {
    id: value.id,
    rawId: bufferToBase64Url(value.rawId as ArrayBuffer),
    type: value.type,
    response: {
      clientDataJSON: bufferToBase64Url(value.response.clientDataJSON as ArrayBuffer),
      attestationObject: value.response.attestationObject
        ? bufferToBase64Url(value.response.attestationObject as ArrayBuffer)
        : undefined,
      authenticatorData: value.response.authenticatorData
        ? bufferToBase64Url(value.response.authenticatorData as ArrayBuffer)
        : undefined,
      signature: value.response.signature
        ? bufferToBase64Url(value.response.signature as ArrayBuffer)
        : undefined,
      transports: typeof getTransports === 'function' ? getTransports.call(value.response) : undefined,
    },
    clientExtensionResults: typeof getExtensions === 'function' ? getExtensions.call(value) : {},
  };
}

export function serializeAssertionCredential(value: unknown): UnknownRecord {
  if (!isRecord(value) || !isRecord(value.response)) throw new Error('Invalid WebAuthn assertion.');
  const id = requireString(value.id, 8192);
  if (value.type !== 'public-key') throw new Error('Invalid WebAuthn assertion.');

  const response = value.response;
  const rawId = requireArrayBuffer(value.rawId, MAX_CREDENTIAL_ID_BYTES);
  const clientDataJSON = requireArrayBuffer(response.clientDataJSON, MAX_ASSERTION_FIELD_BYTES);
  const authenticatorData = requireArrayBuffer(response.authenticatorData, MAX_ASSERTION_FIELD_BYTES);
  const signature = requireArrayBuffer(response.signature, MAX_ASSERTION_FIELD_BYTES);
  let userHandle: string | null = null;
  if (response.userHandle !== null && response.userHandle !== undefined) {
    userHandle = bufferToBase64Url(requireArrayBuffer(response.userHandle, MAX_USER_HANDLE_BYTES));
  }

  const getExtensions = value.getClientExtensionResults;
  const extensionValue = typeof getExtensions === 'function' ? getExtensions.call(value) : {};
  if (!isRecord(extensionValue)) throw new Error('Invalid WebAuthn extension results.');
  const clientExtensionResults = jsonCloneWithinLimit(extensionValue, MAX_EXTENSION_JSON_BYTES);

  const serialized: UnknownRecord = {
    id,
    rawId: bufferToBase64Url(rawId),
    type: 'public-key',
    response: {
      clientDataJSON: bufferToBase64Url(clientDataJSON),
      authenticatorData: bufferToBase64Url(authenticatorData),
      signature: bufferToBase64Url(signature),
      userHandle,
    },
    clientExtensionResults,
  };

  if (value.authenticatorAttachment !== null && value.authenticatorAttachment !== undefined) {
    if (!['platform', 'cross-platform'].includes(String(value.authenticatorAttachment))) {
      throw new Error('Invalid WebAuthn authenticator attachment.');
    }
    serialized.authenticatorAttachment = value.authenticatorAttachment;
  }

  return jsonCloneWithinLimit(serialized, MAX_ASSERTION_JSON_BYTES) as UnknownRecord;
}
