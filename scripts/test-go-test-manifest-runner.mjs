#!/usr/bin/env node

// Exercise the real shell runner and strict verifier without compiling Go or
// contacting a module proxy. Only the Go CLI is replaced by a temporary stub.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const runner = path.join(repositoryRoot, 'scripts', 'run-go-test-manifest.sh');
const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'tokenrouter-manifest-runner-test-'));
const selectors = [
  './internal/store:TestMigration',
  './internal/store:TestRetention',
  './internal/billing:TestAccounting',
];
const expectedGoArguments = [
  'test', '-json', '-p', '1', '-count=1', '-run',
  '^(TestMigration|TestRetention|TestAccounting)$',
  './internal/store', './internal/billing',
];

function event(action, packageName, testName) {
  return JSON.stringify({
    Time: '2026-09-06T00:00:00Z',
    Action: action,
    Package: `example.test/tokenrouter/${packageName}`,
    Test: testName,
  });
}

const passingEvents = [
  event('run', 'internal/store', 'TestMigration'),
  event('pass', 'internal/store', 'TestMigration'),
  event('run', 'internal/store', 'TestRetention'),
  event('pass', 'internal/store', 'TestRetention'),
  event('run', 'internal/billing', 'TestAccounting'),
  event('pass', 'internal/billing', 'TestAccounting'),
];
const downloadMessages = 'go: downloading example.test/dependency v1.2.3\n'
  + 'go: warning: a diagnostic belongs on stderr\n';

const cases = [
  { name: 'complete manifest and deduplicated packages', status: 0 },
  {
    name: 'cold-cache downloads and warnings stay outside JSON',
    status: 0,
    testStderr: downloadMessages,
    listStderr: 'go: downloading example.test/list-dependency v4.5.6\n',
  },
  {
    name: 'Go command failure overrides passing events',
    status: 1,
    testStatus: 23,
    diagnostic: /gate failed \(go=23, tee=0, verify=0\)/,
  },
  {
    name: 'package resolution failure stops before testing',
    status: 9,
    listStatus: 9,
    listStderr: 'go: package resolution failed\n',
    noTestCommand: true,
  },
  {
    name: 'missing expected test',
    status: 1,
    events: passingEvents.slice(0, -2),
    diagnostic: /TestAccounting: expected exactly one run event, saw 0/,
  },
  {
    name: 'skipped expected test',
    status: 1,
    events: [...passingEvents.slice(0, -1), event('skip', 'internal/billing', 'TestAccounting')],
    diagnostic: /TestAccounting: test was skipped/,
  },
  {
    name: 'duplicate run event',
    status: 1,
    events: [...passingEvents, event('run', 'internal/store', 'TestMigration')],
    diagnostic: /TestMigration: expected exactly one run event, saw 2/,
  },
  {
    name: 'duplicate pass event',
    status: 1,
    events: [...passingEvents, event('pass', 'internal/store', 'TestMigration')],
    diagnostic: /TestMigration: expected exactly one pass event, saw 2/,
  },
  {
    name: 'fail event overrides passing events',
    status: 1,
    events: [...passingEvents, event('fail', 'internal/store', 'TestMigration')],
    diagnostic: /TestMigration: test emitted 1 fail event/,
  },
  {
    name: 'malformed stdout remains a strict failure',
    status: 1,
    events: [...passingEvents, 'go: non-JSON output on stdout is not allowed'],
    diagnostic: /malformed JSON on line\(s\): 7/,
  },
  { name: 'no selectors', status: 2, selectors: [], noGoCommand: true, diagnostic: /usage:/ },
  {
    name: 'selector without separator',
    status: 2,
    selectors: ['./internal/store'],
    noGoCommand: true,
    diagnostic: /invalid test manifest entry/,
  },
  {
    name: 'selector without package',
    status: 2,
    selectors: [':TestMigration'],
    noGoCommand: true,
    diagnostic: /invalid test manifest entry/,
  },
  {
    name: 'selector with invalid test name',
    status: 2,
    selectors: ['./internal/store:NotATest'],
    noGoCommand: true,
    diagnostic: /invalid test manifest entry/,
  },
  {
    name: 'duplicate expected selector',
    status: 1,
    selectors: [selectors[0], selectors[0]],
    diagnostic: /duplicate expected test example\.test\/tokenrouter\/internal\/store:TestMigration/,
  },
];

