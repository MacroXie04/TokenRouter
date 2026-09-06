import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { parseHeaderNavigationModules } from '../../shared/config/nav-modules';
import { parseSidebarModules } from '../../shared/config/sidebar-navigation';
import {
  isAbortError,
  updateSystemOption,
  type SystemOption,
} from './system-settings-api';

const MAX_NAVIGATION_BYTES = 64 * 1024;

type SaveOption = (input: { key: string; value: string }, signal?: AbortSignal) => Promise<void>;
type Notice = { kind: 'success' | 'error'; text: string } | null;

export interface NavigationSettingsPanelProps {
  options: readonly SystemOption[];
  onSaved: () => Promise<void> | void;
  saveOption?: SaveOption;
}

interface HeaderFormState {
  home: boolean;
  console: boolean;
  docs: boolean;
  about: boolean;
  pricingEnabled: boolean;
  pricingRequireAuth: boolean;
  rankingsEnabled: boolean;
  rankingsRequireAuth: boolean;
}

interface SidebarSectionState {
  enabled: boolean;
  modules: Record<string, boolean>;
}

type SidebarFormState = Record<string, SidebarSectionState>;

const HEADER_DEFAULTS: HeaderFormState = {
  home: true,
  console: true,
  docs: true,
  about: true,
  pricingEnabled: true,
  pricingRequireAuth: false,
  rankingsEnabled: true,
  rankingsRequireAuth: false,
};

const SIDEBAR_SECTIONS = [
  {
    key: 'chat',
    title: 'Chat area',
    modules: [
      { key: 'playground', label: 'Playground' },
      { key: 'chat', label: 'Chat' },
    ],
  },
  {
    key: 'console',
    title: 'Dashboard',
    modules: [
      { key: 'detail', label: 'Overview and analytics' },
      { key: 'token', label: 'API keys' },
      { key: 'log', label: 'Usage logs' },
      { key: 'midjourney', label: 'Drawing logs' },
      { key: 'task', label: 'Task history' },
    ],
  },
  {
    key: 'personal',
    title: 'Personal use',
    modules: [
      { key: 'topup', label: 'Wallet' },
      { key: 'personal', label: 'Profile' },
    ],
  },
  {
    key: 'admin',
    title: 'Administrator',
    modules: [
      { key: 'channel', label: 'Channels' },
      { key: 'models', label: 'Models' },
      { key: 'redemption', label: 'Redemption codes' },
      { key: 'user', label: 'Users' },
      { key: 'setting', label: 'System settings' },
      { key: 'subscription', label: 'Subscriptions' },
    ],
  },
] as const;

function optionValue(options: readonly SystemOption[], key: string): string {
  return options.find((option) => option.key === key)?.value ?? '';
}

function storedObject(raw: string): { value: Record<string, unknown> | null; invalid: boolean } {
  if (raw === '') return { value: {}, invalid: false };
  if (new TextEncoder().encode(raw).byteLength > MAX_NAVIGATION_BYTES) {
    return { value: null, invalid: true };
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { value: null, invalid: true };
    }
    return { value: parsed as Record<string, unknown>, invalid: false };
  } catch {
    return { value: null, invalid: true };
  }
}

function recordOrEmpty(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? { ...(value as Record<string, unknown>) }
    : {};
}

function headerState(raw: string): HeaderFormState {
  const parsed = parseHeaderNavigationModules(raw);
  return {
    home: parsed.home ?? true,
    console: parsed.console ?? true,
    docs: parsed.docs ?? true,
    about: parsed.about ?? true,
    pricingEnabled: parsed.pricing.enabled,
    pricingRequireAuth: parsed.pricing.requireAuth,
    rankingsEnabled: parsed.rankings.enabled,
    rankingsRequireAuth: parsed.rankings.requireAuth,
  };
}

function serializeHeader(raw: string, state: HeaderFormState): string {
  const source = storedObject(raw).value;
  const output: Record<string, unknown> = source ? { ...source } : {};
  output.home = state.home;
  output.console = state.console;
  output.docs = state.docs;
  output.about = state.about;
  output.pricing = {
    ...recordOrEmpty(source?.pricing),
    enabled: state.pricingEnabled,
    requireAuth: state.pricingRequireAuth,
  };
  output.rankings = {
    ...recordOrEmpty(source?.rankings),
    enabled: state.rankingsEnabled,
    requireAuth: state.rankingsRequireAuth,
  };
  return JSON.stringify(output);
}

