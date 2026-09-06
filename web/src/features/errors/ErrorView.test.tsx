// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { authenticatedErrorCode, ErrorView, MAX_ERROR_DETAIL_CHARACTERS } from './ErrorView';

vi.mock('react-i18next', () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('ErrorView', () => {
  it('maps authenticated error names and rejects unknown names', () => {
    expect(authenticatedErrorCode('unauthorized')).toBe('401');
    expect(authenticatedErrorCode('forbidden')).toBe('403');
    expect(authenticatedErrorCode('not-found')).toBe('404');
    expect(authenticatedErrorCode('internal-server-error')).toBe('500');
    expect(authenticatedErrorCode('maintenance-error')).toBe('503');
    expect(authenticatedErrorCode('private-upstream-detail')).toBe('404');
  });

  it.each([
    ['401', 'Sign-in required', 'You need to sign in to view this page.'],
    ['403', 'Access denied', 'Your account does not have permission to view this page.'],
    ['404', 'Page not found', 'The page you requested does not exist.'],
    ['500', 'Something went wrong', 'The application could not complete this request.'],
    ['503', 'Service unavailable', 'TokenRouter is temporarily unavailable.'],
  ])('renders the %s contract', (code, title, detail) => {
    render(<ErrorView code={code} />);

    const heading = screen.getByRole('heading', { name: title });
    expect(screen.getByRole('main').getAttribute('aria-labelledby')).toBe(heading.id);
    expect(screen.getByText(detail)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Back to home' }).getAttribute('href')).toBe('/');
    expect(screen.queryByRole('link', { name: 'Sign in' }) !== null).toBe(code === '401');
    expect(screen.queryByRole('link', { name: 'Report a problem' }) !== null).toBe(code === '500');
  });

  it('supports history navigation and a bounded custom detail surface', () => {
    const back = vi.spyOn(window.history, 'back').mockImplementation(() => undefined);
    const detail = `Request id: ${'x'.repeat(MAX_ERROR_DETAIL_CHARACTERS)}`;
    render(<ErrorView code="500" detail={detail} />);

    fireEvent.click(screen.getByRole('button', { name: 'Go back' }));
    expect(back).toHaveBeenCalledOnce();
    const renderedDetail = screen.getByText(/^Request id:/).textContent ?? '';
    expect(renderedDetail).toHaveLength(MAX_ERROR_DETAIL_CHARACTERS);
    expect(renderedDetail.length).toBeLessThan(detail.length);
  });
});
