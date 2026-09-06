import { describe, expect, it } from 'vitest';
import { parseHeaderNavigationModules } from './nav-modules';

describe('header navigation module parsing', () => {
  it('defaults absent and malformed settings to public enabled modules', () => {
    expect(parseHeaderNavigationModules('not-json')).toEqual({
      home: true,
      console: true,
      docs: true,
      about: true,
      pricing: { enabled: true, requireAuth: false },
      rankings: { enabled: true, requireAuth: false },
    });
  });

  it('supports object and legacy scalar forms', () => {
    expect(parseHeaderNavigationModules(JSON.stringify({
      home: false,
      about: { enabled: 0 },
      pricing: false,
      rankings: { enabled: '1', requireAuth: 'true' },
    }))).toEqual({
      home: false,
      console: true,
      docs: true,
      about: false,
      pricing: { enabled: false, requireAuth: false },
      rankings: { enabled: true, requireAuth: true },
    });
  });
});
