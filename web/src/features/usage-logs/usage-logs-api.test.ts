import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  buildUsageLogParams,
  drawingGatewayContentURL,
  emptyUsageLogQuery,
  loadUsageLogs,
  parseUsageLogStats,
  taskGatewayContentURL,
  UsageLogContractError,
} from './usage-logs-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn() } }));

const mockedGet = vi.mocked(api.get);

function page(items: unknown[], currentPage = 1, pageSize = 20, total = items.length) {
  return { data: { success: true, data: { items, page: currentPage, page_size: pageSize, total } } };
}

function commonItem(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    user_id: 9,
    created_at: 1_700_000_000,
    type: 2,
    username: 'alice',
    token_name: 'primary',
    model_name: 'gpt-4o',
    quota: 42,
    prompt_tokens: 12,
    completion_tokens: 8,
    use_time: 430,
    is_stream: true,
    channel_id: 7,
    channel_name: 'safe channel',
    group: 'default',
    request_id: 'request-1',
    upstream_request_id: 'upstream-fingerprint',
    content: 'request completed',
    other: '{"key":"sk-private","upstream_model_name":"gpt-4o-2024","frt":120,"cache_tokens":4,"model_ratio":1.5}',
    ip: '192.0.2.1',
    ...overrides,
  };
}

function drawingItem(overrides: Record<string, unknown> = {}) {
  return {
    id: 2,
    user_id: 3,
    channel_id: 4,
    mj_id: 'mj-safe-id',
    action: 'IMAGINE',
    submit_time: 1_700_000_000_000,
    status: 'SUCCESS',
    progress: '100%',
    quota: 9,
    image_url: 'https://untrusted.example/private/image.png',
    prompt: 'private prompt is not part of the display model',
    prompt_en: 'safe translated prompt',
    fail_reason: '',
    start_time: 1_700_000_000_100,
    finish_time: 1_700_000_000_900,
    ...overrides,
  };
}

function taskItem(overrides: Record<string, unknown> = {}) {
  return {
    id: 5,
    user_id: 6,
    username: 'operator',
    channel_id: 8,
    task_id: 'task_0123456789abcdef0123456789abcdef',
    platform: '55',
    action: 'generate',
    submit_time: 1_700_000_000,
    status: 'SUCCESS',
    progress: '100%',
    quota: 11,
    result_url: 'https://provider.example/result.mp4',
    data: { provider_secret: 'hidden' },
    group: 'default',
    properties: { input: 'safe video prompt', upstream_model_name: 'sora-upstream', origin_model_name: 'sora' },
    fail_reason: '',
    start_time: 1_700_000_001,
    finish_time: 1_700_000_030,
    private_data: { key: 'never rendered' },
    ...overrides,
  };
}

beforeEach(() => vi.resetAllMocks());

