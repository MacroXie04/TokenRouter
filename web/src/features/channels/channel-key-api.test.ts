import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import {
  beginChannelKeyPasskey,
  ChannelKeyContractError,
  finishChannelKeyPasskey,
  parseChannelKeyRevealResponse,
  revealChannelKey,
  verifyChannelKeyTwoFactor,
} from './channel-key-api';

vi.mock('../../api', () => ({
  api: { post: vi.fn() },
}));

const mockedPost = vi.mocked(api.post);

function proofEnvelope(method: '2fa' | 'passkey', overrides: Record<string, unknown> = {}) {
  return {
    success: true,
    data: {
      proof_token: 'header.payload.signature',
      expires_at: 1_900_000_000,
      method,
      scope: 'channel.key.read',
      ...overrides,
    },
  };
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('channel key step-up API', () => {
  it('verifies a bounded six-digit 2FA code for the exact channel-key scope', async () => {
    mockedPost.mockResolvedValueOnce({ data: proofEnvelope('2fa') });

    await expect(verifyChannelKeyTwoFactor(' 123456 ')).resolves.toBe('header.payload.signature');
    expect(mockedPost).toHaveBeenCalledWith('/verify', {
      method: '2fa',
      code: '123456',
      scope: 'channel.key.read',
    }, expect.objectContaining({
      maxContentLength: 1024 * 1024,
      maxBodyLength: 1024 * 1024,
      headers: expect.objectContaining({ 'Cache-Control': 'no-store', Pragma: 'no-cache' }),
    }));
  });

  it('fails closed on invalid codes, proof methods, scopes, tokens, and expiries', async () => {
    await expect(verifyChannelKeyTwoFactor('12345')).rejects.toBeInstanceOf(ChannelKeyContractError);
    expect(mockedPost).not.toHaveBeenCalled();

    for (const envelope of [
      proofEnvelope('passkey'),
      proofEnvelope('2fa', { scope: 'passkey.delete' }),
      proofEnvelope('2fa', { proof_token: 'contains spaces' }),
      proofEnvelope('2fa', { expires_at: -1 }),
    ]) {
      mockedPost.mockResolvedValueOnce({ data: envelope });
      await expect(verifyChannelKeyTwoFactor('123456')).rejects.toBeInstanceOf(ChannelKeyContractError);
    }
  });

  it('parses passkey begin options and submits a flattened bounded assertion', async () => {
    mockedPost
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            flow_token: 'flow_token-1',
            options: { publicKey: { challenge: 'AAAAAAAAAAAAAAAAAAAAAA', userVerification: 'preferred' } },
          },
        },
      })
      .mockResolvedValueOnce({ data: proofEnvelope('passkey') });

    const begin = await beginChannelKeyPasskey();
    expect(begin.flowToken).toBe('flow_token-1');
    expect(begin.requestOptions.publicKey?.challenge).toBeInstanceOf(ArrayBuffer);

    const assertion = {
      id: 'credential',
      rawId: 'AQID',
      type: 'public-key',
      response: { clientDataJSON: 'AQID', authenticatorData: 'AQID', signature: 'AQID', userHandle: null },
      clientExtensionResults: {},
    };
    await expect(finishChannelKeyPasskey(begin.flowToken, assertion)).resolves.toBe('header.payload.signature');
    expect(mockedPost).toHaveBeenNthCalledWith(2, '/user/passkey/verify/finish', {
      ...assertion,
      flow_token: 'flow_token-1',
    }, expect.objectContaining({ headers: expect.objectContaining({ 'Cache-Control': 'no-store' }) }));
  });

  it('rejects malformed passkey envelopes, unsafe flow tokens, and oversized assertions', async () => {
    mockedPost.mockResolvedValueOnce({ data: { success: true, data: { flow_token: 'flow', options: {} } } });
    await expect(beginChannelKeyPasskey()).rejects.toBeInstanceOf(ChannelKeyContractError);

    await expect(finishChannelKeyPasskey('bad token', {})).rejects.toBeInstanceOf(ChannelKeyContractError);
    await expect(finishChannelKeyPasskey('flow', { value: 'x'.repeat(128 * 1024) }))
      .rejects.toBeInstanceOf(ChannelKeyContractError);
  });

  it('reveals only a bounded safe key and sends the proof in a header', async () => {
    mockedPost.mockResolvedValueOnce({ data: { success: true, data: { key: 'sk-first\nsk-second' } } });
    await expect(revealChannelKey(7, 'header.payload.signature')).resolves.toBe('sk-first\nsk-second');
    expect(mockedPost).toHaveBeenCalledWith('/channel/7/key', {}, expect.objectContaining({
      headers: expect.objectContaining({
        'X-Security-Proof': 'header.payload.signature',
        'Cache-Control': 'no-store',
      }),
    }));
  });

  it('rejects invalid ids, failed envelopes, oversized keys, and unsafe key text', async () => {
    await expect(revealChannelKey(0, 'proof')).rejects.toBeInstanceOf(ChannelKeyContractError);
    expect(mockedPost).not.toHaveBeenCalled();

    expect(() => parseChannelKeyRevealResponse({ success: false, data: { key: 'secret' } }))
      .toThrow(ChannelKeyContractError);
    expect(() => parseChannelKeyRevealResponse({ success: true, data: { key: '' } }))
      .toThrow(ChannelKeyContractError);
    expect(() => parseChannelKeyRevealResponse({ success: true, data: { key: 'sk\u202esecret' } }))
      .toThrow(ChannelKeyContractError);
    expect(() => parseChannelKeyRevealResponse({ success: true, data: { key: 'x'.repeat(512 * 1024 + 1) } }))
      .toThrow(ChannelKeyContractError);
  });
});
