// @vitest-environment jsdom

import { describe, expect, it, vi } from 'vitest';
import {
  PLAYGROUND_ENDPOINT,
  PLAYGROUND_SESSION_EXPIRED_EVENT,
  PlaygroundContractError,
  buildPlaygroundRequest,
  loadPlaygroundGroups,
  loadPlaygroundModels,
  parsePlaygroundGroups,
  parsePlaygroundModels,
  streamPlaygroundCompletion,
  type PlaygroundChatRequest,
  type PlaygroundFetch,
} from './playground-api';

function jsonResponse(value: unknown, status = 200, headers?: HeadersInit): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function byteStreamResponse(bytes: Uint8Array[], contentType = 'text/event-stream; charset=utf-8'): Response {
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of bytes) controller.enqueue(chunk);
      controller.close();
    },
  }), { headers: { 'Content-Type': contentType } });
}

function sseResponse(...events: string[]): Response {
  const encoder = new TextEncoder();
  return byteStreamResponse(events.map((event) => encoder.encode(event)));
}

function validPayload(): PlaygroundChatRequest {
  return buildPlaygroundRequest({
    model: 'gpt-4o-mini',
    group: 'default',
    systemPrompt: '',
    conversation: [],
    message: 'Hello',
    parameters: {
      temperature: 0.7,
      topP: 1,
      maxTokens: 4096,
      frequencyPenalty: 0,
      presencePenalty: 0,
      seed: null,
    },
  });
}

describe('playground catalog contracts', () => {
  it('parses bounded groups and models without trusting malformed entries', () => {
    expect(parsePlaygroundGroups({
      success: true,
      data: {
        vip: { ratio: 1.5, desc: 'Priority' },
        auto: { ratio: '自动', desc: 'Automatic routing' },
        default: { ratio: 1, desc: 'Standard' },
      },
    })).toEqual([
      { id: 'default', ratio: 1, description: 'Standard', automatic: false },
      { id: 'auto', ratio: null, description: 'Automatic routing', automatic: true },
      { id: 'vip', ratio: 1.5, description: 'Priority', automatic: false },
    ]);
    expect(parsePlaygroundModels({ success: true, data: ['gpt-4o', 'claude-3'] }))
      .toEqual(['gpt-4o', 'claude-3']);

    expect(() => parsePlaygroundGroups({ success: true, data: { 'bad\nname': { ratio: 1 } } }))
      .toThrow(PlaygroundContractError);
    expect(() => parsePlaygroundGroups({ success: true, data: { auto: { ratio: Number.POSITIVE_INFINITY } } }))
      .toThrow(PlaygroundContractError);
    expect(() => parsePlaygroundModels({ success: true, data: ['duplicate', 'duplicate'] }))
      .toThrow(PlaygroundContractError);
    expect(() => parsePlaygroundModels({ success: true, data: [`safe\u202Efdp.exe`] }))
      .toThrow(PlaygroundContractError);
    expect(() => parsePlaygroundModels({ success: false, data: [] }))
      .toThrow(PlaygroundContractError);
  });

  it('uses same-origin authenticated catalog requests and encodes the selected group', async () => {
    const fetcher = vi.fn<PlaygroundFetch>()
      .mockResolvedValueOnce(jsonResponse({ success: true, data: { default: { ratio: 1, desc: '' } } }))
      .mockResolvedValueOnce(jsonResponse({ success: true, data: ['model-a'] }));

    await expect(loadPlaygroundGroups(undefined, fetcher)).resolves.toHaveLength(1);
    await expect(loadPlaygroundModels('staff & research', undefined, fetcher)).resolves.toEqual(['model-a']);
    expect(fetcher.mock.calls[0][0]).toBe('/api/user/self/groups');
    expect(fetcher.mock.calls[0][1]).toMatchObject({
      method: 'GET', credentials: 'same-origin', cache: 'no-store',
    });
    expect(fetcher.mock.calls[1][0]).toBe('/api/user/models?group=staff+%26+research');
    expect(JSON.stringify(fetcher.mock.calls)).not.toContain('Authorization');
  });

  it('caps catalog bodies and announces a server-side session expiry', async () => {
    const sessionExpired = vi.fn();
    window.addEventListener(PLAYGROUND_SESSION_EXPIRED_EVENT, sessionExpired, { once: true });
    const unauthorized = vi.fn<PlaygroundFetch>().mockResolvedValue(jsonResponse({ private: 'detail' }, 401));
    await expect(loadPlaygroundGroups(undefined, unauthorized)).rejects.toMatchObject({ status: 401 });
    expect(sessionExpired).toHaveBeenCalledTimes(1);

    const oversized = vi.fn<PlaygroundFetch>().mockResolvedValue(jsonResponse(
      { success: true, data: {} },
      200,
      { 'Content-Length': String(600 * 1024) },
    ));
    await expect(loadPlaygroundGroups(undefined, oversized)).rejects.toMatchObject({ code: 'response-too-large' });
  });
});

