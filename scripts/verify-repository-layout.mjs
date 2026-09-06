#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, '..');
const manifestPath = path.join(repositoryRoot, 'scripts', 'manifests', 'go-tests.json');

const legacyPrefixes = [
  'common/',
  'constant/',
  'controller/',
  'dto/',
  'middleware/',
  'model/',
  'pkg/',
  'relay/',
  'router/',
  'service/',
  'setting/',
  'views/',
  'lib/',
  'web/src/views/',
  'web/src/lib/',
];

const requiredGoAreas = [
  'cmd/tokenrouter/',
  'internal/app/',
  'internal/auth/',
  'internal/billing/',
  'internal/channels/',
  'internal/users/',
  'internal/settings/',
  'internal/store/',
  'internal/operations/',
  'internal/httpapi/handlers/',
  'internal/httpapi/router/',
  'internal/relay/contract/',
  'internal/relay/providers/',
  'internal/relay/engine/',
  'internal/relay/tasks/',
  'internal/testutil/',
  'protocolkit/',
];

function repositoryFiles(root) {
  const output = execFileSync(
    process.execPath,
    [path.join(scriptDirectory, 'list-source-files.mjs'), root],
    { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 },
  );
  // Share the fail-closed source enumeration used by formatting and secrets:
  // only Git-confirmed deletions may disappear, and unreadable/replaced files
  // must remain visible to the checks below rather than silently dropping out.
  return output.split('\0').filter(Boolean);
}

function readRepositoryFile(relativeName) {
  return fs.readFileSync(path.join(repositoryRoot, ...relativeName.split('/')), 'utf8');
}

function parseModulePath(goModText, fileName) {
  const match = goModText.match(/(?:^|\n)[ \t]*module[ \t]+([^\s]+)[ \t]*(?:\n|$)/);
  if (!match) throw new Error('cannot find a module directive in ' + fileName);
  return match[1];
}

function stripGoComments(source) {
  let result = '';
  let state = 'code';
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const character = source[index];
    const next = source[index + 1];

    if (state === 'code') {
      if (character === '/' && next === '/') {
        result += '  ';
        index += 1;
        state = 'line-comment';
      } else if (character === '/' && next === '*') {
        result += '  ';
        index += 1;
        state = 'block-comment';
      } else {
        result += character;
        if (character === '"') {
          state = 'string';
          escaped = false;
        } else if (character === "'") {
          state = 'rune';
          escaped = false;
        } else if (character.charCodeAt(0) === 96) {
          state = 'raw-string';
        }
      }
      continue;
    }

    if (state === 'line-comment') {
      if (character === '\n') {
        result += '\n';
        state = 'code';
      } else {
        result += ' ';
      }
      continue;
    }

    if (state === 'block-comment') {
      if (character === '*' && next === '/') {
        result += '  ';
        index += 1;
        state = 'code';
      } else {
        result += character === '\n' ? '\n' : ' ';
      }
      continue;
    }

    result += character;
    if (state === 'raw-string') {
      if (character.charCodeAt(0) === 96) state = 'code';
      continue;
    }
    if (escaped) {
      escaped = false;
    } else if (character === '\\') {
      escaped = true;
    } else if (
      (state === 'string' && character === '"')
      || (state === 'rune' && character === "'")
    ) {
      state = 'code';
    }
  }
  return result;
}

