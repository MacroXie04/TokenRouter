#!/usr/bin/env node

// Derive API_MATRIX target verdicts from three independent inputs:
//   1. the read-only reference inventory captured in inventory.json;
//   2. Gin's exact target route table, including runtime handler names; and
//   3. behavioral tests that mention the handler or exercise the route.
//
// Exact paths preserve trailing slashes. Parameter names are canonicalized
// because :id and :user_id select the same Gin path shape; static segments are
// never allowed to satisfy a parameter segment.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
// The original parity matrix is historical evidence. Current source paths and
// route evidence are maintained separately after the package reorganization.
const matrixPath = process.env.TOKENROUTER_API_MATRIX || path.join(repo, 'docs/development/API_MATRIX.md');
const inventoryPath = process.env.TOKENROUTER_API_INVENTORY || path.join(repo, 'docs/parity/inventory.json');
const inventoryMarkdownPath = process.env.TOKENROUTER_API_INVENTORY_MARKDOWN
  || path.join(repo, 'docs/parity/INVENTORY.md');
const summaryPath = process.env.TOKENROUTER_API_SUMMARY || path.join(repo, 'docs/parity/summary.json');
const dumpPath = process.env.TOKENROUTER_ROUTE_DUMP || '/tmp/tokenrouter_routes.txt';
const checkOnly = process.argv.includes('--check') || process.argv.includes('--verify');

const validStatuses = new Set([
  'PASS',
  'IN_PROGRESS',
  'NOT_STARTED',
  'BLOCKED_EXTERNAL',
  'INTENTIONAL_DEVIATION',
  'REFERENCE_PLACEHOLDER',
]);

// If an exact compatibility alias regresses, these alternate contracts explain
// the partial implementation without silently promoting the missing route.
const explicitAlternates = new Map([
  ['GET /api/verification', 'POST /api/verification'],
  ['GET /api/reset_password', 'POST /api/reset_password'],
  ['POST /api/user/2fa/setup', 'POST /api/user/2fa/start'],
  ['GET /api/user/checkin', 'GET /api/user/checkin/status'],
]);

