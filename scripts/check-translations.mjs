#!/usr/bin/env node
/* global console, process */
// Verifies locale shape, key parity, localized copy, interpolation tokens, and a
// deterministic synchronization report.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const BASE_FILE = 'en.json';
const REPORT_RELATIVE_PATH = '_reports/_sync-report.json';
const MAX_STATIC_VALUES = 10_000;
const scriptDir = path.dirname(fileURLToPath(import.meta.url));

// A localized value may legitimately match English when it is a product name,
// protocol term, role identifier, or a word whose natural translation is the
// same spelling. Keep this list narrow so copied English UI text fails CI.
const COMMON_IDENTICAL_KEYS = [
  '{{count}} {{unit}}',
  '{{path}}: {{value}} {{metric}}',
  'Bark',
  'Claude',
  'CNY',
  'Creem',
  'Discord',
  'Epay',
  'Gemini',
  'GitHub',
  'Gotify',
  'Grok',
  'ID',
  'LinuxDO',
  'OAuth',
  'OIDC',
  'Root',
  'Stripe',
  'Telegram',
  'Top P',
  'Uptime Kuma',
  'Waffo',
  'Waffo Pancake',
  'Waffo Pancake: {{id}}',
  'Webhook',
  'Webhook URL',
  'WeChat',
];

const IDENTICAL_VALUE_ALLOWLIST = new Map([
  [
    'fr.json',
    new Set([
      ...COMMON_IDENTICAL_KEYS,
      '5 minutes',
      'Action',
      'Actions',
      'Assistant',
      'Code',
      'Conversation',
      'Description',
      'Exact',
      'FAQ',
      'Instance',
      'Message',
      'Minimum {{amount}}',
      'Minute',
      'Pagination',
      'Quota',
      'Source',
      'Standard',
      'TTFT',
      'Total',
      'Type',
      'Webhook (production)',
      'Webhook (test)',
      'quota',
    ]),
  ],
  ['ja.json', new Set(COMMON_IDENTICAL_KEYS)],
  ['ru.json', new Set(COMMON_IDENTICAL_KEYS)],
  ['vi.json', new Set([...COMMON_IDENTICAL_KEYS, 'Email', 'Token #{{id}}'])],
  ['zh-TW.json', new Set(COMMON_IDENTICAL_KEYS)],
  ['zh.json', new Set(COMMON_IDENTICAL_KEYS)],
]);

function sorted(values) {
  return [...values].sort((left, right) => left.localeCompare(right));
}

export function interpolationTokens(value) {
  if (typeof value !== 'string') return [];
  return [...value.matchAll(/\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}/gu)]
    .map((match) => match[1])
    .sort((left, right) => left.localeCompare(right));
}

export function hasValidInterpolationSyntax(value) {
  if (typeof value !== 'string') return false;
  const withoutTokens = value.replace(/\{\{\s*[A-Za-z0-9_.-]+\s*\}\}/gu, '');
  return !withoutTokens.includes('{{') && !withoutTokens.includes('}}');
}

export function findDuplicateKeys(source) {
  const seen = new Set();
  const duplicates = new Set();
  for (const match of source.matchAll(/("(?:\\.|[^"\\])*")\s*:/gu)) {
    const key = JSON.parse(match[1]);
    if (seen.has(key)) duplicates.add(key);
    seen.add(key);
  }
  return sorted(duplicates);
}

export function analyzeLocale(base, data, allowedIdenticalKeys = new Set()) {
  const baseKeys = new Set(Object.keys(base));
  const keys = new Set(Object.keys(data));
  const sharedKeys = [...baseKeys].filter((key) => keys.has(key));
  const invalidValues = [...keys].filter(
    (key) => typeof data[key] !== 'string' || data[key].trim().length === 0,
  );
  const interpolationMismatches = sharedKeys.filter(
    (key) =>
      typeof data[key] === 'string' &&
      (!hasValidInterpolationSyntax(base[key]) ||
        !hasValidInterpolationSyntax(data[key]) ||
        JSON.stringify(interpolationTokens(base[key])) !== JSON.stringify(interpolationTokens(data[key]))),
  );
  const untranslated = sharedKeys.filter(
    (key) =>
      typeof data[key] === 'string' &&
      data[key] === base[key] &&
      !allowedIdenticalKeys.has(key),
  );
  return {
    missing: sorted([...baseKeys].filter((key) => !keys.has(key))),
    extras: sorted([...keys].filter((key) => !baseKeys.has(key))),
    invalidValues: sorted(invalidValues),
    interpolationMismatches: sorted(interpolationMismatches),
    untranslated: sorted(untranslated),
  };
}

