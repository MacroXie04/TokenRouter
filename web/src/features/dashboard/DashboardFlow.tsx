import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  dashboardMetricValue,
  type DashboardFlowRow,
  type DashboardMetric
} from './dashboard-api';
import { MAX_PRESENTED_FLOW_ROWS, METRIC_LABELS, formatNumber, metricLabel, modelDimension } from './dashboard-presentation';

export type FlowNodeKind = 'user' | 'node' | 'token' | 'group' | 'model' | 'channel';

export interface FlowNode { kind: FlowNodeKind; key: string; label: string }

export interface FlowNodeOption extends FlowNode { value: number; alias: string }

export const FLOW_NODE_LABELS: Record<FlowNodeKind, string> = {
  user: 'User', node: 'Node', token: 'Token', group: 'Group', model: 'Model', channel: 'Channel',
};

export const FLOW_ROLE_STAGES: Record<1 | 10 | 100, FlowNodeKind[]> = {
  1: ['token', 'group', 'model'],
  10: ['user', 'group', 'model', 'channel'],
  100: ['user', 'node', 'token', 'group', 'model', 'channel'],
};

export function flowNodes(row: DashboardFlowRow, role: number,
  t: (key: string, values?: Record<string, unknown>) => string): FlowNode[] {
  const stages = FLOW_ROLE_STAGES[role as 1 | 10 | 100] ?? [];
  const candidates: Array<[FlowNodeKind, string, string]> = [
    ['user', row.username || (row.userId ? t('User #{{id}}', { id: row.userId }) : t('Unknown user')),
      row.userId ? `id:${row.userId}` : row.username ? `name:${row.username}` : 'unknown'],
    ['node', row.nodeName || t('Unknown node'), row.nodeName ? `name:${row.nodeName}` : 'unknown'],
    ['token', row.tokenName || (row.tokenId ? t('Token #{{id}}', { id: row.tokenId }) : t('Unknown token')),
      row.tokenId ? `id:${row.tokenId}` : row.tokenName ? `name:${row.tokenName}` : 'unknown'],
    ['group', row.group, `name:${row.group}`],
    ['model', modelDimension(row.modelName, t).label, row.modelName ? `name:${row.modelName}` : 'empty'],
    ['channel', row.channelName || (row.channelId ? t('Channel #{{id}}', { id: row.channelId }) : t('Unknown channel')),
      row.channelId ? `id:${row.channelId}` : row.channelName ? `name:${row.channelName}` : 'unknown'],
  ];
  return candidates.filter(([kind]) => stages.includes(kind))
    .map(([kind, label, identity]) => ({ kind, label, key: `${kind}\u0000${identity}` }));
}

export function flowNodeKindFromKey(key: string): FlowNodeKind {
  return key.slice(0, key.indexOf('\u0000')) as FlowNodeKind;
}

export function selectedFlowRows(rows: DashboardFlowRow[], selected: string[],
  role: number, t: (key: string, values?: Record<string, unknown>) => string, ignoredKind?: FlowNodeKind) {
  if (selected.length === 0) return rows;
  const selectedByKind = new Map<FlowNodeKind, Set<string>>();
  selected.forEach((key) => {
    const kind = flowNodeKindFromKey(key);
    if (kind === ignoredKind) return;
    const values = selectedByKind.get(kind) ?? new Set<string>();
    values.add(key);
    selectedByKind.set(kind, values);
  });
  return rows.filter((row) => {
    const keys = new Set(flowNodes(row, role, t).map((node) => node.key));
    return [...selectedByKind.values()].every((values) => [...values].some((key) => keys.has(key)));
  });
}

export function presentedFlowOptionLabel(option: FlowNodeOption, showSensitive: boolean): string {
  return showSensitive || option.kind === 'model' ? option.label : option.alias;
}

