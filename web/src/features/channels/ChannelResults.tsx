import {
  CHANNEL_ENABLED,
  CHANNEL_TYPE_CODEX,
  CHANNEL_TYPE_OLLAMA
} from './channel-api';
import {
  channelStatusLabel,
  modelSummary,
  modelValues,
  supportsBalance,
  UPSTREAM_DISCOVERY_TYPES,
} from './channel-editor-model';
import {
  channelProviderLabel
} from './channel-providers';
import type { ChannelAdminViewController } from './useChannelController';

type ChannelResultsProps = Pick<ChannelAdminViewController,
  'allVisibleSelected' | 'busyAction' | 'canOperate' | 'canSelect'
  | 'canSensitiveWrite' | 'canWrite' | 'channels' | 'codexPanel'
  | 'copyPanel' | 'editing' | 'isRoot' | 'keyRevealChannel'
  | 'loadError' | 'loading' | 'multiKeyPanel' | 'ollamaPanel'
  | 'openEditor' | 'pageCount' | 'query' | 'refresh'
  | 'refreshBalance' | 'removeChannel' | 'runTest' | 'selected'
  | 'setCodexPanel' | 'setCopyPanel' | 'setKeyRevealChannel' | 'setMultiKeyPanel'
  | 'setOllamaPanel' | 'setQuery' | 'setSelected' | 'setTestModels'
  | 'setUpstreamPanel' | 'showModels' | 'someVisibleSelected' | 't'
  | 'testModels' | 'toggleAllVisible' | 'toggleSelected' | 'toggleStatus'
  | 'total' | 'upstreamPanel'
>;

