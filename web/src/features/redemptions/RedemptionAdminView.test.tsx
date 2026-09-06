// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  REDEMPTION_STATUS_DISABLED,
  REDEMPTION_STATUS_ENABLED,
  REDEMPTION_STATUS_USED,
  createRedemptions,
  deleteInvalidRedemptions,
  deleteRedemption,
  getRedemptionCode,
  getRedemptionForEdit,
  listRedemptions,
  searchRedemptions,
  updateRedemption,
  updateRedemptionStatus,
  type ManagedRedemption,
  type RedemptionPage,
} from './redemption-api';
import { RedemptionAdminView } from './RedemptionAdminView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./redemption-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./redemption-api')>();
  return {
    ...actual,
    createRedemptions: vi.fn(),
    deleteInvalidRedemptions: vi.fn(),
    deleteRedemption: vi.fn(),
    getRedemptionCode: vi.fn(),
    getRedemptionForEdit: vi.fn(),
    listRedemptions: vi.fn(),
    searchRedemptions: vi.fn(),
    updateRedemption: vi.fn(),
    updateRedemptionStatus: vi.fn(),
  };
});

const mockedCreate = vi.mocked(createRedemptions);
const mockedDeleteInvalid = vi.mocked(deleteInvalidRedemptions);
const mockedDelete = vi.mocked(deleteRedemption);
const mockedGetCode = vi.mocked(getRedemptionCode);
const mockedGetForEdit = vi.mocked(getRedemptionForEdit);
const mockedList = vi.mocked(listRedemptions);
const mockedSearch = vi.mocked(searchRedemptions);
const mockedUpdate = vi.mocked(updateRedemption);
const mockedStatus = vi.mocked(updateRedemptionStatus);
const clipboardWrite = vi.fn();

const enabled: ManagedRedemption = {
  id: 7,
  userId: 2,
  name: 'Launch grant',
  maskedCode: '0123••••••••cdef',
  status: REDEMPTION_STATUS_ENABLED,
  quota: 500,
  createdTime: 1_780_000_000,
  redeemedTime: 0,
  expiredTime: 0,
  usedUserId: 0,
};

const disabled: ManagedRedemption = {
  ...enabled,
  id: 8,
  name: 'Paused grant',
  maskedCode: 'abcd••••••••1234',
  status: REDEMPTION_STATUS_DISABLED,
};

const used: ManagedRedemption = {
  ...enabled,
  id: 9,
  name: 'Redeemed grant',
  maskedCode: 'aaaa••••••••bbbb',
  status: REDEMPTION_STATUS_USED,
  redeemedTime: 1_780_100_000,
  usedUserId: 20,
};

const expired: ManagedRedemption = {
  ...enabled,
  id: 10,
  name: 'Expired grant',
  maskedCode: 'bbbb••••••••cccc',
  expiredTime: 1,
};

function page(items: ManagedRedemption[] = [enabled, disabled, used, expired], total = 21): RedemptionPage {
  return { items, total, page: 1, pageSize: 20 };
}

