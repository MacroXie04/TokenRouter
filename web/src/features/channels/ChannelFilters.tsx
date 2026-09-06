import { type SearchDraft } from './channel-editor-model';
import type { ChannelAdminViewController } from './useChannelController';

type ChannelFiltersProps = Pick<ChannelAdminViewController,
  'catalog' | 'clearSearch' | 'draft' | 'setDraft'
  | 'submitSearch' | 't' | 'typeCounts' | 'typeOptions'
>;

export function ChannelFilters({ catalog, clearSearch, draft, setDraft, submitSearch, t, typeCounts, typeOptions }: ChannelFiltersProps) {
  return (
    <>
      <form className="channel-filters" role="search" aria-label={t('Search channels')} onSubmit={submitSearch}>
        <label>
          {t('Search channels')}
          <input
            value={draft.keyword}
            maxLength={128}
            placeholder={t('Name or channel ID')}
            onChange={(event) => setDraft((current) => ({ ...current, keyword: event.target.value }))}
          />
        </label>
        <label>
          {t('Group')}
          <input value={draft.group} maxLength={512} onChange={(event) => setDraft((current) => ({ ...current, group: event.target.value }))} />
        </label>
        <label>
          {t('Model')}
          <input
            value={draft.model}
            maxLength={255}
            list="enabled-channel-models"
            onChange={(event) => setDraft((current) => ({ ...current, model: event.target.value }))}
          />
          <datalist id="enabled-channel-models">{catalog.map((model) => <option key={model} value={model} />)}</datalist>
        </label>
        <label>
          {t('Status')}
          <select value={draft.status} onChange={(event) => setDraft((current) => ({ ...current, status: event.target.value as SearchDraft['status'] }))}>
            <option value="">{t('All statuses')}</option>
            <option value="enabled">{t('Enabled')}</option>
            <option value="disabled">{t('Disabled')}</option>
          </select>
        </label>
        <label>
          {t('Type')}
          <select
            value={draft.type ?? ''}
            onChange={(event) => setDraft((current) => ({ ...current, type: event.target.value === '' ? null : Number(event.target.value) }))}
          >
            <option value="">{t('All types')}</option>
            {typeOptions.map((type) => <option key={type} value={type}>{type} ({typeCounts[type] ?? 0})</option>)}
          </select>
        </label>
        <label>
          {t('Sort by')}
          <select value={draft.sortBy} onChange={(event) => setDraft((current) => ({ ...current, sortBy: event.target.value as SearchDraft['sortBy'] }))}>
            <option value="">{t('Default priority order')}</option>
            <option value="id">{t('ID')}</option>
            <option value="name">{t('Name')}</option>
            <option value="priority">{t('Priority')}</option>
            <option value="balance">{t('Balance')}</option>
            <option value="response_time">{t('Response time')}</option>
            <option value="test_time">{t('Test time')}</option>
          </select>
        </label>
        <label>
          {t('Sort order')}
          <select value={draft.sortOrder} disabled={!draft.sortBy} onChange={(event) => setDraft((current) => ({ ...current, sortOrder: event.target.value as SearchDraft['sortOrder'] }))}>
            <option value="desc">{t('Descending')}</option>
            <option value="asc">{t('Ascending')}</option>
          </select>
        </label>
        <label className="channel-checkbox">
          <input type="checkbox" checked={draft.tagMode} onChange={(event) => setDraft((current) => ({ ...current, tagMode: event.target.checked }))} />
          {t('Keep matching tags together')}
        </label>
        <label className="channel-checkbox">
          <input type="checkbox" checked={draft.idSort} disabled={Boolean(draft.sortBy)} onChange={(event) => setDraft((current) => ({ ...current, idSort: event.target.checked }))} />
          {t('Prefer ID order')}
        </label>
        <div className="channel-filter-actions">
          <button type="submit">{t('Apply filters')}</button>
          <button type="button" className="link" onClick={clearSearch}>{t('Clear filters')}</button>
        </div>
      </form>
    </>
  );
}
