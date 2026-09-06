// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  USER_ROLE_ADMIN,
  USER_ROLE_COMMON,
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
  type ManagedUser,
  type PermissionCatalog,
  type UserPage,
} from './user-api';
import { UserAdminView } from './UserAdminView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./user-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./user-api')>();
  return {
    ...actual,
    adjustUserQuota: vi.fn(),
    createUser: vi.fn(),
    deleteUser: vi.fn(),
    listUsers: vi.fn(),
    loadPermissionCatalog: vi.fn(),
    loadUserDetails: vi.fn(),
    loadUserGroups: vi.fn(),
    manageUser: vi.fn(),
    resetUserPasskey: vi.fn(),
    resetUserTwoFactor: vi.fn(),
    searchUsers: vi.fn(),
    updateUser: vi.fn(),
  };
});

const mockedAdjustQuota = vi.mocked(adjustUserQuota);
const mockedCreate = vi.mocked(createUser);
const mockedDelete = vi.mocked(deleteUser);
const mockedList = vi.mocked(listUsers);
const mockedPermissionCatalog = vi.mocked(loadPermissionCatalog);
const mockedDetails = vi.mocked(loadUserDetails);
const mockedGroups = vi.mocked(loadUserGroups);
const mockedManage = vi.mocked(manageUser);
const mockedResetPasskey = vi.mocked(resetUserPasskey);
const mockedResetTwoFactor = vi.mocked(resetUserTwoFactor);
const mockedSearch = vi.mocked(searchUsers);
const mockedUpdate = vi.mocked(updateUser);

const alice: ManagedUser = {
  id: 7,
  username: 'alice',
  displayName: 'Alice Operator',
  email: 'alice@example.test',
  quota: 10_000,
  usedQuota: 250,
  requestCount: 12,
  group: 'default',
  status: 1,
  role: USER_ROLE_COMMON,
  remark: 'reviewed',
  createdAt: 1_700_000_000,
  lastLoginAt: 1_700_000_100,
};

const peerAdmin: ManagedUser = {
  ...alice,
  id: 8,
  username: 'peer-admin',
  displayName: 'Peer Admin',
  email: '',
  status: 2,
  role: USER_ROLE_ADMIN,
  lastLoginAt: 0,
};

const permissionCatalog: PermissionCatalog = {
  resources: [{
    resource: 'channel',
    labelKey: 'Channel Management',
    actions: [
      { action: 'read', labelKey: 'Read channels', descriptionKey: 'View channel lists.' },
      { action: 'sensitive_write', labelKey: 'Edit sensitive channels', descriptionKey: 'Change credentials.' },
    ],
  }],
  roles: [{
    key: 'admin',
    name: 'Admin',
    builtIn: true,
    superuser: false,
    grants: { channel: { read: true, sensitive_write: false } },
  }],
};

function page(items: ManagedUser[] = [alice, peerAdmin], total = 21): UserPage {
  return { items, total, page: 1, pageSize: 20 };
}

function renderUsers(operatorRole = 100, operatorId = 1) {
  return render(<UserAdminView operatorId={operatorId} operatorRole={operatorRole} />);
}

