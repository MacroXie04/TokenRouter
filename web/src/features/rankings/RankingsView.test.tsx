// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { loadRankings } from './rankings-api';
import { RankingsView } from './RankingsView';
import type { RankingsSnapshot } from './types';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(/{{(\w+)}}/g, (_, name: string) => String(values?.[name] ?? ''));
  return { useTranslation: () => ({ t }) };
});
vi.mock('./rankings-api', () => ({ loadRankings: vi.fn() }));

const mockedLoadRankings = vi.mocked(loadRankings);

function snapshot(): RankingsSnapshot {
  return {
    models: [{
      rank: 1, previous_rank: 2, model_name: 'alpha-model', vendor: 'Acme',
      category: 'all', total_tokens: 1_200, share: 0.75, growth_pct: 25,
    }],
    vendors: [{
      rank: 1, vendor: 'Acme', total_tokens: 1_200, share: 0.75,
      growth_pct: 25, models_count: 1, top_model: 'alpha-model',
    }],
    top_movers: [{
      model_name: 'alpha-model', vendor: 'Acme', rank_delta: 1,
      current_rank: 1, growth_pct: 25,
    }],
    top_droppers: [{
      model_name: 'beta-model', vendor: 'Bee', rank_delta: -2,
      current_rank: 4, growth_pct: -10,
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
  };
}

function emptySnapshot(): RankingsSnapshot {
  return {
    models: [], vendors: [], top_movers: [], top_droppers: [],
    models_history: { points: [], models: [], buckets: 0 },
    vendor_share_history: { points: [], vendors: [], buckets: 0 },
  };
}

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe('RankingsView', () => {
  it('renders the period-sensitive model, market-share, history, and movement workflow', async () => {
    mockedLoadRankings.mockResolvedValueOnce(snapshot());
    render(<RankingsView search="?period=month" />);

    expect(screen.getByRole('status').textContent).toBe('Loading rankings…');
    expect(screen.getByRole('tab', { name: 'Month' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByRole('heading', { name: 'Top Models' })).toBeTruthy();
    expect(mockedLoadRankings).toHaveBeenCalledWith('month', expect.any(AbortSignal));
    expect(screen.getByText('Daily token usage by model across the past month')).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Model usage history' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Vendor share history' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Market Share' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Trending up' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Trending down' })).toBeTruthy();

    const leaderboard = screen.getByRole('heading', { name: 'LLM Leaderboard' }).closest('.rankings-subsection');
    expect(leaderboard).not.toBeNull();
    const modelLink = within(leaderboard as HTMLElement).getByRole('link', { name: 'alpha-model' });
    expect(modelLink.getAttribute('href')).toBe('/pricing/alpha-model');
    expect(within(leaderboard as HTMLElement).getByRole('link', { name: 'Acme' }).getAttribute('href')).toBe('/pricing?vendor=Acme');
    expect(within(leaderboard as HTMLElement).getByText('1.2K')).toBeTruthy();
    expect(within(leaderboard as HTMLElement).getByText('75.0%')).toBeTruthy();
    expect(within(leaderboard as HTMLElement).getByText('↑25.0%')).toBeTruthy();
    expect(screen.getByText(/Current rank #1/)).toBeTruthy();
    expect(screen.getByText(/Current rank #4/)).toBeTruthy();
  });

  it('navigates through only the four reference periods and defaults malformed search to week', async () => {
    const onNavigate = vi.fn();
    mockedLoadRankings.mockResolvedValueOnce(emptySnapshot());
    render(<RankingsView search="?period=forever" onNavigate={onNavigate} />);
    await screen.findByText('No models match the selected period');

    expect(screen.getByRole('tab', { name: 'Week' }).getAttribute('aria-selected')).toBe('true');
    expect(mockedLoadRankings).toHaveBeenCalledWith('week', expect.any(AbortSignal));
    await userEvent.click(screen.getByRole('tab', { name: 'Year' }));
    expect(onNavigate).toHaveBeenCalledWith('/rankings?period=year');
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Today', 'Week', 'Month', 'Year']);
  });

  it('renders explicit empty states for every reference section', async () => {
    mockedLoadRankings.mockResolvedValueOnce(emptySnapshot());
    render(<RankingsView search="?period=today" />);

    expect(await screen.findByText('No model history data available')).toBeTruthy();
    expect(screen.getByText('No models match the selected period')).toBeTruthy();
    expect(screen.getByText('No vendor history data available')).toBeTruthy();
    expect(screen.getByText('No vendor data available')).toBeTruthy();
    expect(screen.getByText('No notable climbers right now')).toBeTruthy();
    expect(screen.getByText('No notable drops right now')).toBeTruthy();
  });

  it('redacts failures, retries, and aborts an in-flight request on unmount', async () => {
    mockedLoadRankings.mockRejectedValueOnce(new Error('private database detail'));
    mockedLoadRankings.mockResolvedValueOnce(emptySnapshot());
    const rendered = render(<RankingsView search="?period=week" />);

    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load rankings.');
    expect(screen.queryByText('private database detail')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('No models match the selected period')).toBeTruthy();
    expect(mockedLoadRankings).toHaveBeenCalledTimes(2);

    let resolveRequest: ((value: RankingsSnapshot) => void) | undefined;
    mockedLoadRankings.mockReturnValueOnce(new Promise((resolve) => { resolveRequest = resolve; }));
    rendered.unmount();
    const pending = render(<RankingsView search="?period=month" />);
    const signal = mockedLoadRankings.mock.calls.at(-1)?.[1];
    pending.unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => {
      resolveRequest?.(snapshot());
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.queryByText('alpha-model')).toBeNull());
  });
});
