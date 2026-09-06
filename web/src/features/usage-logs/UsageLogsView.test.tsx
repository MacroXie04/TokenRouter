// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { User } from '../../shared/api/client';
import { UsageLogsView } from './UsageLogsView';
import {
  loadUsageLogs,
  type CommonUsageLog,
  type DrawingUsageLog,
  type TaskUsageLog,
  type UsageLogQuery,
  type UsageLogResult,
  type UsageLogSection,
} from './usage-logs-api';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) => key.replace(
      /\{\{(\w+)\}\}/gu,
      (_match, name: string) => String(values?.[name] ?? ''),
    ),
  }),
}));

vi.mock('./usage-logs-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./usage-logs-api')>();
  return { ...actual, loadUsageLogs: vi.fn() };
});

const mockedLoad = vi.mocked(loadUsageLogs);
const adminUser: User = {
  id: 1, username: 'root', display_name: 'Root', role: 10, group: 'default',
  quota: 0, used_quota: 0, request_count: 0,
};
const commonUser: User = { ...adminUser, id: 2, username: 'alice', display_name: 'Alice', role: 1 };

const commonLog: CommonUsageLog = {
  kind: 'common', id: 1, userId: 2, createdAt: 1_700_000_000, type: 2, username: 'alice', tokenName: 'primary',
  modelName: 'gpt-4o', quota: 42, promptTokens: 12, completionTokens: 8, useTime: 400,
  streamed: true, channelId: 7, channelName: 'channel-seven', group: 'default', requestId: 'request-1',
  upstreamRequestId: 'upstream-1', content: 'Request completed safely.', ip: '192.0.2.1',
  billing: {
    upstreamModelName: 'gpt-4o-2024', firstResponseTime: 120, cacheTokens: 4,
    audioInputTokens: 0, audioOutputTokens: 0, imageOutputTokens: 0,
    modelRatio: 1.5, completionRatio: 2, groupRatio: 1, userGroupRatio: -1,
    billingMode: 'standard', matchedTier: '', billingSource: 'local',
  },
};
const drawingLog: DrawingUsageLog = {
  kind: 'drawing', id: 2, userId: 3, channelId: 4, drawingId: 'mj-1', action: 'IMAGINE',
  submitTime: 1_700_000_000_000, status: 'SUCCESS', progress: '100%', quota: 5,
  contentURL: '/mj/image/mj-1', prompt: 'Draw a safe landscape', promptEnglish: 'Translated landscape prompt',
  failReason: '', startTime: 1_700_000_000_100, finishTime: 1_700_000_000_900,
};
const taskLog: TaskUsageLog = {
  kind: 'task', id: 3, userId: 4, username: 'bob', channelId: 8, taskId: 'task-1', platform: 'video',
  action: 'generate', submitTime: 1_700_000_000, status: 'SUCCESS', progress: '100%', quota: 7,
  contentURL: '/v1/videos/task-1/content',
  group: 'default', input: 'Animate the landscape', upstreamModelName: 'video-upstream',
  originModelName: 'video', failReason: '', startTime: 1_700_000_001, finishTime: 1_700_000_030,
};

function result(items: Array<CommonUsageLog | DrawingUsageLog | TaskUsageLog>, query?: UsageLogQuery): UsageLogResult {
  return {
    items,
    page: query?.page ?? 1,
    pageSize: query?.pageSize ?? 20,
    total: items.length,
    stats: items[0]?.kind === 'common' ? { quota: 100, rpm: 2, tpm: 80 } : null,
  };
}