export function buildReport(localeFiles, duplicateKeysByFile = new Map()) {
  const base = localeFiles.get(BASE_FILE);
  if (!base) throw new Error(`missing base locale ${BASE_FILE}`);
  const locales = {};
  for (const file of sorted(localeFiles.keys())) {
    const data = localeFiles.get(file);
    const allowedIdenticalKeys =
      file === BASE_FILE ? new Set(Object.keys(base)) : (IDENTICAL_VALUE_ALLOWLIST.get(file) ?? new Set());
    const result = analyzeLocale(base, data, allowedIdenticalKeys);
    locales[file.slice(0, -'.json'.length)] = {
      file,
      keyCount: Object.keys(data).length,
      duplicateKeyCount: (duplicateKeysByFile.get(file) ?? []).length,
      missingCount: result.missing.length,
      extrasCount: result.extras.length,
      invalidValueCount: result.invalidValues.length,
      untranslatedCount: result.untranslated.length,
      interpolationMismatchCount: result.interpolationMismatches.length,
    };
  }
  return { base: BASE_FILE, keyCount: Object.keys(base).length, locales };
}

function canonicalJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function runtimeSourceFiles(root) {
  const files = [];
  const visit = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const absolute = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        if (entry.name !== 'locales' && entry.name !== 'node_modules') visit(absolute);
        continue;
      }
      if (!entry.isFile() || !/\.(?:ts|tsx)$/u.test(entry.name)) continue;
      if (/\.(?:test|spec)\.(?:ts|tsx)$/u.test(entry.name) || entry.name.endsWith('.d.ts')) continue;
      files.push(absolute);
    }
  };
  visit(root);
  return sorted(files);
}

function createTranslationProgram(ts, sourceByFile) {
  const options = {
    jsx: ts.JsxEmit.Preserve,
    module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    skipLibCheck: true,
    target: ts.ScriptTarget.Latest,
  };
  const normalizedSources = new Map(
    [...sourceByFile].map(([file, source]) => [path.resolve(file), source]),
  );
  const host = ts.createCompilerHost(options, true);
  const hostFileExists = host.fileExists.bind(host);
  const hostReadFile = host.readFile.bind(host);
  const hostGetSourceFile = host.getSourceFile.bind(host);
  const hostDirectoryExists = host.directoryExists?.bind(host);
  host.fileExists = (file) => normalizedSources.has(path.resolve(file)) || hostFileExists(file);
  host.readFile = (file) => normalizedSources.get(path.resolve(file)) ?? hostReadFile(file);
  host.directoryExists = (directory) => {
    const absolute = path.resolve(directory);
    return [...normalizedSources.keys()].some(
      (file) => path.dirname(file) === absolute || file.startsWith(`${absolute}${path.sep}`),
    ) || (hostDirectoryExists?.(directory) ?? false);
  };
  host.getSourceFile = (file, languageVersion, onError, shouldCreateNewSourceFile) => {
    const source = normalizedSources.get(path.resolve(file));
    if (source === undefined) {
      return hostGetSourceFile(file, languageVersion, onError, shouldCreateNewSourceFile);
    }
    return ts.createSourceFile(
      file,
      source,
      languageVersion,
      true,
      file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
    );
  };
  return ts.createProgram({ rootNames: [...normalizedSources.keys()], options, host });
}

function isFunctionNode(ts, node) {
  return ts.isFunctionDeclaration(node) || ts.isFunctionExpression(node) || ts.isArrowFunction(node) ||
    ts.isMethodDeclaration(node) || ts.isGetAccessorDeclaration(node) || ts.isSetAccessorDeclaration(node) ||
    ts.isConstructorDeclaration(node);
}

function unwrapStaticExpression(ts, expression) {
  let current = expression;
  while (
    current &&
    (ts.isParenthesizedExpression(current) || ts.isAsExpression(current) ||
      ts.isTypeAssertionExpression(current) || ts.isNonNullExpression(current) ||
      ts.isSatisfiesExpression(current))
  ) {
    current = current.expression;
  }
  return current;
}

function staticPropertyName(ts, name) {
  if (!name) return undefined;
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name)) return name.text;
  if (ts.isComputedPropertyName(name)) {
    const expression = unwrapStaticExpression(ts, name.expression);
    if (ts.isStringLiteral(expression) || ts.isNumericLiteral(expression)) return expression.text;
  }
  return undefined;
}

