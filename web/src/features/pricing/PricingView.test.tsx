// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadPerformanceMetrics, loadPerformanceSummary } from '../performance-metrics/performance-api';
import {
  PERFORMANCE_SERIES_SCHEMA,
  type PerformanceMetrics,
} from '../performance-metrics/performance-metrics';
import type { PricingCatalog, PricingCatalogItem } from './catalog';
import { loadPricingCatalog } from './pricing-api';
import { PricingView } from './PricingView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});
vi.mock('../public-documents/PublicNotice', () => ({ PublicNotice: () => null }));
vi.mock('./pricing-api', () => ({
  loadPricingCatalog: vi.fn(),
}));
vi.mock('../performance-metrics/performance-api', () => ({
  loadPerformanceMetrics: vi.fn(),
  loadPerformanceSummary: vi.fn(),
}));

const mockedCatalog = vi.mocked(loadPricingCatalog);
const mockedPerformance = vi.mocked(loadPerformanceMetrics);
const mockedSummary = vi.mocked(loadPerformanceSummary);

function item(name: string, overrides: Partial<PricingCatalogItem> = {}): PricingCatalogItem {
  return {
    model_name: name, description: `${name} description`, tags: 'chat', vendor_id: 1, quota_type: 0,
    model_ratio: 1, model_price: 0, prompt_price: 1, completion_price: 3, owner_by: 'custom',
    completion_ratio: 3, enable_groups: ['default'], supported_endpoint_types: ['openai'],
    ...overrides,
  };
}

function catalog(items: PricingCatalogItem[] = [
  item('alpha/model', { prompt_price: 0.25, completion_price: 0.75, enable_groups: ['default', 'vip'] }),
  item('image-model', {
    vendor_id: 0, owner_by: 'community', tags: 'art', enable_groups: ['vip'],
    supported_endpoint_types: ['image-generation'], prompt_price: 2, completion_price: 4,
  }),
  item('zeta-model'),
]): PricingCatalog {
  return {
    items,
    vendors: [{ id: 1, name: 'Acme' }],
    groupRatio: { default: 1, vip: 0.5 },
    usableGroup: { default: 'Default', vip: 'VIP' },
    supportedEndpoint: {
      openai: { path: '/v1/chat/completions', method: 'POST' },
      'image-generation': { path: '/v1/images/generations/{model}', method: 'POST' },
    },
    autoGroups: ['default'], version: 'a'.repeat(64),
  };
}

function performance(modelName = 'alpha/model'): PerformanceMetrics {
  return {
    model_name: modelName,
    series_schema: PERFORMANCE_SERIES_SCHEMA,
    groups: [{
      group: 'vip', avg_ttft_ms: 45, avg_latency_ms: 180, success_rate: 99.75, avg_tps: 32.5,
      series: [{ ts: 1_700_000_000, avg_ttft_ms: 45, avg_latency_ms: 180, success_rate: 99.75, avg_tps: 32.5 }],
    }],
  };
}

beforeEach(() => {
  window.history.replaceState(null, '', '/pricing');
  mockedCatalog.mockResolvedValue(catalog());
  mockedPerformance.mockResolvedValue(performance());
  mockedSummary.mockResolvedValue({ models: [{
    model_name: 'alpha/model', avg_latency_ms: 180, success_rate: 99.75, avg_tps: 32.5,
    recent_success_rates: [99.5, 100],
  }] });
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  window.history.replaceState(null, '', '/');
});

