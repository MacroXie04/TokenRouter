// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  loadCurrentLogCleanupTask,
  loadPerformanceLogSummary,
  loadRuntimeVersion,
  loadSystemOptions,
} from './system-settings-api';
import { SystemSettingsView } from './SystemSettingsView';

vi.mock('./system-settings-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./system-settings-api')>();
  return {
    ...actual,
    loadCurrentLogCleanupTask: vi.fn(),
    loadPerformanceLogSummary: vi.fn(),
    loadRuntimeVersion: vi.fn(),
    loadSystemOptions: vi.fn(),
  };
});
vi.mock('../wallet/WaffoPancakeAdminPanel', () => ({
  WaffoPancakeAdminPanel: () => <p>Waffo Pancake controls</p>,
}));
vi.mock('../models/models-api', () => ({ testDeploymentConnection: vi.fn() }));
vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      i18n: { language: 'en' },
      t: (key: string, values?: Record<string, unknown>) => Object.entries(values ?? {}).reduce(
        (text, [name, value]) => text.replaceAll(`{{${name}}}`, String(value)),
        key,
      ),
    }),
  };
});

const mockedLoad = vi.mocked(loadSystemOptions);
const mockedLoadCurrentLogCleanupTask = vi.mocked(loadCurrentLogCleanupTask);
const mockedLoadPerformanceLogSummary = vi.mocked(loadPerformanceLogSummary);
const mockedLoadRuntimeVersion = vi.mocked(loadRuntimeVersion);

beforeEach(() => {
  vi.resetAllMocks();
  mockedLoad.mockResolvedValue([{ key: 'SystemName', value: 'TokenRouter' }]);
  mockedLoadCurrentLogCleanupTask.mockResolvedValue(null);
  mockedLoadPerformanceLogSummary.mockResolvedValue({
    enabled: false,
    logDirectory: '',
    fileCount: 0,
    totalSize: 0,
  });
  mockedLoadRuntimeVersion.mockResolvedValue({ currentVersion: '1.2.3', startTimestamp: 1_700_000_000 });
});

afterEach(cleanup);

describe('SystemSettingsView', () => {
  it('fails closed before issuing any request when the operator is not root', () => {
    const view = render(<SystemSettingsView role={99} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toContain('Root access required');
    view.rerender(<SystemSettingsView role={Number.NaN} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toContain('Root access required');
    expect(mockedLoad).not.toHaveBeenCalled();
  });

  it('loads options for root and renders the requested section', async () => {
    render(<SystemSettingsView role={100} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    expect(screen.getByRole('status').textContent).toContain('Loading system settings…');
    const input = await screen.findByLabelText('Site name') as HTMLInputElement;
    expect(input.value).toBe('TokenRouter');
    expect(mockedLoad).toHaveBeenCalledOnce();
    expect(mockedLoad.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
  });

  it('mounts structured navigation workflows instead of raw JSON writers', async () => {
    mockedLoad.mockResolvedValueOnce([
      { key: 'HeaderNavModules', value: '{"home":false}' },
      { key: 'SidebarModulesAdmin', value: '{"admin":{"channel":false}}' },
    ]);
    const view = render(<SystemSettingsView role={100} settingsPath="site/header-navigation" onNavigate={vi.fn()} />);

    expect(await screen.findByLabelText('Home')).toHaveProperty('checked', false);
    expect(screen.queryByLabelText('Public module access (JSON)')).toBeNull();

    view.rerender(<SystemSettingsView role={100} settingsPath="site/sidebar-modules" onNavigate={vi.fn()} />);
    expect(await screen.findByLabelText('Channels')).toHaveProperty('checked', false);
    expect(screen.queryByLabelText('Administrator module access (JSON)')).toBeNull();
  });

  it('mounts the atomic SMTP workflow instead of the stale unavailable notice', async () => {
    mockedLoad.mockResolvedValueOnce([]);
    render(<SystemSettingsView role={100} settingsPath="operations/email" onNavigate={vi.fn()} />);

    expect(await screen.findByRole('heading', { name: 'SMTP delivery' })).toBeTruthy();
    expect(screen.getByLabelText('SMTP server')).toBeTruthy();
    expect(screen.getByRole('note').textContent).toContain('SMTP delivery is not configured.');
    expect(screen.queryByText('TokenRouter SMTP credentials are environment configuration')).toBeNull();
  });

  it('mounts the log-maintenance and update-checker workflows', async () => {
    mockedLoad.mockResolvedValue([]);
    const view = render(<SystemSettingsView role={100} settingsPath="operations/logs" onNavigate={vi.fn()} />);

    expect(await screen.findByRole('heading', { name: 'Usage-history cleanup' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Local log files' })).toBeTruthy();
    expect(screen.queryByText('TokenRouter log maintenance uses dedicated performance endpoints')).toBeNull();

    view.rerender(<SystemSettingsView role={100} settingsPath="operations/update-checker" onNavigate={vi.fn()} />);
    expect(await screen.findByRole('heading', { name: 'System maintenance' })).toBeTruthy();
    expect(screen.getByText('1.2.3')).toBeTruthy();
    expect(screen.queryByText('TokenRouter exposes version status but no update-checker mutation endpoint.')).toBeNull();
  });

  it('offers a retry after a contract or network failure', async () => {
    const user = userEvent.setup();
    mockedLoad.mockRejectedValueOnce(new Error('bad response'))
      .mockResolvedValueOnce([{ key: 'SystemName', value: 'Recovered' }]);
    render(<SystemSettingsView role={100} settingsPath="site/system-info" onNavigate={vi.fn()} />);

    expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('Unable to load system settings.'));
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect((screen.getByLabelText('Site name') as HTMLInputElement).value).toBe('Recovered'));
    expect(mockedLoad).toHaveBeenCalledTimes(2);
  });

  it('aborts a pending read when access is revoked', async () => {
    let signal: AbortSignal | undefined;
    let began!: () => void;
    const started = new Promise<void>((resolve) => { began = resolve; });
    mockedLoad.mockImplementationOnce((requestSignal) => {
      signal = requestSignal;
      began();
      return new Promise(() => undefined);
    });
    const view = render(<SystemSettingsView role={100} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    await started;
    view.rerender(<SystemSettingsView role={99} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    expect(signal?.aborted).toBe(true);
    expect(screen.getByRole('alert').textContent).toContain('Root access required');
  });

  it('ignores a stale response after access is revoked and then restored', async () => {
    let releaseFirst!: (value: Array<{ key: string; value: string }>) => void;
    let releaseSecond!: (value: Array<{ key: string; value: string }>) => void;
    mockedLoad.mockImplementationOnce(() => new Promise((resolve) => { releaseFirst = resolve; }));
    mockedLoad.mockImplementationOnce(() => new Promise((resolve) => { releaseSecond = resolve; }));
    const view = render(<SystemSettingsView role={100} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    await vi.waitFor(() => expect(mockedLoad).toHaveBeenCalledOnce());
    view.rerender(<SystemSettingsView role={99} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    view.rerender(<SystemSettingsView role={100} settingsPath="site/system-info" onNavigate={vi.fn()} />);
    await vi.waitFor(() => expect(mockedLoad).toHaveBeenCalledTimes(2));
    releaseSecond([{ key: 'SystemName', value: 'New response' }]);
    await waitFor(() => expect((screen.getByLabelText('Site name') as HTMLInputElement).value).toBe('New response'));
    releaseFirst([{ key: 'SystemName', value: 'Stale response' }]);
    await Promise.resolve();
    expect((screen.getByLabelText('Site name') as HTMLInputElement).value).toBe('New response');
  });
});
