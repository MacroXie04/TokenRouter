// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  ProfileWebAuthnError,
  passkeyRegistrationSupported,
  preparePasskeyCreationOptions,
  serializePasskeyRegistrationCredential,
} from './profile-webauthn';

function bytes(...values: number[]): ArrayBuffer {
  return new Uint8Array(values).buffer;
}

function creationOptions(overrides: Record<string, unknown> = {}) {
  return {
    challenge: 'AAECAwQFBgcICQoLDA0ODw',
    rp: { id: 'router.example.test', name: 'TokenRouter' },
    user: {
      id: 'AQIDBA',
      name: 'profile-user',
      displayName: 'Profile User',
    },
    pubKeyCredParams: [
      { type: 'public-key', alg: -7 },
      { type: 'public-key', alg: -257 },
    ],
    timeout: 60_000,
    attestation: 'none',
    authenticatorSelection: {
      residentKey: 'preferred',
      userVerification: 'preferred',
    },
    excludeCredentials: [{ type: 'public-key', id: 'BQYH', transports: ['internal'] }],
    ...overrides,
  };
}

afterEach(() => vi.unstubAllGlobals());

describe('profile passkey registration boundary', () => {
  it('converts only a bounded, complete creation contract to browser buffers', () => {
    const source = creationOptions();
    const prepared = preparePasskeyCreationOptions(source);

    expect(Array.from(new Uint8Array(prepared.challenge))).toEqual([
      0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
    ]);
    expect(Array.from(new Uint8Array(prepared.user.id))).toEqual([1, 2, 3, 4]);
    expect(Array.from(new Uint8Array(prepared.excludeCredentials?.[0].id as ArrayBuffer))).toEqual([5, 6, 7]);
    expect(source.challenge).toBe('AAECAwQFBgcICQoLDA0ODw');
  });

  it('rejects underspecified, weak, duplicated, unsupported, and oversized options', () => {
    const invalid = [
      { ...creationOptions(), rp: undefined },
      creationOptions({ challenge: 'AQID' }),
      creationOptions({ pubKeyCredParams: [] }),
      creationOptions({ pubKeyCredParams: [{ type: 'public-key', alg: -7 }, { type: 'public-key', alg: -7 }] }),
      creationOptions({ attestation: 'unsafe' }),
      creationOptions({ authenticatorSelection: { userVerification: 'always' } }),
      creationOptions({ excludeCredentials: [{ type: 'public-key', id: 'AQID', transports: ['satellite'] }] }),
      creationOptions({ extensionPadding: 'x'.repeat(70_000) }),
    ];
    for (const value of invalid) {
      expect(() => preparePasskeyCreationOptions(value)).toThrow(ProfileWebAuthnError);
    }
  });

  it('serializes a bounded attestation without leaking browser object methods', () => {
    const credential = {
      id: 'credential-id',
      rawId: bytes(1, 2, 3),
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response: {
        clientDataJSON: bytes(4, 5, 6),
        attestationObject: bytes(7, 8, 9),
        getTransports: () => ['internal', 'hybrid'],
      },
      getClientExtensionResults: () => ({ credProps: { rk: true } }),
    };

    expect(serializePasskeyRegistrationCredential(credential)).toEqual({
      id: 'credential-id',
      rawId: 'AQID',
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response: {
        clientDataJSON: 'BAUG',
        attestationObject: 'BwgJ',
        transports: ['internal', 'hybrid'],
      },
      clientExtensionResults: { credProps: { rk: true } },
    });
  });

  it('rejects malformed credentials and oversized or unsupported attestation fields', () => {
    const base = {
      id: 'credential-id',
      rawId: bytes(1),
      type: 'public-key',
      response: {
        clientDataJSON: bytes(2),
        attestationObject: bytes(3),
        getTransports: () => ['internal'],
      },
      getClientExtensionResults: () => ({}),
    };
    const invalid = [
      { ...base, type: 'password' },
      { ...base, rawId: new ArrayBuffer(0) },
      { ...base, response: { ...base.response, attestationObject: new ArrayBuffer(70_000) } },
      { ...base, response: { ...base.response, getTransports: () => ['telepathy'] } },
      { ...base, response: { ...base.response, getTransports: () => ['internal', 'internal'] } },
      { ...base, authenticatorAttachment: 'remote' },
      { ...base, getClientExtensionResults: () => ({ padding: 'x'.repeat(20_000) }) },
    ];
    for (const value of invalid) {
      expect(() => serializePasskeyRegistrationCredential(value)).toThrow(ProfileWebAuthnError);
    }
  });

  it('reports support only when both the platform type and creation method exist', () => {
    vi.stubGlobal('PublicKeyCredential', class PublicKeyCredential {});
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { create: vi.fn() },
    });
    expect(passkeyRegistrationSupported()).toBe(true);

    vi.stubGlobal('PublicKeyCredential', undefined);
    expect(passkeyRegistrationSupported()).toBe(false);
  });
});