function maskGoNonCode(source) {
  let result = '';
  let state = 'code';
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const character = source[index];
    const next = source[index + 1];

    if (state === 'code') {
      if (character === '/' && next === '/') {
        result += '  ';
        index += 1;
        state = 'line-comment';
      } else if (character === '/' && next === '*') {
        result += '  ';
        index += 1;
        state = 'block-comment';
      } else if (character === '"') {
        result += ' ';
        state = 'string';
        escaped = false;
      } else if (character === "'") {
        result += ' ';
        state = 'rune';
        escaped = false;
      } else if (character.charCodeAt(0) === 96) {
        result += ' ';
        state = 'raw-string';
      } else {
        result += character;
      }
      continue;
    }

    if (state === 'line-comment') {
      if (character === '\n') {
        result += '\n';
        state = 'code';
      } else {
        result += ' ';
      }
      continue;
    }

    if (state === 'block-comment') {
      if (character === '*' && next === '/') {
        result += '  ';
        index += 1;
        state = 'code';
      } else {
        result += character === '\n' ? '\n' : ' ';
      }
      continue;
    }

    result += character === '\n' ? '\n' : ' ';
    if (state === 'raw-string') {
      if (character.charCodeAt(0) === 96) state = 'code';
      continue;
    }
    if (escaped) {
      escaped = false;
    } else if (character === '\\') {
      escaped = true;
    } else if (
      (state === 'string' && character === '"')
      || (state === 'rune' && character === "'")
    ) {
      state = 'code';
    }
  }
  return result;
}

function extractGoImports(source) {
  const withoutComments = stripGoComments(source);
  const declarationPattern = /(?:^|\n)[ \t]*import[ \t]*(?:\(([\s\S]*?)\)|([^\r\n]+))/g;
  const rawQuote = String.fromCharCode(96);
  const literalPattern = new RegExp(
    '"(?:\\\\.|[^"\\\\])*"|' + rawQuote + '[^' + rawQuote + ']*' + rawQuote,
    'g',
  );
  const imports = [];
  let declaration;
  while ((declaration = declarationPattern.exec(withoutComments)) !== null) {
    const body = declaration[1] === undefined ? declaration[2] : declaration[1];
    let literal;
    literalPattern.lastIndex = 0;
    while ((literal = literalPattern.exec(body)) !== null) {
      const value = literal[0].charCodeAt(0) === 96
        ? literal[0].slice(1, -1)
        : JSON.parse(literal[0]);
      imports.push(value);
    }
  }
  return imports;
}