function literalTranslationUsage(ts, files, sourceRoot, suppliedSources) {
  const sourceByFile = suppliedSources ?? new Map(files.map((file) => [file, fs.readFileSync(file, 'utf8')]));
  const program = createTranslationProgram(ts, sourceByFile);
  const checker = program.getTypeChecker();
  const locations = new Map();
  const directLiteralKeys = new Set();
  const symbolCache = new Map();

  const bounded = (values) => {
    if (values.length > MAX_STATIC_VALUES) {
      throw new Error(`static translation evaluation exceeded ${MAX_STATIC_VALUES} values`);
    }
    return values;
  };
  const append = (target, values) => {
    for (const value of values) {
      if (target.length >= MAX_STATIC_VALUES) {
        throw new Error(`static translation evaluation exceeded ${MAX_STATIC_VALUES} values`);
      }
      target.push(value);
    }
    return target;
  };
  const stringValue = (value, node) => ({ kind: 'string', value, node });
  const arrayValue = (elements, node) => ({ kind: 'array', elements: bounded(elements), node });
  const objectValue = (properties, node) => ({ kind: 'object', properties, node });

  const resolvedSymbol = (node) => {
    let symbol = checker.getSymbolAtLocation(node);
    if (!symbol) return undefined;
    if (symbol.flags & ts.SymbolFlags.Alias) {
      try {
        symbol = checker.getAliasedSymbol(symbol);
      } catch {
        return undefined;
      }
    }
    return symbol;
  };

  const objectPropertyValues = (values, propertyName) => {
    const resolved = [];
    for (const value of values) {
      if (value.kind !== 'object') continue;
      append(resolved, value.properties.get(propertyName) ?? []);
    }
    return resolved;
  };

  const arrayElements = (values) => {
    const resolved = [];
    for (const value of values) {
      if (value.kind === 'array') append(resolved, value.elements);
    }
    return resolved;
  };

  let evaluate;
  let evaluateSymbol;
  const bindPattern = (name, values, environment) => {
    if (!name) return;
    if (ts.isIdentifier(name)) {
      const symbol = resolvedSymbol(name);
      if (symbol) environment.set(symbol, bounded(values));
      return;
    }
    if (ts.isObjectBindingPattern(name)) {
      for (const element of name.elements) {
        if (element.dotDotDotToken) continue;
        const propertyName = staticPropertyName(ts, element.propertyName ?? element.name);
        let propertyValues = propertyName === undefined ? [] : objectPropertyValues(values, propertyName);
        if (propertyValues.length === 0 && element.initializer) {
          propertyValues = evaluate(element.initializer, environment);
        }
        bindPattern(element.name, propertyValues, environment);
      }
      return;
    }
    if (ts.isArrayBindingPattern(name)) {
      const elements = arrayElements(values);
      name.elements.forEach((element, index) => {
        if (!ts.isBindingElement(element)) return;
        const selected = elements[index] ? [elements[index]] : [];
        bindPattern(element.name, selected, environment);
      });
    }
  };

  const returnExpressions = (functionNode) => {
    if (!functionNode.body) return [];
    if (!ts.isBlock(functionNode.body)) return [functionNode.body];
    const expressions = [];
    const visitReturn = (node) => {
      if (node !== functionNode.body && isFunctionNode(ts, node)) return;
      if (ts.isReturnStatement(node) && node.expression) {
        expressions.push(node.expression);
        return;
      }
      ts.forEachChild(node, visitReturn);
    };
    visitReturn(functionNode.body);
    return expressions;
  };

  const functionFromSymbol = (symbol) => {
    for (const declaration of symbol?.declarations ?? []) {
      if (isFunctionNode(ts, declaration)) return declaration;
      if (ts.isVariableDeclaration(declaration) && declaration.initializer) {
        const initializer = unwrapStaticExpression(ts, declaration.initializer);
        if (ts.isArrowFunction(initializer) || ts.isFunctionExpression(initializer)) return initializer;
      }
    }
    return undefined;
  };

  const bindingElementValues = (declaration, environment, symbolStack, depth) => {
    const pattern = declaration.parent;
    const owner = pattern.parent;
    let containerValues = [];
    if (ts.isVariableDeclaration(owner) && owner.initializer) {
      containerValues = evaluate(owner.initializer, environment, symbolStack, depth + 1);
    } else if (ts.isBindingElement(owner)) {
      containerValues = bindingElementValues(owner, environment, symbolStack, depth + 1);
    }
    let values = [];
    if (ts.isObjectBindingPattern(pattern)) {
      const propertyName = staticPropertyName(ts, declaration.propertyName ?? declaration.name);
      if (propertyName !== undefined) values = objectPropertyValues(containerValues, propertyName);
    } else if (ts.isArrayBindingPattern(pattern)) {
      const index = pattern.elements.indexOf(declaration);
      for (const value of containerValues) {
        if (value.kind === 'array' && value.elements[index]) values.push(value.elements[index]);
      }
    }
    if (values.length === 0 && declaration.initializer) {
      values = evaluate(declaration.initializer, environment, symbolStack, depth + 1);
    }
    return bounded(values);
  };

  evaluateSymbol = (symbol, environment, symbolStack, depth) => {
    if (!symbol) return [];
    if (environment.has(symbol)) return environment.get(symbol);
    if (environment.size === 0 && symbolCache.has(symbol)) return symbolCache.get(symbol);
    if (symbolStack.has(symbol)) return [];
    const nextStack = new Set(symbolStack);
    nextStack.add(symbol);
    const values = [];
    for (const declaration of symbol.declarations ?? []) {
      if (ts.isVariableDeclaration(declaration) && declaration.initializer) {
        append(values, evaluate(declaration.initializer, environment, nextStack, depth + 1));
      } else if (ts.isBindingElement(declaration)) {
        append(values, bindingElementValues(declaration, environment, nextStack, depth + 1));
      }
    }
    const result = bounded(values);
    if (environment.size === 0) symbolCache.set(symbol, result);
    return result;
  };

  evaluate = (input, environment = new Map(), symbolStack = new Set(), depth = 0) => {
    if (!input) return [];
    if (depth > 40) throw new Error('static translation evaluation exceeded maximum depth');
    const expression = unwrapStaticExpression(ts, input);
    if (ts.isStringLiteral(expression) || ts.isNoSubstitutionTemplateLiteral(expression)) {
      return [stringValue(expression.text, expression)];
    }
    if (ts.isIdentifier(expression)) {
      return evaluateSymbol(resolvedSymbol(expression), environment, symbolStack, depth);
    }
    if (ts.isArrayLiteralExpression(expression)) {
      const elements = [];
      for (const element of expression.elements) {
        if (ts.isSpreadElement(element)) {
          append(elements, arrayElements(evaluate(element.expression, environment, symbolStack, depth + 1)));
        } else {
          append(elements, evaluate(element, environment, symbolStack, depth + 1));
        }
      }
      return [arrayValue(elements, expression)];
    }
    if (ts.isObjectLiteralExpression(expression)) {
      const properties = new Map();
      const addProperty = (name, values) => {
        const prior = properties.get(name) ?? [];
        properties.set(name, bounded([...prior, ...values]));
      };
      for (const property of expression.properties) {
        if (ts.isPropertyAssignment(property)) {
          const name = staticPropertyName(ts, property.name);
          if (name !== undefined) addProperty(name, evaluate(property.initializer, environment, symbolStack, depth + 1));
        } else if (ts.isShorthandPropertyAssignment(property)) {
          const valueSymbol = checker.getShorthandAssignmentValueSymbol(property);
          const values = valueSymbol
            ? evaluateSymbol(valueSymbol, environment, symbolStack, depth + 1)
            : evaluate(property.name, environment, symbolStack, depth + 1);
          addProperty(property.name.text, values);
        } else if (ts.isSpreadAssignment(property)) {
          for (const spread of evaluate(property.expression, environment, symbolStack, depth + 1)) {
            if (spread.kind !== 'object') continue;
            for (const [name, values] of spread.properties) addProperty(name, values);
          }
        }
      }
      return [objectValue(properties, expression)];
    }
    if (ts.isPropertyAccessExpression(expression)) {
      return objectPropertyValues(
        evaluate(expression.expression, environment, symbolStack, depth + 1),
        expression.name.text,
      );
    }
    if (ts.isElementAccessExpression(expression)) {
      const ownerValues = evaluate(expression.expression, environment, symbolStack, depth + 1);
      const indexValues = evaluate(expression.argumentExpression, environment, symbolStack, depth + 1)
        .filter((value) => value.kind === 'string');
      const resolved = [];
      for (const owner of ownerValues) {
        if (owner.kind === 'object') {
          if (indexValues.length === 0) {
            for (const values of owner.properties.values()) append(resolved, values);
          } else {
            for (const index of indexValues) append(resolved, owner.properties.get(index.value) ?? []);
          }
        } else if (owner.kind === 'array') {
          append(resolved, owner.elements);
        }
      }
      return resolved;
    }
    if (ts.isConditionalExpression(expression)) {
      return bounded([
        ...evaluate(expression.whenTrue, environment, symbolStack, depth + 1),
        ...evaluate(expression.whenFalse, environment, symbolStack, depth + 1),
      ]);
    }
    if (
      ts.isBinaryExpression(expression) &&
      (expression.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken ||
        expression.operatorToken.kind === ts.SyntaxKind.BarBarToken)
    ) {
      return bounded([
        ...evaluate(expression.left, environment, symbolStack, depth + 1),
        ...evaluate(expression.right, environment, symbolStack, depth + 1),
      ]);
    }
    if (ts.isCallExpression(expression)) {
      const callee = unwrapStaticExpression(ts, expression.expression);
      if (ts.isPropertyAccessExpression(callee)) {
        const method = callee.name.text;
        const receiver = evaluate(callee.expression, environment, symbolStack, depth + 1);
        if (method === 'find' || method === 'at') return arrayElements(receiver);
        if (method === 'filter' || method === 'slice') return receiver;
        if (method === 'map' || method === 'flatMap') {
          const callback = unwrapStaticExpression(ts, expression.arguments[0]);
          if (!callback || (!ts.isArrowFunction(callback) && !ts.isFunctionExpression(callback))) return [];
          const callbackEnvironment = new Map(environment);
          bindPattern(callback.parameters[0]?.name, arrayElements(receiver), callbackEnvironment);
          const mapped = [];
          for (const returned of returnExpressions(callback)) {
            append(mapped, evaluate(returned, callbackEnvironment, symbolStack, depth + 1));
          }
          const elements = method === 'flatMap' ? arrayElements(mapped) : mapped;
          return [arrayValue(elements, expression)];
        }
        if (
          ts.isIdentifier(callee.expression) && callee.expression.text === 'Object' &&
          (method === 'keys' || method === 'values')
        ) {
          const objects = evaluate(expression.arguments[0], environment, symbolStack, depth + 1);
          const values = [];
          for (const object of objects) {
            if (object.kind !== 'object') continue;
            if (method === 'keys') {
              for (const name of object.properties.keys()) append(values, [stringValue(name, object.node)]);
            } else {
              for (const propertyValues of object.properties.values()) append(values, propertyValues);
            }
          }
          return [arrayValue(values, expression)];
        }
      }
      const symbol = resolvedSymbol(callee);
      const functionNode = functionFromSymbol(symbol);
      if (!functionNode || symbolStack.has(symbol)) return [];
      const callEnvironment = new Map(environment);
      functionNode.parameters.forEach((parameter, index) => {
        let values = expression.arguments[index]
          ? evaluate(expression.arguments[index], environment, symbolStack, depth + 1)
          : [];
        if (values.length === 0 && parameter.initializer) {
          values = evaluate(parameter.initializer, callEnvironment, symbolStack, depth + 1);
        }
        bindPattern(parameter.name, values, callEnvironment);
      });
      const nextStack = new Set(symbolStack);
      if (symbol) nextStack.add(symbol);
      const returned = [];
      for (const result of returnExpressions(functionNode)) {
        append(returned, evaluate(result, callEnvironment, nextStack, depth + 1));
      }
      return returned;
    }
    return [];
  };

  const isTranslatorCall = (node) => {
    if (!ts.isCallExpression(node)) return false;
    const callee = unwrapStaticExpression(ts, node.expression);
    return (ts.isIdentifier(callee) && (callee.text === 't' || callee.text === 'translate')) ||
      (ts.isPropertyAccessExpression(callee) && callee.name.text === 't');
  };

  const addUsage = (values, directLiteral = false) => {
    for (const value of values) {
      if (value.kind !== 'string' || value.value === '') continue;
      const sourceFile = value.node.getSourceFile();
      const position = sourceFile.getLineAndCharacterOfPosition(value.node.getStart(sourceFile));
      const location = `${path.relative(sourceRoot, sourceFile.fileName)}:${position.line + 1}`;
      const prior = locations.get(value.value) ?? [];
      if (!prior.includes(location)) prior.push(location);
      locations.set(value.value, prior);
      if (directLiteral) directLiteralKeys.add(value.value);
    }
  };

  const componentFunction = (tagName) => functionFromSymbol(resolvedSymbol(tagName));
  const jsxProps = (attributes, environment) => {
    const properties = new Map();
    const addProperty = (name, values) => {
      const prior = properties.get(name) ?? [];
      properties.set(name, bounded([...prior, ...values]));
    };
    for (const attribute of attributes.properties) {
      if (ts.isJsxAttribute(attribute)) {
        if (!attribute.initializer) continue;
        if (ts.isStringLiteral(attribute.initializer)) {
          addProperty(attribute.name.text, [stringValue(attribute.initializer.text, attribute.initializer)]);
        } else if (ts.isJsxExpression(attribute.initializer) && attribute.initializer.expression) {
          addProperty(attribute.name.text, evaluate(attribute.initializer.expression, environment));
        }
      } else if (ts.isJsxSpreadAttribute(attribute)) {
        for (const spread of evaluate(attribute.expression, environment)) {
          if (spread.kind !== 'object') continue;
          for (const [name, values] of spread.properties) addProperty(name, values);
        }
      }
    }
    return objectValue(properties, attributes);
  };

  const callbackMethods = new Set(['map', 'flatMap', 'forEach']);
  const visit = (node, environment = new Map(), componentStack = new Set()) => {
    if (isTranslatorCall(node)) {
      const argument = node.arguments[0];
      if (argument) {
        const unwrapped = unwrapStaticExpression(ts, argument);
        const directLiteral = ts.isStringLiteral(unwrapped) || ts.isNoSubstitutionTemplateLiteral(unwrapped);
        addUsage(evaluate(argument, environment), directLiteral);
      }
    }

    if (ts.isCallExpression(node)) {
      const callee = unwrapStaticExpression(ts, node.expression);
      if (ts.isPropertyAccessExpression(callee) && callbackMethods.has(callee.name.text)) {
        const callback = unwrapStaticExpression(ts, node.arguments[0]);
        if (callback && (ts.isArrowFunction(callback) || ts.isFunctionExpression(callback))) {
          visit(callee.expression, environment, componentStack);
          const callbackEnvironment = new Map(environment);
          bindPattern(
            callback.parameters[0]?.name,
            arrayElements(evaluate(callee.expression, environment)),
            callbackEnvironment,
          );
          visit(callback.body, callbackEnvironment, componentStack);
          node.arguments.slice(1).forEach((argument) => visit(argument, environment, componentStack));
          return;
        }
      }
    }

    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const component = componentFunction(node.tagName);
      if (component?.body && component.parameters[0] && !componentStack.has(component)) {
        const componentEnvironment = new Map();
        bindPattern(component.parameters[0].name, [jsxProps(node.attributes, environment)], componentEnvironment);
        const nextStack = new Set(componentStack);
        nextStack.add(component);
        visit(component.body, componentEnvironment, nextStack);
      }
    }

    ts.forEachChild(node, (child) => visit(child, environment, componentStack));
  };

  const sourceFiles = files
    .map((file) => program.getSourceFile(path.resolve(file)) ?? program.getSourceFile(file))
    .filter(Boolean);
  for (const sourceFile of sourceFiles) {
    visit(sourceFile);
  }
  return { locations, directLiteralKeys };
}

