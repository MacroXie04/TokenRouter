import type { ChannelAdminViewController } from './useChannelController';

type ChannelBulkActionsProps = Pick<ChannelAdminViewController,
  'bulkTag' | 'busyAction' | 'canOperate' | 'canSelect'
  | 'canSensitiveWrite' | 'canWrite' | 'loading' | 'removeSelected'
  | 'selectedIDs' | 'setBulkTag' | 'setSelected' | 't'
  | 'tagSelected' | 'updateSelectedStatus'
>;

export function ChannelBulkActions({
  bulkTag, busyAction, canOperate, canSelect,
  canSensitiveWrite, canWrite, loading, removeSelected,
  selectedIDs, setBulkTag, setSelected, t,
  tagSelected, updateSelectedStatus,
}: ChannelBulkActionsProps) {
  return (
    <>
      {canSelect && selectedIDs.length > 0 && (
        <section className="channel-bulk" aria-label={t('Selected channel actions')}>
          <p role="status">{t('{{count}} channels selected.', { count: selectedIDs.length })}</p>
          <div className="channel-filter-actions">
            {canOperate && <button type="button" disabled={loading || Boolean(busyAction)} onClick={() => void updateSelectedStatus(true)}>{t('Enable selected')}</button>}
            {canOperate && <button type="button" disabled={loading || Boolean(busyAction)} onClick={() => void updateSelectedStatus(false)}>{t('Disable selected')}</button>}
            {canSensitiveWrite && <button type="button" className="danger-link" disabled={loading || Boolean(busyAction)} onClick={() => void removeSelected()}>{busyAction === 'bulk-delete' ? t('Deleting…') : t('Delete selected')}</button>}
            <button type="button" className="link" disabled={loading || Boolean(busyAction)} onClick={() => setSelected(new Set())}>{t('Clear selection')}</button>
          </div>
          {canWrite && <form className="channel-inline-form" onSubmit={tagSelected}>
            <label>{t('Tag selected channels')}<input value={bulkTag} maxLength={64} placeholder={t('Leave blank to remove the tag')} onChange={(event) => setBulkTag(event.target.value)} /></label>
            <button type="submit" disabled={loading || Boolean(busyAction)}>{busyAction === 'bulk-tag' ? t('Saving tag…') : t('Set tag')}</button>
          </form>}
        </section>
      )}
    </>
  );
}
