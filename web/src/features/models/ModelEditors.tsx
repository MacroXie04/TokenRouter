import type { FormEvent } from 'react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { type ModelMetadata, type ModelMutationInput, type VendorMetadata, type VendorMutationInput } from "./metadata-contracts";
import { Overlay, type EditorMode } from './models-ui';

export const EMPTY_MODEL: ModelMutationInput = {
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

export const EMPTY_VENDOR: VendorMutationInput = {
  name: '',
  description: '',
  icon: '',
  status: 1,
};

export function modelInput(value: ModelMetadata): ModelMutationInput & { id: number } {
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

export function vendorInput(value: VendorMetadata): VendorMutationInput & { id: number } {
  return {
    id: value.id,
    name: value.name,
    description: value.description,
    icon: value.icon,
    status: value.status,
  };
}

export function ModelEditor({ mode, initial, vendors, busy, onCancel, onSave }: {
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

export function VendorEditor({ mode, initial, busy, onCancel, onSave }: {
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
