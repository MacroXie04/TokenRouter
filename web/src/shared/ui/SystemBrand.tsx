import { createContext, useContext, type MouseEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

export interface SystemBrandConfig {
  systemName: string;
  logo: string;
  loading: boolean;
}

const DEFAULT_BRAND: SystemBrandConfig = {
  systemName: 'TokenRouter',
  logo: '',
  loading: false,
};

const SystemBrandContext = createContext<SystemBrandConfig>(DEFAULT_BRAND);

export function SystemBrandProvider({
  value,
  children,
}: {
  value: SystemBrandConfig;
  children: ReactNode;
}) {
  return <SystemBrandContext.Provider value={value}>{children}</SystemBrandContext.Provider>;
}

export function useSystemBrand(): SystemBrandConfig {
  return useContext(SystemBrandContext);
}

function shouldNavigateInApp(event: MouseEvent<HTMLAnchorElement>): boolean {
  return event.button === 0
    && !event.defaultPrevented
    && !event.metaKey
    && !event.ctrlKey
    && !event.shiftKey
    && !event.altKey;
}

export function SystemBrand({
  variant,
  onNavigate,
}: {
  variant: 'auth' | 'header';
  onNavigate?: (target: string) => void;
}) {
  const { t } = useTranslation();
  const { systemName, logo, loading } = useSystemBrand();
  const initial = Array.from(systemName.trim())[0]?.toLocaleUpperCase() || 'T';

  return (
    <a
      className={`system-brand system-brand-${variant}`}
      href="/"
      aria-label={t('Back to home')}
      aria-busy={loading || undefined}
      onClick={(event) => {
        if (!onNavigate || !shouldNavigateInApp(event)) return;
        event.preventDefault();
        onNavigate('/');
      }}
    >
      <span className="system-brand-mark" aria-hidden="true">
        {loading ? (
          <span className="system-brand-skeleton system-brand-skeleton-logo" />
        ) : (
          <>
            <span className="system-brand-fallback">{initial}</span>
            {logo && (
              <img
                src={logo}
                alt=""
                onError={(event) => { event.currentTarget.hidden = true; }}
              />
            )}
          </>
        )}
      </span>
      {loading ? (
        <>
          <span className="system-brand-skeleton system-brand-skeleton-name" aria-hidden="true" />
          <span className="sr-only">{t('Loading TokenRouter…')}</span>
        </>
      ) : (
        <span className="system-brand-name">{systemName}</span>
      )}
    </a>
  );
}
