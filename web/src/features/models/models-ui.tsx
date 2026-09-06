import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

export type Notice = { kind: 'success' | 'error'; text: string } | null;

export type EditorMode = 'create' | 'edit';

export function formatDate(value: number): string {
  if (value === 0) return '—';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(value * 1_000);
}

export function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value);
}

export function Overlay({ title, children, onClose, busy = false }: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className="models-overlay" role="presentation">
      <section className="models-dialog" role="dialog" aria-modal="true" aria-label={title}>
        <header className="models-dialog-header">
          <h2>{title}</h2>
          <button type="button" className="models-icon-button" onClick={onClose} disabled={busy} aria-label={t('Close')}>
            ×
          </button>
        </header>
        {children}
      </section>
    </div>
  );
}

export function NoticeBanner({ notice }: { notice: Notice }) {
  if (!notice) return null;
  return <p className={`models-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>;
}

export function LoadingPanel({ label }: { label: string }) {
  return <div className="models-state" role="status" aria-live="polite"><span className="models-spinner" />{label}</div>;
}

export function ErrorPanel({ message, retry }: { message: string; retry: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="models-state error" role="alert">
      <p>{message}</p>
      <button type="button" onClick={retry}>{t('Retry')}</button>
    </div>
  );
}

export type ResourceState<T> =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; value: T };
