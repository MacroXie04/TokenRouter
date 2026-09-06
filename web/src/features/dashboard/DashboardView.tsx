import type { FormEvent, MouseEvent } from 'react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  canAccessDashboardSection,
  canFilterDashboardByUsername,
  DASHBOARD_MAX_RANGE_SECONDS,
  DASHBOARD_SECTIONS,
  dashboardQueryURL,
  defaultDashboardQuery,
  isAdministratorRole,
  loadDashboardSection,
  normalizeDashboardQuery,
  parseDashboardSearch,
  type DashboardQuery,
  type DashboardResult,
  type DashboardSection
} from './dashboard-api';
import { draftFromQuery, queryFromDraft, QUICK_RANGE_DAYS, sameQuery, type FilterDraft } from './dashboard-presentation';
import './dashboard.css';
import { DashboardPresentation } from './DashboardAggregates';
import { DashboardOverviewPanel } from './DashboardOverviewPanel';
import { DashboardPerformancePanel } from './DashboardPerformancePanel';

export interface DashboardViewProps {
  section: DashboardSection;
  role: number;
  search?: string;
  onNavigate?: (target: string) => void;
}

export const SECTION_LABELS: Record<DashboardSection, string> = {
  overview: 'Overview',
  models: 'Models',
  flow: 'Flow',
  users: 'Users',
};

export const SECTION_DESCRIPTIONS: Record<DashboardSection, string> = {
  overview: 'A concise view of consumption during the selected period.',
  models: 'Compare usage aggregated by model.',
  flow: 'Trace requests across the available traffic dimensions.',
  users: 'Compare usage aggregated by user.',
};

