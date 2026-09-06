import { Component, useCallback, useEffect, useMemo, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  getData,
  isTransientSessionRefreshFailure,
  SESSION_EXPIRED_EVENT,
  type User,
} from './api';
import { supportedLanguages } from './i18n';
import { parseHeaderNavigationModules, type HeaderNavigationModules } from './lib/nav-modules';
import { parseSelfUser } from './lib/admin-permissions';
import { parsePublicStatus, type PublicStatusInfo } from './lib/public-status';
import { decideRoute, matchRoute, safeReturnTarget, type AppRoute } from './lib/router';
import { HomeView } from './views/HomeView';
import { SetupView } from './views/SetupView';
import { LoginView } from './views/LoginView';
import { ConsoleView } from './views/ConsoleView';
import { AdminConsole, type AdminTab } from './views/AdminConsole';
import { ErrorView, authenticatedErrorCode } from './views/ErrorView';
import { OtpView } from './views/OtpView';
import { PasswordResetView } from './views/PasswordResetView';
import { PricingView } from './views/PricingView';
import { PublicDocumentView } from './views/PublicDocumentView';
import { RankingsView } from './features/rankings/RankingsView';
import { parseSetupStatus, type SetupStatus } from './features/setup/setup';
import { SystemInfoView } from './features/system-info/SystemInfoView';
import { WalletView } from './features/wallet/WalletView';
import { ChatView } from './features/chat/ChatView';
import { DashboardView, type DashboardSection } from './features/dashboard';
import { UsageLogsView } from './features/usage-logs/UsageLogsView';
import type { UsageLogSection } from './features/usage-logs/usage-logs-api';
import { PlaygroundView } from './features/playground';
import { ProfileView } from './features/profile/ProfileView';
import type { ProfileAccount } from './features/profile/profile-api';
import { SubscriptionAdminView } from './features/subscriptions';
import { ModelsView, type ModelsSection } from './features/models';
import { SystemSettingsView } from './features/system-settings';
import { AuthLayout } from './features/auth/AuthLayout';
import { AuthenticatedLayout, SystemBrandProvider, type SystemBrandConfig } from './features/layout';
import {
  TURNSTILE_DISABLED,
  parseTurnstileConfig,
  type TurnstileConfig,
} from './features/security/TurnstileWidget';

interface BootState {
  loading: boolean;
  setupRequired: boolean;
  setupStatus: SetupStatus;
  user: User | null;
  modules: HeaderNavigationModules;
  status: PublicStatusInfo;
  fatal: boolean;
  turnstile: TurnstileConfig;
}

const DEFAULT_MODULES = parseHeaderNavigationModules(undefined);
const DEFAULT_SETUP_STATUS = parseSetupStatus({ setup_required: false });
const DEFAULT_PUBLIC_STATUS = parsePublicStatus({});
const MAX_USER_SETTINGS_BYTES = 64 * 1024;
const SUPPORTED_LANGUAGE_CODES = new Set(supportedLanguages.map(({ code }) => code));
const AUTH_LAYOUT_ROUTES = new Set<AppRoute['name']>([
  'sign-in',
  'sign-up',
  'forgot-password',
  'reset-password',
  'otp',
  'oauth',
]);

function supportedUserLanguage(value: unknown): string | null {
  return typeof value === 'string' && SUPPORTED_LANGUAGE_CODES.has(value) ? value : null;
}

/**
 * Read the saved locale without trusting the user-settings blob. Malformed or
 * oversized legacy data is ignored so it cannot break authentication boot.
 */
export function savedUserLanguage(user: User | null | undefined): string | null {
  if (!user) return null;
  const direct = supportedUserLanguage(user.language);
  if (direct) return direct;

  let settings = user.setting;
  if (typeof settings === 'string') {
    if (new TextEncoder().encode(settings).byteLength > MAX_USER_SETTINGS_BYTES) return null;
    try {
      settings = JSON.parse(settings);
    } catch {
      return null;
    }
  }
  if (!settings || typeof settings !== 'object' || Array.isArray(settings)) return null;
  return supportedUserLanguage((settings as Record<string, unknown>).language);
}

function systemBrand(status: PublicStatusInfo, loading = false): SystemBrandConfig {
  return {
    systemName: status.system_name || status.site_name || status.app_name || 'TokenRouter',
    logo: status.logo,
    loading,
  };
}