// Manually reviewed tests whose helper indirection, multiline request, or
// dynamically constructed path cannot be inferred safely from one source line.
const testOverrides = new Map([
  ['GET /api/perf-metrics/summary', ['internal/httpapi/router/metrics_test.go']],
  ['GET /api/perf-metrics', ['internal/httpapi/router/metrics_test.go']],
  ['GET /api/verification', ['internal/httpapi/router/reference_aliases_test.go']],
  ['GET /api/reset_password', ['internal/httpapi/router/reference_aliases_test.go']],
  ['GET /api/oauth/:provider', ['internal/httpapi/router/custom_oauth_admin_test.go']],
  ['GET /api/ratio_config', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/user/login/2fa', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/user/passkey/login/begin', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/user/passkey/login/finish', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['GET /api/user/epay/notify', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['GET /api/user/groups', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['GET /api/user/self/groups', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['GET /api/user/models', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/user/2fa/enable', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/user/checkin', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/waffo-pancake/webhook/:env', ['internal/httpapi/router/waffo_pancake_test.go']],
  ['GET /api/models/', ['internal/httpapi/router/auth_misc_routes_test.go']],
  ['POST /api/channel/:id/codex/refresh', ['internal/httpapi/router/codex_admin_test.go']],
  ['GET /api/channel/:id/codex/usage', ['internal/httpapi/router/codex_admin_test.go']],
  ['GET /api/channel/:id/codex/usage/reset-credits', ['internal/httpapi/router/codex_admin_test.go']],
  ['POST /api/channel/:id/codex/usage/reset', ['internal/httpapi/router/codex_admin_test.go']],
  ['POST /api/user/passkey/register/finish', ['internal/httpapi/router/self_service_session_gate_test.go']],
  ['GET /api/channel/:id', ['internal/httpapi/router/secure_verify_test.go']],
  ['POST /api/channel/:id/status', ['internal/httpapi/router/channel_write_test.go']],
  ['POST /api/channel/:id/key', ['internal/httpapi/router/secure_verify_test.go']],
  ['DELETE /api/channel/:id', ['internal/httpapi/router/channel_durability_test.go']],
  ['GET /api/token/:id', ['internal/httpapi/router/token_group_test.go']],
  ['POST /api/token/:id/key', ['internal/httpapi/router/token_group_test.go']],
  ['DELETE /api/token/:id', ['internal/httpapi/router/token_group_test.go']],
  ['DELETE /api/redemption/:id', ['internal/httpapi/router/redemption_admin_test.go']],
  ['DELETE /api/user/:id/bindings/:binding_type', ['internal/httpapi/router/admin_user_routes_test.go']],
  ['DELETE /api/user/:id', ['internal/httpapi/router/admin_user_routes_test.go']],
  ['DELETE /api/user/:id/reset_passkey', ['internal/httpapi/router/auth_adjacent_test.go']],
  ['DELETE /api/user/:id/2fa', ['internal/httpapi/router/auth_adjacent_test.go']],
  ['POST /v1/completions', ['internal/relay/engine/mode_lifecycle_test.go']],
  ['POST /v1/images/generations', ['internal/relay/engine/mode_lifecycle_test.go']],
  ['POST /v1/images/edits', ['internal/relay/engine/multipart_lifecycle_test.go']],
  ['POST /v1/audio/translations', ['internal/relay/engine/multipart_lifecycle_test.go']],
  ['POST /v1/audio/speech', ['internal/relay/engine/mode_lifecycle_test.go']],
  ['POST /v1/rerank', ['internal/relay/engine/mode_lifecycle_test.go']],
  ['POST /v1/engines/:model/embeddings', ['internal/relay/engine/gemini_native_test.go']],
  ['POST /v1/models/*path', ['internal/relay/engine/gemini_native_test.go']],
  ['POST /v1/moderations', ['internal/relay/engine/mode_lifecycle_test.go']],
  ['POST /v1beta/models/*path', ['internal/relay/engine/gemini_native_test.go']],
  ['GET /v1/videos/:task_id/content', ['internal/relay/tasks/video_task_test.go']],
  ['POST /v1/video/generations', ['internal/relay/tasks/video_task_test.go']],
  ['GET /v1/video/generations/:task_id', ['internal/relay/tasks/video_task_test.go']],
  ['POST /v1/videos/:video_id/remix', ['internal/relay/tasks/video_task_test.go']],
  ['POST /v1/videos', ['internal/relay/tasks/video_task_test.go']],
  ['GET /v1/videos/:task_id', ['internal/relay/tasks/video_task_test.go']],
]);