try {
  const binaryDirectory = path.join(fixtureRoot, 'bin');
  fs.mkdirSync(binaryDirectory);
  fs.writeFileSync(path.join(binaryDirectory, 'go'), `#!/usr/bin/env node
const fs = require('node:fs');
const fixture = JSON.parse(fs.readFileSync(process.env.TOKENROUTER_MANIFEST_RUNNER_FIXTURE, 'utf8'));
const args = process.argv.slice(2);
fs.appendFileSync(fixture.calls, JSON.stringify(args) + '\\n');
if (args[0] === 'list') {
  process.stderr.write(fixture.listStderr || '');
  if (!fixture.listStatus) {
    process.stdout.write('example.test/tokenrouter/' + args.at(-1).replace(/^\\.\\//, '') + '\\n');
  }
  process.exitCode = fixture.listStatus || 0;
} else if (args[0] === 'test') {
  process.stderr.write(fixture.testStderr || '');
  process.stdout.write(fixture.stdout);
  process.exitCode = fixture.testStatus || 0;
} else {
  process.stderr.write('unexpected stub Go invocation\\n');
  process.exitCode = 99;
}
`, { mode: 0o755 });

  for (const [index, testCase] of cases.entries()) {
    const caseDirectory = path.join(fixtureRoot, `case-${index}`);
    const temporaryDirectory = path.join(caseDirectory, 'tmp');
    fs.mkdirSync(temporaryDirectory, { recursive: true });
    const fixturePath = path.join(caseDirectory, 'fixture.json');
    const callsPath = path.join(caseDirectory, 'calls.jsonl');
    fs.writeFileSync(fixturePath, JSON.stringify({
      calls: callsPath,
      stdout: `${(testCase.events ?? passingEvents).join('\n')}\n`,
      testStderr: testCase.testStderr,
      testStatus: testCase.testStatus,
      listStderr: testCase.listStderr,
      listStatus: testCase.listStatus,
    }));
    const result = spawnSync('bash', [runner, ...(testCase.selectors ?? selectors)], {
      cwd: caseDirectory,
      encoding: 'utf8',
      timeout: 10_000,
      maxBuffer: 1024 * 1024,
      env: {
        ...process.env,
        PATH: [binaryDirectory, path.dirname(process.execPath), process.env.PATH ?? ''].join(path.delimiter),
        TMPDIR: temporaryDirectory,
        TOKENROUTER_MANIFEST_RUNNER_FIXTURE: fixturePath,
      },
    });
    const context = `${testCase.name}\nstdout:\n${result.stdout}\nstderr:\n${result.stderr}`;
    assert.ifError(result.error);
    assert.equal(result.signal, null, context);
    assert.equal(result.status, testCase.status, context);
    if (testCase.diagnostic) assert.match(result.stderr, testCase.diagnostic, context);
    if (testCase.listStderr) assert.ok(result.stderr.includes(testCase.listStderr), context);
    if (testCase.testStderr) {
      assert.ok(result.stderr.includes(testCase.testStderr), context);
      assert.ok(!result.stdout.includes(testCase.testStderr), context);
    }

    const calls = fs.existsSync(callsPath)
      ? fs.readFileSync(callsPath, 'utf8').trim().split('\n').map(line => JSON.parse(line))
      : [];
    if (testCase.noGoCommand) assert.deepEqual(calls, [], context);
    const testCommands = calls.filter(args => args[0] === 'test');
    if (testCase.noGoCommand || testCase.noTestCommand) {
      assert.deepEqual(testCommands, [], context);
    } else {
      assert.equal(testCommands.length, 1, context);
    }
    if (testCase.status === 0) {
      assert.deepEqual(testCommands[0], expectedGoArguments, context);
      assert.equal(calls.filter(args => args[0] === 'list').length, selectors.length, context);
      assert.equal((result.stdout.match(/verified test ran and passed:/g) ?? []).length, selectors.length, context);
    }
    assert.deepEqual(fs.readdirSync(temporaryDirectory), [], `${testCase.name}: runner leaked its temporary JSON log`);
  }
  console.log(`Go test manifest runner regression tests passed (${cases.length} cases).`);
} finally {
  fs.rmSync(fixtureRoot, { recursive: true, force: true });
}
