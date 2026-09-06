// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { User } from '../../api';
import { authPost } from './auth-api';
import { OtpView } from './OtpView';

vi.mock('react-i18next', () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});

vi.mock('./auth-api', () => ({
  authPost: vi.fn(),
  isCanceledAuthRequest: (_error: unknown, signal?: AbortSignal) => signal?.aborted === true,
}));

const signedInUser: User = {
  id: 7,
  username: 'secured-user',
  display_name: '',
  role: 1,
  group: 'default',
  quota: 500_000,
  used_quota: 0,
  request_count: 0,
};

afterEach(() => {
  cleanup();
  vi.mocked(authPost).mockReset();
});

describe('OtpView', () => {
  it('fails closed when the login flow is absent or oversized', () => {
    const rendered = render(<OtpView flowToken="" onLoggedIn={vi.fn()} />);
    expect(rendered.container.querySelector('[data-auth-layout="true"]')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toBe('This sign-in challenge has expired.');
    expect(screen.getByRole('link', { name: 'Back to sign in' }).getAttribute('href')).toBe('/sign-in');

    rendered.unmount();
    render(<OtpView flowToken={'x'.repeat(257)} onLoggedIn={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toBe('This sign-in challenge has expired.');
  });

  it('submits exactly six digits and completes sign-in', async () => {
    vi.mocked(authPost).mockResolvedValueOnce(signedInUser);
    const onLoggedIn = vi.fn();
    const user = userEvent.setup();
    render(<OtpView flowToken="flow-token" onLoggedIn={onLoggedIn} />);

    const input = screen.getByLabelText('Verification code');
    await user.type(input, '12a34567');
    expect(input.getAttribute('value')).toBe('123456');
    await user.click(screen.getByRole('button', { name: 'Verify' }));

    await waitFor(() => expect(authPost).toHaveBeenCalledWith('/user/login/2fa', {
      flow_token: 'flow-token', code: '123456',
    }, undefined, expect.any(AbortSignal)));
    expect(onLoggedIn).toHaveBeenCalledWith(signedInUser);
  });

  it('formats and normalizes a single-use backup code', async () => {
    vi.mocked(authPost).mockResolvedValueOnce(signedInUser);
    const user = userEvent.setup();
    render(<OtpView flowToken="flow-token" onLoggedIn={vi.fn()} />);

    await user.click(screen.getByRole('button', { name: 'Use backup code' }));
    const input = screen.getByLabelText('Backup code');
    await user.type(input, 'abcd-1234-extra');
    expect(input.getAttribute('value')).toBe('ABCD-1234');
    await user.click(screen.getByRole('button', { name: 'Verify' }));
    await waitFor(() => expect(authPost).toHaveBeenCalledWith('/user/login/2fa', {
      flow_token: 'flow-token', code: 'ABCD1234',
    }, undefined, expect.any(AbortSignal)));
  });

  it('redacts API and malformed-response details', async () => {
    vi.mocked(authPost).mockRejectedValueOnce(new Error('private factor detail'));
    const user = userEvent.setup();
    const rendered = render(<OtpView flowToken="flow-token" onLoggedIn={vi.fn()} />);
    await user.type(screen.getByLabelText('Verification code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Verify' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Authentication failed.');
    expect(screen.queryByText('private factor detail')).toBeNull();

    rendered.unmount();
    vi.mocked(authPost).mockResolvedValueOnce({ id: 0 });
    render(<OtpView flowToken="flow-token" onLoggedIn={vi.fn()} />);
    await user.type(screen.getByLabelText('Verification code'), '654321');
    await user.click(screen.getByRole('button', { name: 'Verify' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Authentication failed.');
  });

  it('deduplicates submission and aborts a late response after unmount', async () => {
    let resolveRequest: (value: unknown) => void = () => {};
    const request = new Promise<unknown>((resolve) => { resolveRequest = resolve; });
    vi.mocked(authPost).mockReturnValueOnce(request);
    const onLoggedIn = vi.fn();
    const rendered = render(<OtpView flowToken="flow-token" onLoggedIn={onLoggedIn} />);
    await userEvent.setup().type(screen.getByLabelText('Verification code'), '123456');
    const submit = screen.getByRole('button', { name: 'Verify' });

    act(() => {
      submit.click();
      submit.click();
    });
    expect(authPost).toHaveBeenCalledTimes(1);
    const signal = vi.mocked(authPost).mock.calls[0][3] as AbortSignal;
    rendered.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      resolveRequest(signedInUser);
      await request;
    });
    expect(onLoggedIn).not.toHaveBeenCalled();
  });
});
