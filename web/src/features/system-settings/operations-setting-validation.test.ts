import { describe, expect, it } from 'vitest';
import {
  validateChannelDisableKeywords,
  validateHTTPStatusRanges,
  validateToolPriceMap,
} from './operations-setting-validation';

describe('operations setting validation', () => {
  it('accepts exact and prefix tool prices, including terminal zero', () => {
    expect(validateToolPriceMap('{"web_search":10,"web_search:gpt-4o-*":25,"custom":0}')).toBeNull();
    expect(validateToolPriceMap('{"image_generation":1.25e2}')).toBeNull();
    expect(validateToolPriceMap('{"minimum":1e-12,"maximum":1e12}')).toBeNull();
    expect(validateToolPriceMap('{}')).toBeNull();
  });

  it.each([
    ['duplicate key', '{"web_search":1,"web_search":2}'],
    ['escaped duplicate key', String.raw`{"web_search":1,"web_\u0073earch":2}`],
    ['negative price', '{"web_search":-1}'],
    ['over maximum', '{"web_search":1000000000001}'],
    ['invalid wildcard', '{"web*":1}'],
    ['empty model prefix', '{"web_search:*":1}'],
    ['nested value', '{"web_search":{"price":1}}'],
    ['trailing value', '{}[]'],
    ['huge positive exponent', '{"web_search":1e2147483647}'],
    ['huge negative exponent', '{"web_search":1e-2147483648}'],
    ['effective exponent too positive', '{"web_search":1e13}'],
    ['effective exponent too negative', '{"web_search":1e-13}'],
    ['long coefficient', '{"web_search":12345678901234567890123456e-14}'],
  ])('rejects %s without lossy JSON parsing', (_name, source) => {
    expect(validateToolPriceMap(source)).toBe('Enter valid bounded tool prices.');
  });

  it('rejects hostile exponents before doing exponent-sized work', () => {
    const source = '{"web_search":1e2147483647}';
    const started = performance.now();
    for (let count = 0; count < 1_000; count += 1) {
      expect(validateToolPriceMap(source)).not.toBeNull();
    }
    expect(performance.now() - started).toBeLessThan(1_000);
  });

  it('validates bounded HTTP status-code rules', () => {
    expect(validateHTTPStatusRanges('401, 409-499，500-503')).toBeNull();
    expect(validateHTTPStatusRanges('')).toBeNull();
    expect(validateHTTPStatusRanges('099')).not.toBeNull();
    expect(validateHTTPStatusRanges('500-400')).not.toBeNull();
    expect(validateHTTPStatusRanges('401\n500')).not.toBeNull();
    expect(validateHTTPStatusRanges(Array.from({ length: 129 }, () => '401').join(','))).not.toBeNull();
  });

  it('validates keyword count, byte, and control-character limits', () => {
    expect(validateChannelDisableKeywords('Permission denied\r\npermission DENIED\nQuota exceeded')).toBeNull();
    expect(validateChannelDisableKeywords('bad\tkeyword')).not.toBeNull();
    expect(validateChannelDisableKeywords('x'.repeat(257))).not.toBeNull();
    expect(validateChannelDisableKeywords(Array.from({ length: 129 }, (_, index) => `failure ${index}`).join('\n'))).not.toBeNull();
  });
});