// Broad table-driven overrides and routes that share a dynamic prefix need
// exact test markers so one neighboring case cannot keep another override green.
const testOverrideMarkers = new Map([
  ['GET /api/ratio_config', 'func TestRatioConfigRouteContract'],
  ['POST /api/user/login/2fa', 'func TestTwoFALoginRouteContract'],
  ['POST /api/user/passkey/login/begin', 'func TestPasskeyLoginBeginRouteContract'],
  ['POST /api/user/passkey/login/finish', 'func TestPasskeyLoginFinishRouteContract'],
  ['GET /api/user/epay/notify', 'func TestEpayNotifyGETRouteContract'],
  ['GET /api/user/groups', 'func TestPublicUserGroupsRouteContract'],
  ['GET /api/user/self/groups', 'func TestSelfUserGroupsRouteContract'],
  ['GET /api/user/models', 'func TestUserModelsRouteContract'],
  ['POST /api/user/2fa/enable', 'func TestEnableTwoFARouteContract'],
  ['POST /api/user/checkin', 'func TestCheckInPOSTRouteContract'],
  ['POST /api/waffo-pancake/webhook/:env', 'func TestWaffoPancakeWebhookAvailabilitySignatureAndBodyLimits'],
  ['GET /api/models/', 'func TestGetAllModelsMetaRouteContract'],
  ['POST /api/channel/:id/codex/refresh', 'func TestCodexCredentialRefreshRouteContract'],
  ['GET /api/channel/:id/codex/usage', 'func TestCodexUsageRouteContract'],
  ['GET /api/channel/:id/codex/usage/reset-credits', 'func TestCodexResetCreditsRouteContract'],
  ['POST /api/channel/:id/codex/usage/reset', 'func TestCodexUsageResetRouteContract'],
  ['DELETE /api/user/:id/bindings/:binding_type', 'func TestAdminClearUserBindingRoute'],
  ['DELETE /api/user/:id', 'func TestAdminDeleteUserRouteAndRoleGuard'],
  ['DELETE /api/user/:id/reset_passkey', 'func TestAdminResetPasskey'],
  ['DELETE /api/user/:id/2fa', 'func TestAdminDisable2FA'],
  ['POST /v1/completions', [
    'func TestOpenAIModeLifecycleWireResponseAndAccounting',
    'name: "Completions", clientPath: "/v1/completions", upstreamPath: "/v1/completions"',
  ]],
  ['POST /v1/images/generations', [
    'func TestOpenAIModeLifecycleWireResponseAndAccounting',
    'name: "Image generation", clientPath: "/v1/images/generations", upstreamPath: "/v1/images/generations"',
  ]],
  ['POST /v1/images/edits', [
    'func TestMultipartMediaLifecyclePreservesFilesMapsModelAndSettles',
    'name: "image edit", path: "/v1/images/edits", fileField: "image"',
  ]],
  ['POST /v1/audio/translations', [
    'func TestMultipartMediaLifecyclePreservesFilesMapsModelAndSettles',
    'name: "audio translation", path: "/v1/audio/translations", fileField: "file"',
  ]],
  ['POST /v1/audio/speech', [
    'func TestOpenAIModeLifecycleWireResponseAndAccounting',
    'name: "Audio speech", clientPath: "/v1/audio/speech", upstreamPath: "/v1/audio/speech"',
  ]],
  ['POST /v1/rerank', [
    'func TestOpenAIModeLifecycleWireResponseAndAccounting',
    'name: "Rerank", clientPath: "/v1/rerank", upstreamPath: "/v1/rerank"',
  ]],
  ['POST /v1/models/*path', [
    'func TestGeminiNativeV1RoutePassthroughAndAccounting',
    'httptest.NewRequest(http.MethodPost, "/v1/models/"+modelName+":generateContent"',
  ]],
  ['POST /v1/moderations', [
    'func TestOpenAIModeLifecycleWireResponseAndAccounting',
    'name: "Moderations", clientPath: "/v1/moderations", upstreamPath: "/v1/moderations"',
  ]],
  ['GET /v1/videos/:task_id/content', [
    'func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle',
    'content := fixture.request(http.MethodGet, "/v1/videos/"+created.ID+"/content"',
  ]],
  ['POST /v1/video/generations', [
    'func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle',
    'submit := fixture.request(http.MethodPost, "/v1/video/generations"',
  ]],
  ['GET /v1/video/generations/:task_id', [
    'func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle',
    'legacyFetch := fixture.request(http.MethodGet, "/v1/video/generations/"+created.ID',
  ]],
  ['POST /v1/videos/:video_id/remix', [
    'func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle',
    'remix := fixture.request(http.MethodPost, "/v1/videos/"+created.ID+"/remix"',
  ]],
  ['POST /v1/videos', [
    'func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle',
    'openAISubmit := fixture.request(http.MethodPost, "/v1/videos"',
  ]],
  ['GET /v1/videos/:task_id', [
    'func TestVideoTaskSubmitPollFetchRemixAndContentLifecycle',
    'fetched := fixture.request(http.MethodGet, "/v1/videos/"+created.ID',
  ]],
]);

function fail(message) {
  console.error(`api-matrix: ${message}`);
  process.exit(1);
}

function routeShape(routePath) {
  return routePath
    .replace(/:[A-Za-z0-9_]+/g, ':')
    .replace(/\*[A-Za-z0-9_]+/g, '*');
}

function parseMatrix(text) {
  const lines = text.split('\n');
  const rows = [];
  for (let lineIndex = 0; lineIndex < lines.length; lineIndex += 1) {
    const line = lines[lineIndex];
    if (!/^\| [0-9]+ \|/.test(line)) continue;
    const fields = line.slice(1, -1).split('|').map((field) => field.trim());
    if (fields.length !== 6) fail(`malformed row at line ${lineIndex + 1}`);
    const id = Number(fields[0]);
    let method = '';
    let routePath = '';
    if (fields[1] !== 'NoRoute fallback') {
      const match = fields[1].match(/^([A-Z]+) (\/.*)$/);
      if (!match) fail(`invalid method/path in row ${id}: ${fields[1]}`);
      [, method, routePath] = match;
    }
    rows.push({ id, fields, lineIndex, method, routePath, key: fields[1] });
  }
  return { lines, rows };
}

