import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  applyAllUpstreamUpdates,
  CHANNEL_ENABLED,
  CHANNEL_MANUALLY_DISABLED,
  CHANNEL_PAGE_SIZE,
  copyChannel,
  createChannel,
  deleteChannel,
  deleteChannels,
  deleteDisabledChannels,
  detectAllUpstreamUpdates,
  discoverDraftModels,
  fetchChannelDetail,
  fetchChannelModels,
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
  testAllChannels,
  testChannel,
  updateChannel,
  updateTagChannels,
  type ChannelCreateInput,
  type ChannelDetail,
  type ChannelProviderSettings,
  type ChannelSearchInput,
  type ChannelSummary,
  type ChannelUpdateInput,
  type TagUpdateInput,
} from './channel-api';
import {
  applyDiscoveredModels,
  EMPTY_CREATE,
  EMPTY_SEARCH,
  type CopyPanel,
  type DiscoveryTarget,
  type DraftDiscovery,
  type Notice,
  type SearchDraft,
  type TagPanel,
} from './channel-editor-model';
import type { ChannelAdminViewProps } from './channel-view-props';

export function useChannelController({
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

  return {
    allVisibleSelected, applyAllUpdates, bulkTag, busyAction,
    canOperate, canRead, canSelect, canSensitiveWrite,
    canWrite, catalog, catalogError, channels,
    chooseDiscoveredModels, clearSearch, codexPanel, copyPanel,
    createDraft, detectAllUpdates, discoverModelsFor, draft,
    draftDiscovery, editing, editingSourceType, isRoot,
    keyRevealChannel, loadError, loading, manageTag,
    modelPanel, multiKeyPanel, notice, notify,
    ollamaPanel, openEditor, pageCount, query,
    refresh, refreshAllBalances, refreshBalance, removeAllDisabledChannels,
    removeChannel, removeSelected, repairChannelConsistency, runTest,
    saveCopy, saveEdit, saveTag, selected,
    selectedIDs, setBulkTag, setCodexPanel, setCopyPanel,
    setCreateDraft, setDraft, setDraftDiscovery, setEditing,
    setEditingSourceType, setKeyRevealChannel, setModelPanel, setMultiKeyPanel,
    setOllamaPanel, setQuery, setSelected, setTagLookup,
    setTagPanel, setTagReload, setTagStatus, setTestModels,
    setUpstreamPanel, showModels, someVisibleSelected, startAllChannelTests,
    submitCreate, submitSearch, t, tagLoadError,
    tagLoading, tagLookup, tagPanel, tagSelected,
    testModels, toggleAllVisible, toggleSelected, toggleStatus,
    total, typeCounts, typeOptions, updateProviderSetting,
    updateSelectedStatus, updateSensitiveDetail, upstreamPanel, visibleTags,
  };
}

export type ChannelAdminViewController = ReturnType<typeof useChannelController>;
