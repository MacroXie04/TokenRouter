import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { WaffoPancakeAdminPanel } from '../wallet/WaffoPancakeAdminPanel';
import { CustomOAuthPanel } from './CustomOAuthPanel';
import { LogMaintenancePanel } from './LogMaintenancePanel';
import {
  HeaderNavigationSettingsPanel,
  SidebarModulesSettingsPanel,
} from './NavigationSettingsPanels';
import { OAuthCallbackGuidance } from './OAuthCallbackGuidance';
import { SMTPSettingsPanel } from './SMTPSettingsPanel';
import { SystemSettingsEditor } from './SystemSettingsEditor';
import {
  AffinityCachePanel,
  DeploymentConnectionPanel,
  PaymentCompliancePanel,
  PricingResetPanel,
} from './SystemSettingsTools';
import { UpdateCheckerPanel } from './UpdateCheckerPanel';
import {
  SYSTEM_SETTINGS_ROOT_ROLE,
  isAbortError,
  loadSystemOptions,
  type SystemOption,
} from './system-settings-api';

export interface SystemSettingsViewProps {
  role: number;
  settingsPath?: string;
  onNavigate: (target: string) => void;
}

export function SystemSettingsView({ role, settingsPath, onNavigate }: SystemSettingsViewProps) {
  const { t } = useTranslation();
  const isRoot = Number.isSafeInteger(role) && role >= SYSTEM_SETTINGS_ROOT_ROLE;
  const mounted = useRef(true);
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const [options, setOptions] = useState<SystemOption[]>([]);
  const [loading, setLoading] = useState(isRoot);
  const [error, setError] = useState(false);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      controller.current?.abort();
    };
  }, []);

  const load = useCallback(async () => {
    if (!isRoot) return;
    const currentGeneration = ++generation.current;
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    setLoading(true);
    setError(false);
    try {
      const result = await loadSystemOptions(request.signal);
      if (mounted.current && currentGeneration === generation.current && !request.signal.aborted) setOptions(result);
    } catch (caught) {
      if (mounted.current && currentGeneration === generation.current && !isAbortError(caught)) setError(true);
    } finally {
      if (mounted.current && currentGeneration === generation.current && !request.signal.aborted) setLoading(false);
    }
  }, [isRoot]);

  useEffect(() => {
    if (isRoot) void load();
    else {
      generation.current += 1;
      controller.current?.abort();
      setOptions([]);
      setLoading(false);
      setError(false);
    }
  }, [isRoot, load]);

  const supplements = useMemo(() => ({
    'site/header-navigation': <HeaderNavigationSettingsPanel options={options} onSaved={load} />,
    'site/sidebar-modules': <SidebarModulesSettingsPanel options={options} onSaved={load} />,
    'auth/oauth': <OAuthCallbackGuidance options={options} />,
    'auth/custom-oauth': <CustomOAuthPanel />,
    'billing/payment': (
      <>
        <PaymentCompliancePanel options={options} onChanged={load} />
        <WaffoPancakeAdminPanel options={options} onSaved={load} />
      </>
    ),
    'billing/model-pricing': <PricingResetPanel onChanged={load} />,
    'models/channel-affinity': <AffinityCachePanel />,
    'models/model-deployment': <DeploymentConnectionPanel />,
    'operations/email': <SMTPSettingsPanel options={options} onSaved={load} />,
    'operations/logs': <LogMaintenancePanel />,
    'operations/update-checker': <UpdateCheckerPanel />,
  }), [load, options]);

  if (!isRoot) {
    return (
      <main className="system-settings-access" role="alert">
        <h1>{t('Root access required')}</h1>
        <p>{t('System settings include credentials and billing controls and are available only to root operators.')}</p>
      </main>
    );
  }
  if (loading) {
    return <main className="system-settings-state" role="status" aria-live="polite"><span className="system-settings-spinner" />{t('Loading system settings…')}</main>;
  }
  if (error) {
    return (
      <main className="system-settings-state error" role="alert">
        <h1>{t('Unable to load system settings.')}</h1>
        <p>{t('The response was unavailable or did not match the expected contract.')}</p>
        <button type="button" onClick={() => void load()}>{t('Retry')}</button>
      </main>
    );
  }
  return (
    <SystemSettingsEditor
      options={options}
      settingsPath={settingsPath}
      onNavigate={onNavigate}
      onSaved={load}
      supplements={supplements}
    />
  );
}
