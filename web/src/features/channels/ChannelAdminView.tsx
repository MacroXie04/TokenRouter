import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  applyAllUpstreamUpdates,
  CHANNEL_AUTO_DISABLED,
  CHANNEL_ENABLED,
  CHANNEL_MANUALLY_DISABLED,
  CHANNEL_PAGE_SIZE,
  CHANNEL_TYPE_CODEX,
  CHANNEL_TYPE_OLLAMA,
  copyChannel,
  createChannel,
  deleteDisabledChannels,
  deleteChannels,
  deleteChannel,
  detectAllUpstreamUpdates,
  fetchChannelModels,
  fetchChannelDetail,
  discoverDraftModels,
  loadEnabledModels,
  loadTagModels,
  normalizeChannelTag,
  refreshAllChannelBalances,
  refreshChannelBalance,
  repairChannelAbilities,
  searchChannels,
  setChannelsStatus,
  setChannelsTag,
  setChannelStatus,
  setTagChannelsStatus,
  testChannel,
  testAllChannels,
  updateTagChannels,
  updateChannel,
  type ChannelCreateInput,
  type ChannelDetail,
  type ChannelProviderSettings,
  type ChannelSearchInput,
  type ChannelSummary,
  type ChannelUpdateInput,
  type TagUpdateInput,
} from './channel-api';
import { CodexPanel, MultiKeyPanel, OllamaPanel, UpstreamUpdatesPanel } from './ChannelWorkflowPanels';
import { ChannelKeyRevealPanel } from './ChannelKeyRevealPanel';
import {
  CHANNEL_PROVIDER_OPTIONS,
  channelProviderLabel,
  isChannelProviderType,
} from './channel-providers';

type SearchDraft = Pick<
  ChannelSearchInput,
  'keyword' | 'group' | 'model' | 'status' | 'type' | 'tagMode' | 'idSort' | 'sortBy' | 'sortOrder'
>;
type Notice = { kind: 'success' | 'error'; text: string } | null;
type DiscoveryTarget = 'edit' | 'create';
type DraftDiscovery = { target: DiscoveryTarget; models: string[] };

type CopyPanel = { channel: ChannelSummary; suffix: string; resetBalance: boolean };
type TagPanel = {
  tag: string;
  newTag: string;
  models: string;
  groups: string;
  modelMapping: string;
  priority: string;
  weight: string;
  paramOverride: string;
  headerOverride: string;
  modelsDirty: boolean;
  modelMappingDirty: boolean;
  paramOverrideDirty: boolean;
  headerOverrideDirty: boolean;
};

const EMPTY_SEARCH: SearchDraft = {
  keyword: '',
  group: '',
  model: '',
  status: '',
  type: null,
  tagMode: false,
  idSort: false,
  sortBy: '',
  sortOrder: 'desc',
};
const EMPTY_CREATE: ChannelCreateInput = {
  mode: 'single',
  multi_key_mode: 'random',
  batch_add_set_key_prefix_2_name: false,
  name: '',
  type: 1,
  key: '',
  base_url: '',
  models: '',
  group: 'default',
};

// Mirrors the target backend's model-discovery capability gate.
const UPSTREAM_DISCOVERY_TYPES = new Set([
  1, 3, 4, 6, 7, 8, 9, 10, 12, 13, 14, 17, 19, 20, 22, 24, 25, 26, 27, 31,
  40, 42, 43, 45, 47, 48, 57, 58, 59, 60,
]);

function modelSummary(models: string): string {
  const values = models.split(',').map((value) => value.trim()).filter(Boolean);
  if (values.length === 0) return '—';
  const visible = values.slice(0, 3).join(', ');
  return values.length > 3 ? `${visible} +${values.length - 3}` : visible;
}

function modelValues(models: string): string[] {
  return [...new Set(models.split(',').map((value) => value.trim()).filter(Boolean))];
}

function applyDiscoveredModels(current: string, discovered: string[], merge: boolean): string {
  return (merge ? [...new Set([...modelValues(current), ...discovered])] : discovered).join(',');
}

function providerOtherLabel(type: number, t: (key: string) => string): string {
  if (type === 3) return t('Default API version');
  if (type === 18) return t('Model version');
  if (type === 21) return t('Knowledge base ID');
  if (type === 39) return t('Account ID');
  if (type === 41) return t('Deployment region');
  if (type === 49) return t('Agent ID');
  return t('Provider-specific value');
}

function channelStatusLabel(status: number, t: (key: string) => string): string {
  if (status === CHANNEL_ENABLED) return t('Enabled');
  if (status === CHANNEL_AUTO_DISABLED) return t('Automatically disabled');
  if (status === CHANNEL_MANUALLY_DISABLED) return t('Manually disabled');
  return t('Disabled');
}

const BALANCE_PROVIDER_TYPES = new Set([1, 8, 10, 12, 13, 20, 25, 40, 43]);

function supportsBalance(channel: ChannelSummary): boolean {
  return BALANCE_PROVIDER_TYPES.has(channel.type) && !channel.isMultiKey;
}

export interface ChannelAdminViewProps {
  canRead: boolean;
  canOperate: boolean;
  canWrite: boolean;
  canSensitiveWrite: boolean;
  isRoot?: boolean;
}

