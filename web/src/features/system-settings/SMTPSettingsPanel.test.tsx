// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SMTPSettingsPanel } from './SMTPSettingsPanel';
import type { SMTPSettingsData } from './smtp-settings-api';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return { ...actual, useTranslation: () => ({ t: (key: string) => key }) };
});

const options = [
  { key: 'SMTPServer', value: 'smtp.example.test' },
  { key: 'SMTPPort', value: '587' },
  { key: 'SMTPAccount', value: 'mailer@example.test' },
  { key: 'SMTPFrom', value: 'no-reply@example.test' },
  { key: 'SMTPToken', value: '', redacted: true },
  { key: 'SMTPSSLEnabled', value: 'false' },
  { key: 'SMTPStartTLSEnabled', value: 'true' },
  { key: 'SMTPInsecureSkipVerify', value: 'false' },
  { key: 'SMTPForceAuthLogin', value: 'false' },
];

function saved(overrides: Partial<SMTPSettingsData> = {}): SMTPSettingsData {
  return {
    server: 'smtp.example.test',
    port: '587',
    account: 'mailer@example.test',
    from: 'no-reply@example.test',
    tokenConfigured: true,
    sslEnabled: false,
    startTLSEnabled: true,
    insecureSkipVerify: false,
    forceAuthLogin: false,
    ...overrides,
  };
}

afterEach(cleanup);

describe('SMTPSettingsPanel', () => {
  it('renders a write-only password marker and atomically preserves the stored password', async () => {
    const update = vi.fn().mockResolvedValue(saved({ server: 'smtp.next.test' }));
    const onSaved = vi.fn();
    render(<SMTPSettingsPanel options={options} onSaved={onSaved} update={update} />);

    const password = screen.getByLabelText('SMTP password') as HTMLInputElement;
    expect(password.type).toBe('password');
    expect(password.value).toBe('');
    expect(password.placeholder).toBe('Configured — enter a replacement');
    expect(document.body.textContent).not.toContain('smtp-option-secret');

    fireEvent.change(screen.getByLabelText('SMTP server'), { target: { value: 'smtp.next.test' } });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Save SMTP settings' }));
    await waitFor(() => expect(update).toHaveBeenCalledOnce());
    expect(update.mock.calls[0][0]).toMatchObject({
      server: 'smtp.next.test',
      token: '',
      currentTokenConfigured: true,
      clearToken: false,
      startTLSEnabled: true,
      sslEnabled: false,
    });
    expect(update.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
    expect(onSaved).toHaveBeenCalledOnce();
  });

  it('submits an implicit-TLS transition and replacement password as one update', async () => {
    const update = vi.fn().mockResolvedValue(saved({
      port: '465', sslEnabled: true, startTLSEnabled: false,
    }));
    render(<SMTPSettingsPanel options={options} onSaved={vi.fn()} update={update} />);
    const user = userEvent.setup();

    fireEvent.change(screen.getByLabelText('SMTP port'), { target: { value: '465' } });
    await user.selectOptions(screen.getByLabelText('SMTP transport security'), 'implicit');
    await user.type(screen.getByLabelText('SMTP password'), 'replacement-secret');
    await user.click(screen.getByRole('button', { name: 'Save SMTP settings' }));

    await waitFor(() => expect(update).toHaveBeenCalledOnce());
    expect(update.mock.calls[0][0]).toMatchObject({
      port: '465', token: 'replacement-secret', sslEnabled: true, startTLSEnabled: false,
    });
    expect(document.body.textContent).not.toContain('replacement-secret');
  });

  it('requires explicit confirmation before disabling certificate verification', async () => {
    const update = vi.fn().mockResolvedValue(saved({ insecureSkipVerify: true }));
    render(<SMTPSettingsPanel options={options} onSaved={vi.fn()} update={update} />);
    const user = userEvent.setup();

    await user.click(screen.getByLabelText('Disable TLS certificate verification'));
    await user.click(screen.getByRole('button', { name: 'Save SMTP settings' }));
    expect(update).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Disable certificate verification?' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));
    await waitFor(() => expect(update).toHaveBeenCalledOnce());
  });

  it('blocks remote plaintext and incomplete credential changes locally', async () => {
    const update = vi.fn();
    render(<SMTPSettingsPanel options={options} onSaved={vi.fn()} update={update} />);
    const user = userEvent.setup();

    await user.selectOptions(screen.getByLabelText('SMTP transport security'), 'none');
    fireEvent.change(screen.getByLabelText('SMTP port'), { target: { value: '25' } });
    await user.click(screen.getByRole('button', { name: 'Save SMTP settings' }));
    expect(screen.getByRole('alert').textContent).toBe('Remote SMTP servers require implicit TLS or STARTTLS.');
    expect(update).not.toHaveBeenCalled();

    await user.selectOptions(screen.getByLabelText('SMTP transport security'), 'starttls');
    fireEvent.change(screen.getByLabelText('SMTP account'), { target: { value: '' } });
    await user.click(screen.getByRole('button', { name: 'Save SMTP settings' }));
    expect(screen.getByRole('alert').textContent).toBe('SMTP account and password must be configured together.');
    expect(update).not.toHaveBeenCalled();
  });

  it('clears a stored password only through the explicit control', async () => {
    const update = vi.fn().mockResolvedValue(saved({
      server: '', account: '', from: '', tokenConfigured: false,
    }));
    render(<SMTPSettingsPanel options={options} onSaved={vi.fn()} update={update} />);
    const user = userEvent.setup();

    fireEvent.change(screen.getByLabelText('SMTP server'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('SMTP account'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('Sender email'), { target: { value: '' } });
    await user.click(screen.getByLabelText('Clear the stored SMTP password'));
    await user.click(screen.getByRole('button', { name: 'Save SMTP settings' }));
    await waitFor(() => expect(update).toHaveBeenCalledOnce());
    expect(update.mock.calls[0][0]).toMatchObject({ clearToken: true, token: '' });
  });

  it('fails closed on malformed option state and offers a bounded reload action', async () => {
    const onSaved = vi.fn();
    render(<SMTPSettingsPanel
      options={options.map((option) => option.key === 'SMTPPort' ? { ...option, value: '0' } : option)}
      onSaved={onSaved}
    />);
    expect(screen.getByRole('alert').textContent).toContain('Unable to read SMTP settings.');
    expect(screen.queryByLabelText('SMTP server')).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Reload settings' }));
    expect(onSaved).toHaveBeenCalledOnce();
  });

  it('rejects duplicate option rows instead of silently choosing one', () => {
    render(<SMTPSettingsPanel
      options={[...options, { key: 'SMTPServer', value: 'attacker.example.test' }]}
      onSaved={vi.fn()}
    />);
    expect(screen.getByRole('alert').textContent).toContain('Unable to read SMTP settings.');
    expect(screen.queryByText('attacker.example.test')).toBeNull();
  });

  it('aborts an in-flight atomic update when unmounted', async () => {
    let signal: AbortSignal | undefined;
    let began!: () => void;
    const started = new Promise<void>((resolve) => { began = resolve; });
    const update = vi.fn((_input, requestSignal?: AbortSignal) => {
      signal = requestSignal;
      began();
      return new Promise<SMTPSettingsData>(() => undefined);
    });
    const view = render(<SMTPSettingsPanel options={options} onSaved={vi.fn()} update={update} />);
    fireEvent.change(screen.getByLabelText('SMTP server'), { target: { value: 'smtp.next.test' } });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Save SMTP settings' }));
    await started;
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });
});
