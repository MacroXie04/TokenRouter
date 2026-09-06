import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { PublicNotice } from '../features/home/PublicNotice';
import {
  PerformanceMetricsPanel,
  PerformanceSummaryBadge,
  loadPerformanceSummary,
  type PerformanceModelSummary,
} from '../features/performance-metrics';
import { DynamicBillingExplanation } from '../features/pricing/DynamicBillingExplanation';
import { isDynamicPricingItem } from '../features/pricing/billing-expression';
import {
  filterPricingItems,
  itemModalities,
  MAX_PRICING_MODEL_NAME_LENGTH,
  ownerName,
  parsePricingQuery,
  pricingItemTags,
  PRICING_PAGE_SIZE,
  pricingQueryString,
  type PricingCatalog,
  type PricingCatalogItem,
  type PricingModality,
  type PricingModalityFilter,
  type PricingQuotaType,
  type PricingQueryState,
  type PricingSort,
} from '../features/pricing/catalog';
import { loadPricingCatalog } from '../features/pricing/pricing-api';
import '../features/pricing/pricing.css';

type CatalogState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; catalog: PricingCatalog };

const MODALITY_LABELS: Record<PricingModality, string> = {
  text: 'Text / chat',
  image: 'Image',
  audio: 'Audio',
  video: 'Video',
  embedding: 'Embeddings',
  rerank: 'Reranking',
};

function initialQuery(): PricingQueryState {
  return parsePricingQuery(typeof window === 'undefined' ? '' : window.location.search);
}

function safeDetailModel(value: string | undefined): string | null {
  if (value === undefined) return null;
  if (!value || new TextEncoder().encode(value).byteLength > MAX_PRICING_MODEL_NAME_LENGTH || value !== value.trim()) return '';
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)) return '';
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return '';
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) return '';
  }
  return value;
}

function formatPrice(value: number): string {
  return new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 6 }).format(value);
}

function effectiveQuery(query: PricingQueryState, catalog: PricingCatalog): PricingQueryState {
  const groups = new Set(Object.keys(catalog.usableGroup));
  const vendors = new Set(catalog.items.map((item) => ownerName(item, catalog.vendors)));
  const endpointTypes = new Set(Object.keys(catalog.supportedEndpoint));
  const modalities = new Set(catalog.items.flatMap(itemModalities));
  const itemTags = new Set(catalog.items.flatMap(pricingItemTags));
  return {
    ...query,
    group: query.group && groups.has(query.group) ? query.group : '',
    vendor: query.vendor && vendors.has(query.vendor) ? query.vendor : '',
    modality: query.modality && modalities.has(query.modality) ? query.modality : '',
    endpointType: query.endpointType && endpointTypes.has(query.endpointType) ? query.endpointType : '',
    tag: query.tag && itemTags.has(query.tag) ? query.tag : '',
  };
}

function modelHref(modelName: string, query: PricingQueryState): string {
  return `/pricing/${encodeURIComponent(modelName)}${pricingQueryString({ ...query, page: 1 })}`;
}