async function loadTypeScript() {
  const typescriptModule = await import(pathToFileURL(
    path.resolve(scriptDir, '../web/node_modules/typescript/lib/typescript.js'),
  ).href);
  return typescriptModule.default ?? typescriptModule;
}

async function selfTest() {
  const fixture = new Map([
    [
      'en.json',
      { alpha: 'Alpha {{count}}', beta: 'Beta', copied: 'Copied', missing: 'Missing', 'Top P': 'Top P' },
    ],
    ['fr.json', { alpha: 'Alpha', beta: '', copied: 'Copied', extra: 'Supplément', 'Top P': 'Top P' }],
  ]);
  const result = analyzeLocale(fixture.get('en.json'), fixture.get('fr.json'), new Set(['Top P']));
  assert.deepEqual(result, {
    missing: ['missing'],
    extras: ['extra'],
    invalidValues: ['beta'],
    interpolationMismatches: ['alpha'],
    untranslated: ['copied'],
  });
  assert.deepEqual(buildReport(fixture), {
    base: 'en.json',
    keyCount: 5,
    locales: {
      en: {
        file: 'en.json',
        keyCount: 5,
        duplicateKeyCount: 0,
        missingCount: 0,
        extrasCount: 0,
        invalidValueCount: 0,
        untranslatedCount: 0,
        interpolationMismatchCount: 0,
      },
      fr: {
        file: 'fr.json',
        keyCount: 5,
        duplicateKeyCount: 0,
        missingCount: 1,
        extrasCount: 1,
        invalidValueCount: 1,
        untranslatedCount: 1,
        interpolationMismatchCount: 1,
      },
    },
  });
  assert.deepEqual(interpolationTokens('Page {{page}} of {{pages}} / {{page}}'), ['page', 'page', 'pages']);
  assert.equal(hasValidInterpolationSyntax('Hello {{name}}'), true);
  assert.equal(hasValidInterpolationSyntax('Hello {{name}'), false);
  assert.deepEqual(findDuplicateKeys('{"alpha":"one","alpha":"two"}'), ['alpha']);

  const ts = await loadTypeScript();
  const sourceRoot = path.resolve('/translation-check-self-test');
  const catalogFile = path.join(sourceRoot, 'catalog.ts');
  const viewFile = path.join(sourceRoot, 'view.tsx');
  const sources = new Map([
    [catalogFile, `
      const entry = (label: string) => ({ label, configValue: 'Not a key' });
      export const IMPORTED_ITEMS = [entry('Imported factory label'), { label: 'Imported literal label' }];
    `],
    [viewFile, `
      import { IMPORTED_ITEMS } from './catalog';
      declare const t: (key: string) => string;
      declare const dynamicContent: string;
      const METRIC_LABELS = { quota: 'Static quota', requests: 'Static requests' } as const;
      const SECTION_LABELS = { overview: 'Static overview', flow: 'Static flow' } as const;
      const LOCAL_ITEMS = [
        { label: 'Local catalog label', configValue: 'Still not a key' },
        { label: '', configValue: 'Empty labels are not keys' },
      ];
      function metricLabel(metric: string) { return METRIC_LABELS[metric as keyof typeof METRIC_LABELS]; }
      function Panel({ title }: { title: string }) { return <h2>{t(title)}</h2>; }
      function ItemPanel({ item }: { item: { label: string } }) { return <span>{t(item.label)}</span>; }
      export function Fixture() {
        return <>
          {t('Direct literal')}
          {t(METRIC_LABELS[dynamicContent as keyof typeof METRIC_LABELS])}
          {t(SECTION_LABELS[dynamicContent as keyof typeof SECTION_LABELS])}
          {t(metricLabel(dynamicContent))}
          {LOCAL_ITEMS.map((item) => t(item.label))}
          {IMPORTED_ITEMS.map((item) => <ItemPanel item={item} />)}
          {t(dynamicContent ? 'Conditional yes' : 'Conditional no')}
          {t(dynamicContent)}
          <Panel title="Literal component prop" />
        </>;
      }
    `],
  ]);
  const usage = literalTranslationUsage(ts, [...sources.keys()], sourceRoot, sources);
  assert.deepEqual(sorted(usage.locations.keys()), [
    'Conditional no',
    'Conditional yes',
    'Direct literal',
    'Imported factory label',
    'Imported literal label',
    'Literal component prop',
    'Local catalog label',
    'Static flow',
    'Static overview',
    'Static quota',
    'Static requests',
  ]);
  assert.deepEqual(sorted(usage.directLiteralKeys), ['Direct literal']);
  assert.equal(usage.locations.has('Not a key'), false);
  assert.equal(usage.locations.has('Still not a key'), false);
  assert.equal(usage.locations.has('Empty labels are not keys'), false);
  assert.equal(usage.locations.has(''), false);
  console.log('Translation report verifier self-test passed.');
}

