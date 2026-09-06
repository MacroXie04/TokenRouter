// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  applyAllUpstreamUpdates,
  applyUpstreamUpdates,
  copyChannel,
  createChannel,
  deleteDisabledChannels,
  deleteChannels,
  deleteChannel,
  deleteOllamaModel,
  detectAllUpstreamUpdates,
  detectUpstreamUpdates,
  discoverDraftModels,
  fetchChannelDetail,
  fetchChannelModels,
  loadCodexResetCredits,
  loadCodexUsage,
  loadEnabledModels,
  loadMultiKeyPage,
  loadOllamaVersion,
  loadTagModels,
  manageMultiKey,
  pullOllamaModelStream,
  refreshAllChannelBalances,
  refreshChannelBalance,
  refreshCodexCredential,
  repairChannelAbilities,
  resetCodexUsage,
  searchChannels,
  setChannelsStatus,
  setChannelsTag,
  setChannelStatus,
  setTagChannelsStatus,
  testChannel,
  testAllChannels,
  updateTagChannels,
  updateChannel,
} from './channel-api';
import { ChannelAdminView, type ChannelAdminViewProps } from './ChannelAdminView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./channel-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./channel-api')>();
  return {
    ...actual,
    applyAllUpstreamUpdates: vi.fn(),
    applyUpstreamUpdates: vi.fn(),
    copyChannel: vi.fn(),
    createChannel: vi.fn(),
    deleteDisabledChannels: vi.fn(),
    deleteChannels: vi.fn(),
    deleteChannel: vi.fn(),
    deleteOllamaModel: vi.fn(),
    detectAllUpstreamUpdates: vi.fn(),
    detectUpstreamUpdates: vi.fn(),
    discoverDraftModels: vi.fn(),
    fetchChannelDetail: vi.fn(),
    fetchChannelModels: vi.fn(),
    loadCodexResetCredits: vi.fn(),
    loadCodexUsage: vi.fn(),
    loadEnabledModels: vi.fn(),
    loadMultiKeyPage: vi.fn(),
    loadOllamaVersion: vi.fn(),
    loadTagModels: vi.fn(),
    manageMultiKey: vi.fn(),
    pullOllamaModelStream: vi.fn(),
    refreshAllChannelBalances: vi.fn(),
    refreshChannelBalance: vi.fn(),
    refreshCodexCredential: vi.fn(),
    repairChannelAbilities: vi.fn(),
    resetCodexUsage: vi.fn(),
    searchChannels: vi.fn(),
    setChannelsStatus: vi.fn(),
    setChannelsTag: vi.fn(),
    setChannelStatus: vi.fn(),
    setTagChannelsStatus: vi.fn(),
    testChannel: vi.fn(),
    testAllChannels: vi.fn(),
    updateTagChannels: vi.fn(),
    updateChannel: vi.fn(),
  };
});

const mockedCreate = vi.mocked(createChannel);
const mockedApplyAllUpdates = vi.mocked(applyAllUpstreamUpdates);
const mockedApplyUpdates = vi.mocked(applyUpstreamUpdates);
const mockedCopy = vi.mocked(copyChannel);
const mockedDeleteDisabled = vi.mocked(deleteDisabledChannels);
const mockedDeleteChannels = vi.mocked(deleteChannels);
const mockedDelete = vi.mocked(deleteChannel);
const mockedDeleteOllama = vi.mocked(deleteOllamaModel);
const mockedDetectAllUpdates = vi.mocked(detectAllUpstreamUpdates);
const mockedDetectUpdates = vi.mocked(detectUpstreamUpdates);
const mockedDiscoverDraftModels = vi.mocked(discoverDraftModels);
const mockedFetchDetail = vi.mocked(fetchChannelDetail);
const mockedFetchModels = vi.mocked(fetchChannelModels);
const mockedLoadCodexCredits = vi.mocked(loadCodexResetCredits);
const mockedLoadCodexUsage = vi.mocked(loadCodexUsage);
const mockedLoadModels = vi.mocked(loadEnabledModels);
const mockedLoadMultiKeys = vi.mocked(loadMultiKeyPage);
const mockedLoadOllamaVersion = vi.mocked(loadOllamaVersion);
const mockedLoadTagModels = vi.mocked(loadTagModels);
const mockedManageMultiKey = vi.mocked(manageMultiKey);
const mockedPullOllama = vi.mocked(pullOllamaModelStream);
const mockedRefreshAllBalances = vi.mocked(refreshAllChannelBalances);
const mockedRefreshBalance = vi.mocked(refreshChannelBalance);
const mockedRefreshCodex = vi.mocked(refreshCodexCredential);
const mockedRepair = vi.mocked(repairChannelAbilities);
const mockedResetCodex = vi.mocked(resetCodexUsage);
const mockedSearch = vi.mocked(searchChannels);
const mockedSetChannelsStatus = vi.mocked(setChannelsStatus);
const mockedSetChannelsTag = vi.mocked(setChannelsTag);
const mockedSetStatus = vi.mocked(setChannelStatus);
const mockedSetTagStatus = vi.mocked(setTagChannelsStatus);
const mockedTest = vi.mocked(testChannel);
const mockedTestAll = vi.mocked(testAllChannels);
const mockedUpdateTag = vi.mocked(updateTagChannels);
const mockedUpdate = vi.mocked(updateChannel);

