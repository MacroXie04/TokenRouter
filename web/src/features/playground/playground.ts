export const PLAYGROUND_ENDPOINT = '/pg/chat/completions';

// Playground requests use the authenticated browser session and the backend's
// request-local relay token. User token lists intentionally expose only masked
// keys, so this flow must never manufacture an Authorization header from them.
export function playgroundRequestInit(model: string, message: string): RequestInit {
  return {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: message }],
      stream: true,
    }),
  };
}
