import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  isPasskeyLoginSupported,
  serializeAssertionCredential,
} from '../../shared/browser/webauthn';
import {
  beginChannelKeyPasskey,
  finishChannelKeyPasskey,
  revealChannelKey,
  verifyChannelKeyTwoFactor,
} from './channel-key-api';

const AUTO_HIDE_MILLISECONDS = 45_000;

export interface ChannelKeyRevealPanelProps {
  channel: { id: number; name: string };
  onClose: () => void;
}

export function ChannelKeyRevealPanel({ channel, onClose }: ChannelKeyRevealPanelProps) {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState<'2fa' | 'passkey' | ''>('');
  const [secret, setSecret] = useState('');
  const [error, setError] = useState(false);
  const [autoHidden, setAutoHidden] = useState(false);
  const activeRequest = useRef<AbortController | null>(null);
  const hideTimer = useRef<number | null>(null);

  useEffect(() => () => {
    activeRequest.current?.abort();
    if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
  }, []);

  useEffect(() => {
    if (!secret) return undefined;
    if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
    hideTimer.current = window.setTimeout(() => {
      hideTimer.current = null;
      setSecret('');
      setAutoHidden(true);
    }, AUTO_HIDE_MILLISECONDS);
    return () => {
      if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
      hideTimer.current = null;
    };
  }, [secret]);

  function startRequest(method: '2fa' | 'passkey'): AbortController {
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    setBusy(method);
    setError(false);
    setAutoHidden(false);
    setSecret('');
    return controller;
  }

  function finishRequest(controller: AbortController) {
    if (activeRequest.current === controller) {
      activeRequest.current = null;
      setBusy('');
    }
  }

  async function showWithProof(proof: string, controller: AbortController) {
    const key = await revealChannelKey(channel.id, proof, controller.signal);
    if (!controller.signal.aborted) {
      setCode('');
      setSecret(key);
    }
  }

  async function verifyTwoFactor(event: React.FormEvent) {
    event.preventDefault();
    if (busy || !/^\d{6}$/.test(code.trim())) return;
    const controller = startRequest('2fa');
    try {
      const proof = await verifyChannelKeyTwoFactor(code, controller.signal);
      await showWithProof(proof, controller);
    } catch {
      if (!controller.signal.aborted) {
        setCode('');
        setError(true);
      }
    } finally {
      finishRequest(controller);
    }
  }

  async function verifyPasskey() {
    if (busy || !isPasskeyLoginSupported()) return;
    const controller = startRequest('passkey');
    try {
      const begin = await beginChannelKeyPasskey(controller.signal);
      const credential = await navigator.credentials.get({
        ...begin.requestOptions,
        signal: controller.signal,
      });
      if (!credential || controller.signal.aborted) throw new Error('Passkey verification cancelled');
      const assertion = serializeAssertionCredential(credential);
      const proof = await finishChannelKeyPasskey(begin.flowToken, assertion, controller.signal);
      await showWithProof(proof, controller);
    } catch {
      if (!controller.signal.aborted) setError(true);
    } finally {
      finishRequest(controller);
    }
  }

  function hideSecret() {
    if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
    hideTimer.current = null;
    setSecret('');
    setAutoHidden(false);
  }

  function close() {
    activeRequest.current?.abort();
    activeRequest.current = null;
    setBusy('');
    hideSecret();
    setCode('');
    setError(false);
    onClose();
  }

  const passkeyAvailable = isPasskeyLoginSupported();

  return (
    <section className="channel-subpanel channel-key-reveal" aria-labelledby="channel-key-reveal-title">
      <div className="channel-heading">
        <div>
          <h3 id="channel-key-reveal-title">{t('View key for {{name}}', { name: channel.name })}</h3>
          <p className="muted">{t('Verify your identity before viewing this channel key.')}</p>
        </div>
        <button type="button" className="link" onClick={close}>{t('Close')}</button>
      </div>

      {error && <p className="error" role="alert">{t('Verification failed. Check your security method and try again.')}</p>}
      {autoHidden && <p role="status">{t('The channel key was hidden automatically.')}</p>}

      {secret ? (
        <div className="grid-form">
          <label>
            {t('Channel key')}
            <textarea
              className="channel-key-reveal-value"
              aria-label={t('Channel key')}
              rows={Math.min(8, Math.max(3, secret.split('\n').length + 1))}
              readOnly
              autoComplete="off"
              spellCheck={false}
              value={secret}
            />
          </label>
          <p className="muted">{t('This value will be hidden automatically after 45 seconds.')}</p>
          <div className="channel-filter-actions">
            <button type="button" onClick={hideSecret}>{t('Hide now')}</button>
            <button type="button" className="link" onClick={close}>{t('Close')}</button>
          </div>
        </div>
      ) : (
        <div className="channel-key-verification">
          <form className="channel-inline-form" onSubmit={verifyTwoFactor}>
            <label>
              {t('Six-digit authenticator code')}
              <input
                value={code}
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                disabled={Boolean(busy)}
                onChange={(event) => setCode(event.target.value.replace(/\D/g, '').slice(0, 6))}
              />
            </label>
            <button type="submit" disabled={Boolean(busy) || code.length !== 6}>
              {busy === '2fa' ? t('Verifying…') : t('Verify with 2FA')}
            </button>
          </form>
          <div className="channel-filter-actions">
            <button type="button" disabled={Boolean(busy) || !passkeyAvailable} onClick={() => void verifyPasskey()}>
              {busy === 'passkey' ? t('Waiting for passkey…') : t('Verify with passkey')}
            </button>
          </div>
          {!passkeyAvailable && <p className="muted">{t('Passkey verification is unavailable in this browser.')}</p>}
        </div>
      )}
    </section>
  );
}
