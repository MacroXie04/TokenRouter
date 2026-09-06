// @vitest-environment jsdom

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  HeaderNavigationSettingsPanel,
  SidebarModulesSettingsPanel,
} from './NavigationSettingsPanels';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return { ...actual, useTranslation: () => ({ t: (key: string) => key }) };
});

afterEach(cleanup);

describe('HeaderNavigationSettingsPanel', () => {
  it('edits supported modules while preserving unknown configuration', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    const onSaved = vi.fn();
    render(
      <HeaderNavigationSettingsPanel
        options={[{
          key: 'HeaderNavModules',
          value: JSON.stringify({
            home: true,
            pricing: { enabled: true, requireAuth: false, badge: 'beta' },
            extension: { enabled: false, path: '/extension' },
          }),
        }]}
        saveOption={saveOption}
        onSaved={onSaved}
      />,
    );

    await user.click(screen.getByLabelText('Home'));
    await user.click(screen.getByLabelText('Require login for pricing'));
    await user.click(screen.getByRole('button', { name: 'Save navigation' }));
    expect(saveOption).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));

    await waitFor(() => expect(saveOption).toHaveBeenCalledOnce());
    const mutation = saveOption.mock.calls[0][0];
    expect(mutation.key).toBe('HeaderNavModules');
    expect(JSON.parse(mutation.value)).toMatchObject({
      home: false,
      pricing: { enabled: true, requireAuth: true, badge: 'beta' },
      extension: { enabled: false, path: '/extension' },
    });
    expect(saveOption.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
    expect(onSaved).toHaveBeenCalledOnce();
  });

  it('fails safely to enabled defaults for malformed stored JSON', () => {
    render(
      <HeaderNavigationSettingsPanel
        options={[{ key: 'HeaderNavModules', value: '{broken' }]}
        saveOption={vi.fn()}
        onSaved={vi.fn()}
      />,
    );

    expect(screen.getByLabelText('Home')).toHaveProperty('checked', true);
    expect(screen.getByLabelText('Pricing')).toHaveProperty('checked', true);
    expect(screen.getByLabelText('Require login for pricing')).toHaveProperty('checked', false);
    expect(screen.getByRole('note').textContent).toContain('invalid');
  });
});

describe('SidebarModulesSettingsPanel', () => {
  it('normalizes known module controls and preserves custom sections and modules', async () => {
    const user = userEvent.setup();
    const saveOption = vi.fn().mockResolvedValue(undefined);
    render(
      <SidebarModulesSettingsPanel
        options={[{
          key: 'SidebarModulesAdmin',
          value: JSON.stringify({
            chat: { enabled: true, playground: true, chat: true, custom_chat: false },
            extension: { enabled: false, custom: true },
          }),
        }]}
        saveOption={saveOption}
        onSaved={vi.fn()}
      />,
    );

    const admin = screen.getByRole('group', { name: 'Administrator' });
    await user.click(within(admin).getByLabelText('Channels'));
    const extension = screen.getByRole('group', { name: 'Extension' });
    await user.click(within(extension).getByLabelText('Extension — Enabled'));
    await user.click(within(extension).getByLabelText('Custom'));
    await user.click(screen.getByRole('button', { name: 'Save sidebar modules' }));
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));

    await waitFor(() => expect(saveOption).toHaveBeenCalledOnce());
    const saved = JSON.parse(saveOption.mock.calls[0][0].value);
    expect(saved.extension).toEqual({ enabled: true, custom: false });
    expect(saved.chat.custom_chat).toBe(false);
    expect(saved.admin.channel).toBe(false);
    expect(saved.admin.models).toBe(true);
  });

  it('disables child controls when a section is hidden and can reset to open defaults', async () => {
    const user = userEvent.setup();
    render(
      <SidebarModulesSettingsPanel
        options={[{ key: 'SidebarModulesAdmin', value: JSON.stringify({ admin: false }) }]}
        saveOption={vi.fn()}
        onSaved={vi.fn()}
      />,
    );

    const admin = screen.getByRole('group', { name: 'Administrator' });
    expect(within(admin).getByLabelText('Channels')).toHaveProperty('disabled', true);
    await user.click(screen.getByRole('button', { name: 'Reset to defaults' }));
    expect(within(admin).getByLabelText('Administrator — Enabled')).toHaveProperty('checked', true);
    expect(within(admin).getByLabelText('Channels')).toHaveProperty('disabled', false);
  });

  it('treats prototype-named extension keys as inert configuration data', () => {
    render(
      <SidebarModulesSettingsPanel
        options={[{
          key: 'SidebarModulesAdmin',
          value: JSON.stringify({ constructor: { enabled: false, toString: false } }),
        }]}
        saveOption={vi.fn()}
        onSaved={vi.fn()}
      />,
    );

    const extension = screen.getByRole('group', { name: 'Constructor' });
    expect(within(extension).getByLabelText('Constructor — Enabled')).toHaveProperty('checked', false);
    expect(within(extension).getByLabelText('ToString')).toHaveProperty('disabled', true);
    expect(Object.hasOwn(Object, 'enabled')).toBe(false);
  });

  it('aborts an in-flight write when it unmounts', async () => {
    const user = userEvent.setup();
    let signal: AbortSignal | undefined;
    let began!: () => void;
    const started = new Promise<void>((resolve) => { began = resolve; });
    const saveOption = vi.fn((_input, requestSignal?: AbortSignal) => {
      signal = requestSignal;
      began();
      return new Promise<void>(() => undefined);
    });
    const view = render(
      <SidebarModulesSettingsPanel options={[]} saveOption={saveOption} onSaved={vi.fn()} />,
    );

    await user.click(screen.getByLabelText('Chat area — Enabled'));
    await user.click(screen.getByRole('button', { name: 'Save sidebar modules' }));
    await user.click(screen.getByRole('button', { name: 'Confirm and save' }));
    await started;
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });
});
