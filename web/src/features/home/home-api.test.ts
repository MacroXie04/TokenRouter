import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import { loadBasicRankings, loadPerformanceSummary, parseBasicRankingsResponse, parsePerformanceSummaryResponse } from './home-api';
import { loadPublicContent, parsePublicContentResponse, PublicHomeContractError } from '../public-documents/public-content-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn() } }));

const mockedAPI = vi.mocked(api);

beforeEach(() => vi.resetAllMocks());

describe('public home contracts', () => {
  it('accepts exact content, performance, and basic-ranking envelopes', () => {
    expect(parsePublicContentResponse({ success: true, data: '  # Notice  ' })).toBe('# Notice');
    expect(parsePerformanceSummaryResponse({
      success: true,
      data: { models: [{
        model_name: 'alpha', avg_latency_ms: 120, success_rate: 99.9, avg_tps: 42,
        recent_success_rates: [100, 99.5], private_count: 100,
      }] },
    })).toEqual([{ model_name: 'alpha', avg_latency_ms: 120, success_rate: 99.9, avg_tps: 42 }]);
    expect(parseBasicRankingsResponse({
      success: true, data: [{ model_name: 'alpha', count: 10, quota: 50 }],
    })).toEqual([{ model_name: 'alpha', count: 10, quota: 50 }]);
  });

  it('rejects malformed, unsafe, and oversized data without exposing it', () => {
    expect(() => parsePublicContentResponse({ success: false, data: 'private failure' })).toThrow(PublicHomeContractError);
    expect(() => parsePublicContentResponse({ success: true, data: 'x'.repeat(1_000_001) })).toThrow(PublicHomeContractError);
    expect(() => parsePerformanceSummaryResponse({
      success: true, data: { models: [{ model_name: 'alpha', avg_latency_ms: -1, success_rate: 50, avg_tps: 1 }] },
    })).toThrow(PublicHomeContractError);
    expect(() => parseBasicRankingsResponse({ success: true, data: [{ model_name: 'alpha', count: -1, quota: 1 }] })).toThrow();
  });
});

describe('public home API routes', () => {
  it('uses exact bounded cancellable requests and the 24-hour summary query', async () => {
    mockedAPI.get
      .mockResolvedValueOnce({ data: { success: true, data: '# Notice' } })
      .mockResolvedValueOnce({ data: { success: true, data: { models: [] } } })
      .mockResolvedValueOnce({ data: { success: true, data: [] } });
    const controller = new AbortController();

    await expect(loadPublicContent('/notice', controller.signal)).resolves.toBe('# Notice');
    await expect(loadPerformanceSummary(controller.signal)).resolves.toEqual([]);
    await expect(loadBasicRankings(controller.signal)).resolves.toEqual([]);

    expect(mockedAPI.get).toHaveBeenNthCalledWith(1, '/notice', {
      signal: controller.signal,
      maxContentLength: 6 * 1024 * 1024,
      maxBodyLength: 6 * 1024 * 1024,
    });
    expect(mockedAPI.get).toHaveBeenNthCalledWith(2, '/perf-metrics/summary', {
      params: { hours: 24 },
      signal: controller.signal,
      maxContentLength: 2 * 1024 * 1024,
      maxBodyLength: 2 * 1024 * 1024,
    });
    expect(mockedAPI.get).toHaveBeenNthCalledWith(3, '/rankings', {
      signal: controller.signal,
      maxContentLength: 2 * 1024 * 1024,
      maxBodyLength: 2 * 1024 * 1024,
    });
  });
});