function validateReferenceInventory(rows) {
  const inventory = JSON.parse(fs.readFileSync(inventoryPath, 'utf8'));
  const routeSection = inventory.find((section) => section.key === 'routes');
  if (!routeSection || !Array.isArray(routeSection.items)) {
    fail('docs/parity/inventory.json has no route inventory');
  }
  if (routeSection.items.length !== rows.length) {
    fail(`reference inventory has ${routeSection.items.length} rows but API_MATRIX has ${rows.length}`);
  }

  const inventoryMarkdown = fs.readFileSync(inventoryMarkdownPath, 'utf8');
  const declaredCount = inventoryMarkdown.match(/^- \*\*HTTP Routes\*\*: ([0-9]+) entries$/m);
  if (!declaredCount || Number(declaredCount[1]) !== routeSection.items.length) {
    fail('INVENTORY.md HTTP route count is stale');
  }
  const routeSectionStart = inventoryMarkdown.indexOf('## HTTP Routes\n');
  const routeSectionEnd = inventoryMarkdown.indexOf('\n## ', routeSectionStart + 1);
  if (routeSectionStart === -1 || routeSectionEnd === -1) {
    fail('INVENTORY.md has no bounded HTTP Routes section');
  }
  const markdownItems = inventoryMarkdown.slice(routeSectionStart, routeSectionEnd)
    .split('\n')
    .filter((line) => /^\| (?:[A-Z]+ \/|NoRoute fallback)/.test(line))
    .map((line) => {
      const fields = line.slice(1, -1).split('|').map((field) => field.trim());
      if (fields.length !== 3) fail(`malformed INVENTORY.md route row: ${line}`);
      return { name: fields[0], detail: fields[1], evidence: fields[2] };
    });
  if (markdownItems.length !== routeSection.items.length) {
    fail(`INVENTORY.md has ${markdownItems.length} routes but inventory.json has ${routeSection.items.length}`);
  }
  markdownItems.forEach((item, index) => {
    const jsonItem = routeSection.items[index];
    if (item.name !== jsonItem.name || item.detail !== jsonItem.detail || item.evidence !== jsonItem.evidence) {
      fail(`INVENTORY.md route ${index + 1} does not match inventory.json`);
    }
  });

  const summary = JSON.parse(fs.readFileSync(summaryPath, 'utf8'));
  if (summary?.counts?.routes !== routeSection.items.length) {
    fail(`summary.json reports ${summary?.counts?.routes ?? 'no'} routes but inventory.json has ${routeSection.items.length}`);
  }

  const seen = new Map();
  rows.forEach((row, index) => {
    if (row.id !== index + 1) fail(`row IDs are not consecutive at ${row.id}`);
    const prior = seen.get(row.key);
    if (prior) fail(`duplicate reference route ${row.key} in rows ${prior} and ${row.id}`);
    seen.set(row.key, row.id);
    const item = routeSection.items[index];
    if (item.name !== row.key) fail(`row ${row.id} does not match docs/parity/inventory.json`);
    row.referenceDetail = item.detail;
    row.referenceEvidence = item.evidence;
    if (!validStatuses.has(row.fields[4])) fail(`row ${row.id} has invalid status ${row.fields[4]}`);
  });
  if (seen.has('POST /:mode/mj/submit/*')) {
    fail('compressed mode-prefixed Midjourney pseudo-route is stale; enumerate exact reference routes');
  }
}

function parseTargetRoutes() {
  if (!fs.existsSync(dumpPath)) fail(`target route dump is missing: ${dumpPath}`);
  const routes = new Map();
  for (const line of fs.readFileSync(dumpPath, 'utf8').trim().split('\n')) {
    if (!line) continue;
    const [method, routePath, handler] = line.split('\t');
    if (!method || !routePath || !handler) fail(`malformed target route dump line: ${line}`);
    const key = `${method} ${routeShape(routePath)}`;
    if (routes.has(key)) fail(`duplicate target route shape: ${method} ${routePath}`);
    routes.set(key, { method, routePath, handler });
  }
  return routes;
}

