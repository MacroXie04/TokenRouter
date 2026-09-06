import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { User } from '../../api';
import {
  USAGE_LOG_PAGE_SIZES,
  emptyUsageLogQuery,
  loadUsageLogs,
  type CommonUsageLog,
  type DrawingUsageLog,
  type TaskUsageLog,
  type UsageLogItem,
  type UsageLogPageSize,
  type UsageLogQuery,
  type UsageLogResult,
  type UsageLogSection,
} from './usage-logs-api';

interface UsageLogsViewProps {
  user: User;
  section: UsageLogSection;
  onNavigate: (target: string) => void;
}

interface FilterDraft {
  type: string;
  model: string;
  token: string;
  group: string;
  username: string;
  channel: string;
  requestId: string;
  upstreamRequestId: string;
  identifier: string;
  platform: string;
  status: string;
  action: string;
  startTime: string;
  endTime: string;
  pageSize: UsageLogPageSize;
}

const EMPTY_DRAFT: FilterDraft = {
  type: '0',
  model: '',
  token: '',
  group: '',
  username: '',
  channel: '',
  requestId: '',
  upstreamRequestId: '',
  identifier: '',
  platform: '',
  status: '',
  action: '',
  startTime: '',
  endTime: '',
  pageSize: 20,
};

const SECTIONS: Array<{ id: UsageLogSection; label: string }> = [
  { id: 'common', label: 'Common' },
  { id: 'drawing', label: 'Drawing' },
  { id: 'task', label: 'Task' },
];

const LOG_TYPES = ['All types', 'Top-up', 'Consume', 'Manage', 'System', 'Error', 'Refund', 'Login'];
const TASK_STATUSES = ['', 'NOT_START', 'SUBMITTED', 'QUEUED', 'IN_PROGRESS', 'SUCCESS', 'FAILURE', 'UNKNOWN'];
const MAX_SECONDS = 4_102_444_800;

function dateInputTimestamp(value: string, milliseconds: boolean): number | undefined {
  if (!value) return undefined;
  if (value.length > 32) return undefined;
  const timestamp = new Date(value).getTime();
  if (!Number.isFinite(timestamp) || timestamp < 0 || timestamp > MAX_SECONDS * 1_000) return undefined;
  return milliseconds ? Math.floor(timestamp) : Math.floor(timestamp / 1_000);
}

function formatTimestamp(value: number, milliseconds: boolean): string {
  if (value <= 0) return '—';
  const date = new Date(milliseconds ? value : value * 1_000);
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString();
}

function statusClass(status: string): string {
  if (status === 'SUCCESS') return 'success';
  if (status === 'FAILURE') return 'failed';
  if (status === 'IN_PROGRESS' || status === 'SUBMITTED' || status === 'QUEUED') return 'pending';
  return 'neutral';
}

