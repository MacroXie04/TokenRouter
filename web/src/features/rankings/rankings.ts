export const MAX_RANKING_ROWS = 1_000;

export interface RankingRow {
  model_name: string;
  count: number;
  quota: number;
}

function isSafeCount(value: unknown): value is number {
  return typeof value === 'number'
    && Number.isSafeInteger(value)
    && value >= 0;
}

export function parseRankingRows(value: unknown): RankingRow[] {
  if (!Array.isArray(value) || value.length > MAX_RANKING_ROWS) {
    throw new Error('invalid rankings payload');
  }
  const seen = new Set<string>();
  return value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      throw new Error('invalid ranking row');
    }
    const row = entry as Partial<RankingRow>;
    const modelName = typeof row.model_name === 'string' ? row.model_name.trim() : '';
    if (
      !modelName
      || modelName !== row.model_name
      || modelName.length > 512
      || seen.has(modelName)
      || !isSafeCount(row.count)
      || !isSafeCount(row.quota)
    ) {
      throw new Error('invalid ranking row');
    }
    seen.add(modelName);
    return { model_name: modelName, count: row.count, quota: row.quota };
  });
}
