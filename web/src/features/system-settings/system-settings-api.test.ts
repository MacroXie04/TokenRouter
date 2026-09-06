import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import {
  SystemSettingsAccessError,
  SystemSettingsContractError,
  SystemSettingsRequestError,
  assertSystemSettingsRoot,
  cleanupPerformanceLogFiles,
  clearAffinityCache,
  confirmPaymentCompliance,
  isCurrentTokenRouterRelease,
  loadCurrentLogCleanupTask,
  loadAffinityCacheStats,
  loadLatestTokenRouterRelease,
  loadLogCleanupTask,
  loadPerformanceLogSummary,
  loadRuntimeVersion,
  loadSystemOptions,
  normalizeRegistrationSystemOptions,
  parsePerformanceLogSummaryResponse,
  parseRuntimeVersionResponse,
  parseSystemOptionsResponse,
  parseTokenRouterReleaseResponse,
  redactProvidedSystemOptions,
  resetModelPricing,
  startLogCleanupTask,
  TOKENROUTER_LATEST_RELEASE_URL,
  updateSystemOption,
} from './system-settings-api';

vi.mock('../../api', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);
const mockedPut = vi.mocked(api.put);
const mockedDelete = vi.mocked(api.delete);

const ok = (data?: unknown) => data === undefined
  ? { success: true, message: '' }
  : { success: true, message: '', data };

beforeEach(() => vi.resetAllMocks());
afterEach(() => vi.unstubAllGlobals());

describe('system option response contracts', () => {
  it('accepts the exact bounded option envelope and erases leaked credentials', () => {
    expect(parseSystemOptionsResponse(ok([
      { key: 'SystemName', value: 'TokenRouter' },
      { key: 'legacy_api_key', value: 'must-not-render' },
      { key: 'lowercasepassword', value: 'also-secret' },
    ]))).toEqual([
      { key: 'SystemName', value: 'TokenRouter' },
      { key: 'legacy_api_key', value: '', redacted: true },
      { key: 'lowercasepassword', value: '', redacted: true },
    ]);

    expect(redactProvidedSystemOptions([
      { key: 'TurnstileSecretKey', value: '', redacted: true },
    ])).toEqual([{ key: 'TurnstileSecretKey', value: '', redacted: true }]);
    expect(parseSystemOptionsResponse(ok([
      { key: 'oidc.client_secret', value: '', redacted: true },
    ]))).toEqual([{ key: 'oidc.client_secret', value: '', redacted: true }]);
  });

  it('rejects failed, duplicate, structurally widened, unsafe, and oversized responses', () => {
    const invalid = [
      { success: false, message: 'no', data: [] },
      ok([{ key: 'A', value: '' }, { key: 'A', value: '' }]),
      ok([{ key: 'unsafe key', value: '' }]),
      ok([{ key: 'Safe', value: 'x\u2066y' }]),
      ok([{ key: 'Safe', value: String.fromCharCode(0xd800) }]),
      ok([{ key: 'Safe', value: '', extra: true }]),
      ok([{ key: 'Safe', value: '', redacted: true }]),
      ok([{ key: 'oidc.client_secret', value: 'leaked', redacted: true }]),
      ok([{ key: 'oidc.client_secret', value: '', redacted: 'yes' }]),
      { ...ok([]), extra: true },
      ok([{ key: 'Safe', value: 'x'.repeat(1024 * 1024 + 1) }]),
    ];
    expect(() => parseSystemOptionsResponse(invalid[0])).toThrow(SystemSettingsRequestError);
    invalid.slice(1).forEach((value) => {
      expect(() => parseSystemOptionsResponse(value)).toThrow(SystemSettingsContractError);
    });
  });

  it('normalizes the legacy new-user quota only while the canonical option is absent', () => {
    expect(parseSystemOptionsResponse(ok([
      { key: 'SystemName', value: 'Router' },
      { key: 'InitialQuota', value: '500000' },
    ]))).toEqual([
      { key: 'SystemName', value: 'Router' },
      { key: 'QuotaForNewUser', value: '500000', compatibilitySource: 'InitialQuota' },
    ]);

    expect(parseSystemOptionsResponse(ok([
      { key: 'InitialQuota', value: '500000' },
      { key: 'QuotaForNewUser', value: '0' },
    ]))).toEqual([
      { key: 'InitialQuota', value: '500000' },
      { key: 'QuotaForNewUser', value: '0' },
    ]);
    expect(parseSystemOptionsResponse(ok([
      { key: 'InitialQuota', value: '500000' },
      { key: 'QuotaForNewUser', value: '' },
    ]))).toEqual([
      { key: 'InitialQuota', value: '500000' },
      { key: 'QuotaForNewUser', value: '' },
    ]);

    const normalized = normalizeRegistrationSystemOptions([{ key: 'InitialQuota', value: '7' }]);
    normalized[0].value = 'mutated';
    expect(normalizeRegistrationSystemOptions([{ key: 'InitialQuota', value: '7' }]))
      .toEqual([{ key: 'QuotaForNewUser', value: '7', compatibilitySource: 'InitialQuota' }]);
    expect(() => redactProvidedSystemOptions([{
      key: 'SystemName', value: 'Router', compatibilitySource: 'InitialQuota',
    } as never])).toThrow(SystemSettingsContractError);
  });

  it('fails closed for non-root or nonsensical roles', () => {
    expect(() => assertSystemSettingsRoot(100)).not.toThrow();
    expect(() => assertSystemSettingsRoot(99)).toThrow(SystemSettingsAccessError);
    expect(() => assertSystemSettingsRoot(Number.NaN)).toThrow(SystemSettingsAccessError);
  });
});

