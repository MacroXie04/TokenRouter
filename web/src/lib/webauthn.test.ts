import { describe, expect, it } from 'vitest';

import {
  base64UrlToBuffer,
  bufferToBase64Url,
  MAX_PASSKEY_FLOW_TOKEN_CHARACTERS,
  parsePasskeyLoginBegin,
  prepareCreationOptions,
  serializeAssertionCredential,
  serializeCredential,
} from './webauthn';

function bytes(...values: number[]): ArrayBuffer {
  return Uint8Array.from(values).buffer;
}

function values(buffer: ArrayBuffer): number[] {
  return Array.from(new Uint8Array(buffer));
}

describe('WebAuthn base64url conversion', () => {
  it('encodes binary data without standard-base64 punctuation or padding', () => {
    expect(bufferToBase64Url(bytes(0xfb, 0xff, 0xef))).toBe('-__v');
  });

  it('round-trips empty and arbitrary binary values', () => {
    for (const input of [[], [0], [0, 1, 2, 127, 128, 254, 255]]) {
      const original = bytes(...input);
      expect(values(base64UrlToBuffer(bufferToBase64Url(original)))).toEqual(input);
    }
  });

  it('accepts unpadded base64url input', () => {
    expect(values(base64UrlToBuffer('AAECA_7_'))).toEqual([0, 1, 2, 3, 254, 255]);
  });
});

describe('prepareCreationOptions', () => {
  it('converts binary WebAuthn fields while preserving the server payload', () => {
    const source = {
      challenge: 'AAECAw',
      rp: { name: 'TokenRouter' },
      user: { id: '__4', name: 'alice', displayName: 'Alice' },
      excludeCredentials: [
        { type: 'public-key', id: 'AQI', transports: ['internal'] },
      ],
    };

    const prepared = prepareCreationOptions(source);
    const excluded = prepared.excludeCredentials;

    expect(values(prepared.challenge)).toEqual([0, 1, 2, 3]);
    expect(values(prepared.user.id)).toEqual([255, 254]);
    expect(excluded).toHaveLength(1);
    expect(values(excluded?.[0].id as ArrayBuffer)).toEqual([1, 2]);
    expect(prepared.rp).toEqual(source.rp);
    expect(excluded?.[0].transports).toEqual(['internal']);
    expect(source.challenge).toBe('AAECAw');
    expect(source.user.id).toBe('__4');
    expect(source.excludeCredentials[0].id).toBe('AQI');
  });
});

describe('serializeCredential', () => {
  it('serializes registration fields, transports, and extension results', () => {
    const credential = {
      id: 'credential-id',
      rawId: bytes(0, 255),
      type: 'public-key',
      response: {
        clientDataJSON: bytes(1, 2, 3),
        attestationObject: bytes(4, 5),
        getTransports: () => ['usb', 'internal'],
      },
      getClientExtensionResults: () => ({ credProps: { rk: true } }),
    };

    expect(serializeCredential(credential)).toEqual({
      id: 'credential-id',
      rawId: 'AP8',
      type: 'public-key',
      response: {
        clientDataJSON: 'AQID',
        attestationObject: 'BAU',
        authenticatorData: undefined,
        signature: undefined,
        transports: ['usb', 'internal'],
      },
      clientExtensionResults: { credProps: { rk: true } },
    });
  });

  it('serializes assertion fields and tolerates optional browser methods', () => {
    const credential = {
      id: 'assertion-id',
      rawId: bytes(9),
      type: 'public-key',
      response: {
        clientDataJSON: bytes(10),
        authenticatorData: bytes(11),
        signature: bytes(12),
      },
    };

    const serialized = serializeCredential(credential);

    expect(serialized.response).toMatchObject({
      clientDataJSON: 'Cg',
      authenticatorData: 'Cw',
      signature: 'DA',
      transports: undefined,
    });
    expect(serialized.clientExtensionResults).toEqual({});
  });
});