export function flowPath(row: DashboardFlowRow, role: number, options: Map<string, FlowNodeOption>, showSensitive: boolean,
  t: (key: string, values?: Record<string, unknown>) => string): string[] {
  return flowNodes(row, role, t).map((node) => {
    if (showSensitive || node.kind === 'model') return node.label;
    return options.get(node.key)?.alias ?? t('Hidden {{kind}}', { kind: t(FLOW_NODE_LABELS[node.kind]) });
  });
}

export function flowPathText(nodes: string[]): string {
  return nodes.join(' → ');
}

export function FlowTable({ rows, role, options, showSensitive }: {
  rows: DashboardFlowRow[];
  role: number;
  options: Map<string, FlowNodeOption>;
  showSensitive: boolean;
}) {
  const { t } = useTranslation();
  const visible = rows.slice(0, MAX_PRESENTED_FLOW_ROWS);
  return (
    <div className="table-scroll">
      <table className="dashboard-table dashboard-flow-table">
        <caption>{t('Traffic flow')}</caption>
        <thead><tr><th scope="col">{t('Path')}</th><th scope="col">{t('Quota')}</th>
          <th scope="col">{t('Requests')}</th><th scope="col">{t('Tokens')}</th></tr></thead>
        <tbody>{visible.map((row, index) => {
          const path = flowPathText(flowPath(row, role, options, showSensitive, t));
          return (
            <tr key={index}>
              <th scope="row" className="dashboard-flow-path">{path}</th>
              <td>{formatNumber(row.quota)}</td><td>{formatNumber(row.requests)}</td><td>{formatNumber(row.tokens)}</td>
            </tr>
          );
        })}</tbody>
      </table>
      {rows.length > visible.length && <p className="dashboard-limit-note muted">
        {t('Showing the top {{count}} flow rows.', { count: visible.length })}
      </p>}
    </div>
  );
}