function sidebarDefaults(): SidebarFormState {
  const defaults = Object.create(null) as SidebarFormState;
  for (const section of SIDEBAR_SECTIONS) {
    defaults[section.key] = {
      enabled: true,
      modules: Object.fromEntries(section.modules.map((module) => [module.key, true])),
    };
  }
  return defaults;
}

function sidebarState(raw: string): SidebarFormState {
  const parsed = parseSidebarModules(raw);
  const state = sidebarDefaults();
  if (!parsed) return state;
  for (const [sectionKey, configured] of Object.entries(parsed)) {
    const section = Object.hasOwn(state, sectionKey)
      ? state[sectionKey]
      : { enabled: true, modules: {} };
    if (typeof configured === 'boolean') {
      section.enabled = configured;
      state[sectionKey] = section;
      continue;
    }
    if (!configured) continue;
    section.enabled = configured.enabled !== false;
    for (const [moduleKey, enabled] of Object.entries(configured)) {
      if (moduleKey !== 'enabled' && typeof enabled === 'boolean') {
        section.modules[moduleKey] = enabled;
      }
    }
    state[sectionKey] = section;
  }
  return state;
}

function serializeSidebar(raw: string, state: SidebarFormState): string {
  const source = storedObject(raw).value;
  const output: Record<string, unknown> = source ? { ...source } : {};
  for (const [sectionKey, current] of Object.entries(state)) {
    output[sectionKey] = {
      ...recordOrEmpty(source?.[sectionKey]),
      enabled: current.enabled,
      ...current.modules,
    };
  }
  return JSON.stringify(output);
}

function titleCase(value: string): string {
  return value.replace(/[_-]+/gu, ' ').replace(/\b\w/gu, (character) => character.toUpperCase());
}

function useNavigationMutation({
  dirty,
  key,
  onSaved,
  saveOption,
  serialized,
}: {
  dirty: boolean;
  key: string;
  onSaved: () => Promise<void> | void;
  saveOption: SaveOption;
  serialized: string;
}) {
  const { t } = useTranslation();
  const mounted = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const operation = useRef(0);
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      operation.current += 1;
      controller.current?.abort();
    };
  }, []);

  function requestSave() {
    if (!busy && dirty) {
      if (new TextEncoder().encode(serialized).byteLength > MAX_NAVIGATION_BYTES) {
        setNotice({ kind: 'error', text: t('Navigation configuration is too large.') });
      } else {
        setNotice(null);
        setConfirming(true);
      }
    }
  }

  async function commit() {
    if (busy || !dirty) return;
    const currentOperation = ++operation.current;
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    setConfirming(false);
    setBusy(true);
    setNotice(null);
    try {
      await saveOption({ key, value: serialized }, request.signal);
      if (!mounted.current || request.signal.aborted || currentOperation !== operation.current) return;
      setNotice({ kind: 'success', text: t('Saved.') });
      await onSaved();
    } catch (error) {
      if (!isAbortError(error) && mounted.current && currentOperation === operation.current) {
        setNotice({ kind: 'error', text: t('Request failed.') });
      }
    } finally {
      if (mounted.current && !request.signal.aborted && currentOperation === operation.current) setBusy(false);
    }
  }

  return {
    busy,
    commit,
    confirming,
    notice,
    requestSave,
    setConfirming,
    setNotice,
  };
}