function findTopLevelTestNames(source) {
  const code = maskGoNonCode(source);
  const declarationPattern = /(?:^|\n)[ \t]*func[ \t\r\n]+(Test[A-Za-z0-9_]*)[ \t\r\n]*\(/g;
  const names = [];
  let match;
  while ((match = declarationPattern.exec(code)) !== null) {
    // TestMain is the package's lifecycle hook, not a runnable test selector.
    if (match[1] !== 'TestMain') names.push(match[1]);
  }
  return names;
}

function selectorForTestFile(fileName, moduleName, testName) {
  const modulePrefix = moduleName === 'protocolkit' ? 'protocolkit/' : '';
  const relativeName = modulePrefix === '' ? fileName : fileName.slice(modulePrefix.length);
  const directory = path.posix.dirname(relativeName);
  const packageSelector = directory === '.' ? './' : './' + directory;
  return packageSelector + ':' + testName;
}

function discoverTestDeclarations(files, readFile) {
  const declarations = { root: [], protocolkit: [] };
  for (const fileName of files) {
    if (!fileName.endsWith('_test.go')) continue;
    const moduleName = fileName.startsWith('protocolkit/') ? 'protocolkit' : 'root';
    for (const testName of findTopLevelTestNames(readFile(fileName))) {
      declarations[moduleName].push({
        selector: selectorForTestFile(fileName, moduleName, testName),
        file: fileName,
      });
    }
  }
  return declarations;
}

function declarationCounts(declarations) {
  const counts = new Map();
  for (const declaration of declarations) {
    const current = counts.get(declaration.selector) || [];
    current.push(declaration.file);
    counts.set(declaration.selector, current);
  }
  return counts;
}

function actualManifest(declarations) {
  return {
    version: 1,
    root: declarations.root.map((item) => item.selector).sort(),
    protocolkit: declarations.protocolkit.map((item) => item.selector).sort(),
  };
}

function sourceDuplicateFailures(declarations) {
  const failures = [];
  for (const moduleName of ['root', 'protocolkit']) {
    for (const [selector, files] of declarationCounts(declarations[moduleName])) {
      if (files.length > 1) {
        failures.push(
          moduleName + ' module declares ' + selector + ' ' + files.length
          + ' times: ' + files.join(', '),
        );
      }
    }
  }
  return failures;
}

function selectorIsCanonical(selector) {
  if (typeof selector !== 'string') return false;
  const separator = selector.lastIndexOf(':');
  if (separator <= 0) return false;
  const packageSelector = selector.slice(0, separator);
  const testName = selector.slice(separator + 1);
  if (!/^Test[A-Za-z0-9_]*$/.test(testName)) return false;
  if (packageSelector === './') return true;
  if (!packageSelector.startsWith('./')) return false;
  const segments = packageSelector.slice(2).split('/');
  return segments.length > 0
    && segments.every((segment) => segment !== '' && segment !== '.' && segment !== '..'
      && /^[A-Za-z0-9_.-]+$/.test(segment));
}

function manifestFailures(manifest, actual) {
  const failures = [];
  if (manifest === null || typeof manifest !== 'object' || Array.isArray(manifest)) {
    return ['test manifest must be a JSON object'];
  }
  const keys = Object.keys(manifest).sort();
  const expectedKeys = ['protocolkit', 'root', 'version'];
  if (JSON.stringify(keys) !== JSON.stringify(expectedKeys)) {
    failures.push('test manifest keys must be exactly version, root, and protocolkit');
  }
  if (manifest.version !== 1) failures.push('test manifest version must be 1');

  for (const moduleName of ['root', 'protocolkit']) {
    const expected = manifest[moduleName];
    if (!Array.isArray(expected)) {
      failures.push('test manifest ' + moduleName + ' entry must be an array');
      continue;
    }
    const invalid = expected.filter((selector) => !selectorIsCanonical(selector));
    if (invalid.length > 0) {
      failures.push(
        'test manifest ' + moduleName + ' contains invalid selector(s): '
        + invalid.map((item) => JSON.stringify(item)).join(', '),
      );
    }
    const counts = new Map();
    for (const selector of expected) counts.set(selector, (counts.get(selector) || 0) + 1);
    const duplicates = [...counts]
      .filter(([, count]) => count > 1)
      .map(([selector, count]) => selector + ' (' + count + ')');
    if (duplicates.length > 0) {
      failures.push(
        'test manifest ' + moduleName + ' contains duplicate selector(s): '
        + duplicates.join(', '),
      );
    }
    const sorted = [...expected].sort();
    if (JSON.stringify(expected) !== JSON.stringify(sorted)) {
      failures.push('test manifest ' + moduleName + ' selectors are not sorted; use --update-manifest');
    }

    const expectedSet = new Set(expected.filter((item) => typeof item === 'string'));
    const actualSet = new Set(actual[moduleName]);
    const stale = [...expectedSet].filter((selector) => !actualSet.has(selector)).sort();
    const missing = [...actualSet].filter((selector) => !expectedSet.has(selector)).sort();
    if (stale.length > 0) {
      failures.push(
        'test manifest ' + moduleName + ' contains stale/moved selector(s): '
        + summarizeItems(stale),
      );
    }
    if (missing.length > 0) {
      failures.push(
        'source contains unmanifested ' + moduleName + ' test selector(s): '
        + summarizeItems(missing),
      );
    }
  }
  return failures;
}

function summarizeItems(items, limit = 20) {
  const shown = items.slice(0, limit).join(', ');
  return items.length > limit ? shown + ' ... (' + (items.length - limit) + ' more)' : shown;
}

function legacyLayoutFailures(files) {
  const failures = [];
  for (const prefix of legacyPrefixes) {
    const matches = files.filter((fileName) => fileName.startsWith(prefix));
    if (matches.length > 0) {
      failures.push('legacy path ' + prefix + ' still contains repository files: ' + summarizeItems(matches));
    }
  }
  if (files.includes('main.go')) failures.push('legacy root main.go still exists; use cmd/tokenrouter');
  return failures;
}

function requiredLayoutFailures(files) {
  const failures = [];
  for (const prefix of requiredGoAreas) {
    const present = files.some(
      (fileName) => fileName.startsWith(prefix)
        && fileName.endsWith('.go')
        && !fileName.endsWith('_test.go'),
    );
    if (!present) failures.push('required Go area has no production source: ' + prefix);
  }
  return failures;
}

function structuralFailures(files, rootModule, protocolModule) {
  const failures = [
    ...legacyLayoutFailures(files),
    ...requiredLayoutFailures(files),
  ];

  const commandFile = 'cmd/tokenrouter/main.go';
  if (!files.includes(commandFile)) {
    failures.push(commandFile + ' is missing');
  } else {
    const imports = new Set(extractGoImports(readRepositoryFile(commandFile)));
    if (!imports.has(rootModule + '/internal/app')) {
      failures.push(commandFile + ' must import ' + rootModule + '/internal/app');
    }
  }

  const appFiles = files.filter(
    (fileName) => fileName.startsWith('internal/app/')
      && fileName.endsWith('.go')
      && !fileName.endsWith('_test.go'),
  );
  const appSources = appFiles.map(readRepositoryFile);
  const appImports = new Set(appSources.flatMap(extractGoImports));
  if (!appImports.has(rootModule + '/web')) {
    failures.push('internal/app must import the embedded web package');
  }
  if (!appImports.has(rootModule + '/internal/httpapi/router')) {
    failures.push('internal/app must assemble internal/httpapi/router');
  }
  if (!appSources.some((source) => /\bweb\.Dist\b/.test(maskGoNonCode(source)))) {
    failures.push('internal/app must mount web.Dist');
  }

  const embedFile = 'web/embed.go';
  if (!files.includes(embedFile)) {
    failures.push(embedFile + ' is missing');
  } else {
    const embedSource = readRepositoryFile(embedFile);
    if (!/\/\/go:embed[ \t]+dist(?:[ \t]*\n|[ \t]*$)/m.test(embedSource)) {
      failures.push(embedFile + ' must embed web/dist with //go:embed dist');
    }
    if (!/\bvar[ \t]+Dist[ \t]+embed\.FS\b/.test(maskGoNonCode(embedSource))) {
      failures.push(embedFile + ' must expose the embedded filesystem as Dist');
    }
  }

  const rootGoMod = readRepositoryFile('go.mod');
  const escapedProtocolModule = protocolModule.replace(/[.*+?^$\{\}()|[\]\\]/g, '\\$&');
  const requiredProtocol = new RegExp(
    '(?:^|\\n)[ \\t]*(?:require[ \\t]+)?' + escapedProtocolModule + '[ \\t]+v\\S+',
  );
  if (!requiredProtocol.test(rootGoMod)) {
    failures.push('go.mod must require the standalone protocolkit module');
  }
  const replacedProtocol = new RegExp(
    escapedProtocolModule + '[ \\t\\r\\n]*=>[ \\t\\r\\n]*\\./protocolkit(?:[ \\t\\r\\n]|$)',
  );
  if (!replacedProtocol.test(rootGoMod)) {
    failures.push('go.mod must replace the protocolkit module with ./protocolkit');
  }

  const rootProductionGo = files.filter(
    (fileName) => fileName.endsWith('.go')
      && !fileName.endsWith('_test.go')
      && !fileName.startsWith('protocolkit/'),
  );
  let protocolkitImported = false;
  for (const fileName of rootProductionGo) {
    const imports = extractGoImports(readRepositoryFile(fileName));
    if (imports.some(
      (importName) => importName === protocolModule || importName.startsWith(protocolModule + '/'),
    )) {
      protocolkitImported = true;
    }
    if (imports.some(
      (importName) => importName === rootModule + '/internal/testutil'
        || importName.startsWith(rootModule + '/internal/testutil/'),
    )) {
      failures.push('non-test source imports internal/testutil: ' + fileName);
    }
  }
  if (!protocolkitImported) {
    failures.push('root production source must consume the standalone protocolkit module');
  }

  const protocolGo = files.filter(
    (fileName) => fileName.startsWith('protocolkit/') && fileName.endsWith('.go'),
  );
  for (const fileName of protocolGo) {
    for (const importName of extractGoImports(readRepositoryFile(fileName))) {
      const importsRoot = importName === rootModule || importName.startsWith(rootModule + '/');
      const importsProtocolkit = importName === protocolModule
        || importName.startsWith(protocolModule + '/');
      if (importsRoot && !importsProtocolkit) {
        failures.push('protocolkit imports the root module: ' + fileName + ' -> ' + importName);
      }
    }
  }
  return failures;
}

function extractCISelectors(workflowText) {
  return workflowText.match(/\.\/[A-Za-z0-9_.\/-]+:Test[A-Za-z0-9_]*/g) || [];
}

function ciSelectorFailures(selectors, rootDeclarations) {
  const failures = [];
  if (selectors.length === 0) {
    return ['.github/workflows/ci.yml contains no explicit ./package:TestName selector'];
  }
  const counts = declarationCounts(rootDeclarations);
  for (const selector of selectors) {
    const declarations = counts.get(selector) || [];
    if (declarations.length !== 1) {
      failures.push(
        'CI selector ' + selector + ' resolves to ' + declarations.length
        + ' test declarations' + (declarations.length > 0 ? ': ' + declarations.join(', ') : ''),
      );
    }
  }
  return failures;
}

function expectSelfTestFailure(name, failures, fragment) {
  if (!failures.some((failure) => failure.includes(fragment))) {
    throw new Error('self-test ' + name + ' did not report ' + JSON.stringify(fragment));
  }
}

function runSelfTests() {
  const actual = {
    version: 1,
    root: ['./current:TestAlive', './current:TestNew'],
    protocolkit: ['./:TestProtocol'],
  };
  const exact = {
    version: 1,
    root: [...actual.root],
    protocolkit: [...actual.protocolkit],
  };
  if (manifestFailures(exact, actual).length !== 0) {
    throw new Error('self-test exact manifest did not pass');
  }

  const stale = {
    version: 1,
    root: ['./old:TestAlive', './current:TestNew'],
    protocolkit: ['./:TestProtocol'],
  };
  expectSelfTestFailure('stale path', manifestFailures(stale, actual), 'stale/moved');

  const missing = {
    version: 1,
    root: ['./current:TestAlive'],
    protocolkit: ['./:TestProtocol'],
  };
  expectSelfTestFailure('missing test', manifestFailures(missing, actual), 'unmanifested');

  const duplicate = {
    version: 1,
    root: ['./current:TestAlive', './current:TestAlive', './current:TestNew'],
    protocolkit: ['./:TestProtocol'],
  };
  expectSelfTestFailure('duplicate manifest', manifestFailures(duplicate, actual), 'duplicate');

  expectSelfTestFailure(
    'legacy source path',
    legacyLayoutFailures(['controller/stale.go']),
    'legacy path controller/',
  );

  const virtualFiles = {
    'internal/channels/channel_health.go': [
      'package channels',
      'func TestChannelHealth(channelID int) bool { return true }',
    ].join('\n'),
    'internal/channels/channel_health_test.go': [
      'package channels',
      '// func TestCommentOnly(t *testing.T) {}',
      'var example = "func TestStringOnly(t *testing.T) {}"',
      'func TestMain(m *testing.M) {}',
      'func TestChannelHealthLifecycle(t *testing.T) {}',
    ].join('\n'),
  };
  const discovered = discoverTestDeclarations(
    Object.keys(virtualFiles),
    (fileName) => virtualFiles[fileName],
  );
  const discoveredNames = discovered.root.map((item) => item.selector);
  if (
    discoveredNames.length !== 1
    || discoveredNames[0] !== './internal/channels:TestChannelHealthLifecycle'
  ) {
    throw new Error('self-test source scanner included a non-test declaration or masked text');
  }

  const duplicateDeclarations = {
    root: [
      { selector: './current:TestAlive', file: 'current/a_test.go' },
      { selector: './current:TestAlive', file: 'current/b_test.go' },
    ],
    protocolkit: [],
  };
  expectSelfTestFailure(
    'duplicate source declaration',
    sourceDuplicateFailures(duplicateDeclarations),
    'declares ./current:TestAlive 2 times',
  );

  console.log('Repository layout verifier self-test passed.');
}

function printFailures(failures) {
  for (const failure of failures) console.error('repository layout verification failed: ' + failure);
}

function printSummary(manifest, ciSelectors) {
  const uniqueCISelectors = [...new Set(ciSelectors)].sort();
  console.log(
    'Verified repository layout and test inventory: '
    + manifest.root.length + ' root-module tests, '
    + manifest.protocolkit.length + ' standalone protocolkit tests.',
  );
  console.log(
    'Explicit CI selectors (' + uniqueCISelectors.length + ' unique, '
    + ciSelectors.length + ' workflow entries):',
  );
  for (const selector of uniqueCISelectors) console.log('  ' + selector);
}

function usage() {
  console.error(
    'usage: node scripts/verify-repository-layout.mjs '
    + '[--self-test | --update-manifest]',
  );
}

const argumentsList = process.argv.slice(2);
if (argumentsList.length === 1 && argumentsList[0] === '--self-test') {
  runSelfTests();
  process.exit(0);
}
if (
  argumentsList.length > 1
  || (argumentsList.length === 1 && argumentsList[0] !== '--update-manifest')
) {
  usage();
  process.exit(2);
}

try {
  const files = repositoryFiles(repositoryRoot);
  const rootGoMod = readRepositoryFile('go.mod');
  const protocolGoMod = readRepositoryFile('protocolkit/go.mod');
  const rootModule = parseModulePath(rootGoMod, 'go.mod');
  const protocolModule = parseModulePath(protocolGoMod, 'protocolkit/go.mod');
  const declarations = discoverTestDeclarations(files, readRepositoryFile);
  const generatedManifest = actualManifest(declarations);
  const ciSelectors = extractCISelectors(readRepositoryFile('.github/workflows/ci.yml'));
  const failures = [
    ...(protocolModule === rootModule + '/protocolkit'
      ? []
      : ['protocolkit module must be ' + rootModule + '/protocolkit, found ' + protocolModule]),
    ...structuralFailures(files, rootModule, protocolModule),
    ...sourceDuplicateFailures(declarations),
    ...ciSelectorFailures(ciSelectors, declarations.root),
  ];

  if (failures.length > 0) {
    printFailures(failures);
    process.exit(1);
  }

  if (argumentsList[0] === '--update-manifest') {
    fs.mkdirSync(path.dirname(manifestPath), { recursive: true });
    fs.writeFileSync(manifestPath, JSON.stringify(generatedManifest, null, 2) + '\n');
    console.log('Updated ' + path.relative(repositoryRoot, manifestPath) + '.');
    printSummary(generatedManifest, ciSelectors);
    process.exit(0);
  }

  let committedManifest;
  try {
    committedManifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  } catch (error) {
    throw new Error(
      'cannot read ' + path.relative(repositoryRoot, manifestPath) + ': '
      + error.message + '; run with --update-manifest',
    );
  }
  const inventoryFailures = manifestFailures(committedManifest, generatedManifest);
  if (inventoryFailures.length > 0) {
    printFailures(inventoryFailures);
    process.exit(1);
  }
  printSummary(generatedManifest, ciSelectors);
} catch (error) {
  console.error('repository layout verification failed: ' + error.message);
  process.exit(1);
}
