import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../shared/api/client';
import {
  ChatContractError,
  loadActiveChatKey,
  loadChatContext,
  parseChatStatusResponse,
  resolveChatURL,
} from './chat-api';

vi.mock('../../shared/api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);

beforeEach(() => vi.resetAllMocks());

describe('chat API contract', () => {
  it('parses only bounded safe single-entry presets', () => {
    const parsed = parseChatStatusResponse({
      success: true,
      data: {
        server_address: 'https://router.example.test',
        chats: [
          { 'Hosted chat': 'https://chat.example.test/?key={key}&base={address}' },
          { Desktop: 'clientapp://configure?data={cherryConfig}' },
          { Shortcut: 'fluentread' },
        ],
      },
    }, 'https://fallback.example.test');

    expect(parsed.serverAddress).toBe('https://router.example.test');
    expect(parsed.presets.map(({ id, name, type, requiresKey }) => ({ id, name, type, requiresKey }))).toEqual([
      { id: '0', name: 'Hosted chat', type: 'web', requiresKey: true },
      { id: '1', name: 'Desktop', type: 'custom-protocol', requiresKey: true },
      { id: '2', name: 'Shortcut', type: 'shortcut', requiresKey: false },
    ]);

    for (const chats of [
      [{ A: 'javascript:alert(1)' }],
      [{ A: 'https://one.example.test', B: 'https://two.example.test' }],
      [{ A: 'https://one.example.test' }, { A: 'https://two.example.test' }],
      Array.from({ length: 65 }, (_, index) => ({ [`P${index}`]: 'https://chat.example.test' })),
    ]) {
      expect(() => parseChatStatusResponse({ success: true, data: { chats } }, 'https://fallback.example.test'))
        .toThrow(ChatContractError);
    }
  });

  it('loads the exact public status contract with a response bound', async () => {
    mockedGet.mockResolvedValueOnce({
      data: { success: true, data: { server_address: '', chats: [] } },
    });
    await expect(loadChatContext(undefined, 'https://router.example.test')).resolves.toEqual({
      serverAddress: 'https://router.example.test',
      presets: [],
    });
    expect(mockedGet).toHaveBeenCalledWith('/status', expect.objectContaining({ maxContentLength: 524_288 }));
  });

  it('resolves key and encoded-client placeholders without duplicating the key prefix', () => {
    const hosted = {
      id: '0', name: 'Hosted', type: 'web' as const, requiresKey: true,
      url: 'https://chat.example.test/?key={key}&base={address}',
    };
    expect(resolveChatURL(hosted, 'sk-secret_key', 'https://router.example.test')).toBe(
      'https://chat.example.test/?key=sk-secret_key&base=https%3A%2F%2Frouter.example.test',
    );

    const desktop = {
      id: '1', name: 'Desktop', type: 'custom-protocol' as const, requiresKey: true,
      url: 'clientapp://configure?data={cherryConfig}',
    };
    const resolved = resolveChatURL(desktop, 'secret_key', 'https://router.example.test');
    const encoded = new URL(resolved!).searchParams.get('data');
    expect(JSON.parse(atob(decodeURIComponent(encoded!)))).toEqual({
      id: 'tokenrouter', baseUrl: 'https://router.example.test', apiKey: 'sk-secret_key',
    });
  });

  it('selects the first enabled token and discloses only that caller-owned key', async () => {
    mockedGet.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          page: 1, page_size: 50, total: 2,
          items: [{ id: 4, status: 2 }, { id: 9, status: 1 }],
        },
      },
    });
    mockedPost.mockResolvedValueOnce({ data: { success: true, data: { key: 'secret_key' } } });

    await expect(loadActiveChatKey()).resolves.toBe('sk-secret_key');
    expect(mockedGet).toHaveBeenCalledWith('/token/', expect.objectContaining({ params: { p: 1, page_size: 50 } }));
    expect(mockedPost).toHaveBeenCalledWith('/token/9/key', undefined, expect.objectContaining({ maxContentLength: 524_288 }));
  });
});