if (process.argv.includes('--self-test')) {
  await selfTest();
  process.exit(0);
}

const writeReport = process.argv.includes('--write-report');
const unknownOptions = process.argv.slice(2).filter((option) => option !== '--write-report');
if (unknownOptions.length > 0) {
  console.error(`unknown option(s): ${unknownOptions.join(', ')}`);
  process.exit(2);
}

const localesDir = path.resolve(scriptDir, '../web/src/i18n/locales');
const sourceRoot = path.resolve(scriptDir, '../web/src');
const files = sorted(fs.readdirSync(localesDir).filter((file) => file.endsWith('.json')));
if (files.length === 0) {
  console.error('no locale files found in', localesDir);
  process.exit(1);
}

const localeFiles = new Map();
const duplicateKeysByFile = new Map();
let ok = true;
for (const file of files) {
  const source = fs.readFileSync(path.join(localesDir, file), 'utf8');
  const duplicateKeys = findDuplicateKeys(source);
  duplicateKeysByFile.set(file, duplicateKeys);
  if (duplicateKeys.length) {
    console.error(`${file}: duplicate key(s): ${duplicateKeys.slice(0, 8).join(' | ')}`);
    ok = false;
  }
  const parsed = JSON.parse(source);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    console.error(`${file}: locale must be a JSON object`);
    ok = false;
    continue;
  }
  localeFiles.set(file, parsed);
}

