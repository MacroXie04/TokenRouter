// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SystemBrandProvider } from '../layout/SystemBrand';
import { AuthLayout } from './AuthLayout';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

afterEach(cleanup);

describe('AuthLayout', () => {
  it('renders an accessible loading brand without exposing a broken image', () => {
    const { container } = render(
      <SystemBrandProvider value={{ systemName: 'TokenRouter', logo: '', loading: true }}>
        <AuthLayout title="Sign in"><p>Form</p></AuthLayout>
      </SystemBrandProvider>,
    );

    expect(screen.getByRole('main').getAttribute('aria-labelledby')).toBeTruthy();
    expect(screen.getByRole('main').getAttribute('aria-busy')).toBe('true');
    expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Back to home' }).getAttribute('aria-busy')).toBe('true');
    expect(screen.getByText('Loading TokenRouter…')).toBeTruthy();
    expect(container.querySelector('.system-brand-skeleton-logo')).toBeTruthy();
    expect(container.querySelector('img')).toBeNull();
  });

  it('uses the configured name and logo and falls back safely when artwork fails', () => {
    const { container } = render(
      <SystemBrandProvider value={{ systemName: 'Acme Gateway', logo: 'https://cdn.example/logo.png', loading: false }}>
        <AuthLayout title="Sign in"><p>Form</p></AuthLayout>
      </SystemBrandProvider>,
    );

    const brand = screen.getByRole('link', { name: 'Back to home' });
    const image = container.querySelector('img')!;
    expect(brand.textContent).toContain('Acme Gateway');
    expect(image.getAttribute('src')).toBe('https://cdn.example/logo.png');
    fireEvent.error(image);
    expect(image.hidden).toBe(true);
    expect(container.querySelector('.system-brand-fallback')?.textContent).toBe('A');
  });
});