function LanguageSwitcher() {
  const { t, i18n } = useTranslation();
  return (
    <select
      className="lang"
      value={i18n.language}
      onChange={(event) => i18n.changeLanguage(event.target.value)}
      aria-label={t('Language')}
    >
      {supportedLanguages.map((language) => (
        <option key={language.code} value={language.code}>{language.label}</option>
      ))}
    </select>
  );
}

function currentLocation() {
  return { pathname: window.location.pathname, search: window.location.search };
}

export function navigate(to: string, replace = false) {
  if (replace) window.history.replaceState(null, '', to);
  else window.history.pushState(null, '', to);
  window.dispatchEvent(new PopStateEvent('popstate'));
  window.scrollTo?.({ top: 0, behavior: 'instant' });
}

function useBrowserLocation() {
  const [location, setLocation] = useState(currentLocation);
  useEffect(() => {
    const update = () => setLocation(currentLocation());
    window.addEventListener('popstate', update);
    return () => window.removeEventListener('popstate', update);
  }, []);
  return location;
}

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

function adminTabForRoute(route: AppRoute): AdminTab {
  switch (route.name) {
    case 'channels': return 'channels';
    case 'models': return 'models';
    case 'users': return 'users';
    case 'redemption-codes': return 'redemptions';
    case 'subscriptions': return 'plans';
    case 'usage-logs': return 'logs';
    default: return 'overview';
  }
}

function adminRoute(route: AppRoute): boolean {
  return route.name === 'channels'
    || route.name === 'models'
    || route.name === 'users'
    || route.name === 'redemption-codes'
    || route.name === 'subscriptions';
}