let report;
try {
  report = buildReport(localeFiles, duplicateKeysByFile);
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
}

const base = localeFiles.get(BASE_FILE);
const baseKeys = new Set(Object.keys(base));

let sourceFiles;
let sourceUsage;
try {
  const ts = await loadTypeScript();
  sourceFiles = runtimeSourceFiles(sourceRoot);
  sourceUsage = literalTranslationUsage(ts, sourceFiles, sourceRoot);
} catch (error) {
  console.error('unable to inspect runtime translation calls:', error instanceof Error ? error.message : String(error));
  process.exit(1);
}

const staticallyResolvedKeys = sorted(
  [...sourceUsage.locations.keys()].filter((key) => !sourceUsage.directLiteralKeys.has(key)),
);
const missingSourceKeys = sorted([...sourceUsage.locations.keys()].filter((key) => !baseKeys.has(key)));
const missingStaticallyResolvedKeys = staticallyResolvedKeys.filter((key) => !baseKeys.has(key));
report.source = {
  fileCount: sourceFiles.length,
  literalKeyCount: sourceUsage.directLiteralKeys.size,
  staticallyResolvedKeyCount: staticallyResolvedKeys.length,
  staticallyResolvedMissingFromBaseCount: missingStaticallyResolvedKeys.length,
  runtimeKeyCount: sourceUsage.locations.size,
  missingFromBaseCount: missingSourceKeys.length,
  missingFromBase: missingSourceKeys,
};
if (missingSourceKeys.length > 0) {
  ok = false;
  console.error(
    `runtime source: ${sourceUsage.locations.size} translation key(s) ` +
      `(${sourceUsage.directLiteralKeys.size} direct literal, ${staticallyResolvedKeys.length} statically resolved), ` +
      `${missingSourceKeys.length} missing from ${BASE_FILE}`,
  );
  for (const key of missingSourceKeys.slice(0, 12)) {
    console.error(`  missing: ${key} (${sourceUsage.locations.get(key)[0]})`);
  }
  if (missingStaticallyResolvedKeys.length > 0) {
    console.error(
      `  statically resolved missing: ${missingStaticallyResolvedKeys.slice(0, 12).join(' | ')}`,
    );
  }
}