function walkFiles(directory, predicate, output = []) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) walkFiles(full, predicate, output);
    else if (predicate(full)) output.push(full);
  }
  return output;
}

const implementationFiles = [
  ...walkFiles(path.join(repo, 'internal/httpapi'), (file) => file.endsWith('.go') && !file.endsWith('_test.go')),
  ...walkFiles(path.join(repo, 'internal/relay'), (file) => file.endsWith('.go') && !file.endsWith('_test.go')),
  path.join(repo, 'internal/app/bootstrap.go'),
].sort();

const testFiles = ['internal/httpapi', 'internal/relay']
  .flatMap((directory) => walkFiles(path.join(repo, directory), (file) =>
    file.endsWith('_test.go') && !file.endsWith('routes_dump_test.go')))
  .sort()
  .map((file) => ({ file, content: fs.readFileSync(file, 'utf8') }));
const testFilesByRelativePath = new Map(
  testFiles.map((testFile) => [path.relative(repo, testFile.file), testFile]),
);

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function sourceInfo(handler) {
  const qualifiedName = handler.slice(handler.lastIndexOf('/') + 1);
  const separator = qualifiedName.indexOf('.');
  const packageName = separator === -1 ? '' : qualifiedName.slice(0, separator);
  const functionName = separator === -1 ? qualifiedName : qualifiedName.slice(separator + 1);
  const pattern = new RegExp(`^func\\s+${escapeRegExp(functionName)}\\s*\\(`, 'm');
  for (const file of implementationFiles) {
    const relativeFile = path.relative(repo, file);
    const packagePath = handler.slice(0, handler.lastIndexOf('/') + 1) + packageName;
    const relativePackage = packagePath.replace('github.com/tokenrouter/tokenrouter/', '');
    if (path.dirname(relativeFile) !== relativePackage) continue;
    const content = fs.readFileSync(file, 'utf8');
    const match = pattern.exec(content);
    if (!match) continue;
    const line = content.slice(0, match.index).split('\n').length;
    const nextFunction = content.indexOf('\nfunc ', match.index + match[0].length);
    const body = content.slice(match.index, nextFunction === -1 ? content.length : nextFunction);
    return {
      functionName,
      packageName,
      relativeFile,
      line,
      body,
    };
  }
  return { functionName, packageName, relativeFile: '', line: 0, body: '' };
}

function forbiddenStaticSegments(row, targetRoutes) {
  const segments = row.routePath.split('/');
  const forbidden = new Map();
  for (let index = 0; index < segments.length; index += 1) {
    if (!segments[index].startsWith(':')) continue;
    const staticSiblings = new Set();
    for (const candidate of targetRoutes.values()) {
      if (candidate.method !== row.method) continue;
      const candidateSegments = candidate.routePath.split('/');
      const candidateSegment = candidateSegments[index];
      if (!candidateSegment || candidateSegment.startsWith(':') || candidateSegment.startsWith('*')) continue;
      let compatiblePrefix = true;
      for (let prefixIndex = 0; prefixIndex < index; prefixIndex += 1) {
        const expected = segments[prefixIndex];
        if (!expected.startsWith(':') && !expected.startsWith('*') && expected !== candidateSegments[prefixIndex]) {
          compatiblePrefix = false;
          break;
        }
      }
      if (compatiblePrefix) staticSiblings.add(candidateSegment);
    }
    if (staticSiblings.size > 0) forbidden.set(index, [...staticSiblings].sort());
  }
  return forbidden;
}

function routePattern(routePath, forbidden = new Map()) {
  const routeSource = routePath.split('/').map((segment, index) => {
    if (segment.startsWith(':')) {
      const staticSiblings = forbidden.get(index) || [];
      const negative = staticSiblings.length > 0
        ? `(?!(?:${staticSiblings.map(escapeRegExp).join('|')})(?=$|[/?"'\\x60\\s,)]))`
        : '';
      return `${negative}[^/\\s"'\\x60?]+`;
    }
    if (segment.startsWith('*')) return '[^"\'\\x60\\s]*';
    return escapeRegExp(segment);
  }).join('/');
  // A closing quote followed by `+` means this is only a dynamic-path prefix,
  // not evidence for the shorter literal route (for example `/api/channel/`).
  return `(?:${routeSource}(?=$|[?\\s,)])|${routeSource}(?=["'\\x60](?!\\s*\\+)))`;
}

