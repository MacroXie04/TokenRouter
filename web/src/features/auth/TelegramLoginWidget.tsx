import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

const TELEGRAM_WIDGET_URL = 'https://telegram.org/js/telegram-widget.js?22';
let telegramCallbackSequence = 0;

export function normalizeTelegramBotName(value: string): string | null {
  const normalized = value.trim().replace(/^@/, '');
  if (!/^[A-Za-z0-9_]{5,32}$/.test(normalized) || !/bot$/i.test(normalized)) return null;
  return normalized;
}

export function TelegramLoginWidget({
  botName,
  pending,
  onAuthorization,
}: {
  botName: string;
  pending: boolean;
  onAuthorization: (value: unknown) => void;
}) {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const authorizationRef = useRef(onAuthorization);
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<'loading' | 'ready' | 'failed'>('loading');
  const [callbackName] = useState(() => `tokenRouterTelegramLogin${++telegramCallbackSequence}`);

  useEffect(() => {
    authorizationRef.current = onAuthorization;
  }, [onAuthorization]);

  useEffect(() => {
    const container = containerRef.current;
    const normalizedBotName = normalizeTelegramBotName(botName);
    if (!container || !normalizedBotName) {
      setState('failed');
      return undefined;
    }

    let active = true;
    setState('loading');
    const browserWindow = window as unknown as Record<string, unknown>;
    browserWindow[callbackName] = (value: unknown) => {
      if (active) authorizationRef.current(value);
    };

    const script = document.createElement('script');
    script.async = true;
    script.referrerPolicy = 'no-referrer';
    script.src = TELEGRAM_WIDGET_URL;
    script.dataset.telegramLogin = normalizedBotName;
    script.dataset.size = 'large';
    script.dataset.radius = '8';
    script.dataset.onauth = `${callbackName}(user)`;
    const loaded = () => { if (active) setState('ready'); };
    const failed = () => { if (active) setState('failed'); };
    script.addEventListener('load', loaded);
    script.addEventListener('error', failed);
    container.replaceChildren(script);

    return () => {
      active = false;
      script.removeEventListener('load', loaded);
      script.removeEventListener('error', failed);
      container.replaceChildren();
      delete browserWindow[callbackName];
    };
  }, [attempt, botName, callbackName]);

  return (
    <div aria-busy={state === 'loading' || pending}>
      {(state === 'loading' || pending) && <p className="muted" role="status">{t('Loading…')}</p>}
      {state === 'failed' && (
        <div className="error-panel" role="alert">
          <p>{t('External sign-in failed. Please try again.')}</p>
          {normalizeTelegramBotName(botName) && (
            <button type="button" className="link" onClick={() => setAttempt((value) => value + 1)}>
              {t('Retry')}
            </button>
          )}
        </div>
      )}
      <div ref={containerRef} hidden={state !== 'ready' || pending} />
    </div>
  );
}
