// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App, { AppErrorBoundary } from '../../App';
import { getData } from '../../api';

vi.mock('../../api', () => ({
  getData: vi.fn(),
  isTransientSessionRefreshFailure: vi.fn(() => false),
  SESSION_EXPIRED_EVENT: 'tokenrouter:session-expired',
}));

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  const t = (key: string) => key;
  return {
    ...actual,
    useTranslation: () => ({ t, i18n: { language: 'en', changeLanguage: vi.fn() } }),
  };
});

afterEach(() => {
  cleanup();
  vi.mocked(getData).mockReset();
  vi.restoreAllMocks();
});

function BrokenView(): never {
  throw new Error('private render detail');
}

describe('AppErrorBoundary', () => {
  it('turns an unexpected render failure into the localized 500 surface', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    render(
      <AppErrorBoundary>
        <BrokenView />
      </AppErrorBoundary>,
    );

    expect(screen.getByRole('heading', { name: 'Something went wrong' })).toBeTruthy();
    expect(screen.getByText('The application could not complete this request.')).toBeTruthy();
    expect(screen.queryByText('private render detail')).toBeNull();
  });

  it('renders the localized 503 surface when bootstrap fails', async () => {
    vi.mocked(getData).mockRejectedValueOnce(new Error('private bootstrap detail'));
    render(<App />);

    expect(await screen.findByRole('heading', { name: 'Service unavailable' })).toBeTruthy();
    expect(screen.getByText('TokenRouter is temporarily unavailable.')).toBeTruthy();
    expect(screen.queryByText('private bootstrap detail')).toBeNull();
  });
});
