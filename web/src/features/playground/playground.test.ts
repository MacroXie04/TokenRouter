import { describe, expect, it } from 'vitest';
import { PLAYGROUND_ENDPOINT, playgroundRequestInit } from './playground';

describe('playground browser request', () => {
  it('uses the session-backed endpoint without forwarding a masked API key', () => {
    const request = playgroundRequestInit('gpt-4o-mini', 'hello');
    expect(PLAYGROUND_ENDPOINT).toBe('/pg/chat/completions');
    expect(request.method).toBe('POST');
    expect(request.credentials).toBe('same-origin');
    expect(request.headers).toEqual({ 'Content-Type': 'application/json' });
    expect(JSON.parse(String(request.body))).toEqual({
      model: 'gpt-4o-mini',
      messages: [{ role: 'user', content: 'hello' }],
      stream: true,
    });
    expect(JSON.stringify(request)).not.toContain('Authorization');
  });
});
