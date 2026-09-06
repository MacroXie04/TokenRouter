import { describe, expect, it } from 'vitest';
import { decideRoute } from '../../lib/router';

const context = {
  setupRequired: false,
  user: null,
  search: '?period=month',
};

describe('rankings route authorization', () => {
  it('allows anonymous access only when the published module policy does', () => {
    const decision = decideRoute('/rankings', {
      ...context,
      modules: { rankings: { enabled: true, requireAuth: false } },
    });
    expect(decision.route.name).toBe('rankings');
    expect(decision.redirect).toBeUndefined();
  });

  it('preserves the selected period across the sign-in redirect when authentication is required', () => {
    expect(decideRoute('/rankings', {
      ...context,
      modules: { rankings: { enabled: true, requireAuth: true } },
    }).redirect).toBe('/sign-in?redirect=%2Frankings%3Fperiod%3Dmonth');
  });

  it('fails closed to home when the module is disabled', () => {
    expect(decideRoute('/rankings', {
      ...context,
      modules: { rankings: { enabled: false, requireAuth: false } },
    }).redirect).toBe('/');
  });
});
