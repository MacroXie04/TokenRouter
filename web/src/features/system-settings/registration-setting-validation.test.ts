import { describe, expect, it } from 'vitest';
import { validateSpecialUsableGroupDirectives } from './registration-setting-validation';

const ERROR = 'Enter valid bounded special usable-group directives.';

describe('special usable-group directive validation', () => {
  it('accepts the reference add/remove syntax and empty/default values', () => {
    expect(validateSpecialUsableGroupDirectives('')).toBeNull();
    expect(validateSpecialUsableGroupDirectives('{}')).toBeNull();
    expect(validateSpecialUsableGroupDirectives(`{
      "default": {"+:vip": "VIP", "-:blocked": "", "team": "Team"},
      "staff": {}
    }`)).toBeNull();
  });

  it('rejects duplicate, conflicting, malformed, and unsafe directives', () => {
    for (const source of [
      'null',
      '[]',
      '{"default":null}',
      '{"default":{"vip":1}}',
      '{"default":{"vip":"one","vip":"two"}}',
      String.raw`{"default":{"vip":"one","\u0076ip":"two"}}`,
      '{"default":{},"default":{}}',
      '{"default":{"vip":"one","+:vip":"two"}}',
      '{"default":{"+:vip":"one","-:vip":"two"}}',
      '{"default":{"+:+:vip":"nested"}}',
      '{" default":{"vip":"trim"}}',
      '{"default":{" vip":"trim"}}',
      '{"default":{"vip":"line\\nbreak"}}',
      '{"default":{"vip":"bidi\\u202evalue"}}',
      '{"default":{"vip":"ok"},}',
      '{"default":{"vip":"ok",}}',
      '{"default":{"vip":"ok"}} trailing',
    ]) expect(validateSpecialUsableGroupDirectives(source), source).toBe(ERROR);
  });

  it('enforces decoded byte and cardinality bounds', () => {
    const longIdentifier = 'g'.repeat(65);
    const longDescription = 'd'.repeat(257);
    expect(validateSpecialUsableGroupDirectives(JSON.stringify({ [longIdentifier]: {} }))).toBe(ERROR);
    expect(validateSpecialUsableGroupDirectives(JSON.stringify({ default: { [longIdentifier]: 'bad' } }))).toBe(ERROR);
    expect(validateSpecialUsableGroupDirectives(JSON.stringify({ default: { vip: longDescription } }))).toBe(ERROR);

    const tooManyRules = Object.fromEntries(Array.from({ length: 65 }, (_, index) => [`g${index}`, 'group']));
    expect(validateSpecialUsableGroupDirectives(JSON.stringify({ default: tooManyRules }))).toBe(ERROR);
    const tooManyUserGroups = Object.fromEntries(Array.from({ length: 257 }, (_, index) => [`u${index}`, {}]));
    expect(validateSpecialUsableGroupDirectives(JSON.stringify(tooManyUserGroups))).toBe(ERROR);
    const sixtyFourRules = Object.fromEntries(Array.from({ length: 64 }, (_, index) => [`g${index}`, '']));
    const tooManyTotalRules = Object.fromEntries(Array.from({ length: 65 }, (_, index) => [`u${index}`, sixtyFourRules]));
    expect(validateSpecialUsableGroupDirectives(JSON.stringify(tooManyTotalRules))).toBe(ERROR);
    expect(validateSpecialUsableGroupDirectives(`{"default":{"vip":"${'d'.repeat(64 * 1024)}"}}`)).toBe(ERROR);
  });
});
