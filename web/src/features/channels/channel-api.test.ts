import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  applyAllUpstreamUpdates,
  applyUpstreamUpdates,
  CHANNEL_MANUALLY_DISABLED,
  ChannelContractError,
  copyChannel,
  createChannel,
  deleteChannels,
  deleteDisabledChannels,
  deleteOllamaModel,
  detectAllUpstreamUpdates,
  detectUpstreamUpdates,
  discoverDraftModels,
  fetchChannelDetail,
  fetchChannelModels,
  loadCodexResetCredits,
  loadCodexUsage,
  loadMultiKeyPage,
  loadOllamaVersion,
  loadTagModels,
  manageMultiKey,
  parseChannelDetailResponse,
  parseChannelRepairResponse,
  parseChannelSearchResponse,
  parseChannelTestResponse,
  parseCodexDocumentResponse,
  parseModelListResponse,
  parseMultiKeyPageResponse,
  pullOllamaModel,
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
  testAllChannels,
  testChannel,
  updateChannel,
  updateTagChannels,
  type ChannelCreateInput,
} from './channel-api';

vi.mock('../../shared/api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const mockedAPI = vi.mocked(api);

function channel(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    name: 'Primary',
    type: 1,
    status: 1,
    models: 'gpt-4o,gpt-4.1',
    group: 'default',
    tag: 'fast',
    remark: 'operator note',
    key: '',
    ...overrides,
  };
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('channel response contracts', () => {
  it('accepts the bounded search envelope and drops fields the screen does not use', () => {
    expect(parseChannelSearchResponse({
      success: true,
      data: { items: [channel({ base_url: 'https://private.example' })], total: 1, type_counts: { 1: 1 } },
    })).toEqual({
      items: [{
        id: 7,
        name: 'Primary',
        type: 1,
        status: 1,
        models: 'gpt-4o,gpt-4.1',
        group: 'default',
        tag: 'fast',
        remark: 'operator note',
        balance: 0,
        balanceUpdatedTime: 0,
        isMultiKey: false,
        upstreamCheckEnabled: false,
        pendingAddModels: [],
        pendingRemoveModels: [],
      }],
      total: 1,
      typeCounts: { 1: 1 },
    });
  });

  it('fails closed on secret-bearing, oversized, or malformed search payloads', () => {
    const envelope = (item: unknown) => ({ success: true, data: { items: [item], total: 1, type_counts: { 1: 1 } } });
    expect(() => parseChannelSearchResponse(envelope(channel({ key: 'sk-must-not-render' })))).toThrow(ChannelContractError);
    expect(() => parseChannelSearchResponse(envelope(channel({ models: 'x'.repeat(256 * 1024 + 1) })))).toThrow(ChannelContractError);
    expect(() => parseChannelSearchResponse(envelope(channel({ tag: 'unsafe\u202etag' })))).toThrow(ChannelContractError);
    expect(() => parseChannelSearchResponse({ success: true, data: { items: [], total: 0, type_counts: { invalid: 1 } } })).toThrow(ChannelContractError);
    expect(() => parseChannelSearchResponse({ success: true, data: { items: Array.from({ length: 51 }, () => channel()), total: 51, type_counts: {} } })).toThrow(ChannelContractError);
  });

  it('strictly parses fresh masked detail and drops sensitive fields for non-sensitive callers', () => {
    const response = {
      success: true,
      data: channel({
        priority: 8,
        weight: 7,
        test_model: 'gpt-4.1',
        auto_ban: 0,
        model_mapping: '{"client":"upstream"}',
        status_code_mapping: '{"429":500}',
        base_url: 'https://gateway.example.test',
        openai_organization: 'org-test',
        other: 'west',
        param_override: '{"temperature":0}',
        header_override: '{"X-Test":"yes"}',
        setting: '{"balance_url":"https://balance.example.test","future":true}',
        settings: '{"azure_responses_version":"preview","upstream_model_update_check_enabled":true,"future":true}',
      }),
    };

    const nonSensitive = parseChannelDetailResponse(response, false);
    expect(nonSensitive).toMatchObject({
      id: 7,
      priority: 8,
      weight: 7,
      testModel: 'gpt-4.1',
      autoBan: 0,
    });
    expect(nonSensitive.sensitive).toBeUndefined();
    const sensitive = parseChannelDetailResponse(response, true).sensitive;
    expect(sensitive).toMatchObject({
      baseUrl: 'https://gateway.example.test',
      organization: 'org-test',
      other: 'west',
      paramOverride: '{"temperature":0}',
      headerOverride: '{"X-Test":"yes"}',
      providerSettings: {
        balanceUrl: 'https://balance.example.test',
        azureResponsesVersion: 'preview',
        upstreamCheckEnabled: true,
      },
    });
    expect(JSON.stringify(sensitive)).not.toContain('key');
    expect(() => parseChannelDetailResponse({
      success: true,
      data: channel({ key: 'sk-must-not-render' }),
    }, true)).toThrow(ChannelContractError);
    const legacyMultiKey = parseChannelDetailResponse({
      success: true,
      data: channel({
        channel_info: '{"is_multi_key":true}',
        other: '["sk-legacy-one","sk-legacy-two"]',
      }),
    }, true);
    expect(legacyMultiKey.sensitive?.other).toBeUndefined();
    expect(JSON.stringify(legacyMultiKey)).not.toContain('sk-legacy');
  });

  it('bounds, normalizes, and deduplicates model and health-test responses', () => {
    expect(parseModelListResponse({ success: true, data: ['z-model', 'a-model', 'a-model', ' '] })).toEqual(['a-model', 'z-model']);
    expect(parseChannelTestResponse({ success: true, message: '', time: 0.125 })).toEqual({ success: true, time: 0.125 });
    expect(parseChannelTestResponse({ success: false, message: 'upstream rejected Bearer top-secret and sk-private1234', time: 0 })).toEqual({
      success: false,
      time: 0,
      message: 'upstream rejected Bearer [redacted] and [redacted]',
    });
    expect(() => parseChannelTestResponse({ success: true, time: 301 })).toThrow(ChannelContractError);
    expect(() => parseModelListResponse({ success: true, data: ['x'.repeat(256)] })).toThrow(ChannelContractError);
  });

  it('extracts only bounded operational metadata from channel JSON fields', () => {
    const parsed = parseChannelSearchResponse({
      success: true,
      data: {
        items: [channel({
          balance: -12.5,
          balance_updated_time: 1_700_000_000,
          channel_info: JSON.stringify({
            is_multi_key: true,
            multi_key_disabled_reason: { 0: 'must not enter the row model' },
          }),
          settings: JSON.stringify({
            upstream_model_update_check_enabled: true,
            upstream_model_update_last_detected_models: ['new', 'new'],
            upstream_model_update_last_removed_models: ['old'],
            provider_secret: 'must not enter the row model',
          }),
        })],
        total: 1,
        type_counts: { 1: 1 },
      },
    }).items[0];

    expect(parsed).toMatchObject({
      balance: -12.5,
      balanceUpdatedTime: 1_700_000_000,
      isMultiKey: true,
      upstreamCheckEnabled: true,
      pendingAddModels: ['new'],
      pendingRemoveModels: ['old'],
    });
    expect(JSON.stringify(parsed)).not.toContain('provider_secret');
    expect(JSON.stringify(parsed)).not.toContain('must not enter');
    expect(() => parseChannelSearchResponse({
      success: true,
      data: { items: [channel({ channel_info: '{' })], total: 1, type_counts: {} },
    })).toThrow(ChannelContractError);
  });

  it('parses bounded multi-key state while accepting only irreversible fingerprints and discarding reason text', () => {
    expect(parseMultiKeyPageResponse({
      success: true,
      data: {
        keys: [{ index: 0, status: 3, disabled_time: 123, reason: 'upstream detail', key_preview: 'sha256:deadbeef' }],
        total: 1,
        page: 1,
        page_size: 20,
        total_pages: 1,
        enabled_count: 0,
        manual_disabled_count: 0,
        auto_disabled_count: 1,
      },
    })).toEqual({
      keys: [{ index: 0, fingerprint: 'sha256:deadbeef', status: 3, disabledTime: 123, hasReason: true }],
      total: 1,
      page: 1,
      pageSize: 20,
      totalPages: 1,
      enabledCount: 0,
      manualDisabledCount: 0,
      autoDisabledCount: 1,
    });
    expect(() => parseMultiKeyPageResponse({
      success: true,
      data: {
        keys: [{ index: 0, status: 4 }], total: 1, page: 1, page_size: 20, total_pages: 1,
        enabled_count: 0, manual_disabled_count: 0, auto_disabled_count: 0,
      },
    })).toThrow(ChannelContractError);
    expect(() => parseMultiKeyPageResponse({
      success: true,
      data: {
        keys: [{ index: 0, status: 1, key_preview: 'sk-secret...' }], total: 1, page: 1, page_size: 20, total_pages: 1,
        enabled_count: 1, manual_disabled_count: 0, auto_disabled_count: 0,
      },
    })).toThrow(ChannelContractError);
  });

  it('bounds provider documents and redacts secret-shaped fields before returning them', () => {
    expect(parseCodexDocumentResponse({
      success: true,
      upstream_status: 200,
      data: { plan_type: 'pro', access_token: 'secret', nested: { password: 'hidden', remaining: 17 } },
    })).toEqual({
      upstreamStatus: 200,
      data: { plan_type: 'pro', access_token: '[redacted]', nested: { password: '[redacted]', remaining: 17 } },
    });
    let nested: unknown = 'leaf';
    for (let index = 0; index < 10; index += 1) nested = { nested };
    expect(() => parseCodexDocumentResponse({ success: true, upstream_status: 200, data: nested })).toThrow(ChannelContractError);
  });

  it('strictly parses channel consistency repair counts', () => {
    expect(parseChannelRepairResponse({ success: true, data: { success: 12, fails: 1 } })).toEqual({
      successfulChannels: 12,
      failedChannels: 1,
    });
    expect(() => parseChannelRepairResponse({ success: true, data: { success: -1, fails: 0 } })).toThrow(ChannelContractError);
    expect(() => parseChannelRepairResponse({ success: true, data: { success: 1, fails: '0' } })).toThrow(ChannelContractError);
  });
});

describe('channel API routes', () => {
  it('uses the exact search contract with bounded filters and pagination', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: { success: true, data: { items: [channel()], total: 1, type_counts: { 1: 1 } } } });
    await searchChannels({
      keyword: ' Primary ',
      group: ' default ',
      model: ' gpt-4o ',
      status: 'enabled',
      type: 1,
      page: 2,
      pageSize: 20,
    });

    expect(mockedAPI.get).toHaveBeenCalledWith('/channel/search', expect.objectContaining({
      params: {
        p: 2,
        page_size: 20,
        sort_by: 'id',
        sort_order: 'desc',
        keyword: 'Primary',
        group: 'default',
        model: 'gpt-4o',
        status: 'enabled',
        type: 1,
      },
      maxContentLength: 67_108_864,
      maxBodyLength: 67_108_864,
    }));
  });

  it('passes only whitelisted tag grouping and sort controls', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: { success: true, data: { items: [], total: 0, type_counts: {} } } });
    await searchChannels({
      keyword: '',
      group: '',
      model: '',
      status: '',
      type: null,
      page: 1,
      pageSize: 20,
      tagMode: true,
      idSort: false,
      sortBy: 'balance',
      sortOrder: 'asc',
    });

    expect(mockedAPI.get).toHaveBeenCalledWith('/channel/search', expect.objectContaining({
      params: {
        p: 1,
        page_size: 20,
        tag_mode: true,
        id_sort: false,
        sort_by: 'balance',
        sort_order: 'asc',
      },
    }));
  });

  it('calls only implemented operator routes and sends the manually-disabled status', async () => {
    mockedAPI.post.mockResolvedValueOnce({ data: { success: true, data: true } });
    mockedAPI.get.mockResolvedValueOnce({ data: { success: true, data: ['gpt-4o'] } });

    await setChannelStatus(7, CHANNEL_MANUALLY_DISABLED);
    await fetchChannelModels(7);

    expect(mockedAPI.post).toHaveBeenCalledWith('/channel/7/status', { status: 3 }, expect.objectContaining({ maxContentLength: 67_108_864 }));
    expect(mockedAPI.get).toHaveBeenCalledWith('/channel/fetch_models/7', expect.objectContaining({ maxContentLength: 67_108_864 }));
  });

  it('loads masked detail, discovers from the unsaved draft contract, and passes an explicit test model', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: { success: true, data: channel({ priority: 1, weight: 2, auto_ban: 1 }) } })
      .mockResolvedValueOnce({ data: { success: false, message: 'provider rejected model', time: 0 } });
    mockedAPI.post.mockResolvedValueOnce({ data: { success: true, data: ['gpt-4.1', 'gpt-4o'] } });

    await expect(fetchChannelDetail(7, false)).resolves.toMatchObject({ id: 7, priority: 1, weight: 2 });
    await expect(discoverDraftModels({
      type: 1,
      baseUrl: ' https://draft.example.test ',
      key: 'sk-draft',
      headerOverride: '{"X-Draft":"yes"}',
    })).resolves.toEqual(['gpt-4.1', 'gpt-4o']);
    await expect(testChannel(7, ' gpt-4.1 ')).resolves.toEqual({
      success: false,
      time: 0,
      message: 'provider rejected model',
    });

    expect(mockedAPI.get).toHaveBeenNthCalledWith(1, '/channel/7', expect.objectContaining({ maxContentLength: 67_108_864 }));
    expect(mockedAPI.post).toHaveBeenCalledWith('/channel/fetch_models', {
      type: 1,
      base_url: 'https://draft.example.test',
      key: 'sk-draft',
      header_override: '{"X-Draft":"yes"}',
    }, expect.objectContaining({ maxContentLength: 67_108_864 }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(2, '/channel/test/7', expect.objectContaining({
      params: { model: 'gpt-4.1' },
    }));
  });

  it('uses the target maintenance routes and strictly parses their results', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: { success: true, data: { task_id: 'task-7', status: 'pending' } } });
    mockedAPI.post.mockResolvedValueOnce({ data: { success: true, data: { success: 9, fails: 1 } } });
    mockedAPI.delete.mockResolvedValueOnce({ data: { success: true, data: 4 } });

    await expect(testAllChannels()).resolves.toEqual({ taskId: 'task-7', status: 'pending' });
    await expect(repairChannelAbilities()).resolves.toEqual({ successfulChannels: 9, failedChannels: 1 });
    await expect(deleteDisabledChannels()).resolves.toBe(4);

    expect(mockedAPI.get).toHaveBeenCalledWith('/channel/test', expect.objectContaining({ maxContentLength: 67_108_864 }));
    expect(mockedAPI.post).toHaveBeenCalledWith('/channel/fix', undefined, expect.objectContaining({ maxContentLength: 67_108_864 }));
    expect(mockedAPI.delete).toHaveBeenCalledWith('/channel/disabled', expect.objectContaining({ maxContentLength: 67_108_864 }));
  });

  it('sends the complete non-sensitive edit contract without adding sensitive fields', async () => {
    mockedAPI.put.mockResolvedValueOnce({ data: { success: true, message: '' } });
    await updateChannel({
      id: 7,
      name: ' Primary ',
      models: ' gpt-4o ',
      group: ' default ',
      tag: ' fast ',
      remark: ' reviewed ',
      priority: -2,
      weight: 7,
      testModel: ' gpt-4.1 ',
      autoBan: 0,
      modelMapping: '{"client":"upstream"}',
      statusCodeMapping: '{"429":500}',
    });

    expect(mockedAPI.put).toHaveBeenCalledWith('/channel', {
      id: 7,
      name: 'Primary',
      models: 'gpt-4o',
      group: 'default',
      tag: 'fast',
      remark: 'reviewed',
      priority: -2,
      weight: 7,
      test_model: 'gpt-4.1',
      auto_ban: 0,
      model_mapping: '{"client":"upstream"}',
      status_code_mapping: '{"429":500}',
    }, expect.objectContaining({ maxContentLength: 67_108_864 }));
    const body = mockedAPI.put.mock.calls[0][1] as Record<string, unknown>;
    expect(body).not.toHaveProperty('key');
    expect(body).not.toHaveProperty('base_url');
    expect(body).not.toHaveProperty('type');
    expect(body).not.toHaveProperty('setting');
    expect(body).not.toHaveProperty('settings');

    await expect(updateChannel({
      id: 7,
      name: 'Primary',
      models: 'gpt-4o',
      group: 'g'.repeat(65),
      tag: '',
      remark: '',
    })).rejects.toBeInstanceOf(ChannelContractError);
  });

  it('serializes sensitive editor fields and merges only supported provider settings', async () => {
    mockedAPI.put.mockResolvedValueOnce({ data: { success: true, message: '' } });
    await updateChannel({
      id: 7,
      name: 'Primary',
      models: 'gpt-4o',
      group: 'default',
      tag: '',
      remark: '',
      type: 33,
      baseUrl: ' https://bedrock.example.test ',
      organization: ' org-test ',
      other: ' us-east-1 ',
      paramOverride: '{"temperature":0}',
      headerOverride: '{"X-Test":"yes"}',
      providerSettings: {
        settingSource: '{"future_setting":true,"balance_url":"https://old.example"}',
        settingsSource: '{"future_provider":true,"upstream_model_update_last_check_time":123}',
        balanceUrl: 'https://balance.example.test',
        passThroughBodyEnabled: true,
        azureResponsesVersion: 'unused-on-aws',
        vertexKeyType: 'json',
        awsKeyType: 'api_key',
        advancedCustom: '',
        upstreamCheckEnabled: false,
        upstreamAutoSyncEnabled: true,
        upstreamIgnoredModels: 'old-model,regex:^private-',
      },
    });

    const body = mockedAPI.put.mock.calls[0][1] as Record<string, unknown>;
    expect(body).toMatchObject({
      type: 33,
      base_url: 'https://bedrock.example.test',
      openai_organization: 'org-test',
      other: 'us-east-1',
      param_override: '{"temperature":0}',
      header_override: '{"X-Test":"yes"}',
    });
    expect(JSON.parse(String(body.setting))).toEqual({
      future_setting: true,
      balance_url: 'https://balance.example.test',
      pass_through_body_enabled: true,
    });
    expect(JSON.parse(String(body.settings))).toEqual({
      future_provider: true,
      upstream_model_update_last_check_time: 123,
      aws_key_type: 'api_key',
      upstream_model_update_check_enabled: false,
      upstream_model_update_auto_sync_enabled: false,
      upstream_model_update_ignored_models: ['old-model', 'regex:^private-'],
    });
    expect(body).not.toHaveProperty('key');
  });

  it('creates channels through the additive single, batch, and multi-key envelope', async () => {
    mockedAPI.post.mockResolvedValueOnce({ data: { success: true } });
    await createChannel({
      mode: 'batch',
      multi_key_mode: 'random',
      batch_add_set_key_prefix_2_name: true,
      name: ' Workers ',
      type: 1,
      key: ' first-key \n\n second-key ',
      base_url: ' https://api.example.com ',
      models: ' gpt-4o ',
      group: ' default ',
    });

    expect(mockedAPI.post).toHaveBeenCalledWith('/channel', {
      mode: 'batch',
      multi_key_mode: undefined,
      batch_add_set_key_prefix_2_name: true,
      channel: {
        name: 'Workers',
        type: 1,
        key: 'first-key\nsecond-key',
        base_url: 'https://api.example.com',
        models: 'gpt-4o',
        group: 'default',
      },
    }, expect.objectContaining({ maxContentLength: 67_108_864 }));

    await expect(createChannel({
      mode: 'multi_to_single',
      multi_key_mode: 'polling',
      batch_add_set_key_prefix_2_name: false,
      name: 'Codex',
      type: 57,
      key: 'one\ntwo',
      base_url: '',
      models: 'codex',
      group: 'default',
    })).rejects.toBeInstanceOf(ChannelContractError);
    await expect(createChannel({
      mode: 'batch',
      multi_key_mode: 'random',
      batch_add_set_key_prefix_2_name: false,
      name: 'Duplicate',
      type: 1,
      key: 'same\nsame',
      base_url: '',
      models: 'gpt-4o',
      group: 'default',
    })).rejects.toBeInstanceOf(ChannelContractError);
    await expect(createChannel({
      mode: 'single',
      multi_key_mode: 'random',
      batch_add_set_key_prefix_2_name: false,
      name: 'Unsupported',
      type: 61,
      key: 'secret',
      base_url: '',
      models: 'test',
      group: 'default',
    } as unknown as ChannelCreateInput)).rejects.toBeInstanceOf(ChannelContractError);
  });

  it('uses exact bounded bulk, tag, copy, and balance contracts', async () => {
    mockedAPI.post
      .mockResolvedValueOnce({ data: { success: true, data: 2 } })
      .mockResolvedValueOnce({ data: { success: true, data: 2 } })
      .mockResolvedValueOnce({ data: { success: true, data: 2 } })
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({ data: { success: true, data: { id: 99 } } });
    mockedAPI.put.mockResolvedValueOnce({ data: { success: true } });
    mockedAPI.get
      .mockResolvedValueOnce({ data: { success: true, data: 'gpt-4o,gpt-4.1' } })
      .mockResolvedValueOnce({ data: { success: true, balance: 42.25 } })
      .mockResolvedValueOnce({ data: { success: true } });

    await expect(setChannelsStatus([7, 8], CHANNEL_MANUALLY_DISABLED)).resolves.toBe(2);
    await expect(deleteChannels([7, 8])).resolves.toBe(2);
    await expect(setChannelsTag([7, 8], '')).resolves.toBe(2);
    await setTagChannelsStatus('fast', false);
    await updateTagChannels({
      tag: 'fast',
      newTag: '',
      priority: -1,
      weight: 2,
      modelMapping: '{"old":"new"}',
      paramOverride: '{"temperature":0}',
    });
    await expect(copyChannel(7, '_v2', false)).resolves.toBe(99);
    await expect(loadTagModels('fast')).resolves.toBe('gpt-4o,gpt-4.1');
    await expect(refreshChannelBalance(7)).resolves.toBe(42.25);
    await refreshAllChannelBalances();

    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/channel/status/batch', { ids: [7, 8], status: 3 }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/channel/batch', { ids: [7, 8] }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(3, '/channel/batch/tag', { ids: [7, 8], tag: null }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(4, '/channel/tag/disabled', { tag: 'fast' }, expect.any(Object));
    expect(mockedAPI.put).toHaveBeenCalledWith('/channel/tag', {
      tag: 'fast',
      new_tag: '',
      priority: -1,
      weight: 2,
      model_mapping: '{"old":"new"}',
      param_override: '{"temperature":0}',
    }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(5, '/channel/copy/7', null, expect.objectContaining({
      params: { suffix: '_v2', reset_balance: false },
    }));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(2, '/channel/update_balance/7', expect.any(Object));
    expect(mockedAPI.get).toHaveBeenNthCalledWith(3, '/channel/update_balance', expect.any(Object));

    await expect(setChannelsStatus([7, 7], CHANNEL_MANUALLY_DISABLED)).rejects.toBeInstanceOf(ChannelContractError);
    await expect(setChannelsTag([7], 'bad\u202etag')).rejects.toBeInstanceOf(ChannelContractError);
    await expect(updateTagChannels({ tag: 'fast', modelMapping: '[]' })).rejects.toBeInstanceOf(ChannelContractError);
  });

  it('uses the multi-key and upstream detection/apply contracts', async () => {
    mockedAPI.post
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            keys: [], total: 0, page: 1, page_size: 20, total_pages: 1,
            enabled_count: 1, manual_disabled_count: 1, auto_disabled_count: 0,
          },
        },
      })
      .mockResolvedValueOnce({ data: { success: true } })
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { channel_id: 7, channel_name: 'Primary', add_models: ['new'], remove_models: ['old'], last_check_time: 123, auto_added_models: 0 },
        },
      })
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { added_models: ['new'], removed_models: ['old'], ignored_models: ['skip'], remaining_models: [], remaining_remove_models: [] },
        },
      })
      .mockResolvedValueOnce({ data: { success: true, data: { task_id: 'task-1', status: 'pending' } } })
      .mockResolvedValueOnce({ data: { success: true, data: { processed_channels: 1, added_models: 1, removed_models: 1, failed_channel_ids: [], results: [] } } });

    await loadMultiKeyPage(7, 2, 20, 3);
    await manageMultiKey(7, 'disable_key', 4);
    await expect(detectUpstreamUpdates(7)).resolves.toMatchObject({ addModels: ['new'], removeModels: ['old'] });
    await expect(applyUpstreamUpdates(7, ['new'], ['old'], ['skip'])).resolves.toMatchObject({ addedModels: ['new'] });
    await expect(detectAllUpstreamUpdates()).resolves.toEqual({ taskId: 'task-1', status: 'pending' });
    await expect(applyAllUpstreamUpdates()).resolves.toMatchObject({ processedChannels: 1, failedChannelIds: [] });

    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/channel/multi_key/manage', {
      channel_id: 7, action: 'get_key_status', page: 2, page_size: 20, status: 3,
    }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/channel/multi_key/manage', {
      channel_id: 7, action: 'disable_key', key_index: 4,
    }, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(4, '/channel/upstream_updates/apply', {
      id: 7,
      add_models: ['new'],
      remove_models: ['old'],
      ignore_models: ['skip'],
    }, expect.any(Object));
    await expect(manageMultiKey(7, 'delete_key')).rejects.toBeInstanceOf(ChannelContractError);
  });

  it('uses only target-supported Codex and Ollama management routes', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: { success: true, upstream_status: 200, data: { remaining: 17 } } })
      .mockResolvedValueOnce({ data: { success: true, upstream_status: 200, data: { available_count: 2 } } })
      .mockResolvedValueOnce({ data: { success: true, data: { version: '0.11.7' } } });
    mockedAPI.post
      .mockResolvedValueOnce({ data: { success: true, upstream_status: 200, data: { reset: true } } })
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { expires_at: 'tomorrow', last_refresh: 'now', account_id: 'acct', email: 'admin@example.com', channel_id: 57, channel_type: 57, channel_name: 'Codex' },
        },
      })
      .mockResolvedValueOnce({ data: { success: true } });
    mockedAPI.delete.mockResolvedValueOnce({ data: { success: true } });

    await expect(loadCodexUsage(57)).resolves.toMatchObject({ upstreamStatus: 200 });
    await expect(loadCodexResetCredits(57)).resolves.toMatchObject({ upstreamStatus: 200 });
    await expect(resetCodexUsage(57)).resolves.toMatchObject({ data: { reset: true } });
    await refreshCodexCredential(57);
    await expect(loadOllamaVersion(4)).resolves.toBe('0.11.7');
    await pullOllamaModel(4, ' llama3.2 ');
    await deleteOllamaModel(4, 'llama3.2');

    expect(mockedAPI.post).toHaveBeenNthCalledWith(1, '/channel/57/codex/usage/reset', {}, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(2, '/channel/57/codex/refresh', {}, expect.any(Object));
    expect(mockedAPI.post).toHaveBeenNthCalledWith(3, '/channel/ollama/pull', { channel_id: 4, model_name: 'llama3.2' }, expect.any(Object));
    expect(mockedAPI.delete).toHaveBeenCalledWith('/channel/ollama/delete', expect.objectContaining({
      data: { channel_id: 4, model_name: 'llama3.2' },
    }));
    await expect(pullOllamaModel(4, ' ')).rejects.toBeInstanceOf(ChannelContractError);
  });

  it('consumes bounded Ollama SSE progress and requires an explicit terminal event', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response([
        'data: {"status":"downloading","completed":5,"total":10}\n\n',
        'data: {"message":"complete"}\n\n',
        'data: [DONE]\n\n',
      ].join(''), { status: 200, headers: { 'Content-Type': 'text/event-stream' } }))
      .mockResolvedValueOnce(new Response('data: {"status":"started"}\n\n', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const updates: Array<{ status: string; completed: number; total: number }> = [];

    await pullOllamaModelStream(4, 'llama3.2', (progress) => updates.push(progress));
    expect(updates).toEqual([
      { status: 'downloading', completed: 5, total: 10 },
      { status: 'complete', completed: 0, total: 0 },
    ]);
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/channel/ollama/pull/stream', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      body: JSON.stringify({ channel_id: 4, model_name: 'llama3.2' }),
    }));
    await expect(pullOllamaModelStream(4, 'llama3.2', () => undefined)).rejects.toBeInstanceOf(ChannelContractError);
    vi.unstubAllGlobals();
  });
});
