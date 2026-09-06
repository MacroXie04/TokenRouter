// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadPerformanceMetrics } from './performance-api';
import { PERFORMANCE_SERIES_SCHEMA, type PerformanceMetrics } from './performance-metrics';
import { PerformanceMetricsPanel, PerformanceSummaryBadge } from './PerformanceMetricsPanel';

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('./performance-api', () => ({ loadPerformanceMetrics: vi.fn() }));

const mockedMetrics = vi.mocked(loadPerformanceMetrics);

function metrics(modelName = 'alpha/model'): PerformanceMetrics {
  return {
    model_name: modelName,
    series_schema: PERFORMANCE_SERIES_SCHEMA,
    groups: [
      {
        group: 'default', avg_ttft_ms: 55, avg_latency_ms: 205, success_rate: 99.5, avg_tps: 41,
        series: [
          { ts: 1_700_000_000, avg_ttft_ms: 50, avg_latency_ms: 200, success_rate: 99, avg_tps: 40 },
          { ts: 1_700_003_600, avg_ttft_ms: 60, avg_latency_ms: 210, success_rate: 100, avg_tps: 42 },
        ],
      },
      {
        group: 'vip', avg_ttft_ms: 45, avg_latency_ms: 180, success_rate: 99.75, avg_tps: 32.5,
        series: [{ ts: 1_700_000_000, avg_ttft_ms: 45, avg_latency_ms: 180, success_rate: 99.75, avg_tps: 32.5 }],
      },
    ],
  };
}

beforeEach(() => mockedMetrics.mockResolvedValue(metrics()));

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe('PerformanceMetricsPanel', () => {
  it('renders reusable summary, group selection, all series, and safe group labels', async () => {
    render(<PerformanceMetricsPanel
      modelName="alpha/model"
      preferredGroup="vip"
      groupLabels={{ default: 'Default', vip: 'VIP' }}
    />);

    expect(await screen.findByRole('heading', { name: 'Performance (24h)' })).toBeTruthy();
    await waitFor(() => expect(
      (screen.getByRole('combobox', { name: 'Group' }) as HTMLSelectElement).value,
    ).toBe('vip'));
    expect(screen.getByRole('img', { name: 'TTFT · vip · 24h: 45 ms' })).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Latency · vip · 24h: 180 ms' })).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Success · vip · 24h: 99.75%' })).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Throughput · vip · 24h: 32.5 tokens/s' })).toBeTruthy();
    expect(within(screen.getByRole('table', { name: 'Performance (24h)' })).getByText('Default')).toBeTruthy();

    await userEvent.setup().selectOptions(screen.getByRole('combobox', { name: 'Group' }), 'default');
    expect(screen.getByRole('img', { name: 'TTFT · default · 24h: 60 ms' })).toBeTruthy();
    expect(mockedMetrics).toHaveBeenCalledWith({
      modelName: 'alpha/model', hours: 24, signal: expect.any(AbortSignal),
    });
  });

  it('redacts failures, retries, handles empty data, and aborts on unmount', async () => {
    mockedMetrics
      .mockRejectedValueOnce(new Error('private metrics database password'))
      .mockResolvedValueOnce(metrics());
    const rendered = render(<PerformanceMetricsPanel modelName="alpha/model" />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load model performance.');
    expect(document.body.textContent).not.toContain('private metrics database password');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('table', { name: 'Performance (24h)' })).toBeTruthy();
    rendered.unmount();

    let signal: AbortSignal | undefined;
    mockedMetrics.mockImplementationOnce((request) => {
      signal = request.signal;
      return new Promise(() => {});
    });
    const pending = render(<PerformanceMetricsPanel modelName="alpha/model" />);
    pending.unmount();
    expect(signal?.aborted).toBe(true);

    mockedMetrics.mockResolvedValueOnce({ ...metrics(), groups: [] });
    render(<PerformanceMetricsPanel modelName="alpha/model" />);
    expect(await screen.findByText('Performance data is not yet available for this model.')).toBeTruthy();
  });

  it('ignores a stale model response after switching models', async () => {
    let resolveFirst: ((value: PerformanceMetrics) => void) | undefined;
    let resolveSecond: ((value: PerformanceMetrics) => void) | undefined;
    let firstSignal: AbortSignal | undefined;
    mockedMetrics
      .mockImplementationOnce((request) => {
        firstSignal = request.signal;
        return new Promise((resolve) => { resolveFirst = resolve; });
      })
      .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve; }));

    const rendered = render(<PerformanceMetricsPanel modelName="first" />);
    rendered.rerender(<PerformanceMetricsPanel modelName="second" />);
    expect(firstSignal?.aborted).toBe(true);
    await act(async () => {
      resolveFirst?.(metrics('first'));
      resolveSecond?.(metrics('second'));
      await Promise.resolve();
    });
    expect(await screen.findByRole('table', { name: 'Performance (24h)' })).toBeTruthy();
    expect(mockedMetrics).toHaveBeenLastCalledWith({
      modelName: 'second', hours: 24, signal: expect.any(AbortSignal),
    });
  });
});

describe('PerformanceSummaryBadge', () => {
  it('renders a compact accessible summary and nothing for an absent metric', () => {
    const rendered = render(<PerformanceSummaryBadge summary={{
      model_name: 'alpha', avg_latency_ms: 125, success_rate: 99.5, avg_tps: 42,
      recent_success_rates: [99, 99.5, 100],
    }} />);
    expect(screen.getByLabelText('Performance: Success 99.5%, Latency 125 ms')).toBeTruthy();
    expect(document.querySelector('.performance-summary-sparkline polyline')).toBeTruthy();
    rendered.rerender(<PerformanceSummaryBadge />);
    expect(screen.queryByLabelText(/Performance:/)).toBeNull();
  });
});
