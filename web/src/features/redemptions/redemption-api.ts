import { api } from '../../api';

export const REDEMPTION_PAGE_SIZE = 20;
export const REDEMPTION_STATUS_ENABLED = 1;
export const REDEMPTION_STATUS_DISABLED = 2;
export const REDEMPTION_STATUS_USED = 3;

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_REDEMPTIONS = 100;
const MAX_TOTAL = 10_000_000;
const MAX_PAGE = 1_000_000;
const MAX_ID = 2_147_483_647;
const MAX_QUOTA = 2_147_483_647;
const MAX_TIMESTAMP = 253_402_300_799;
const REQUEST_TIMEOUT_MS = 15_000;
const REDEMPTION_KEYS = new Set([
  'id',
  'user_id',
  'name',
  'key',
  'status',
  'quota',
  'created_time',
  'redeemed_time',
  'expired_time',
  'used_user_id',
]);

export type RedemptionStatus =
  | typeof REDEMPTION_STATUS_ENABLED
  | typeof REDEMPTION_STATUS_DISABLED
  | typeof REDEMPTION_STATUS_USED;
export type RedemptionStatusFilter = '' | '1' | '2' | '3' | 'expired';

export interface ManagedRedemption {
  id: number;
  userId: number;
  name: string;
  maskedCode: string;
  status: RedemptionStatus;
  quota: number;
  createdTime: number;
  redeemedTime: number;
  expiredTime: number;
  usedUserId: number;
}

export interface RedemptionPage {
  items: ManagedRedemption[];
  total: number;
  page: number;
  pageSize: number;
}

export interface RedemptionQuery {
  keyword: string;
  status: RedemptionStatusFilter;
  page: number;
  pageSize: number;
}

export interface RedemptionInput {
  name: string;
  quota: number;
  expiredTime: number;
}

export interface CreateRedemptionsInput extends RedemptionInput {
  count: number;
}

type UnknownRecord = Record<string, unknown>;

export class RedemptionContractError extends Error {
  constructor() {
    super('Invalid redemption API contract');
    this.name = 'RedemptionContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new RedemptionContractError();
  }
  return value as UnknownRecord;
}

function exactKeys(value: UnknownRecord, allowed: Set<string>): void {
  if (Object.keys(value).some((key) => !allowed.has(key))) throw new RedemptionContractError();
}

function boundedPayload(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new RedemptionContractError();
  }
  if (new TextEncoder().encode(encoded).byteLength > MAX_RESPONSE_BYTES) {
    throw new RedemptionContractError();
  }
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new RedemptionContractError();
  }
  return value as number;
}

function text(value: unknown, maximumCodePoints: number, minimumCodePoints = 0): string {
  if (typeof value !== 'string' || value.includes('\0')) throw new RedemptionContractError();
  const size = [...value].length;
  if (size < minimumCodePoints || size > maximumCodePoints) throw new RedemptionContractError();
  return value;
}

function status(value: unknown): RedemptionStatus {
  const parsed = integer(value, REDEMPTION_STATUS_ENABLED, REDEMPTION_STATUS_USED);
  if (
    parsed !== REDEMPTION_STATUS_ENABLED
    && parsed !== REDEMPTION_STATUS_DISABLED
    && parsed !== REDEMPTION_STATUS_USED
  ) throw new RedemptionContractError();
  return parsed;
}

function code(value: unknown): string {
  const parsed = text(value, 32, 32);
  if (!/^[a-f\d]{32}$/i.test(parsed)) throw new RedemptionContractError();
  return parsed;
}

function maskCode(value: string): string {
  return `${value.slice(0, 4)}${'•'.repeat(8)}${value.slice(-4)}`;
}

function successfulEnvelope(value: unknown, requireData = true): UnknownRecord {
  boundedPayload(value);
  const envelope = record(value);
  exactKeys(envelope, new Set(['success', 'message', 'data']));
  if (envelope.success !== true || (requireData && !Object.hasOwn(envelope, 'data'))) {
    throw new RedemptionContractError();
  }
  if (envelope.message !== undefined) text(envelope.message, 512);
  return envelope;
}

function parseRedemption(value: unknown): { item: ManagedRedemption; code: string } {
  const raw = record(value);
  exactKeys(raw, REDEMPTION_KEYS);
  const rawCode = code(raw.key);
  return {
    item: {
      id: integer(raw.id, 1, MAX_ID),
      userId: integer(raw.user_id, 1, MAX_ID),
      name: text(raw.name, 20, 1),
      maskedCode: maskCode(rawCode),
      status: status(raw.status),
      quota: integer(raw.quota, 1, MAX_QUOTA),
      createdTime: integer(raw.created_time, 0, MAX_TIMESTAMP),
      redeemedTime: integer(raw.redeemed_time, 0, MAX_TIMESTAMP),
      expiredTime: integer(raw.expired_time, 0, MAX_TIMESTAMP),
      usedUserId: integer(raw.used_user_id, 0, MAX_ID),
    },
    code: rawCode,
  };
}

export function parseRedemptionPageResponse(value: unknown): RedemptionPage {
  const envelope = successfulEnvelope(value);
  const data = record(envelope.data);
  exactKeys(data, new Set(['items', 'total', 'page', 'page_size']));
  if (!Array.isArray(data.items) || data.items.length > MAX_REDEMPTIONS) {
    throw new RedemptionContractError();
  }
  const pageSize = integer(data.page_size, 1, MAX_REDEMPTIONS);
  if (data.items.length > pageSize) throw new RedemptionContractError();
  return {
    items: data.items.map((item) => parseRedemption(item).item),
    total: integer(data.total, 0, MAX_TOTAL),
    page: integer(data.page, 1, MAX_PAGE),
    pageSize,
  };
}

