// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadPublicContent } from './public-content-api';
import { PublicNotice, publicNoticeSignature } from './PublicNotice';

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('./public-content-api', () => ({ loadPublicContent: vi.fn() }));

const mockedContent = vi.mocked(loadPublicContent);

beforeEach(() => {
  const values = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      clear: () => values.clear(),
      getItem: (key: string) => values.get(key) ?? null,
      removeItem: (key: string) => values.delete(key),
      setItem: (key: string, value: string) => values.set(key, String(value)),
    },
  });
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe('PublicNotice', () => {
  it('exposes bounded loading and empty states without leaking a failed request', async () => {
    mockedContent.mockReturnValueOnce(new Promise(() => {}));
    const pending = render(<PublicNotice />);
    await userEvent.setup().click(screen.getByRole('button', { name: /System notice/ }));
    expect(screen.getByRole('status').textContent).toBe('Loading system notice…');
    pending.unmount();

    mockedContent.mockRejectedValueOnce(new Error('private upstream detail'));
    render(<PublicNotice />);
    await userEvent.setup().click(screen.getByRole('button', { name: /System notice/ }));
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load system notice.');
    expect(screen.queryByText(/private upstream detail/)).toBeNull();
  });

  it('combines notice and announcement unread state and renders the safe timeline', async () => {
    mockedContent.mockResolvedValue('# Service notice');
    render(
      <PublicNotice
        announcementsEnabled
        announcements={[
          {
            id: 7,
            type: 'warning',
            content: '**Maintenance**\n\n<script>unsafe()</script>',
            extra: '[Status](https://status.example.test)',
            publishDate: '2026-09-06T10:00:00Z',
          },
          {
            content: 'Earlier release',
            publishDate: '2026-09-05T10:00:00Z',
          },
        ]}
      />,
    );

    const toggle = screen.getByRole('button', { name: /System notice/ });
    await waitFor(() => expect(toggle.textContent).toContain('New 3'));
    await userEvent.setup().click(toggle);
    expect(await screen.findByRole('heading', { name: 'Service notice' })).toBeTruthy();
    expect(window.localStorage.getItem('tokenrouter.public_notice_read.v1')).toBe(
      publicNoticeSignature('# Service notice'),
    );

    await userEvent.setup().click(screen.getByRole('tab', { name: /Announcements/ }));
    expect(screen.getByText('Maintenance')).toBeTruthy();
    expect(screen.getByText('Earlier release')).toBeTruthy();
    expect(screen.queryByText('unsafe()')).toBeNull();
    expect(screen.getByRole('link', { name: 'Status' }).getAttribute('rel')).toBe('noopener noreferrer');
    expect(JSON.parse(window.localStorage.getItem('tokenrouter.public_announcements_read.v1') ?? '[]')).toHaveLength(2);
    await waitFor(() => expect(toggle.textContent).not.toContain('New'));
  });

  it('ignores malformed read storage and omits the disabled timeline tab', async () => {
    window.localStorage.setItem('tokenrouter.public_announcements_read.v1', '{invalid');
    mockedContent.mockResolvedValue('');
    render(<PublicNotice announcementsEnabled={false} announcements={[]} />);
    await userEvent.setup().click(screen.getByRole('button', { name: /System notice/ }));
    expect(await screen.findByText('No system notice is currently published.')).toBeTruthy();
    expect(screen.queryByRole('tab', { name: /Announcements/ })).toBeNull();
  });
});
