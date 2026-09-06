// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SettingEditor, SystemSettingsEditor } from './SystemSettingsEditor';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return { ...actual, useTranslation: () => ({ t: (key: string) => key }) };
});

afterEach(cleanup);

describe('SystemSettingsEditor', () => {
  it('renders fixed deployment contracts without an option writer', () => {
    const saveOption = vi.fn();
    render(
      <SettingEditor
        option={{ key: 'QuotaPerUnit', value: '10' }}
        setting={{
          key: 'QuotaPerUnit',
          label: 'Quota per unit',
          description: 'Fixed accounting scale',
          kind: 'integer',
          fixedValue: '500000',
        }}
        onSaved={vi.fn()}
        saveOption={saveOption}
      />,
    );

    const input = screen.getByLabelText('Quota per unit') as HTMLInputElement;
    expect(input.value).toBe('500000');
    expect(input.readOnly).toBe(true);
    expect(screen.queryByRole('button')).toBeNull();
    expect(saveOption).not.toHaveBeenCalled();
  });

  it('shows the legacy quota fallback in the canonical editor and migrates it on save', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[{ key: 'InitialQuota', value: '500000' }]}
        settingsPath="billing/quota"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
        saveOption={saveOption}
      />,
    );

    const quota = screen.getByLabelText('New-user quota (canonical)') as HTMLInputElement;
    expect(quota.value).toBe('500000');
    expect(quota.getAttribute('aria-describedby')).toBeTruthy();
    expect(screen.getByRole('note').textContent).toContain('Legacy InitialQuota');
    expect(screen.queryByLabelText('Initial quota')).toBeNull();

    await user.click(within(quota.closest('.system-settings-field') as HTMLElement)
      .getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alertdialog', { name: 'Confirm setting change' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));
    await waitFor(() => expect(saveOption).toHaveBeenCalledOnce());
    expect(saveOption.mock.calls[0][0]).toEqual({ key: 'QuotaForNewUser', value: '500000' });
  });

  it('renders generated-token routing and special usable-group controls', () => {
    const commonProps = {
      options: [],
      onNavigate: vi.fn(),
      onSaved: vi.fn(),
      saveOption: vi.fn().mockResolvedValue(undefined),
    };
    const view = render(<SystemSettingsEditor {...commonProps} settingsPath="models/global" />);
    const automatic = screen.getByLabelText('Use automatic group for generated default tokens') as HTMLSelectElement;
    expect(automatic.tagName).toBe('SELECT');
    expect(automatic.value).toBe('false');

    view.rerender(<SystemSettingsEditor {...commonProps} settingsPath="billing/group-pricing" />);
    const directives = screen.getByLabelText('Special usable groups (JSON)') as HTMLTextAreaElement;
    expect(directives.value).toBe('{}');
    expect(directives.maxLength).toBe(64 * 1024);
  });

  it('renders the exact route hierarchy and saves a risky field only after confirmation', async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    const onSaved = vi.fn();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[{ key: 'passkey.enabled', value: 'false' }, { key: 'InitialQuota', value: '10' }]}
        settingsPath="auth/passkey"
        onNavigate={onNavigate}
        onSaved={onSaved}
        saveOption={saveOption}
      />,
    );

    expect(screen.getByRole('navigation', { name: 'Settings categories' }).querySelectorAll('button')).toHaveLength(7);
    expect(screen.getByRole('navigation', { name: 'Authentication Settings' }).querySelectorAll('button')).toHaveLength(5);
    expect(screen.getByRole('heading', { name: 'Passkey', level: 2 })).toBeTruthy();
    expect(screen.queryByLabelText('Initial quota')).toBeNull();

    const passkeyEnabled = screen.getByLabelText('Passkey login enabled');
    await user.selectOptions(passkeyEnabled, 'true');
    await user.click(within(passkeyEnabled.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(saveOption).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Confirm setting change' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));
    await waitFor(() => expect(saveOption).toHaveBeenCalledOnce());
    expect(saveOption.mock.calls[0][0]).toEqual({ key: 'passkey.enabled', value: 'true' });
    expect(saveOption.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
    expect(onSaved).toHaveBeenCalledOnce();

    await user.click(screen.getByRole('button', { name: 'OAuth' }));
    expect(onNavigate).toHaveBeenCalledWith('/system-settings/auth/oauth');
  });

  it('rejects malformed JSON locally and mounts only the selected supplement', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[{ key: 'channel_affinity_setting.rules', value: '[]' }]}
        settingsPath="models/channel-affinity"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
        saveOption={saveOption}
        supplements={{
          'models/channel-affinity': <p>Live affinity controls</p>,
          'billing/payment': <p>Payment-only controls</p>,
        }}
      />,
    );

    expect(screen.getByText('Live affinity controls')).toBeTruthy();
    expect(screen.queryByText('Payment-only controls')).toBeNull();
    fireEvent.change(screen.getByLabelText('Affinity rules (JSON)'), { target: { value: '{bad' } });
    await user.click(screen.getAllByRole('button', { name: 'Save changes' }).at(-1)!);
    expect(screen.getByRole('alert')).toHaveProperty('textContent', 'Enter valid JSON.');
    expect(saveOption).not.toHaveBeenCalled();
  });

  it('renders accessible typed Gemini controls and rejects an unsupported safety threshold', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[]}
        settingsPath="models/gemini"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
        saveOption={saveOption}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Gemini', level: 2 })).toBeTruthy();
    const safety = screen.getByLabelText('Safety thresholds (JSON)') as HTMLTextAreaElement;
    expect(safety.value).toBe('{"default":"OFF"}');
    expect(screen.getByLabelText('Thinking suffix adapter enabled').tagName).toBe('SELECT');
    const budget = screen.getByLabelText('Thinking budget share') as HTMLInputElement;
    expect(budget.inputMode).toBe('decimal');
    expect(budget.value).toBe('0.6');

    fireEvent.change(safety, { target: { value: '{"default":"BLOCK_SOME"}' } });
    await user.click(within(safety.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alert')).toHaveProperty('textContent', 'Enter valid bounded Gemini safety settings.');
    expect(saveOption).not.toHaveBeenCalled();
  });

  it('renders bounded Grok fee controls and preserves the exact decimal string on save', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[]}
        settingsPath="models/grok"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
        saveOption={saveOption}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Grok', level: 2 })).toBeTruthy();
    const enabled = screen.getByLabelText('Violation deduction enabled') as HTMLSelectElement;
    expect(enabled.tagName).toBe('SELECT');
    expect(enabled.required).toBe(true);
    expect(enabled.value).toBe('true');

    const amount = screen.getByLabelText('Violation deduction amount (USD)') as HTMLInputElement;
    expect(amount.inputMode).toBe('decimal');
    expect(amount.required).toBe(true);
    expect(amount.min).toBe('0');
    expect(amount.max).toBe('4294.967294');
    expect(amount.value).toBe('0.05');

    fireEvent.change(amount, { target: { value: '' } });
    await user.click(within(amount.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alert')).toHaveProperty('textContent', 'Enter a value within the allowed range.');
    expect(saveOption).not.toHaveBeenCalled();

    fireEvent.change(amount, { target: { value: '4294.967295' } });
    await user.click(within(amount.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alert')).toHaveProperty('textContent', 'Enter a value within the allowed range.');
    expect(saveOption).not.toHaveBeenCalled();

    fireEvent.change(amount, { target: { value: '0.125000' } });
    await user.click(within(amount.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alertdialog', { name: 'Confirm setting change' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));
    await waitFor(() => expect(saveOption).toHaveBeenCalledOnce());
    expect(saveOption.mock.calls[0][0]).toEqual({
      key: 'grok.violation_deduction_amount',
      value: '0.125000',
    });
  });

  it('renders model request limits and blocks duplicate group policies before transport', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[]}
        settingsPath="security/rate-limit"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
        saveOption={saveOption}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Rate limiting', level: 2 })).toBeTruthy();
    expect((screen.getByLabelText('Rate-limit window (minutes)') as HTMLInputElement).value).toBe('1');
    const groups = screen.getByLabelText('Group request limits (JSON)') as HTMLTextAreaElement;
    expect(groups.value).toBe('{}');
    fireEvent.change(groups, { target: { value: '{"vip":[1,2],"vip":[3,4]}' } });
    await user.click(within(groups.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alert')).toHaveProperty('textContent', 'Enter valid bounded model request group limits.');
    expect(saveOption).not.toHaveBeenCalled();
  });

  it('renders the three console-content editors and rejects an unsafe API card before transport', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    const commonProps = {
      onNavigate: vi.fn(),
      onSaved: vi.fn(),
      saveOption,
    };
    const view = render(
      <SystemSettingsEditor
        {...commonProps}
        options={[
          { key: 'console_setting.api_info_enabled', value: 'true' },
          { key: 'console_setting.api_info', value: '[]' },
          { key: 'console_setting.faq_enabled', value: 'true' },
          { key: 'console_setting.faq', value: '[]' },
          { key: 'console_setting.uptime_kuma_enabled', value: 'true' },
          { key: 'console_setting.uptime_kuma_groups', value: '[]' },
        ]}
        settingsPath="content/api-info"
      />,
    );

    const apiInput = screen.getByLabelText('API information entries (JSON)');
    fireEvent.change(apiInput, { target: { value: '[{"url":"javascript:alert(1)","route":"Primary","description":"Unsafe","color":"blue"}]' } });
    await user.click(within(apiInput.closest('.system-settings-field') as HTMLElement).getByRole('button', { name: 'Save changes' }));
    expect(screen.getByRole('alert')).toHaveProperty('textContent', 'Enter API information as a valid bounded JSON array.');
    expect(saveOption).not.toHaveBeenCalled();

    view.rerender(<SystemSettingsEditor {...commonProps} options={[]} settingsPath="content/faq" />);
    expect(screen.getByLabelText('FAQ entries (JSON)')).toBeTruthy();
    view.rerender(<SystemSettingsEditor {...commonProps} options={[]} settingsPath="content/uptime-kuma" />);
    expect(screen.getByLabelText('Uptime Kuma groups (JSON)')).toBeTruthy();
    expect(screen.queryByRole('note')).toBeNull();
  });

  it('redacts leaked secrets, preserves the configured marker, and never renders the value', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SystemSettingsEditor
        options={[{ key: 'TurnstileSecretKey', value: 'stored-private-secret' }]}
        settingsPath="auth/bot-protection"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
        saveOption={saveOption}
      />,
    );

    const input = screen.getByLabelText('Turnstile secret key') as HTMLInputElement;
    expect(input.type).toBe('password');
    expect(input.value).toBe('');
    expect(input.placeholder).toBe('Configured — enter a replacement');
    expect(document.body.textContent).not.toContain('stored-private-secret');
    await user.type(input, 'replacement-secret');
    await user.click(screen.getAllByRole('button', { name: 'Save changes' }).at(-1)!);
    expect(screen.getByRole('alertdialog').textContent).not.toContain('replacement-secret');
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));
    await waitFor(() => expect(saveOption).toHaveBeenCalledOnce());
    expect(saveOption.mock.calls[0][0]).toEqual({ key: 'TurnstileSecretKey', value: 'replacement-secret' });
    expect(input.value).toBe('');
  });

  it('aborts an in-flight save when its editor unmounts', async () => {
    const user = userEvent.setup();
    let capturedSignal: AbortSignal | undefined;
    let started!: () => void;
    const began = new Promise<void>((resolve) => { started = resolve; });
    const saveOption = vi.fn((_input, signal?: AbortSignal) => {
      capturedSignal = signal;
      started();
      return new Promise<void>(() => undefined);
    });
    const onSaved = vi.fn();
    const view = render(
      <SystemSettingsEditor
        options={[{ key: 'Chats', value: '[]' }]}
        settingsPath="content/chat"
        onNavigate={vi.fn()}
        onSaved={onSaved}
        saveOption={saveOption}
      />,
    );

    fireEvent.change(screen.getByLabelText('Chat launchers (JSON)'), { target: { value: '[{"Help":"https://example.com"}]' } });
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    await began;
    view.unmount();
    expect(capturedSignal?.aborted).toBe(true);
    expect(onSaved).not.toHaveBeenCalled();
  });

  it('shows exact unsupported routes without exposing a generic option writer', () => {
    render(
      <SystemSettingsEditor
        options={[{ key: 'UnknownRuntimeKey', value: 'do-not-edit' }]}
        settingsPath="security/ssrf"
        onNavigate={vi.fn()}
        onSaved={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: 'SSRF protection' })).toBeTruthy();
    expect(screen.getByRole('note').textContent).toContain('no mutable SSRF option contract');
    expect(screen.queryByRole('button', { name: 'Save changes' })).toBeNull();
    expect(document.body.textContent).not.toContain('UnknownRuntimeKey');
  });
});