function testEvidence(row, target, handlerUseCount, targetRoutes) {
  const source = sourceInfo(target.handler);
  const { functionName } = source;
  const handlerIsUnique = handlerUseCount.get(target.handler) === 1;
  const directHandlerRegex = new RegExp(
    `(?:\\b${escapeRegExp(functionName)}\\s*\\(|func\\s+Test[A-Za-z0-9_]*${escapeRegExp(functionName)}\\b|handler\\s*:\\s*${escapeRegExp(functionName)}\\b)`,
  );
  const pathSource = routePattern(row.routePath, forbiddenStaticSegments(row, targetRoutes));
  const methodNames = { GET: 'Get', POST: 'Post', PUT: 'Put', DELETE: 'Delete', PATCH: 'Patch' };
  const methodSource = `(?:http\\.Method${methodNames[row.method] || row.method}|["'\x60]${row.method}["'\x60])`;
  const methodPathRegex = new RegExp(
    `${methodSource}[^\\n]{0,300}${pathSource}|${pathSource}[^\\n]{0,300}${methodSource}`,
  );

  const overridden = testOverrides.get(row.key);
  if (overridden) {
    const literalPrefix = row.routePath.split(/[:*]/, 1)[0];
    const exactMarker = testOverrideMarkers.get(row.key);
    const exactMarkers = Array.isArray(exactMarker)
      ? exactMarker
      : (exactMarker ? [exactMarker] : []);
    for (const relativeFile of overridden) {
      const testFile = testFilesByRelativePath.get(relativeFile);
      if (!testFile) fail(`test override for ${row.key} is missing ${relativeFile}`);
      if (!testFile.content.includes(literalPrefix)
        || !new RegExp(methodSource).test(testFile.content)
        || exactMarkers.some((marker) => !testFile.content.includes(marker))) {
        fail(`test override for ${row.key} is stale: ${relativeFile}`);
      }
    }
    return overridden;
  }

  return testFiles
    .map(({ file, content }) => ({
      file,
      score: methodPathRegex.test(content)
        ? 2
        : (handlerIsUnique && path.dirname(path.relative(repo, file)) === path.dirname(source.relativeFile) && directHandlerRegex.test(content) ? 1 : 0),
    }))
    .filter(({ score }) => score > 0)
    .sort((a, b) => b.score - a.score || a.file.localeCompare(b.file))
    .map(({ file }) => path.relative(repo, file))
    .slice(0, 2);
}

function alternateSlashRoute(row, targetRoutes) {
  if (!row.routePath || row.routePath === '/') return null;
  const alternatePath = row.routePath.endsWith('/')
    ? row.routePath.slice(0, -1)
    : `${row.routePath}/`;
  return targetRoutes.get(`${row.method} ${routeShape(alternatePath)}`) || null;
}

function targetEvidence(target, source, tests) {
  const implementation = source.relativeFile
    ? `${source.relativeFile}:${source.line} (${source.functionName})`
    : target.handler;
  const test = tests.length > 0 ? tests.join(', ') : 'no route-level behavioral test found';
  return { implementation, test };
}

