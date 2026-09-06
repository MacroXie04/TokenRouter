// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  beginChannelKeyPasskey,
  finishChannelKeyPasskey,
  revealChannelKey,
  verifyChannelKeyTwoFactor,
} from './channel-key-api';
import { ChannelKeyRevealPanel } from './ChannelKeyRevealPanel';
import { isPasskeyLoginSupported, serializeAssertionCredential } from '../../lib/webauthn';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./channel-key-api', () => ({
  beginChannelKeyPasskey: vi.fn(),
  finishChannelKeyPasskey: vi.fn(),
  revealChannelKey: vi.fn(),
  verifyChannelKeyTwoFactor: vi.fn(),
}));

vi.mock('../../lib/webauthn', () => ({
  isPasskeyLoginSupported: vi.fn(),
  serializeAssertionCredential: vi.fn(),
}));

const mockedBeginPasskey = vi.mocked(beginChannelKeyPasskey);
const mockedFinishPasskey = vi.mocked(finishChannelKeyPasskey);
const mockedReveal = vi.mocked(revealChannelKey);
const mockedVerifyTwoFactor = vi.mocked(verifyChannelKeyTwoFactor);
const mockedPasskeySupported = vi.mocked(isPasskeyLoginSupported);
const mockedSerializeAssertion = vi.mocked(serializeAssertionCredential);

beforeEach(() => {
  vi.resetAllMocks();
  mockedPasskeySupported.mockReturnValue(true);
  mockedVerifyTwoFactor.mockResolvedValue('twofa.proof');
  mockedReveal.mockResolvedValue('sk-highly-sensitive');
  mockedBeginPasskey.mockResolvedValue({
    flowToken: 'flow-token',
    requestOptions: { publicKey: { challenge: new Uint8Array(32).buffer } },
  });
  mockedFinishPasskey.mockResolvedValue('passkey.proof');
  mockedSerializeAssertion.mockReturnValue({ id: 'credential', type: 'public-key' });
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { get: vi.fn().mockResolvedValue({ id: 'browser-credential' }) },
  });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('ChannelKeyRevealPanel', () => {
  it('uses a local 2FA proof, clears the code, and renders the returned key read-only', async () => {
    render(<ChannelKeyRevealPanel channel={{ id: 7, name: 'Primary' }} onClose={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Six-digit authenticator code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: 'Verify with 2FA' }));

    const key = await screen.findByLabelText('Channel key') as HTMLTextAreaElement;
    expect(key.value).toBe('sk-highly-sensitive');
    expect(key.readOnly).toBe(true);
    expect(mockedVerifyTwoFactor).toHaveBeenCalledWith('123456', expect.any(AbortSignal));
    expect(mockedReveal).toHaveBeenCalledWith(7, 'twofa.proof', expect.any(AbortSignal));
    expect(screen.queryByLabelText('Six-digit authenticator code')).toBeNull();
  });

  it('runs a passkey assertion and never sends the browser credential directly to reveal', async () => {
    render(<ChannelKeyRevealPanel channel={{ id: 9, name: 'Passkey channel' }} onClose={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Verify with passkey' }));

    await screen.findByDisplayValue('sk-highly-sensitive');
    expect(mockedBeginPasskey).toHaveBeenCalledWith(expect.any(AbortSignal));
    expect(navigator.credentials.get).toHaveBeenCalledWith(expect.objectContaining({ publicKey: expect.anything() }));
    expect(mockedSerializeAssertion).toHaveBeenCalledWith({ id: 'browser-credential' });
    expect(mockedFinishPasskey).toHaveBeenCalledWith(
      'flow-token',
      { id: 'credential', type: 'public-key' },
      expect.any(AbortSignal),
    );
    expect(mockedReveal).toHaveBeenCalledWith(9, 'passkey.proof', expect.any(AbortSignal));
  });

  it('shows a generic failure, clears a rejected 2FA code, and never renders returned error text', async () => {
    mockedVerifyTwoFactor.mockRejectedValueOnce(new Error('upstream-secret-error'));
    render(<ChannelKeyRevealPanel channel={{ id: 7, name: 'Primary' }} onClose={vi.fn()} />);
    const input = screen.getByLabelText('Six-digit authenticator code') as HTMLInputElement;
    fireEvent.change(input, { target: { value: '123456' } });
    fireEvent.submit(input.closest('form') as HTMLFormElement);

    await screen.findByRole('alert');
    expect(screen.getByRole('alert').textContent).toBe('Verification failed. Check your security method and try again.');
    expect(input.value).toBe('');
    expect(screen.queryByText('upstream-secret-error')).toBeNull();
    expect(mockedReveal).not.toHaveBeenCalled();
  });

  it('automatically removes the key after 45 seconds and supports immediate hiding', async () => {
    vi.useFakeTimers();
    render(<ChannelKeyRevealPanel channel={{ id: 7, name: 'Primary' }} onClose={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Six-digit authenticator code'), { target: { value: '123456' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Verify with 2FA' }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(screen.getByDisplayValue('sk-highly-sensitive')).toBeTruthy();

    act(() => { vi.advanceTimersByTime(45_000); });
    expect(screen.queryByDisplayValue('sk-highly-sensitive')).toBeNull();
    expect(screen.getByRole('status').textContent).toBe('The channel key was hidden automatically.');

    vi.useRealTimers();
    fireEvent.change(screen.getByLabelText('Six-digit authenticator code'), { target: { value: '654321' } });
    fireEvent.click(screen.getByRole('button', { name: 'Verify with 2FA' }));
    await screen.findByDisplayValue('sk-highly-sensitive');
    fireEvent.click(screen.getByRole('button', { name: 'Hide now' }));
    expect(screen.queryByDisplayValue('sk-highly-sensitive')).toBeNull();
  });

  it('aborts an in-flight verification when closed or unmounted', async () => {
    let capturedSignal: AbortSignal | undefined;
    mockedVerifyTwoFactor.mockImplementationOnce((_code, signal) => {
      capturedSignal = signal;
      return new Promise<string>(() => undefined);
    });
    const onClose = vi.fn();
    const rendered = render(<ChannelKeyRevealPanel channel={{ id: 7, name: 'Primary' }} onClose={onClose} />);
    fireEvent.change(screen.getByLabelText('Six-digit authenticator code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: 'Verify with 2FA' }));
    await waitFor(() => expect(capturedSignal).toBeDefined());
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(capturedSignal?.aborted).toBe(true);
    expect(onClose).toHaveBeenCalledOnce();

    mockedVerifyTwoFactor.mockImplementationOnce((_code, signal) => {
      capturedSignal = signal;
      return new Promise<string>(() => undefined);
    });
    rendered.unmount();
    const second = render(<ChannelKeyRevealPanel channel={{ id: 8, name: 'Second' }} onClose={onClose} />);
    fireEvent.change(screen.getByLabelText('Six-digit authenticator code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: 'Verify with 2FA' }));
    second.unmount();
    expect(capturedSignal?.aborted).toBe(true);
  });

  it('disables passkey verification when the browser does not support it', () => {
    mockedPasskeySupported.mockReturnValue(false);
    render(<ChannelKeyRevealPanel channel={{ id: 7, name: 'Primary' }} onClose={vi.fn()} />);
    expect((screen.getByRole('button', { name: 'Verify with passkey' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('Passkey verification is unavailable in this browser.')).toBeTruthy();
  });
});
