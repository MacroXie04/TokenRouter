import { useTranslation } from 'react-i18next';

export function EmptyOrFailure({
  loading,
  error,
  loadingText,
  errorText,
  onRetry,
}: {
  loading: boolean;
  error: boolean;
  loadingText: string;
  errorText: string;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  if (loading) return <p className="muted" role="status">{loadingText}</p>;
  if (error) {
    return (
      <div role="alert">
        <p className="error">{errorText}</p>
        <button type="button" className="link" onClick={onRetry}>{t('Try again')}</button>
      </div>
    );
  }
  return null;
}
