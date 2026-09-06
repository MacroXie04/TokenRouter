import { providerOtherLabel } from './channel-editor-model';
import {
  CHANNEL_PROVIDER_OPTIONS,
  channelProviderLabel,
  isChannelProviderType,
} from './channel-providers';
import type { ChannelAdminViewController } from './useChannelController';

type ChannelEditorProps = Pick<ChannelAdminViewController,
  'busyAction' | 'canSensitiveWrite' | 'canWrite' | 'chooseDiscoveredModels'
  | 'discoverModelsFor' | 'draftDiscovery' | 'editing' | 'editingSourceType'
  | 'saveEdit' | 'setDraftDiscovery' | 'setEditing' | 'setEditingSourceType'
  | 't' | 'updateProviderSetting' | 'updateSensitiveDetail'
>;

export function ChannelEditor({
  busyAction, canSensitiveWrite, canWrite, chooseDiscoveredModels,
  discoverModelsFor, draftDiscovery, editing, editingSourceType,
  saveEdit, setDraftDiscovery, setEditing, setEditingSourceType,
  t, updateProviderSetting, updateSensitiveDetail,
}: ChannelEditorProps) {
  return (
    <>
      {editing && canWrite && (
        <form id="channel-edit-panel" className="channel-subpanel grid-form" onSubmit={saveEdit}>
          <h3>{t('Edit channel {{name}}', { name: editing.name })}</h3>
          <label>{t('Name')}<input value={editing.name} maxLength={191} required onChange={(event) => setEditing((current) => current && ({ ...current, name: event.target.value }))} /></label>
          <label>{t('Group')}<input value={editing.group} maxLength={64} onChange={(event) => setEditing((current) => current && ({ ...current, group: event.target.value }))} /></label>
          <label>{t('Models')}<textarea rows={4} value={editing.models} maxLength={256 * 1024} onChange={(event) => setEditing((current) => current && ({ ...current, models: event.target.value }))} /></label>
          {canSensitiveWrite && editing.sensitive && editing.type === 58 && editingSourceType === 58 && (
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void discoverModelsFor('edit')}>
              {busyAction === 'draft-models:edit' ? t('Discovering…') : t('Discover models from draft')}
            </button>
          )}
          {draftDiscovery?.target === 'edit' && (
            <fieldset>
              <legend>{t('Discovered {{count}} upstream models', { count: draftDiscovery.models.length })}</legend>
              <p className="muted">{t('Merge keeps the current draft. Replace removes any model the upstream did not return.')}</p>
              <div className="channel-filter-actions">
                <button type="button" onClick={() => chooseDiscoveredModels('edit', true)}>{t('Merge models')}</button>
                <button type="button" onClick={() => chooseDiscoveredModels('edit', false)}>{t('Replace all models')}</button>
                <button type="button" className="link" onClick={() => setDraftDiscovery(null)}>{t('Cancel')}</button>
              </div>
            </fieldset>
          )}
          <label>{t('Tag')}<input value={editing.tag} maxLength={64} onChange={(event) => setEditing((current) => current && ({ ...current, tag: event.target.value }))} /></label>
          <label>{t('Remark')}<input value={editing.remark} maxLength={255} onChange={(event) => setEditing((current) => current && ({ ...current, remark: event.target.value }))} /></label>
          <label>{t('Priority')}<input type="number" value={editing.priority} onChange={(event) => setEditing((current) => current && ({ ...current, priority: Number(event.target.value) }))} /></label>
          <label>{t('Weight')}<input type="number" min={0} max={4_294_967_295} value={editing.weight} onChange={(event) => setEditing((current) => current && ({ ...current, weight: Number(event.target.value) }))} /></label>
          <label>{t('Test model')}<input list="enabled-channel-models" value={editing.testModel} maxLength={255} onChange={(event) => setEditing((current) => current && ({ ...current, testModel: event.target.value }))} /></label>
          <label className="channel-checkbox"><input type="checkbox" checked={editing.autoBan === 1} onChange={(event) => setEditing((current) => current && ({ ...current, autoBan: event.target.checked ? 1 : 0 }))} /> {t('Automatically disable on failed tests')}</label>
          <label>{t('Model mapping')}<textarea rows={3} value={editing.modelMapping} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setEditing((current) => current && ({ ...current, modelMapping: event.target.value }))} /></label>
          <label>{t('Status code mapping')}<textarea rows={3} value={editing.statusCodeMapping} maxLength={1_024} placeholder="{}" onChange={(event) => setEditing((current) => current && ({ ...current, statusCodeMapping: event.target.value }))} /></label>
          {canSensitiveWrite && editing.sensitive ? <fieldset>
            <legend>{t('Sensitive provider settings')}</legend>
            <p className="muted">{t('The stored API key remains masked and is never loaded into this form.')}</p>
            <label>
              {t('Type')}
              <select
                value={editing.type}
                onChange={(event) => {
                  const type = Number(event.target.value);
                  if (isChannelProviderType(type)) setEditing((current) => current && ({ ...current, type }));
                }}
              >
                {!isChannelProviderType(editing.type) && <option value={editing.type}>{channelProviderLabel(editing.type)}</option>}
                {CHANNEL_PROVIDER_OPTIONS.map((provider) => <option key={provider.value} value={provider.value}>{provider.label} (#{provider.value})</option>)}
              </select>
            </label>
            <label>{t('Base URL')}<input value={editing.sensitive.baseUrl} maxLength={4_096} onChange={(event) => updateSensitiveDetail('baseUrl', event.target.value)} /></label>
            <label>{t('OpenAI organization')}<input value={editing.sensitive.organization} maxLength={512} onChange={(event) => updateSensitiveDetail('organization', event.target.value)} /></label>
            {editing.sensitive.other === undefined
              ? <p className="muted">{t('The legacy provider field contains a credential collection and remains masked.')}</p>
              : <label>{providerOtherLabel(editing.type, t)}<textarea rows={2} value={editing.sensitive.other} maxLength={256 * 1024} onChange={(event) => updateSensitiveDetail('other', event.target.value)} /></label>}
            <label>{t('Parameter override')}<textarea rows={3} value={editing.sensitive.paramOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => updateSensitiveDetail('paramOverride', event.target.value)} /></label>
            <label>{t('Header override')}<textarea rows={3} value={editing.sensitive.headerOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => updateSensitiveDetail('headerOverride', event.target.value)} /></label>
            <label>{t('Balance URL')}<input value={editing.sensitive.providerSettings.balanceUrl} maxLength={4_096} onChange={(event) => updateProviderSetting('balanceUrl', event.target.value)} /></label>
            {editing.type === 3 && <label>{t('Azure Responses API version')}<input value={editing.sensitive.providerSettings.azureResponsesVersion} maxLength={128} onChange={(event) => updateProviderSetting('azureResponsesVersion', event.target.value)} /></label>}
            {editing.type === 41 && <label>
              {t('Vertex AI key format')}
              <select value={editing.sensitive.providerSettings.vertexKeyType} onChange={(event) => updateProviderSetting('vertexKeyType', event.target.value as 'json' | 'api_key')}>
                <option value="json">{t('Service account JSON')}</option>
                <option value="api_key">{t('API key')}</option>
              </select>
            </label>}
            {editing.type === 33 && <>
              <label>
                {t('AWS key format')}
                <select value={editing.sensitive.providerSettings.awsKeyType} onChange={(event) => updateProviderSetting('awsKeyType', event.target.value as 'auto' | 'ak_sk' | 'api_key')}>
                  <option value="auto">{t('Auto-detect')}</option>
                  <option value="ak_sk">{t('Access key and secret')}</option>
                  <option value="api_key">{t('API key')}</option>
                </select>
              </label>
              <label className="channel-checkbox"><input type="checkbox" checked={editing.sensitive.providerSettings.passThroughBodyEnabled} onChange={(event) => updateProviderSetting('passThroughBodyEnabled', event.target.checked)} /> {t('Pass request body through')}</label>
            </>}
            {editing.type === 58 && <label>{t('Advanced Custom routes')}<textarea required rows={8} value={editing.sensitive.providerSettings.advancedCustom} maxLength={512 * 1024} placeholder="{}" onChange={(event) => updateProviderSetting('advancedCustom', event.target.value)} /></label>}
            <label className="channel-checkbox"><input type="checkbox" checked={editing.sensitive.providerSettings.upstreamCheckEnabled} onChange={(event) => updateProviderSetting('upstreamCheckEnabled', event.target.checked)} /> {t('Check upstream model changes')}</label>
            <label className="channel-checkbox"><input type="checkbox" disabled={!editing.sensitive.providerSettings.upstreamCheckEnabled} checked={editing.sensitive.providerSettings.upstreamCheckEnabled && editing.sensitive.providerSettings.upstreamAutoSyncEnabled} onChange={(event) => updateProviderSetting('upstreamAutoSyncEnabled', event.target.checked)} /> {t('Automatically add discovered models')}</label>
            <label>{t('Ignored upstream models')}<textarea rows={2} value={editing.sensitive.providerSettings.upstreamIgnoredModels} maxLength={256 * 1024} placeholder={t('Comma-separated model names or regex: patterns')} onChange={(event) => updateProviderSetting('upstreamIgnoredModels', event.target.value)} /></label>
          </fieldset> : <p className="muted">{t('Sensitive provider fields are hidden because this account lacks sensitive-write permission.')}</p>}
          <div className="channel-filter-actions">
            <button type="submit" disabled={busyAction === `edit:${editing.id}`}>{busyAction === `edit:${editing.id}` ? t('Saving…') : t('Save changes')}</button>
            <button type="button" className="link" disabled={busyAction === `edit:${editing.id}`} onClick={() => { setEditing(null); setEditingSourceType(null); setDraftDiscovery((current) => current?.target === 'edit' ? null : current); }}>{t('Cancel')}</button>
          </div>
        </form>
      )}
    </>
  );
}