export function PricingView({ selectedModel }: { selectedModel?: string }) {
  const { t } = useTranslation();
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<CatalogState>({ status: 'loading' });
  const [query, setQuery] = useState<PricingQueryState>(initialQuery);
  const detailModel = safeDetailModel(selectedModel);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setState({ status: 'loading' });
    loadPricingCatalog(controller.signal)
      .then((catalog) => {
        if (active) setState({ status: 'ready', catalog });
      })
      .catch(() => {
        if (active && !controller.signal.aborted) setState({ status: 'error' });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [attempt]);

  useEffect(() => {
    const handlePopState = () => setQuery(parsePricingQuery(window.location.search));
    window.addEventListener('popstate', handlePopState);
    return () => window.removeEventListener('popstate', handlePopState);
  }, []);

  useEffect(() => {
    const normalized = pricingQueryString(query);
    if (window.location.search !== normalized) {
      window.history.replaceState(null, '', `${window.location.pathname}${normalized}${window.location.hash}`);
    }
  }, [query]);

  useEffect(() => {
    if (state.status !== 'ready') return;
    const normalized = effectiveQuery(query, state.catalog);
    if (
      normalized.group !== query.group
      || normalized.vendor !== query.vendor
      || normalized.modality !== query.modality
      || normalized.endpointType !== query.endpointType
      || normalized.tag !== query.tag
    ) setQuery(normalized);
  }, [query, state]);

  const updateQuery = useCallback((updates: Partial<PricingQueryState>, resetPage = true) => {
    setQuery((previous) => {
      const candidate = {
        ...previous,
        ...updates,
        page: resetPage ? 1 : updates.page ?? previous.page,
      };
      return parsePricingQuery(pricingQueryString(candidate));
    });
  }, []);

  return (
    <main className="app pricing-page">
      <header className="header row pricing-heading">
        <div>
          <h1>{detailModel === null ? t('Pricing') : t('Model details')}</h1>
          <p className="tagline">
            {detailModel === null ? t('Compare the live model catalog and its published prices.') : t('Pricing, routes, groups, and measured performance.')}
          </p>
        </div>
        <a className="button" href={detailModel === null ? '/' : `/pricing${pricingQueryString(query)}`}>
          {detailModel === null ? t('Back to home') : t('Back to pricing')}
        </a>
      </header>
      <PublicNotice />

      {state.status === 'loading' && (
        <section className="card pricing-state" aria-busy="true">
          <p className="muted" role="status">{t('Loading pricing…')}</p>
        </section>
      )}
      {state.status === 'error' && (
        <section className="card pricing-state">
          <h2>{t('Unable to load pricing')}</h2>
          <p className="error" role="alert">{t('Unable to load pricing.')}</p>
          <button type="button" onClick={() => setAttempt((value) => value + 1)}>{t('Try again')}</button>
        </section>
      )}
      {state.status === 'ready' && detailModel !== null && (
        <PricingDetail
          catalog={state.catalog}
          modelName={detailModel}
          selectedGroup={effectiveQuery(query, state.catalog).group}
        />
      )}
      {state.status === 'ready' && detailModel === null && (
        <PricingCatalogView catalog={state.catalog} query={query} updateQuery={updateQuery} />
      )}
    </main>
  );
}

function PricingCatalogView({ catalog, query, updateQuery }: {
  catalog: PricingCatalog;
  query: PricingQueryState;
  updateQuery: (updates: Partial<PricingQueryState>, resetPage?: boolean) => void;
}) {
  const { t } = useTranslation();
  const [performanceByModel, setPerformanceByModel] = useState<ReadonlyMap<string, PerformanceModelSummary>>(
    () => new Map(),
  );
  const activeQuery = effectiveQuery(query, catalog);
  const rows = useMemo(
    () => filterPricingItems(catalog.items, catalog.vendors, activeQuery),
    [activeQuery, catalog.items, catalog.vendors],
  );
  const groups = useMemo(() => Object.keys(catalog.usableGroup).sort(), [catalog.usableGroup]);
  const vendors = useMemo(
    () => [...new Set(catalog.items.map((item) => ownerName(item, catalog.vendors)))].sort(),
    [catalog.items, catalog.vendors],
  );
  const endpointTypes = useMemo(
    () => [...new Set(catalog.items.flatMap((item) => item.supported_endpoint_types))].sort(),
    [catalog.items],
  );
  const modalities = useMemo(
    () => [...new Set(catalog.items.flatMap(itemModalities))].sort(),
    [catalog.items],
  );
  const availableTags = useMemo(() => [...new Set(catalog.items.flatMap(pricingItemTags))].sort(), [catalog.items]);
  const totalPages = Math.max(1, Math.ceil(rows.length / PRICING_PAGE_SIZE));
  const page = Math.min(activeQuery.page, totalPages);
  const visibleRows = rows.slice((page - 1) * PRICING_PAGE_SIZE, page * PRICING_PAGE_SIZE);
  const selectedRatio = activeQuery.group ? catalog.groupRatio[activeQuery.group] ?? 1 : 1;

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    loadPerformanceSummary(24, controller.signal)
      .then((summary) => {
        if (active) setPerformanceByModel(new Map(summary.models.map((model) => [model.model_name, model])));
      })
      .catch(() => {
        if (active && !controller.signal.aborted) setPerformanceByModel(new Map());
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, []);

  const clearFilters = () => updateQuery({
    search: '', sort: 'name', vendor: '', group: '', quotaType: '', modality: '', endpointType: '', tag: '', page: 1,
  });

  useEffect(() => {
    if (activeQuery.page !== page) updateQuery({ page }, false);
  }, [activeQuery.page, page, updateQuery]);

  return (
    <section className="pricing-catalog" aria-labelledby="pricing-results-heading">
      <div className="card pricing-controls">
        <label className="pricing-search">
          {t('Search models')}
          <input
            type="search"
            value={activeQuery.search}
            maxLength={200}
            onChange={(event) => updateQuery({ search: event.target.value })}
            placeholder={t('Name, description, tag, or owner')}
          />
        </label>
        <div className="pricing-filter-grid">
          <label>{t('Access group')}
            <select value={activeQuery.group} onChange={(event) => updateQuery({ group: event.target.value })}>
              <option value="">{t('All groups')}</option>
              {groups.map((group) => <option value={group} key={group}>{catalog.usableGroup[group] || group}</option>)}
            </select>
          </label>
          <label>{t('Owner')}
            <select value={activeQuery.vendor} onChange={(event) => updateQuery({ vendor: event.target.value })}>
              <option value="">{t('All vendors')}</option>
              {vendors.map((vendor) => <option value={vendor} key={vendor}>{vendor}</option>)}
            </select>
          </label>
          <label>{t('Modalities')}
            <select value={activeQuery.modality} onChange={(event) => updateQuery({ modality: event.target.value as PricingModalityFilter })}>
              <option value="">{t('All modalities')}</option>
              {modalities.map((modality) => <option value={modality} key={modality}>{t(MODALITY_LABELS[modality])}</option>)}
            </select>
          </label>
          <label>{t('Model pricing')}
            <select value={activeQuery.quotaType} onChange={(event) => updateQuery({ quotaType: event.target.value as PricingQuotaType })}>
              <option value="">{t('All models')}</option>
              <option value="token">{t('Per token')}</option>
              <option value="request">{t('Per request')}</option>
            </select>
          </label>
          <label>{t('Type')}
            <select value={activeQuery.endpointType} onChange={(event) => updateQuery({ endpointType: event.target.value })}>
              <option value="">{t('All types')}</option>
              {endpointTypes.map((type) => <option value={type} key={type}>{type}</option>)}
            </select>
          </label>
          <label>{t('Tag')}
            <select value={activeQuery.tag} onChange={(event) => updateQuery({ tag: event.target.value })}>
              <option value="">{t('Any')}</option>
              {availableTags.map((tag) => <option value={tag} key={tag}>{tag}</option>)}
            </select>
          </label>
          <label>{t('Sort by')}
            <select value={activeQuery.sort} onChange={(event) => updateQuery({ sort: event.target.value as PricingSort })}>
              <option value="name">{t('Name')}</option>
              <option value="price-low">{t('Price (USD)')} ↑</option>
              <option value="price-high">{t('Price (USD)')} ↓</option>
            </select>
          </label>
        </div>
        <p className="pricing-filter-note">{t('Filter by the live groups, vendors, tags, and API routes advertised by the gateway.')}</p>
        <div className="pricing-toolbar">
          <p aria-live="polite" id="pricing-results-heading">
            {t('{{shown}} of {{total}} models', { shown: rows.length, total: catalog.items.length })}
          </p>
          <div role="group" aria-label={t('View')} className="pricing-view-switch">
            <button aria-pressed={activeQuery.view === 'card'} type="button" onClick={() => updateQuery({ view: 'card' })}>{t('Cards')}</button>
            <button aria-pressed={activeQuery.view === 'table'} type="button" onClick={() => updateQuery({ view: 'table' })}>{t('Table')}</button>
          </div>
          {(activeQuery.search || activeQuery.sort !== 'name' || activeQuery.group || activeQuery.vendor || activeQuery.modality
            || activeQuery.quotaType || activeQuery.endpointType || activeQuery.tag) && (
            <button className="link" type="button" onClick={clearFilters}>{t('Clear filters')}</button>
          )}
        </div>
      </div>

      {rows.length === 0 && <div className="card pricing-state"><p className="muted">{t('No matching models.')}</p></div>}
      {rows.length > 0 && activeQuery.view === 'card' && (
        <div className="pricing-card-grid" aria-label={t('Pricing cards')} role="list">
          {visibleRows.map((item) => (
            <ModelCard
              catalog={catalog}
              item={item}
              query={activeQuery}
              ratio={selectedRatio}
              performance={performanceByModel.get(item.model_name)}
              key={item.model_name}
            />
          ))}
        </div>
      )}
      {rows.length > 0 && activeQuery.view === 'table' && (
        <PricingTable
          catalog={catalog}
          items={visibleRows}
          query={activeQuery}
          ratio={selectedRatio}
          performanceByModel={performanceByModel}
        />
      )}
      {rows.length > 0 && (
        <nav className="pricing-pagination" aria-label={t('Pricing pages')}>
          <button type="button" disabled={page <= 1} onClick={() => updateQuery({ page: page - 1 }, false)}>{t('Previous')}</button>
          <span>{t('Page {{page}} of {{pages}}', { page, pages: totalPages })}</span>
          <button type="button" disabled={page >= totalPages} onClick={() => updateQuery({ page: page + 1 }, false)}>{t('Next')}</button>
        </nav>
      )}
    </section>
  );
}

function ModelCard({ catalog, item, query, ratio, performance }: {
  catalog: PricingCatalog;
  item: PricingCatalogItem;
  query: PricingQueryState;
  ratio: number;
  performance?: PerformanceModelSummary;
}) {
  const { t } = useTranslation();
  const owner = ownerName(item, catalog.vendors);
  const modalities = itemModalities(item);
  const itemTags = pricingItemTags(item);
  const dynamic = isDynamicPricingItem(item);
  return (
    <article className="card pricing-model-card" role="listitem">
      <header>
        <div><p>{owner}</p><h2><a href={modelHref(item.model_name, query)}>{item.model_name}</a></h2></div>
        <span className="pricing-type">{dynamic ? 'tiered_expr' : item.quota_type === 0 ? t('Per token') : t('Per request')}</span>
      </header>
      {item.description && <p className="pricing-description">{item.description}</p>}
      {dynamic ? (
        <p className="pricing-dynamic-summary"><span>{t('Billing mode')}</span><code>tiered_expr</code></p>
      ) : (
        <dl className="pricing-price-grid">
          {item.quota_type === 0 ? (
          <>
            <div><dt>{t('Input / 1M')}</dt><dd>${formatPrice(item.prompt_price * ratio)}</dd></div>
            <div><dt>{t('Output / 1M')}</dt><dd>${formatPrice(item.completion_price * ratio)}</dd></div>
          </>
          ) : <div><dt>{t('Per request')}</dt><dd>${formatPrice(item.model_price * ratio)}</dd></div>}
        </dl>
      )}
      <PerformanceSummaryBadge summary={performance} />
      <div className="pricing-pills" aria-label={t('Model metadata')}>
        {modalities.map((modality) => <span key={modality}>{t(MODALITY_LABELS[modality])}</span>)}
        {item.enable_groups.slice(0, 4).map((group) => <span key={group}>{catalog.usableGroup[group] || group}</span>)}
        {itemTags.slice(0, 3).map((tag) => <span key={tag}>{tag}</span>)}
      </div>
      <a className="pricing-detail-link" href={modelHref(item.model_name, query)}>{t('View model details')} →</a>
    </article>
  );
}

function PricingTable({ catalog, items, query, ratio, performanceByModel }: {
  catalog: PricingCatalog;
  items: PricingCatalogItem[];
  query: PricingQueryState;
  ratio: number;
  performanceByModel: ReadonlyMap<string, PerformanceModelSummary>;
}) {
  const { t } = useTranslation();
  return (
    <div className="card table-scroll pricing-table-wrap">
      <table className="pricing-table">
        <caption className="sr-only">{t('Model pricing')}</caption>
        <thead><tr><th scope="col">{t('Model')}</th><th scope="col">{t('Owner')}</th><th scope="col">{t('Modalities')}</th><th scope="col">{t('Groups')}</th><th scope="col">{t('Model pricing')}</th><th scope="col">{t('Performance')}</th></tr></thead>
        <tbody>{items.map((item) => (
          <tr key={item.model_name}>
            <th scope="row"><a href={modelHref(item.model_name, query)}>{item.model_name}</a>{item.description && <small>{item.description}</small>}</th>
            <td>{ownerName(item, catalog.vendors)}</td>
            <td>{itemModalities(item).map((modality) => t(MODALITY_LABELS[modality])).join(', ')}</td>
            <td>{item.enable_groups.map((group) => catalog.usableGroup[group] || group).join(', ')}</td>
            <td className="pricing-table-price">{isDynamicPricingItem(item)
              ? <span>{t('Billing mode')}: <code>tiered_expr</code></span>
              : item.quota_type === 0
                ? <><span>{t('Input / 1M')}: ${formatPrice(item.prompt_price * ratio)}</span><span>{t('Output / 1M')}: ${formatPrice(item.completion_price * ratio)}</span></>
                : <span>{t('Per request')}: ${formatPrice(item.model_price * ratio)}</span>}</td>
            <td><PerformanceSummaryBadge summary={performanceByModel.get(item.model_name)} /></td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  );
}

function PricingDetail({ catalog, modelName, selectedGroup }: {
  catalog: PricingCatalog;
  modelName: string;
  selectedGroup: string;
}) {
  const { t } = useTranslation();
  if (!modelName) {
    return <section className="card pricing-state"><p className="error" role="alert">{t('Invalid model address.')}</p></section>;
  }
  const item = catalog.items.find((entry) => entry.model_name === modelName);
  if (!item) {
    return <section className="card pricing-state"><h2>{t('Model not found')}</h2><p className="muted">{t('This model is not present in the current public catalog.')}</p></section>;
  }
  const owner = ownerName(item, catalog.vendors);
  const activeGroup = selectedGroup && item.enable_groups.includes(selectedGroup) ? selectedGroup : '';
  const selectedRatio = activeGroup ? catalog.groupRatio[activeGroup] ?? 1 : 1;
  const dynamic = isDynamicPricingItem(item);
  return (
    <article className="pricing-detail" aria-labelledby="pricing-model-heading">
      <section className="card pricing-detail-hero">
        <p className="pricing-owner">{owner}</p>
        <h2 id="pricing-model-heading">{item.model_name}</h2>
        {item.description && <p>{item.description}</p>}
        <div className="pricing-pills">
          {itemModalities(item).map((modality) => <span key={modality}>{t(MODALITY_LABELS[modality])}</span>)}
          {pricingItemTags(item).slice(0, 12).map((tag) => <span key={tag}>{tag}</span>)}
        </div>
      </section>

      <section className="card" aria-labelledby="pricing-breakdown-heading">
        <h2 id="pricing-breakdown-heading">{t('Price breakdown')}</h2>
        {activeGroup && <p className="muted">{t('Showing the {{group}} group adjustment (×{{ratio}}).', { group: catalog.usableGroup[activeGroup] || activeGroup, ratio: selectedRatio })}</p>}
        {dynamic ? (
          <DynamicBillingExplanation expression={item.billing_expr!} />
        ) : (
          <dl className="pricing-detail-prices">
            {item.quota_type === 0 ? (
            <>
              <div><dt>{t('Input tokens')}</dt><dd>${formatPrice(item.prompt_price * selectedRatio)} <small>{t('per 1M tokens')}</small></dd></div>
              <div><dt>{t('Output tokens')}</dt><dd>${formatPrice(item.completion_price * selectedRatio)} <small>{t('per 1M tokens')}</small></dd></div>
              <div><dt>{t('Output / input ratio')}</dt><dd>×{formatPrice(item.completion_ratio)}</dd></div>
            </>
            ) : <div><dt>{t('Request price')}</dt><dd>${formatPrice(item.model_price * selectedRatio)}</dd></div>}
          </dl>
        )}
      </section>

      <section className="card" aria-labelledby="pricing-groups-heading">
        <h2 id="pricing-groups-heading">{t('Available groups')}</h2>
        {item.enable_groups.length === 0 ? <p className="muted">{t('No access groups are advertised.')}</p> : (
          <div className="table-scroll"><table>
            <caption className="sr-only">{t('Available groups')}</caption>
            <thead><tr><th scope="col">{t('Group')}</th><th scope="col">{t('Adjustment')}</th>{!dynamic && (item.quota_type === 0 ? <><th scope="col">{t('Input / 1M')}</th><th scope="col">{t('Output / 1M')}</th></> : <th scope="col">{t('Per request')}</th>)}</tr></thead>
            <tbody>{item.enable_groups.map((group) => {
              const ratio = catalog.groupRatio[group] ?? 1;
              return <tr key={group}><th scope="row">{catalog.usableGroup[group] || group}{catalog.autoGroups.includes(group) && <small> {t('(automatic)')}</small>}</th><td>×{formatPrice(ratio)}</td>{!dynamic && (item.quota_type === 0 ? <><td>${formatPrice(item.prompt_price * ratio)}</td><td>${formatPrice(item.completion_price * ratio)}</td></> : <td>${formatPrice(item.model_price * ratio)}</td>)}</tr>;
            })}</tbody>
          </table></div>
        )}
      </section>

      <section className="card" aria-labelledby="pricing-routes-heading">
        <h2 id="pricing-routes-heading">{t('Supported API routes')}</h2>
        {item.supported_endpoint_types.length === 0 ? <p className="muted">{t('No API routes are advertised.')}</p> : (
          <div className="table-scroll"><table>
            <caption className="sr-only">{t('Supported API routes')}</caption>
            <thead><tr><th scope="col">{t('Type')}</th><th scope="col">{t('Method')}</th><th scope="col">{t('Path')}</th></tr></thead>
            <tbody>{item.supported_endpoint_types.map((type) => {
              const endpoint = catalog.supportedEndpoint[type];
              return <tr key={type}><th scope="row">{type}</th><td><code>{endpoint.method}</code></td><td><code>{endpoint.path.replaceAll('{model}', item.model_name)}</code></td></tr>;
            })}</tbody>
          </table></div>
        )}
      </section>

      <PerformanceMetricsPanel
        modelName={item.model_name}
        preferredGroup={activeGroup || undefined}
        groupLabels={catalog.usableGroup}
      />
    </article>
  );
}