export function UsageLogsView({ user, section, onNavigate }: UsageLogsViewProps) {
  const { t } = useTranslation();
  const canManageScope = user.role >= 10;
  const [viewScope, setViewScope] = useState<'all' | 'self'>('all');
  const [sensitiveVisible, setSensitiveVisible] = useState(true);
  const isAdmin = canManageScope && viewScope === 'all';
  const [draft, setDraft] = useState<FilterDraft>(EMPTY_DRAFT);
  const [query, setQuery] = useState<UsageLogQuery>(emptyUsageLogQuery);
  const [result, setResult] = useState<UsageLogResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [filterError, setFilterError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [selectedDetail, setSelectedDetail] = useState<UsageLogItem | null>(null);
  const requestGeneration = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const generation = requestGeneration.current + 1;
    requestGeneration.current = generation;
    setLoading(true);
    setLoadError(false);
    void loadUsageLogs(section, isAdmin, query, controller.signal)
      .then((next) => {
        if (controller.signal.aborted || requestGeneration.current !== generation) return;
        setResult(next);
        const lastPage = Math.max(1, Math.ceil(next.total / next.pageSize));
        if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
      })
      .catch(() => {
        if (controller.signal.aborted || requestGeneration.current !== generation) return;
        setResult(null);
        setLoadError(true);
      })
      .finally(() => {
        if (!controller.signal.aborted && requestGeneration.current === generation) setLoading(false);
      });
    return () => controller.abort();
  }, [attempt, isAdmin, query, section]);

  useEffect(() => {
    setSelectedDetail(null);
  }, [isAdmin, query, section]);

  const pageCount = Math.max(1, Math.ceil((result?.total ?? 0) / query.pageSize));
  const sectionLabel = SECTIONS.find((item) => item.id === section)?.label ?? 'Common';

  const updateDraft = useCallback(<K extends keyof FilterDraft>(key: K, value: FilterDraft[K]) => {
    setDraft((current) => ({ ...current, [key]: value }));
  }, []);

  function applyFilters(event: React.FormEvent) {
    event.preventDefault();
    if (loading) return;
    const useMilliseconds = section === 'drawing';
    const startTimestamp = dateInputTimestamp(draft.startTime, useMilliseconds);
    const endTimestamp = dateInputTimestamp(draft.endTime, useMilliseconds);
    const invalidTime = (draft.startTime !== '' && startTimestamp === undefined)
      || (draft.endTime !== '' && endTimestamp === undefined)
      || (startTimestamp !== undefined && endTimestamp !== undefined && startTimestamp > endTimestamp);
    const invalidChannel = draft.channel !== ''
      && (!/^\d{1,10}$/u.test(draft.channel) || Number(draft.channel) < 1 || Number(draft.channel) > 2_147_483_647);
    if (invalidTime || invalidChannel) {
      setFilterError(true);
      return;
    }
    setFilterError(false);
    setQuery({
      page: 1,
      pageSize: draft.pageSize,
      type: Number(draft.type),
      model: draft.model,
      token: draft.token,
      group: draft.group,
      username: isAdmin ? draft.username : '',
      channel: isAdmin ? draft.channel : '',
      requestId: draft.requestId,
      upstreamRequestId: draft.upstreamRequestId,
      identifier: draft.identifier,
      platform: draft.platform,
      status: draft.status,
      action: draft.action,
      startTimestamp,
      endTimestamp,
    });
  }

  function clearFilters() {
    if (loading) return;
    setDraft(EMPTY_DRAFT);
    setFilterError(false);
    setQuery(emptyUsageLogQuery());
  }

  function changeScope(nextScope: 'all' | 'self') {
    if (loading || nextScope === viewScope) return;
    setDraft(EMPTY_DRAFT);
    setFilterError(false);
    setResult(null);
    setQuery(emptyUsageLogQuery());
    setViewScope(nextScope);
  }

  return (
    <main className="app usage-logs-page">
      <header className="header usage-logs-heading">
        <div>
          <h1>{t('Usage Logs')}</h1>
          <p className="tagline">{t('Review API consumption and asynchronous generation history.')}</p>
        </div>
        <a className="button" href="/dashboard">{t('Dashboard')}</a>
      </header>

      <nav className="usage-log-sections" aria-label={t('Usage log sections')}>
        {SECTIONS.map((item) => (
          <a
            aria-current={item.id === section ? 'page' : undefined}
            className={item.id === section ? 'active' : ''}
            href={`/usage-logs/${item.id}`}
            key={item.id}
            onClick={(event) => {
              event.preventDefault();
              if (item.id !== section) onNavigate(`/usage-logs/${item.id}`);
            }}
          >
            {t(item.label)}
          </a>
        ))}
      </nav>

      <section className="card usage-log-panel" aria-labelledby="usage-log-section-heading">
        <div className="usage-log-panel-heading">
          <div>
            <h2 id="usage-log-section-heading">{t('{{section}} logs', { section: t(sectionLabel) })}</h2>
            <p className="muted">{isAdmin ? t('Administrator scope') : t('Your account only')}</p>
          </div>
          <div className="usage-log-heading-actions">
            {canManageScope && (
              <div aria-label={t('Log scope')} className="usage-log-scope" role="group">
                <button
                  aria-pressed={viewScope === 'all'}
                  className="link"
                  disabled={loading}
                  onClick={() => changeScope('all')}
                  type="button"
                >
                  {t('All accounts')}
                </button>
                <button
                  aria-pressed={viewScope === 'self'}
                  className="link"
                  disabled={loading}
                  onClick={() => changeScope('self')}
                  type="button"
                >
                  {t('My account')}
                </button>
              </div>
            )}
            {section === 'common' && (
              <button
                aria-pressed={!sensitiveVisible}
                className="link"
                onClick={() => setSensitiveVisible((visible) => !visible)}
                type="button"
              >
                {sensitiveVisible ? t('Hide sensitive values') : t('Show sensitive values')}
              </button>
            )}
            <button
              className="link"
              disabled={loading}
              onClick={() => setAttempt((value) => value + 1)}
              type="button"
            >
              {loading ? t('Refreshing…') : t('Refresh')}
            </button>
          </div>
        </div>

        <UsageLogFilters
          draft={draft}
          filterError={filterError}
          isAdmin={isAdmin}
          loading={loading}
          onApply={applyFilters}
          onClear={clearFilters}
          section={section}
          update={updateDraft}
        />

        {section === 'common' && result?.stats && <Stats sensitiveVisible={sensitiveVisible} stats={result.stats} />}

        <div aria-busy={loading} aria-live="polite" className="usage-log-results">
          {loading && <p className="usage-log-state muted" role="status">{t('Loading usage logs…')}</p>}
          {!loading && loadError && (
            <div className="usage-log-state error-panel">
              <p role="alert">{t('Unable to load usage logs.')}</p>
              <button type="button" onClick={() => setAttempt((value) => value + 1)}>{t('Try again')}</button>
            </div>
          )}
          {!loading && !loadError && result && result.items.length === 0 && (
            <p className="usage-log-state muted">{t('No usage logs match these filters.')}</p>
          )}
          {!loading && !loadError && result && result.items.length > 0 && (
            <UsageLogTable
              isAdmin={isAdmin}
              items={result.items}
              onInspect={setSelectedDetail}
              section={section}
              sensitiveVisible={sensitiveVisible}
            />
          )}
        </div>

        {!loading && !loadError && result && (
          <div className="usage-log-pagination" aria-label={t('Pagination')}>
            <span>{t('Showing {{count}} of {{total}}', { count: result.items.length, total: result.total })}</span>
            <button
              className="link"
              disabled={query.page <= 1}
              onClick={() => setQuery((current) => ({ ...current, page: current.page - 1 }))}
              type="button"
            >
              {t('Previous')}
            </button>
            <span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span>
            <button
              className="link"
              disabled={query.page >= pageCount}
              onClick={() => setQuery((current) => ({ ...current, page: current.page + 1 }))}
              type="button"
            >
              {t('Next')}
            </button>
          </div>
        )}
      </section>
      {selectedDetail && (
        <UsageLogDetailDialog
          item={selectedDetail}
          sensitiveVisible={sensitiveVisible}
          onClose={() => setSelectedDetail(null)}
        />
      )}
    </main>
  );
}

