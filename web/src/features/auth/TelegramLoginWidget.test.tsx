// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { normalizeTelegramBotName, TelegramLoginWidget } from './TelegramLoginWidget';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

afterEach(() => {
  cleanup();
});

describe('TelegramLoginWidget', () => {
  it('loads one fixed, no-referrer widget and forwards the opaque callback value', async () => {
    const onAuthorization = vi.fn();
    const rendered = render(
      <TelegramLoginWidget botName="@TokenRouter_bot" pending={false} onAuthorization={onAuthorization} />,
    );

    const script = rendered.container.querySelector('script');
    expect(script?.src).toBe('https://telegram.org/js/telegram-widget.js?22');
    expect(script?.referrerPolicy).toBe('no-referrer');
    expect(script?.dataset.telegramLogin).toBe('TokenRouter_bot');
    expect(script?.dataset.onauth).toMatch(/^tokenRouterTelegramLogin\d+\(user\)$/);
    fireEvent.load(script!);
    await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull());

    const callbackName = script?.dataset.onauth?.replace('(user)', '') ?? '';
    const callback = (window as unknown as Record<string, unknown>)[callbackName];
    expect(typeof callback).toBe('function');
    (callback as (value: unknown) => void)({ id: 7, private: 'ignored later' });
    expect(onAuthorization).toHaveBeenCalledWith({ id: 7, private: 'ignored later' });

    rendered.unmount();
    expect((window as unknown as Record<string, unknown>)[callbackName]).toBeUndefined();
  });

  it('fails closed for invalid bot names and retries a provider load failure', async () => {
    const first = render(
      <TelegramLoginWidget botName="not a bot" pending={false} onAuthorization={vi.fn()} />,
    );
    expect((await screen.findByRole('alert')).textContent)
      .toBe('External sign-in failed. Please try again.');
    expect(first.container.querySelector('script')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
    first.unmount();

    const second = render(
      <TelegramLoginWidget botName="router_bot" pending={false} onAuthorization={vi.fn()} />,
    );
    const script = second.container.querySelector('script');
    fireEvent.error(script!);
    const retry = await screen.findByRole('button', { name: 'Retry' });
    await userEvent.setup().click(retry);
    expect(second.container.querySelectorAll('script')).toHaveLength(1);
    expect(second.container.querySelector('script')).not.toBe(script);
  });

  it('normalizes only valid Telegram bot usernames', () => {
    expect(normalizeTelegramBotName('@Example_bot')).toBe('Example_bot');
    expect(normalizeTelegramBotName('example')).toBeNull();
    expect(normalizeTelegramBotName('bad bot')).toBeNull();
    expect(normalizeTelegramBotName(`${'x'.repeat(32)}bot`)).toBeNull();
  });
});
