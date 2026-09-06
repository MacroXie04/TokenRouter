import {
  CHANNEL_TYPE_CODEX,
  type ChannelCreateInput
} from './channel-api';
import { UPSTREAM_DISCOVERY_TYPES } from './channel-editor-model';
import {
  CHANNEL_PROVIDER_OPTIONS,
  isChannelProviderType
} from './channel-providers';
import type { ChannelAdminViewController } from './useChannelController';

type ChannelCreatePanelProps = Pick<ChannelAdminViewController,
  'busyAction' | 'canSensitiveWrite' | 'chooseDiscoveredModels' | 'createDraft'
  | 'discoverModelsFor' | 'draftDiscovery' | 'setCreateDraft' | 'setDraftDiscovery'
  | 'submitCreate' | 't'
>;

export function ChannelCreatePanel({
  busyAction, canSensitiveWrite, chooseDiscoveredModels, createDraft,
  discoverModelsFor, draftDiscovery, setCreateDraft, setDraftDiscovery,
  submitCreate, t,
}: ChannelCreatePanelProps) {
  return (
    <>
      {canSensitiveWrite && (
        <details className="channel-create">
          <summary>{t('Create channel')}</summary>
          <form className="grid-form" onSubmit={submitCreate}>
            <p className="muted">{t('API keys are sent once and are never displayed by this screen.')}</p>
            <label>
              {t('Add mode')}
              <select value={createDraft.mode} onChange={(event) => setCreateDraft((current) => ({ ...current, mode: event.target.value as ChannelCreateInput['mode'] }))}>
                <option value="single">{t('Single key')}</option>
                {createDraft.type !== CHANNEL_TYPE_CODEX && <option value="batch">{t('Batch channels (one key per line)')}</option>}
                {createDraft.type !== CHANNEL_TYPE_CODEX && <option value="multi_to_single">{t('Multiple keys in one channel')}</option>}
              </select>
            </label>
            {createDraft.mode === 'multi_to_single' && (
              <label>
                {t('Multi-key strategy')}
                <select value={createDraft.multi_key_mode} onChange={(event) => setCreateDraft((current) => ({ ...current, multi_key_mode: event.target.value as ChannelCreateInput['multi_key_mode'] }))}>
                  <option value="random">{t('Random')}</option>
                  <option value="polling">{t('Polling')}</option>
                </select>
              </label>
            )}
            {createDraft.mode === 'batch' && (
              <label className="channel-checkbox">
                <input type="checkbox" checked={createDraft.batch_add_set_key_prefix_2_name} onChange={(event) => setCreateDraft((current) => ({ ...current, batch_add_set_key_prefix_2_name: event.target.checked }))} />
                {t('Add a key fingerprint to each batch channel name')}
              </label>
            )}
            <label>{t('Name')}<input value={createDraft.name} maxLength={191} required onChange={(event) => setCreateDraft((current) => ({ ...current, name: event.target.value }))} /></label>
            <label>
              {t('Type')}
              <select
                value={createDraft.type}
                required
                onChange={(event) => {
                  const type = Number(event.target.value);
                  if (!isChannelProviderType(type)) return;
                  setDraftDiscovery((current) => current?.target === 'create' ? null : current);
                  setCreateDraft((current) => ({
                    ...current,
                    type,
                    ...(type === CHANNEL_TYPE_CODEX ? { mode: 'single' as const } : {}),
                  }));
                }}
              >
                {CHANNEL_PROVIDER_OPTIONS
                  // Advanced Custom creation requires a route configuration
                  // that this bounded create form does not submit. Keep it
                  // editable without offering a create action that must fail.
                  .filter((provider) => provider.value !== 58)
                  .map((provider) => <option key={provider.value} value={provider.value}>{provider.label} (#{provider.value})</option>)}
              </select>
            </label>
            {createDraft.mode === 'single' ? (
              <label>{t('API key')}<input type="password" autoComplete="new-password" value={createDraft.key} maxLength={512 * 1024} onChange={(event) => setCreateDraft((current) => ({ ...current, key: event.target.value }))} /></label>
            ) : (
              <label>{t('API keys, one per line')}<textarea className="channel-secret-textarea" rows={6} autoComplete="off" spellCheck={false} value={createDraft.key} maxLength={512 * 1024} onChange={(event) => setCreateDraft((current) => ({ ...current, key: event.target.value }))} /></label>
            )}
            <label>{t('Base URL')}<input type="url" value={createDraft.base_url} maxLength={4_096} placeholder="https://api.example.com" onChange={(event) => setCreateDraft((current) => ({ ...current, base_url: event.target.value }))} /></label>
            <label>{t('Models')}<textarea rows={3} value={createDraft.models} maxLength={256 * 1024} onChange={(event) => setCreateDraft((current) => ({ ...current, models: event.target.value }))} /></label>
            {UPSTREAM_DISCOVERY_TYPES.has(createDraft.type) && createDraft.type !== 58 && (
              <button type="button" disabled={Boolean(busyAction)} onClick={() => void discoverModelsFor('create')}>
                {busyAction === 'draft-models:create' ? t('Discovering…') : t('Discover models from draft')}
              </button>
            )}
            {draftDiscovery?.target === 'create' && (
              <fieldset>
                <legend>{t('Discovered {{count}} upstream models', { count: draftDiscovery.models.length })}</legend>
                <p className="muted">{t('Merge keeps the current draft. Replace removes any model the upstream did not return.')}</p>
                <div className="channel-filter-actions">
                  <button type="button" onClick={() => chooseDiscoveredModels('create', true)}>{t('Merge models')}</button>
                  <button type="button" onClick={() => chooseDiscoveredModels('create', false)}>{t('Replace all models')}</button>
                  <button type="button" className="link" onClick={() => setDraftDiscovery(null)}>{t('Cancel')}</button>
                </div>
              </fieldset>
            )}
            <label>{t('Group')}<input value={createDraft.group} maxLength={64} onChange={(event) => setCreateDraft((current) => ({ ...current, group: event.target.value }))} /></label>
            <button type="submit" disabled={busyAction === 'create'}>{busyAction === 'create' ? t('Creating…') : t('Add channel')}</button>
          </form>
        </details>
      )}
    </>
  );
}