describe('PricingView catalog', () => {
  it('filters by search, group, vendor, quota, endpoint, and tag and switches sorted card/table views', async () => {
    render(<PricingView />);
    expect(await screen.findByRole('heading', { name: 'alpha/model' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'image-model' })).toBeTruthy();
    expect(screen.getByText('3 of 3 models')).toBeTruthy();
    expect(await screen.findByLabelText('Performance: Success 99.75%, Latency 180 ms')).toBeTruthy();
    expect(mockedSummary).toHaveBeenCalledWith(24, expect.any(AbortSignal));

    const user = userEvent.setup();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Access group' }), 'vip');
    expect(screen.queryByRole('heading', { name: 'zeta-model' })).toBeNull();
    expect(screen.getByText('2 of 3 models')).toBeTruthy();

    await user.selectOptions(screen.getByRole('combobox', { name: 'Owner' }), 'community');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Model pricing' }), 'token');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Modalities' }), 'image');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Type' }), 'image-generation');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Tag' }), 'art');
    expect(screen.getByRole('heading', { name: 'image-model' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'alpha/model' })).toBeNull();
    expect(window.location.search).toContain('group=vip');
    expect(window.location.search).toContain('vendor=community');
    expect(window.location.search).toContain('quotaType=token');
    expect(window.location.search).toContain('modality=image');
    expect(window.location.search).toContain('endpointType=image-generation');
    expect(window.location.search).toContain('tag=art');

    await user.click(screen.getByRole('button', { name: 'Table' }));
    const table = screen.getByRole('table', { name: 'Model pricing' });
    expect(within(table).getByText('image-model')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Table' }).getAttribute('aria-pressed')).toBe('true');

    await user.type(screen.getByRole('searchbox', { name: 'Search models' }), 'missing');
    expect(screen.getByText('No matching models.')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(screen.getByText('3 of 3 models')).toBeTruthy();
  });

  it('validates initial query state and paginates at a bounded 24 rows', async () => {
    const many = Array.from({ length: 25 }, (_, index) => item(`model-${String(index + 1).padStart(2, '0')}`, {
      enable_groups: ['vip'],
    }));
    mockedCatalog.mockResolvedValueOnce(catalog(many));
    window.history.replaceState(null, '', '/pricing?group=vip&view=table&page=2&ignored=secret');
    render(<PricingView />);

    const table = await screen.findByRole('table', { name: 'Model pricing' });
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    expect(within(table).getByText('model-25')).toBeTruthy();
    expect(screen.getByText('Page 2 of 2')).toBeTruthy();
    expect(window.location.search).toBe('?group=vip&view=table&page=2');

    await userEvent.setup().click(screen.getByRole('button', { name: 'Previous' }));
    await waitFor(() => expect(screen.getByText('Page 1 of 2')).toBeTruthy());
    expect(within(screen.getByRole('table', { name: 'Model pricing' })).getAllByRole('row')).toHaveLength(25);
  });

  it('sorts by displayed price and renders per-request group prices without token labels', async () => {
    const mixedCatalog = catalog([
      item('expensive-token', { prompt_price: 9 }),
      item('request-model', { quota_type: 1, model_price: 0.2, prompt_price: 0, completion_price: 0 }),
      item('cheap-token', { prompt_price: 1 }),
    ]);
    mockedCatalog.mockResolvedValueOnce(mixedCatalog);
    const user = userEvent.setup();
    render(<PricingView />);
    await screen.findByRole('heading', { name: 'expensive-token' });
    await user.selectOptions(screen.getByRole('combobox', { name: 'Sort by' }), 'price-low');
    const cards = screen.getAllByRole('listitem');
    expect(cards.map((card) => within(card).getByRole('heading').textContent)).toEqual([
      'request-model', 'cheap-token', 'expensive-token',
    ]);

    cleanup();
    window.history.replaceState(null, '', '/pricing/request-model');
    mockedCatalog.mockResolvedValueOnce(mixedCatalog);
    render(<PricingView selectedModel="request-model" />);
    const groups = await screen.findByRole('table', { name: 'Available groups' });
    expect(within(groups).getByRole('columnheader', { name: 'Per request' })).toBeTruthy();
    expect(within(groups).queryByRole('columnheader', { name: 'Input / 1M' })).toBeNull();
    expect(within(groups).getByText('$0.20')).toBeTruthy();
  });

  it('shows retryable redacted errors and aborts the catalog request on unmount', async () => {
    mockedCatalog.mockRejectedValueOnce(new Error('private catalog failure'));
    render(<PricingView />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load pricing.');
    expect(screen.queryByText('private catalog failure')).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('heading', { name: 'alpha/model' })).toBeTruthy();

    cleanup();
    let signal: AbortSignal | undefined;
    mockedCatalog.mockImplementationOnce((requestSignal) => {
      signal = requestSignal;
      return new Promise(() => {});
    });
    const rendered = render(<PricingView />);
    rendered.unmount();
    expect(signal?.aborted).toBe(true);
  });
});

