// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { authGet, authPost } from './auth-api';
import { PasswordResetView } from './PasswordResetView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, string | number>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? `{{${name}}}`),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./auth-api', () => ({
  authGet: vi.fn(),
  authPost: vi.fn(),
  isCanceledAuthRequest: (_error: unknown, signal?: AbortSignal) => signal?.aborted === true,
}));

afterEach(() => {
  cleanup();
  vi.mocked(authGet).mockReset();
  vi.mocked(authPost).mockReset();
  delete window.turnstile;
  window.history.replaceState(null, '', '/');
});

describe('PasswordResetView', () => {
  it('gates reset-email requests with the configured Turnstile token', async () => {
    let options: Record<string, unknown> = {};
    window.turnstile = {
      render: vi.fn((_element, value) => {
        options = value;
        return 'forgot-widget';
      }),
      remove: vi.fn(),
    };
    window.history.replaceState(null, '', '/forgot-password');
    vi.mocked(authGet).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<PasswordResetView
      resetMode={false}
      turnstileConfig={{ required: true, siteKey: 'public-site-key' }}
    />);

    await user.type(screen.getByLabelText('Email'), 'person@example.com');
    await user.click(screen.getByRole('button', { name: 'Send reset code' }));
    expect(authGet).not.toHaveBeenCalled();
    expect(screen.getByRole('alert').textContent).toBe('Complete the human verification challenge.');

    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    (options.callback as (token: string) => void)('forgot-challenge');
    await user.click(screen.getByRole('button', { name: 'Send reset code' }));
    await waitFor(() => expect(authGet).toHaveBeenCalledWith('/reset_password', {
      email: 'person@example.com', turnstile: 'forgot-challenge',
    }, expect.any(AbortSignal)));
  });

  it('requests a reset without disclosing account existence', async () => {
    window.history.replaceState(null, '', '/forgot-password');
    vi.mocked(authGet).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<PasswordResetView resetMode={false} />);

    await user.type(screen.getByLabelText('Email'), 'person@example.com');
    await user.click(screen.getByRole('button', { name: 'Send reset code' }));

    await waitFor(() => expect(authGet).toHaveBeenCalledWith(
      '/reset_password', { email: 'person@example.com' }, expect.any(AbortSignal),
    ));
    expect((await screen.findByRole('status')).textContent)
      .toBe('If the address is registered, a reset code has been sent.');
    expect(screen.getByRole('button', { name: 'Try again in 30 seconds' }).hasAttribute('disabled')).toBe(true);
  });

  it('consumes a reference token link and displays the generated password', async () => {
    window.history.replaceState(null, '', '/user/reset?email=person%40example.com&token=opaque-token');
    vi.mocked(authPost).mockResolvedValueOnce('GeneratedPassword16');
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    render(<PasswordResetView resetMode />);

    expect(screen.getByText('Confirm the reset link to generate a new password.')).toBeTruthy();
    expect(screen.getByLabelText('Email').hasAttribute('readonly')).toBe(true);
    await user.click(screen.getByRole('button', { name: 'Reset password' }));

    await waitFor(() => expect(authPost).toHaveBeenCalledWith('/user/reset', {
      email: 'person@example.com', token: 'opaque-token',
    }, undefined, expect.any(AbortSignal)));
    expect((await screen.findByLabelText('New password')).getAttribute('value')).toBe('GeneratedPassword16');
    expect(screen.getByRole('status').textContent).toBe('Password reset. You can sign in now.');
    expect(writeText).toHaveBeenCalledWith('GeneratedPassword16');
    await user.click(screen.getByRole('button', { name: 'Copy password' }));
    expect(writeText).toHaveBeenCalledTimes(2);
  });

  it('retains the TokenRouter choose-password extension for emailed codes', async () => {
    window.history.replaceState(null, '', '/reset?email=person%40example.com&code=123456');
    vi.mocked(authPost).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<PasswordResetView resetMode />);

    await user.type(screen.getByLabelText('New password'), 'chosen-password');
    await user.click(screen.getByRole('button', { name: 'Reset password' }));
    await waitFor(() => expect(authPost).toHaveBeenCalledWith('/user/reset', {
      email: 'person@example.com', token: '123456', new_password: 'chosen-password',
    }, undefined, expect.any(AbortSignal)));
    expect(screen.getByRole('status').textContent).toBe('Password reset. You can sign in now.');
  });

  it('applies the backend byte ceiling to a chosen reset password', async () => {
    window.history.replaceState(null, '', '/reset?email=person%40example.com&code=123456');
    const user = userEvent.setup();
    render(<PasswordResetView resetMode />);

    await user.type(screen.getByLabelText('New password'), '你'.repeat(22));
    await user.click(screen.getByRole('button', { name: 'Reset password' }));

    expect(screen.getByRole('alert').textContent).toBe('Request failed.');
    expect(authPost).not.toHaveBeenCalled();
  });

  it('bounds reset credentials and redacts request failures', async () => {
    const oversized = 'x'.repeat(257);
    window.history.replaceState(null, '', `/reset?email=person%40example.com&token=${oversized}`);
    const user = userEvent.setup();
    const rendered = render(<PasswordResetView resetMode />);
    await user.click(screen.getByRole('button', { name: 'Reset password' }));
    expect(screen.getByRole('alert').textContent).toBe('Request failed.');
    expect(authPost).not.toHaveBeenCalled();

    rendered.unmount();
    window.history.replaceState(null, '', '/reset?email=person%40example.com&token=valid-token');
    vi.mocked(authPost).mockRejectedValueOnce(new Error('private database detail'));
    render(<PasswordResetView resetMode />);
    await user.click(screen.getByRole('button', { name: 'Reset password' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Request failed.');
    expect(screen.queryByText('private database detail')).toBeNull();
    expect(screen.getByRole('button', { name: 'Try again in 30 seconds' }).hasAttribute('disabled')).toBe(true);
  });

  it('rejects ambiguous reset links before transport and uses the shared auth layout', () => {
    window.history.replaceState(
      null,
      '',
      '/user/reset?email=person%40example.com&token=one&token=two',
    );
    const rendered = render(<PasswordResetView resetMode />);

    expect(rendered.container.querySelector('[data-auth-layout="true"]')).toBeTruthy();
    expect(screen.getByRole('main', { name: 'Reset password' })).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toBe('Request failed.');
    expect(screen.getByRole('button', { name: 'Reset password' }).hasAttribute('disabled')).toBe(true);
    expect(authPost).not.toHaveBeenCalled();
  });

  it('deduplicates requests and aborts a late response after unmount', async () => {
    window.history.replaceState(null, '', '/forgot-password');
    let resolveRequest: (value: unknown) => void = () => {};
    const request = new Promise<unknown>((resolve) => { resolveRequest = resolve; });
    vi.mocked(authGet).mockReturnValueOnce(request);
    const rendered = render(<PasswordResetView resetMode={false} />);
    await userEvent.setup().type(screen.getByLabelText('Email'), 'person@example.com');
    const submit = screen.getByRole('button', { name: 'Send reset code' });

    act(() => {
      submit.click();
      submit.click();
    });
    expect(authGet).toHaveBeenCalledTimes(1);
    const signal = vi.mocked(authGet).mock.calls[0][2] as AbortSignal;
    rendered.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      resolveRequest(undefined);
      await request;
    });
  });
});
