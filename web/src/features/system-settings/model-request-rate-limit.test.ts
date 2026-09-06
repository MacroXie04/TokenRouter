import { describe, expect, it } from 'vitest';
import { validateModelRequestRateLimitGroups } from './model-request-rate-limit';

describe('model request rate-limit group settings', () => {
  it('accepts the empty/default shapes and exact signed-32-bit boundary', () => {
    expect(validateModelRequestRateLimitGroups('')).toBeNull();
    expect(validateModelRequestRateLimitGroups(' {} \n')).toBeNull();
    expect(validateModelRequestRateLimitGroups('{"default":[0,1000],"vip":[2147483647,2147483647]}')).toBeNull();
    expect(validateModelRequestRateLimitGroups('{"\u4f1a\u5458":[50,40]}')).toBeNull();
  });

  it('rejects malformed shapes, non-integer spellings, and out-of-range values', () => {
    [
      '[]',
      '{"vip":1}',
      '{"vip":[1]}',
      '{"vip":[1,2,3]}',
      '{"vip":[1.0,2]}',
      '{"vip":[1e2,2]}',
      '{"vip":[-1,2]}',
      '{"vip":[1,0]}',
      '{"vip":[2147483648,2]}',
      '{"vip":[1,2147483648]}',
      '{"vip":[1,2]} trailing',
      '{"vip":[1,2],}',
    ].forEach((source) => expect(validateModelRequestRateLimitGroups(source), source).not.toBeNull());
  });

  it('rejects duplicate decoded groups and unsafe group identifiers', () => {
    [
      '{"vip":[1,2],"vip":[3,4]}',
      '{"vip":[1,2],"v\\u0069p":[3,4]}',
      '{" bad ":[1,2]}',
      '{"bad\\u0000name":[1,2]}',
      '{"bad\\u202ename":[1,2]}',
      `{ "${'🙂'.repeat(17)}": [1, 2] }`,
    ].forEach((source) => expect(validateModelRequestRateLimitGroups(source), source).not.toBeNull());
  });

  it('bounds the number of groups and encoded input bytes', () => {
    const groups = Object.fromEntries(Array.from({ length: 256 }, (_, index) => [`group-${index}`, [0, 1]]));
    expect(validateModelRequestRateLimitGroups(JSON.stringify(groups))).toBeNull();
    groups['one-too-many'] = [0, 1];
    expect(validateModelRequestRateLimitGroups(JSON.stringify(groups))).not.toBeNull();
    expect(validateModelRequestRateLimitGroups(`{"vip":[1,2]}${' '.repeat(64 * 1024)}`)).not.toBeNull();
  });
});
