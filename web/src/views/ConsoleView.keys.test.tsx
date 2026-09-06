// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, getData, type User } from '../api';
import { ConsoleView } from './ConsoleView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/g,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('../features/keys/RelayTokenManager', () => ({
  RelayTokenManager: () => <section aria-label="relay-token-feature">Token feature</section>,
}));

vi.mock('../api', () => ({
  api: { post: vi.fn(), put: vi.fn(), delete: vi.fn() },
  getData: vi.fn(),
  postData: vi.fn(),
  putData: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe('ConsoleView key route integration', () => {
  it('renders the focused relay-token feature without loading unrelated dashboard resources', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    const onLogout = vi.fn();
    const user = { id: 4, username: 'owner', display_name: 'Owner' } as User;

    render(<ConsoleView user={user} keysOnly onLogout={onLogout} />);

    expect(screen.getByRole('region', { name: 'relay-token-feature' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Profile' })).toBeNull();
    expect(vi.mocked(getData)).not.toHaveBeenCalled();

    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign out' }));
    expect(api.post).toHaveBeenCalledWith('/user/auth/logout');
    expect(onLogout).toHaveBeenCalledTimes(1);
  });
});