export function DashboardView({ section, role, search = '', onNavigate }: DashboardViewProps) {
  const { t } = useTranslation();
  const isAdmin = isAdministratorRole(role);
  const allowUsername = canFilterDashboardByUsername(section, role);
  const initialQueryRef = useRef<DashboardQuery | null>(null);
  if (initialQueryRef.current === null) {
    initialQueryRef.current = normalizeDashboardQuery(parseDashboardSearch(search), allowUsername);
  }
  const initialQuery = initialQueryRef.current;
  const [query, setQuery] = useState<DashboardQuery>(initialQuery);
  const [draft, setDraft] = useState<FilterDraft>(() => draftFromQuery(initialQuery));
  const [result, setResult] = useState<DashboardResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [filterError, setFilterError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);
  const previousSearch = useRef(search);
  const allowed = canAccessDashboardSection(section, role);
  const visibleSections = DASHBOARD_SECTIONS.filter((item) => canAccessDashboardSection(item, role));
  const effectiveQuery = useMemo(
    () => normalizeDashboardQuery(query, allowUsername),
    [allowUsername, query],
  );

  useEffect(() => {
    if (previousSearch.current === search) return;
    previousSearch.current = search;
    const next = normalizeDashboardQuery(parseDashboardSearch(search), allowUsername);
    setQuery((current) => (sameQuery(current, next) ? current : next));
    setDraft(draftFromQuery(next));
    setFilterError(false);
  }, [allowUsername, search]);

  useEffect(() => {
    if (!allowed) {
      generation.current += 1;
      setResult(null);
      setLoading(false);
      setLoadError(false);
      return undefined;
    }
    const controller = new AbortController();
    const requestGeneration = generation.current + 1;
    generation.current = requestGeneration;
    setLoading(true);
    setLoadError(false);
    setResult(null);
    void loadDashboardSection(section, role, effectiveQuery, controller.signal)
      .then((next) => {
        if (!controller.signal.aborted && generation.current === requestGeneration) setResult(next);
      })
      .catch(() => {
        if (!controller.signal.aborted && generation.current === requestGeneration) setLoadError(true);
      })
      .finally(() => {
        if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false);
      });
    return () => controller.abort();
  }, [allowed, attempt, effectiveQuery, role, section]);

  function navigate(event: MouseEvent<HTMLAnchorElement>, target: string): void {
    if (!onNavigate) return;
    event.preventDefault();
    onNavigate(target);
  }

  function applyFilters(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (loading || !allowed) return;
    try {
      const next = queryFromDraft(draft, allowUsername);
      setFilterError(false);
      setQuery(next);
      const target = dashboardQueryURL(section, role, next);
      previousSearch.current = target.slice(target.indexOf('?'));
      onNavigate?.(target);
    } catch {
      setFilterError(true);
    }
  }

  function resetFilters(): void {
    if (loading || !allowed) return;
    const next = defaultDashboardQuery();
    setDraft(draftFromQuery(next));
    setFilterError(false);
    setQuery(next);
    const target = dashboardQueryURL(section, role, next);
    previousSearch.current = target.slice(target.indexOf('?'));
    onNavigate?.(target);
  }

  function applyQuickRange(days: (typeof QUICK_RANGE_DAYS)[number]): void {
    if (loading || !allowed) return;
    const current = defaultDashboardQuery();
    const next = normalizeDashboardQuery({
      startTimestamp: current.endTimestamp - days * 86_400,
      endTimestamp: current.endTimestamp,
      username: allowUsername ? draft.username : '',
    }, allowUsername);
    setDraft(draftFromQuery(next));
    setFilterError(false);
    setQuery(next);
    const target = dashboardQueryURL(section, role, next);
    previousSearch.current = target.slice(target.indexOf('?'));
    onNavigate?.(target);
  }

  return (
    <main className="app dashboard-page">
      <header className="header dashboard-heading">
        <div>
          <h1>{t('Dashboard')}</h1>
          <p className="tagline">{t('Understand usage without exposing account or provider secrets.')}</p>
        </div>
        <button
          type="button"
          className="link"
          disabled={loading || !allowed}
          onClick={() => setAttempt((value) => value + 1)}
        >
          {loading ? t('Refreshing…') : t('Refresh')}
        </button>
      </header>

      <nav className="dashboard-sections" aria-label={t('Dashboard sections')}>
        {visibleSections.map((item) => {
          const target = dashboardQueryURL(item, role, effectiveQuery);
          return (
            <a
              aria-current={item === section ? 'page' : undefined}
              className={item === section ? 'active' : ''}
              href={target}
              key={item}
              onClick={(event) => navigate(event, target)}
            >
              {t(SECTION_LABELS[item])}
            </a>
          );
        })}
      </nav>

      <section className="card dashboard-panel" aria-labelledby="dashboard-section-heading">
        <div className="dashboard-panel-heading">
          <div>
            <h2 id="dashboard-section-heading">{t(SECTION_LABELS[section])}</h2>
            <p className="muted">{t(SECTION_DESCRIPTIONS[section])}</p>
          </div>
          {allowed && <span className="dashboard-scope-pill">
            {isAdmin && section !== 'overview' ? t('Administrator scope') : t('Your account only')}
          </span>}
        </div>

        {!allowed ? (
          <div className="dashboard-state dashboard-access-denied" role="alert">
            <h3>{t(section === 'users' ? 'Administrator access required' : 'Dashboard access unavailable')}</h3>
            <p>{t(section === 'users'
              ? 'User analytics is available only to administrators.'
              : 'This account role cannot access dashboard analytics.')}</p>
          </div>
        ) : (
          <>
            <form className="dashboard-filters" onSubmit={applyFilters} noValidate>
              <div className="dashboard-quick-ranges" role="group" aria-label={t('Quick range')}>
                <span>{t('Quick range')}</span>
                {QUICK_RANGE_DAYS.map((days) => (
                  <button key={days} onClick={() => applyQuickRange(days)} type="button">
                    {days === 1 ? t('1 Day') : t('{{days}} Days', { days })}
                  </button>
                ))}
              </div>
              <label>
                {t('Start time')}
                <input
                  max="2099-12-31T23:59:59"
                  min="1970-01-02T00:00:00"
                  onChange={(event) => setDraft((current) => ({ ...current, startTime: event.target.value }))}
                  required
                  step={1}
                  type="datetime-local"
                  value={draft.startTime}
                />
              </label>
              <label>
                {t('End time')}
                <input
                  max="2099-12-31T23:59:59"
                  min="1970-01-02T00:00:00"
                  onChange={(event) => setDraft((current) => ({ ...current, endTime: event.target.value }))}
                  required
                  step={1}
                  type="datetime-local"
                  value={draft.endTime}
                />
              </label>
              {allowUsername && (
                <label>
                  {t('Username (optional)')}
                  <input
                    autoComplete="off"
                    maxLength={64}
                    onChange={(event) => setDraft((current) => ({ ...current, username: event.target.value }))}
                    type="search"
                    value={draft.username}
                  />
                </label>
              )}
              <div className="dashboard-filter-actions">
                <button type="submit" disabled={loading}>{t('Apply filters')}</button>
                <button type="button" className="link" disabled={loading} onClick={resetFilters}>{t('Reset')}</button>
              </div>
              <p className="dashboard-filter-help muted">
                {t('Date ranges are limited to {{days}} days.', { days: DASHBOARD_MAX_RANGE_SECONDS / 86_400 })}
              </p>
              {filterError && (
                <p className="error dashboard-filter-error" role="alert">
                  {t('Choose a valid date range of no more than 30 days.')}
                </p>
              )}
            </form>

            <div className="dashboard-results" aria-busy={loading} aria-live="polite">
              {loading && <p className="dashboard-state muted" role="status">{t('Loading dashboard data…')}</p>}
              {!loading && loadError && (
                <div className="dashboard-state dashboard-error-panel">
                  <p className="error" role="alert">{t('Unable to load dashboard data.')}</p>
                  <button type="button" onClick={() => setAttempt((value) => value + 1)}>{t('Try again')}</button>
                </div>
              )}
              {!loading && !loadError && result && result.rows.length === 0 && (
                <p className="dashboard-state muted">{t('No usage data matches this date range.')}</p>
              )}
              {!loading && !loadError && result && result.rows.length > 0 && (
                <DashboardPresentation query={effectiveQuery} result={result} role={role} section={section} />
              )}
            </div>
            {section === 'overview' && <DashboardOverviewPanel refreshKey={attempt} />}
            {isAdmin && section === 'models' && <DashboardPerformancePanel refreshKey={attempt} />}
          </>
        )}
      </section>
    </main>
  );
}