beforeEach(() => {
  vi.resetAllMocks();
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: clipboardWrite },
  });
  clipboardWrite.mockResolvedValue(undefined);
  mockedList.mockResolvedValue(page());
  mockedSearch.mockResolvedValue(page([enabled], 1));
  mockedCreate.mockResolvedValue(['11111111111111111111111111111111', '22222222222222222222222222222222']);
  mockedDeleteInvalid.mockResolvedValue(3);
  mockedDelete.mockResolvedValue();
  mockedGetCode.mockResolvedValue('0123456789abcdef0123456789abcdef');
  mockedGetForEdit.mockResolvedValue(enabled);
  mockedUpdate.mockResolvedValue(enabled);
  mockedStatus.mockResolvedValue({ ...enabled, status: REDEMPTION_STATUS_DISABLED });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('RedemptionAdminView', () => {
  it('lists only masked codes, paginates, and submits the complete server-side search contract', async () => {
    render(<RedemptionAdminView operatorRole={10} />);
    const user = userEvent.setup();

    expect(await screen.findByText('Launch grant')).toBeTruthy();
    expect(screen.getByText('0123••••••••cdef')).toBeTruthy();
    expect(screen.queryByText('0123456789abcdef0123456789abcdef')).toBeNull();
    expect(screen.getByRole('table').getAttribute('aria-busy')).toBe('false');

    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedList).toHaveBeenLastCalledWith(2, 20, expect.any(AbortSignal)));

    const search = screen.getByRole('search', { name: 'Search redemption codes' });
    await user.type(within(search).getByLabelText('Name or ID'), ' launch ');
    await user.selectOptions(within(search).getByLabelText('Status'), 'expired');
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));
    await waitFor(() => expect(mockedSearch).toHaveBeenLastCalledWith({
      keyword: 'launch',
      status: 'expired',
      page: 1,
      pageSize: 20,
    }, expect.any(AbortSignal)));

    await user.click(within(search).getByRole('button', { name: 'Clear filters' }));
    await waitFor(() => expect(mockedList).toHaveBeenLastCalledWith(1, 20, expect.any(AbortSignal)));
  });

  it('creates bounded batches and treats returned secrets as one-time material', async () => {
    render(<RedemptionAdminView operatorRole={100} />);
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: clipboardWrite },
    });
    await screen.findByText('Launch grant');
    await user.click(screen.getByRole('button', { name: 'Create codes' }));
    const form = screen.getByRole('heading', { name: 'Create redemption codes' }).closest('form') as HTMLFormElement;
    await user.type(within(form).getByLabelText('Name'), 'September launch');
    await user.clear(within(form).getByLabelText('Quota per code'));
    await user.type(within(form).getByLabelText('Quota per code'), '750');
    await user.clear(within(form).getByLabelText('Number of codes'));
    await user.type(within(form).getByLabelText('Number of codes'), '2');
    await user.click(within(form).getByRole('button', { name: 'Create codes' }));

    await waitFor(() => expect(mockedCreate).toHaveBeenCalledWith({
      name: 'September launch',
      quota: 750,
      expiredTime: 0,
      count: 2,
    }));
    const secretPanel = await screen.findByRole('heading', { name: 'New redemption codes — shown once' });
    const panel = secretPanel.closest('section') as HTMLElement;
    expect(within(panel).getByText('11111111111111111111111111111111')).toBeTruthy();
    await user.click(within(panel).getByRole('button', { name: 'Copy all' }));
    await waitFor(() => expect(clipboardWrite).toHaveBeenCalledWith(
      '11111111111111111111111111111111\n22222222222222222222222222222222',
    ));
    await user.click(within(panel).getByRole('button', { name: 'Dismiss and hide' }));
    expect(screen.queryByText('11111111111111111111111111111111')).toBeNull();
  });

  it('fetches a fresh safe row before edit and supports status transitions', async () => {
    render(<RedemptionAdminView operatorRole={10} />);
    const user = userEvent.setup();
    let row = (await screen.findByText('Launch grant')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Edit' }));
    await waitFor(() => expect(mockedGetForEdit).toHaveBeenCalledWith(7));
    const editForm = await screen.findByRole('form', { name: 'Edit redemption code 7' });
    await user.clear(within(editForm).getByLabelText('Name'));
    await user.type(within(editForm).getByLabelText('Name'), 'Updated grant');
    await user.clear(within(editForm).getByLabelText('Quota'));
    await user.type(within(editForm).getByLabelText('Quota'), '900');
    await user.click(within(editForm).getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledWith(7, {
      name: 'Updated grant',
      quota: 900,
      expiredTime: 0,
      count: 1,
    }));

    row = screen.getByText('Launch grant').closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(mockedStatus).toHaveBeenCalledWith(7, REDEMPTION_STATUS_DISABLED));

    const disabledRow = screen.getByText('Paused grant').closest('tr') as HTMLTableRowElement;
    expect((within(disabledRow).getByRole('button', { name: 'Edit' }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(disabledRow).getByRole('button', { name: 'Enable' })).toBeTruthy();
    const usedRow = screen.getByText('Redeemed grant').closest('tr') as HTMLTableRowElement;
    expect((within(usedRow).getByRole('button', { name: 'Edit' }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(usedRow).queryByRole('button', { name: /Enable|Disable/ })).toBeNull();
    const expiredRow = screen.getByText('Expired grant').closest('tr') as HTMLTableRowElement;
    expect((within(expiredRow).getByRole('button', { name: 'Edit' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('copies a full code only after an explicit per-row action and never renders it', async () => {
    render(<RedemptionAdminView operatorRole={10} />);
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: clipboardWrite },
    });
    const row = (await screen.findByText('Launch grant')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Copy code' }));
    await waitFor(() => expect(mockedGetCode).toHaveBeenCalledWith(7));
    await waitFor(() => expect(clipboardWrite).toHaveBeenCalledWith('0123456789abcdef0123456789abcdef'));
    expect(screen.queryByText('0123456789abcdef0123456789abcdef')).toBeNull();
    expect(screen.getByRole('status').textContent).toBe('Redemption code copied.');
  });

  it('requires confirmation for individual and invalid-code deletion', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    render(<RedemptionAdminView operatorRole={10} />);
    const user = userEvent.setup();
    const row = (await screen.findByText('Launch grant')).closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    expect(mockedDelete).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Delete invalid' }));
    await waitFor(() => expect(mockedDeleteInvalid).toHaveBeenCalledOnce());
    expect(confirm).toHaveBeenNthCalledWith(1, 'Delete redemption “Launch grant” (ID 7)? This cannot be undone.');
    expect(confirm).toHaveBeenNthCalledWith(2, 'Delete all used, disabled, and expired redemption codes? This cannot be undone.');
    expect(screen.getByRole('status').textContent).toBe('3 invalid redemption codes deleted.');
  });

  it('renders loading, empty, and redacted retryable failure states', async () => {
    let resolveList: ((value: RedemptionPage) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    render(<RedemptionAdminView operatorRole={10} />);
    expect(screen.getByRole('status').textContent).toBe('Loading redemption codes…');
    await act(async () => resolveList?.(page([], 0)));
    expect(await screen.findByText('No redemption codes match these filters.')).toBeTruthy();
    cleanup();

    mockedList.mockRejectedValueOnce(new Error(`database failure leaked ${'a'.repeat(32)}`));
    render(<RedemptionAdminView operatorRole={10} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load redemption codes.');
    expect(screen.queryByText(/database failure|leaked/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('does not request privileged data for non-admins and ignores late responses after unmount', async () => {
    const unauthorized = render(<RedemptionAdminView operatorRole={1} />);
    expect(screen.getByRole('alert').textContent).toBe('Administrator access is required.');
    expect(mockedList).not.toHaveBeenCalled();
    unauthorized.unmount();

    let resolveList: ((value: RedemptionPage) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    const rendered = render(<RedemptionAdminView operatorRole={10} />);
    rendered.unmount();
    await act(async () => {
      resolveList?.(page([{ ...enabled, name: 'Late private grant' }], 1));
      await Promise.resolve();
    });
    expect(screen.queryByText('Late private grant')).toBeNull();
  });
});
