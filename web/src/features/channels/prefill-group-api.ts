export interface PrefillGroup {
  id: number;
  name: string;
  type: string;
  items: unknown;
  description?: string;
  created_time: number;
  updated_time: number;
}

export const MAX_PREFILL_GROUPS = 1_000;
export const MAX_PREFILL_ITEMS = 10_000;
export const MAX_PREFILL_ITEM_BYTES = 512;
export const MAX_PREFILL_JSON_BYTES = 64 * 1024;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function boundedInteger(value: unknown, minimum = 0): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum;
}

export function parsePrefillGroups(value: unknown): PrefillGroup[] {
  if (!Array.isArray(value) || value.length > MAX_PREFILL_GROUPS) {
    throw new Error('invalid prefill group response');
  }
  return value.map((candidate) => {
    if (!isRecord(candidate) || !boundedInteger(candidate.id, 1) ||
        typeof candidate.name !== 'string' || candidate.name.length === 0 || candidate.name.length > 64 ||
        (candidate.type !== 'model' && candidate.type !== 'tag' && candidate.type !== 'endpoint') ||
        (candidate.description !== undefined && (typeof candidate.description !== 'string' || candidate.description.length > 255)) ||
        !boundedInteger(candidate.created_time) || !boundedInteger(candidate.updated_time)) {
      throw new Error('invalid prefill group response');
    }
    const encodedItems = JSON.stringify(candidate.items ?? null);
    if (encodedItems === undefined || encodedItems.length > MAX_PREFILL_JSON_BYTES ||
        (Array.isArray(candidate.items) && (candidate.items.length > MAX_PREFILL_ITEMS || candidate.items.some((item) => typeof item !== 'string' || item.length > MAX_PREFILL_ITEM_BYTES)))) {
      throw new Error('invalid prefill group response');
    }
    return candidate as unknown as PrefillGroup;
  });
}