function Application() {
  const { t, i18n } = useTranslation();
  const i18nRef = useRef(i18n);
  i18nRef.current = i18n;
  const location = useBrowserLocation();
  const [loginFlow, setLoginFlow] = useState('');
  const [boot, setBoot] = useState<BootState>({
    loading: true,
    setupRequired: false,
    setupStatus: DEFAULT_SETUP_STATUS,
    user: null,
    modules: DEFAULT_MODULES,
    status: DEFAULT_PUBLIC_STATUS,
    fatal: false,
    turnstile: TURNSTILE_DISABLED,
  });

  useEffect(() => {
    const expireSession = () => {
      setBoot((previous) => ({ ...previous, user: null }));
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, expireSession);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, expireSession);
  }, []);

  useEffect(() => {
    let active = true;
    (async () => {
      try {
        const setup = parseSetupStatus(await getData<unknown>('/setup'));
        let status = parsePublicStatus({});
        try {
          status = parsePublicStatus(await getData<unknown>('/status'));
        } catch {
          // API middleware remains authoritative if status is unavailable.
        }
        let user: User | null = null;
        if (!setup.setupRequired) {
          try {
            user = parseSelfUser(await getData<unknown>('/user/self'));
          } catch (error) {
            if (isTransientSessionRefreshFailure(error)) throw error;
            // A failed identity probe is anonymous; protected routes fail closed.
          }
        }
        const savedLanguage = savedUserLanguage(user);
        if (savedLanguage && savedLanguage !== i18nRef.current.language) {
          try {
            await i18nRef.current.changeLanguage(savedLanguage);
          } catch {
            // A locale-loader failure must not prevent a valid session boot.
          }
        }
        if (active) {
          setBoot({
            loading: false,
            setupRequired: setup.setupRequired,
            setupStatus: setup,
            user,
            modules: parseHeaderNavigationModules(status.HeaderNavModules),
            status,
            fatal: false,
            turnstile: parseTurnstileConfig(status),
          });
        }
      } catch {
        if (active) setBoot((previous) => ({ ...previous, loading: false, fatal: true }));
      }
    })();
    return () => { active = false; };
  }, []);

  const decision = useMemo(() => decideRoute(location.pathname, {
    setupRequired: boot.setupRequired,
    user: boot.user,
    search: location.search,
    modules: boot.modules,
  }), [boot.modules, boot.setupRequired, boot.user, location.pathname, location.search]);

  useEffect(() => {
    if (!boot.loading && !boot.fatal && decision.redirect) navigate(decision.redirect, true);
  }, [boot.fatal, boot.loading, decision.redirect]);

  const signedIn = (user: User) => {
    void (async () => {
      const fallbackUser: User = { ...user, permissions: undefined };
      const savedLanguage = savedUserLanguage(user);
      if (savedLanguage && savedLanguage !== i18nRef.current.language) {
        try {
          await i18nRef.current.changeLanguage(savedLanguage);
        } catch {
          // Keep sign-in available if changing the presentation locale fails.
        }
      }
      setBoot((previous) => ({ ...previous, user: fallbackUser }));
      setLoginFlow('');
      navigate(safeReturnTarget(location.search) ?? '/dashboard', true);

      try {
        const effectiveUser = parseSelfUser(await getData<unknown>('/user/self'));
        setBoot((previous) => previous.user?.id === fallbackUser.id
          ? { ...previous, user: effectiveUser }
          : previous);
      } catch {
        // The signed-in shell remains usable, but fine-grained controls stay
        // denied. A 401 event clears fallbackUser before this branch completes.
      }
    })();
  };
  const signedOut = () => {
    setBoot((previous) => ({ ...previous, user: null }));
    navigate('/sign-in', true);
  };
  const walletUserChanged = useCallback((updated: User) => {
    setBoot((previous) => ({
      ...previous,
      user: previous.user ? {
        ...updated,
        permissions: updated.permissions ?? previous.user?.permissions,
      } : null,
    }));
  }, []);
  const profileUserChanged = useCallback((updated: ProfileAccount) => {
    if (updated.language && updated.language !== i18nRef.current.language) {
      void i18nRef.current.changeLanguage(updated.language).catch(() => undefined);
    }
    setBoot((previous) => ({
      ...previous,
      user: previous.user ? {
        ...previous.user,
        id: updated.id,
        username: updated.username,
        display_name: updated.displayName,
        email: updated.email,
        role: updated.role,
        group: updated.group,
        quota: updated.quota,
        used_quota: updated.usedQuota,
        request_count: updated.requestCount,
        language: updated.language,
        sidebar_modules: JSON.stringify(updated.sidebarModules),
      } : null,
    }));
  }, []);
  const setupDone = () => {
    setBoot((previous) => ({
      ...previous,
      setupRequired: false,
      setupStatus: {
        ...previous.setupStatus,
        setupRequired: false,
        completed: true,
        rootInitialized: true,
      },
    }));
    navigate('/sign-in', true);
  };

  if (boot.loading) {
    const pendingRoute = matchRoute(location.pathname);
    if (AUTH_LAYOUT_ROUTES.has(pendingRoute.name)) {
      return (
        <SystemBrandProvider value={systemBrand(boot.status, true)}>
          <AuthLayout title={t('Loading TokenRouter…')}>
            <span />
          </AuthLayout>
        </SystemBrandProvider>
      );
    }
    return <main className="app"><p className="muted">{t('Loading TokenRouter…')}</p></main>;
  }
  if (boot.fatal) return <ErrorView code="503" />;
  if (decision.redirect) {
    return <main className="app"><p className="muted">{t('Loading TokenRouter…')}</p></main>;
  }

  const route = decision.route;
  const brandedAuth = (content: ReactNode) => (
    <SystemBrandProvider value={systemBrand(boot.status)}>{content}</SystemBrandProvider>
  );
  if (route.name === 'setup') return <SetupView status={boot.setupStatus} onDone={setupDone} />;
  if (route.name === 'home') {
    return brandedAuth(
      <HomeView
        announcements={boot.status.announcements}
        announcementsEnabled={boot.status.announcements_enabled}
        modules={boot.modules}
        onSignIn={() => navigate(boot.user ? '/dashboard' : '/sign-in')}
        signedIn={Boolean(boot.user)}
      />,
    );
  }
  if (route.name === 'about' || route.name === 'privacy-policy' || route.name === 'user-agreement') {
    return <PublicDocumentView kind={route.name} />;
  }
  if (route.name === 'pricing' || route.name === 'pricing-detail') {
    return <PricingView selectedModel={route.name === 'pricing-detail' ? route.parameter : undefined} />;
  }
  if (route.name === 'rankings') return <RankingsView search={location.search} onNavigate={(target) => navigate(target)} />;
  if (route.name === 'sign-in' || route.name === 'sign-up' || route.name === 'oauth') {
    return brandedAuth(
      <LoginView
        key={route.name}
        initialMode={route.name === 'sign-up' ? 'register' : 'login'}
        returnTarget={safeReturnTarget(location.search) ?? undefined}
        onLoggedIn={signedIn}
        onTwoFARequired={(flowToken) => {
          setLoginFlow(flowToken);
          navigate(twoFactorContinuationTarget(location.search));
        }}
      />
    );
  }
  if (route.name === 'forgot-password') {
    return brandedAuth(<PasswordResetView key={`${location.pathname}${location.search}`} resetMode={false} turnstileConfig={boot.turnstile} />);
  }
  if (route.name === 'reset-password') {
    return brandedAuth(<PasswordResetView key={`${location.pathname}${location.search}`} resetMode turnstileConfig={boot.turnstile} />);
  }
  if (route.name === 'otp') return brandedAuth(<OtpView flowToken={loginFlow} onLoggedIn={signedIn} />);
  if (route.name === 'oauth-callback') {
    return <OAuthCallbackForwarder provider={route.parameter ?? ''} search={location.search} />;
  }
  if (route.name === 'error') return <ErrorView code={route.parameter ?? '500'} />;
  if (route.name === 'not-found') return <ErrorView code="404" />;

  const user = boot.user!;
  let authenticatedContent: ReactNode;
  if (route.name === 'system-info') {
    authenticatedContent = <SystemInfoView onNavigate={(target) => navigate(target)} />;
  } else if (route.name === 'wallet') {
    authenticatedContent = (
      <WalletView
        user={user}
        onNavigate={(target) => navigate(target)}
        onUserChange={walletUserChanged}
        onLogout={signedOut}
        turnstileConfig={boot.turnstile}
        initialShowHistory={new URLSearchParams(location.search).get('show_history') === 'true'}
      />
    );
  } else if (route.name === 'chat' || route.name === 'chat2link') {
    authenticatedContent = (
      <ChatView
        presetId={route.name === 'chat' ? route.parameter : undefined}
        firstWeb={route.name === 'chat2link'}
        onNavigate={navigate}
      />
    );
  } else if (route.name === 'dashboard') {
    authenticatedContent = (
      <DashboardView
        key={route.parameter}
        section={(route.parameter ?? 'overview') as DashboardSection}
        role={user.role}
        search={location.search}
        onNavigate={navigate}
      />
    );
  } else if (route.name === 'usage-logs') {
    authenticatedContent = (
      <UsageLogsView
        key={route.parameter}
        user={user}
        section={(route.parameter ?? 'common') as UsageLogSection}
        onNavigate={navigate}
      />
    );
  } else if (route.name === 'playground') {
    authenticatedContent = <PlaygroundView user={user} onNavigate={navigate} />;
  } else if (route.name === 'profile') {
    authenticatedContent = <ProfileView onNavigate={navigate} onProfileChange={profileUserChanged} />;
  } else if (route.name === 'subscriptions') {
    authenticatedContent = <SubscriptionAdminView operatorRole={user.role} />;
  } else if (route.name === 'models') {
    authenticatedContent = (
      <ModelsView
        section={(route.parameter ?? 'metadata') as ModelsSection}
        role={user.role}
        onNavigate={navigate}
      />
    );
  } else if (route.name === 'system-settings') {
    authenticatedContent = (
      <SystemSettingsView
        role={user.role}
        settingsPath={route.parameter}
        onNavigate={navigate}
      />
    );
  } else if (route.name === 'authenticated-error') {
    authenticatedContent = <ErrorView code={authenticatedErrorCode(route.parameter)} />;
  } else if (adminRoute(route)) {
    authenticatedContent = (
      <AdminConsole
        user={user}
        initialTab={adminTabForRoute(route)}
        onNavigate={(target) => navigate(target)}
        onLogout={signedOut}
      />
    );
  } else {
    authenticatedContent = <ConsoleView user={user} keysOnly={route.name === 'keys'} onLogout={signedOut} />;
  }

  return (
    <SystemBrandProvider value={systemBrand(boot.status)}>
      <AuthenticatedLayout
        user={user}
        pathname={location.pathname}
        modules={boot.modules}
        sidebarModules={boot.status.SidebarModulesAdmin}
        defaultCollapseSidebar={boot.status.default_collapse_sidebar}
        userSidebarModules={user.sidebar_modules}
        onNavigate={navigate}
        onLogout={signedOut}
      >
        {authenticatedContent}
      </AuthenticatedLayout>
    </SystemBrandProvider>
  );
}

interface BoundaryState { failed: boolean }

export class AppErrorBoundary extends Component<{ children: ReactNode }, BoundaryState> {
  state: BoundaryState = { failed: false };

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('TokenRouter frontend error', error, info.componentStack);
  }

  render() {
    if (this.state.failed) return <ErrorView code="500" />;
    return this.props.children;
  }
}

export default function App() {
  return (
    <AppErrorBoundary>
      <div className="langbar"><LanguageSwitcher /></div>
      <Application />
    </AppErrorBoundary>
  );
}