function embeddedWebEvidence() {
  const implementationPath = path.join(repo, 'internal/app/web_static.go');
  const testPath = path.join(repo, 'internal/app/web_static_test.go');
  if (!fs.existsSync(implementationPath) || !fs.existsSync(testPath)) return null;
  const implementation = fs.readFileSync(implementationPath, 'utf8');
  const tests = fs.readFileSync(testPath, 'utf8');
  const implementationMarkers = [
    'func newEmbeddedWebHandler(',
    'func (handler *embeddedWebHandler) ServeGIN(',
    'func (handler *embeddedWebHandler) serveContent(',
    'func injectAnalytics(',
  ];
  const testMarkers = [
    'func TestEmbeddedWebServesRootAndSPAFallback(',
    'func TestEmbeddedWebServesActualAssetWithHTTPMetadata(',
    'func TestEmbeddedWebNegotiatesGzip(',
    'func TestEmbeddedWebStructuredServiceFallbacks(',
    'func TestEmbeddedWebRejectsTraversalAndMalformedPaths(',
    'func TestEmbeddedWebRateLimitIsWebOnly(',
    'func TestEmbeddedWebRateLimitConfigurationIsBoundedAndUsesSeconds(',
    'func TestInjectAnalyticsEscapesAndValidatesConfiguration(',
  ];
  if (!implementationMarkers.every((marker) => implementation.includes(marker))
    || !testMarkers.every((marker) => tests.includes(marker))) return null;
  return {
    implementation: 'internal/app/bootstrap.go (serveEmbedded), internal/app/web_static.go (embeddedWebHandler)',
    test: 'internal/app/web_static_test.go',
  };
}

function classifyRows(rows, targetRoutes) {
  const exactTargets = new Map();
  const handlerUseCount = new Map();
  for (const target of targetRoutes.values()) {
    handlerUseCount.set(target.handler, (handlerUseCount.get(target.handler) || 0) + 1);
  }
  for (const row of rows) {
    if (!row.method || row.routePath === '/ (web static)') continue;
    const target = targetRoutes.get(`${row.method} ${routeShape(row.routePath)}`);
    if (!target) continue;
    exactTargets.set(row.id, target);
  }

  let referencePlaceholderCount = 0;
  const verdicts = new Map();
  const webEvidence = embeddedWebEvidence();
  for (const row of rows) {
    if (row.key === 'NoRoute fallback') {
      verdicts.set(row.id, {
        status: webEvidence ? 'PASS' : 'IN_PROGRESS',
        evidence: webEvidence
          ? `impl: ${webEvidence.implementation}; test: ${webEvidence.test}`
          : 'partial: internal/httpapi/router/router.go and internal/app/bootstrap.go implement API/relay 404 plus SPA fallback; tested embedded web handler is absent',
      });
      continue;
    }
    if (row.routePath === '/ (web static)') {
      verdicts.set(row.id, {
        status: webEvidence ? 'PASS' : 'IN_PROGRESS',
        evidence: webEvidence
          ? `impl: ${webEvidence.implementation}; test: ${webEvidence.test}`
          : 'partial: internal/app/bootstrap.go (serveEmbedded) embeds web/dist; tested embedded web handler is absent',
      });
      continue;
    }

    const referencePlaceholder = row.referenceDetail.includes('RelayNotImplemented');
    if (referencePlaceholder) referencePlaceholderCount += 1;
    const target = exactTargets.get(row.id);
    const referenceHandler = row.referenceDetail.match(/(?:controller|relay)\.[A-Za-z0-9_]+/)?.[0] || row.referenceDetail;

    if (referencePlaceholder) {
      const tests = target ? testEvidence(row, target, handlerUseCount, targetRoutes) : [];
      const matches = target && target.handler.endsWith('.RelayNotImplemented') && tests.length > 0;
      verdicts.set(row.id, {
        status: 'REFERENCE_PLACEHOLDER',
        evidence: matches
          ? `reference and target both use structured RelayNotImplemented 501; test: ${tests.join(', ')}`
          : 'reference exposes RelayNotImplemented 501; exact tested target placeholder behavior is absent',
      });
      continue;
    }

    if (target) {
      const source = sourceInfo(target.handler);
      const tests = testEvidence(row, target, handlerUseCount, targetRoutes);
      const evidence = targetEvidence(target, source, tests);
      const targetIsStub = /taskNotConfigured|StatusNotImplemented|api_not_implemented/.test(source.body);
      if (targetIsStub) {
        verdicts.set(row.id, {
          status: 'NOT_STARTED',
          evidence: `target stub: ${evidence.implementation} returns 501/not-configured; reference ${referenceHandler} is implemented, not a placeholder`,
        });
      } else if (!source.relativeFile || tests.length === 0) {
        verdicts.set(row.id, {
          status: 'IN_PROGRESS',
          evidence: `partial: ${evidence.implementation}; test: ${evidence.test}; PASS withheld`,
        });
      } else {
        verdicts.set(row.id, {
          status: 'PASS',
          evidence: `impl: ${evidence.implementation}; test: ${evidence.test}`,
        });
      }
      continue;
    }

    const explicitAlternate = explicitAlternates.get(row.key);
    if (explicitAlternate) {
      const [alternateMethod, alternatePath] = explicitAlternate.split(' ');
      const alternate = targetRoutes.get(`${alternateMethod} ${routeShape(alternatePath)}`);
      if (!alternate) fail(`documented alternate ${explicitAlternate} for ${row.key} is not registered`);
      const source = sourceInfo(alternate.handler);
      verdicts.set(row.id, {
        status: 'IN_PROGRESS',
        evidence: `partial: target implements ${explicitAlternate} at ${source.relativeFile}:${source.line} (${source.functionName}), but exact reference ${row.key} is absent`,
      });
      continue;
    }

    const slashAlternate = alternateSlashRoute(row, targetRoutes);
    if (slashAlternate) {
      const source = sourceInfo(slashAlternate.handler);
      verdicts.set(row.id, {
        status: 'IN_PROGRESS',
        evidence: `partial: target registers ${slashAlternate.method} ${slashAlternate.routePath} at ${source.relativeFile}:${source.line} (${source.functionName}); exact trailing-slash contract is absent and redirects`,
      });
      continue;
    }

    verdicts.set(row.id, {
      status: 'NOT_STARTED',
      evidence: `missing: no exact target route; reference ${referenceHandler} is implemented and is not a placeholder`,
    });
  }

  if (referencePlaceholderCount !== 12) {
    fail(`expected 12 genuine reference RelayNotImplemented routes, found ${referencePlaceholderCount}`);
  }
  return verdicts;
}

