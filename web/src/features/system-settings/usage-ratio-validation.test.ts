import { describe, expect, it } from 'vitest';

import { validateUsageRatioMap } from './usage-ratio-validation';

describe('usage ratio map validation', () => {
  it('accepts empty, zero, decimal, exponent, unicode, and wildcard values', () => {
    expect(validateUsageRatioMap('')).toBeNull();
    expect(validateUsageRatioMap('{}')).toBeNull();
    expect(validateUsageRatioMap('{"gpt-4o":0.5,"gemini-*-thinking":0,"模型":1e2}')).toBeNull();
  });

  it.each([
    '[]',
    'null',
    '{"m":"1"}',
    '{"m":-0.1}',
    '{"m":1000000000001}',
    '{" bad ":1}',
    '{"m":1,}',
    '{"m":01}',
    '{"m":1.}',
    '{"m":1e}',
    '{"m":NaN}',
  ])('rejects malformed or out-of-policy input %s', (source) => {
    expect(validateUsageRatioMap(source)).toBe('Enter a valid bounded non-negative ratio map.');
  });

  it('rejects duplicate decoded keys instead of accepting JSON.parse last-write-wins behavior', () => {
    expect(validateUsageRatioMap('{"model":1,"model":2}'))
      .toBe('Enter a valid bounded non-negative ratio map.');
    expect(validateUsageRatioMap('{"model":1,"\\u006dodel":2}'))
      .toBe('Enter a valid bounded non-negative ratio map.');
  });

  it('enforces the entry and encoded-size bounds', () => {
    const oversizedMap = `{${Array.from({ length: 20_001 }, (_, index) => `"m-${index}":1`).join(',')}}`;
    expect(validateUsageRatioMap(oversizedMap)).toBe('Enter a valid bounded non-negative ratio map.');
    expect(validateUsageRatioMap(`{"${'m'.repeat(257)}":1}`))
      .toBe('Enter a valid bounded non-negative ratio map.');
    expect(validateUsageRatioMap(`{"m":1}${' '.repeat(1024 * 1024)}`))
      .toBe('Enter a valid bounded non-negative ratio map.');
  });
});
