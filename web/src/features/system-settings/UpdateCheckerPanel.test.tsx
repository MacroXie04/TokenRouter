// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  loadLatestTokenRouterRelease,
  loadRuntimeVersion,
} from './system-settings-api';
import { UpdateCheckerPanel } from './UpdateCheckerPanel';

vi.mock('./system-settings-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./system-settings-api')>();
  return {
    ...actual,
    loadLatestTokenRouterRelease: vi.fn(),
    loadRuntimeVersion: vi.fn(),
  };
});
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

const mockedLoadRelease = vi.mocked(loadLatestTokenRouterRelease);
const mockedLoadRuntime = vi.mocked(loadRuntimeVersion);

beforeEach(() => {
  vi.resetAllMocks();
  mockedLoadRuntime.mockResolvedValue({ currentVersion: '1.2.3', startTimestamp: 1_700_000_000 });
  mockedLoadRelease.mockResolvedValue({
    tagName: 'v1.2.3',
    name: 'TokenRouter 1.2.3',
    notes: 'Safe release notes.',
    url: 'https://github.com/MacroXie04/TokenRouter/releases/tag/v1.2.3',
    publishedAt: '2026-09-01T00:00:00Z',
  });
});

afterEach(cleanup);

describe('UpdateCheckerPanel', () => {
  it('shows runtime metadata and reports an exact current release', async () => {
    const user = userEvent.setup();
    render(<UpdateCheckerPanel />);

    expect(await screen.findByText('1.2.3')).toBeTruthy();
    expect(screen.getByText('Current version')).toBeTruthy();
    expect(screen.getByText('Uptime since')).toBeTruthy();
    expect(mockedLoadRuntime.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
    await user.click(screen.getByRole('button', { name: 'Check for updates' }));

    await waitFor(() => expect(mockedLoadRelease).toHaveBeenCalledOnce());
    expect(mockedLoadRelease.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
    expect(await screen.findByText('You are running the latest version (v1.2.3).')).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('renders a mismatched release as inert text and a fixed safe link', async () => {
    mockedLoadRelease.mockResolvedValueOnce({
      tagName: 'v1.3.0',
      name: 'TokenRouter 1.3.0',
      notes: '<script>alert("never")</script>\nSecurity fixes.',
      url: 'https://github.com/MacroXie04/TokenRouter/releases/tag/v1.3.0',
      publishedAt: '2026-09-02T00:00:00Z',
    });
    const user = userEvent.setup();
    render(<UpdateCheckerPanel />);
    await screen.findByText('1.2.3');
    await user.click(screen.getByRole('button', { name: 'Check for updates' }));

    const dialog = await screen.findByRole('dialog', { name: 'New version available: v1.3.0' });
    expect(dialog.textContent).toContain('<script>alert("never")</script>');
    expect(dialog.querySelector('script')).toBeNull();
    const link = screen.getByRole('link', { name: 'Open release' });
    expect(link.getAttribute('href')).toBe('https://github.com/MacroXie04/TokenRouter/releases/tag/v1.3.0');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toBe('noopener noreferrer');
    await user.click(screen.getByRole('button', { name: 'Close' }));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('redacts runtime and release failures while keeping each action retryable', async () => {
    mockedLoadRuntime.mockRejectedValueOnce(new Error('private runtime failure'));
    const user = userEvent.setup();
    render(<UpdateCheckerPanel />);

    expect(await screen.findByText('Unable to load version information.')).toBeTruthy();
    expect(screen.queryByText('private runtime failure')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('1.2.3')).toBeTruthy();

    mockedLoadRelease.mockRejectedValueOnce(new Error('private release failure'));
    await user.click(screen.getByRole('button', { name: 'Check for updates' }));
    expect(await screen.findByText('Unable to check for updates.')).toBeTruthy();
    expect(screen.queryByText('private release failure')).toBeNull();
  });

  it('aborts pending runtime and release reads when unmounted', async () => {
    let runtimeSignal: AbortSignal | undefined;
    let releaseSignal: AbortSignal | undefined;
    mockedLoadRuntime.mockImplementationOnce((signal) => {
      runtimeSignal = signal;
      return Promise.resolve({ currentVersion: '1.2.3', startTimestamp: 1_700_000_000 });
    });
    mockedLoadRelease.mockImplementationOnce((signal) => {
      releaseSignal = signal;
      return new Promise(() => undefined);
    });
    const user = userEvent.setup();
    const view = render(<UpdateCheckerPanel />);
    await screen.findByText('1.2.3');
    await user.click(screen.getByRole('button', { name: 'Check for updates' }));
    await waitFor(() => expect(mockedLoadRelease).toHaveBeenCalledOnce());
    view.unmount();
    expect(runtimeSignal?.aborted).toBe(true);
    expect(releaseSignal?.aborted).toBe(true);
  });
});
