import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  createUserSubscription,
  deleteUserSubscription,
  invalidateUserSubscription,
  listSubscriptionPlans,
  listUserSubscriptions,
  resetUserSubscriptionsByPlan,
  type ManagedSubscriptionPlan,
  type ManagedUserSubscription,
} from '../subscriptions/subscription-api';
import {
  USER_ROLE_ADMIN,
  USER_ROLE_ROOT,
  clearBuiltInUserBinding,
  loadCustomOAuthBindings,
  loadPermissionCatalog,
  loadUserBindingDetails,
  loadUserPermissionState,
  unbindCustomOAuth,
  updateUserPermissions,
  type BuiltInBindingState,
  type BuiltInBindingType,
  type CustomOAuthBinding,
  type ManagedUser,
  type PermissionCatalog,
  type PermissionMatrix,
} from './user-api';
import './user-admin-dialogs.css';

interface GuardedDialogProps {
  user: ManagedUser;
  authorized: boolean;
  operatorId: number;
  operatorRole: number;
  onClose: () => void;
}

function canManageFreshTarget(
  targetId: number,
  targetRole: number,
  operatorId: number,
  operatorRole: number,
): boolean {
  return targetId !== operatorId && targetRole < operatorRole;
}

function DialogFrame({
  title,
  description,
  busy = false,
  onClose,
  children,
}: {
  title: string;
  description: string;
  busy?: boolean;
  onClose: () => void;
  children: React.ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <section className="user-admin-dialog user-subpanel" role="dialog" aria-modal="false" aria-labelledby="user-dialog-title">
      <div className="user-heading">
        <div>
          <h3 id="user-dialog-title">{title}</h3>
          <p className="muted">{description}</p>
        </div>
        <button className="link" type="button" disabled={busy} onClick={onClose}>{t('Close')}</button>
      </div>
      {children}
    </section>
  );
}

function cloneMatrix(value: PermissionMatrix): PermissionMatrix {
  return Object.fromEntries(Object.entries(value).map(([resource, actions]) => [resource, { ...actions }]));
}

function adminDefaults(catalog: PermissionCatalog): PermissionMatrix {
  const admin = catalog.roles.find((role) => role.key === 'admin');
  return cloneMatrix(admin?.grants ?? {});
}

export function UserPermissionDialog({
  user,
  authorized,
  operatorId,
  operatorRole,
  onClose,
}: GuardedDialogProps) {
  const { t } = useTranslation();
  const [catalog, setCatalog] = useState<PermissionCatalog | null>(null);
  const [permissions, setPermissions] = useState<PermissionMatrix | null>(null);
  const [loading, setLoading] = useState(authorized);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState<'saved' | 'save-error' | null>(null);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (!authorized) {
      setLoading(false);
      return undefined;
    }
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    setNotice(null);
    void (async () => {
      try {
        const nextCatalog = await loadPermissionCatalog(controller.signal);
        if (controller.signal.aborted) return;
        const state = await loadUserPermissionState(user.id, nextCatalog, controller.signal);
        if (controller.signal.aborted) return;
        if (operatorRole !== USER_ROLE_ROOT || state.role !== USER_ROLE_ADMIN
          || !canManageFreshTarget(state.id, state.role, operatorId, operatorRole)) {
          throw new Error('target is no longer manageable');
        }
        setCatalog(nextCatalog);
        setPermissions(cloneMatrix(state.permissions));
      } catch {
        if (!controller.signal.aborted) {
          setCatalog(null);
          setPermissions(null);
          setLoadError(true);
        }
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    })();
    return () => controller.abort();
  }, [authorized, operatorId, operatorRole, revision, user.id]);

  function toggle(resource: string, action: string, checked: boolean) {
    setPermissions((current) => current === null ? null : {
      ...current,
      [resource]: { ...current[resource], [action]: checked },
    });
    setNotice(null);
  }

  async function save() {
    if (!authorized || !catalog || !permissions || saving) return;
    if (!window.confirm(t('Save administrator permissions for “{{name}}”?', { name: user.username }))) return;
    setSaving(true);
    setNotice(null);
    try {
      await updateUserPermissions(user.id, catalog, permissions);
      setNotice('saved');
    } catch {
      setNotice('save-error');
    } finally {
      setSaving(false);
    }
  }

  return (
    <DialogFrame
      title={t('Permissions for {{name}}', { name: user.username })}
      description={t('Root operators can tailor this administrator’s capabilities.')}
      busy={saving}
      onClose={onClose}
    >
      {!authorized && <p className="error" role="alert">{t('Root access is required.')}</p>}
      {authorized && loading && <p role="status">{t('Loading permissions…')}</p>}
      {authorized && !loading && loadError && (
        <div className="user-dialog-state">
          <p className="error" role="alert">{t('Unable to load permissions.')}</p>
          <button type="button" onClick={() => setRevision((value) => value + 1)}>{t('Try again')}</button>
        </div>
      )}
      {authorized && !loading && !loadError && catalog && permissions && catalog.resources.length === 0 && (
        <p>{t('No configurable permissions are available.')}</p>
      )}
      {authorized && !loading && !loadError && catalog && permissions && catalog.resources.length > 0 && (
        <>
          <div className="user-permission-grid">
            {catalog.resources.map((resource) => (
              <fieldset key={resource.resource}>
                <legend>{t(resource.labelKey)}</legend>
                {resource.actions.map((action) => (
                  <label key={action.action}>
                    <input
                      type="checkbox"
                      checked={permissions[resource.resource]?.[action.action] === true}
                      onChange={(event) => toggle(resource.resource, action.action, event.target.checked)}
                    />
                    <span>
                      <strong>{t(action.labelKey)}</strong>
                      <small>{t(action.descriptionKey)}</small>
                    </span>
                  </label>
                ))}
              </fieldset>
            ))}
          </div>
          {notice === 'saved' && <p className="success" role="status">{t('Administrator permissions saved.')}</p>}
          {notice === 'save-error' && <p className="error" role="alert">{t('Unable to save administrator permissions.')}</p>}
          <div className="user-dialog-actions">
            <button className="link" type="button" disabled={saving}
              onClick={() => setPermissions(adminDefaults(catalog))}>{t('Restore admin defaults')}</button>
            <button type="button" disabled={saving} onClick={() => void save()}>
              {saving ? t('Saving…') : t('Save permissions')}
            </button>
          </div>
        </>
      )}
    </DialogFrame>
  );
}

