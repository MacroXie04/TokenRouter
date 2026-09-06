// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  deleteStaleSystemInstance,
  deleteStaleSystemInstances,
  listSystemInstances,
  listSystemTasks,
  type SystemInstance,
  type SystemTask,
} from './system-info-api';
import { SystemInfoView } from './SystemInfoView';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, string | number>) => Object.entries(values ?? {}).reduce(
      (text, [name, value]) => text.replace(`{{${name}}}`, String(value)),
      key,
    ),
    i18n: { language: 'en' },
  }),
}));

vi.mock('./system-info-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./system-info-api')>();
  return {
    ...actual,
    listSystemInstances: vi.fn(),
    listSystemTasks: vi.fn(),
    deleteStaleSystemInstance: vi.fn(),
    deleteStaleSystemInstances: vi.fn(),
  };
});

const mockedInstances = vi.mocked(listSystemInstances);
const mockedTasks = vi.mocked(listSystemTasks);
const mockedDeleteOne = vi.mocked(deleteStaleSystemInstance);
const mockedDeleteAll = vi.mocked(deleteStaleSystemInstances);

const instances: SystemInstance[] = [
  {
    nodeName: 'node-online',
    status: 'online',
    staleAfterSeconds: 90,
    startedAt: 1_700_000_000,
    lastSeenAt: 1_700_000_100,
    info: {
      nodeName: 'Primary node',
      hostname: 'host-a',
      isMaster: true,
      runtimeOS: 'linux',
      runtimeArch: 'amd64',
      runtimeVersion: 'go1.25',
      cpuPercent: 12.5,
      memoryPercent: 25,
      storageUsedPercent: 50,
      storageUsedBytes: 1_024,
      storageTotalBytes: 2_048,
    },
  },
  {
    nodeName: 'stale/node',
    status: 'stale',
    staleAfterSeconds: 90,
    startedAt: 1_699_000_000,
    lastSeenAt: 1_699_000_010,
  },
];

const tasks: SystemTask[] = [
  {
    id: 2,
    taskId: 'task-running',
    type: 'log_cleanup',
    status: 'running',
    progress: 42,
    lockedBy: 'runner-a',
    error: '',
    createdAt: 1_700_000_000,
    updatedAt: 1_700_000_100,
  },
  {
    id: 1,
    taskId: 'task-failed',
    type: 'model_update',
    status: 'failed',
    progress: null,
    lockedBy: '',
    error: 'lease expired',
    createdAt: 1_699_000_000,
    updatedAt: 1_699_000_100,
  },
];

beforeEach(() => {
  vi.resetAllMocks();
  mockedInstances.mockResolvedValue(instances);
  mockedTasks.mockResolvedValue(tasks);
  mockedDeleteOne.mockResolvedValue(undefined);
  mockedDeleteAll.mockResolvedValue(1);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('SystemInfoView', () => {
  it('renders root-only cluster and task details with accessible live states', async () => {
    const onNavigate = vi.fn();
    render(<SystemInfoView onNavigate={onNavigate} />);
    const user = userEvent.setup();

    expect(screen.getByRole('heading', { name: 'System information' })).toBeTruthy();
    expect(screen.getByText('Root')).toBeTruthy();
    expect(await screen.findByText('Primary node')).toBeTruthy();
    expect(screen.getByText('host-a')).toBeTruthy();
    expect(screen.getByText('linux/amd64 · go1.25')).toBeTruthy();
    expect(screen.getByText(/CPU 12.5% · RAM 25% · Disk 50%/)).toBeTruthy();
    expect(screen.getByText('Master')).toBeTruthy();
    expect(screen.getByText('Worker')).toBeTruthy();
    expect(await screen.findByText('Log cleanup')).toBeTruthy();
    expect(screen.getByText('Batch upstream model update')).toBeTruthy();
    expect(screen.getByText('runner-a')).toBeTruthy();
    expect(screen.getByText('lease expired')).toBeTruthy();
    expect(screen.getAllByRole('progressbar', { name: 'Task progress' })[0].getAttribute('value')).toBe('42');
    expect(mockedInstances).toHaveBeenCalledTimes(1);
    expect(mockedTasks).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: 'Dashboard' }));
    expect(onNavigate).toHaveBeenCalledWith('/dashboard');
  });

  it('confirms and serializes stale cleanup, then refreshes without exposing online deletion', async () => {
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<SystemInfoView onNavigate={vi.fn()} />);
    const user = userEvent.setup();

    await screen.findByText('Primary node');
    expect(screen.getAllByRole('button', { name: 'Delete stale' })).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Delete stale' }));

    expect(confirmation).toHaveBeenCalledWith('Delete stale instance “stale/node”? Only an expired registration can be removed.');
    expect(mockedDeleteOne).toHaveBeenCalledWith('stale/node');
    await waitFor(() => expect(mockedInstances).toHaveBeenCalledTimes(2));
    expect(screen.getByRole('status').textContent).toContain('Stale instance deleted.');

    await user.click(screen.getByRole('button', { name: 'Delete all stale' }));
    expect(confirmation).toHaveBeenLastCalledWith('Delete 1 stale instances? Only expired registrations will be removed.');
    expect(mockedDeleteAll).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mockedInstances).toHaveBeenCalledTimes(3));
    expect(screen.getByRole('status').textContent).toContain('1 stale instances deleted.');
  });

  it('shows independent empty/error states, redacts failures, and retries only the failed panel', async () => {
    mockedInstances.mockResolvedValueOnce([]);
    mockedTasks.mockRejectedValueOnce(new Error('postgres password=hidden'));
    render(<SystemInfoView onNavigate={vi.fn()} />);
    const user = userEvent.setup();

    expect(await screen.findByText('No instances have reported yet.')).toBeTruthy();
    expect(await screen.findByText('Unable to load system tasks.')).toBeTruthy();
    expect(screen.queryByText(/password=hidden/)).toBeNull();
    expect(screen.queryByText('Unable to load system instances.')).toBeNull();

    mockedTasks.mockResolvedValueOnce([]);
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('No system tasks yet.')).toBeTruthy();
    expect(mockedTasks).toHaveBeenCalledTimes(2);
    expect(mockedInstances).toHaveBeenCalledTimes(1);
  });
});