for (const [file, data] of localeFiles) {
  const allowedIdenticalKeys =
    file === BASE_FILE ? baseKeys : (IDENTICAL_VALUE_ALLOWLIST.get(file) ?? new Set());
  const result = analyzeLocale(base, data, allowedIdenticalKeys);
  if (
    result.missing.length ||
    result.extras.length ||
    result.invalidValues.length ||
    result.untranslated.length ||
    result.interpolationMismatches.length
  ) {
    ok = false;
    console.error(
      `${file}: ${result.missing.length} missing, ${result.extras.length} extra, ` +
        `${result.invalidValues.length} empty/non-string, ${result.untranslated.length} copied-English, ` +
        `${result.interpolationMismatches.length} interpolation mismatch(es)`,
    );
    if (result.missing.length) console.error('  missing:', result.missing.slice(0, 8).join(' | '));
    if (result.extras.length) console.error('  extra:', result.extras.slice(0, 8).join(' | '));
    if (result.invalidValues.length) console.error('  invalid:', result.invalidValues.slice(0, 8).join(' | '));
    if (result.untranslated.length) console.error('  copied English:', result.untranslated.slice(0, 8).join(' | '));
    if (result.interpolationMismatches.length) {
      console.error('  interpolation:', result.interpolationMismatches.slice(0, 8).join(' | '));
    }
  }
}

const reportPath = path.join(localesDir, REPORT_RELATIVE_PATH);
const expectedReport = canonicalJSON(report);
if (writeReport) {
  fs.mkdirSync(path.dirname(reportPath), { recursive: true });
  fs.writeFileSync(reportPath, expectedReport);
} else {
  const actualReport = fs.existsSync(reportPath) ? fs.readFileSync(reportPath, 'utf8') : '';
  if (actualReport !== expectedReport) {
    ok = false;
    console.error(`${REPORT_RELATIVE_PATH} is missing or stale; run this command with --write-report`);
  }
}

if (!ok) process.exit(1);
console.log(
  `Translation completeness OK: ${files.length} locales, ${baseKeys.size} keys each; ` +
    `${sourceUsage.locations.size} runtime keys covered ` +
    `(${sourceUsage.directLiteralKeys.size} direct literal, ${staticallyResolvedKeys.length} statically resolved); ` +
    'sync report current.',
);
