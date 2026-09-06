import {
  CHANNEL_AUTO_DISABLED,
  CHANNEL_ENABLED,
  CHANNEL_MANUALLY_DISABLED,
  type ChannelCreateInput,
  type ChannelSearchInput,
  type ChannelSummary
} from './channel-api';

export type SearchDraft = Pick<
  ChannelSearchInput,
  'keyword' | 'group' | 'model' | 'status' | 'type' | 'tagMode' | 'idSort' | 'sortBy' | 'sortOrder'
>;

export type Notice = { kind: 'success' | 'error'; text: string } | null;

export type DiscoveryTarget = 'edit' | 'create';

export type DraftDiscovery = { target: DiscoveryTarget; models: string[] };

export type CopyPanel = { channel: ChannelSummary; suffix: string; resetBalance: boolean };

export type TagPanel = {
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

export const EMPTY_SEARCH: SearchDraft = {
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

export const EMPTY_CREATE: ChannelCreateInput = {
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

export // Mirrors the target backend's model-discovery capability gate.
const UPSTREAM_DISCOVERY_TYPES = new Set([
  1, 3, 4, 6, 7, 8, 9, 10, 12, 13, 14, 17, 19, 20, 22, 24, 25, 26, 27, 31,
  40, 42, 43, 45, 47, 48, 57, 58, 59, 60,
]);

export function modelSummary(models: string): string {
  const values = models.split(',').map((value) => value.trim()).filter(Boolean);
  if (values.length === 0) return '—';
  const visible = values.slice(0, 3).join(', ');
  return values.length > 3 ? `${visible} +${values.length - 3}` : visible;
}

export function modelValues(models: string): string[] {
  return [...new Set(models.split(',').map((value) => value.trim()).filter(Boolean))];
}

export function applyDiscoveredModels(current: string, discovered: string[], merge: boolean): string {
  return (merge ? [...new Set([...modelValues(current), ...discovered])] : discovered).join(',');
}

export function providerOtherLabel(type: number, t: (key: string) => string): string {
  if (type === 3) return t('Default API version');
  if (type === 18) return t('Model version');
  if (type === 21) return t('Knowledge base ID');
  if (type === 39) return t('Account ID');
  if (type === 41) return t('Deployment region');
  if (type === 49) return t('Agent ID');
  return t('Provider-specific value');
}

export function channelStatusLabel(status: number, t: (key: string) => string): string {
  if (status === CHANNEL_ENABLED) return t('Enabled');
  if (status === CHANNEL_AUTO_DISABLED) return t('Automatically disabled');
  if (status === CHANNEL_MANUALLY_DISABLED) return t('Manually disabled');
  return t('Disabled');
}

export const BALANCE_PROVIDER_TYPES = new Set([1, 8, 10, 12, 13, 20, 25, 40, 43]);

export function supportsBalance(channel: ChannelSummary): boolean {
  return BALANCE_PROVIDER_TYPES.has(channel.type) && !channel.isMultiKey;
}