function UsageLogFilters({
  draft,
  filterError,
  isAdmin,
  loading,
  onApply,
  onClear,
  section,
  update,
}: {
  draft: FilterDraft;
  filterError: boolean;
  isAdmin: boolean;
  loading: boolean;
  onApply: (event: React.FormEvent) => void;
  onClear: () => void;
  section: UsageLogSection;
  update: <K extends keyof FilterDraft>(key: K, value: FilterDraft[K]) => void;
}) {
  const { t } = useTranslation();
  return (
    <form aria-label={t('Filter usage logs')} className="usage-log-filters" onSubmit={onApply} role="search">
      {section === 'common' && (
        <>
          <label>{t('Type')}
            <select value={draft.type} onChange={(event) => update('type', event.target.value)}>
              {LOG_TYPES.map((label, value) => <option key={label} value={value}>{t(label)}</option>)}
            </select>
          </label>
          <TextFilter label={t('Model')} maximum={255} value={draft.model} onChange={(value) => update('model', value)} />
          <TextFilter label={t('Token name')} maximum={64} value={draft.token} onChange={(value) => update('token', value)} />
          <TextFilter label={t('Group')} maximum={512} value={draft.group} onChange={(value) => update('group', value)} />
          <TextFilter label={t('Request ID')} maximum={128} value={draft.requestId} onChange={(value) => update('requestId', value)} />
          <TextFilter label={t('Upstream request ID')} maximum={128} value={draft.upstreamRequestId} onChange={(value) => update('upstreamRequestId', value)} />
          {isAdmin && <TextFilter label={t('Username')} maximum={64} value={draft.username} onChange={(value) => update('username', value)} />}
        </>
      )}
      {section === 'drawing' && (
        <TextFilter label={t('Drawing ID')} maximum={191} value={draft.identifier} onChange={(value) => update('identifier', value)} />
      )}
      {section === 'task' && (
        <>
          <TextFilter label={t('Task ID')} maximum={191} value={draft.identifier} onChange={(value) => update('identifier', value)} />
          <TextFilter label={t('Platform')} maximum={30} value={draft.platform} onChange={(value) => update('platform', value)} />
          <label>{t('Status')}
            <select value={draft.status} onChange={(event) => update('status', event.target.value)}>
              {TASK_STATUSES.map((status) => <option key={status || 'all'} value={status}>{status || t('All statuses')}</option>)}
            </select>
          </label>
          <TextFilter label={t('Action')} maximum={40} value={draft.action} onChange={(value) => update('action', value)} />
        </>
      )}
      {isAdmin && <TextFilter inputMode="numeric" label={t('Channel ID')} maximum={10} value={draft.channel} onChange={(value) => update('channel', value)} />}
      <label>{t('Start time')}
        <input type="datetime-local" value={draft.startTime} onChange={(event) => update('startTime', event.target.value)} />
      </label>
      <label>{t('End time')}
        <input type="datetime-local" value={draft.endTime} onChange={(event) => update('endTime', event.target.value)} />
      </label>
      <label>{t('Page size')}
        <select value={draft.pageSize} onChange={(event) => update('pageSize', Number(event.target.value) as UsageLogPageSize)}>
          {USAGE_LOG_PAGE_SIZES.map((size) => <option key={size} value={size}>{size}</option>)}
        </select>
      </label>
      <div className="usage-log-filter-actions">
        <button disabled={loading} type="submit">{t('Apply filters')}</button>
        <button className="link" disabled={loading} onClick={onClear} type="button">{t('Clear')}</button>
      </div>
      {filterError && <p className="error usage-log-filter-error" role="alert">{t('Check the time range and channel ID.')}</p>}
    </form>
  );
}

