import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  DashboardAccessError,
  DashboardContractError,
  aggregateDashboardRows,
  aggregateDashboardTimeline,
  canFilterDashboardByUsername,
  dashboardMetricValue,
  dashboardQueryURL,
  dashboardTimelineBucket,
  dashboardTimelineBuckets,
  defaultDashboardQuery,
  isAdministratorRole,
  loadDashboardOverview,
  loadDashboardPerformance,
  loadDashboardSection,
  normalizeDashboardQuery,
  parseDashboardGatewayResponse,
  parseDashboardNoticeResponse,
  parseDashboardPerformanceResponse,
  parseDashboardResponse,
  parseDashboardSearch,
  parseDashboardUptimeResponse,
  type DashboardQuery,
} from './dashboard-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn() } }));

const mockedGet = vi.mocked(api.get);

const query: DashboardQuery = {
  startTimestamp: 1_700_000_000,
  endTimestamp: 1_700_086_400,
  username: ' alice ',
};

function envelope(data: unknown[]) {
  return { data: { success: true, message: '', data } };
}

function quotaRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 0,
    user_id: 0,
    username: '',
    model_name: 'gpt-4o',
    created_at: 1_700_000_000,
    use_group: '',
    token_id: 0,
    channel_id: 0,
    node_name: '',
    token_used: 30,
    count: 2,
    quota: 40,
    ...overrides,
  };
}

function flowRow(overrides: Record<string, unknown> = {}) {
  return {
    user_id: 7,
    username: 'alice',
    node_name: 'node-a',
    token_id: 8,
    token_name: 'primary',
    use_group: 'default',
    channel_id: 9,
    channel_name: 'main',
    model_name: 'gpt-4o',
    token_used: 30,
    count: 2,
    quota: 40,
    ...overrides,
  };
}

beforeEach(() => vi.resetAllMocks());