describe('passkey login parsing', () => {
  it('validates the begin payload and decodes assertion options for the browser', () => {
    const begin = parsePasskeyLoginBegin({
      flow_token: 'f'.repeat(64),
      options: {
        mediation: 'optional',
        publicKey: {
          challenge: 'AAECAwQFBgcICQoLDA0ODw',
          timeout: 60_000,
          rpId: 'example.test',
          userVerification: 'preferred',
          allowCredentials: [{
            id: 'AQID',
            type: 'public-key',
            transports: ['internal', 'hybrid'],
          }],
          hints: ['client-device'],
          extensions: { appid: 'https://example.test' },
        },
      },
    });

    const publicKey = begin.requestOptions.publicKey;
    expect(begin.flowToken).toBe('f'.repeat(64));
    expect(begin.requestOptions.mediation).toBe('optional');
    expect(values(publicKey?.challenge as ArrayBuffer)).toEqual([
      0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
    ]);
    expect(values(publicKey?.allowCredentials?.[0].id as ArrayBuffer)).toEqual([1, 2, 3]);
    expect(publicKey?.allowCredentials?.[0].transports).toEqual(['internal', 'hybrid']);
    expect(publicKey?.rpId).toBe('example.test');
    expect(publicKey?.extensions).toEqual({ appid: 'https://example.test' });
  });

  it('rejects malformed, short, and oversized server-controlled values', () => {
    const options = { publicKey: { challenge: 'AAECAwQFBgcICQoLDA0ODw' } };

    expect(() => parsePasskeyLoginBegin({
      flow_token: 'x'.repeat(MAX_PASSKEY_FLOW_TOKEN_CHARACTERS + 1),
      options,
    })).toThrow();
    expect(() => parsePasskeyLoginBegin({
      flow_token: 'flow',
      options: { publicKey: { challenge: 'AQID' } },
    })).toThrow();
    expect(() => parsePasskeyLoginBegin({
      flow_token: 'flow',
      options: {
        publicKey: {
          challenge: 'AAECAwQFBgcICQoLDA0ODw',
          allowCredentials: Array.from({ length: 65 }, () => ({ id: 'AQID', type: 'public-key' })),
        },
      },
    })).toThrow();
    expect(() => parsePasskeyLoginBegin({
      flow_token: 'flow',
      options: { publicKey: { challenge: 'AAECAwQFBgcICQoLDA0ODw', extensions: { blob: 'x'.repeat(70_000) } } },
    })).toThrow();
  });
});

describe('serializeAssertionCredential', () => {
  it('serializes all assertion fields into the backend credential shape', () => {
    const serialized = serializeAssertionCredential({
      id: 'assertion-id',
      rawId: bytes(1, 2),
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response: {
        clientDataJSON: bytes(3),
        authenticatorData: bytes(4),
        signature: bytes(5),
        userHandle: bytes(6),
      },
      getClientExtensionResults: () => ({ appid: true }),
    });

    expect(serialized).toEqual({
      id: 'assertion-id',
      rawId: 'AQI',
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response: {
        clientDataJSON: 'Aw',
        authenticatorData: 'BA',
        signature: 'BQ',
        userHandle: 'Bg',
      },
      clientExtensionResults: { appid: true },
    });
  });

  it('rejects incomplete and oversized browser credentials', () => {
    const base = {
      id: 'assertion-id',
      rawId: bytes(1),
      type: 'public-key',
      response: {
        clientDataJSON: bytes(2),
        authenticatorData: bytes(3),
        signature: bytes(4),
        userHandle: null,
      },
    };
    expect(() => serializeAssertionCredential({
      ...base,
      response: { ...base.response, signature: undefined },
    })).toThrow();
    expect(() => serializeAssertionCredential({
      ...base,
      rawId: new ArrayBuffer(4097),
    })).toThrow();
    expect(() => serializeAssertionCredential({
      ...base,
      getClientExtensionResults: () => ({ large: 'x'.repeat(17_000) }),
    })).toThrow();
  });
});