function TextFilter({
  inputMode,
  label,
  maximum,
  onChange,
  value,
}: {
  inputMode?: 'numeric';
  label: string;
  maximum: number;
  onChange: (value: string) => void;
  value: string;
}) {
  return <label>{label}<input inputMode={inputMode} maxLength={maximum} value={value} onChange={(event) => onChange(event.target.value)} /></label>;
}

function hiddenValue(value: string | number, visible: boolean): string {
  if (!visible) return '••••••';
  return typeof value === 'number' ? value.toLocaleString() : value || '—';
}

function Stats({ sensitiveVisible, stats }: {
  sensitiveVisible: boolean;
  stats: NonNullable<UsageLogResult['stats']>;
}) {
  const { t } = useTranslation();
  return (
    <dl className="usage-log-stats" aria-label={t('Common usage statistics')}>
      <div><dt>{t('Quota')}</dt><dd>{hiddenValue(stats.quota, sensitiveVisible)}</dd></div>
      <div><dt>{t('RPM')}</dt><dd>{stats.rpm.toLocaleString()}</dd></div>
      <div><dt>{t('TPM')}</dt><dd>{stats.tpm.toLocaleString()}</dd></div>
    </dl>
  );
}

function UsageLogTable({ items, isAdmin, onInspect, section, sensitiveVisible }: {
  items: UsageLogItem[];
  isAdmin: boolean;
  onInspect: (item: UsageLogItem) => void;
  section: UsageLogSection;
  sensitiveVisible: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className="table-scroll">
      <table className="usage-log-table">
        <caption className="sr-only">{t('{{section}} logs', { section: t(SECTIONS.find((item) => item.id === section)?.label ?? 'Common') })}</caption>
        {section === 'common' && (
          <CommonTableBody items={items as CommonUsageLog[]} isAdmin={isAdmin} onInspect={onInspect} sensitiveVisible={sensitiveVisible} />
        )}
        {section === 'drawing' && <DrawingTableBody items={items as DrawingUsageLog[]} isAdmin={isAdmin} onInspect={onInspect} />}
        {section === 'task' && <TaskTableBody items={items as TaskUsageLog[]} isAdmin={isAdmin} onInspect={onInspect} />}
      </table>
    </div>
  );
}

