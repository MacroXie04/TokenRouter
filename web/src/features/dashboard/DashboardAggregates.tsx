import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  type DashboardQuery,
  type DashboardQuotaRow,
  type DashboardResult,
  type DashboardSection,
  type DashboardSummary,
  type DashboardUserRow
} from './dashboard-api';
import { aggregatePresentationRows, buildTimelineSeries, formatNumber, modelDimension, sortAggregates, useAnalyticsPreferences, userDimension } from './dashboard-presentation';
import { AggregateTable, AnalyticsControls, DistributionBars, SummaryCards, TimelineChart } from './DashboardCharts';
import { FlowPresentation } from './DashboardFlow';

export function ModelsPresentation({ rows, query }: { rows: DashboardQuotaRow[]; query: DashboardQuery }) {
  const { t } = useTranslation();
  const [preferences, setPreferences] = useAnalyticsPreferences('models');
  const [search, setSearch] = useState('');
  const normalizedSearch = search.trim().toLocaleLowerCase();
  const filteredRows = useMemo(() => rows.filter((row) => !normalizedSearch
    || modelDimension(row.modelName, t).label.toLocaleLowerCase().includes(normalizedSearch)), [normalizedSearch, rows, t]);
  const aggregates = useMemo(() => sortAggregates(aggregatePresentationRows(
    filteredRows, (row) => modelDimension(row.modelName, t),
  ), preferences.metric), [filteredRows, preferences.metric, t]);
  const totalModelCount = useMemo(() => new Set(rows.map((row) => modelDimension(row.modelName, t).key)).size, [rows, t]);
  const filteredSummary = useMemo(() => filteredRows.reduce<DashboardSummary>((summary, row) => ({
    quota: summary.quota + row.quota,
    requests: summary.requests + row.requests,
    tokens: summary.tokens + row.tokens,
  }), { quota: 0, requests: 0, tokens: 0 }), [filteredRows]);
  const timelineDataset = useMemo(() => buildTimelineSeries(
    filteredRows,
    (row) => modelDimension('modelName' in row ? row.modelName : '', t),
    aggregates.slice(0, preferences.topLimit),
    preferences.granularity,
    query,
    t('Other models'),
  ), [aggregates, filteredRows, preferences.granularity, preferences.topLimit, query, t]);
  return (
    <section className="dashboard-analytics" aria-labelledby="dashboard-model-analytics-heading">
      <h3 className="sr-only" id="dashboard-model-analytics-heading">{t('Model analytics')}</h3>
      <AnalyticsControls
        label="Model chart preferences and filters"
        onChange={setPreferences}
        onSearch={setSearch}
        preferences={preferences}
        search={search}
      />
      {normalizedSearch && <p className="dashboard-filter-count muted" aria-live="polite">
        {t('Showing {{visible}} of {{total}} models. Filtered charts and table total {{quota}} quota, {{requests}} requests, and {{tokens}} tokens.', {
          visible: aggregates.length,
          total: totalModelCount,
          quota: formatNumber(filteredSummary.quota),
          requests: formatNumber(filteredSummary.requests),
          tokens: formatNumber(filteredSummary.tokens),
        })}
      </p>}
      {filteredRows.length === 0 ? (
        <p className="dashboard-client-empty muted" role="status">{t('No models match this filter.')}</p>
      ) : (
        <div className="dashboard-chart-grid">
          <TimelineChart
            dataset={timelineDataset}
            granularity={preferences.granularity}
            metric={preferences.metric}
            note={aggregates.length > 4
              ? t('Trend shows the four highest-ranked models and combines the remainder as Other models.')
              : undefined}
            title="Model usage over time"
          />
          <DistributionBars
            limit={preferences.topLimit}
            metric={preferences.metric}
            rows={aggregates}
            title="Model consumption distribution"
          />
          <div className="dashboard-chart-wide">
            <AggregateTable
              caption="Usage by model"
              labelHeading="Model"
              limit={preferences.topLimit}
              metric={preferences.metric}
              rows={aggregates}
            />
          </div>
        </div>
      )}
    </section>
  );
}

export function UsersPresentation({ rows, query }: { rows: DashboardUserRow[]; query: DashboardQuery }) {
  const { t } = useTranslation();
  const [preferences, setPreferences] = useAnalyticsPreferences('users');
  const aggregates = useMemo(() => sortAggregates(aggregatePresentationRows(
    rows, (row) => userDimension(row.username, t),
  ), preferences.metric), [preferences.metric, rows, t]);
  const timelineDataset = useMemo(() => buildTimelineSeries(
    rows,
    (row) => userDimension('username' in row ? row.username : '', t),
    aggregates.slice(0, preferences.topLimit),
    preferences.granularity,
    query,
  ), [aggregates, preferences.granularity, preferences.topLimit, query, rows, t]);
  return (
    <section className="dashboard-analytics" aria-labelledby="dashboard-user-analytics-heading">
      <h3 className="sr-only" id="dashboard-user-analytics-heading">{t('User analytics')}</h3>
      <AnalyticsControls
        label="User chart preferences"
        onChange={setPreferences}
        preferences={preferences}
      />
      <div className="dashboard-chart-grid">
        <DistributionBars
          limit={preferences.topLimit}
          metric={preferences.metric}
          rows={aggregates}
          title="User consumption ranking"
        />
        <TimelineChart
          dataset={timelineDataset}
          granularity={preferences.granularity}
          metric={preferences.metric}
          note={aggregates.length > 5 ? t('Trend shows the five highest-ranked users.') : undefined}
          title="User consumption trend"
        />
        <div className="dashboard-chart-wide">
          <AggregateTable
            caption="Usage by user"
            labelHeading="User"
            limit={preferences.topLimit}
            metric={preferences.metric}
            rows={aggregates}
          />
        </div>
      </div>
    </section>
  );
}

export function DashboardPresentation({ result, section, query, role }: {
  result: DashboardResult;
  section: DashboardSection;
  query: DashboardQuery;
  role: number;
}) {
  const { t } = useTranslation();
  const duration = query.endTimestamp - query.startTimestamp;
  const overviewAggregates = useMemo(() => result.kind === 'quota'
    ? aggregatePresentationRows(result.rows, (row) => modelDimension(row.modelName, t)).slice(0, 5)
    : [], [result, t]);
  const userCount = result.kind === 'users'
    ? new Set(result.rows.map((row) => userDimension(row.username, t).key)).size : 0;
  return (
    <>
      <SummaryCards
        durationSeconds={duration}
        extra={result.kind === 'users' ? [{ label: 'Active users', value: userCount }] : []}
        summary={result.summary}
      />
      {result.kind === 'flow' ? <FlowPresentation role={role} rows={result.rows} />
        : result.kind === 'users' ? <UsersPresentation query={query} rows={result.rows} />
          : section === 'models' ? <ModelsPresentation query={query} rows={result.rows} />
            : <AggregateTable rows={overviewAggregates} labelHeading="Model" caption="Top models" />}
      <p className="dashboard-scope-note muted">
        {t('All totals reflect only the selected date range and account scope.')}{' '}
        {t('Usage histograms may update with a short delay.')}
      </p>
    </>
  );
}
