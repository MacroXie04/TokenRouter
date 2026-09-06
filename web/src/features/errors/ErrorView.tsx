import { useTranslation } from 'react-i18next';

interface ErrorCopy {
  title: string;
  detail: string;
}

const ERROR_COPY: Record<string, ErrorCopy> = {
  '401': { title: 'Sign-in required', detail: 'You need to sign in to view this page.' },
  '403': { title: 'Access denied', detail: 'Your account does not have permission to view this page.' },
  '404': { title: 'Page not found', detail: 'The page you requested does not exist.' },
  '500': { title: 'Something went wrong', detail: 'The application could not complete this request.' },
  '503': { title: 'Service unavailable', detail: 'TokenRouter is temporarily unavailable.' },
};

const AUTHENTICATED_ERROR_CODES: Record<string, string> = {
  unauthorized: '401',
  forbidden: '403',
  'not-found': '404',
  'internal-server-error': '500',
  'maintenance-error': '503',
};

export const MAX_ERROR_DETAIL_CHARACTERS = 512;
const FEEDBACK_URL = 'https://github.com/MacroXie04/TokenRouter/issues';

export function authenticatedErrorCode(name: string | undefined): string {
  return (name && AUTHENTICATED_ERROR_CODES[name]) || '404';
}

export function ErrorView({ code, detail }: { code: string; detail?: string }) {
  const { t } = useTranslation();
  const error = ERROR_COPY[code] ?? ERROR_COPY['500'];
  const headingID = `error-${code}-title`;

  return (
    <main className="app app-narrow error-page" aria-labelledby={headingID}>
      <p className="error-code" aria-hidden="true">{code}</p>
      <h1 id={headingID}>{t(error.title)}</h1>
      <p className="muted">{detail?.slice(0, MAX_ERROR_DETAIL_CHARACTERS) || t(error.detail)}</p>
      <div className="error-actions">
        <button className="button" type="button" onClick={() => window.history.back()}>
          {t('Go back')}
        </button>
        <a className="button" href="/">{t('Back to home')}</a>
        {code === '401' && <a className="button" href="/sign-in">{t('Sign in')}</a>}
        {code === '500' && (
          <a className="button" href={FEEDBACK_URL} target="_blank" rel="noopener noreferrer">
            {t('Report a problem')}
          </a>
        )}
      </div>
    </main>
  );
}
