// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RelayTokenManager } from './RelayTokenManager';
import {
  createToken,
  deleteToken,
  deleteTokens,
  listTokens,
  loadTokenAutoGroups,
  loadTokenGroups,
  loadTokenModels,
  searchTokens,
  updateToken,
  updateTokenStatus,
  type RelayToken,
} from './token-api';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./token-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./token-api')>();
  return {
    ...actual,
    createToken: vi.fn(),
    deleteToken: vi.fn(),
    deleteTokens: vi.fn(),
    listTokens: vi.fn(),
    loadTokenAutoGroups: vi.fn(),
    loadTokenGroups: vi.fn(),
    loadTokenModels: vi.fn(),
    searchTokens: vi.fn(),
    updateToken: vi.fn(),
    updateTokenStatus: vi.fn(),
  };
});

const mockedCreate = vi.mocked(createToken);
const mockedDelete = vi.mocked(deleteToken);
const mockedDeleteBatch = vi.mocked(deleteTokens);
const mockedList = vi.mocked(listTokens);
const mockedAutoGroups = vi.mocked(loadTokenAutoGroups);
const mockedGroups = vi.mocked(loadTokenGroups);
const mockedModels = vi.mocked(loadTokenModels);
const mockedSearch = vi.mocked(searchTokens);
const mockedUpdate = vi.mocked(updateToken);
const mockedStatus = vi.mocked(updateTokenStatus);

const primary: RelayToken = {
  id: 7,
  name: 'Primary key',
  maskedKey: 'sk-a**********1234',
  status: 1,
  remainQuota: 5_000,
  usedQuota: 100,
  unlimitedQuota: false,
  expiredTime: -1,
  createdTime: 1_700_000_000,
  accessedTime: 1_700_000_100,
  group: 'default',
  autoGroups: [],
  crossGroupRetry: false,
  modelLimitsEnabled: true,
  modelLimits: ['gpt-4o'],
  allowIps: '192.0.2.1',
};

const secondary: RelayToken = {
  ...primary,
  id: 8,
  name: 'Backup key',
  maskedKey: 'sk-b**********5678',
  status: 2,
  group: 'auto',
  autoGroups: ['vip', 'default'],
};

function tokenPage(items: RelayToken[] = [primary, secondary], total = 21) {
  return { items, total, page: 1, pageSize: 20 };
}