describe('usage log API', () => {
  it('uses the exact common admin endpoints, bounded filters, shared abort signal, and redacted model', async () => {
    const signal = new AbortController().signal;
    mockedGet.mockImplementation(async (url) => {
      if (url === '/log') return page([commonItem()], 2, 50, 61);
      if (url === '/log/stat') return { data: { success: true, data: { quota: 99, rpm: 2, tpm: 77 } } };
      throw new Error(`unexpected ${String(url)}`);
    });
    const query = {
      ...emptyUsageLogQuery(),
      page: 2,
      pageSize: 50 as const,
      type: 2,
      model: ' gpt-4o ',
      token: ' primary ',
      group: ' vip ',
      username: ' alice ',
      channel: '7',
      requestId: ' req ',
      upstreamRequestId: ' upstream ',
      startTimestamp: 100,
      endTimestamp: 200,
    };
    const result = await loadUsageLogs('common', true, query, signal);

    expect(mockedGet).toHaveBeenCalledWith('/log', expect.objectContaining({
      signal,
      params: {
        p: 2, page_size: 50, type: 2, model_name: 'gpt-4o', token_name: 'primary', group: 'vip',
        username: 'alice', channel: 7, request_id: 'req', upstream_request_id: 'upstream',
        start_timestamp: 100, end_timestamp: 200,
      },
    }));
    expect(mockedGet).toHaveBeenCalledWith('/log/stat', expect.objectContaining({
      signal,
      params: expect.not.objectContaining({ p: expect.anything(), page_size: expect.anything(), request_id: expect.anything() }),
    }));
    expect(result.stats).toEqual({ quota: 99, rpm: 2, tpm: 77 });
    expect(result.items[0]).toMatchObject({
      kind: 'common', userId: 9, username: 'alice', modelName: 'gpt-4o', content: 'request completed',
      ip: '192.0.2.1', upstreamRequestId: 'upstream-fingerprint',
      billing: { upstreamModelName: 'gpt-4o-2024', firstResponseTime: 120, cacheTokens: 4, modelRatio: 1.5 },
    });
    expect(JSON.stringify(result.items[0])).not.toMatch(/sk-private|"key"/);
  });

  it('uses only self endpoints and strips administrator-only filters', async () => {
    mockedGet.mockImplementation(async (url) => {
      if (url === '/log/self') return page([commonItem()]);
      if (url === '/log/self/stat') return { data: { success: true, data: { quota: 1, rpm: 0, tpm: 0 } } };
      throw new Error(`unexpected ${String(url)}`);
    });
    await loadUsageLogs('common', false, {
      ...emptyUsageLogQuery(), username: 'other-user', channel: '99',
    });
    const params = mockedGet.mock.calls[0][1]?.params;
    expect(params).not.toHaveProperty('username');
    expect(params).not.toHaveProperty('channel');
    expect(mockedGet).toHaveBeenCalledWith('/log/self/stat', expect.any(Object));
  });

  it('uses exact drawing and task admin/self endpoints and gateway-owned content links', async () => {
    mockedGet
      .mockResolvedValueOnce(page([drawingItem()]))
      .mockResolvedValueOnce(page([drawingItem()]))
      .mockResolvedValueOnce(page([taskItem()]))
      .mockResolvedValueOnce(page([taskItem({ result_url: undefined, data: { has_inline_video: true, inline_mime_type: 'video/mp4' } })]));

    const drawingAdmin = await loadUsageLogs('drawing', true, {
      ...emptyUsageLogQuery(), identifier: 'mj-1', channel: '4', startTimestamp: 1_000, endTimestamp: 2_000,
    });
    await loadUsageLogs('drawing', false, emptyUsageLogQuery());
    const taskAdmin = await loadUsageLogs('task', true, {
      ...emptyUsageLogQuery(), identifier: 'task-1', channel: '8', platform: 'video', status: 'SUCCESS', action: 'generate',
    });
    const taskSelf = await loadUsageLogs('task', false, emptyUsageLogQuery());

    expect(mockedGet.mock.calls.map(([url]) => url)).toEqual(['/mj/', '/mj/self', '/task/', '/task/self']);
    expect(mockedGet.mock.calls[0][1]?.params).toEqual({
      p: 1, page_size: 20, start_timestamp: 1_000, end_timestamp: 2_000, mj_id: 'mj-1', channel_id: 4,
    });
    expect(mockedGet.mock.calls[2][1]?.params).toMatchObject({
      task_id: 'task-1', channel_id: 8, platform: 'video', status: 'SUCCESS', action: 'generate',
    });
    expect(drawingAdmin.items[0]).toMatchObject({ contentURL: '/mj/image/mj-safe-id' });
    expect(drawingAdmin.items[0]).toMatchObject({ promptEnglish: 'safe translated prompt', startTime: 1_700_000_000_100 });
    expect(taskAdmin.items[0]).toMatchObject({ contentURL: '/v1/videos/task_0123456789abcdef0123456789abcdef/content' });
    expect(taskAdmin.items[0]).toMatchObject({ input: 'safe video prompt', upstreamModelName: 'sora-upstream' });
    expect(taskSelf.items[0]).toMatchObject({ contentURL: '/v1/videos/task_0123456789abcdef0123456789abcdef/content' });
    expect(JSON.stringify(taskAdmin.items[0])).not.toMatch(/provider\.example|provider_secret|private_data/);
  });

  it('fails closed on malformed, oversized, contradictory, and credential-bearing error envelopes', async () => {
    mockedGet.mockResolvedValueOnce({ data: { success: false, message: 'database password sk-private' } });
    let redactedError: unknown;
    try {
      await loadUsageLogs('drawing', false, emptyUsageLogQuery());
    } catch (error) {
      redactedError = error;
    }
    expect(redactedError).toBeInstanceOf(UsageLogContractError);
    expect((redactedError as Error).message).not.toContain('sk-private');

    mockedGet.mockResolvedValueOnce(page(Array.from({ length: 101 }, () => drawingItem())));
    await expect(loadUsageLogs('drawing', false, emptyUsageLogQuery())).rejects.toBeInstanceOf(UsageLogContractError);

    mockedGet.mockResolvedValueOnce(page([drawingItem({ mj_id: `bad\u0000id` })]));
    await expect(loadUsageLogs('drawing', false, emptyUsageLogQuery())).rejects.toBeInstanceOf(UsageLogContractError);

    mockedGet.mockResolvedValueOnce(page([drawingItem({ mj_id: `safe\u202Efdp.exe` })]));
    await expect(loadUsageLogs('drawing', false, emptyUsageLogQuery())).rejects.toBeInstanceOf(UsageLogContractError);

    mockedGet.mockResolvedValueOnce({
      data: { success: true, data: { items: [], page: 1, page_size: 20, total: 0, padding: 'x'.repeat(1024 * 1024) } },
    });
    await expect(loadUsageLogs('drawing', false, emptyUsageLogQuery())).rejects.toBeInstanceOf(UsageLogContractError);

    expect(() => parseUsageLogStats({ success: true, data: { quota: 0, rpm: -1, tpm: 0 } })).toThrow(UsageLogContractError);
  });

  it('omits unsafe remote results and rejects invalid filters before transport', async () => {
    mockedGet.mockResolvedValueOnce(page([taskItem({ result_url: 'file:///private/video.mp4' })]));
    const result = await loadUsageLogs('task', false, emptyUsageLogQuery());
    expect(result.items[0]).toMatchObject({ contentURL: null });

    expect(() => buildUsageLogParams('task', true, {
      ...emptyUsageLogQuery(), channel: '-1',
    })).toThrow(UsageLogContractError);
    expect(() => buildUsageLogParams('common', false, {
      ...emptyUsageLogQuery(), startTimestamp: 200, endTimestamp: 100,
    })).toThrow(UsageLogContractError);
    expect(mockedGet).toHaveBeenCalledTimes(1);
  });

  it('omits drawing links when the stored result or route segment cannot be served safely', async () => {
    mockedGet
      .mockResolvedValueOnce(page([drawingItem({ image_url: 'file:///private/image.png' })]))
      .mockResolvedValueOnce(page([drawingItem({ mj_id: 'provider/id' })]));

    const unsafeResult = await loadUsageLogs('drawing', false, emptyUsageLogQuery());
    const invalidPath = await loadUsageLogs('drawing', false, emptyUsageLogQuery());

    expect(unsafeResult.items[0]).toMatchObject({ contentURL: null });
    expect(invalidPath.items[0]).toMatchObject({ drawingId: 'provider/id', contentURL: null });
  });

  it('offers task content only for exact gateway-supported platform and public ID pairs', async () => {
    mockedGet
      .mockResolvedValueOnce(page([taskItem({ platform: '50' })]))
      .mockResolvedValueOnce(page([taskItem({ task_id: 'provider-video-1' })]));

    const unsupportedPlatform = await loadUsageLogs('task', false, emptyUsageLogQuery());
    const invalidPublicId = await loadUsageLogs('task', false, emptyUsageLogQuery());

    expect(unsupportedPlatform.items[0]).toMatchObject({ platform: '50', contentURL: null });
    expect(invalidPublicId.items[0]).toMatchObject({ taskId: 'provider-video-1', contentURL: null });
  });

  it('constructs only relative same-origin gateway paths', () => {
    expect(drawingGatewayContentURL('abc/123')).toBe('/mj/image/abc%2F123');
    expect(taskGatewayContentURL('task_0123456789abcdef0123456789abcdef'))
      .toBe('/v1/videos/task_0123456789abcdef0123456789abcdef/content');
    expect(() => taskGatewayContentURL('task?1')).toThrow(UsageLogContractError);
  });
});