export function FlowPresentation({ rows, role }: { rows: DashboardFlowRow[]; role: number }) {
  const { t } = useTranslation();
  const [metric, setMetric] = useState<DashboardMetric>('quota');
  const [showSensitive, setShowSensitive] = useState(false);
  const [selectedKind, setSelectedKind] = useState<FlowNodeKind>('model');
  const [selectedNodes, setSelectedNodes] = useState<string[]>([]);
  const [nodeSearch, setNodeSearch] = useState('');
  const options = useMemo(() => {
    const totals = new Map<string, FlowNodeOption>();
    const counters = new Map<FlowNodeKind, number>();
    rows.forEach((row) => flowNodes(row, role, t).forEach((node) => {
      const current = totals.get(node.key) ?? { ...node, value: 0, alias: '' };
      current.value += dashboardMetricValue(row, metric);
      totals.set(node.key, current);
    }));
    const withAliases = [...totals.values()]
      .sort((left, right) => left.kind.localeCompare(right.kind) || left.key.localeCompare(right.key))
      .map((option) => {
        if (option.kind === 'model') return { ...option, alias: option.label };
        const index = (counters.get(option.kind) ?? 0) + 1;
        counters.set(option.kind, index);
        return { ...option, alias: t('Hidden {{kind}} {{index}}', {
          kind: t(FLOW_NODE_LABELS[option.kind]), index,
        }) };
      });
    return withAliases.sort((left, right) => right.value - left.value || left.label.localeCompare(right.label));
  }, [metric, role, rows, t]);
  const optionMap = useMemo(() => new Map(options.map((option) => [option.key, option])), [options]);
  useEffect(() => {
    setSelectedNodes((current) => {
      const next = current.filter((key) => optionMap.has(key));
      return next.length === current.length ? current : next;
    });
  }, [optionMap]);
  const availableKinds = useMemo(() => (Object.keys(FLOW_NODE_LABELS) as FlowNodeKind[])
    .filter((kind) => options.some((option) => option.kind === kind)), [options]);
  useEffect(() => {
    if (!availableKinds.includes(selectedKind) && availableKinds[0]) setSelectedKind(availableKinds[0]);
  }, [availableKinds, selectedKind]);
  const filtered = useMemo(() => selectedFlowRows(rows, selectedNodes, role, t)
    .sort((left, right) => dashboardMetricValue(right, metric) - dashboardMetricValue(left, metric)),
  [metric, role, rows, selectedNodes, t]);
  const visible = filtered.slice(0, 20);
  const maximum = visible.reduce((value, row) => Math.max(value, dashboardMetricValue(row, metric)), 0) || 1;
  const filteredMetricTotal = filtered.reduce((total, row) => total + dashboardMetricValue(row, metric), 0);
  const normalizedNodeSearch = nodeSearch.trim().toLocaleLowerCase();
  const facetedRows = selectedFlowRows(rows, selectedNodes, role, t, selectedKind);
  const facetedTotals = new Map<string, number>();
  facetedRows.forEach((row) => flowNodes(row, role, t).forEach((node) => {
    if (node.kind === selectedKind) {
      facetedTotals.set(node.key, (facetedTotals.get(node.key) ?? 0) + dashboardMetricValue(row, metric));
    }
  }));
  const allKindOptions = options
    .filter((option) => option.kind === selectedKind
      && (facetedTotals.has(option.key) || selectedNodes.includes(option.key)))
    .map((option) => ({ ...option, value: facetedTotals.get(option.key) ?? 0 }))
    .sort((left, right) => right.value - left.value || left.label.localeCompare(right.label));
  const matchingKindOptions = allKindOptions.filter((option) => !normalizedNodeSearch
    || presentedFlowOptionLabel(option, showSensitive).toLocaleLowerCase().includes(normalizedNodeSearch));
  const pinnedKindOptions = allKindOptions.filter((option) => selectedNodes.includes(option.key));
  const kindOptions = [...matchingKindOptions.slice(0, 50)];
  pinnedKindOptions.forEach((option) => {
    if (!kindOptions.some((candidate) => candidate.key === option.key)) kindOptions.push(option);
  });
  const selectedOptions = selectedNodes
    .map((key) => optionMap.get(key))
    .filter((option): option is FlowNodeOption => option !== undefined);
  const tableVisibleCount = Math.min(filtered.length, MAX_PRESENTED_FLOW_ROWS);
  return (
    <section className="dashboard-flow-analysis" aria-labelledby="dashboard-flow-analysis-heading">
      <div className="dashboard-chart-heading dashboard-flow-heading">
        <div><h3 id="dashboard-flow-analysis-heading">{t('Traffic flow map')}</h3>
          <p className="muted">{t('Each lane follows one observed request path; bar length represents the selected metric.')}</p></div>
        <button
          aria-pressed={showSensitive}
          className="link"
          onClick={() => setShowSensitive((visibleValue) => !visibleValue)}
          type="button"
        >{showSensitive ? t('Hide sensitive labels') : t('Show sensitive labels')}</button>
      </div>
      <div className="dashboard-flow-controls">
        <label>{t('Metric')}<select value={metric} onChange={(event) => setMetric(event.target.value as DashboardMetric)}>
          {(Object.keys(METRIC_LABELS) as DashboardMetric[]).map((item) => (
            <option key={item} value={item}>{t(metricLabel(item))}</option>
          ))}
        </select></label>
        <label>{t('Node type')}<select value={selectedKind} onChange={(event) => setSelectedKind(event.target.value as FlowNodeKind)}>
          {availableKinds.map((kind) => <option key={kind} value={kind}>{t(FLOW_NODE_LABELS[kind])}</option>)}
        </select></label>
        <label>{t('Filter nodes')}<input
          autoComplete="off"
          maxLength={512}
          onChange={(event) => setNodeSearch(event.target.value)}
          placeholder={t('Search available nodes')}
          type="search"
          value={nodeSearch}
        /></label>
        {selectedNodes.length > 0 && <button className="link" type="button" onClick={() => setSelectedNodes([])}>
          {t('Clear node filters')}
        </button>}
      </div>
      {selectedOptions.length > 0 && <ul className="dashboard-active-filters" aria-label={t('Active node filters')}>
        {selectedOptions.map((option) => <li key={option.key}>
          <span>{t(FLOW_NODE_LABELS[option.kind])}:</span>
          <button
            aria-label={t('Remove {{kind}} filter {{name}}', {
              kind: t(FLOW_NODE_LABELS[option.kind]), name: presentedFlowOptionLabel(option, showSensitive),
            })}
            onClick={() => setSelectedNodes((current) => current.filter((key) => key !== option.key))}
            type="button"
          >{presentedFlowOptionLabel(option, showSensitive)} <span aria-hidden="true">×</span></button>
        </li>)}
      </ul>}
      <fieldset className="dashboard-node-filters">
        <legend>{t('Node filters')}</legend>
        {kindOptions.length === 0 ? <p className="muted">{t('No nodes are available.')}</p> : kindOptions.map((option) => (
          <label key={option.key}>
            <input
              checked={selectedNodes.includes(option.key)}
              onChange={() => setSelectedNodes((current) => current.includes(option.key)
                ? current.filter((key) => key !== option.key) : [...current, option.key])}
              type="checkbox"
            />
            <span>{presentedFlowOptionLabel(option, showSensitive)}</span>
            <small>{formatNumber(option.value)}</small>
          </label>
        ))}
        {matchingKindOptions.length > Math.min(matchingKindOptions.length, 50) && <p className="dashboard-limit-note muted">
          {t('Showing {{visible}} of {{total}} matching nodes. Search to narrow the list; selected nodes remain visible.', {
            visible: Math.min(matchingKindOptions.length, 50), total: matchingKindOptions.length,
          })}
        </p>}
      </fieldset>
      <p className="dashboard-filter-count muted" aria-live="polite">
        {t('Map shows {{visible}} of {{matched}} matching flow paths ({{total}} total); table shows {{tableVisible}}.', {
          visible: visible.length, matched: filtered.length, total: rows.length, tableVisible: tableVisibleCount,
        })}{' '}{t('{{value}} {{metric}} across matching paths.', {
          value: formatNumber(filteredMetricTotal), metric: t(metricLabel(metric)).toLocaleLowerCase(),
        })}
      </p>
      {!showSensitive && <p className="dashboard-filter-count muted">
        {t('Labels are hidden in this presentation; authorized network responses still contain the underlying values.')}
      </p>}
      {filtered.length === 0 ? <p className="dashboard-client-empty muted" role="status">
        {t('No flow paths match the selected nodes.')}
      </p> : <>
        <figure className="dashboard-flow-map">
          <figcaption className="sr-only">{t('Traffic flow map')}</figcaption>
          <ol>{visible.map((row, index) => {
            const pathNodes = flowPath(row, role, optionMap, showSensitive, t);
            const path = flowPathText(pathNodes);
            return <li key={index}>
              <div className="dashboard-flow-nodes" aria-label={path}>
                {pathNodes.map((node, nodeIndex) => <span key={nodeIndex}>{node}</span>)}
              </div>
              <div className="dashboard-flow-strength">
                <progress
                  aria-label={t('{{path}}: {{value}} {{metric}}', {
                    path, value: formatNumber(dashboardMetricValue(row, metric)),
                    metric: t(metricLabel(metric)).toLocaleLowerCase(),
                  })}
                  max={maximum}
                  value={dashboardMetricValue(row, metric)}
                />
                <output>{formatNumber(dashboardMetricValue(row, metric))}</output>
              </div>
            </li>;
          })}</ol>
        </figure>
        <FlowTable options={optionMap} role={role} rows={filtered} showSensitive={showSensitive} />
      </>}
    </section>
  );
}