describe('system option transports', () => {
  it('loads and writes through the exact root option route with bounded requests', async () => {
    mockedGet.mockResolvedValueOnce({ data: ok([{ key: 'SystemName', value: 'Router' }]) });
    mockedPut.mockResolvedValueOnce({ data: ok() });
    const signal = new AbortController().signal;

    await expect(loadSystemOptions(signal)).resolves.toEqual([{ key: 'SystemName', value: 'Router' }]);
    await expect(updateSystemOption({ key: 'SystemName', value: 'New name' }, signal)).resolves.toBeUndefined();

    expect(mockedGet).toHaveBeenCalledWith('/option/', {
      signal,
      timeout: 15_000,
      maxContentLength: 4 * 1024 * 1024,
      maxBodyLength: 4 * 1024 * 1024,
    });
    expect(mockedPut).toHaveBeenCalledWith('/option/', { key: 'SystemName', value: 'New name' }, {
      signal,
      timeout: 15_000,
      maxContentLength: 4 * 1024 * 1024,
      maxBodyLength: 4 * 1024 * 1024,
    });
  });

  it('never sends objects or arrays to the scalar-only option endpoint', () => {
    expect(() => updateSystemOption({ key: 'PayMethods', value: [{ type: 'card' }] as never }))
      .toThrow(SystemSettingsContractError);
    expect(() => updateSystemOption({ key: 'ModelPrice', value: { model: 1 } as never }))
      .toThrow(SystemSettingsContractError);
    expect(mockedPut).not.toHaveBeenCalled();
  });

  it('serializes writes and skips an aborted write before it reaches the network', async () => {
    let release!: () => void;
    mockedPut.mockImplementationOnce(() => new Promise((resolve) => {
      release = () => resolve({ data: ok() });
    }));
    const first = updateSystemOption({ key: 'First', value: '1' });
    const controller = new AbortController();
    const second = updateSystemOption({ key: 'Second', value: '2' }, controller.signal);
    await vi.waitFor(() => expect(mockedPut).toHaveBeenCalledOnce());
    controller.abort();
    release();

    await expect(first).resolves.toBeUndefined();
    await expect(second).rejects.toMatchObject({ name: 'AbortError' });
    expect(mockedPut).toHaveBeenCalledOnce();
  });

  it('uses the dedicated compliance, pricing-reset, and affinity contracts exactly', async () => {
    mockedPost.mockResolvedValueOnce({ data: ok({
      confirmed: true,
      terms_version: 'v1',
      confirmed_at: 1_780_000_000,
      confirmed_by: 7,
    }) });
    mockedPost.mockResolvedValueOnce({ data: ok() });
    mockedGet.mockResolvedValueOnce({ data: ok({
      enabled: true,
      total: 3,
      unknown: 1,
      by_rule_name: { premium: 2 },
      cache_capacity: 100,
      cache_algo: 'lru',
    }) });
    mockedDelete.mockResolvedValueOnce({ data: ok({ deleted: 2 }) });
    mockedDelete.mockResolvedValueOnce({ data: ok({ deleted: 3 }) });
    const signal = new AbortController().signal;

    await expect(confirmPaymentCompliance(signal)).resolves.toEqual({
      confirmed: true,
      termsVersion: 'v1',
      confirmedAt: 1_780_000_000,
      confirmedBy: 7,
    });
    await expect(resetModelPricing(signal)).resolves.toBeUndefined();
    await expect(loadAffinityCacheStats(signal)).resolves.toMatchObject({
      total: 3,
      unknown: 1,
      byRuleName: { premium: 2 },
      cacheCapacity: 100,
      cacheAlgorithm: 'lru',
    });
    await expect(clearAffinityCache({ ruleName: ' premium ' }, signal)).resolves.toBe(2);
    await expect(clearAffinityCache({ all: true }, signal)).resolves.toBe(3);

    expect(mockedPost.mock.calls[0]).toEqual([
      '/option/payment_compliance',
      { confirmed: true },
      expect.objectContaining({ signal, timeout: 15_000 }),
    ]);
    expect(mockedPost.mock.calls[1]).toEqual([
      '/option/rest_model_ratio',
      undefined,
      expect.objectContaining({ signal, timeout: 15_000 }),
    ]);
    expect(mockedGet).toHaveBeenCalledWith('/option/channel_affinity_cache', expect.objectContaining({ signal }));
    expect(mockedDelete.mock.calls[0]).toEqual([
      '/option/channel_affinity_cache',
      expect.objectContaining({ params: { rule_name: 'premium' }, signal }),
    ]);
    expect(mockedDelete.mock.calls[1]).toEqual([
      '/option/channel_affinity_cache',
      expect.objectContaining({ params: { all: true }, signal }),
    ]);
  });

  it('rejects inconsistent affinity statistics and unsuccessful mutations', async () => {
    mockedGet.mockResolvedValueOnce({ data: ok({
      enabled: true,
      total: 3,
      unknown: 0,
      by_rule_name: { premium: 2 },
      cache_capacity: 100,
      cache_algo: 'lru',
    }) });
    mockedPut.mockResolvedValueOnce({ data: { success: false, message: 'server rejected it' } });
    await expect(loadAffinityCacheStats()).rejects.toBeInstanceOf(SystemSettingsContractError);
    await expect(updateSystemOption({ key: 'SystemName', value: 'Name' })).rejects.toMatchObject({
      name: 'SystemSettingsRequestError',
      message: 'server rejected it',
    });
  });
});

