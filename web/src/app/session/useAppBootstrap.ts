import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { parseSelfUser } from '../../features/auth/admin-permissions';
import { TURNSTILE_DISABLED, parseTurnstileConfig, type TurnstileConfig } from '../../features/security/TurnstileWidget';
import { parseSetupStatus, type SetupStatus } from '../../features/setup/setup';
import { SESSION_EXPIRED_EVENT, getData, isTransientSessionRefreshFailure, type User } from '../../shared/api/client';
import { parseHeaderNavigationModules, type HeaderNavigationModules } from '../../shared/config/nav-modules';
import { parsePublicStatus, type PublicStatusInfo } from '../../shared/config/public-status';
import { savedUserLanguage } from './user-language';

export interface BootState {
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

export function useAppBootstrap() {
  const { i18n } = useTranslation();
  const i18nRef = useRef(i18n);
  i18nRef.current = i18n;
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

  return { boot, setBoot, i18nRef };
}
