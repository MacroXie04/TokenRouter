import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import { loadPricingCatalog } from './pricing-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn() } }));

const mockedAPI = vi.mocked(api);

function catalogResponse() {
  return {
    success: true,
    data: [{
      model_name: 'alpha-model', vendor_id: 0, quota_type: 0, model_ratio: 1, model_price: 0,
      prompt_price: 1, completion_price: 3, owner_by: 'custom', completion_ratio: 3,
      enable_groups: ['default'], supported_endpoint_types: ['openai'],
    }],
    vendors: [],
    group_ratio: { default: 1 },
    usable_group: { default: 'Default' },
    supported_endpoint: { openai: { path: '/v1/chat/completions', method: 'POST' } },
    auto_groups: ['default'],
    pricing_version: 'a'.repeat(64),
  };
}

beforeEach(() => vi.resetAllMocks());

describe('pricing API requests', () => {
  it('uses the exact catalog route with transport bounds and cancellation', async () => {
    mockedAPI.get.mockResolvedValueOnce({ data: catalogResponse() });
    const controller = new AbortController();
    await expect(loadPricingCatalog(controller.signal)).resolves.toMatchObject({
      items: [{ model_name: 'alpha-model' }], version: 'a'.repeat(64),
    });
    expect(mockedAPI.get).toHaveBeenCalledWith('/pricing', {
      signal: controller.signal,
      maxContentLength: 8 * 1024 * 1024,
      maxBodyLength: 8 * 1024 * 1024,
    });
  });
});
