import { useEffect, useMemo, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  DEPLOYMENT_PAGE_SIZE,
  MODEL_PAGE_SIZE,
  MODEL_SECTIONS,
  assertModelsAdministrator,
  checkDeploymentName,
  createDeployment,
  createModel,
  createVendor,
  deleteDeployment,
  deleteModel,
  deleteVendor,
  extendDeployment,
  estimateDeploymentPrice,
  getDeployment,
  getDeploymentContainer,
  getModel,
  getVendor,
  loadDeploymentContainers,
  loadDeploymentHardware,
  loadDeploymentLogs,
  loadDeploymentReplicas,
  loadDeploymentSettings,
  loadDeployments,
  loadMissingModels,
  loadModels,
  loadVendors,
  previewUpstream,
  renameDeployment,
  setModelStatus,
  syncUpstream,
  testDeploymentConnection,
  updateDeployment,
  updateModel,
  updateVendor,
  type DeploymentAccess,
  type DeploymentContainer,
  type DeploymentCreateInput,
  type DeploymentDetail,
  type DeploymentHardware,
  type DeploymentPage,
  type DeploymentPriceEstimate,
  type DeploymentQuery,
  type DeploymentReplica,
  type DeploymentStatus,
  type DeploymentSummary,
  type DeploymentUpdateInput,
  type MetadataPage,
  type ModelMetadata,
  type ModelMutationInput,
  type ModelQuery,
  type ModelsSection,
  type SyncLocale,
  type SyncPreview,
  type VendorMetadata,
  type VendorMutationInput,
  type VendorPage,
} from './models-api';
import './models.css';

export interface ModelsViewProps {
  section: ModelsSection;
  role: number;
  onNavigate?: (target: string) => void;
}

type Notice = { kind: 'success' | 'error'; text: string } | null;
type EditorMode = 'create' | 'edit';

const EMPTY_MODEL: ModelMutationInput = {
  modelName: '',
  description: '',
  icon: '',
  tags: '',
  vendorId: 0,
  endpoints: '[]',
  status: 1,
  syncOfficial: 1,
  nameRule: 0,
};

const EMPTY_VENDOR: VendorMutationInput = {
  name: '',
  description: '',
  icon: '',
  status: 1,
};

const EMPTY_MODEL_QUERY: ModelQuery = {
  keyword: '',
  vendor: '',
  status: '',
  syncOfficial: '',
  page: 1,
  pageSize: MODEL_PAGE_SIZE,
};

const EMPTY_DEPLOYMENT_QUERY: DeploymentQuery = {
  keyword: '',
  status: '',
  page: 1,
  pageSize: DEPLOYMENT_PAGE_SIZE,
};

const EMPTY_DEPLOYMENT: DeploymentCreateInput = {
  name: '',
  durationHours: 1,
  GPUsPerContainer: 1,
  hardwareId: 0,
  locationIds: [],
  replicaCount: 1,
  image: '',
  registryUsername: '',
  registrySecret: '',
  trafficPort: 5_000,
};

const DEPLOYMENT_STATUS_OPTIONS: DeploymentStatus[] = [
  '',
  'running',
  'completed',
  'failed',
  'deployment requested',
  'termination requested',
  'destroyed',
];

function modelInput(value: ModelMetadata): ModelMutationInput & { id: number } {
  return {
    id: value.id,
    modelName: value.modelName,
    description: value.description,
    icon: value.icon,
    tags: value.tags,
    vendorId: value.vendorId,
    endpoints: value.endpoints || '[]',
    status: value.status,
    syncOfficial: value.syncOfficial,
    nameRule: value.nameRule,
  };
}

function vendorInput(value: VendorMetadata): VendorMutationInput & { id: number } {
  return {
    id: value.id,
    name: value.name,
    description: value.description,
    icon: value.icon,
    status: value.status,
  };
}

function formatDate(value: number): string {
  if (value === 0) return '—';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(value * 1_000);
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function Overlay({ title, children, onClose, busy = false }: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className="models-overlay" role="presentation">
      <section className="models-dialog" role="dialog" aria-modal="true" aria-label={title}>
        <header className="models-dialog-header">
          <h2>{title}</h2>
          <button type="button" className="models-icon-button" onClick={onClose} disabled={busy} aria-label={t('Close')}>
            ×
          </button>
        </header>
        {children}
      </section>
    </div>
  );
}