function NavigationPanel({
  busy,
  children,
  confirmDescription,
  confirming,
  dirty,
  invalid,
  notice,
  onCancelConfirm,
  onConfirm,
  onReset,
  onSave,
  saveLabel,
  title,
}: {
  busy: boolean;
  children: ReactNode;
  confirmDescription: string;
  confirming: boolean;
  dirty: boolean;
  invalid: boolean;
  notice: Notice;
  onCancelConfirm: () => void;
  onConfirm: () => void;
  onReset: () => void;
  onSave: () => void;
  saveLabel: string;
  title: string;
}) {
  const { t } = useTranslation();
  const titleId = useId();
  const confirmTitleId = useId();
  const confirmDescriptionId = useId();
  return (
    <section className="system-settings-tool" aria-labelledby={titleId}>
      <header className="system-settings-tool-heading">
        <div>
          <h3 id={titleId}>{title}</h3>
          <p>{t('Use structured controls for supported modules; unrecognized valid configuration is preserved.')}</p>
        </div>
      </header>
      {invalid && (
        <p className="system-settings-unavailable" role="note">
          {t('The stored navigation value is invalid. Safe enabled defaults are shown; saving will replace the invalid value.')}
        </p>
      )}
      <div className="system-settings-module-list">{children}</div>
      <div className="system-settings-field-actions">
        <button type="button" disabled={busy} onClick={onReset}>{t('Reset to defaults')}</button>
        <button type="button" className="primary" disabled={busy || !dirty} onClick={onSave}>
          {busy ? t('Saving…') : saveLabel}
        </button>
      </div>
      {notice && (
        <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>
          {notice.text}
        </p>
      )}
      {confirming && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby={confirmTitleId} aria-describedby={confirmDescriptionId}>
            <h3 id={confirmTitleId}>{t('Confirm navigation change')}</h3>
            <p id={confirmDescriptionId}>{confirmDescription}</p>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={onCancelConfirm}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={onConfirm}>{t('Confirm and save')}</button>
            </footer>
          </section>
        </div>
      )}
    </section>
  );
}

export function HeaderNavigationSettingsPanel({
  options,
  onSaved,
  saveOption = updateSystemOption,
}: NavigationSettingsPanelProps) {
  const { t } = useTranslation();
  const raw = optionValue(options, 'HeaderNavModules');
  const initial = useMemo(() => headerState(raw), [raw]);
  const [state, setState] = useState(initial);
  const invalid = storedObject(raw).invalid;

  useEffect(() => { setState(initial); }, [initial]);

  const serialized = useMemo(() => serializeHeader(raw, state), [raw, state]);
  const dirty = JSON.stringify(state) !== JSON.stringify(initial) || invalid;
  const mutation = useNavigationMutation({
    dirty,
    key: 'HeaderNavModules',
    onSaved,
    saveOption,
    serialized,
  });

  function toggle(key: keyof HeaderFormState) {
    setState((current) => ({ ...current, [key]: !current[key] }));
    mutation.setNotice(null);
  }

  const simpleModules: Array<{ key: keyof HeaderFormState; label: string }> = [
    { key: 'home', label: t('Home') },
    { key: 'console', label: t('Console') },
    { key: 'docs', label: t('Documentation') },
    { key: 'about', label: t('About') },
  ];
  const accessModules = [
    {
      name: t('Pricing'),
      enabled: 'pricingEnabled' as const,
      requireAuth: 'pricingRequireAuth' as const,
      requireAuthLabel: t('Require login for pricing'),
    },
    {
      name: t('Rankings'),
      enabled: 'rankingsEnabled' as const,
      requireAuth: 'rankingsRequireAuth' as const,
      requireAuthLabel: t('Require login for rankings'),
    },
  ];

  return (
    <NavigationPanel
      title={t('Header navigation')}
      saveLabel={t('Save navigation')}
      confirmDescription={t('Hidden public modules can make pages harder to discover. Confirm the new header navigation policy.')}
      invalid={invalid}
      dirty={dirty}
      busy={mutation.busy}
      notice={mutation.notice}
      confirming={mutation.confirming}
      onReset={() => { setState({ ...HEADER_DEFAULTS }); mutation.setNotice(null); }}
      onSave={mutation.requestSave}
      onCancelConfirm={() => mutation.setConfirming(false)}
      onConfirm={() => void mutation.commit()}
    >
      <div className="system-settings-module-grid">
        {simpleModules.map((module) => (
          <label className="system-settings-module" key={module.key}>
            <span>{module.label}</span>
            <input type="checkbox" checked={state[module.key]} disabled={mutation.busy} onChange={() => toggle(module.key)} />
          </label>
        ))}
      </div>
      {accessModules.map((module) => (
        <fieldset className="system-settings-module-group" key={module.name}>
          <legend>{module.name}</legend>
          <label className="system-settings-module">
            <span>{module.name}</span>
            <input type="checkbox" checked={state[module.enabled]} disabled={mutation.busy} onChange={() => toggle(module.enabled)} />
          </label>
          <label className="system-settings-module child">
            <span>{module.requireAuthLabel}</span>
            <input
              type="checkbox"
              checked={state[module.requireAuth]}
              disabled={mutation.busy || !state[module.enabled]}
              onChange={() => toggle(module.requireAuth)}
            />
          </label>
        </fieldset>
      ))}
    </NavigationPanel>
  );
}

