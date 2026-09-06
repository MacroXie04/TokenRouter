import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  isAbortError,
  isCurrentTokenRouterRelease,
  loadLatestTokenRouterRelease,
  loadRuntimeVersion,
  type RuntimeVersionInfo,
  type TokenRouterReleaseInfo,
} from './system-settings-api';

type Notice = { kind: 'success' | 'error'; text: string } | null;

function formatStartTime(timestamp: number, language: string): string {
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'medium' })
    .format(new Date(timestamp * 1_000));
}

function formatPublishedAt(timestamp: string | undefined, language: string): string {
  if (!timestamp) return '—';
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' })
    .format(new Date(timestamp));
}

export function UpdateCheckerPanel() {
  const { t, i18n } = useTranslation();
  const mounted = useRef(true);
  const runtimeLoad = useRef<AbortController | null>(null);
  const releaseLoad = useRef<AbortController | null>(null);
  const [runtime, setRuntime] = useState<RuntimeVersionInfo | null>(null);
  const [runtimeLoading, setRuntimeLoading] = useState(true);
  const [runtimeError, setRuntimeError] = useState(false);
  const [checking, setChecking] = useState(false);
  const [release, setRelease] = useState<TokenRouterReleaseInfo | null>(null);
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      runtimeLoad.current?.abort();
      releaseLoad.current?.abort();
    };
  }, []);

  const refreshRuntime = useCallback(async () => {
    runtimeLoad.current?.abort();
    const request = new AbortController();
    runtimeLoad.current = request;
    setRuntimeLoading(true);
    setRuntimeError(false);
    try {
      const next = await loadRuntimeVersion(request.signal);
      if (mounted.current && !request.signal.aborted) setRuntime(next);
    } catch (error) {
      if (mounted.current && !request.signal.aborted && !isAbortError(error)) {
        setRuntime(null);
        setRuntimeError(true);
      }
    } finally {
      if (mounted.current && !request.signal.aborted) setRuntimeLoading(false);
    }
  }, []);

  useEffect(() => { void refreshRuntime(); }, [refreshRuntime]);

  async function checkForUpdates() {
    if (!runtime || checking) return;
    releaseLoad.current?.abort();
    const request = new AbortController();
    releaseLoad.current = request;
    setChecking(true);
    setNotice(null);
    setRelease(null);
    try {
      const latest = await loadLatestTokenRouterRelease(request.signal);
      if (!mounted.current || request.signal.aborted) return;
      if (isCurrentTokenRouterRelease(runtime.currentVersion, latest.tagName)) {
        setNotice({
          kind: 'success',
          text: t('You are running the latest version ({{version}}).', { version: latest.tagName }),
        });
      } else {
        setRelease(latest);
      }
    } catch (error) {
      if (mounted.current && !request.signal.aborted && !isAbortError(error)) {
        setNotice({ kind: 'error', text: t('Unable to check for updates.') });
      }
    } finally {
      if (mounted.current && !request.signal.aborted) setChecking(false);
    }
  }

  return (
    <>
      <section className="system-settings-tool" aria-labelledby="update-checker-title">
        <header className="system-settings-tool-heading">
          <div>
            <h3 id="update-checker-title">{t('System maintenance')}</h3>
            <p>{t('Compare this running build with the latest published TokenRouter release.')}</p>
          </div>
          {!runtimeLoading && !runtimeError && (
            <button type="button" disabled={checking} onClick={() => void refreshRuntime()}>{t('Refresh')}</button>
          )}
        </header>

        {runtimeLoading ? <p role="status">{t('Loading version information…')}</p> : runtimeError || !runtime ? (
          <div className="system-settings-inline-error" role="alert">
            <span>{t('Unable to load version information.')}</span>
            <button type="button" onClick={() => void refreshRuntime()}>{t('Retry')}</button>
          </div>
        ) : (
          <>
            <dl className="system-settings-stats system-settings-version-stats">
              <div><dt>{t('Current version')}</dt><dd>{runtime.currentVersion}</dd></div>
              <div><dt>{t('Uptime since')}</dt><dd>{formatStartTime(runtime.startTimestamp, i18n.language)}</dd></div>
            </dl>
            <div className="system-settings-row-actions">
              <button type="button" className="primary" disabled={checking} onClick={() => void checkForUpdates()}>
                {checking ? t('Checking updates…') : t('Check for updates')}
              </button>
            </div>
          </>
        )}

        {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      </section>

      {release && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="dialog" aria-modal="true" aria-labelledby="update-release-title" aria-describedby="update-release-description">
            <header className="system-settings-dialog-header">
              <div>
                <h3 id="update-release-title">{t('New version available: {{version}}', { version: release.tagName })}</h3>
                <p id="update-release-description">{t('Published {{date}}', { date: formatPublishedAt(release.publishedAt, i18n.language) })}</p>
              </div>
            </header>
            {release.name && <p><strong>{release.name}</strong></p>}
            <h4>{t('Release notes')}</h4>
            {release.notes ? (
              <pre className="system-settings-release-notes">{release.notes}</pre>
            ) : <p className="system-settings-empty">{t('No release notes provided.')}</p>}
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus onClick={() => setRelease(null)}>{t('Close')}</button>
              <a className="system-settings-button-link primary" href={release.url} target="_blank" rel="noopener noreferrer">
                {t('Open release')}
              </a>
            </footer>
          </section>
        </div>
      )}
    </>
  );
}
