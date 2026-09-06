import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SystemOption } from './system-settings-api';

const PROVIDERS = [
  { id: 'github', label: 'GitHub' },
  { id: 'discord', label: 'Discord' },
  { id: 'oidc', label: 'OpenID Connect' },
  { id: 'linuxdo', label: 'Linux DO' },
] as const;

function loopbackHost(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/gu, '').replace(/\.$/u, '').toLowerCase();
  if (host === 'localhost' || host.endsWith('.localhost') || host === '::1') return true;
  const parts = host.split('.');
  return parts.length === 4 && parts.every((part) => /^(?:0|[1-9]\d{0,2})$/u.test(part) && Number(part) <= 255)
    && Number(parts[0]) === 127;
}

export function buildOAuthCallbackURLs(options: readonly SystemOption[], browserOrigin: string): Array<{
  id: typeof PROVIDERS[number]['id'];
  label: typeof PROVIDERS[number]['label'];
  url: string;
}> | null {
  const configured = options.find((option) => option.key === 'ServerAddress')?.value;
  const source = configured && configured !== '' ? configured : browserOrigin;
  if (source === '' || source !== source.trim() || source.includes('\\') || source.length > 2_048) return null;
  try {
    const parsed = new URL(source);
    if (parsed.username !== '' || parsed.password !== '' || parsed.hash !== '' || parsed.search !== ''
      || parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && loopbackHost(parsed.hostname))) return null;
    const basePath = parsed.pathname.replace(/\/+$/u, '');
    return PROVIDERS.map((provider) => {
      const callback = new URL(parsed.toString());
      callback.pathname = `${basePath}/api/oauth/${provider.id}/callback`;
      return { ...provider, url: callback.toString() };
    });
  } catch {
    return null;
  }
}

export function OAuthCallbackGuidance({ options }: { options: readonly SystemOption[] }) {
  const { t } = useTranslation();
  const headingId = useId();
  const mounted = useRef(true);
  const [notice, setNotice] = useState('');
  const browserOrigin = typeof window === 'undefined' ? '' : window.location.origin;
  const callbacks = buildOAuthCallbackURLs(options, browserOrigin);

  useEffect(() => () => { mounted.current = false; }, []);

  async function copy(provider: string, value: string) {
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(value);
      if (mounted.current) setNotice(t('{{provider}} callback URL copied.', { provider }));
    } catch {
      if (mounted.current) setNotice(t('Unable to copy the callback URL.'));
    }
  }

  return (
    <section className="system-settings-tool" aria-labelledby={headingId}>
      <header className="system-settings-tool-heading">
        <div>
          <h3 id={headingId}>{t('OAuth callback URLs')}</h3>
          <p>{t('Register these exact URLs with each provider. They use the configured server address when available.')}</p>
        </div>
      </header>
      {callbacks === null ? (
        <p className="system-settings-inline-error" role="alert">
          {t('Set a safe HTTPS server address before registering OAuth callbacks.')}
        </p>
      ) : (
        <dl className="system-settings-callback-list">
          {callbacks.map((callback) => (
            <div key={callback.id}>
              <dt>{t(callback.label)}</dt>
              <dd><code>{callback.url}</code></dd>
              <button
                type="button"
                aria-label={t('Copy {{provider}} callback URL', { provider: t(callback.label) })}
                onClick={() => void copy(t(callback.label), callback.url)}
              >
                {t('Copy URL')}
              </button>
            </div>
          ))}
        </dl>
      )}
      {notice && <p className="system-settings-field-message" role="status" aria-live="polite">{notice}</p>}
    </section>
  );
}