describe('log-maintenance contracts', () => {
  const targetTimestamp = Math.floor(Date.now() / 1_000) - 3_600;
  const task = (overrides: Record<string, unknown> = {}) => ({
    id: 7,
    task_id: '0123456789abcdef0123456789abcdef',
    type: 'log_cleanup',
    status: 'pending',
    active_key: 'log_cleanup',
    payload: { target_timestamp: targetTimestamp, batch_size: 100 },
    state: null,
    result: null,
    error: '',
    locked_by: '',
    created_at: targetTimestamp,
    updated_at: targetTimestamp,
    ...overrides,
  });

  it('strictly reconciles bounded local log-file summaries', () => {
    const response = ok({
      log_dir: '/var/log/tokenrouter',
      enabled: true,
      file_count: 2,
      total_size: 8,
      oldest_time: '2026-09-01T00:00:00Z',
      newest_time: '2026-09-02T00:00:00Z',
      files: [
        { name: 'oneapi-20260902.log', size: 5, mod_time: '2026-09-02T00:00:00Z' },
        { name: 'oneapi-20260901.log', size: 3, mod_time: '2026-09-01T00:00:00Z' },
      ],
    });
    expect(parsePerformanceLogSummaryResponse(response)).toEqual({
      enabled: true,
      logDirectory: '/var/log/tokenrouter',
      fileCount: 2,
      totalSize: 8,
      oldestTime: '2026-09-01T00:00:00Z',
      newestTime: '2026-09-02T00:00:00Z',
    });

    const invalid = [
      ok({ ...response.data, file_count: 1 }),
      ok({ ...response.data, total_size: 9 }),
      ok({ ...response.data, files: [{ name: '../oneapi-unsafe.log', size: 8, mod_time: '2026-09-01T00:00:00Z' }] }),
      ok({ log_dir: '/tmp/logs', enabled: false, file_count: 0, total_size: 0, files: null }),
      ok({ ...response.data, extra: true }),
    ];
    invalid.forEach((value) => expect(() => parsePerformanceLogSummaryResponse(value))
      .toThrow(SystemSettingsContractError));
  });

  it('uses exact root-only file and database cleanup transports', async () => {
    mockedGet.mockResolvedValueOnce({ data: ok({
      log_dir: '', enabled: false, file_count: 0, total_size: 0, files: null,
    }) });
    mockedDelete.mockResolvedValueOnce({ data: ok({
      deleted_count: 2, freed_bytes: 4_096, failed_files: [],
    }) });
    mockedGet.mockResolvedValueOnce({ data: ok(task()) });
    mockedGet.mockResolvedValueOnce({ data: ok(task({
      status: 'succeeded',
      active_key: undefined,
      state: { total: 4, processed: 4, progress: 100, remaining: 0 },
      result: { deleted_count: 4 },
    })) });
    mockedPost.mockResolvedValueOnce({ data: ok(task()) });
    const signal = new AbortController().signal;

    await expect(loadPerformanceLogSummary(signal)).resolves.toMatchObject({ enabled: false, fileCount: 0 });
    await expect(cleanupPerformanceLogFiles('by_days', 30, signal)).resolves.toEqual({
      deletedCount: 2, freedBytes: 4_096, failedCount: 0,
    });
    await expect(loadCurrentLogCleanupTask(signal)).resolves.toMatchObject({ status: 'pending', targetTimestamp });
    await expect(loadLogCleanupTask('0123456789abcdef0123456789abcdef', signal)).resolves.toMatchObject({
      status: 'succeeded', deletedCount: 4, progress: 100,
    });
    await expect(startLogCleanupTask(targetTimestamp, signal)).resolves.toMatchObject({ status: 'pending' });

    expect(mockedGet.mock.calls[0]).toEqual(['/performance/logs', expect.objectContaining({ signal, timeout: 15_000 })]);
    expect(mockedDelete).toHaveBeenCalledWith('/performance/logs', expect.objectContaining({
      params: { mode: 'by_days', value: 30 }, signal, timeout: 15_000,
    }));
    expect(mockedGet.mock.calls[1]).toEqual(['/system-task/current', expect.objectContaining({
      params: { type: 'log_cleanup' }, signal,
    })]);
    expect(mockedGet.mock.calls[2]).toEqual([
      '/system-task/0123456789abcdef0123456789abcdef', expect.objectContaining({ signal }),
    ]);
    expect(mockedPost).toHaveBeenCalledWith('/system-task/log-cleanup', undefined, expect.objectContaining({
      params: { target_timestamp: targetTimestamp }, signal,
    }));
  });

  it('rejects unsafe cleanup input and corrupt task state before it is rendered', async () => {
    expect(() => cleanupPerformanceLogFiles('by_count', 0)).toThrow(SystemSettingsContractError);
    expect(() => cleanupPerformanceLogFiles('by_days', 36_501)).toThrow(SystemSettingsContractError);
    expect(() => startLogCleanupTask(Math.floor(Date.now() / 1_000) + 60)).toThrow(SystemSettingsContractError);
    await expect(loadLogCleanupTask('../task')).rejects.toBeInstanceOf(SystemSettingsContractError);
    expect(mockedDelete).not.toHaveBeenCalled();
    expect(mockedPost).not.toHaveBeenCalled();

    mockedGet.mockResolvedValueOnce({ data: ok(task({
      status: 'running',
      state: { total: 10, processed: 4, progress: 40, remaining: 7 },
    })) });
    await expect(loadCurrentLogCleanupTask()).rejects.toBeInstanceOf(SystemSettingsContractError);
  });
});

