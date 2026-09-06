import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import { loadPerformanceMetrics, loadPerformanceSummary } from './performance-api';
import { PERFORMANCE_SERIES_SCHEMA, PerformanceContractError } from './performance-metrics';

vi.mock('../../api', () => ({ api: { get: vi.fn() } }));

const mockedAPI = vi.mocked(api);

beforeEach(() => vi.resetAllMocks());

describe('performance metrics transport', () => {
  it('uses the exact bounded detail route, including an optional validated group', async () => {
    mockedAPI.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: { model_name: 'alpha/model', series_schema: PERFORMANCE_SERIES_SCHEMA, groups: [] },
      },
    });
    const controller = new AbortController();
    await expect(loadPerformanceMetrics({
      modelName: 'alpha/model', hours: 48, group: 'vip', signal: controller.signal,
    })).resolves.toMatchObject({ model_name: 'alpha/model' });
    expect(mockedAPI.get).toHaveBeenCalledWith('/perf-metrics', {
      params: { model: 'alpha/model', hours: 48, group: 'vip' },
      signal: controller.signal,
      maxContentLength: 4 * 1024 * 1024,
      maxBodyLength: 4 * 1024 * 1024,
    });
  });

  it('uses the exact bounded summary route and rejects invalid inputs before I/O', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: { success: true, data: { models: [] } } });
    const controller = new AbortController();
    await expect(loadPerformanceSummary(24, controller.signal)).resolves.toEqual({ models: [] });
    expect(mockedAPI.get).toHaveBeenCalledWith('/perf-metrics/summary', {
      params: { hours: 24 },
      signal: controller.signal,
      maxContentLength: 4 * 1024 * 1024,
      maxBodyLength: 4 * 1024 * 1024,
    });

    await expect(loadPerformanceMetrics({ modelName: ' bad ' })).rejects.toBeInstanceOf(PerformanceContractError);
    await expect(loadPerformanceMetrics({ modelName: 'alpha', hours: 721 })).rejects.toBeInstanceOf(PerformanceContractError);
    await expect(loadPerformanceMetrics({ modelName: 'alpha', group: 'vip\u202e' })).rejects.toBeInstanceOf(PerformanceContractError);
    expect(mockedAPI.get).toHaveBeenCalledTimes(1);
  });
});
