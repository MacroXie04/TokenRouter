import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  loadRankings,
  parseRankingsResponse,
  RankingsContractError,
} from './rankings-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn() } }));

const mockedAPI = vi.mocked(api);

function response() {
  return {
    success: true,
    message: '',
    data: {
      models: [{
        rank: 1,
        previous_rank: 2,
        model_name: 'alpha-model',
        vendor: 'Acme',
        vendor_icon: 'acme',
        category: 'all',
        total_tokens: 1_200,
        share: 0.75,
        growth_pct: 25,
        ignored_private_field: 'not exposed',
      }],
      vendors: [{
        rank: 1,
        vendor: 'Acme',
        vendor_icon: 'acme',
        total_tokens: 1_200,
        share: 0.75,
        growth_pct: 25,
        models_count: 1,
        top_model: 'alpha-model',
      }],
      top_movers: [{
        model_name: 'alpha-model', vendor: 'Acme', vendor_icon: 'acme',
        rank_delta: 1, current_rank: 1, growth_pct: 25,
      }],
      top_droppers: [{
        model_name: 'beta-model', vendor: 'Bee',
        rank_delta: -2, current_rank: 4, growth_pct: -10,
      }],
      models_history: {
        points: [{
          ts: '2026-09-01T00:00:00Z', label: 'Sep 1', model: 'alpha-model',
          vendor: 'Acme', tokens: 1_200,
        }],
        models: [{ name: 'alpha-model', vendor: 'Acme', total: 1_200 }],
        buckets: 1,
      },
      vendor_share_history: {
        points: [{
          ts: '2026-09-01T00:00:00Z', label: 'Sep 1', vendor: 'Acme',
          share: 0.75, tokens: 1_200,
        }],
        vendors: [{ name: 'Acme', total: 1_200, share: 0.75 }],
        buckets: 1,
      },
    },
  };
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('rankings response contract', () => {
  it('accepts the bounded reference snapshot and exposes only rendered fields', () => {
    const parsed = parseRankingsResponse(response());
    expect(parsed.models[0]).toEqual({
      rank: 1,
      previous_rank: 2,
      model_name: 'alpha-model',
      vendor: 'Acme',
      vendor_icon: 'acme',
      category: 'all',
      total_tokens: 1_200,
      share: 0.75,
      growth_pct: 25,
    });
    expect(parsed.top_movers[0].rank_delta).toBe(1);
    expect(parsed.top_droppers[0].rank_delta).toBe(-2);
    expect(parsed.models_history.buckets).toBe(1);
    expect(parsed.vendor_share_history.buckets).toBe(1);
  });

  it('fails closed on malformed, inconsistent, duplicate, and unsafe values', () => {
    const malformed = response();
    malformed.data.models[0].total_tokens = -1;
    expect(() => parseRankingsResponse(malformed)).toThrow(RankingsContractError);

    const wrongRank = response();
    wrongRank.data.models[0].rank = 2;
    expect(() => parseRankingsResponse(wrongRank)).toThrow(RankingsContractError);

    const wrongDirection = response();
    wrongDirection.data.top_movers[0].rank_delta = -1;
    expect(() => parseRankingsResponse(wrongDirection)).toThrow(RankingsContractError);

    const unknownHistoryModel = response();
    unknownHistoryModel.data.models_history.points[0].model = 'unlisted-model';
    expect(() => parseRankingsResponse(unknownHistoryModel)).toThrow(RankingsContractError);

    const excessiveShare = response();
    excessiveShare.data.models[0].share = 1.1;
    expect(() => parseRankingsResponse(excessiveShare)).toThrow(RankingsContractError);

    expect(() => parseRankingsResponse({ success: false, data: response().data })).toThrow(RankingsContractError);
  });

  it('rejects oversized payloads before inspecting unused fields', () => {
    const oversized = response() as Record<string, unknown>;
    oversized.unused = 'x'.repeat(768 * 1024);
    expect(() => parseRankingsResponse(oversized)).toThrow(RankingsContractError);

    const oversizedUtf8 = response() as Record<string, unknown>;
    oversizedUtf8.unused = '界'.repeat(300_000);
    expect(() => parseRankingsResponse(oversizedUtf8)).toThrow(RankingsContractError);
  });
});

describe('rankings API route', () => {
  it('sends the exact period with transport bounds and cancellation', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: response() });
    const controller = new AbortController();

    await expect(loadRankings('month', controller.signal)).resolves.toMatchObject({
      models: [{ model_name: 'alpha-model' }],
    });
    expect(mockedAPI.get).toHaveBeenCalledWith('/rankings', {
      params: { period: 'month' },
      signal: controller.signal,
      maxContentLength: 786_432,
      maxBodyLength: 786_432,
    });
  });

  it('rejects an invalid runtime period without issuing a request', async () => {
    await expect(loadRankings('forever' as 'week')).rejects.toBeInstanceOf(RankingsContractError);
    expect(mockedAPI.get).not.toHaveBeenCalled();
  });
});
