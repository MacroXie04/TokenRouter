// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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
  clearBuiltInUserBinding,
  loadCustomOAuthBindings,
  loadPermissionCatalog,
  loadUserBindingDetails,
  loadUserPermissionState,
  unbindCustomOAuth,
  updateUserPermissions,
  type ManagedUser,
  type PermissionCatalog,
  type UserBindingDetails,
} from './user-api';
import {
  UserBindingsDialog,
  UserPermissionDialog,
  UserSubscriptionsDialog,
} from './UserAdminDialogs';

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
    clearBuiltInUserBinding: vi.fn(),
    loadCustomOAuthBindings: vi.fn(),
    loadPermissionCatalog: vi.fn(),
    loadUserBindingDetails: vi.fn(),
    loadUserPermissionState: vi.fn(),
    unbindCustomOAuth: vi.fn(),
    updateUserPermissions: vi.fn(),
  };
});

vi.mock('../subscriptions/subscription-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../subscriptions/subscription-api')>();
  return {
    ...actual,
    createUserSubscription: vi.fn(),
    deleteUserSubscription: vi.fn(),
    invalidateUserSubscription: vi.fn(),
    listSubscriptionPlans: vi.fn(),
    listUserSubscriptions: vi.fn(),
    resetUserSubscriptionsByPlan: vi.fn(),
  };
});

const mockedClearBinding = vi.mocked(clearBuiltInUserBinding);
const mockedCustomBindings = vi.mocked(loadCustomOAuthBindings);
const mockedPermissionCatalog = vi.mocked(loadPermissionCatalog);
const mockedBindingDetails = vi.mocked(loadUserBindingDetails);
const mockedPermissionState = vi.mocked(loadUserPermissionState);
const mockedUnbindOAuth = vi.mocked(unbindCustomOAuth);
const mockedUpdatePermissions = vi.mocked(updateUserPermissions);
const mockedCreateSubscription = vi.mocked(createUserSubscription);
const mockedDeleteSubscription = vi.mocked(deleteUserSubscription);
const mockedInvalidateSubscription = vi.mocked(invalidateUserSubscription);
const mockedPlans = vi.mocked(listSubscriptionPlans);
const mockedSubscriptions = vi.mocked(listUserSubscriptions);
const mockedResetSubscription = vi.mocked(resetUserSubscriptionsByPlan);

const admin: ManagedUser = {
  id: 7,
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.test',
  quota: 1000,
  usedQuota: 25,
  requestCount: 2,
  group: 'default',
  status: 1,
  role: USER_ROLE_ADMIN,
  remark: '',
  createdAt: 1_700_000_000,
  lastLoginAt: 1_700_000_100,
};

