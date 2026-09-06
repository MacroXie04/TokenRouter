import { describe, expect, it } from 'vitest';
import type { User } from '../api';
import {
  decideRoute,
  matchRoute,
  safeReturnTarget,
  sectionRouteRedirect,
  signInTarget,
  systemSettingsRedirect,
} from './router';

const user = { id: 1, username: 'user', display_name: 'User', role: 1 } as User;
const admin = { ...user, id: 2, role: 10 };
const root = { ...user, id: 3, role: 100 };

describe('route matching and guards', () => {
  it('matches public, parameterized, and authenticated routes', () => {
    expect(matchRoute('/pricing/gpt-4o/')).toMatchObject({ name: 'pricing-detail', parameter: 'gpt-4o' });
    expect(matchRoute('/system-settings/security/ssrf')).toMatchObject({
      name: 'system-settings', authenticated: true, minimumRole: 100, parameter: 'security/ssrf',
    });
    expect(matchRoute('/not-a-route')).toMatchObject({ name: 'not-found' });
  });

  it('forces setup globally and redirects away when setup is complete', () => {
    expect(decideRoute('/pricing', { setupRequired: true, user: null }).redirect).toBe('/setup');
    expect(decideRoute('/setup', { setupRequired: true, user: null }).redirect).toBeUndefined();
    expect(decideRoute('/setup', { setupRequired: false, user: null }).redirect).toBe('/');
  });

  it('keeps legacy authentication links functional', () => {
    expect(decideRoute('/register', {
      setupRequired: false,
      user: null,
      search: '?redirect=%2Fwallet&invite=bounded-code',
    }).redirect).toBe('/sign-up?redirect=%2Fwallet&invite=bounded-code');
    expect(decideRoute('/login', {
      setupRequired: false,
      user: null,
      search: '?error=oauth_state',
    }).redirect).toBe('/sign-in?error=oauth_state');
  });

  it('preserves a safe requested URL for anonymous users', () => {
    expect(decideRoute('/keys', { setupRequired: false, user: null, search: '?page=2' }).redirect)
      .toBe('/sign-in?redirect=%2Fkeys%3Fpage%3D2');
    expect(decideRoute('/keys', { setupRequired: false, user }).redirect).toBeUndefined();
  });

  it('enforces admin and root route roles', () => {
    expect(decideRoute('/channels', { setupRequired: false, user }).redirect).toBe('/403');
    expect(decideRoute('/channels', { setupRequired: false, user: admin }).redirect).toBeUndefined();
    expect(matchRoute('/redemption-codes')).toMatchObject({
      name: 'redemption-codes', authenticated: true, minimumRole: 10,
    });
    expect(decideRoute('/redemption-codes', { setupRequired: false, user }).redirect).toBe('/403');
    expect(decideRoute('/redemption-codes', { setupRequired: false, user: admin }).redirect).toBeUndefined();
    expect(decideRoute('/system-settings', { setupRequired: false, user: admin }).redirect).toBe('/403');
    expect(decideRoute('/system-settings', { setupRequired: false, user: root }).redirect).toBe('/system-settings/site');
    expect(decideRoute('/system-info', { setupRequired: false, user: admin }).redirect).toBe('/403');
    expect(decideRoute('/system-info', { setupRequired: false, user: root }).redirect).toBeUndefined();
  });

  it('normalizes every system-settings landing and invalid section after the root guard', () => {
    expect(systemSettingsRedirect()).toBe('/system-settings/site');
    expect(systemSettingsRedirect('site')).toBe('/system-settings/site/system-info');
    expect(systemSettingsRedirect('auth')).toBe('/system-settings/auth/basic-auth');
    expect(systemSettingsRedirect('billing')).toBe('/system-settings/billing/quota');
    expect(systemSettingsRedirect('content')).toBe('/system-settings/content/dashboard');
    expect(systemSettingsRedirect('models')).toBe('/system-settings/models/global');
    expect(systemSettingsRedirect('operations')).toBe('/system-settings/operations/behavior');
    expect(systemSettingsRedirect('security')).toBe('/system-settings/security/rate-limit');
    expect(systemSettingsRedirect('auth/not-real')).toBe('/system-settings/auth/basic-auth');
    expect(systemSettingsRedirect('operations/monitoring')).toBe('/system-settings/models/routing-reliability');
    expect(systemSettingsRedirect('not-real/anything')).toBe('/system-settings/site');
    expect(systemSettingsRedirect('site/notice/extra')).toBe('/system-settings/site/system-info');
    expect(systemSettingsRedirect('site/notice')).toBeNull();

    expect(decideRoute('/system-settings/auth', { setupRequired: false, user: root }).redirect)
      .toBe('/system-settings/auth/basic-auth');
    expect(decideRoute('/system-settings/auth/not-real', { setupRequired: false, user: root }).redirect)
      .toBe('/system-settings/auth/basic-auth');
    expect(decideRoute('/system-settings/auth', { setupRequired: false, user: admin }).redirect)
      .toBe('/403');
  });

  it('canonicalizes dashboard, model, and usage-log sections after their guards', () => {
    expect(sectionRouteRedirect('dashboard')).toBe('/dashboard/overview');
    expect(sectionRouteRedirect('dashboard', 'models')).toBeNull();
    expect(sectionRouteRedirect('dashboard', 'unknown')).toBe('/dashboard/overview');
    expect(sectionRouteRedirect('models')).toBe('/models/metadata');
    expect(sectionRouteRedirect('models', 'deployments')).toBeNull();
    expect(sectionRouteRedirect('models', 'unknown')).toBe('/models/metadata');
    expect(sectionRouteRedirect('usage-logs')).toBe('/usage-logs/common');
    expect(sectionRouteRedirect('usage-logs', 'drawing', '?type=2&model=gpt-4o'))
      .toBe('/usage-logs/drawing?model=gpt-4o');
    expect(sectionRouteRedirect('usage-logs', 'task', '?type=1&type=2')).toBe('/usage-logs/task');
    expect(sectionRouteRedirect('usage-logs', 'common', '?type=2')).toBeNull();

    expect(decideRoute('/dashboard', { setupRequired: false, user }).redirect).toBe('/dashboard/overview');
    expect(decideRoute('/dashboard/not-real', { setupRequired: false, user }).redirect).toBe('/dashboard/overview');
    expect(decideRoute('/models', { setupRequired: false, user: admin }).redirect).toBe('/models/metadata');
    expect(decideRoute('/models/deployments', { setupRequired: false, user: admin }).redirect).toBeUndefined();
    expect(decideRoute('/models', { setupRequired: false, user }).redirect).toBe('/403');
    expect(decideRoute('/usage-logs', { setupRequired: false, user }).redirect).toBe('/usage-logs/common');
    expect(decideRoute('/usage-logs/task', {
      setupRequired: false,
      user,
      search: '?type=6&requestId=bounded',
    }).redirect).toBe('/usage-logs/task?requestId=bounded');
  });

  it('applies configurable public-module access consistently', () => {
    expect(decideRoute('/pricing', {
      setupRequired: false,
      user,
      modules: { pricing: { enabled: false, requireAuth: false } },
    }).redirect).toBe('/');
    expect(decideRoute('/rankings', {
      setupRequired: false,
      user: null,
      modules: { rankings: { enabled: true, requireAuth: true } },
    }).redirect).toBe('/sign-in?redirect=%2Frankings');
  });

  it('accepts only same-origin relative post-login targets', () => {
    expect(safeReturnTarget('?redirect=%2Fwallet%3Fpage%3D2')).toBe('/wallet?page=2');
    expect(safeReturnTarget('?redirect=https%3A%2F%2Fevil.example')).toBeNull();
    expect(safeReturnTarget('?redirect=%2F%2Fevil.example')).toBeNull();
    expect(safeReturnTarget('?redirect=%2Fsign-in')).toBeNull();
    expect(safeReturnTarget('?redirect=%2Fotp')).toBeNull();
    expect(safeReturnTarget('?redirect=%2Foauth%2Fgithub')).toBeNull();
    expect(signInTarget('/profile', '?tab=security')).toBe('/sign-in?redirect=%2Fprofile%3Ftab%3Dsecurity');
  });
});
