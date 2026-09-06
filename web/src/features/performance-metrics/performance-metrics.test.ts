import { describe, expect, it } from 'vitest';
import {
  PERFORMANCE_SERIES_SCHEMA,
  PerformanceContractError,
  parsePerformanceMetricsResponse,
  parsePerformanceSummaryResponse,
  validatePerformanceHours,
  validatePerformanceIdentifier,
} from './performance-metrics';

function detailResponse() {
  return {
    success: true,
    data: {
      model_name: 'alpha-model',
      series_schema: PERFORMANCE_SERIES_SCHEMA,
      groups: [{
        group: 'default', avg_ttft_ms: 50, avg_latency_ms: 250, success_rate: 99.5, avg_tps: 42.25,
        series: [
          { ts: 1_700_000_000, avg_ttft_ms: 40, avg_latency_ms: 200, success_rate: 100, avg_tps: 40 },
          { ts: 1_700_003_600, avg_ttft_ms: 60, avg_latency_ms: 300, success_rate: 99, avg_tps: 44.5 },
        ],
      }],
    },
  };
}

describe('performance metrics contracts', () => {
  it('parses only bounded detail and summary display fields', () => {
    expect(parsePerformanceMetricsResponse(detailResponse(), 'alpha-model').groups[0]).toMatchObject({
      group: 'default', avg_ttft_ms: 50, success_rate: 99.5,
    });
    expect(parsePerformanceSummaryResponse({
      success: true,
      data: { models: [{
        model_name: 'alpha-model', avg_latency_ms: 250, success_rate: 99.5, avg_tps: 42.25,
        recent_success_rates: [98, 99.5, 100], private_count: 123,
      }] },
      internal: 'not exposed',
    })).toEqual({ models: [{
      model_name: 'alpha-model', avg_latency_ms: 250, success_rate: 99.5, avg_tps: 42.25,
      recent_success_rates: [98, 99.5, 100],
    }] });
  });

  it('rejects cross-model, invalid-rate, duplicate, unsorted, and unknown-schema detail responses', () => {
    expect(() => parsePerformanceMetricsResponse(detailResponse(), 'other')).toThrow(PerformanceContractError);

    const invalidRate = detailResponse();
    invalidRate.data.groups[0].success_rate = 101;
    expect(() => parsePerformanceMetricsResponse(invalidRate, 'alpha-model')).toThrow(PerformanceContractError);

    const unsorted = detailResponse();
    unsorted.data.groups[0].series.reverse();
    expect(() => parsePerformanceMetricsResponse(unsorted, 'alpha-model')).toThrow(PerformanceContractError);

    const duplicate = detailResponse();
    duplicate.data.groups.push({ ...duplicate.data.groups[0], series: [] });
    expect(() => parsePerformanceMetricsResponse(duplicate, 'alpha-model')).toThrow(PerformanceContractError);

    const wrongSchema = detailResponse();
    wrongSchema.data.series_schema = 'unknown';
    expect(() => parsePerformanceMetricsResponse(wrongSchema, 'alpha-model')).toThrow(PerformanceContractError);
  });

  it('rejects malformed summaries and validates outbound query bounds before transport', () => {
    expect(() => parsePerformanceSummaryResponse({
      success: true,
      data: { models: [
        { model_name: 'same', avg_latency_ms: 1, success_rate: 99, avg_tps: 2 },
        { model_name: 'same', avg_latency_ms: 1, success_rate: 99, avg_tps: 2 },
      ] },
    })).toThrow(PerformanceContractError);
    expect(() => parsePerformanceSummaryResponse({
      success: true,
      data: { models: [{ model_name: 'bad\u202ename', avg_latency_ms: 1, success_rate: 99, avg_tps: 2 }] },
    })).toThrow(PerformanceContractError);
    expect(() => parsePerformanceSummaryResponse({
      success: true,
      data: { models: [{
        model_name: 'alpha', avg_latency_ms: 1, success_rate: 99, avg_tps: 2,
        recent_success_rates: [1, 2, 3, 4],
      }] },
    })).toThrow(PerformanceContractError);

    expect(validatePerformanceHours(1)).toBe(1);
    expect(validatePerformanceHours(720)).toBe(720);
    expect(() => validatePerformanceHours(0)).toThrow(PerformanceContractError);
    expect(() => validatePerformanceHours(721)).toThrow(PerformanceContractError);
    expect(validatePerformanceIdentifier('alpha/model', 512)).toBe('alpha/model');
    expect(() => validatePerformanceIdentifier(' alpha ', 512)).toThrow(PerformanceContractError);
  });
});