function NoticeBanner({ notice }: { notice: Notice }) {
  if (!notice) return null;
  return <p className={`models-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>;
}

function LoadingPanel({ label }: { label: string }) {
  return <div className="models-state" role="status" aria-live="polite"><span className="models-spinner" />{label}</div>;
}

function ErrorPanel({ message, retry }: { message: string; retry: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="models-state error" role="alert">
      <p>{message}</p>
      <button type="button" onClick={retry}>{t('Retry')}</button>
    </div>
  );
}

function ModelEditor({ mode, initial, vendors, busy, onCancel, onSave }: {
  mode: EditorMode;
  initial: ModelMutationInput;
  vendors: VendorMetadata[];
  busy: boolean;
  onCancel: () => void;
  onSave: (input: ModelMutationInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<ModelMutationInput>(initial);
  const [invalidJSON, setInvalidJSON] = useState(false);

  function submit(event: FormEvent) {
    event.preventDefault();
    try {
      if (draft.endpoints.trim()) JSON.parse(draft.endpoints);
      setInvalidJSON(false);
    } catch {
      setInvalidJSON(true);
      return;
    }
    void onSave(draft);
  }

  return (
    <Overlay title={mode === 'create' ? t('Create model') : t('Edit model')} onClose={onCancel} busy={busy}>
      <form className="models-form" onSubmit={submit} aria-label={mode === 'create' ? t('Create model') : t('Edit model')}>
        <label>{t('Model name')}<input autoFocus required maxLength={128} value={draft.modelName} onChange={(e) => setDraft({ ...draft, modelName: e.target.value })} /></label>
        <label>{t('Vendor')}
          <select value={draft.vendorId} onChange={(e) => setDraft({ ...draft, vendorId: Number(e.target.value) })}>
            <option value={0}>{t('No vendor')}</option>
            {vendors.map((vendor) => <option key={vendor.id} value={vendor.id}>{vendor.name}</option>)}
          </select>
        </label>
        <label>{t('Description')}<textarea maxLength={1_048_576} rows={3} value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} /></label>
        <div className="models-form-grid">
          <label>{t('Icon')}<input maxLength={128} value={draft.icon} onChange={(e) => setDraft({ ...draft, icon: e.target.value })} /></label>
          <label>{t('Tags')}<input maxLength={255} value={draft.tags} onChange={(e) => setDraft({ ...draft, tags: e.target.value })} /></label>
          <label>{t('Name rule')}
            <select value={draft.nameRule} onChange={(e) => setDraft({ ...draft, nameRule: Number(e.target.value) as 0 | 1 | 2 | 3 })}>
              <option value={0}>{t('Exact')}</option>
              <option value={1}>{t('Prefix')}</option>
              <option value={2}>{t('Contains')}</option>
              <option value={3}>{t('Suffix')}</option>
            </select>
          </label>
          <label>{t('Status')}
            <select value={draft.status} onChange={(e) => setDraft({ ...draft, status: Number(e.target.value) as 0 | 1 })}>
              <option value={1}>{t('Enabled')}</option><option value={0}>{t('Disabled')}</option>
            </select>
          </label>
          <label>{t('Official sync')}
            <select value={draft.syncOfficial} onChange={(e) => setDraft({ ...draft, syncOfficial: Number(e.target.value) as 0 | 1 })}>
              <option value={1}>{t('Enabled')}</option><option value={0}>{t('Disabled')}</option>
            </select>
          </label>
        </div>
        <label>{t('Endpoints JSON')}<textarea className="models-code" rows={5} maxLength={1_048_576} value={draft.endpoints} onChange={(e) => setDraft({ ...draft, endpoints: e.target.value })} aria-invalid={invalidJSON} /></label>
        {invalidJSON && <p className="models-field-error" role="alert">{t('Endpoints must be valid JSON.')}</p>}
        <footer className="models-dialog-actions">
          <button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button>
          <button type="submit" className="primary" disabled={busy}>{busy ? t('Saving…') : t('Save')}</button>
        </footer>
      </form>
    </Overlay>
  );
}

function VendorEditor({ mode, initial, busy, onCancel, onSave }: {
  mode: EditorMode;
  initial: VendorMutationInput;
  busy: boolean;
  onCancel: () => void;
  onSave: (input: VendorMutationInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(initial);
  return (
    <Overlay title={mode === 'create' ? t('Create vendor') : t('Edit vendor')} onClose={onCancel} busy={busy}>
      <form className="models-form" aria-label={mode === 'create' ? t('Create vendor') : t('Edit vendor')} onSubmit={(event) => { event.preventDefault(); void onSave(draft); }}>
        <label>{t('Vendor name')}<input autoFocus required maxLength={128} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></label>
        <label>{t('Description')}<textarea maxLength={1_048_576} rows={4} value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} /></label>
        <label>{t('Icon')}<input maxLength={128} value={draft.icon} onChange={(e) => setDraft({ ...draft, icon: e.target.value })} /></label>
        <label>{t('Status')}
          <select value={draft.status} onChange={(e) => setDraft({ ...draft, status: Number(e.target.value) as 0 | 1 })}>
            <option value={1}>{t('Enabled')}</option><option value={0}>{t('Disabled')}</option>
          </select>
        </label>
        <footer className="models-dialog-actions">
          <button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button>
          <button type="submit" className="primary" disabled={busy}>{busy ? t('Saving…') : t('Save')}</button>
        </footer>
      </form>
    </Overlay>
  );
}

function MetadataSection() {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(EMPTY_MODEL_QUERY);
  const [query, setQuery] = useState(EMPTY_MODEL_QUERY);
  const [page, setPage] = useState<MetadataPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [vendorPage, setVendorPage] = useState<VendorPage | null>(null);
  const [vendorOptions, setVendorOptions] = useState<VendorMetadata[]>([]);
  const [vendorDraft, setVendorDraft] = useState('');
  const [vendorQuery, setVendorQuery] = useState('');
  const [vendorLoading, setVendorLoading] = useState(true);
  const [vendorError, setVendorError] = useState(false);
  const [vendorPageNumber, setVendorPageNumber] = useState(1);
  const [vendorReload, setVendorReload] = useState(0);
  const [busyAction, setBusyAction] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [modelEditor, setModelEditor] = useState<{ mode: EditorMode; value: ModelMutationInput } | null>(null);
  const [vendorEditor, setVendorEditor] = useState<{ mode: EditorMode; value: VendorMutationInput } | null>(null);
  const [inspectionLoading, setInspectionLoading] = useState(false);
  const [missing, setMissing] = useState<string[] | null>(null);
  const [syncLocale, setSyncLocale] = useState<SyncLocale>('');
  const [syncPreview, setSyncPreview] = useState<SyncPreview | null>(null);
  const [syncSelections, setSyncSelections] = useState<Set<string>>(() => new Set());
  const alive = useRef(true);
  const busyRef = useRef(false);
  const inspectionController = useRef<AbortController | null>(null);

  useEffect(() => () => {
    alive.current = false;
    inspectionController.current?.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void loadModels(query, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      setPage(result);
      const lastPage = Math.max(1, Math.ceil(result.total / query.pageSize));
      if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
    }).catch(() => {
      if (!controller.signal.aborted) {
        setPage(null);
        setLoadError(true);
      }
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [query, reload]);

  useEffect(() => {
    const controller = new AbortController();
    setVendorLoading(true);
    setVendorError(false);
    void loadVendors(controller.signal, vendorQuery, vendorPageNumber, 100).then((result) => {
      if (!controller.signal.aborted) {
        setVendorPage(result);
        setVendorOptions((current) => {
          const combined = new Map(current.map((vendor) => [vendor.id, vendor]));
          result.items.forEach((vendor) => combined.set(vendor.id, vendor));
          return [...combined.values()].sort((left, right) => left.name.localeCompare(right.name));
        });
        const lastPage = Math.max(1, Math.ceil(result.total / result.pageSize));
        if (vendorPageNumber > lastPage) setVendorPageNumber(lastPage);
      }
    }).catch(() => {
      if (!controller.signal.aborted) {
        setVendorPage(null);
        setVendorError(true);
      }
    }).finally(() => {
      if (!controller.signal.aborted) setVendorLoading(false);
    });
    return () => controller.abort();
  }, [vendorPageNumber, vendorQuery, vendorReload]);

  const vendorNames = useMemo(() => new Map(vendorOptions.map((vendor) => [vendor.id, vendor.name])), [vendorOptions]);
  const pageCount = Math.max(1, Math.ceil((page?.total ?? 0) / query.pageSize));

  async function runMutation(action: string, operation: () => Promise<void>, success: string): Promise<boolean> {
    if (busyRef.current) return false;
    busyRef.current = true;
    setBusyAction(action);
    setNotice(null);
    try {
      await operation();
      if (!alive.current) return true;
      setNotice({ kind: 'success', text: success });
      setReload((value) => value + 1);
      setVendorReload((value) => value + 1);
      return true;
    } catch {
      if (alive.current) setNotice({ kind: 'error', text: t('The model operation could not be completed.') });
      return false;
    } finally {
      busyRef.current = false;
      if (alive.current) setBusyAction('');
    }
  }

  function inspect(operation: (signal: AbortSignal) => Promise<void>) {
    inspectionController.current?.abort();
    const controller = new AbortController();
    inspectionController.current = controller;
    setInspectionLoading(true);
    setNotice(null);
    void operation(controller.signal).catch(() => {
      if (!controller.signal.aborted) setNotice({ kind: 'error', text: t('Unable to load model details.') });
    }).finally(() => {
      if (!controller.signal.aborted) setInspectionLoading(false);
    });
  }

  function submitSearch(event: FormEvent) {
    event.preventDefault();
    setNotice(null);
    setQuery({ ...draft, keyword: draft.keyword.trim(), vendor: draft.vendor.trim(), page: 1 });
  }

  function clearSearch() {
    setDraft(EMPTY_MODEL_QUERY);
    setQuery(EMPTY_MODEL_QUERY);
    setNotice(null);
  }

  function editModel(id: number) {
    inspect(async (signal) => {
      const value = await getModel(id, signal);
      if (!signal.aborted) setModelEditor({ mode: 'edit', value: modelInput(value) });
    });
  }

  function editVendor(id: number) {
    inspect(async (signal) => {
      const value = await getVendor(id, signal);
      if (!signal.aborted) setVendorEditor({ mode: 'edit', value: vendorInput(value) });
    });
  }

  function showMissing() {
    inspect(async (signal) => {
      const result = await loadMissingModels(signal);
      if (!signal.aborted) setMissing(result);
    });
  }

  function showSyncPreview() {
    inspect(async (signal) => {
      const result = await previewUpstream(syncLocale, signal);
      if (!signal.aborted) {
        setSyncSelections(new Set());
        setSyncPreview(result);
      }
    });
  }

  function syncFieldKey(modelName: string, field: string): string {
    return `${modelName}\u0000${field}`;
  }

  const selectedSyncConflicts = syncPreview?.conflicts.flatMap((conflict) => {
    const fields = conflict.fields.filter((field) => syncSelections.has(syncFieldKey(conflict.modelName, field)));
    return fields.length > 0 ? [{ ...conflict, fields }] : [];
  }) ?? [];

  return (
    <section className="models-section" aria-labelledby="models-metadata-title">
      <div className="models-section-heading">
        <div><h1 id="models-metadata-title">{t('Model metadata')}</h1><p>{t('Manage the local model and vendor registry.')}</p></div>
        <div className="models-toolbar-actions">
          <button type="button" onClick={() => setVendorEditor({ mode: 'create', value: EMPTY_VENDOR })}>{t('Create vendor')}</button>
          <button type="button" className="primary" onClick={() => setModelEditor({ mode: 'create', value: EMPTY_MODEL })}>{t('Create model')}</button>
        </div>
      </div>

      <NoticeBanner notice={notice} />
      {inspectionLoading && <p className="models-inline-progress" role="status">{t('Loading details…')}</p>}

      <form className="models-filters" role="search" aria-label={t('Search model metadata')} onSubmit={submitSearch}>
        <label>{t('Name, description, or tag')}<input maxLength={256} value={draft.keyword} onChange={(e) => setDraft({ ...draft, keyword: e.target.value })} /></label>
        <label>{t('Vendor')}
          <select value={draft.vendor} disabled={vendorLoading || vendorError} onChange={(e) => setDraft({ ...draft, vendor: e.target.value })}>
            <option value="">{vendorLoading ? t('Loading vendors…') : t('All vendors')}</option>
            {vendorOptions.map((vendor) => <option value={vendor.id} key={vendor.id}>{vendor.name}</option>)}
          </select>
        </label>
        <label>{t('Status')}<select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as ModelQuery['status'] })}><option value="">{t('All statuses')}</option><option value="enabled">{t('Enabled')}</option><option value="disabled">{t('Disabled')}</option></select></label>
        <label>{t('Official sync')}<select value={draft.syncOfficial} onChange={(e) => setDraft({ ...draft, syncOfficial: e.target.value as ModelQuery['syncOfficial'] })}><option value="">{t('Any')}</option><option value="yes">{t('Enabled')}</option><option value="no">{t('Disabled')}</option></select></label>
        <div className="models-filter-actions"><button type="submit" className="primary">{t('Apply filters')}</button><button type="button" onClick={clearSearch}>{t('Clear filters')}</button></div>
      </form>

      <div className="models-secondary-actions">
        <button type="button" onClick={showMissing} disabled={inspectionLoading}>{t('Show missing models')}</button>
        <label>{t('Sync language')}<select value={syncLocale} onChange={(e) => setSyncLocale(e.target.value as SyncLocale)}><option value="">{t('Default')}</option><option value="en">English</option><option value="ja">日本語</option><option value="zh-cn">简体中文</option><option value="zh-tw">繁體中文</option></select></label>
        <button type="button" onClick={showSyncPreview} disabled={inspectionLoading}>{t('Preview upstream sync')}</button>
      </div>

      {loading ? <LoadingPanel label={t('Loading model metadata…')} /> : loadError ? (
        <ErrorPanel message={t('Unable to load model metadata.')} retry={() => setReload((value) => value + 1)} />
      ) : page && page.items.length > 0 ? (
        <div className="models-table-wrap">
          <table className="models-table" aria-busy="false">
            <caption>{t('Registered models')}</caption>
            <thead><tr><th scope="col">{t('Model')}</th><th scope="col">{t('Vendor')}</th><th scope="col">{t('Match')}</th><th scope="col">{t('Status')}</th><th scope="col">{t('Updated')}</th><th scope="col">{t('Actions')}</th></tr></thead>
            <tbody>{page.items.map((model) => (
              <tr key={model.id}>
                <th scope="row"><span className="models-primary-value">{model.modelName}</span><span className="models-secondary-value">{model.description || t('No description')}</span></th>
                <td>{vendorNames.get(model.vendorId) ?? (model.vendorId ? t('Vendor #{{id}}', { id: model.vendorId }) : '—')}</td>
                <td>{model.nameRule === 0 ? t('Exact') : `${['', t('Prefix'), t('Contains'), t('Suffix')][model.nameRule]} · ${formatNumber(model.matchedCount)}`}</td>
                <td><span className={`models-status ${model.status === 1 ? 'enabled' : 'disabled'}`}>{model.status === 1 ? t('Enabled') : t('Disabled')}</span></td>
                <td>{formatDate(model.updatedTime)}</td>
                <td><div className="models-row-actions">
                  <button type="button" onClick={() => editModel(model.id)} disabled={Boolean(busyAction) || inspectionLoading}>{t('Edit')}</button>
                  <button type="button" onClick={() => void runMutation(`status:${model.id}`, () => setModelStatus(model.id, model.status === 1 ? 0 : 1), model.status === 1 ? t('Model disabled.') : t('Model enabled.'))} disabled={Boolean(busyAction)}>{model.status === 1 ? t('Disable') : t('Enable')}</button>
                  <button type="button" className="danger" onClick={() => { if (window.confirm(t('Delete model “{{name}}”?', { name: model.modelName }))) void runMutation(`delete:${model.id}`, () => deleteModel(model.id), t('Model deleted.')); }} disabled={Boolean(busyAction)}>{t('Delete')}</button>
                </div></td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      ) : <div className="models-state"><p>{t('No models match these filters.')}</p></div>}

      <nav className="models-pagination" aria-label={t('Model pages')}>
        <button type="button" disabled={loading || query.page <= 1} onClick={() => setQuery((value) => ({ ...value, page: value.page - 1 }))}>{t('Previous')}</button>
        <span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span>
        <button type="button" disabled={loading || query.page >= pageCount} onClick={() => setQuery((value) => ({ ...value, page: value.page + 1 }))}>{t('Next')}</button>
      </nav>

      <details className="models-vendors" open>
        <summary>{t('Vendor catalog')}</summary>
        <form className="models-vendor-search" role="search" aria-label={t('Search vendors')} onSubmit={(event) => { event.preventDefault(); setVendorPageNumber(1); setVendorQuery(vendorDraft.trim()); }}>
          <label>{t('Vendor keyword')}<input maxLength={256} value={vendorDraft} onChange={(e) => setVendorDraft(e.target.value)} /></label>
          <button type="submit">{t('Search')}</button><button type="button" onClick={() => { setVendorDraft(''); setVendorQuery(''); setVendorPageNumber(1); }}>{t('Clear')}</button>
        </form>
        {vendorLoading ? <LoadingPanel label={t('Loading vendors…')} /> : vendorError ? <ErrorPanel message={t('Unable to load vendors.')} retry={() => setVendorReload((value) => value + 1)} /> : vendorPage && vendorPage.items.length > 0 ? (
          <div className="models-table-wrap compact"><table className="models-table"><caption>{t('Vendors')}</caption><thead><tr><th scope="col">{t('Vendor')}</th><th scope="col">{t('Models')}</th><th scope="col">{t('Status')}</th><th scope="col">{t('Actions')}</th></tr></thead><tbody>
            {vendorPage.items.map((vendor) => <tr key={vendor.id}><th scope="row">{vendor.name}</th><td>{formatNumber(page?.vendorCounts[vendor.id] ?? 0)}</td><td>{vendor.status === 1 ? t('Enabled') : t('Disabled')}</td><td><div className="models-row-actions"><button type="button" onClick={() => editVendor(vendor.id)} disabled={inspectionLoading || Boolean(busyAction)}>{t('Edit')}</button><button type="button" className="danger" disabled={Boolean(busyAction)} onClick={() => { if (window.confirm(t('Delete vendor “{{name}}”?', { name: vendor.name }))) void runMutation(`vendor-delete:${vendor.id}`, () => deleteVendor(vendor.id), t('Vendor deleted.')); }}>{t('Delete')}</button></div></td></tr>)}
          </tbody></table></div>
        ) : <p className="models-empty-note">{t('No vendors found.')}</p>}
        {vendorPage && vendorPage.total > vendorPage.pageSize && <nav className="models-pagination" aria-label={t('Vendor pages')}><button type="button" disabled={vendorLoading || vendorPageNumber <= 1} onClick={() => setVendorPageNumber((value) => value - 1)}>{t('Previous')}</button><span>{t('Page {{page}} of {{pages}}', { page: vendorPageNumber, pages: Math.max(1, Math.ceil(vendorPage.total / vendorPage.pageSize)) })}</span><button type="button" disabled={vendorLoading || vendorPageNumber >= Math.ceil(vendorPage.total / vendorPage.pageSize)} onClick={() => setVendorPageNumber((value) => value + 1)}>{t('Next')}</button></nav>}
      </details>

      {modelEditor && <ModelEditor mode={modelEditor.mode} initial={modelEditor.value} vendors={vendorOptions} busy={Boolean(busyAction)} onCancel={() => setModelEditor(null)} onSave={async (input) => {
        const current = modelEditor;
        const ok = await runMutation('model-save', async () => { if (current.mode === 'edit') await updateModel({ ...input, id: (current.value as ModelMutationInput & { id: number }).id }); else await createModel(input); }, current.mode === 'edit' ? t('Model updated.') : t('Model created.'));
        if (ok && alive.current) setModelEditor(null);
      }} />}

      {vendorEditor && <VendorEditor mode={vendorEditor.mode} initial={vendorEditor.value} busy={Boolean(busyAction)} onCancel={() => setVendorEditor(null)} onSave={async (input) => {
        const current = vendorEditor;
        const ok = await runMutation('vendor-save', async () => { if (current.mode === 'edit') await updateVendor({ ...input, id: (current.value as VendorMutationInput & { id: number }).id }); else await createVendor(input); }, current.mode === 'edit' ? t('Vendor updated.') : t('Vendor created.'));
        if (ok && alive.current) setVendorEditor(null);
      }} />}

      {missing && <Overlay title={t('Missing models')} onClose={() => setMissing(null)}>{missing.length > 0 ? <ul className="models-scroll-list">{missing.map((name) => <li key={name}>{name}</li>)}</ul> : <p className="models-empty-note">{t('Every enabled model has metadata.')}</p>}<footer className="models-dialog-actions"><button type="button" onClick={() => setMissing(null)}>{t('Close')}</button></footer></Overlay>}

      {syncPreview && <Overlay title={t('Upstream sync preview')} onClose={() => { setSyncPreview(null); setSyncSelections(new Set()); }} busy={Boolean(busyAction)}>
        <div className="models-sync-summary"><p>{t('{{count}} missing models can be created.', { count: syncPreview.missing.length })}</p><p>{t('{{count}} local models have upstream differences.', { count: syncPreview.conflicts.length })}</p></div>
        {syncPreview.conflicts.length > 0 && <fieldset className="models-sync-fields">
          <legend>{t('Select fields to overwrite')}</legend>
          <p>{t('Unselected fields keep their local values.')}</p>
          <ul className="models-scroll-list">{syncPreview.conflicts.map((conflict) => <li key={conflict.modelName}>
            <strong>{conflict.modelName}</strong>
            <div>{conflict.fields.map((field) => {
              const key = syncFieldKey(conflict.modelName, field);
              return <label key={field}><input type="checkbox" checked={syncSelections.has(key)} onChange={(event) => setSyncSelections((current) => {
                const next = new Set(current);
                if (event.target.checked) next.add(key); else next.delete(key);
                return next;
              })} />{field}</label>;
            })}</div>
          </li>)}</ul>
        </fieldset>}
        <footer className="models-dialog-actions"><button type="button" onClick={() => { setSyncPreview(null); setSyncSelections(new Set()); }} disabled={Boolean(busyAction)}>{t('Cancel')}</button><button type="button" className="primary" disabled={Boolean(busyAction) || (syncPreview.missing.length === 0 && selectedSyncConflicts.length === 0)} onClick={() => {
          if (!window.confirm(t('Apply this upstream sync?'))) return;
          void runMutation('sync', async () => {
            await syncUpstream(syncLocale, { missing: syncPreview.missing, conflicts: selectedSyncConflicts });
          }, t('Upstream sync completed.')).then((ok) => { if (ok && alive.current) { setSyncPreview(null); setSyncSelections(new Set()); } });
        }}>{busyAction === 'sync' ? t('Syncing…') : t('Apply sync')}</button></footer>
      </Overlay>}
    </section>
  );
}

type ResourceState<T> =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; value: T };

function environmentDraft(value: string): Record<string, string> | undefined {
  if (!value.trim()) return undefined;
  const parsed: unknown = JSON.parse(value);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('invalid environment');
  const entries = Object.entries(parsed as Record<string, unknown>);
  if (entries.length === 0 || entries.length > 256 || entries.some(([key, entry]) => !key || typeof entry !== 'string')) {
    throw new Error('invalid environment');
  }
  return Object.fromEntries(entries) as Record<string, string>;
}

function argumentsDraft(value: string): string[] | undefined {
  const values = value.split(/\s+/u).map((entry) => entry.trim()).filter(Boolean);
  return values.length > 0 ? values : undefined;
}

function DeploymentCreator({ busy, onCancel, onSave }: {
  busy: boolean;
  onCancel: () => void;
  onSave: (input: DeploymentCreateInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(EMPTY_DEPLOYMENT);
  const [hardware, setHardware] = useState<ResourceState<DeploymentHardware[]>>({ status: 'loading' });
  const [hardwareAttempt, setHardwareAttempt] = useState(0);
  const [replicas, setReplicas] = useState<ResourceState<DeploymentReplica[]>>({ status: 'loading' });
  const [replicaAttempt, setReplicaAttempt] = useState(0);
  const [nameState, setNameState] = useState<'idle' | 'checking' | 'available' | 'unavailable' | 'error'>('idle');
  const [nameAttempt, setNameAttempt] = useState(0);
  const [priceState, setPriceState] = useState<ResourceState<DeploymentPriceEstimate> | { status: 'idle' }>({ status: 'idle' });
  const [priceAttempt, setPriceAttempt] = useState(0);
  const [currency, setCurrency] = useState<'usdc' | 'iocoin'>('usdc');
  const [environmentJSON, setEnvironmentJSON] = useState('');
  const [secretEnvironmentJSON, setSecretEnvironmentJSON] = useState('');
  const [entrypoint, setEntrypoint] = useState('');
  const [args, setArgs] = useState('');
  const [locationError, setLocationError] = useState(false);
  const [configurationError, setConfigurationError] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setHardware({ status: 'loading' });
    void loadDeploymentHardware(controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      const options = result.items.filter((item) => item.available && item.maxGPUs > 0);
      setHardware({ status: 'ready', value: options });
      if (options.length > 0) {
        setDraft((current) => current.hardwareId > 0 ? current : { ...current, hardwareId: options[0].id });
      }
    }).catch(() => {
      if (!controller.signal.aborted) setHardware({ status: 'error' });
    });
    return () => controller.abort();
  }, [hardwareAttempt]);

  const selectedHardware = hardware.status === 'ready'
    ? hardware.value.find((item) => item.id === draft.hardwareId)
    : undefined;

  useEffect(() => {
    if (draft.hardwareId <= 0 || draft.GPUsPerContainer <= 0) {
      setReplicas({ status: 'ready', value: [] });
      return;
    }
    const controller = new AbortController();
    setReplicas({ status: 'loading' });
    void loadDeploymentReplicas(draft.hardwareId, draft.GPUsPerContainer, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      setReplicas({ status: 'ready', value: result });
      const valid = new Set(result.filter((entry) => entry.availableCount > 0).map((entry) => entry.locationId));
      setDraft((current) => ({ ...current, locationIds: current.locationIds.filter((id) => valid.has(id)) }));
    }).catch(() => {
      if (!controller.signal.aborted) setReplicas({ status: 'error' });
    });
    return () => controller.abort();
  }, [draft.GPUsPerContainer, draft.hardwareId, replicaAttempt]);

  useEffect(() => {
    const name = draft.name.trim();
    if (!name) {
      setNameState('idle');
      return;
    }
    const controller = new AbortController();
    setNameState('checking');
    const timeout = window.setTimeout(() => {
      void checkDeploymentName(name, controller.signal).then((available) => {
        if (!controller.signal.aborted) setNameState(available ? 'available' : 'unavailable');
      }).catch(() => {
        if (!controller.signal.aborted) setNameState('error');
      });
    }, 250);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [draft.name, nameAttempt]);

  useEffect(() => {
    if (draft.hardwareId <= 0 || draft.locationIds.length === 0 || draft.GPUsPerContainer <= 0
      || draft.durationHours <= 0 || draft.replicaCount <= 0) {
      setPriceState({ status: 'idle' });
      return;
    }
    const controller = new AbortController();
    setPriceState({ status: 'loading' });
    const timeout = window.setTimeout(() => {
      void estimateDeploymentPrice({
        durationHours: draft.durationHours,
        GPUsPerContainer: draft.GPUsPerContainer,
        hardwareId: draft.hardwareId,
        locationIds: draft.locationIds,
        replicaCount: draft.replicaCount,
      }, currency, controller.signal).then((value) => {
        if (!controller.signal.aborted) setPriceState({ status: 'ready', value });
      }).catch(() => {
        if (!controller.signal.aborted) setPriceState({ status: 'error' });
      });
    }, 200);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [currency, draft.durationHours, draft.GPUsPerContainer, draft.hardwareId, draft.locationIds, draft.replicaCount, priceAttempt]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (draft.locationIds.length === 0 || nameState !== 'available') {
      setLocationError(draft.locationIds.length === 0);
      return;
    }
    try {
      const environmentVariables = environmentDraft(environmentJSON);
      const secretEnvironmentVariables = environmentDraft(secretEnvironmentJSON);
      setConfigurationError(false);
      setLocationError(false);
      void onSave({
        ...draft,
        ...(environmentVariables ? { environmentVariables } : {}),
        ...(secretEnvironmentVariables ? { secretEnvironmentVariables } : {}),
        ...(argumentsDraft(entrypoint) ? { entrypoint: argumentsDraft(entrypoint) } : {}),
        ...(argumentsDraft(args) ? { args: argumentsDraft(args) } : {}),
      });
    } catch {
      setConfigurationError(true);
    }
  }

  return (
    <Overlay title={t('Create deployment')} onClose={onCancel} busy={busy}>
      <form className="models-form" aria-label={t('Create deployment')} onSubmit={submit}>
        <label>{t('Deployment name')}<input autoFocus required maxLength={128} value={draft.name} aria-describedby="deployment-name-state" onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></label>
        <p className={`models-validation-state ${nameState}`} id="deployment-name-state" aria-live="polite">
          {nameState === 'checking' ? t('Checking name…') : nameState === 'available' ? t('Name is available') : nameState === 'unavailable' ? t('Name is not available') : nameState === 'error' ? <>{t('Unable to check this name.')} <button type="button" className="models-inline-link" onClick={() => setNameAttempt((value) => value + 1)}>{t('Retry')}</button></> : ''}
        </p>
        <label>{t('Container image')}<input required maxLength={2_048} placeholder="registry.example/image:tag" value={draft.image} onChange={(e) => setDraft({ ...draft, image: e.target.value })} /></label>
        <div className="models-form-grid">
          <label>{t('Hardware')}
            <select required value={draft.hardwareId || ''} disabled={hardware.status !== 'ready' || hardware.value.length === 0} onChange={(e) => {
              const hardwareId = Number(e.target.value);
              const option = hardware.status === 'ready' ? hardware.value.find((item) => item.id === hardwareId) : undefined;
              setDraft((current) => ({ ...current, hardwareId, locationIds: [], GPUsPerContainer: Math.min(current.GPUsPerContainer, option?.maxGPUs ?? 1) }));
            }}>
              <option value="">{hardware.status === 'loading' ? t('Loading hardware…') : t('Select')}</option>
              {hardware.status === 'ready' && hardware.value.map((item) => <option value={item.id} key={item.id}>{item.brandName ? `${item.brandName} ` : ''}{item.name} · {item.availableCount} {t('Available')}</option>)}
            </select>
          </label>
          <label>{t('GPUs per container')}<input required type="number" min={1} max={selectedHardware?.maxGPUs ?? 1_024} value={draft.GPUsPerContainer} onChange={(e) => setDraft({ ...draft, GPUsPerContainer: Number(e.target.value), locationIds: [] })} /></label>
          <label>{t('Replica count')}<input required type="number" min={1} max={1_000} value={draft.replicaCount} onChange={(e) => setDraft({ ...draft, replicaCount: Number(e.target.value) })} /></label>
          <label>{t('Duration hours')}<input required type="number" min={1} max={43_920} value={draft.durationHours} onChange={(e) => setDraft({ ...draft, durationHours: Number(e.target.value) })} /></label>
          <label>{t('Billing currency')}<select value={currency} onChange={(event) => setCurrency(event.target.value as 'usdc' | 'iocoin')}><option value="usdc">USDC</option><option value="iocoin">IOCOIN</option></select></label>
        </div>
        {hardware.status === 'error' && <div className="models-inline-error" role="alert"><span>{t('Unable to load hardware.')}</span><button type="button" onClick={() => setHardwareAttempt((value) => value + 1)}>{t('Retry')}</button></div>}
        {hardware.status === 'ready' && hardware.value.length === 0 && <p className="models-empty-note">{t('No hardware is available.')}</p>}
        <fieldset className="models-option-fieldset" aria-describedby={locationError ? 'deployment-location-error' : undefined}>
          <legend>{t('Locations')}</legend>
          {replicas.status === 'loading' ? <p role="status">{t('Loading locations…')}</p> : replicas.status === 'error' ? <div className="models-inline-error" role="alert"><span>{t('Unable to load locations.')}</span><button type="button" onClick={() => setReplicaAttempt((value) => value + 1)}>{t('Retry')}</button></div> : replicas.value.length === 0 ? <p>{t('No locations are available for this hardware.')}</p> : <div className="models-checkbox-grid">{replicas.value.map((replica) => <label key={replica.locationId}><input type="checkbox" disabled={replica.availableCount < 1} checked={draft.locationIds.includes(replica.locationId)} onChange={(event) => setDraft((current) => ({ ...current, locationIds: event.target.checked ? [...current.locationIds, replica.locationId] : current.locationIds.filter((id) => id !== replica.locationId) }))} />{replica.locationName} · {replica.availableCount} {t('Available')}</label>)}</div>}
        </fieldset>
        {locationError && <p className="models-field-error" id="deployment-location-error" role="alert">{t('Select one or more available locations.')}</p>}
        <section className="models-price-estimate" aria-live="polite" aria-busy={priceState.status === 'loading'}>
          <strong>{t('Price estimation')}</strong>
          {priceState.status === 'idle' ? <span>{t('Select hardware and locations to estimate the price.')}</span> : priceState.status === 'loading' ? <span>{t('Estimating price…')}</span> : priceState.status === 'error' ? <span>{t('Unable to estimate price.')} <button type="button" className="models-inline-link" onClick={() => setPriceAttempt((value) => value + 1)}>{t('Retry')}</button></span> : <span>{t('Estimated cost')}: {formatPriceEstimate(priceState.value.totalCost)} {priceState.value.currency.toUpperCase()}</span>}
        </section>
        <details className="models-advanced"><summary>{t('Advanced configuration')}</summary><div className="models-form-grid">
          <label>{t('Traffic port')}<input type="number" min={1} max={65_535} value={draft.trafficPort ?? ''} onChange={(e) => setDraft({ ...draft, trafficPort: Number(e.target.value) })} /></label>
          <label>{t('Entrypoint (space separated)')}<input maxLength={4_096} value={entrypoint} onChange={(e) => setEntrypoint(e.target.value)} /></label>
          <label>{t('Arguments (space separated)')}<input maxLength={4_096} value={args} onChange={(e) => setArgs(e.target.value)} /></label>
          <label>{t('Registry username')}<input maxLength={4_096} autoComplete="off" value={draft.registryUsername} onChange={(e) => setDraft({ ...draft, registryUsername: e.target.value })} /></label>
          <label>{t('Registry secret')}<input type="password" maxLength={4_096} autoComplete="new-password" value={draft.registrySecret} onChange={(e) => setDraft({ ...draft, registrySecret: e.target.value })} /></label>
        </div>
        <label>{t('Environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"KEY":"value"}'} value={environmentJSON} aria-invalid={configurationError} onChange={(e) => setEnvironmentJSON(e.target.value)} /></label>
        <label>{t('Secret environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"TOKEN":"value"}'} value={secretEnvironmentJSON} aria-invalid={configurationError} onChange={(e) => setSecretEnvironmentJSON(e.target.value)} /></label>
        </details>
        {configurationError && <p className="models-field-error" role="alert">{t('Configuration must be valid JSON objects with string values.')}</p>}
        <p className="models-security-note">{t('Registry credentials are sent only when you submit and are never displayed again.')}</p>
        <footer className="models-dialog-actions"><button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button><button type="submit" className="primary" disabled={busy || hardware.status !== 'ready' || hardware.value.length === 0 || nameState !== 'available'}>{busy ? t('Creating…') : t('Create deployment')}</button></footer>
      </form>
    </Overlay>
  );
}

function formatPriceEstimate(value: number): string {
  return new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 6 }).format(value);
}

function DeploymentUpdater({ deployment, busy, onCancel, onSave }: {
  deployment: DeploymentSummary;
  busy: boolean;
  onCancel: () => void;
  onSave: (input: DeploymentUpdateInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [image, setImage] = useState('');
  const [trafficPort, setTrafficPort] = useState('');
  const [command, setCommand] = useState('');
  const [entrypoint, setEntrypoint] = useState('');
  const [args, setArgs] = useState('');
  const [registryUsername, setRegistryUsername] = useState('');
  const [registrySecret, setRegistrySecret] = useState('');
  const [environmentJSON, setEnvironmentJSON] = useState('');
  const [secretEnvironmentJSON, setSecretEnvironmentJSON] = useState('');
  const [error, setError] = useState<'none' | 'empty' | 'configuration'>('none');

  function submit(event: FormEvent) {
    event.preventDefault();
    try {
      const input: DeploymentUpdateInput = {
        ...(image.trim() ? { image } : {}),
        ...(trafficPort ? { trafficPort: Number(trafficPort) } : {}),
        ...(command.trim() ? { command } : {}),
        ...(entrypoint.trim() ? { entrypoint: argumentsDraft(entrypoint) } : {}),
        ...(args.trim() ? { args: argumentsDraft(args) } : {}),
        ...(registryUsername.trim() ? { registryUsername } : {}),
        ...(registrySecret ? { registrySecret } : {}),
        ...(environmentJSON.trim() ? { environmentVariables: environmentDraft(environmentJSON) } : {}),
        ...(secretEnvironmentJSON.trim()
          ? { secretEnvironmentVariables: environmentDraft(secretEnvironmentJSON) }
          : {}),
      };
      if (Object.keys(input).length === 0) {
        setError('empty');
        return;
      }
      setError('none');
      void onSave(input);
    } catch {
      setError('configuration');
    }
  }

  return (
    <Overlay title={t('Update deployment')} onClose={onCancel} busy={busy}>
      <form className="models-form" aria-label={t('Update deployment')} onSubmit={submit}>
        <p className="models-security-note">{t('Only the settings entered below will be changed for {{name}}.', { name: deployment.name })}</p>
        <label>{t('Container image')}<input autoFocus maxLength={2_048} placeholder="registry.example/image:tag" value={image} onChange={(event) => setImage(event.target.value)} /></label>
        <div className="models-form-grid">
          <label>{t('Traffic port')}<input type="number" min={1} max={65_535} value={trafficPort} onChange={(event) => setTrafficPort(event.target.value)} /></label>
          <label>{t('Command')}<input maxLength={4_096} value={command} onChange={(event) => setCommand(event.target.value)} /></label>
          <label>{t('Entrypoint (space separated)')}<input maxLength={4_096} value={entrypoint} onChange={(event) => setEntrypoint(event.target.value)} /></label>
          <label>{t('Arguments (space separated)')}<input maxLength={4_096} value={args} onChange={(event) => setArgs(event.target.value)} /></label>
          <label>{t('Registry username')}<input maxLength={4_096} autoComplete="off" value={registryUsername} onChange={(event) => setRegistryUsername(event.target.value)} /></label>
          <label>{t('Registry secret')}<input type="password" maxLength={4_096} autoComplete="new-password" value={registrySecret} onChange={(event) => setRegistrySecret(event.target.value)} /></label>
        </div>
        <label>{t('Environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"KEY":"value"}'} value={environmentJSON} aria-invalid={error === 'configuration'} onChange={(event) => setEnvironmentJSON(event.target.value)} /></label>
        <label>{t('Secret environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"TOKEN":"value"}'} value={secretEnvironmentJSON} aria-invalid={error === 'configuration'} onChange={(event) => setSecretEnvironmentJSON(event.target.value)} /></label>
        {error === 'empty' && <p className="models-field-error" role="alert">{t('Enter at least one setting to update.')}</p>}
        {error === 'configuration' && <p className="models-field-error" role="alert">{t('Configuration must be valid JSON objects with string values.')}</p>}
        <p className="models-security-note">{t('Registry credentials and secret variables are sent only when you submit and are never displayed again.')}</p>
        <footer className="models-dialog-actions"><button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button><button type="submit" className="primary" disabled={busy}>{busy ? t('Saving…') : t('Update')}</button></footer>
      </form>
    </Overlay>
  );
}

function DeploymentDetails({ detail, onClose }: { detail: DeploymentDetail; onClose: () => void }) {
  const { t } = useTranslation();
  return <Overlay title={t('Deployment details')} onClose={onClose}><dl className="models-detail-grid"><div><dt>{t('Deployment ID')}</dt><dd>{detail.id}</dd></div><div><dt>{t('Status')}</dt><dd>{detail.status}</dd></div><div><dt>{t('Hardware')}</dt><dd>{detail.brandName} {detail.hardwareName} (#{detail.hardwareId})</dd></div><div><dt>{t('GPUs')}</dt><dd>{detail.totalGPUs} · {detail.GPUsPerContainer} {t('per container')}</dd></div><div><dt>{t('Containers')}</dt><dd>{detail.totalContainers}</dd></div><div><dt>{t('Progress')}</dt><dd>{detail.completedPercent}%</dd></div><div><dt>{t('Minutes remaining')}</dt><dd>{formatNumber(detail.computeMinutesRemaining)}</dd></div><div><dt>{t('Amount paid')}</dt><dd>{detail.amountPaid}</dd></div><div><dt>{t('Created')}</dt><dd>{formatDate(detail.createdAt)}</dd></div></dl><footer className="models-dialog-actions"><button type="button" onClick={onClose}>{t('Close')}</button></footer></Overlay>;
}

function ContainersDialog({ deployment, containers, selected, logs, loading, error = false, onClose, onDetail, onLogs, onRetry }: {
  deployment: DeploymentSummary;
  containers: DeploymentContainer[];
  selected: DeploymentContainer | null;
  logs: string | null;
  loading: boolean;
  onClose: () => void;
  onDetail: (id: string) => void;
  onLogs: (id: string) => void;
  error: boolean;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Overlay title={t('Containers for {{name}}', { name: deployment.name })} onClose={onClose}>
      {loading && <p role="status" className="models-inline-progress">{t('Loading container data…')}</p>}
      {error && <div className="models-inline-error" role="alert"><span>{t('Unable to load container data.')}</span><button type="button" onClick={onRetry}>{t('Retry')}</button></div>}
      {containers.length === 0 && !loading && !error ? <p className="models-empty-note">{t('No containers found.')}</p> : <ul className="models-container-list">{containers.map((container) => <li key={container.containerId}><div><strong>{container.containerId}</strong><span>{container.status} · {container.hardware || t('Unknown hardware')}</span></div><div className="models-row-actions"><button type="button" onClick={() => onDetail(container.containerId)} disabled={loading}>{t('Details')}</button><button type="button" onClick={() => onLogs(container.containerId)} disabled={loading}>{t('Logs')}</button></div></li>)}</ul>}
      {selected && <section className="models-subpanel" aria-label={t('Container details')}><h3>{t('Container details')}</h3><dl><div><dt>{t('Container ID')}</dt><dd>{selected.containerId}</dd></div><div><dt>{t('Device ID')}</dt><dd>{selected.deviceId || '—'}</dd></div><div><dt>{t('Uptime')}</dt><dd>{selected.uptimePercent}%</dd></div><div><dt>{t('GPUs per container')}</dt><dd>{selected.GPUsPerContainer}</dd></div></dl>{selected.publicURL && <a href={selected.publicURL} target="_blank" rel="noreferrer">{t('Open public container URL')}</a>}<h4>{t('Container events')}</h4>{selected.events.length === 0 ? <p className="models-empty-note">{t('No container events.')}</p> : <div className="models-table-wrap compact"><table className="models-table models-events-table"><caption>{t('Container events')}</caption><thead><tr><th scope="col">{t('Time')}</th><th scope="col">{t('Message')}</th></tr></thead><tbody>{selected.events.map((event, index) => <tr key={`${event.time}:${index}`}><td>{formatDate(event.time)}</td><td><pre>{event.message}</pre></td></tr>)}</tbody></table></div>}</section>}
      {logs !== null && <section className="models-subpanel" aria-label={t('Container logs')}><h3>{t('Container logs')}</h3><pre className="models-logs">{logs || t('No log output.')}</pre></section>}
      <footer className="models-dialog-actions"><button type="button" onClick={onClose}>{t('Close')}</button></footer>
    </Overlay>
  );
}

function DeploymentSection() {
  const { t } = useTranslation();
  const [access, setAccess] = useState<DeploymentAccess | null>(null);
  const [accessPhase, setAccessPhase] = useState<'settings' | 'connection' | 'done'>('settings');
  const [accessError, setAccessError] = useState(false);
  const [connectionError, setConnectionError] = useState(false);
  const [accessReload, setAccessReload] = useState(0);
  const [draft, setDraft] = useState(EMPTY_DEPLOYMENT_QUERY);
  const [query, setQuery] = useState(EMPTY_DEPLOYMENT_QUERY);
  const [page, setPage] = useState<DeploymentPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [busyAction, setBusyAction] = useState('');
  const [notice, setNotice] = useState<Notice>(null);
  const [creatorOpen, setCreatorOpen] = useState(false);
  const [updateTarget, setUpdateTarget] = useState<DeploymentSummary | null>(null);
  const [renameTarget, setRenameTarget] = useState<DeploymentSummary | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [extendTarget, setExtendTarget] = useState<DeploymentSummary | null>(null);
  const [extendHours, setExtendHours] = useState(1);
  const [detail, setDetail] = useState<DeploymentDetail | null>(null);
  const [containerTarget, setContainerTarget] = useState<DeploymentSummary | null>(null);
  const [containers, setContainers] = useState<DeploymentContainer[]>([]);
  const [containerDetail, setContainerDetail] = useState<DeploymentContainer | null>(null);
  const [logs, setLogs] = useState<string | null>(null);
  const [containerError, setContainerError] = useState<{ kind: 'list' | 'detail' | 'logs'; containerId?: string } | null>(null);
  const [inspectionLoading, setInspectionLoading] = useState(false);
  const alive = useRef(true);
  const busyRef = useRef(false);
  const inspectionController = useRef<AbortController | null>(null);

  useEffect(() => () => {
    alive.current = false;
    inspectionController.current?.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setAccess(null);
    setAccessError(false);
    setConnectionError(false);
    setAccessPhase('settings');
    void loadDeploymentSettings(controller.signal).then(async (result) => {
      if (controller.signal.aborted) return;
      setAccess(result);
      if (!result.canConnect) {
        setAccessPhase('done');
        return;
      }
      setAccessPhase('connection');
      try {
        await testDeploymentConnection(controller.signal);
        if (!controller.signal.aborted) setAccessPhase('done');
      } catch {
        if (!controller.signal.aborted) {
          setConnectionError(true);
          setAccessPhase('done');
        }
      }
    }).catch(() => {
      if (!controller.signal.aborted) {
        setAccessError(true);
        setAccessPhase('done');
      }
    });
    return () => controller.abort();
  }, [accessReload]);

  const ready = access?.canConnect === true && accessPhase === 'done' && !connectionError;

  useEffect(() => {
    if (!ready) return;
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void loadDeployments(query, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      setPage(result);
      const lastPage = Math.max(1, Math.ceil(result.total / query.pageSize));
      if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
    }).catch(() => {
      if (!controller.signal.aborted) {
        setPage(null);
        setLoadError(true);
      }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [query, ready, reload]);

  const pageCount = Math.max(1, Math.ceil((page?.total ?? 0) / query.pageSize));

  async function runMutation(action: string, operation: () => Promise<void>, success: string): Promise<boolean> {
    if (busyRef.current) return false;
    busyRef.current = true;
    setBusyAction(action);
    setNotice(null);
    try {
      await operation();
      if (!alive.current) return true;
      setNotice({ kind: 'success', text: success });
      setReload((value) => value + 1);
      return true;
    } catch {
      if (alive.current) setNotice({ kind: 'error', text: t('The deployment operation could not be completed.') });
      return false;
    } finally {
      busyRef.current = false;
      if (alive.current) setBusyAction('');
    }
  }

  function inspect(operation: (signal: AbortSignal) => Promise<void>, onError?: () => void) {
    inspectionController.current?.abort();
    const controller = new AbortController();
    inspectionController.current = controller;
    setInspectionLoading(true);
    setNotice(null);
    void operation(controller.signal).catch(() => {
      if (!controller.signal.aborted) {
        if (onError) onError();
        else setNotice({ kind: 'error', text: t('Unable to load deployment details.') });
      }
    }).finally(() => { if (!controller.signal.aborted) setInspectionLoading(false); });
  }

  function showDetail(id: string) {
    inspect(async (signal) => {
      const result = await getDeployment(id, signal);
      if (!signal.aborted) setDetail(result);
    });
  }

  function showContainers(deployment: DeploymentSummary) {
    setContainerTarget(deployment);
    setContainers([]);
    setContainerDetail(null);
    setLogs(null);
    setContainerError(null);
    inspect(async (signal) => {
      const result = await loadDeploymentContainers(deployment.id, signal);
      if (!signal.aborted) setContainers(result);
    }, () => setContainerError({ kind: 'list' }));
  }

  function showContainerDetail(containerId: string) {
    if (!containerTarget) return;
    setContainerDetail(null);
    setLogs(null);
    setContainerError(null);
    inspect(async (signal) => {
      const result = await getDeploymentContainer(containerTarget.id, containerId, signal);
      if (!signal.aborted) setContainerDetail(result);
    }, () => setContainerError({ kind: 'detail', containerId }));
  }

  function showLogs(containerId: string) {
    if (!containerTarget) return;
    setContainerDetail(null);
    setLogs(null);
    setContainerError(null);
    inspect(async (signal) => {
      const result = await loadDeploymentLogs(containerTarget.id, containerId, signal);
      if (!signal.aborted) setLogs(result);
    }, () => setContainerError({ kind: 'logs', containerId }));
  }

  function retryContainerRequest() {
    if (!containerTarget || !containerError) return;
    const failed = containerError;
    if (failed.kind === 'list') showContainers(containerTarget);
    else if (failed.kind === 'detail' && failed.containerId) showContainerDetail(failed.containerId);
    else if (failed.kind === 'logs' && failed.containerId) showLogs(failed.containerId);
  }

  if (accessPhase === 'settings' || accessPhase === 'connection') {
    return <LoadingPanel label={accessPhase === 'settings' ? t('Loading deployment settings…') : t('Checking deployment connection…')} />;
  }
  if (accessError) return <ErrorPanel message={t('Unable to verify deployment settings.')} retry={() => setAccessReload((value) => value + 1)} />;
  if (!access?.enabled) {
    return <div className="models-access-state" role="status"><h1>{t('Model deployment is disabled')}</h1><p>{t('Enable io.net deployment in model settings before using this page.')}</p></div>;
  }
  if (!access.configured) {
    return <div className="models-access-state" role="status"><h1>{t('Model deployment is not configured')}</h1><p>{t('Add a valid io.net API key in model settings. The key is never shown here.')}</p></div>;
  }
  if (connectionError) {
    return <ErrorPanel message={t('The deployment provider connection could not be verified.')} retry={() => setAccessReload((value) => value + 1)} />;
  }

  return (
    <section className="models-section" aria-labelledby="models-deployments-title">
      <div className="models-section-heading"><div><h1 id="models-deployments-title">{t('Deployments')}</h1><p>{t('Create and operate io.net model containers.')}</p></div><button type="button" className="primary" onClick={() => setCreatorOpen(true)}>{t('Create deployment')}</button></div>
      <NoticeBanner notice={notice} />
      {inspectionLoading && <p role="status" className="models-inline-progress">{t('Loading deployment data…')}</p>}
      <form className="models-filters deployment" role="search" aria-label={t('Search deployments')} onSubmit={(event) => { event.preventDefault(); setQuery({ ...draft, keyword: draft.keyword.trim(), page: 1 }); }}>
        <label>{t('Deployment name')}<input maxLength={256} value={draft.keyword} onChange={(e) => setDraft({ ...draft, keyword: e.target.value })} /></label>
        <label>{t('Status')}<select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as DeploymentStatus })}>{DEPLOYMENT_STATUS_OPTIONS.map((status) => <option key={status || 'all'} value={status}>{status ? t(status) : t('All statuses')}</option>)}</select></label>
        <div className="models-filter-actions"><button type="submit" className="primary">{t('Apply filters')}</button><button type="button" onClick={() => { setDraft(EMPTY_DEPLOYMENT_QUERY); setQuery(EMPTY_DEPLOYMENT_QUERY); }}>{t('Clear filters')}</button></div>
      </form>
      {loading ? <LoadingPanel label={t('Loading deployments…')} /> : loadError ? <ErrorPanel message={t('Unable to load deployments.')} retry={() => setReload((value) => value + 1)} /> : page && page.items.length > 0 ? (
        <div className="models-table-wrap"><table className="models-table" aria-busy="false"><caption>{t('Model deployments')}</caption><thead><tr><th scope="col">{t('Deployment')}</th><th scope="col">{t('Status')}</th><th scope="col">{t('Hardware')}</th><th scope="col">{t('Remaining')}</th><th scope="col">{t('Progress')}</th><th scope="col">{t('Actions')}</th></tr></thead><tbody>
          {page.items.map((deployment) => <tr key={deployment.id}><th scope="row"><span className="models-primary-value">{deployment.name}</span><span className="models-secondary-value">{deployment.id}</span></th><td><span className={`models-status deployment-${deployment.status.replaceAll(' ', '-')}`}>{t(deployment.status)}</span></td><td>{deployment.hardwareInfo}</td><td>{deployment.timeRemaining}</td><td><progress max={100} value={deployment.completedPercent} aria-label={t('Deployment progress for {{name}}', { name: deployment.name })} /> {deployment.completedPercent}%</td><td><div className="models-row-actions">
            <button type="button" onClick={() => showDetail(deployment.id)} disabled={inspectionLoading}>{t('Details')}</button>
            <button type="button" onClick={() => showContainers(deployment)} disabled={inspectionLoading}>{t('Containers')}</button>
            <button type="button" onClick={() => setUpdateTarget(deployment)} disabled={Boolean(busyAction)}>{t('Edit configuration')}</button>
            <button type="button" onClick={() => { setRenameTarget(deployment); setRenameValue(deployment.name); }}>{t('Rename')}</button>
            <button type="button" onClick={() => { setExtendTarget(deployment); setExtendHours(1); }}>{t('Extend')}</button>
            <button type="button" className="danger" disabled={Boolean(busyAction)} onClick={() => { if (window.confirm(t('Delete deployment “{{name}}”?', { name: deployment.name }))) void runMutation(`delete:${deployment.id}`, () => deleteDeployment(deployment.id), t('Deployment termination requested.')); }}>{t('Delete')}</button>
          </div></td></tr>)}
        </tbody></table></div>
      ) : <div className="models-state"><p>{t('No deployments match these filters.')}</p></div>}
      <nav className="models-pagination" aria-label={t('Deployment pages')}><button type="button" disabled={loading || query.page <= 1} onClick={() => setQuery((value) => ({ ...value, page: value.page - 1 }))}>{t('Previous')}</button><span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span><button type="button" disabled={loading || query.page >= pageCount} onClick={() => setQuery((value) => ({ ...value, page: value.page + 1 }))}>{t('Next')}</button></nav>

      {creatorOpen && <DeploymentCreator busy={Boolean(busyAction)} onCancel={() => setCreatorOpen(false)} onSave={async (input) => { const ok = await runMutation('create', () => createDeployment(input), t('Deployment created.')); if (ok && alive.current) setCreatorOpen(false); }} />}
      {updateTarget && <DeploymentUpdater deployment={updateTarget} busy={Boolean(busyAction)} onCancel={() => setUpdateTarget(null)} onSave={async (input) => { const target = updateTarget; const ok = await runMutation('update', () => updateDeployment(target.id, input), t('Deployment updated.')); if (ok && alive.current) setUpdateTarget(null); }} />}
      {renameTarget && <Overlay title={t('Rename deployment')} onClose={() => setRenameTarget(null)} busy={Boolean(busyAction)}><form className="models-form" aria-label={t('Rename deployment')} onSubmit={(event) => { event.preventDefault(); const target = renameTarget; void runMutation('rename', () => renameDeployment(target.id, renameValue), t('Deployment renamed.')).then((ok) => { if (ok && alive.current) setRenameTarget(null); }); }}><label>{t('New deployment name')}<input autoFocus required maxLength={128} value={renameValue} onChange={(e) => setRenameValue(e.target.value)} /></label><footer className="models-dialog-actions"><button type="button" onClick={() => setRenameTarget(null)} disabled={Boolean(busyAction)}>{t('Cancel')}</button><button type="submit" className="primary" disabled={Boolean(busyAction)}>{busyAction === 'rename' ? t('Saving…') : t('Rename')}</button></footer></form></Overlay>}
      {extendTarget && <Overlay title={t('Extend deployment')} onClose={() => setExtendTarget(null)} busy={Boolean(busyAction)}><form className="models-form" aria-label={t('Extend deployment')} onSubmit={(event) => { event.preventDefault(); const target = extendTarget; if (!window.confirm(t('Extend deployment “{{name}}” by {{hours}} hours?', { name: target.name, hours: extendHours }))) return; void runMutation('extend', () => extendDeployment(target.id, extendHours), t('Deployment extended.')).then((ok) => { if (ok && alive.current) setExtendTarget(null); }); }}><label>{t('Additional hours')}<input autoFocus required type="number" min={1} max={43_920} value={extendHours} onChange={(e) => setExtendHours(Number(e.target.value))} /></label><footer className="models-dialog-actions"><button type="button" onClick={() => setExtendTarget(null)} disabled={Boolean(busyAction)}>{t('Cancel')}</button><button type="submit" className="primary" disabled={Boolean(busyAction)}>{busyAction === 'extend' ? t('Extending…') : t('Extend')}</button></footer></form></Overlay>}
      {detail && <DeploymentDetails detail={detail} onClose={() => setDetail(null)} />}
      {containerTarget && <ContainersDialog deployment={containerTarget} containers={containers} selected={containerDetail} logs={logs} loading={inspectionLoading} error={containerError !== null} onRetry={retryContainerRequest} onClose={() => { inspectionController.current?.abort(); setContainerTarget(null); setContainers([]); setContainerDetail(null); setLogs(null); setContainerError(null); setInspectionLoading(false); }} onDetail={showContainerDetail} onLogs={showLogs} />}
    </section>
  );
}

export function ModelsView({ section, role, onNavigate }: ModelsViewProps) {
  const { t } = useTranslation();
  try {
    assertModelsAdministrator(role);
  } catch {
    return <section className="models-access-state" role="alert"><h1>{t('Administrator access required')}</h1><p>{t('You do not have permission to manage model metadata or deployments.')}</p></section>;
  }
  const activeSection = MODEL_SECTIONS.includes(section) ? section : 'metadata';
  return (
    <main className="models-view">
      <nav className="models-tabs" aria-label={t('Model management sections')}>
        {MODEL_SECTIONS.map((value) => <button type="button" key={value} aria-current={activeSection === value ? 'page' : undefined} className={activeSection === value ? 'active' : ''} onClick={() => { if (value !== activeSection) onNavigate?.(`/models/${value}`); }}>{value === 'metadata' ? t('Metadata') : t('Deployments')}</button>)}
      </nav>
      {activeSection === 'metadata' ? <MetadataSection /> : <DeploymentSection />}
    </main>
  );
}
