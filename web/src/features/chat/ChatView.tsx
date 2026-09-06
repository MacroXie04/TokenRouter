import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadActiveChatKey,
  loadChatContext,
  resolveChatURL,
  type ChatContext,
  type ChatPreset,
} from './chat-api';

interface ChatViewProps {
  presetId?: string;
  firstWeb?: boolean;
  onNavigate: (target: string, replace?: boolean) => void;
  onExternalNavigate?: (target: string) => void;
}

type LoadState<T> =
  | { kind: 'loading' }
  | { kind: 'error' }
  | { kind: 'ready'; value: T };

function validPresetID(value: string | undefined): boolean {
  return value !== undefined && /^(0|[1-9][0-9]?)$/.test(value) && Number(value) < 64;
}

export function ChatView({
  presetId,
  firstWeb = false,
  onNavigate,
  onExternalNavigate = (target) => window.location.assign(target),
}: ChatViewProps) {
  const { t } = useTranslation();
  const [contextState, setContextState] = useState<LoadState<ChatContext>>({ kind: 'loading' });
  const [keyState, setKeyState] = useState<LoadState<string> | { kind: 'idle' }>({ kind: 'idle' });
  const [contextAttempt, setContextAttempt] = useState(0);
  const [keyAttempt, setKeyAttempt] = useState(0);
  const [preparedURL, setPreparedURL] = useState('');
  const [prepareFailed, setPrepareFailed] = useState(false);

  const invalidRoute = !firstWeb && !validPresetID(presetId);

  useEffect(() => {
    if (!invalidRoute) return;
    onNavigate('/dashboard', true);
  }, [invalidRoute, onNavigate]);

  useEffect(() => {
    if (invalidRoute) return;
    const controller = new AbortController();
    setContextState({ kind: 'loading' });
    void loadChatContext(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setContextState({ kind: 'ready', value });
      })
      .catch(() => {
        if (!controller.signal.aborted) setContextState({ kind: 'error' });
      });
    return () => controller.abort();
  }, [contextAttempt, invalidRoute]);

  const selected = useMemo<ChatPreset | undefined>(() => {
    if (contextState.kind !== 'ready') return undefined;
    if (firstWeb) return contextState.value.presets.find((preset) => preset.type === 'web');
    return contextState.value.presets[Number(presetId)];
  }, [contextState, firstWeb, presetId]);

  useEffect(() => {
    setPreparedURL('');
    setPrepareFailed(false);
    if (!selected?.requiresKey) {
      setKeyState({ kind: 'idle' });
      return;
    }
    const controller = new AbortController();
    setKeyState({ kind: 'loading' });
    void loadActiveChatKey(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setKeyState({ kind: 'ready', value });
      })
      .catch(() => {
        if (!controller.signal.aborted) setKeyState({ kind: 'error' });
      });
    return () => controller.abort();
  }, [keyAttempt, selected]);

  function prepare(preset: ChatPreset, context: ChatContext) {
    const key = keyState.kind === 'ready' ? keyState.value : undefined;
    const resolved = resolveChatURL(preset, key, context.serverAddress);
    if (!resolved) {
      setPrepareFailed(true);
      return;
    }
    if (firstWeb) {
      onExternalNavigate(resolved);
      return;
    }
    setPrepareFailed(false);
    setPreparedURL(resolved);
  }

  if (invalidRoute) {
    return <main className="app chat-app" aria-label={t('Chat launcher')}><p className="muted">{t('Returning to dashboard…')}</p></main>;
  }

  return (
    <main className="app chat-app" aria-label={t('Chat launcher')}>
      <header className="header row">
        <div>
          <p className="eyebrow">{t('Chat launcher')}</p>
          <h1>{selected?.name ?? t('Chat')}</h1>
        </div>
        <nav className="chat-actions" aria-label={t('Chat actions')}>
          <button type="button" className="link" onClick={() => onNavigate('/dashboard')}>{t('Dashboard')}</button>
          <button type="button" className="link" onClick={() => onNavigate('/keys')}>{t('Manage API keys')}</button>
        </nav>
      </header>

      {contextState.kind === 'loading' && <section className="card"><p className="muted">{t('Loading chat launchers…')}</p></section>}
      {contextState.kind === 'error' && (
        <section className="card error-panel" role="alert">
          <p>{t('Unable to load chat launchers.')}</p>
          <button type="button" onClick={() => setContextAttempt((value) => value + 1)}>{t('Retry')}</button>
        </section>
      )}
      {contextState.kind === 'ready' && contextState.value.presets.length === 0 && (
        <section className="card empty-state"><p>{t('No chat launchers are configured.')}</p></section>
      )}
      {contextState.kind === 'ready' && contextState.value.presets.length > 0 && !selected && (
        <section className="card empty-state">
          <h2>{t('Chat preset not found')}</h2>
          <p>{t('The requested chat preset does not exist or has been removed.')}</p>
          <button type="button" onClick={() => onNavigate('/dashboard')}>{t('Return to dashboard')}</button>
        </section>
      )}
      {contextState.kind === 'ready' && selected?.type === 'shortcut' && (
        <section className="card empty-state">
          <h2>{selected.name}</h2>
          <p>{t('This preset opens through its companion application. Use the configured application shortcut.')}</p>
        </section>
      )}
      {contextState.kind === 'ready' && selected && selected.type !== 'shortcut' && (
        <section className="card chat-launch-card" aria-labelledby="chat-launch-title">
          <div>
            <p className="eyebrow">{selected.type === 'web' ? t('Web chat') : t('Desktop application')}</p>
            <h2 id="chat-launch-title">{selected.name}</h2>
          </div>
          {selected.requiresKey && keyState.kind === 'loading' && <p className="muted">{t('Preparing an enabled API key…')}</p>}
          {selected.requiresKey && keyState.kind === 'error' && (
            <div className="error-panel" role="alert">
              <p>{t('Unable to prepare this chat launcher.')}</p>
              <div className="chat-actions">
                <button type="button" onClick={() => setKeyAttempt((value) => value + 1)}>{t('Retry')}</button>
                <button type="button" className="link" onClick={() => onNavigate('/keys')}>{t('Manage API keys')}</button>
              </div>
            </div>
          )}
          {(!selected.requiresKey || keyState.kind === 'ready') && (
            <>
              {selected.requiresKey && (
                <p className="notice-panel">{t('This launcher will share one enabled API key with the configured chat application.')}</p>
              )}
              {!preparedURL && (
                <button type="button" onClick={() => prepare(selected, contextState.value)}>{t('Open chat')}</button>
              )}
              {prepareFailed && <p className="error" role="alert">{t('The configured chat URL is unsafe or invalid.')}</p>}
              {preparedURL && selected.type === 'custom-protocol' && (
                <a className="button" href={preparedURL} rel="noreferrer">{t('Launch application')}</a>
              )}
              {preparedURL && selected.type === 'web' && (
                <iframe
                  src={preparedURL}
                  title={`Chat preset: ${selected.name}`}
                  className="chat-frame"
                  sandbox="allow-forms allow-scripts allow-popups allow-downloads"
                  referrerPolicy="no-referrer"
                  allow="camera; microphone"
                />
              )}
            </>
          )}
        </section>
      )}
    </main>
  );
}