const primaryChannel = {
  id: 7,
  name: 'Primary channel',
  type: 1,
  status: 1,
  models: 'gpt-4o,gpt-4.1,o3-mini,o1',
  group: 'default',
  tag: 'fast',
  remark: 'reviewed',
  balance: 12.5,
  balanceUpdatedTime: 1_700_000_000,
  isMultiKey: false,
  upstreamCheckEnabled: true,
  pendingAddModels: [],
  pendingRemoveModels: [],
};

const primaryDetail = {
  id: 7,
  name: 'Fresh primary channel',
  type: 1,
  models: 'gpt-4o,gpt-4.1,o3-mini,o1',
  group: 'default',
  tag: 'fast',
  remark: 'fresh detail',
  priority: 2,
  weight: 3,
  testModel: 'gpt-4o',
  autoBan: 1 as const,
  modelMapping: '{"client":"upstream"}',
  statusCodeMapping: '{"429":500}',
};

const primarySensitiveDetail = {
  baseUrl: 'https://gateway.example.test',
  organization: 'org-test',
  other: '',
  paramOverride: '{"temperature":0}',
  headerOverride: '{"X-Test":"yes"}',
  providerSettings: {
    settingSource: '{"future_setting":true}',
    settingsSource: '{"future_provider":true}',
    balanceUrl: '',
    passThroughBodyEnabled: false,
    azureResponsesVersion: '',
    vertexKeyType: 'json' as const,
    awsKeyType: 'auto' as const,
    advancedCustom: '',
    upstreamCheckEnabled: false,
    upstreamAutoSyncEnabled: false,
    upstreamIgnoredModels: '',
  },
};

const baselineCapabilities: ChannelAdminViewProps = {
  canRead: true,
  canOperate: true,
  canWrite: true,
  canSensitiveWrite: false,
};

function renderChannels(capabilities: boolean | Partial<ChannelAdminViewProps> = {}) {
  const overrides = typeof capabilities === 'boolean'
    ? { canSensitiveWrite: capabilities }
    : capabilities;
  return render(<ChannelAdminView {...baselineCapabilities} {...overrides} />);
}