function renderLogs(section: UsageLogSection, user = adminUser, onNavigate = vi.fn()) {
  return {
    onNavigate,
    ...render(<UsageLogsView user={user} section={section} onNavigate={onNavigate} />),
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mockedLoad.mockImplementation(async (section, _admin, query) => {
    if (section === 'common') return { ...result([commonLog], query), total: 21 };
    if (section === 'drawing') return result([drawingLog], query);
    return result([taskLog], query);
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('UsageLogsView', () => {
  it('renders common administrator statistics, role-sensitive columns, exact navigation, and no private details', async () => {
    const { onNavigate } = renderLogs('common');
    const user = userEvent.setup();

    expect(await screen.findByText('gpt-4o')).toBeTruthy();
    expect(screen.getByLabelText('Common usage statistics').textContent).toContain('100');
    expect(screen.getByRole('columnheader', { name: 'User' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Channel' })).toBeTruthy();
    expect(screen.getByRole('search', { name: 'Filter usage logs' })).toBeTruthy();
    expect(screen.queryByText(/private|password|secret/i)).toBeNull();

    await user.click(screen.getByRole('link', { name: 'Drawing' }));
    expect(onNavigate).toHaveBeenCalledWith('/usage-logs/drawing');
    expect(screen.getByRole('link', { name: 'Common' }).getAttribute('aria-current')).toBe('page');
  });

  it('keeps self-service common history scoped and omits administrator filters and columns', async () => {
    renderLogs('common', commonUser);
    expect(await screen.findByText('gpt-4o')).toBeTruthy();
    expect(mockedLoad).toHaveBeenCalledWith('common', false, expect.any(Object), expect.any(AbortSignal));
    expect(screen.getByText('Your account only')).toBeTruthy();
    expect(screen.queryByLabelText('Username')).toBeNull();
    expect(screen.queryByLabelText('Channel ID')).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'User' })).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Channel' })).toBeNull();
  });

  it('lets administrators switch atomically between all-account and self scope', async () => {
    const user = userEvent.setup();
    renderLogs('common');
    expect(await screen.findByText('gpt-4o')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'My account' }));
    await waitFor(() => expect(mockedLoad).toHaveBeenLastCalledWith(
      'common', false, expect.objectContaining({ page: 1 }), expect.any(AbortSignal),
    ));
    expect(screen.getByText('Your account only')).toBeTruthy();
    expect(screen.queryByLabelText('Username')).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'User' })).toBeNull();

    await user.click(screen.getByRole('button', { name: 'All accounts' }));
    await waitFor(() => expect(mockedLoad).toHaveBeenLastCalledWith(
      'common', true, expect.objectContaining({ page: 1 }), expect.any(AbortSignal),
    ));
    expect(screen.getByText('Administrator scope')).toBeTruthy();
  });

  it('masks and restores the reference-sensitive common values locally', async () => {
    const user = userEvent.setup();
    renderLogs('common');
    expect(await screen.findByText('primary')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'Hide sensitive values' }));
    expect(screen.queryByText('primary')).toBeNull();
    expect(screen.queryByText('alice')).toBeNull();
    expect(screen.queryByText('default')).toBeNull();
    expect(screen.getAllByText('••••••').length).toBeGreaterThanOrEqual(4);

    await user.click(screen.getByRole('button', { name: 'Show sensitive values' }));
    expect(screen.getByText('primary')).toBeTruthy();
    expect(screen.getByText('alice')).toBeTruthy();
  });

  it('shows a bounded common-log breakdown and follows the sensitive-value toggle', async () => {
    const user = userEvent.setup();
    renderLogs('common');
    const table = await screen.findByRole('table', { name: 'Common logs' });
    await user.click(within(table).getByRole('button', { name: 'View details' }));
    const dialog = screen.getByRole('dialog', { name: 'Usage log details' });
    expect(within(dialog).getByText('gpt-4o-2024')).toBeTruthy();
    expect(within(dialog).getByText('upstream-1')).toBeTruthy();
    expect(within(dialog).getByText('Request completed safely.')).toBeTruthy();
    expect(within(dialog).getByText('120 ms')).toBeTruthy();
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));

    await user.click(screen.getByRole('button', { name: 'Hide sensitive values' }));
    await user.click(within(table).getByRole('button', { name: 'View details' }));
    expect(within(screen.getByRole('dialog')).queryByText('upstream-1')).toBeNull();
    expect(within(screen.getByRole('dialog')).queryByText('Request completed safely.')).toBeNull();
  });

  it('previews drawing prompts and task failures without using provider URLs', async () => {
    const user = userEvent.setup();
    renderLogs('drawing');
    let table = await screen.findByRole('table', { name: 'Drawing logs' });
    await user.click(within(table).getByRole('button', { name: 'View details' }));
    let dialog = screen.getByRole('dialog', { name: 'Drawing details' });
    expect(within(dialog).getByText('Draw a safe landscape')).toBeTruthy();
    expect(within(dialog).getByRole('img', { name: 'Generated image' }).getAttribute('src')).toBe('/mj/image/mj-1');
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    cleanup();

    mockedLoad.mockImplementation(async (_section, _admin, query) => result([{
      ...taskLog, status: 'FAILURE', contentURL: null, failReason: 'Provider rejected the safe request.',
    }], query));
    renderLogs('task');
    table = await screen.findByRole('table', { name: 'Task logs' });
    await user.click(within(table).getByRole('button', { name: 'View details' }));
    dialog = screen.getByRole('dialog', { name: 'Task details' });
    expect(within(dialog).getByText('Animate the landscape')).toBeTruthy();
    expect(within(dialog).getByText('Provider rejected the safe request.')).toBeTruthy();
    expect(within(dialog).queryByRole('link', { name: 'View content' })).toBeNull();
  });

  it.each([
    ['drawing', 'mj-1', '/mj/image/mj-1', 'Drawing ID'],
    ['task', 'task-1', '/v1/videos/task-1/content', 'Task ID'],
  ] as const)('renders the %s workflow with only a safe gateway content link', async (section, identifier, href, filterLabel) => {
    renderLogs(section);
    expect(await screen.findByText(identifier)).toBeTruthy();
    expect(screen.getByLabelText(filterLabel)).toBeTruthy();
    const resultLink = screen.getByRole('link', { name: section === 'drawing' ? 'View result' : 'View content' });
    expect(resultLink.getAttribute('href')).toBe(href);
    expect(resultLink.getAttribute('href')).not.toMatch(/^https?:/u);
  });

  it('applies bounded common filters, page size, and pagination while serializing loads', async () => {
    const user = userEvent.setup();
    renderLogs('common');
    await screen.findByText('gpt-4o');
    const search = screen.getByRole('search', { name: 'Filter usage logs' });
    await user.selectOptions(within(search).getByLabelText('Type'), '2');
    await user.type(within(search).getByLabelText('Model'), 'gpt-4o-mini');
    await user.type(within(search).getByLabelText('Token name'), 'primary');
    await user.type(within(search).getByLabelText('Group'), 'vip');
    await user.type(within(search).getByLabelText('Username'), 'alice');
    await user.type(within(search).getByLabelText('Channel ID'), '7');
    await user.type(within(search).getByLabelText('Request ID'), 'req-2');
    await user.type(within(search).getByLabelText('Upstream request ID'), 'up-2');
    fireEvent.change(within(search).getByLabelText('Start time'), { target: { value: '2024-01-01T00:00' } });
    fireEvent.change(within(search).getByLabelText('End time'), { target: { value: '2024-01-02T00:00' } });
    await user.selectOptions(within(search).getByLabelText('Page size'), '10');
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));

    await waitFor(() => expect(mockedLoad).toHaveBeenLastCalledWith('common', true, expect.objectContaining({
      page: 1, pageSize: 10, type: 2, model: 'gpt-4o-mini', token: 'primary', group: 'vip',
      username: 'alice', channel: '7', requestId: 'req-2', upstreamRequestId: 'up-2',
      startTimestamp: expect.any(Number), endTimestamp: expect.any(Number),
    }), expect.any(AbortSignal)));

    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedLoad).toHaveBeenLastCalledWith('common', true, expect.objectContaining({ page: 2 }), expect.any(AbortSignal)));
  });

  it('validates the time range and channel before issuing another request', async () => {
    renderLogs('task');
    await screen.findByText('task-1');
    const calls = mockedLoad.mock.calls.length;
    const search = screen.getByRole('search', { name: 'Filter usage logs' });
    fireEvent.change(within(search).getByLabelText('Channel ID'), { target: { value: '-1' } });
    fireEvent.change(within(search).getByLabelText('Start time'), { target: { value: '2024-02-02T00:00' } });
    fireEvent.change(within(search).getByLabelText('End time'), { target: { value: '2024-01-01T00:00' } });
    fireEvent.submit(search);
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'Check the time range and channel ID.');
    expect(mockedLoad).toHaveBeenCalledTimes(calls);
  });

  it('renders loading, empty, redacted failure, retry, and serialized-action states', async () => {
    let resolveInitial: ((value: UsageLogResult) => void) | undefined;
    mockedLoad.mockImplementationOnce(() => new Promise((resolve) => { resolveInitial = resolve; }));
    renderLogs('drawing');
    expect(screen.getByRole('status')).toHaveProperty('textContent', 'Loading usage logs…');
    expect(screen.getByRole('button', { name: 'Apply filters' })).toHaveProperty('disabled', true);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Apply filters' }));
    expect(mockedLoad).toHaveBeenCalledTimes(1);

    await act(async () => resolveInitial?.({ ...result([]), stats: null }));
    expect(await screen.findByText('No usage logs match these filters.')).toBeTruthy();
    cleanup();

    mockedLoad.mockRejectedValueOnce(new Error('database password and provider secret'));
    renderLogs('drawing');
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveProperty('textContent', 'Unable to load usage logs.');
    expect(screen.queryByText(/database password|provider secret/i)).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('mj-1')).toBeTruthy();
  });

  it('aborts obsolete section loads, ignores late results, and aborts on unmount', async () => {
    let resolveCommon: ((value: UsageLogResult) => void) | undefined;
    let firstSignal: AbortSignal | undefined;
    mockedLoad.mockImplementationOnce((_section, _admin, _query, signal) => {
      firstSignal = signal;
      return new Promise((resolve) => { resolveCommon = resolve; });
    });
    const view = renderLogs('common');
    view.rerender(<UsageLogsView user={adminUser} section="task" onNavigate={view.onNavigate} />);

    expect(await screen.findByText('task-1')).toBeTruthy();
    expect(firstSignal?.aborted).toBe(true);
    await act(async () => resolveCommon?.(result([{ ...commonLog, modelName: 'late-private-model' }])));
    expect(screen.queryByText('late-private-model')).toBeNull();

    const latestSignal = mockedLoad.mock.calls.at(-1)?.[3];
    view.unmount();
    expect(latestSignal?.aborted).toBe(true);
  });
});
