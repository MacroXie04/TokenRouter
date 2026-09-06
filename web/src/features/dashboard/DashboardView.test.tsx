// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadDashboardOverview, loadDashboardPerformance, loadDashboardSection } from './dashboard-api';
import type { DashboardOverviewContent, DashboardResult } from './dashboard-api';
import { DashboardView } from './DashboardView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) =>
    key.replace(/{{(\w+)}}/g, (_, name: string) => String(values?.[name] ?? ''));
  return { useTranslation: () => ({ t }) };
});

vi.mock('./dashboard-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./dashboard-api')>();
  return {
    ...actual,
    loadDashboardOverview: vi.fn(),
    loadDashboardPerformance: vi.fn(),
    loadDashboardSection: vi.fn(),
  };
});

const mockedLoad = vi.mocked(loadDashboardSection);
const mockedOverview = vi.mocked(loadDashboardOverview);
const mockedPerformance = vi.mocked(loadDashboardPerformance);
const fixedSearch = '?start_timestamp=1700000000&end_timestamp=1700086400';

function quotaResult(): DashboardResult {
  return {
    kind: 'quota',
    rows: [
      { modelName: 'gpt-4o', username: '', createdAt: 1_700_000_000, quota: 70, requests: 3, tokens: 90 },
      { modelName: 'gpt-4o', username: '', createdAt: 1_700_003_600, quota: 30, requests: 2, tokens: 40 },
      { modelName: 'claude-sonnet', username: '', createdAt: 1_700_000_000, quota: 50, requests: 1, tokens: 60 },
    ],
    summary: { quota: 150, requests: 6, tokens: 190 },
  };
}

function quotaTimelineResult(): DashboardResult {
  const rows = Array.from({ length: 8 }, (_, index) => ({
    modelName: index % 2 === 0 ? 'gpt-4o' : 'claude-sonnet',
    username: '',
    createdAt: 1_700_000_000 + index * 3_600,
    quota: 10 + index,
    requests: 1 + index,
    tokens: 20 + index,
  }));
  return {
    kind: 'quota',
    rows,
    summary: rows.reduce((summary, row) => ({
      quota: summary.quota + row.quota,
      requests: summary.requests + row.requests,
      tokens: summary.tokens + row.tokens,
    }), { quota: 0, requests: 0, tokens: 0 }),
  };
}

function usersResult(): DashboardResult {
  return {
    kind: 'users',
    rows: [
      { username: 'alice', createdAt: 1_700_000_000, quota: 60, requests: 3, tokens: 70 },
      { username: 'alice', createdAt: 1_700_003_600, quota: 10, requests: 1, tokens: 15 },
      { username: 'bob', createdAt: 1_700_000_000, quota: 40, requests: 2, tokens: 50 },
    ],
    summary: { quota: 110, requests: 6, tokens: 135 },
  };
}

function usersTimelineResult(): DashboardResult {
  const rows = Array.from({ length: 8 }, (_, index) => ({
    username: index % 2 === 0 ? 'alice' : 'bob',
    createdAt: 1_700_000_000 + Math.floor(index / 2) * 3_600,
    quota: 10 + index,
    requests: 1 + index,
    tokens: 20 + index,
  }));
  return {
    kind: 'users',
    rows,
    summary: rows.reduce((summary, row) => ({
      quota: summary.quota + row.quota,
      requests: summary.requests + row.requests,
      tokens: summary.tokens + row.tokens,
    }), { quota: 0, requests: 0, tokens: 0 }),
  };
}

function multiFlowResult(): DashboardResult {
  const first = (flowResult() as Extract<DashboardResult, { kind: 'flow' }>).rows[0];
  return {
    kind: 'flow',
    rows: [first, {
      ...first,
      userId: 10,
      username: 'bob',
      nodeName: 'node-b',
      tokenId: 11,
      tokenName: 'secondary',
      group: 'vip',
      modelName: 'claude-sonnet',
      channelId: 12,
      channelName: 'fallback',
      quota: 30,
      requests: 2,
      tokens: 50,
    }],
    summary: { quota: 120, requests: 6, tokens: 170 },
  };
}

