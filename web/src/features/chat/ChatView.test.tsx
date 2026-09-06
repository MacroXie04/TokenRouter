// @vitest-environment jsdom

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadActiveChatKey, loadChatContext, resolveChatURL } from './chat-api';
import { ChatView } from './ChatView';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('./chat-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./chat-api')>();
  return { ...actual, loadActiveChatKey: vi.fn(), loadChatContext: vi.fn(), resolveChatURL: vi.fn(actual.resolveChatURL) };
});

const mockedContext = vi.mocked(loadChatContext);
const mockedKey = vi.mocked(loadActiveChatKey);
const mockedResolve = vi.mocked(resolveChatURL);

beforeEach(() => {
  vi.resetAllMocks();
  mockedResolve.mockImplementation((preset, key, address) =>
    `https://chat.example.test/?key=${key}&base=${encodeURIComponent(address)}&preset=${preset.id}`);
});

afterEach(cleanup);

describe('ChatView', () => {
  it('redirects malformed chat identifiers before requesting configuration', async () => {
    const onNavigate = vi.fn();
    render(<ChatView presetId="not-a-number" onNavigate={onNavigate} />);
    await waitFor(() => expect(onNavigate).toHaveBeenCalledWith('/dashboard', true));
    expect(mockedContext).not.toHaveBeenCalled();
  });

  it('aborts an in-flight catalog request when the route unmounts', () => {
    let capturedSignal: AbortSignal | undefined;
    mockedContext.mockImplementation((signal) => {
      capturedSignal = signal;
      return new Promise(() => undefined);
    });
    const view = render(<ChatView presetId="0" onNavigate={vi.fn()} />);
    view.unmount();
    expect(capturedSignal?.aborted).toBe(true);
  });

  it('requires explicit confirmation before placing a key-bearing chat URL in an iframe', async () => {
    mockedContext.mockResolvedValue({
      serverAddress: 'https://router.example.test',
      presets: [{
        id: '0', name: 'Hosted chat', url: 'https://chat.example.test/?key={key}',
        type: 'web', requiresKey: true,
      }],
    });
    mockedKey.mockResolvedValue('sk-secret_key');

    render(<ChatView presetId="0" onNavigate={vi.fn()} />);
    expect(await screen.findByRole('heading', { name: 'Hosted chat', level: 1 })).toBeTruthy();
    expect(screen.queryByTitle('Chat preset: Hosted chat')).toBeNull();
    expect(await screen.findByText('This launcher will share one enabled API key with the configured chat application.')).toBeTruthy();

    await userEvent.setup().click(screen.getByRole('button', { name: 'Open chat' }));
    const frame = await screen.findByTitle('Chat preset: Hosted chat');
    expect(frame.getAttribute('src')).toContain('sk-secret_key');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin');
  });

  it('offers a retry and key-management escape hatch when no active key can be disclosed', async () => {
    mockedContext.mockResolvedValue({
      serverAddress: 'https://router.example.test',
      presets: [{
        id: '0', name: 'Hosted chat', url: 'https://chat.example.test/?key={key}',
        type: 'web', requiresKey: true,
      }],
    });
    mockedKey.mockRejectedValue(new Error('private upstream detail'));
    const onNavigate = vi.fn();
    render(<ChatView presetId="0" onNavigate={onNavigate} />);

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Unable to prepare this chat launcher.');
    expect(screen.queryByText('private upstream detail')).toBeNull();
    await userEvent.setup().click(within(alert).getByRole('button', { name: 'Manage API keys' }));
    expect(onNavigate).toHaveBeenCalledWith('/keys');
  });

  it('selects the first web preset for the compatibility route and handles an empty catalog', async () => {
    mockedContext.mockResolvedValueOnce({ serverAddress: 'https://router.example.test', presets: [] });
    render(<ChatView firstWeb onNavigate={vi.fn()} />);
    expect(await screen.findByText('No chat launchers are configured.')).toBeTruthy();

    cleanup();
    mockedContext.mockResolvedValueOnce({
      serverAddress: 'https://router.example.test',
      presets: [
        { id: '0', name: 'Desktop', url: 'clientapp://open', type: 'custom-protocol', requiresKey: false },
        { id: '1', name: 'Hosted', url: 'https://chat.example.test', type: 'web', requiresKey: false },
      ],
    });
    const onExternalNavigate = vi.fn();
    render(<ChatView firstWeb onNavigate={vi.fn()} onExternalNavigate={onExternalNavigate} />);
    expect(await screen.findByRole('heading', { name: 'Hosted', level: 1 })).toBeTruthy();
    expect(mockedKey).not.toHaveBeenCalled();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Open chat' }));
    expect(onExternalNavigate).toHaveBeenCalledWith(expect.stringContaining('preset=1'));
  });
});
