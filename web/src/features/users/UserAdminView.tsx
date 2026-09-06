import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  UserBindingsDialog,
  UserPermissionDialog,
  UserSubscriptionsDialog,
} from './UserAdminDialogs';
import {
  USER_PAGE_SIZE,
  USER_ROLE_ADMIN,
  USER_ROLE_COMMON,
  USER_ROLE_ROOT,
  USER_STATUS_DISABLED,
  USER_STATUS_ENABLED,
  adjustUserQuota,
  createUser,
  deleteUser,
  listUsers,
  loadPermissionCatalog,
  loadUserDetails,
  loadUserGroups,
  manageUser,
  resetUserPasskey,
  resetUserTwoFactor,
  searchUsers,
  updateUser,
  type CreateUserInput,
  type ManagedUser,
  type PermissionCatalog,
  type PermissionMatrix,
  type QuotaMode,
  type UserManageAction,
  type UserQuery,
  type UserRoleFilter,
  type UserSortBy,
  type UserSortOrder,
  type UserStatusFilter,
} from './user-api';

type FilterDraft = Omit<UserQuery, 'page' | 'pageSize'>;
type Notice = { kind: 'success' | 'error'; text: string } | null;
type UserDialog = { kind: 'permissions' | 'bindings' | 'subscriptions'; user: ManagedUser } | null;

const EMPTY_FILTERS: FilterDraft = {
  keyword: '',
  group: '',
  role: '',
  status: '',
  sortBy: 'id',
  sortOrder: 'desc',
};

const EMPTY_CREATE: CreateUserInput = {
  username: '',
  displayName: '',
  password: '',
  role: USER_ROLE_COMMON,
};

function clonePermissionMatrix(value: PermissionMatrix): PermissionMatrix {
  return Object.fromEntries(Object.entries(value).map(([resource, actions]) => [resource, { ...actions }]));
}

function administratorPermissionDefaults(catalog: PermissionCatalog): PermissionMatrix {
  return clonePermissionMatrix(catalog.roles.find((role) => role.key === 'admin')?.grants ?? {});
}

function requiresSearch(query: UserQuery): boolean {
  return Boolean(query.keyword || query.group || query.role !== '' || query.status !== ''
    || query.sortBy !== 'id' || query.sortOrder !== 'desc');
}

function manageable(user: ManagedUser, operatorId: number, operatorRole: number, deletedResults: boolean): boolean {
  return !deletedResults && user.id !== operatorId && user.role < operatorRole;
}

function userTimestamp(value: number): string {
  return new Date(value * 1_000).toLocaleString();
}

