import type { ChannelAdminViewController } from './useChannelController';

type ChannelMaintenanceProps = Pick<ChannelAdminViewController,
  'applyAllUpdates' | 'busyAction' | 'canOperate' | 'canSensitiveWrite'
  | 'canWrite' | 'detectAllUpdates' | 'manageTag' | 'refreshAllBalances'
  | 'removeAllDisabledChannels' | 'repairChannelConsistency' | 'setTagLookup' | 'startAllChannelTests'
  | 't' | 'tagLookup' | 'visibleTags'
>;

export function ChannelMaintenance({
  applyAllUpdates, busyAction, canOperate, canSensitiveWrite,
  canWrite, detectAllUpdates, manageTag, refreshAllBalances,
  removeAllDisabledChannels, repairChannelConsistency, setTagLookup, startAllChannelTests,
  t, tagLookup, visibleTags,
}: ChannelMaintenanceProps) {
  return (
    <>
      {(canOperate || canWrite || canSensitiveWrite) && (
        <div className="channel-toolbar" aria-label={t('Channel maintenance actions')}>
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void startAllChannelTests()}>
            {busyAction === 'test-all' ? t('Starting tests…') : t('Test all channels')}
          </button>}
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void refreshAllBalances()}>
            {busyAction === 'balance-all' ? t('Refreshing balances…') : t('Refresh all balances')}
          </button>}
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void detectAllUpdates()}>
            {busyAction === 'upstream-detect-all' ? t('Starting detection…') : t('Detect all upstream updates')}
          </button>}
          {canWrite && <button type="button" disabled={Boolean(busyAction)} onClick={() => void applyAllUpdates()}>
            {busyAction === 'upstream-apply-all' ? t('Applying updates…') : t('Apply all staged updates')}
          </button>}
          {canOperate && <button type="button" disabled={Boolean(busyAction)} onClick={() => void repairChannelConsistency()}>
            {busyAction === 'repair' ? t('Repairing…') : t('Repair channel consistency')}
          </button>}
          {canSensitiveWrite && (
            <button type="button" className="danger-link" disabled={Boolean(busyAction)} onClick={() => void removeAllDisabledChannels()}>
              {busyAction === 'delete-disabled' ? t('Deleting…') : t('Delete all disabled')}
            </button>
          )}
          {(canOperate || canWrite) && <form className="channel-inline-form" onSubmit={manageTag}>
            <label>
              {t('Manage tag')}
              <input value={tagLookup} maxLength={64} list="visible-channel-tags" placeholder={t('Enter a tag')} onChange={(event) => setTagLookup(event.target.value)} />
              <datalist id="visible-channel-tags">{visibleTags.map((tag) => <option key={tag} value={tag} />)}</datalist>
            </label>
            <button type="submit" disabled={Boolean(busyAction) || !tagLookup.trim()}>{t('Manage')}</button>
          </form>}
        </div>
      )}
    </>
  );
}