describe('dashboard endpoint contract', () => {
  it('selects each exact self and administrator endpoint with bounded transport settings', async () => {
    mockedGet.mockImplementation(async (url) => {
      if (url === '/perf-metrics/summary') return {
        data: { success: true, data: { models: [] } },
      };
      if (url === '/data/users') return envelope([quotaRow({ username: 'alice', model_name: '' })]);
      if (url === '/data/flow') return envelope([flowRow()]);
      if (url === '/data/flow/self') return envelope([{
        token_id: 8,
        token_name: 'primary',
        use_group: 'default',
        model_name: 'gpt-4o',
        token_used: 30,
        count: 2,
        quota: 40,
      }]);
      return envelope([quotaRow()]);
    });
    const signal = new AbortController().signal;

    await loadDashboardSection('overview', 1, query, signal);
    await loadDashboardSection('overview', 100, query, signal);
    await loadDashboardSection('models', 10, query, signal);
    await loadDashboardSection('flow', 1, query, signal);
    await loadDashboardSection('flow', 100, query, signal);
    await loadDashboardSection('users', 10, query, signal);
    await loadDashboardPerformance(signal);

    expect(mockedGet.mock.calls.map(([url]) => url)).toEqual([
      '/data/self', '/data/self', '/data', '/data/flow/self', '/data/flow', '/data/users',
      '/perf-metrics/summary',
    ]);
    expect(mockedGet.mock.calls[0][1]).toEqual(expect.objectContaining({
      params: { start_timestamp: query.startTimestamp, end_timestamp: query.endTimestamp },
      signal,
      adapter: 'fetch',
      maxContentLength: 2 * 1024 * 1024,
      maxBodyLength: 2 * 1024 * 1024,
    }));
    expect(mockedGet.mock.calls[1][1]?.params).not.toHaveProperty('username');
    expect(mockedGet.mock.calls[2][1]?.params).toEqual({
      start_timestamp: query.startTimestamp,
      end_timestamp: query.endTimestamp,
      username: 'alice',
    });
    expect(mockedGet.mock.calls[3][1]?.params).not.toHaveProperty('username');
    expect(mockedGet.mock.calls[5][1]?.params).not.toHaveProperty('username');
    expect(mockedGet.mock.calls[6][1]).toEqual(expect.objectContaining({
      params: { hours: 24 }, signal,
    }));
  });

  it('fails closed before transport when a common user requests user analytics', async () => {
    await expect(loadDashboardSection('users', 1, query)).rejects.toBeInstanceOf(DashboardAccessError);
    await expect(loadDashboardSection('overview', 11, query)).rejects.toBeInstanceOf(DashboardAccessError);
    expect(mockedGet).not.toHaveBeenCalled();
  });

  it('loads bounded local overview alternatives independently and preserves partial failures', async () => {
    mockedGet.mockImplementation(async (url) => {
      if (url === '/notice') return { data: { success: true, message: '', data: '  Planned maintenance\nSaturday  ' } };
      if (url === '/status') return { data: { success: true, message: '', data: {
        system_name: 'Acme Gateway', version: '1.2.3', node_name: 'edge-a',
        server_address: 'https://api.example.test/', internal_flag: 'discarded',
        api_info_enabled: true,
        api_info: [{ id: 1, url: 'https://relay.example.test/v1', route: 'Primary', description: 'Main route', color: 'blue' }],
        faq_enabled: true,
        faq: [{ id: 2, question: 'Question?', answer: 'Answer.' }],
        uptime_kuma_enabled: true,
      } } };
      if (url === '/uptime/status') return { data: {
        success: true, message: '', data: [{
          categoryName: 'Core', monitors: [{ name: 'Chat', uptime: 0.999, status: 1, group: 'APIs' }],
        }],
      } };
      throw new Error('unexpected route');
    });
    const signal = new AbortController().signal;
    await expect(loadDashboardOverview('https://fallback.example.test', signal)).resolves.toEqual({
      notice: { ok: true, value: 'Planned maintenance\nSaturday' },
      gateway: { ok: true, value: {
        systemName: 'Acme Gateway', version: '1.2.3', nodeName: 'edge-a',
        serverAddress: 'https://api.example.test',
        apiInfoEnabled: true,
        apiInfo: [{ id: 1, url: 'https://relay.example.test/v1', route: 'Primary', description: 'Main route', color: 'blue' }],
        faqEnabled: true,
        faq: [{ id: 2, question: 'Question?', answer: 'Answer.' }],
        uptimeKumaEnabled: true,
      } },
      uptime: { ok: true, value: [{
        categoryName: 'Core', monitors: [{ name: 'Chat', uptime: 0.999, status: 1, group: 'APIs' }],
      }] },
    });
    expect(mockedGet.mock.calls.map(([url]) => url)).toEqual(['/notice', '/status', '/uptime/status']);
    expect(mockedGet.mock.calls[1][1]).toEqual(expect.objectContaining({
      signal, adapter: 'fetch', maxContentLength: 4 * 1024 * 1024, maxBodyLength: 4 * 1024 * 1024,
    }));

    mockedGet.mockImplementation(async (url) => {
      if (url === '/status') throw new Error('private status failure');
      if (url === '/notice') return { data: { success: true, data: '' } };
      return { data: { success: true, message: '', data: [] } };
    });
    const partial = await loadDashboardOverview('https://fallback.example.test');
    expect(partial.gateway).toEqual({ ok: false });
    expect(partial.notice).toEqual({ ok: true, value: '' });
    expect(partial.uptime).toMatchObject({ ok: true });
  });

  it('normalizes safe ranges and never forwards malformed or privileged query values', () => {
    expect(normalizeDashboardQuery(query)).toEqual({
      startTimestamp: query.startTimestamp,
      endTimestamp: query.endTimestamp,
      username: 'alice',
    });
    expect(normalizeDashboardQuery(query, false).username).toBe('');
    expect(() => normalizeDashboardQuery({ ...query, endTimestamp: query.startTimestamp + 2_592_001 })).toThrow(DashboardContractError);
    expect(() => normalizeDashboardQuery({ ...query, username: 'bad\u0000name' })).toThrow(DashboardContractError);
    expect(() => normalizeDashboardQuery({ ...query, startTimestamp: Number.NaN })).toThrow(DashboardContractError);
    expect(isAdministratorRole(100)).toBe(true);
    expect(isAdministratorRole(11)).toBe(false);
    expect(isAdministratorRole(9)).toBe(false);
    expect(canFilterDashboardByUsername('overview', 100)).toBe(false);
    expect(canFilterDashboardByUsername('models', 10)).toBe(true);
    const defaultQuery = defaultDashboardQuery(1_800_000_123);
    expect(defaultQuery.endTimestamp - defaultQuery.startTimestamp).toBe(86_400);
    expect(defaultQuery.endTimestamp).toBe(1_800_000_123);
  });

  it('sanitizes URL state, rejects duplicates, and emits only canonical dashboard URLs', () => {
    expect(parseDashboardSearch('?start_timestamp=1700000000&end_timestamp=1700086400&username=%20alice%20', 1_800_000_000)).toEqual({
      startTimestamp: 1_700_000_000,
      endTimestamp: 1_700_086_400,
      username: 'alice',
    });

    const fallback = defaultDashboardQuery(1_800_000_000);
    expect(parseDashboardSearch('?start_timestamp=1&start_timestamp=2&end_timestamp=3', 1_800_000_000)).toEqual(fallback);
    expect(parseDashboardSearch(`?${'x'.repeat(4_097)}`, 1_800_000_000)).toEqual(fallback);
    expect(parseDashboardSearch('?start_timestamp=1700000000&end_timestamp=1703000000&username=alice', 1_800_000_000)).toEqual({
      ...fallback,
      username: 'alice',
    });

    expect(dashboardQueryURL('models', 10, query)).toBe(
      '/dashboard/models?start_timestamp=1700000000&end_timestamp=1700086400',
    );
    expect(dashboardQueryURL('overview', 1, query)).toBe(
      '/dashboard/overview?start_timestamp=1700000000&end_timestamp=1700086400',
    );
    expect(dashboardQueryURL('overview', 100, query)).toBe(
      '/dashboard/overview?start_timestamp=1700000000&end_timestamp=1700086400',
    );
    expect(() => dashboardQueryURL('users', 1, query)).toThrow(DashboardAccessError);
  });
});