export function ChannelAdminView({
  canRead,
  canOperate,
  canWrite,
  canSensitiveWrite,
  isRoot = false,
}: ChannelAdminViewProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<SearchDraft>(EMPTY_SEARCH);
  const [query, setQuery] = useState<ChannelSearchInput>({ ...EMPTY_SEARCH, page: 1, pageSize: CHANNEL_PAGE_SIZE });
  const [channels, setChannels] = useState<ChannelSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [typeCounts, setTypeCounts] = useState<Record<number, number>>({});
  const [catalog, setCatalog] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [catalogError, setCatalogError] = useState(false);
  const [reload, setReload] = useState(0);
  const [busyAction, setBusyAction] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [bulkTag, setBulkTag] = useState('');
  const [tagLookup, setTagLookup] = useState('');
  const [editing, setEditing] = useState<ChannelDetail | null>(null);
  const [editingSourceType, setEditingSourceType] = useState<number | null>(null);
  const [draftDiscovery, setDraftDiscovery] = useState<DraftDiscovery | null>(null);
  const [testModels, setTestModels] = useState<Record<number, string>>({});
  const [modelPanel, setModelPanel] = useState<{ id: number; name: string; models: string[] } | null>(null);
  const [copyPanel, setCopyPanel] = useState<CopyPanel | null>(null);
  const [tagPanel, setTagPanel] = useState<TagPanel | null>(null);
  const [tagLoading, setTagLoading] = useState(false);
  const [tagLoadError, setTagLoadError] = useState(false);
  const [tagReload, setTagReload] = useState(0);
  const [multiKeyPanel, setMultiKeyPanel] = useState<ChannelSummary | null>(null);
  const [upstreamPanel, setUpstreamPanel] = useState<ChannelSummary | null>(null);
  const [codexPanel, setCodexPanel] = useState<ChannelSummary | null>(null);
  const [ollamaPanel, setOllamaPanel] = useState<ChannelSummary | null>(null);
  const [keyRevealChannel, setKeyRevealChannel] = useState<ChannelSummary | null>(null);
  const [createDraft, setCreateDraft] = useState<ChannelCreateInput>(EMPTY_CREATE);

  useEffect(() => {
    if (!canRead) {
      setChannels([]);
      setTotal(0);
      setTypeCounts({});
      setLoading(false);
      setLoadError(false);
      return undefined;
    }
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void searchChannels(query, controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return;
        setChannels(result.items);
        setTotal(result.total);
        setTypeCounts(result.typeCounts);
        const lastPage = Math.max(1, Math.ceil(result.total / query.pageSize));
        if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setChannels([]);
          setTotal(0);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [canRead, query, reload]);

  useEffect(() => {
    const visible = new Set(channels.map((channel) => channel.id));
    setSelected((current) => {
      const next = new Set([...current].filter((id) => visible.has(id)));
      if (next.size === current.size && [...next].every((id) => current.has(id))) return current;
      return next;
    });
  }, [channels]);

  useEffect(() => {
    if (!canRead) {
      setCatalog([]);
      setCatalogError(false);
      return undefined;
    }
    const controller = new AbortController();
    void loadEnabledModels(controller.signal)
      .then((models) => {
        if (!controller.signal.aborted) setCatalog(models);
      })
      .catch(() => {
        if (!controller.signal.aborted) setCatalogError(true);
      });
    return () => controller.abort();
  }, [canRead]);

  const activeTag = tagPanel?.tag ?? '';
  useEffect(() => {
    if (!canRead || !activeTag) return undefined;
    const tag = activeTag;
    const controller = new AbortController();
    setTagLoading(true);
    setTagLoadError(false);
    void loadTagModels(tag, controller.signal)
      .then((models) => {
        if (controller.signal.aborted) return;
        setTagPanel((current) => current && current.tag === tag && !current.modelsDirty
          ? { ...current, models }
          : current);
      })
      .catch(() => {
        if (!controller.signal.aborted) setTagLoadError(true);
      })
      .finally(() => {
        if (!controller.signal.aborted) setTagLoading(false);
      });
    return () => controller.abort();
  }, [activeTag, canRead, tagReload]);

  const typeOptions = useMemo(() => {
    const values = Object.keys(typeCounts).map(Number);
    if (draft.type !== null && !values.includes(draft.type)) values.push(draft.type);
    return values.sort((left, right) => left - right);
  }, [draft.type, typeCounts]);
  const pageCount = Math.max(1, Math.ceil(total / query.pageSize));

  const refresh = useCallback(() => setReload((value) => value + 1), []);
  const notify = useCallback((kind: 'success' | 'error', text: string) => setNotice({ kind, text }), []);
  const selectedIDs = useMemo(() => [...selected].sort((left, right) => left - right), [selected]);
  const allVisibleSelected = channels.length > 0 && channels.every((channel) => selected.has(channel.id));
  const someVisibleSelected = channels.some((channel) => selected.has(channel.id));
  const visibleTags = useMemo(() => [...new Set(channels.map((channel) => channel.tag).filter(Boolean))].sort(), [channels]);
  const canSelect = canOperate || canWrite || canSensitiveWrite;

  function submitSearch(event: React.FormEvent) {
    event.preventDefault();
    if (!canRead) return;
    setNotice(null);
    setSelected(new Set());
    setQuery({ ...draft, page: 1, pageSize: CHANNEL_PAGE_SIZE });
  }

  function clearSearch() {
    if (!canRead) return;
    setDraft(EMPTY_SEARCH);
    setQuery({ ...EMPTY_SEARCH, page: 1, pageSize: CHANNEL_PAGE_SIZE });
    setSelected(new Set());
    setNotice(null);
  }

  function toggleSelected(id: number) {
    if (!canSelect || busyAction) return;
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function toggleAllVisible() {
    if (!canSelect || busyAction) return;
    setSelected(allVisibleSelected ? new Set() : new Set(channels.map((channel) => channel.id)));
  }

  function closePanelsFor(channelIDs: number[]) {
    const deleted = new Set(channelIDs);
    if (editing && deleted.has(editing.id)) {
      setDraftDiscovery((discovery) => discovery?.target === 'edit' ? null : discovery);
      setEditingSourceType(null);
    }
    setEditing((current) => current && deleted.has(current.id) ? null : current);
    setModelPanel((current) => current && deleted.has(current.id) ? null : current);
    setCopyPanel((current) => current && deleted.has(current.channel.id) ? null : current);
    setMultiKeyPanel((current) => current && deleted.has(current.id) ? null : current);
    setUpstreamPanel((current) => current && deleted.has(current.id) ? null : current);
    setCodexPanel((current) => current && deleted.has(current.id) ? null : current);
    setOllamaPanel((current) => current && deleted.has(current.id) ? null : current);
  }

  async function updateSelectedStatus(enabled: boolean) {
    if (!canOperate || busyAction || selectedIDs.length === 0) return;
    setBusyAction(`bulk-status:${enabled ? 'enabled' : 'disabled'}`);
    setNotice(null);
    try {
      const changed = await setChannelsStatus(selectedIDs, enabled ? CHANNEL_ENABLED : CHANNEL_MANUALLY_DISABLED);
      setSelected(new Set());
      setNotice({ kind: 'success', text: t('Updated {{count}} selected channels.', { count: changed }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update the selected channels.') });
    } finally {
      setBusyAction('');
    }
  }

  async function tagSelected(event: React.FormEvent) {
    event.preventDefault();
    if (!canWrite || busyAction || selectedIDs.length === 0) return;
    setBusyAction('bulk-tag');
    setNotice(null);
    try {
      const changed = await setChannelsTag(selectedIDs, bulkTag);
      setSelected(new Set());
      setBulkTag('');
      setNotice({ kind: 'success', text: t('Tagged {{count}} selected channels.', { count: changed }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to tag the selected channels.') });
    } finally {
      setBusyAction('');
    }
  }

  async function removeSelected() {
    if (!canSensitiveWrite || busyAction || selectedIDs.length === 0 ||
        !window.confirm(t('Delete {{count}} selected channels? This action cannot be undone.', { count: selectedIDs.length }))) return;
    setBusyAction('bulk-delete');
    setNotice(null);
    try {
      const deleted = await deleteChannels(selectedIDs);
      closePanelsFor(selectedIDs);
      setSelected(new Set());
      setNotice({ kind: 'success', text: t('Deleted {{count}} selected channels.', { count: deleted }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete the selected channels.') });
    } finally {
      setBusyAction('');
    }
  }

  async function refreshBalance(channel: ChannelSummary) {
    if (!canOperate || busyAction || channel.isMultiKey) return;
    setBusyAction(`balance:${channel.id}`);
    setNotice(null);
    try {
      const balance = await refreshChannelBalance(channel.id);
      setNotice({ kind: 'success', text: t('Channel balance: {{balance}}', { balance: balance.toFixed(2) }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to refresh the channel balance.') });
    } finally {
      setBusyAction('');
    }
  }

  async function refreshAllBalances() {
    if (!canOperate || busyAction || !window.confirm(t('Refresh every supported channel balance? Channels without balance may be disabled.'))) return;
    setBusyAction('balance-all');
    setNotice(null);
    try {
      await refreshAllChannelBalances();
      setNotice({ kind: 'success', text: t('Channel balances refreshed.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to refresh channel balances.') });
    } finally {
      setBusyAction('');
    }
  }

  async function startAllChannelTests() {
    if (!canOperate || busyAction) return;
    setBusyAction('test-all');
    setNotice(null);
    try {
      const task = await testAllChannels();
      setNotice({ kind: 'success', text: t('Channel test task {{task}} is {{status}}.', { task: task.taskId, status: task.status }) });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to start the channel test task.') });
    } finally {
      setBusyAction('');
    }
  }

  async function repairChannelConsistency() {
    if (!canOperate || busyAction || !window.confirm(t('Repair channel routing consistency now? Routing may be briefly incomplete.'))) return;
    setBusyAction('repair');
    setNotice(null);
    try {
      const result = await repairChannelAbilities();
      setNotice({
        kind: result.failedChannels > 0 ? 'error' : 'success',
        text: t('Rebuilt {{success}} channels; {{failed}} failed.', {
          success: result.successfulChannels,
          failed: result.failedChannels,
        }),
      });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to repair channel consistency.') });
    } finally {
      setBusyAction('');
    }
  }

  async function removeAllDisabledChannels() {
    if (!canSensitiveWrite || busyAction ||
        !window.confirm(t('Delete every manually and automatically disabled channel? This action cannot be undone.'))) return;
    setBusyAction('delete-disabled');
    setNotice(null);
    try {
      const deleted = await deleteDisabledChannels();
      closePanelsFor(channels.filter((channel) => channel.status !== CHANNEL_ENABLED).map((channel) => channel.id));
      setSelected(new Set());
      setNotice({ kind: 'success', text: t('Deleted {{count}} disabled channels.', { count: deleted }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete disabled channels.') });
    } finally {
      setBusyAction('');
    }
  }

  async function detectAllUpdates() {
    if (!canOperate || busyAction) return;
    setBusyAction('upstream-detect-all');
    setNotice(null);
    try {
      const task = await detectAllUpstreamUpdates();
      setNotice({ kind: 'success', text: t('Upstream detection task {{task}} is {{status}}.', { task: task.taskId, status: task.status }) });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to start upstream detection.') });
    } finally {
      setBusyAction('');
    }
  }

  async function applyAllUpdates() {
    if (!canWrite || busyAction || !window.confirm(t('Apply every staged upstream model update?'))) return;
    setBusyAction('upstream-apply-all');
    setNotice(null);
    try {
      const result = await applyAllUpstreamUpdates();
      const truncation = result.resultsTruncated || result.failedIdsTruncated
        ? ` ${t('Some result details were truncated.')}`
        : '';
      setNotice({
        kind: result.failedChannelIds.length > 0 ? 'error' : 'success',
        text: t('Applied updates to {{channels}} channels: {{added}} added, {{removed}} removed, {{failed}} failed.', {
          channels: result.processedChannels,
          added: result.addedModels,
          removed: result.removedModels,
          failed: result.failedChannelIds.length,
        }) + truncation,
      });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to apply all upstream updates.') });
    } finally {
      setBusyAction('');
    }
  }

  async function saveCopy(event: React.FormEvent) {
    event.preventDefault();
    if (!copyPanel || !canSensitiveWrite || busyAction) return;
    setBusyAction(`copy:${copyPanel.channel.id}`);
    setNotice(null);
    try {
      const id = await copyChannel(copyPanel.channel.id, copyPanel.suffix, copyPanel.resetBalance);
      setCopyPanel(null);
      setNotice({ kind: 'success', text: t('Channel copied as #{{id}}.', { id }) });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to copy the channel.') });
    } finally {
      setBusyAction('');
    }
  }

  function openTagPanel(tag: string) {
    if (!canOperate && !canWrite) return;
    setTagPanel({
      tag,
      newTag: tag,
      models: '',
      groups: '',
      modelMapping: '',
      priority: '',
      weight: '',
      paramOverride: '',
      headerOverride: '',
      modelsDirty: false,
      modelMappingDirty: false,
      paramOverrideDirty: false,
      headerOverrideDirty: false,
    });
    setTagLoadError(false);
    setTagReload((value) => value + 1);
  }

  function manageTag(event: React.FormEvent) {
    event.preventDefault();
    if (!canOperate && !canWrite) return;
    try {
      openTagPanel(normalizeChannelTag(tagLookup));
    } catch {
      setNotice({ kind: 'error', text: t('Enter a valid tag.') });
    }
  }

  async function setTagStatus(enabled: boolean) {
    if (!canOperate || !tagPanel || busyAction) return;
    setBusyAction(`tag-status:${enabled ? 'enabled' : 'disabled'}`);
    setNotice(null);
    try {
      await setTagChannelsStatus(tagPanel.tag, enabled);
      setNotice({ kind: 'success', text: enabled ? t('Tagged channels enabled.') : t('Tagged channels disabled.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update tagged channels.') });
    } finally {
      setBusyAction('');
    }
  }

  async function saveTag(event: React.FormEvent) {
    event.preventDefault();
    if (!canWrite || !tagPanel || busyAction) return;
    if (tagPanel.modelsDirty && !tagPanel.models.trim()) {
      setNotice({ kind: 'error', text: t('Models cannot be cleared with a tag update.') });
      return;
    }
    const input: TagUpdateInput = { tag: tagPanel.tag };
    if (tagPanel.newTag.trim() !== tagPanel.tag) input.newTag = tagPanel.newTag;
    if (tagPanel.modelsDirty) input.models = tagPanel.models;
    if (tagPanel.groups.trim()) input.groups = tagPanel.groups;
    if (tagPanel.modelMappingDirty) input.modelMapping = tagPanel.modelMapping;
    if (tagPanel.priority.trim()) input.priority = Number(tagPanel.priority);
    if (tagPanel.weight.trim()) input.weight = Number(tagPanel.weight);
    if (canSensitiveWrite && tagPanel.paramOverrideDirty) input.paramOverride = tagPanel.paramOverride;
    if (canSensitiveWrite && tagPanel.headerOverrideDirty) input.headerOverride = tagPanel.headerOverride;
    if (Object.keys(input).length === 1) {
      setNotice({ kind: 'error', text: t('Make at least one tag change first.') });
      return;
    }
    setBusyAction('tag-edit');
    setNotice(null);
    try {
      await updateTagChannels(input);
      setTagPanel(null);
      setNotice({ kind: 'success', text: t('Tagged channels updated.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to edit tagged channels.') });
    } finally {
      setBusyAction('');
    }
  }

  async function runTest(channel: ChannelSummary) {
    const action = `test:${channel.id}`;
    if (!canOperate || busyAction) return;
    setBusyAction(action);
    setNotice(null);
    try {
      const selectedModel = testModels[channel.id]?.trim() ?? '';
      const result = selectedModel
        ? await testChannel(channel.id, selectedModel)
        : await testChannel(channel.id);
      setNotice(result.success
        ? { kind: 'success', text: t('Channel test passed in {{time}} s.', { time: result.time.toFixed(2) }) }
        : {
            kind: 'error',
            text: result.message
              ? t('Channel test failed: {{message}}', { message: result.message })
              : t('Channel test failed.'),
          });
    } catch {
      setNotice({ kind: 'error', text: t('Channel test failed.') });
    } finally {
      setBusyAction('');
    }
  }

  async function toggleStatus(channel: ChannelSummary) {
    const action = `status:${channel.id}`;
    if (!canOperate || busyAction) return;
    const enabling = channel.status !== CHANNEL_ENABLED;
    setBusyAction(action);
    setNotice(null);
    try {
      await setChannelStatus(channel.id, enabling ? CHANNEL_ENABLED : CHANNEL_MANUALLY_DISABLED);
      setNotice({ kind: 'success', text: enabling ? t('Channel enabled.') : t('Channel disabled.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update channel status.') });
    } finally {
      setBusyAction('');
    }
  }

  async function showModels(channel: ChannelSummary) {
    const action = `models:${channel.id}`;
    if (!canOperate || busyAction) return;
    setBusyAction(action);
    setNotice(null);
    try {
      const models = await fetchChannelModels(channel.id);
      setModelPanel({ id: channel.id, name: channel.name, models });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to fetch upstream models.') });
    } finally {
      setBusyAction('');
    }
  }

  async function openEditor(channel: ChannelSummary) {
    const action = `edit-load:${channel.id}`;
    if (!canWrite || busyAction) return;
    setBusyAction(action);
    setNotice(null);
    setDraftDiscovery((current) => current?.target === 'edit' ? null : current);
    try {
      const detail = await fetchChannelDetail(channel.id, canSensitiveWrite);
      setEditing(detail);
      setEditingSourceType(detail.type);
    } catch {
      setEditing(null);
      setEditingSourceType(null);
      setNotice({ kind: 'error', text: t('Unable to load fresh channel details.') });
    } finally {
      setBusyAction('');
    }
  }

  async function discoverModelsFor(target: DiscoveryTarget) {
    if (!canSensitiveWrite || busyAction) return;
    const action = `draft-models:${target}`;
    setBusyAction(action);
    setNotice(null);
    setDraftDiscovery(null);
    try {
      const models = target === 'create'
        ? await discoverDraftModels({
            type: createDraft.type,
            key: createDraft.key,
            baseUrl: createDraft.base_url,
          })
        : editing?.type === 58 && editing.sensitive
          ? await discoverDraftModels({
              type: editing.type,
              channelId: editing.id,
              baseUrl: editing.sensitive.baseUrl,
              advancedCustom: editing.sensitive.providerSettings.advancedCustom,
              headerOverride: editing.sensitive.headerOverride,
            })
          : null;
      if (models === null) throw new Error('unsupported draft discovery');
      setDraftDiscovery({ target, models });
    } catch {
      setNotice({ kind: 'error', text: t('Unable to discover models from this draft.') });
    } finally {
      setBusyAction('');
    }
  }

  function chooseDiscoveredModels(target: DiscoveryTarget, merge: boolean) {
    if (draftDiscovery?.target !== target) return;
    if (target === 'edit') {
      setEditing((current) => current && ({
        ...current,
        models: applyDiscoveredModels(current.models, draftDiscovery.models, merge),
      }));
    } else {
      setCreateDraft((current) => ({
        ...current,
        models: applyDiscoveredModels(current.models, draftDiscovery.models, merge),
      }));
    }
    setDraftDiscovery(null);
  }

  function updateSensitiveDetail(
    field: Exclude<keyof NonNullable<ChannelDetail['sensitive']>, 'providerSettings'>,
    value: string,
  ) {
    setEditing((current) => current?.sensitive
      ? { ...current, sensitive: { ...current.sensitive, [field]: value } }
      : current);
  }

  function updateProviderSetting<Field extends keyof ChannelProviderSettings>(
    field: Field,
    value: ChannelProviderSettings[Field],
  ) {
    setEditing((current) => current?.sensitive
      ? {
          ...current,
          sensitive: {
            ...current.sensitive,
            providerSettings: { ...current.sensitive.providerSettings, [field]: value },
          },
        }
      : current);
  }

  async function saveEdit(event: React.FormEvent) {
    event.preventDefault();
    if (!canWrite || !editing || busyAction) return;
    setBusyAction(`edit:${editing.id}`);
    setNotice(null);
    try {
      const input: ChannelUpdateInput = {
        id: editing.id,
        name: editing.name,
        models: editing.models,
        group: editing.group,
        tag: editing.tag,
        remark: editing.remark,
        priority: editing.priority,
        weight: editing.weight,
        testModel: editing.testModel,
        autoBan: editing.autoBan,
        modelMapping: editing.modelMapping,
        statusCodeMapping: editing.statusCodeMapping,
      };
      if (canSensitiveWrite && editing.sensitive) {
        Object.assign(input, {
          type: editing.type,
          baseUrl: editing.sensitive.baseUrl,
          organization: editing.sensitive.organization,
          other: editing.sensitive.other,
          paramOverride: editing.sensitive.paramOverride,
          headerOverride: editing.sensitive.headerOverride,
          providerSettings: editing.sensitive.providerSettings,
        });
      }
      await updateChannel(input);
      setEditing(null);
      setEditingSourceType(null);
      setDraftDiscovery((current) => current?.target === 'edit' ? null : current);
      setNotice({ kind: 'success', text: t('Channel updated.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update channel.') });
    } finally {
      setBusyAction('');
    }
  }

  async function submitCreate(event: React.FormEvent) {
    event.preventDefault();
    if (busyAction || !canSensitiveWrite) return;
    setBusyAction('create');
    setNotice(null);
    try {
      await createChannel(createDraft);
      setCreateDraft(EMPTY_CREATE);
      setDraftDiscovery((current) => current?.target === 'create' ? null : current);
      setNotice({ kind: 'success', text: t('Channel created.') });
      refresh();
    } catch {
      setCreateDraft((current) => ({ ...current, key: '' }));
      setNotice({ kind: 'error', text: t('Unable to create channel.') });
    } finally {
      setBusyAction('');
    }
  }

  async function removeChannel(channel: ChannelSummary) {
    if (!canSensitiveWrite || busyAction || !window.confirm(t('Delete channel “{{name}}”?', { name: channel.name }))) return;
    setBusyAction(`delete:${channel.id}`);
    setNotice(null);
    try {
      await deleteChannel(channel.id);
      closePanelsFor([channel.id]);
      setNotice({ kind: 'success', text: t('Channel deleted.') });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to delete channel.') });
    } finally {
      setBusyAction('');
    }
  }

  if (!canRead) {
    return (
      <section className="card channel-admin" aria-labelledby="channel-admin-title">
        <h2 id="channel-admin-title">{t('Channels')}</h2>
        <p className="error" role="alert">{t('Your account does not have permission to view this page.')}</p>
      </section>
    );
  }

  return (
    <section className="card channel-admin" aria-labelledby="channel-admin-title">
      <div className="channel-heading">
        <div>
          <h2 id="channel-admin-title">{t('Channels')}</h2>
          <p className="muted">{t('Search, test, and safely update upstream channels.')}</p>
        </div>
        <button type="button" className="link" disabled={loading} onClick={refresh}>
          {loading ? t('Refreshing…') : t('Refresh')}
        </button>
      </div>

      {(canOperate || canWrite || canSensitiveWrite) && (
        <div className="channel-toolbar" aria-label={t('Channel maintenance actions')}>
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void startAllChannelTests()}>
            {busyAction === 'test-all' ? t('Starting tests…') : t('Test all channels')}
          </button>}
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void refreshAllBalances()}>
            {busyAction === 'balance-all' ? t('Refreshing balances…') : t('Refresh all balances')}
          </button>}
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void detectAllUpdates()}>
            {busyAction === 'upstream-detect-all' ? t('Starting detection…') : t('Detect all upstream updates')}
          </button>}
          {canWrite && <button type="button" disabled={Boolean(busyAction)} onClick={() => void applyAllUpdates()}>
            {busyAction === 'upstream-apply-all' ? t('Applying updates…') : t('Apply all staged updates')}
          </button>}
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void repairChannelConsistency()}>
            {busyAction === 'repair' ? t('Repairing…') : t('Repair channel consistency')}
          </button>}
          {canSensitiveWrite && (
            <button type="button" className="danger-link" disabled={Boolean(busyAction)} onClick={() => void removeAllDisabledChannels()}>
              {busyAction === 'delete-disabled' ? t('Deleting…') : t('Delete all disabled')}
            </button>
          )}
          {(canOperate || canWrite) && <form className="channel-inline-form" onSubmit={manageTag}>
            <label>
              {t('Manage tag')}
              <input value={tagLookup} maxLength={64} list="visible-channel-tags" placeholder={t('Enter a tag')} onChange={(event) => setTagLookup(event.target.value)} />
              <datalist id="visible-channel-tags">{visibleTags.map((tag) => <option key={tag} value={tag} />)}</datalist>
            </label>
            <button type="submit" disabled={Boolean(busyAction) || !tagLookup.trim()}>{t('Manage')}</button>
          </form>}
        </div>
      )}

      <form className="channel-filters" role="search" aria-label={t('Search channels')} onSubmit={submitSearch}>
        <label>
          {t('Search channels')}
          <input
            value={draft.keyword}
            maxLength={128}
            placeholder={t('Name or channel ID')}
            onChange={(event) => setDraft((current) => ({ ...current, keyword: event.target.value }))}
          />
        </label>
        <label>
          {t('Group')}
          <input value={draft.group} maxLength={512} onChange={(event) => setDraft((current) => ({ ...current, group: event.target.value }))} />
        </label>
        <label>
          {t('Model')}
          <input
            value={draft.model}
            maxLength={255}
            list="enabled-channel-models"
            onChange={(event) => setDraft((current) => ({ ...current, model: event.target.value }))}
          />
          <datalist id="enabled-channel-models">{catalog.map((model) => <option key={model} value={model} />)}</datalist>
        </label>
        <label>
          {t('Status')}
          <select value={draft.status} onChange={(event) => setDraft((current) => ({ ...current, status: event.target.value as SearchDraft['status'] }))}>
            <option value="">{t('All statuses')}</option>
            <option value="enabled">{t('Enabled')}</option>
            <option value="disabled">{t('Disabled')}</option>
          </select>
        </label>
        <label>
          {t('Type')}
          <select
            value={draft.type ?? ''}
            onChange={(event) => setDraft((current) => ({ ...current, type: event.target.value === '' ? null : Number(event.target.value) }))}
          >
            <option value="">{t('All types')}</option>
            {typeOptions.map((type) => <option key={type} value={type}>{type} ({typeCounts[type] ?? 0})</option>)}
          </select>
        </label>
        <label>
          {t('Sort by')}
          <select value={draft.sortBy} onChange={(event) => setDraft((current) => ({ ...current, sortBy: event.target.value as SearchDraft['sortBy'] }))}>
            <option value="">{t('Default priority order')}</option>
            <option value="id">{t('ID')}</option>
            <option value="name">{t('Name')}</option>
            <option value="priority">{t('Priority')}</option>
            <option value="balance">{t('Balance')}</option>
            <option value="response_time">{t('Response time')}</option>
            <option value="test_time">{t('Test time')}</option>
          </select>
        </label>
        <label>
          {t('Sort order')}
          <select value={draft.sortOrder} disabled={!draft.sortBy} onChange={(event) => setDraft((current) => ({ ...current, sortOrder: event.target.value as SearchDraft['sortOrder'] }))}>
            <option value="desc">{t('Descending')}</option>
            <option value="asc">{t('Ascending')}</option>
          </select>
        </label>
        <label className="channel-checkbox">
          <input type="checkbox" checked={draft.tagMode} onChange={(event) => setDraft((current) => ({ ...current, tagMode: event.target.checked }))} />
          {t('Keep matching tags together')}
        </label>
        <label className="channel-checkbox">
          <input type="checkbox" checked={draft.idSort} disabled={Boolean(draft.sortBy)} onChange={(event) => setDraft((current) => ({ ...current, idSort: event.target.checked }))} />
          {t('Prefer ID order')}
        </label>
        <div className="channel-filter-actions">
          <button type="submit">{t('Apply filters')}</button>
          <button type="button" className="link" onClick={clearSearch}>{t('Clear filters')}</button>
        </div>
      </form>

      {canSelect && selectedIDs.length > 0 && (
        <section className="channel-bulk" aria-label={t('Selected channel actions')}>
          <p role="status">{t('{{count}} channels selected.', { count: selectedIDs.length })}</p>
          <div className="channel-filter-actions">
            {canOperate && <button type="button" disabled={loading || Boolean(busyAction)} onClick={() => void updateSelectedStatus(true)}>{t('Enable selected')}</button>}
            {canOperate && <button type="button" disabled={loading || Boolean(busyAction)} onClick={() => void updateSelectedStatus(false)}>{t('Disable selected')}</button>}
            {canSensitiveWrite && <button type="button" className="danger-link" disabled={loading || Boolean(busyAction)} onClick={() => void removeSelected()}>{busyAction === 'bulk-delete' ? t('Deleting…') : t('Delete selected')}</button>}
            <button type="button" className="link" disabled={loading || Boolean(busyAction)} onClick={() => setSelected(new Set())}>{t('Clear selection')}</button>
          </div>
          {canWrite && <form className="channel-inline-form" onSubmit={tagSelected}>
            <label>{t('Tag selected channels')}<input value={bulkTag} maxLength={64} placeholder={t('Leave blank to remove the tag')} onChange={(event) => setBulkTag(event.target.value)} /></label>
            <button type="submit" disabled={loading || Boolean(busyAction)}>{busyAction === 'bulk-tag' ? t('Saving tag…') : t('Set tag')}</button>
          </form>}
        </section>
      )}

      {catalogError && <p className="muted" role="status">{t('Model suggestions are unavailable.')}</p>}
      {notice && <p className={notice.kind} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      {loadError && (
        <div className="channel-state" role="alert">
          <p className="error">{t('Unable to load channels.')}</p>
          <button type="button" onClick={refresh}>{t('Try again')}</button>
        </div>
      )}
      {!loadError && (
        <div className="table-scroll" aria-busy={loading}>
          <table aria-busy={loading}>
            <caption className="sr-only">{t('Channel results')}</caption>
            <thead>
              <tr>
                {canSelect && <th scope="col">
                  <input
                    type="checkbox"
                    aria-label={t('Select all channels on this page')}
                    aria-checked={someVisibleSelected && !allVisibleSelected ? 'mixed' : allVisibleSelected}
                    ref={(node) => { if (node) node.indeterminate = someVisibleSelected && !allVisibleSelected; }}
                    checked={allVisibleSelected}
                    disabled={loading || Boolean(busyAction) || channels.length === 0}
                    onChange={toggleAllVisible}
                  />
                </th>}
                <th scope="col">{t('Name')}</th>
                <th scope="col">{t('Status')}</th>
                <th scope="col">{t('Group')}</th>
                <th scope="col">{t('Models')}</th>
                <th scope="col">{t('Balance')}</th>
                <th scope="col">{t('Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {loading && channels.length === 0 && <tr><td colSpan={canSelect ? 7 : 6} role="status">{t('Loading channels…')}</td></tr>}
              {!loading && channels.length === 0 && <tr><td colSpan={canSelect ? 7 : 6}>{t('No channels match these filters.')}</td></tr>}
              {channels.map((channel) => {
                const anyBusy = Boolean(busyAction);
                return (
                  <tr key={channel.id}>
                    {canSelect && <td><input type="checkbox" aria-label={t('Select channel {{name}}', { name: channel.name })} checked={selected.has(channel.id)} disabled={anyBusy} onChange={() => toggleSelected(channel.id)} /></td>}
                    <td><strong>{channel.name}</strong><span className="channel-meta">#{channel.id} · {channelProviderLabel(channel.type)}</span></td>
                    <td><span className={channel.status === CHANNEL_ENABLED ? 'status-pill enabled' : 'status-pill disabled'}>{channelStatusLabel(channel.status, t)}</span></td>
                    <td>{channel.group || '—'}{channel.tag && <span className="channel-meta">{channel.tag}</span>}</td>
                    <td className="channel-model-summary" title={channel.models}>{modelSummary(channel.models)}</td>
                    <td>{supportsBalance(channel) ? channel.balance.toFixed(2) : '—'}{supportsBalance(channel) && channel.balanceUpdatedTime > 0 && <span className="channel-meta">{new Date(channel.balanceUpdatedTime * 1000).toLocaleString()}</span>}</td>
                    <td>
                      <div className="channel-actions">
                        {canOperate && <>
                          <input
                            aria-label={t('Test model for {{name}}', { name: channel.name })}
                            list={`channel-test-models-${channel.id}`}
                            value={testModels[channel.id] ?? ''}
                            maxLength={255}
                            placeholder={channel.testModel || t('Default test model')}
                            disabled={anyBusy}
                            onChange={(event) => setTestModels((current) => ({ ...current, [channel.id]: event.target.value }))}
                          />
                          <datalist id={`channel-test-models-${channel.id}`}>
                            {modelValues(channel.models).map((model) => <option key={model} value={model} />)}
                          </datalist>
                        </>}
                        {canOperate && <button type="button" className="link" disabled={anyBusy} onClick={() => void runTest(channel)}>{busyAction === `test:${channel.id}` ? t('Testing…') : t('Test')}</button>}
                        {canOperate && <button type="button" className="link" disabled={anyBusy} onClick={() => void toggleStatus(channel)}>{busyAction === `status:${channel.id}` ? t('Updating…') : channel.status === CHANNEL_ENABLED ? t('Disable') : t('Enable')}</button>}
                        {canOperate && UPSTREAM_DISCOVERY_TYPES.has(channel.type) && <button type="button" className="link" disabled={anyBusy} onClick={() => void showModels(channel)}>{busyAction === `models:${channel.id}` ? t('Fetching…') : t('Fetch models')}</button>}
                        {canOperate && supportsBalance(channel) && <button type="button" className="link" disabled={anyBusy} onClick={() => void refreshBalance(channel)}>{busyAction === `balance:${channel.id}` ? t('Refreshing…') : t('Refresh balance')}</button>}
                        {UPSTREAM_DISCOVERY_TYPES.has(channel.type) && (canOperate || (canWrite && channel.pendingAddModels.length + channel.pendingRemoveModels.length > 0)) && <button type="button" className="link" disabled={anyBusy} aria-expanded={upstreamPanel?.id === channel.id} onClick={() => setUpstreamPanel(channel)}>{channel.pendingAddModels.length + channel.pendingRemoveModels.length > 0 ? t('Review updates') : t('Upstream updates')}</button>}
                        {canOperate && channel.isMultiKey && <button type="button" className="link" disabled={anyBusy} aria-expanded={multiKeyPanel?.id === channel.id} onClick={() => setMultiKeyPanel(channel)}>{t('Manage keys')}</button>}
                        {channel.type === CHANNEL_TYPE_CODEX && <button type="button" className="link" disabled={anyBusy} aria-expanded={codexPanel?.id === channel.id} onClick={() => setCodexPanel(channel)}>{t('Codex usage')}</button>}
                        {channel.type === CHANNEL_TYPE_OLLAMA && canSensitiveWrite && <button type="button" className="link" disabled={anyBusy} aria-expanded={ollamaPanel?.id === channel.id} onClick={() => setOllamaPanel(channel)}>{t('Manage Ollama models')}</button>}
                        {isRoot && <button type="button" className="link" disabled={anyBusy} aria-expanded={keyRevealChannel?.id === channel.id} onClick={() => setKeyRevealChannel(channel)}>{t('View key')}</button>}
                        {canWrite && <button type="button" className="link" disabled={anyBusy} aria-expanded={editing?.id === channel.id} aria-controls="channel-edit-panel" onClick={() => void openEditor(channel)}>{busyAction === `edit-load:${channel.id}` ? t('Loading…') : t('Edit')}</button>}
                        {canSensitiveWrite && <button type="button" className="link" disabled={anyBusy} aria-expanded={copyPanel?.channel.id === channel.id} onClick={() => setCopyPanel({ channel, suffix: '_copy', resetBalance: true })}>{t('Copy')}</button>}
                        {canSensitiveWrite && <button type="button" className="link danger-link" disabled={anyBusy} onClick={() => void removeChannel(channel)}>{busyAction === `delete:${channel.id}` ? t('Deleting…') : t('Delete')}</button>}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {!loadError && total > 0 && (
        <nav className="channel-pagination" aria-label={t('Channel pages')}>
          <button type="button" disabled={loading || query.page <= 1} onClick={() => { setSelected(new Set()); setQuery((current) => ({ ...current, page: current.page - 1 })); }}>{t('Previous')}</button>
          <span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span>
          <button type="button" disabled={loading || query.page >= pageCount} onClick={() => { setSelected(new Set()); setQuery((current) => ({ ...current, page: current.page + 1 })); }}>{t('Next')}</button>
        </nav>
      )}

      {copyPanel && canSensitiveWrite && (
        <form className="channel-subpanel grid-form" onSubmit={saveCopy} aria-labelledby="copy-channel-title">
          <h3 id="copy-channel-title">{t('Copy channel {{name}}', { name: copyPanel.channel.name })}</h3>
          <label>{t('Name suffix')}<input value={copyPanel.suffix} maxLength={64} onChange={(event) => setCopyPanel((current) => current && ({ ...current, suffix: event.target.value }))} /></label>
          <label className="channel-checkbox"><input type="checkbox" checked={copyPanel.resetBalance} onChange={(event) => setCopyPanel((current) => current && ({ ...current, resetBalance: event.target.checked }))} /> {t('Reset balance and used quota')}</label>
          <p className="muted">{t('New channel name: {{name}}', { name: `${copyPanel.channel.name}${copyPanel.suffix}` })}</p>
          <div className="channel-filter-actions">
            <button type="submit" disabled={Boolean(busyAction)}>{busyAction === `copy:${copyPanel.channel.id}` ? t('Copying…') : t('Copy channel')}</button>
            <button type="button" className="link" disabled={Boolean(busyAction)} onClick={() => setCopyPanel(null)}>{t('Cancel')}</button>
          </div>
        </form>
      )}

      {tagPanel && (canOperate || canWrite) && (
        <form className="channel-subpanel grid-form" onSubmit={saveTag} aria-labelledby="tag-channel-title">
          <div className="channel-heading">
            <div><h3 id="tag-channel-title">{t('Manage tag {{tag}}', { tag: tagPanel.tag })}</h3><p className="muted">{t('Changes apply to every channel carrying this tag.')}</p></div>
            <button type="button" className="link" disabled={Boolean(busyAction)} onClick={() => setTagPanel(null)}>{t('Close')}</button>
          </div>
          {canOperate && <div className="channel-filter-actions">
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void setTagStatus(true)}>{t('Enable tagged channels')}</button>
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void setTagStatus(false)}>{t('Disable tagged channels')}</button>
          </div>}
          {tagLoadError && <div role="alert"><span className="error">{t('Unable to load tag models.')}</span> <button type="button" className="link" onClick={() => setTagReload((value) => value + 1)}>{t('Try again')}</button></div>}
          {canWrite && <>
            <label>{t('New tag')}<input value={tagPanel.newTag} maxLength={64} placeholder={t('Leave blank to remove the tag')} onChange={(event) => setTagPanel((current) => current && ({ ...current, newTag: event.target.value }))} /></label>
            <label>{t('Models')}<textarea rows={4} value={tagPanel.models} maxLength={256 * 1024} disabled={tagLoading} onChange={(event) => setTagPanel((current) => current && ({ ...current, models: event.target.value, modelsDirty: true }))} /></label>
            <label>{t('Groups')}<input value={tagPanel.groups} maxLength={64} placeholder={t('Leave blank to keep existing groups')} onChange={(event) => setTagPanel((current) => current && ({ ...current, groups: event.target.value }))} /></label>
            <label>{t('Priority')}<input type="number" value={tagPanel.priority} onChange={(event) => setTagPanel((current) => current && ({ ...current, priority: event.target.value }))} /></label>
            <label>{t('Weight')}<input type="number" min={0} max={4_294_967_295} value={tagPanel.weight} onChange={(event) => setTagPanel((current) => current && ({ ...current, weight: event.target.value }))} /></label>
            <label>{t('Model mapping')}<textarea rows={3} value={tagPanel.modelMapping} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setTagPanel((current) => current && ({ ...current, modelMapping: event.target.value, modelMappingDirty: true }))} /></label>
            {canSensitiveWrite && <label>{t('Parameter override')}<textarea rows={3} value={tagPanel.paramOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setTagPanel((current) => current && ({ ...current, paramOverride: event.target.value, paramOverrideDirty: true }))} /></label>}
            {canSensitiveWrite && <label>{t('Header override')}<textarea rows={3} value={tagPanel.headerOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setTagPanel((current) => current && ({ ...current, headerOverride: event.target.value, headerOverrideDirty: true }))} /></label>}
            <p className="muted">{t('Leave optional fields blank to keep their current values.')}</p>
          </>}
          <div className="channel-filter-actions">
            {canWrite && <button type="submit" disabled={tagLoading || Boolean(busyAction)}>{busyAction === 'tag-edit' ? t('Saving…') : t('Save tag changes')}</button>}
            <button type="button" className="link" disabled={Boolean(busyAction)} onClick={() => setTagPanel(null)}>{t('Cancel')}</button>
          </div>
        </form>
      )}

      {multiKeyPanel && canOperate && <MultiKeyPanel channel={multiKeyPanel} canOperate={canOperate} canSensitiveWrite={canSensitiveWrite} onClose={() => setMultiKeyPanel(null)} onChanged={refresh} notify={notify} />}
      {upstreamPanel && (canOperate || canWrite) && <UpstreamUpdatesPanel channel={upstreamPanel} canOperate={canOperate} canWrite={canWrite} onClose={() => setUpstreamPanel(null)} onChanged={refresh} notify={notify} />}
      {codexPanel && <CodexPanel channel={codexPanel} canOperate={canOperate} canSensitiveWrite={canSensitiveWrite} onClose={() => setCodexPanel(null)} notify={notify} />}
      {ollamaPanel && canSensitiveWrite && <OllamaPanel channel={ollamaPanel} canOperate={canOperate} onClose={() => setOllamaPanel(null)} notify={notify} />}
      {keyRevealChannel && isRoot && (
        <ChannelKeyRevealPanel
          key={keyRevealChannel.id}
          channel={keyRevealChannel}
          onClose={() => setKeyRevealChannel(null)}
        />
      )}

      {modelPanel && canOperate && (
        <section className="channel-subpanel" aria-labelledby="channel-model-panel-title">
          <div className="channel-heading">
            <h3 id="channel-model-panel-title">{t('Upstream models for {{name}}', { name: modelPanel.name })}</h3>
            <button type="button" className="link" onClick={() => setModelPanel(null)}>{t('Close')}</button>
          </div>
          {modelPanel.models.length === 0
            ? <p className="muted">{t('No upstream models returned.')}</p>
            : <ul className="channel-model-list">{modelPanel.models.map((model) => <li key={model}><code>{model}</code></li>)}</ul>}
        </section>
      )}

      {editing && canWrite && (
        <form id="channel-edit-panel" className="channel-subpanel grid-form" onSubmit={saveEdit}>
          <h3>{t('Edit channel {{name}}', { name: editing.name })}</h3>
          <label>{t('Name')}<input value={editing.name} maxLength={191} required onChange={(event) => setEditing((current) => current && ({ ...current, name: event.target.value }))} /></label>
          <label>{t('Group')}<input value={editing.group} maxLength={64} onChange={(event) => setEditing((current) => current && ({ ...current, group: event.target.value }))} /></label>
          <label>{t('Models')}<textarea rows={4} value={editing.models} maxLength={256 * 1024} onChange={(event) => setEditing((current) => current && ({ ...current, models: event.target.value }))} /></label>
          {canSensitiveWrite && editing.sensitive && editing.type === 58 && editingSourceType === 58 && (
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void discoverModelsFor('edit')}>
              {busyAction === 'draft-models:edit' ? t('Discovering…') : t('Discover models from draft')}
            </button>
          )}
          {draftDiscovery?.target === 'edit' && (
            <fieldset>
              <legend>{t('Discovered {{count}} upstream models', { count: draftDiscovery.models.length })}</legend>
              <p className="muted">{t('Merge keeps the current draft. Replace removes any model the upstream did not return.')}</p>
              <div className="channel-filter-actions">
                <button type="button" onClick={() => chooseDiscoveredModels('edit', true)}>{t('Merge models')}</button>
                <button type="button" onClick={() => chooseDiscoveredModels('edit', false)}>{t('Replace all models')}</button>
                <button type="button" className="link" onClick={() => setDraftDiscovery(null)}>{t('Cancel')}</button>
              </div>
            </fieldset>
          )}
          <label>{t('Tag')}<input value={editing.tag} maxLength={64} onChange={(event) => setEditing((current) => current && ({ ...current, tag: event.target.value }))} /></label>
          <label>{t('Remark')}<input value={editing.remark} maxLength={255} onChange={(event) => setEditing((current) => current && ({ ...current, remark: event.target.value }))} /></label>
          <label>{t('Priority')}<input type="number" value={editing.priority} onChange={(event) => setEditing((current) => current && ({ ...current, priority: Number(event.target.value) }))} /></label>
          <label>{t('Weight')}<input type="number" min={0} max={4_294_967_295} value={editing.weight} onChange={(event) => setEditing((current) => current && ({ ...current, weight: Number(event.target.value) }))} /></label>
          <label>{t('Test model')}<input list="enabled-channel-models" value={editing.testModel} maxLength={255} onChange={(event) => setEditing((current) => current && ({ ...current, testModel: event.target.value }))} /></label>
          <label className="channel-checkbox"><input type="checkbox" checked={editing.autoBan === 1} onChange={(event) => setEditing((current) => current && ({ ...current, autoBan: event.target.checked ? 1 : 0 }))} /> {t('Automatically disable on failed tests')}</label>
          <label>{t('Model mapping')}<textarea rows={3} value={editing.modelMapping} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setEditing((current) => current && ({ ...current, modelMapping: event.target.value }))} /></label>
          <label>{t('Status code mapping')}<textarea rows={3} value={editing.statusCodeMapping} maxLength={1_024} placeholder="{}" onChange={(event) => setEditing((current) => current && ({ ...current, statusCodeMapping: event.target.value }))} /></label>
          {canSensitiveWrite && editing.sensitive ? <fieldset>
            <legend>{t('Sensitive provider settings')}</legend>
            <p className="muted">{t('The stored API key remains masked and is never loaded into this form.')}</p>
            <label>
              {t('Type')}
              <select
                value={editing.type}
                onChange={(event) => {
                  const type = Number(event.target.value);
                  if (isChannelProviderType(type)) setEditing((current) => current && ({ ...current, type }));
                }}
              >
                {!isChannelProviderType(editing.type) && <option value={editing.type}>{channelProviderLabel(editing.type)}</option>}
                {CHANNEL_PROVIDER_OPTIONS.map((provider) => <option key={provider.value} value={provider.value}>{provider.label} (#{provider.value})</option>)}
              </select>
            </label>
            <label>{t('Base URL')}<input value={editing.sensitive.baseUrl} maxLength={4_096} onChange={(event) => updateSensitiveDetail('baseUrl', event.target.value)} /></label>
            <label>{t('OpenAI organization')}<input value={editing.sensitive.organization} maxLength={512} onChange={(event) => updateSensitiveDetail('organization', event.target.value)} /></label>
            {editing.sensitive.other === undefined
              ? <p className="muted">{t('The legacy provider field contains a credential collection and remains masked.')}</p>
              : <label>{providerOtherLabel(editing.type, t)}<textarea rows={2} value={editing.sensitive.other} maxLength={256 * 1024} onChange={(event) => updateSensitiveDetail('other', event.target.value)} /></label>}
            <label>{t('Parameter override')}<textarea rows={3} value={editing.sensitive.paramOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => updateSensitiveDetail('paramOverride', event.target.value)} /></label>
            <label>{t('Header override')}<textarea rows={3} value={editing.sensitive.headerOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => updateSensitiveDetail('headerOverride', event.target.value)} /></label>
            <label>{t('Balance URL')}<input value={editing.sensitive.providerSettings.balanceUrl} maxLength={4_096} onChange={(event) => updateProviderSetting('balanceUrl', event.target.value)} /></label>
            {editing.type === 3 && <label>{t('Azure Responses API version')}<input value={editing.sensitive.providerSettings.azureResponsesVersion} maxLength={128} onChange={(event) => updateProviderSetting('azureResponsesVersion', event.target.value)} /></label>}
            {editing.type === 41 && <label>
              {t('Vertex AI key format')}
              <select value={editing.sensitive.providerSettings.vertexKeyType} onChange={(event) => updateProviderSetting('vertexKeyType', event.target.value as 'json' | 'api_key')}>
                <option value="json">{t('Service account JSON')}</option>
                <option value="api_key">{t('API key')}</option>
              </select>
            </label>}
            {editing.type === 33 && <>
              <label>
                {t('AWS key format')}
                <select value={editing.sensitive.providerSettings.awsKeyType} onChange={(event) => updateProviderSetting('awsKeyType', event.target.value as 'auto' | 'ak_sk' | 'api_key')}>
                  <option value="auto">{t('Auto-detect')}</option>
                  <option value="ak_sk">{t('Access key and secret')}</option>
                  <option value="api_key">{t('API key')}</option>
                </select>
              </label>
              <label className="channel-checkbox"><input type="checkbox" checked={editing.sensitive.providerSettings.passThroughBodyEnabled} onChange={(event) => updateProviderSetting('passThroughBodyEnabled', event.target.checked)} /> {t('Pass request body through')}</label>
            </>}
            {editing.type === 58 && <label>{t('Advanced Custom routes')}<textarea required rows={8} value={editing.sensitive.providerSettings.advancedCustom} maxLength={512 * 1024} placeholder="{}" onChange={(event) => updateProviderSetting('advancedCustom', event.target.value)} /></label>}
            <label className="channel-checkbox"><input type="checkbox" checked={editing.sensitive.providerSettings.upstreamCheckEnabled} onChange={(event) => updateProviderSetting('upstreamCheckEnabled', event.target.checked)} /> {t('Check upstream model changes')}</label>
            <label className="channel-checkbox"><input type="checkbox" disabled={!editing.sensitive.providerSettings.upstreamCheckEnabled} checked={editing.sensitive.providerSettings.upstreamCheckEnabled && editing.sensitive.providerSettings.upstreamAutoSyncEnabled} onChange={(event) => updateProviderSetting('upstreamAutoSyncEnabled', event.target.checked)} /> {t('Automatically add discovered models')}</label>
            <label>{t('Ignored upstream models')}<textarea rows={2} value={editing.sensitive.providerSettings.upstreamIgnoredModels} maxLength={256 * 1024} placeholder={t('Comma-separated model names or regex: patterns')} onChange={(event) => updateProviderSetting('upstreamIgnoredModels', event.target.value)} /></label>
          </fieldset> : <p className="muted">{t('Sensitive provider fields are hidden because this account lacks sensitive-write permission.')}</p>}
          <div className="channel-filter-actions">
            <button type="submit" disabled={busyAction === `edit:${editing.id}`}>{busyAction === `edit:${editing.id}` ? t('Saving…') : t('Save changes')}</button>
            <button type="button" className="link" disabled={busyAction === `edit:${editing.id}`} onClick={() => { setEditing(null); setEditingSourceType(null); setDraftDiscovery((current) => current?.target === 'edit' ? null : current); }}>{t('Cancel')}</button>
          </div>
        </form>
      )}

      {canSensitiveWrite && (
        <details className="channel-create">
          <summary>{t('Create channel')}</summary>
          <form className="grid-form" onSubmit={submitCreate}>
            <p className="muted">{t('API keys are sent once and are never displayed by this screen.')}</p>
            <label>
              {t('Add mode')}
              <select value={createDraft.mode} onChange={(event) => setCreateDraft((current) => ({ ...current, mode: event.target.value as ChannelCreateInput['mode'] }))}>
                <option value="single">{t('Single key')}</option>
                {createDraft.type !== CHANNEL_TYPE_CODEX && <option value="batch">{t('Batch channels (one key per line)')}</option>}
                {createDraft.type !== CHANNEL_TYPE_CODEX && <option value="multi_to_single">{t('Multiple keys in one channel')}</option>}
              </select>
            </label>
            {createDraft.mode === 'multi_to_single' && (
              <label>
                {t('Multi-key strategy')}
                <select value={createDraft.multi_key_mode} onChange={(event) => setCreateDraft((current) => ({ ...current, multi_key_mode: event.target.value as ChannelCreateInput['multi_key_mode'] }))}>
                  <option value="random">{t('Random')}</option>
                  <option value="polling">{t('Polling')}</option>
                </select>
              </label>
            )}
            {createDraft.mode === 'batch' && (
              <label className="channel-checkbox">
                <input type="checkbox" checked={createDraft.batch_add_set_key_prefix_2_name} onChange={(event) => setCreateDraft((current) => ({ ...current, batch_add_set_key_prefix_2_name: event.target.checked }))} />
                {t('Add a key fingerprint to each batch channel name')}
              </label>
            )}
            <label>{t('Name')}<input value={createDraft.name} maxLength={191} required onChange={(event) => setCreateDraft((current) => ({ ...current, name: event.target.value }))} /></label>
            <label>
              {t('Type')}
              <select
                value={createDraft.type}
                required
                onChange={(event) => {
                  const type = Number(event.target.value);
                  if (!isChannelProviderType(type)) return;
                  setDraftDiscovery((current) => current?.target === 'create' ? null : current);
                  setCreateDraft((current) => ({
                    ...current,
                    type,
                    ...(type === CHANNEL_TYPE_CODEX ? { mode: 'single' as const } : {}),
                  }));
                }}
              >
                {CHANNEL_PROVIDER_OPTIONS
                  // Advanced Custom creation requires a route configuration
                  // that this bounded create form does not submit. Keep it
                  // editable without offering a create action that must fail.
                  .filter((provider) => provider.value !== 58)
                  .map((provider) => <option key={provider.value} value={provider.value}>{provider.label} (#{provider.value})</option>)}
              </select>
            </label>
            {createDraft.mode === 'single' ? (
              <label>{t('API key')}<input type="password" autoComplete="new-password" value={createDraft.key} maxLength={512 * 1024} onChange={(event) => setCreateDraft((current) => ({ ...current, key: event.target.value }))} /></label>
            ) : (
              <label>{t('API keys, one per line')}<textarea className="channel-secret-textarea" rows={6} autoComplete="off" spellCheck={false} value={createDraft.key} maxLength={512 * 1024} onChange={(event) => setCreateDraft((current) => ({ ...current, key: event.target.value }))} /></label>
            )}
            <label>{t('Base URL')}<input type="url" value={createDraft.base_url} maxLength={4_096} placeholder="https://api.example.com" onChange={(event) => setCreateDraft((current) => ({ ...current, base_url: event.target.value }))} /></label>
            <label>{t('Models')}<textarea rows={3} value={createDraft.models} maxLength={256 * 1024} onChange={(event) => setCreateDraft((current) => ({ ...current, models: event.target.value }))} /></label>
            {UPSTREAM_DISCOVERY_TYPES.has(createDraft.type) && createDraft.type !== 58 && (
              <button type="button" disabled={Boolean(busyAction)} onClick={() => void discoverModelsFor('create')}>
                {busyAction === 'draft-models:create' ? t('Discovering…') : t('Discover models from draft')}
              </button>
            )}
            {draftDiscovery?.target === 'create' && (
              <fieldset>
                <legend>{t('Discovered {{count}} upstream models', { count: draftDiscovery.models.length })}</legend>
                <p className="muted">{t('Merge keeps the current draft. Replace removes any model the upstream did not return.')}</p>
                <div className="channel-filter-actions">
                  <button type="button" onClick={() => chooseDiscoveredModels('create', true)}>{t('Merge models')}</button>
                  <button type="button" onClick={() => chooseDiscoveredModels('create', false)}>{t('Replace all models')}</button>
                  <button type="button" className="link" onClick={() => setDraftDiscovery(null)}>{t('Cancel')}</button>
                </div>
              </fieldset>
            )}
            <label>{t('Group')}<input value={createDraft.group} maxLength={64} onChange={(event) => setCreateDraft((current) => ({ ...current, group: event.target.value }))} /></label>
            <button type="submit" disabled={busyAction === 'create'}>{busyAction === 'create' ? t('Creating…') : t('Add channel')}</button>
          </form>
        </details>
      )}
    </section>
  );
}
