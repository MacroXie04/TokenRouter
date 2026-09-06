import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { ErrorView } from '../../features/errors/ErrorView';
import { safeReturnTarget } from './router';

const MAX_OAUTH_CALLBACK_QUERY_BYTES = 8 * 1024;
const replaceBrowserLocation = (target: string) => window.location.replace(target);

function hasASCIIControl(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 31 || code === 127) return true;
  }
  return false;
}

export function oauthCallbackTarget(provider: string, search: string): string | null {
  if (!/^[a-zA-Z0-9_-]{1,64}$/.test(provider) ||
      search.length > MAX_OAUTH_CALLBACK_QUERY_BYTES || hasASCIIControl(search) ||
      (search !== '' && !search.startsWith('?'))) {
    return null;
  }
  return `/api/oauth/${encodeURIComponent(provider)}/callback${search}`;
}

export function OAuthCallbackForwarder({
  provider,
  search,
  replaceLocation = replaceBrowserLocation,
}: {
  provider: string;
  search: string;
  replaceLocation?: (target: string) => void;
}) {
  const { t } = useTranslation();
  const target = oauthCallbackTarget(provider, search);
  useEffect(() => {
    if (target) replaceLocation(target);
  }, [replaceLocation, target]);
  if (!target) return <ErrorView code="404" />;
  return <main className="app"><p className="muted">{t('Completing external sign-in…')}</p></main>;
}

export function twoFactorContinuationTarget(search: string): string {
  const returnTarget = safeReturnTarget(search);
  return returnTarget ? `/otp?redirect=${encodeURIComponent(returnTarget)}` : '/otp';
}
