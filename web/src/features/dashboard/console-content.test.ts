import { describe, expect, it } from 'vitest';
import {
  ConsoleContentContractError,
  parseConsoleAPIInfo,
  parseConsoleFAQ,
  parseConsoleOptionJSON,
  parseConsoleUptimeKumaGroups,
} from './console-content';

describe('console content contracts', () => {
  it('parses the three minimal bounded presentation models', () => {
    expect(parseConsoleAPIInfo([{
      id: 1,
      url: 'https://api.example.test/v1?region=us',
      route: 'Primary',
      description: 'Main API route',
      color: 'light-blue',
    }])).toEqual([{
      id: 1,
      url: 'https://api.example.test/v1?region=us',
      route: 'Primary',
      description: 'Main API route',
      color: 'light-blue',
    }]);
    expect(parseConsoleFAQ([{ question: 'How?', answer: 'Read the guide.\nThen create a token.' }]))
      .toEqual([{ question: 'How?', answer: 'Read the guide.\nThen create a token.' }]);
    expect(parseConsoleUptimeKumaGroups([{
      categoryName: 'Core', url: 'https://status.example.test/base', slug: 'public_status-1', description: '',
    }])).toEqual([{ categoryName: 'Core', url: 'https://status.example.test/base', slug: 'public_status-1' }]);
  });

  it('rejects unknown fields, unsafe URLs, invalid colors, duplicates, and bounds', () => {
    const invalidAPI = [
      [{ url: 'javascript:alert(1)', route: 'Primary', description: 'Main', color: 'blue' }],
      [{ url: 'https://user:secret@example.test', route: 'Primary', description: 'Main', color: 'blue' }],
      [{ url: 'https://api.example.test', route: 'Primary', description: 'Main', color: 'black' }],
      [{ url: 'https://api.example.test', route: 'Primary', description: 'Main', color: 'blue', secret: 'x' }],
      Array.from({ length: 51 }, () => ({ url: 'https://api.example.test', route: 'Primary', description: 'Main', color: 'blue' })),
    ];
    invalidAPI.forEach((value) => expect(() => parseConsoleAPIInfo(value)).toThrow(ConsoleContentContractError));

    expect(() => parseConsoleFAQ([
      { id: 1, question: 'One?', answer: 'One.' },
      { id: 1, question: 'Two?', answer: 'Two.' },
    ])).toThrow(ConsoleContentContractError);
    expect(() => parseConsoleFAQ([{ question: 'bad\u202equestion', answer: 'No.' }]))
      .toThrow(ConsoleContentContractError);
    expect(() => parseConsoleUptimeKumaGroups([
      { categoryName: 'Same', url: 'https://one.example.test', slug: 'one' },
      { categoryName: 'Same', url: 'https://two.example.test', slug: 'two' },
    ])).toThrow(ConsoleContentContractError);
    expect(() => parseConsoleUptimeKumaGroups([
      { categoryName: 'Core', url: 'https://status.example.test?secret=x', slug: 'bad/slug' },
    ])).toThrow(ConsoleContentContractError);
    expect(() => parseConsoleUptimeKumaGroups([
      { categoryName: 'Core', url: 'http://status.example.test', slug: 'public' },
    ])).toThrow(ConsoleContentContractError);
  });

  it('parses option JSON once and refuses malformed or oversized input', () => {
    expect(parseConsoleOptionJSON('[{"question":"Q?","answer":"A."}]', parseConsoleFAQ))
      .toEqual([{ question: 'Q?', answer: 'A.' }]);
    expect(() => parseConsoleOptionJSON('{bad', parseConsoleFAQ)).toThrow(ConsoleContentContractError);
    expect(() => parseConsoleOptionJSON(`"${'x'.repeat(1024 * 1024)}"`, parseConsoleFAQ))
      .toThrow(ConsoleContentContractError);
  });
});