function CommonTableBody({ items, isAdmin, onInspect, sensitiveVisible }: {
  items: CommonUsageLog[];
  isAdmin: boolean;
  onInspect: (item: CommonUsageLog) => void;
  sensitiveVisible: boolean;
}) {
  const { t } = useTranslation();
  return (
    <>
      <thead><tr>
        <th>{t('Time')}</th><th>{t('Type')}</th>
        {isAdmin && <th>{t('User')}</th>}
        <th>{t('Model')}</th><th>{t('Token name')}</th><th>{t('Tokens')}</th><th>{t('Quota')}</th>
        {isAdmin && <th>{t('Channel')}</th>}
        <th>{t('Request ID')}</th><th>{t('Details')}</th>
      </tr></thead>
      <tbody>{items.map((item) => (
        <tr key={`common:${item.id}`}>
          <td><time dateTime={new Date(item.createdAt * 1_000).toISOString()}>{formatTimestamp(item.createdAt, false)}</time></td>
          <td>{t(LOG_TYPES[item.type] ?? 'Unknown')}</td>
          {isAdmin && <td>{hiddenValue(item.username, sensitiveVisible)}</td>}
          <td>{item.modelName || '—'}<small className="usage-log-meta">{hiddenValue(item.group, sensitiveVisible)}</small></td>
          <td>{hiddenValue(item.tokenName, sensitiveVisible)}</td>
          <td>{item.promptTokens.toLocaleString()} / {item.completionTokens.toLocaleString()}</td>
          <td>{item.quota.toLocaleString()}<small className="usage-log-meta">{item.useTime} ms · {item.streamed ? t('Stream') : t('Standard')}</small></td>
          {isAdmin && <td>{item.channelName || item.channelId || '—'}</td>}
          <td><code>{item.requestId || '—'}</code></td>
          <td><button className="link" type="button" onClick={() => onInspect(item)}>{t('View details')}</button></td>
        </tr>
      ))}</tbody>
    </>
  );
}

function DrawingTableBody({ items, isAdmin, onInspect }: {
  items: DrawingUsageLog[];
  isAdmin: boolean;
  onInspect: (item: DrawingUsageLog) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      <thead><tr>
        <th>{t('Time')}</th><th>{t('Drawing ID')}</th>
        {isAdmin && <><th>{t('User')}</th><th>{t('Channel')}</th></>}
        <th>{t('Action')}</th><th>{t('Status')}</th><th>{t('Quota')}</th><th>{t('Result')}</th><th>{t('Details')}</th>
      </tr></thead>
      <tbody>{items.map((item) => (
        <tr key={`drawing:${item.id}`}>
          <td>{formatTimestamp(item.submitTime, true)}</td>
          <td><code>{item.drawingId}</code></td>
          {isAdmin && <><td>{item.userId}</td><td>{item.channelId || '—'}</td></>}
          <td>{item.action}</td>
          <td><span className={`status-pill ${statusClass(item.status)}`}>{item.status}</span><small className="usage-log-meta">{item.progress || '—'}</small></td>
          <td>{item.quota.toLocaleString()}</td>
          <td>{item.contentURL ? <a href={item.contentURL}>{t('View result')}</a> : '—'}</td>
          <td><button className="link" type="button" onClick={() => onInspect(item)}>{t('View details')}</button></td>
        </tr>
      ))}</tbody>
    </>
  );
}