describe('playground request contract', () => {
  it('builds the exact supported chat-completions payload', () => {
    expect(buildPlaygroundRequest({
      model: 'gpt-4o-mini',
      group: 'vip',
      systemPrompt: 'Be concise.',
      conversation: [
        { role: 'user', content: 'First question' },
        { role: 'assistant', content: 'First answer' },
      ],
      message: 'Follow up',
      parameters: {
        temperature: 0.2,
        topP: 0.9,
        maxTokens: 1024,
        frequencyPenalty: -0.5,
        presencePenalty: 0.4,
        seed: 42,
      },
    })).toEqual({
      model: 'gpt-4o-mini',
      group: 'vip',
      messages: [
        { role: 'system', content: 'Be concise.' },
        { role: 'user', content: 'First question' },
        { role: 'assistant', content: 'First answer' },
        { role: 'user', content: 'Follow up' },
      ],
      stream: true,
      temperature: 0.2,
      top_p: 0.9,
      max_tokens: 1024,
      frequency_penalty: -0.5,
      presence_penalty: 0.4,
      seed: 42,
    });
  });

  it('rejects control-bearing, blank, oversized, and out-of-range request values', () => {
    const base = {
      model: 'model',
      group: 'default',
      systemPrompt: '',
      conversation: [],
      message: 'hello',
      parameters: {
        temperature: 0.7,
        topP: 1,
        maxTokens: 10,
        frequencyPenalty: 0,
        presencePenalty: 0,
        seed: null,
      },
    };
    expect(() => buildPlaygroundRequest({ ...base, model: 'bad\nmodel' })).toThrow(PlaygroundContractError);
    expect(() => buildPlaygroundRequest({ ...base, group: `safe\u2066hidden` })).toThrow(PlaygroundContractError);
    expect(() => buildPlaygroundRequest({ ...base, message: '   ' })).toThrow(PlaygroundContractError);
    expect(() => buildPlaygroundRequest({
      ...base,
      parameters: { ...base.parameters, temperature: 2.1 },
    })).toThrow(PlaygroundContractError);
    expect(() => buildPlaygroundRequest({
      ...base,
      message: 'x'.repeat(65_537),
    })).toThrow(PlaygroundContractError);
  });
});

describe('playground streaming contract', () => {
  it('decodes split UTF-8 SSE events, reasoning, and content without sending an API key', async () => {
    const wire = [
      'data: {"choices":[{"delta":{"reasoning_content":"think ","content":"Hello "}}]}\r\n\r\n',
      'data: {"choices":[{"delta":{"content":"世界"}}]}\n\n',
      'data: [DONE]\n\n',
    ].join('');
    const encoded = new TextEncoder().encode(wire);
    const split = encoded.indexOf(0xe4) + 1;
    const fetcher = vi.fn<PlaygroundFetch>().mockResolvedValue(byteStreamResponse([
      encoded.slice(0, split),
      encoded.slice(split, split + 1),
      encoded.slice(split + 1),
    ]));
    let content = '';
    let reasoning = '';

    await streamPlaygroundCompletion(validPayload(), {
      onContent: (chunk) => { content += chunk; },
      onReasoning: (chunk) => { reasoning += chunk; },
    }, undefined, fetcher);

    expect(content).toBe('Hello 世界');
    expect(reasoning).toBe('think ');
    expect(fetcher).toHaveBeenCalledWith(PLAYGROUND_ENDPOINT, expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
    }));
    const init = fetcher.mock.calls[0][1] as RequestInit;
    expect(init.headers).toEqual({ Accept: 'text/event-stream', 'Content-Type': 'application/json' });
    expect(JSON.parse(String(init.body))).toEqual(validPayload());
    expect(JSON.stringify(init)).not.toContain('Authorization');
  });

  it('fails closed on malformed, oversized, non-event-stream, and truncated responses', async () => {
    const callbacks = { onContent: vi.fn(), onReasoning: vi.fn() };
    await expect(streamPlaygroundCompletion(
      validPayload(),
      callbacks,
      undefined,
      vi.fn<PlaygroundFetch>().mockResolvedValue(sseResponse('data: not-json\n\n')),
    )).rejects.toMatchObject({ code: 'invalid-response' });

    await expect(streamPlaygroundCompletion(
      validPayload(),
      callbacks,
      undefined,
      vi.fn<PlaygroundFetch>().mockResolvedValue(sseResponse(
        `data: ${'x'.repeat(256 * 1024 + 1)}\n\n`,
      )),
    )).rejects.toMatchObject({ code: 'response-too-large' });

    await expect(streamPlaygroundCompletion(
      validPayload(),
      callbacks,
      undefined,
      vi.fn<PlaygroundFetch>().mockResolvedValue(jsonResponse({ choices: [] })),
    )).rejects.toMatchObject({ code: 'invalid-response' });

    await expect(streamPlaygroundCompletion(
      validPayload(),
      callbacks,
      undefined,
      vi.fn<PlaygroundFetch>().mockResolvedValue(sseResponse(
        'data: {"choices":[{"delta":{"content":"partial"}}]}\n\n',
      )),
    )).rejects.toMatchObject({ code: 'truncated' });
  });

  it('never exposes an upstream error body through its exception text', async () => {
    const privateDetail = 'upstream-secret-value';
    const fetcher = vi.fn<PlaygroundFetch>().mockResolvedValue(jsonResponse({ error: privateDetail }, 502));
    let caught: unknown;
    try {
      await streamPlaygroundCompletion(validPayload(), {
        onContent: vi.fn(),
        onReasoning: vi.fn(),
      }, undefined, fetcher);
    } catch (error) {
      caught = error;
    }
    expect(caught).toBeInstanceOf(PlaygroundContractError);
    expect(String(caught)).not.toContain(privateDetail);
  });
});
