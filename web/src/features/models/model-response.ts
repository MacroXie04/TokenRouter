

export const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;

export const MAX_ID = 2_147_483_647;

export const MAX_TOTAL = 1_000_000_000;

export const MAX_PAGE = 1_000_000;

export const MAX_ROWS = 100;

export const MAX_UNIX_SECONDS = 4_102_444_800;

export const MAX_METADATA_BYTES = 1024 * 1024;

export const MAX_RELATED_ITEMS = 10_000;

export const MAX_LOG_BYTES = 4 * 1024 * 1024;

export const MAX_LOG_RESPONSE_BYTES = (MAX_LOG_BYTES * 2) + 1_024;

export const MAX_DEPLOYMENT_REQUEST_BYTES = 256 * 1024;

export const MAX_PROVIDER_ROWS = 5_000;

export const MAX_CONTAINER_EVENTS = 2_000;

export const MAX_CONTAINER_EVENT_BYTES = 64 * 1024;

export const MAX_ENVIRONMENT_VARIABLES = 256;

export const MAX_ENVIRONMENT_KEY_BYTES = 128;

export const MAX_ENVIRONMENT_VALUE_BYTES = 8_192;

export const MAX_ARGUMENT_ITEMS = 128;

export const MAX_ARGUMENT_BYTES = 4_096;

export const IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/u;

export const SYNC_FIELDS = new Set(['description', 'icon', 'tags', 'vendor', 'name_rule', 'status']);

export const DEPLOYMENT_STATUSES = new Set([
  'running',
  'completed',
  'failed',
  'deployment requested',
  'termination requested',
  'destroyed',
]);

export type UnknownRecord = Record<string, unknown>;

export class ModelsContractError extends Error {
  constructor() {
    super('Invalid models API response');
    this.name = 'ModelsContractError';
  }
}

export class ModelsAccessError extends Error {
  constructor() {
    super('Administrator access is required');
    this.name = 'ModelsAccessError';
  }
}

export function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new ModelsContractError();
  return value as UnknownRecord;
}

export function allowedKeys(value: UnknownRecord, allowed: readonly string[]): void {
  const permitted = new Set(allowed);
  if (Object.keys(value).some((key) => !permitted.has(key))) throw new ModelsContractError();
}

export function payloadBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    throw new ModelsContractError();
  }
}

export function envelope(value: unknown, maximumBytes = MAX_RESPONSE_BYTES): UnknownRecord {
  if (payloadBytes(value) > maximumBytes) throw new ModelsContractError();
  const parsed = record(value);
  allowedKeys(parsed, ['success', 'message', 'data']);
  if (parsed.success !== true || !('data' in parsed)) throw new ModelsContractError();
  if (parsed.message !== undefined) safeText(parsed.message, 4_096, true);
  return parsed;
}

export function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new ModelsContractError();
  }
  return value as number;
}

export function finite(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new ModelsContractError();
  }
  return value;
}

export function safeText(value: unknown, maximum: number, allowNewlines = false): string {
  if (typeof value !== 'string' || new TextEncoder().encode(value).byteLength > maximum) throw new ModelsContractError();
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    const permittedWhitespace = allowNewlines && (code === 0x09 || code === 0x0a || code === 0x0d);
    if (!permittedWhitespace && (code <= 0x1f || (code >= 0x7f && code <= 0x9f))) throw new ModelsContractError();
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) throw new ModelsContractError();
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      throw new ModelsContractError();
    }
  }
  return value;
}

export function optionalText(value: unknown, maximum: number, allowNewlines = false): string {
  return value === undefined || value === null ? '' : safeText(value, maximum, allowNewlines);
}

export function binary(value: unknown): 0 | 1 {
  const parsed = integer(value, 0, 1);
  return parsed as 0 | 1;
}

export function boolean(value: unknown): boolean {
  if (typeof value !== 'boolean') throw new ModelsContractError();
  return value;
}

export function boundedArray(value: unknown, maximum = MAX_RELATED_ITEMS): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) throw new ModelsContractError();
  return value;
}

export function optionalArray(value: unknown, maximum = MAX_RELATED_ITEMS): unknown[] {
  return value === undefined || value === null ? [] : boundedArray(value, maximum);
}

export function optionalStringArray(value: unknown, maximumLength = 512): string[] {
  if (value === undefined || value === null) return [];
  const parsed = boundedArray(value).map((item) => safeText(item, maximumLength));
  if (new Set(parsed).size !== parsed.length) throw new ModelsContractError();
  return parsed;
}

export function validID(value: number): number {
  return integer(value, 1, MAX_ID);
}

export function validIdentifier(value: unknown): string {
  const parsed = safeText(value, 128);
  if (!IDENTIFIER.test(parsed)) throw new ModelsContractError();
  return parsed;
}

export function trimInput(value: string, maximum: number, required = false): string {
  if (typeof value !== 'string') throw new ModelsContractError();
  const parsed = safeText(value.trim(), maximum);
  if (required && parsed === '') throw new ModelsContractError();
  return parsed;
}

export function trimInputRunes(value: string, maximumRunes: number, maximumBytes: number, required = false): string {
  const parsed = trimInput(value, maximumBytes, required);
  if ([...parsed].length > maximumRunes) throw new ModelsContractError();
  return parsed;
}

export const responseLimits = { maxContentLength: MAX_RESPONSE_BYTES, maxBodyLength: MAX_RESPONSE_BYTES };

export async function responseData(request: Promise<{ data: unknown }>): Promise<unknown> {
  try {
    return (await request).data;
  } catch {
    throw new ModelsContractError();
  }
}

export function assertModelsAdministrator(role: number): void {
  if (role !== 10 && role !== 100) throw new ModelsAccessError();
}