beforeEach(() => {
  vi.resetAllMocks();
  mockedList.mockResolvedValue(tokenPage());
  mockedSearch.mockResolvedValue(tokenPage([primary], 1));
  mockedGroups.mockResolvedValue([
    { name: 'auto', description: 'Automatic', ratio: '自动' },
    { name: 'default', description: 'Default', ratio: '1' },
    { name: 'vip', description: 'VIP', ratio: '2' },
  ]);
  mockedAutoGroups.mockResolvedValue({ groups: ['vip', 'default'], maxCount: 2 });
  mockedModels.mockResolvedValue(['gpt-4o', 'o3-mini']);
  mockedCreate.mockResolvedValue();
  mockedUpdate.mockResolvedValue();
  mockedStatus.mockResolvedValue();
  mockedDelete.mockResolvedValue();
  mockedDeleteBatch.mockResolvedValue(2);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('RelayTokenManager', () => {
  it('renders masked keys, searches by name, and paginates with keyboard-native controls', async () => {
    render(<RelayTokenManager />);
    const user = userEvent.setup();

    expect(await screen.findByText('Primary key')).toBeTruthy();
    expect(screen.getByRole('table').getAttribute('aria-busy')).toBe('false');
    expect(screen.getByText('sk-a**********1234')).toBeTruthy();
    expect(screen.queryByText(/full-secret/)).toBeNull();
    expect(mockedList).toHaveBeenCalledWith(1, expect.any(AbortSignal));

    const search = screen.getByRole('search', { name: 'Search API keys' });
    await user.type(within(search).getByLabelText('Search by key name'), 'Primary');
    await user.click(within(search).getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(mockedSearch).toHaveBeenCalledWith('Primary', 1, expect.any(AbortSignal)));

    await user.click(within(search).getByRole('button', { name: 'Clear' }));
    await waitFor(() => expect(mockedList).toHaveBeenCalledTimes(2));
    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedList).toHaveBeenLastCalledWith(2, expect.any(AbortSignal)));
  });

  it('creates a bounded key policy with quota, auto-group, model, expiry, and IP controls', async () => {
    render(<RelayTokenManager />);
    const user = userEvent.setup();
    await screen.findByText('Primary key');
    await user.click(screen.getByRole('button', { name: 'Create key' }));
    const form = screen.getByRole('heading', { name: 'Create API key' }).closest('form') as HTMLFormElement;

    await user.type(within(form).getByLabelText('Name'), 'Workload key');
    await user.click(within(form).getByLabelText('Unlimited quota'));
    await user.type(within(form).getByLabelText('Quota'), '9000');
    await user.selectOptions(within(form).getByLabelText('Routing group'), 'auto');
    await waitFor(() => expect(mockedModels).toHaveBeenCalledWith('auto', expect.any(AbortSignal)));
    await user.click(within(form).getByLabelText('vip'));
    await user.click(within(form).getByLabelText('default'));
    await user.click(within(form).getByLabelText('Retry across selected groups'));
    await user.click(within(form).getByLabelText('Limit this key to selected models'));
    await user.selectOptions(within(form).getByLabelText('Allowed models'), ['gpt-4o', 'o3-mini']);
    await user.type(within(form).getByLabelText('IP allowlist'), '192.0.2.1{enter}2001:db8::/32');
    await user.click(within(form).getByRole('button', { name: 'Save API key' }));

    await waitFor(() => expect(mockedCreate).toHaveBeenCalledWith({
      name: 'Workload key',
      expiredTime: -1,
      remainQuota: 9_000,
      unlimitedQuota: false,
      modelLimitsEnabled: true,
      modelLimits: ['gpt-4o', 'o3-mini'],
      allowIps: '192.0.2.1,2001:db8::/32',
      group: 'auto',
      autoGroups: ['vip', 'default'],
      crossGroupRetry: true,
    }));
    expect(screen.queryByText(/sk-[A-Za-z0-9]{10}/)).toBeNull();
  });

  it('edits the complete non-secret policy and isolates status-only updates', async () => {
    render(<RelayTokenManager />);
    const user = userEvent.setup();
    const row = (await screen.findByText('Primary key')).closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Edit' }));
    const form = screen.getByRole('heading', { name: 'Edit API key' }).closest('form') as HTMLFormElement;
    const name = within(form).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'Renamed key');
    await user.click(within(form).getByRole('button', { name: 'Save API key' }));
    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledWith(7, expect.objectContaining({
      name: 'Renamed key',
      group: 'default',
      modelLimits: ['gpt-4o'],
      allowIps: '192.0.2.1',
    })));

    await user.click(within(row).getByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(mockedStatus).toHaveBeenCalledWith(7, 2));
    expect(screen.getByText('API key disabled.')).toBeTruthy();
  });

  it('requires confirmation for single and batch deletion and submits only selected IDs', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    render(<RelayTokenManager />);
    const user = userEvent.setup();
    const primaryRow = (await screen.findByText('Primary key')).closest('tr') as HTMLTableRowElement;

    await user.click(within(primaryRow).getByRole('button', { name: 'Delete' }));
    expect(mockedDelete).not.toHaveBeenCalled();
    await user.click(within(primaryRow).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(mockedDelete).toHaveBeenCalledWith(7));

    await user.click(screen.getByLabelText('Select Primary key'));
    await user.click(screen.getByLabelText('Select Backup key'));
    await user.click(screen.getByRole('button', { name: 'Delete selected' }));
    await waitFor(() => expect(mockedDeleteBatch).toHaveBeenCalledWith([7, 8]));
    expect(confirm).toHaveBeenCalledWith('Delete API key “Primary key”?');
    expect(confirm).toHaveBeenCalledWith('Delete 2 selected API keys?');
  });

  it('shows loading, empty, validation, metadata, and redacted API failures', async () => {
    let resolveList: ((value: ReturnType<typeof tokenPage>) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    render(<RelayTokenManager />);
    expect(screen.getByRole('status').textContent).toBe('Loading API keys…');
    await act(async () => resolveList?.(tokenPage([], 0)));
    expect(await screen.findByText('No API keys yet.')).toBeTruthy();
    cleanup();

    mockedList.mockRejectedValueOnce(new Error('private database and key detail'));
    mockedGroups.mockRejectedValueOnce(new Error('private group detail'));
    render(<RelayTokenManager />);
    expect((await screen.findByRole('alert')).textContent).toContain('Unable to load API keys.');
    expect(await screen.findByText('Group and model suggestions are unavailable.')).toBeTruthy();
    expect(screen.queryByText(/private database|private group/)).toBeNull();
    cleanup();

    render(<RelayTokenManager />);
    const user = userEvent.setup();
    await screen.findByText('Primary key');
    const search = screen.getByRole('search', { name: 'Search API keys' });
    await user.type(within(search).getByLabelText('Search by key name'), 'bad%search');
    await user.click(within(search).getByRole('button', { name: 'Search' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Search text cannot contain percent signs.');
    expect(mockedSearch).not.toHaveBeenCalled();
  });

  it('ignores list and metadata responses that complete after unmount', async () => {
    let resolveList: ((value: ReturnType<typeof tokenPage>) => void) | undefined;
    let resolveGroups: ((value: Awaited<ReturnType<typeof loadTokenGroups>>) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    mockedGroups.mockImplementationOnce(() => new Promise((resolve) => { resolveGroups = resolve; }));
    const rendered = render(<RelayTokenManager />);
    rendered.unmount();

    await act(async () => {
      resolveList?.(tokenPage([{ ...primary, name: 'Late key' }], 1));
      resolveGroups?.([{ name: 'late-group', description: 'Late', ratio: '1' }]);
      await Promise.resolve();
    });
    expect(screen.queryByText(/Late key|late-group/)).toBeNull();
  });
});
