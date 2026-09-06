import type { ChannelAdminViewController } from './useChannelController';

type ChannelCopyTagPanelsProps = Pick<ChannelAdminViewController,
  'busyAction' | 'canOperate' | 'canSensitiveWrite' | 'canWrite'
  | 'copyPanel' | 'saveCopy' | 'saveTag' | 'setCopyPanel'
  | 'setTagPanel' | 'setTagReload' | 'setTagStatus' | 't'
  | 'tagLoadError' | 'tagLoading' | 'tagPanel'
>;

export function ChannelCopyTagPanels({
  busyAction, canOperate, canSensitiveWrite, canWrite,
  copyPanel, saveCopy, saveTag, setCopyPanel,
  setTagPanel, setTagReload, setTagStatus, t,
  tagLoadError, tagLoading, tagPanel,
}: ChannelCopyTagPanelsProps) {
  return (
    <>
      {copyPanel && canSensitiveWrite && (
        <form className="channel-subpanel grid-form" onSubmit={saveCopy} aria-labelledby="copy-channel-title">
          <h3 id="copy-channel-title">{t('Copy channel {{name}}', { name: copyPanel.channel.name })}</h3>
          <label>{t('Name suffix')}<input value={copyPanel.suffix} maxLength={64} onChange={(event) => setCopyPanel((current) => current && ({ ...current, suffix: event.target.value }))} /></label>
          <label className="channel-checkbox"><input type="checkbox" checked={copyPanel.resetBalance} onChange={(event) => setCopyPanel((current) => current && ({ ...current, resetBalance: event.target.checked }))} /> {t('Reset balance and used quota')}</label>
          <p className="muted">{t('New channel name: {{name}}', { name: `${copyPanel.channel.name}${copyPanel.suffix}` })}</p>
          <div className="channel-filter-actions">
            <button type="submit" disabled={Boolean(busyAction)}>{busyAction === `copy:${copyPanel.channel.id}` ? t('Copying…') : t('Copy channel')}</button>
            <button type="button" className="link" disabled={Boolean(busyAction)} onClick={() => setCopyPanel(null)}>{t('Cancel')}</button>
          </div>
        </form>
      )}

      {tagPanel && (canOperate || canWrite) && (
        <form className="channel-subpanel grid-form" onSubmit={saveTag} aria-labelledby="tag-channel-title">
          <div className="channel-heading">
            <div><h3 id="tag-channel-title">{t('Manage tag {{tag}}', { tag: tagPanel.tag })}</h3><p className="muted">{t('Changes apply to every channel carrying this tag.')}</p></div>
            <button type="button" className="link" disabled={Boolean(busyAction)} onClick={() => setTagPanel(null)}>{t('Close')}</button>
          </div>
          {canOperate && <div className="channel-filter-actions">
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void setTagStatus(true)}>{t('Enable tagged channels')}</button>
            <button type="button" disabled={Boolean(busyAction)} onClick={() => void setTagStatus(false)}>{t('Disable tagged channels')}</button>
          </div>}
          {tagLoadError && <div role="alert"><span className="error">{t('Unable to load tag models.')}</span> <button type="button" className="link" onClick={() => setTagReload((value) => value + 1)}>{t('Try again')}</button></div>}
          {canWrite && <>
            <label>{t('New tag')}<input value={tagPanel.newTag} maxLength={64} placeholder={t('Leave blank to remove the tag')} onChange={(event) => setTagPanel((current) => current && ({ ...current, newTag: event.target.value }))} /></label>
            <label>{t('Models')}<textarea rows={4} value={tagPanel.models} maxLength={256 * 1024} disabled={tagLoading} onChange={(event) => setTagPanel((current) => current && ({ ...current, models: event.target.value, modelsDirty: true }))} /></label>
            <label>{t('Groups')}<input value={tagPanel.groups} maxLength={64} placeholder={t('Leave blank to keep existing groups')} onChange={(event) => setTagPanel((current) => current && ({ ...current, groups: event.target.value }))} /></label>
            <label>{t('Priority')}<input type="number" value={tagPanel.priority} onChange={(event) => setTagPanel((current) => current && ({ ...current, priority: event.target.value }))} /></label>
            <label>{t('Weight')}<input type="number" min={0} max={4_294_967_295} value={tagPanel.weight} onChange={(event) => setTagPanel((current) => current && ({ ...current, weight: event.target.value }))} /></label>
            <label>{t('Model mapping')}<textarea rows={3} value={tagPanel.modelMapping} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setTagPanel((current) => current && ({ ...current, modelMapping: event.target.value, modelMappingDirty: true }))} /></label>
            {canSensitiveWrite && <label>{t('Parameter override')}<textarea rows={3} value={tagPanel.paramOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setTagPanel((current) => current && ({ ...current, paramOverride: event.target.value, paramOverrideDirty: true }))} /></label>}
            {canSensitiveWrite && <label>{t('Header override')}<textarea rows={3} value={tagPanel.headerOverride} maxLength={256 * 1024} placeholder="{}" onChange={(event) => setTagPanel((current) => current && ({ ...current, headerOverride: event.target.value, headerOverrideDirty: true }))} /></label>}
            <p className="muted">{t('Leave optional fields blank to keep their current values.')}</p>
          </>}
          <div className="channel-filter-actions">
            {canWrite && <button type="submit" disabled={tagLoading || Boolean(busyAction)}>{busyAction === 'tag-edit' ? t('Saving…') : t('Save tag changes')}</button>}
            <button type="button" className="link" disabled={Boolean(busyAction)} onClick={() => setTagPanel(null)}>{t('Cancel')}</button>
          </div>
        </form>
      )}
    </>
  );
}
