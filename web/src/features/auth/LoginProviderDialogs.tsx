import type { FormEventHandler, RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { MAX_WECHAT_CODE_CHARACTERS } from './auth-flow';
import { TelegramLoginWidget } from './TelegramLoginWidget';

export function WeChatLoginDialog({
  dialogRef, qrCode, code, error, busy, onCodeChange, onClose, onSubmit,
}: {
  dialogRef: RefObject<HTMLFormElement | null>;
  qrCode?: string;
  code: string;
  error: string;
  busy: boolean;
  onCodeChange: (value: string) => void;
  onClose: () => void;
  onSubmit: FormEventHandler<HTMLFormElement>;
}) {
  const { t } = useTranslation();
  return (
    <div
      className="modal-overlay"
      role="presentation"
      onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}
    >
      <form
        ref={dialogRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="wechat-login-title"
        aria-describedby="wechat-login-description"
        onSubmit={onSubmit}
      >
        <h2 id="wechat-login-title">{t('WeChat login')}</h2>
        {qrCode && <img src={qrCode} alt={t('WeChat authorization QR code')} />}
        <p id="wechat-login-description" className="muted">{t('Scan the QR code with WeChat, then enter the code you receive.')}</p>
        <label>
          {t('WeChat authorization code')}
          <input
            value={code}
            onChange={(event) => onCodeChange(event.target.value.slice(0, MAX_WECHAT_CODE_CHARACTERS))}
            required
            maxLength={MAX_WECHAT_CODE_CHARACTERS}
            autoComplete="one-time-code"
          />
        </label>
        {error && <p className="error" role="alert">{error}</p>}
        <div className="modal-actions">
          <button type="button" className="link" onClick={onClose}>{t('Close')}</button>
          <button type="submit" disabled={busy}>{busy ? t('Please wait…') : t('Authorize')}</button>
        </div>
      </form>
    </div>
  );
}

export function TelegramLoginDialog({
  dialogRef, botName, pending, error, onClose, onAuthorization,
}: {
  dialogRef: RefObject<HTMLElement | null>;
  botName: string;
  pending: boolean;
  error: string;
  onClose: () => void;
  onAuthorization: (value: unknown) => void;
}) {
  const { t } = useTranslation();
  return (
    <div
      className="modal-overlay"
      role="presentation"
      onMouseDown={(event) => {
        if (!pending && event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="telegram-login-title"
      >
        <h2 id="telegram-login-title">Telegram</h2>
        <TelegramLoginWidget botName={botName} pending={pending} onAuthorization={onAuthorization} />
        {error && <p className="error" role="alert">{error}</p>}
        <div className="modal-actions">
          <button type="button" className="link" disabled={pending} onClick={onClose}>{t('Close')}</button>
        </div>
      </section>
    </div>
  );
}