describe('dashboard response contract', () => {
  it('parses model, user, and role-shaped flow rows into minimal display models', () => {
    const models = parseDashboardResponse({
      success: true,
      message: '',
      data: [quotaRow(), quotaRow({ quota: 10, count: 1, token_used: 5 })],
    }, 'models', 1);
    expect(models).toMatchObject({
      kind: 'quota',
      summary: { quota: 50, requests: 3, tokens: 35 },
    });
    expect(JSON.stringify(models)).not.toMatch(/use_group|channel_id|node_name/);

    const users = parseDashboardResponse({
      success: true,
      data: [quotaRow({ username: 'alice', model_name: '' })],
    }, 'users', 10);
    expect(users).toMatchObject({ kind: 'users', rows: [{ username: 'alice' }] });

    const flow = parseDashboardResponse({ success: true, data: [flowRow()] }, 'flow', 100);
    expect(flow).toMatchObject({
      kind: 'flow',
      rows: [{ username: 'alice', nodeName: 'node-a', tokenName: 'primary', group: 'default', modelName: 'gpt-4o' }],
    });
  });

  it('aggregates deterministically with checked arithmetic', () => {
    const rows = [
      { quota: 4, requests: 1, tokens: 5 },
      { quota: 7, requests: 2, tokens: 8 },
      { quota: 3, requests: 1, tokens: 4 },
    ];
    expect(aggregateDashboardRows(rows, ['beta', 'alpha', 'beta'])).toEqual([
      { label: 'beta', quota: 7, requests: 2, tokens: 9 },
      { label: 'alpha', quota: 7, requests: 2, tokens: 8 },
    ]);
    expect(() => aggregateDashboardRows([
      { quota: Number.MAX_SAFE_INTEGER, requests: 0, tokens: 0 },
      { quota: 1, requests: 0, tokens: 0 },
    ], ['same', 'same'])).toThrow(DashboardContractError);
  });

  it('builds checked calendar timelines, pads requested buckets, and reads a selected metric', () => {
    const rows = [
      { modelName: 'a', username: '', createdAt: 1_700_000_000, quota: 4, requests: 1, tokens: 5 },
      { modelName: 'b', username: '', createdAt: 1_700_000_100, quota: 7, requests: 2, tokens: 8 },
      { modelName: 'a', username: '', createdAt: 1_700_090_000, quota: 3, requests: 1, tokens: 4 },
    ];
    const hourly = aggregateDashboardTimeline(rows, 'hour');
    expect(hourly).toHaveLength(2);
    expect(hourly[0]).toMatchObject({ quota: 11, requests: 3, tokens: 13 });
    expect(dashboardMetricValue(hourly[0], 'tokens')).toBe(13);
    expect(dashboardTimelineBucket(1_700_000_123, 'hour')).toBe(
      Math.floor(1_700_000_123 / 3_600) * 3_600,
    );
    expect(aggregateDashboardTimeline(rows, 'day')).toHaveLength(2);
    const monday = Math.floor(new Date(2026, 8, 7, 12, 0, 0).getTime() / 1_000);
    const sunday = Math.floor(new Date(2026, 8, 13, 18, 0, 0).getTime() / 1_000);
    expect(aggregateDashboardTimeline([
      { ...rows[0], createdAt: monday },
      { ...rows[1], createdAt: sunday },
    ], 'week')).toHaveLength(1);
    expect(dashboardTimelineBucket(monday, 'week')).toBe(
      Math.floor(new Date(2026, 8, 7, 0, 0, 0).getTime() / 1_000),
    );
    const rangeStart = Math.floor(new Date(2026, 8, 7, 10, 15, 0).getTime() / 1_000);
    const rangeEnd = Math.floor(new Date(2026, 8, 7, 13, 15, 0).getTime() / 1_000);
    expect(dashboardTimelineBuckets({ startTimestamp: rangeStart, endTimestamp: rangeEnd, username: '' }, 'hour'))
      .toHaveLength(3);
    expect(dashboardTimelineBuckets({
      startTimestamp: 1_700_000_123,
      endTimestamp: 1_700_000_123 + 86_400,
      username: '',
    }, 'hour')).toHaveLength(24);
    expect(() => aggregateDashboardTimeline([
      { ...rows[0], quota: Number.MAX_SAFE_INTEGER },
      { ...rows[1], quota: 1 },
    ], 'hour')).toThrow(DashboardContractError);
  });

  it('enforces role-shaped flow dimensions and refuses unexpected privilege data', () => {
    const selfPayload = { success: true, data: [{
      token_id: 8, token_name: 'primary', use_group: 'default', model_name: 'gpt-4o',
      token_used: 30, count: 2, quota: 40,
    }] };
    expect(parseDashboardResponse(selfPayload, 'flow', 1)).toMatchObject({
      kind: 'flow', rows: [{ tokenName: 'primary', username: '', channelName: '' }],
    });
    expect(() => parseDashboardResponse({ success: true, data: [flowRow()] }, 'flow', 1))
      .toThrow(DashboardContractError);
    expect(() => parseDashboardResponse({ success: true, data: [{
      user_id: 7, username: 'alice', token_id: 8, token_name: 'should-not-arrive',
      use_group: 'default', model_name: 'gpt-4o', channel_id: 9, channel_name: 'main',
      token_used: 30, count: 2, quota: 40,
    }] }, 'flow', 10)).toThrow(DashboardContractError);
    expect(() => parseDashboardResponse({ success: true, data: [] }, 'users', 1))
      .toThrow(DashboardAccessError);
  });

  it('parses only bounded safe notice, console content, and grouped uptime fields', () => {
    expect(parseDashboardNoticeResponse({ success: true, data: 'Line one\nLine two' })).toBe('Line one\nLine two');
    expect(parseDashboardGatewayResponse({ success: true, data: {
      app_name: 'TokenRouter', server_address: '', secret: 'discard me',
      api_info_enabled: true,
      api_info: [{ url: 'https://api.example.test/v1', route: 'Primary', description: 'Main route', color: 'green' }],
      faq_enabled: true,
      faq: [{ question: 'Question?', answer: 'Answer.' }],
      uptime_kuma_enabled: true,
    } }, 'https://dashboard.example.test')).toEqual({
      systemName: 'TokenRouter', version: '', nodeName: '', serverAddress: 'https://dashboard.example.test',
      apiInfoEnabled: true,
      apiInfo: [{ url: 'https://api.example.test/v1', route: 'Primary', description: 'Main route', color: 'green' }],
      faqEnabled: true,
      faq: [{ question: 'Question?', answer: 'Answer.' }],
      uptimeKumaEnabled: true,
    });
    expect(parseDashboardUptimeResponse({ success: true, message: '', data: [{
      categoryName: 'Core', monitors: [{ name: 'Chat API', uptime: 0.999, status: 1, group: 'APIs' }],
    }] })).toEqual([{ categoryName: 'Core', monitors: [{
      name: 'Chat API', uptime: 0.999, status: 1, group: 'APIs',
    }] }]);
    expect(() => parseDashboardNoticeResponse({ success: true, data: 'bad\u202etext' })).toThrow(DashboardContractError);
    expect(() => parseDashboardGatewayResponse({ success: true, data: {
      server_address: 'http://internal.example.test',
      api_info_enabled: true, api_info: [], faq_enabled: true, faq: [], uptime_kuma_enabled: true,
    } }, '')).toThrow(DashboardContractError);
    expect(() => parseDashboardUptimeResponse({
      success: true, data: [{ categoryName: 'Core', monitors: [{ name: 'Bad', uptime: 1.1, status: 1 }] }],
    })).toThrow(DashboardContractError);
    expect(() => parseDashboardGatewayResponse({ success: true, data: {
      server_address: '', api_info_enabled: true,
      api_info: [{ url: 'javascript:alert(1)', route: 'Primary', description: 'Main', color: 'blue' }],
      faq_enabled: true, faq: [], uptime_kuma_enabled: true,
    } }, '')).toThrow(DashboardContractError);
    expect(() => parseDashboardGatewayResponse({ success: true, data: {
      server_address: '', api_info_enabled: false, api_info: [],
      faq_enabled: false, uptime_kuma_enabled: false,
    } }, '')).toThrow(DashboardContractError);
  });

  it('parses the bounded performance summary contract without retaining unknown state', () => {
    expect(parseDashboardPerformanceResponse({
      success: true,
      data: {
        models: [{
          model_name: 'gpt-4o',
          avg_latency_ms: 245,
          success_rate: 99.75,
          avg_tps: 42.5,
          recent_success_rates: [99, 100],
        }],
      },
    })).toEqual([{
      modelName: 'gpt-4o',
      averageLatencyMs: 245,
      successRate: 99.75,
      averageTokensPerSecond: 42.5,
      recentSuccessRates: [99, 100],
    }]);

    const invalid = [
      { success: true, data: { models: [{ model_name: 'gpt-4o', avg_latency_ms: -1, success_rate: 99, avg_tps: 2 }] } },
      { success: true, data: { models: [{ model_name: 'gpt-4o', avg_latency_ms: 1, success_rate: 101, avg_tps: 2 }] } },
      { success: true, data: { models: [
        { model_name: 'same', avg_latency_ms: 1, success_rate: 99, avg_tps: 2 },
        { model_name: 'same', avg_latency_ms: 1, success_rate: 99, avg_tps: 2 },
      ] } },
      { success: true, data: { models: [{
        model_name: 'gpt-4o', avg_latency_ms: 1, success_rate: 99, avg_tps: 2,
        recent_success_rates: [1, 2, 3, 4],
      }] } },
      { success: true, data: { models: [], internal: 'private' } },
    ];
    invalid.forEach((payload) => {
      expect(() => parseDashboardPerformanceResponse(payload)).toThrow(DashboardContractError);
    });
  });

  it('rejects failed, malformed, credential-bearing, oversized, and overflowing payloads without reflecting details', async () => {
    const invalid = [
      { success: false, message: 'database password sk-private', data: [] },
      { success: true, data: [quotaRow({ password: 'secret' })] },
      { success: true, data: [quotaRow({ quota: -1 })] },
      { success: true, data: [quotaRow({ model_name: 'bad\u0000model' })] },
      { success: true, data: { not: 'an array' } },
      { success: true, data: [quotaRow({ quota: Number.MAX_SAFE_INTEGER }), quotaRow({ quota: 1 })] },
      { success: true, data: [], padding: 'unexpected' },
    ];
    for (const payload of invalid) {
      expect(() => parseDashboardResponse(payload, 'models', 1)).toThrow(DashboardContractError);
    }

    mockedGet
      .mockResolvedValueOnce({
        data: { success: true, data: [], message: 'x'.repeat(2 * 1024 * 1024) },
      })
      .mockResolvedValueOnce({
        data: { success: false, data: [], message: 'database password sk-private' },
      });
    await expect(loadDashboardSection('models', 1, query)).rejects.toEqual(new DashboardContractError());
    await expect(loadDashboardSection('models', 1, query)).rejects.toEqual(new DashboardContractError());

    mockedGet.mockResolvedValueOnce(envelope([quotaRow({ created_at: query.endTimestamp + 1 })]));
    await expect(loadDashboardSection('models', 1, query)).rejects.toEqual(new DashboardContractError());
  });
});
