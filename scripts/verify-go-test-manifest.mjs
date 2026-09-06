#!/usr/bin/env node

import fs from 'node:fs';

function parseExpectedSpec(spec) {
  const separator = spec.lastIndexOf(':');
  if (separator <= 0 || separator === spec.length - 1) {
    throw new Error(`invalid expected test ${JSON.stringify(spec)}; use <import-path>:<test-name>`);
  }
  return {
    packageName: spec.slice(0, separator),
    testName: spec.slice(separator + 1),
    key: spec,
  };
}

function verifyManifest(logText, specs) {
  const expected = new Map();
  for (const rawSpec of specs) {
    const spec = parseExpectedSpec(rawSpec);
    if (expected.has(spec.key)) {
      throw new Error(`duplicate expected test ${spec.key}`);
    }
    expected.set(spec.key, { ...spec, run: 0, pass: 0, fail: 0, skip: 0 });
  }
  if (expected.size === 0) {
    throw new Error('at least one expected test is required');
  }

  const malformedLines = [];
  for (const [index, line] of logText.split(/\r?\n/).entries()) {
    if (line.trim() === '') continue;
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      malformedLines.push(index + 1);
      continue;
    }
    if (typeof event.Package !== 'string' || typeof event.Test !== 'string') continue;
    const state = expected.get(`${event.Package}:${event.Test}`);
    if (!state) continue;
    if (event.Action === 'run') state.run += 1;
    if (event.Action === 'pass') state.pass += 1;
    if (event.Action === 'fail') state.fail += 1;
    if (event.Action === 'skip') state.skip += 1;
  }

  const failures = [];
  if (malformedLines.length > 0) {
    failures.push(`go test -json output contains malformed JSON on line(s): ${malformedLines.join(', ')}`);
  }
  for (const state of expected.values()) {
    if (state.run !== 1) {
      failures.push(`${state.key}: expected exactly one run event, saw ${state.run}`);
    }
    if (state.pass !== 1) {
      failures.push(`${state.key}: expected exactly one pass event, saw ${state.pass}`);
    }
    if (state.skip !== 0) {
      failures.push(`${state.key}: test was skipped`);
    }
    if (state.fail !== 0) {
      failures.push(`${state.key}: test emitted ${state.fail} fail event(s)`);
    }
  }
  return failures;
}

function event(action, packageName, testName) {
  return JSON.stringify({ Time: '2026-09-05T00:00:00Z', Action: action, Package: packageName, Test: testName });
}

function runSelfTest() {
  const specs = ['example.test/model:TestMigration', 'example.test/service:TestAccounting'];
  const good = [
    event('run', 'example.test/model', 'TestMigration'),
    event('pass', 'example.test/model', 'TestMigration'),
    event('run', 'example.test/service', 'TestAccounting'),
    event('pass', 'example.test/service', 'TestAccounting'),
  ].join('\n');

  const cases = [
    { name: 'complete manifest', text: good, shouldPass: true },
    {
      name: 'missing test',
      text: [event('run', 'example.test/model', 'TestMigration'), event('pass', 'example.test/model', 'TestMigration')].join('\n'),
      shouldPass: false,
    },
    {
      name: 'skipped test',
      text: good.replace(event('pass', 'example.test/service', 'TestAccounting'), event('skip', 'example.test/service', 'TestAccounting')),
      shouldPass: false,
    },
    {
      name: 'pass without run',
      text: good.replace(`${event('run', 'example.test/service', 'TestAccounting')}\n`, ''),
      shouldPass: false,
    },
    {
      name: 'failed test',
      text: good.replace(event('pass', 'example.test/service', 'TestAccounting'), event('fail', 'example.test/service', 'TestAccounting')),
      shouldPass: false,
    },
    { name: 'malformed JSON', text: `${good}\nnot-json`, shouldPass: false },
  ];

  for (const testCase of cases) {
    const passed = verifyManifest(testCase.text, specs).length === 0;
    if (passed !== testCase.shouldPass) {
      throw new Error(`self-test case ${JSON.stringify(testCase.name)} produced the wrong result`);
    }
  }
  console.log('Go test manifest verifier self-test passed.');
}

const args = process.argv.slice(2);
if (args[0] === '--self-test') {
  runSelfTest();
  process.exit(0);
}
if (args.length < 2) {
  console.error('usage: verify-go-test-manifest.mjs <go-test-json-log> <import-path:test-name> [...]');
  process.exit(2);
}

let failures;
try {
  failures = verifyManifest(fs.readFileSync(args[0], 'utf8'), args.slice(1));
} catch (error) {
  console.error(`test manifest verification failed: ${error.message}`);
  process.exit(1);
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`test manifest verification failed: ${failure}`);
  process.exit(1);
}
for (const spec of args.slice(1)) console.log(`verified test ran and passed: ${spec}`);