export function UserAdminView({ operatorId, operatorRole }: { operatorId: number; operatorRole: number }) {
  const { t } = useTranslation();
  const authorized = operatorRole >= USER_ROLE_ADMIN;
  const isRoot = operatorRole >= USER_ROLE_ROOT;
  const [draft, setDraft] = useState<FilterDraft>(EMPTY_FILTERS);
  const [query, setQuery] = useState<UserQuery>({ ...EMPTY_FILTERS, page: 1, pageSize: USER_PAGE_SIZE });
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [total, setTotal] = useState(0);
  const [groups, setGroups] = useState<string[]>([]);
  const [groupsUnavailable, setGroupsUnavailable] = useState(false);
  const [loading, setLoading] = useState(authorized);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [notice, setNotice] = useState<Notice>(null);
  const [busyAction, setBusyAction] = useState('');
  const [creating, setCreating] = useState(false);
  const [createDraft, setCreateDraft] = useState<CreateUserInput>(EMPTY_CREATE);
  const [createPermissionCatalog, setCreatePermissionCatalog] = useState<PermissionCatalog | null>(null);
  const [createPermissions, setCreatePermissions] = useState<PermissionMatrix | null>(null);
  const [createPermissionsLoading, setCreatePermissionsLoading] = useState(false);
  const [createPermissionsError, setCreatePermissionsError] = useState(false);
  const [createPermissionsRevision, setCreatePermissionsRevision] = useState(0);
  const [editing, setEditing] = useState<ManagedUser | null>(null);
  const [editDisplayName, setEditDisplayName] = useState('');
  const [editGroup, setEditGroup] = useState('');
  const [editRemark, setEditRemark] = useState('');
  const [editPassword, setEditPassword] = useState('');
  const [editLoading, setEditLoading] = useState(false);
  const [editLoadError, setEditLoadError] = useState(false);
  const editRequest = useRef<AbortController | null>(null);
  const editGeneration = useRef(0);
  const [quotaMode, setQuotaMode] = useState<QuotaMode>('add');
  const [quotaValue, setQuotaValue] = useState('');
  const [dialog, setDialog] = useState<UserDialog>(null);

  useEffect(() => {
    if (!authorized) return undefined;
    const controller = new AbortController();
    setLoading(true);
    setLoadError(false);
    const request = requiresSearch(query)
      ? searchUsers(query, controller.signal)
      : listUsers(query.page, query.pageSize, controller.signal);
    void request
      .then((page) => {
        if (controller.signal.aborted) return;
        setUsers(page.items);
        setTotal(page.total);
        const lastPage = Math.max(1, Math.ceil(page.total / query.pageSize));
        if (query.page > lastPage) setQuery((current) => ({ ...current, page: lastPage }));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setUsers([]);
          setTotal(0);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [authorized, query, reload]);

  useEffect(() => {
    if (!authorized) return undefined;
    const controller = new AbortController();
    void loadUserGroups(controller.signal)
      .then((values) => {
        if (!controller.signal.aborted) {
          setGroups(values);
          setGroupsUnavailable(false);
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setGroupsUnavailable(true);
      });
    return () => controller.abort();
  }, [authorized]);

  useEffect(() => () => {
    editGeneration.current += 1;
    editRequest.current?.abort();
  }, []);

  useEffect(() => {
    if (!creating || !isRoot || createDraft.role !== USER_ROLE_ADMIN) {
      setCreatePermissionCatalog(null);
      setCreatePermissions(null);
      setCreatePermissionsLoading(false);
      setCreatePermissionsError(false);
      return undefined;
    }
    const controller = new AbortController();
    setCreatePermissionsLoading(true);
    setCreatePermissionsError(false);
    void loadPermissionCatalog(controller.signal)
      .then((catalog) => {
        if (controller.signal.aborted) return;
        setCreatePermissionCatalog(catalog);
        setCreatePermissions(administratorPermissionDefaults(catalog));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setCreatePermissionCatalog(null);
          setCreatePermissions(null);
          setCreatePermissionsError(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setCreatePermissionsLoading(false);
      });
    return () => controller.abort();
  }, [createDraft.role, createPermissionsRevision, creating, isRoot]);

  const pageCount = Math.max(1, Math.ceil(total / query.pageSize));
  const deletedResults = query.status === -1;
  const refresh = useCallback(() => setReload((value) => value + 1), []);

  function submitSearch(event: React.FormEvent) {
    event.preventDefault();
    setNotice(null);
    setQuery({ ...draft, page: 1, pageSize: USER_PAGE_SIZE });
  }

  function clearSearch() {
    setDraft(EMPTY_FILTERS);
    setQuery({ ...EMPTY_FILTERS, page: 1, pageSize: USER_PAGE_SIZE });
    setNotice(null);
  }

  function closeEditor() {
    editGeneration.current += 1;
    editRequest.current?.abort();
    setEditing(null);
    setEditPassword('');
    setEditLoading(false);
    setEditLoadError(false);
  }

  function openEditor(user: ManagedUser) {
    setDialog(null);
    setEditing(user);
    setEditDisplayName(user.displayName || user.username);
    setEditGroup(user.group || 'default');
    setEditRemark(user.remark);
    setEditPassword('');
    setQuotaMode('add');
    setQuotaValue('');
    setNotice(null);
    setEditLoading(true);
    setEditLoadError(false);
    const generation = ++editGeneration.current;
    editRequest.current?.abort();
    const controller = new AbortController();
    editRequest.current = controller;
    void loadUserDetails(user.id, controller.signal)
      .then((fresh) => {
        if (controller.signal.aborted || generation !== editGeneration.current) return;
        if (!manageable(fresh, operatorId, operatorRole, deletedResults)) {
          throw new Error('target is no longer manageable');
        }
        setEditing(fresh);
        setEditDisplayName(fresh.displayName || fresh.username);
        setEditGroup(fresh.group || 'default');
        setEditRemark(fresh.remark);
      })
      .catch(() => {
        if (!controller.signal.aborted && generation === editGeneration.current) setEditLoadError(true);
      })
      .finally(() => {
        if (!controller.signal.aborted && generation === editGeneration.current) setEditLoading(false);
      });
  }

  async function submitCreate(event: React.FormEvent) {
    event.preventDefault();
    if (busyAction) return;
    const needsPermissions = isRoot && createDraft.role === USER_ROLE_ADMIN;
    if (needsPermissions && (!createPermissionCatalog || !createPermissions || createPermissionsError)) return;
    const action = 'create';
    setBusyAction(action);
    setNotice(null);
    try {
      await createUser({
        ...createDraft,
        ...(needsPermissions && createPermissions ? { adminPermissions: createPermissions } : {}),
      });
      setNotice({ kind: 'success', text: t('User created.') });
      setCreateDraft(EMPTY_CREATE);
      setCreating(false);
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to create user.') });
    } finally {
      // A password is write-only and never retained after any submission.
      setCreateDraft((current) => ({ ...current, password: '' }));
      setBusyAction('');
    }
  }

  async function submitEdit(event: React.FormEvent) {
    event.preventDefault();
    if (!editing || busyAction || editLoading || editLoadError) return;
    if (editPassword && !window.confirm(
      t('Reset the password for “{{name}}” and revoke every active session?', { name: editing.username }),
    )) return;
    const action = `edit:${editing.id}`;
    setBusyAction(action);
    setNotice(null);
    try {
      await updateUser({
        id: editing.id,
        displayName: editDisplayName,
        group: editGroup,
        remark: editRemark,
        ...(editPassword ? { password: editPassword } : {}),
      });
      setNotice({ kind: 'success', text: t('User updated.') });
      closeEditor();
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to update user.') });
    } finally {
      setEditPassword('');
      setBusyAction('');
    }
  }

  async function submitQuota(event: React.FormEvent) {
    event.preventDefault();
    if (!editing || busyAction) return;
    const parsed = Number(quotaValue);
    if (!Number.isSafeInteger(parsed) || parsed < (quotaMode === 'override' ? 0 : 1)) {
      setNotice({ kind: 'error', text: t('Enter a valid whole-number quota.') });
      return;
    }
    const action = `quota:${editing.id}`;
    setBusyAction(action);
    setNotice(null);
    try {
      await adjustUserQuota(editing.id, quotaMode, parsed);
      setNotice({ kind: 'success', text: t('User quota adjusted.') });
      setQuotaValue('');
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to adjust user quota.') });
    } finally {
      setBusyAction('');
    }
  }

  async function runManage(user: ManagedUser, action: UserManageAction) {
    if (busyAction) return;
    if ((action === 'promote' || action === 'demote') && !window.confirm(
      action === 'promote'
        ? t('Promote user “{{name}}” to administrator?', { name: user.username })
        : t('Demote administrator “{{name}}” to user?', { name: user.username }),
    )) return;
    const actionKey = `${action}:${user.id}`;
    setBusyAction(actionKey);
    setNotice(null);
    try {
      await manageUser(user.id, action);
      const messages: Record<UserManageAction, string> = {
        enable: t('User enabled.'),
        disable: t('User disabled.'),
        promote: t('User promoted.'),
        demote: t('User demoted.'),
      };
      setNotice({ kind: 'success', text: messages[action] });
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to complete user action.') });
    } finally {
      setBusyAction('');
    }
  }

  async function runDestructive(user: ManagedUser, kind: 'delete' | 'passkey' | 'two-factor') {
    if (busyAction) return;
    const prompts = {
      delete: t('Delete user “{{name}}”? Access and active sessions will be revoked.', { name: user.username }),
      passkey: t('Reset the passkey for “{{name}}”?', { name: user.username }),
      'two-factor': t('Reset two-factor authentication for “{{name}}”?', { name: user.username }),
    };
    if (!window.confirm(prompts[kind])) return;
    const action = `${kind}:${user.id}`;
    setBusyAction(action);
    setNotice(null);
    try {
      if (kind === 'delete') await deleteUser(user.id);
      else if (kind === 'passkey') await resetUserPasskey(user.id);
      else await resetUserTwoFactor(user.id);
      const messages = {
        delete: t('User deleted.'),
        passkey: t('User passkey reset.'),
        'two-factor': t('User two-factor authentication reset.'),
      };
      setNotice({ kind: 'success', text: messages[kind] });
      if (editing?.id === user.id) closeEditor();
      if (dialog?.user.id === user.id) setDialog(null);
      refresh();
    } catch {
      setNotice({ kind: 'error', text: t('Unable to complete user action.') });
    } finally {
      setBusyAction('');
    }
  }

  const groupOptions = useMemo(() => {
    const values = new Set(groups);
    if (draft.group) values.add(draft.group);
    if (editGroup) values.add(editGroup);
    return [...values].sort((left, right) => left.localeCompare(right));
  }, [draft.group, editGroup, groups]);

  function roleName(value: number): string {
    if (value === USER_ROLE_ROOT) return t('Root');
    if (value === USER_ROLE_ADMIN) return t('Administrator');
    return t('User');
  }

  if (!authorized) {
    return (
      <section className="card user-admin">
        <h2>{t('Users')}</h2>
        <p className="error" role="alert">{t('Administrator access is required.')}</p>
      </section>
    );
  }

  return (
    <section className="card user-admin" aria-labelledby="user-admin-title">
      <div className="user-heading">
        <div>
          <h2 id="user-admin-title">{t('User management')}</h2>
          <p className="muted">{t('Search accounts and apply role-bounded administrative changes.')}</p>
        </div>
        <button type="button" onClick={() => setCreating((value) => !value)} aria-expanded={creating}>
          {creating ? t('Cancel') : t('Create user')}
        </button>
      </div>

      {notice && <p className={notice.kind} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}

      {creating && (
        <form className="user-subpanel grid-form" onSubmit={submitCreate} aria-labelledby="create-user-title">
          <h3 id="create-user-title">{t('Create user')}</h3>
          <label>{t('Username')}
            <input required maxLength={20} autoComplete="off" value={createDraft.username}
              onChange={(event) => setCreateDraft((current) => ({ ...current, username: event.target.value }))} />
          </label>
          <label>{t('Display name')}
            <input maxLength={20} value={createDraft.displayName}
              onChange={(event) => setCreateDraft((current) => ({ ...current, displayName: event.target.value }))} />
          </label>
          <label>{t('Password')}
            <input required type="password" minLength={8} maxLength={20} autoComplete="new-password" value={createDraft.password}
              onChange={(event) => setCreateDraft((current) => ({ ...current, password: event.target.value }))} />
          </label>
          <label>{t('Role')}
            <select value={createDraft.role} onChange={(event) => setCreateDraft((current) => ({
              ...current,
              role: Number(event.target.value) === USER_ROLE_ADMIN ? USER_ROLE_ADMIN : USER_ROLE_COMMON,
            }))}>
              <option value={USER_ROLE_COMMON}>{t('User')}</option>
              {isRoot && <option value={USER_ROLE_ADMIN}>{t('Administrator')}</option>}
            </select>
          </label>
          {isRoot && createDraft.role === USER_ROLE_ADMIN && (
            <fieldset className="user-create-permissions">
              <legend>{t('Administrator permissions')}</legend>
              {createPermissionsLoading && <p role="status">{t('Loading permission defaults…')}</p>}
              {!createPermissionsLoading && createPermissionsError && (
                <div className="user-dialog-state">
                  <p className="error" role="alert">{t('Unable to load permission defaults.')}</p>
                  <button type="button" onClick={() => setCreatePermissionsRevision((value) => value + 1)}>{t('Try again')}</button>
                </div>
              )}
              {!createPermissionsLoading && !createPermissionsError && createPermissionCatalog && createPermissions && (
                <>
                  <div className="user-permission-grid">
                    {createPermissionCatalog.resources.map((resource) => (
                      <fieldset key={resource.resource}>
                        <legend>{t(resource.labelKey)}</legend>
                        {resource.actions.map((action) => (
                          <label key={action.action}>
                            <input
                              type="checkbox"
                              checked={createPermissions[resource.resource]?.[action.action] === true}
                              onChange={(event) => setCreatePermissions((current) => current === null ? null : ({
                                ...current,
                                [resource.resource]: {
                                  ...current[resource.resource],
                                  [action.action]: event.target.checked,
                                },
                              }))}
                            />
                            <span><strong>{t(action.labelKey)}</strong><small>{t(action.descriptionKey)}</small></span>
                          </label>
                        ))}
                      </fieldset>
                    ))}
                  </div>
                  <button className="link" type="button" onClick={() => setCreatePermissions(
                    administratorPermissionDefaults(createPermissionCatalog),
                  )}>{t('Restore admin defaults')}</button>
                </>
              )}
            </fieldset>
          )}
          <p className="muted user-form-note">{t('Passwords are submitted once and never displayed by this screen.')}</p>
          <button type="submit" disabled={Boolean(busyAction) || (isRoot && createDraft.role === USER_ROLE_ADMIN && (
            createPermissionsLoading || createPermissionsError || createPermissions === null
          ))}>
            {busyAction === 'create' ? t('Creating…') : t('Add user')}
          </button>
        </form>
      )}

      <datalist id="user-group-options">
        {groupOptions.map((group) => <option key={group} value={group} />)}
      </datalist>

      <form className="user-filters" role="search" aria-label={t('Search users')} onSubmit={submitSearch}>
        <label>{t('Username, email, or ID')}
          <input maxLength={128} value={draft.keyword}
            onChange={(event) => setDraft((current) => ({ ...current, keyword: event.target.value }))} />
        </label>
        <label>{t('Group')}
          <input maxLength={64} list="user-group-options" value={draft.group}
            onChange={(event) => setDraft((current) => ({ ...current, group: event.target.value }))} />
        </label>
        <label>{t('Role')}
          <select value={draft.role} onChange={(event) => setDraft((current) => ({
            ...current,
            role: (event.target.value === '' ? '' : Number(event.target.value)) as UserRoleFilter,
          }))}>
            <option value="">{t('All roles')}</option>
            <option value={USER_ROLE_COMMON}>{t('User')}</option>
            <option value={USER_ROLE_ADMIN}>{t('Administrator')}</option>
            <option value={USER_ROLE_ROOT}>{t('Root')}</option>
          </select>
        </label>
        <label>{t('Status')}
          <select value={draft.status} onChange={(event) => setDraft((current) => ({
            ...current,
            status: (event.target.value === '' ? '' : Number(event.target.value)) as UserStatusFilter,
          }))}>
            <option value="">{t('All statuses')}</option>
            <option value={USER_STATUS_ENABLED}>{t('Enabled')}</option>
            <option value={USER_STATUS_DISABLED}>{t('Disabled')}</option>
            <option value={-1}>{t('Deleted')}</option>
          </select>
        </label>
        <label>{t('Sort by')}
          <select value={draft.sortBy} onChange={(event) => setDraft((current) => ({
            ...current,
            sortBy: event.target.value as UserSortBy,
          }))}>
            <option value="id">{t('User ID')}</option>
            <option value="username">{t('Username')}</option>
            <option value="quota">{t('Quota')}</option>
            <option value="group">{t('Group')}</option>
            <option value="created_at">{t('Created time')}</option>
            <option value="last_login_at">{t('Last login')}</option>
          </select>
        </label>
        <label>{t('Order')}
          <select value={draft.sortOrder} onChange={(event) => setDraft((current) => ({
            ...current,
            sortOrder: event.target.value as UserSortOrder,
          }))}>
            <option value="desc">{t('Descending')}</option>
            <option value="asc">{t('Ascending')}</option>
          </select>
        </label>
        <div className="user-filter-actions">
          <button type="submit">{t('Apply filters')}</button>
          <button className="link" type="button" onClick={clearSearch}>{t('Clear filters')}</button>
          <button className="link" type="button" onClick={refresh}>{t('Refresh')}</button>
        </div>
      </form>

      {groupsUnavailable && <p className="muted">{t('Group suggestions are unavailable; manual values still work.')}</p>}

      <div className="table-scroll">
        <table aria-label={t('User results')} aria-busy={loading}>
          <thead>
            <tr>
              <th scope="col">{t('User')}</th>
              <th scope="col">{t('Role')}</th>
              <th scope="col">{t('Status')}</th>
              <th scope="col">{t('Quota')}</th>
              <th scope="col">{t('Group')}</th>
              <th scope="col">{t('Created time')}</th>
              <th scope="col">{t('Last login')}</th>
              <th scope="col">{t('Actions')}</th>
            </tr>
          </thead>
          <tbody>
            {loading && <tr><td colSpan={8} className="user-state" role="status">{t('Loading users…')}</td></tr>}
            {!loading && loadError && (
              <tr><td colSpan={8} className="user-state">
                <p className="error" role="alert">{t('Unable to load users.')}</p>
                <button type="button" onClick={refresh}>{t('Try again')}</button>
              </td></tr>
            )}
            {!loading && !loadError && users.length === 0 && (
              <tr><td colSpan={8} className="user-state">{t('No users match these filters.')}</td></tr>
            )}
            {!loading && !loadError && users.map((user) => {
              const canManage = manageable(user, operatorId, operatorRole, deletedResults);
              const isDisabled = user.status === USER_STATUS_DISABLED;
              return (
                <tr key={user.id}>
                  <td>
                    <strong>{user.displayName || user.username}</strong>
                    <span className="user-meta">@{user.username} · ID {user.id}</span>
                    {user.email && <span className="user-meta">{user.email}</span>}
                  </td>
                  <td>{roleName(user.role)}</td>
                  <td><span className={`status-pill ${isDisabled || deletedResults ? 'disabled' : 'enabled'}`}>
                    {deletedResults ? t('Deleted') : isDisabled ? t('Disabled') : t('Enabled')}
                  </span></td>
                  <td>
                    {user.quota.toLocaleString()}
                    <span className="user-meta">{t('{{used}} used', { used: user.usedQuota.toLocaleString() })}</span>
                    <span className="user-meta">{t('{{count}} requests', { count: user.requestCount })}</span>
                  </td>
                  <td>{user.group || '—'}</td>
                  <td><time dateTime={new Date(user.createdAt * 1_000).toISOString()}>{userTimestamp(user.createdAt)}</time></td>
                  <td>{user.lastLoginAt > 0
                    ? <time dateTime={new Date(user.lastLoginAt * 1_000).toISOString()}>{userTimestamp(user.lastLoginAt)}</time>
                    : t('Never')}</td>
                  <td>
                    {canManage ? (
                      <div className="user-actions">
                        <button className="link" type="button" onClick={() => openEditor(user)}>{t('Edit')}</button>
                        {isRoot && user.role === USER_ROLE_ADMIN && (
                          <button className="link" type="button" disabled={Boolean(busyAction)}
                            onClick={() => setDialog({ kind: 'permissions', user })}>{t('Permissions')}</button>
                        )}
                        <button className="link" type="button" disabled={Boolean(busyAction)}
                          onClick={() => setDialog({ kind: 'bindings', user })}>{t('Bindings')}</button>
                        <button className="link" type="button" disabled={Boolean(busyAction)}
                          onClick={() => setDialog({ kind: 'subscriptions', user })}>{t('Subscriptions')}</button>
                        <button className="link" type="button" disabled={Boolean(busyAction)}
                          onClick={() => void runManage(user, isDisabled ? 'enable' : 'disable')}>
                          {isDisabled ? t('Enable') : t('Disable')}
                        </button>
                        {isRoot && user.role === USER_ROLE_COMMON && (
                          <button className="link" type="button" disabled={Boolean(busyAction)}
                            onClick={() => void runManage(user, 'promote')}>{t('Promote')}</button>
                        )}
                        {isRoot && user.role === USER_ROLE_ADMIN && (
                          <button className="link" type="button" disabled={Boolean(busyAction)}
                            onClick={() => void runManage(user, 'demote')}>{t('Demote')}</button>
                        )}
                        <button className="link" type="button" disabled={Boolean(busyAction)}
                          onClick={() => void runDestructive(user, 'passkey')}>{t('Reset passkey')}</button>
                        <button className="link" type="button" disabled={Boolean(busyAction)}
                          onClick={() => void runDestructive(user, 'two-factor')}>{t('Reset 2FA')}</button>
                        <button className="link danger-link" type="button" disabled={Boolean(busyAction)}
                          onClick={() => void runDestructive(user, 'delete')}>{t('Delete')}</button>
                      </div>
                    ) : <span className="muted">{t('Protected')}</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <nav className="user-pagination" aria-label={t('User pages')}>
        <button type="button" disabled={loading || query.page <= 1}
          onClick={() => setQuery((current) => ({ ...current, page: current.page - 1 }))}>{t('Previous')}</button>
        <span>{t('Page {{page}} of {{pages}}', { page: query.page, pages: pageCount })}</span>
        <button type="button" disabled={loading || query.page >= pageCount}
          onClick={() => setQuery((current) => ({ ...current, page: current.page + 1 }))}>{t('Next')}</button>
      </nav>

      {dialog?.kind === 'permissions' && (
        <UserPermissionDialog user={dialog.user} authorized={isRoot && manageable(
          dialog.user,
          operatorId,
          operatorRole,
          deletedResults,
        )} operatorId={operatorId} operatorRole={operatorRole} onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === 'bindings' && (
        <UserBindingsDialog user={dialog.user} authorized={manageable(
          dialog.user,
          operatorId,
          operatorRole,
          deletedResults,
        )} operatorId={operatorId} operatorRole={operatorRole} onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === 'subscriptions' && (
        <UserSubscriptionsDialog user={dialog.user} authorized={manageable(
          dialog.user,
          operatorId,
          operatorRole,
          deletedResults,
        )} operatorId={operatorId} operatorRole={operatorRole} onClose={() => setDialog(null)} />
      )}

      {editing && (
        <div className="user-subpanel" aria-labelledby="edit-user-title">
          <div className="user-heading">
            <h3 id="edit-user-title">{t('Edit user {{name}}', { name: editing.username })}</h3>
            <button className="link" type="button" onClick={closeEditor}>{t('Close')}</button>
          </div>
          {editLoading && <p role="status">{t('Loading user details…')}</p>}
          {!editLoading && editLoadError && (
            <div className="user-dialog-state">
              <p className="error" role="alert">{t('Unable to load user details.')}</p>
              <button type="button" onClick={() => openEditor(editing)}>{t('Try again')}</button>
            </div>
          )}
          {!editLoading && !editLoadError && (
            <>
              <form className="grid-form" onSubmit={submitEdit}>
                <label>{t('Display name')}
                  <input required maxLength={64} value={editDisplayName}
                    onChange={(event) => setEditDisplayName(event.target.value)} />
                </label>
                <label>{t('Group')}
                  <input required maxLength={64} list="user-group-options" value={editGroup}
                    onChange={(event) => setEditGroup(event.target.value)} />
                </label>
                <label>{t('Remark')}
                  <textarea maxLength={255} rows={2} value={editRemark} onChange={(event) => setEditRemark(event.target.value)} />
                </label>
                <label>{t('New password (optional)')}
                  <input type="password" minLength={8} maxLength={64} autoComplete="new-password" value={editPassword}
                    onChange={(event) => setEditPassword(event.target.value)} />
                </label>
                <p className="muted user-form-note">{t('A new password revokes every active session and is cleared from this form after submission.')}</p>
                <button type="submit" disabled={Boolean(busyAction)}>
                  {busyAction === `edit:${editing.id}` ? t('Saving…') : t('Save changes')}
                </button>
              </form>
              <form className="user-quota-form" onSubmit={submitQuota} aria-label={t('Adjust quota for {{name}}', { name: editing.username })}>
                <label>{t('Quota operation')}
                  <select value={quotaMode} onChange={(event) => setQuotaMode(event.target.value as QuotaMode)}>
                    <option value="add">{t('Add')}</option>
                    <option value="subtract">{t('Subtract')}</option>
                    <option value="override">{t('Set total')}</option>
                  </select>
                </label>
                <label>{t('Quota amount')}
                  <input required type="number" min={quotaMode === 'override' ? 0 : 1} max={2_147_483_647} step={1}
                    value={quotaValue} onChange={(event) => setQuotaValue(event.target.value)} />
                </label>
                <button type="submit" disabled={Boolean(busyAction)}>
                  {busyAction === `quota:${editing.id}` ? t('Updating…') : t('Adjust quota')}
                </button>
              </form>
            </>
          )}
        </div>
      )}
    </section>
  );
}