function TaskTableBody({ items, isAdmin, onInspect }: {
  items: TaskUsageLog[];
  isAdmin: boolean;
  onInspect: (item: TaskUsageLog) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      <thead><tr>
        <th>{t('Time')}</th><th>{t('Task ID')}</th>
        {isAdmin && <><th>{t('User')}</th><th>{t('Channel')}</th></>}
        <th>{t('Platform')}</th><th>{t('Action')}</th><th>{t('Status')}</th><th>{t('Quota')}</th><th>{t('Result')}</th><th>{t('Details')}</th>
      </tr></thead>
      <tbody>{items.map((item) => (
        <tr key={`task:${item.id}`}>
          <td>{formatTimestamp(item.submitTime, false)}</td>
          <td><code>{item.taskId}</code></td>
          {isAdmin && <><td>{item.username || item.userId}</td><td>{item.channelId || '—'}</td></>}
          <td>{item.platform}</td><td>{item.action}</td>
          <td><span className={`status-pill ${statusClass(item.status)}`}>{item.status}</span><small className="usage-log-meta">{item.progress || '—'}</small></td>
          <td>{item.quota.toLocaleString()}</td>
          <td>{item.contentURL ? <a href={item.contentURL}>{t('View content')}</a> : '—'}</td>
          <td><button className="link" type="button" onClick={() => onInspect(item)}>{t('View details')}</button></td>
        </tr>
      ))}</tbody>
    </>
  );
}