function flowResult(): DashboardResult {
  return {
    kind: 'flow',
    rows: [{
      userId: 7,
      username: 'alice',
      nodeName: 'node-a',
      tokenId: 8,
      tokenName: 'primary',
      group: 'default',
      modelName: 'gpt-4o',
      channelId: 9,
      channelName: 'main',
      quota: 90,
      requests: 4,
      tokens: 120,
    }],
    summary: { quota: 90, requests: 4, tokens: 120 },
  };
}

function overviewContent(): DashboardOverviewContent {
  return {
    notice: { ok: true, value: '' },
    gateway: {
      ok: true,
      value: {
        systemName: 'TokenRouter', version: '1.2.3', nodeName: 'node-a', serverAddress: 'https://api.example.test',
        apiInfoEnabled: false, apiInfo: [], faqEnabled: false, faq: [], uptimeKumaEnabled: false,
      },
    },
    uptime: { ok: true, value: [] },
  };
}

beforeEach(() => {
  mockedOverview.mockResolvedValue(overviewContent());
  mockedPerformance.mockResolvedValue([]);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.resetAllMocks();
});

describe('DashboardView', () => {
  it('renders scoped model aggregates, summaries, empty-safe navigation, and accessible filters', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    const onNavigate = vi.fn();
    render(<DashboardView section="models" role={1} search={fixedSearch} onNavigate={onNavigate} />);

    expect(screen.getByRole('status').textContent).toBe('Loading dashboard data…');
    expect(await screen.findByRole('table', { name: 'Usage by model' })).toBeTruthy();
    expect(mockedLoad).toHaveBeenCalledWith('models', 1, {
      startTimestamp: 1_700_000_000,
      endTimestamp: 1_700_086_400,
      username: '',
    }, expect.any(AbortSignal));
    expect(screen.getByText('Your account only')).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Users' })).toBeNull();
    expect(screen.getByLabelText('Start time')).toBeTruthy();
    expect(screen.getByLabelText('End time')).toBeTruthy();
    expect(screen.queryByLabelText('Username (optional)')).toBeNull();
    expect(mockedPerformance).not.toHaveBeenCalled();

    const table = screen.getByRole('table', { name: 'Usage by model' });
    const gptRow = within(table).getByRole('rowheader', { name: 'gpt-4o' }).closest('tr');
    expect(gptRow).not.toBeNull();
    expect(within(gptRow as HTMLElement).getAllByRole('cell').map((cell) => cell.textContent)).toEqual(['100', '5', '130', '']);
    expect(screen.getByRole('progressbar', { name: 'Relative quota for gpt-4o' })).toBeTruthy();
    expect(within(screen.getByLabelText('Usage summary')).getByText('150')).toBeTruthy();

    const flowLink = screen.getByRole('link', { name: 'Flow' });
    flowLink.focus();
    await userEvent.setup().keyboard('{Enter}');
    expect(onNavigate).toHaveBeenCalledWith(
      '/dashboard/flow?start_timestamp=1700000000&end_timestamp=1700086400',
    );
  });

  it('offers persistent model chart preferences, filtering, a source-backed trend, and distribution', async () => {
    mockedLoad.mockResolvedValueOnce(quotaTimelineResult());
    render(<DashboardView section="models" role={1} search={fixedSearch} />);

    const controls = await screen.findByRole('group', { name: 'Model chart preferences and filters' });
    expect(screen.getByRole('img', { name: /Model usage over time/ })).toBeTruthy();
    const series = screen.getByRole('list', { name: 'Chart series' });
    expect(within(series).getByText('gpt-4o')).toBeTruthy();
    expect(within(series).getByText('claude-sonnet')).toBeTruthy();
    expect(screen.getByRole('list', { name: 'Model consumption distribution' })).toBeTruthy();
    fireEvent.change(within(controls).getByLabelText('Metric'), { target: { value: 'requests' } });
    expect(screen.getByRole('progressbar', { name: 'Requests for claude-sonnet' })).toBeTruthy();
    await userEvent.setup().type(within(controls).getByLabelText('Filter models'), 'claude');
    const table = screen.getByRole('table', { name: 'Usage by model' });
    expect(within(table).getByRole('rowheader', { name: 'claude-sonnet' })).toBeTruthy();
    expect(within(table).queryByRole('rowheader', { name: 'gpt-4o' })).toBeNull();
    expect(screen.getByText(
      'Showing 1 of 2 models. Filtered charts and table total 56 quota, 20 requests, and 96 tokens.',
    )).toBeTruthy();
    expect(screen.getByText('4 observed intervals')).toBeTruthy();
  });

  it('keeps missing identities distinct from legitimate fallback-like model names', async () => {
    mockedLoad.mockResolvedValueOnce({
      kind: 'quota',
      rows: [
        { modelName: '', username: '', createdAt: 1_700_000_000, quota: 10, requests: 1, tokens: 11 },
        { modelName: 'Unknown model', username: '', createdAt: 1_700_000_000, quota: 20, requests: 2, tokens: 22 },
      ],
      summary: { quota: 30, requests: 3, tokens: 33 },
    });
    render(<DashboardView section="models" role={1} search={fixedSearch} />);

    const table = await screen.findByRole('table', { name: 'Usage by model' });
    const namedRow = within(table).getByRole('rowheader', { name: 'Unknown model' }).closest('tr') as HTMLElement;
    const missingRow = within(table).getByRole('rowheader', { name: 'Missing model name' }).closest('tr') as HTMLElement;
    expect(within(namedRow).getAllByRole('cell')[0].textContent).toBe('20');
    expect(within(missingRow).getAllByRole('cell')[0].textContent).toBe('10');
  });

  it('uses the administrator-only user view and renders per-user aggregates', async () => {
    mockedLoad.mockResolvedValueOnce(usersResult());
    render(<DashboardView section="users" role={10} search={`${fixedSearch}&username=ignored`} />);

    const table = await screen.findByRole('table', { name: 'Usage by user' });
    expect(mockedLoad).toHaveBeenCalledWith('users', 10, expect.objectContaining({ username: '' }), expect.any(AbortSignal));
    expect(screen.getByText('Administrator scope')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Users' }).getAttribute('aria-current')).toBe('page');
    expect(screen.queryByLabelText('Username (optional)')).toBeNull();
    const aliceRow = within(table).getByRole('rowheader', { name: 'alice' }).closest('tr');
    expect(within(aliceRow as HTMLElement).getAllByRole('cell').map((cell) => cell.textContent)).toEqual(['70', '4', '85', '']);
  });

  it('renders admin-only active-user, ranking, and trend analytics with exact-data fallback', async () => {
    mockedLoad.mockResolvedValueOnce(usersTimelineResult());
    render(<DashboardView section="users" role={10} search={fixedSearch} />);

    expect(await screen.findByText('Active users')).toBeTruthy();
    expect(screen.getByRole('img', { name: /User consumption trend/ })).toBeTruthy();
    const ranking = screen.getByRole('list', { name: 'User consumption ranking' });
    expect(within(ranking).getByText('alice')).toBeTruthy();
    expect(within(ranking).getByText('bob')).toBeTruthy();
    expect(screen.getByRole('table', { name: 'Usage by user' })).toBeTruthy();
  });

  it('fails closed without a network request when a non-admin reaches user analytics', () => {
    render(<DashboardView section="users" role={1} search={fixedSearch} />);

    expect(screen.getByRole('alert').textContent).toContain('Administrator access required');
    expect(screen.getByRole('alert').textContent).toContain('User analytics is available only to administrators.');
    expect(screen.queryByRole('link', { name: 'Users' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Apply filters' })).toBeNull();
    expect(mockedLoad).not.toHaveBeenCalled();
  });

  it('renders a dependency-free flow path and role-scoped totals', async () => {
    mockedLoad.mockResolvedValueOnce(flowResult());
    render(<DashboardView section="flow" role={100} search={fixedSearch} />);

    const table = await screen.findByRole('table', { name: 'Traffic flow' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Show sensitive labels' }));
    expect(within(table).getByRole('rowheader', {
      name: 'alice → node-a → primary → default → gpt-4o → main',
    })).toBeTruthy();
    expect(mockedLoad).toHaveBeenCalledWith('flow', 100, expect.any(Object), expect.any(AbortSignal));
  });

  it('treats arrow characters inside flow labels as content instead of extra stages', async () => {
    const result = flowResult() as Extract<DashboardResult, { kind: 'flow' }>;
    mockedLoad.mockResolvedValueOnce({
      ...result,
      rows: [{ ...result.rows[0], nodeName: 'node-a → deceptive-stage' }],
    });
    const rendered = render(<DashboardView section="flow" role={100} search={fixedSearch} />);
    await screen.findByRole('table', { name: 'Traffic flow' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Show sensitive labels' }));

    const nodeStrip = rendered.container.querySelector('.dashboard-flow-nodes');
    expect(nodeStrip?.children).toHaveLength(6);
    expect(within(nodeStrip as HTMLElement).getByText('node-a → deceptive-stage')).toBeTruthy();
  });

  it('keeps the unknown-token stage in self-service flow rows', async () => {
    mockedLoad.mockResolvedValueOnce({
      kind: 'flow',
      rows: [{
        username: '', nodeName: '', tokenName: '', group: 'default', modelName: 'gpt-4o',
        channelName: '', quota: 9, requests: 1, tokens: 12,
      }],
      summary: { quota: 9, requests: 1, tokens: 12 },
    });
    const rendered = render(<DashboardView section="flow" role={1} search={fixedSearch} />);
    const table = await screen.findByRole('table', { name: 'Traffic flow' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Show sensitive labels' }));

    expect(within(table).getByRole('rowheader', { name: 'Unknown token → default → gpt-4o' })).toBeTruthy();
    expect(rendered.container.querySelector('.dashboard-flow-nodes')?.children).toHaveLength(3);
  });

  it('masks every secondary flow surface and applies OR-within-kind node filters', async () => {
    mockedLoad.mockResolvedValueOnce(multiFlowResult());
    render(<DashboardView section="flow" role={100} search={fixedSearch} />);
    const user = userEvent.setup();
    await screen.findByRole('table', { name: 'Traffic flow' });

    expect(screen.getByRole('button', { name: 'Show sensitive labels' })).toBeTruthy();
    expect(screen.queryByText('alice')).toBeNull();
    expect(screen.queryByText('bob')).toBeNull();
    expect(screen.getAllByText('Hidden User 1').length).toBeGreaterThan(0);
    await user.selectOptions(screen.getByLabelText('Node type'), 'user');
    const userFilters = screen.getByRole('group', { name: 'Node filters' });
    const checkboxes = within(userFilters).getAllByRole('checkbox');
    await user.click(checkboxes[0]);
    expect(screen.getByText(/Map shows 1 of 1 matching flow paths \(2 total\); table shows 1\./)).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Show sensitive labels' }));
    expect(screen.getAllByText('alice').length).toBeGreaterThan(0);
  });

  it('facets node choices across kinds and keeps hidden constraints removable', async () => {
    mockedLoad.mockResolvedValueOnce(multiFlowResult());
    render(<DashboardView section="flow" role={100} search={fixedSearch} />);
    const user = userEvent.setup();
    await screen.findByRole('table', { name: 'Traffic flow' });
    await user.click(screen.getByRole('button', { name: 'Show sensitive labels' }));
    await user.selectOptions(screen.getByLabelText('Node type'), 'group');
    await user.click(within(screen.getByRole('group', { name: 'Node filters' }))
      .getByRole('checkbox', { name: /default/ }));
    await user.selectOptions(screen.getByLabelText('Node type'), 'model');

    expect(within(screen.getByRole('list', { name: 'Active node filters' }))
      .getByRole('button', { name: 'Remove Group filter default' })).toBeTruthy();
    const modelFilters = screen.getByRole('group', { name: 'Node filters' });
    const gpt = within(modelFilters).getByRole('checkbox', { name: /gpt-4o/ });
    expect(within(gpt.closest('label') as HTMLElement).getByText('90')).toBeTruthy();
    expect(within(modelFilters).queryByRole('checkbox', { name: /claude-sonnet/ })).toBeNull();
  });

  it('keeps the overview account-scoped for an administrator and strips a privileged filter', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    render(<DashboardView section="overview" role={100} search={`${fixedSearch}&username=alice`} />);

    expect(await screen.findByRole('table', { name: 'Top models' })).toBeTruthy();
    expect(screen.getByText('Your account only')).toBeTruthy();
    expect(screen.queryByLabelText('Username (optional)')).toBeNull();
    expect(mockedLoad).toHaveBeenCalledWith('overview', 100, {
      startTimestamp: 1_700_000_000,
      endTimestamp: 1_700_086_400,
      username: '',
    }, expect.any(AbortSignal));
    expect(mockedPerformance).not.toHaveBeenCalled();
  });

  it('renders validated API information, FAQ, and grouped Uptime Kuma content', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    mockedOverview.mockResolvedValueOnce({
      ...overviewContent(),
      notice: { ok: true, value: 'Planned maintenance Saturday' },
      gateway: { ok: true, value: {
        systemName: 'TokenRouter', version: '1.2.3', nodeName: 'node-a', serverAddress: 'https://api.example.test',
        apiInfoEnabled: true,
        apiInfo: [{ id: 1, url: 'https://relay.example.test/v1', route: 'Primary', description: 'Main route', color: 'green' }],
        faqEnabled: true,
        faq: [{ id: 2, question: 'How do I connect?', answer: 'Create a token first.' }],
        uptimeKumaEnabled: true,
      } },
      uptime: { ok: true, value: [{ categoryName: 'Core services', monitors: [{
        name: 'Chat API', group: 'APIs', status: 1, uptime: 0.9995,
      }] }] },
    });
    render(<DashboardView section="overview" role={1} search={fixedSearch} />);

    expect(await screen.findByRole('heading', { name: 'Site notice' })).toBeTruthy();
    expect(screen.getByText('Planned maintenance Saturday')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Gateway API' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'https://api.example.test/v1' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'API information' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Primary' }).getAttribute('href')).toBe('https://relay.example.test/v1');
    expect(screen.getByRole('heading', { name: 'FAQ' })).toBeTruthy();
    expect(screen.getByText('How do I connect?')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Uptime Kuma' })).toBeTruthy();
    expect(screen.getByText('Chat API (APIs)')).toBeTruthy();
    expect(screen.getByText('99.95% uptime')).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Operational' })).toBeTruthy();
    expect(mockedOverview).toHaveBeenCalledWith(window.location.origin, expect.any(AbortSignal));
  });

  it('shows explicit empty and isolated Uptime Kuma error states while respecting disabled panels', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    mockedOverview.mockResolvedValueOnce({
      ...overviewContent(),
      gateway: { ok: true, value: {
        systemName: 'TokenRouter', version: '', nodeName: '', serverAddress: '',
        apiInfoEnabled: true, apiInfo: [], faqEnabled: true, faq: [], uptimeKumaEnabled: true,
      } },
      uptime: { ok: false },
    });
    render(<DashboardView section="overview" role={1} search={fixedSearch} />);

    expect(await screen.findByText('No API information is configured.')).toBeTruthy();
    expect(screen.getByText('No FAQ entries are configured.')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toBe('Unable to load Uptime Kuma status.');
    expect(screen.queryByText(/credential|upstream secret/i)).toBeNull();
  });

  it('keeps partial overview failures generic and retries all local overview sources', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    mockedOverview
      .mockResolvedValueOnce({ ...overviewContent(), notice: { ok: false } })
      .mockResolvedValueOnce(overviewContent());
    render(<DashboardView section="overview" role={1} search={fixedSearch} />);

    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load the site notice.');
    expect(screen.queryByText(/private|database|credential/i)).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('No site notice at this time.')).toBeTruthy();
    expect(mockedOverview).toHaveBeenCalledTimes(2);
    expect(mockedLoad).toHaveBeenCalledTimes(1);
  });

  it('fails closed for an unknown role across every dashboard section', () => {
    render(<DashboardView section="overview" role={11} search={fixedSearch} />);
    expect(screen.getByRole('alert').textContent).toContain('Dashboard access unavailable');
    expect(screen.queryByRole('navigation', { name: 'Dashboard sections' })?.childElementCount).toBe(0);
    expect(mockedLoad).not.toHaveBeenCalled();
    expect(mockedOverview).not.toHaveBeenCalled();
  });

  it('renders bounded administrator performance data as an accessible table and chart', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    mockedPerformance.mockResolvedValueOnce([{
      modelName: 'gpt-4o',
      averageLatencyMs: 245,
      successRate: 99.5,
      averageTokensPerSecond: 42.25,
      recentSuccessRates: [99, 100],
    }]);
    render(<DashboardView section="models" role={10} search={fixedSearch} />);

    const table = await screen.findByRole('table', { name: 'Model performance (24h)' });
    expect(mockedPerformance).toHaveBeenCalledWith(expect.any(AbortSignal));
    const row = within(table).getByRole('rowheader', { name: 'gpt-4o' }).closest('tr');
    expect(within(row as HTMLElement).getByText('245 ms average latency')).toBeTruthy();
    expect(within(row as HTMLElement).getByText('99.5% success')).toBeTruthy();
    expect(within(row as HTMLElement).getByText('42.25 tokens/s')).toBeTruthy();
    expect(screen.getByRole('progressbar', { name: 'gpt-4o: 99.5%' })).toBeTruthy();
  });

  it('redacts performance failures and retries into an explicit empty state', async () => {
    mockedLoad.mockResolvedValueOnce(quotaResult());
    mockedPerformance
      .mockRejectedValueOnce(new Error('private metrics database'))
      .mockResolvedValueOnce([]);
    render(<DashboardView section="models" role={10} search={fixedSearch} />);

    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load model performance.');
    expect(screen.queryByText(/private metrics/)).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again: Model performance (24h)' }));
    expect(await screen.findByText('No performance data yet.')).toBeTruthy();
    expect(mockedPerformance).toHaveBeenCalledTimes(2);
    expect(mockedLoad).toHaveBeenCalledTimes(1);
  });

  it('preserves an exact timestamp range when only the administrator username changes', async () => {
    mockedLoad.mockResolvedValue(quotaResult());
    const onNavigate = vi.fn();
    render(<DashboardView section="models" role={10} search={fixedSearch} onNavigate={onNavigate} />);
    await screen.findByRole('table', { name: 'Usage by model' });

    fireEvent.change(screen.getByLabelText('Username (optional)'), { target: { value: 'alice' } });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Apply filters' }));

    await waitFor(() => expect(mockedLoad).toHaveBeenCalledTimes(2));
    expect(mockedLoad).toHaveBeenLastCalledWith('models', 10, {
      startTimestamp: 1_700_000_000,
      endTimestamp: 1_700_086_400,
      username: 'alice',
    }, expect.any(AbortSignal));
    expect(onNavigate).toHaveBeenCalledWith(
      '/dashboard/models?start_timestamp=1700000000&end_timestamp=1700086400',
    );
  });

  it('applies reference-style quick ranges while keeping usernames out of browser history', async () => {
    const now = 1_800_000_123;
    const dateSpy = vi.spyOn(Date, 'now').mockReturnValue(now * 1_000);
    mockedLoad.mockResolvedValue(quotaResult());
    const onNavigate = vi.fn();
    render(<DashboardView section="models" role={10} search={fixedSearch} onNavigate={onNavigate} />);
    await screen.findByRole('table', { name: 'Usage by model' });
    fireEvent.change(screen.getByLabelText('Username (optional)'), { target: { value: 'alice' } });

    await userEvent.setup().click(within(screen.getByRole('group', { name: 'Quick range' }))
      .getByRole('button', { name: '7 Days' }));
    await waitFor(() => expect(mockedLoad).toHaveBeenCalledTimes(2));
    const requested = mockedLoad.mock.calls.at(-1)?.[2];
    expect((requested?.endTimestamp ?? 0) - (requested?.startTimestamp ?? 0)).toBe(7 * 86_400);
    expect(requested?.endTimestamp).toBe(now);
    expect(requested?.username).toBe('alice');
    const target = String(onNavigate.mock.calls.at(-1)?.[0]);
    expect(target).toContain('/dashboard/models?start_timestamp=');
    expect(target).not.toContain('username');
    expect(target).not.toContain('alice');
    dateSpy.mockRestore();
  });

  it('rejects overlong dates locally, then applies a bounded canonical admin filter', async () => {
    mockedLoad.mockResolvedValue(quotaResult());
    const onNavigate = vi.fn();
    render(<DashboardView section="models" role={10} search={fixedSearch} onNavigate={onNavigate} />);
    await screen.findByRole('table', { name: 'Usage by model' });

    fireEvent.change(screen.getByLabelText('Start time'), { target: { value: '2026-08-01T00:00:00' } });
    fireEvent.change(screen.getByLabelText('End time'), { target: { value: '2026-08-31T23:59:59' } });
    fireEvent.change(screen.getByLabelText('Username (optional)'), { target: { value: ' alice ' } });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Apply filters' }));
    expect(screen.getByRole('alert').textContent).toBe('Choose a valid date range of no more than 30 days.');
    expect(mockedLoad).toHaveBeenCalledTimes(1);
    expect(onNavigate).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText('Start time'), { target: { value: '2026-08-02T00:00:00' } });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Apply filters' }));
    const start = new Date(2026, 7, 2, 0, 0, 0, 0).getTime() / 1_000;
    const end = new Date(2026, 7, 31, 23, 59, 59, 0).getTime() / 1_000;
    await waitFor(() => expect(mockedLoad).toHaveBeenCalledTimes(2));
    expect(mockedLoad).toHaveBeenLastCalledWith('models', 10, {
      startTimestamp: start,
      endTimestamp: end,
      username: 'alice',
    }, expect.any(AbortSignal));
    expect(onNavigate).toHaveBeenCalledWith(
      `/dashboard/models?start_timestamp=${start}&end_timestamp=${end}`,
    );
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('uses the actual selected span for sub-minute rates and reports zero for a zero-length span', async () => {
    const result: DashboardResult = {
      kind: 'quota',
      rows: [{ modelName: 'gpt-4o', username: '', createdAt: 1_700_000_000, quota: 40, requests: 2, tokens: 30 }],
      summary: { quota: 40, requests: 2, tokens: 30 },
    };
    mockedLoad.mockResolvedValue(result);
    const rendered = render(<DashboardView
      section="overview"
      role={1}
      search="?start_timestamp=1700000000&end_timestamp=1700000030"
    />);
    await screen.findByRole('table', { name: 'Top models' });
    let summary = screen.getByLabelText('Usage summary');
    let rpmCard = within(summary).getByText('Average RPM').closest('div') as HTMLElement;
    let tpmCard = within(summary).getByText('Average TPM').closest('div') as HTMLElement;
    expect(within(rpmCard).getByText('4')).toBeTruthy();
    expect(within(tpmCard).getByText('60')).toBeTruthy();

    rendered.unmount();
    cleanup();
    render(<DashboardView
      section="overview"
      role={1}
      search="?start_timestamp=1700000000&end_timestamp=1700000000"
    />);
    await screen.findByRole('table', { name: 'Top models' });
    summary = screen.getByLabelText('Usage summary');
    rpmCard = within(summary).getByText('Average RPM').closest('div') as HTMLElement;
    tpmCard = within(summary).getByText('Average TPM').closest('div') as HTMLElement;
    expect(within(rpmCard).getByText('0')).toBeTruthy();
    expect(within(tpmCard).getByText('0')).toBeTruthy();
  });

  it('redacts failures, retries, and renders an explicit empty state', async () => {
    mockedLoad.mockRejectedValueOnce(new Error('private database host and credential'));
    mockedLoad.mockResolvedValueOnce({ kind: 'quota', rows: [], summary: { quota: 0, requests: 0, tokens: 0 } });
    render(<DashboardView section="overview" role={1} search={fixedSearch} />);

    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load dashboard data.');
    expect(screen.queryByText(/private database/)).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('No usage data matches this date range.')).toBeTruthy();
    expect(mockedLoad).toHaveBeenCalledTimes(2);
  });

  it('aborts obsolete requests and ignores late resolutions after a section change and unmount', async () => {
    let resolveModels: ((value: DashboardResult) => void) | undefined;
    mockedLoad.mockReturnValueOnce(new Promise((resolve) => { resolveModels = resolve; }));
    mockedLoad.mockResolvedValueOnce(flowResult());
    const rendered = render(<DashboardView section="models" role={1} search={fixedSearch} />);
    const modelSignal = mockedLoad.mock.calls[0]?.[3];

    rendered.rerender(<DashboardView section="flow" role={1} search={fixedSearch} />);
    expect(modelSignal?.aborted).toBe(true);
    expect(await screen.findByRole('table', { name: 'Traffic flow' })).toBeTruthy();
    await act(async () => {
      resolveModels?.(quotaResult());
      await Promise.resolve();
    });
    expect(screen.queryByRole('table', { name: 'Usage by model' })).toBeNull();

    let resolveUnmounted: ((value: DashboardResult) => void) | undefined;
    mockedLoad.mockReturnValueOnce(new Promise((resolve) => { resolveUnmounted = resolve; }));
    rendered.rerender(<DashboardView section="overview" role={1} search={fixedSearch} />);
    const unmountSignal = mockedLoad.mock.calls.at(-1)?.[3];
    rendered.unmount();
    expect(unmountSignal?.aborted).toBe(true);
    await act(async () => {
      resolveUnmounted?.(quotaResult());
      await Promise.resolve();
    });
    expect(screen.queryByText('gpt-4o')).toBeNull();
  });

  it('aborts overview content requests and ignores their late result after unmount', async () => {
    let resolveOverview: ((value: DashboardOverviewContent) => void) | undefined;
    mockedLoad.mockResolvedValueOnce(quotaResult());
    mockedOverview.mockReturnValueOnce(new Promise((resolve) => { resolveOverview = resolve; }));
    const rendered = render(<DashboardView section="overview" role={1} search={fixedSearch} />);
    await waitFor(() => expect(mockedOverview).toHaveBeenCalledTimes(1));
    const signal = mockedOverview.mock.calls[0]?.[1];

    rendered.unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => {
      resolveOverview?.({ ...overviewContent(), notice: { ok: true, value: 'late notice' } });
      await Promise.resolve();
    });
    expect(screen.queryByText('late notice')).toBeNull();
  });
});
