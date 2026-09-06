// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getData, postData } from '../../shared/api/client';
import { ChannelsView } from './ChannelsView';
import type { ChannelAdminViewProps } from './ChannelAdminView';

vi.mock('react-i18next', () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});
vi.mock('../../shared/api/client', () => ({ api: { delete: vi.fn() }, getData: vi.fn(), postData: vi.fn(), putData: vi.fn() }));
vi.mock('./ChannelAdminView', () => ({
  ChannelAdminView: ({ canRead }: ChannelAdminViewProps) => canRead
    ? <h2>Channel management</h2>
    : <p role="alert">Your account does not have permission to view this page.</p>,
}));

const permissions = { canRead: true, canOperate: true, canWrite: true, canSensitiveWrite: true };
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(getData).mockResolvedValue([]);
  vi.mocked(postData).mockResolvedValue(undefined);
});
afterEach(cleanup);

describe('channel feature composition', () => {
  it('preserves channel read denial while retaining independently authorized prefill access', async () => {
    render(<ChannelsView {...permissions} canRead={false} />);
    expect(screen.getByRole('alert').textContent).toContain('does not have permission');
    expect(screen.queryByRole('button', { name: 'Abilities' })).toBeNull();
    expect(getData).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: 'Prefill' }));
    await waitFor(() => expect(getData).toHaveBeenCalledExactlyOnceWith('/prefill_group/', undefined));
  });

  it('loads abilities and prefill only when their panels are selected', async () => {
    render(<ChannelsView {...permissions} />);
    expect(screen.getByRole('heading', { name: 'Channel management' })).toBeTruthy();
    expect(getData).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: 'Abilities' }));
    await waitFor(() => expect(getData).toHaveBeenCalledExactlyOnceWith('/ability'));
    expect(screen.getByRole('heading', { name: 'Routing abilities' })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Prefill' }));
    await waitFor(() => expect(getData).toHaveBeenCalledWith('/prefill_group/', undefined));
    expect(vi.mocked(getData).mock.calls.map(([url]) => url)).toEqual(['/ability', '/prefill_group/']);
  });

  it('keeps the ability write control behind the channel write capability', async () => {
    const view = render(<ChannelsView {...permissions} canWrite={false} />);
    await userEvent.click(screen.getByRole('button', { name: 'Abilities' }));
    expect(screen.queryByRole('button', { name: 'Add ability' })).toBeNull();
    view.rerender(<ChannelsView {...permissions} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Model'), 'gpt-4.1');
    await user.type(screen.getByLabelText('Channel ID'), '7');
    await user.click(screen.getByRole('button', { name: 'Add ability' }));
    await waitFor(() => expect(postData).toHaveBeenCalledWith('/ability', {
      group: 'default', model: 'gpt-4.1', channel_id: 7, weight: 1,
    }));
  });
});
