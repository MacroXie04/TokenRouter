// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getData } from '../shared/api/client';
import App from './App';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en', changeLanguage: vi.fn() },
    }),
  };
});

vi.mock('../shared/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../shared/api/client')>();
  return { ...actual, getData: vi.fn() };
});

vi.mock('../features/models', () => ({
  ModelsView: ({
    section,
    role,
    onNavigate,
  }: {
    section: string;
    role: number;
    onNavigate: (target: string) => void;
  }) => (
    <main aria-label="dedicated models">
      <span>{section} / role {role}</span>
      <button type="button" onClick={() => onNavigate('/models/deployments')}>Deployments</button>
    </main>
  ),
}));

const mockedGetData = vi.mocked(getData);

beforeEach(() => {
  vi.resetAllMocks();
  window.history.replaceState(null, '', '/models');
  Object.defineProperty(window, 'scrollTo', { configurable: true, value: vi.fn() });
  mockedGetData.mockImplementation(async (path: string) => {
    if (path === '/setup') return { setup_required: false };
    if (path === '/status') return {};
    if (path === '/user/self') {
      return {
        id: 7,
        username: 'operator',
        display_name: 'Operator',
        role: 10,
        group: 'default',
        quota: 0,
        used_quota: 0,
        request_count: 0,
      };
    }
    throw new Error(`unexpected ${path}`);
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('/models integration', () => {
  it('renders metadata and nested deployment sections in the dedicated bounded view', async () => {
    render(<App />);

    expect(await screen.findByRole('main', { name: 'dedicated models' }))
      .toHaveProperty('textContent', 'metadata / role 10Deployments');

    fireEvent.click(screen.getByRole('button', { name: 'Deployments' }));
    expect(await screen.findByRole('main', { name: 'dedicated models' }))
      .toHaveProperty('textContent', 'deployments / role 10Deployments');
  });
});
