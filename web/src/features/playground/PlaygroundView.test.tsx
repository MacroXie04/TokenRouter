// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { User } from '../../api';
import { PlaygroundView } from './PlaygroundView';
import type { PlaygroundFetch } from './playground-api';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

const USER: User = {
  id: 7,
  username: 'reader',
  display_name: 'Reader',
  role: 1,
  group: 'default',
  quota: 100,
  used_quota: 0,
  request_count: 0,
};

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function sseResponse(events: unknown[]): Response {
  const body = events.map((event) => event === '[DONE]'
    ? 'data: [DONE]\n\n'
    : `data: ${JSON.stringify(event)}\n\n`).join('');
  return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } });
}

function catalogFetcher(streamEvents: unknown[] = [
  { choices: [{ delta: { content: 'Hello' } }] },
  '[DONE]',
]): ReturnType<typeof vi.fn<PlaygroundFetch>> {
  return vi.fn<PlaygroundFetch>(async (input) => {
    const url = String(input);
    if (url === '/api/user/self/groups') {
      return jsonResponse({
        success: true,
        data: {
          default: { ratio: 1, desc: 'Standard routing' },
          vip: { ratio: 2, desc: 'Priority routing' },
        },
      });
    }
    if (url.startsWith('/api/user/models?')) {
      return jsonResponse({ success: true, data: ['gpt-4o-mini', 'gpt-4o'] });
    }
    if (url === '/pg/chat/completions') return sseResponse(streamEvents);
    throw new Error(`unexpected request: ${url}`);
  });
}

