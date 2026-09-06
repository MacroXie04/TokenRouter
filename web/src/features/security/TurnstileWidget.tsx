import { useEffect, useRef, useState } from 'react';

const SCRIPT_ID = 'tokenrouter-turnstile-script';
const SCRIPT_URL = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
const MAX_SITE_KEY_CHARACTERS = 256;
const MAX_TOKEN_CHARACTERS = 4_096;

export interface TurnstileConfig {
  required: boolean;
  siteKey: string;
}

export const TURNSTILE_DISABLED: TurnstileConfig = Object.freeze({
  required: false,
  siteKey: '',
});

interface TurnstileAPI {
  render: (element: HTMLElement, options: Record<string, unknown>) => string;
  remove?: (widgetId: string) => void;
}

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function hasControlCharacters(value: string): boolean {
  for (const character of value) {
    const code = character.charCodeAt(0);
    if (code <= 31 || code === 127) return true;
  }
  return false;
}

function validSiteKey(value: unknown): string {
  if (typeof value !== 'string') return '';
  const key = value.trim();
  if (
    key.length === 0
    || key.length > MAX_SITE_KEY_CHARACTERS
    || hasControlCharacters(key)
    || !/^[A-Za-z0-9_-]+$/.test(key)
  ) return '';
  return key;
}

export function parseTurnstileConfig(value: unknown): TurnstileConfig {
  const data = record(value);
  if (!data || data.turnstile_check !== true) return TURNSTILE_DISABLED;
  return { required: true, siteKey: validSiteKey(data.turnstile_site_key) };
}

function normalizeToken(value: unknown): string {
  if (typeof value !== 'string') throw new Error('Invalid human-verification token');
  const token = value.trim();
  if (
    token.length === 0
    || token.length > MAX_TOKEN_CHARACTERS
    || hasControlCharacters(token)
  ) throw new Error('Invalid human-verification token');
  return token;
}

export function turnstileParams(token: string): { turnstile: string } {
  return { turnstile: normalizeToken(token) };
}

interface TurnstileWidgetProps {
  siteKey: string;
  label: string;
  onVerify: (token: string) => void;
  onExpire?: () => void;
  onError?: () => void;
  className?: string;
}

export function TurnstileWidget({
  siteKey,
  label,
  onVerify,
  onExpire,
  onError,
  className,
}: TurnstileWidgetProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const verifyRef = useRef(onVerify);
  const expireRef = useRef(onExpire);
  const errorRef = useRef(onError);
  const [ready, setReady] = useState(false);

  verifyRef.current = onVerify;
  expireRef.current = onExpire;
  errorRef.current = onError;

  useEffect(() => {
    const key = validSiteKey(siteKey);
    if (!key) {
      errorRef.current?.();
      return undefined;
    }

    let active = true;
    let widgetId = '';
    let script = document.getElementById(SCRIPT_ID) as HTMLScriptElement | null;

    const fail = () => {
      if (!active) return;
      setReady(false);
      errorRef.current?.();
    };
    const renderWidget = () => {
      if (!active || !containerRef.current || widgetId) return;
      if (!window.turnstile) {
        fail();
        return;
      }
      try {
        widgetId = window.turnstile.render(containerRef.current, {
          sitekey: key,
          callback: (rawToken: unknown) => {
            if (!active) return;
            try {
              verifyRef.current(normalizeToken(rawToken));
            } catch {
              expireRef.current?.();
            }
          },
          'expired-callback': () => { if (active) expireRef.current?.(); },
          'error-callback': () => { if (active) fail(); },
        });
        setReady(true);
      } catch {
        fail();
      }
    };
    const loaded = () => {
      if (script) script.dataset.turnstileLoaded = 'true';
      renderWidget();
    };

    if (window.turnstile) {
      renderWidget();
    } else {
      if (!script) {
        script = document.createElement('script');
        script.id = SCRIPT_ID;
        script.src = SCRIPT_URL;
        script.async = true;
        script.defer = true;
        script.referrerPolicy = 'no-referrer';
        document.head.appendChild(script);
      }
      if (script.dataset.turnstileLoaded === 'true') fail();
      else {
        script.addEventListener('load', loaded);
        script.addEventListener('error', fail);
      }
    }

    return () => {
      active = false;
      script?.removeEventListener('load', loaded);
      script?.removeEventListener('error', fail);
      if (widgetId) {
        try {
          window.turnstile?.remove?.(widgetId);
        } catch {
          // Provider cleanup is best-effort and never affects navigation.
        }
      }
    };
  }, [siteKey]);

  return (
    <div
      ref={containerRef}
      className={className}
      role="group"
      aria-label={label}
      aria-busy={!ready}
    />
  );
}
