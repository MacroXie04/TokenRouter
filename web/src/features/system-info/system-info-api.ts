import { api } from '../../shared/api/client';

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_INSTANCES = 1_000;
const MAX_TASKS = 100;
const MAX_DELETED = 10_000_000;

const responseLimits = {
  maxContentLength: MAX_RESPONSE_BYTES,
  maxBodyLength: MAX_RESPONSE_BYTES,
};

type UnknownRecord = Record<string, unknown>;

export type SystemInstanceStatus = 'online' | 'stale';

export interface SystemInstanceInfo {
  nodeName?: string;
  source?: string;
  isMaster?: boolean;
  runtimeVersion?: string;
  runtimeOS?: string;
  runtimeArch?: string;
  hostname?: string;
  cpuPercent?: number;
  memoryPercent?: number;
  storageUsedPercent?: number;
  storageUsedBytes?: number;
  storageTotalBytes?: number;
}

export interface SystemInstance {
  nodeName: string;
  status: SystemInstanceStatus;
  staleAfterSeconds: number;
  startedAt: number;
  lastSeenAt: number;
  info?: SystemInstanceInfo;
}

export type SystemTaskStatus = 'pending' | 'running' | 'succeeded' | 'failed';

export interface SystemTask {
  id: number;
  taskId: string;
  type: string;
  status: SystemTaskStatus;
  progress: number | null;
  lockedBy: string;
  error: string;
  createdAt: number;
  updatedAt: number;
}

export class SystemInfoContractError extends Error {
  constructor() {
    super('Invalid system information response');
    this.name = 'SystemInfoContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new SystemInfoContractError();
  }
  return value as UnknownRecord;
}

function boundedPayload(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new SystemInfoContractError();
  }
  if (encoded.length > MAX_RESPONSE_BYTES) throw new SystemInfoContractError();
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new SystemInfoContractError();
  }
  return value as number;
}

function finite(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new SystemInfoContractError();
  }
  return value;
}

function text(value: unknown, maximum: number, allowEmpty = true): string {
  if (typeof value !== 'string' || value.length > maximum || (!allowEmpty && value.length === 0)) {
    throw new SystemInfoContractError();
  }
  return value;
}

function optionalText(value: unknown, maximum: number): string | undefined {
  if (value === undefined || value === null) return undefined;
  return text(value, maximum);
}

function optionalRecord(value: unknown): UnknownRecord | undefined {
  if (value === undefined || value === null) return undefined;
  return record(value);
}

function optionalFinite(value: unknown, minimum: number, maximum: number): number | undefined {
  if (value === undefined || value === null) return undefined;
  return finite(value, minimum, maximum);
}

function successfulData(value: unknown): unknown {
  boundedPayload(value);
  const envelope = record(value);
  if (envelope.success !== true || !Object.prototype.hasOwnProperty.call(envelope, 'data')) {
    throw new SystemInfoContractError();
  }
  return envelope.data;
}

function parseInstanceInfo(value: unknown): SystemInstanceInfo | undefined {
  const info = optionalRecord(value);
  if (!info) return undefined;

  if (info.schema_version !== undefined) integer(info.schema_version, 0, 1_000);
  const node = optionalRecord(info.node);
  const role = optionalRecord(info.role);
  const runtime = optionalRecord(info.runtime);
  const host = optionalRecord(info.host);
  const resources = optionalRecord(info.resources);
  const cpu = optionalRecord(resources?.cpu);
  const memory = optionalRecord(resources?.memory);
  const storage = optionalRecord(resources?.storage);

  let isMaster: boolean | undefined;
  if (role?.is_master !== undefined) {
    if (typeof role.is_master !== 'boolean') throw new SystemInfoContractError();
    isMaster = role.is_master;
  }

  return {
    nodeName: optionalText(node?.name, 128),
    source: optionalText(node?.source, 64),
    isMaster,
    runtimeVersion: optionalText(runtime?.version, 64),
    runtimeOS: optionalText(runtime?.goos, 32),
    runtimeArch: optionalText(runtime?.goarch, 32),
    hostname: optionalText(host?.hostname, 255),
    cpuPercent: optionalFinite(cpu?.usage_percent, 0, 100),
    memoryPercent: optionalFinite(memory?.usage_percent, 0, 100),
    storageUsedPercent: optionalFinite(storage?.used_percent, 0, 100),
    storageUsedBytes: optionalFinite(storage?.used_bytes, 0, Number.MAX_SAFE_INTEGER),
    storageTotalBytes: optionalFinite(storage?.total_bytes, 0, Number.MAX_SAFE_INTEGER),
  };
}

