import { describe, expect, it } from 'vitest';
import {
  filterPricingItems,
  itemModalities,
  parsePricingCatalogResponse,
  parsePricingQuery,
  PricingContractError,
  pricingQueryString,
  type PricingCatalogItem,
} from './catalog';

const VERSION = 'a'.repeat(64);

function item(overrides: Partial<Record<keyof PricingCatalogItem, unknown>> = {}) {
  return {
    model_name: 'alpha-model',
    description: 'Fast text model',
    tags: 'chat,reasoning',
    vendor_id: 7,
    quota_type: 0,
    model_ratio: 2,
    model_price: 0,
    prompt_price: 2,
    completion_price: 8,
    owner_by: 'custom',
    completion_ratio: 4,
    enable_groups: ['default', 'vip'],
    supported_endpoint_types: ['openai'],
    ignored_secret: 'redacted',
    ...overrides,
  };
}

function response() {
  return {
    success: true,
    message: '',
    data: [item()],
    vendors: [{ id: 7, name: 'Acme', description: 'Model publisher', icon: 'unsafe-not-rendered' }],
    group_ratio: { default: 1, vip: 0.5 },
    usable_group: { default: 'Default', vip: 'VIP' },
    supported_endpoint: { openai: { path: '/v1/chat/completions', method: 'POST' } },
    auto_groups: ['default'],
    pricing_version: VERSION,
    internal: 'not exposed',
  };
}

describe('pricing catalog contract', () => {
  it('parses the exact rich catalog and exposes only display-safe fields', () => {
    const parsed = parsePricingCatalogResponse(response());
    expect(parsed.items).toEqual([{
      model_name: 'alpha-model',
      description: 'Fast text model',
      tags: 'chat,reasoning',
      vendor_id: 7,
      quota_type: 0,
      model_ratio: 2,
      model_price: 0,
      prompt_price: 2,
      completion_price: 8,
      owner_by: 'custom',
      completion_ratio: 4,
      enable_groups: ['default', 'vip'],
      supported_endpoint_types: ['openai'],
    }]);
    expect(parsed.vendors[0]).toEqual({ id: 7, name: 'Acme', description: 'Model publisher' });
    expect(parsed.supportedEndpoint.openai).toEqual({ path: '/v1/chat/completions', method: 'POST' });
    expect(parsed).not.toHaveProperty('internal');
  });

  it('accepts only a paired, bounded tiered billing mode and expression', () => {
    const dynamic = response();
    dynamic.data[0] = item({
      billing_mode: 'tiered_expr',
      billing_expr: 'v1:len <= 200000 ? tier("base", p * 2 + c * 8) : tier("long", p * 4 + c * 12)',
    });
    expect(parsePricingCatalogResponse(dynamic).items[0]).toMatchObject({
      billing_mode: 'tiered_expr',
      billing_expr: 'v1:len <= 200000 ? tier("base", p * 2 + c * 8) : tier("long", p * 4 + c * 12)',
    });

    const missingExpression = response();
    missingExpression.data[0] = item({ billing_mode: 'tiered_expr' });
    expect(() => parsePricingCatalogResponse(missingExpression)).toThrow(PricingContractError);

    const orphanExpression = response();
    orphanExpression.data[0] = item({ billing_expr: 'p * 2' });
    expect(() => parsePricingCatalogResponse(orphanExpression)).toThrow(PricingContractError);

    const unsupportedMode = response();
    unsupportedMode.data[0] = item({ billing_mode: 'operator-only', billing_expr: 'p * 2' });
    expect(() => parsePricingCatalogResponse(unsupportedMode)).toThrow(PricingContractError);

    const oversizedExpression = response();
    oversizedExpression.data[0] = item({ billing_mode: 'tiered_expr', billing_expr: `p * 2 + ${'x'.repeat(64 * 1024)}` });
    expect(() => parsePricingCatalogResponse(oversizedExpression)).toThrow(PricingContractError);
  });

  it('fails closed on duplicate, inconsistent, malformed, and oversized catalogs', () => {
    const duplicate = response();
    duplicate.data.push(item());
    expect(() => parsePricingCatalogResponse(duplicate)).toThrow(PricingContractError);

    const missingGroup = response();
    missingGroup.data[0].enable_groups = ['private'];
    expect(() => parsePricingCatalogResponse(missingGroup)).toThrow(PricingContractError);

    const unsafeEndpoint = response();
    unsafeEndpoint.supported_endpoint.openai.path = 'https://attacker.example/collect';
    expect(() => parsePricingCatalogResponse(unsafeEndpoint)).toThrow(PricingContractError);

    const invalidPrice = response();
    invalidPrice.data[0].prompt_price = Number.POSITIVE_INFINITY;
    expect(() => parsePricingCatalogResponse(invalidPrice)).toThrow(PricingContractError);

    const overflowingAdjustment = response();
    overflowingAdjustment.data[0].prompt_price = Number.MAX_VALUE;
    overflowingAdjustment.group_ratio.vip = Number.MAX_VALUE;
    expect(() => parsePricingCatalogResponse(overflowingAdjustment)).toThrow(PricingContractError);

    const paddedName = response();
    paddedName.data[0].model_name = ' alpha-model ';
    expect(() => parsePricingCatalogResponse(paddedName)).toThrow(PricingContractError);

    const multilineName = response();
    multilineName.data[0].model_name = 'alpha\nmodel';
    expect(() => parsePricingCatalogResponse(multilineName)).toThrow(PricingContractError);

    const multilineEndpoint = response();
    multilineEndpoint.supported_endpoint.openai.path = '/v1/chat\n/collect';
    expect(() => parsePricingCatalogResponse(multilineEndpoint)).toThrow(PricingContractError);

    const oversized = response() as Record<string, unknown>;
    oversized.unused = 'x'.repeat(8 * 1024 * 1024);
    expect(() => parsePricingCatalogResponse(oversized)).toThrow(PricingContractError);
  });
});

