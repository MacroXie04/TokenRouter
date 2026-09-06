import type { FormEvent } from 'react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { EMPTY_MODEL, EMPTY_VENDOR, ModelEditor, VendorEditor, modelInput, vendorInput } from './ModelEditors';
import { createModel, createVendor, deleteModel, deleteVendor, getModel, getVendor, loadMissingModels, loadModels, loadVendors, previewUpstream, setModelStatus, syncUpstream, updateModel, updateVendor } from "./metadata-api";
import { MODEL_PAGE_SIZE, type MetadataPage, type ModelMutationInput, type ModelQuery, type SyncLocale, type SyncPreview, type VendorMetadata, type VendorMutationInput, type VendorPage } from "./metadata-contracts";
import { ErrorPanel, LoadingPanel, NoticeBanner, Overlay, formatDate, formatNumber, type EditorMode, type Notice } from './models-ui';

export const EMPTY_MODEL_QUERY: ModelQuery = {
  keyword: '',
  vendor: '',
  status: '',
  syncOfficial: '',
  page: 1,
  pageSize: MODEL_PAGE_SIZE,
};

export function MetadataSection() {
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
