// @vitest-environment jsdom

import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { parseTurnstileConfig, turnstileParams } from './turnstile';
import { TurnstileWidget,  } from './TurnstileWidget';

afterEach(() => {
  cleanup();
  document.getElementById('tokenrouter-turnstile-script')?.remove();
  delete window.turnstile;
});

describe('TurnstileWidget', () => {
  it('parses only bounded public configuration and query tokens', () => {
    expect(parseTurnstileConfig({ turnstile_check: true, turnstile_site_key: '  site-key  ' }))
      .toEqual({ required: true, siteKey: 'site-key' });
    expect(parseTurnstileConfig({ turnstile_check: false, turnstile_site_key: 'site-key' }))
      .toEqual({ required: false, siteKey: '' });
    expect(parseTurnstileConfig({ turnstile_check: true, turnstile_site_key: 'x'.repeat(257) }))
      .toEqual({ required: true, siteKey: '' });
    expect(parseTurnstileConfig(null)).toEqual({ required: false, siteKey: '' });
    expect(turnstileParams(' challenge-token ')).toEqual({ turnstile: 'challenge-token' });
    expect(() => turnstileParams('x'.repeat(4097))).toThrow();
    expect(() => turnstileParams('bad\u0000token')).toThrow();
  });

  it('renders through the provider API, bounds callbacks, and removes its widget', async () => {
    let options: Record<string, unknown> = {};
    const remove = vi.fn();
    window.turnstile = {
      render: vi.fn((_element, value) => {
        options = value;
        return 'widget-1';
      }),
      remove,
    };
    const onVerify = vi.fn();
    const onExpire = vi.fn();
    const rendered = render(
      <TurnstileWidget siteKey="site-key" label="Human verification" onVerify={onVerify} onExpire={onExpire} />,
    );

    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));
    expect(rendered.getByRole('group', { name: 'Human verification' })).toBeTruthy();
    (options.callback as (token: string) => void)('verified-token');
    expect(onVerify).toHaveBeenCalledWith('verified-token');
    (options.callback as (token: string) => void)('x'.repeat(4097));
    expect(onVerify).toHaveBeenCalledTimes(1);
    (options['expired-callback'] as () => void)();
    expect(onExpire).toHaveBeenCalledTimes(2);

    rendered.unmount();
    expect(remove).toHaveBeenCalledWith('widget-1');
  });

  it('loads the fixed provider script once and renders after it becomes ready', async () => {
    render(<TurnstileWidget siteKey="site-key" label="Human verification" onVerify={vi.fn()} />);
    const script = document.getElementById('tokenrouter-turnstile-script') as HTMLScriptElement;
    expect(script).toBeTruthy();
    expect(script.src).toBe('https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit');

    window.turnstile = { render: vi.fn(() => 'widget-2'), remove: vi.fn() };
    fireEvent.load(script);
    await waitFor(() => expect(window.turnstile?.render).toHaveBeenCalledTimes(1));

    render(<TurnstileWidget siteKey="second-site" label="Second verification" onVerify={vi.fn()} />);
    expect(document.querySelectorAll('#tokenrouter-turnstile-script')).toHaveLength(1);
  });
});