export function SidebarModulesSettingsPanel({
  options,
  onSaved,
  saveOption = updateSystemOption,
}: NavigationSettingsPanelProps) {
  const { t } = useTranslation();
  const raw = optionValue(options, 'SidebarModulesAdmin');
  const initial = useMemo(() => sidebarState(raw), [raw]);
  const [state, setState] = useState(initial);
  const invalid = storedObject(raw).invalid;

  useEffect(() => { setState(initial); }, [initial]);

  const serialized = useMemo(() => serializeSidebar(raw, state), [raw, state]);
  const dirty = JSON.stringify(state) !== JSON.stringify(initial) || invalid;
  const mutation = useNavigationMutation({
    dirty,
    key: 'SidebarModulesAdmin',
    onSaved,
    saveOption,
    serialized,
  });
  const knownCopy: Record<string, { title: string; modules: Record<string, string> }> = {
    chat: {
      title: t('Chat area'),
      modules: { playground: t('Playground'), chat: t('Chat') },
    },
    console: {
      title: t('Dashboard'),
      modules: {
        detail: t('Overview'),
        token: t('API keys'),
        log: t('Usage logs'),
        midjourney: t('Drawing logs'),
        task: t('Task history'),
      },
    },
    personal: {
      title: t('Personal use'),
      modules: { topup: t('Wallet'), personal: t('Profile and security') },
    },
    admin: {
      title: t('Administrator'),
      modules: {
        channel: t('Channels'),
        models: t('Models'),
        redemption: t('Redemption codes'),
        user: t('Users'),
        setting: t('System settings'),
        subscription: t('Subscriptions'),
      },
    },
  };

  function toggleSection(sectionKey: string) {
    setState((current) => ({
      ...current,
      [sectionKey]: { ...current[sectionKey], enabled: !current[sectionKey].enabled },
    }));
    mutation.setNotice(null);
  }

  function toggleModule(sectionKey: string, moduleKey: string) {
    setState((current) => ({
      ...current,
      [sectionKey]: {
        ...current[sectionKey],
        modules: {
          ...current[sectionKey].modules,
          [moduleKey]: !current[sectionKey].modules[moduleKey],
        },
      },
    }));
    mutation.setNotice(null);
  }

  return (
    <NavigationPanel
      title={t('Sidebar modules')}
      saveLabel={t('Save sidebar modules')}
      confirmDescription={t('Hidden sidebar sections can remove navigation links for every user. Confirm the new administrator policy.')}
      invalid={invalid}
      dirty={dirty}
      busy={mutation.busy}
      notice={mutation.notice}
      confirming={mutation.confirming}
      onReset={() => { setState(sidebarDefaults()); mutation.setNotice(null); }}
      onSave={mutation.requestSave}
      onCancelConfirm={() => mutation.setConfirming(false)}
      onConfirm={() => void mutation.commit()}
    >
      {Object.entries(state).map(([sectionKey, current]) => {
        const known = Object.hasOwn(knownCopy, sectionKey) ? knownCopy[sectionKey] : undefined;
        const title = known?.title ?? titleCase(sectionKey);
        return (
          <fieldset className="system-settings-module-group" key={sectionKey}>
            <legend>{title}</legend>
            <label className="system-settings-module">
              <span>{title} — {t('Enabled')}</span>
              <input type="checkbox" checked={current.enabled} disabled={mutation.busy} onChange={() => toggleSection(sectionKey)} />
            </label>
            <div className="system-settings-module-grid child">
              {Object.keys(current.modules).map((moduleKey) => {
                const knownLabel = known && Object.hasOwn(known.modules, moduleKey)
                  ? known.modules[moduleKey]
                  : undefined;
                return (
                  <label className="system-settings-module" key={moduleKey}>
                    <span>{knownLabel ?? titleCase(moduleKey)}</span>
                    <input
                      type="checkbox"
                      checked={current.modules[moduleKey]}
                      disabled={mutation.busy || !current.enabled}
                      onChange={() => toggleModule(sectionKey, moduleKey)}
                    />
                  </label>
                );
              })}
            </div>
          </fieldset>
        );
      })}
    </NavigationPanel>
  );
}