export function ChannelResults({
  allVisibleSelected, busyAction, canOperate, canSelect,
  canSensitiveWrite, canWrite, channels, codexPanel,
  copyPanel, editing, isRoot, keyRevealChannel,
  loadError, loading, multiKeyPanel, ollamaPanel,
  openEditor, pageCount, query, refresh,
  refreshBalance, removeChannel, runTest, selected,
  setCodexPanel, setCopyPanel, setKeyRevealChannel, setMultiKeyPanel,
  setOllamaPanel, setQuery, setSelected, setTestModels,
  setUpstreamPanel, showModels, someVisibleSelected, t,
  testModels, toggleAllVisible, toggleSelected, toggleStatus,
  total, upstreamPanel,
}: ChannelResultsProps) {
  return (
    <>
      {loadError && (
        <div className="channel-state" role="alert">
          <p className="error">{t('Unable to load channels.')}</p>
          <button type="button" onClick={refresh}>{t('Try again')}</button>
        </div>
      )}
      {!loadError && (
        <div className="table-scroll" aria-busy={loading}>
          <table aria-busy={loading}>
            <caption className="sr-only">{t('Channel results')}</caption>
            <thead>
              <tr>
                {canSelect && <th scope="col">
                  <input
                    type="checkbox"
                    aria-label={t('Select all channels on this page')}
                    aria-checked={someVisibleSelected && !allVisibleSelected ? 'mixed' : allVisibleSelected}
                    ref={(node) => { if (node) node.indeterminate = someVisibleSelected && !allVisibleSelected; }}
                    checked={allVisibleSelected}
                    disabled={loading || Boolean(busyAction) || channels.length === 0}
                    onChange={toggleAllVisible}
                  />
                </th>}
                <th scope="col">{t('Name')}</th>
                <th scope="col">{t('Status')}</th>
                <th scope="col">{t('Group')}</th>
                <th scope="col">{t('Models')}</th>
                <th scope="col">{t('Balance')}</th>
                <th scope="col">{t('Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {loading && channels.length === 0 && <tr><td colSpan={canSelect ? 7 : 6} role="status">{t('Loading channels…')}</td></tr>}
              {!loading && channels.length === 0 && <tr><td colSpan={canSelect ? 7 : 6}>{t('No channels match these filters.')}</td></tr>}
              {channels.map((channel) => {
                const anyBusy = Boolean(busyAction);
                return (
                  <tr key={channel.id}>
                    {canSelect && <td><input type="checkbox" aria-label={t('Select channel {{name}}', { name: channel.name })} checked={selected.has(channel.id)} disabled={anyBusy} onChange={() => toggleSelected(channel.id)} /></td>}
                    <td><strong>{channel.name}</strong><span className="channel-meta">#{channel.id} · {channelProviderLabel(channel.type)}</span></td>
                    <td><span className={channel.status === CHANNEL_ENABLED ? 'status-pill enabled' : 'status-pill disabled'}>{channelStatusLabel(channel.status, t)}</span></td>
                    <td>{channel.group || '—'}{channel.tag && <span className="channel-meta">{channel.tag}</span>}</td>
                    <td className="channel-model-summary" title={channel.models}>{modelSummary(channel.models)}</td>
                    <td>{supportsBalance(channel) ? channel.balance.toFixed(2) : '—'}{supportsBalance(channel) && channel.balanceUpdatedTime > 0 && <span className="channel-meta">{new Date(channel.balanceUpdatedTime * 1000).toLocaleString()}</span>}</td>
                    <td>
                      <div className="channel-actions">
                        {canOperate && <>
                          <input
                            aria-label={t('Test model for {{name}}', { name: channel.name })}
                            list={`channel-test-models-${channel.id}`}
                            value={testModels[channel.id] ?? ''}
                            maxLength={255}
                            placeholder={channel.testModel || t('Default test model')}
                            disabled={anyBusy}
                            onChange={(event) => setTestModels((current) => ({ ...current, [channel.id]: event.target.value }))}
                          />
                          <datalist id={`channel-test-models-${channel.id}`}>
                            {modelValues(channel.models).map((model) => <option key={model} value={model} />)}
                          </datalist>
                        </>}
                        {canOperate && <button type="button" className="link" disabled={anyBusy} onClick={() => void runTest(channel)}>{busyAction === `test:${channel.id}` ? t('Testing…') : t('Test')}</button>}
                        {canOperate && <button type="button" className="link" disabled={anyBusy} onClick={() => void toggleStatus(channel)}>{busyAction === `status:${channel.id}` ? t('Updating…') : channel.status === CHANNEL_ENABLED ? t('Disable') : t('Enable')}</button>}
                        {canOperate && UPSTREAM_DISCOVERY_TYPES.has(channel.type) && <button type="button" className="link" disabled={anyBusy} onClick={() => void showModels(channel)}>{busyAction === `models:${channel.id}` ? t('Fetching…') : t('Fetch models')}</button>}
                        {canOperate && supportsBalance(channel) && <button type="button" className="link" disabled={anyBusy} onClick={() => void refreshBalance(channel)}>{busyAction === `balance:${channel.id}` ? t('Refreshing…') : t('Refresh balance')}</button>}
                        {UPSTREAM_DISCOVERY_TYPES.has(channel.type) && (canOperate || (canWrite && channel.pendingAddModels.length + channel.pendingRemoveModels.length > 0)) && <button type="button" className="link" disabled={anyBusy} aria-expanded={upstreamPanel?.id === channel.id} onClick={() => setUpstreamPanel(channel)}>{channel.pendingAddModels.length + channel.pendingRemoveModels.length > 0 ? t('Review updates') : t('Upstream updates')}</button>}
                        {canOperate && channel.isMultiKey && <button type="button" className="link" disabled={anyBusy} aria-expanded={multiKeyPanel?.id === channel.id} onClick={() => setMultiKeyPanel(channel)}>{t('Manage keys')}</button>}
                        {channel.type === CHANNEL_TYPE_CODEX && <button type="button" className="link" disabled={anyBusy} aria-expanded={codexPanel?.id === channel.id} onClick={() => setCodexPanel(channel)}>{t('Codex usage')}</button>}
                        {channel.type === CHANNEL_TYPE_OLLAMA && canSensitiveWrite && <button type="button" className="link" disabled={anyBusy} aria-expanded={ollamaPanel?.id === channel.id} onClick={() => setOllamaPanel(channel)}>{t('Manage Ollama models')}</button>}
                        {isRoot && <button type="button" className="link" disabled={anyBusy} aria-expanded={keyRevealChannel?.id === channel.id} onClick={() => setKeyRevealChannel(channel)}>{t('View key')}</button>}
                        {canWrite && <button type="button" className="link" disabled={anyBusy} aria-expanded={editing?.id === channel.id} aria-controls="channel-edit-panel" onClick={() => void openEditor(channel)}>{busyAction === `edit-load:${channel.id}` ? t('Loading…') : t('Edit')}</button>}
                        {canSensitiveWrite && <button type="button" className="link" disabled={anyBusy} aria-expanded={copyPanel?.channel.id === channel.id} onClick={() => setCopyPanel({ channel, suffix: '_copy', resetBalance: true })}>{t('Copy')}</button>}
                        {canSensitiveWrite && <button type="button" className="link danger-link" disabled={anyBusy} onClick={() => void removeChannel(channel)}>{busyAction === `delete:${channel.id}` ? t('Deleting…') : t('Delete')}</button>}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {!loadError && total > 0 && (
        <nav className="channel-pagination" aria-label={t('Channel pages')}>
          <button type="button" disabled={loading || query.page <= 1} onClick={() => { setSelected(new Set()); setQuery((current) => ({ ...current, page: current.page - 1 })); }}>{t('Previous')}</button>
          <span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span>
          <button type="button" disabled={loading || query.page >= pageCount} onClick={() => { setSelected(new Set()); setQuery((current) => ({ ...current, page: current.page + 1 })); }}>{t('Next')}</button>
        </nav>
      )}
    </>
  );
}