describe('update-checker contracts and transports', () => {
  const release = (overrides: Record<string, unknown> = {}) => ({
    tag_name: 'v1.2.3',
    name: 'TokenRouter 1.2.3',
    body: 'Bounded release notes.\nSecond line.',
    html_url: 'https://github.com/MacroXie04/TokenRouter/releases/tag/v1.2.3',
    published_at: '2026-09-01T00:00:00Z',
    assets: [],
    ...overrides,
  });

  it('accepts bounded status and fixed-origin release data', () => {
    expect(parseRuntimeVersionResponse(ok({
      version: '1.2.3', start_time: 1_700_000_000, unrelated_status_field: true,
    }))).toEqual({ currentVersion: '1.2.3', startTimestamp: 1_700_000_000 });
    expect(parseTokenRouterReleaseResponse(release())).toEqual({
      tagName: 'v1.2.3',
      name: 'TokenRouter 1.2.3',
      notes: 'Bounded release notes.\nSecond line.',
      url: 'https://github.com/MacroXie04/TokenRouter/releases/tag/v1.2.3',
      publishedAt: '2026-09-01T00:00:00Z',
    });
    expect(isCurrentTokenRouterRelease('1.2.3', 'v1.2.3')).toBe(true);
    expect(isCurrentTokenRouterRelease('dev', 'v1.2.3')).toBe(false);
    expect(isCurrentTokenRouterRelease('1.2.2', 'v1.2.3')).toBe(false);
  });

  it('rejects malformed versions, untrusted links, unsafe text, and oversized notes', () => {
    expect(() => parseRuntimeVersionResponse(ok({ version: ' 1.2.3', start_time: 1 })))
      .toThrow(SystemSettingsContractError);
    expect(() => parseRuntimeVersionResponse(ok({ version: '1.2.3', start_time: 0 })))
      .toThrow(SystemSettingsContractError);
    for (const value of [
      release({ tag_name: '../v1.2.3' }),
      release({ html_url: 'https://example.test/MacroXie04/TokenRouter/releases/tag/v1.2.3' }),
      release({ html_url: 'https://github.com/MacroXie04/TokenRouter/releases/tag/v9.9.9' }),
      release({ name: 'unsafe\u2066name' }),
      release({ body: 'x'.repeat(128 * 1_024 + 1) }),
      release({ published_at: 'not-a-date' }),
    ]) {
      expect(() => parseTokenRouterReleaseResponse(value)).toThrow(SystemSettingsContractError);
    }
  });

  it('uses the public status route and a privacy-preserving fixed GitHub request', async () => {
    mockedGet.mockResolvedValueOnce({ data: ok({ version: '1.2.3', start_time: 1_700_000_000 }) });
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(release()), {
      status: 200,
      headers: { 'content-type': 'application/json; charset=utf-8' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    const signal = new AbortController().signal;

    await expect(loadRuntimeVersion(signal)).resolves.toEqual({
      currentVersion: '1.2.3', startTimestamp: 1_700_000_000,
    });
    await expect(loadLatestTokenRouterRelease(signal)).resolves.toMatchObject({ tagName: 'v1.2.3' });

    expect(mockedGet).toHaveBeenCalledWith('/status', expect.objectContaining({ signal, timeout: 15_000 }));
    expect(fetchMock).toHaveBeenCalledWith(TOKENROUTER_LATEST_RELEASE_URL, expect.objectContaining({
      method: 'GET',
      credentials: 'omit',
      redirect: 'error',
      referrerPolicy: 'no-referrer',
      cache: 'no-store',
      signal: expect.any(AbortSignal),
    }));
    vi.unstubAllGlobals();
  });

  it('fails closed when the streamed release response exceeds its byte bound', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('x'.repeat(512 * 1_024 + 1), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(loadLatestTokenRouterRelease()).rejects.toBeInstanceOf(SystemSettingsContractError);
    vi.unstubAllGlobals();
  });
});
