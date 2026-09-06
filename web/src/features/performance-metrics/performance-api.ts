import { api } from '../../shared/api/client';
import {
  MAX_PERFORMANCE_MODEL_NAME_BYTES,
  parsePerformanceMetricsResponse,
  parsePerformanceSummaryResponse,
  validatePerformanceHours,
  validatePerformanceIdentifier,
  type PerformanceMetrics,
  type PerformanceSummary,
} from './performance-metrics';

const PERFORMANCE_RESPONSE_BYTES = 4 * 1024 * 1024;

export interface PerformanceMetricsRequest {
  modelName: string;
  hours?: number;
  group?: string;
  signal?: AbortSignal;
}

export async function loadPerformanceMetrics({
  modelName,
  hours = 24,
  group,
  signal,
}: PerformanceMetricsRequest): Promise<PerformanceMetrics> {
  const safeModelName = validatePerformanceIdentifier(modelName, MAX_PERFORMANCE_MODEL_NAME_BYTES);
  const safeHours = validatePerformanceHours(hours);
  const safeGroup = group === undefined ? undefined : validatePerformanceIdentifier(group, 128);
  const response = await api.get<unknown>('/perf-metrics', {
    params: {
      model: safeModelName,
      hours: safeHours,
      ...(safeGroup ? { group: safeGroup } : {}),
    },
    signal,
    maxContentLength: PERFORMANCE_RESPONSE_BYTES,
    maxBodyLength: PERFORMANCE_RESPONSE_BYTES,
  });
  return parsePerformanceMetricsResponse(response.data, safeModelName);
}

export async function loadPerformanceSummary(
  hours = 24,
  signal?: AbortSignal,
): Promise<PerformanceSummary> {
  const safeHours = validatePerformanceHours(hours);
  const response = await api.get<unknown>('/perf-metrics/summary', {
    params: { hours: safeHours },
    signal,
    maxContentLength: PERFORMANCE_RESPONSE_BYTES,
    maxBodyLength: PERFORMANCE_RESPONSE_BYTES,
  });
  return parsePerformanceSummaryResponse(response.data);
}