function parseInstance(value: unknown): SystemInstance {
  const item = record(value);
  if (item.status !== 'online' && item.status !== 'stale') throw new SystemInfoContractError();
  return {
    nodeName: text(item.node_name, 128, false),
    status: item.status,
    staleAfterSeconds: integer(item.stale_after_seconds, 1, 86_400),
    startedAt: integer(item.started_at, 0, Number.MAX_SAFE_INTEGER),
    lastSeenAt: integer(item.last_seen_at, 0, Number.MAX_SAFE_INTEGER),
    info: parseInstanceInfo(item.info),
  };
}

export function parseSystemInstancesResponse(value: unknown): SystemInstance[] {
  const data = successfulData(value);
  if (!Array.isArray(data) || data.length > MAX_INSTANCES) throw new SystemInfoContractError();
  return data.map(parseInstance);
}

function parseTask(value: unknown): SystemTask {
  const item = record(value);
  if (!['pending', 'running', 'succeeded', 'failed'].includes(String(item.status))) {
    throw new SystemInfoContractError();
  }
  const state = optionalRecord(item.state);
  const progress = state?.progress === undefined || state.progress === null
    ? null
    : finite(state.progress, 0, 100);
  if (item.active_key !== undefined && item.active_key !== null) text(item.active_key, 64);
  if (item.payload !== undefined && item.payload !== null) record(item.payload);
  if (item.result !== undefined && item.result !== null) record(item.result);
  return {
    id: integer(item.id, 1, Number.MAX_SAFE_INTEGER),
    taskId: text(item.task_id, 64, false),
    type: text(item.type, 64, false),
    status: item.status as SystemTaskStatus,
    progress,
    lockedBy: optionalText(item.locked_by, 128) ?? '',
    error: optionalText(item.error, 2_048) ?? '',
    createdAt: integer(item.created_at, 0, Number.MAX_SAFE_INTEGER),
    updatedAt: integer(item.updated_at, 0, Number.MAX_SAFE_INTEGER),
  };
}

export function parseSystemTasksResponse(value: unknown): SystemTask[] {
  const data = successfulData(value);
  if (!Array.isArray(data) || data.length > MAX_TASKS) throw new SystemInfoContractError();
  return data.map(parseTask);
}

export function parseDeleteResponse(value: unknown): number {
  const data = record(successfulData(value));
  return integer(data.deleted_count, 0, MAX_DELETED);
}

export async function listSystemInstances(signal?: AbortSignal): Promise<SystemInstance[]> {
  const response = await api.get<unknown>('/system-info/instances', { signal, ...responseLimits });
  return parseSystemInstancesResponse(response.data);
}

export async function listSystemTasks(signal?: AbortSignal): Promise<SystemTask[]> {
  const response = await api.get<unknown>('/system-task/list', {
    params: { limit: 20 },
    signal,
    ...responseLimits,
  });
  return parseSystemTasksResponse(response.data);
}

export async function deleteStaleSystemInstances(): Promise<number> {
  const response = await api.delete<unknown>('/system-info/stale-instances', responseLimits);
  return parseDeleteResponse(response.data);
}

export async function deleteStaleSystemInstance(nodeName: string): Promise<void> {
  const safeNodeName = text(nodeName, 128, false);
  const response = await api.delete<unknown>(
    `/system-info/instances/${encodeURIComponent(safeNodeName)}`,
    responseLimits,
  );
  if (parseDeleteResponse(response.data) !== 1) throw new SystemInfoContractError();
}
