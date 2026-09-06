import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { AdminConsole, type AdminTab } from '../features/admin/AdminConsole';
import { AuthLayout } from '../features/auth/AuthLayout';
import { LoginView } from '../features/auth/LoginView';
import { OtpView } from '../features/auth/OtpView';
import { PasswordResetView } from '../features/auth/PasswordResetView';
import { parseSelfUser } from '../features/auth/admin-permissions';
import { ChatView } from '../features/chat/ChatView';
import { DashboardView, type DashboardSection } from '../features/dashboard';
import { ErrorView, authenticatedErrorCode } from '../features/errors/ErrorView';
import { HomeView } from '../features/home/HomeView';
import { ConsoleView } from '../features/keys/ConsoleView';
import { ModelsView, type ModelsSection } from '../features/models';
import { PlaygroundView } from '../features/playground';
import { PricingView } from '../features/pricing/PricingView';
import { ProfileView } from '../features/profile/ProfileView';
import type { ProfileAccount } from '../features/profile/profile-api';
import { PublicDocumentView } from '../features/public-documents/PublicDocumentView';
import { RankingsView } from '../features/rankings/RankingsView';
import { SetupView } from '../features/setup/SetupView';
import { SubscriptionAdminView } from '../features/subscriptions';
import { SystemInfoView } from '../features/system-info/SystemInfoView';
import { SystemSettingsView } from '../features/system-settings';
import { UsageLogsView } from '../features/usage-logs/UsageLogsView';
import type { UsageLogSection } from '../features/usage-logs/usage-logs-api';
import { WalletView } from '../features/wallet/WalletView';
import { getData, type User } from '../shared/api/client';
import type { PublicStatusInfo } from '../shared/config/public-status';
import { AppErrorBoundary } from './AppErrorBoundary';
import { AuthenticatedLayout, SystemBrandProvider, type SystemBrandConfig } from './layout';
import { LanguageSwitcher } from './layout/LanguageSwitcher';
import { OAuthCallbackForwarder, twoFactorContinuationTarget } from './routing/OAuthCallbackForwarder';
import { navigate, useBrowserLocation } from './routing/browser-location';
import { decideRoute, matchRoute, safeReturnTarget, type AppRoute } from './routing/router';
import { useAppBootstrap } from './session/useAppBootstrap';
import { savedUserLanguage } from './session/user-language';

const AUTH_LAYOUT_ROUTES = new Set<AppRoute['name']>([
  'sign-in',
  'sign-up',
  'forgot-password',
  'reset-password',
  'otp',
  'oauth',
]);

function systemBrand(status: PublicStatusInfo, loading = false): SystemBrandConfig {
  return {
    systemName: status.system_name || status.site_name || status.app_name || 'TokenRouter',
    logo: status.logo,
    loading,
  };
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
  const { t } = useTranslation();
  const location = useBrowserLocation();
  const [loginFlow, setLoginFlow] = useState('');
  const { boot, setBoot, i18nRef } = useAppBootstrap();

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
  }, [setBoot]);
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
  }, [i18nRef, setBoot]);
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

export default function App() {
  return (
    <AppErrorBoundary>
      <div className="langbar"><LanguageSwitcher /></div>
      <Application />
    </AppErrorBoundary>
  );
}
