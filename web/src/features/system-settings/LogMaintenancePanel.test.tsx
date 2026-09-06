// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LogMaintenancePanel } from './LogMaintenancePanel';
import {
  cleanupPerformanceLogFiles,
  loadCurrentLogCleanupTask,
  loadLogCleanupTask,
  loadPerformanceLogSummary,
  startLogCleanupTask,
} from './system-settings-api';

vi.mock('./system-settings-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./system-settings-api')>();
  return {
    ...actual,
    cleanupPerformanceLogFiles: vi.fn(),
    loadCurrentLogCleanupTask: vi.fn(),
    loadLogCleanupTask: vi.fn(),
    loadPerformanceLogSummary: vi.fn(),
    startLogCleanupTask: vi.fn(),
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

const mockedCleanupFiles = vi.mocked(cleanupPerformanceLogFiles);
const mockedLoadCurrent = vi.mocked(loadCurrentLogCleanupTask);
const mockedLoadTask = vi.mocked(loadLogCleanupTask);
const mockedLoadFiles = vi.mocked(loadPerformanceLogSummary);
const mockedStartCleanup = vi.mocked(startLogCleanupTask);

const pendingTask = {
  id: 1,
  taskId: '0123456789abcdef0123456789abcdef',
  status: 'pending' as const,
  targetTimestamp: 1_767_326_640,
  progress: 0,
  processed: 0,
  total: 0,
  deletedCount: null,
};

beforeEach(() => {
  vi.resetAllMocks();
  mockedLoadFiles.mockResolvedValue({
    enabled: true,
    logDirectory: '/var/log/tokenrouter',
    fileCount: 3,
    totalSize: 4_096,
    oldestTime: '2026-01-01T00:00:00Z',
    newestTime: '2026-01-02T00:00:00Z',
  });
  mockedLoadCurrent.mockResolvedValue(null);
  mockedCleanupFiles.mockResolvedValue({ deletedCount: 2, freedBytes: 2_048, failedCount: 0 });
  mockedStartCleanup.mockResolvedValue(pendingTask);
  mockedLoadTask.mockResolvedValue(pendingTask);
});

afterEach(cleanup);

describe('LogMaintenancePanel', () => {
  it('renders bounded file metadata and confirms file deletion before calling the root API', async () => {
    const user = userEvent.setup();
    render(<LogMaintenancePanel />);

    expect(await screen.findByText('/var/log/tokenrouter')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Usage-history cleanup' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Local log files' })).toBeTruthy();
    await user.selectOptions(screen.getByLabelText('Retention mode'), 'by_days');
    await user.clear(screen.getByLabelText('Days to keep'));
    await user.type(screen.getByLabelText('Days to keep'), '30');
    await user.click(screen.getByRole('button', { name: 'Clean local log files' }));

    expect(mockedCleanupFiles).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Clean local log files?' }).textContent)
      .toContain('older than 30 days');
    await user.click(screen.getByRole('button', { name: 'Confirm cleanup' }));

    await waitFor(() => expect(mockedCleanupFiles).toHaveBeenCalledOnce());
    expect(mockedCleanupFiles.mock.calls[0][0]).toBe('by_days');
    expect(mockedCleanupFiles.mock.calls[0][1]).toBe(30);
    expect(mockedCleanupFiles.mock.calls[0][2]).toBeInstanceOf(AbortSignal);
    expect(await screen.findByText('2 local log files deleted; 2 KB freed.')).toBeTruthy();
    expect(mockedLoadFiles).toHaveBeenCalledTimes(2);
  });

  it('validates a past timestamp and confirms durable history cleanup', async () => {
    const user = userEvent.setup();
    render(<LogMaintenancePanel />);
    await screen.findByText('/var/log/tokenrouter');
    const target = '2026-01-02T03:04';
    fireEvent.change(screen.getByLabelText('Delete entries created before'), { target: { value: target } });
    await user.click(screen.getByRole('button', { name: 'Start usage-history cleanup' }));

    expect(mockedStartCleanup).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Delete usage history?' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Confirm cleanup' }));

    await waitFor(() => expect(mockedStartCleanup).toHaveBeenCalledOnce());
    expect(mockedStartCleanup.mock.calls[0][0]).toBe(Math.floor(new Date(target).getTime() / 1_000));
    expect(mockedStartCleanup.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
    expect(screen.getByText('Usage-history cleanup started.')).toBeTruthy();
    expect(screen.getByText('pending')).toBeTruthy();
  });

  it('fails locally for invalid cleanup values and exposes retryable read failures', async () => {
    mockedLoadFiles.mockRejectedValueOnce(new Error('private file error'));
    mockedLoadCurrent.mockRejectedValueOnce(new Error('private task error'));
    const user = userEvent.setup();
    render(<LogMaintenancePanel />);

    expect(await screen.findByText('Unable to load local log files.')).toBeTruthy();
    expect(screen.getByText('Unable to load cleanup status.')).toBeTruthy();
    expect(screen.queryByText('private file error')).toBeNull();
    expect(screen.queryByText('private task error')).toBeNull();

    await user.click(screen.getAllByRole('button', { name: 'Retry' })[0]);
    await waitFor(() => expect(mockedLoadCurrent).toHaveBeenCalledTimes(2));
    expect(screen.getByText('No cleanup task is running.')).toBeTruthy();
  });

  it('aborts both initial reads when the panel unmounts', async () => {
    let fileSignal: AbortSignal | undefined;
    let taskSignal: AbortSignal | undefined;
    mockedLoadFiles.mockImplementationOnce((signal) => {
      fileSignal = signal;
      return new Promise(() => undefined);
    });
    mockedLoadCurrent.mockImplementationOnce((signal) => {
      taskSignal = signal;
      return new Promise(() => undefined);
    });
    const view = render(<LogMaintenancePanel />);
    await waitFor(() => expect(mockedLoadFiles).toHaveBeenCalledOnce());
    await waitFor(() => expect(mockedLoadCurrent).toHaveBeenCalledOnce());
    view.unmount();
    expect(fileSignal?.aborted).toBe(true);
    expect(taskSignal?.aborted).toBe(true);
  });
});
