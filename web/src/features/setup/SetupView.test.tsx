// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { postData } from '../../api';
import { SetupView } from './SetupView';
import { parseSetupStatus } from './setup';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, values?: Record<string, number>) => (
    values ? key.replace('{{current}}', String(values.current)).replace('{{total}}', String(values.total)) : key
  ) }),
}));

vi.mock('../../api', () => ({ postData: vi.fn() }));

const mockedPostData = vi.mocked(postData);

function status(overrides: Record<string, unknown> = {}) {
  return parseSetupStatus({
    setup_required: true,
    status: false,
    root_init: false,
    database_type: 'sqlite',
    SelfUseModeEnabled: false,
    DemoSiteEnabled: false,
    site_name: 'TokenRouter',
    version: 'test',
    ...overrides,
  });
}

afterEach(() => {
  cleanup();
  mockedPostData.mockReset();
});

describe('SetupView', () => {
  it('walks the accessible four-step flow, validates credentials, and submits the exact personal payload', async () => {
    mockedPostData.mockResolvedValueOnce(undefined);
    const onDone = vi.fn();
    render(<SetupView status={status()} onDone={onDone} />);
    const user = userEvent.setup();

    expect(screen.getByRole('navigation', { name: 'Setup progress' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Database connection' })).toBeTruthy();
    expect(screen.getByText('sqlite')).toBeTruthy();
    expect(screen.getByText(/persistent storage/)).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByRole('heading', { name: 'Administrator account' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByRole('alert').textContent).toBe('Username must be 3 to 12 characters.');

    await user.type(screen.getByLabelText('Administrator username'), '  root-user  ');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.type(screen.getByLabelText('Confirm password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Next' }));

    expect(screen.getByRole('group', { name: 'How will you use TokenRouter?' })).toBeTruthy();
    await user.click(screen.getByRole('radio', { name: 'Personal use' }));
    await user.click(screen.getByRole('button', { name: 'Next' }));

    expect(screen.getByRole('heading', { name: 'Ready to initialize' })).toBeTruthy();
    expect(screen.getByText('root-user')).toBeTruthy();
    expect(screen.getByText('Personal use')).toBeTruthy();
    expect(screen.queryByText('password1')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Initialize system' }));
    await waitFor(() => expect(mockedPostData).toHaveBeenCalledWith('/setup', {
      username: 'root-user',
      password: 'password1',
      confirmPassword: 'password1',
      SelfUseModeEnabled: true,
      DemoSiteEnabled: false,
    }));
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it('keeps an existing root, prefills the persisted mode, and omits all credentials', async () => {
    mockedPostData.mockResolvedValueOnce(undefined);
    const onDone = vi.fn();
    render(<SetupView status={status({ root_init: true, DemoSiteEnabled: true })} onDone={onDone} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByRole('status').textContent).toContain('An administrator already exists');
    expect(screen.queryByLabelText('Administrator username')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect((screen.getByRole('radio', { name: 'Demo site' }) as HTMLInputElement).checked).toBe(true);
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('Existing account')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Initialize system' }));

    await waitFor(() => expect(mockedPostData).toHaveBeenCalledWith('/setup', {
      SelfUseModeEnabled: false,
      DemoSiteEnabled: true,
    }));
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it('redacts request failures, re-enables controls, and prevents duplicate in-flight submissions', async () => {
    let rejectRequest: ((reason: Error) => void) | undefined;
    mockedPostData.mockReturnValueOnce(new Promise((_, reject) => { rejectRequest = reject; }));
    const onDone = vi.fn();
    render(<SetupView status={status({ root_init: true })} onDone={onDone} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: 'Next' }));
    await user.click(screen.getByRole('button', { name: 'Next' }));
    await user.click(screen.getByRole('button', { name: 'Next' }));
    const submit = screen.getByRole('button', { name: 'Initialize system' });
    await user.click(submit);
    expect(mockedPostData).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: 'Initializing…' }).hasAttribute('disabled')).toBe(true);
    await user.click(screen.getByRole('button', { name: 'Initializing…' }));
    expect(mockedPostData).toHaveBeenCalledTimes(1);

    rejectRequest?.(new Error('database-password=super-secret'));
    expect((await screen.findByRole('alert')).textContent).toBe('Initialization failed. Please try again.');
    expect(screen.queryByText(/super-secret/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Initialize system' }).hasAttribute('disabled')).toBe(false);
    expect(onDone).not.toHaveBeenCalled();
  });

  it('supports backwards navigation without losing entered values', async () => {
    render(<SetupView status={status()} onDone={vi.fn()} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    await user.type(screen.getByLabelText('Administrator username'), 'root-user');
    await user.type(screen.getByLabelText('Password'), 'password1');
    await user.type(screen.getByLabelText('Confirm password'), 'password1');
    await user.click(screen.getByRole('button', { name: 'Next' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect((screen.getByLabelText('Administrator username') as HTMLInputElement).value).toBe('root-user');
  });
});

