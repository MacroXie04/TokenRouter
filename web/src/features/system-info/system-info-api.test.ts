import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  deleteStaleSystemInstance,
  deleteStaleSystemInstances,
  listSystemInstances,
  listSystemTasks,
  parseDeleteResponse,
  parseSystemInstancesResponse,
  parseSystemTasksResponse,
  SystemInfoContractError,
} from './system-info-api';

vi.mock('../../shared/api/client', () => ({
  api: { get: vi.fn(), delete: vi.fn() },
}));

const mockedGet = vi.mocked(api.get);
const mockedDelete = vi.mocked(api.delete);

const instanceEnvelope = {
  success: true,
  message: '',
  data: [{
    node_name: 'node-a',
    status: 'online',
    stale_after_seconds: 90,
    started_at: 1_700_000_000,
    last_seen_at: 1_700_000_010,
    info: {
      schema_version: 1,
      node: { name: 'Node A', source: 'env', ignored: 'safe' },
      role: { is_master: true },
      runtime: { version: 'go1.25', goos: 'linux', goarch: 'arm64' },
      host: { hostname: 'host-a' },
      resources: {
        cpu: { usage_percent: 12.5 },
        memory: { usage_percent: 25 },
        storage: { used_percent: 50, used_bytes: 512, total_bytes: 1024 },
      },
    },
  }],
};

const taskEnvelope = {
  success: true,
  message: '',
  data: [{
    id: 7,
    task_id: '0123456789abcdef0123456789abcdef',
    type: 'log_cleanup',
    status: 'running',
    active_key: 'log_cleanup',
    payload: { target_timestamp: 1 },
    state: { progress: 42 },
    result: null,
    error: '',
    locked_by: 'worker-a',
    created_at: 1_700_000_000,
    updated_at: 1_700_000_020,
  }],
};

beforeEach(() => {
  vi.resetAllMocks();
});

describe('system information response contracts', () => {
  it('selects bounded instance information and task progress from valid envelopes', () => {
    expect(parseSystemInstancesResponse(instanceEnvelope)).toEqual([{
      nodeName: 'node-a',
      status: 'online',
      staleAfterSeconds: 90,
      startedAt: 1_700_000_000,
      lastSeenAt: 1_700_000_010,
      info: {
        nodeName: 'Node A',
        source: 'env',
        isMaster: true,
        runtimeVersion: 'go1.25',
        runtimeOS: 'linux',
        runtimeArch: 'arm64',
        hostname: 'host-a',
        cpuPercent: 12.5,
        memoryPercent: 25,
        storageUsedPercent: 50,
        storageUsedBytes: 512,
        storageTotalBytes: 1024,
      },
    }]);
    expect(parseSystemTasksResponse(taskEnvelope)[0]).toMatchObject({
      id: 7,
      type: 'log_cleanup',
      status: 'running',
      progress: 42,
      lockedBy: 'worker-a',
    });
    expect(parseDeleteResponse({ success: true, data: { deleted_count: 3 } })).toBe(3);
  });

  it('rejects failed, malformed, oversized, and unsafe numeric responses', () => {
    const invalidValues = [
      { success: false, data: [] },
      { success: true, data: [{ ...instanceEnvelope.data[0], status: 'unknown' }] },
      { success: true, data: [{ ...instanceEnvelope.data[0], node_name: 'x'.repeat(129) }] },
      { success: true, data: [{ ...instanceEnvelope.data[0], info: { role: { is_master: 'yes' } } }] },
      { success: true, data: [{ ...instanceEnvelope.data[0], info: { resources: { cpu: { usage_percent: 101 } } } }] },
    ];
    for (const value of invalidValues) {
      expect(() => parseSystemInstancesResponse(value)).toThrow(SystemInfoContractError);
    }
    expect(() => parseSystemTasksResponse({
      success: true,
      data: [{ ...taskEnvelope.data[0], state: { progress: Number.NaN } }],
    })).toThrow(SystemInfoContractError);
    expect(() => parseSystemTasksResponse({
      success: true,
      data: [{ ...taskEnvelope.data[0], error: 'x'.repeat(600_000) }],
    })).toThrow(SystemInfoContractError);
    expect(() => parseDeleteResponse({ success: true, data: { deleted_count: -1 } })).toThrow(SystemInfoContractError);
  });
});

describe('system information transports', () => {
  it('uses the exact bounded list routes and encoded stale-instance deletion routes', async () => {
    mockedGet.mockResolvedValueOnce({ data: instanceEnvelope });
    mockedGet.mockResolvedValueOnce({ data: taskEnvelope });
    mockedDelete.mockResolvedValueOnce({ data: { success: true, data: { deleted_count: 1 } } });
    mockedDelete.mockResolvedValueOnce({ data: { success: true, data: { deleted_count: 4 } } });
    const signal = new AbortController().signal;

    await expect(listSystemInstances(signal)).resolves.toHaveLength(1);
    await expect(listSystemTasks(signal)).resolves.toHaveLength(1);
    await expect(deleteStaleSystemInstance('node/a ?')).resolves.toBeUndefined();
    await expect(deleteStaleSystemInstances()).resolves.toBe(4);

    expect(mockedGet).toHaveBeenNthCalledWith(1, '/system-info/instances', {
      signal,
      maxContentLength: 512 * 1024,
      maxBodyLength: 512 * 1024,
    });
    expect(mockedGet).toHaveBeenNthCalledWith(2, '/system-task/list', {
      params: { limit: 20 },
      signal,
      maxContentLength: 512 * 1024,
      maxBodyLength: 512 * 1024,
    });
    expect(mockedDelete).toHaveBeenNthCalledWith(1, '/system-info/instances/node%2Fa%20%3F', {
      maxContentLength: 512 * 1024,
      maxBodyLength: 512 * 1024,
    });
    expect(mockedDelete).toHaveBeenNthCalledWith(2, '/system-info/stale-instances', {
      maxContentLength: 512 * 1024,
      maxBodyLength: 512 * 1024,
    });
  });

  it('rejects a single-delete response that did not delete exactly one row', async () => {
    mockedDelete.mockResolvedValueOnce({ data: { success: true, data: { deleted_count: 0 } } });
    await expect(deleteStaleSystemInstance('stale-node')).rejects.toBeInstanceOf(SystemInfoContractError);
  });
});