const catalog: PermissionCatalog = {
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

const bindings: UserBindingDetails = {
  id: 7,
  username: 'alice',
  role: USER_ROLE_ADMIN,
  bindings: [
    { type: 'email', bound: true },
    { type: 'github', bound: true },
    { type: 'discord', bound: false },
    { type: 'oidc', bound: false },
    { type: 'wechat', bound: false },
    { type: 'telegram', bound: false },
    { type: 'linuxdo', bound: false },
  ],
};

const plan: ManagedSubscriptionPlan = {
  id: 3,
  title: 'Team',
  subtitle: '',
  priceAmount: '12.00',
  currency: 'USD',
  durationUnit: 'month',
  durationValue: 1,
  customSeconds: 0,
  enabled: true,
  sortOrder: 0,
  allowBalancePay: true,
  allowWalletOverflow: false,
  stripePriceId: '',
  creemProductId: '',
  waffoPancakeProductId: '',
  maxPurchasePerUser: 0,
  upgradeGroup: '',
  downgradeGroup: '',
  totalAmount: 10_000,
  quotaResetPeriod: 'monthly',
  quotaResetCustomSeconds: 0,
  createdAt: 1,
  updatedAt: 1,
};

const subscription: ManagedUserSubscription = {
  id: 9,
  userId: 7,
  planId: 3,
  amountTotal: 10_000,
  amountUsed: 250,
  startTime: 1,
  endTime: 2_000_000_000,
  status: 'active',
  source: 'admin',
  lastResetTime: 1,
  nextResetTime: 2,
  upgradeGroup: '',
  previousUserGroup: '',
  downgradeGroup: '',
  allowWalletOverflow: false,
  createdAt: 1,
  updatedAt: 1,
};

beforeEach(() => {
  vi.resetAllMocks();
  mockedPermissionCatalog.mockResolvedValue(catalog);
  mockedPermissionState.mockResolvedValue({
    id: 7,
    username: 'alice',
    role: USER_ROLE_ADMIN,
    permissions: { channel: { read: true, sensitive_write: false } },
  });
  mockedBindingDetails.mockResolvedValue(bindings);
  mockedCustomBindings.mockResolvedValue([{
    providerId: 42,
    providerName: 'Corporate SSO',
    providerSlug: 'corp-sso',
  }]);
  mockedClearBinding.mockResolvedValue();
  mockedUnbindOAuth.mockResolvedValue();
  mockedPlans.mockResolvedValue([plan]);
  mockedSubscriptions.mockResolvedValue([subscription]);
  mockedCreateSubscription.mockResolvedValue();
  mockedResetSubscription.mockResolvedValue({
    planId: 3,
    matchedCount: 1,
    resetCount: 1,
    userCount: 1,
    advanceResetTime: true,
  });
  mockedInvalidateSubscription.mockResolvedValue();
  mockedDeleteSubscription.mockResolvedValue();
  mockedUpdatePermissions.mockResolvedValue();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('UserPermissionDialog', () => {
  it('loads the root-only matrix, confirms changes, and saves a complete matrix', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    const user = userEvent.setup();
    render(<UserPermissionDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);

    const dialog = await screen.findByRole('dialog', { name: 'Permissions for alice' });
    const sensitive = await within(dialog).findByRole('checkbox', { name: /Edit sensitive channels/ });
    expect(mockedPermissionCatalog).toHaveBeenCalledWith(expect.any(AbortSignal));
    expect(mockedPermissionState).toHaveBeenCalledWith(7, catalog, expect.any(AbortSignal));
    expect((sensitive as HTMLInputElement).checked).toBe(false);
    await user.click(sensitive);
    await user.click(within(dialog).getByRole('button', { name: 'Save permissions' }));
    expect(mockedUpdatePermissions).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: 'Save permissions' }));
    await waitFor(() => expect(mockedUpdatePermissions).toHaveBeenCalledWith(7, catalog, {
      channel: { read: true, sensitive_write: true },
    }));
    expect((await within(dialog).findByRole('status')).textContent).toBe('Administrator permissions saved.');
    expect(confirm).toHaveBeenCalledWith('Save administrator permissions for “alice”?');
  });

  it('renders empty and redacted error states, retries, and ignores late work after unmount', async () => {
    mockedPermissionCatalog.mockRejectedValueOnce(new Error('database DSN and policy internals'));
    const user = userEvent.setup();
    const first = render(<UserPermissionDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load permissions.');
    expect(screen.queryByText(/database DSN|policy internals/)).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    await screen.findByRole('checkbox', { name: /Read channels/ });
    first.unmount();

    mockedPermissionCatalog.mockResolvedValueOnce({ resources: [], roles: [{
      key: 'admin', name: 'Admin', builtIn: true, superuser: false, grants: {},
    }] });
    mockedPermissionState.mockResolvedValueOnce({ id: 7, username: 'alice', role: USER_ROLE_ADMIN, permissions: {} });
    render(<UserPermissionDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect(await screen.findByText('No configurable permissions are available.')).toBeTruthy();
    cleanup();

    let resolveCatalog: ((value: PermissionCatalog) => void) | undefined;
    mockedPermissionCatalog.mockImplementationOnce(() => new Promise((resolve) => { resolveCatalog = resolve; }));
    const late = render(<UserPermissionDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect(screen.getByRole('status').textContent).toBe('Loading permissions…');
    late.unmount();
    await act(async () => {
      resolveCatalog?.(catalog);
      await Promise.resolve();
    });
    expect(screen.queryByText('Channel Management')).toBeNull();
  });

  it('does not request permission data without root authorization', () => {
    render(<UserPermissionDialog user={admin} authorized={false} operatorId={1} operatorRole={10} onClose={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toBe('Root access is required.');
    expect(mockedPermissionCatalog).not.toHaveBeenCalled();
    expect(mockedPermissionState).not.toHaveBeenCalled();
  });
});

describe('UserBindingsDialog', () => {
  it('shows only connection state, confirms built-in and custom unbinding, and can reveal unbound providers', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const user = userEvent.setup();
    render(<UserBindingsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    const dialog = await screen.findByRole('dialog', { name: 'Account bindings for alice' });

    expect(await within(dialog).findByText('Email')).toBeTruthy();
    expect(await within(dialog).findByText('Corporate SSO')).toBeTruthy();
    expect(within(dialog).queryByText('Discord')).toBeNull();
    expect(within(dialog).queryByText(/provider-subject|alice@example/)).toBeNull();
    await user.click(within(dialog).getByLabelText('Show unbound providers'));
    expect(within(dialog).getByText('Discord')).toBeTruthy();

    const emailRow = within(dialog).getByText('Email').closest('li') as HTMLLIElement;
    await user.click(within(emailRow).getByRole('button', { name: 'Unbind' }));
    await waitFor(() => expect(mockedClearBinding).toHaveBeenCalledWith(7, 'email'));
    const customRow = within(dialog).getByText('Corporate SSO').closest('li') as HTMLLIElement;
    await user.click(within(customRow).getByRole('button', { name: 'Unbind' }));
    await waitFor(() => expect(mockedUnbindOAuth).toHaveBeenCalledWith(7, 42));
    expect(window.confirm).toHaveBeenCalledWith('Unbind Email from “alice”?');
    expect(window.confirm).toHaveBeenCalledWith('Unbind Corporate SSO from “alice”?');
  });

  it('covers loading, empty, failure, unauthorized, and unmount-safe states', async () => {
    mockedBindingDetails.mockResolvedValueOnce({ ...bindings, bindings: bindings.bindings.map((item) => ({ ...item, bound: false })) });
    mockedCustomBindings.mockResolvedValueOnce([]);
    render(<UserBindingsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect(screen.getByRole('status').textContent).toBe('Loading account bindings…');
    expect(await screen.findByText('This user has no account bindings.')).toBeTruthy();
    cleanup();

    mockedBindingDetails.mockRejectedValueOnce(new Error('private OAuth subject'));
    render(<UserBindingsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load account bindings.');
    expect(screen.queryByText(/private OAuth subject/)).toBeNull();
    cleanup();

    mockedBindingDetails.mockClear();
    mockedCustomBindings.mockClear();
    render(<UserBindingsDialog user={admin} authorized={false} operatorId={1} operatorRole={10} onClose={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toBe('Administrator access is required.');
    expect(mockedBindingDetails).not.toHaveBeenCalled();
    cleanup();

    let resolveDetails: ((value: UserBindingDetails) => void) | undefined;
    mockedBindingDetails.mockImplementationOnce(() => new Promise((resolve) => { resolveDetails = resolve; }));
    const rendered = render(<UserBindingsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    rendered.unmount();
    await act(async () => {
      resolveDetails?.(bindings);
      await Promise.resolve();
    });
    expect(screen.queryByText('Corporate SSO')).toBeNull();
  });
});

describe('UserSubscriptionsDialog', () => {
  it('loads records and confirms grant, reset, invalidate, and permanent deletion', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const user = userEvent.setup();
    render(<UserSubscriptionsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    const dialog = await screen.findByRole('dialog', { name: 'Subscriptions for alice' });
    expect(await within(dialog).findByText('250 / 10,000')).toBeTruthy();

    await user.selectOptions(within(dialog).getByLabelText('Subscription plan'), '3');
    await user.click(within(dialog).getByRole('button', { name: 'Grant subscription' }));
    await waitFor(() => expect(mockedCreateSubscription).toHaveBeenCalledWith(7, 3));

    const advanceResetTime = within(dialog).getByRole('checkbox', { name: 'Advance next reset time' });
    expect((advanceResetTime as HTMLInputElement).checked).toBe(true);
    await user.click(advanceResetTime);
    await user.click(within(dialog).getByRole('button', { name: 'Reset quota' }));
    await waitFor(() => expect(mockedResetSubscription).toHaveBeenCalledWith(7, 3, false));
    await user.click(within(dialog).getByRole('button', { name: 'Invalidate' }));
    await waitFor(() => expect(mockedInvalidateSubscription).toHaveBeenCalledWith(9));
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(mockedDeleteSubscription).toHaveBeenCalledWith(9));

    expect(window.confirm).toHaveBeenCalledWith('Grant “Team” to “alice”?');
    expect(window.confirm).toHaveBeenCalledWith('Reset active “Team” subscriptions for “alice”?');
    expect(window.confirm).toHaveBeenCalledWith('Invalidate subscription #9 for “alice”?');
    expect(window.confirm).toHaveBeenCalledWith('Permanently delete subscription #9 for “alice”?');
  });

  it('renders loading, empty, redacted failure, unauthorized, and unmount-safe states', async () => {
    mockedPlans.mockResolvedValueOnce([]);
    mockedSubscriptions.mockResolvedValueOnce([]);
    render(<UserSubscriptionsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect(screen.getByRole('status').textContent).toBe('Loading subscriptions…');
    expect(await screen.findByText('No subscription records.')).toBeTruthy();
    expect(screen.getByRole('option', { name: 'No plans available' })).toBeTruthy();
    cleanup();

    mockedSubscriptions.mockRejectedValueOnce(new Error('SQL subscription detail'));
    render(<UserSubscriptionsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load subscriptions.');
    expect(screen.queryByText(/SQL subscription detail/)).toBeNull();
    cleanup();

    mockedPlans.mockClear();
    mockedSubscriptions.mockClear();
    render(<UserSubscriptionsDialog user={admin} authorized={false} operatorId={1} operatorRole={10} onClose={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toBe('Administrator access is required.');
    expect(mockedPlans).not.toHaveBeenCalled();
    expect(mockedSubscriptions).not.toHaveBeenCalled();
    cleanup();

    let resolvePlans: ((value: ManagedSubscriptionPlan[]) => void) | undefined;
    mockedPlans.mockImplementationOnce(() => new Promise((resolve) => { resolvePlans = resolve; }));
    const rendered = render(<UserSubscriptionsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    rendered.unmount();
    await act(async () => {
      resolvePlans?.([plan]);
      await Promise.resolve();
    });
    expect(screen.queryByText('Team')).toBeNull();
  });
});

describe('fresh target authorization', () => {
  it('stops every privileged workflow when the target is no longer below the operator', async () => {
    mockedPermissionState.mockResolvedValueOnce({
      id: 7,
      username: 'alice',
      role: 100,
      permissions: { channel: { read: true, sensitive_write: true } },
    });
    render(<UserPermissionDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load permissions.');
    expect(mockedUpdatePermissions).not.toHaveBeenCalled();
    cleanup();

    mockedBindingDetails.mockClear();
    mockedCustomBindings.mockClear();
    mockedBindingDetails.mockResolvedValueOnce({ ...bindings, role: 100 });
    render(<UserBindingsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load account bindings.');
    expect(mockedCustomBindings).not.toHaveBeenCalled();
    cleanup();

    mockedBindingDetails.mockClear();
    mockedPlans.mockClear();
    mockedSubscriptions.mockClear();
    mockedBindingDetails.mockResolvedValueOnce({ ...bindings, role: 100 });
    render(<UserSubscriptionsDialog user={admin} authorized operatorId={1} operatorRole={100} onClose={vi.fn()} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load subscriptions.');
    expect(mockedPlans).not.toHaveBeenCalled();
    expect(mockedSubscriptions).not.toHaveBeenCalled();
  });
});
