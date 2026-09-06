import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { loadRankings } from './rankings-api';
import type {
  ModelHistoryPoint,
  ModelRanking,
  RankingMover,
  RankingPeriod,
  RankingsSnapshot,
  VendorRanking,
  VendorSharePoint,
} from './types';
import { RANKING_PERIODS, rankingPeriodFromSearch } from './types';

type ViewState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; snapshot: RankingsSnapshot };

interface RankingsViewProps {
  search?: string;
  onNavigate?: (target: string) => void;
}

const PERIOD_LABELS: Record<RankingPeriod, string> = {
  today: 'Today',
  week: 'Week',
  month: 'Month',
  year: 'Year',
};

const MODEL_PERIOD_DESCRIPTIONS: Record<RankingPeriod, string> = {
  today: 'Hourly token usage by model across the last 24 hours',
  week: 'Daily token usage by model across the last week',
  month: 'Daily token usage by model across the past month',
  year: 'Weekly token usage by model across the past year',
};

const VENDOR_PERIOD_DESCRIPTIONS: Record<RankingPeriod, string> = {
  today: 'Token share by model author across the last 24 hours',
  week: 'Token share by model author across the last week',
  month: 'Token share by model author across the past month',
  year: 'Token share by model author across the past year',
};

function defaultNavigate(target: string) {
  window.history.pushState(null, '', target);
  window.dispatchEvent(new PopStateEvent('popstate'));
  window.scrollTo?.({ top: 0, behavior: 'instant' });
}