function renderMatrix(parsed, verdicts) {
  const lines = [...parsed.lines];
  for (const row of parsed.rows) {
    const verdict = verdicts.get(row.id);
    if (!verdict) fail(`no verdict for row ${row.id}`);
    const fields = [...row.fields];
    fields[2] = row.referenceDetail;
    fields[3] = row.referenceEvidence;
    fields[4] = verdict.status;
    fields[5] = verdict.evidence;
    lines[row.lineIndex] = `| ${fields.join(' | ')} |`;
  }
  return lines.join('\n');
}

const current = fs.readFileSync(matrixPath, 'utf8');
const parsed = parseMatrix(current);
validateReferenceInventory(parsed.rows);
const targetRoutes = parseTargetRoutes();
const verdicts = classifyRows(parsed.rows, targetRoutes);
const rendered = renderMatrix(parsed, verdicts);

const totals = {};
for (const { status } of verdicts.values()) totals[status] = (totals[status] || 0) + 1;
const referenceHttpRows = parsed.rows.filter((row) => row.method && row.routePath !== '/ (web static)');
const exactTargetMatches = referenceHttpRows.filter((row) =>
  targetRoutes.has(`${row.method} ${routeShape(row.routePath)}`)).length;
const summary = Object.entries(totals)
  .sort(([a], [b]) => a.localeCompare(b))
  .map(([status, count]) => `${status}=${count}`)
  .join(' ');

if (checkOnly) {
  if (rendered !== current) {
    const currentLines = current.split('\n');
    const renderedLines = rendered.split('\n');
    const firstDifference = renderedLines.findIndex((line, index) => line !== currentLines[index]);
    const differingRow = parsed.rows.find((row) => row.lineIndex === firstDifference);
    const rowLabel = differingRow ? `, row ${differingRow.id} ${differingRow.key}` : '';
    const location = firstDifference === -1 ? '' : ` (first difference at line ${firstDifference + 1}${rowLabel})`;
    fail(`API_MATRIX.md is stale${location}; run bash scripts/update-api-matrix.sh`);
  }
  console.log(`API matrix verified: rows=${parsed.rows.length} reference_http_routes=${referenceHttpRows.length} exact_target_matches=${exactTargetMatches} target_routes=${targetRoutes.size} ${summary}`);
} else {
  fs.writeFileSync(matrixPath, rendered);
  console.log(`API matrix updated: rows=${parsed.rows.length} reference_http_routes=${referenceHttpRows.length} exact_target_matches=${exactTargetMatches} target_routes=${targetRoutes.size} ${summary}`);
}
