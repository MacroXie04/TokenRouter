import type { ChannelAdminViewProps } from './channel-view-props';
import { ChannelBulkActions } from './ChannelBulkActions';
import { ChannelCopyTagPanels } from './ChannelCopyTagPanels';
import { ChannelCreatePanel } from './ChannelCreatePanel';
import { ChannelEditor } from './ChannelEditor';
import { ChannelFilters } from './ChannelFilters';
import { ChannelKeyRevealPanel } from './ChannelKeyRevealPanel';
import { ChannelMaintenance } from './ChannelMaintenance';
import { ChannelResults } from './ChannelResults';
import { CodexPanel, MultiKeyPanel, OllamaPanel, UpstreamUpdatesPanel } from './ChannelWorkflowPanels';
import { useChannelController } from './useChannelController';
export type { ChannelAdminViewProps } from './channel-view-props';

export function ChannelAdminView(props: ChannelAdminViewProps) {
  const controller = useChannelController(props);
  const {
    canOperate, canRead, canSensitiveWrite, canWrite,
    catalogError, codexPanel, isRoot, keyRevealChannel,
    loading, modelPanel, multiKeyPanel, notice,
    notify, ollamaPanel, refresh, setCodexPanel,
    setKeyRevealChannel, setModelPanel, setMultiKeyPanel, setOllamaPanel,
    setUpstreamPanel, t, upstreamPanel,
  } = controller;

  if (!canRead) {
    return (
      <section className="card channel-admin" aria-labelledby="channel-admin-title">
        <h2 id="channel-admin-title">{t('Channels')}</h2>
        <p className="error" role="alert">{t('Your account does not have permission to view this page.')}</p>
      </section>
    );
  }

  return (
    <section className="card channel-admin" aria-labelledby="channel-admin-title">
      <div className="channel-heading">
        <div>
          <h2 id="channel-admin-title">{t('Channels')}</h2>
          <p className="muted">{t('Search, test, and safely update upstream channels.')}</p>
        </div>
        <button type="button" className="link" disabled={loading} onClick={refresh}>
          {loading ? t('Refreshing…') : t('Refresh')}
        </button>
      </div>

      <ChannelMaintenance {...controller} />

      <ChannelFilters {...controller} />

      <ChannelBulkActions {...controller} />

      {catalogError && <p className="muted" role="status">{t('Model suggestions are unavailable.')}</p>}
      {notice && <p className={notice.kind} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      <ChannelResults {...controller} />

      <ChannelCopyTagPanels {...controller} />

      {multiKeyPanel && canOperate && <MultiKeyPanel channel={multiKeyPanel} canOperate={canOperate} canSensitiveWrite={canSensitiveWrite} onClose={() => setMultiKeyPanel(null)} onChanged={refresh} notify={notify} />}
      {upstreamPanel && (canOperate || canWrite) && <UpstreamUpdatesPanel channel={upstreamPanel} canOperate={canOperate} canWrite={canWrite} onClose={() => setUpstreamPanel(null)} onChanged={refresh} notify={notify} />}
      {codexPanel && <CodexPanel channel={codexPanel} canOperate={canOperate} canSensitiveWrite={canSensitiveWrite} onClose={() => setCodexPanel(null)} notify={notify} />}
      {ollamaPanel && canSensitiveWrite && <OllamaPanel channel={ollamaPanel} canOperate={canOperate} onClose={() => setOllamaPanel(null)} notify={notify} />}
      {keyRevealChannel && isRoot && (
        <ChannelKeyRevealPanel
          key={keyRevealChannel.id}
          channel={keyRevealChannel}
          onClose={() => setKeyRevealChannel(null)}
        />
      )}

      {modelPanel && canOperate && (
        <section className="channel-subpanel" aria-labelledby="channel-model-panel-title">
          <div className="channel-heading">
            <h3 id="channel-model-panel-title">{t('Upstream models for {{name}}', { name: modelPanel.name })}</h3>
            <button type="button" className="link" onClick={() => setModelPanel(null)}>{t('Close')}</button>
          </div>
          {modelPanel.models.length === 0
            ? <p className="muted">{t('No upstream models returned.')}</p>
            : <ul className="channel-model-list">{modelPanel.models.map((model) => <li key={model}><code>{model}</code></li>)}</ul>}
        </section>
      )}

      <ChannelEditor {...controller} />

      <ChannelCreatePanel {...controller} />
    </section>
  );
}