beforeEach(() => {
  vi.resetAllMocks();
  mockedSearch.mockResolvedValue({ items: [primaryChannel], total: 21, typeCounts: { 1: 20, 8: 1 } });
  mockedLoadModels.mockResolvedValue(['gpt-4o', 'gpt-4.1']);
  mockedTest.mockResolvedValue({ success: true, time: 0.12 });
  mockedSetStatus.mockResolvedValue();
  mockedFetchModels.mockResolvedValue(['gpt-4.1', 'gpt-4o']);
  mockedFetchDetail.mockImplementation(async (_id, includeSensitive) => ({
    ...primaryDetail,
    ...(includeSensitive ? { sensitive: primarySensitiveDetail } : {}),
  }));
  mockedDiscoverDraftModels.mockResolvedValue(['gpt-4.1', 'new-model']);
  mockedUpdate.mockResolvedValue();
  mockedCreate.mockResolvedValue();
  mockedDelete.mockResolvedValue();
  mockedSetChannelsStatus.mockResolvedValue(1);
  mockedSetChannelsTag.mockResolvedValue(1);
  mockedDeleteChannels.mockResolvedValue(1);
  mockedRefreshBalance.mockResolvedValue(12.5);
  mockedRefreshAllBalances.mockResolvedValue();
  mockedCopy.mockResolvedValue(8);
  mockedDeleteDisabled.mockResolvedValue(2);
  mockedLoadTagModels.mockResolvedValue('gpt-4o,gpt-4.1');
  mockedSetTagStatus.mockResolvedValue();
  mockedUpdateTag.mockResolvedValue();
  mockedDetectAllUpdates.mockResolvedValue({ taskId: 'task-1', status: 'pending' });
  mockedApplyAllUpdates.mockResolvedValue({
    processedChannels: 1,
    addedModels: 1,
    removedModels: 0,
    failedChannelIds: [],
    resultCount: 1,
    resultsTruncated: false,
    failedIdsTruncated: false,
  });
  mockedDetectUpdates.mockResolvedValue({ channelId: 7, channelName: 'Primary channel', addModels: ['new-model'], removeModels: ['old-model'], lastCheckTime: 123, autoAddedModels: 0 });
  mockedApplyUpdates.mockResolvedValue({ addedModels: ['new-model'], removedModels: ['old-model'], ignoredModels: [], remainingModels: [], remainingRemoveModels: [] });
  mockedLoadMultiKeys.mockResolvedValue({ keys: [], total: 0, page: 1, pageSize: 20, totalPages: 1, enabledCount: 2, manualDisabledCount: 0, autoDisabledCount: 0 });
  mockedManageMultiKey.mockResolvedValue();
  mockedLoadCodexUsage.mockResolvedValue({ upstreamStatus: 200, data: { remaining: 17 } });
  mockedLoadCodexCredits.mockResolvedValue({ upstreamStatus: 200, data: { available_count: 1 } });
  mockedResetCodex.mockResolvedValue({ upstreamStatus: 200, data: { reset: true } });
  mockedRefreshCodex.mockResolvedValue();
  mockedRepair.mockResolvedValue({ successfulChannels: 1, failedChannels: 0 });
  mockedLoadOllamaVersion.mockResolvedValue('0.11.7');
  mockedPullOllama.mockResolvedValue();
  mockedDeleteOllama.mockResolvedValue();
  mockedTestAll.mockResolvedValue({ taskId: 'test-task-1', status: 'pending' });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('ChannelAdminView', () => {
  it('offers full-key verification only when the authenticated operator is root', async () => {
    const nonRoot = renderChannels({ isRoot: false, canSensitiveWrite: true });
    const nonRootRow = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    expect(within(nonRootRow).queryByRole('button', { name: 'View key' })).toBeNull();
    nonRoot.unmount();

    renderChannels({ isRoot: true, canSensitiveWrite: true });
    const rootRow = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    fireEvent.click(within(rootRow).getByRole('button', { name: 'View key' }));
    expect(screen.getByRole('heading', { name: 'View key for Primary channel' })).toBeTruthy();
    expect(screen.getByLabelText('Six-digit authenticator code')).toBeTruthy();
  });

  it('fails closed without read permission and does not issue channel reads', () => {
    renderChannels({
      canRead: false,
      canOperate: true,
      canWrite: true,
      canSensitiveWrite: true,
    });

    expect(screen.getByRole('alert').textContent).toBe('Your account does not have permission to view this page.');
    expect(mockedSearch).not.toHaveBeenCalled();
    expect(mockedLoadModels).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Test all channels' })).toBeNull();
    expect(screen.queryByText('Create channel')).toBeNull();
  });

  it('keeps read, operate, write, and sensitive controls independent', async () => {
    const readOnly = renderChannels({ canOperate: false, canWrite: false, canSensitiveWrite: false });
    await screen.findByText('Primary channel');
    expect(screen.queryByRole('button', { name: 'Test' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
    expect(screen.queryByRole('checkbox', { name: 'Select channel Primary channel' })).toBeNull();
    expect(screen.queryByText('Create channel')).toBeNull();
    readOnly.unmount();

    const operateOnly = renderChannels({ canOperate: true, canWrite: false, canSensitiveWrite: false });
    const operateRow = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    expect(within(operateRow).getByRole('button', { name: 'Test' })).toBeTruthy();
    expect(within(operateRow).queryByRole('button', { name: 'Edit' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Apply all staged updates' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
    operateOnly.unmount();

    const writeOnly = renderChannels({ canOperate: false, canWrite: true, canSensitiveWrite: false });
    const writeRow = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    expect(within(writeRow).queryByRole('button', { name: 'Test' })).toBeNull();
    expect(within(writeRow).getByRole('button', { name: 'Edit' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Apply all staged updates' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
    writeOnly.unmount();

    renderChannels({ canOperate: false, canWrite: false, canSensitiveWrite: true });
    const sensitiveRow = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    expect(within(sensitiveRow).queryByRole('button', { name: 'Test' })).toBeNull();
    expect(within(sensitiveRow).queryByRole('button', { name: 'Edit' })).toBeNull();
    expect(within(sensitiveRow).getByRole('button', { name: 'Copy' })).toBeTruthy();
    expect(within(sensitiveRow).getByRole('button', { name: 'Delete' })).toBeTruthy();
    expect(screen.getByText('Create channel')).toBeTruthy();
  });

  it('does not issue denied nested-operation requests for write-only or sensitive-only grants', async () => {
    mockedSearch.mockResolvedValueOnce({
      items: [{
        ...primaryChannel,
        pendingAddModels: ['new-model'],
        pendingRemoveModels: ['old-model'],
      }],
      total: 1,
      typeCounts: { 1: 1 },
    });
    const writeOnly = renderChannels({ canOperate: false, canWrite: true, canSensitiveWrite: false });
    const writeRow = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    await userEvent.setup().click(within(writeRow).getByRole('button', { name: 'Review updates' }));
    expect(await screen.findByRole('button', { name: 'Apply selected updates' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Detect again' })).toBeNull();
    expect(mockedDetectUpdates).not.toHaveBeenCalled();
    writeOnly.unmount();

    mockedSearch.mockResolvedValueOnce({
      items: [{ ...primaryChannel, id: 4, name: 'Ollama', type: 4 }],
      total: 1,
      typeCounts: { 4: 1 },
    });
    renderChannels({ canOperate: false, canWrite: false, canSensitiveWrite: true });
    const ollamaRow = (await screen.findByText('Ollama')).closest('tr') as HTMLTableRowElement;
    await userEvent.setup().click(within(ollamaRow).getByRole('button', { name: 'Manage Ollama models' }));
    await waitFor(() => expect(mockedLoadOllamaVersion).toHaveBeenCalledWith(4, expect.any(AbortSignal)));
    expect(mockedFetchModels).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Refresh models' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Pull model' })).toBeTruthy();
  });

  it('keeps Codex read, reset, and credential capabilities separate', async () => {
    mockedSearch.mockResolvedValue({
      items: [{ ...primaryChannel, id: 57, name: 'Codex', type: 57 }],
      total: 1,
      typeCounts: { 57: 1 },
    });
    const readOnly = renderChannels({ canOperate: false, canWrite: false, canSensitiveWrite: false });
    let row = (await screen.findByText('Codex')).closest('tr') as HTMLTableRowElement;
    await userEvent.setup().click(within(row).getByRole('button', { name: 'Codex usage' }));
    await waitFor(() => expect(mockedLoadCodexUsage).toHaveBeenCalledWith(57, expect.any(AbortSignal)));
    expect(screen.getByRole('button', { name: 'Refresh usage' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Use reset credit' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Refresh credential' })).toBeNull();
    readOnly.unmount();

    renderChannels({ canOperate: false, canWrite: false, canSensitiveWrite: true });
    row = (await screen.findByText('Codex')).closest('tr') as HTMLTableRowElement;
    await userEvent.setup().click(within(row).getByRole('button', { name: 'Codex usage' }));
    expect(await screen.findByRole('button', { name: 'Refresh credential' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Use reset credit' })).toBeNull();
  });

  it('renders an accessible bounded list and applies search, model, group, status, type, and page filters', async () => {
    renderChannels();
    const user = userEvent.setup();

    expect(await screen.findByText('Primary channel')).toBeTruthy();
    expect(screen.getByRole('table').getAttribute('aria-busy')).toBe('false');
    expect(screen.getByText('gpt-4o, gpt-4.1, o3-mini +1')).toBeTruthy();
    expect(screen.queryByText(/sk-/)).toBeNull();
    expect(screen.getByRole('option', { name: '1 (20)' })).toBeTruthy();

    const search = screen.getByRole('search', { name: 'Search channels' });
    await user.type(within(search).getByLabelText('Search channels'), 'primary');
    await user.type(within(search).getByLabelText('Group'), 'vip');
    await user.type(within(search).getByLabelText('Model'), 'gpt-4o');
    await user.selectOptions(within(search).getByLabelText('Status'), 'enabled');
    await user.selectOptions(within(search).getByLabelText('Type'), '8');
    await user.selectOptions(within(search).getByLabelText('Sort by'), 'balance');
    await user.selectOptions(within(search).getByLabelText('Sort order'), 'asc');
    await user.click(within(search).getByRole('checkbox', { name: 'Keep matching tags together' }));
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));

    await waitFor(() => expect(mockedSearch).toHaveBeenLastCalledWith({
      keyword: 'primary',
      group: 'vip',
      model: 'gpt-4o',
      status: 'enabled',
      type: 8,
      page: 1,
      pageSize: 20,
      tagMode: true,
      idSort: false,
      sortBy: 'balance',
      sortOrder: 'asc',
    }, expect.any(AbortSignal)));

    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedSearch).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }), expect.any(AbortSignal)));
  });

  it('runs the test, status, upstream-model, and safe edit workflows without exposing credentials', async () => {
    renderChannels();
    const user = userEvent.setup();
    const row = (await screen.findByText('Primary channel')).closest('tr');
    expect(row).not.toBeNull();
    const actions = within(row as HTMLTableRowElement);

    await user.click(actions.getByRole('button', { name: 'Test' }));
    expect((await screen.findByRole('status')).textContent).toBe('Channel test passed in 0.12 s.');
    expect(mockedTest).toHaveBeenCalledWith(7);

    await user.click(actions.getByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(mockedSetStatus).toHaveBeenCalledWith(7, 3));
    expect(screen.getByText('Channel disabled.')).toBeTruthy();

    await user.click(actions.getByRole('button', { name: 'Fetch models' }));
    expect(await screen.findByRole('heading', { name: 'Upstream models for Primary channel' })).toBeTruthy();
    expect(screen.getByText('gpt-4.1')).toBeTruthy();
    expect(mockedFetchModels).toHaveBeenCalledWith(7);

    await user.click(actions.getByRole('button', { name: 'Edit' }));
    const editForm = (await screen.findByRole('heading', { name: 'Edit channel Fresh primary channel' })).closest('form');
    expect(editForm).not.toBeNull();
    expect(mockedFetchDetail).toHaveBeenCalledWith(7, false);
    const name = within(editForm as HTMLFormElement).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'Renamed channel');
    await user.click(within(editForm as HTMLFormElement).getByRole('button', { name: 'Save changes' }));

    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledWith({
      id: 7,
      name: 'Renamed channel',
      models: 'gpt-4o,gpt-4.1,o3-mini,o1',
      group: 'default',
      tag: 'fast',
      remark: 'fresh detail',
      priority: 2,
      weight: 3,
      testModel: 'gpt-4o',
      autoBan: 1,
      modelMapping: '{"client":"upstream"}',
      statusCodeMapping: '{"429":500}',
    }));
    expect(mockedUpdate.mock.calls[0][0]).not.toHaveProperty('key');
    expect(screen.queryByLabelText('API key')).toBeNull();
  });

  it('loads and submits sensitive editor fields only with the sensitive-write grant', async () => {
    renderChannels({ canSensitiveWrite: true });
    const user = userEvent.setup();
    const row = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Edit' }));

    const editForm = (await screen.findByRole('heading', { name: 'Edit channel Fresh primary channel' })).closest('form') as HTMLFormElement;
    expect(mockedFetchDetail).toHaveBeenCalledWith(7, true);
    expect(within(editForm).queryByLabelText('API key')).toBeNull();
    expect(within(editForm).getByLabelText('Base URL')).toBeTruthy();
    expect(within(editForm).getByLabelText('OpenAI organization')).toBeTruthy();

    await user.selectOptions(within(editForm).getByLabelText('Type'), '33');
    await user.selectOptions(within(editForm).getByLabelText('AWS key format'), 'api_key');
    await user.type(within(editForm).getByLabelText('Balance URL'), 'https://balance.example.test');
    fireEvent.change(within(editForm).getByLabelText('Header override'), { target: { value: '{"X-New":"yes"}' } });
    await user.click(within(editForm).getByRole('button', { name: 'Save changes' }));

    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledWith(expect.objectContaining({
      id: 7,
      type: 33,
      baseUrl: 'https://gateway.example.test',
      organization: 'org-test',
      headerOverride: '{"X-New":"yes"}',
      providerSettings: expect.objectContaining({
        balanceUrl: 'https://balance.example.test',
        awsKeyType: 'api_key',
      }),
    })));
    expect(mockedUpdate.mock.calls[0][0]).not.toHaveProperty('key');
  });

  it('offers explicit merge and replace choices for unsaved create-model discovery', async () => {
    renderChannels({ canSensitiveWrite: true });
    const user = userEvent.setup();
    await screen.findByText('Primary channel');
    await user.click(screen.getByText('Create channel'));
    const createForm = screen.getByRole('button', { name: 'Add channel' }).closest('form') as HTMLFormElement;
    await user.type(within(createForm).getByLabelText('API key'), 'sk-draft');
    await user.type(within(createForm).getByLabelText('Base URL'), 'https://draft.example.test');
    await user.type(within(createForm).getByLabelText('Models'), 'gpt-4o');
    await user.click(within(createForm).getByRole('button', { name: 'Discover models from draft' }));

    await waitFor(() => expect(mockedDiscoverDraftModels).toHaveBeenCalledWith({
      type: 1,
      key: 'sk-draft',
      baseUrl: 'https://draft.example.test',
    }));
    expect(await within(createForm).findByText('Discovered 2 upstream models')).toBeTruthy();
    await user.click(within(createForm).getByRole('button', { name: 'Merge models' }));
    expect((within(createForm).getByLabelText('Models') as HTMLTextAreaElement).value).toBe('gpt-4o,gpt-4.1,new-model');

    await user.click(within(createForm).getByRole('button', { name: 'Discover models from draft' }));
    await within(createForm).findByText('Discovered 2 upstream models');
    await user.click(within(createForm).getByRole('button', { name: 'Replace all models' }));
    expect((within(createForm).getByLabelText('Models') as HTMLTextAreaElement).value).toBe('gpt-4.1,new-model');
  });

  it('passes an operator-selected test model and shows only the bounded API diagnostic', async () => {
    mockedTest.mockResolvedValueOnce({ success: false, time: 0, message: 'provider rejected this model' });
    renderChannels();
    const user = userEvent.setup();
    const row = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    await user.type(within(row).getByLabelText('Test model for Primary channel'), 'gpt-4.1');
    await user.click(within(row).getByRole('button', { name: 'Test' }));

    await waitFor(() => expect(mockedTest).toHaveBeenCalledWith(7, 'gpt-4.1'));
    expect((await screen.findByRole('alert')).textContent).toBe('Channel test failed: provider rejected this model');
  });

  it('keeps credential-bearing create/delete controls root-only and clears a rejected key', async () => {
    const user = userEvent.setup();
    const ordinary = renderChannels(false);
    await screen.findByText('Primary channel');
    expect(screen.queryByText('Create channel')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
    ordinary.unmount();

    mockedCreate.mockRejectedValueOnce(new Error('backend leaked sk-private'));
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderChannels(true);
    await screen.findByText('Primary channel');
    await user.click(screen.getByText('Create channel'));
    const createForm = screen.getByRole('button', { name: 'Add channel' }).closest('form') as HTMLFormElement;
    expect(within(createForm).getByLabelText('Type').tagName).toBe('SELECT');
    expect(within(createForm).queryByRole('option', { name: 'Dummy' })).toBeNull();
    expect(within(createForm).queryByRole('option', { name: /Advanced Custom/ })).toBeNull();
    await user.selectOptions(screen.getByLabelText('Add mode'), 'multi_to_single');
    expect(screen.getByLabelText('Multi-key strategy')).toBeTruthy();
    expect(screen.getByLabelText('API keys, one per line')).toBeTruthy();
    await user.selectOptions(screen.getByLabelText('Add mode'), 'single');
    const key = screen.getByLabelText('API key') as HTMLInputElement;
    await user.type(screen.getByLabelText('Name', { selector: 'input' }), 'New channel');
    await user.type(key, 'sk-private');
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to create channel.');
    expect(key.value).toBe('');
    expect(screen.queryByText('backend leaked sk-private')).toBeNull();

    const row = screen.getByText('Primary channel').closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(mockedDelete).toHaveBeenCalledWith(7));
  });

  it('renders explicit loading, empty, and redacted failure states', async () => {
    let resolveSearch: ((value: { items: []; total: number; typeCounts: Record<number, number> }) => void) | undefined;
    mockedSearch.mockImplementationOnce(() => new Promise((resolve) => { resolveSearch = resolve; }));
    renderChannels();
    expect(screen.getByRole('status').textContent).toBe('Loading channels…');

    await act(async () => resolveSearch?.({ items: [], total: 0, typeCounts: {} }));
    expect(await screen.findByText('No channels match these filters.')).toBeTruthy();
    cleanup();

    mockedSearch.mockRejectedValueOnce(new Error('database password and upstream secret'));
    renderChannels();
    expect((await screen.findByRole('alert')).textContent).toContain('Unable to load channels.');
    expect(screen.queryByText(/database password|upstream secret/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('ignores late list and catalog responses after unmount', async () => {
    let resolveSearch: ((value: { items: typeof primaryChannel[]; total: number; typeCounts: Record<number, number> }) => void) | undefined;
    let resolveModels: ((value: string[]) => void) | undefined;
    mockedSearch.mockImplementationOnce(() => new Promise((resolve) => { resolveSearch = resolve; }));
    mockedLoadModels.mockImplementationOnce(() => new Promise((resolve) => { resolveModels = resolve; }));
    const rendered = renderChannels();
    rendered.unmount();

    await act(async () => {
      resolveSearch?.({ items: [{ ...primaryChannel, name: 'Late channel' }], total: 1, typeCounts: { 1: 1 } });
      resolveModels?.(['late-secret-model']);
      await Promise.resolve();
    });
    expect(screen.queryByText(/Late channel|late-secret-model/)).toBeNull();
  });

  it('keeps bulk status, tag, and destructive selection scoped to visible checked rows', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderChannels(true);
    const user = userEvent.setup();
    await screen.findByText('Primary channel');

    await user.click(screen.getByRole('checkbox', { name: 'Select channel Primary channel' }));
    expect(screen.getByRole('status', { name: '' }).textContent).toContain('1 channels selected.');
    await user.click(screen.getByRole('button', { name: 'Disable selected' }));
    await waitFor(() => expect(mockedSetChannelsStatus).toHaveBeenCalledWith([7], 3));

    await user.click(screen.getByRole('checkbox', { name: 'Select channel Primary channel' }));
    await user.type(screen.getByLabelText('Tag selected channels'), 'new-tag');
    await user.click(screen.getByRole('button', { name: 'Set tag' }));
    await waitFor(() => expect(mockedSetChannelsTag).toHaveBeenCalledWith([7], 'new-tag'));

    await user.click(screen.getByRole('checkbox', { name: 'Select channel Primary channel' }));
    await user.click(screen.getByRole('button', { name: 'Delete selected' }));
    await waitFor(() => expect(mockedDeleteChannels).toHaveBeenCalledWith([7]));
    expect(window.confirm).toHaveBeenCalledWith('Delete 1 selected channels? This action cannot be undone.');
  });

  it('supports balance, copy, tag-wide, and global upstream workflows with confirmations', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderChannels(true);
    const user = userEvent.setup();
    const row = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Refresh balance' }));
    await waitFor(() => expect(mockedRefreshBalance).toHaveBeenCalledWith(7));

    await user.click(within(row).getByRole('button', { name: 'Copy' }));
    const copyForm = screen.getByRole('heading', { name: 'Copy channel Primary channel' }).closest('form') as HTMLFormElement;
    await user.clear(within(copyForm).getByLabelText('Name suffix'));
    await user.type(within(copyForm).getByLabelText('Name suffix'), '_v2');
    await user.click(within(copyForm).getByRole('button', { name: 'Copy channel' }));
    await waitFor(() => expect(mockedCopy).toHaveBeenCalledWith(7, '_v2', true));

    await user.type(screen.getByLabelText('Manage tag'), 'fast');
    await user.click(screen.getByRole('button', { name: 'Manage' }));
    expect(await screen.findByRole('heading', { name: 'Manage tag fast' })).toBeTruthy();
    await waitFor(() => expect(mockedLoadTagModels).toHaveBeenCalledWith('fast', expect.any(AbortSignal)));
    await user.click(screen.getByRole('button', { name: 'Disable tagged channels' }));
    await waitFor(() => expect(mockedSetTagStatus).toHaveBeenCalledWith('fast', false));
    await user.clear(screen.getByLabelText('New tag'));
    await user.type(screen.getByLabelText('New tag'), 'slower');
    await user.click(screen.getByRole('button', { name: 'Save tag changes' }));
    await waitFor(() => expect(mockedUpdateTag).toHaveBeenCalledWith({ tag: 'fast', newTag: 'slower' }));

    await user.click(screen.getByRole('button', { name: 'Refresh all balances' }));
    await waitFor(() => expect(mockedRefreshAllBalances).toHaveBeenCalled());
    await user.click(screen.getByRole('button', { name: 'Detect all upstream updates' }));
    await waitFor(() => expect(mockedDetectAllUpdates).toHaveBeenCalled());
    await user.click(screen.getByRole('button', { name: 'Apply all staged updates' }));
    await waitFor(() => expect(mockedApplyAllUpdates).toHaveBeenCalled());
  });

  it('offers balance refresh for every provider supported by the server', async () => {
    const supportedTypes = [1, 8, 10, 12, 13, 20, 25, 40, 43];
    mockedSearch.mockResolvedValue({
      items: supportedTypes.map((type, index) => ({
        ...primaryChannel,
        id: 100 + index,
        name: `Balance provider ${type}`,
        type,
      })),
      total: supportedTypes.length,
      typeCounts: Object.fromEntries(supportedTypes.map((type) => [type, 1])),
    });
    renderChannels();

    for (const type of supportedTypes) {
      const row = (await screen.findByText(`Balance provider ${type}`)).closest('tr') as HTMLTableRowElement;
      expect(within(row).getByRole('button', { name: 'Refresh balance' })).toBeTruthy();
    }
  });

  it('runs supported maintenance tasks and keeps destructive cleanup root-only', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const ordinary = renderChannels(false);
    const user = userEvent.setup();
    await screen.findByText('Primary channel');

    await user.click(screen.getByRole('button', { name: 'Test all channels' }));
    await waitFor(() => expect(mockedTestAll).toHaveBeenCalled());
    expect(screen.getByText('Channel test task test-task-1 is pending.')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Repair channel consistency' }));
    await waitFor(() => expect(mockedRepair).toHaveBeenCalled());
    expect(screen.queryByRole('button', { name: 'Delete all disabled' })).toBeNull();
    ordinary.unmount();

    renderChannels(true);
    await screen.findByText('Primary channel');
    await user.click(screen.getByRole('button', { name: 'Delete all disabled' }));
    await waitFor(() => expect(mockedDeleteDisabled).toHaveBeenCalled());
    expect(window.confirm).toHaveBeenLastCalledWith('Delete every manually and automatically disabled channel? This action cannot be undone.');
  });

  it('detects and selectively applies one channel upstream update', async () => {
    renderChannels();
    const user = userEvent.setup();
    const row = (await screen.findByText('Primary channel')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Upstream updates' }));
    expect(await screen.findByRole('heading', { name: 'Upstream updates for Primary channel' })).toBeTruthy();
    await waitFor(() => expect(mockedDetectUpdates).toHaveBeenCalledWith(7, expect.any(AbortSignal)));
    await screen.findByText('new-model');
    await user.click(screen.getByRole('button', { name: 'Apply selected updates' }));
    await waitFor(() => expect(mockedApplyUpdates).toHaveBeenCalledWith(7, ['new-model'], ['old-model'], []));
  });

  it('exposes multi-key, Codex, and Ollama tools only for matching channel types', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockedLoadMultiKeys.mockResolvedValue({
      keys: [{ index: 0, fingerprint: 'sha256:deadbeef', status: 1, disabledTime: 0, hasReason: false }],
      total: 1,
      page: 1,
      pageSize: 20,
      totalPages: 1,
      enabledCount: 2,
      manualDisabledCount: 0,
      autoDisabledCount: 0,
    });
    mockedSearch.mockResolvedValue({
      items: [
        { ...primaryChannel, id: 10, name: 'Multi', isMultiKey: true },
        { ...primaryChannel, id: 57, name: 'Codex', type: 57 },
        { ...primaryChannel, id: 4, name: 'Ollama', type: 4 },
      ],
      total: 3,
      typeCounts: { 1: 1, 4: 1, 57: 1 },
    });
    renderChannels(true);
    const user = userEvent.setup();

    const multiRow = (await screen.findByText('Multi')).closest('tr') as HTMLTableRowElement;
    await user.click(within(multiRow).getByRole('button', { name: 'Manage keys' }));
    expect(await screen.findByRole('heading', { name: 'Manage keys for Multi' })).toBeTruthy();
    await waitFor(() => expect(mockedLoadMultiKeys).toHaveBeenCalledWith(10, 1, 20, '', expect.any(AbortSignal)));
    await user.click(screen.getByRole('button', { name: 'Disable all keys' }));
    await waitFor(() => expect(mockedManageMultiKey).toHaveBeenCalledWith(10, 'disable_all_keys', undefined));
    await user.click(screen.getByRole('button', { name: 'Close' }));

    const codexRow = screen.getByText('Codex').closest('tr') as HTMLTableRowElement;
    await user.click(within(codexRow).getByRole('button', { name: 'Codex usage' }));
    expect(await screen.findByRole('heading', { name: 'Codex usage for Codex' })).toBeTruthy();
    await waitFor(() => expect(mockedLoadCodexUsage).toHaveBeenCalledWith(57, expect.any(AbortSignal)));
    await user.click(screen.getByRole('button', { name: 'Use reset credit' }));
    await waitFor(() => expect(mockedResetCodex).toHaveBeenCalledWith(57));
    await user.click(screen.getByRole('button', { name: 'Close' }));

    const ollamaRow = screen.getByText('Ollama').closest('tr') as HTMLTableRowElement;
    await user.click(within(ollamaRow).getByRole('button', { name: 'Manage Ollama models' }));
    expect(await screen.findByRole('heading', { name: 'Ollama models for Ollama' })).toBeTruthy();
    await waitFor(() => expect(mockedLoadOllamaVersion).toHaveBeenCalledWith(4, expect.any(AbortSignal)));
    await user.type(screen.getByLabelText('Model to pull'), 'llama3.2');
    await user.click(screen.getByRole('button', { name: 'Pull model' }));
    await waitFor(() => expect(mockedPullOllama).toHaveBeenCalledWith(4, 'llama3.2', expect.any(Function), expect.any(AbortSignal)));
  });
});