describe('PricingView model detail', () => {
  it('renders exact prices, group adjustments, routes, and per-group performance', async () => {
    window.history.replaceState(null, '', '/pricing/alpha%2Fmodel?group=vip');
    render(<PricingView selectedModel="alpha/model" />);
    expect(await screen.findByRole('heading', { name: 'alpha/model' })).toBeTruthy();
    expect(screen.getByText('Showing the VIP group adjustment (×0.5).')).toBeTruthy();
    expect(screen.getAllByText('$0.125')).toHaveLength(2);
    expect(screen.getAllByText('$0.375')).toHaveLength(2);
    expect(screen.getByRole('heading', { name: 'Supported API routes' })).toBeTruthy();
    expect(screen.getByText('/v1/chat/completions')).toBeTruthy();
    expect((await screen.findAllByText('99.75%')).length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText('32.5 tokens/s').length).toBeGreaterThanOrEqual(2);
    expect(screen.getByRole('table', { name: 'Performance (24h)' })).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Success · vip · 24h: 99.75%' })).toBeTruthy();
    expect(screen.queryByRole('columnheader', { name: 'Samples' })).toBeNull();
    expect(mockedPerformance).toHaveBeenCalledWith({
      modelName: 'alpha/model', hours: 24, signal: expect.any(AbortSignal),
    });
    expect(mockedSummary).not.toHaveBeenCalled();
  });

  it('handles invalid/missing models without issuing a performance request', async () => {
    const rendered = render(<PricingView selectedModel=" bad " />);
    expect((await screen.findByRole('alert')).textContent).toBe('Invalid model address.');
    expect(mockedPerformance).not.toHaveBeenCalled();
    rendered.unmount();

    render(<PricingView selectedModel="missing" />);
    expect(await screen.findByRole('heading', { name: 'Model not found' })).toBeTruthy();
    expect(mockedPerformance).not.toHaveBeenCalled();
  });

  it('does not apply a globally valid group to a model that does not advertise it', async () => {
    window.history.replaceState(null, '', '/pricing/zeta-model?group=vip');
    render(<PricingView selectedModel="zeta-model" />);
    expect(await screen.findByRole('heading', { name: 'zeta-model' })).toBeTruthy();
    expect(screen.queryByText(/Showing the VIP group adjustment/)).toBeNull();
    expect(screen.getAllByText('$1.00')).toHaveLength(2);
    expect(screen.getAllByText('$3.00')).toHaveLength(2);
  });

  it('shows retryable and empty performance states without reflecting provider errors', async () => {
    mockedPerformance
      .mockRejectedValueOnce(new Error('metrics database password'))
      .mockResolvedValueOnce(performance());
    render(<PricingView selectedModel="alpha/model" />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load model performance.');
    expect(document.body.textContent).not.toContain('metrics database password');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('table', { name: 'Performance (24h)' })).toBeTruthy();

    cleanup();
    mockedPerformance.mockResolvedValueOnce({ ...performance(), groups: [] });
    render(<PricingView selectedModel="alpha/model" />);
    expect(await screen.findByText('Performance data is not yet available for this model.')).toBeTruthy();
  });

  it('explains tiered expressions without presenting flat prices as dynamic charges', async () => {
    const expression = 'v1:len <= 200000 ? tier("base", p * 2 + c * 8) : tier("long", p * 4 + c * 12)';
    mockedCatalog.mockResolvedValueOnce(catalog([
      item('dynamic-model', { billing_mode: 'tiered_expr', billing_expr: expression }),
    ]));
    mockedPerformance.mockResolvedValueOnce({ ...performance('dynamic-model'), groups: [] });
    render(<PricingView selectedModel="dynamic-model" />);

    expect(await screen.findByText(expression)).toBeTruthy();
    expect(screen.getByText('tiered_expr')).toBeTruthy();
    const groups = screen.getByRole('table', { name: 'Available groups' });
    expect(within(groups).getAllByRole('columnheader').map((cell) => cell.textContent)).toEqual(['Group', 'Adjustment']);
    expect(document.body.textContent).not.toContain('$1.00');
    expect(document.body.textContent).not.toContain('$3.00');
  });

  it('aborts stale performance requests when the selected model changes', async () => {
    mockedCatalog.mockResolvedValueOnce(catalog([
      item('alpha/model'), item('other-model'),
    ]));
    let firstSignal: AbortSignal | undefined;
    let resolveFirst: ((value: PerformanceMetrics) => void) | undefined;
    let resolveSecond: ((value: PerformanceMetrics) => void) | undefined;
    mockedPerformance
      .mockImplementationOnce((request) => {
        firstSignal = request.signal;
        return new Promise((resolve) => { resolveFirst = resolve; });
      })
      .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve; }));

    const rendered = render(<PricingView selectedModel="alpha/model" />);
    expect(await screen.findByRole('heading', { name: 'alpha/model' })).toBeTruthy();
    rendered.rerender(<PricingView selectedModel="other-model" />);
    expect(await screen.findByRole('heading', { name: 'other-model' })).toBeTruthy();
    expect(firstSignal?.aborted).toBe(true);

    await act(async () => {
      resolveFirst?.(performance('alpha/model'));
      resolveSecond?.(performance('other-model'));
      await Promise.resolve();
    });
    expect((await screen.findAllByText('99.75%')).length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText('alpha/model')).toBeNull();
  });
});