beforeEach(() => {
  vi.resetAllMocks();
  mockedList.mockResolvedValue(page());
  mockedPermissionCatalog.mockResolvedValue(permissionCatalog);
  mockedDetails.mockResolvedValue(alice);
  mockedSearch.mockResolvedValue(page([alice], 1));
  mockedGroups.mockResolvedValue(['default', 'vip']);
  mockedAdjustQuota.mockResolvedValue();
  mockedCreate.mockResolvedValue();
  mockedDelete.mockResolvedValue();
  mockedManage.mockResolvedValue();
  mockedResetPasskey.mockResolvedValue();
  mockedResetTwoFactor.mockResolvedValue();
  mockedUpdate.mockResolvedValue();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('UserAdminView', () => {
  it('lists bounded users, paginates, and submits the complete server-side search contract', async () => {
    renderUsers();
    const user = userEvent.setup();

    expect(await screen.findByText('Alice Operator')).toBeTruthy();
    expect(screen.getByRole('table').getAttribute('aria-busy')).toBe('false');
    expect(screen.getByText('@alice · ID 7')).toBeTruthy();
    expect(screen.getByText('alice@example.test')).toBeTruthy();
    expect(screen.getAllByText('12 requests')).toHaveLength(2);
    expect(screen.getByRole('columnheader', { name: 'Created time' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Last login' })).toBeTruthy();
    expect(screen.getByRole('table').querySelectorAll('time')).toHaveLength(3);
    expect(screen.getByText('Never')).toBeTruthy();
    expect(screen.queryByText(/password|access_token|pat-must-not-render/i)).toBeNull();
    expect(mockedList).toHaveBeenCalledWith(1, 20, expect.any(AbortSignal));

    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedList).toHaveBeenLastCalledWith(2, 20, expect.any(AbortSignal)));

    const search = screen.getByRole('search', { name: 'Search users' });
    await user.type(within(search).getByLabelText('Username, email, or ID'), 'alice');
    await user.type(within(search).getByLabelText('Group'), 'vip');
    await user.selectOptions(within(search).getByLabelText('Role'), '1');
    await user.selectOptions(within(search).getByLabelText('Status'), '2');
    await user.selectOptions(within(search).getByLabelText('Sort by'), 'username');
    await user.selectOptions(within(search).getByLabelText('Order'), 'asc');
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));

    await waitFor(() => expect(mockedSearch).toHaveBeenLastCalledWith({
      keyword: 'alice',
      group: 'vip',
      role: 1,
      status: 2,
      sortBy: 'username',
      sortOrder: 'asc',
      page: 1,
      pageSize: 20,
    }, expect.any(AbortSignal)));

    await user.click(within(search).getByRole('button', { name: 'Clear filters' }));
    await waitFor(() => expect(mockedList).toHaveBeenLastCalledWith(1, 20, expect.any(AbortSignal)));
  });

  it('keeps account creation role-bounded and clears a rejected write-only password', async () => {
    const user = userEvent.setup();
    const ordinaryAdmin = renderUsers(10, 99);
    await screen.findByText('Alice Operator');
    await user.click(screen.getByRole('button', { name: 'Create user' }));
    const adminCreateForm = screen.getByRole('heading', { name: 'Create user' }).closest('form') as HTMLFormElement;
    expect(within(adminCreateForm).queryByRole('option', { name: 'Administrator' })).toBeNull();
    const peerRow = screen.getByText('Peer Admin').closest('tr') as HTMLTableRowElement;
    expect(within(peerRow).getByText('Protected')).toBeTruthy();
    const ordinaryUserRow = screen.getByText('Alice Operator').closest('tr') as HTMLTableRowElement;
    expect(within(ordinaryUserRow).queryByRole('button', { name: 'Permissions' })).toBeNull();
    expect(within(ordinaryUserRow).getByRole('button', { name: 'Bindings' })).toBeTruthy();
    expect(within(ordinaryUserRow).getByRole('button', { name: 'Subscriptions' })).toBeTruthy();
    ordinaryAdmin.unmount();

    mockedCreate.mockRejectedValueOnce(new Error('database detail with password hash'));
    renderUsers();
    await screen.findByText('Alice Operator');
    await user.click(screen.getByRole('button', { name: 'Create user' }));
    const rootCreateForm = screen.getByRole('heading', { name: 'Create user' }).closest('form') as HTMLFormElement;
    expect(within(rootCreateForm).getByRole('option', { name: 'Administrator' })).toBeTruthy();
    const rootPeerRow = screen.getByText('Peer Admin').closest('tr') as HTMLTableRowElement;
    expect(within(rootPeerRow).getByRole('button', { name: 'Permissions' })).toBeTruthy();
    await user.type(within(rootCreateForm).getByLabelText('Username'), 'new-user');
    await user.type(within(rootCreateForm).getByLabelText('Display name'), 'New User');
    const password = within(rootCreateForm).getByLabelText('Password') as HTMLInputElement;
    await user.type(password, 'password8');
    await user.selectOptions(within(rootCreateForm).getByLabelText('Role'), '10');
    const sensitive = await within(rootCreateForm).findByRole('checkbox', { name: /Edit sensitive channels/ });
    expect((sensitive as HTMLInputElement).checked).toBe(false);
    await user.click(sensitive);
    await user.click(within(rootCreateForm).getByRole('button', { name: 'Add user' }));

    await waitFor(() => expect(mockedCreate).toHaveBeenCalledWith({
      username: 'new-user',
      displayName: 'New User',
      password: 'password8',
      role: 10,
      adminPermissions: { channel: { read: true, sensitive_write: true } },
    }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to create user.');
    expect(password.value).toBe('');
    expect(screen.queryByText(/database detail|password hash/)).toBeNull();
  });

  it('edits safe profile fields, adjusts quota atomically, and applies status and role actions', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderUsers();
    const user = userEvent.setup();
    let row = (await screen.findByText('Alice Operator')).closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Edit' }));
    let panel = screen.getByRole('heading', { name: 'Edit user alice' }).closest('.user-subpanel') as HTMLElement;
    const displayName = await within(panel).findByLabelText('Display name');
    const group = within(panel).getByLabelText('Group');
    const remark = within(panel).getByLabelText('Remark');
    const password = within(panel).getByLabelText('New password (optional)') as HTMLInputElement;
    await user.clear(displayName);
    await user.type(displayName, 'Alice Updated');
    await user.clear(group);
    await user.type(group, 'vip');
    await user.clear(remark);
    await user.type(remark, 'updated note');
    await user.type(password, 'replacement8');
    await user.click(within(panel).getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledWith({
      id: 7,
      displayName: 'Alice Updated',
      group: 'vip',
      remark: 'updated note',
      password: 'replacement8',
    }));
    expect(window.confirm).toHaveBeenCalledWith('Reset the password for “alice” and revoke every active session?');
    expect(screen.queryByLabelText('New password (optional)')).toBeNull();

    row = screen.getByText('Alice Operator').closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Edit' }));
    panel = screen.getByRole('heading', { name: 'Edit user alice' }).closest('.user-subpanel') as HTMLElement;
    await within(panel).findByLabelText('Display name');
    const quotaForm = within(panel).getByRole('form', { name: 'Adjust quota for alice' });
    await user.selectOptions(within(quotaForm).getByLabelText('Quota operation'), 'subtract');
    await user.type(within(quotaForm).getByLabelText('Quota amount'), '25');
    await user.click(within(quotaForm).getByRole('button', { name: 'Adjust quota' }));
    await waitFor(() => expect(mockedAdjustQuota).toHaveBeenCalledWith(7, 'subtract', 25));

    row = screen.getByText('Alice Operator').closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(mockedManage).toHaveBeenCalledWith(7, 'disable'));
    await user.click(within(row).getByRole('button', { name: 'Promote' }));
    await waitFor(() => expect(mockedManage).toHaveBeenCalledWith(7, 'promote'));
    expect(window.confirm).toHaveBeenCalledWith('Promote user “alice” to administrator?');
  });

  it('fails closed when fresh edit details are unavailable and supports retry', async () => {
    mockedDetails.mockRejectedValueOnce(new Error('private database detail')).mockResolvedValueOnce(alice);
    renderUsers();
    const user = userEvent.setup();
    const row = (await screen.findByText('Alice Operator')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Edit' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load user details.');
    expect(screen.queryByText(/private database detail/)).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByLabelText('Display name')).toBeTruthy();
    expect(mockedDetails).toHaveBeenCalledTimes(2);
  });

  it('requires explicit confirmation for deletion and security-factor resets', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    renderUsers();
    const user = userEvent.setup();
    const row = (await screen.findByText('Alice Operator')).closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    expect(mockedDelete).not.toHaveBeenCalled();
    await user.click(within(row).getByRole('button', { name: 'Reset passkey' }));
    await waitFor(() => expect(mockedResetPasskey).toHaveBeenCalledWith(7));
    await user.click(within(row).getByRole('button', { name: 'Reset 2FA' }));
    await waitFor(() => expect(mockedResetTwoFactor).toHaveBeenCalledWith(7));
    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(mockedDelete).toHaveBeenCalledWith(7));
    expect(confirm).toHaveBeenCalledWith('Reset the passkey for “alice”?');
    expect(confirm).toHaveBeenCalledWith('Reset two-factor authentication for “alice”?');
    expect(confirm).toHaveBeenCalledWith('Delete user “alice”? Access and active sessions will be revoked.');
  });

  it('renders loading, empty, catalog-degraded, and redacted failure states', async () => {
    let resolveList: ((value: UserPage) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    mockedGroups.mockRejectedValueOnce(new Error('private option detail'));
    renderUsers();
    expect(screen.getByRole('status').textContent).toBe('Loading users…');
    await act(async () => resolveList?.(page([], 0)));
    expect(await screen.findByText('No users match these filters.')).toBeTruthy();
    expect(screen.getByText('Group suggestions are unavailable; manual values still work.')).toBeTruthy();
    cleanup();

    mockedList.mockRejectedValueOnce(new Error('SQL password leaked by server'));
    renderUsers();
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load users.');
    expect(screen.queryByText(/SQL password|leaked by server/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('does not request privileged data for a non-admin and ignores late responses after unmount', async () => {
    const unauthorized = renderUsers(1, 7);
    expect(screen.getByRole('alert').textContent).toBe('Administrator access is required.');
    expect(mockedList).not.toHaveBeenCalled();
    expect(mockedGroups).not.toHaveBeenCalled();
    unauthorized.unmount();

    let resolveList: ((value: UserPage) => void) | undefined;
    let resolveGroups: ((value: string[]) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    mockedGroups.mockImplementationOnce(() => new Promise((resolve) => { resolveGroups = resolve; }));
    const rendered = renderUsers();
    rendered.unmount();
    await act(async () => {
      resolveList?.(page([{ ...alice, displayName: 'Late User' }], 1));
      resolveGroups?.(['late-private-group']);
      await Promise.resolve();
    });
    expect(screen.queryByText(/Late User|late-private-group/)).toBeNull();
  });
});