const BUILT_IN_LABELS: Record<BuiltInBindingType, string> = {
  email: 'Email',
  github: 'GitHub',
  discord: 'Discord',
  oidc: 'OIDC',
  wechat: 'WeChat',
  telegram: 'Telegram',
  linuxdo: 'LinuxDO',
};

type BindingRow = {
  key: string;
  label: string;
  bound: boolean;
  builtIn?: BuiltInBindingType;
  providerId?: number;
  providerSlug?: string;
};

function bindingRows(builtIns: BuiltInBindingState[], custom: CustomOAuthBinding[]): BindingRow[] {
  return [
    ...builtIns.map((binding) => ({
      key: `builtin:${binding.type}`,
      label: BUILT_IN_LABELS[binding.type],
      bound: binding.bound,
      builtIn: binding.type,
    })),
    ...custom.map((binding) => ({
      key: `custom:${binding.providerId}`,
      label: binding.providerName,
      bound: true,
      providerId: binding.providerId,
      providerSlug: binding.providerSlug,
    })),
  ];
}

function bindingLabel(row: BindingRow, translate: (key: string) => string): string {
  return row.builtIn ? translate(BUILT_IN_LABELS[row.builtIn]) : row.label;
}

export function UserBindingsDialog({
  user,
  authorized,
  operatorId,
  operatorRole,
  onClose,
}: GuardedDialogProps) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<BindingRow[]>([]);
  const [showUnbound, setShowUnbound] = useState(false);
  const [loading, setLoading] = useState(authorized);
  const [loadError, setLoadError] = useState(false);
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<'cleared' | 'clear-error' | null>(null);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (!authorized) {
      setLoading(false);
      return undefined;
    }
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void (async () => {
      try {
        const details = await loadUserBindingDetails(user.id, controller.signal);
        if (controller.signal.aborted) return;
        if (!canManageFreshTarget(details.id, details.role, operatorId, operatorRole)) {
          throw new Error('target is no longer manageable');
        }
        const custom = await loadCustomOAuthBindings(user.id, controller.signal);
        if (!controller.signal.aborted) setRows(bindingRows(details.bindings, custom));
      } catch {
        if (!controller.signal.aborted) {
          setRows([]);
          setLoadError(true);
        }
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    })();
    return () => controller.abort();
  }, [authorized, operatorId, operatorRole, revision, user.id]);

  const displayedRows = showUnbound ? rows : rows.filter((row) => row.bound);

  async function clear(row: BindingRow) {
    if (!authorized || busy || !row.bound) return;
    if (!window.confirm(t('Unbind {{provider}} from “{{name}}”?', {
      provider: bindingLabel(row, t),
      name: user.username,
    }))) return;
    setBusy(row.key);
    setNotice(null);
    try {
      if (row.builtIn) await clearBuiltInUserBinding(user.id, row.builtIn);
      else if (row.providerId) await unbindCustomOAuth(user.id, row.providerId);
      else throw new Error('invalid binding');
      setNotice('cleared');
      setRevision((value) => value + 1);
    } catch {
      setNotice('clear-error');
    } finally {
      setBusy('');
    }
  }

  return (
    <DialogFrame
      title={t('Account bindings for {{name}}', { name: user.username })}
      description={t('Provider identifiers are hidden; only connection state is shown.')}
      busy={Boolean(busy)}
      onClose={onClose}
    >
      {!authorized && <p className="error" role="alert">{t('Administrator access is required.')}</p>}
      {authorized && loading && <p role="status">{t('Loading account bindings…')}</p>}
      {authorized && !loading && loadError && (
        <div className="user-dialog-state">
          <p className="error" role="alert">{t('Unable to load account bindings.')}</p>
          <button type="button" onClick={() => setRevision((value) => value + 1)}>{t('Try again')}</button>
        </div>
      )}
      {authorized && !loading && !loadError && (
        <>
          <label className="user-dialog-toggle">
            <input type="checkbox" checked={showUnbound}
              onChange={(event) => setShowUnbound(event.target.checked)} />
            {t('Show unbound providers')}
          </label>
          {displayedRows.length === 0 ? <p>{t('This user has no account bindings.')}</p> : (
            <ul className="user-binding-list" aria-label={t('Account bindings')}>
              {displayedRows.map((row) => (
                <li key={row.key}>
                  <span>
                    <strong>{bindingLabel(row, t)}</strong>
                    {row.providerSlug && <small>{row.providerSlug}</small>}
                  </span>
                  <span className={`status-pill ${row.bound ? 'enabled' : 'disabled'}`}>
                    {row.bound ? t('Bound') : t('Not bound')}
                  </span>
                  {row.bound && (
                    <button className="link danger-link" type="button" disabled={Boolean(busy)}
                      onClick={() => void clear(row)}>
                      {busy === row.key ? t('Unbinding…') : t('Unbind')}
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {notice === 'cleared' && <p className="success" role="status">{t('Account binding removed.')}</p>}
          {notice === 'clear-error' && <p className="error" role="alert">{t('Unable to remove account binding.')}</p>}
        </>
      )}
    </DialogFrame>
  );
}

function subscriptionStatus(subscription: ManagedUserSubscription): 'Active' | 'Expired' | 'Cancelled' {
  if (subscription.status === 'cancelled') return 'Cancelled';
  if (subscription.status === 'expired' || (subscription.endTime > 0 && subscription.endTime <= Date.now() / 1000)) {
    return 'Expired';
  }
  return 'Active';
}

function translatedSubscriptionStatus(
  status: ReturnType<typeof subscriptionStatus>,
  translate: (key: string) => string,
): string {
  if (status === 'Active') return translate('Active');
  if (status === 'Cancelled') return translate('Cancelled');
  return translate('Expired');
}

function timestamp(value: number): string {
  if (value <= 0) return '—';
  return new Date(value * 1000).toLocaleString();
}

export function UserSubscriptionsDialog({
  user,
  authorized,
  operatorId,
  operatorRole,
  onClose,
}: GuardedDialogProps) {
  const { t } = useTranslation();
  const [plans, setPlans] = useState<ManagedSubscriptionPlan[]>([]);
  const [subscriptions, setSubscriptions] = useState<ManagedUserSubscription[]>([]);
  const [selectedPlanId, setSelectedPlanId] = useState('');
  const [advanceResetTime, setAdvanceResetTime] = useState(true);
  const [loading, setLoading] = useState(authorized);
  const [loadError, setLoadError] = useState(false);
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState<'updated' | 'update-error' | null>(null);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (!authorized) {
      setLoading(false);
      return undefined;
    }
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    void (async () => {
      try {
        const details = await loadUserBindingDetails(user.id, controller.signal);
        if (controller.signal.aborted) return;
        if (!canManageFreshTarget(details.id, details.role, operatorId, operatorRole)) {
          throw new Error('target is no longer manageable');
        }
        const [nextPlans, nextSubscriptions] = await Promise.all([
          listSubscriptionPlans(controller.signal),
          listUserSubscriptions(user.id, controller.signal),
        ]);
        if (controller.signal.aborted) return;
        setPlans(nextPlans);
        setSubscriptions(nextSubscriptions);
      } catch {
        if (!controller.signal.aborted) {
          setPlans([]);
          setSubscriptions([]);
          setLoadError(true);
        }
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    })();
    return () => controller.abort();
  }, [authorized, operatorId, operatorRole, revision, user.id]);

  const planNames = useMemo(() => new Map(plans.map((plan) => [plan.id, plan.title])), [plans]);

  async function mutate(action: 'grant' | 'reset' | 'invalidate' | 'delete', subscription?: ManagedUserSubscription) {
    if (!authorized || busy) return;
    const planId = action === 'grant' ? Number(selectedPlanId) : subscription?.planId;
    if (!Number.isSafeInteger(planId) || !planId || planId < 1) return;
    const prompt = action === 'grant'
      ? t('Grant “{{plan}}” to “{{name}}”?', { plan: planNames.get(planId) ?? `#${planId}`, name: user.username })
      : action === 'reset'
        ? t('Reset active “{{plan}}” subscriptions for “{{name}}”?', { plan: planNames.get(planId) ?? `#${planId}`, name: user.username })
        : action === 'invalidate'
          ? t('Invalidate subscription #{{id}} for “{{name}}”?', { id: subscription?.id, name: user.username })
          : t('Permanently delete subscription #{{id}} for “{{name}}”?', { id: subscription?.id, name: user.username });
    if (!window.confirm(prompt)) return;
    const actionKey = `${action}:${subscription?.id ?? planId}`;
    setBusy(actionKey);
    setNotice(null);
    try {
      if (action === 'grant') await createUserSubscription(user.id, planId);
      else if (action === 'reset') await resetUserSubscriptionsByPlan(user.id, planId, advanceResetTime);
      else if (action === 'invalidate' && subscription) await invalidateUserSubscription(subscription.id);
      else if (action === 'delete' && subscription) await deleteUserSubscription(subscription.id);
      else throw new Error('invalid subscription action');
      setNotice('updated');
      setSelectedPlanId('');
      setRevision((value) => value + 1);
    } catch {
      setNotice('update-error');
    } finally {
      setBusy('');
    }
  }

  return (
    <DialogFrame
      title={t('Subscriptions for {{name}}', { name: user.username })}
      description={t('Grant plans and manage this user’s subscription records.')}
      busy={Boolean(busy)}
      onClose={onClose}
    >
      {!authorized && <p className="error" role="alert">{t('Administrator access is required.')}</p>}
      {authorized && loading && <p role="status">{t('Loading subscriptions…')}</p>}
      {authorized && !loading && loadError && (
        <div className="user-dialog-state">
          <p className="error" role="alert">{t('Unable to load subscriptions.')}</p>
          <button type="button" onClick={() => setRevision((value) => value + 1)}>{t('Try again')}</button>
        </div>
      )}
      {authorized && !loading && !loadError && (
        <>
          <div className="user-subscription-grant">
            <label>{t('Subscription plan')}
              <select value={selectedPlanId} disabled={Boolean(busy) || plans.length === 0}
                onChange={(event) => setSelectedPlanId(event.target.value)}>
                <option value="">{plans.length === 0 ? t('No plans available') : t('Select a plan')}</option>
                {plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.title}</option>)}
              </select>
            </label>
            <button type="button" disabled={Boolean(busy) || !selectedPlanId}
              onClick={() => void mutate('grant')}>{t('Grant subscription')}</button>
          </div>
          <label className="user-subscription-reset-policy">
            <input
              type="checkbox"
              checked={advanceResetTime}
              disabled={Boolean(busy)}
              onChange={(event) => setAdvanceResetTime(event.target.checked)}
            />
            {t('Advance next reset time')}
          </label>
          {subscriptions.length === 0 ? <p>{t('No subscription records.')}</p> : (
            <div className="table-scroll">
              <table aria-label={t('User subscriptions')}>
                <thead><tr>
                  <th scope="col">{t('Plan')}</th>
                  <th scope="col">{t('Status')}</th>
                  <th scope="col">{t('Quota')}</th>
                  <th scope="col">{t('End')}</th>
                  <th scope="col">{t('Actions')}</th>
                </tr></thead>
                <tbody>
                  {subscriptions.map((subscription) => {
                    const currentStatus = subscriptionStatus(subscription);
                    const active = currentStatus === 'Active';
                    return (
                      <tr key={subscription.id}>
                        <td><strong>{planNames.get(subscription.planId) ?? `#${subscription.planId}`}</strong>
                          <span className="user-meta">{t('ID')} {subscription.id}</span></td>
                        <td>{translatedSubscriptionStatus(currentStatus, t)}</td>
                        <td>{subscription.amountUsed.toLocaleString()} / {subscription.amountTotal.toLocaleString()}</td>
                        <td>{timestamp(subscription.endTime)}</td>
                        <td><div className="user-actions">
                          <button className="link" type="button" disabled={Boolean(busy) || !active}
                            onClick={() => void mutate('reset', subscription)}>{t('Reset quota')}</button>
                          <button className="link" type="button" disabled={Boolean(busy) || !active}
                            onClick={() => void mutate('invalidate', subscription)}>{t('Invalidate')}</button>
                          <button className="link danger-link" type="button" disabled={Boolean(busy)}
                            onClick={() => void mutate('delete', subscription)}>{t('Delete')}</button>
                        </div></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {notice === 'updated' && <p className="success" role="status">{t('Subscriptions updated.')}</p>}
          {notice === 'update-error' && <p className="error" role="alert">{t('Unable to update subscriptions.')}</p>}
        </>
      )}
    </DialogFrame>
  );
}