export function parseCreatedCodesResponse(value: unknown): string[] {
  const envelope = successfulEnvelope(value);
  if (!Array.isArray(envelope.data) || envelope.data.length < 1 || envelope.data.length > 100) {
    throw new RedemptionContractError();
  }
  const codes = envelope.data.map(code);
  if (new Set(codes).size !== codes.length) throw new RedemptionContractError();
  return codes;
}

function parseUpdatedResponse(value: unknown, expectedId: number): ManagedRedemption {
  const item = parseRedemption(successfulEnvelope(value).data).item;
  if (item.id !== expectedId) throw new RedemptionContractError();
  return item;
}

function parseDeleteCountResponse(value: unknown): number {
  return integer(successfulEnvelope(value).data, 0, MAX_TOTAL);
}

function parseSuccessResponse(value: unknown): void {
  successfulEnvelope(value, false);
}

function requestOptions(signal?: AbortSignal) {
  return {
    signal,
    timeout: REQUEST_TIMEOUT_MS,
    maxContentLength: MAX_RESPONSE_BYTES,
    maxBodyLength: MAX_RESPONSE_BYTES,
  };
}

function validPage(value: number): number {
  return integer(value, 1, MAX_PAGE);
}

function validPageSize(value: number): number {
  return integer(value, 1, MAX_REDEMPTIONS);
}

function validId(value: number): number {
  return integer(value, 1, MAX_ID);
}

function validInput(input: RedemptionInput): RedemptionInput {
  const name = text(input.name.trim(), 20, 1);
  const quota = integer(input.quota, 1, MAX_QUOTA);
  const expiredTime = integer(input.expiredTime, 0, MAX_TIMESTAMP);
  if (expiredTime !== 0 && expiredTime <= Math.floor(Date.now() / 1000)) {
    throw new RedemptionContractError();
  }
  return { name, quota, expiredTime };
}

function validStatusFilter(value: RedemptionStatusFilter): RedemptionStatusFilter {
  if (value !== '' && value !== '1' && value !== '2' && value !== '3' && value !== 'expired') {
    throw new RedemptionContractError();
  }
  return value;
}

export async function listRedemptions(
  page = 1,
  pageSize = REDEMPTION_PAGE_SIZE,
  signal?: AbortSignal,
): Promise<RedemptionPage> {
  const response = await api.get<unknown>('/redemption/', {
    ...requestOptions(signal),
    params: { p: validPage(page), page_size: validPageSize(pageSize) },
  });
  return parseRedemptionPageResponse(response.data);
}

export async function searchRedemptions(
  query: RedemptionQuery,
  signal?: AbortSignal,
): Promise<RedemptionPage> {
  const keyword = text(query.keyword.trim(), 64);
  const selectedStatus = validStatusFilter(query.status);
  const params: Record<string, string | number> = {
    keyword,
    p: validPage(query.page),
    page_size: validPageSize(query.pageSize),
  };
  if (selectedStatus) params.status = selectedStatus;
  const response = await api.get<unknown>('/redemption/search', {
    ...requestOptions(signal),
    params,
  });
  return parseRedemptionPageResponse(response.data);
}

export async function getRedemptionForEdit(id: number, signal?: AbortSignal): Promise<ManagedRedemption> {
  const expectedId = validId(id);
  const response = await api.get<unknown>(`/redemption/${expectedId}`, requestOptions(signal));
  return parseUpdatedResponse(response.data, expectedId);
}

export async function getRedemptionCode(id: number): Promise<string> {
  const expectedId = validId(id);
  const response = await api.get<unknown>(`/redemption/${expectedId}`, requestOptions());
  const parsed = parseRedemption(successfulEnvelope(response.data).data);
  if (parsed.item.id !== expectedId) throw new RedemptionContractError();
  return parsed.code;
}

export async function createRedemptions(input: CreateRedemptionsInput): Promise<string[]> {
  const validated = validInput(input);
  const count = integer(input.count, 1, 100);
  const response = await api.post<unknown>('/redemption/', {
    name: validated.name,
    quota: validated.quota,
    expired_time: validated.expiredTime,
    count,
  }, requestOptions());
  const codes = parseCreatedCodesResponse(response.data);
  if (codes.length !== count) throw new RedemptionContractError();
  return codes;
}

export async function updateRedemption(id: number, input: RedemptionInput): Promise<ManagedRedemption> {
  const expectedId = validId(id);
  const validated = validInput(input);
  const response = await api.put<unknown>('/redemption/', {
    id: expectedId,
    name: validated.name,
    quota: validated.quota,
    expired_time: validated.expiredTime,
  }, requestOptions());
  return parseUpdatedResponse(response.data, expectedId);
}

export async function updateRedemptionStatus(
  id: number,
  nextStatus: typeof REDEMPTION_STATUS_ENABLED | typeof REDEMPTION_STATUS_DISABLED,
): Promise<ManagedRedemption> {
  const expectedId = validId(id);
  if (nextStatus !== REDEMPTION_STATUS_ENABLED && nextStatus !== REDEMPTION_STATUS_DISABLED) {
    throw new RedemptionContractError();
  }
  const response = await api.put<unknown>('/redemption/', {
    id: expectedId,
    status: nextStatus,
  }, {
    ...requestOptions(),
    params: { status_only: 'true' },
  });
  return parseUpdatedResponse(response.data, expectedId);
}

export async function deleteRedemption(id: number): Promise<void> {
  const response = await api.delete<unknown>(`/redemption/${validId(id)}`, requestOptions());
  parseSuccessResponse(response.data);
}

export async function deleteInvalidRedemptions(): Promise<number> {
  const response = await api.delete<unknown>('/redemption/invalid', requestOptions());
  return parseDeleteCountResponse(response.data);
}