function deferred<Value>() {
  let resolve!: (value: Value) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<Value>((accept, deny) => {
    resolve = accept;
    reject = deny;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  vi.restoreAllMocks();
});

afterEach(cleanup);

describe('PlaygroundView', () => {
  it('fails closed for an invalid authenticated-user context', async () => {
    const fetcher = vi.fn<PlaygroundFetch>();
    const onNavigate = vi.fn();
    render(<PlaygroundView user={{ ...USER, id: 0 }} onNavigate={onNavigate} fetcher={fetcher} />);

    expect(screen.getByText('Returning to sign in…')).toBeTruthy();
    await waitFor(() => expect(onNavigate).toHaveBeenCalledWith('/sign-in'));
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('honors the reference module switch without loading private catalogs', async () => {
    const fetcher = vi.fn<PlaygroundFetch>();
    const onNavigate = vi.fn();
    render(
      <PlaygroundView
        user={USER}
        moduleEnabled={false}
        onNavigate={onNavigate}
        fetcher={fetcher}
      />,
    );

    expect(screen.getByText('Returning to dashboard…')).toBeTruthy();
    await waitFor(() => expect(onNavigate).toHaveBeenCalledWith('/dashboard'));
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('loads caller-scoped options and sends a session-only bounded conversation', async () => {
    const unsafeLookingText = '<img src=x onerror=alert(1)> & still text';
    const fetcher = catalogFetcher([
      { choices: [{ delta: { reasoning_content: 'brief thought', content: unsafeLookingText } }] },
      '[DONE]',
    ]);
    render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);

    const groupSelect = await screen.findByLabelText('Group');
    const modelSelect = await screen.findByLabelText('Model');
    await waitFor(() => expect((modelSelect as HTMLSelectElement).value).toBe('gpt-4o-mini'));
    expect((groupSelect as HTMLSelectElement).value).toBe('default');
    expect(screen.getByText('Standard routing')).toBeTruthy();
    expect(screen.getByText('Your account · Reader')).toBeTruthy();

    await userEvent.setup().type(screen.getByLabelText('Message'), 'Say hello');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Send message' }));

    expect(await screen.findByText(unsafeLookingText)).toBeTruthy();
    expect(screen.queryByRole('img')).toBeNull();
    expect(screen.getByText('brief thought')).toBeTruthy();
    const postCall = fetcher.mock.calls.find(([input]) => String(input) === '/pg/chat/completions');
    expect(postCall).toBeTruthy();
    const init = postCall?.[1] as RequestInit;
    expect(init.credentials).toBe('same-origin');
    expect(JSON.stringify(init)).not.toContain('Authorization');
    expect(JSON.parse(String(init.body))).toEqual({
      model: 'gpt-4o-mini',
      group: 'default',
      messages: [{ role: 'user', content: 'Say hello' }],
      stream: true,
      temperature: 0.7,
      top_p: 1,
      max_tokens: 4096,
      frequency_penalty: 0,
      presence_penalty: 0,
    });

    await userEvent.setup().click(screen.getByRole('button', { name: 'Clear conversation' }));
    expect(screen.getByText('No playground messages yet.')).toBeTruthy();
  });

  it('includes system instructions and completed conversation context in the next request', async () => {
    const fetcher = catalogFetcher();
    render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);
    await screen.findByRole('option', { name: 'gpt-4o-mini' });
    await userEvent.setup().type(screen.getByLabelText('System instructions (optional)'), 'Be concise.');
    const composer = screen.getByLabelText('Message');
    await userEvent.setup().type(composer, 'First');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Send message' }));
    await screen.findByText('Hello');
    await userEvent.setup().type(composer, 'Second');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Send message' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([input]) => String(input) === '/pg/chat/completions')).toHaveLength(2));

    const posts = fetcher.mock.calls.filter(([input]) => String(input) === '/pg/chat/completions');
    const second = JSON.parse(String((posts[1][1] as RequestInit).body));
    expect(second.messages).toEqual([
      { role: 'system', content: 'Be concise.' },
      { role: 'user', content: 'First' },
      { role: 'assistant', content: 'Hello' },
      { role: 'user', content: 'Second' },
    ]);
  });

  it('renders request failures without exposing upstream response details', async () => {
    const privateDetail = 'provider-secret-debug-payload';
    const fetcher = catalogFetcher();
    fetcher.mockImplementation(async (input) => {
      const url = String(input);
      if (url === '/api/user/self/groups') {
        return jsonResponse({ success: true, data: { default: { ratio: 1, desc: '' } } });
      }
      if (url.startsWith('/api/user/models?')) {
        return jsonResponse({ success: true, data: ['model-a'] });
      }
      if (url === '/pg/chat/completions') {
        return jsonResponse({ error: { message: privateDetail } }, 502);
      }
      throw new Error(`unexpected request: ${url}`);
    });
    render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);
    await screen.findByRole('option', { name: 'model-a' });
    await userEvent.setup().type(screen.getByLabelText('Message'), 'Hello');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Send message' }));

    expect(await screen.findByText('Unable to complete the playground request.')).toBeTruthy();
    expect(screen.getByText('Failed')).toBeTruthy();
    expect(screen.queryByText(privateDetail)).toBeNull();
  });

  it('shows loading, redacted failure, retry, and empty group states', async () => {
    let calls = 0;
    const fetcher = vi.fn<PlaygroundFetch>(async (input) => {
      if (String(input) !== '/api/user/self/groups') throw new Error('unexpected request');
      calls += 1;
      if (calls === 1) return jsonResponse({ private_detail: 'database-password' }, 500);
      return jsonResponse({ success: true, data: {} });
    });
    render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);

    expect(screen.getByText('Loading playground groups…')).toBeTruthy();
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Unable to load playground groups.');
    expect(screen.queryByText('database-password')).toBeNull();
    await userEvent.setup().click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('No playground groups are available for this account.')).toBeTruthy();
  });

  it('aborts an obsolete model request and ignores its late result after a group change', async () => {
    const firstModels = deferred<Response>();
    const defaultSignal: { current?: AbortSignal } = {};
    const fetcher = vi.fn<PlaygroundFetch>(async (input, init) => {
      const url = String(input);
      if (url === '/api/user/self/groups') {
        return jsonResponse({
          success: true,
          data: { default: { ratio: 1, desc: '' }, vip: { ratio: 2, desc: '' } },
        });
      }
      if (url === '/api/user/models?group=default') {
        defaultSignal.current = init?.signal as AbortSignal;
        return firstModels.promise;
      }
      if (url === '/api/user/models?group=vip') {
        return jsonResponse({ success: true, data: ['vip-model'] });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);
    const groups = await screen.findByLabelText('Group');
    await userEvent.setup().selectOptions(groups, 'vip');
    expect(await screen.findByRole('option', { name: 'vip-model' })).toBeTruthy();
    expect(defaultSignal.current?.aborted).toBe(true);

    firstModels.resolve(jsonResponse({ success: true, data: ['stale-model'] }));
    await Promise.resolve();
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe('vip-model');
    expect(screen.queryByRole('option', { name: 'stale-model' })).toBeNull();
  });

  it('stops an in-flight generation, aborts transport, and rejects late state updates', async () => {
    const requestSignal: { current?: AbortSignal } = {};
    const fetcher = vi.fn<PlaygroundFetch>(async (input, init) => {
      const url = String(input);
      if (url === '/api/user/self/groups') {
        return jsonResponse({ success: true, data: { default: { ratio: 1, desc: '' } } });
      }
      if (url.startsWith('/api/user/models?')) {
        return jsonResponse({ success: true, data: ['model-a'] });
      }
      if (url === '/pg/chat/completions') {
        requestSignal.current = init?.signal as AbortSignal;
        return new Promise<Response>((_resolve, reject) => {
          requestSignal.current?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);
    await screen.findByRole('option', { name: 'model-a' });
    await userEvent.setup().type(screen.getByLabelText('Message'), 'Long request');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Send message' }));
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Stop generation' }));

    expect(requestSignal.current?.aborted).toBe(true);
    expect(screen.getByText('Stopped')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('button', { name: 'Send message' })).toBeTruthy();
  });

  it('validates advanced fields before transport and aborts catalog work on unmount', async () => {
    const fetcher = catalogFetcher();
    const view = render(<PlaygroundView user={{ ...USER, role: 10 }} onNavigate={vi.fn()} fetcher={fetcher} />);
    await screen.findByRole('option', { name: 'gpt-4o-mini' });
    expect(screen.getByText('Administrator account · Reader')).toBeTruthy();
    await userEvent.setup().click(screen.getByText('Advanced parameters'));
    fireEvent.change(screen.getByLabelText('Temperature'), { target: { value: '2.1' } });
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'hello' } });
    fireEvent.submit(screen.getByLabelText('Message').closest('form') as HTMLFormElement);
    expect(await screen.findByText('Check the message and request parameters.')).toBeTruthy();
    expect(fetcher.mock.calls.some(([input]) => String(input) === '/pg/chat/completions')).toBe(false);
    view.unmount();
  });

  it('aborts the group catalog request when the route unmounts', () => {
    const signal: { current?: AbortSignal } = {};
    const fetcher = vi.fn<PlaygroundFetch>((_input, init) => {
      signal.current = init?.signal as AbortSignal;
      return new Promise<Response>(() => undefined);
    });
    const view = render(<PlaygroundView user={USER} onNavigate={vi.fn()} fetcher={fetcher} />);
    view.unmount();
    expect(signal.current?.aborted).toBe(true);
  });
});