describe('pricing query and filtering', () => {
  it('accepts only bounded known query values and serializes canonical state', () => {
    const parsed = parsePricingQuery('?search=alpha&sort=price-low&vendor=Acme&group=vip&quotaType=request&modality=image&endpointType=image-generation&tag=art&view=table&page=3&ignored=1');
    expect(parsed).toEqual({
      search: 'alpha', sort: 'price-low', vendor: 'Acme', group: 'vip', quotaType: 'request',
      modality: 'image', endpointType: 'image-generation', tag: 'art', view: 'table', page: 3,
    });
    expect(pricingQueryString(parsed)).toBe('?search=alpha&sort=price-low&vendor=Acme&group=vip&quotaType=request&modality=image&endpointType=image-generation&tag=art&view=table&page=3');
    expect(pricingQueryString({
      ...parsed,
      search: '界'.repeat(70),
      page: Number.POSITIVE_INFINITY,
      vendor: 'unsafe\u202evalue',
    })).toBe('?sort=price-low&group=vip&quotaType=request&modality=image&endpointType=image-generation&tag=art&view=table');

    expect(parsePricingQuery('?search=one&search=two&sort=secret&quotaType=other&modality=smell&view=tiles&page=-1')).toEqual({
      search: '', sort: 'name', vendor: '', group: '', quotaType: '', modality: '', endpointType: '', tag: '', view: 'card', page: 1,
    });
    expect(parsePricingQuery(`?search=${'x'.repeat(4_096)}`)).toEqual({
      search: '', sort: 'name', vendor: '', group: '', quotaType: '', modality: '', endpointType: '', tag: '', view: 'card', page: 1,
    });
    expect(parsePricingQuery('?search=bad%C2%80value').search).toBe('');
    expect(parsePricingQuery(`?search=${encodeURIComponent('界'.repeat(70))}`).search).toBe('');
  });

  it('derives conservative modalities from advertised endpoints and combines all filters', () => {
    const catalog = parsePricingCatalogResponse({
      ...response(),
      data: [
        item(),
        item({
          model_name: 'image-model', description: 'Creates art', vendor_id: 0, owner_by: 'community',
          enable_groups: ['vip'], supported_endpoint_types: ['image-generation'],
        }),
      ],
      supported_endpoint: {
        openai: { path: '/v1/chat/completions', method: 'POST' },
        'image-generation': { path: '/v1/images/generations', method: 'POST' },
      },
    });
    expect(itemModalities(catalog.items[0])).toEqual(['text']);
    expect(itemModalities(catalog.items[1])).toEqual(['image']);
    const filtered = filterPricingItems(catalog.items, catalog.vendors, {
      search: 'art', sort: 'name', vendor: 'community', group: 'vip', quotaType: 'token',
      modality: 'image', endpointType: 'image-generation', tag: 'reasoning', view: 'card', page: 1,
    });
    expect(filtered.map((entry) => entry.model_name)).toEqual(['image-model']);

    const sorted = filterPricingItems([
      { ...catalog.items[0], prompt_price: 8 },
      { ...catalog.items[1], prompt_price: 2 },
    ], catalog.vendors, {
      search: '', sort: 'price-low', vendor: '', group: '', quotaType: '', modality: '', endpointType: '', tag: '', view: 'card', page: 1,
    });
    expect(sorted.map((entry) => entry.model_name)).toEqual(['image-model', 'alpha-model']);

    const withDynamic = filterPricingItems([
      catalog.items[0],
      { ...catalog.items[1], billing_mode: 'tiered_expr', billing_expr: 'p * 2' },
    ], catalog.vendors, {
      search: '', sort: 'price-high', vendor: '', group: '', quotaType: '', modality: '', endpointType: '', tag: '', view: 'card', page: 1,
    });
    expect(withDynamic.map((entry) => entry.model_name)).toEqual(['alpha-model', 'image-model']);
  });
});