function UsageLogDetailDialog({ item, onClose, sensitiveVisible }: {
  item: UsageLogItem;
  onClose: () => void;
  sensitiveVisible: boolean;
}) {
  const { t } = useTranslation();
  const title = item.kind === 'common'
    ? t('Usage log details')
    : item.kind === 'drawing'
      ? t('Drawing details')
      : t('Task details');
  return (
    <div
      className="modal-overlay"
      onKeyDown={(event) => {
        if (event.key === 'Escape') onClose();
      }}
      onMouseDown={(event) => {
        if (event.currentTarget === event.target) onClose();
      }}
      role="presentation"
    >
      <section aria-labelledby="usage-log-detail-title" aria-modal="true" className="modal" role="dialog">
        <h2 id="usage-log-detail-title">{title}</h2>
        {item.kind === 'common' && (
          <dl>
            <DetailLine label={t('Time')} value={formatTimestamp(item.createdAt, false)} />
            <DetailLine label={t('User')} value={hiddenValue(item.username || item.userId, sensitiveVisible)} />
            <DetailLine label={t('Model')} value={item.modelName || '—'} />
            <DetailLine label={t('Upstream model')} value={item.billing?.upstreamModelName || '—'} />
            <DetailLine label={t('Token name')} value={hiddenValue(item.tokenName, sensitiveVisible)} />
            <DetailLine label={t('Group')} value={hiddenValue(item.group, sensitiveVisible)} />
            <DetailLine label={t('Channel')} value={hiddenValue(item.channelName || item.channelId, sensitiveVisible)} />
            <DetailLine label={t('Request ID')} value={hiddenValue(item.requestId, sensitiveVisible)} mono />
            <DetailLine label={t('Upstream request ID')} value={hiddenValue(item.upstreamRequestId, sensitiveVisible)} mono />
            <DetailLine label={t('IP address')} value={hiddenValue(item.ip, sensitiveVisible)} mono />
            <DetailLine label={t('Prompt tokens')} value={item.promptTokens.toLocaleString()} />
            <DetailLine label={t('Completion tokens')} value={item.completionTokens.toLocaleString()} />
            <DetailLine label={t('Cache tokens')} value={(item.billing?.cacheTokens ?? 0).toLocaleString()} />
            <DetailLine label={t('First response time')} value={item.billing?.firstResponseTime
              ? `${item.billing.firstResponseTime.toLocaleString()} ms`
              : '—'} />
            <DetailLine label={t('Total time')} value={`${item.useTime.toLocaleString()} ms`} />
            <DetailLine label={t('Billing mode')} value={item.billing?.billingMode || item.billing?.billingSource || '—'} />
            <DetailLine label={t('Matched tier')} value={item.billing?.matchedTier || '—'} />
            <DetailLine label={t('Ratios')} value={[
              item.billing?.modelRatio,
              item.billing?.completionRatio,
              item.billing?.userGroupRatio ?? item.billing?.groupRatio,
            ].map((value) => value === null || value === undefined ? '—' : `${value}×`).join(' / ')} />
          </dl>
        )}
        {item.kind === 'drawing' && (
          <>
            <dl>
              <DetailLine label={t('Drawing ID')} value={item.drawingId} mono />
              <DetailLine label={t('Status')} value={`${item.status}${item.progress ? ` · ${item.progress}` : ''}`} />
              <DetailLine label={t('Submitted')} value={formatTimestamp(item.submitTime, true)} />
              <DetailLine label={t('Started')} value={formatTimestamp(item.startTime, true)} />
              <DetailLine label={t('Finished')} value={formatTimestamp(item.finishTime, true)} />
            </dl>
            {item.contentURL && <img alt={t('Generated image')} loading="lazy" src={item.contentURL} />}
            {item.prompt && <DetailText label={t('Prompt')} value={item.prompt} />}
            {item.promptEnglish && <DetailText label={t('Prompt (EN)')} value={item.promptEnglish} />}
            {item.failReason && <DetailText label={t('Fail reason')} value={item.failReason} />}
          </>
        )}
        {item.kind === 'task' && (
          <>
            <dl>
              <DetailLine label={t('Task ID')} value={item.taskId} mono />
              <DetailLine label={t('Platform')} value={item.platform} />
              <DetailLine label={t('Action')} value={item.action} />
              <DetailLine label={t('Status')} value={`${item.status}${item.progress ? ` · ${item.progress}` : ''}`} />
              <DetailLine label={t('Origin model')} value={item.originModelName || '—'} />
              <DetailLine label={t('Upstream model')} value={item.upstreamModelName || '—'} />
              <DetailLine label={t('Submitted')} value={formatTimestamp(item.submitTime, false)} />
              <DetailLine label={t('Started')} value={formatTimestamp(item.startTime, false)} />
              <DetailLine label={t('Finished')} value={formatTimestamp(item.finishTime, false)} />
            </dl>
            {item.contentURL && <a href={item.contentURL}>{t('View content')}</a>}
            {item.input && <DetailText label={t('Input')} value={item.input} />}
            {item.failReason && <DetailText label={t('Fail reason')} value={item.failReason} />}
          </>
        )}
        {item.kind === 'common' && item.content && (
          <DetailText label={t('Log content')} value={hiddenValue(item.content, sensitiveVisible)} />
        )}
        <div className="modal-actions">
          <button autoFocus type="button" onClick={onClose}>{t('Close')}</button>
        </div>
      </section>
    </div>
  );
}

function DetailLine({ label, mono = false, value }: { label: string; mono?: boolean; value: string | number }) {
  return <div><dt>{label}</dt><dd className={mono ? 'mono' : undefined}>{value}</dd></div>;
}

function DetailText({ label, value }: { label: string; value: string }) {
  return <div><strong>{label}</strong><pre className="play-response">{value}</pre></div>;
}