export function RankingsView({ search = window.location.search, onNavigate = defaultNavigate }: RankingsViewProps) {
  const { t } = useTranslation();
  const period = useMemo(() => rankingPeriodFromSearch(search), [search]);
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<ViewState>({ status: 'loading' });

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setState({ status: 'loading' });
    loadRankings(period, controller.signal)
      .then((snapshot) => {
        if (active) setState({ status: 'ready', snapshot });
      })
      .catch(() => {
        if (active && !controller.signal.aborted) setState({ status: 'error' });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [attempt, period]);

  const changePeriod = (next: RankingPeriod) => {
    if (next === period) return;
    onNavigate(`/rankings?period=${next}`);
  };

  return (
    <main className="app rankings-page">
      <header className="header rankings-heading">
        <div>
          <h1>{t('Rankings')}</h1>
          <p className="tagline">
            {t('Discover the most-used models and rising vendors from live usage data.')}
          </p>
        </div>
        <a className="button" href="/">{t('Back to home')}</a>
      </header>

      <nav className="rankings-periods" aria-label={t('Period')} role="tablist">
        {RANKING_PERIODS.map((item) => (
          <button
            aria-selected={period === item}
            className={period === item ? 'active' : ''}
            key={item}
            onClick={() => changePeriod(item)}
            role="tab"
            type="button"
          >
            {t(PERIOD_LABELS[item])}
          </button>
        ))}
      </nav>

      <div aria-busy={state.status === 'loading'} aria-live="polite" className="rankings-results">
        {state.status === 'loading' && (
          <section className="card rankings-state">
            <p className="muted" role="status">{t('Loading rankings…')}</p>
          </section>
        )}
        {state.status === 'error' && (
          <section className="card rankings-state">
            <h2>{t('Unable to load rankings')}</h2>
            <p className="error" role="alert">{t('Unable to load rankings.')}</p>
            <button type="button" onClick={() => setAttempt((value) => value + 1)}>{t('Try again')}</button>
          </section>
        )}
        {state.status === 'ready' && (
          <RankingsSnapshotView period={period} snapshot={state.snapshot} />
        )}
      </div>
    </main>
  );
}

function RankingsSnapshotView({ period, snapshot }: { period: RankingPeriod; snapshot: RankingsSnapshot }) {
  return (
    <>
      <ModelsSection period={period} rows={snapshot.models} points={snapshot.models_history.points} />
      <MarketShareSection period={period} rows={snapshot.vendors} points={snapshot.vendor_share_history.points} />
      <PulseSection movers={snapshot.top_movers} droppers={snapshot.top_droppers} />
    </>
  );
}

function ModelsSection({ period, rows, points }: {
  period: RankingPeriod;
  rows: ModelRanking[];
  points: ModelHistoryPoint[];
}) {
  const { t } = useTranslation();
  const total = rows.reduce((sum, row) => sum + row.total_tokens, 0);
  return (
    <section className="card rankings-section" aria-labelledby="ranking-models-heading">
      <header className="rankings-section-heading">
        <div>
          <h2 id="ranking-models-heading">{t('Top Models')}</h2>
          <p className="muted">{t(MODEL_PERIOD_DESCRIPTIONS[period])}</p>
        </div>
        <div className="rankings-total"><strong>{formatTokens(total)}</strong><span>{t('tokens')}</span></div>
      </header>
      <HistoryBars<ModelHistoryPoint>
        emptyLabel={t('No model history data available')}
        label={t('Model usage history')}
        name={(point) => point.model}
        points={points}
        tokens={(point) => point.tokens}
      />
      <div className="rankings-subsection">
        <h3>{t('LLM Leaderboard')}</h3>
        <p className="muted">{t('Compare the most popular models on the platform')}</p>
        {rows.length === 0 ? (
          <p className="rankings-empty">{t('No models match the selected period')}</p>
        ) : (
          <div className="table-scroll">
            <table className="rankings-table">
              <caption className="sr-only">{t('LLM Leaderboard')}</caption>
              <thead><tr>
                <th>{t('Rank')}</th><th>{t('Model')}</th><th>{t('Vendor')}</th>
                <th>{t('Tokens')}</th><th>{t('Share')}</th><th>{t('Change')}</th>
              </tr></thead>
              <tbody>{rows.map((row) => (
                <tr key={row.model_name}>
                  <td>{row.rank}</td>
                  <td><a href={`/pricing/${encodeURIComponent(row.model_name)}`}>{row.model_name}</a></td>
                  <td><VendorLink vendor={row.vendor} /></td>
                  <td>{formatTokens(row.total_tokens)}</td>
                  <td>{formatShare(row.share)}</td>
                  <td><Growth value={row.growth_pct} /></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  );
}

function MarketShareSection({ period, rows, points }: {
  period: RankingPeriod;
  rows: VendorRanking[];
  points: VendorSharePoint[];
}) {
  const { t } = useTranslation();
  return (
    <section className="card rankings-section" aria-labelledby="ranking-vendors-heading">
      <header className="rankings-section-heading">
        <div>
          <h2 id="ranking-vendors-heading">{t('Market Share')}</h2>
          <p className="muted">{t(VENDOR_PERIOD_DESCRIPTIONS[period])}</p>
        </div>
      </header>
      <HistoryBars<VendorSharePoint>
        emptyLabel={t('No vendor history data available')}
        label={t('Vendor share history')}
        name={(point) => point.vendor}
        points={points}
        tokens={(point) => point.tokens}
      />
      <div className="rankings-subsection">
        <h3>{t('By model author')}</h3>
        <p className="muted">{t('Vendors ranked by aggregated token volume')}</p>
        {rows.length === 0 ? (
          <p className="rankings-empty">{t('No vendor data available')}</p>
        ) : (
          <div className="table-scroll">
            <table className="rankings-table">
              <caption className="sr-only">{t('By model author')}</caption>
              <thead><tr>
                <th>{t('Rank')}</th><th>{t('Vendor')}</th><th>{t('Tokens')}</th>
                <th>{t('Share')}</th><th>{t('Models')}</th><th>{t('Top model')}</th>
              </tr></thead>
              <tbody>{rows.slice(0, 12).map((row) => (
                <tr key={row.vendor}>
                  <td>{row.rank}</td>
                  <td><VendorLink vendor={row.vendor} /></td>
                  <td>{formatTokens(row.total_tokens)}</td>
                  <td>{formatShare(row.share)}</td>
                  <td>{row.models_count}</td>
                  <td><a href={`/pricing/${encodeURIComponent(row.top_model)}`}>{row.top_model}</a></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  );
}

function PulseSection({ movers, droppers }: { movers: RankingMover[]; droppers: RankingMover[] }) {
  const { t } = useTranslation();
  return (
    <section className="rankings-pulse" aria-label={t('Ranking movement')}>
      <PulseCard
        description={t('Models climbing the leaderboard')}
        empty={t('No notable climbers right now')}
        rows={movers}
        title={t('Trending up')}
      />
      <PulseCard
        description={t('Models losing positions')}
        empty={t('No notable drops right now')}
        rows={droppers}
        title={t('Trending down')}
      />
    </section>
  );
}

function PulseCard({ title, description, empty, rows }: {
  title: string;
  description: string;
  empty: string;
  rows: RankingMover[];
}) {
  const { t } = useTranslation();
  return (
    <article className="card rankings-pulse-card">
      <h2>{title}</h2>
      <p className="muted">{description}</p>
      {rows.length === 0 ? <p className="rankings-empty">{empty}</p> : (
        <ul>{rows.map((row) => (
          <li key={row.model_name}>
            <div>
              <a href={`/pricing/${encodeURIComponent(row.model_name)}`}>{row.model_name}</a>
              <span className="muted">{t('Current rank #{{rank}}', { rank: row.current_rank })} · <VendorLink vendor={row.vendor} /></span>
            </div>
            <strong className={row.rank_delta > 0 ? 'rankings-up' : 'rankings-down'}>
              {row.rank_delta > 0 ? '↑' : '↓'}{Math.abs(row.rank_delta)}
            </strong>
          </li>
        ))}</ul>
      )}
    </article>
  );
}

function HistoryBars<Point extends { ts: string; label: string }>({ points, label, emptyLabel, name, tokens }: {
  points: Point[];
  label: string;
  emptyLabel: string;
  name: (point: Point) => string;
  tokens: (point: Point) => number;
}) {
  const groups = new Map<string, { label: string; points: Point[]; total: number }>();
  for (const point of points) {
    const group = groups.get(point.ts) ?? { label: point.label, points: [], total: 0 };
    group.points.push(point);
    group.total += tokens(point);
    groups.set(point.ts, group);
  }
  if (groups.size === 0) return <p className="rankings-empty rankings-history-empty">{emptyLabel}</p>;
  return (
    <div className="rankings-history" aria-label={label} role="region">
      <ol>{[...groups.entries()].map(([timestamp, group]) => (
        <li key={timestamp}>
          <time dateTime={timestamp}>{group.label}</time>
          <div className="rankings-history-track" role="group" aria-label={`${group.label}: ${formatTokens(group.total)}`}>
            {group.points.map((point) => (
              <span
                aria-label={`${name(point)}: ${formatTokens(tokens(point))}`}
                className="rankings-history-segment"
                key={name(point)}
                role="img"
                style={{ width: `${(tokens(point) / group.total) * 100}%` }}
                title={`${name(point)}: ${formatTokens(tokens(point))}`}
              />
            ))}
          </div>
          <strong>{formatTokens(group.total)}</strong>
        </li>
      ))}</ol>
    </div>
  );
}

function VendorLink({ vendor }: { vendor: string }) {
  return <a href={`/pricing?vendor=${encodeURIComponent(vendor)}`}>{vendor}</a>;
}

function Growth({ value }: { value: number }) {
  if (value === 0) return <span className="muted">0%</span>;
  const positive = value > 0;
  const absolute = Math.abs(value);
  return (
    <span className={positive ? 'rankings-up' : 'rankings-down'}>
      {positive ? '↑' : '↓'}{absolute.toFixed(absolute >= 100 ? 0 : 1)}%
    </span>
  );
}

export function formatTokens(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0';
  if (value >= 1_000_000_000_000) return `${(value / 1_000_000_000_000).toFixed(2)}T`;
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(value >= 10_000_000_000 ? 1 : 2)}B`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 1 : 2)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}K`;
  return value.toLocaleString();
}

function formatShare(value: number): string {
  if (value <= 0) return '0%';
  if (value < 0.001) return '<0.1%';
  return `${(value * 100).toFixed(value < 0.01 ? 2 : 1)}%`;
}
